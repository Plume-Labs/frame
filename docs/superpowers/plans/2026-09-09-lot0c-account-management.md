# Lot 0c — inviting a second human: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A second person can be given an account, enrol a passkey on it, and be cut off again — all without `kubectl`, and with one function deciding whether any identity may be issued at all.

**Architecture:** `FrameUser` gains `spec.state: enabled|disabled` on both served versions. `internal/authd` gains one unexported predicate, `requireIssuable`, and every path that turns a credential or a cookie into an identity calls it: `/auth/token` (through `sessionUser`), password login, passkey login, and invitation acceptance. Four new routes — `POST /auth/invite`, `POST /auth/invite/accept`, `GET /auth/credentials`, `DELETE /auth/credentials/{id}` — sit on the existing `Store`, which already has every method they need. The invitation is a sealed `PurposeInvite` token, refused the moment the account holds a credential: no table, no revocation list. The console gains an admin-only Accounts screen and an acceptance page, with every testable transformation in `src/lib`.

**Tech Stack:** Go 1.26.1, `net/http` (Go 1.22 method+wildcard mux patterns), go-webauthn, controller-runtime v0.23.x, envtest + Ginkgo, kubebuilder v4 multigroup layout, React 19 + Vite + vitest (node environment), kustomize, Helm.

**Spec:** docs/superpowers/specs/2026-09-09-lot0c-account-management-design.md

## Global Constraints

- Module path is `github.com/rmocq/frame`. Go 1.26.1. This lot adds **no dependency**; if that changes, the new entry needs an upper bound in `go.mod`.
- The API group is `frame.plume-labs.io`. `v1beta1` is the storage version and the conversion hub; `v1alpha1` is served and deprecated. Both are patched for the conversion webhook (`config/crd/patches/webhook_in_frame_frameusers.yaml`).
- `TestHubRoundTripIsLossless` in `api/frame/v1alpha1/conversion_test.go` fuzzes **v1beta1 → v1alpha1 → v1beta1** and demands exact equality. **A field added to `v1beta1` alone fails it.** Every new `v1beta1` field must also exist on `v1alpha1` and be carried in both directions of `api/frame/v1alpha1/conversion.go`.
- `spec.state` values are exactly `enabled` and `disabled` (`framev1beta1.StateEnabled` / `StateDisabled`), default `enabled`.
- FrameUser roles are `admin`, `operator`, `viewer`. `internal/authd.GroupForRole` returns them **unprefixed** (`admins`/`operators`/`viewers`); the `frame:` prefix is applied by `frame-uiproxy`. A token's `groups` claim therefore reads `["admins"]`, not `["frame:admins"]` — the browser must match the unprefixed form, the admission webhook the prefixed one.
- authd's ServiceAccount (`deploy/kubernetes/authd/rbac.yaml`) holds `get,list,watch,create` on `frameusers` and `get,patch,update` on `frameusers/status`, and **no `update` on the main resource**. Everything this lot adds to authd fits inside that. It is not an admin, and the admission webhook refuses a `spec.role: admin` create from a non-admin — which is why `/auth/invite` cannot mint an admin.
- Recording nothing may fail a request that already succeeded: any best-effort side effect is logged, never returned.
- `config/rbac/*_role.yaml` and `charts/frame/templates/rbac-tier-roles.yaml` are two hand-maintained copies of the same rules. **`make helm-parity` has a KNOWN pre-existing failure** on a cpu-request drift (`10m` vs `100m`) in files this branch does not touch. Do not chase it; confirm the diff it reports is only that.
- The CRD lives in both `config/crd/bases/` and `charts/frame/files/crds/`. `make manifests` regenerates the first and calls `make helm-sync-crds` to copy to the second; `make helm-crds-check` fails on drift.
- The envtest suite in `internal/controller/frame` serves **`bin/crd-render`**, produced by `make crd-render` from `config/crd` — so a schema spec there tests the CRD that actually ships, not a hand-built one.
- `internal/controller/frame` bootstraps envtest through a Ginkgo suite (`TestControllers`) and Go orders test files alphabetically: **a plain `func TestX` in that package panics on a nil client.** Schema assertions go inside a `Describe`/`DescribeTable`.
- `+kubebuilder:validation:Required` does not reject an empty string from a typed client. Where empty is invalid, use `MinLength=1` — or, as here, an `Enum` that does not contain `""`.
- A typed Go client serialises a zero-value struct, so a "field absent" schema case must be written with `unstructured.Unstructured`.
- Vitest runs `environment: 'node'` with `include: ['src/**/*.test.ts']` — **`.tsx` specs never execute.** Anything needing a test lives in `src/lib/`. No component test is planned; the logic is moved out of the components instead.
- The frontend publishes the session on `globalThis`, not `window` (vitest is node). New code follows suit and never touches `window`.
- Full test run: `make test` (it chains `manifests generate fmt vet crd-render setup-envtest`). Go-only packages: `go test ./internal/authd/...`. Frontend: `npx vitest run` and `npm run build`.

---

### Task 1: `spec.state` on the kind, on both versions, in the shipped CRD

**Files:**
- Modify: `api/frame/v1beta1/frameuser_types.go` (const block at lines 23-30; `FrameUserSpec`, after `PasswordAuth` at line 82)
- Modify: `api/frame/v1alpha1/frameuser_types.go` (const block at lines 23-30; `FrameUserSpec`, after `PasswordAuth`)
- Modify: `api/frame/v1alpha1/conversion.go` (`FrameUser.ConvertTo` and `FrameUser.ConvertFrom`)
- Test: `api/frame/v1alpha1/conversion_test.go` (`TestFuzzCorpusReachesTheInterestingBranches`)
- Test: `internal/controller/frame/frameuser_v1beta1_schema_test.go` (three new `It`s inside the existing `Describe`)
- Generated: `config/crd/bases/frame.plume-labs.io_frameusers.yaml`, `charts/frame/files/crds/frame.plume-labs.io_frameusers.yaml`, `api/frame/*/zz_generated.deepcopy.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  // api/frame/v1beta1 and api/frame/v1alpha1, identically:
  const (
      StateEnabled  = "enabled"
      StateDisabled = "disabled"
  )
  // FrameUserSpec.State string `json:"state,omitempty"`
  ```

**The shipped manifest, not just the test fixture.** Nothing here builds its own CRD: the envtest suite loads `bin/crd-render`, which `make crd-render` produces by running `kustomize build config/crd`. So the specs below pass only if `config/crd/bases/frame.plume-labs.io_frameusers.yaml` really carries the enum and the default. `make helm-crds-check` is what proves the chart's copy in `charts/frame/files/crds/` carries the same thing; run it in Step 6.

- [ ] **Step 1: Write the failing tests**

Add to `api/frame/v1alpha1/conversion_test.go`, inside `TestFuzzCorpusReachesTheInterestingBranches`. Declare the counter alongside the existing ones:

```go
	var (
		populatedParams, emptyParams, nilParams int
		populatedDisks, emptyDisks, nilDisks    int
		nonEmptyHash                            int
		populatedCreds                          int
		nonEmptyState                           int
	)
```

then, in the loop, right after the `user.Status.Credentials` check:

```go
		if user.Spec.State != "" {
			nonEmptyState++
		}
```

and add one row to the check table, after `{"populated credentials", populatedCreds}`:

```go
		{"non-empty spec.state", nonEmptyState},
```

Add to `internal/controller/frame/frameuser_v1beta1_schema_test.go`, inside the existing `Describe("FrameUser v1beta1 schema", ...)`, immediately after the `It("defaults passwordAuth to disabled, ...")` block and before the `// ---- F11: the hash moves out of spec ----` comment:

```go
	// ---- lot 0c: spec.state ----

	// The absence case is written with rawSpec, not with a typed object whose
	// State is "": a typed client serialises a zero-valued field unless it is
	// tagged omitempty, so a typed "absent" case tests the Go tag as much as
	// the schema. An unstructured spec carrying only email and role has no
	// `state` key at all, which is the thing the default is supposed to fill.
	It("defaults state to enabled, so an account that never mentions it can sign in", func() {
		raw := rawSpec("fu-default-state", map[string]any{
			"email": "admin@example.test",
			"role":  "admin",
		})
		Expect(k8sClient.Create(ctx, raw)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })

		back := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-default-state", Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.State).To(Equal(framev1beta1.StateEnabled))
	})

	It("stores state: disabled as written", func() {
		u := sampleShaped("fu-state-disabled")
		u.Spec.State = framev1beta1.StateDisabled
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })

		back := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-state-disabled", Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.State).To(Equal(framev1beta1.StateDisabled))
	})

	// The closed set is what lets internal/authd read an empty state as
	// enabled without that being a hole: there is no third word for the
	// apiserver to hand it. `+kubebuilder:validation:Required` would not have
	// bought this — it does not reject "" — and no MinLength is needed either,
	// because "" is not in the enum.
	It("rejects any state other than enabled or disabled", func() {
		raw := rawSpec("fu-state-suspended", map[string]any{
			"email": "admin@example.test",
			"role":  "admin",
			"state": "suspended",
		})
		err := k8sClient.Create(ctx, raw)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got %v", err)
	})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
cd /home/rmocq/frame-lot0 && go build ./... && go test ./api/frame/v1alpha1/...
```

Expected: the packages do not compile — `user.Spec.State undefined (type v1beta1.FrameUserSpec has no field or method State)` and `framev1beta1.StateEnabled undefined`. That compile failure is the failing test.

- [ ] **Step 3: Add the field to `v1beta1` only — and see the trap**

In `api/frame/v1beta1/frameuser_types.go`, extend the const block:

```go
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"

	PasswordEnabled  = "enabled"
	PasswordDisabled = "disabled"

	StateEnabled  = "enabled"
	StateDisabled = "disabled"
)
```

and add the field to `FrameUserSpec`, after `PasswordAuth`:

```go
	// State decides whether authd may issue this account an identity at all.
	//
	// Deactivation is a flag rather than an absence. Revoking every passkey
	// would have avoided touching a kind the roadmap declared frozen, but it
	// makes deactivation destructive and reversible only by re-enrolment — the
	// person has to be in the room with their key again to come back.
	//
	// The enum is what makes the empty string safe to read as "enabled" in
	// internal/authd: the apiserver defaults an absent value and refuses every
	// word but these two, so authd never sees a third state it would have to
	// guess about. No MinLength is needed — "" is not in the enum — and
	// +kubebuilder:validation:Required would not have helped, since it does
	// not reject an empty string from a typed client.
	// +optional
	// +kubebuilder:validation:Enum=enabled;disabled
	// +kubebuilder:default=enabled
	State string `json:"state,omitempty"`
```

Then run the fuzz:

```bash
cd /home/rmocq/frame-lot0 && go test ./api/frame/v1alpha1/... -run 'TestHubRoundTrip|TestFuzzCorpus'
```

Expected: `TestFuzzCorpusReachesTheInterestingBranches` passes, and **`TestHubRoundTripIsLossless` fails** with a diff on `Spec.State` — v1beta1 → v1alpha1 dropped it and there was nothing to carry back. That is the constraint at the top of this plan, seen rather than trusted. Step 4 fixes it.

If instead the *corpus* check fails ("the corpus produced no non-empty spec.state"), the filler is not reaching the field: compare against how `nonEmptyHash` is produced for `Status.PasswordHash`, which is the same plain-string case, and fix the counter rather than weakening it — a corpus that never populates the field would leave the round trip proving nothing about it.

- [ ] **Step 4: Give `v1alpha1` the same field and carry it both ways**

In `api/frame/v1alpha1/frameuser_types.go`, add the same two constants to the const block:

```go
	StateEnabled  = "enabled"
	StateDisabled = "disabled"
```

and the field to `FrameUserSpec`, after `PasswordAuth` and before `PasswordHash`:

```go
	// State decides whether authd may issue this account an identity at all.
	//
	// This deprecated version carries it for one reason: v1beta1 is the
	// storage version and TestHubRoundTripIsLossless fuzzes
	// v1beta1 -> v1alpha1 -> v1beta1 demanding exact equality, so a field this
	// version lacks is a field a round trip through this version destroys.
	// The same reasoning put observedGeneration here before the freeze.
	// +optional
	// +kubebuilder:validation:Enum=enabled;disabled
	// +kubebuilder:default=enabled
	State string `json:"state,omitempty"`
```

In `api/frame/v1alpha1/conversion.go`, in `(*FrameUser).ConvertTo`, after `dst.Spec.Role = src.Spec.Role`:

```go
	dst.Spec.State = src.Spec.State
```

and in `(*FrameUser).ConvertFrom`, after `dst.Spec.Role = src.Spec.Role`:

```go
	dst.Spec.State = src.Spec.State
```

- [ ] **Step 5: Regenerate the manifests and the deepcopy**

```bash
cd /home/rmocq/frame-lot0 && make generate manifests
```

`make manifests` calls `make helm-sync-crds` itself, so both CRD copies move together. Confirm the enum landed in both:

```bash
cd /home/rmocq/frame-lot0 && grep -c 'enabled$' config/crd/bases/frame.plume-labs.io_frameusers.yaml charts/frame/files/crds/frame.plume-labs.io_frameusers.yaml && grep -n -A6 'state:' config/crd/bases/frame.plume-labs.io_frameusers.yaml | head -40
```

Expected: `state:` appears under both versions' `spec.properties`, each with `default: enabled` and `enum: [enabled, disabled]`.

- [ ] **Step 6: Run the tests**

Run these as three separate commands, not one pipeline — a `| tail` swallows the exit code and a failed suite reads as a pass:

