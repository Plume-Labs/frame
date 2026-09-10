package uiproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type stubVerifier struct {
	id  Identity
	err error
}

func (s stubVerifier) Verify(context.Context, string) (Identity, error) { return s.id, s.err }

// stubRecorder lets tests observe Start/Finish without a real client. The
// mutex is not decoration: an upgraded request is closed from its own
// goroutine after the client hangs up, so the test reads these fields while
// the server may still be writing them.
type stubRecorder struct {
	mu        sync.Mutex
	startName string

	finishCalled bool
	finishedCode int
}

func (s *stubRecorder) Start(context.Context, Identity, *http.Request) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startName
}

func (s *stubRecorder) Finish(_ context.Context, _ string, httpCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalled = true
	s.finishedCode = httpCode
}

func (s *stubRecorder) finished() (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishCalled, s.finishedCode
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

	called, code := rec.finished()
	if !called {
		t.Fatal("Finish was never called after the proxied call panicked")
	}
	if code != http.StatusInternalServerError {
		t.Fatalf("finishedCode = %d, want %d", code, http.StatusInternalServerError)
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

// echoUpgrade is an upstream that accepts a protocol upgrade and echoes one
// line back, the smallest thing that exercises the whole hijack path without
// pulling in a WebSocket library. The framing does not matter — what is being
// tested is that bytes flow in both directions after the 101.
func echoUpgrade(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the upstream's own writer is not an http.Hijacker")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
		if p := r.Header.Get("Sec-WebSocket-Protocol"); p != "" {
			resp += "Sec-WebSocket-Protocol: " + p + "\r\n"
		}
		if _, err := brw.WriteString(resp + "\r\n"); err != nil {
			return
		}
		if err := brw.Flush(); err != nil {
			return
		}
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = brw.WriteString("echo:" + line)
		_ = brw.Flush()
	}))
}

// dialUpgrade opens a raw connection to `addr` and performs a WebSocket-shaped
// upgrade for `path`, returning the response and the buffered connection so
// the caller can keep talking on it.
func dialUpgrade(t *testing.T, addr, path string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: frame.test\r\n" +
		"Authorization: Bearer good\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n\r\n"
	if _, err := fmt.Fprint(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return res, conn, br
}

// The proxy must be able to carry a protocol upgrade, because a pod shell is
// one and there is no second door.
//
// httputil.ReverseProxy type-asserts the ResponseWriter to http.Hijacker the
// moment the upstream answers 101, and statusRecorder wraps that writer. Delete
// statusRecorder.Hijack and this test reports 502 with a body reading
// "can't switch protocols using non-Hijacker ResponseWriter type
// *uiproxy.statusRecorder" — the exact failure the shipped code had, and the
// same shape as the missing Flush before it.
func TestCarriesAProtocolUpgradeThrough(t *testing.T) {
	up := echoUpgrade(t)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com"}}, up.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	res, conn, br := dialUpgrade(t, strings.TrimPrefix(front.URL, "http://"),
		"/api/v1/namespaces/neura/pods/api-0/exec")
	defer func() { _ = conn.Close() }()

	if res.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("got %d (%s), want 101 — the upgrade never happened", res.StatusCode, body)
	}
	if _, err := fmt.Fprint(conn, "ping\n"); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "echo:ping\n" {
		t.Fatalf("read %q after the upgrade, want %q", line, "echo:ping\n")
	}
}

// ReverseProxy writes the 101 straight to the hijacked connection's
// bufio.Writer and never calls WriteHeader, so the wrapper's `code` stays 0
// unless Hijack sets it. 0 is what the deferred close in ServeHTTP turns into
// a 500 — so without this the record of every successful shell would say the
// server failed.
func TestAnUpgradeIsRecordedAs101NotAsAServerError(t *testing.T) {
	up := echoUpgrade(t)
	defer up.Close()
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	rec := &stubRecorder{startName: "task-ws"}
	p, err := New(Options{
		Verifier: stubVerifier{id: Identity{User: "alice@example.com"}},
		Recorder: rec,
		Upstream: u,
	})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Force the recorder on for this GET; Task 4 makes ServeHTTP decide
		// this for itself.
		r.Method = http.MethodPatch
		p.ServeHTTP(w, r)
	}))
	defer front.Close()

	res, conn, _ := dialUpgrade(t, strings.TrimPrefix(front.URL, "http://"),
		"/api/v1/namespaces/neura/pods/api-0/exec")
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("got %d, want 101", res.StatusCode)
	}
	_ = conn.Close()

	// The record closes when the hijacked copy ends, which is after the client
	// hangs up — give ServeHTTP a moment to return. Read through finished(),
	// not the fields: this runs on the test goroutine while the server may
	// still be writing them.
	deadline := time.Now().Add(2 * time.Second)
	var called bool
	var code int
	for time.Now().Before(deadline) {
		if called, code = rec.finished(); called {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !called {
		t.Fatal("the record was never closed after the upgraded connection ended")
	}
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("finishedCode = %d, want 101", code)
	}
}
