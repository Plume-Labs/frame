package authd

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func storeWith(t *testing.T, users ...*framev1beta1.FrameUser) *Store {
	t.Helper()
	s := scheme.Scheme
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1beta1.FrameUser{})
	for _, u := range users {
		b = b.WithObjects(u)
	}
	return NewStore(b.Build(), "cluster-control")
}

func fixture(name, email, role string, creds ...framev1beta1.WebAuthnCredential) *framev1beta1.FrameUser {
	return &framev1beta1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cluster-control"},
		Spec:       framev1beta1.FrameUserSpec{Email: email, Role: role},
		Status:     framev1beta1.FrameUserStatus{Credentials: creds},
	}
}

func TestByEmailFindsAndMisses(t *testing.T) {
	s := storeWith(t, fixture("alice", "alice@example.com", framev1beta1.RoleAdmin))
	got, err := s.ByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("got %q", got.Name)
	}
	if _, err := s.ByEmail(context.Background(), "nobody@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}

// TestByEmailDoesNotConflateAccountsThatDifferOnlyByCase pins ByEmail as an
// identity-resolution lookup, not a duplicate-detection one: given two
// accounts whose addresses differ only in case (a pair the admission webhook
// does not itself refuse for an out-of-band kubectl write — see
// requireAdminRequester's own doc comment), a query for one account's exact
// address must resolve to that account and no other, never to whichever
// case-variant happens to sort first in Store.list.
//
// A version of ByEmail that matches with strings.EqualFold instead of ==
// fails this: it returns the first EqualFold match regardless of which
// account the caller actually asked for, so — depending on object-name
// ordering — querying the viewer's exact address could resolve to the admin
// account instead. That is exactly the escalation this test exists to catch:
// a session sealed for the viewer's address being handed the admin's
// FrameUser on the next request.
func TestByEmailDoesNotConflateAccountsThatDifferOnlyByCase(t *testing.T) {
	viewer := fixture("bob-viewer", "bob@example.com", framev1beta1.RoleViewer)
	admin := fixture("bob-admin", "Bob@Example.com", framev1beta1.RoleAdmin)
	s := storeWith(t, viewer, admin)

	got, err := s.ByEmail(context.Background(), "bob@example.com")
	if err != nil {
		t.Fatalf("ByEmail(bob@example.com): %v", err)
	}
	if got.Name != "bob-viewer" || got.Spec.Role != framev1beta1.RoleViewer {
		t.Fatalf("ByEmail(bob@example.com) resolved to %q (role %q), want bob-viewer (role viewer) — "+
			"an exact-address lookup must not return whichever case-variant sorts first",
			got.Name, got.Spec.Role)
	}

	got, err = s.ByEmail(context.Background(), "Bob@Example.com")
	if err != nil {
		t.Fatalf("ByEmail(Bob@Example.com): %v", err)
	}
	if got.Name != "bob-admin" || got.Spec.Role != framev1beta1.RoleAdmin {
		t.Fatalf("ByEmail(Bob@Example.com) resolved to %q (role %q), want bob-admin (role admin)",
			got.Name, got.Spec.Role)
	}
}

func TestByCredentialID(t *testing.T) {
	cred := framev1beta1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk", SignCount: 7}
	s := storeWith(t, fixture("alice", "alice@example.com", framev1beta1.RoleAdmin, cred))
	got, err := s.ByCredentialID(context.Background(), "cred-1")
	if err != nil {
		t.Fatalf("ByCredentialID: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("got %q", got.Name)
	}
	if _, err := s.ByCredentialID(context.Background(), "unknown"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}

func TestUpdateSignCount(t *testing.T) {
	cred := framev1beta1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk", SignCount: 7}
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin, cred)
	s := storeWith(t, u)
	if err := s.UpdateSignCount(context.Background(), u, "cred-1", 9); err != nil {
		t.Fatalf("UpdateSignCount: %v", err)
	}
	got, _ := s.ByEmail(context.Background(), "alice@example.com")
	if got.Status.Credentials[0].SignCount != 9 {
		t.Fatalf("counter = %d, want 9", got.Status.Credentials[0].SignCount)
	}
}

func TestRemoveCredentialKeepsLastKeyWhenPasswordDisabled(t *testing.T) {
	cred := framev1beta1.WebAuthnCredential{ID: "only", PublicKey: "pk"}
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin, cred)
	u.Spec.PasswordAuth = framev1beta1.PasswordDisabled
	s := storeWith(t, u)
	if err := s.RemoveCredential(context.Background(), u, "only"); err == nil {
		t.Fatal("removing the last key of a passkey-only account was allowed")
	}
}

// TestRemoveCredentialKeepsLastKeyWhenPasswordAuthUnset pins the zero-value
// behavior of Store.RemoveCredential's last-credential guard, which reads
// `u.Spec.PasswordAuth != framev1beta1.PasswordEnabled` rather than
// `== framev1beta1.PasswordDisabled`. Those two are equivalent today only
// because fixture() never sets PasswordAuth explicitly here, leaving it at
// Go's zero value for the string-typed field (""), which correctly falls on
// the "not enabled" side of the `!=` comparison. A refactor to the `==`
// spelling would compile, and every other RemoveCredential test would still
// pass (they all set PasswordAuth explicitly), but it would silently treat
// an unset field as "enabled" and let the last credential of an
// unconfigured account be removed — reopening the lockout hole. This test is
// the only thing that would catch that refactor, which matters because
// RemoveCredential has no production caller yet: nothing else exercises this
// guard until a future revocation endpoint wires it up.
func TestRemoveCredentialKeepsLastKeyWhenPasswordAuthUnset(t *testing.T) {
	cred := framev1beta1.WebAuthnCredential{ID: "only", PublicKey: "pk"}
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin, cred)
	// PasswordAuth deliberately left at its zero value ("") — not set to
	// PasswordDisabled, which is the point of this test.
	s := storeWith(t, u)
	if err := s.RemoveCredential(context.Background(), u, "only"); err == nil {
		t.Fatal("removing the last key of an account with PasswordAuth unset (zero value) was allowed")
	}
}

func TestRemoveCredentialAllowedWhenPasswordEnabled(t *testing.T) {
	cred := framev1beta1.WebAuthnCredential{ID: "only", PublicKey: "pk"}
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin, cred)
	u.Spec.PasswordAuth = framev1beta1.PasswordEnabled
	s := storeWith(t, u)
	if err := s.RemoveCredential(context.Background(), u, "only"); err != nil {
		t.Fatalf("RemoveCredential: %v", err)
	}
	got, _ := s.ByEmail(context.Background(), "alice@example.com")
	if len(got.Status.Credentials) != 0 {
		t.Fatalf("credential not removed: %v", got.Status.Credentials)
	}
}

func TestAdminCount(t *testing.T) {
	s := storeWith(t,
		fixture("alice", "alice@example.com", framev1beta1.RoleAdmin),
		fixture("bob", "bob@example.com", framev1beta1.RoleViewer),
	)
	n, err := s.AdminCount(context.Background())
	if err != nil {
		t.Fatalf("AdminCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("AdminCount = %d, want 1", n)
	}
}
