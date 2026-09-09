package authd

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// maxEmailLength mirrors the CRD's MaxLength on spec.email (RFC 5321's limit,
// T6). Checked here so an over-long address is a clean 400 rather than an
// opaque admission failure after the account name has already been derived.
const maxEmailLength = 254

// handleInvite creates an account holding no credential, and returns the one
// link that can give it one.
//
// The role is operator or viewer, and that is a consequence rather than a
// preference. authd creates the FrameUser under its own ServiceAccount, and
// the admission webhook refuses a create carrying spec.role: admin from
// anyone who is not already an admin, once any admin exists
// (requireAdminRequester, internal/webhook/frame/v1beta1/frameuser_webhook.go).
// That guard is what stops everything holding `create frameusers` from minting
// an admin, and authd holds exactly that. So an admin invite would be refused
// at admission and surface here as a 500 with an unreadable message; refusing
// it up front, naming the way through, is the same rule stated where the
// caller can act on it. The way through is real: promoting an account from the
// Accounts screen travels under the signed-in admin's own impersonated
// identity, which is precisely what the webhook asks for.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	if caller.Spec.Role != framev1beta1.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !emailPattern.MatchString(body.Email) || len(body.Email) > maxEmailLength {
		http.Error(w, "invalid request: email", http.StatusBadRequest)
		return
	}
	if body.Role != framev1beta1.RoleOperator && body.Role != framev1beta1.RoleViewer {
		http.Error(w, "an invitation may only create an operator or a viewer. "+
			"Create the account as one of those, then promote it from the Accounts screen, "+
			"which acts under your own identity rather than authd's", http.StatusBadRequest)
		return
	}
	name := frameUserNameForEmail(body.Email)
	if name == "" {
		http.Error(w, "invalid request: email", http.StatusBadRequest)
		return
	}

	user := &framev1beta1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.cfg.Namespace},
		Spec: framev1beta1.FrameUserSpec{
			Email: body.Email,
			Role:  body.Role,
			State: framev1beta1.StateEnabled,
			// Passkey-only, like the first admin: no route sets a password,
			// and the invitation's whole job is to reach enrolment.
			PasswordAuth: framev1beta1.PasswordDisabled,
		},
	}
	if err := s.cfg.Store.Create(r.Context(), user); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// frameUserNameForEmail is deterministic, so this is either the
			// same address invited twice or the collision its own comment
			// warns about ("a@b" and "a-at-b" derive the same name). It stops
			// being a one-caller function here — bootstrap ran once, invite
			// runs whenever an admin asks — so the collision is answered
			// rather than assumed away. Both cases are the caller's to
			// resolve and neither is an authd failure.
			http.Error(w, "an account with that name already exists", http.StatusConflict)
			return
		}
		slog.Error("invite: failed to create the invited account", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sealed, err := s.cfg.Codec.Seal(PurposeInvite, []byte(body.Email), s.cfg.InviteTTL)
	if err != nil {
		// The account exists and the admin can invite again once this is
		// fixed; there is nothing to roll back that leaving it enabled and
		// credential-less does not already express.
		slog.Error("invite: failed to seal the invitation token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// The link is returned, never sent: there is no mail path in this cluster,
	// and inventing one would be a second project. The admin copies it.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url": s.cfg.ConsoleOrigin + "/invite?token=" + url.QueryEscape(sealed),
	})
}