```bash
cd /home/rmocq/frame-lot0 && go test ./api/frame/...
cd /home/rmocq/frame-lot0 && make test
cd /home/rmocq/frame-lot0 && make helm-crds-check
```

Expected: `TestHubRoundTripIsLossless` green, the three new `FrameUser v1beta1 schema` specs green, `helm-crds-check` silent (no drift). `make helm-parity` is not run here — see the known cpu-request failure in Global Constraints.

- [ ] **Step 7: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add api config/crd charts/frame/files/crds internal/controller/frame/frameuser_v1beta1_schema_test.go && git commit -m "feat(api): a FrameUser can be disabled without losing its keys"
```

---

### Task 2: One function decides whether an identity may be issued — three of the four paths

**Files:**
- Create: `internal/authd/state.go`
- Create: `internal/authd/state_test.go`
- Modify: `internal/authd/server_session.go` (`sessionUser` at lines 163-180, `handleToken` at lines 121-151, `handlePasswordLogin`'s `usable` at line 77)
- Modify: `internal/authd/server_webauthn.go` (`handleLoginFinish` at lines 38-65)

**Interfaces:**
- Consumes: `framev1beta1.StateDisabled` (Task 1).
- Produces:
  ```go
  var ErrAccountDisabled = errors.New("account is disabled")
  func requireIssuable(u *framev1beta1.FrameUser) error
  var finishLogin = (*Authenticator).FinishLogin   // test seam, package-level
  ```

**Why four tests and not one.** The defect this design guards against is not "the function is wrong" — it is "the check exists in three places out of four". A test of `requireIssuable` alone would stay green while any one call site was deleted. So there is one test per identity-issuing path, each driven through `ServeHTTP` against a disabled account, and each one goes green again the moment its own call site comes back. Three of them are here; the fourth (`/auth/invite/accept`) arrives with that route, in Task 5, and is named the same way.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/state_test.go`:

```go
package authd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// hasSessionCookie reports whether a response handed out a session. Every
// refusal below asserts on this as well as on the status code: a handler that
// returns 401 after already calling setSession would still have set a usable
// cookie, and the status alone would not show it.
func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return true
		}
	}
	return false
}

func TestRequireIssuableTreatsAnUnsetStateAsEnabled(t *testing.T) {
	// The CRD defaults spec.state to enabled and its enum admits no third
	// word, so an account read back from the apiserver is never "". Anything
	// that does not come through the apiserver — the fake client in these
	// tests, an account stored before the field existed — must still be able
	// to sign in, so the empty string is enabled and only the literal
	// "disabled" refuses.
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	if err := requireIssuable(u); err != nil {
		t.Fatalf("requireIssuable on an unset state = %v, want nil", err)
	}
	u.Spec.State = framev1beta1.StateEnabled
	if err := requireIssuable(u); err != nil {
		t.Fatalf("requireIssuable on an explicitly enabled account = %v, want nil", err)
	}
	u.Spec.State = framev1beta1.StateDisabled
	if err := requireIssuable(u); err == nil {
		t.Fatal("requireIssuable admitted a disabled account")
	}
	if requireIssuable(nil) == nil {
		t.Fatal("requireIssuable admitted a nil account")
	}
}

// TestTokenPathRefusesADisabledAccount covers path 1 of 4: POST /auth/token.
//
// This is the load-bearing one. The UI calls /auth/token every fifteen
// minutes, so the session is deliberately minted here while the account is
// still enabled and the account is disabled afterwards, through the same
// client the store reads: disabling must cut a session that is already open,
// within one token lifetime, without anyone touching the cookie.
func TestTokenPathRefusesADisabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, u)

	sessRec := httptest.NewRecorder()
	if !srv.setSession(sessRec, u) {
		t.Fatal("setSession failed")
	}
	session := sessionCookieFrom(t, sessRec)

	// Non-vacuity: the same cookie must work before the account is disabled,
	// or the 401 below would prove nothing about spec.state.
	if rec := doWithCookie(t, srv, "/auth/token", "", session); rec.Code != http.StatusOK {
		t.Fatalf("/auth/token while enabled = %d, want 200", rec.Code)
	}

	var fresh framev1beta1.FrameUser
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: "alice", Namespace: "cluster-control"}, &fresh); err != nil {
		t.Fatalf("get: %v", err)
	}
	fresh.Spec.State = framev1beta1.StateDisabled
	if err := c.Update(context.Background(), &fresh); err != nil {
		t.Fatalf("disable alice: %v", err)
	}

	rec := doWithCookie(t, srv, "/auth/token", "", session)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/auth/token for a disabled account = %d, want 401 — "+
			"disabling must cut a session already open", rec.Code)
	}
}

// TestPasswordLoginPathRefusesADisabledAccount covers path 2 of 4:
// POST /auth/login/password. The fixture is exactly
// TestPasswordLoginSucceedsWhenEnabled's, plus spec.state — so the only thing
// that can turn that test's 204 into this test's 401 is the state check.
func TestPasswordLoginPathRefusesADisabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	u.Spec.PasswordAuth = framev1beta1.PasswordEnabled
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u.Status.PasswordHash = hash
	u.Spec.State = framev1beta1.StateDisabled
	srv := testServer(t, u)

	rec := do(t, srv, http.MethodPost, "/auth/login/password",
		`{"email":"alice@example.com","password":"hunter2"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password login for a disabled account = %d, want 401", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a disabled account was handed a session cookie")
	}
}

// stubFinishLogin replaces the WebAuthn half of handleLoginFinish for the
// duration of one test, so the handler's post-ceremony half can be driven
// without signing a real assertion. Same idiom, and same reason, as
// verifyPassword in server_session.go: the alternative is a wall-clock or
// crypto-heavy test of code that is not what is under test.
func stubFinishLogin(t *testing.T, u *framev1beta1.FrameUser) {
	t.Helper()
	restore := finishLogin
	t.Cleanup(func() { finishLogin = restore })
	finishLogin = func(*Authenticator, context.Context, string, []byte) (*framev1beta1.FrameUser, error) {
		return u, nil
	}
}

func postLoginFinish(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: challengeCookie, Value: "opened-by-the-stub"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestPasskeyLoginPathRefusesADisabledAccount covers path 3 of 4:
// POST /auth/login/finish.
func TestPasskeyLoginPathRefusesADisabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	u.Spec.State = framev1beta1.StateDisabled
	srv := testServer(t, u)
	stubFinishLogin(t, u)

	rec := postLoginFinish(t, srv)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("passkey login for a disabled account = %d, want 401", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a disabled account was handed a session cookie")
	}
}

// TestPasskeyLoginPathAdmitsAnEnabledAccount is what keeps the test above
// honest: with the same stub and the same request, an enabled account reaches
// setSession and gets its 204. Without this, a handler that rejected every
// assertion outright would look correct.
func TestPasskeyLoginPathAdmitsAnEnabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	srv := testServer(t, u)
	stubFinishLogin(t, u)

	rec := postLoginFinish(t, srv)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("passkey login for an enabled account = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if !hasSessionCookie(rec) {
		t.Fatal("an enabled account got no session cookie")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/...
```

Expected: the package does not compile — `undefined: requireIssuable` and `undefined: finishLogin`.

- [ ] **Step 3: Implement**

Create `internal/authd/state.go`:

```go
package authd

import (
	"errors"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// ErrAccountDisabled is what requireIssuable returns for an account whose
// spec.state is disabled. It is a distinct error so a caller can choose its
// own status code — the login endpoints answer 401 like every other refusal
// they make, and /auth/invite/accept answers 403, because there the caller
// has already proven they hold a valid, unspent invitation and telling them
// the account is switched off leaks nothing they did not already know.
var ErrAccountDisabled = errors.New("account is disabled")

// requireIssuable is the one place that decides whether authd may mint or
// re-mint an identity for u.
//
// Four paths turn something into an identity: POST /auth/token (a cookie into
// a bearer token), password login, passkey login, and invitation acceptance
// (each of the last three, a credential into a cookie). A check missing from
// any one of them makes deactivation a lie, so they all call this. Each has
// its own test, named for its path, that fails if its call is removed —
// a single test of this function would not.
//
// An empty state is enabled. The CRD defaults spec.state to enabled and its
// enum admits no third word, so an account decoded by the apiserver is never
// "" — the only way to see one is a client that never went through the
// apiserver. Reading "" as disabled would lock out every such account for no
// gain; the closed set is enforced one layer up, by the schema, which is what
// makes fail-open right here and would make it wrong for a free-form field.
//
// A nil account is refused: no account is not evidence of a permitted one.
func requireIssuable(u *framev1beta1.FrameUser) error {
	if u == nil || u.Spec.State == framev1beta1.StateDisabled {
		return ErrAccountDisabled
	}
	return nil
}
```

In `internal/authd/server_session.go`, fold the check into `sessionUser`, which is the gate on every session-derived route — `/auth/token`, both register ceremonies, and the credential routes Task 6 adds:

```go
// sessionUser resolves the signed-in account, writing the 401 itself so every
// caller is a two-liner that cannot forget to stop on failure.
//
// The state check is here rather than in each caller because this is the one
// place a cookie becomes an account: putting it here means a route added later
// cannot forget it, and a disabled account cannot enrol a further key on a
// cookie it was already holding when it was switched off.
func (s *Server) sessionUser(w http.ResponseWriter, r *http.Request) (*framev1beta1.FrameUser, bool) {
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
	if err := requireIssuable(u); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}
```

and replace `handleToken`'s first fifteen lines with a call to it — the cookie-opening code they duplicated is gone, and with it the chance of the two drifting:

```go
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
```

In the same file, extend `handlePasswordLogin`'s `usable` (the `err == nil` clause must stay first — Go short-circuits `&&`, and that is what keeps `u` from being dereferenced when there is no account):

```go
	usable := err == nil && requireIssuable(u) == nil &&
		u.Spec.PasswordAuth == framev1beta1.PasswordEnabled && hashIsUsable(u.Status.PasswordHash)
```

This deliberately folds into `usable` rather than returning early: `verifyPassword` is still called exactly once on every path, so the argon2id cost — and the wall clock — stays identical for an unknown email, a passkey-only account, a disabled account and a wrong password. `TestPasswordLoginAlwaysVerifiesExactlyOnce` is what holds that.

In `internal/authd/server_webauthn.go`, add the seam above `handleLoginFinish` and the check inside it:

```go
// finishLogin is an indirection over Authenticator.FinishLogin, so a test can
// exercise handleLoginFinish's post-ceremony half — which is where the state
// check lives — without signing a real WebAuthn assertion. Same idiom, and
// same reason, as verifyPassword in server_session.go. Production code always
// calls through this var unmodified.
var finishLogin = (*Authenticator).FinishLogin
```

```go
	u, err := finishLogin(s.cfg.Auth, r.Context(), c.Value, body)
	if err != nil {
		if errors.Is(err, ErrCounterRegression) {
			slog.Error("possible cloned authenticator: sign counter did not advance", "error", err)
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
	w.WriteHeader(http.StatusNoContent)
```

- [ ] **Step 4: Run the tests**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/... -v -run 'Path|RequireIssuable|Password|Token'
```

Expected: the five new tests green, and the existing `TestPasswordLoginSucceedsWhenEnabled`, `TestPasswordLoginAlwaysVerifiesExactlyOnce`, `TestTokenReflectsRoleChangedAfterSessionIssued`, `TestBootstrapSessionAllowsImmediateRegisterBegin` still green — the last one in particular, since `sessionUser` is now stricter and bootstrap's fresh admin has no explicit state.

Then the whole package: `go test ./internal/authd/...`

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add internal/authd && git commit -m "feat(authd): one function decides whether an identity may be issued"
```

---

### Task 3: Admission — who may switch an account off, and what "another admin" means

**Files:**
- Modify: `internal/webhook/frame/v1beta1/frameuser_webhook.go` (`ValidateUpdate` at lines 98-116; `requireAnotherAdmin` at lines 276-289)
- Test: `internal/webhook/frame/v1beta1/frameuser_webhook_test.go` (new `It`s in the existing `Describe`)

**Interfaces:**
- Consumes: `framev1beta1.StateEnabled` / `StateDisabled` (Task 1).
- Produces:
  ```go
  func isEnabled(state string) bool        // "" and "enabled" are enabled
  func stateOrEnabled(state string) string // "" renders as "enabled" in messages
  ```
  and a stricter `requireAnotherAdmin`, which now requires the other admin to be **enabled**.

**The shipped manifest.** No manifest changes. The `ValidatingWebhookConfiguration` generated from the marker on `FrameUserCustomValidator` already selects `create;update;delete` on `frameusers` with `matchPolicy=Equivalent`, so the new rules run on exactly the requests they must. `config/rbac/frameuser_admin_role.yaml` already grants the admin tier `patch`/`update` on `frameusers`, which is how the console changes `spec.state` under the signed-in admin's own impersonated identity. Nothing in this task creates a Role, a Secret or a CRD.

- [ ] **Step 1: Write the failing tests**

Append to `internal/webhook/frame/v1beta1/frameuser_webhook_test.go`, inside the existing `Describe("FrameUser webhook", ...)`:

```go
	// disabledUser is `user` switched off. The helper exists so a spec reads
	// as a sentence rather than as two statements.
	disabledUser := func(name, role string) *framev1beta1.FrameUser {
		u := user(name, role)
		u.Spec.State = framev1beta1.StateDisabled
		return u
	}

	It("refuses a non-admin switching an account off", func() {
		alice := user("alice", framev1beta1.RoleViewer)
		v := newValidator(alice, user("root", framev1beta1.RoleAdmin))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		_, err := v.ValidateUpdate(requestBy("frame:viewers"), alice, off)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.state"))
		Expect(err.Error()).To(ContainSubstring("not an admin"))
	})

	It("lets an admin switch an account off and back on", func() {
		alice := user("alice", framev1beta1.RoleViewer)
		v := newValidator(alice, user("root", framev1beta1.RoleAdmin))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).NotTo(HaveOccurred())

		on := off.DeepCopy()
		on.Spec.State = framev1beta1.StateEnabled
		_, err = v.ValidateUpdate(requestBy("frame:admins"), off, on)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses disabling the only admin", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, user("bob", framev1beta1.RoleViewer))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		// An admin requester, so this exercises the last-admin rule rather
		// than the authorization one in front of it.
		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows disabling an admin when another enabled admin remains", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, user("carol", framev1beta1.RoleAdmin))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).NotTo(HaveOccurred())
	})

	// The discriminating one. A disabled admin cannot obtain a token — authd
	// refuses every identity-issuing path for it — so counting it as "another
	// admin" would let the last usable admin be demoted, deleted or disabled
	// behind an account nobody can sign in to. Before requireAnotherAdmin
	// looked at spec.state, this passed and locked the cluster out of its own
	// console.
	It("does not count a disabled admin as the admin who remains", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, disabledUser("dave", framev1beta1.RoleAdmin))

		demoted := alice.DeepCopy()
		demoted.Spec.Role = framev1beta1.RoleViewer
		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, demoted)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))

		_, err = v.ValidateDelete(requestBy("frame:admins"), alice)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))

		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled
		_, err = v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	// A create cannot take anyone's access away, and refusing one would break
	// /auth/invite, which creates under authd's ServiceAccount. Pinned so the
	// asymmetry is a decision rather than an omission.
	It("does not guard spec.state on create", func() {
		v := newValidator(user("root", framev1beta1.RoleAdmin))
		_, err := v.ValidateCreate(requestBy("frame:viewers"), disabledUser("newbie", framev1beta1.RoleViewer))
		Expect(err).NotTo(HaveOccurred())
	})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
