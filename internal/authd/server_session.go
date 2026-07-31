package authd

import (
	"encoding/json"
	"errors"
	"net/http"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// dummyPasswordHash is a valid argon2id PHC string for a password nobody
// will ever type. It exists purely so VerifyPassword is called with the same
// cost parameters on every failure branch of handlePasswordLogin: Go
// short-circuits `||`, so a naive `err != nil || ... || !VerifyPassword(...)`
// never reaches VerifyPassword for an unknown email or a passkey-only
// account, and those paths return in microseconds next to the ~100ms argon2id
// costs on a real attempt — trivially distinguishable with a stopwatch, even
// though the status code and body are already identical (see
// TestUnknownEmailAndWrongPasswordAreIndistinguishable).
var dummyPasswordHash = mustDummyPasswordHash()

func mustDummyPasswordHash() string {
	hash, err := HashPassword("authd-dummy-password-for-timing-safety")
	if err != nil {
		// HashPassword only fails if crypto/rand can't be read, which would
		// make password hashing unsafe everywhere else too — fail loudly at
		// package init rather than silently falling back to a timing oracle.
		panic("authd: failed to precompute dummy password hash: " + err.Error())
	}
	return hash
}

// verifyPassword is an indirection over the package-level VerifyPassword,
// solely so a test can count how many times handlePasswordLogin invokes it
// per request — a structural, timing-independent way to pin down "exactly
// one verification on every path" (see TestPasswordLoginAlwaysVerifiesExactlyOnce
// in server_test.go) instead of relying on a wall-clock measurement, which
// would be flaky. Production code always calls through this var unmodified.
var verifyPassword = VerifyPassword

func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), body.Email)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Always perform exactly one argon2id verification, on every path,
	// whether or not there is a real account or a real hash to check: this
	// keeps the CPU cost — and so the wall-clock time — identical for an
	// unknown email, a passkey-only account, and a genuinely wrong password.
	// usable stays false, and hash stays the dummy, for the first two; only
	// a real, password-enabled account with a parseable hash gets its own
	// hash checked. Folding hasUsableHash into usable matters because an
	// account can have PasswordAuth: enabled with an empty or malformed
	// spec.passwordHash — nothing writes that field yet, so it's unreachable
	// today, but it is exactly the half-completed state a future
	// set-password endpoint will transiently create between enabling
	// password auth and writing the hash. Without this check, that account
	// would return 401 in microseconds (VerifyPassword's malformed-input
	// fast path) while every other unknown/disabled account costs ~100ms of
	// real argon2id work, positively identifying it by timing alone — the
	// same class of oracle TestUnknownEmailAndWrongPasswordAreIndistinguishable
	// guards against.
	usable := err == nil && u.Spec.PasswordAuth == framev1alpha1.PasswordEnabled && hashIsUsable(u.Spec.PasswordHash)
	hash := dummyPasswordHash
	if usable {
		hash = u.Spec.PasswordHash
	}
	verified := verifyPassword(hash, body.Password)

	// One response for every failure: unknown account, password disabled, and
	// wrong password are indistinguishable, so this endpoint cannot be used to
	// enumerate who has an account.
	if !usable || !verified {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.setSession(w, u) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setSession seals a session cookie for u and writes it onto the response.
// It reports whether that succeeded; on failure it has already written a 500
// itself, and every caller must stop immediately rather than go on to write
// its own success status on top (the bug this return value exists to
// prevent: a 500 followed by an unconditional 204, and the
// "superfluous response.WriteHeader call" warning that comes with it).
func (s *Server) setSession(w http.ResponseWriter, u *framev1alpha1.FrameUser) bool {
	sealed, err := s.cfg.Codec.Seal(PurposeSession, []byte(u.Spec.Email), s.cfg.SessionTTL)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sealed,
		Path:     "/",
		HttpOnly: true, // unreadable from JavaScript: an XSS cannot steal the session
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	return true
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	email, err := s.cfg.Codec.Open(PurposeSession, c.Value)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), string(email))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Minted fresh from the current role, so a demotion takes effect within one
	// token lifetime instead of lasting as long as the session.
	token, err := s.cfg.Issuer.Mint(u.Spec.Email, u.Spec.Role, s.cfg.TokenTTL)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Returned in the body, never as a cookie: the browser keeps it in memory
	// only, so it is never persisted where a later XSS could read it.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id_token":   token,
		"expires_in": int(s.cfg.TokenTTL.Seconds()),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// sessionUser resolves the signed-in account, writing the 401 itself so every
// caller is a two-liner that cannot forget to stop on failure.
func (s *Server) sessionUser(w http.ResponseWriter, r *http.Request) (*framev1alpha1.FrameUser, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	email, err := s.cfg.Codec.Open(PurposeSession, c.Value)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), string(email))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}
