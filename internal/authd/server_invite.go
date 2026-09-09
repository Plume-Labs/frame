package authd

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

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
		msg := "an invitation may only create an operator or a viewer."
		if body.Role == framev1beta1.RoleAdmin {
			// Only the admin case gets the promotion pointer: a typo'd or
			// empty role is not asking to be an admin, and telling it "go to
			// the Accounts screen" answers a question it didn't ask.
			msg += " Create the account as one of those, then promote it from the Accounts screen, " +
				"which acts under your own identity rather than authd's"
		}
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	// Identity here is the email, not the derived object name (see
	// frameUserNameForEmail's own comment): two accounts must never share an
	// address, case-insensitively, regardless of what their names happen to
	// sanitize to. Checked before Create so the common case — inviting
	// someone twice — gets a message about the email that is actually in
	// conflict, not the internal object name.
	//
	// Deliberately not Store.ByEmail: that method answers "which account
	// holds the credential/session just verified" and must match by exact
	// address only, or two accounts differing solely by case could resolve
	// to each other depending on which sorts first — an identity-resolution
	// bug, not a duplicate-detection one. "Does anyone already claim this
	// address, any case" is a different question, asked only here.
	exists, err := s.inviteeAlreadyExists(r.Context(), body.Email)
	if err != nil {
		slog.Error("invite: failed to check for an existing account", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Error(w, "an account with that email already exists", http.StatusConflict)
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
			// Backstop, not the primary guard: inviteeAlreadyExists above is
			// what normally catches a duplicate invite, by the identity that
			// actually matters. This still fires in two cases the pre-check
			// cannot rule out: two requests racing between that check and
			// this Create, or an out-of-band object (created straight
			// through the apiserver, bypassing authd entirely) already
			// occupying the derived name under a *different* Spec.Email —
			// frameUserNameForEmail's hash suffix makes that name collision
			// vanishingly unlikely for two distinct addresses, but does not
			// make it impossible.
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
	// The body carries a bearer credential — the sealed invitation token —
	// like every other route in this package that hands one out. The others
	// all travel as a Set-Cookie the browser is trusted to manage; this one
	// is JSON, which a proxy or the browser's own back-forward cache could
	// otherwise retain.
	w.Header().Set("Cache-Control", "no-store")
	// The link is returned, never sent: there is no mail path in this cluster,
	// and inventing one would be a second project. The admin copies it.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url": s.cfg.ConsoleOrigin + "/invite?token=" + url.QueryEscape(sealed),
	})
}

// inviteeAlreadyExists reports whether any account already claims email,
// matching case-insensitively.
//
// This is deliberately not Store.ByEmail. ByEmail resolves an identity from
// a caller-supplied credential (a session cookie, a login attempt) and must
// return exactly the account the caller authenticated as, by exact address —
// folding case there would let two accounts differing only in case resolve
// to each other depending on which sorts first in Store.list, independent of
// which one actually holds the verified credential. "Does an account already
// claim this address, under any casing" is a different question: it doesn't
// resolve to an identity, it only needs a yes/no, and it is asked exactly
// once, here, by the one route that creates new accounts from a
// caller-supplied address rather than an already-verified one.
func (s *Server) inviteeAlreadyExists(ctx context.Context, email string) (bool, error) {
	items, err := s.cfg.Store.list(ctx)
	if err != nil {
		return false, err
	}
	for i := range items {
		if strings.EqualFold(items[i].Spec.Email, email) {
			return true, nil
		}
	}
	return false, nil
}

// enrolSessionTTL is how long the enrolment cookie an accepted invitation
// grants lasts. Long enough to find a key and enrol it; short enough that a
// link forwarded to the wrong person, or left in a browser's history on a
// shared machine, is not a standing account. It bounds one acceptance, not
// the invitation as a whole: see setEnrolSession and the note on
// handleInviteAccept about what actually spends the link.
const enrolSessionTTL = 15 * time.Minute

// setEnrolSession seals a PurposeEnrol cookie for u — deliberately not
// PurposeSession — through the shared setSessionFor (server_session.go),
// which is what keeps this cookie's name and attributes byte-identical to a
// full session's without a second copy of that cookie block to drift out of
// sync: only the purpose and the TTL differ, and setSessionFor is where that
// difference is made. sessionUser (used by every route except
// handleRegisterBegin and handleRegisterFinish, see sessionUserFor) opens
// only PurposeSession, so this cookie is refused everywhere but the two
// routes that need it to reach a first credential: it cannot mint an
// id_token from /auth/token, and it cannot call /auth/invite, no matter what
// role the account holds.
func (s *Server) setEnrolSession(w http.ResponseWriter, u *framev1beta1.FrameUser) bool {
	return s.setSessionFor(w, u, PurposeEnrol, enrolSessionTTL)
}

// handleInviteAccept spends an invitation.
//
// Single-use by construction rather than by a stored flag: the link is
// refused the moment the account holds any credential. There is no table to
// clean up, no revocation list to keep in step, and nothing that can
// disagree with the account itself about whether the link has been spent.
// Note what that construction actually pins: the link is spendable until the
// first *enrolment*, not until the first *acceptance* — accepting the same
// unspent link twice, with no credential added in between, succeeds both
// times (see TestAcceptingAnUnspentLinkTwiceIsNotItselfSpending). Each
// acceptance grants its own PurposeEnrol cookie, and neither of those can do
// anything beyond enrolling a credential — see setEnrolSession — so two live
// enrolment windows for an account that still holds no key is not a standing
// credential the way a second full session would be.
//
// Every failure that is not "already used" answers the same 401 with the same
// wording. An invitation link travels through chat and inboxes, so telling a
// holder of a wrong link whether the address exists, whether it expired, or
// whether the token was ever real would be an account-enumeration oracle
// reachable without any session at all.
func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	email, err := s.cfg.Codec.Open(PurposeInvite, body.Token)
	if err != nil {
		http.Error(w, "this invitation link is invalid or has expired", http.StatusUnauthorized)
		return
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), string(email))
	if err != nil {
		http.Error(w, "this invitation link is invalid or has expired", http.StatusUnauthorized)
		return
	}
	// The fourth of the four identity-issuing paths; see requireIssuable in
	// state.go. 403 rather than 401 here: the caller has already produced a
	// valid, unexpired invitation for this exact account, so saying it is
	// switched off tells them nothing they could not already infer, and
	// leaving them at "invalid or expired" would send them chasing the link.
	if err := requireIssuable(u); err != nil {
		http.Error(w, "this account is disabled", http.StatusForbidden)
		return
	}
	if len(u.Status.Credentials) > 0 || u.Status.PasswordHash != "" {
		// "already been used" would overstate it for the password branch:
		// no route sets a password today, so if this ever fires for a
		// password it did not necessarily come from this invitation. What is
		// actually true, and all that matters here, is that the account
		// already has a credential.
		http.Error(w, "this account already has a credential", http.StatusGone)
		return
	}
	if !s.setEnrolSession(w, u) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