cd /home/rmocq/frame-lot0 && make test 2>&1 | grep -A20 'FrameUser webhook'
```

Expected: `refuses a non-admin switching an account off`, `refuses disabling the only admin` and `does not count a disabled admin as the admin who remains` fail — the validator has no opinion about `spec.state` yet, so every one of those updates is allowed.

- [ ] **Step 3: Implement**

In `internal/webhook/frame/v1beta1/frameuser_webhook.go`, replace `ValidateUpdate`:

```go
func (v *FrameUserCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *framev1beta1.FrameUser) (admission.Warnings, error) {
	if err := guardPasswordHash(ctx, oldObj.Status.PasswordHash, newObj.Status.PasswordHash); err != nil {
		return nil, err
	}
	// Before the last-admin rule, not after it: whether another admin exists
	// is not something to tell a caller who has no business changing a role
	// in the first place.
	if oldObj.Spec.Role != newObj.Spec.Role {
		action := fmt.Sprintf("change spec.role from %q to %q", oldObj.Spec.Role, newObj.Spec.Role)
		if err := requireAdminRequester(ctx, action); err != nil {
			return nil, err
		}
	}
	// spec.state takes the same guard as spec.role, for the same reason.
	// Switching an account off ends every session it holds within one token
	// lifetime (requireIssuable in internal/authd/state.go refuses /auth/token
	// for it), so "who may disable" is exactly as privilege-affecting as "who
	// may demote", and leaving it ungoverned would hand anyone with `patch
	// frameusers` the ability to lock every colleague out one at a time.
	if oldObj.Spec.State != newObj.Spec.State {
		action := fmt.Sprintf("change spec.state from %q to %q",
			stateOrEnabled(oldObj.Spec.State), stateOrEnabled(newObj.Spec.State))
		if err := requireAdminRequester(ctx, action); err != nil {
			return nil, err
		}
	}
	// Disabling an admin removes them from the pool of people who can
	// authorize anything, exactly as a demotion does, so the last-admin rule
	// covers it too.
	if newObj.Spec.Role == framev1beta1.RoleAdmin &&
		isEnabled(oldObj.Spec.State) && !isEnabled(newObj.Spec.State) {
		return nil, v.requireAnotherAdmin(ctx, oldObj.Name)
	}
	// Only a demotion can remove an admin; anything else leaves the count alone.
	if oldObj.Spec.Role != framev1beta1.RoleAdmin || newObj.Spec.Role == framev1beta1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, oldObj.Name)
}

// isEnabled reads spec.state the way authd does: the empty string is enabled,
// because the CRD defaults the field and its enum admits no third word, so ""
// only ever means "written by something that did not go through defaulting".
// The two readings must agree — a webhook that counted "" as disabled would
// refuse writes authd would have honoured, and vice versa.
func isEnabled(state string) bool { return state != framev1beta1.StateDisabled }

// stateOrEnabled renders spec.state for a human-readable refusal, spelling the
// empty string as what it actually means.
func stateOrEnabled(state string) string {
	if state == "" {
		return framev1beta1.StateEnabled
	}
	return state
}
```

and replace `requireAnotherAdmin`:

```go
// requireAnotherAdmin fails unless some *enabled* admin other than `excluding`
// exists.
//
// Enabled is load-bearing, not decoration. A disabled admin cannot obtain a
// token by any route — authd refuses all four identity-issuing paths for it —
// so counting one as the admin who remains would allow the last usable admin
// to be demoted, deleted or switched off, leaving a console nobody can enter
// and a recovery that runs through the node's kubeconfig.
func (v *FrameUserCustomValidator) requireAnotherAdmin(ctx context.Context, excluding string) error {
	var users framev1beta1.FrameUserList
	if err := v.Client.List(ctx, &users); err != nil {
		// Fail closed: an unreadable list is not evidence that another admin
		// exists, and guessing wrong here locks everyone out of the UI.
		return fmt.Errorf("cannot verify remaining admins: %w", err)
	}
	for _, u := range users.Items {
		if u.Name != excluding && u.Spec.Role == framev1beta1.RoleAdmin && isEnabled(u.Spec.State) {
			return nil
		}
	}
	return fmt.Errorf("refusing to remove the last admin (%s): no other enabled account holds the admin role", excluding)
}
```

The message keeps the words "last admin" verbatim: the pre-existing specs `refuses deleting the only admin` and `refuses demoting the only admin` assert `ContainSubstring("last admin")`, and a rewording would break them silently in the direction of looking fixed.

- [ ] **Step 4: Run the tests**

```bash
cd /home/rmocq/frame-lot0 && make test
```

Expected: the six new specs green and all pre-existing `FrameUser webhook` specs still green. Read Ginkgo's own summary line for `internal/webhook/frame/v1beta1` rather than piping through `grep` — a pipe hands you grep's exit code, and a failed suite then reads as a pass.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add internal/webhook/frame/v1beta1 && git commit -m "fix(webhook): a disabled admin is not a way back in"
```

---

### Task 4: `POST /auth/invite` — an account with no credential, and the one link that can give it one

**Files:**
- Modify: `internal/authd/challenge.go` (the `Purpose` const block, lines 40-46)
- Modify: `internal/authd/server.go` (`ServerConfig`, `NewServer`)
- Modify: `cmd/authd/main.go` (`authd.ServerConfig{...}` literal at lines 111-121)
- Create: `internal/authd/server_invite.go`
- Create: `internal/authd/server_invite_test.go`
- Test: `internal/authd/server_test.go` (`bootstrapServer` gains a console origin)

**Interfaces:**
- Consumes: `requireIssuable` (Task 2), `framev1beta1.StateEnabled` (Task 1), the existing `emailPattern`, `frameUserNameForEmail`, `Store.Create`, `Server.sessionUser`.
- Produces:
  ```go
  const PurposeInvite Purpose = "invite"
  const maxEmailLength = 254
  // ServerConfig gains:
  //   ConsoleOrigin string        // e.g. "https://frame.example"
  //   InviteTTL     time.Duration // defaults to 24h in NewServer
  // Route: POST /auth/invite -> 200 {"url": "<origin>/invite?token=<sealed>"}
  const testConsoleOrigin = "https://frame.example" // test-only
  ```

**No new environment variable.** `ConsoleOrigin` is fed from `RP_ORIGIN`, which `cmd/authd` already requires and which WebAuthn already requires to be the console's exact origin. Anything else would be a second source of truth for the same string, free to drift from the origin the browser will actually be on.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/server_invite_test.go`:

```go
package authd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// sessionFor mints a session cookie for u against srv, which is how every
// test below authenticates as a caller without driving a login ceremony.
func sessionFor(t *testing.T, srv *Server, u *framev1beta1.FrameUser) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if !srv.setSession(rec, u) {
		t.Fatal("setSession failed")
	}
	return sessionCookieFrom(t, rec)
}

// inviteURLToken pulls the sealed token back out of the link the handler
// returned, so a test can assert on what the link actually carries rather
// than on the string's shape alone.
func inviteURLToken(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("invite url %q does not parse: %v", rawURL, err)
	}
	return parsed.Query().Get("token")
}

func countUsers(t *testing.T, c client.Client) int {
	t.Helper()
	var list framev1beta1.FrameUserList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	return len(list.Items)
}

func TestInviteRequiresAnAdminSession(t *testing.T) {
	viewer := fixture("bob", "bob@example.com", framev1beta1.RoleViewer)
	srv, c := bootstrapServer(t, false, viewer)

	if rec := do(t, srv, http.MethodPost, "/auth/invite",
		`{"email":"new@example.com","role":"viewer"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invite with no session = %d, want 401", rec.Code)
	}
	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"new@example.com","role":"viewer"}`, sessionFor(t, srv, viewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invite from a viewer = %d, want 403", rec.Code)
	}
	if n := countUsers(t, c); n != 1 {
		t.Fatalf("a refused invite created an account: %d users, want 1", n)
	}
}

func TestInviteCreatesAPasskeylessAccountAndReturnsALink(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"Bob@Example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(body.URL, testConsoleOrigin+"/invite?token=") {
		t.Fatalf("invite url = %q, want %s/invite?token=…", body.URL, testConsoleOrigin)
	}

	// The link carries the invitee's address, sealed under its own purpose.
	payload, err := testCodec().Open(PurposeInvite, inviteURLToken(t, body.URL))
	if err != nil {
		t.Fatalf("the invitation token does not open under PurposeInvite: %v", err)
	}
	if string(payload) != "Bob@Example.com" {
		t.Fatalf("token carries %q, want the invited address", payload)
	}

	created, err := NewStore(c, "cluster-control").ByEmail(context.Background(), "Bob@Example.com")
	if err != nil {
		t.Fatalf("the invited account was not created: %v", err)
	}
	if created.Spec.Role != framev1beta1.RoleViewer {
		t.Fatalf("role = %q, want viewer", created.Spec.Role)
	}
	if created.Spec.State != framev1beta1.StateEnabled {
		t.Fatalf("state = %q, want enabled", created.Spec.State)
	}
	if created.Spec.PasswordAuth != framev1beta1.PasswordDisabled {
		t.Fatalf("passwordAuth = %q, want disabled — an invited account is passkey-only", created.Spec.PasswordAuth)
	}
	if len(created.Status.Credentials) != 0 {
		t.Fatalf("an invited account arrived holding credentials: %v", created.Status.Credentials)
	}
	if created.Name != "bob-at-example.com" {
		t.Fatalf("object name = %q, want the lowercased derivation", created.Name)
	}
}

// authd creates the FrameUser under its own ServiceAccount, and the admission
// webhook refuses a `spec.role: admin` create from anyone who is not already
// an admin. So an admin invite would fail at admission with an opaque 500;
// this refuses it here, with a message naming the way through.
func TestInviteRefusesAnAdminRole(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"new@example.com","role":"admin"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invite with role admin = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Accounts screen") {
		t.Fatalf("the refusal does not say how to get an admin: %q", rec.Body.String())
	}
	if n := countUsers(t, c); n != 1 {
		t.Fatalf("a refused invite created an account: %d users, want 1", n)
	}
}

func TestInviteRefusesAMalformedEmailAndAnUnknownRole(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	session := sessionFor(t, srv, admin)

	for _, body := range []string{
		`{"email":"not-an-email","role":"viewer"}`,
		`{"email":"","role":"viewer"}`,
		`{"email":"new@example.com","role":"editor"}`,
		`{"email":"new@example.com","role":""}`,
	} {
		if rec := doWithCookie(t, srv, "/auth/invite", body, session); rec.Code != http.StatusBadRequest {
			t.Fatalf("invite %s = %d, want 400", body, rec.Code)
		}
	}
	if n := countUsers(t, c); n != 1 {
		t.Fatalf("a refused invite created an account: %d users, want 1", n)
	}
}

func TestInviteRefusesADuplicate(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin,
		fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer))

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"bob@example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusConflict {
		t.Fatalf("inviting an existing account = %d, want 409", rec.Code)
	}
}

// The invitation token and the session cookie share one HMAC key, so the
// purpose is the only thing keeping a 24-hour invitation from being presented
// as a session — and a session from being spent as an invitation.
func TestAnInvitationTokenIsNotASession(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"bob@example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	token := inviteURLToken(t, body.URL)

	if _, err := testCodec().Open(PurposeSession, token); err == nil {
		t.Fatal("an invitation token opened as a session")
	}
	sealed, err := testCodec().Seal(PurposeSession, []byte("bob@example.com"), time.Hour)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := testCodec().Open(PurposeInvite, sealed); err == nil {
		t.Fatal("a session cookie opened as an invitation")
	}
}
```

