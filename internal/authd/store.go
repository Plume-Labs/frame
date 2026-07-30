package authd

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// ErrUserNotFound is returned for both an unknown email and an unknown
// credential, so a caller cannot use the error to tell which addresses exist.
var ErrUserNotFound = errors.New("no such user")

// Store is authd's view of the FrameUser resources.
type Store struct {
	client    client.Client
	namespace string
}

func NewStore(c client.Client, namespace string) *Store {
	return &Store{client: c, namespace: namespace}
}

func (s *Store) list(ctx context.Context) ([]framev1alpha1.FrameUser, error) {
	var users framev1alpha1.FrameUserList
	if err := s.client.List(ctx, &users, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	return users.Items, nil
}

func (s *Store) ByEmail(ctx context.Context, email string) (*framev1alpha1.FrameUser, error) {
	items, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Spec.Email == email {
			return &items[i], nil
		}
	}
	return nil, ErrUserNotFound
}

// Create writes a brand-new FrameUser. Only /auth/bootstrap calls this — every
// other write in this package goes through Status().Update via
// AddCredential/UpdateSignCount/RemoveCredential, because every other write
// is authd editing an account that already exists.
func (s *Store) Create(ctx context.Context, u *framev1alpha1.FrameUser) error {
	if u.Namespace == "" {
		u.Namespace = s.namespace
	}
	if err := s.client.Create(ctx, u); err != nil {
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

// ByCredentialID resolves the account owning a credential, which is how the
// usernameless sign-in flow identifies who is at the keyboard.
func (s *Store) ByCredentialID(ctx context.Context, credID string) (*framev1alpha1.FrameUser, error) {
	items, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		for _, c := range items[i].Status.Credentials {
			if c.ID == credID {
				return &items[i], nil
			}
		}
	}
	return nil, ErrUserNotFound
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	items, err := s.list(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range items {
		if u.Spec.Role == framev1alpha1.RoleAdmin {
			n++
		}
	}
	return n, nil
}

func (s *Store) AddCredential(ctx context.Context, u *framev1alpha1.FrameUser, cred framev1alpha1.WebAuthnCredential) error {
	for _, existing := range u.Status.Credentials {
		if existing.ID == cred.ID {
			return fmt.Errorf("credential %s is already enrolled", cred.ID)
		}
	}
	u.Status.Credentials = append(u.Status.Credentials, cred)
	return s.client.Status().Update(ctx, u)
}

func (s *Store) UpdateSignCount(ctx context.Context, u *framev1alpha1.FrameUser, credID string, count uint32) error {
	for i := range u.Status.Credentials {
		if u.Status.Credentials[i].ID == credID {
			u.Status.Credentials[i].SignCount = count
			return s.client.Status().Update(ctx, u)
		}
	}
	return ErrUserNotFound
}

// RemoveCredential refuses to strip an account of its last way in.
//
// Same class of guard as the admission webhook's, applied here because authd
// is the only writer of status.credentials: revoking the final key of a
// passkey-only account would lock its owner out with no recovery short of
// kubectl.
func (s *Store) RemoveCredential(ctx context.Context, u *framev1alpha1.FrameUser, credID string) error {
	kept := make([]framev1alpha1.WebAuthnCredential, 0, len(u.Status.Credentials))
	found := false
	for _, c := range u.Status.Credentials {
		if c.ID == credID {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return ErrUserNotFound
	}
	if len(kept) == 0 && u.Spec.PasswordAuth != framev1alpha1.PasswordEnabled {
		return fmt.Errorf("refusing to remove the last key of %s: password sign-in is disabled, so the account would become unreachable", u.Spec.Email)
	}
	u.Status.Credentials = kept
	return s.client.Status().Update(ctx, u)
}
