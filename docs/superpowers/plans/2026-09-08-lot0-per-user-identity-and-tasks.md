# Lot 0a — per-user identity and a task record: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every write the Frame UI makes is attributed to a named human, authorised by that human's RBAC tier, and recorded as a `FrameTask` anyone can read.

**Architecture:** A new `frame-uiproxy` sidecar replaces the `kubectl proxy` sidecar on port 8001. It validates the ES256 token authd already issues, strips any inbound `Impersonate-*` header, impersonates the user and their `frame:` group, and forwards with the pod ServiceAccount — which loses its viewer and operator bindings and keeps only `impersonate` plus write access to `frametasks`. The same proxy records a `FrameTask` per mutating request. No controller is added: tasks referencing an object that already carries progress (FrameJob, TalosUpgrade, Backup) are read through that object.

**Tech Stack:** Go 1.26.1, `net/http/httputil.ReverseProxy`, go-jose/v4 (already a dependency), controller-runtime v0.23.x, envtest, kubebuilder v4 multigroup layout, React 19 + Vite + vitest (node environment), kustomize, Helm.

**Spec:** `docs/superpowers/specs/2026-09-08-lot0-per-user-identity-and-tasks-design.md`

## Global Constraints

- Module path is `github.com/rmocq/frame`. API group for new kinds: `frame.plume-labs.io`, version `v1beta1` (storage/hub version).
- Impersonated group names are `frame:admins`, `frame:operators`, `frame:viewers`. `internal/authd.GroupForRole` returns them **unprefixed** (`admins`/`operators`/`viewers`); the `frame:` prefix is applied by the proxy so that migrating to native OIDC (`--oidc-groups-prefix=frame:`) leaves every RBAC binding untouched.
- FrameUser roles are `admin`, `operator`, `viewer` (`framev1beta1.RoleAdmin` etc.). Tier ClusterRoles are named `frame-<kind>-{viewer,editor,admin}-role`. The mapping, stated once: `frame:viewers` → `*-viewer-role`, `frame:operators` → `*-editor-role`, `frame:admins` → `*-admin-role`.
- `FrameTask` objects are always written in the `frame-system` namespace, whatever namespace the proxied request targets.
- Recording a task must never fail a request. A recorder error is logged and the request proceeds.
- Every dependency added to `go.mod` needs an upper bound. This plan adds none: go-jose, controller-runtime and client-go are already present.
- `make helm-parity` must stay green: `config/rbac/*.yaml` and `charts/frame/templates/rbac-tier-roles.yaml` are two hand-maintained copies of the same rules.
- Vitest runs with `environment: 'node'` and `include: ['src/**/*.test.ts']` — **`.tsx` component tests do not run**. All UI logic that needs a test goes in `src/lib/`.
- This plan targets `main`. The branch `feat/typed-job-submission` has moved the SDK from `src/lib/frame-sdk.ts` into `packages/frame-sdk/`; whichever lands second resolves that rename.

---

### Task 1: The proxy handler — validate, strip, impersonate

**Files:**
- Create: `internal/uiproxy/proxy.go`
- Test: `internal/uiproxy/proxy_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  type Identity struct { User string; Groups []string }        // Groups unprefixed
  type Verifier interface { Verify(ctx context.Context, raw string) (Identity, error) }
  type Recorder interface {
      Start(ctx context.Context, id Identity, r *http.Request) string // returns task name, "" if not recorded
      Finish(ctx context.Context, name string, httpCode int)
  }
  type Options struct {
      Verifier    Verifier
      Recorder    Recorder      // nil disables recording
      Upstream    *url.URL
      Transport   http.RoundTripper
      GroupPrefix string        // "frame:"
      Log         *slog.Logger
  }
  func New(o Options) (*Proxy, error)   // *Proxy implements http.Handler
  ```

- [ ] **Step 1: Write the failing tests**

```go
package uiproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type stubVerifier struct {
	id  Identity
	err error
}

func (s stubVerifier) Verify(context.Context, string) (Identity, error) { return s.id, s.err }

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
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/uiproxy/...`
Expected: FAIL — `undefined: New`, `undefined: Identity`.

- [ ] **Step 3: Write the implementation**

```go
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
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/uiproxy/... -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/uiproxy/proxy.go internal/uiproxy/proxy_test.go
git commit -m "feat(uiproxy): impersonate the token's owner and refuse the caller's own headers"
```

---

### Task 2: The token verifier — authd's JWKS

**Files:**
- Create: `internal/uiproxy/verifier.go`
- Test: `internal/uiproxy/verifier_test.go`

**Interfaces:**
- Consumes: `Identity`, `Verifier` from Task 1.
- Produces:
  ```go
  type JWKSVerifier struct{ ... }
  func NewJWKSVerifier(jwksURL, issuer, audience string, hc *http.Client) *JWKSVerifier
  func (v *JWKSVerifier) Verify(ctx context.Context, raw string) (Identity, error)
  ```
  Claims read: `sub` (becomes `Identity.User`), `groups` (becomes `Identity.Groups`). authd mints exactly one group per user — see `internal/authd/issuer.go`.

- [ ] **Step 1: Write the failing tests**

```go
package uiproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// signerFixture serves a JWKS and mints tokens against it, the way authd does.
type signerFixture struct {
	key    *ecdsa.PrivateKey
	server *httptest.Server
}

func newSignerFixture(t *testing.T) *signerFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &signerFixture{key: key}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "k1", Algorithm: string(jose.ES256), Use: "sig",
		}}}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *signerFixture) mint(t *testing.T, sub, iss, aud string, groups []string, exp time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: f.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), "k1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	claims := struct {
		jwt.Claims
		Groups []string `json:"groups"`
	}{
		Claims: jwt.Claims{
			Issuer:   iss,
			Subject:  sub,
			Audience: jwt.Audience{aud},
			Expiry:   jwt.NewNumericDate(exp),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Groups: groups,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifyAcceptsAGoodToken(t *testing.T) {
	f := newSignerFixture(t)
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", f.server.Client())

	tok := f.mint(t, "alice@example.com", "https://authd", "frame-ui", []string{"operators"}, time.Now().Add(10*time.Minute))
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.User != "alice@example.com" {
		t.Fatalf("User = %q", id.User)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "operators" {
		t.Fatalf("Groups = %v", id.Groups)
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newSignerFixture(t)
	other := newSignerFixture(t)
	v := NewJWKSVerifier(f.server.URL, "https://authd", "frame-ui", f.server.Client())

	cases := map[string]string{
		"expired":       f.mint(t, "a@b.c", "https://authd", "frame-ui", []string{"viewers"}, time.Now().Add(-time.Minute)),
		"wrong issuer":  f.mint(t, "a@b.c", "https://evil", "frame-ui", []string{"viewers"}, time.Now().Add(time.Hour)),
		"wrong aud":     f.mint(t, "a@b.c", "https://authd", "someone-else", []string{"viewers"}, time.Now().Add(time.Hour)),
		"foreign key":   other.mint(t, "a@b.c", "https://authd", "frame-ui", []string{"admins"}, time.Now().Add(time.Hour)),
		"not a jwt":     "hello",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tok); err == nil {
				t.Fatal("accepted a token it must reject")
			}
		})
	}
}

func TestVerifyCachesTheKeySet(t *testing.T) {
	f := newSignerFixture(t)
	var fetches int
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		f.server.Config.Handler.ServeHTTP(w, r)
	}))
	defer counting.Close()

	v := NewJWKSVerifier(counting.URL, "https://authd", "frame-ui", counting.Client())
	tok := f.mint(t, "a@b.c", "https://authd", "frame-ui", []string{"viewers"}, time.Now().Add(time.Hour))
	for range 3 {
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 1 {
		t.Fatalf("fetched the JWKS %d times, want 1", fetches)
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/uiproxy/... -run TestVerify`
Expected: FAIL — `undefined: NewJWKSVerifier`.