In `internal/authd/server_test.go`, add the console origin to `bootstrapServer`'s `ServerConfig` literal so every test above has one, and declare the constant next to `bootstrapServerToken`:

```go
const (
	bootstrapServerToken      = "s3cret-bootstrap"
	bootstrapServerSecretName = "frame-auth-bootstrap"
	// testConsoleOrigin is what an invitation link is built on. Fixed here
	// rather than threaded through as a parameter that never varies.
	testConsoleOrigin = "https://frame.example"
)
```

and inside `bootstrapServer`'s `authd.ServerConfig{...}`, alongside `TokenTTL`:

```go
		ConsoleOrigin: testConsoleOrigin,
```

- [ ] **Step 2: Run them and watch them fail**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/... -run Invite
```

Expected: compile failure — `undefined: PurposeInvite`, and `unknown field ConsoleOrigin in struct literal of type ServerConfig`.

- [ ] **Step 3: Implement**

In `internal/authd/challenge.go`, extend the `Purpose` block:

```go
const (
	// PurposeSession seals/opens the "frame_session" cookie value.
	PurposeSession Purpose = "session"
	// PurposeChallenge seals/opens the "frame_challenge" cookie value used by
	// the WebAuthn registration and login ceremonies.
	PurposeChallenge Purpose = "challenge"
	// PurposeInvite seals/opens the token in an invitation link. It is the
	// only sealed value that travels in a URL rather than a cookie, which is
	// exactly why it must not verify as either of the other two: an
	// invitation link is pasted into chat, forwarded, and left in browser
	// history, and none of that may turn it into a session.
	PurposeInvite Purpose = "invite"
)
```

In `internal/authd/server.go`, add to `ServerConfig`, after `SessionTTL`:

```go
	// ConsoleOrigin is the browser origin an invitation link points at, e.g.
	// "https://frame.example". cmd/authd feeds it RP_ORIGIN — the value
	// WebAuthn already requires to be the console's exact origin — so this
	// adds no environment variable and cannot drift from the origin the
	// browser will actually be on.
	ConsoleOrigin string
	// InviteTTL bounds how long an invitation link is good for. Defaults to
	// 24 hours in NewServer.
	InviteTTL time.Duration
```

and in `NewServer`, beside the `SessionTTL` default and the route table:

```go
	if cfg.InviteTTL == 0 {
		cfg.InviteTTL = 24 * time.Hour
	}
```
```go
	s.mux.HandleFunc("POST /auth/invite", s.handleInvite)
```

In `cmd/authd/main.go`, add one line to the `authd.ServerConfig{...}` literal, after `TokenTTL`:

```go
		ConsoleOrigin:       cfg.rpOrigin,
```

Create `internal/authd/server_invite.go`:

```go
package authd

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// maxEmailLength mirrors the CRD's MaxLength on spec.email (RFC 5321's limit,
// T6). Checked here so an over-long address is a clean 400 rather than an
// opaque admission failure after the account name has already been derived.
const maxEmailLength = 254

// handleInvite creates an account holding no credential, and returns the one
// link that can give it one.
//
// The role is operator or viewer, and that is a consequence rather than a
// preference. authd creates the FrameUser under its own ServiceAccount, and
// the admission webhook refuses a create carrying spec.role: admin from
// anyone who is not already an admin, once any admin exists
// (requireAdminRequester, internal/webhook/frame/v1beta1/frameuser_webhook.go).
// That guard is what stops everything holding `create frameusers` from minting
// an admin, and authd holds exactly that. So an admin invite would be refused
// at admission and surface here as a 500 with an unreadable message; refusing
// it up front, naming the way through, is the same rule stated where the
// caller can act on it. The way through is real: promoting an account from the
// Accounts screen travels under the signed-in admin's own impersonated
// identity, which is precisely what the webhook asks for.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	if caller.Spec.Role != framev1beta1.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !emailPattern.MatchString(body.Email) || len(body.Email) > maxEmailLength {
		http.Error(w, "invalid request: email", http.StatusBadRequest)
		return
	}
	if body.Role != framev1beta1.RoleOperator && body.Role != framev1beta1.RoleViewer {
		http.Error(w, "an invitation may only create an operator or a viewer. "+
			"Create the account as one of those, then promote it from the Accounts screen, "+
			"which acts under your own identity rather than authd's", http.StatusBadRequest)
		return
	}
	name := frameUserNameForEmail(body.Email)
	if name == "" {
		http.Error(w, "invalid request: email", http.StatusBadRequest)
		return
	}

	user := &framev1beta1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.cfg.Namespace},
		Spec: framev1beta1.FrameUserSpec{
			Email: body.Email,
			Role:  body.Role,
			State: framev1beta1.StateEnabled,
			// Passkey-only, like the first admin: no route sets a password,
			// and the invitation's whole job is to reach enrolment.
			PasswordAuth: framev1beta1.PasswordDisabled,
		},
	}
	if err := s.cfg.Store.Create(r.Context(), user); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// frameUserNameForEmail is deterministic, so this is either the
			// same address invited twice or the collision its own comment
			// warns about ("a@b" and "a-at-b" derive the same name). It stops
			// being a one-caller function here — bootstrap ran once, invite
			// runs whenever an admin asks — so the collision is answered
			// rather than assumed away. Both cases are the caller's to
			// resolve and neither is an authd failure.
			http.Error(w, "an account with that name already exists", http.StatusConflict)
			return
		}
		slog.Error("invite: failed to create the invited account", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sealed, err := s.cfg.Codec.Seal(PurposeInvite, []byte(body.Email), s.cfg.InviteTTL)
	if err != nil {
		// The account exists and the admin can invite again once this is
		// fixed; there is nothing to roll back that leaving it enabled and
		// credential-less does not already express.
		slog.Error("invite: failed to seal the invitation token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// The link is returned, never sent: there is no mail path in this cluster,
	// and inventing one would be a second project. The admin copies it.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url": s.cfg.ConsoleOrigin + "/invite?token=" + url.QueryEscape(sealed),
	})
}
```

- [ ] **Step 4: Run the tests**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/... && go build ./cmd/...
```

Expected: all six new `Invite` tests green, the whole `internal/authd` package green, `cmd/authd` still building.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add internal/authd cmd/authd && git commit -m "feat(authd): an admin can invite a second person"
```

---

### Task 5: `POST /auth/invite/accept` — single-use by construction, and the fourth path

**Files:**
- Modify: `internal/authd/server_session.go` (`setSession`, lines 103-119)
- Modify: `internal/authd/server.go` (`NewServer` route table)
- Modify: `internal/authd/server_invite.go` (add `handleInviteAccept`)
- Test: `internal/authd/server_invite_test.go` (append)

**Interfaces:**
- Consumes: `PurposeInvite`, `testConsoleOrigin`, `sessionFor`, `inviteURLToken` (Task 4); `requireIssuable` (Task 2).
- Produces:
  ```go
  const enrolSessionTTL = 15 * time.Minute
  func (s *Server) setSessionFor(w http.ResponseWriter, u *framev1beta1.FrameUser, ttl time.Duration) bool
  // Route: POST /auth/invite/accept, body {"token": "<sealed>"} -> 204 + short session
  ```

**Single-use by construction.** There is no stored flag, no table and no revocation list: the link is refused the moment the account holds any credential, and the only thing the session it grants can do is enrol one. The account's own contents are the record of whether the link has been spent — which is why nothing can get out of step with it.

- [ ] **Step 1: Write the failing tests**

Append to `internal/authd/server_invite_test.go`:

```go
// inviteFor drives a real /auth/invite as an admin and returns the token from
// the link. Going through the route rather than sealing a token by hand is
// what makes the tests below cover the pair rather than one half of it.
func inviteFor(t *testing.T, srv *Server, admin *framev1beta1.FrameUser, email, role string) string {
	t.Helper()
	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"`+email+`","role":"`+role+`"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return inviteURLToken(t, body.URL)
}

func TestInviteAcceptGrantsAShortSessionThatCanEnrol(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	session := sessionCookieFrom(t, rec)
	if session.MaxAge != int(enrolSessionTTL.Seconds()) {
		t.Fatalf("accepted session Max-Age = %d, want %d — an accepted invitation is a "+
			"window to enrol, not a working day", session.MaxAge, int(enrolSessionTTL.Seconds()))
	}

	// The session is not merely present; it is the one thing the invitee
	// needs. /auth/register/begin is the only route to a first credential.
	begin := doWithCookie(t, srv, "/auth/register/begin", "", session)
	if begin.Code != http.StatusOK {
		t.Fatalf("register/begin on an accepted invitation = %d, want 200: %s", begin.Code, begin.Body.String())
	}
}

// TestSecondInviteAcceptIsRefusedOnceAKeyIsEnrolled is the single-use proof.
// The first acceptance succeeds; a credential is then added through the store,
// exactly as a completed enrolment would; the same link is presented again and
// must be refused. Delete the credential check in handleInviteAccept and this
// returns 204 — the link would be a standing key to the account.
func TestSecondInviteAcceptIsRefusedOnceAKeyIsEnrolled(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	if rec := do(t, srv, http.MethodPost, "/auth/invite/accept",
		`{"token":"`+token+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("first accept = %d, want 204", rec.Code)
	}

	store := NewStore(c, "cluster-control")
	bob, err := store.ByEmail(context.Background(), "bob@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	if err := store.AddCredential(context.Background(), bob, framev1beta1.WebAuthnCredential{
		ID: "ZW5yb2xsZWQ", PublicKey: "cGs", AddedAt: metav1.Now(), Label: "YubiKey 5C",
	}); err != nil {
		t.Fatalf("AddCredential: %v", err)
	}

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusGone {
		t.Fatalf("second accept = %d, want 410", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a spent invitation still handed out a session")
	}
}

// The password branch of the same guard. No route sets a password today, so
// this state is unreachable — which is exactly why it is pinned: "any
// credential" must keep meaning any credential when one arrives.
func TestInviteAcceptIsRefusedOnceAPasswordExists(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	var bob framev1beta1.FrameUser
	// Derive the name rather than spelling it: frameUserNameForEmail appends a
	// hash suffix, so a literal "bob-at-example.com" is NotFound here.
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: frameUserNameForEmail("bob@example.com"), Namespace: "cluster-control"}, &bob); err != nil {
		t.Fatalf("get: %v", err)
	}
	bob.Status.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"
	if err := c.Status().Update(context.Background(), &bob); err != nil {
		t.Fatalf("status update: %v", err)
	}

	if rec := do(t, srv, http.MethodPost, "/auth/invite/accept",
		`{"token":"`+token+`"}`); rec.Code != http.StatusGone {
		t.Fatalf("accept for an account holding a password = %d, want 410", rec.Code)
	}
}

// TestInviteAcceptPathRefusesADisabledAccount covers path 4 of 4:
// POST /auth/invite/accept. Same shape as the three in state_test.go, and
// named the same way, because the defect guarded against is "the check exists
// in three places out of four".
func TestInviteAcceptPathRefusesADisabledAccount(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	var bob framev1beta1.FrameUser
	// Derive the name rather than spelling it: frameUserNameForEmail appends a
	// hash suffix, so a literal "bob-at-example.com" is NotFound here.
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: frameUserNameForEmail("bob@example.com"), Namespace: "cluster-control"}, &bob); err != nil {
		t.Fatalf("get: %v", err)
	}
	bob.Spec.State = framev1beta1.StateDisabled
	if err := c.Update(context.Background(), &bob); err != nil {
		t.Fatalf("disable bob: %v", err)
	}

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("accept for a disabled account = %d, want 403", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a disabled account was handed a session cookie")
	}
}

func TestInviteAcceptRefusesForgedExpiredAndUnknown(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)

	expired, err := testCodec().Seal(PurposeInvite, []byte("bob@example.com"), -time.Second)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// A well-formed, unexpired token for an account that does not exist —
	// deleted between invitation and acceptance.
	unknown, err := testCodec().Seal(PurposeInvite, []byte("ghost@example.com"), time.Hour)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	sessionShaped, err := testCodec().Seal(PurposeSession, []byte("root@example.com"), time.Hour)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	for name, token := range map[string]string{
		"forged":        "forged.token",
		"expired":       expired,
		"unknown":       unknown,
		"session-shape": sessionShaped,
	} {
		rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("accept with a %s token = %d, want 401", name, rec.Code)
		}
		if hasSessionCookie(rec) {
			t.Fatalf("a %s token produced a session cookie", name)
		}
	}
}
```

Add the imports this appends need to the file's import block: `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`.

- [ ] **Step 2: Run them and watch them fail**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/... -run 'InviteAccept|SecondInvite'
```

Expected: compile failure — `undefined: enrolSessionTTL` — and, once that resolves, 404s from the mux for `/auth/invite/accept`.

- [ ] **Step 3: Implement**

In `internal/authd/server_session.go`, split `setSession` so a caller can choose the lifetime:

