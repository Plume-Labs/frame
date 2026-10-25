package authd

import (
	"encoding/json"
	"net/http"
	"time"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// setSession seals a cookie lasting cfg.SessionTTL under PurposeSession for u
// and writes it onto the response. It reports whether that succeeded; on
// failure it has already written a 500 itself, and every caller must stop
// immediately rather than go on to write its own success status on top (the
// bug this return value exists to prevent: a 500 followed by an
// unconditional 204, and the "superfluous response.WriteHeader call" warning
// that comes with it).
func (s *Server) setSession(w http.ResponseWriter, u *framev1beta1.FrameUser) bool {
	return s.setSessionFor(w, u, PurposeSession, s.cfg.SessionTTL)
}

// setSessionFor seals a cookie for u under purpose, valid for ttl, and writes
// it as the frame_session cookie. It is the one place that builds that
// cookie: every session-shaped credential this package hands out — a full
// sign-in (setSession, above) and an accepted invitation's enrolment window
// (setEnrolSession, server_invite.go) alike — goes through here, so the two
// can never drift on name, Path, HttpOnly, Secure, or SameSite; a change to
// one is a change to both, by construction rather than by remembering to
// update two copies. Only the purpose and the TTL are allowed to differ
// between callers, and both are folded into the sealed payload itself, not
// just the cookie's own fields, so neither is a suggestion the browser could
// ignore. Reports whether sealing succeeded, on the same contract as every
// other setter here: on failure it has already written a 500, and the caller
// must stop immediately.
func (s *Server) setSessionFor(w http.ResponseWriter, u *framev1beta1.FrameUser, purpose Purpose, ttl time.Duration) bool {
	sealed, err := s.cfg.Codec.Seal(purpose, []byte(u.Spec.Email), ttl)
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
		MaxAge:   int(ttl.Seconds()),
	})
	return true
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	// Minted fresh from the current role, so a demotion takes effect within one
	// token lifetime instead of lasting as long as the session. The same is
	// true of a deactivation: sessionUser above refuses a disabled account, so
	// the fifteen-minute token is also the longest an open session survives
	// being switched off.
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
//
// The state check is here rather than in each caller because this is the one
// place a cookie becomes an account: putting it here means a route added later
// cannot forget it, and a disabled account cannot enrol a further key on a
// cookie it was already holding when it was switched off.
//
// This is the strict reader: it accepts only a PurposeSession cookie. An
// accepted-invitation cookie is sealed under PurposeEnrol precisely so it
// fails here — /auth/token and /auth/invite must never honour it, or
// accepting a link would be indistinguishable from a full sign-in for as
// long as the link keeps working.
func (s *Server) sessionUser(w http.ResponseWriter, r *http.Request) (*framev1beta1.FrameUser, bool) {
	return s.sessionUserFor(w, r, PurposeSession)
}

// sessionUserFor is sessionUser generalized over which sealed purposes the
// cookie may have been sealed under, tried in order. It exists for exactly
// one pair of callers — handleRegisterBegin and handleRegisterFinish — which
// must accept both an ordinary session and the narrower PurposeEnrol cookie
// an accepted invitation grants, since enrolling a first passkey is the one
// thing that cookie is for. Every other route keeps using the strict
// sessionUser above.
func (s *Server) sessionUserFor(w http.ResponseWriter, r *http.Request, purposes ...Purpose) (*framev1beta1.FrameUser, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	var email []byte
	opened := false
	for _, purpose := range purposes {
		if e, err := s.cfg.Codec.Open(purpose, c.Value); err == nil {
			email = e
			opened = true
			break
		}
	}
	if !opened {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), string(email))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	if err := requireIssuable(u); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}
