package uiproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// JWKSVerifier validates authd's ES256 tokens against the key set authd
// serves at /keys.
type JWKSVerifier struct {
	url, issuer, audience string
	hc                    *http.Client

	mu   sync.Mutex
	keys *jose.JSONWebKeySet

	// fetchAttempted and refreshAttempted each rate-limit their own kind of
	// fetch attempt to at most one per jwksMinRefresh, win or lose. They are
	// tracked separately: fetchAttempted guards populating an empty (or
	// unreachable-authd) cache, refreshAttempted guards refetching for an
	// unrecognized kid. Sharing one clock between them would let the
	// warm-up fetch consume the refresh budget before a rotated key ever
	// got a chance to be picked up.
	fetchAttempted   time.Time
	refreshAttempted time.Time

	// Why the last fetch failed, replayed by the rate limiter. Without it
	// the second and every subsequent request inside the window answers
	// "fetch attempted too recently" and says nothing about the actual
	// fault — which, when the fault was an untrusted JWKS certificate,
	// sent a whole review looking at the rate limiter instead of at the
	// missing CA (finding C2).
	lastErr error
}

// jwksMinRefresh bounds how often either kind of fetch attempt above may go
// out, so a stream of bogus tokens — or an unreachable authd — cannot turn
// into a stream of requests to authd.
const jwksMinRefresh = time.Minute

// HTTPClientWithCA builds the client the verifier should use when authd's
// JWKS endpoint is served with a certificate no public root chains to —
// which is every real deployment: authd's serving certificate comes from the
// in-cluster `frame-auth-ca` Issuer (deploy/kubernetes/authd/certificate.yaml)
// and the uiproxy image is distroless/static, so it carries public roots
// only.
//
// The pool holds the given CA and nothing else. Adding it to the system pool
// instead would mean any public CA could also vouch for authd, which is not
// what "trust our own CA" should mean, and the container has no system roots
// worth keeping anyway.
//
// It fails rather than falling back: a proxy that silently started with an
// empty trust pool would 401 every request in the cluster, and the reason
// would be a line in a log nobody reads until the console is already down.
func HTTPClientWithCA(caFile string) (*http.Client, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("jwks CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("jwks CA: %s holds no PEM certificate", caFile)
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}, nil
}

func NewJWKSVerifier(jwksURL, issuer, audience string, hc *http.Client) *JWKSVerifier {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &JWKSVerifier{url: jwksURL, issuer: issuer, audience: audience, hc: hc}
}

// attempt reports whether a fetch may proceed right now, given the time of
// its kind's last attempt. It never delays a first-ever attempt, and it
// records this attempt's time before the caller does any I/O, so a failure
// still counts toward the rate limit.
func attempt(last *time.Time) bool {
	if !last.IsZero() && time.Since(*last) < jwksMinRefresh {
		return false
	}
	*last = time.Now()
	return true
}

func (v *JWKSVerifier) keySet(ctx context.Context, refresh bool) (*jose.JSONWebKeySet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.keys != nil && !refresh {
		return v.keys, nil
	}

	last := &v.fetchAttempted
	if refresh {
		last = &v.refreshAttempted
	}
	if !attempt(last) {
		if v.keys != nil {
			// Still stale, but that's the rate limit doing its job.
			return v.keys, nil
		}
		if v.lastErr != nil {
			return nil, fmt.Errorf("jwks: %w (retrying in %s)", v.lastErr, jwksMinRefresh-time.Since(*last))
		}
		return nil, fmt.Errorf("jwks: fetch attempted too recently, retrying in %s", jwksMinRefresh-time.Since(*last))
	}

	set, err := v.fetch(ctx)
	// Remembered win or lose: a nil lastErr is what tells the branch above
	// that there is nothing better to say than "too recently".
	v.lastErr = err
	if err != nil {
		return nil, err
	}
	v.keys = set
	return v.keys, nil
}

func (v *JWKSVerifier) fetch(ctx context.Context) (*jose.JSONWebKeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, err
	}
	res, err := v.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", res.Status)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return nil, err
	}
	return &set, nil
}

func (v *JWKSVerifier) Verify(ctx context.Context, raw string) (Identity, error) {
	// ES256 only: accepting whatever the header names is how alg-confusion
	// attacks get in.
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return Identity{}, err
	}
	set, err := v.keySet(ctx, false)
	if err != nil {
		return Identity{}, err
	}
	claims := struct {
		jwt.Claims
		Groups []string `json:"groups"`
	}{}
	err = tok.Claims(set, &claims)
	if err != nil {
		// An unknown kid is what a key rotation looks like; refetch once.
		if set, rerr := v.keySet(ctx, true); rerr == nil {
			err = tok.Claims(set, &claims)
		}
	}
	if err != nil {
		return Identity{}, err
	}
	if err := claims.Validate(jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: jwt.Audience{v.audience},
		Time:        time.Now(),
	}); err != nil {
		return Identity{}, err
	}
	if err := checkSubject(claims.Subject); err != nil {
		return Identity{}, err
	}
	return Identity{User: claims.Subject, Groups: claims.Groups}, nil
}

// maxSubjectLen mirrors FrameUserSpec.Email's MaxLength.
const maxSubjectLen = 254

// checkSubject refuses a subject that is not an ordinary email address.
//
// The proxy's ServiceAccount holds `impersonate` on `users` with no
// `resourceNames` — email addresses are not enumerable, so there is nothing
// to bind it to — and the subject goes verbatim into `Impersonate-User`. The
// only thing keeping `system:kube-controller-manager` out of that header was
// the pattern on `FrameUserSpec.Email`, in another kind's CRD, enforced by a
// component that is not this one. This makes the invariant local, so it
// holds whatever authd does next (whole-branch review, I6).
//
// `system:` first, then `@`: an address like `system:x@example.com` has an
// `@` and is still the shape that matters.
func checkSubject(sub string) error {
	switch {
	case sub == "":
		return fmt.Errorf("token has no subject")
	case len(sub) > maxSubjectLen:
		return fmt.Errorf("token subject is longer than %d characters", maxSubjectLen)
	case strings.HasPrefix(sub, "system:"):
		return fmt.Errorf("refusing to impersonate a system: identity (%q)", sub)
	case !strings.Contains(sub, "@"):
		return fmt.Errorf("token subject %q is not an email address", sub)
	}
	return nil
}
