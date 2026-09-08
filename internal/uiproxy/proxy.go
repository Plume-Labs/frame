// Package uiproxy authenticates the Frame UI's requests to the Kubernetes
// apiserver.
//
// It exists because the UI used to reach the apiserver through a `kubectl
// proxy` sidecar holding the pod ServiceAccount, which was bound to both a
// viewer and an operator ClusterRole: every write the UI made was anonymous
// and carried the union of those permissions. This proxy takes the token
// authd issues, turns it into an impersonated identity, and lets the
// apiserver's own RBAC decide.
package uiproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Identity is who the caller is, as proven by their token. Groups are
// unprefixed; New's GroupPrefix is applied on the way out.
type Identity struct {
	User   string
	Groups []string
}

// Verifier validates a raw bearer token and returns who presented it.
type Verifier interface {
	Verify(ctx context.Context, raw string) (Identity, error)
}

// Recorder writes the trace of a mutating request. Start returns the name of
// the record, or "" when nothing was recorded — Finish ignores "".
type Recorder interface {
	Start(ctx context.Context, id Identity, r *http.Request) string
	Finish(ctx context.Context, name string, httpCode int)
}

type Options struct {
	Verifier    Verifier
	Recorder    Recorder
	Upstream    *url.URL
	Transport   http.RoundTripper
	GroupPrefix string
	Log         *slog.Logger
}

type Proxy struct {
	rp       *httputil.ReverseProxy
	verifier Verifier
	recorder Recorder
	prefix   string
	log      *slog.Logger
}

func New(o Options) (*Proxy, error) {
	if o.Verifier == nil {
		return nil, fmt.Errorf("uiproxy: Verifier is required")
	}
	if o.Upstream == nil {
		return nil, fmt.Errorf("uiproxy: Upstream is required")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	rp := httputil.NewSingleHostReverseProxy(o.Upstream)
	if o.Transport != nil {
		rp.Transport = o.Transport
	}
	// Watches are chunked streams the UI depends on for live updates.
	// Without immediate flushing the ReverseProxy buffers them and every
	// screen silently stops refreshing.
	rp.FlushInterval = -1
	return &Proxy{rp: rp, verifier: o.Verifier, recorder: o.Recorder, prefix: o.GroupPrefix, log: o.Log}, nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return h[7:]
	}
	return ""
}

// stripImpersonation removes every impersonation header the caller supplied.
// Prefix matching, not an allow-list of known names: Impersonate-Extra-* is
// open-ended, and a header we forgot to enumerate is a header the caller
// controls.
func stripImpersonation(h http.Header) {
	for k := range h {
		if strings.HasPrefix(textproto.CanonicalMIMEHeaderKey(k), "Impersonate-") {
			h.Del(k)
		}
	}
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// statusRecorder remembers the code so the task can be closed with it.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps watch streaming working: ReverseProxy type-asserts the writer
// to http.Flusher, and a wrapper that does not implement it turns every
// watch into a buffered response.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// unauthorized answers the way the apiserver answers, with a metav1.Status
// body.
//
// Not cosmetic. This proxy stands exactly where the apiserver stands, and
// every client of it is `parseApiserverResponse` in `src/lib/frame-sdk.ts`,
// which calls `res.json()` on every response. A `text/plain` body turned a
// 401 into `SyntaxError: Unexpected token 'u'` in the caller — so nothing
// could distinguish "your session lapsed" from a bug, and a tab left open
// past the 12h cookie filled with parse errors instead of returning to the
// login screen (whole-branch review, I1).
//
// `message` is the field the UI renders, so it says what to do rather than
// repeating the status line.
func unauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Code:     http.StatusUnauthorized,
		Reason:   metav1.StatusReasonUnauthorized,
		Message:  "not signed in: " + detail + ". Sign in again.",
	})
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		unauthorized(w, "the request carried no bearer token")
		return
	}
	id, err := p.verifier.Verify(r.Context(), tok)
	if err != nil {
		// The reason stays in the log, not in the body: telling the caller
		// which check their token failed is telling an attacker the same.
		p.log.Info("rejected token", "err", err)
		unauthorized(w, "the bearer token was not accepted")
		return
	}

	stripImpersonation(r.Header)
	// The user's token is not a credential the apiserver accepts, and
	// client-go's bearer transport only sets Authorization when it is empty.
	r.Header.Del("Authorization")

	r.Header.Set("Impersonate-User", id.User)
	for _, g := range id.Groups {
		r.Header.Add("Impersonate-Group", p.prefix+g)
	}

	sr := &statusRecorder{ResponseWriter: w}
	var task string
	if p.recorder != nil && isMutating(r.Method) {
		task = p.recorder.Start(r.Context(), id, r)
	}
	if task != "" {
		// Under defer, not called only after ServeHTTP returns normally: a
		// panic in the proxied call must still close the record, or it is
		// left Running forever.
		defer func() {
			code := sr.code
			if code == 0 {
				// sr.code is only ever 0 here if the proxied call exited
				// without writing a header at all — the reverse proxy's
				// own error handler always writes one, even for a broken
				// upstream, so this means it panicked. "0" is not a
				// status a reader can make sense of; record the
				// server-side failure it actually is.
				code = http.StatusInternalServerError
			}
			// Detached from the request context: it is cancelled the
			// moment the response finishes, which is exactly when this
			// runs.
			p.recorder.Finish(context.WithoutCancel(r.Context()), task, code)
		}()
	}
	p.rp.ServeHTTP(sr, r)
}
