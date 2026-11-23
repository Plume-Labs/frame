package installer

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// Server is the guided installer's HTTP side. It listens on loopback only:
// whoever can reach it can erase the disks, so it is not offered to a network.
type Server struct {
	// Hardware reads the machine; called once per page load, so a disk
	// plugged in meanwhile shows up.
	Hardware func(context.Context) (Hardware, error)
	// Start hands the rendered autoinstall document to the installer. It is
	// called at most once.
	Start func(ctx context.Context, userData string) error

	// Token must accompany every write (header X-Installer-Token). It is shown
	// on the console, so only someone at the machine can start an erase: the
	// Host and Origin checks stop a browser page, not another local process.
	Token string
	// Keymap, when set, switches the live console to a layout (the first thing the
	// text assistant asks, so that everything typed after it lands as typed).
	// Best effort: its error is shown, not fatal.
	Keymap func(layout string) error

	mu      sync.Mutex
	started bool
	failed  string
}

// Handler serves the screens and their JSON endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/hardware", s.hardware)
	mux.HandleFunc("POST /api/summary", s.summary)
	mux.HandleFunc("POST /api/install", s.install)
	mux.HandleFunc("GET /api/status", s.status)
	return s.guard(mux)
}

// ListenLoopback opens addr, refusing one that is not a loopback address.
func ListenLoopback(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("listen address %q: the installer only listens on a loopback address", addr)
	}
	return net.Listen("tcp", addr)
}

// guard refuses requests a page in the person's own browser could be tricked
// into sending: a Host that is not loopback (DNS rebinding), or a write that
// does not come from this page.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if ip := net.ParseIP(host); !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if s.Token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Installer-Token")), []byte(s.Token)) != 1 {
				http.Error(w, "missing or wrong token", http.StatusForbidden)
				return
			}
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "json only", http.StatusUnsupportedMediaType)
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					http.Error(w, "bad origin", http.StatusForbidden)
					return
				}
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hardware(w http.ResponseWriter, r *http.Request) {
	hw, err := s.Hardware(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "hardware": hw})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hardware": hw, "roles": rolesList(), "keyboards": keyboardsList()})
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.plan(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": plan.Summary})
}

func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.plan(w, r)
	if !ok {
		return
	}
	status, err := s.Run(context.WithoutCancel(r.Context()), plan)
	if err != nil {
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"uid": plan.Spec.UID})
}

// Run renders plan and hands it to Start, once per process: the page and the
// text console share it, so whichever is used first locks out the other. The
// HTTP status is the one the page reports on failure.
func (s *Server) Run(ctx context.Context, plan Plan) (int, error) {
	doc, err := plan.Render()
	if err != nil {
		return http.StatusUnprocessableEntity, err
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return http.StatusConflict, errors.New("an installation is already running")
	}
	s.started = true
	s.failed = ""
	s.mu.Unlock()

	// Not the request's context: the installer must keep going if the page is closed.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := s.Start(ctx, doc); err != nil {
		// Never reopened: Start may have written the document and restarted the
		// installer before it failed, and a second run over a half-erased disk
		// is worse than a reboot. The page says so.
		s.mu.Lock()
		s.failed = err.Error()
		s.mu.Unlock()
		return http.StatusInternalServerError, err
	}
	return http.StatusAccepted, nil
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := "idle"
	if s.started {
		state = "installing"
	}
	if s.failed != "" {
		state = "failed"
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "error": s.failed})
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) (Plan, bool) {
	var a Answers
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request: " + err.Error()})
		return Plan{}, false
	}
	hw, err := s.Hardware(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return Plan{}, false
	}
	plan, err := BuildPlan(hw, a, NewUID())
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return Plan{}, false
	}
	return plan, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