```go
// setSession seals a 12-hour session cookie for u and writes it onto the
// response. It reports whether that succeeded; on failure it has already
// written a 500 itself, and every caller must stop immediately rather than go
// on to write its own success status on top (the bug this return value exists
// to prevent: a 500 followed by an unconditional 204, and the "superfluous
// response.WriteHeader call" warning that comes with it).
func (s *Server) setSession(w http.ResponseWriter, u *framev1beta1.FrameUser) bool {
	return s.setSessionFor(w, u, s.cfg.SessionTTL)
}

// setSessionFor is setSession with an explicit lifetime, for the one caller
// that does not want a working day: an accepted invitation grants only long
// enough to enrol a key (see enrolSessionTTL). The TTL is inside the sealed
// payload as well as on the cookie, so shortening it is a real constraint and
// not a suggestion the browser could ignore.
func (s *Server) setSessionFor(w http.ResponseWriter, u *framev1beta1.FrameUser, ttl time.Duration) bool {
	sealed, err := s.cfg.Codec.Seal(PurposeSession, []byte(u.Spec.Email), ttl)
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
```

and add `"time"` to that file's imports.

In `internal/authd/server.go`, register the route beside `POST /auth/invite`:

```go
	s.mux.HandleFunc("POST /auth/invite/accept", s.handleInviteAccept)
```

Append to `internal/authd/server_invite.go`:

```go
// enrolSessionTTL is how long the session an accepted invitation grants lasts.
// Long enough to find a key and enrol it; short enough that a link forwarded
// to the wrong person, or left in a browser's history on a shared machine, is
// not a standing account.
const enrolSessionTTL = 15 * time.Minute

// handleInviteAccept spends an invitation.
//
// Single-use by construction rather than by a stored flag: the link is refused
// the moment the account holds any credential, and the only thing the session
// it grants can do is enrol one. There is no table to clean up, no revocation
// list to keep in step, and nothing that can disagree with the account itself
// about whether the link has been spent.
//
// Every failure that is not "already used" answers the same 401 with the same
// wording. An invitation link travels through chat and inboxes, so telling a
// holder of a wrong link whether the address exists, whether it expired, or
// whether the token was ever real would be an account-enumeration oracle
// reachable without any session at all.
func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	email, err := s.cfg.Codec.Open(PurposeInvite, body.Token)
	if err != nil {
		http.Error(w, "this invitation link is invalid or has expired", http.StatusUnauthorized)
		return
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), string(email))
	if err != nil {
		http.Error(w, "this invitation link is invalid or has expired", http.StatusUnauthorized)
		return
	}
	// The fourth of the four identity-issuing paths; see requireIssuable in
	// state.go. 403 rather than 401 here: the caller has already produced a
	// valid, unexpired invitation for this exact account, so saying it is
	// switched off tells them nothing they could not already infer, and
	// leaving them at "invalid or expired" would send them chasing the link.
	if err := requireIssuable(u); err != nil {
		http.Error(w, "this account is disabled", http.StatusForbidden)
		return
	}
	if len(u.Status.Credentials) > 0 || u.Status.PasswordHash != "" {
		http.Error(w, "this invitation has already been used", http.StatusGone)
		return
	}
	if !s.setSessionFor(w, u, enrolSessionTTL) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

and add `"time"` to that file's imports.

- [ ] **Step 4: Run the tests**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/...
```

Expected: the five new accept tests green, and the whole package green — in particular `TestPasswordLoginSucceedsWhenEnabled` and `TestBootstrapCreatesTheFirstAdminAndDeletesTheSecret`, which now reach the cookie through `setSessionFor`.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add internal/authd && git commit -m "feat(authd): an invitation dies at the first enrolment"
```

---

### Task 6: `GET /auth/credentials` and `DELETE /auth/credentials/{id}`

**Files:**
- Modify: `internal/authd/store.go` (`RemoveCredential`, lines 113-137)
- Modify: `internal/authd/server.go` (`NewServer` route table)
- Create: `internal/authd/server_credentials.go`
- Create: `internal/authd/server_credentials_test.go`

**Interfaces:**
- Consumes: `Server.sessionUser` (Task 2), `sessionFor` (Task 4), `Store.RemoveCredential`, `ErrUserNotFound`.
- Produces:
  ```go
  var ErrLastCredential = errors.New("refusing to remove an account's last credential")
  type credentialView struct {
      ID        string      `json:"id"`
      Label     string      `json:"label,omitempty"`
      AddedAt   metav1.Time `json:"addedAt"`
      SignCount uint32      `json:"signCount"`
  }
  // GET    /auth/credentials[?user=<email>]     -> 200 {"credentials":[credentialView]}
  // DELETE /auth/credentials/{id}[?user=<email>] -> 204 | 404 | 409
  ```

**No second copy of the last-admin rule.** The design says revocation "refuses if it would leave the last admin with none". `Store.RemoveCredential` already refuses to leave *any* passkey-only account with none — strictly stronger, and it covers the last admin as a special case of everyone. This task turns that refusal into a typed error so the route can answer 409 instead of 500, and proves it through the route. It does not add a rule that would then have two places to be wrong in.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/server_credentials_test.go`:

```go
package authd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func key(id, label string) framev1beta1.WebAuthnCredential {
	return framev1beta1.WebAuthnCredential{
		ID: id, PublicKey: "cHVibGljLWtleS1tYXRlcmlhbA", SignCount: 3,
		AddedAt: metav1.Now(), Label: label,
	}
}

// doWithCookieMethod is doWithCookie for a method other than POST — DELETE,
// here — since the credential routes are the first in this package that are
// not all POSTs.
func doWithCookieMethod(t *testing.T, srv *Server, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeCredentials(t *testing.T, rec *httptest.ResponseRecorder) []credentialView {
	t.Helper()
	var body struct {
		Credentials []credentialView `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body.Credentials
}

func TestListCredentialsReturnsTheCallersOwnKeysWithoutPublicKeyMaterial(t *testing.T) {
	alice := fixture("alice", "alice@example.com", framev1beta1.RoleViewer,
		key("a2V5LW9uZQ", "YubiKey 5C"), key("a2V5LXR3bw", "Pixel 8"))
	srv := testServer(t, alice)

	rec := doWithCookieMethod(t, srv, http.MethodGet, "/auth/credentials", sessionFor(t, srv, alice))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeCredentials(t, rec)
	if len(got) != 2 || got[0].Label != "YubiKey 5C" || got[1].ID != "a2V5LXR3bw" {
		t.Fatalf("credentials = %+v", got)
	}
	// The public key is not the UI's business, and the less of a credential
	// record that travels, the better. The ID does travel — DELETE needs
	// something to address, and it is public data an ordinary ceremony puts
	// in allowCredentials anyway.
	if strings.Contains(rec.Body.String(), "publicKey") ||
		strings.Contains(rec.Body.String(), "cHVibGljLWtleS1tYXRlcmlhbA") {
		t.Fatalf("public key material was returned: %s", rec.Body.String())
	}
}

func TestListCredentialsNeedsASession(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1beta1.RoleViewer, key("a2V5", "k")))
	req := httptest.NewRequest(http.MethodGet, "/auth/credentials", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("list with no session = %d, want 401", rec.Code)
	}
}

func TestAdminCanReadAnotherAccountsCredentialsAndAViewerCannot(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "root key"))
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer, key("Ym9iLWtleQ", "bob key"))
	viewer := fixture("eve", "eve@example.com", framev1beta1.RoleViewer, key("ZXZlLWtleQ", "eve key"))
	srv := testServer(t, admin, bob, viewer)

	rec := doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials?user=bob%40example.com", sessionFor(t, srv, admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin reading bob = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := decodeCredentials(t, rec); len(got) != 1 || got[0].Label != "bob key" {
		t.Fatalf("admin got %+v, want bob's key", got)
	}

	rec = doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials?user=bob%40example.com", sessionFor(t, srv, viewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer reading bob = %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "bob key") {
		t.Fatalf("a refused read still leaked the answer: %s", rec.Body.String())
	}
}

func TestRevokeRemovesTheCallersOwnKey(t *testing.T) {
	alice := fixture("alice", "alice@example.com", framev1beta1.RoleViewer,
		key("a2V5LW9uZQ", "YubiKey 5C"), key("a2V5LXR3bw", "Pixel 8"))
	srv := testServer(t, alice)
	session := sessionFor(t, srv, alice)

	rec := doWithCookieMethod(t, srv, http.MethodDelete, "/auth/credentials/a2V5LW9uZQ", session)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet, "/auth/credentials", session))
	if len(after) != 1 || after[0].ID != "a2V5LXR3bw" {
		t.Fatalf("after revoking one key: %+v", after)
	}
}

func TestRevokeAnUnknownKeyIs404(t *testing.T) {
	alice := fixture("alice", "alice@example.com", framev1beta1.RoleViewer, key("a2V5LW9uZQ", "k"))
	srv := testServer(t, alice)
	rec := doWithCookieMethod(t, srv, http.MethodDelete,
		"/auth/credentials/bm90LWEta2V5", sessionFor(t, srv, alice))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoking an unknown key = %d, want 404", rec.Code)
	}
}

// The last admin is the case the design names; the guard in
// Store.RemoveCredential is broader than that, and this proves it through the
// route rather than restating it in the handler.
func TestRevokeRefusesToStrandTheLastAdmin(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "only key"))
	srv := testServer(t, admin)
	session := sessionFor(t, srv, admin)

	rec := doWithCookieMethod(t, srv, http.MethodDelete, "/auth/credentials/cm9vdC1rZXk", session)
	if rec.Code != http.StatusConflict {
		t.Fatalf("revoking the last admin's only key = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet, "/auth/credentials", session))
	if len(after) != 1 {
		t.Fatalf("a refused revocation removed the key anyway: %+v", after)
	}
}

func TestRevokeRefusesAViewerActingOnSomeoneElse(t *testing.T) {
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer,
		key("Ym9iLW9uZQ", "one"), key("Ym9iLXR3bw", "two"))
	eve := fixture("eve", "eve@example.com", framev1beta1.RoleViewer, key("ZXZlLWtleQ", "eve"))
	srv := testServer(t, bob, eve)

	rec := doWithCookieMethod(t, srv, http.MethodDelete,
		"/auth/credentials/Ym9iLW9uZQ?user=bob%40example.com", sessionFor(t, srv, eve))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer revoking bob's key = %d, want 403", rec.Code)
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials", sessionFor(t, srv, bob)))
	if len(after) != 2 {
		t.Fatalf("a refused revocation removed a key anyway: %+v", after)
	}
}

func TestAdminCanRevokeAnotherAccountsKey(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "root"))
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer,
		key("Ym9iLW9uZQ", "one"), key("Ym9iLXR3bw", "two"))
	srv := testServer(t, admin, bob)

	rec := doWithCookieMethod(t, srv, http.MethodDelete,
		"/auth/credentials/Ym9iLW9uZQ?user=bob%40example.com", sessionFor(t, srv, admin))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin revoking bob's key = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials", sessionFor(t, srv, bob)))
	if len(after) != 1 || after[0].ID != "Ym9iLXR3bw" {
		t.Fatalf("after the admin revoked one of bob's keys: %+v", after)
	}
}

func TestCredentialsForAnUnknownUserIs404(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "root"))
	srv := testServer(t, admin)
	rec := doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials?user=ghost%40example.com", sessionFor(t, srv, admin))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("admin reading a missing account = %d, want 404", rec.Code)
	}
}

func TestErrLastCredentialIsTypedNotTextual(t *testing.T) {
	// The route distinguishes "refused on purpose" (409) from "the apiserver
	// failed" (500). Matching on message text would make that distinction a
	// string comparison one reword away from turning every refusal into a 500.
	s := storeWith(t, fixture("alice", "alice@example.com", framev1beta1.RoleViewer, key("b25seQ", "only")))
	u, err := s.ByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	if err := s.RemoveCredential(context.Background(), u, "b25seQ"); !errors.Is(err, ErrLastCredential) {
		t.Fatalf("RemoveCredential = %v, want ErrLastCredential", err)
	}
}
```

Add `"errors"` to that file's import block.

- [ ] **Step 2: Run them and watch them fail**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/... -run 'Credential|Revoke'
```

Expected: compile failure — `undefined: credentialView`, `undefined: ErrLastCredential`.

- [ ] **Step 3: Implement**

In `internal/authd/store.go`, give the refusal a type. Add beside `ErrUserNotFound`:

```go
// ErrLastCredential is the refusal to strip an account of its last way in. It
// is a distinct error so an HTTP caller can tell a deliberate refusal (409)
// from an apiserver failure (500) without matching on message text.
var ErrLastCredential = errors.New("refusing to remove an account's last credential")
```

and wrap it in `RemoveCredential`, leaving the surrounding logic untouched:

```go
	if len(kept) == 0 && u.Spec.PasswordAuth != framev1beta1.PasswordEnabled {
		return fmt.Errorf("%w: %s has no password sign-in, so the account would become unreachable",
			ErrLastCredential, u.Spec.Email)
	}
```

In `internal/authd/server.go`, register both routes:

```go
	s.mux.HandleFunc("GET /auth/credentials", s.handleListCredentials)
	s.mux.HandleFunc("DELETE /auth/credentials/{id}", s.handleRevokeCredential)
```

Create `internal/authd/server_credentials.go`:

