package authd

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
)

const challengeCookie = "frame_challenge"

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
	u, err := s.cfg.Auth.FinishLogin(r.Context(), c.Value, body)
	if err != nil {
		// A counter regression means a possible cloned authenticator. It is
		// logged loudly and refused, but the credential is deliberately left
		// in place — see ErrCounterRegression for why revoking here would be
		// an unauthenticated denial-of-service.
		if errors.Is(err, ErrCounterRegression) {
			slog.Error("possible cloned authenticator: sign counter did not advance", "error", err)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.setSession(w, u) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRegisterBegin enrols an additional key for whoever is already signed
// in. Enrolling for someone else is not possible here by construction: the
// account comes from the session cookie, never from the request body.
func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
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

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
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
		http.Error(w, "registration failed", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
