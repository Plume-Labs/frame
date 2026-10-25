package authd

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
)

const challengeCookie = "frame_challenge"

// finishLogin is an indirection over Authenticator.FinishLogin, so a test can
// exercise handleLoginFinish's post-ceremony half — which is where the state
// check lives — without signing a real WebAuthn assertion. Production code
// always calls through this var unmodified.
var finishLogin = (*Authenticator).FinishLogin

// setChallenge parks the sealed ceremony state in a short-lived cookie. Same
// attributes as the session cookie: the challenge is signed, but there is no
// reason to let script read it either.
func (s *Server) setChallenge(w http.ResponseWriter, sealed string) {
	http.SetCookie(w, &http.Cookie{
		Name:     challengeCookie,
		Value:    sealed,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(challengeTTL.Seconds()),
	})
}

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	options, sealed, err := s.cfg.Auth.BeginLogin(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setChallenge(w, sealed)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(options)
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	u, result, err := finishLogin(s.cfg.Auth, r.Context(), c.Value, body)
	if err != nil {
		// A counter regression means a possible cloned authenticator. It is
		// logged loudly and refused, but the credential is deliberately left
		// in place — see ErrCounterRegression for why revoking here would be
		// an unauthenticated denial-of-service.
		if errors.Is(err, ErrCounterRegression) {
			slog.Error("possible cloned authenticator: sign counter did not advance", "error", err)
		}
		// Logged so an operator who just switched to required can tell a key
		// without a PIN from any other failed sign-in. ErrUserNotVerified is
		// only returned after the assertion validated, signature included, so
		// this line needs an enrolled key to produce: no anonymous caller can
		// fill the log with it. The error names a credential fingerprint,
		// never an account.
		if errors.Is(err, ErrUserNotVerified) {
			slog.Warn("Refused passkey sign-in without user verification", "error", err)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Possession of the key is not the whole question. Refused after the
	// ceremony rather than before it, because before it there is no account
	// to ask about: the login is usernameless and the assertion is what names
	// the holder. Same bare 401 as every other failure here.
	if err := requireIssuable(u); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.setSession(w, u) {
		return
	}
	// One line per successful passkey sign-in, saying whether the
	// authenticator verified the user. In the default "log" mode this is how
	// an operator finds the keys that "required" would refuse before
	// switching. The role is logged, the account is not: FrameUser names are
	// derived from the email address, and no other authd log line carries
	// one. The credential is a fingerprint (credentialFingerprint), not its ID.
	slog.Info("Signed in with passkey",
		"userVerified", result.UserVerified,
		"credential", result.Credential,
		"role", u.Spec.Role,
		"userVerificationMode", string(s.cfg.Auth.UserVerification()))
	w.WriteHeader(http.StatusNoContent)
}

// handleRegisterBegin enrols an additional key for whoever is already signed
// in. Enrolling for someone else is not possible here by construction: the
// account comes from the session cookie, never from the request body.
//
// Uses the permissive reader: this is one of the two routes (with
// handleRegisterFinish) that must also accept the PurposeEnrol cookie an
// accepted invitation grants, since enrolling a first passkey is the entire
// point of that cookie. Every other route keeps the strict sessionUser.
func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUserFor(w, r, PurposeSession, PurposeEnrol)
	if !ok {
		return
	}
	options, sealed, err := s.cfg.Auth.BeginRegistration(r.Context(), u)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setChallenge(w, sealed)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(options)
}

// Uses the permissive reader for the same reason as handleRegisterBegin: an
// accepted invitation must be able to complete enrolment on the PurposeEnrol
// cookie it was granted.
func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUserFor(w, r, PurposeSession, PurposeEnrol)
	if !ok {
		return
	}
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	label := r.URL.Query().Get("label")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Auth.FinishRegistration(r.Context(), u, c.Value, body, label); err != nil {
		if errors.Is(err, ErrUserNotVerified) {
			slog.Warn("Refused passkey registration without user verification", "error", err)
		}
		http.Error(w, "registration failed", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
