package uiproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// signerFixture serves a JWKS and mints tokens against it, the way authd does.
type signerFixture struct {
	key    *ecdsa.PrivateKey
	server *httptest.Server
}

func newSignerFixture(t *testing.T) *signerFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &signerFixture{key: key}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "k1", Algorithm: string(jose.ES256), Use: "sig",
		}}}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *signerFixture) mint(t *testing.T, sub, iss, aud string, groups []string, exp time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: f.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), "k1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	claims := struct {
		jwt.Claims
		Groups []string `json:"groups"`
	}{
		Claims: jwt.Claims{
			Issuer:   iss,
			Subject:  sub,
			Audience: jwt.Audience{aud},
			Expiry:   jwt.NewNumericDate(exp),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Groups: groups,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifyAcceptsAGoodToken(t *testing.T) {
	f := newSignerFixture(t)
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", f.server.Client())

	tok := f.mint(t, "alice@example.com", "https://authd", "frame-ui", []string{"operators"}, time.Now().Add(10*time.Minute))
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.User != "alice@example.com" {
		t.Fatalf("User = %q", id.User)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "operators" {
		t.Fatalf("Groups = %v", id.Groups)
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newSignerFixture(t)
	other := newSignerFixture(t)
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", f.server.Client())

	cases := map[string]string{
		"expired":      f.mint(t, "a@b.c", "https://authd", "frame-ui", []string{"viewers"}, time.Now().Add(-time.Minute)),
		"wrong issuer": f.mint(t, "a@b.c", "https://evil", "frame-ui", []string{"viewers"}, time.Now().Add(time.Hour)),
		"wrong aud":    f.mint(t, "a@b.c", "https://authd", "someone-else", []string{"viewers"}, time.Now().Add(time.Hour)),
		"foreign key":  other.mint(t, "a@b.c", "https://authd", "frame-ui", []string{"admins"}, time.Now().Add(time.Hour)),
		"not a jwt":    "hello",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tok); err == nil {
				t.Fatal("accepted a token it must reject")
			}
		})
	}
}

func TestVerifyCachesTheKeySet(t *testing.T) {
	f := newSignerFixture(t)
	var fetches int
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		f.server.Config.Handler.ServeHTTP(w, r)
	}))
	defer counting.Close()

	v := NewJWKSVerifier(counting.URL, "https://authd", "frame-ui", counting.Client())
	tok := f.mint(t, "a@b.c", "https://authd", "frame-ui", []string{"viewers"}, time.Now().Add(time.Hour))
	for range 3 {
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 1 {
		t.Fatalf("fetched the JWKS %d times, want 1", fetches)
	}
}

// jwkFor builds the public JWK authd would publish for one signing key.
func jwkFor(key *ecdsa.PrivateKey, kid string) jose.JSONWebKey {
	return jose.JSONWebKey{Key: key.Public(), KeyID: kid, Algorithm: string(jose.ES256), Use: "sig"}
}

// mintWithKey signs a token against an arbitrary key/kid pair, unlike
// signerFixture.mint which is pinned to a single fixture's key and "k1".
// It's needed to mint a token under a second, distinct kid to simulate authd
// rotating in a new key.
func mintWithKey(t *testing.T, key *ecdsa.PrivateKey, kid, sub, iss, aud string, groups []string, exp time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), kid),
	)
	if err != nil {
		t.Fatal(err)
	}
	claims := struct {
		jwt.Claims
		Groups []string `json:"groups"`
	}{
		Claims: jwt.Claims{
			Issuer:   iss,
			Subject:  sub,
			Audience: jwt.Audience{aud},
			Expiry:   jwt.NewNumericDate(exp),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Groups: groups,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestVerifyRefetchesOnceWhenAKeyRotates proves the refresh-on-unknown-kid
// path does what it exists for: after the cache is warmed against a token
// signed under "k1", a token signed under a brand new "k2" — one the cached
// set does not contain — must still verify, because Verify refetches once
// and checks the token against the *refreshed* set, not the stale one.
//
// This is the discriminating case TestVerifyRejects/foreign_key does not
// cover: that test proves a bad token stays rejected even through a refetch,
// but a refresh implementation that silently re-checked the stale set
// instead of the refreshed one would also pass it. Here, re-checking the
// stale set is exactly what must NOT happen — a bug that did so would leave
// this test's Verify call returning an error, and this test would fail.
func TestVerifyRefetchesOnceWhenAKeyRotates(t *testing.T) {
	key1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var rotated atomic.Bool
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		keys := []jose.JSONWebKey{jwkFor(key1, "k1")}
		if rotated.Load() {
			keys = append(keys, jwkFor(key2, "k2"))
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: keys})
	}))
	defer server.Close()

	v := NewJWKSVerifier(server.URL, "https://authd", "frame-ui", server.Client())

	// Warm the cache against the original key.
	tok1 := mintWithKey(t, key1, "k1", "a@b.c", "https://authd", "frame-ui", []string{"viewers"}, time.Now().Add(time.Hour))
	if _, err := v.Verify(context.Background(), tok1); err != nil {
		t.Fatalf("warm-up Verify: %v", err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("fetched the JWKS %d times after warm-up, want 1", got)
	}

	// authd rotates in a second key, then mints a token under it. The
	// cached set (still key1-only) does not have "k2".
	rotated.Store(true)
	tok2 := mintWithKey(t, key2, "k2", "rotated@example.com", "https://authd", "frame-ui", []string{"admins"}, time.Now().Add(time.Hour))

	id, err := v.Verify(context.Background(), tok2)
	if err != nil {
		t.Fatalf("Verify after rotation: %v", err)
	}
	if id.User != "rotated@example.com" {
		t.Fatalf("User = %q", id.User)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("fetched the JWKS %d times after rotation, want 2 (one extra for the unrecognized kid)", got)
	}
}

