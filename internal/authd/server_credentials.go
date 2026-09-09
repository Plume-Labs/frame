package authd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// credentialView is what a caller may see of an enrolled authenticator.
//
// PublicKey is deliberately absent: nothing in the console needs it, and a
// record that does not travel cannot leak. The credential ID does travel,
// because DELETE needs something to address — but note that this route is
// the *first* place a credential ID is exposed in this system: BeginLogin
// (webauthn.go) deliberately sends no allowCredentials, because listing
// credential IDs there would tell an unauthenticated caller which accounts
// exist. The exposure here is safe for a different reason: an ID without the
// matching private key confers nothing, and this route only ever hands one
// to the account's own owner or to an admin who could already read it
// straight off the FrameUser's status through the apiserver.
type credentialView struct {
	ID        string      `json:"id"`
	Label     string      `json:"label,omitempty"`
	AddedAt   metav1.Time `json:"addedAt"`
	SignCount uint32      `json:"signCount"`
}

// subjectOf resolves which account a credentials request is about: the
// caller's own, or someone else's when ?user= names them and the caller is an
// admin.
//
// The role check is what stops this being an account-enumerator: without it
// any signed-in viewer could walk a list of addresses and read existence off
// the 200/404 difference. An admin can already list every FrameUser through
// the apiserver, so the same answer costs nothing there.
func (s *Server) subjectOf(w http.ResponseWriter, r *http.Request, caller *framev1beta1.FrameUser) (*framev1beta1.FrameUser, bool) {
	email := r.URL.Query().Get("user")
	if email == "" || email == caller.Spec.Email {
		return caller, true
	}
	if caller.Spec.Role != framev1beta1.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), email)
	if err != nil {
		// "no such account" here is a different message from the "no such
		// credential" a failed revoke answers with below — unlike
		// Store.ByEmail's own ErrUserNotFound, which is deliberately shared
		// across both cases so a caller cannot use it to learn which
		// addresses exist. That protection is unnecessary on this path: only
		// an admin ever reaches it (the role check above), and an admin can
		// already enumerate every FrameUser's existence through the
		// apiserver, so distinguishing the two answers here tells them
		// nothing they could not already ask for directly.
		http.Error(w, "no such account", http.StatusNotFound)
		return nil, false
	}
	return u, true
}

// handleListCredentials answers the question PasskeysDialog could not ask
// before this route existed: which keys does this account actually hold?
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	subject, ok := s.subjectOf(w, r, caller)
	if !ok {
		return
	}
	// Allocated, not nil: an account with no keys must serialise as [] rather
	// than null, so the UI's empty state is one branch instead of two.
	views := make([]credentialView, 0, len(subject.Status.Credentials))
	for _, c := range subject.Status.Credentials {
		views = append(views, credentialView{
			ID: c.ID, Label: c.Label, AddedAt: c.AddedAt, SignCount: c.SignCount,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	// A list of someone's enrolled authenticators is at least as sensitive as
	// the invite link /auth/invite hands out (server_invite.go) — it must not
	// sit in a shared cache or the browser's back-forward cache.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"credentials": views})
}

// handleRevokeCredential removes one enrolled authenticator.
//
// The one refusal it can meet is Store.RemoveCredential's: it will not strip a
// passkey-only account of its last key. That is stricter than "refuses if it
// would leave the last admin with none" — it protects every passkey-only
// account, the last admin among them — so there is no second copy of the rule
// here to drift from the first.
func (s *Server) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	subject, ok := s.subjectOf(w, r, caller)
	if !ok {
		return
	}
	err := s.cfg.Store.RemoveCredential(r.Context(), subject, r.PathValue("id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrUserNotFound):
		http.Error(w, "no such credential", http.StatusNotFound)
	case errors.Is(err, ErrLastCredential):
		// 409, not 500 and not 403: the request was well formed and the caller
		// was entitled to make it; it is the account's state that refuses.
		// The message is the handler's own, not the store's err.Error() — the
		// store's wording (which also names the account) is free to change
		// without silently changing the API's wire text underneath it.
		http.Error(w, "refusing to remove this account's last credential: "+
			"it has no password sign-in configured, so it would become unreachable",
			http.StatusConflict)
	default:
		slog.Error("revoking a credential failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
