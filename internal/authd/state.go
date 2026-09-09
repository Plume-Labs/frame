package authd

import (
	"errors"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// ErrAccountDisabled is what requireIssuable returns for an account whose
// spec.state is disabled. It is a distinct error so a caller can choose its
// own status code — the login endpoints answer 401 like every other refusal
// they make, and /auth/invite/accept answers 403, because there the caller
// has already proven they hold a valid, unspent invitation and telling them
// the account is switched off leaks nothing they did not already know.
var ErrAccountDisabled = errors.New("account is disabled")

// requireIssuable is the one place that decides whether authd may mint or
// re-mint an identity for u.
//
// Four paths turn something into an identity: POST /auth/token (a cookie into
// a bearer token), password login, passkey login, and invitation acceptance
// (each of the last three, a credential into a cookie). A check missing from
// any one of them makes deactivation a lie, so they all call this. Each has
// its own test, named for its path, that fails if its call is removed —
// a single test of this function would not.
//
// An empty state is enabled. The CRD defaults spec.state to enabled and its
// enum admits no third word, so an account decoded by the apiserver is never
// "" — the only way to see one is a client that never went through the
// apiserver. Reading "" as disabled would lock out every such account for no
// gain; the closed set is enforced one layer up, by the schema, which is what
// makes fail-open right here and would make it wrong for a free-form field.
//
// A nil account is refused: no account is not evidence of a permitted one.
func requireIssuable(u *framev1beta1.FrameUser) error {
	if u == nil || u.Spec.State == framev1beta1.StateDisabled {
		return ErrAccountDisabled
	}
	return nil
}