// --- the JWKS endpoint's own TLS ------------------------------------------
//
// C2 of the whole-branch review. authd serves /keys over TLS with a
// certificate issued by the in-cluster `frame-auth-ca` Issuer, and the
// uiproxy image is distroless/static — public roots only. With the default
// client every JWKS fetch fails `x509: certificate signed by unknown
// authority`, so Verify errors and the proxy 401s every single request.
//
// newTLSSignerFixture is newSignerFixture over TLS, with the server's own
// certificate written out as PEM so a test can decide whether to trust it.
type tlsSignerFixture struct {
	*signerFixture
	caFile string
}

func newTLSSignerFixture(t *testing.T) *tlsSignerFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &signerFixture{key: key}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "k1", Algorithm: string(jose.ES256), Use: "sig",
		}}}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(f.server.Close)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return &tlsSignerFixture{signerFixture: f, caFile: caFile}
}

func TestVerifyFailsAgainstAPrivateCAWithoutIt(t *testing.T) {
	f := newTLSSignerFixture(t)
	// nil client: exactly what cmd/uiproxy passed before this fix.
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", nil)
	tok := f.mint(t, "alice@example.com", "https://authd", "frame-ui", []string{"operators"}, time.Now().Add(10*time.Minute))
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("verified a token whose JWKS was served by an untrusted CA — the fixture is not testing what it claims")
	} else if !strings.Contains(err.Error(), "x509") {
		t.Fatalf("expected an x509 trust failure, got %v", err)
	}
}

func TestVerifyTrustsTheConfiguredCA(t *testing.T) {
	f := newTLSSignerFixture(t)
	hc, err := HTTPClientWithCA(f.caFile)
	if err != nil {
		t.Fatalf("HTTPClientWithCA: %v", err)
	}
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", hc)
	tok := f.mint(t, "alice@example.com", "https://authd", "frame-ui", []string{"operators"}, time.Now().Add(10*time.Minute))
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.User != "alice@example.com" {
		t.Fatalf("User = %q", id.User)
	}
}

func TestHTTPClientWithCARejectsAnUnusableFile(t *testing.T) {
	if _, err := HTTPClientWithCA(filepath.Join(t.TempDir(), "absent.crt")); err == nil {
		t.Fatal("accepted a CA file that does not exist")
	}
	notPEM := filepath.Join(t.TempDir(), "junk.crt")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := HTTPClientWithCA(notPEM); err == nil {
		t.Fatal("accepted a file holding no PEM certificate — the proxy would start with an empty trust pool and 401 everything")
	}
}

// The rate limiter must not swallow the reason the first fetch failed:
// "fetch attempted too recently" on its own sent the last review looking at
// the wrong thing entirely.
func TestRateLimitedFetchStillReportsTheOriginalFailure(t *testing.T) {
	f := newTLSSignerFixture(t)
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", nil)
	tok := f.mint(t, "alice@example.com", "https://authd", "frame-ui", []string{"operators"}, time.Now().Add(10*time.Minute))
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("expected the first fetch to fail")
	}
	_, err := v.Verify(context.Background(), tok)
	if err == nil {
		t.Fatal("expected the second fetch to fail too")
	}
	if !strings.Contains(err.Error(), "x509") {
		t.Fatalf("the rate-limited error hides why the fetch failed: %v", err)
	}
}

// I6 of the whole-branch review. `rbac.yaml` grants `impersonate` on `users`
// with no `resourceNames` — email addresses are not an enumerable set, so
// there is nothing to bind it to — and Verify passed `claims.Subject`
// straight into `Impersonate-User`. The only thing keeping a subject like
// `system:kube-controller-manager` out was `FrameUserSpec.Email`'s pattern,
// in another kind's CRD, enforced by a component that is not this one.
//
// Not exploitable today: the subject comes from a token this verifier just
// checked the signature of, and only authd signs those. This makes the
// invariant local rather than borrowed, so it survives authd changing.
func TestVerifyRejectsASubjectThatIsNotAnEmail(t *testing.T) {
	f := newSignerFixture(t)
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", f.server.Client())

	for _, sub := range []string{
		"system:kube-controller-manager",
		"system:masters",
		"admin",
		"system:serviceaccount:kube-system:default",
		// An `@` alone is not enough: `system:` first is the dangerous shape.
		"system:anything@example.com",
	} {
		tok := f.mint(t, sub, "https://authd", "frame-ui", []string{"admins"}, time.Now().Add(10*time.Minute))
		if _, err := v.Verify(context.Background(), tok); err == nil {
			t.Errorf("accepted subject %q — that string goes verbatim into Impersonate-User", sub)
		}
	}
}

func TestVerifyAcceptsAnOrdinaryEmail(t *testing.T) {
	f := newSignerFixture(t)
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", f.server.Client())
	tok := f.mint(t, "alice@example.com", "https://authd", "frame-ui", []string{"admins"}, time.Now().Add(10*time.Minute))
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("rejected an ordinary email: %v", err)
	}
}
