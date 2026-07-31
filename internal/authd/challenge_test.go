package authd

import (
	"strings"
	"testing"
	"time"
)

func testCodec() *ChallengeCodec {
	return NewChallengeCodec([]byte("0123456789abcdef0123456789abcdef"))
}

func TestSealOpenRoundTrip(t *testing.T) {
	c := testCodec()
	sealed, err := c.Seal(PurposeChallenge, []byte(`{"challenge":"abc"}`), time.Minute)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := c.Open(PurposeChallenge, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(got) != `{"challenge":"abc"}` {
		t.Fatalf("payload mangled: %s", got)
	}
}

func TestOpenRejectsExpired(t *testing.T) {
	c := testCodec()
	sealed, _ := c.Seal(PurposeChallenge, []byte("x"), -time.Second)
	if _, err := c.Open(PurposeChallenge, sealed); err == nil {
		t.Fatal("expired challenge accepted")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	c := testCodec()
	sealed, _ := c.Seal(PurposeChallenge, []byte("x"), time.Minute)
	parts := strings.Split(sealed, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected format %q", sealed)
	}
	// Flip the payload, keep the signature.
	if _, err := c.Open(PurposeChallenge, "Zm9ybmV5."+parts[1]); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestOpenRejectsOtherKey(t *testing.T) {
	sealed, _ := testCodec().Seal(PurposeChallenge, []byte("x"), time.Minute)
	other := NewChallengeCodec([]byte("ffffffffffffffffffffffffffffffff"))
	if _, err := other.Open(PurposeChallenge, sealed); err == nil {
		t.Fatal("signature from another key accepted")
	}
}

// TestOpenRejectsWrongPurpose is the domain-separation fix from review: the
// session cookie and the WebAuthn challenge cookie share one HMAC key
// (cmd/authd/main.go builds a single ChallengeCodec and hands it to both
// NewAuthenticator and ServerConfig.Codec), so without a purpose baked into
// the signature a token sealed for one role would verify just as well for
// the other — a 12-hour session cookie presented as frame_challenge would
// open, and a sealed challenge presented as frame_session would open and
// reach Store.ByEmail. Sealing under one purpose and opening under the other
// must fail exactly like a tampered payload or a mismatched key.
func TestOpenRejectsWrongPurpose(t *testing.T) {
	c := testCodec()
	sealed, err := c.Seal(PurposeSession, []byte("alice@example.com"), time.Hour)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := c.Open(PurposeChallenge, sealed); err == nil {
		t.Fatal("a token sealed for PurposeSession opened under PurposeChallenge")
	}
	// And the round trip under its own purpose still works, so this isn't
	// just Open rejecting everything.
	if _, err := c.Open(PurposeSession, sealed); err != nil {
		t.Fatalf("token sealed and opened under the same purpose was rejected: %v", err)
	}
}
