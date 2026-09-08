package uiproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
