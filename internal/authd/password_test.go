package authd

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword(h, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(h, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("identical hashes for the same password: salt is not random")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, encoded := range []string{"", "not-a-phc-string", "$argon2id$v=19$m=1", "$bcrypt$x$y$z$w"} {
		if VerifyPassword(encoded, "anything") {
			t.Fatalf("malformed hash %q was accepted", encoded)
		}
	}
}

func TestVerifyEmptyPasswordAgainstEmptyHash(t *testing.T) {
	// An account with no hash stored must never authenticate, whatever is sent.
	if VerifyPassword("", "") {
		t.Fatal("empty hash accepted an empty password")
	}
}

func TestVerifyHonoursEncodedCostParameters(t *testing.T) {
	// Regression test: VerifyPassword must respect the cost parameters (m, t, p)
	// encoded in the PHC string, not blindly use package constants.
	// If this test passes but uses the package constants instead of parsing them,
	// a regression (e.g. always using argonTime instead of parsed time) would
	// go unnoticed because HashPassword always encodes with those same constants.
	plaintext := "test password"
	h, err := HashPassword(plaintext)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	// Mutate only the t= (time) parameter, leaving salt and hash untouched.
	// VerifyPassword must parse this and reject it because the recomputed hash won't match.
	mutated := strings.Replace(h, "t=3", "t=4", 1)
	if mutated == h {
		t.Fatal("failed to mutate t= parameter in PHC string")
	}
	if VerifyPassword(mutated, plaintext) {
		t.Fatal("VerifyPassword accepted hash with modified time parameter")
	}

	// Also test m= (memory) parameter.
	mutated = strings.Replace(h, "m=65536", "m=65535", 1)
	if mutated == h {
		t.Fatal("failed to mutate m= parameter in PHC string")
	}
	if VerifyPassword(mutated, plaintext) {
		t.Fatal("VerifyPassword accepted hash with modified memory parameter")
	}
}