```go
package authd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// credentialView is what a caller may see of an enrolled authenticator.
//
// PublicKey is deliberately absent: nothing in the console needs it, and a
// record that does not travel cannot leak. The credential ID does travel,
// because DELETE needs something to address and because it is public by
// construction — an ordinary WebAuthn ceremony puts credential IDs in
// allowCredentials, and this one is returned only to the account's owner or
// to an admin.
type credentialView struct {
	ID        string      `json:"id"`
	Label     string      `json:"label,omitempty"`
	AddedAt   metav1.Time `json:"addedAt"`
	SignCount uint32      `json:"signCount"`
}

// subjectOf resolves which account a credentials request is about: the
// caller's own, or someone else's when ?user= names them and the caller is an
// admin.
//
// The role check is what stops this being an account-enumerator: without it
// any signed-in viewer could walk a list of addresses and read existence off
// the 200/404 difference. An admin can already list every FrameUser through
// the apiserver, so the same answer costs nothing there.
func (s *Server) subjectOf(w http.ResponseWriter, r *http.Request, caller *framev1beta1.FrameUser) (*framev1beta1.FrameUser, bool) {
	email := r.URL.Query().Get("user")
	if email == "" || email == caller.Spec.Email {
		return caller, true
	}
	if caller.Spec.Role != framev1beta1.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), email)
	if err != nil {
		http.Error(w, "no such account", http.StatusNotFound)
		return nil, false
	}
	return u, true
}

// handleListCredentials answers the question PasskeysDialog could not ask
// before this route existed: which keys does this account actually hold?
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	subject, ok := s.subjectOf(w, r, caller)
	if !ok {
		return
	}
	// Allocated, not nil: an account with no keys must serialise as [] rather
	// than null, so the UI's empty state is one branch instead of two.
	views := make([]credentialView, 0, len(subject.Status.Credentials))
	for _, c := range subject.Status.Credentials {
		views = append(views, credentialView{
			ID: c.ID, Label: c.Label, AddedAt: c.AddedAt, SignCount: c.SignCount,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"credentials": views})
}

// handleRevokeCredential removes one enrolled authenticator.
//
// The one refusal it can meet is Store.RemoveCredential's: it will not strip a
// passkey-only account of its last key. That is stricter than "refuses if it
// would leave the last admin with none" — it protects every passkey-only
// account, the last admin among them — so there is no second copy of the rule
// here to drift from the first.
func (s *Server) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	subject, ok := s.subjectOf(w, r, caller)
	if !ok {
		return
	}
	err := s.cfg.Store.RemoveCredential(r.Context(), subject, r.PathValue("id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrUserNotFound):
		http.Error(w, "no such credential", http.StatusNotFound)
	case errors.Is(err, ErrLastCredential):
		// 409, not 500 and not 403: the request was well formed and the caller
		// was entitled to make it; it is the account's state that refuses.
		// The message is the store's own, which names the account and why.
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		slog.Error("revoking a credential failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
```

- [ ] **Step 4: Run the tests**

```bash
cd /home/rmocq/frame-lot0 && go test ./internal/authd/...
```

Expected: the ten new credential tests green and the package green — including `TestRemoveCredentialKeepsLastKeyWhenPasswordDisabled` and `TestRemoveCredentialKeepsLastKeyWhenPasswordAuthUnset`, which assert only that an error occurred and so are unaffected by the wrapping.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add internal/authd && git commit -m "feat(authd): list and revoke enrolled keys"
```

---

### Task 7: The console's account logic, where vitest can reach it

**Files:**
- Modify: `src/lib/auth.ts` (append after `logout`, before `__resetForTests`)
- Create: `src/lib/accounts.ts`
- Test: `src/lib/auth.test.ts` (append)
- Test: `src/lib/accounts.test.ts` (create)

**Interfaces:**
- Consumes: the routes from Tasks 4-6.
- Produces:
  ```ts
  // src/lib/auth.ts
  export interface Identity { email: string; groups: string[] }
  export function identityFromToken(token: string): Identity | undefined
  export function isAdminToken(token: string): boolean

  // src/lib/accounts.ts
  export interface CredentialSummary { id: string; label: string; addedAt: string; signCount: number }
  export type InvitableRole = 'operator' | 'viewer'
  export function inviteTokenFromLocation(pathname: string, search: string): string | undefined
  export async function inviteAccount(email: string, role: InvitableRole): Promise<string>
  export async function acceptInvitation(token: string): Promise<void>
  export async function listCredentials(email?: string): Promise<CredentialSummary[]>
  export async function revokeCredential(id: string, email?: string): Promise<void>
  ```

**Why none of this lives in a component.** Vitest runs `environment: 'node'` with `include: ['src/**/*.test.ts']`, so a `.tsx` spec never executes — writing one would produce a file that looks like coverage and runs never. Every decision worth a test is therefore a function here, and Task 8's components only call them.

- [ ] **Step 1: Write the failing tests**

Append to `src/lib/auth.test.ts`:

```ts
// A real ES256 token's payload, base64url with the padding stripped — the
// shape authd's issuer actually emits (jose serialises unpadded). The
// signature is not checked here and must not be: see identityFromToken.
function tokenWith(claims: Record<string, unknown>): string {
  const b64url = (s: string) =>
    Buffer.from(s, 'utf8').toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64url('{"alg":"ES256"}')}.${b64url(JSON.stringify(claims))}.c2ln`
}

describe('identityFromToken', () => {
  it('reads the email and groups an authd token carries', () => {
    const id = identityFromToken(tokenWith({ email: 'alice@example.com', groups: ['admins'] }))
    expect(id).toEqual({ email: 'alice@example.com', groups: ['admins'] })
  })

  // authd's issuer puts the group in unprefixed — frame-uiproxy is what adds
  // `frame:` on the way to the apiserver. Matching the prefixed form here
  // would hide the Accounts screen from every admin.
  it('matches the unprefixed group authd mints, not the impersonated one', () => {
    expect(isAdminToken(tokenWith({ email: 'a@b.c', groups: ['admins'] }))).toBe(true)
    expect(isAdminToken(tokenWith({ email: 'a@b.c', groups: ['frame:admins'] }))).toBe(false)
    expect(isAdminToken(tokenWith({ email: 'a@b.c', groups: ['viewers'] }))).toBe(false)
  })

  it('returns undefined rather than throwing on anything that is not a token', () => {
    expect(identityFromToken('')).toBeUndefined()
    expect(identityFromToken('not.a.token')).toBeUndefined()
    expect(identityFromToken('only-one-part')).toBeUndefined()
    expect(identityFromToken(tokenWith({ groups: ['admins'] }))).toBeUndefined()
    expect(isAdminToken('garbage')).toBe(false)
  })
})
```

and add `identityFromToken, isAdminToken` to the existing import from `@/lib/auth`.

Create `src/lib/accounts.test.ts`:

```ts
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  acceptInvitation,
  inviteAccount,
  inviteTokenFromLocation,
  listCredentials,
  revokeCredential,
} from '@/lib/accounts'

/** Records what was fetched, so a test can assert on the request as well as the answer. */
function stubFetch(response: Response | ((input: string, init?: RequestInit) => Response)) {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string, init?: RequestInit) => {
      calls.push({ url: input, init })
      return typeof response === 'function' ? response(input, init) : response
    }),
  )
  return calls
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })

beforeEach(() => {
  vi.unstubAllGlobals()
})

describe('inviteTokenFromLocation', () => {
  it('finds the token on the invitation page', () => {
    expect(inviteTokenFromLocation('/invite', '?token=abc.def')).toBe('abc.def')
  })

  // Scoped to /invite on purpose: without the path check, any screen reached
  // with a stray ?token= in the URL would try to spend an invitation instead
  // of rendering.
  it('ignores a token anywhere but the invitation page', () => {
    expect(inviteTokenFromLocation('/', '?token=abc.def')).toBeUndefined()
    expect(inviteTokenFromLocation('/nodes', '?token=abc.def')).toBeUndefined()
  })

  it('is undefined when there is no token', () => {
    expect(inviteTokenFromLocation('/invite', '')).toBeUndefined()
    expect(inviteTokenFromLocation('/invite', '?token=')).toBeUndefined()
  })
})

describe('inviteAccount', () => {
  it('posts the address and role and returns the link', async () => {
    const calls = stubFetch(json({ url: 'https://frame.example/invite?token=sealed' }))
    const url = await inviteAccount('bob@example.com', 'viewer')
    expect(url).toBe('https://frame.example/invite?token=sealed')
    expect(calls[0].url).toBe('/auth/invite')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ email: 'bob@example.com', role: 'viewer' })
  })

  // authd's refusal names the way through — invite as operator or viewer,
  // then promote — and that sentence is the useful part, so it is surfaced
  // rather than replaced with a generic failure.
  it("surfaces authd's own message on a refusal", async () => {
    stubFetch(new Response('an invitation may only create an operator or a viewer', { status: 400 }))
    await expect(inviteAccount('bob@example.com', 'viewer')).rejects.toThrow(/operator or a viewer/)
  })
})

describe('acceptInvitation', () => {
  it('resolves on 204', async () => {
    const calls = stubFetch(new Response(null, { status: 204 }))
    await acceptInvitation('sealed')
    expect(calls[0].url).toBe('/auth/invite/accept')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ token: 'sealed' })
  })

  it('says the link is spent on 410, rather than repeating the status', async () => {
    stubFetch(new Response('this invitation has already been used', { status: 410 }))
    await expect(acceptInvitation('sealed')).rejects.toThrow(/already been used/)
  })

  it('rejects on an expired or forged link', async () => {
    stubFetch(new Response('this invitation link is invalid or has expired', { status: 401 }))
    await expect(acceptInvitation('sealed')).rejects.toThrow(/invalid or has expired/)
  })
})

describe('listCredentials', () => {
  it("reads the caller's own keys", async () => {
    const calls = stubFetch(
      json({ credentials: [{ id: 'k1', label: 'YubiKey 5C', addedAt: '2026-09-09T10:00:00Z', signCount: 3 }] }),
    )
    const keys = await listCredentials()
    expect(keys).toHaveLength(1)
    expect(keys[0].label).toBe('YubiKey 5C')
    expect(calls[0].url).toBe('/auth/credentials')
  })

  it("escapes the address when reading someone else's", async () => {
    const calls = stubFetch(json({ credentials: [] }))
    await listCredentials('bob+test@example.com')
    expect(calls[0].url).toBe('/auth/credentials?user=bob%2Btest%40example.com')
  })

  it('answers an empty list for an account with no keys', async () => {
    stubFetch(json({ credentials: [] }))
    expect(await listCredentials()).toEqual([])
  })
})

describe('revokeCredential', () => {
  it('deletes by id, escaping it into the path', async () => {
    const calls = stubFetch(new Response(null, { status: 204 }))
    await revokeCredential('key/with+chars')
    expect(calls[0].url).toBe('/auth/credentials/key%2Fwith%2Bchars')
    expect(calls[0].init?.method).toBe('DELETE')
  })

  // The 409 is the design's own guard — the last key of a passkey-only
  // account — and it is the one refusal a user can act on, so it must not be
  // flattened into "request failed".
  it('surfaces the refusal to strand an account', async () => {
    stubFetch(
      new Response(
        'refusing to remove an account\'s last credential: root@example.com has no password sign-in',
        { status: 409 },
      ),
    )
    await expect(revokeCredential('k1')).rejects.toThrow(/last credential/)
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
cd /home/rmocq/frame-lot0 && npx vitest run src/lib/accounts.test.ts src/lib/auth.test.ts
```

Expected: `Failed to resolve import "@/lib/accounts"`, and `identityFromToken is not a function` in `auth.test.ts`.

- [ ] **Step 3: Implement**

Append to `src/lib/auth.ts`, after `logout` and before `__resetForTests`:

```ts
/** Who a token says its holder is. */
export interface Identity {
  email: string
  groups: string[]
}

/**
 * Read the `email` and `groups` claims off an id_token.
 *
 * This decodes; it does not verify — and that is correct here and nowhere
 * else. The signature is checked by `frame-uiproxy` on every request the
 * token is sent with, and by the apiserver behind it. Nothing in the browser
 * is a security decision: hiding the Accounts screen from a non-admin is a
 * courtesy, and forging a token in devtools buys nothing, because every write
 * it would reveal is refused server-side by RBAC and by the FrameUser
 * admission webhook.
 *
 * The group is matched **unprefixed**. `GroupForRole` in
 * `internal/authd/issuer.go` mints `admins`; the `frame:` prefix is applied by
 * the proxy on the way to the apiserver, so it never appears in the claim the
 * browser holds.
 */
export function identityFromToken(token: string): Identity | undefined {
  const parts = token.split('.')
  if (parts.length !== 3) return undefined
  try {
    const payload = parts[1]
    const padded = payload + '='.repeat((4 - (payload.length % 4)) % 4)
    const claims = JSON.parse(atob(padded.replace(/-/g, '+').replace(/_/g, '/'))) as {
      email?: unknown
      groups?: unknown
    }
    if (typeof claims.email !== 'string' || claims.email === '') return undefined
    const groups = Array.isArray(claims.groups)
      ? claims.groups.filter((g): g is string => typeof g === 'string')
      : []
    return { email: claims.email, groups }
  } catch {
    // A malformed token is "not signed in enough to be an admin", not a crash:
    // the console must still render its login gate.
    return undefined
  }
}

/** True when the token's groups claim carries authd's admin group. */
export function isAdminToken(token: string): boolean {
  return identityFromToken(token)?.groups.includes('admins') ?? false
}
```

Create `src/lib/accounts.ts`:

