package authd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

func testAuthenticator(t *testing.T, users ...*framev1alpha1.FrameUser) *Authenticator {
	t.Helper()
	a, err := NewAuthenticator("example.com", "https://example.com", storeWith(t, users...), testCodec())
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	return a
}

func TestBeginLoginIsUsernameless(t *testing.T) {
	a := testAuthenticator(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin,
		framev1alpha1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk"}))

	opts, sealed, err := a.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if sealed == "" {
		t.Fatal("no sealed challenge returned")
	}
	var parsed struct {
		PublicKey struct {
			AllowCredentials []any  `json:"allowCredentials"`
			Challenge        string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// No allowCredentials: the browser picks the resident key. Populating it
	// would leak which credentials exist to an unauthenticated caller.
	if len(parsed.PublicKey.AllowCredentials) != 0 {
		t.Fatalf("allowCredentials was populated: %v", parsed.PublicKey.AllowCredentials)
	}
	if parsed.PublicKey.Challenge == "" {
		t.Fatal("no challenge in options")
	}
}

func TestFinishLoginRejectsForgedChallenge(t *testing.T) {
	a := testAuthenticator(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	_, err := a.FinishLogin(context.Background(), "forged.challenge", []byte(`{}`))
	if err == nil {
		t.Fatal("a forged sealed challenge was accepted")
	}
}

func TestFinishLoginRejectsExpiredChallenge(t *testing.T) {
	a := testAuthenticator(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	expired, _ := testCodec().Seal([]byte(`{"challenge":"x"}`), -1)
	if _, err := a.FinishLogin(context.Background(), expired, []byte(`{}`)); err == nil {
		t.Fatal("an expired challenge was accepted")
	}
}

func TestBeginRegistrationSealsChallenge(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	a := testAuthenticator(t, u)
	opts, sealed, err := a.BeginRegistration(context.Background(), u)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if len(opts) == 0 || sealed == "" {
		t.Fatal("empty options or challenge")
	}
	if _, err := testCodec().Open(sealed); err != nil {
		t.Fatalf("sealed challenge does not verify: %v", err)
	}
}

func TestCounterRegressionIsDistinguishable(t *testing.T) {
	// ErrCounterRegression must be its own error so the HTTP layer can log it
	// for investigation while still refusing the login. It must never lead to
	// deleting the credential: the check runs before signature verification,
	// so anyone who learns a credentialId could otherwise revoke it.
	if ErrCounterRegression == nil {
		t.Fatal("ErrCounterRegression is not defined")
	}
	if !errors.Is(ErrCounterRegression, ErrCounterRegression) {
		t.Fatal("ErrCounterRegression is not comparable with errors.Is")
	}
}
