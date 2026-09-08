package uiproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type stubVerifier struct {
	id  Identity
	err error
}

func (s stubVerifier) Verify(context.Context, string) (Identity, error) { return s.id, s.err }

// stubRecorder lets tests observe Start/Finish without a real client.
type stubRecorder struct {
	startName string

	finishCalled bool
	finishedCode int
}

func (s *stubRecorder) Start(context.Context, Identity, *http.Request) string { return s.startName }

func (s *stubRecorder) Finish(_ context.Context, _ string, httpCode int) {
	s.finishCalled = true
	s.finishedCode = httpCode
}

// panicTransport simulates a proxied call that never gets as far as writing
// a response — a bug in a custom RoundTripper, for instance — rather than
// an upstream error, which the reverse proxy's own ErrorHandler always
// turns into a written status.
type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("boom") }

// upstreamEcho records what the proxy actually sent upstream.
func upstreamEcho(t *testing.T, seen *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
}

func newTestProxy(t *testing.T, v Verifier, upstream string) *Proxy {
	t.Helper()
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{Verifier: v, Upstream: u, GroupPrefix: "frame:"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRejectsMissingToken(t *testing.T) {
	var seen http.Header
	up := upstreamEcho(t, &seen)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "a@b.c"}}, up.URL)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if seen != nil {
		t.Fatal("request reached upstream despite having no token")
	}
}

func TestRejectsInvalidToken(t *testing.T) {
	var seen http.Header
	up := upstreamEcho(t, &seen)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{err: errors.New("expired")}, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if seen != nil {
		t.Fatal("request reached upstream with an invalid token")
	}
}

func TestImpersonatesUserAndPrefixedGroup(t *testing.T) {
	var seen http.Header
	up := upstreamEcho(t, &seen)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com", Groups: []string{"operators"}}}, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer good")
	p.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Get("Impersonate-User"); got != "alice@example.com" {
		t.Fatalf("Impersonate-User = %q", got)
	}
	if got := seen.Values("Impersonate-Group"); len(got) != 1 || got[0] != "frame:operators" {
		t.Fatalf("Impersonate-Group = %v, want [frame:operators]", got)
	}
}

// The whole point of this component. A caller that supplies its own
// impersonation headers must not be able to choose who it is.
func TestDropsInboundImpersonationHeaders(t *testing.T) {
	var seen http.Header
	up := upstreamEcho(t, &seen)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com", Groups: []string{"viewers"}}}, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("Impersonate-User", "system:admin")
	req.Header.Add("Impersonate-Group", "system:masters")
	req.Header.Set("Impersonate-Uid", "0")
	req.Header.Set("Impersonate-Extra-Scopes", "everything")
	p.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Get("Impersonate-User"); got != "alice@example.com" {
		t.Fatalf("Impersonate-User = %q, caller's value survived", got)
	}
	for _, g := range seen.Values("Impersonate-Group") {
		if g == "system:masters" {
			t.Fatal("caller-supplied Impersonate-Group reached the apiserver")
		}
	}
	if seen.Get("Impersonate-Uid") != "" {
		t.Fatal("caller-supplied Impersonate-Uid reached the apiserver")
	}
	if seen.Get("Impersonate-Extra-Scopes") != "" {
		t.Fatal("caller-supplied Impersonate-Extra header reached the apiserver")
	}
}

// The user's token is not a credential the apiserver accepts, and
// client-go's bearer transport only fills Authorization when it is empty —
// so leaving it set would send the wrong credential and get a 401.
func TestStripsTheUsersAuthorizationHeader(t *testing.T) {
	var seen http.Header
	up := upstreamEcho(t, &seen)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com"}}, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer users-token")
	p.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Get("Authorization"); got == "Bearer users-token" {
		t.Fatal("the user's own token was forwarded to the apiserver")
	}
}

// A panic in the proxied call must still close the task record — otherwise
// it is left Running forever — and it must not be closed with the
// meaningless code 0.
func TestFinishRunsAndReportsFailureWhenTheProxiedCallPanics(t *testing.T) {
	u, err := url.Parse("http://upstream.invalid")
	if err != nil {
		t.Fatal(err)
	}
	rec := &stubRecorder{startName: "task-1"}
	p, err := New(Options{
		Verifier:  stubVerifier{id: Identity{User: "alice@example.com"}},
		Recorder:  rec,
		Upstream:  u,
		Transport: panicTransport{},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", nil)
	req.Header.Set("Authorization", "Bearer good")

	func() {
		defer func() { recover() }()
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()

	if !rec.finishCalled {
		t.Fatal("Finish was never called after the proxied call panicked")
	}
	if rec.finishedCode != http.StatusInternalServerError {
		t.Fatalf("finishedCode = %d, want %d", rec.finishedCode, http.StatusInternalServerError)
	}
}

// I1 of the whole-branch review. The proxy answered `http.Error(w,
// "unauthorized", 401)` — text/plain — while every client of this endpoint is
// the Kubernetes SDK in `src/lib/frame-sdk.ts`, which calls `res.json()`
// unconditionally. The caller got `SyntaxError: Unexpected token 'u'` instead
// of a 401 it could act on, so nothing could tell "session gone" from a bug,
// and a tab left open past the 12h cookie filled with parse errors.
//
// The apiserver answers a rejection with a metav1.Status body. This proxy
// stands where the apiserver stands, so it answers the same way.
func TestUnauthorizedIsAMetav1Status(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    Verifier
		auth string
	}{
		{"no token", stubVerifier{id: Identity{User: "a@b.c"}}, ""},
		{"bad token", stubVerifier{err: errors.New("expired")}, "Bearer whatever"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen http.Header
			up := upstreamEcho(t, &seen)
			defer up.Close()
			p := newTestProxy(t, tc.v, up.URL)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)

			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json — the SDK parses this body as JSON", ct)
			}
			var status metav1.Status
			if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
				t.Fatalf("body is not a metav1.Status (%q): %v", rec.Body.String(), err)
			}
			if status.Kind != "Status" || status.Code != http.StatusUnauthorized {
				t.Fatalf("kind=%q code=%d, want Status/401", status.Kind, status.Code)
			}
			if status.Reason != metav1.StatusReasonUnauthorized {
				t.Fatalf("reason = %q, want Unauthorized", status.Reason)
			}
			// `message` is the field the SDK's FrameAPIError renders.
			if status.Message == "" {
				t.Fatal("no message — the UI would show a bare status code")
			}
		})
	}
}