```ts
/**
 * The console's account-management calls: invitations, and the keys an
 * account holds.
 *
 * Everything here is a function rather than a hook or a component method for
 * one reason: vitest runs with `environment: 'node'` and collects only
 * `.test.ts` files under `src/`, so a `.tsx` spec never executes. Logic that
 * matters lives where a test can reach it, and `AccountsView`,
 * `InviteAcceptView` and `PasskeysDialog` only call it.
 *
 * Errors carry authd's own message rather than a status code. Each of these
 * routes has exactly one refusal a person can act on — invite: "operator or
 * viewer only"; accept: "already been used"; revoke: "would leave the account
 * unreachable" — and rewriting those into "request failed" would throw away
 * the only useful part.
 */

/** One enrolled authenticator, as `GET /auth/credentials` returns it. */
export interface CredentialSummary {
  id: string
  label: string
  addedAt: string
  signCount: number
}

/**
 * The roles an invitation may create.
 *
 * Not `admin`, and not by preference: authd creates the FrameUser under its
 * own ServiceAccount, and the admission webhook refuses a `spec.role: admin`
 * create from anyone who is not already an admin. Promotion happens from the
 * Accounts screen instead, which writes through the apiserver under the
 * signed-in admin's own impersonated identity.
 */
export type InvitableRole = 'operator' | 'viewer'

async function failure(res: Response, fallback: string): Promise<Error> {
  const body = (await res.text()).trim()
  return new Error(body === '' ? `${fallback} (${res.status})` : body)
}

/**
 * The invitation token in the current URL, if this is the invitation page.
 *
 * Takes the location apart rather than reading it, so it is testable under
 * node — and scoped to `/invite`, so a stray `?token=` on any other screen
 * cannot divert the console into spending an invitation.
 */
export function inviteTokenFromLocation(pathname: string, search: string): string | undefined {
  if (pathname !== '/invite') return undefined
  const token = new URLSearchParams(search).get('token')
  return token ? token : undefined
}

/** Create an account with no credential; resolves to the link that gives it one. */
export async function inviteAccount(email: string, role: InvitableRole): Promise<string> {
  const res = await fetch('/auth/invite', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, role }),
  })
  if (!res.ok) throw await failure(res, 'could not create the invitation')
  const body = (await res.json()) as { url: string }
  return body.url
}

/**
 * Spend an invitation. On success the browser holds a fifteen-minute session
 * whose only use is `enrolPasskey` — long enough to enrol, short enough that a
 * forwarded link is not a standing account.
 */
export async function acceptInvitation(token: string): Promise<void> {
  const res = await fetch('/auth/invite/accept', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (!res.ok) throw await failure(res, 'this invitation could not be accepted')
}

/** The caller's enrolled keys, or another account's when `email` is given and the caller is an admin. */
export async function listCredentials(email?: string): Promise<CredentialSummary[]> {
  const query = email ? `?user=${encodeURIComponent(email)}` : ''
  const res = await fetch(`/auth/credentials${query}`)
  if (!res.ok) throw await failure(res, 'could not read the enrolled keys')
  const body = (await res.json()) as { credentials?: CredentialSummary[] }
  return body.credentials ?? []
}

/** Remove one enrolled key. Refused (409) if it would leave the account unreachable. */
export async function revokeCredential(id: string, email?: string): Promise<void> {
  const query = email ? `?user=${encodeURIComponent(email)}` : ''
  const res = await fetch(`/auth/credentials/${encodeURIComponent(id)}${query}`, { method: 'DELETE' })
  if (!res.ok) throw await failure(res, 'could not remove the key')
}
```

- [ ] **Step 4: Run the tests**

```bash
cd /home/rmocq/frame-lot0 && npx vitest run && npm run typecheck
```

Expected: every spec in `src/lib/accounts.test.ts` and `src/lib/auth.test.ts` green, the whole suite green, `tsc -b` clean.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add src/lib && git commit -m "feat(ui): the account transformations, where vitest can reach them"
```

---

### Task 8: The Accounts screen, and a page that spends an invitation

**Files:**
- Create: `src/components/AccountsView.tsx`
- Create: `src/components/InviteAcceptView.tsx`
- Modify: `src/components/PasskeysDialog.tsx` (replace the "enrolled this session" half, lines 16-19 and 103-130)
- Modify: `src/App.tsx` (`TabId` union line 104-131; the icon import block; `NAV`'s last group; `NAV_INDEX`; the session gate around lines 433-455; `renderTab`; the sidebar's `NAV` reference)

**Interfaces:**
- Consumes: `inviteAccount`, `acceptInvitation`, `listCredentials`, `revokeCredential`, `inviteTokenFromLocation`, `CredentialSummary`, `InvitableRole` (Task 7); `isAdminToken`, `currentSession`, `enrolPasskey`, `PasskeyCancelledError`, `Session` (`@/lib/auth`); `createFrameClient`/`frameListPath` conventions from `@/lib/frame-sdk` for the FrameUser list and the role/state writes.
- Produces: `AccountsView`, `InviteAcceptView`; `TabId` gains `'accounts'`.

**No component test, on purpose.** Vitest never runs `.tsx`. What replaces it is `npm run build`, which is not a formality here: `TabId` is a union, `NAV` is typed against it, and `renderTab`'s switch is exhaustive — so a nav entry with no `case`, a `case` with no nav entry, and a typo in either are all compile errors. Those are the three typed gates a screen has to pass through, and `tsc -b` is what checks them.

- [ ] **Step 1: Write the failing check**

There is no unit test to write; the failing check is the compiler, and it fails as soon as `TabId` carries a member `renderTab` does not handle. Establish that first by adding **only** the union member to `src/App.tsx`:

```ts
  | 'tasks'
  | 'accounts'
  | 'settings'
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd /home/rmocq/frame-lot0 && npm run typecheck
```

Expected: an error on `const unhandled: never = tab` in `renderTab`'s `default` branch (`src/App.tsx` line 552) — `'accounts'` is not assignable to `never`, because no `case` handles it. That is the gate doing its job; Step 3 walks through it.

- [ ] **Step 3: Implement**

Create `src/components/AccountsView.tsx`:

```tsx
import { FormEvent, useCallback, useEffect, useState } from 'react'
import {
  inviteAccount,
  listCredentials,
  revokeCredential,
  type CredentialSummary,
  type InvitableRole,
} from '@/lib/accounts'
import { frameListPath } from '@/lib/frame-sdk'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Trash, UserPlus } from '@phosphor-icons/react'

/**
 * Who can sign in, with what rights, and on which keys.
 *
 * Two different backends, and the split is not arbitrary. Roles and
 * `spec.state` are written straight to the apiserver through
 * `frame-uiproxy`, so they travel under the signed-in admin's own
 * impersonated identity — which is exactly what the FrameUser admission
 * webhook asks for when it refuses a role or state change from a non-admin,
 * and the only reason promoting someone to admin works at all. Invitations and
 * key revocation go to authd, which owns credentials and is the only writer of
 * `status.credentials`.
 *
 * The screen is reachable only for an admin (App gates the nav entry on the
 * token's groups claim). That gate is a courtesy: every write below is
 * refused server-side for anyone else.
 */
interface Account {
  name: string
  email: string
  role: string
  state: string
  keyCount: number
}

const FRAMEUSERS = frameListPath('frameusers')

/**
 * The accounts, with how many keys each holds.
 *
 * The count comes from authd rather than from `status.credentials` on the
 * FrameUser, even though the apiserver would return it in the same list this
 * function already makes. That is deliberate: `GET /auth/credentials` is the
 * one shape the console is allowed to see of a credential record — no
 * public-key material — and reading the raw status here would put material on
 * screen that the dedicated route exists to withhold. One request per account
 * is affordable: these are humans, not nodes.
 */
async function loadAccounts(): Promise<Account[]> {
  const res = await fetch(FRAMEUSERS, {
    headers: { Authorization: `Bearer ${(globalThis as Record<string, unknown>).__FRAME_TOKEN__ ?? ''}` },
  })
  if (!res.ok) throw new Error(`could not list accounts: ${res.status} ${await res.text()}`)
  const body = (await res.json()) as {
    items: Array<{ metadata: { name: string }; spec: { email: string; role: string; state?: string } }>
  }
  return Promise.all(
    body.items.map(async (i) => ({
      name: i.metadata.name,
      email: i.spec.email,
      role: i.spec.role,
      // An account written before spec.state existed reads back defaulted by
      // the apiserver; the fallback is for a response assembled anywhere else.
      state: i.spec.state ?? 'enabled',
      // A failure to read one account's keys must not blank the whole table:
      // -1 renders as "?" below, which is honest, where 0 would be a lie
      // about an account that may well hold keys.
      keyCount: await listCredentials(i.spec.email).then((k) => k.length).catch(() => -1),
    })),
  )
}

async function patchAccount(name: string, patch: Record<string, unknown>): Promise<void> {
  const res = await fetch(`${FRAMEUSERS}/${name}`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/merge-patch+json',
      Authorization: `Bearer ${(globalThis as Record<string, unknown>).__FRAME_TOKEN__ ?? ''}`,
    },
    body: JSON.stringify({ spec: patch }),
  })
  if (!res.ok) throw new Error(await res.text())
}

