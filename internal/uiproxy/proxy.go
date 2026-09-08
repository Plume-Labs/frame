// Package uiproxy authenticates the Frame UI's requests to the Kubernetes
// apiserver.
//
// It exists because the UI used to reach the apiserver through a `kubectl
// proxy` sidecar holding the pod ServiceAccount, which was bound to both a
// viewer and an operator ClusterRole: every write the UI made was anonymous
// and carried the union of those permissions. This proxy takes the token
// authd issues, turns it into an impersonated identity, and lets the
// apiserver's own RBAC decide.
package uiproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strings"
)

// Identity is who the caller is, as proven by their token. Groups are
// unprefixed; New's GroupPrefix is applied on the way out.
type Identity struct {
	User   string
	Groups []string
}

// Verifier validates a raw bearer token and returns who presented it.
type Verifier interface {
	Verify(ctx context.Context, raw string) (Identity, error)
}

// Recorder writes the trace of a mutating request. Start returns the name of
// the record, or "" when nothing was recorded — Finish ignores "".
type Recorder interface {
	Start(ctx context.Context, id Identity, r *http.Request) string
	Finish(ctx context.Context, name string, httpCode int)
}

type Options struct {
	Verifier    Verifier
	Recorder    Recorder
	Upstream    *url.URL
	Transport   http.RoundTripper
	GroupPrefix string
	Log         *slog.Logger
}

type Proxy struct {
	rp       *httputil.ReverseProxy
	verifier Verifier
	recorder Recorder
	prefix   string
	log      *slog.Logger
}

func New(o Options) (*Proxy, error) {
	if o.Verifier == nil {
		return nil, fmt.Errorf("uiproxy: Verifier is required")
	}
	if o.Upstream == nil {
		return nil, fmt.Errorf("uiproxy: Upstream is required")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	rp := httputil.NewSingleHostReverseProxy(o.Upstream)
	if o.Transport != nil {
		rp.Transport = o.Transport
	}
	// Watches are chunked streams the UI depends on for live updates.
	// Without immediate flushing the ReverseProxy buffers them and every
	// screen silently stops refreshing.
	rp.FlushInterval = -1
	return &Proxy{rp: rp, verifier: o.Verifier, recorder: o.Recorder, prefix: o.GroupPrefix, log: o.Log}, nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return h[7:]
	}
	return ""
}

// stripImpersonation removes every impersonation header the caller supplied.
// Prefix matching, not an allow-list of known names: Impersonate-Extra-* is
// open-ended, and a header we forgot to enumerate is a header the caller
// controls.
func stripImpersonation(h http.Header) {
	for k := range h {
		if strings.HasPrefix(textproto.CanonicalMIMEHeaderKey(k), "Impersonate-") {
			h.Del(k)
		}
	}
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// statusRecorder remembers the code so the task can be closed with it.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps watch streaming working: ReverseProxy type-asserts the writer
// to http.Flusher, and a wrapper that does not implement it turns every
// watch into a buffered response.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := p.verifier.Verify(r.Context(), tok)
	if err != nil {
		p.log.Info("rejected token", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	stripImpersonation(r.Header)
	// The user's token is not a credential the apiserver accepts, and
	// client-go's bearer transport only sets Authorization when it is empty.
	r.Header.Del("Authorization")

	r.Header.Set("Impersonate-User", id.User)
	for _, g := range id.Groups {
		r.Header.Add("Impersonate-Group", p.prefix+g)
	}

	sr := &statusRecorder{ResponseWriter: w}
	var task string
	if p.recorder != nil && isMutating(r.Method) {
		task = p.recorder.Start(r.Context(), id, r)
	}
	p.rp.ServeHTTP(sr, r)
	if task != "" {
		// Detached from the request context: it is cancelled the moment the
		// response finishes, which is exactly when this runs.
		p.recorder.Finish(context.WithoutCancel(r.Context()), task, sr.code)
	}
}