- [ ] **Step 3: Write the implementation**

```go
package uiproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// JWKSVerifier validates authd's ES256 tokens against the key set authd
// serves at /keys.
type JWKSVerifier struct {
	url, issuer, audience string
	hc                    *http.Client

	mu      sync.Mutex
	keys    *jose.JSONWebKeySet
	fetched time.Time
}

// jwksMinRefresh bounds how often an unknown `kid` can force a fetch, so a
// stream of bogus tokens cannot turn into a stream of requests to authd.
const jwksMinRefresh = time.Minute

func NewJWKSVerifier(jwksURL, issuer, audience string, hc *http.Client) *JWKSVerifier {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &JWKSVerifier{url: jwksURL, issuer: issuer, audience: audience, hc: hc}
}

func (v *JWKSVerifier) keySet(ctx context.Context, refresh bool) (*jose.JSONWebKeySet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys != nil && (!refresh || time.Since(v.fetched) < jwksMinRefresh) {
		return v.keys, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, err
	}
	res, err := v.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: %s", res.Status)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return nil, err
	}
	v.keys, v.fetched = &set, time.Now()
	return v.keys, nil
}

func (v *JWKSVerifier) Verify(ctx context.Context, raw string) (Identity, error) {
	// ES256 only: accepting whatever the header names is how alg-confusion
	// attacks get in.
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return Identity{}, err
	}
	set, err := v.keySet(ctx, false)
	if err != nil {
		return Identity{}, err
	}
	claims := struct {
		jwt.Claims
		Groups []string `json:"groups"`
	}{}
	err = tok.Claims(set, &claims)
	if err != nil {
		// An unknown kid is what a key rotation looks like; refetch once.
		if set, rerr := v.keySet(ctx, true); rerr == nil {
			err = tok.Claims(set, &claims)
		}
	}
	if err != nil {
		return Identity{}, err
	}
	if err := claims.Validate(jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: jwt.Audience{v.audience},
		Time:        time.Now(),
	}); err != nil {
		return Identity{}, err
	}
	if claims.Subject == "" {
		return Identity{}, fmt.Errorf("token has no subject")
	}
	return Identity{User: claims.Subject, Groups: claims.Groups}, nil
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/uiproxy/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/uiproxy/verifier.go internal/uiproxy/verifier_test.go
git commit -m "feat(uiproxy): verify authd's ES256 tokens against its JWKS"
```

---

### Task 3: The `FrameTask` kind

**Files:**
- Create: `api/frame/v1beta1/frametask_types.go`
- Create: `internal/controller/frame/frametask_v1beta1_schema_test.go`
- Modify: `charts/frame/templates/_helpers.tpl` (the `frame.tierRoleCRDs` list)
- Generated: `api/frame/v1beta1/zz_generated.deepcopy.go`, `config/crd/bases/frame.plume-labs.io_frametasks.yaml`, `config/rbac/frametask_{admin,editor,viewer}_role.yaml`, `charts/frame/files/crds/...`

**Interfaces:**
- Produces: `framev1beta1.FrameTask`, `FrameTaskSpec{User, Verb, Target, Action, Ref}`, `FrameTaskStatus{Phase, HTTPCode, Message, StartedAt, FinishedAt}`, `ObjectRef{Group, Resource, Namespace, Name}`, and the constants `TaskPhaseRunning/Succeeded/Failed`, `TaskVerbCreate/Update/Patch/Delete`.

- [ ] **Step 1: Write the failing schema test**

Follow the existing pattern in `internal/controller/frame/frameuser_v1beta1_schema_test.go` (same package, envtest client from `suite_test.go`).

```go
func TestFrameTaskSchema(t *testing.T) {
	// A task records an action that already happened, so the required
	// fields are the ones without which the record means nothing.
	cases := []struct {
		name    string
		spec    framev1beta1.FrameTaskSpec
		wantErr bool
	}{
		{"minimal", framev1beta1.FrameTaskSpec{
			User: "alice@example.com", Verb: "patch",
			Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
		}, false},
		{"no user", framev1beta1.FrameTaskSpec{
			Verb: "patch", Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
		}, true},
		{"unknown verb", framev1beta1.FrameTaskSpec{
			User: "alice@example.com", Verb: "get",
			Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &framev1beta1.FrameTask{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "task-", Namespace: "default"},
				Spec:       tc.spec,
			}
			err := k8sClient.Create(context.Background(), obj)
			if tc.wantErr && err == nil {
				t.Fatal("apiserver accepted a spec it should reject")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("apiserver rejected a valid spec: %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/controller/frame/... -run TestFrameTaskSchema`
Expected: FAIL — `undefined: framev1beta1.FrameTask`.

- [ ] **Step 3: Write the type**

```go
package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	TaskPhaseRunning   = "Running"
	TaskPhaseSucceeded = "Succeeded"
	TaskPhaseFailed    = "Failed"

	TaskVerbCreate = "create"
	TaskVerbUpdate = "update"
	TaskVerbPatch  = "patch"
	TaskVerbDelete = "delete"
)

// ObjectRef points at a Kubernetes object without importing its type.
type ObjectRef struct {
	// +optional
	Group string `json:"group,omitempty"`
	// Resource is the plural resource name as it appears in the request
	// path ("nodes", "framejobs"), not a Kind. The proxy reads paths, and a
	// path carries the resource; deriving a Kind from it would need a
	// RESTMapper and buy nothing the screen cannot render.
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// FrameTaskSpec is the record of one mutating request the UI made.
type FrameTaskSpec struct {
	// User is the Kubernetes username the request was impersonated as —
	// the FrameUser's email.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=254
	User string `json:"user"`

	// Verb is the Kubernetes verb the HTTP method mapped to. Reads are not
	// recorded, so there is no get/list/watch here.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=create;update;patch;delete
	Verb string `json:"verb"`

	// Target is the object the request acted on.
	// +kubebuilder:validation:Required
	Target ObjectRef `json:"target"`

	// Action is a human-readable label supplied by the UI through the
	// X-Frame-Action header ("cordon node w2"). Absent when the request
	// came from something other than the UI.
	// +optional
	// +kubebuilder:validation:MaxLength=200
	Action string `json:"action,omitempty"`

	// Ref points at an object that carries the action's progress — a
	// FrameJob, a TalosUpgrade, a Velero Backup. The Tasks screen reads
	// that object's own status rather than copying it here, which is what
	// lets this kind exist without a controller.
	// +optional
	Ref *ObjectRef `json:"ref,omitempty"`
}

// FrameTaskStatus is the outcome, written by the proxy once the apiserver
// has answered.
//
// This kind reports a phase rather than a Ready condition, against the
// convention the other eight kinds follow, and the reason is that it is not
// reconciled: there is no desired state, no controller, and nothing to
// converge. A record of a finished HTTP call has exactly one dimension of
// health, which is what phase expresses well and conditions do not.
type FrameTaskStatus struct {
	// +kubebuilder:validation:Enum=Running;Succeeded;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// HTTPCode is the apiserver's status code, 0 while running.
	// +optional
	HTTPCode int32 `json:"httpCode,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Code",type=integer,JSONPath=`.status.httpCode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameTask is the trace of one write made through the Frame UI.
type FrameTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FrameTaskSpec   `json:"spec,omitempty"`
	Status            FrameTaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameTask `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameTask{}, &FrameTaskList{}) }
