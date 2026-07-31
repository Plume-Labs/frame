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

// Purpose domain-separates one class of sealed token from another. The same
// HMAC key seals both the WebAuthn ceremony challenge and the session
// cookie; without a purpose baked into the signature, a value sealed for one
// role verifies just as well for the other (see Seal/Open), because nothing
// about the signed bytes said which role the token was for. A 12-hour
// session cookie presented where a challenge is expected — or a
// short-lived challenge presented as a session — must fail closed instead of
// opening on the strength of a shared key.
type Purpose string

const (
	// PurposeSession seals/opens the "frame_session" cookie value.
	PurposeSession Purpose = "session"
	// PurposeChallenge seals/opens the "frame_challenge" cookie value used by
	// the WebAuthn registration and login ceremonies.
	PurposeChallenge Purpose = "challenge"
)

// Seal returns "<base64url payload>.<base64url signature>". The expiry is part
// of the signed payload, so it cannot be extended by the holder. purpose is
// mixed into the signature (not stored in the payload itself) so a token
// sealed under one purpose never verifies when opened under another.
func (c *ChallengeCodec) Seal(purpose Purpose, data []byte, ttl time.Duration) (string, error) {
	payload := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().Add(ttl).Unix()))
	copy(payload[8:], data)

	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(c.sign(purpose, encoded)), nil
}

// Open verifies the signature under purpose, then the expiry, and returns the
// payload. A token sealed under a different purpose fails signature
// verification here, exactly like a tampered payload or the wrong key.
func (c *ChallengeCodec) Open(purpose Purpose, token string) ([]byte, error) {
	encoded, sig, found := strings.Cut(token, ".")
	if !found {
		return nil, errBadChallenge
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotSig, c.sign(purpose, encoded)) {
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

// sign includes purpose ahead of a NUL separator so that, e.g., purpose="ab"
// with encoded="c..." cannot be confused with purpose="a" and
// encoded="bc..." — NUL never appears in a base64url-encoded purpose string
// or in the base64url-encoded payload, so the split point is unambiguous.
func (c *ChallengeCodec) sign(purpose Purpose, encoded string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(encoded))
	return m.Sum(nil)
}
