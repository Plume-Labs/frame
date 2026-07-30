package authd

import "testing"

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