```

`FrameTask` has no `v1alpha1`, so there is nothing to convert and no hub/spoke work — the same position NodeTuning is in.

- [ ] **Step 4: Generate, and add the kind to the tier list**

```bash
make manifests generate
```

Then add to `charts/frame/templates/_helpers.tpl`, inside `frame.tierRoleCRDs`, keeping the list alphabetical:

```yaml
- roleBase: frametask
  apiGroup: frame.plume-labs.io
  resource: frametasks
```

- [ ] **Step 5: Run the schema test and the parity check**

Run: `make test` then `make helm-parity helm-crds-check`
Expected: schema test PASS; parity and CRD-copy checks green. If `helm-crds-check` fails, run `make helm-sync-crds`.

- [ ] **Step 6: Commit**

```bash
git add api/frame/v1beta1/frametask_types.go api/frame/v1beta1/zz_generated.deepcopy.go \
        config/crd/bases config/rbac charts/frame internal/controller/frame/frametask_v1beta1_schema_test.go
git commit -m "feat(api): add FrameTask, the record of a write made through the UI"
```

---

### Task 4: The recorder — write a `FrameTask` per mutating request

**Files:**
- Create: `internal/uiproxy/recorder.go`
- Test: `internal/uiproxy/recorder_test.go`

**Interfaces:**
- Consumes: `Recorder`, `Identity` (Task 1); `framev1beta1.FrameTask` (Task 3).
- Produces:
  ```go
  func NewRecorder(c client.Client, namespace string, log *slog.Logger) *TaskRecorder
  func (t *TaskRecorder) Start(ctx context.Context, id Identity, r *http.Request) string
  func (t *TaskRecorder) Finish(ctx context.Context, name string, httpCode int)
  func (t *TaskRecorder) Purge(ctx context.Context, olderThan time.Duration) error
  func parsePath(p string) (ref framev1beta1.ObjectRef, resource string, ok bool)
  ```

- [ ] **Step 1: Write the failing tests**

```go
package uiproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func TestParsePath(t *testing.T) {
	cases := []struct {
		path string
		want framev1beta1.ObjectRef
		ok   bool
	}{
		{"/api/v1/nodes/w2", framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"}, true},
		{"/api/v1/namespaces/neura/pods/api-0/eviction",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0"}, true},
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs/j-1",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "j-1"}, true},
		{"/apis/apps/v1/namespaces/neura/deployments/api/scale",
			framev1beta1.ObjectRef{Group: "apps", Resource: "deployments", Namespace: "neura", Name: "api"}, true},
		// A create has no name in the path; the record still has to exist.
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "-"}, true},
		{"/healthz", framev1beta1.ObjectRef{}, false},
	}
	for _, tc := range cases {
		got, _, ok := parsePath(tc.path)
		if ok != tc.ok {
			t.Fatalf("%s: ok = %v", tc.path, ok)
		}
		if ok && got != tc.want {
			t.Fatalf("%s: got %+v, want %+v", tc.path, got, tc.want)
		}
	}
}

func newRecorderFixture(t *testing.T) (*TaskRecorder, client.Client) {
	t.Helper()
	s := scheme.Scheme
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1beta1.FrameTask{}).Build()
	return NewRecorder(c, "frame-system", nil), c
}

func TestStartRecordsTheUserAndAction(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", nil)
	req.Header.Set("X-Frame-Action", "cordon node w2")

	name := rec.Start(context.Background(), Identity{User: "alice@example.com", Groups: []string{"operators"}}, req)
	if name == "" {
		t.Fatal("Start recorded nothing")
	}
	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	if task.Spec.User != "alice@example.com" {
		t.Fatalf("User = %q", task.Spec.User)
	}
	if task.Spec.Verb != framev1beta1.TaskVerbPatch {
		t.Fatalf("Verb = %q", task.Spec.Verb)
	}
	if task.Spec.Action != "cordon node w2" {
		t.Fatalf("Action = %q", task.Spec.Action)
	}
	if task.Status.Phase != framev1beta1.TaskPhaseRunning {
		t.Fatalf("Phase = %q", task.Status.Phase)
	}
}

// A viewer's refused write is exactly the trace worth keeping.
func TestFinishRecordsFailure(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", nil)
	name := rec.Start(context.Background(), Identity{User: "bob@example.com", Groups: []string{"viewers"}}, req)

	rec.Finish(context.Background(), name, http.StatusForbidden)

	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	if task.Status.Phase != framev1beta1.TaskPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", task.Status.Phase)
	}
	if task.Status.HTTPCode != 403 {
		t.Fatalf("HTTPCode = %d", task.Status.HTTPCode)
	}
	if task.Status.FinishedAt == nil {
		t.Fatal("FinishedAt not set")
	}
}

