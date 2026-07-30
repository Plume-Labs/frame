package authd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// ChallengeCodec seals short-lived data into a self-contained, signed token.
//
// WebAuthn requires the challenge issued at the start of a ceremony to be
// checked at the end. Keeping it server-side would force the replicas to share
// a store; signing it and handing it to the browser removes that requirement
// without weakening anything, because the signature is what makes the value
// trustworthy, not where it was parked.
type ChallengeCodec struct {
	key []byte
}

func NewChallengeCodec(key []byte) *ChallengeCodec {
	return &ChallengeCodec{key: key}
}

var errBadChallenge = errors.New("challenge is missing, expired or has been tampered with")

// Seal returns "<base64url payload>.<base64url signature>". The expiry is part
// of the signed payload, so it cannot be extended by the holder.
func (c *ChallengeCodec) Seal(data []byte, ttl time.Duration) (string, error) {
	payload := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().Add(ttl).Unix()))
	copy(payload[8:], data)

	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(c.sign(encoded)), nil
}

// Open verifies the signature, then the expiry, and returns the payload.
func (c *ChallengeCodec) Open(token string) ([]byte, error) {
	encoded, sig, found := strings.Cut(token, ".")
	if !found {
		return nil, errBadChallenge
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotSig, c.sign(encoded)) {
		return nil, errBadChallenge
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) < 8 {
		return nil, errBadChallenge
	}
	if time.Now().After(time.Unix(int64(binary.BigEndian.Uint64(payload[:8])), 0)) {
		return nil, errBadChallenge
	}
	return payload[8:], nil
}

func (c *ChallengeCodec) sign(encoded string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(encoded))
	return m.Sum(nil)
}
