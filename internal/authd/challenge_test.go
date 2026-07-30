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
	sealed, err := c.Seal([]byte(`{"challenge":"abc"}`), time.Minute)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := c.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(got) != `{"challenge":"abc"}` {
		t.Fatalf("payload mangled: %s", got)
	}
}

func TestOpenRejectsExpired(t *testing.T) {
	c := testCodec()
	sealed, _ := c.Seal([]byte("x"), -time.Second)
	if _, err := c.Open(sealed); err == nil {
		t.Fatal("expired challenge accepted")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	c := testCodec()
	sealed, _ := c.Seal([]byte("x"), time.Minute)
	parts := strings.Split(sealed, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected format %q", sealed)
	}
	// Flip the payload, keep the signature.
	if _, err := c.Open("Zm9ybmV5." + parts[1]); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestOpenRejectsOtherKey(t *testing.T) {
	sealed, _ := testCodec().Seal([]byte("x"), time.Minute)
	other := NewChallengeCodec([]byte("ffffffffffffffffffffffffffffffff"))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("signature from another key accepted")
	}
}
