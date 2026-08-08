package authd

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

// keyID is stable because the apiserver caches the JWKS: a changing kid would
// make it refetch on every token.
const keyID = "frame-authd"

// Issuer mints the ID tokens the Kubernetes apiserver validates, and publishes
// the two documents it needs to do so.
type Issuer struct {
	url      string
	clientID string
	signer   jose.Signer
	public   jose.JSONWebKey
}

func NewIssuer(url, clientID string, key *ecdsa.PrivateKey) (*Issuer, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: keyID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, fmt.Errorf("building signer: %w", err)
	}
	return &Issuer{
		url:      url,
		clientID: clientID,
		signer:   signer,
		public:   jose.JSONWebKey{Key: key.Public(), KeyID: keyID, Algorithm: string(jose.ES256), Use: "sig"},
	}, nil
}

// GroupForRole maps a FrameUser role to the group claim. The apiserver adds
// its own `frame:` prefix, so these stay unprefixed here.
func GroupForRole(role string) string {
	switch role {
	case framev1alpha1.RoleAdmin:
		return "admins"
	case framev1alpha1.RoleOperator:
		return "operators"
	case framev1alpha1.RoleViewer:
		return "viewers"
	default:
		return ""
	}
}

// Mint issues a short-lived ID token for one person.
func (i *Issuer) Mint(email, role string, ttl time.Duration) (string, error) {
	group := GroupForRole(role)
	if group == "" {
		// Refusing beats defaulting: a typo in a role must not silently
		// produce a token, and least of all a viewer-shaped one that looks
		// like it worked.
		return "", fmt.Errorf("unknown role %q", role)
	}
	now := time.Now()
	claims := struct {
		jwt.Claims
		Email  string   `json:"email"`
		Groups []string `json:"groups"`
	}{
		Claims: jwt.Claims{
			Issuer:    i.url,
			Subject:   email,
			Audience:  jwt.Audience{i.clientID},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(now.Add(ttl)),
		},
		Email:  email,
		Groups: []string{group},
	}
	return jwt.Signed(i.signer).Claims(claims).Serialize()
}

func (i *Issuer) JWKS() ([]byte, error) {
	return json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{i.public}})
}

func (i *Issuer) Discovery() ([]byte, error) {
	return json.Marshal(map[string]any{
		"issuer":                                i.url,
		"jwks_uri":                              i.url + "/keys",
		"id_token_signing_alg_values_supported": []string{"ES256"},
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"claims_supported":                      []string{"iss", "sub", "aud", "exp", "iat", "email", "groups"},
	})
}
