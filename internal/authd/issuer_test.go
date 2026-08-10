package authd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func testIssuer(t *testing.T) *Issuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	iss, err := NewIssuer("https://auth.example.svc", "frame-ui", key)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	return iss
}

func TestMintCarriesEmailAndGroup(t *testing.T) {
	iss := testIssuer(t)
	raw, err := iss.Mint("alice@example.com", framev1beta1.RoleOperator, 15*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var claims struct {
		Issuer   string       `json:"iss"`
		Audience jwt.Audience `json:"aud"`
		Email    string       `json:"email"`
		Groups   []string     `json:"groups"`
	}
	if err := tok.UnsafeClaimsWithoutVerification(&claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims.Email != "alice@example.com" {
		t.Fatalf("email claim = %q", claims.Email)
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "operators" {
		t.Fatalf("groups claim = %v, want [operators]", claims.Groups)
	}
	if claims.Issuer != "https://auth.example.svc" {
		t.Fatalf("iss = %q", claims.Issuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != "frame-ui" {
		t.Fatalf("aud = %v", claims.Audience)
	}
}

func TestMintRejectsUnknownRole(t *testing.T) {
	if _, err := testIssuer(t).Mint("a@b.c", "wizard", time.Minute); err == nil {
		t.Fatal("unknown role was minted a token")
	}
}

func TestJWKSPublishesPublicKeyOnly(t *testing.T) {
	raw, err := testIssuer(t).JWKS()
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(set.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(set.Keys))
	}
	if !set.Keys[0].IsPublic() {
		t.Fatal("JWKS exposed a private key")
	}
}

func TestDiscoveryPointsAtOurEndpoints(t *testing.T) {
	raw, err := testIssuer(t).Discovery()
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	var doc struct {
		Issuer  string   `json:"issuer"`
		JWKSURI string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Issuer != "https://auth.example.svc" {
		t.Fatalf("issuer = %q", doc.Issuer)
	}
	if doc.JWKSURI != "https://auth.example.svc/keys" {
		t.Fatalf("jwks_uri = %q", doc.JWKSURI)
	}
	if len(doc.Algs) != 1 || doc.Algs[0] != "ES256" {
		t.Fatalf("algs = %v", doc.Algs)
	}
}
