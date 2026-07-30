package authd

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// emailPattern mirrors the CRD's own validation
// (`+kubebuilder:validation:Pattern` on FrameUserSpec.Email in
// api/v1alpha1/frameuser_types.go): no '@' or whitespace on either side of
// exactly one '@'. Checking it here means a malformed email is rejected with
// a clean 400 instead of surfacing as an opaque admission-webhook failure
// after the Secret has already been consulted.
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)

// handleBootstrap creates the very first FrameUser, as an admin, then deletes
// the one-shot Secret that authorized it.
//
// Two independent locks close this door, deliberately: AdminCount() > 0
// refuses the request before the token is even checked, and — once that
// first admin exists — the Secret is gone too, so a second replica racing on
// a stale Secret still can't use it. Neither lock alone is enough: the count
// check is what makes a second call 404 instead of leaking whether a stale
// token is "merely" wrong; the Secret delete is what makes the token itself
// worthless afterward, independent of the FrameUser being deleted, edited or
// never created (Store.Create failing halfway through the request would
// otherwise leave a permanently valid token around, e.g. after a rollback).
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if n, err := s.cfg.Store.AdminCount(r.Context()); err != nil || n > 0 {
		http.NotFound(w, r)
		return
	}

	var body struct {
		Token string `json:"token"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.cfg.BootstrapSecret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !emailPattern.MatchString(body.Email) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	name := frameUserNameForEmail(body.Email)
	if name == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	user := &framev1alpha1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.cfg.Namespace},
		Spec: framev1alpha1.FrameUserSpec{
			Email: body.Email,
			Role:  framev1alpha1.RoleAdmin,
			// The first admin enrols a passkey next (POST /auth/register/*,
			// once they have a session); it does not get a password by
			// default.
			PasswordAuth: framev1alpha1.PasswordDisabled,
		},
	}
	if err := s.cfg.Store.Create(r.Context(), user); err != nil {
		// Never log the token; this is a Kubernetes API error about the
		// FrameUser object, not the request body.
		slog.Error("bootstrap: failed to create the first admin", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.deleteBootstrapSecret(r)
	w.WriteHeader(http.StatusNoContent)
}

// deleteBootstrapSecret is best-effort on purpose: the FrameUser already
// exists by the time this runs, and the AdminCount guard above closes
// /auth/bootstrap regardless of whether this delete succeeds. A failure here
// is logged for an operator to clean up by hand, not returned as a request
// failure — the caller already got the admin account it asked for.
func (s *Server) deleteBootstrapSecret(r *http.Request) {
	if s.cfg.Client == nil || s.cfg.BootstrapSecretName == "" {
		return
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: s.cfg.BootstrapSecretName, Namespace: s.cfg.Namespace},
	}
	if err := s.cfg.Client.Delete(r.Context(), secret); err != nil && !apierrors.IsNotFound(err) {
		slog.Error("bootstrap: failed to delete the bootstrap secret; delete it manually so the token cannot be reused",
			"secret", s.cfg.BootstrapSecretName, "namespace", s.cfg.Namespace, "error", err)
	}
}

// frameUserNameForEmail derives a deterministic, valid Kubernetes object name
// from an email address. An email is not a legal object name (RFC 1123
// subdomain: lowercase alphanumerics, '-' and '.', starting/ending
// alphanumeric) — most obviously because of the '@' — so this lowercases the
// address, spells out the '@' as "-at-", and replaces every other disallowed
// character with '-'. It is deterministic and collision-resistant for
// realistic email addresses; it does not guarantee global uniqueness for
// pathological inputs (e.g. "a@b" and "a-at-b" would collide), which is an
// accepted tradeoff here because this function is exercised by exactly one
// caller — the bootstrap handler, which only ever runs once, to create the
// single first admin.
func frameUserNameForEmail(email string) string {
	lower := strings.ToLower(email)
	spelled := strings.ReplaceAll(lower, "@", "-at-")

	var b strings.Builder
	for _, r := range spelled {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-.")
	const maxNameLength = 253
	if len(name) > maxNameLength {
		name = strings.TrimRight(name[:maxNameLength], "-.")
	}
	return name
}
