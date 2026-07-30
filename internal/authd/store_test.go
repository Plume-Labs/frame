package authd

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

func storeWith(t *testing.T, users ...*framev1alpha1.FrameUser) *Store {
	t.Helper()
	s := scheme.Scheme
	if err := framev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1alpha1.FrameUser{})
	for _, u := range users {
		b = b.WithObjects(u)
	}
	return NewStore(b.Build(), "cluster-control")
}

func fixture(name, email, role string, creds ...framev1alpha1.WebAuthnCredential) *framev1alpha1.FrameUser {
	return &framev1alpha1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cluster-control"},
		Spec:       framev1alpha1.FrameUserSpec{Email: email, Role: role},
		Status:     framev1alpha1.FrameUserStatus{Credentials: creds},
	}
}

func TestByEmailFindsAndMisses(t *testing.T) {
	s := storeWith(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
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

func TestByCredentialID(t *testing.T) {
	cred := framev1alpha1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk", SignCount: 7}
	s := storeWith(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred))
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
	cred := framev1alpha1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk", SignCount: 7}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred)
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
	cred := framev1alpha1.WebAuthnCredential{ID: "only", PublicKey: "pk"}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred)
	u.Spec.PasswordAuth = framev1alpha1.PasswordDisabled
	s := storeWith(t, u)
	if err := s.RemoveCredential(context.Background(), u, "only"); err == nil {
		t.Fatal("removing the last key of a passkey-only account was allowed")
	}
}

func TestRemoveCredentialAllowedWhenPasswordEnabled(t *testing.T) {
	cred := framev1alpha1.WebAuthnCredential{ID: "only", PublicKey: "pk"}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred)
	u.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
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
		fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin),
		fixture("bob", "bob@example.com", framev1alpha1.RoleViewer),
	)
	n, err := s.AdminCount(context.Background())
	if err != nil {
		t.Fatalf("AdminCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("AdminCount = %d, want 1", n)
	}
}