export function AccountsView() {
  const [accounts, setAccounts] = useState<Account[]>([])
  const [error, setError] = useState<string | undefined>(undefined)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<InvitableRole>('viewer')
  const [inviteLink, setInviteLink] = useState<string | undefined>(undefined)
  const [keysFor, setKeysFor] = useState<string | undefined>(undefined)
  const [keys, setKeys] = useState<CredentialSummary[]>([])

  const refresh = useCallback(() => {
    loadAccounts()
      .then((next) => {
        setAccounts(next)
        setError(undefined)
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
  }, [])

  useEffect(refresh, [refresh])

  async function act(fn: () => Promise<void>) {
    try {
      await fn()
      setError(undefined)
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleInvite(e: FormEvent) {
    e.preventDefault()
    try {
      // The link is shown, never sent: there is no mail path in this cluster.
      setInviteLink(await inviteAccount(inviteEmail.trim(), inviteRole))
      setError(undefined)
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  async function showKeys(email: string) {
    setKeysFor(email)
    try {
      setKeys(await listCredentials(email))
    } catch (err) {
      setKeys([])
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="font-mono text-sm">Accounts</CardTitle>
        <Button className="font-mono gap-1.5" onClick={() => { setInviteLink(undefined); setInviteOpen(true) }}>
          <UserPlus />
          Invite
        </Button>
      </CardHeader>
      <CardContent className="space-y-3">
        {error && (
          <p role="alert" className="text-xs font-mono text-destructive break-words">
            {error}
          </p>
        )}
        <table className="w-full text-xs font-mono">
          <thead className="text-muted-foreground">
            <tr>
              <th className="text-left font-normal">Email</th>
              <th className="text-left font-normal">Role</th>
              <th className="text-left font-normal">State</th>
              <th className="text-right font-normal">Keys</th>
            </tr>
          </thead>
          <tbody>
            {accounts.map((a) => (
              <tr key={a.name} className="border-t border-border">
                <td className="py-1.5">{a.email}</td>
                <td>
                  <select
                    className="bg-transparent"
                    value={a.role}
                    onChange={(e) => void act(() => patchAccount(a.name, { role: e.target.value }))}
                  >
                    <option value="viewer">viewer</option>
                    <option value="operator">operator</option>
                    <option value="admin">admin</option>
                  </select>
                </td>
                <td>
                  <Button
                    variant="outline"
                    size="sm"
                    className="font-mono"
                    onClick={() =>
                      void act(() =>
                        patchAccount(a.name, { state: a.state === 'enabled' ? 'disabled' : 'enabled' }),
                      )
                    }
                  >
                    {a.state}
                  </Button>
                </td>
                <td className="text-right">
                  <Button variant="ghost" size="sm" className="font-mono" onClick={() => void showKeys(a.email)}>
                    {a.keyCount < 0 ? '?' : a.keyCount} {a.keyCount === 1 ? 'key' : 'keys'}
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>

        {keysFor && (
          <div className="space-y-1.5 pt-2 border-t border-border">
            <p className="text-[10px] font-mono uppercase tracking-widest text-muted-foreground">
              Keys enrolled by {keysFor}
            </p>
            {keys.length === 0 ? (
              <p className="text-xs text-muted-foreground">No keys enrolled.</p>
            ) : (
              <ul className="space-y-1">
                {keys.map((k) => (
                  <li
                    key={k.id}
                    className="flex items-center justify-between rounded border border-border bg-secondary/30 px-2 py-1.5 text-xs font-mono"
                  >
                    <span className="truncate">{k.label || k.id}</span>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        void act(async () => {
                          await revokeCredential(k.id, keysFor)
                          setKeys(await listCredentials(keysFor))
                        })
                      }
                    >
                      <Trash />
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </CardContent>

      <Dialog open={inviteOpen} onOpenChange={setInviteOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="font-mono">Invite someone</DialogTitle>
            <DialogDescription>
              Creates an account with no credential and returns a link. Copy it to them yourself —
              Frame sends no mail. The link expires in 24 hours and dies the moment they enrol a key.
            </DialogDescription>
          </DialogHeader>
          <form className="space-y-3" onSubmit={handleInvite}>
            <div className="space-y-1.5">
              <Label htmlFor="invite-email">Email</Label>
              <Input
                id="invite-email"
                type="email"
                value={inviteEmail}
                onChange={(e) => setInviteEmail(e.target.value)}
                required
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="invite-role">Role</Label>
              <select
                id="invite-role"
                className="w-full bg-transparent border border-border rounded px-2 py-1.5 text-sm font-mono"
                value={inviteRole}
                onChange={(e) => setInviteRole(e.target.value as InvitableRole)}
              >
                <option value="viewer">viewer</option>
                <option value="operator">operator</option>
              </select>
              <p className="text-[10px] text-muted-foreground">
                An invitation cannot create an admin — authd acts under its own identity, and only an
                admin may mint one. Invite, then change the role in the table above.
              </p>
            </div>
            <Button type="submit" className="w-full font-mono" disabled={!inviteEmail.trim()}>
              Create invitation
            </Button>
          </form>
          {inviteLink && (
            <p className="text-xs font-mono break-all rounded border border-border bg-secondary/30 p-2">
              {inviteLink}
            </p>
          )}
          <DialogFooter>
            <Button variant="outline" className="font-mono" onClick={() => setInviteOpen(false)}>
              Close
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
```

Create `src/components/InviteAcceptView.tsx`:

```tsx
import { useState } from 'react'
import { acceptInvitation } from '@/lib/accounts'
import { currentSession, enrolPasskey, PasskeyCancelledError, type Session } from '@/lib/auth'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Cpu, Fingerprint } from '@phosphor-icons/react'

/**
 * Where an invitation is spent.
 *
 * The link's token buys one thing: a fifteen-minute session whose only use is
 * enrolling a key. So this screen does the two steps in one gesture — accept,
 * then immediately run the WebAuthn ceremony — rather than dropping the
 * invitee at a login screen they have no credential for. Once the key exists
 * the invitation is dead by construction (authd refuses it the moment the
 * account holds a credential), which is also why the URL is rewritten on the
 * way out: a reload of /invite?token=… would meet a 410 and read as a bug.
 */
export function InviteAcceptView({
  token,
  onEnrolled,
}: {
  token: string
  onEnrolled: (session: Session) => void
}) {
  const [label, setLabel] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)

  async function handleAccept() {
    setBusy(true)
    setError(undefined)
    try {
      await acceptInvitation(token)
      await enrolPasskey(label.trim() || 'Passkey')
      const session = await currentSession()
      if (!session) {
        setError('the key was enrolled but no session was issued — sign in from the console')
        return
      }
      globalThis.history.replaceState({}, '', '/')
      onEnrolled(session)
    } catch (err) {
      if (!(err instanceof PasskeyCancelledError)) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Cpu className="text-primary" weight="bold" />
            FRAME
          </CardTitle>
          <p className="text-xs text-muted-foreground">
            You have been invited. Enrol a passkey to finish — this link works once.
          </p>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="invite-key-label">Name this key</Label>
            <Input
              id="invite-key-label"
              placeholder="YubiKey 5C"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              disabled={busy}
              autoFocus
            />
          </div>
          {error && (
            <p role="alert" className="text-xs font-mono text-destructive break-words">
              {error}
            </p>
          )}
          <Button className="w-full font-mono gap-1.5" disabled={busy} onClick={() => void handleAccept()}>
            <Fingerprint />
            {busy ? 'Waiting for authenticator…' : 'Enrol a passkey'}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
```

In `src/components/PasskeysDialog.tsx`: delete the `EnrolledThisSession` interface and the `enrolled` state, import `listCredentials`, `revokeCredential` and `type CredentialSummary` from `@/lib/accounts`, hold `const [keys, setKeys] = useState<CredentialSummary[]>([])`, load them with `useEffect` whenever `open` becomes true, refresh after a successful `enrolPasskey`, and replace the "Enrolled this session" block with the real list plus a revoke button per row. Rewrite the component's doc comment: the caveat it carried — *"authd exposes no endpoint to list a user's previously enrolled credentials … Showing a full history here would be lying about what this screen can actually see"* — is now false, and `GET /auth/credentials` is the endpoint that was missing. Keep one honest sentence in its place:

```tsx
/**
 * Where a signed-in user manages their passkeys.
 *
 * The list is the account's real one, read from `GET /auth/credentials` —
 * lot 0c added that route, and this component's previous "enrolled this
 * session" caveat was the visible shape of its absence. Revoking is refused
 * by authd (409) when it would leave a passkey-only account with no way in,
 * so the last key cannot be removed by accident from here.
 */
```

In `src/App.tsx`:

1. Add `Users` to the existing `@phosphor-icons/react` import block.
2. Add the lazy import beside `TasksView`:
   ```ts
   const AccountsView = lazy(() => import('@/components/AccountsView').then((m) => ({ default: m.AccountsView })))
   ```
   and the eager one beside `LoginView`:
   ```ts
   import { InviteAcceptView } from '@/components/InviteAcceptView'
   ```
3. Import the two new helpers:
   ```ts
   import { currentSession, ensureToken, isAdminToken, logout, onSessionLost, type Session } from '@/lib/auth'
   import { inviteTokenFromLocation } from '@/lib/accounts'
   ```
4. Add the NAV item, in the last group, between `tasks` and `settings`:
   ```tsx
         {
           id: 'accounts',
           label: 'Accounts',
           icon: <Users />,
           description: 'Who can sign in, with what rights, and on which keys',
           tabs: [{ id: 'accounts', label: 'Accounts' }],
         },
   ```
5. Add the `renderTab` case beside `'tasks'`:
   ```tsx
       case 'accounts':
         return <AccountsView />
   ```
6. Derive admin-ness and the visible nav, with the other `useMemo`s and **before** the early returns, so the hook order never changes:
   ```tsx
   // The nav gate is a courtesy, not a control: the token is decoded, not
   // verified (see identityFromToken), and every write the screen makes is
   // refused server-side for a non-admin regardless. It also does not
   // re-evaluate mid-session — the five-minute refresh replaces the token
   // without touching sessionState — so a demotion shows up at the next sign
   // in, while its effect is immediate at the apiserver.
   const admin = useMemo(
     () => (sessionState.phase === 'signed-in' ? isAdminToken(sessionState.session.token) : false),
     [sessionState],
   )
   const visibleNav = useMemo(
     () =>
       NAV.map((group) => ({ ...group, items: group.items.filter((i) => i.id !== 'accounts' || admin) }))
         .filter((group) => group.items.length > 0),
     [admin],
   )
   const inviteToken = useMemo(
     () => inviteTokenFromLocation(globalThis.location.pathname, globalThis.location.search),
     [],
   )
   ```
   Then use `visibleNav` where the sidebar currently maps over `NAV` (`{NAV.map((group) => (` at line 605). `NAV_INDEX` (line 322) stays built from `NAV`, so a direct navigation still resolves.
7. Add the invitation gate, after the `checking` early return and before the `signed-out` one:
   ```tsx
   if (inviteToken && sessionState.phase !== 'signed-in') {
     return (
       <InviteAcceptView
         token={inviteToken}
         onEnrolled={(session) => setSessionState({ phase: 'signed-in', session })}
       />
     )
   }
   ```

- [ ] **Step 4: Run the checks**

```bash
cd /home/rmocq/frame-lot0 && npm run build && npx vitest run
```

Expected: `tsc -b` clean (the three typed gates satisfied), the Vite build succeeding, and the whole vitest suite green — `src/lib/auth.test.ts` in particular, since `auth.ts` gained exports.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add src && git commit -m "feat(ui): an Accounts screen, and a page that spends an invitation"
```

---

### Task 9: The documentation, and the one thing still unproven

**Files:**
- Modify: `docs/crd-reference.md` (the `## FrameUser` section, from line 507)
- Modify: `docs/api.md` (the `## Authentication` section, from line 9)
- Modify: `docs/deployment.md` (after the bootstrap steps, around lines 456-500)
- Modify: `docs/roadmap.md` (the "Current state" bullets, lines 32-34)
- Modify: `docs/superpowers/specs/2026-09-09-lot0c-account-management-design.md` (the `**Status:**` line)

**Interfaces:**
- Consumes: everything above. Produces no code.

- [ ] **Step 1: Write the failing check**

The check here is a reader's, not a compiler's, so make it explicit — these three claims must be findable in the docs after this task, and none of them is today:

```bash
cd /home/rmocq/frame-lot0 && grep -c 'spec.state' docs/crd-reference.md docs/deployment.md docs/roadmap.md ; grep -c '/auth/invite' docs/api.md docs/deployment.md
```

- [ ] **Step 2: Run it and watch it fail**

Expected: every count is `0`.

- [ ] **Step 3: Write the documentation**

**`docs/crd-reference.md`**, in the `## FrameUser` section: add `state` to the `**Spec:**` paragraph —

> `state` (`enabled` | `disabled`, **defaults to `enabled`**) decides whether `authd` may issue the account an identity at all. It is a flag rather than an absence on purpose: revoking every passkey would deactivate an account too, but destructively, and only re-enrolment in person would undo it. One function in `internal/authd` (`requireIssuable`) enforces it, and all four identity-issuing paths — `POST /auth/token`, password login, passkey login, invitation acceptance — call it. The load-bearing one is `/auth/token`: the console calls it every fifteen minutes, so disabling an account cuts a session that is already open within one token lifetime, without touching the cookie.

and extend the `**Webhook:**` paragraph —

> Validation also refuses a non-admin changing `spec.state`, for the same reason it refuses a role change, and refuses disabling the last admin. "Last admin" now means the last **enabled** admin: a disabled admin cannot obtain a token by any route, so counting one would allow the last usable admin to be demoted, deleted or switched off behind an account nobody can sign in to.

Add a note recording the freeze cost, since this is a field added to a frozen kind:

> **This field was added after the freeze**, on 2026-09-09, and it is a field on an existing kind rather than a new kind — a stronger break than NodeTuning's or FrameTask's. It ships with the four debts those raised already paid: the RBAC tiers for `frameusers` already exist, no controller was added, this reference moved with it, and it arrives with a test that proves the *effect* (a disabled account is refused an identity on all four paths) rather than the field's presence. It exists on both served versions and is carried in both directions of the conversion, so `v1alpha1` is not lossy for it.

**`docs/api.md`**, under `## Authentication`, add the route table (the existing `window.__FRAME_TOKEN__` prose describes the pre-`authd` world and should say so, but rewriting it is not this lot's job — add the table and a pointer):

| Route | Caller | Answer |
|---|---|---|
| `POST /auth/bootstrap` | one-shot token | Creates the first admin, then closes forever. |
| `POST /auth/login/password` | anyone | 204 + `frame_session` (12h). Disabled account: 401. |
| `POST /auth/login/begin` / `finish` | anyone | Usernameless WebAuthn. Disabled account: 401. |
| `POST /auth/token` | session | `{id_token, expires_in}`. Disabled account: 401. |
| `POST /auth/logout` | session | 204, clears the cookie. |
| `POST /auth/register/begin` / `finish` | session | Enrols a key for the session's owner, never for anyone named in the body. |
| `POST /auth/invite` | admin session | `{url}` — an account with no credential, plus a 24h single-use link. `role` is `operator` or `viewer` only. |
| `POST /auth/invite/accept` | invitation token | 204 + a 15-minute session. 410 once the account holds any credential. |
| `GET /auth/credentials` | session (`?user=` for an admin) | The enrolled keys, without public-key material. |
| `DELETE /auth/credentials/{id}` | own, or admin | 204; 409 if it would leave a passkey-only account with no way in. |

**`docs/deployment.md`**, after the bootstrap steps, a new subsection **"Inviting a second person"**:

1. Sign in as an admin, open **Accounts**, choose **Invite**, give an address and `viewer` or `operator`.
2. Copy the link and send it however you already talk to that person. Frame sends no mail: there is no mail path in this cluster, and adding one is a separate project.
3. They open the link, name a key, and enrol it. The link then dies — refused because the account holds a credential, not because a flag was flipped. Nothing needs cleaning up.
4. To make someone an admin, invite them as an operator or viewer first and change the role in the table. An invitation cannot create an admin: `authd` acts under its own ServiceAccount, and admission refuses a `spec.role: admin` create from anyone who is not already an admin — the guard that stops everything holding `create frameusers` from minting one.
5. To cut someone off, set their state to `disabled`. Their keys stay enrolled; re-enabling restores them without a new ceremony. An open session stops working within one token lifetime (15 minutes by default).

and, plainly labelled as **not yet executed**, the end-to-end check this lot exists to make possible:

> **The check that has never once been run end to end.** Invite a viewer. Accept it in a separate browser profile. Enrol a key. Sign in as them, attempt to cordon a node, and confirm a **403** and a `FrameTask` recording the refusal. Then disable the account from the admin's Accounts screen and confirm the viewer's console returns to the login gate within one token lifetime without anyone clearing a cookie. Until that has been done on the cluster, per-user identity is proven only in envtest and by `SubjectAccessReview`.

**`docs/roadmap.md`**, a bullet beside the NodeTuning and FrameTask entries in "Current state":

> - ✅ **`FrameUser.spec.state`**, added post-freeze (2026-09-09, lot 0c): the first *field* added to a frozen kind, as opposed to a new kind alongside them. On both served versions and carried both ways by the conversion, so nothing is lossy. It is what makes deactivation non-destructive — the alternative was revoking every passkey, which is reversible only by re-enrolment in person. See [crd-reference.md](crd-reference.md).

**The spec's status line**, in `docs/superpowers/specs/2026-09-09-lot0c-account-management-design.md`:

```markdown
**Status:** design approved 2026-09-09; implemented on `feat/lot0-identity-tasks`. The end-to-end cluster check in docs/deployment.md ("Inviting a second person") has not been executed.
```

- [ ] **Step 4: Run the check**

```bash
cd /home/rmocq/frame-lot0 && grep -c 'spec.state' docs/crd-reference.md docs/deployment.md docs/roadmap.md
cd /home/rmocq/frame-lot0 && grep -c '/auth/invite' docs/api.md docs/deployment.md
cd /home/rmocq/frame-lot0 && make test
cd /home/rmocq/frame-lot0 && npm run build && npx vitest run
```

Expected: every count non-zero, and the whole tree green — Go, envtest and frontend — one last time before the branch is offered for review.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-lot0 && git add docs && git commit -m "docs: inviting a second human, and the field that freezes with it"
```
