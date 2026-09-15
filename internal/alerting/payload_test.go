package alerting

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func validPayload() Payload {
	return Payload{Version: "4", Alerts: []Alert{{Status: "firing", Fingerprint: "a1b2c3"}}}
}

func TestValidateAcceptsAMinimalPayload(t *testing.T) {
	if err := Validate(validPayload()); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Payload){
		"version 3": func(p *Payload) { p.Version = "3" },
		"no alerts": func(p *Payload) { p.Alerts = nil },
		"101 alerts": func(p *Payload) {
			p.Alerts = make([]Alert, 101)
			for i := range p.Alerts {
				p.Alerts[i] = Alert{Status: "firing", Fingerprint: "ab"}
			}
		},
		"empty fingerprint":     func(p *Payload) { p.Alerts[0].Fingerprint = "" },
		"uppercase fingerprint": func(p *Payload) { p.Alerts[0].Fingerprint = "AB" },
		"65-char fingerprint":   func(p *Payload) { p.Alerts[0].Fingerprint = strings.Repeat("a", 65) },
		"unknown status":        func(p *Payload) { p.Alerts[0].Status = "pending" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validPayload()
			mutate(&p)
			if Validate(p) == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestBoundMapKeepsAtMost64EntriesDeterministically(t *testing.T) {
	m := map[string]string{}
	for i := 0; i < 100; i++ {
		m[strings.Repeat("k", 3)+string(rune('A'+i%26))+strings.Repeat("x", i)] = "v"
	}
	a, b := BoundMap(m), BoundMap(m)
	if len(a) != 64 {
		t.Fatalf("want 64 entries, got %d", len(a))
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			t.Fatalf("two calls kept different keys: %q", k)
		}
	}
}

func TestBoundMapTruncatesOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", 3000) // 6000 bytes
	got := BoundMap(map[string]string{strings.Repeat("ü", 200): long})
	for k, v := range got {
		if len(k) > 256 || len(v) > 4096 {
			t.Fatalf("not bounded: key %d bytes, value %d bytes", len(k), len(v))
		}
		if !utf8.ValidString(k) || !utf8.ValidString(v) {
			t.Fatal("truncation split a rune")
		}
	}
}

func TestBoundMapOfNilIsNil(t *testing.T) {
	if BoundMap(nil) != nil {
		t.Fatal("nil map became non-nil")
	}
}

func TestBoundMapDoesNotCollapseDistinctKeyPrefixes(t *testing.T) {
	// Two keys with identical 300-byte prefix but different suffixes
	// should not collapse after truncation; the first in sort order wins.
	prefix := strings.Repeat("a", 300)
	key1 := prefix + "b" // sorts first after prefix
	key2 := prefix + "c" // sorts second after prefix
	m := map[string]string{
		key1: "value1",
		key2: "value2",
	}
	got := BoundMap(m)
	if len(got) != 1 {
		t.Fatalf("want 1 entry after truncation collision, got %d", len(got))
	}
	// The truncated key should hold the value of the key that sorts first.
	for k, v := range got {
		if v != "value1" {
			t.Fatalf("want value1 (from first sorted key), got %q", v)
		}
		if len(k) > 256 {
			t.Fatalf("truncated key exceeds 256 bytes: %d", len(k))
		}
	}
}

func TestTruncateHandlesInvalidUTF8AtStart(t *testing.T) {
	// A string starting with invalid byte 0xff followed by ASCII 'a's,
	// truncated to 256 bytes, should remain valid and non-empty.
	invalid := "\xff" + strings.Repeat("a", 500)
	got := BoundMap(map[string]string{invalid: invalid})
	for k, v := range got {
		if !utf8.ValidString(k) || !utf8.ValidString(v) {
			t.Fatal("result contains invalid UTF-8")
		}
		if len(k) == 0 || len(v) == 0 {
			t.Fatal("result is empty")
		}
		if len(k) > 256 || len(v) > 4096 {
			t.Fatalf("result exceeds bounds: key %d, value %d", len(k), len(v))
		}
	}
}
