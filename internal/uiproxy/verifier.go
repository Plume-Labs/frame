package uiproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

	mu      sync.Mutex
	keys    *jose.JSONWebKeySet
	fetched time.Time
}

// jwksMinRefresh bounds how often an unknown `kid` can force a fetch, so a
// stream of bogus tokens cannot turn into a stream of requests to authd.
const jwksMinRefresh = time.Minute

func NewJWKSVerifier(jwksURL, issuer, audience string, hc *http.Client) *JWKSVerifier {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &JWKSVerifier{url: jwksURL, issuer: issuer, audience: audience, hc: hc}
}

func (v *JWKSVerifier) keySet(ctx context.Context, refresh bool) (*jose.JSONWebKeySet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys != nil && (!refresh || time.Since(v.fetched) < jwksMinRefresh) {
		return v.keys, nil
	}
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
		return nil, fmt.Errorf("jwks: %s", res.Status)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return nil, err
	}
	v.keys, v.fetched = &set, time.Now()
	return v.keys, nil
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
	if claims.Subject == "" {
		return Identity{}, fmt.Errorf("token has no subject")
	}
	return Identity{User: claims.Subject, Groups: claims.Groups}, nil
}
