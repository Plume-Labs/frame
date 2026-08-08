package authd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
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
	expired, _ := testCodec().Seal(PurposeChallenge, []byte(`{"challenge":"x"}`), -1)
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
	if _, err := testCodec().Open(PurposeChallenge, sealed); err != nil {
		t.Fatalf("sealed challenge does not verify: %v", err)
	}
}

func TestCounterRegressionIsDistinguishable(t *testing.T) {
	// ErrCounterRegression must be its own error so the HTTP layer can log it
	// for investigation while still refusing the login. It must never lead to
	// deleting the credential: go-webauthn's ValidateDiscoverableLogin
	// verifies the assertion's signature first and only afterwards compares
	// the reported counter against the stored one (see the comment on
	// recordLoginCounter), so by the time this error can even occur the
	// caller has already proven possession of the private key. Revoking here
	// would instead punish the legitimate case — an authenticator that
	// glitched or was restored from a backup — by automatically taking away
	// its owner's only way in.
	if ErrCounterRegression == nil {
		t.Fatal("ErrCounterRegression is not defined")
	}
	if !errors.Is(ErrCounterRegression, ErrCounterRegression) {
		t.Fatal("ErrCounterRegression is not comparable with errors.Is")
	}
}

// TestRecordLoginCounterRefusesOnCloneWarningWithoutTouchingStore exercises
// the actual decision FinishLogin delegates to on every login: recordLoginCounter
// is the same method the real ceremony calls (see FinishLogin), not a copy of
// its logic. A post-validation credential with CloneWarning set must produce
// ErrCounterRegression and must leave the stored credential exactly as it
// was -- no sign-count write, no removal -- because the check runs after
// go-webauthn already verified the assertion's signature, and revoking a key
// that just proved possession of the private key would lock out its owner on
// nothing more than a stale or restored counter.
func TestRecordLoginCounterRefusesOnCloneWarningWithoutTouchingStore(t *testing.T) {
	rawID := []byte("clone-warning-credential")
	credID := base64.RawURLEncoding.EncodeToString(rawID)
	stored := framev1alpha1.WebAuthnCredential{ID: credID, PublicKey: "pk", SignCount: 7}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, stored)
	a := testAuthenticator(t, u)

	// SignCount is deliberately different from the stored value (7): if the
	// early return in recordLoginCounter were ever deleted, the fallthrough
	// would write this value and the test would catch it.
	result := &webauthn.Credential{
		ID:            rawID,
		Authenticator: webauthn.Authenticator{SignCount: 99, CloneWarning: true},
	}

	err := a.recordLoginCounter(context.Background(), u, result)
	if !errors.Is(err, ErrCounterRegression) {
		t.Fatalf("recordLoginCounter = %v, want ErrCounterRegression", err)
	}

	got, fetchErr := a.store.ByEmail(context.Background(), "alice@example.com")
	if fetchErr != nil {
		t.Fatalf("ByEmail: %v", fetchErr)
	}
	if len(got.Status.Credentials) != 1 {
		t.Fatalf("credential was removed on a counter regression: %v", got.Status.Credentials)
	}
	if got.Status.Credentials[0].SignCount != 7 {
		t.Fatalf("sign count changed on a counter regression: got %d, want 7 (unchanged)", got.Status.Credentials[0].SignCount)
	}
}

// TestRecordLoginCounterRotatesSignCountWhenClean is the companion positive
// case: a credential with no CloneWarning must have its sign count rotated
// forward in the store, proving a legitimate login is not silently refused
// by the same code path.
func TestRecordLoginCounterRotatesSignCountWhenClean(t *testing.T) {
	rawID := []byte("clean-credential")
	credID := base64.RawURLEncoding.EncodeToString(rawID)
	stored := framev1alpha1.WebAuthnCredential{ID: credID, PublicKey: "pk", SignCount: 7}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, stored)
	a := testAuthenticator(t, u)

	result := &webauthn.Credential{
		ID:            rawID,
		Authenticator: webauthn.Authenticator{SignCount: 9, CloneWarning: false},
	}

	if err := a.recordLoginCounter(context.Background(), u, result); err != nil {
		t.Fatalf("recordLoginCounter: %v", err)
	}

	got, fetchErr := a.store.ByEmail(context.Background(), "alice@example.com")
	if fetchErr != nil {
		t.Fatalf("ByEmail: %v", fetchErr)
	}
	if len(got.Status.Credentials) != 1 {
		t.Fatalf("credential count changed: %v", got.Status.Credentials)
	}
	if got.Status.Credentials[0].SignCount != 9 {
		t.Fatalf("sign count not rotated: got %d, want 9", got.Status.Credentials[0].SignCount)
	}
}
