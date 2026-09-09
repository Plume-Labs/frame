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
// because DELETE needs something to address and because it is public by
// construction — an ordinary WebAuthn ceremony puts credential IDs in
// allowCredentials, and this one is returned only to the account's owner or
// to an admin.
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
		// The message is the store's own, which names the account and why.
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		slog.Error("revoking a credential failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
