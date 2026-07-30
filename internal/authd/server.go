package authd

import (
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sessionCookie = "frame_session"

// ServerConfig wires the HTTP surface to the pieces built in Tasks 3-7.
//
// Client and BootstrapSecretName exist only for /auth/bootstrap's two
// side effects that Store cannot perform on its own: Store is "authd's view
// of the FrameUser resources" (see store.go) and deliberately knows nothing
// about Secrets, so deleting the one-shot bootstrap Secret needs a raw
// client.Client rather than a new Store method. Creating the FrameUser itself
// does go through Store (a new Store.Create, alongside its existing
// AddCredential/UpdateSignCount/RemoveCredential) so every FrameUser write
// stays in one place.
type ServerConfig struct {
	Store  *Store
	Auth   *Authenticator
	Issuer *Issuer
	Codec  *ChallengeCodec

	// BootstrapSecret is the token value itself, compared constant-time
	// against what the caller presents.
	BootstrapSecret string
	// BootstrapSecretName is the Kubernetes Secret object deleted once
	// bootstrap succeeds, so the token cannot be replayed. Optional: if
	// empty (or Client is nil) the deletion step is skipped, which only
	// matters for tests that don't exercise bootstrap.
	BootstrapSecretName string
	// Client reaches the Kubernetes API directly for that Secret delete.
	Client client.Client

	Namespace  string
	TokenTTL   time.Duration
	SessionTTL time.Duration
}

// Server is authd's HTTP surface: WebAuthn and password login, session
// cookies, OIDC discovery/JWKS, and the one-shot bootstrap.
type Server struct {
	cfg ServerConfig
	mux *http.ServeMux
}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /.well-known/openid-configuration", s.handleDiscovery)
	s.mux.HandleFunc("GET /keys", s.handleJWKS)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.HandleFunc("POST /auth/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("POST /auth/login/password", s.handlePasswordLogin)
	s.mux.HandleFunc("POST /auth/token", s.handleToken)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("POST /auth/login/begin", s.handleLoginBegin)
	s.mux.HandleFunc("POST /auth/login/finish", s.handleLoginFinish)
	s.mux.HandleFunc("POST /auth/register/begin", s.handleRegisterBegin)
	s.mux.HandleFunc("POST /auth/register/finish", s.handleRegisterFinish)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	body, err := s.cfg.Issuer.Discovery()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	body, err := s.cfg.Issuer.JWKS()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