func TestPurgeKeepsRunningAndRecentTasks(t *testing.T) {
	rec, c := newRecorderFixture(t)
	ctx := context.Background()
	old := metav1.NewTime(time.Now().Add(-8 * 24 * time.Hour))
	recent := metav1.NewTime(time.Now().Add(-time.Hour))

	mk := func(name, phase string, finished *metav1.Time) {
		task := &framev1beta1.FrameTask{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "frame-system"},
			Spec: framev1beta1.FrameTaskSpec{
				User: "a@b.c", Verb: framev1beta1.TaskVerbPatch,
				Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
			},
		}
		if err := c.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		task.Status = framev1beta1.FrameTaskStatus{Phase: phase, FinishedAt: finished}
		if err := c.Status().Update(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	mk("old-done", framev1beta1.TaskPhaseSucceeded, &old)
	mk("recent-done", framev1beta1.TaskPhaseSucceeded, &recent)
	mk("still-running", framev1beta1.TaskPhaseRunning, nil)

	if err := rec.Purge(ctx, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var list framev1beta1.FrameTaskList
	if err := c.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("kept %d tasks, want 2", len(list.Items))
	}
	for _, it := range list.Items {
		if it.Name == "old-done" {
			t.Fatal("purge kept a finished task older than the window")
		}
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/uiproxy/... -run 'TestParsePath|TestStart|TestFinish|TestPurge'`
Expected: FAIL — `undefined: NewRecorder`, `undefined: parsePath`.

- [ ] **Step 3: Write the implementation**

```go
package uiproxy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// TaskRecorder writes one FrameTask per mutating request.
//
// It writes with the pod ServiceAccount rather than through impersonation
// on purpose: a viewer must be able to leave the trace of their own 403,
// and a viewer cannot create FrameTasks.
type TaskRecorder struct {
	c   client.Client
	ns  string
	log *slog.Logger
}

func NewRecorder(c client.Client, namespace string, log *slog.Logger) *TaskRecorder {
	if log == nil {
		log = slog.Default()
	}
	return &TaskRecorder{c: c, ns: namespace, log: log}
}

func verbFor(method string) string {
	switch method {
	case http.MethodPost:
		return framev1beta1.TaskVerbCreate
	case http.MethodPut:
		return framev1beta1.TaskVerbUpdate
	case http.MethodPatch:
		return framev1beta1.TaskVerbPatch
	case http.MethodDelete:
		return framev1beta1.TaskVerbDelete
	}
	return ""
}

// parsePath turns a Kubernetes request path into a reference.
//
//	/api/v1/nodes/w2
//	/api/v1/namespaces/{ns}/{resource}[/{name}[/{subresource}]]
//	/apis/{group}/{version}/namespaces/{ns}/{resource}[/{name}[/{sub}]]
//
// A create has no name in its path; the record still needs one field to
// print, so it gets "-".
func parsePath(p string) (framev1beta1.ObjectRef, string, bool) {
	seg := strings.Split(strings.Trim(p, "/"), "/")
	var ref framev1beta1.ObjectRef
	var rest []string
	switch {
	case len(seg) >= 3 && seg[0] == "api":
		rest = seg[2:]
	case len(seg) >= 4 && seg[0] == "apis":
		ref.Group = seg[1]
		rest = seg[3:]
	default:
		return framev1beta1.ObjectRef{}, "", false
	}
	if len(rest) >= 2 && rest[0] == "namespaces" && len(rest) > 2 {
		ref.Namespace = rest[1]
		rest = rest[2:]
	}
	if len(rest) == 0 {
		return framev1beta1.ObjectRef{}, "", false
	}
	ref.Resource = rest[0]
	ref.Name = "-"
	if len(rest) >= 2 {
		ref.Name = rest[1]
	}
	return ref, rest[0], true
}

func (t *TaskRecorder) Start(ctx context.Context, id Identity, r *http.Request) string {
	verb := verbFor(r.Method)
	ref, _, ok := parsePath(r.URL.Path)
	if verb == "" || !ok {
		return ""
	}
	task := &framev1beta1.FrameTask{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "task-", Namespace: t.ns},
		Spec: framev1beta1.FrameTaskSpec{
			User:   id.User,
			Verb:   verb,
			Target: ref,
			Action: r.Header.Get("X-Frame-Action"),
		},
	}
	if err := t.c.Create(ctx, task); err != nil {
		// A missing trace must not cost the user their action.
		t.log.Error("could not record task", "err", err, "user", id.User, "path", r.URL.Path)
		return ""
	}
	task.Status = framev1beta1.FrameTaskStatus{
		Phase:     framev1beta1.TaskPhaseRunning,
		StartedAt: ptr(metav1.Now()),
	}
	if err := t.c.Status().Update(ctx, task); err != nil {
		t.log.Error("could not set task status", "err", err, "task", task.Name)
	}
	return task.Name
}

func (t *TaskRecorder) Finish(ctx context.Context, name string, httpCode int) {
	if name == "" {
		return
	}
	var task framev1beta1.FrameTask
	if err := t.c.Get(ctx, client.ObjectKey{Name: name, Namespace: t.ns}, &task); err != nil {
		t.log.Error("could not close task", "err", err, "task", name)
		return
	}
	phase := framev1beta1.TaskPhaseSucceeded
	if httpCode < 200 || httpCode >= 300 {
		phase = framev1beta1.TaskPhaseFailed
	}
	task.Status.Phase = phase
	task.Status.HTTPCode = int32(httpCode)
	task.Status.FinishedAt = ptr(metav1.Now())
	if err := t.c.Status().Update(ctx, &task); err != nil {
		t.log.Error("could not close task", "err", err, "task", name)
	}
}

// Purge deletes finished tasks older than the window. Running tasks are
// never deleted: one that never finished is a bug worth seeing.
func (t *TaskRecorder) Purge(ctx context.Context, olderThan time.Duration) error {
	var list framev1beta1.FrameTaskList
	if err := t.c.List(ctx, &list, client.InNamespace(t.ns)); err != nil {
		return err
	}
	cutoff := time.Now().Add(-olderThan)
	for i := range list.Items {
		f := list.Items[i].Status.FinishedAt
		if f == nil || f.Time.After(cutoff) {
			continue
		}
		if err := t.c.Delete(ctx, &list.Items[i]); err != nil {
			t.log.Error("could not purge task", "err", err, "task", list.Items[i].Name)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/uiproxy/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/uiproxy/recorder.go internal/uiproxy/recorder_test.go
git commit -m "feat(uiproxy): record a FrameTask for every write, including the refused ones"
```

---

### Task 5: The binary, the image, the Makefile

**Files:**
- Create: `cmd/uiproxy/main.go`
- Create: `Dockerfile.uiproxy`
- Modify: `Makefile` (targets `docker-build-uiproxy`, `docker-push-uiproxy`, and add it to `docker-images`)

**Interfaces:**
- Consumes: `uiproxy.New`, `uiproxy.NewJWKSVerifier`, `uiproxy.NewRecorder`.
- Produces: a binary configured entirely by environment: `LISTEN_ADDR` (default `127.0.0.1:8001`), `JWKS_URL`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `GROUP_PREFIX` (default `frame:`), `TASK_NAMESPACE` (default `frame-system`), `TASK_RETENTION` (default `168h`).

- [ ] **Step 1: Write the failing test**

```go
// cmd/uiproxy/main_test.go
package main

import "testing"

func TestConfigFromEnvRejectsAMissingIssuer(t *testing.T) {
	_, err := configFromEnv(func(k string) string {
		switch k {
		case "JWKS_URL":
			return "https://authd/keys"
		case "OIDC_CLIENT_ID":
			return "frame-ui"
		}
		return ""
	})
	if err == nil {
		t.Fatal("accepted a configuration with no issuer — every token would then be trusted by name only")
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	cfg, err := configFromEnv(func(k string) string {
		switch k {
		case "JWKS_URL":
			return "https://authd/keys"
		case "OIDC_ISSUER_URL":
			return "https://authd"
		case "OIDC_CLIENT_ID":
			return "frame-ui"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8001" {
		t.Fatalf("Listen = %q", cfg.Listen)
	}
	if cfg.GroupPrefix != "frame:" {
		t.Fatalf("GroupPrefix = %q", cfg.GroupPrefix)
	}
	if cfg.TaskNamespace != "frame-system" {
		t.Fatalf("TaskNamespace = %q", cfg.TaskNamespace)
	}
	if cfg.Retention.Hours() != 168 {
		t.Fatalf("Retention = %v", cfg.Retention)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/uiproxy/...`
Expected: FAIL — `undefined: configFromEnv`.

- [ ] **Step 3: Write `cmd/uiproxy/main.go`**

```go
// Command uiproxy authenticates the Frame UI's requests to the Kubernetes
// apiserver: it validates authd's token, impersonates the person it names,
// and records what they did. It replaces the `kubectl proxy` sidecar, which
// authenticated every request as the pod ServiceAccount.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/uiproxy"
)

type config struct {
	Listen        string
	JWKSURL       string
	Issuer        string
	ClientID      string
	GroupPrefix   string
	TaskNamespace string
	Retention     time.Duration
}

func configFromEnv(get func(string) string) (config, error) {
	c := config{
		Listen:        or(get("LISTEN_ADDR"), "127.0.0.1:8001"),
		JWKSURL:       get("JWKS_URL"),
		Issuer:        get("OIDC_ISSUER_URL"),
		ClientID:      get("OIDC_CLIENT_ID"),
		GroupPrefix:   or(get("GROUP_PREFIX"), "frame:"),
		TaskNamespace: or(get("TASK_NAMESPACE"), "frame-system"),
		Retention:     7 * 24 * time.Hour,
	}
	if v := get("TASK_RETENTION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return config{}, fmt.Errorf("TASK_RETENTION: %w", err)
		}
		c.Retention = d
	}
	for k, v := range map[string]string{"JWKS_URL": c.JWKSURL, "OIDC_ISSUER_URL": c.Issuer, "OIDC_CLIENT_ID": c.ClientID} {
		if v == "" {
			return config{}, fmt.Errorf("%s is required", k)
		}
	}
	return c, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "uiproxy:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	// The apiserver's own address and the SA credential, both from the
	// in-cluster config — the same pair kubectl proxy used.
	upstream, err := url.Parse(restCfg.Host)
	if err != nil {
		return err
	}
	transport, err := rest.TransportFor(restCfg)
	if err != nil {
		return err
	}
	if err := framev1beta1.AddToScheme(scheme.Scheme); err != nil {
		return err
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return err
	}
	rec := uiproxy.NewRecorder(c, cfg.TaskNamespace, log)

	p, err := uiproxy.New(uiproxy.Options{
		Verifier:    uiproxy.NewJWKSVerifier(cfg.JWKSURL, cfg.Issuer, cfg.ClientID, nil),
		Recorder:    rec,
		Upstream:    upstream,
		Transport:   transport,
		GroupPrefix: cfg.GroupPrefix,
		Log:         log,
	})
	if err != nil {
		return err
	}

	ctx := ctrl.SetupSignalHandler()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if err := rec.Purge(ctx, cfg.Retention); err != nil {
				log.Error("purge failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	srv := &http.Server{Addr: cfg.Listen, Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", cfg.Listen, "issuer", cfg.Issuer)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Write `Dockerfile.uiproxy`**

```dockerfile
# Dockerfile for uiproxy, the authenticating reverse proxy between the UI and
# the apiserver. Its own image for the same reason authd and the controller
# have theirs: this container holds an impersonation grant, and it should be
# able to reach nothing else.
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -a -o uiproxy cmd/uiproxy/main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/uiproxy .
USER 65532:65532

ENTRYPOINT ["/uiproxy"]
```

- [ ] **Step 5: Add the Makefile targets**

Next to `IMG_AUTHD`, add `IMG_UIPROXY ?= frame-uiproxy:latest`, then next to the authd pair:

```makefile
.PHONY: docker-build-uiproxy
docker-build-uiproxy: ## Build the uiproxy Docker image
	$(CONTAINER_TOOL) build -f Dockerfile.uiproxy -t ${IMG_UIPROXY} .

.PHONY: docker-push-uiproxy
docker-push-uiproxy: ## Push the uiproxy Docker image
	$(CONTAINER_TOOL) push ${IMG_UIPROXY}
```

Add `docker-build-uiproxy` to the `docker-images` target's prerequisites so `make sbom` and `make scan` cover the new image — an image that carries an impersonation grant and is not scanned is the wrong one to leave out.

- [ ] **Step 6: Run the tests and build the image**

Run: `go test ./cmd/uiproxy/... && make docker-build-uiproxy`
Expected: tests PASS, image builds.

- [ ] **Step 7: Commit**

```bash
git add cmd/uiproxy Dockerfile.uiproxy Makefile
git commit -m "feat(uiproxy): ship the proxy as its own binary and image"
```

---

### Task 6: Deploy it — sidecar swap and RBAC

**Files:**
- Modify: `deploy/kubernetes/base/deployment.yaml:90-119` (replace the `kube-proxy-api` container)
- Modify: `deploy/kubernetes/base/rbac.yaml:219-243` (remove two ClusterRoleBindings, add the impersonation Role)
- Create: `deploy/kubernetes/base/rbac-tier-bindings.yaml`
- Modify: `deploy/kubernetes/base/kustomization.yaml`
- Modify: `deploy/kubernetes/authd/deployment.yaml` (real `RP_ID` / `RP_ORIGIN`)
- Modify: `charts/frame/templates/rbac-tier-roles.yaml` + `config/rbac/*_role.yaml` (add the aggregation label)

**Interfaces:**
- Consumes: the `frame-uiproxy` image (Task 5), the `frametasks` tiers (Task 3).
- Produces: three aggregated ClusterRoles `frame-viewer`, `frame-editor`, `frame-admin`, bound to `frame:viewers` / `frame:operators` / `frame:admins`.

- [ ] **Step 1: Replace the sidecar**

```yaml
        # Authenticates the UI's requests against authd and impersonates the
        # person they name. Replaces `kubectl proxy`, which authenticated
        # every request — reads and writes alike — as the pod ServiceAccount.
        - name: uiproxy
          image: frame-uiproxy:latest
          env:
            - name: JWKS_URL
              value: https://cluster-control-auth.cluster-control.svc/keys
            - name: OIDC_ISSUER_URL
              value: https://cluster-control-auth.cluster-control.svc
            - name: OIDC_CLIENT_ID
              value: frame-ui
            - name: TASK_NAMESPACE
              value: frame-system
          ports:
            - name: kube-api
              containerPort: 8001
              protocol: TCP
          resources:
            requests: {memory: "32Mi", cpu: "10m"}
            limits: {memory: "128Mi", cpu: "200m"}
          # No readiness probe: it binds loopback only, so the kubelet cannot
          # reach it. nginx is the readiness gate.
          securityContext:
            runAsNonRoot: true
            runAsUser: 1000
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
```

`nginx.conf` needs no change: the port and the loopback address are the same.

- [ ] **Step 2: Rewrite the ServiceAccount's RBAC**

Delete the `cluster-control-viewer` and `cluster-control-operator` ClusterRoleBindings (`rbac.yaml:219-243`) — the ClusterRoles themselves stay, unbound, until lot 1 confirms nothing else uses them. Add:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-control-impersonator
rules:
  # Impersonating a user cannot be bounded by resourceNames — email
  # addresses are not enumerable. The rule that keeps this safe lives in
  # docs/deployment.md: no RBAC binding may ever name an individual user.
  # Everything is granted to the three frame: groups below.
  - apiGroups: [""]
    resources: [users]
    verbs: [impersonate]
  - apiGroups: [""]
    resources: [groups]
    resourceNames: ["frame:admins", "frame:operators", "frame:viewers"]
    verbs: [impersonate]
  # Deliberately absent: serviceaccounts, uids, userextras.
  - apiGroups: [frame.plume-labs.io]
    resources: [frametasks]
    verbs: [create, get, list, delete]
  - apiGroups: [frame.plume-labs.io]
    resources: [frametasks/status]
    verbs: [update, patch]
```

plus the matching ClusterRoleBinding to `cluster-control-ui`.

- [ ] **Step 3: Aggregate the tiers and bind the groups**

Add `labels: {rbac.frame.plume-labs.io/tier: viewer|editor|admin}` to each generated tier role — in `charts/frame/templates/rbac-tier-roles.yaml` (one line in the label block, keyed off the loop's role) and in the 24 `config/rbac/*_role.yaml` copies, which `make helm-parity` compares. Then `deploy/kubernetes/base/rbac-tier-bindings.yaml`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: frame-viewer
aggregationRule:
  clusterRoleSelectors:
    - matchLabels:
        rbac.frame.plume-labs.io/tier: viewer
rules: []   # filled by the controller-manager
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: frame-viewers
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: frame-viewer}
subjects:
  - kind: Group
    name: "frame:viewers"
    apiGroup: rbac.authorization.k8s.io
```

Repeat for `editor`/`frame:operators` and `admin`/`frame:admins`. Note the asymmetry once more: the FrameUser role is `operator`, the tier is `editor`.

Frame users also need the read access the old `cluster-control-viewer` ClusterRole granted on core objects (nodes, pods, events, deployments, Ceph, Volcano, Velero…), which the per-kind Frame tiers do not cover. Add `cluster-control-viewer` as a fourth aggregated member by labelling it `rbac.frame.plume-labs.io/tier: viewer`, so every tier inherits the read surface the screens need.

- [ ] **Step 4: Point authd at the real hostname**

In `deploy/kubernetes/authd/deployment.yaml`, replace the Stage-1 placeholders `RP_ID: frame.local` / `RP_ORIGIN: https://frame.local` with the UI's real hostname, and confirm `OIDC_ISSUER_URL` / `OIDC_CLIENT_ID` match what the proxy validates (`https://cluster-control-auth.cluster-control.svc`, `frame-ui`).

- [ ] **Step 5: Render and check**

Run: `kubectl kustomize deploy/kubernetes/base > /tmp/rendered.yaml && grep -c "kubectl" /tmp/rendered.yaml`
Expected: the render succeeds and the `rancher/kubectl` image no longer appears.
Run: `make helm-parity`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add deploy/kubernetes charts/frame config/rbac
git commit -m "feat(deploy): impersonate through the proxy and bind the tiers to real groups"
```

---

### Task 7: Proof against a real apiserver

**Files:**
- Create: `internal/uiproxy/impersonation_envtest_test.go`

**Interfaces:**
- Consumes: everything above.

This is the control that discriminates. A proxy that impersonated nothing would answer 200 to both callers, because the envtest client is an admin; a test that only asserts the operator's 200 would pass against that broken version.

- [ ] **Step 1: Write the failing test**

```go
//go:build envtest

package uiproxy_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/uiproxy"
)

var (
	testEnv   *envtest.Environment
	restCfg   *rest.Config
	k8sClient client.Client
)

// stubVerifier stands in for the JWKS verifier: this spec proves what the
// apiserver does with an impersonated identity, not how the token was read.
type stubVerifier struct{ id uiproxy.Identity }

func (s stubVerifier) Verify(context.Context, string) (uiproxy.Identity, error) { return s.id, nil }

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "bin", "crd-render")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir == "" {
		// Same fallback the controller suites use: bin/k8s/<version>.
		matches, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "*"))
		if len(matches) > 0 {
			testEnv.BinaryAssetsDirectory = matches[0]
		}
	}
	var err error
	restCfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest start:", err)
		os.Exit(1)
	}
	if err := framev1beta1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	k8sClient, err = client.New(restCfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

// bindGroups gives frame:operators the right to patch nodes and frame:viewers
// only the right to read them — the shape the real tier bindings have.
func bindGroups(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	roles := []struct {
		name  string
		verbs []string
		group string
	}{
		{"test-node-patcher", []string{"get", "list", "patch"}, "frame:operators"},
		{"test-node-reader", []string{"get", "list"}, "frame:viewers"},
	}
	for _, r := range roles {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: r.name},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: r.verbs},
				{APIGroups: []string{"frame.plume-labs.io"}, Resources: []string{"frametasks"}, Verbs: []string{"get", "list"}},
			},
		}
		if err := k8sClient.Create(ctx, cr); err != nil && !apierrorsIsAlreadyExists(err) {
			t.Fatal(err)
		}
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: r.name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: r.name},
			Subjects:   []rbacv1.Subject{{Kind: "Group", Name: r.group, APIGroup: rbacv1.GroupName}},
		}
		if err := k8sClient.Create(ctx, crb); err != nil && !apierrorsIsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
}

func proxyFor(t *testing.T, id uiproxy.Identity) *uiproxy.Proxy {
	t.Helper()
	u, err := url.Parse(restCfg.Host)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := rest.TransportFor(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := uiproxy.New(uiproxy.Options{
		Verifier:    stubVerifier{id: id},
		Recorder:    uiproxy.NewRecorder(k8sClient, "default", nil),
		Upstream:    u,
		Transport:   tr,
		GroupPrefix: "frame:",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The control that discriminates. A proxy that forwarded without
// impersonating would answer 200 to both callers — envtest's config is an
// admin — so asserting only the operator's 200 would pass against a proxy
// that does nothing at all.
func TestOperatorMayPatchWhereViewerMayNot(t *testing.T) {
	bindGroups(t)
	// A node to act on: envtest runs no kubelet, so create one.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w2"}}
	if err := k8sClient.Create(context.Background(), node); err != nil && !apierrorsIsAlreadyExists(err) {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		id       uiproxy.Identity
		wantCode int
	}{
		{"operator", uiproxy.Identity{User: "alice@example.com", Groups: []string{"operators"}}, http.StatusOK},
		{"viewer", uiproxy.Identity{User: "bob@example.com", Groups: []string{"viewers"}}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := proxyFor(t, tc.id)
			body := []byte(`{"spec":{"unschedulable":true}}`)
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer stub")
			req.Header.Set("Content-Type", "application/merge-patch+json")
			req.Header.Set("X-Frame-Action", "cordon node w2")
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d — body: %s", rec.Code, tc.wantCode, rec.Body.String())
			}

			var tasks framev1beta1.FrameTaskList
			if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
				t.Fatal(err)
			}
			var found *framev1beta1.FrameTask
			for i := range tasks.Items {
				if tasks.Items[i].Spec.User == tc.id.User {
					found = &tasks.Items[i]
				}
			}
			if found == nil {
				t.Fatalf("no FrameTask recorded for %s", tc.id.User)
			}
			if int(found.Status.HTTPCode) != tc.wantCode {
				t.Fatalf("task recorded code %d, want %d", found.Status.HTTPCode, tc.wantCode)
			}
			if found.Spec.Action != "cordon node w2" {
				t.Fatalf("task action = %q", found.Spec.Action)
			}
		})
	}
}
```

Add the two imports the snippet leans on and does not list: `corev1 "k8s.io/api/core/v1"` and a local `apierrorsIsAlreadyExists` wrapping `apierrors.IsAlreadyExists` from `k8s.io/apimachinery/pkg/api/errors`.

- [ ] **Step 2: Run it and watch it fail**

Run: `make crd-render setup-envtest && go test -tags envtest ./internal/uiproxy/...`
Expected: FAIL — either a compile error until the imports are added, or, if the proxy skipped impersonation, both subtests returning 200 and the viewer case failing on `got 200, want 403`.

- [ ] **Step 3: Make it pass**

Nothing new should be needed: Tasks 1-4 implement everything this exercises. If the viewer's request succeeds, the proxy is not impersonating — check that `Impersonate-User` survives `stripImpersonation` (it is set *after* the strip, and a reordering that sets it first silently disables the whole component).

- [ ] **Step 4: Run the whole suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/uiproxy/impersonation_envtest_test.go
git commit -m "test(uiproxy): prove a viewer is refused where an operator is allowed"
```

---

### Task 8: The UI's session

**Files:**
- Create: `src/lib/auth.ts`
- Create: `src/lib/auth.test.ts`
- Modify: `src/lib/frame-sdk.ts:709-728` (`k8sFetchUncached`, retry once on 401)

**Interfaces:**
- Produces:
  ```ts
  export interface Session { token: string; expiresAt: number }
  export async function loginWithPassword(email: string, password: string): Promise<void>
  export async function currentSession(): Promise<Session | undefined>  // POST /auth/token; undefined on 401
  export async function ensureToken(now?: number): Promise<string | undefined> // refreshes under 2 min left
  export async function logout(): Promise<void>
  export function __resetForTests(): void
  ```
  `ensureToken` and `currentSession` both publish the token on `window.__FRAME_TOKEN__`, which `bearerToken()` in `frame-sdk.ts` and `authHeaders()` in `k8s-watch.ts` already read.

- [ ] **Step 1: Write the failing tests**

```ts
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { __resetForTests, currentSession, ensureToken, logout } from '@/lib/auth'

const REFRESH_MARGIN_MS = 120_000

function mockToken(expiresIn: number) {
  return vi.fn(async () => new Response(JSON.stringify({ id_token: 't-' + expiresIn, expires_in: expiresIn }), { status: 200 }))
}

beforeEach(() => { __resetForTests() })

describe('session', () => {
  it('publishes the token where the SDK reads it', async () => {
    vi.stubGlobal('fetch', mockToken(900))
    await currentSession()
    expect((globalThis as Record<string, unknown>).__FRAME_TOKEN__).toBe('t-900')
  })

  it('returns undefined when the session cookie is gone', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('unauthorized', { status: 401 })))
    expect(await currentSession()).toBeUndefined()
  })

  it('does not refresh a token with time left', async () => {
    const f = mockToken(900)
    vi.stubGlobal('fetch', f)
    await currentSession()
    await ensureToken(Date.now() + 60_000)
    expect(f).toHaveBeenCalledTimes(1)
  })

  it('refreshes inside the margin', async () => {
    const f = mockToken(900)
    vi.stubGlobal('fetch', f)
    await currentSession()
    await ensureToken(Date.now() + 900_000 - REFRESH_MARGIN_MS + 1_000)
    expect(f).toHaveBeenCalledTimes(2)
  })

  it('clears the token on logout', async () => {
    vi.stubGlobal('fetch', mockToken(900))
    await currentSession()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))
    await logout()
    expect((globalThis as Record<string, unknown>).__FRAME_TOKEN__).toBeUndefined()
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

Run: `npx vitest run src/lib/auth.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `src/lib/auth.ts`**

Keep the session in a module-level variable and mirror the token onto `window.__FRAME_TOKEN__` — that global is the existing contract with `frame-sdk.ts` and `k8s-watch.ts`, and changing it would touch every screen. The token is never written to `localStorage`: authd deliberately returns it in the body so it lives in memory only.

Endpoints: `POST /auth/login/password` (email, password), `POST /auth/token` (session cookie → `{id_token, expires_in}`), `POST /auth/logout`.

- [ ] **Step 4: Retry once on 401 in `k8sFetchUncached`**

A 401 means the token expired and the request did not execute, so replaying it is safe even for a write. Refresh once, retry once, then give up.

- [ ] **Step 5: Run the tests**

Run: `npx vitest run src/lib/`
Expected: PASS, nothing else broken.

- [ ] **Step 6: Commit**

```bash
git add src/lib/auth.ts src/lib/auth.test.ts src/lib/frame-sdk.ts
git commit -m "feat(ui): hold a real session and keep the token fresh"
```

---

### Task 9: The login gate

**Files:**
- Create: `src/components/LoginView.tsx`
- Modify: `src/App.tsx` (gate the app on a session; a sign-out item in the sidebar footer)

- [ ] **Step 1: Write `LoginView`**

Email + password form calling `loginWithPassword`, then `currentSession`. Errors from authd are shown verbatim rather than translated: "unauthorized" is the only one it returns and inventing friendlier copy would only hide which of the two fields was wrong — which is the point.

WebAuthn login exists in authd (`/auth/login/begin`, `/auth/login/finish`) and is **not** wired here. Password login is the shortest path to a working gate; the passkey button is a follow-up in the same file, listed in the roadmap rather than smuggled into this lot.

- [ ] **Step 2: Gate the app**

In `App.tsx`, run `currentSession()` once on mount. While it is pending render the existing loading shell; when it resolves `undefined` render `<LoginView onSignedIn={...} />`; otherwise render the app as today. Start a `setInterval` calling `ensureToken()` every 5 minutes and clear it on unmount.

- [ ] **Step 3: Verify by hand**

Run: `npm run build && npm run dev`
Expected: without a session the login screen shows; the app does not render and no `/api/` request is made.

- [ ] **Step 4: Commit**

```bash
git add src/components/LoginView.tsx src/App.tsx
git commit -m "feat(ui): require a session before the console loads"
```

---

### Task 10: The Tasks screen

**Files:**
- Modify: `src/lib/frame-sdk.ts` (a `tasks` namespace on `FrameClient`)
- Create: `src/components/TasksView.tsx`
- Modify: `src/App.tsx` (`NAV` entry and the `renderTab` case)
- Modify: `src/lib/frame-sdk.test.ts` (mapper test)

**Interfaces:**
- Consumes: `frameListPath`, `k8sFetch` (module-private), the `FrameTask` CRD.
- Produces:
  ```ts
  export interface TaskRecord {
    name: string; user: string; verb: string; action: string
    target: string          // "kind/name" or "ns/kind/name"
    phase: 'Running' | 'Succeeded' | 'Failed'
    httpCode?: number; startedAt?: string; finishedAt?: string
    ref?: { group: string; resource: string; namespace: string; name: string }
  }
  // on FrameClient:
  tasks: { list(limit?: number): Promise<TaskRecord[]> }
  ```

- [ ] **Step 1: Write the failing mapper test**

Add `crToTask` to the existing `__testing` export at the bottom of `frame-sdk.ts`, and in `frame-sdk.test.ts`:

```ts
const { crToTask } = __testing

it('maps a refused write to a failed task', () => {
  const t = crToTask({
    metadata: { name: 'task-abc' },
    spec: { user: 'bob@example.com', verb: 'patch', action: 'cordon node w2',
            target: { resource: 'nodes', name: 'w2' } },
    status: { phase: 'Failed', httpCode: 403, startedAt: '2026-09-08T10:00:00Z' },
  })
  expect(t.phase).toBe('Failed')
  expect(t.httpCode).toBe(403)
  expect(t.target).toBe('nodes/w2')
})

it('treats a task with no status as running', () => {
  const t = crToTask({
    metadata: { name: 'task-def' },
    spec: { user: 'a@b.c', verb: 'create', target: { resource: 'framejobs', namespace: 'frame-system', name: '-' } },
  })
  expect(t.phase).toBe('Running')
  expect(t.target).toBe('frame-system/framejobs/-')
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `npx vitest run src/lib/frame-sdk.test.ts`
Expected: FAIL — `__testing.crToTask` is undefined.

- [ ] **Step 3: Implement `crToTask` and the `tasks` namespace**

```ts
interface FrameTaskCR {
  metadata: { name: string; creationTimestamp?: string }
  spec: {
    user: string; verb: string; action?: string
    target: { group?: string; resource: string; namespace?: string; name: string }
    ref?: { group?: string; resource: string; namespace?: string; name: string }
  }
  status?: { phase?: string; httpCode?: number; startedAt?: string; finishedAt?: string }
}

function refLabel(r: { resource: string; namespace?: string; name: string }): string {
  return r.namespace ? `${r.namespace}/${r.resource}/${r.name}` : `${r.resource}/${r.name}`
}

function crToTask(cr: FrameTaskCR): TaskRecord {
  return {
    name: cr.metadata.name,
    user: cr.spec.user,
    verb: cr.spec.verb,
    action: cr.spec.action ?? `${cr.spec.verb} ${refLabel(cr.spec.target)}`,
    target: refLabel(cr.spec.target),
    // A task with no status is one the proxy created and has not closed:
    // in flight, or the proxy died mid-request. Both read as Running.
    phase: (cr.status?.phase as TaskRecord['phase']) ?? 'Running',
    httpCode: cr.status?.httpCode,
    startedAt: cr.status?.startedAt ?? cr.metadata.creationTimestamp,
    finishedAt: cr.status?.finishedAt,
    ref: cr.spec.ref && {
      group: cr.spec.ref.group ?? '', resource: cr.spec.ref.resource,
      namespace: cr.spec.ref.namespace ?? '', name: cr.spec.ref.name,
    },
  }
}
```

The `tasks` namespace on `FrameClient`:

```ts
  tasks = {
    list: async (limit = 200): Promise<TaskRecord[]> => {
      const res = await k8sFetch<{ items: FrameTaskCR[] }>(`${frameListPath('frametasks')}?limit=${limit}`)
      return res.items
        .map(crToTask)
        .sort((a, b) => (b.startedAt ?? '').localeCompare(a.startedAt ?? ''))
    },
  }
```

- [ ] **Step 4: Write `TasksView`**

Model it on `ClusterEventsView.tsx`: `useLiveResource(() => frame.tasks.list(200), [], [frameListPath('frametasks')])`, `LiveStates` for the three phases, a table of user / action / target / outcome / age, and a filter input over user and action. When `ref` is set, render a link that navigates to the referenced object's screen via `useNavigation()`.

- [ ] **Step 5: Add it to the navigation**

`NAV` entry under the same group as Events, `id: 'tasks'`, label "Tasks"; matching `case 'tasks': return <TasksView />` in `renderTab`; lazy import alongside the others.

- [ ] **Step 6: Verify**

Run: `npx vitest run && npm run build`
Expected: PASS and a clean build.

- [ ] **Step 7: Commit**

```bash
git add src/lib/frame-sdk.ts src/lib/frame-sdk.test.ts src/components/TasksView.tsx src/App.tsx
git commit -m "feat(ui): show who did what, and how it ended"
```

---

### Task 11: Documentation and the rollout order

**Files:**
- Modify: `docs/deployment.md` (the RBAC section around line 239)
- Modify: `docs/architecture.md`, `docs/roadmap.md`, `docs/crd-reference.md`
- Modify: `deploy/docker/nginx.conf` (the comment on `/api/` is now false)

- [ ] **Step 1: Rewrite the RBAC section of `deployment.md`**

It currently says the tiers "are not currently enforced against any human". That is the sentence this lot exists to delete. It must now say: tiers are bound to `frame:viewers` / `frame:operators` / `frame:admins`; identity comes from authd through the uiproxy; **and no RBAC binding may ever name an individual user**, with the reason (impersonation of users cannot be restricted by `resourceNames`).

- [ ] **Step 2: Write the rollout order**

In the same document, in this order, with the warning that inverting the first two locks everyone out and the only way back is the node's kubeconfig:

1. Bootstrap the first admin through authd's one-shot `/auth/bootstrap` Secret, **while the old anonymous path still works**.
2. Apply the tier bindings.
3. Apply the deployment (sidecar swap) and the SA's RBAC change together.
4. Point authd's `RP_ID` / `RP_ORIGIN` at the real hostname.

Rollback: re-apply the previous kustomization; accounts created in step 1 survive.

- [ ] **Step 3: Fix the nginx comment**

`deploy/docker/nginx.conf:38-46` describes the SA as carrying the write actions. Replace with what is now true: the path reaches `uiproxy`, which impersonates the person named by the token, and the browser holds a 15-minute token that the apiserver would not accept on its own.

- [ ] **Step 4: Update `roadmap.md` and `crd-reference.md`**

Mark authd Stages 2 and 3 as done by a different route than the one they described (impersonation, not `--oidc-issuer-url`), and record native OIDC as the follow-up that belongs in the G9's `config.yaml`. Add `FrameTask` to the CRD reference — including why it reports a phase rather than a `Ready` condition.

- [ ] **Step 5: Commit**

```bash
git add docs deploy/docker/nginx.conf
git commit -m "docs: per-user identity is enforced, and here is the one rule that keeps it safe"
```

---

## What this plan does not do

- **0b, the apiserver audit log.** Needs an apiserver restart; belongs in the G9's `/etc/rancher/k3s/config.yaml`.
- **WebAuthn login in the UI.** authd implements it; the login screen offers password only. Follow-up.
- **Exec, logs, power, storage.** Lots 1-5. This lot changes who may use what already exists.
- **`FrameNode`/`FrameJob` screens changing behaviour.** They keep working; their writes are now attributed and can now be refused.
