package uiproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
