/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package redfish

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// sessionsPath is the Redfish SessionService collection a session is
// created in (POST) and whose members are deleted to end one.
const sessionsPath = "/redfish/v1/SessionService/Sessions/"

// Why sessions at all. An iLO4 (observed on firmware 2.77) treats every
// Basic-auth request as its own implicit session: it writes a "Browser
// login" and a "Browser logout" entry to the iLO event log for each one,
// folding consecutive identical entries into one record with a repeat
// counter. One probe is a few dozen GETs, and the event log holds a few
// hundred records before it wraps — a controller probing once a minute
// rotated the whole log in about three hours, erasing what a crash had left
// in it before anyone could read it. A token session is logged once when
// it is created and once when it ends; the requests made with it in between
// are not logged.
type sessionState struct {
	mu sync.Mutex
	// token is the X-Auth-Token of the live session; empty when there is
	// none yet or the last one was rejected.
	token string
	// location is the path of the session resource, the target of the
	// DELETE that ends it.
	location string
	// unsupported is set once the BMC answered the session POST with 404 or
	// 405: it has no SessionService, and every request falls back to Basic
	// auth for the life of this client.
	unsupported bool
}

// WithSessionReuse makes the client log in once through the Redfish
// SessionService and carry the returned X-Auth-Token on every request,
// instead of Basic auth. The session is kept for the client's lifetime:
// recreated only when the BMC rejects the token with 401 (idle timeout,
// iLO reset, someone deleting it), ended by Logout. A BMC with no
// SessionService (404/405 on the POST) is served over Basic auth instead.
func WithSessionReuse() Option {
	return func(c *client) { c.session = &sessionState{} }
}

// SameTarget reports whether a and b are both clients built by New that talk
// to the same BMC, as the same user, with the same password and the same TLS
// trust. A caller that rebuilds a client on every pass uses it to decide
// whether the client it already holds — and that client's session — is
// still the right one. Any other Client (a test fake) is never the same
// target.
func SameTarget(a, b Client) bool {
	ca, ok := a.(*client)
	if !ok {
		return false
	}
	cb, ok := b.(*client)
	if !ok {
		return false
	}
	return ca.baseURL == cb.baseURL &&
		ca.username == cb.username &&
		ca.password == cb.password &&
		(ca.session == nil) == (cb.session == nil) &&
		sameTLS(ca.tlsConfig, cb.tlsConfig)
}

func sameTLS(a, b *tls.Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.InsecureSkipVerify != b.InsecureSkipVerify || a.MinVersion != b.MinVersion {
		return false
	}
	if a.RootCAs == nil || b.RootCAs == nil {
		return a.RootCAs == b.RootCAs
	}
	return a.RootCAs.Equal(b.RootCAs)
}

// send issues one request, authenticated by the session when the client
// reuses one. A 401 on a request that carried a token means the session is
// gone on the BMC's side: the token is dropped, a new session is created,
// and the request is sent once more. A second 401 is returned as is, and
// do maps it to ErrAuth.
func (c *client) send(ctx context.Context, method, path string, payload []byte) (*http.Response, error) {
	token, err := c.sessionToken(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.roundTrip(ctx, method, path, payload, token)
	if err != nil || token == "" || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	c.dropToken(token)
	token, err = c.sessionToken(ctx)
	if err != nil {
		return nil, err
	}
	return c.roundTrip(ctx, method, path, payload, token)
}

// roundTrip sends one request. token, when non-empty, authenticates it;
// Basic auth otherwise.
func (c *client) roundTrip(ctx context.Context, method, path string, payload []byte, token string) (*http.Response, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+normalizePath(path), reader)
	if err != nil {
		return nil, fmt.Errorf("redfish: build request for %s: %w", path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("X-Auth-Token", token)
	} else {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, transportError(path, err)
	}
	return resp, nil
}

// transportError maps a failure to reach the BMC at all onto ErrTLS when it
// is a certificate the client refused, a plain wrapped error otherwise.
func transportError(path string, err error) error {
	var verifyErr *tls.CertificateVerificationError
	var authorityErr x509.UnknownAuthorityError
	if errors.As(err, &verifyErr) || errors.As(err, &authorityErr) {
		return fmt.Errorf("%s: %w", path, ErrTLS)
	}
	return fmt.Errorf("redfish: request %s: %w", path, err)
}

// sessionToken returns the token to authenticate the next request with,
// logging in first when there is none. It returns "" when the request must
// go out with Basic auth: a client built without WithSessionReuse, or a BMC
// with no SessionService.
func (c *client) sessionToken(ctx context.Context) (string, error) {
	s := c.session
	if s == nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unsupported || s.token != "" {
		return s.token, nil
	}
	return c.loginLocked(ctx)
}

// dropToken forgets token, unless another request already replaced it.
func (c *client) dropToken(token string) {
	s := c.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token, s.location = "", ""
	}
}

// loginLocked creates a session. The caller holds s.mu.
func (c *client) loginLocked(ctx context.Context) (string, error) {
	s := c.session
	payload, err := json.Marshal(struct {
		UserName string `json:"UserName"`
		Password string `json:"Password"`
	}{UserName: c.username, Password: c.password})
	if err != nil {
		return "", fmt.Errorf("redfish: encode session request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+sessionsPath, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("redfish: build session request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", transportError(sessionsPath, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusMethodNotAllowed:
		s.unsupported = true
		return "", nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("%s: %w", sessionsPath, ErrAuth)
	case resp.StatusCode >= 300:
		return "", fmt.Errorf("redfish: %s returned %s", sessionsPath, resp.Status)
	}

	token := resp.Header.Get("X-Auth-Token")
	if token == "" {
		return "", fmt.Errorf("redfish: %s answered %s without an X-Auth-Token", sessionsPath, resp.Status)
	}
	location := sessionPath(resp.Header.Get("Location"))
	if location == "" {
		var doc struct {
			ODataID string `json:"@odata.id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&doc)
		location = sessionPath(doc.ODataID)
	}
	s.token, s.location = token, location
	return token, nil
}

// sessionPath keeps only the path of a session's Location. An iLO4 answers
// with an absolute URL; the DELETE is always sent to this client's own
// baseURL, so the token never leaves for a host the BMC named.
func sessionPath(location string) string {
	if location == "" {
		return ""
	}
	u, err := url.Parse(location)
	if err != nil {
		return ""
	}
	return u.Path
}

// Logout deletes the session this client holds, if any. The token is
// forgotten whatever the BMC answers: a session this client can no longer
// end expires on the BMC's idle timeout, and keeping a token that failed to
// log out would only make the next request fail with it first. A 404 means
// the session is already gone, which is what Logout wanted.
func (c *client) Logout(ctx context.Context) error {
	s := c.session
	if s == nil {
		return nil
	}
	s.mu.Lock()
	token, location := s.token, s.location
	s.token, s.location = "", ""
	s.mu.Unlock()
	if token == "" || location == "" {
		return nil
	}

	resp, err := c.roundTrip(ctx, http.MethodDelete, location, nil, token)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("redfish: logout %s returned %s", location, resp.Status)
	}
	return nil
}
