# Lot 2 — operating workloads from the console: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A person signed in to the Frame console can see every workload on the cluster, read a pod's logs, open a shell in a container, restart a deployment, scale it, delete a pod and edit a resource — under their own identity, with the writes recorded, and without reaching for `kubectl`.

**Architecture:** One thing has to happen before any of it: `patch` on Deployments was removed from this repository on 2026-08-10 as "the single grant that turned the unauthenticated UI into cluster-admin", and restart, scale and the manifest editor cannot exist without a verb of that shape. Task 6 earns it back the way that removal said to — `baseline` Pod Security enforced on the application namespaces, infrastructure namespaces deliberately exempt — and Task 7 binds every grant that can write a pod template with a `RoleBinding` into exactly the enforced namespaces, carrying no tier label so the aggregation cannot widen it. Restart, scale and saving a manifest therefore work on application workloads and 403 on infrastructure ones, and the screen says so. Reads stay cluster-wide: the console shows any workload's YAML anywhere the tree shows the workload, and writes only where the policy is enforced.

The rest keeps going through `frame-uiproxy` on the path it already serves. Three things in the proxy make that possible: `statusRecorder` gains `http.Hijacker` so Go's `ReverseProxy` can perform an HTTP upgrade; `FrameTask`'s `ObjectRef` gains `subresource` so an exec is distinguishable from a pod create; and the recorder learns to open a session-shaped record for a WebSocket exec and close it when the socket closes. A browser cannot set a request header on `new WebSocket()`, so the token rides the `base64url.bearer.authorization.k8s.io.` subprotocol, which the proxy consumes and strips. On the console side every piece of logic that can be tested lives in `src/lib` (`workloads.ts`, `exec-protocol.ts`, `pod-logs.ts`, `manifest-diff.ts`); the components are the untested shell around them, by construction.

**Tech Stack:** Go 1.26.1, `net/http` + `net/http/httputil`, controller-runtime v0.23.x, envtest + Ginkgo, kubebuilder v4 multigroup layout, React 19 + Vite + vitest (node environment), `@xterm/xterm`, Kubernetes Pod Security Admission, kustomize, Helm, nginx.

**Spec:** `docs/superpowers/specs/2026-09-09-lot2-workloads-design.md`, including the amendment in commit `2f5f408` ("Restart and scale require Pod Security first"), which this plan implements as Tasks 6 and 7.

## Global Constraints

- Module path is `github.com/rmocq/frame`. Go 1.26.1. **This lot adds no Go dependency.**
- **This lot adds exactly two npm dependencies**, both in `package.json` `dependencies`, both with an explicit upper bound as the repo requires: `"@xterm/xterm": ">=6.0.0,<7"` and `"@xterm/addon-fit": ">=0.11.0,<0.12"`. (`addon-fit` is pre-1.0, where a minor bump is the breaking one, so its ceiling is the next minor — the same thing `^0.11.0` would mean, spelled so the bound is visible.) **No other dependency, Go or npm.** In particular: no YAML library — the apiserver serialises YAML itself (`Accept: application/yaml`, `Content-Type: application/yaml`), which is why the manifest editor needs no parser.
- Vitest runs `environment: 'node'` with `include: ['src/**/*.test.ts']` — **`.tsx` specs never execute.** Anything needing a test lives in `src/lib/`. Component code carries no coverage by construction; do not write a `.tsx` test and believe it ran.
- The frontend publishes the session on `globalThis`, not `window` (`publish()` in `src/lib/auth.ts`). New code follows suit. Note that `bearerToken()` in `frame-sdk.ts` and `authHeaders()` in `frame-config.ts`/`k8s-watch.ts` still *read* `window`, which is why every test that touches them calls `vi.stubGlobal('window', globalThis)` — copy that, do not "fix" it in this lot.
- A token's `groups` claim is **unprefixed** (`admins`); `frame-uiproxy` adds the `frame:` prefix; the admission webhook matches the prefixed form. Browser code uses `isAdminToken` from `src/lib/auth.ts` and never string-matches `frame:admins`.
- Console writes go through `k8sFetch` (`src/lib/frame-sdk.ts`), which retries once on 401 with a fresh token and sends `X-Frame-Action`. **A bare `fetch` for a write is a defect** — it skips the retry and leaves no audit label. `src/lib/frame-sdk.test.ts` has a structural guard (`has no bare fetch() left in the module`) that fails on any `fetch(` not preceded by a letter or a dot; `globalThis.fetch(`, `proxyFetch(` and `k8sFetch(` are the three allowed spellings.
- **Namespaces: assert the full request path in tests, never `url.includes()`.** The Accounts screen shipped reading `frameusers` in `default` because a test matched a substring; the failure mode is a silently empty list. `frameListPath(plural, ns?)` / `coreListPath(plural, ns?)` take a namespace override. Any new client that pins a namespace must have its full path asserted, spelled out as a constant (see `FRAMEUSERS_PATH` in `src/lib/accounts.test.ts`).
- `config/rbac/*_role.yaml` and `charts/frame/templates/rbac-tier-roles.yaml` are hand-maintained copies of the same rules. **`make helm-parity` has a KNOWN pre-existing failure** on a cpu-request drift (`10m` vs `100m`) in files this branch does not touch — do not chase it; confirm the diff it reports is only that. This lot changes no per-kind tier role, so neither file is edited.
- **No verb that can write a pod template may be granted cluster-wide by this lot.** That means `patch` **and** `update` on `pods`, `deployments`, `statefulsets`, `daemonsets` and `jobs`, and the `scale` subresource with them. `patch` on `apps/deployments` was removed on 2026-08-10 as "the single grant that turned the unauthenticated UI into cluster-admin" (`deploy/kubernetes/base/rbac.yaml`), because RBAC cannot bound a write to one JSON path and a pod template can carry `privileged: true` with a `hostPath: /` volume — and the YAML editor's `update` is the same escalation through a different door. Task 6 enforces `baseline` Pod Security on the application namespaces; Task 7 binds all of them there and nowhere else, through two unaggregated ClusterRoles with **no tier label** and a `RoleBinding` per namespace. `pods/exec` is the one exception and stays cluster-wide, because a shell creates no pod and so bounding it by Pod Security would close nothing. Say what that means rather than hearing it as reassurance: a shell **inherits** whatever privilege the target pod holds, so exec into one of the deliberately privileged pods in an exempt namespace — Ceph, the node-tuning agent, the Talos tooling — is root on the node. That is why exec is admin-only and every session is recorded. Nothing later in the plan may relabel, aggregate or widen any of it.
- `deploy/kubernetes/base/rbac.yaml` requires **every rule to carry a comment naming its call site in `src/`**. Follow that discipline for every rule added. Rules that could not be tied to a call site were deleted from that file in an earlier audit; do not re-add anything speculatively.
- CRDs live in `config/crd/bases/` and `charts/frame/files/crds/`. `make manifests` regenerates the first and calls `make helm-sync-crds`; `make helm-crds-check` fails on drift. Never hand-edit either copy.
- `internal/controller/frame` bootstraps envtest through a Ginkgo suite (`TestControllers`) and Go orders test files alphabetically: **a plain `func TestX` in that package panics on a nil client.** Schema assertions go inside a `Describe`/`DescribeTable`.
- A Go doc comment line that begins with `+kubebuilder:` is swallowed by controller-gen as a marker and vanishes from the generated CRD description. **Never start a wrapped comment line with that token.**
- `FrameTask` is post-freeze, `v1beta1`-only, with **no conversion webhook and no `v1alpha1`** — so a new field on it needs no conversion function and cannot break `TestHubRoundTripIsLossless`.
- `FrameTaskSpec.Action` is capped at **200 characters** by the CRD. A longer value makes the apiserver reject the `FrameTask` create; the recorder logs the error and the user's write still succeeds — so the cost of overflowing it is a **silently missing audit record**, not a failed action.
- Go's `net/http` forces HTTP/1.1 for a request whose `Connection` contains `upgrade` **and** whose `Upgrade` header is exactly `websocket` (`Request.requiresHTTP1`), so the SA transport built by `rest.TransportFor` does not need an HTTP/2-free twin. The console must therefore send `Upgrade: websocket` — which is what a browser's `new WebSocket()` does — and nothing else.
- Test commands: `make test` (full; chains `manifests generate fmt vet crd-render setup-envtest`), `go test ./internal/...`, `go test ./test/manifests/`, `npx vitest run`, `npm run build`.
- **RBAC is testable and already has a harness.** `test/manifests` parses the shipped YAML (`config/rbac/*_role.yaml` + `deploy/kubernetes/base/rbac.yaml`), reconstructs what the controller-manager aggregates into `frame-viewer`/`frame-editor`/`frame-admin`, and asserts on it — `ClusterRoles`, `AggregatedRules`, `Grants`, `Access`. It exists because a whole lot once passed every per-task review and every Go test while the manifests granted nobody the rights to use any of it. Assert new grants there, not with `grep`.
- Work in `/home/rmocq/frame-lot2` on branch `feat/lot2-workloads`. Every command below assumes that directory.

---

### Task 1: `taskNamespace` — the Tasks screen reads where the recorder writes

Carried in from lot 0c's whole-branch review, and first because this lot puts more records into that screen than anything before it.

`frame-uiproxy` creates every `FrameTask` in `TASK_NAMESPACE`, which `cmd/uiproxy/main.go` defaults to `frame-system` and `deploy/kubernetes/base/deployment.yaml` sets to `frame-system`. The console reads `frameListPath('frametasks')`, which resolves through `frameNs()` to `config().frameNamespace`, whose default is `default`. The list has therefore been empty since the lot that shipped it, with no error — a 200 with `items: []`.

It gets a configuration field, not a hardcoded literal: a literal would be the second source of truth the same review rejected on `UserClient.ns`, and `TASK_NAMESPACE` is a deployment-time choice that an operator must be able to follow from the Settings screen.

**Files:**
- Modify: `src/lib/frame-config.ts` (`FrameConfig` interface after `frameNamespace`; `DEFAULT_CONFIG`; `merge()`)
- Modify: `src/lib/frame-sdk.ts` (new export `taskListPath` beside `frameListPath`; `FrameClient.tasks.list`)
- Modify: `src/components/TasksView.tsx` (the `useLiveResource` watch array and the import)
- Modify: `src/components/SettingsView.tsx` (one `Field` in the "API-addressed namespaces" card)
- Modify: `docs/deployment.md` (new subsection at the end of `## 4. Configure the UI for in-cluster auth`)
- Create: `src/lib/frame-config.test.ts`
- Test: `src/lib/frame-sdk.test.ts` (new `describe` block)

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```ts
  // src/lib/frame-config.ts
  interface FrameConfig { /* … */ taskNamespace: string }
  // DEFAULT_CONFIG.taskNamespace === 'frame-system'

  // src/lib/frame-sdk.ts
  export function taskListPath(): string
  // → `/apis/frame.plume-labs.io/v1beta1/namespaces/${config().taskNamespace}/frametasks`
  ```

- [ ] **Step 1: Write the failing tests**

Create `src/lib/frame-config.test.ts`:

```ts
import { describe, it, expect, vi, afterEach } from 'vitest'
import { config, DEFAULT_CONFIG, loadConfig } from './frame-config'

/**
 * `authHeaders()` in frame-config.ts reads a bare `window`, which does not
 * exist under vitest's `environment: 'node'` — referencing it throws a
 * ReferenceError before any assertion runs. Same stub every other suite that
 * reaches the SDK uses.
 */
function stubBrowser() {
  vi.stubGlobal('window', {})
}

function stubConfigMap(data: Record<string, string> | undefined) {
  stubBrowser()
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(JSON.stringify({ data }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        }),
    ),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('taskNamespace', () => {
  // The whole point of the field. frame-uiproxy's TASK_NAMESPACE defaults to
  // frame-system (cmd/uiproxy/main.go) and the shipped Deployment sets it to
  // frame-system, so a console that has never been configured must look there
  // — not at `default`, which is where frameNamespace points and where no
  // FrameTask has ever been written.
  it('defaults to frame-system, where the recorder writes', () => {
    expect(DEFAULT_CONFIG.taskNamespace).toBe('frame-system')
  })

  it('takes the stored value when the ConfigMap sets one', async () => {
    stubConfigMap({ 'config.json': JSON.stringify({ taskNamespace: 'frame-audit' }) })
    await loadConfig()
    expect(config().taskNamespace).toBe('frame-audit')
  })

  // A ConfigMap written before this field existed has no `taskNamespace` key.
  // `merge` must fall through to the default rather than let `undefined` reach
  // the path builder, which would produce
  // `/apis/.../namespaces/undefined/frametasks` — a 404 on every load.
  it('keeps the default when the stored config predates the field', async () => {
    stubConfigMap({ 'config.json': JSON.stringify({ frameNamespace: 'frame-system' }) })
    await loadConfig()
    expect(config().taskNamespace).toBe('frame-system')
  })
})
```

Append to `src/lib/frame-sdk.test.ts`:

```ts
// Carried in from lot 0c's whole-branch review. `frame-uiproxy` creates every
// FrameTask in TASK_NAMESPACE — `frame-system`, from cmd/uiproxy/main.go and
// deploy/kubernetes/base/deployment.yaml — while this client built the path
// from `config().frameNamespace`, whose default is `default`. The Tasks screen
// has shown an empty list since it shipped: a 200 with `items: []`, no error,
// nothing to notice.
//
// The path is spelled out in full, namespace segment included. A
// `url.includes('/frametasks')` assertion passes just as happily against
// `/apis/.../namespaces/default/frametasks`, which is the bug — the same
// substring trap that let the Accounts screen ship pointed at the wrong
// namespace (see FRAMEUSERS_PATH in accounts.test.ts).
describe('FrameTask reads', () => {
  const FRAMETASKS_PATH =
    '/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/frametasks'

  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  it('reads from the namespace the recorder writes to', async () => {
    vi.stubGlobal('window', globalThis)
    const urls: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        urls.push(String(input))
        return new Response(JSON.stringify({ items: [] }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }),
    )

    await createFrameClient().tasks.list()

    expect(urls).toEqual([`${FRAMETASKS_PATH}?limit=200`])
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
npx vitest run src/lib/frame-config.test.ts src/lib/frame-sdk.test.ts
```

Expected: `frame-config.test.ts` fails to compile (`taskNamespace` is not a property of `FrameConfig`), and — once that is momentarily satisfied — the FrameTask case fails with

```
- Expected  ["/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/frametasks?limit=200"]
+ Received  ["/apis/frame.plume-labs.io/v1beta1/namespaces/default/frametasks?limit=200"]
```

That `default` is the shipped defect, and it is the only thing distinguishing a working Tasks screen from an empty one.

- [ ] **Step 3: Add the field**

In `src/lib/frame-config.ts`, inside `interface FrameConfig`, directly after `frameNamespace`:

```ts
  /**
   * Namespace `frame-uiproxy` records FrameTasks into — its `TASK_NAMESPACE`
   * environment variable, defaulted to `frame-system` in
   * `cmd/uiproxy/main.go` and set explicitly in
   * `deploy/kubernetes/base/deployment.yaml`.
   *
   * Separate from `frameNamespace` because it is a different decision made by
   * a different component: the Frame CRs live wherever an operator puts them,
   * while the task trail lives wherever the proxy was told to write it. They
   * were the same value here, and were not on the cluster, so the Tasks screen
   * listed an empty collection with no error from the day it shipped.
   */
  taskNamespace: string
```

In `DEFAULT_CONFIG`, directly after `frameNamespace: 'default',`:

```ts
  taskNamespace: 'frame-system',
```

In `merge()`, directly after the `frameNamespace` line:

```ts
    taskNamespace: s.taskNamespace || DEFAULT_CONFIG.taskNamespace,
```

- [ ] **Step 4: Point the reads at it**

In `src/lib/frame-sdk.ts`, immediately after `frameListPath`:

```ts
/**
 * Where the FrameTask trail is read from.
 *
 * Not `frameListPath('frametasks')`: that resolves through `frameNs()` to
 * `config().frameNamespace`, and the proxy writes to `config().taskNamespace`.
 * One exported function rather than the literal repeated in the SDK and in
 * `TasksView`, so the screen and its watch can never drift apart.
 */
export function taskListPath(): string {
  return frameListPath('frametasks', config().taskNamespace)
}
```

In `FrameClient.tasks.list`, replace the URL:

```ts
      const res = await k8sFetch<{ items: FrameTaskCR[] }>(`${taskListPath()}?limit=${limit}`)
```

In `src/components/TasksView.tsx`, change the import and the watch array:

```ts
import { TaskRecord, createFrameClient, taskListPath } from '@/lib/frame-sdk'
```

```ts
  const { state, reload } = useLiveResource<TaskRecord[]>(
    () => frame.tasks.list(200),
    [],
    [taskListPath()],
  )
```

In `src/components/SettingsView.tsx`, inside the "API-addressed namespaces" `CardContent`, immediately after the `frame-ns` `Field`:

```tsx
          <Field
            id="task-ns"
            label="FrameTasks"
            hint="Must match frame-uiproxy's TASK_NAMESPACE (deployment.yaml)"
            value={draft.taskNamespace}
            onChange={(v) => setDraft((d) => ({ ...d, taskNamespace: v }))}
          />
```

- [ ] **Step 5: Document it**

In `docs/deployment.md`, at the end of `## 4. Configure the UI for in-cluster auth` (immediately before the `---` that precedes `## 5. Ingress`), add:

```markdown
### Where the console reads its own configuration

The console reads a ConfigMap, `cluster-control-config` in `cluster-control`,
at boot and merges it *over* its compiled defaults, so a fresh install works
with no ConfigMap and a stored config that predates a new field still boots
(`src/lib/frame-config.ts`). Every field is editable on the **Settings**
screen.

Two of its fields are namespaces that must match what is deployed, and they
are not the same namespace:

| Field | Default | Must match |
|---|---|---|
| `frameNamespace` | `default` | Wherever the Frame CRs (`FrameJob`, `FrameNode`, `SchedulingPolicy`, `FrameResourceQuota`) are created. |
| `taskNamespace` | `frame-system` | `frame-uiproxy`'s `TASK_NAMESPACE` (`deploy/kubernetes/base/deployment.yaml`), which is where every `FrameTask` is written. |

`taskNamespace` exists because those two were one value until 2026-09-09 and
were never the same on the cluster: the proxy wrote to `frame-system` and the
Tasks screen listed `default`, so it showed an empty table with no error from
the day it shipped. If you change `TASK_NAMESPACE`, change this field too —
nothing reconciles one against the other, and the symptom of a mismatch is
silence.
```

- [ ] **Step 6: Run the checks**

```bash
npx vitest run && npm run build
```

Expected: both new suites green, `tsc -b` clean (`SettingsView` now type-checks against a `FrameConfig` that has the field), and the Vite build succeeding.

- [ ] **Step 7: Commit**

```bash
git add src docs && git commit -m "fix(ui): read FrameTasks from the namespace the recorder writes them to"
```

---

### Task 2: `statusRecorder` implements `http.Hijacker`

`Proxy.ServeHTTP` wraps the `ResponseWriter` in `statusRecorder` so the `FrameTask` can be closed with the response's status code. `httputil.ReverseProxy` performs an HTTP upgrade by type-asserting the writer to `http.Hijacker`; when the assertion fails it hands the request to its error handler, which answers **502** with `can't switch protocols using non-Hijacker ResponseWriter type *uiproxy.statusRecorder`. So with the wrapper in place and no `Hijack`, every WebSocket in the product fails — including the one this lot is built on.

This is the same defect the same type already had with `http.Flusher`, whose fix carries a comment saying a wrapper without it "turns every watch into a buffered response". It will recur the next time someone wraps this writer for a good reason, so it ships with a test that fails when the method is removed.

**Files:**
- Modify: `internal/uiproxy/proxy.go` (imports; after `func (s *statusRecorder) Flush()`)
- Modify: `internal/uiproxy/recorder.go` (`Finish`, the phase decision)
- Test: `internal/uiproxy/proxy_test.go` (two new tests + one helper)
- Test: `internal/uiproxy/recorder_test.go` (one new test)

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  // internal/uiproxy/proxy.go
  func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error)
  // Sets s.code to http.StatusSwitchingProtocols (101) when it is still 0.
  ```
  Task 4 relies on `Finish` treating 101 as a success.

- [ ] **Step 1: Write the failing tests**

Add to `internal/uiproxy/proxy_test.go`. The imports this needs, added to the existing block: `bufio`, `fmt`, `net`, `strings`.

```go
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
```

`stubRecorder` is written to from the request goroutine and read from the test goroutine, so guard it. Replace the existing type with:

```go
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
```

`TestFinishRunsAndReportsFailureWhenTheProxiedCallPanics` reads the same two fields directly today; change it to `called, code := rec.finished()` as well, or `go test -race` will report the write from the request goroutine against the read from the test. Additional imports for the file: `io`, `sync`, `time`.

Add to `internal/uiproxy/recorder_test.go`:

```go
// A hijacked upgrade leaves 101 behind, which is below 200 and would read as
// a failure under a plain 2xx test. Every shell that opened successfully would
// be recorded as failed — the opposite of what the Tasks screen is for.
func TestFinishTreats101AsASuccessfulSession(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/neura/pods/api-0/exec", nil)
	name := rec.Start(context.Background(),
		Identity{User: "alice@example.com", Groups: []string{"admins"}}, req)
	if name == "" {
		t.Skip("Start does not yet record an exec upgrade; see Task 4")
	}

	rec.Finish(context.Background(), name, http.StatusSwitchingProtocols)

	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	if task.Status.Phase != framev1beta1.TaskPhaseSucceeded {
		t.Fatalf("Phase = %q, want Succeeded — 101 is a session that opened", task.Status.Phase)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/uiproxy/ -run 'Upgrade|101' -v
```

Expected: `TestCarriesAProtocolUpgradeThrough` fails with `got 502 (can't switch protocols using non-Hijacker ResponseWriter type *uiproxy.statusRecorder), want 101 — the upgrade never happened`, and `TestAnUpgradeIsRecordedAs101NotAsAServerError` fails the same way before it reaches its own assertion.

- [ ] **Step 3: Implement `Hijack`**

In `internal/uiproxy/proxy.go`, add `bufio` and `net` to the import block, then add immediately after `func (s *statusRecorder) Flush()`:

```go
// Hijack is what makes a protocol upgrade possible at all: httputil.ReverseProxy
// type-asserts the writer to http.Hijacker the moment the upstream answers 101,
// and hands the request to its error handler when the assertion fails — a 502
// reading "can't switch protocols using non-Hijacker ResponseWriter type
// *uiproxy.statusRecorder". A pod shell is a WebSocket, so without this there
// is no shell; the same wrapper had the same defect with Flush, whose comment
// above says it "turns every watch into a buffered response". Anyone wrapping
// this writer again must carry both methods forward.
//
// The status is set here rather than in WriteHeader because ReverseProxy writes
// the 101 response line directly to the hijacked connection's bufio.Writer and
// never calls WriteHeader at all. Left at 0, the deferred close in ServeHTTP
// would record every successful session as a 500. Hijack is only reached after
// the upstream answered 101, so naming that code here is a statement of fact,
// not a guess.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("uiproxy: the underlying ResponseWriter is not an http.Hijacker")
	}
	if s.code == 0 {
		s.code = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}
```

In `internal/uiproxy/recorder.go`, replace the phase decision in `Finish`:

```go
	phase := framev1beta1.TaskPhaseSucceeded
	// 101 is what a hijacked upgrade leaves behind (statusRecorder.Hijack): the
	// session opened. It is below 200, so a bare 2xx test would file every
	// successful shell as a failure.
	if httpCode != http.StatusSwitchingProtocols && (httpCode < 200 || httpCode >= 300) {
		phase = framev1beta1.TaskPhaseFailed
	}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./internal/uiproxy/ -v
```

Expected: all green. `TestFinishTreats101AsASuccessfulSession` still skips — Task 4 is what makes `Start` record a GET exec.

- [ ] **Step 5: Commit**

```bash
git add internal/uiproxy && git commit -m "fix(uiproxy): let the status wrapper hijack, or no upgrade can pass"
```

---

### Task 3: `ObjectRef.subresource`, kept by `parsePath` and rendered by the screen

`parsePath` currently reads `/api/v1/namespaces/neura/pods/api-0/exec` and keeps `pods`/`api-0`, discarding `exec`. So an exec would record as `create pods/api-0` — the same string a pod create produces. Without this field the decision to record exec sessions does not exist in practice.

`FrameTask` is post-freeze, `v1beta1`-only, with no conversion webhook, so an optional field costs no compatibility and needs no conversion function.

**Files:**
- Modify: `api/frame/v1beta1/frametask_types.go` (`ObjectRef`, after `Name`)
- Modify: `internal/uiproxy/recorder.go` (`parsePath` signature and body; its one caller in `Start`)
- Modify: `src/lib/frame-sdk.ts` (`FrameTaskCR`, `refLabel`)
- Test: `internal/uiproxy/recorder_test.go` (`TestParsePath`)
- Test: `internal/controller/frame/frametask_v1beta1_schema_test.go` (one new `It`)
- Test: `src/lib/frame-sdk.test.ts` (one new case in `describe('crToTask')`)
- Generated: `config/crd/bases/frame.plume-labs.io_frametasks.yaml`, `charts/frame/files/crds/frame.plume-labs.io_frametasks.yaml`, `api/frame/v1beta1/zz_generated.deepcopy.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  // api/frame/v1beta1
  type ObjectRef struct {
      Group       string `json:"group,omitempty"`
      Resource    string `json:"resource"`
      Namespace   string `json:"namespace,omitempty"`
      Name        string `json:"name"`
      Subresource string `json:"subresource,omitempty"` // new
  }

  // internal/uiproxy — signature CHANGED: the middle return value was the
  // resource, which every caller discarded.
  func parsePath(p string) (framev1beta1.ObjectRef, bool)
  ```
  ```ts
  // src/lib/frame-sdk.ts — FrameTaskCR.spec.target gains `subresource?: string`,
  // and refLabel renders it as a fourth path segment.
  ```

- [ ] **Step 1: Write the failing tests**

In `internal/uiproxy/recorder_test.go`, replace `TestParsePath` wholesale — the signature changes and two expectations gain a field:

```go
func TestParsePath(t *testing.T) {
	cases := []struct {
		path string
		want framev1beta1.ObjectRef
		ok   bool
	}{
		{"/api/v1/nodes/w2", framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"}, true},
		// The subresource is the difference between draining a pod and
		// deleting one, and between opening a shell and creating a pod.
		{"/api/v1/namespaces/neura/pods/api-0/eviction",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "eviction"}, true},
		{"/api/v1/namespaces/neura/pods/api-0/exec",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "exec"}, true},
		{"/api/v1/namespaces/neura/pods/api-0/log",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "log"}, true},
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs/j-1",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "j-1"}, true},
		{"/apis/apps/v1/namespaces/neura/deployments/api/scale",
			framev1beta1.ObjectRef{Group: "apps", Resource: "deployments", Namespace: "neura", Name: "api", Subresource: "scale"}, true},
		// A create has no name in the path; the record still has to exist.
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "-"}, true},
		{"/healthz", framev1beta1.ObjectRef{}, false},
	}
	for _, tc := range cases {
		got, ok := parsePath(tc.path)
		if ok != tc.ok {
			t.Fatalf("%s: ok = %v", tc.path, ok)
		}
		if ok && got != tc.want {
			t.Fatalf("%s: got %+v, want %+v", tc.path, got, tc.want)
		}
	}
}
```

In `internal/controller/frame/frametask_v1beta1_schema_test.go`, add `"k8s.io/apimachinery/pkg/types"` to the imports and this `It` at the end of the existing `Describe`:

```go
	// Asserted through a round trip against a real apiserver, not on the Go
	// struct, because that is the only thing that can fail. A CRD that was not
	// regenerated *prunes* an unknown field silently: the create succeeds and
	// the value comes back empty. `subresource: ""` on the read is exactly what
	// a forgotten `make manifests` looks like, and nothing else in the tree
	// would notice it.
	It("stores target.subresource, so an exec is not recorded as a pod create", func() {
		obj := &framev1beta1.FrameTask{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "task-exec-", Namespace: "default"},
			Spec: framev1beta1.FrameTaskSpec{
				User: "alice@example.com",
				Verb: "create",
				Target: framev1beta1.ObjectRef{
					Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "exec",
				},
			},
		}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })

		back := &framev1beta1.FrameTask{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: obj.Name, Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.Target.Subresource).To(Equal("exec"),
			"the apiserver pruned it — config/crd/bases was not regenerated")
	})
```

In `src/lib/frame-sdk.test.ts`, inside `describe('crToTask')`:

```ts
  // The whole reason ObjectRef gained a subresource. Rendered without it, a
  // shell opened in a pod and a pod created from the console print the same
  // target, and the Tasks screen cannot tell one from the other.
  it('renders the subresource, so an exec reads as an exec', () => {
    const t = crToTask({
      metadata: { name: 'task-exec' },
      spec: {
        user: 'alice@example.com',
        verb: 'create',
        action: 'open a shell in neura/api-0 (api)',
        target: { resource: 'pods', namespace: 'neura', name: 'api-0', subresource: 'exec' },
      },
    })
    expect(t.target).toBe('neura/pods/api-0/exec')
  })
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/uiproxy/ -run TestParsePath
npx vitest run src/lib/frame-sdk.test.ts -t 'renders the subresource'
```

Expected: the Go test fails to compile (`parsePath` returns three values, `ObjectRef` has no field `Subresource`); the vitest case fails with `expected 'neura/pods/api-0' to be 'neura/pods/api-0/exec'`.

- [ ] **Step 3: Add the field**

In `api/frame/v1beta1/frametask_types.go`, inside `ObjectRef`, after `Name`:

```go
	// Subresource is the trailing segment of the request path when there is
	// one — "exec", "log", "scale", "eviction" — and empty for a request on
	// the object itself.
	//
	// Without it a shell opened in a pod records as "create pods/<name>", the
	// same string a pod create produces, so the decision to record exec
	// sessions has no effect anyone can see. FrameTask is post-freeze,
	// v1beta1-only and has no conversion webhook, so an optional field costs
	// no compatibility and needs no conversion function.
	//
	// 63 is the longest subresource Kubernetes has; the cap is here so a
	// hand-built path cannot store an arbitrary string in the audit trail.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Subresource string `json:"subresource,omitempty"`
```

Beware: no wrapped comment line above may begin with `+kubebuilder:` — controller-gen would eat it as a marker and it would vanish from the CRD description.

In `internal/uiproxy/recorder.go`, replace `parsePath` and fix its caller:

```go
// parsePath turns a Kubernetes request path into a reference.
//
//	/api/v1/nodes/w2
//	/api/v1/namespaces/{ns}/{resource}[/{name}[/{subresource}]]
//	/apis/{group}/{version}/namespaces/{ns}/{resource}[/{name}[/{sub}]]
//
// A create has no name in its path; the record still needs one field to
// print, so it gets "-".
//
// It used to return the resource as a second value, which every caller
// discarded — the reference already carries it.
func parsePath(p string) (framev1beta1.ObjectRef, bool) {
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
		return framev1beta1.ObjectRef{}, false
	}
	if len(rest) >= 2 && rest[0] == "namespaces" && len(rest) > 2 {
		ref.Namespace = rest[1]
		rest = rest[2:]
	}
	if len(rest) == 0 {
		return framev1beta1.ObjectRef{}, false
	}
	ref.Resource = rest[0]
	ref.Name = "-"
	if len(rest) >= 2 {
		ref.Name = rest[1]
	}
	if len(rest) >= 3 {
		ref.Subresource = rest[2]
	}
	return ref, true
}
```

and in `Start`:

```go
	ref, ok := parsePath(r.URL.Path)
```

In `src/lib/frame-sdk.ts`, widen `FrameTaskCR` and `refLabel`:

```ts
interface FrameTaskCR {
  metadata: { name: string; creationTimestamp?: string }
  spec: {
    user: string; verb: string; action?: string
    target: { group?: string; resource: string; namespace?: string; name: string; subresource?: string }
  }
  status?: { phase?: string; httpCode?: number; startedAt?: string; finishedAt?: string }
}

/**
 * `namespace/resource/name[/subresource]`, matching how FrameTaskSpec.ObjectRef
 * names things: plural resources off the request path, not Kinds. The
 * subresource is the difference between "create pods/api-0" — a pod being
 * created — and "create pods/api-0/exec", a shell being opened in one.
 */
function refLabel(r: {
  resource: string
  namespace?: string
  name: string
  subresource?: string
}): string {
  const base = r.namespace ? `${r.namespace}/${r.resource}/${r.name}` : `${r.resource}/${r.name}`
  return r.subresource ? `${base}/${r.subresource}` : base
}
```

- [ ] **Step 4: Regenerate the manifests**

```bash
make manifests generate
git diff --stat config/crd/bases charts/frame/files/crds api/frame/v1beta1/zz_generated.deepcopy.go
```

Expected: `config/crd/bases/frame.plume-labs.io_frametasks.yaml` and `charts/frame/files/crds/frame.plume-labs.io_frametasks.yaml` both gain a `subresource` property with `maxLength: 63` under `spec.target`. Confirm the description survived (a marker-eaten comment would leave the property with no `description:`):

```bash
grep -A4 'subresource:' config/crd/bases/frame.plume-labs.io_frametasks.yaml
make helm-crds-check
```

Expected: the property carries a description, and `helm-crds-check` reports no drift.

- [ ] **Step 5: Run the tests**

```bash
go test ./internal/uiproxy/ && make test && npx vitest run src/lib/frame-sdk.test.ts
```

Expected: all green, including the envtest `It` — which is the one that proves the *shipped* CRD carries the field rather than only the Go struct.

- [ ] **Step 6: Commit**

```bash
git add api config charts internal src && git commit -m "feat(frametask): record the subresource, so an exec is not a pod create"
```

---

### Task 4: the session-shaped `FrameTask`

A shell is not a request that returns; it is a session that lasts. The recorder opens a `FrameTask` when the WebSocket is established and closes it when the socket closes, so a three-hour shell is visible as a three-hour shell. Task 2 already made the *closing* work (the deferred `Finish` fires when the hijacked copy ends, with 101); this task makes the *opening* happen at all.

Three things stand in the way, and each is a small, testable rule:

1. **A browser opens an exec with a GET.** `new WebSocket()` can issue nothing else, and `isMutating` is false for GET, so nothing is recorded today. The proxy must recognise an exec upgrade and record it.
2. **The verb must be `create`.** That is what the apiserver authorizes (`create pods/exec`) whatever HTTP method carries the request, and the record must use the same word the RBAC rule uses or the two cannot be read together.
3. **A browser cannot set `X-Frame-Action` on a WebSocket.** `new WebSocket()` takes a URL and a subprotocol list and nothing else. The label every other write supplies cannot exist here, so the recorder builds it from the path and the `container` query parameter.

This task also adds the one rule the manifest editor depends on: **a `dryRun` request leaves no `FrameTask`.** The YAML editor validates an edit with `PUT ?dryRun=All` before it computes the audit label for the real write (Task 13), and a dry run changes nothing — recording it would double every edit in the trail with a row that did nothing.

**Files:**
- Modify: `internal/uiproxy/proxy.go` (`ServeHTTP`, plus a new `isExecUpgrade`)
- Modify: `internal/uiproxy/recorder.go` (`verbFor` → `verbForRequest`, `Start`, new `execAction` and `isDryRun`)
- Test: `internal/uiproxy/recorder_test.go`
- Test: `internal/uiproxy/proxy_test.go`

**Interfaces:**
- Consumes: `parsePath(p string) (framev1beta1.ObjectRef, bool)` and `ObjectRef.Subresource` (Task 3); `statusRecorder.Hijack` and `Finish`'s 101 handling (Task 2).
- Produces:
  ```go
  // internal/uiproxy
  func isExecUpgrade(r *http.Request) bool
  func isDryRun(q url.Values) bool
  func execAction(ref framev1beta1.ObjectRef, q url.Values) string
  ```
  Task 13's manifest editor relies on `?dryRun=All` leaving no record.

- [ ] **Step 1: Write the failing tests**

Add to `internal/uiproxy/recorder_test.go` (imports gain `net/url`):

```go
// The one action in the product that most deserves a record. A browser opens
// an exec as a WebSocket upgrade, which `new WebSocket()` can only issue as a
// GET — so the mutating-method test that gates every other record says no, and
// without this rule a shell leaves nothing behind at all.
func TestStartRecordsAnExecOpenedOverAWebSocket(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/namespaces/neura/pods/api-0/exec?container=api&stdin=true&stdout=true&tty=true", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	name := rec.Start(context.Background(),
		Identity{User: "alice@example.com", Groups: []string{"admins"}}, req)
	if name == "" {
		t.Fatal("an exec session left no record")
	}
	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	// `create` is the verb the apiserver authorizes for pods/exec whatever
	// method carries it. Recording "get" would make the trail disagree with
	// the RBAC rule that allowed it.
	if task.Spec.Verb != framev1beta1.TaskVerbCreate {
		t.Fatalf("Verb = %q, want create", task.Spec.Verb)
	}
	if task.Spec.Target.Subresource != "exec" {
		t.Fatalf("Subresource = %q, want exec", task.Spec.Target.Subresource)
	}
	if task.Spec.Action != "open a shell in neura/api-0 (api)" {
		t.Fatalf("Action = %q", task.Spec.Action)
	}
	if task.Status.StartedAt == nil {
		t.Fatal("StartedAt not set — a session with no start has no duration")
	}
}

// A plain GET must stay unrecorded. Reads are not recorded by design, and the
// exec rule is the narrowest possible exception to that: an upgrade, on the
// exec subresource. Widening it to "any GET on pods" would put a row in the
// trail for every screen refresh in the console.
func TestStartStillIgnoresOrdinaryReads(t *testing.T) {
	rec, _ := newRecorderFixture(t)
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"plain list", httptest.NewRequest(http.MethodGet, "/api/v1/pods", nil)},
		{"pod log", httptest.NewRequest(http.MethodGet,
			"/api/v1/namespaces/neura/pods/api-0/log?follow=true", nil)},
		{"exec path with no upgrade", httptest.NewRequest(http.MethodGet,
			"/api/v1/namespaces/neura/pods/api-0/exec", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if name := rec.Start(context.Background(), Identity{User: "a@b.c"}, tc.req); name != "" {
				t.Fatalf("recorded %q for a read", name)
			}
		})
	}
}

// The manifest editor PUTs the edited object once with ?dryRun=All to learn
// what the apiserver would store — that is how it computes the changed field
// paths for the real write's label. A dry run changes nothing, so recording it
// would put two rows in the trail for one edit, one of which did nothing.
func TestStartIgnoresADryRun(t *testing.T) {
	rec, _ := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPut,
		"/apis/apps/v1/namespaces/neura/deployments/api?dryRun=All", nil)
	if name := rec.Start(context.Background(), Identity{User: "a@b.c"}, req); name != "" {
		t.Fatalf("recorded %q for a dry run", name)
	}
}

// The same PUT without the parameter is a real write and must be recorded —
// otherwise "skip dry runs" is indistinguishable from "skip PUTs".
func TestStartRecordsTheSamePutWithoutDryRun(t *testing.T) {
	rec, _ := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPut,
		"/apis/apps/v1/namespaces/neura/deployments/api", nil)
	if rec.Start(context.Background(), Identity{User: "a@b.c"}, req) == "" {
		t.Fatal("a real update left no record")
	}
}

func TestExecActionNamesThePodAndContainer(t *testing.T) {
	ref := framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "exec"}
	if got := execAction(ref, url.Values{"container": []string{"api"}}); got != "open a shell in neura/api-0 (api)" {
		t.Fatalf("got %q", got)
	}
	if got := execAction(ref, url.Values{}); got != "open a shell in neura/api-0" {
		t.Fatalf("got %q", got)
	}
	// FrameTaskSpec.Action is capped at 200 characters by the CRD, and a
	// container name arrives from a URL. Over the cap the apiserver refuses the
	// FrameTask create outright, the recorder logs it, and the session runs
	// with no record at all — the failure is silence, not an error the user
	// sees.
	long := execAction(ref, url.Values{"container": []string{strings.Repeat("x", 400)}})
	if len(long) > 200 {
		t.Fatalf("action is %d characters, over the CRD's 200-character cap", len(long))
	}
}
```

Add to `internal/uiproxy/proxy_test.go`:

```go
// ServeHTTP decides what gets recorded, and it must reach the same conclusion
// the recorder does. Left on `isMutating` alone, the exec upgrade is a GET and
// Start is never called — so the recorder's own rule above would be dead code
// and no shell would appear on the Tasks screen.
func TestServeHTTPOpensARecordForAnExecUpgrade(t *testing.T) {
	up := echoUpgrade(t)
	defer up.Close()
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	rec := &stubRecorder{startName: "task-shell"}
	p, err := New(Options{
		Verifier: stubVerifier{id: Identity{User: "alice@example.com", Groups: []string{"admins"}}},
		Recorder: rec,
		Upstream: u,
	})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	res, conn, _ := dialUpgrade(t, strings.TrimPrefix(front.URL, "http://"),
		"/api/v1/namespaces/neura/pods/api-0/exec?container=api")
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("got %d, want 101", res.StatusCode)
	}
	_ = conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		called, code := rec.finished()
		if called {
			if code != http.StatusSwitchingProtocols {
				t.Fatalf("finishedCode = %d, want 101", code)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no record was opened or closed for the exec session")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
```

Delete the temporary `r.Method = http.MethodPatch` line from `TestAnUpgradeIsRecordedAs101NotAsAServerError` (Task 2) and let it use `httptest.NewServer(p)` directly — this task is what makes it record without the nudge. Remove the `t.Skip` from `TestFinishTreats101AsASuccessfulSession` too.

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/uiproxy/ -run 'Exec|DryRun|Ignores|101' -v
```

Expected: `execAction` and `isDryRun` are undefined (compile failure); once stubbed, `TestStartRecordsAnExecOpenedOverAWebSocket` fails with `an exec session left no record`, and `TestServeHTTPOpensARecordForAnExecUpgrade` with `no record was opened or closed for the exec session`.

- [ ] **Step 3: Implement the rules**

In `internal/uiproxy/recorder.go`, add `net/url` and `fmt` to the imports, rename `verbFor` and add the two helpers:

```go
func verbForMethod(method string) string {
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

// verbForRequest is verbForMethod plus the one case where the HTTP method is
// not what the apiserver authorizes: an exec arrives from a browser as a GET
// (`new WebSocket()` can issue nothing else) and is authorized as `create
// pods/exec`. The record says `create` so it can be read against the RBAC rule
// that allowed it.
func verbForRequest(r *http.Request, ref framev1beta1.ObjectRef) string {
	if ref.Subresource == "exec" && isExecUpgrade(r) {
		return framev1beta1.TaskVerbCreate
	}
	return verbForMethod(r.Method)
}

// maxAction is FrameTaskSpec.Action's CRD cap. Past it the apiserver refuses
// the FrameTask create, the recorder logs the error, and the action proceeds
// with no record — the cost of overflowing is a silent hole in the trail.
const maxAction = 200

// execAction names the session the way a person would. The console cannot
// supply an X-Frame-Action here: `new WebSocket()` takes a URL and a
// subprotocol list, and no header, so the label every other write carries has
// nowhere to travel and the recorder builds it instead.
func execAction(ref framev1beta1.ObjectRef, q url.Values) string {
	s := fmt.Sprintf("open a shell in %s/%s", ref.Namespace, ref.Name)
	if c := q.Get("container"); c != "" {
		s = fmt.Sprintf("%s (%s)", s, c)
	}
	if len(s) > maxAction {
		s = s[:maxAction]
	}
	return s
}

// isDryRun reports whether the request asked the apiserver to validate without
// storing. A dry run changes nothing, so it is not a write to record: the
// manifest editor makes one before every real edit, to learn what would be
// stored, and recording both would put two rows in the trail for one action.
func isDryRun(q url.Values) bool {
	for _, v := range q["dryRun"] {
		if v != "" {
			return true
		}
	}
	return false
}
```

Replace `Start`'s opening:

```go
func (t *TaskRecorder) Start(ctx context.Context, id Identity, r *http.Request) string {
	ref, ok := parsePath(r.URL.Path)
	if !ok {
		return ""
	}
	q := r.URL.Query()
	if isDryRun(q) {
		return ""
	}
	verb := verbForRequest(r, ref)
	if verb == "" {
		return ""
	}
	action := r.Header.Get("X-Frame-Action")
	if action == "" && ref.Subresource == "exec" && isExecUpgrade(r) {
		action = execAction(ref, q)
	}
	task := &framev1beta1.FrameTask{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "task-", Namespace: t.ns},
		Spec: framev1beta1.FrameTaskSpec{
			User:   id.User,
			Verb:   verb,
			Target: ref,
			Action: action,
		},
	}
	// From here the function is unchanged: Create, then a Status().Update
	// setting Phase=Running and StartedAt, then `return task.Name`.
```

In `internal/uiproxy/proxy.go`, add `isExecUpgrade` beside `isMutating`:

```go
// isExecUpgrade reports whether this is the console opening a shell.
//
// Narrow on purpose. Reads are not recorded, and this is the one exception:
// it requires a GET *and* a WebSocket upgrade *and* the exec subresource. Any
// two of the three would let ordinary traffic through — "GET on pods" is every
// screen refresh in the console, and an unrecorded exec is exactly the thing
// this lot exists to prevent.
func isExecUpgrade(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	ref, ok := parsePath(r.URL.Path)
	return ok && ref.Subresource == "exec"
}
```

and widen the recording gate in `ServeHTTP`:

```go
	sr := &statusRecorder{ResponseWriter: w}
	var task string
	// An exec is a GET, so isMutating alone would leave the longest-lived and
	// most privileged action in the product with no record at all.
	if p.recorder != nil && (isMutating(r.Method) || isExecUpgrade(r)) {
		task = p.recorder.Start(r.Context(), id, r)
	}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./internal/uiproxy/ -v
```

Expected: all green, `TestFinishTreats101AsASuccessfulSession` included (it no longer skips), and `TestStartRecordsTheUserAndAction` unchanged — the PATCH path still records with the caller's own label.

- [ ] **Step 5: Commit**

```bash
git add internal/uiproxy && git commit -m "feat(uiproxy): record a shell as a session, and never a dry run"
```

---

### Task 5: the bearer token on a WebSocket, and the two hops that must carry an upgrade

`Proxy.ServeHTTP` reads the token from `Authorization`. A browser cannot set that header on a WebSocket — `new WebSocket(url, protocols)` takes a URL and a subprotocol list and nothing else — so every exec the console opens would be rejected with the proxy's own 401 before it reached anything.

Kubernetes has a convention for exactly this: the client offers an extra subprotocol, `base64url.bearer.authorization.k8s.io.<unpadded-base64url-token>`, alongside the real one. The apiserver's own WebSocket handler consumes and strips it. Here the token is authd's, verified by this proxy and useless to the apiserver, so **the proxy consumes it and must strip it too**: forwarded, it would make the apiserver reject the handshake for an unknown subprotocol, and would put a live credential in the apiserver's audit log.

Two hops in front of the proxy also have to be taught to carry an upgrade: nginx does not forward `Upgrade`/`Connection` by default (they are hop-by-hop), and Vite's dev proxy needs `ws: true`.

**Files:**
- Create: `internal/uiproxy/websocket.go`
- Create: `internal/uiproxy/websocket_test.go`
- Modify: `internal/uiproxy/proxy.go` (`ServeHTTP`, the token lookup)
- Modify: `deploy/docker/nginx.conf` (a `map` above `server`, and the `/api/`+`/apis/` blocks)
- Modify: `vite.config.ts` (`ws: true` on the two apiserver proxies)
- Test: `internal/uiproxy/proxy_test.go`

**Interfaces:**
- Consumes: `dialUpgrade`/`echoUpgrade` from Task 2's test file.
- Produces:
  ```go
  // internal/uiproxy
  const bearerProtocolPrefix = "base64url.bearer.authorization.k8s.io."
  func tokenFromProtocols(values []string) (token string, remaining []string)
  ```
  Task 9 builds the browser side of the same convention (`execSubprotocols`).

- [ ] **Step 1: Write the failing tests**

Create `internal/uiproxy/websocket_test.go`:

```go
package uiproxy

import (
	"reflect"
	"testing"
)

func TestTokenFromProtocols(t *testing.T) {
	const tok = "header.payload.signature"
	// Unpadded base64url of the token above. A subprotocol token cannot
	// contain "=", so the padded form is not merely untidy — the browser
	// refuses to send it and the handshake never leaves the tab.
	const enc = "aGVhZGVyLnBheWxvYWQuc2lnbmF0dXJl"

	cases := []struct {
		name      string
		values    []string
		wantTok   string
		wantRest  []string
	}{
		{
			// What a browser sends: one header, comma-separated.
			name:     "one header two protocols",
			values:   []string{"v4.channel.k8s.io, " + bearerProtocolPrefix + enc},
			wantTok:  tok,
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			// What a Go client sends: Header.Add twice.
			name:     "two headers",
			values:   []string{"v4.channel.k8s.io", bearerProtocolPrefix + enc},
			wantTok:  tok,
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			name:     "no bearer entry",
			values:   []string{"v4.channel.k8s.io"},
			wantTok:  "",
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			// Undecodable is not a token. Returning the raw text would put a
			// caller-controlled string into Verify, and a padded or otherwise
			// malformed value must fail closed.
			name:     "undecodable bearer entry",
			values:   []string{"v4.channel.k8s.io, " + bearerProtocolPrefix + "not!base64url"},
			wantTok:  "",
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			name:     "no header at all",
			values:   nil,
			wantTok:  "",
			wantRest: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTok, gotRest := tokenFromProtocols(tc.values)
			if gotTok != tc.wantTok {
				t.Fatalf("token = %q, want %q", gotTok, tc.wantTok)
			}
			if len(gotRest) != len(tc.wantRest) || (len(gotRest) > 0 && !reflect.DeepEqual(gotRest, tc.wantRest)) {
				t.Fatalf("remaining = %v, want %v", gotRest, tc.wantRest)
			}
		})
	}
}
```

Add to `internal/uiproxy/proxy_test.go` (imports gain `encoding/base64`):

```go
// A browser cannot set Authorization on a WebSocket, so without this the
// exec handshake is refused by the proxy's own 401 and the terminal never
// opens for anyone. Remove the subprotocol lookup and this test reports 401.
func TestAcceptsTheBearerTokenFromTheWebSocketSubprotocol(t *testing.T) {
	up := echoUpgrade(t)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com", Groups: []string{"admins"}}}, up.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	enc := base64.RawURLEncoding.EncodeToString([]byte("a.b.c"))
	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// No Authorization header — exactly what a browser can send.
	req := "GET /api/v1/namespaces/neura/pods/api-0/exec HTTP/1.1\r\n" +
		"Host: frame.test\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Protocol: v4.channel.k8s.io, " + bearerProtocolPrefix + enc + "\r\n\r\n"
	if _, err := fmt.Fprint(conn, req); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("got %d (%s), want 101", res.StatusCode, body)
	}
}

// The console's token is authd's, not a credential the apiserver accepts.
// Forwarded, the apiserver's own WebSocket handler would see a subprotocol it
// did not offer and refuse the handshake — and the token would land in the
// apiserver's audit log for every shell anyone opens.
func TestStripsTheBearerSubprotocolBeforeForwarding(t *testing.T) {
	var seen http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com"}}, up.URL)

	enc := base64.RawURLEncoding.EncodeToString([]byte("a.b.c"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/neura/pods/api-0/exec", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "v4.channel.k8s.io, "+bearerProtocolPrefix+enc)
	p.ServeHTTP(httptest.NewRecorder(), req)

	got := seen.Get("Sec-WebSocket-Protocol")
	if strings.Contains(got, bearerProtocolPrefix) {
		t.Fatalf("the console's bearer token reached the apiserver: %q", got)
	}
	if got != "v4.channel.k8s.io" {
		t.Fatalf("Sec-WebSocket-Protocol = %q, want v4.channel.k8s.io — the real protocol must survive", got)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/uiproxy/ -run 'Protocols|Subprotocol' -v
```

Expected: `tokenFromProtocols` and `bearerProtocolPrefix` undefined (compile failure); once stubbed, the acceptance test reports `got 401 ({"kind":"Status",...,"message":"not signed in: the request carried no bearer token. Sign in again."}), want 101`.

- [ ] **Step 3: Implement it**

Create `internal/uiproxy/websocket.go`:

```go
package uiproxy

import (
	"encoding/base64"
	"strings"
)

// bearerProtocolPrefix carries a bearer token as a WebSocket subprotocol.
//
// It exists because `new WebSocket(url, protocols)` is the only WebSocket a
// browser can open, and it accepts no request headers — so the Authorization
// header every other request in the console carries has nowhere to go. This is
// Kubernetes' own convention for the problem (the same constant lives in
// k8s.io/apiserver/pkg/authentication/request/websocket), which is why the
// console's client needs no special case: it offers the entry, and whoever
// authenticates the request consumes it.
//
// Here that is this proxy, not the apiserver: the token is authd's, and the
// apiserver would neither accept it nor recognise the subprotocol. So the
// entry is removed on the way through — see tokenFromProtocols.
const bearerProtocolPrefix = "base64url.bearer.authorization.k8s.io."

// tokenFromProtocols pulls the bearer entry out of a Sec-WebSocket-Protocol
// header and returns the decoded token plus every other protocol the client
// offered, in order.
//
// `values` is Header.Values("Sec-WebSocket-Protocol"): a client may send one
// header with a comma-separated list (what a browser does) or several headers
// (what Header.Add does), and both are one list.
//
// The encoding is unpadded base64url — RFC 6455 forbids "=" in a subprotocol
// token, so the padded form never reaches here from a real client. An entry
// that does not decode yields no token rather than its raw text: the value is
// caller-controlled, and a malformed credential must fail closed.
func tokenFromProtocols(values []string) (string, []string) {
	var token string
	var remaining []string
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !strings.HasPrefix(p, bearerProtocolPrefix) {
				remaining = append(remaining, p)
				continue
			}
			if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p, bearerProtocolPrefix)); err == nil {
				token = string(raw)
			}
		}
	}
	return token, remaining
}
```

In `internal/uiproxy/proxy.go`, replace the opening of `ServeHTTP`:

```go
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	// A WebSocket carries its token as a subprotocol instead, because the
	// browser API that opens one accepts no headers. Whatever the source, the
	// entry is removed here: the apiserver did not offer that subprotocol and
	// would refuse the handshake, and forwarding it would write a live
	// credential into the apiserver's audit log on every shell.
	if wsToken, remaining := tokenFromProtocols(r.Header.Values("Sec-WebSocket-Protocol")); wsToken != "" {
		if tok == "" {
			tok = wsToken
		}
		r.Header.Del("Sec-WebSocket-Protocol")
		if len(remaining) > 0 {
			r.Header.Set("Sec-WebSocket-Protocol", strings.Join(remaining, ", "))
		}
	}
	if tok == "" {
		unauthorized(w, "the request carried no bearer token")
		return
	}
	// From here the function is unchanged: Verify, stripImpersonation, the
	// Authorization delete, the two Impersonate-* headers, the statusRecorder
	// and the recorder gate.
```

- [ ] **Step 4: Teach nginx and Vite to carry an upgrade**

In `deploy/docker/nginx.conf`, above `server {`:

```nginx
# `Connection: upgrade` must be sent only for requests that are upgrades. Sent
# unconditionally it would appear on every proxied read; sent never — the
# default, since Connection and Upgrade are hop-by-hop and nginx drops them —
# the exec handshake dies here and the browser sees whatever the apiserver
# makes of a request with no upgrade, never a 101.
#
# `map` belongs to the http context, which is where this file is included from
# (/etc/nginx/conf.d/default.conf, see the repo Dockerfile), so it goes above
# the server block rather than inside it.
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

and in **both** the `location /api/` and `location /apis/` blocks, after `proxy_set_header Host $host;`:

```nginx
        # Pod exec is a WebSocket, and pod logs with follow=true are a stream
        # that can sit silent for a long time.
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        # The default proxy_read_timeout is 60s, measured between reads — it
        # would cut a terminal nobody has typed into for a minute, and a log
        # follow on a quiet pod.
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
```

Do not touch `proxy_buffering`: watches already stream through this configuration in production, so buffering is demonstrably not in the way, and turning it off changes every other response on these two paths for no evidence.

In `vite.config.ts`, add `ws: true` to the two apiserver proxies:

```ts
      '/api': { target: 'http://localhost:8001', changeOrigin: true, ws: true },
      '/apis': { target: 'http://localhost:8001', changeOrigin: true, ws: true },
```

- [ ] **Step 5: Run the checks**

```bash
go test ./internal/uiproxy/ -v
npm run build
podman run --rm -v "$PWD/deploy/docker/nginx.conf:/etc/nginx/conf.d/default.conf:ro,Z" nginx:alpine nginx -t
```

Expected: Go green; `tsc -b` and the Vite build clean; `nginx -t` reporting `syntax is ok` / `test is successful`. The nginx check is worth running rather than eyeballing — a `map` in the wrong context fails the config outright and would take the whole console down on the next deploy, not just the terminal.

- [ ] **Step 6: Commit**

```bash
git add internal/uiproxy deploy/docker/nginx.conf vite.config.ts && git commit -m "feat(uiproxy): take the bearer token off the WebSocket subprotocol"
```

---

### Task 6: Pod Security on the application namespaces

**This task must land before Task 7, and Task 7 must not be started without it.** Task 7 grants `patch` on Deployments and StatefulSets. That grant was removed from this repository on 2026-08-10 and `deploy/kubernetes/base/rbac.yaml` documents the removal at length: it is "the single grant that turned the unauthenticated UI into cluster-admin", because patching a pod template to add `securityContext.privileged: true` and a `hostPath: /` volume is root on the node, and **no namespace on this cluster carries a `pod-security.kubernetes.io/enforce` label to stop it**. RBAC cannot bound a patch to one JSON path, so "may set the restartedAt annotation" and "may make the pod privileged" are the same grant.

The same comment names the safe way to earn it back, and this lot takes it: enforce Pod Security first, then grant the patch only where the privileged payload is refused at admission.

**Enforcing does not evict anything.** It refuses the *next* admission. A workload that has always violated the policy keeps running and fails the next time it restarts — which is during an incident, not during this change. That is why this task labels `warn` and `audit` first, reads the violations, resolves them, and only then flips to `enforce`. Two commits, in that order. Do not collapse them.

**Files:**
- Create: `deploy/kubernetes/pod-security/kustomization.yaml`
- Create: `deploy/kubernetes/pod-security/namespaces.yaml`
- Modify (Step 6, second commit): `deploy/kubernetes/pod-security/namespaces.yaml`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```
  deploy/kubernetes/pod-security/namespaces.yaml
  ```
  the single source of truth for **which namespaces are enforced**, one `kind: Namespace`
  document per namespace, each with `metadata.name` on its own line. Task 7 binds a
  RoleBinding into exactly these namespaces and Task 8 mirrors the list into
  `OPERABLE_NAMESPACES` in `src/lib/workloads.ts`; both have tests that read this file, so its
  shape is load-bearing — keep one `  name: <ns>` line per document and nothing else indented
  two spaces under `metadata`.

  The enforced set, derived below:
  `default`, `inference`, `neura`, `neura-batch`, `neura-database`, `neura-inference`, `neura-training`.

- [ ] **Step 1: Enumerate, from the manifests rather than from memory**

Do not guess which namespaces can take `baseline`. Read what actually ships:

```bash
grep -rn -A3 '^kind: Namespace' deploy/ --include='*.yaml' | grep -E 'name:'
grep -rhoP '^\s+namespace:\s*\K[a-z0-9-]+' deploy/ --include='*.yaml' | sort -u
for f in $(grep -rl -E 'privileged: true|hostPID: true|hostNetwork: true|hostPath:' deploy/ --include='*.yaml'); do
  echo "$(grep -m1 -oP '^\s+namespace:\s*\K[a-z0-9-]+' "$f") :: $f"
done | sort -u
```

The third command is the one that decides. Its output at the time of writing, minus one false
positive (`deploy/kubernetes/base/rbac.yaml` matches only because its comment quotes
`hostPath: /`):

| Namespace | Why it cannot take `baseline` |
|---|---|
| `kube-system` | `node-tuning-agent` (`hostPID`, the whole point of the agent), `kmod-rdma-loader`, `nvidia-mps`, `cpu-manager-policy`, `dpdk-init`, `multus`, `rdma-device-plugin`, `rdma-network-tuning`, `sriov-device-plugin` |
| `monitoring` | `node-exporter` (`hostPath` on `/proc`, `/sys`), `dcgm-exporter` |
| `alluxio` | `burst-buffer-nvme` (`hostPath` on the NVMe mount) |
| `cilium` | the CNI |
| `ptp` | `ptp-sync` (`hostNetwork`, for PTP hardware timestamps) |

Four more hold privileged workloads that are installed by their own operator or chart rather
than by a manifest in this repository, so the grep cannot see them and they are classified by
what they are: `rook-ceph` (OSDs are privileged with `hostPath` block devices), `gpu-operator`
(the NVIDIA driver and container-toolkit DaemonSets), `node-feature-discovery` (the worker reads
the host), `velero` (the node agent mounts `/var/lib/kubelet/pods`), plus `falco` and `tetragon`
(both eBPF/kernel), and `checkpoint-system` (CRIU needs privileged).

Everything else — `argo`, `argocd`, `flux-system`, `volcano-system`, `yunikorn`, `redis-cache`,
`data-fabric`, `sriov-network-operator`, `cluster-control`, `frame-system` — is left alone too,
for a different and simpler reason: **not labelling a namespace also means not granting it.**
The RoleBinding in Task 7 goes only where the label goes, so an unlabelled namespace is exactly
as operable from the console as it is today, which is not at all. There is no need to decide
whether Argo could survive `baseline` in order to ship this.

That leaves the application namespaces, which are what the Workloads screen exists to operate:
`default`, `inference`, `neura`, `neura-batch`, `neura-database`, `neura-inference`,
`neura-training`. None of them appears in the grep above.

- [ ] **Step 2: Write the manifest, in warn-and-audit mode only**

Create `deploy/kubernetes/pod-security/namespaces.yaml`. **No `enforce` label in this commit** —
that is Step 6, after the violations have been read:

```yaml
# Pod Security Admission on the application namespaces.
#
# This exists to earn back one RBAC grant. `patch` on apps/deployments and
# apps/statefulsets was removed on 2026-08-10 (see
# ../base/rbac.yaml) because patching a pod template to add
# `securityContext.privileged: true` and a `hostPath: /` volume is root on the
# node, and nothing on this cluster refused such a pod. RBAC cannot bound a
# patch to one JSON path, so the only way to hold "may restart a deployment"
# without also holding "may take the node" is to make the second one fail at
# admission. That is what `baseline` does.
#
# ── WHICH NAMESPACES ARE HERE, AND WHICH ARE DELIBERATELY NOT ────────────────
#
# Only application namespaces. Infrastructure namespaces are absent because
# their workloads legitimately need what `baseline` forbids: kube-system runs
# the node-tuning agent with hostPID and six host-network/host-path
# DaemonSets, monitoring runs node-exporter on hostPath /proc, alluxio mounts
# the NVMe device, ptp needs hostNetwork, cilium is the CNI, and rook-ceph,
# gpu-operator, node-feature-discovery, velero, falco, tetragon and
# checkpoint-system all ship privileged pods from their own charts. Labelling
# any of them would refuse their pods at the next restart and take the cluster
# apart one node at a time.
#
# Absence here is not a gap. The RoleBinding that grants restart and scale
# (../base/rbac-workload-operator.yaml) is bound namespace by namespace into
# exactly this list, so a namespace that is not labelled is also not granted —
# it stays as operable from the console as it is today, which is not at all.
#
# ── VERSION PIN ──────────────────────────────────────────────────────────────
#
# `*-version: v1.29` rather than the default `latest`, so that upgrading the
# cluster cannot silently tighten what is admitted. v1.29 is `Chart.yaml`'s own
# `kubeVersion` floor (">=1.29.0-0"), so it can only ever be at or below the
# running version — a pin above it would be ignored with a warning. `baseline`
# has been stable since 1.25; there is no behaviour being given up here.
#
# ── ORDER ────────────────────────────────────────────────────────────────────
#
# `warn` and `audit` only, on purpose, until the violations have been read.
# Enforcing does not evict a running pod; it refuses the next admission — so a
# workload that has always violated the policy keeps running and fails the next
# time it restarts, which is during an incident rather than during this change.
# `enforce` is added in a second commit, after `kubectl label
# --dry-run=server` has been run against every namespace below and come back
# clean. See docs/deployment.md, "Turning Pod Security on".
apiVersion: v1
kind: Namespace
metadata:
  name: default
  labels:
    pod-security.kubernetes.io/warn: baseline
    pod-security.kubernetes.io/warn-version: v1.29
    pod-security.kubernetes.io/audit: baseline
    pod-security.kubernetes.io/audit-version: v1.29
---
apiVersion: v1
kind: Namespace
metadata:
  name: inference
  labels:
    pod-security.kubernetes.io/warn: baseline
    pod-security.kubernetes.io/warn-version: v1.29
    pod-security.kubernetes.io/audit: baseline
    pod-security.kubernetes.io/audit-version: v1.29
---
apiVersion: v1
kind: Namespace
metadata:
  name: neura
  labels:
    pod-security.kubernetes.io/warn: baseline
    pod-security.kubernetes.io/warn-version: v1.29
    pod-security.kubernetes.io/audit: baseline
    pod-security.kubernetes.io/audit-version: v1.29
---
apiVersion: v1
kind: Namespace
metadata:
  name: neura-batch
  labels:
    pod-security.kubernetes.io/warn: baseline
    pod-security.kubernetes.io/warn-version: v1.29
    pod-security.kubernetes.io/audit: baseline
    pod-security.kubernetes.io/audit-version: v1.29
---
apiVersion: v1
kind: Namespace
metadata:
  name: neura-database
  labels:
    pod-security.kubernetes.io/warn: baseline
    pod-security.kubernetes.io/warn-version: v1.29
    pod-security.kubernetes.io/audit: baseline
    pod-security.kubernetes.io/audit-version: v1.29
---
apiVersion: v1
kind: Namespace
metadata:
  name: neura-inference
  labels:
    pod-security.kubernetes.io/warn: baseline
    pod-security.kubernetes.io/warn-version: v1.29
    pod-security.kubernetes.io/audit: baseline
    pod-security.kubernetes.io/audit-version: v1.29
---
apiVersion: v1
kind: Namespace
metadata:
  name: neura-training
  labels:
    pod-security.kubernetes.io/warn: baseline
    pod-security.kubernetes.io/warn-version: v1.29
    pod-security.kubernetes.io/audit: baseline
    pod-security.kubernetes.io/audit-version: v1.29
```

Create `deploy/kubernetes/pod-security/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

# Its own kustomize target, NOT wired into `../base`, for the same reason
# `../containment` is not: `../base/kustomization.yaml` used to carry a
# namespace transformer, and a Namespace object rewritten by one is renamed
# rather than relocated. It also has to be applyable and reversible on its own,
# because the two-phase rollout below is the whole point.
#
# Apply order — and the middle step is not optional:
#
#   1. kubectl apply -k deploy/kubernetes/pod-security     # warn + audit
#   2. read the violations (see docs/deployment.md, "Turning Pod Security on")
#      and resolve every one
#   3. add the `enforce` labels, apply again
#   4. kubectl apply -f deploy/kubernetes/base/rbac-workload-operator.yaml
#
# Doing 4 before 3 hands an operator the escalation this exists to close.
# Doing 3 before 2 breaks a deploy at the next restart of whatever was already
# violating, which will not be today.
#
# No `namespace:` field: every object here is a Namespace and names itself.

resources:
  - namespaces.yaml
```

```bash
kubectl --dry-run=client apply -k deploy/kubernetes/pod-security
```

Expected: seven `namespace/... configured (dry run)` lines and no error. This only checks that the YAML parses and the objects are well formed; it contacts nothing.

- [ ] **Step 3: Commit the warn-and-audit phase**

```bash
git add deploy/kubernetes/pod-security && git commit -m "feat(security): warn and audit baseline Pod Security on the application namespaces"
```

- [ ] **Step 4: Apply it and read the violations**

This step touches the cluster. It is the reason the task exists, and it must be done before Step 6.

```bash
kubectl apply -k deploy/kubernetes/pod-security
```

Then ask the apiserver, for each namespace, what `enforce` *would* refuse. A server-side dry run
of the enforce label makes PodSecurity evaluate every pod already running in the namespace and
return a warning naming each one and the rule it breaks — which is the only way to learn this
without restarting anything:

```bash
for ns in default inference neura neura-batch neura-database neura-inference neura-training; do
  echo "── $ns"
  kubectl label --dry-run=server --overwrite namespace "$ns" \
    pod-security.kubernetes.io/enforce=baseline \
    pod-security.kubernetes.io/enforce-version=v1.29
done
```

A clean namespace prints only `namespace/<ns> labeled (server dry run)`. A dirty one prints, before that line:

```
Warning: existing pods in namespace "neura" violate the new PodSecurity enforce level "baseline:v1.29"
Warning: api-7d9f8-x1: hostPath volumes
```

The `audit` label from Step 2 is the other half, and it is the one that catches a violating pod
created *after* this sweep: it writes a `pod-security.kubernetes.io/audit-violations` annotation
into the apiserver's audit log. That log is only readable if audit logging is configured on this
cluster's apiserver, so treat the dry run above as the primary instrument and the audit
annotation as the backstop:

```bash
kubectl get events -A --field-selector reason=FailedCreate -o wide | head -30
```

- [ ] **Step 5: Resolve every violation found**

**Treat this as part of the task, not as a formality.** If Step 4 printed nothing, record that —
`kubectl label --dry-run=server` output for all seven namespaces, pasted into the commit message
of Step 7 — and move on. If it printed something, each violation is one of a small set and each
has a fix that does not need `baseline` relaxed:

| Warning | What it means | Fix |
|---|---|---|
| `hostPath volumes` | a pod mounts a host directory | replace with a PVC, or move the workload to an unlabelled namespace |
| `privileged` | `securityContext.privileged: true` | the workload is infrastructure and belongs in an unlabelled namespace |
| `host namespaces` | `hostNetwork`/`hostPID`/`hostIPC` | same |
| `non-default capabilities` | `capabilities.add` beyond the baseline set | drop the capability, or justify moving the namespace out of the enforced list |
| `hostPort` | a container binds a host port | use a Service |

If a violation cannot be fixed, the honest resolution is to **remove that namespace from
`namespaces.yaml`**, not to weaken the policy — and then it is also not granted in Task 7, and
Task 8's `OPERABLE_NAMESPACES` must lose it too or the drift test there fails. That coupling is
deliberate: a namespace that cannot take `baseline` is a namespace where the Restart button must
not appear.

- [ ] **Step 6: Add `enforce`, and only now**

Re-run Step 4's dry-run loop and confirm it is clean. Then add the two enforce labels to **every**
document in `deploy/kubernetes/pod-security/namespaces.yaml`, beside the warn and audit ones:

```yaml
    pod-security.kubernetes.io/enforce: baseline
    pod-security.kubernetes.io/enforce-version: v1.29
```

and update the file's `── ORDER ──` comment block to record that the pass was run:

```yaml
# `enforce` was added on <date>, after `kubectl label --dry-run=server` came
# back clean for all seven namespaces. `warn` and `audit` are kept alongside it
# deliberately: enforce refuses, warn explains, and a person applying a rejected
# manifest wants both.
```

```bash
kubectl apply -k deploy/kubernetes/pod-security
kubectl get ns default inference neura neura-batch neura-database neura-inference neura-training \
  -o custom-columns='NS:.metadata.name,ENFORCE:.metadata.labels.pod-security\.kubernetes\.io/enforce'
```

Expected: `baseline` in every row. A blank cell is a namespace the apply did not reach, and it is
also a namespace where Task 7's RoleBinding must not go.

Then prove the policy actually refuses the payload it exists to refuse — this is the
discriminating check for the whole task, and the one thing that distinguishes "labels applied"
from "escalation closed":

```bash
kubectl -n neura run pod-security-probe --image=busybox --restart=Never --dry-run=server \
  --overrides='{"spec":{"hostPID":true,"containers":[{"name":"probe","image":"busybox","securityContext":{"privileged":true}}]}}' \
  -- sleep 1
```

Expected: a **refusal**, naming `violates PodSecurity "baseline:v1.29"`, `privileged` and `host
namespaces`. If this command succeeds, the label is not doing anything and Task 7 must not be
started — that is the exact state the 2026-08-10 removal was protecting against.

- [ ] **Step 7: Commit the enforce phase**

```bash
git add deploy/kubernetes/pod-security && git commit -m "feat(security): enforce baseline Pod Security, after a clean warn pass"
```

Put the Step 4 dry-run output, or "clean for all seven", in the commit body. It is the evidence
that the order was respected, and it is the only place that evidence will exist.

---

### Task 7: RBAC for the three tiers

The spec's table:

| Tier | Gains |
|---|---|
| viewer | nothing new for *pod state* — but the tree also shows DaemonSets and Jobs, which the viewer role does not currently grant, and it attaches a Deployment's pods through their ReplicaSet, which it does not grant either. Those three reads are viewer-tier. |
| operator | `pods/log` (get) and `pods` (delete) at the tier; `deployments`/`statefulsets` **patch** and their `scale` subresource **not at the tier at all** — see below |
| admin | `pods/exec` (create) at the tier; **update/patch** on exactly the kinds the Workloads screen shows — `pods`, `deployments`, `statefulsets`, `daemonsets`, `jobs` — **not at the tier either**, for the same reason |

Four things about this file before touching it.

**Every rule carries a comment naming its call site in `src/`.** That is the discipline the 2026-08-10 audit left behind, and rules that could not be tied to one were deleted rather than kept defensively.

**There is no admin-tier ClusterRole here yet.** `frame-admin` aggregates `tier: admin`, which today is satisfied only by the twenty-seven per-kind Frame CRD roles. The console's admin grants need a new `cluster-control-admin` ClusterRole carrying that label — put it in this file beside its two siblings, not in `config/rbac/`, which is controller-gen's and per-kind.

**Restart, scale and the manifest editor are a namespaced exception, and they will not fit the pattern the rest of this file uses.** Every other grant here is a labelled ClusterRole picked up by one of the three aggregated `frame-*` roles, which makes it cluster-wide. `patch` on Deployments cannot be cluster-wide: it was removed on 2026-08-10 as "the single grant that turned the unauthenticated UI into cluster-admin", because patching a pod template to add `securityContext.privileged: true` and a `hostPath: /` volume is root on the node. Task 6 closes that by enforcing `baseline` Pod Security — **but only on the application namespaces**, because Ceph, the node-tuning agent, the CNI and the Talos tooling legitimately need privileged pods and labelling their namespaces would take the cluster apart.

So a cluster-wide grant would hand the escalation straight back through the exempt namespaces, which is the entire hole. The grant is therefore a **`RoleBinding` per enforced namespace**, in a file of its own, carrying **no tier label at all** — an unaggregated, namespaced exception with its reason written down beside it. These are the only rules in this repository shaped that way, and that is the point: the shape is what confines them to the namespaces where the payload is refused at admission.

**The YAML editor's `update`/`patch` goes the same way, and this is not scope creep.** Cluster-wide, it is the identical escalation through a different door: an admin writes `privileged: true` and a `hostPath: /` volume into a pod template in an exempt namespace and holds node root. Leaving it cluster-wide while namespacing restart would make the design argue with itself one section apart. `pods/exec` is the one workload grant that *stays* cluster-wide, and the distinction is that a shell creates no pod, so namespacing it would close nothing. It is not a claim that exec is harmless: a shell **inherits** the target pod's privilege, and several exempt namespaces run deliberately privileged pods, so exec into one of those is root on the node by inheritance rather than by creation. That is the reason exec is admin-only and every session is recorded, and the reason widening it below admin is a different decision from widening anything else here.

**Two ClusterRoles, not one**, because restart and scale are an operator action while the editor is admin-only and a `RoleBinding` carries one `roleRef` and one subject list. Same file, same mechanism, fourteen bindings.

Consequence, and it is deliberate: restart, scale and saving a manifest work on application workloads and **403 on infrastructure ones**. Reads are untouched and stay cluster-wide. Task 14 makes the screen say so rather than offering a control that fails — and note the distinction it has to draw: the YAML **tab** stays available everywhere, because reading a manifest is genuinely useful on an infrastructure workload; only the **Save** button is disabled.

**It also un-breaks `ApplicationClient.restart()`**, whose Restart button in `ApplicationsView.tsx` has returned 403 since 2026-08-10 — but only for the enforced namespaces. Verify it in Step 7.

**Files:**
- Modify: `deploy/kubernetes/base/rbac.yaml` (`cluster-control-viewer` rules; `cluster-control-operator` rules and the `apps` note; a new `cluster-control-admin` ClusterRole holding `pods/exec` and nothing else, after `cluster-control-operator`; **remove** the existing cluster-wide `deployments/scale`+`statefulsets/scale` rule)
- Create: `deploy/kubernetes/base/rbac-workload-operator.yaml`
- Modify: `deploy/kubernetes/base/kustomization.yaml` (add the new file to `resources`, after `rbac-tier-bindings.yaml`)
- Test: `test/manifests/rbac_tiers_test.go`
- Test: `test/manifests/rbac_workload_operator_test.go` (new)

**This is testable, and there is already a harness for it.** `test/manifests`
parses the shipped YAML — `config/rbac/*_role.yaml` plus
`deploy/kubernetes/base/rbac.yaml` — reconstructs what the controller-manager
will aggregate into `frame-viewer`/`frame-editor`/`frame-admin`, and asserts
on that. It exists because the per-user identity lot passed twelve per-task
reviews and every Go test while the manifests granted nobody the rights to use
any of it. Do not replace it with a `grep`.

**Interfaces:**
- Consumes: `deploy/kubernetes/pod-security/namespaces.yaml` (Task 6) — the enforced set, which is exactly the set of namespaces that get a RoleBinding. Also the call-site names produced by Tasks 9 and 12 (`WorkloadClient.tree`, `.logs`, `.restart`, `.scale`, `.deletePod`, `.applyManifest`; `execUrl` in `src/lib/exec-protocol.ts`). Those files do not exist yet — the comments name where the call site *will* be, which is what this file's own convention already does for `frame-sdk.ts` line numbers that move.
- Produces:
  - a `cluster-control-admin` ClusterRole labelled `rbac.frame.plume-labs.io/tier: admin`, picked up by the existing `frame-admin` aggregation in `rbac-tier-bindings.yaml`;
  - a `cluster-control-workload-operator` ClusterRole carrying **no** tier label (restart and scale), plus one `RoleBinding` of the same name in each enforced namespace, subjecting `frame:operators` and `frame:admins`;
  - a `cluster-control-workload-admin` ClusterRole, also carrying **no** tier label (the YAML editor's `update`/`patch` on the five kinds), plus one `RoleBinding` of the same name in each enforced namespace, subjecting `frame:admins` only.

  Task 8 mirrors the namespace list into `OPERABLE_NAMESPACES`; Task 14 uses it to hide the Restart and Scale buttons and to disable the YAML tab's Save button where the grants do not reach.

- [ ] **Step 1: Write the failing tests**

Append to `test/manifests/rbac_tiers_test.go`:

```go
// lot 2. Each access is asserted at the tier that should have it *and* at the
// tier below, because only the pair discriminates: asserting "an admin can"
// alone passes against a rule mislabelled `tier: viewer`, which grants it to
// everyone, and that is exactly the defect (C4) this file was written after.
var lot2OperatorWrites = []Access{
	{Group: "", Resource: "pods/log", Verb: "get", Why: "the Logs tab, WorkloadClient.logs()"},
	{Group: "", Resource: "pods", Verb: "delete", Why: "Delete pod, WorkloadClient.deletePod()"},
}

func TestOperatorsCanOperateWorkloadsAndViewersCannot(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	editor := AggregatedRules(roles, "editor")
	viewer := AggregatedRules(roles, "viewer")

	for _, a := range lot2OperatorWrites {
		if !Grants(editor, a) {
			t.Errorf("frame-editor does not aggregate %s — %s returns 403 for operators and admins", a, a.Why)
		}
		if Grants(viewer, a) {
			t.Errorf("frame-viewer aggregates %s — a viewer can %s, and a log is where a secret gets printed", a, a.Why)
		}
	}
}

// Exec is admin-only per the decision that opened this lot's design, and it is
// the one workload grant that stays cluster-wide — a shell creates no pod, so
// bounding it to the enforced namespaces would close nothing. It inherits the
// target pod's privilege instead: exec into one of the deliberately privileged
// pods in an exempt namespace is root on the node, which is why this is
// admin-only and recorded rather than why it is safe.
//
// Asserted at both tiers: "an admin can" alone passes against a rule
// mislabelled `tier: viewer`, which grants a shell to everyone.
func TestOnlyAdminsExec(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	admin := AggregatedRules(roles, "admin")
	editor := AggregatedRules(roles, "editor")

	a := Access{Group: "", Resource: "pods/exec", Verb: "create", Why: "the Terminal tab"}
	if !Grants(admin, a) {
		t.Errorf("frame-admin does not aggregate %s — %s is refused for everyone", a, a.Why)
	}
	if Grants(editor, a) {
		t.Errorf("frame-editor aggregates %s — %s is admin-only by design", a, a.Why)
	}
}

// The tree is blank for a viewer without these, and blank is how the Accounts
// screen failed: a 200, an empty list, and nothing to notice.
func TestViewersCanReadTheWholeWorkloadTree(t *testing.T) {
	viewer := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "viewer")
	for _, a := range []Access{
		{Group: "apps", Resource: "daemonsets", Verb: "list", Why: "the DaemonSet rows of the tree"},
		{Group: "apps", Resource: "replicasets", Verb: "list", Why: "attaching a Deployment's pods to it"},
		{Group: "batch", Resource: "jobs", Verb: "list", Why: "the Job rows of the tree"},
		{Group: "apps", Resource: "deployments", Verb: "watch", Why: "useLiveResource streams the tree"},
		{Group: "", Resource: "pods", Verb: "watch", Why: "useLiveResource streams the tree"},
	} {
		if !Grants(viewer, a) {
			t.Errorf("frame-viewer does not aggregate %s — %s", a, a.Why)
		}
	}
}

// Every grant that can write a pod template must NOT reach any aggregated
// tier. All of them are bound namespace by namespace, into exactly the
// namespaces where `baseline` Pod Security refuses the privileged payload; a
// tier label on either workload ClusterRole would make the aggregation pick it
// up and grant it everywhere, including rook-ceph and kube-system, which is the
// escalation the 2026-08-10 removal closed.
//
// The editor's `update`/`patch` is in this list for the same reason restart is:
// cluster-wide, it is the identical escalation through a different door — an
// admin writes `privileged: true` and a `hostPath: /` into a pod template in an
// exempt namespace and holds node root.
//
// Asserted at all three tiers, because "not at editor" alone passes against a
// role mislabelled `tier: admin`.
func TestWorkloadWritesAreNotAggregatedIntoAnyTier(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	for _, tier := range []string{"viewer", "editor", "admin"} {
		rules := AggregatedRules(roles, tier)
		for _, a := range []Access{
			{Group: "apps", Resource: "deployments", Verb: "patch", Why: "Restart"},
			{Group: "apps", Resource: "statefulsets", Verb: "patch", Why: "Restart"},
			{Group: "apps", Resource: "deployments/scale", Verb: "patch", Why: "Scale"},
			{Group: "apps", Resource: "statefulsets/scale", Verb: "patch", Why: "Scale"},
			{Group: "", Resource: "pods", Verb: "update", Why: "the YAML tab"},
			{Group: "", Resource: "pods", Verb: "patch", Why: "the YAML tab"},
			{Group: "apps", Resource: "deployments", Verb: "update", Why: "the YAML tab"},
			{Group: "apps", Resource: "statefulsets", Verb: "update", Why: "the YAML tab"},
			{Group: "apps", Resource: "daemonsets", Verb: "update", Why: "the YAML tab"},
			{Group: "apps", Resource: "daemonsets", Verb: "patch", Why: "the YAML tab"},
			{Group: "batch", Resource: "jobs", Verb: "update", Why: "the YAML tab"},
			{Group: "batch", Resource: "jobs", Verb: "patch", Why: "the YAML tab"},
		} {
			if Grants(rules, a) {
				t.Errorf("frame-%s aggregates %s cluster-wide — %s must be bound per namespace, "+
					"or a pod template can be rewritten in rook-ceph or kube-system and that is node root", tier, a, a.Why)
			}
		}
	}
}

// The other half of the same rule: the tree must still be readable everywhere,
// including in the namespaces where nothing may be written. Without this, a
// zealous reading of the test above could be "satisfied" by removing the reads
// as well, and the console would go blank on every infrastructure namespace.
func TestReadsStayClusterWideEvenWhereWritesDoNot(t *testing.T) {
	viewer := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "viewer")
	for _, a := range []Access{
		{Group: "apps", Resource: "deployments", Verb: "get", Why: "showing a rook-ceph deployment's YAML"},
		{Group: "apps", Resource: "daemonsets", Verb: "get", Why: "showing a kube-system daemonset's YAML"},
		{Group: "batch", Resource: "jobs", Verb: "get", Why: "showing a Job's YAML"},
		{Group: "", Resource: "pods", Verb: "get", Why: "showing a pod's YAML"},
	} {
		if !Grants(viewer, a) {
			t.Errorf("frame-viewer does not aggregate %s — %s", a, a.Why)
		}
	}
}

// The bound the spec draws around the YAML editor. "Edit any resource" would
// mean cluster-wide update for admins, which is a far larger grant than this
// screen needs and could not be tied to a call site the way
// deploy/kubernetes/base/rbac.yaml requires.
func TestTheManifestEditorIsNotAClusterWideGrant(t *testing.T) {
	admin := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "admin")
	for _, a := range []Access{
		{Group: "", Resource: "secrets", Verb: "update"},
		{Group: "", Resource: "configmaps", Verb: "update"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verb: "update"},
		{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions", Verb: "update"},
	} {
		if Grants(admin, a) {
			t.Errorf("frame-admin aggregates %s — the editor is bounded to the five kinds the tree shows", a)
		}
	}
}
```

Create `test/manifests/rbac_workload_operator_test.go` — the namespaced exception needs its own guard, because none of the helpers above look at RoleBindings against a namespace list:

```go
package manifests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// enforcedNamespaces reads deploy/kubernetes/pod-security/namespaces.yaml —
// the single source of truth for where `baseline` is enforced, and therefore
// for where restart and scale may be granted at all.
//
// Parsed rather than duplicated: a second hand-maintained list is how the two
// drift, and the drift is silent in the direction that matters (a RoleBinding
// in a namespace that is not enforced hands back the escalation, and nothing
// else in the repository would notice).
func enforcedNamespaces(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(Root(t), "deploy", "kubernetes", "pod-security", "namespaces.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^  name: (\S+)$`).FindAllStringSubmatch(string(raw), -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("parsed no namespaces out of %s — the fixture is not reading what it claims", path)
	}
	sort.Strings(out)
	return out
}

// workloadRoles is every ClusterRole that may write a pod template, with the
// groups its RoleBindings must reach. Two of them, because restart and scale
// are an operator action while the manifest editor is admin-only, and a
// RoleBinding carries one roleRef and one subject list.
var workloadRoles = []struct {
	name    string
	groups  []string
	notFor  []string
	purpose string
}{
	{
		name:    "cluster-control-workload-operator",
		groups:  []string{"Group:frame:operators", "Group:frame:admins"},
		notFor:  []string{"Group:frame:viewers"},
		purpose: "restart and scale",
	},
	{
		name:    "cluster-control-workload-admin",
		groups:  []string{"Group:frame:admins"},
		notFor:  []string{"Group:frame:viewers", "Group:frame:operators"},
		purpose: "the YAML editor",
	},
}

// The whole shape of the exception, in one test: one RoleBinding per enforced
// namespace, for each role, and none anywhere else.
//
// The second half is the load-bearing one. A RoleBinding in an unenforced
// namespace — rook-ceph, kube-system, monitoring — is a grant to write a pod
// template where nothing refuses `privileged: true` with a `hostPath: /`
// volume, which is node root. That is precisely the state the 2026-08-10
// removal closed, and it would be invisible: the RBAC would look tidy, the tier
// tests would stay green, and the console would simply work in one more
// namespace.
func TestWorkloadRolesAreBoundOnlyWhereBaselineIsEnforced(t *testing.T) {
	root := Root(t)
	file := filepath.Join(root, "deploy", "kubernetes", "base", "rbac-workload-operator.yaml")
	bindings := RoleBindings(t, file)
	want := enforcedNamespaces(t)

	for _, role := range workloadRoles {
		t.Run(role.name, func(t *testing.T) {
			seen := map[string]bool{}
			for key, rb := range bindings {
				if rb.RoleRef.Name != role.name {
					continue
				}
				seen[rb.Namespace] = true
				subs := SubjectNames(rb.Subjects)
				for _, g := range role.groups {
					if !Has(subs, g) {
						t.Errorf("RoleBinding %s does not reach %s (subjects: %s) — %s 403s for them",
							key, g, Join(subs), role.purpose)
					}
				}
				for _, g := range role.notFor {
					if Has(subs, g) {
						t.Errorf("RoleBinding %s names %s — %s is not theirs", key, g, role.purpose)
					}
				}
				if Has(subs, "ServiceAccount:cluster-control-ui") {
					t.Errorf("RoleBinding %s subjects the ServiceAccount, which is not the identity "+
						"an impersonated request carries", key)
				}
			}

			for _, ns := range want {
				if !seen[ns] {
					t.Errorf("no %s RoleBinding in %q — baseline is enforced there, so %s should work and will 403",
						role.name, ns, role.purpose)
				}
				delete(seen, ns)
			}
			for ns := range seen {
				t.Errorf("%s is bound in %q, which is not in "+
					"deploy/kubernetes/pod-security/namespaces.yaml — nothing refuses a privileged pod "+
					"there, so this grant is node root", role.name, ns)
			}
		})
	}
}

// Both ClusterRoles must stay unaggregated and must stay small. A tier label on
// either undoes the namespacing in one line; an extra rule widens a namespaced
// grant into something else.
func TestWorkloadRolesAreUnaggregatedAndNarrow(t *testing.T) {
	file := filepath.Join(Root(t), "deploy", "kubernetes", "base", "rbac-workload-operator.yaml")
	roles := ClusterRoles(t, file)

	for _, role := range workloadRoles {
		cr, ok := roles[role.name]
		if !ok {
			t.Fatalf("%s is not defined in rbac-workload-operator.yaml", role.name)
		}
		if tier, has := cr.Labels[TierLabel]; has {
			t.Fatalf("%s carries %s=%s — the aggregation would grant it cluster-wide, "+
				"which is exactly what binding it per namespace exists to prevent", role.name, TierLabel, tier)
		}
	}

	operator := roles["cluster-control-workload-operator"].Rules
	for _, a := range []Access{
		{Group: "apps", Resource: "deployments", Verb: "patch"},
		{Group: "apps", Resource: "statefulsets", Verb: "patch"},
		{Group: "apps", Resource: "deployments/scale", Verb: "patch"},
		{Group: "apps", Resource: "statefulsets/scale", Verb: "patch"},
	} {
		if !Grants(operator, a) {
			t.Errorf("cluster-control-workload-operator does not grant %s — restart or scale 403s everywhere", a)
		}
	}
	for _, a := range []Access{
		{Group: "apps", Resource: "deployments", Verb: "delete"},
		{Group: "apps", Resource: "deployments", Verb: "update"},
		{Group: "apps", Resource: "daemonsets", Verb: "patch"},
		{Group: "", Resource: "pods", Verb: "delete"},
		{Group: "", Resource: "secrets", Verb: "get"},
	} {
		if Grants(operator, a) {
			t.Errorf("cluster-control-workload-operator grants %s — it is restart and scale, nothing else; "+
				"`update` in particular is the editor's verb and belongs to admins", a)
		}
	}

	admin := roles["cluster-control-workload-admin"].Rules
	for _, a := range []Access{
		{Group: "", Resource: "pods", Verb: "update"},
		{Group: "apps", Resource: "deployments", Verb: "update"},
		{Group: "apps", Resource: "statefulsets", Verb: "update"},
		{Group: "apps", Resource: "daemonsets", Verb: "update"},
		{Group: "batch", Resource: "jobs", Verb: "update"},
	} {
		if !Grants(admin, a) {
			t.Errorf("cluster-control-workload-admin does not grant %s — the YAML tab cannot save anywhere", a)
		}
	}
	// The kind bound the spec draws, asserted on the namespaced role too:
	// bounding by namespace does not license widening by kind.
	for _, a := range []Access{
		{Group: "", Resource: "secrets", Verb: "update"},
		{Group: "", Resource: "configmaps", Verb: "update"},
		{Group: "", Resource: "serviceaccounts", Verb: "update"},
		{Group: "apps", Resource: "deployments", Verb: "delete"},
		{Group: "", Resource: "pods/exec", Verb: "create"},
	} {
		if Grants(admin, a) {
			t.Errorf("cluster-control-workload-admin grants %s — the editor is five kinds, "+
				"update and patch, and nothing else", a)
		}
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./test/manifests/ -run 'Lot2|Workload|Exec|Manifest|Operate|Reads' -v
```

Expected:

- `TestOperatorsCanOperateWorkloadsAndViewersCannot`, `TestOnlyAdminsExec` and `TestViewersCanReadTheWholeWorkloadTree` failing with `frame-editor does not aggregate …` / `frame-admin does not aggregate …` lines naming each missing grant.
- `TestWorkloadRolesAreBoundOnlyWhereBaselineIsEnforced` and `TestWorkloadRolesAreUnaggregatedAndNarrow` failing on the missing file.
- `TestWorkloadWritesAreNotAggregatedIntoAnyTier` **failing on `deployments/scale` and `statefulsets/scale` at the editor tier**, which is the pre-existing cluster-wide scale grant this task moves out. That failure is the useful one: it is the current state of the repository, not a mistake in the test.
- `TestReadsStayClusterWideEvenWhereWritesDoNot` passing already for pods and deployments, and failing for `daemonsets` and `jobs` until Step 3 adds the viewer reads.
- `TestTheManifestEditorIsNotAClusterWideGrant` passing already, which is correct — it guards against the fix going too far, not against the gap.

- [ ] **Step 3: Add the viewer reads**

In `deploy/kubernetes/base/rbac.yaml`, in `cluster-control-viewer`'s `rules`, immediately after the existing `apps: [deployments, statefulsets]` rule:

```yaml
  # The Workloads tree: namespace → controller → pods — frame-sdk.ts
  # WorkloadClient.tree(), src/components/workloads/WorkloadsView.tsx.
  # `deployments` and `statefulsets` above are already granted; these are the
  # other two kinds the tree shows.
  #
  # `replicasets` is the same call site and is not optional decoration: a
  # Deployment's pods are owned by a ReplicaSet, never by the Deployment, so
  # without reading the ReplicaSet's ownerReferences the tree cannot attach a
  # pod to the Deployment that made it. The alternative — stripping the
  # pod-template hash off the ReplicaSet's name — is a guess that puts pods
  # under the wrong controller for any Deployment whose own name ends in
  # something hash-shaped.
  - apiGroups: [apps]
    resources: [daemonsets, replicasets]
    verbs: [get, list, watch]
  # Jobs in the same tree — frame-sdk.ts WorkloadClient.tree().
  - apiGroups: [batch]
    resources: [jobs]
    verbs: [get, list, watch]
```

Note what is deliberately *not* here: `namespaces`. The tree derives its namespaces from the workloads it already reads, so it needs no grant to enumerate them — and the 2026-08-10 audit removed `namespaces` from this role for having no call site. Adding it back to save a `Set` would undo that for nothing.

Logs are **not** here either. A viewer sees state; logs are the most likely place in a cluster for a credential to appear in plain text, and widening a grant later is easy where narrowing one after people depend on it is not. This is a judgement, not a constraint — it is the one rule below, in the operator role.

- [ ] **Step 4: Add the operator writes**

In `cluster-control-operator`'s `rules`, immediately after the existing `pods/eviction` rule:

```yaml
  # Pod logs (Workloads → Logs), including ?previous=true for the instance
  # that crash-looped — frame-sdk.ts WorkloadClient.logs().
  #
  # Operator, not viewer, and that is a decision rather than an oversight: a
  # log is the most likely place for a credential to be printed in plain text,
  # and reads leave no FrameTask — the recorder ignores them by design, so
  # there will be no record that anyone read one. The mitigation is this tier
  # boundary, not the audit trail. Moving it down to the viewer role is one
  # line if that judgement changes.
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
  # Delete a pod (Workloads → Delete pod) — frame-sdk.ts
  # WorkloadClient.deletePod(). Distinct from the pods/eviction grant above:
  # eviction honours PodDisruptionBudgets and is what a drain uses, while this
  # is the deliberate "restart this one pod now" a person asks for from the
  # pod's own panel. The console names the difference in the confirmation —
  # under a controller the pod comes back, on a bare pod it does not.
  - apiGroups: [""]
    resources: [pods]
    verbs: [delete]
```

**`patch` does not come back here.** The paragraph beginning "`patch` REMOVED 2026-08-10" stays exactly as it is — it is still true of this role. Append to it, and to nothing else:

```yaml
  # 2026-09-09, lot 2: the note above still stands for this ClusterRole, and
  # this is where the fix landed instead.
  #
  # Restart and scale are granted, but not here and not cluster-wide: a
  # `cluster-control-workload-operator` ClusterRole in
  # rbac-workload-operator.yaml, bound by a RoleBinding into each namespace
  # that carries `pod-security.kubernetes.io/enforce: baseline`
  # (deploy/kubernetes/pod-security/namespaces.yaml). That is the "safe way to
  # earn it back" this comment named: the privileged-pod payload is refused at
  # admission in exactly the namespaces where the patch is allowed.
  #
  # It is not labelled with a tier, so the aggregation does not pick it up. A
  # tier label there would make it cluster-wide again and would reach
  # rook-ceph, kube-system and monitoring, which are deliberately unlabelled
  # because Ceph, the node-tuning agent and node-exporter need privileged pods.
  # test/manifests/rbac_workload_operator_test.go asserts both halves.
```

**And the existing cluster-wide `scale` rule goes with it.** Delete this rule from `cluster-control-operator`:

```yaml
  # Scale button — frame-sdk.ts:2332-2339. The `/scale` subresource can only
  # change the replica count, so unlike a full patch it cannot touch the pod
  # template. This is the reason the two are split.
  - apiGroups: [apps]
    resources: [deployments/scale, statefulsets/scale]
    verbs: [patch]
```

and leave a tombstone in its place, because deleting a working grant needs a reason a future reader can find:

```yaml
  # `deployments/scale` and `statefulsets/scale` REMOVED 2026-09-09, lot 2, and
  # moved to rbac-workload-operator.yaml alongside the Restart patch.
  #
  # Not because scaling is dangerous — the `/scale` subresource can only change
  # a replica count and cannot touch a pod template, which is why the two used
  # to be split. It moves because the spec requires restart and scale to behave
  # the same way from the screen: both available on application workloads, both
  # 403 on infrastructure ones. Leaving scale cluster-wide would give the
  # Workloads panel two buttons with two different reachs and no way for the UI
  # to explain which is which.
  #
  # The visible cost: scaling anything in an unlabelled namespace (rook-ceph,
  # monitoring, kube-system) now 403s where it used to work. `kubectl scale`
  # still does it for anyone with a real kubeconfig.
```

and add the tree reads the operator tier needs in its own right (the aggregation gives it the viewer role's rules too, so these are a union, not a second grant — but this role is documented as possibly still bound somewhere outside the repo, so it carries its own reads the way it already carries `nodes`/`pods`/`events`):

```yaml
  # The Workloads tree — frame-sdk.ts WorkloadClient.tree(). Same call site as
  # the identical rules in cluster-control-viewer above; duplicated here for
  # the same reason the core reads are, and a duplicate rule in an aggregated
  # ClusterRole is a union that changes nobody's permissions.
  - apiGroups: [apps]
    resources: [daemonsets, replicasets]
    verbs: [get, list, watch]
  - apiGroups: [batch]
    resources: [jobs]
    verbs: [get, list, watch]
```

- [ ] **Step 5: Add the admin role**

Immediately after the `cluster-control-operator` ClusterRole (before the `# The pod ServiceAccount no longer holds…` block), add:

```yaml
---
# Role 3: admin — the one thing the console does cluster-wide that an operator
# must not.
#
# New in lot 2 (2026-09-09). Until now nothing in this file carried
# `tier: admin`: the `frame-admin` aggregated ClusterRole
# (rbac-tier-bindings.yaml) selects viewer, editor and admin, and the admin
# tier was satisfied only by the twenty-seven per-kind Frame CRD roles in
# config/rbac. These are console grants on core and apps kinds, so they belong
# here beside the viewer and operator roles, not in config/rbac, which is
# controller-gen's and per-kind.
#
# It holds `pods/exec` and nothing else. The YAML editor's `update`/`patch` is
# NOT here — see the tombstone at the end of this role.
#
# Same discipline as the two roles above: every rule names its call site in
# `src/`.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-control-admin
  labels:
    rbac.frame.plume-labs.io/tier: admin
rules:
  # An interactive shell (Workloads → Terminal) —
  # src/components/workloads/TerminalTab.tsx, over the URL built by execUrl()
  # in src/lib/exec-protocol.ts.
  #
  # `create` is the verb the apiserver authorizes for pods/exec whatever HTTP
  # method carries the request; a browser opens it as a WebSocket GET, and
  # frame-uiproxy records it as a create for the same reason.
  #
  # Admin only, per the decision that opened the lot's design. A shell is
  # every permission the container's own ServiceAccount holds, plus whatever is
  # on its filesystem, and the session is recorded but its *contents* are not:
  # keystroke recording was considered and rejected, because it would capture
  # every secret typed or displayed and make the audit trail itself a sensitive
  # store. The terminal tab says so where the person opening the session can
  # read it.
  - apiGroups: [""]
    resources: [pods/exec]
    verbs: [create]
  #
  # The YAML editor's `update`/`patch` on pods, deployments, statefulsets,
  # daemonsets and jobs is deliberately NOT in this role, and this is the whole
  # reason the tier tests assert its absence.
  #
  # Cluster-wide, it is the same escalation as the Deployment patch removed on
  # 2026-08-10, through a different door: an admin writes
  # `securityContext.privileged: true` and a `hostPath: /` volume into a pod
  # template in a namespace where nothing refuses it, and holds node root. That
  # this programme earns a privileged grant with Pod Security rather than
  # accepting it is a principle, not a rule about one button, so the editor is
  # bounded exactly as restart and scale are: a namespaced ClusterRole in
  # rbac-workload-operator.yaml, bound only into the namespaces enforcing
  # `baseline`.
  #
  # `pods/exec` stays cluster-wide, because a shell creates no pod: bounding it
  # to the enforced namespaces would remove a capability without closing
  # anything. That is not a claim that it is harmless — a shell *inherits* the
  # target pod's privilege, and exec into one of the deliberately privileged
  # pods in an exempt namespace (Ceph, the node-tuning agent, the Talos
  # tooling) is root on the node. Hence admin-only, and hence every session is
  # recorded.
  #
  # Also deliberately absent: `create` and `delete` on those kinds anywhere.
  # Creating a resource from the console and deleting a controller are both out
  # of scope for this lot (the spec says so), and a verb granted before a call
  # site exists is exactly what the 2026-08-10 audit removed from this file.
```

- [ ] **Step 6: Add the namespaced exception**

Create `deploy/kubernetes/base/rbac-workload-operator.yaml`. **Two** ClusterRoles, both with **no tier label**, and one RoleBinding each per namespace in `deploy/kubernetes/pod-security/namespaces.yaml` — fourteen bindings in total.

**Why two roles and not one.** Restart and scale are an operator action; the YAML editor is admin-only (the spec's Authorization table, unamended). One ClusterRole reached by one set of RoleBindings would have to name one subject list, and naming `frame:operators` on it would hand every operator a manifest editor — a widening nobody asked for, on top of the bounding that was asked for. A RoleBinding has exactly one `roleRef`, so preserving the tier split costs a second role and a second set of bindings. It is the **same mechanism** (an unaggregated ClusterRole reached only through per-namespace RoleBindings, in the same file), not a new one.

```yaml
# Restart, scale and the manifest editor: granted only where a privileged pod
# is refused.
#
# ── WHY THIS FILE EXISTS AT ALL ──────────────────────────────────────────────
#
# `patch` on apps/deployments was removed from base/rbac.yaml on 2026-08-10 as
# "the single grant that turned the unauthenticated UI into cluster-admin":
# patching a pod template to add `securityContext.privileged: true` and a
# `hostPath: /` volume is root on the node, and RBAC cannot bound a patch to
# one JSON path, so "may set the restartedAt annotation" and "may take the
# node" are the same grant.
#
# That comment named the way to earn it back — enforce Pod Security — and this
# lot took it. deploy/kubernetes/pod-security/namespaces.yaml labels the
# application namespaces `pod-security.kubernetes.io/enforce: baseline`, which
# refuses exactly that payload at admission. The grants below go into those
# namespaces and no others.
#
# The YAML editor's `update`/`patch` on the same kinds is here for the same
# reason and not as an afterthought: cluster-wide, it is the identical
# escalation through a different door — an admin writing `privileged: true`
# with a `hostPath: /` into a pod template in an exempt namespace holds node
# root just as surely. The principle this programme settled on is that a
# privileged grant is earned with Pod Security rather than accepted, and that
# principle does not stop at the operator tier.
#
# `pods/exec` is NOT here, and that is not an oversight: a shell creates no pod,
# so binding it per namespace would close nothing. It inherits the target pod's
# privilege instead — exec into a privileged pod in an exempt namespace is root
# on the node — which is why it is admin-only and recorded, not why it is safe.
# It stays cluster-wide in base/rbac.yaml's cluster-control-admin.
#
# The filename says "operator" and the file holds an admin role too. It is not
# renamed because fifteen references and a kustomization entry point at it;
# read it as "the workload grants", which is what its two roles are.
#
# ── TWO ROLES, ONE MECHANISM ─────────────────────────────────────────────────
#
# Restart and scale are an operator action; the manifest editor is admin-only.
# A RoleBinding carries one roleRef and one subject list, so keeping that tier
# split means two ClusterRoles and two sets of bindings. Collapsing them into
# one would either hand every operator a manifest editor or take restart away
# from operators; neither is what anyone decided.
#
# ── WHY IT IS SHAPED UNLIKE EVERYTHING ELSE IN THIS DIRECTORY ────────────────
#
# Every other console grant is a ClusterRole carrying
# `rbac.frame.plume-labs.io/tier`, aggregated into frame-viewer/-editor/-admin
# and therefore cluster-wide. This one carries **no tier label** and is reached
# only through the RoleBindings below.
#
# It has to be. Pod Security is enforced on the application namespaces only —
# rook-ceph, kube-system, monitoring, alluxio, ptp, cilium, gpu-operator,
# node-feature-discovery, velero, falco, tetragon and checkpoint-system all run
# privileged pods legitimately and cannot be labelled without breaking the
# cluster. A cluster-wide grant would therefore reach precisely the namespaces
# where nothing refuses the payload, which is the entire hole. The namespacing
# is not tidiness; it is the control.
#
# Consequence, and it is deliberate: restart and scale work on application
# workloads and 403 on infrastructure ones. The Workloads panel says so rather
# than offering a button that fails (src/lib/workloads.ts,
# OPERABLE_NAMESPACES).
#
# ── KEEPING THE TWO LISTS IN STEP ────────────────────────────────────────────
#
# The namespaces below must equal the namespaces in
# ../pod-security/namespaces.yaml — for both binding sets. Adding one here
# without labelling it hands back the escalation; labelling one there without a
# binding here leaves a Restart button, or a Save button, that 403s.
# test/manifests/rbac_workload_operator_test.go parses both files and fails on
# either mismatch, and src/lib/workloads.test.ts does the same for the UI's
# copy.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-control-workload-operator
  # No `rbac.frame.plume-labs.io/tier` label, on purpose. See above. A tier
  # label here is a one-line reintroduction of the 2026-08-10 escalation.
rules:
  # Rollout restart (Workloads → Restart, and ApplicationsView's Restart
  # button, dead since 2026-08-10) — frame-sdk.ts WorkloadClient.restart() and
  # ApplicationClient.restart(). Bumps the pod template's restartedAt
  # annotation, so the controller's own update strategy is respected.
  - apiGroups: [apps]
    resources: [deployments, statefulsets]
    verbs: [patch]
  # Scale (Workloads → Scale, and ApplicationsView) — frame-sdk.ts
  # WorkloadClient.scale() and ApplicationClient.scale(). The subresource can
  # only change a replica count; it is here rather than in the tier role so
  # that scale and restart reach exactly the same namespaces.
  - apiGroups: [apps]
    resources: [deployments/scale, statefulsets/scale]
    verbs: [patch]
  # Deliberately absent: daemonsets (nothing restarts one from the console —
  # the panel offers Restart only for a Deployment or StatefulSet controller),
  # any `delete`, any `update`, and every core resource. Pod delete stays in
  # cluster-control-operator, where it is a pod-scoped action rather than a
  # pod-template one. The editor's verbs are in the admin role below, not here:
  # an operator restarts, an admin rewrites.
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-control-workload-admin
  # No `rbac.frame.plume-labs.io/tier` label either, and for the same reason:
  # aggregated, this is cluster-wide `update` on every pod template on the
  # cluster, which is node root in any namespace Pod Security does not cover.
rules:
  # The YAML editor (Workloads → YAML) — frame-sdk.ts
  # WorkloadClient.applyManifest(), which PUTs the edited manifest carrying the
  # resourceVersion it read.
  #
  # Bounded twice over. By kind: exactly the five the Workloads tree shows, so
  # "edit any resource" — every Secret, every ClusterRole, every CRD — is not
  # what this grants, and every rule still names a call site. And by namespace:
  # the RoleBindings below reach only the namespaces enforcing `baseline`.
  #
  # `update` is the verb the editor uses (it sends a whole object); `patch` is
  # granted beside it because an admin who could rewrite a workload but not
  # restart it would be a strange tier, and both arrive at the same five kinds.
  #
  # The read is NOT bounded and is not here: `get` on these kinds is already in
  # cluster-control-viewer, cluster-wide, and stays that way. The console can
  # show any workload's YAML anywhere the tree shows the workload; it can save
  # only where the policy is enforced. src/components/workloads/YamlTab.tsx
  # disables the Save button — not the tab — outside those namespaces.
  - apiGroups: [""]
    resources: [pods]
    verbs: [update, patch]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets]
    verbs: [update, patch]
  - apiGroups: [batch]
    resources: [jobs]
    verbs: [update, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-operator
  namespace: default
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-operator}
subjects:
  - kind: Group
    name: "frame:operators"
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-operator
  namespace: inference
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-operator}
subjects:
  - kind: Group
    name: "frame:operators"
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-operator
  namespace: neura
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-operator}
subjects:
  - kind: Group
    name: "frame:operators"
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-operator
  namespace: neura-batch
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-operator}
subjects:
  - kind: Group
    name: "frame:operators"
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-operator
  namespace: neura-database
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-operator}
subjects:
  - kind: Group
    name: "frame:operators"
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-operator
  namespace: neura-inference
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-operator}
subjects:
  - kind: Group
    name: "frame:operators"
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-operator
  namespace: neura-training
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-operator}
subjects:
  - kind: Group
    name: "frame:operators"
    apiGroup: rbac.authorization.k8s.io
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
# The editor's bindings. `frame:admins` only — the tier split the two roles
# exist to preserve. An operator restarts and scales here; an admin also
# rewrites.
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-admin
  namespace: default
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-admin}
subjects:
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-admin
  namespace: inference
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-admin}
subjects:
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-admin
  namespace: neura
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-admin}
subjects:
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-admin
  namespace: neura-batch
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-admin}
subjects:
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-admin
  namespace: neura-database
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-admin}
subjects:
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-admin
  namespace: neura-inference
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-admin}
subjects:
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-workload-admin
  namespace: neura-training
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-control-workload-admin}
subjects:
  - kind: Group
    name: "frame:admins"
    apiGroup: rbac.authorization.k8s.io
```

Add it to `deploy/kubernetes/base/kustomization.yaml`'s `resources`, immediately after `rbac-tier-bindings.yaml`:

```yaml
  # Restart, scale and the manifest editor, bound per namespace rather than
  # cluster-wide — see the header of the file for why that shape is the control
  # and not a style choice.
  - rbac-workload-operator.yaml
```

- [ ] **Step 7: Run the tests**

```bash
go test ./test/manifests/ -v
```

Expected: the whole package green, the seven new tests included, and the pre-existing ones untouched.

Two of them are load-bearing in a way worth restating, because both failures are silent on a cluster:

- **The tier label is the entire mechanism.** A `cluster-control-admin` without
  `rbac.frame.plume-labs.io/tier: admin` renders perfectly, applies cleanly and grants nobody
  anything; `AggregatedRules` is what notices, because it filters on exactly that label.
- **The absence of a tier label on the two workload ClusterRoles is equally the mechanism.**
  With one, the aggregation makes restart, scale and the editor cluster-wide, they reach
  `rook-ceph` and `kube-system` where nothing refuses a privileged pod, and every screen keeps
  working — which is why `TestWorkloadWritesAreNotAggregatedIntoAnyTier` asserts the negative at
  all three tiers, for both roles' verbs.

Then confirm nothing else moved, and that the kustomization still builds:

```bash
git diff --stat deploy test/manifests
make kustomize && ./bin/kustomize build deploy/kubernetes/base > /dev/null && echo 'kustomize ok'
./bin/kustomize build deploy/kubernetes/base | grep -c 'cluster-control-workload-operator'
./bin/kustomize build deploy/kubernetes/base | grep -c 'cluster-control-workload-admin'
make helm-parity 2>&1 | tail -20
```

Expected: only `deploy/kubernetes/base/rbac.yaml`, `deploy/kubernetes/base/rbac-workload-operator.yaml`, `deploy/kubernetes/base/kustomization.yaml` and the two test files changed; `kustomize ok`; **15** occurrences of each of `cluster-control-workload-operator` and `cluster-control-workload-admin` in the rendered base (one ClusterRole `metadata.name`, plus a `metadata.name` and a `roleRef.name` line for each of seven RoleBindings). A lower number means the kustomization does not include the file, so the grant would never be applied and every Restart or Save button would 403 with the RBAC looking correct in git; and `helm-parity` reporting the known pre-existing cpu-request drift (`10m` vs `100m`) and **nothing else** — these files are not in the chart, so a new diff there would mean something unintended moved.

- [ ] **Step 8: Confirm the Restart button that has been dead since August**

`ApplicationClient.restart()` (`src/lib/frame-sdk.ts`) is wired to the Restart button in
`ApplicationsView.tsx`, and it has returned 403 for everyone — admins included — since the
2026-08-10 removal. It comes back to life with this grant, in the enforced namespaces only.

```bash
kubectl auth can-i patch deployments.apps -n neura     --as-group=frame:operators --as=alice@example.com
kubectl auth can-i patch deployments.apps -n rook-ceph --as-group=frame:operators --as=alice@example.com
# the editor, admin-only and namespaced the same way
kubectl auth can-i update deployments.apps -n neura     --as-group=frame:admins    --as=root@example.com
kubectl auth can-i update deployments.apps -n rook-ceph --as-group=frame:admins    --as=root@example.com
kubectl auth can-i update deployments.apps -n neura     --as-group=frame:operators --as=alice@example.com
# and the read, which is not bounded at all
kubectl auth can-i get    deployments.apps -n rook-ceph --as-group=frame:viewers   --as=bob@example.com
```

Expected: `yes`, `no`, `yes`, `no`, `no`, `yes`. The `no`s are not defects — they are the whole
design, and the reason Task 14 hides the buttons and disables Save there instead of letting them
fail. The last `yes` is the other half: reading a manifest stays available everywhere the tree
shows the workload. Then open the **Applications**
screen as an operator and restart something in `neura`: it should succeed and leave a
`restart deployment neura/<name>` row on the Tasks screen. Record the result; this is a cluster
check, so if it has not been run, say so rather than assuming.

- [ ] **Step 9: Commit**

```bash
git add deploy/kubernetes/base test/manifests && git commit -m "feat(rbac): logs and pod delete at operator, restart and scale per enforced namespace"
```

---

### Task 8: `src/lib/workloads.ts` — the tree, the infrastructure list, the ownership warning

Everything the Workloads screen decides that is not rendering. It lives in `src/lib` because that is the only place vitest can reach: the suite runs `environment: 'node'` with `include: ['src/**/*.test.ts']`, so a `.tsx` test would sit in the repository looking like coverage and never execute.

**Files:**
- Create: `src/lib/workloads.ts`
- Create: `src/lib/workloads.test.ts`

**Interfaces:**
- Consumes: `deploy/kubernetes/pod-security/namespaces.yaml` (Task 6) — read as text by the test, to keep `OPERABLE_NAMESPACES` from drifting away from where the grant actually reaches.
- Produces:
  ```ts
  export type WorkloadKind = 'Deployment' | 'StatefulSet' | 'DaemonSet' | 'Job'
  export type EditableKind = WorkloadKind | 'Pod'

  export interface OwnerRef { kind: string; name: string }

  export interface WorkloadController {
    kind: WorkloadKind
    name: string
    namespace: string
    desiredReplicas: number
    readyReplicas: number
    /** Deployment and StatefulSet only — a DaemonSet and a Job have no scale subresource. */
    scalable: boolean
  }

  export interface WorkloadPod {
    name: string
    namespace: string
    phase: string
    nodeName: string
    restarts: number
    containers: string[]
    createdAt?: string
    owner?: OwnerRef
  }

  export interface ControllerNode { controller: WorkloadController; pods: WorkloadPod[] }

  export interface NamespaceNode {
    namespace: string
    infrastructure: boolean
    controllers: ControllerNode[]
    barePods: WorkloadPod[]
    podCount: number
  }

  export const INFRASTRUCTURE_NAMESPACES: readonly string[]
  export function isInfrastructureNamespace(ns: string): boolean
  export const OPERABLE_NAMESPACES: readonly string[]
  export function canOperateWorkloads(ns: string): boolean
  export function controllerKey(namespace: string, kind: WorkloadKind, name: string): string
  export function controllerKeyForPod(
    pod: WorkloadPod,
    replicaSetOwners: ReadonlyMap<string, OwnerRef>,
  ): string | undefined
  export function buildWorkloadTree(input: {
    controllers: WorkloadController[]
    pods: WorkloadPod[]
    replicaSetOwners: ReadonlyMap<string, OwnerRef>
  }): NamespaceNode[]
  export function ownershipWarning(meta: {
    labels?: Record<string, string>
    annotations?: Record<string, string>
  }): string | undefined
  ```
  Task 12 (`WorkloadClient`) produces these types from the apiserver; Task 13 renders them; Task 14 uses `canOperateWorkloads` to decide whether the Restart and Scale buttons are offered at all.

- [ ] **Step 1: Write the failing tests**

Create `src/lib/workloads.test.ts`:

```ts
/// <reference types="vite/client" />
import { describe, it, expect } from 'vitest'
import {
  OPERABLE_NAMESPACES,
  buildWorkloadTree,
  canOperateWorkloads,
  controllerKey,
  controllerKeyForPod,
  isInfrastructureNamespace,
  ownershipWarning,
  type OwnerRef,
  type WorkloadController,
  type WorkloadPod,
} from './workloads'
// The manifest that decides where `baseline` Pod Security is enforced, and so
// where restart and scale are granted at all. Read as text rather than
// re-typed: see the test at the bottom of this file.
import podSecuritySource from '../../deploy/kubernetes/pod-security/namespaces.yaml?raw'

function pod(over: Partial<WorkloadPod> = {}): WorkloadPod {
  return {
    name: 'p',
    namespace: 'neura',
    phase: 'Running',
    nodeName: 'w2',
    restarts: 0,
    containers: ['api'],
    ...over,
  }
}

function controller(over: Partial<WorkloadController> = {}): WorkloadController {
  return {
    kind: 'Deployment',
    name: 'api',
    namespace: 'neura',
    desiredReplicas: 2,
    readyReplicas: 2,
    scalable: true,
    ...over,
  }
}

describe('isInfrastructureNamespace', () => {
  it('folds the namespaces this cluster runs its own plumbing in', () => {
    for (const ns of ['kube-system', 'rook-ceph', 'monitoring', 'cluster-control', 'frame-system']) {
      expect(isInfrastructureNamespace(ns)).toBe(true)
    }
  })

  it('leaves application namespaces open', () => {
    for (const ns of ['neura', 'default', 'inference', 'sandbox']) {
      expect(isInfrastructureNamespace(ns)).toBe(false)
    }
  })

  // The two shape rules, tested with names that are NOT in the list — a test
  // using `kube-system` or `volcano-system` would pass with the rules deleted,
  // because the literal list already covers them.
  it('folds anything shaped like plumbing, not only the names it knows', () => {
    expect(isInfrastructureNamespace('kube-flannel')).toBe(true)
    expect(isInfrastructureNamespace('cilium-system')).toBe(true)
  })
})

describe('controllerKeyForPod', () => {
  const rs: ReadonlyMap<string, OwnerRef> = new Map([
    ['neura/api-7d9f8', { kind: 'Deployment', name: 'api' }],
  ])

  // The case the whole ReplicaSet read exists for. A Deployment's pods are
  // owned by a ReplicaSet, never by the Deployment: resolve only the direct
  // owner and every Deployment in the tree shows zero pods while a phantom
  // "ReplicaSet api-7d9f8" holds them all.
  it('walks a pod through its ReplicaSet to the Deployment', () => {
    const p = pod({ name: 'api-7d9f8-x1', owner: { kind: 'ReplicaSet', name: 'api-7d9f8' } })
    expect(controllerKeyForPod(p, rs)).toBe(controllerKey('neura', 'Deployment', 'api'))
  })

  it('takes a StatefulSet, DaemonSet or Job owner directly', () => {
    expect(controllerKeyForPod(pod({ owner: { kind: 'StatefulSet', name: 'pg' } }), rs))
      .toBe(controllerKey('neura', 'StatefulSet', 'pg'))
    expect(controllerKeyForPod(pod({ owner: { kind: 'DaemonSet', name: 'agent' } }), rs))
      .toBe(controllerKey('neura', 'DaemonSet', 'agent'))
    expect(controllerKeyForPod(pod({ owner: { kind: 'Job', name: 'migrate' } }), rs))
      .toBe(controllerKey('neura', 'Job', 'migrate'))
  })

  it('reports no controller for a bare pod', () => {
    expect(controllerKeyForPod(pod(), rs)).toBeUndefined()
  })

  // A ReplicaSet the console never read — created by something outside the five
  // kinds, or RBAC-hidden. Guessing a Deployment name by stripping the trailing
  // hash would attach the pod to a controller that may not exist.
  it('reports no controller for a ReplicaSet it has not seen', () => {
    const p = pod({ owner: { kind: 'ReplicaSet', name: 'mystery-abc12' } })
    expect(controllerKeyForPod(p, rs)).toBeUndefined()
  })
})

describe('buildWorkloadTree', () => {
  const rs: ReadonlyMap<string, OwnerRef> = new Map([
    ['neura/api-7d9f8', { kind: 'Deployment', name: 'api' }],
  ])

  it('groups pods under their controller and keeps bare pods aside', () => {
    const tree = buildWorkloadTree({
      controllers: [controller()],
      pods: [
        pod({ name: 'api-7d9f8-x1', owner: { kind: 'ReplicaSet', name: 'api-7d9f8' } }),
        pod({ name: 'api-7d9f8-x2', owner: { kind: 'ReplicaSet', name: 'api-7d9f8' } }),
        pod({ name: 'debug-shell' }),
      ],
      replicaSetOwners: rs,
    })
    expect(tree).toHaveLength(1)
    expect(tree[0].namespace).toBe('neura')
    expect(tree[0].controllers).toHaveLength(1)
    expect(tree[0].controllers[0].pods.map((p) => p.name)).toEqual(['api-7d9f8-x1', 'api-7d9f8-x2'])
    expect(tree[0].barePods.map((p) => p.name)).toEqual(['debug-shell'])
    expect(tree[0].podCount).toBe(3)
  })

  // A namespace that holds only bare pods must still appear. Building the tree
  // from the controller list alone would drop it, and a pod nobody controls is
  // exactly the pod a person is most likely to be looking for.
  it('keeps a namespace that has pods but no controller', () => {
    const tree = buildWorkloadTree({
      controllers: [],
      pods: [pod({ namespace: 'sandbox', name: 'scratch' })],
      replicaSetOwners: new Map(),
    })
    expect(tree.map((n) => n.namespace)).toEqual(['sandbox'])
    expect(tree[0].barePods).toHaveLength(1)
  })

  // Applications first, plumbing last, each alphabetical — so opening the
  // screen puts what a person came for at the top without scrolling past
  // kube-system.
  it('sorts application namespaces before infrastructure ones', () => {
    const tree = buildWorkloadTree({
      controllers: [
        controller({ namespace: 'rook-ceph', name: 'mgr' }),
        controller({ namespace: 'neura', name: 'api' }),
        controller({ namespace: 'kube-system', name: 'coredns' }),
        controller({ namespace: 'inference', name: 'llamacpp' }),
      ],
      pods: [],
      replicaSetOwners: new Map(),
    })
    expect(tree.map((n) => n.namespace)).toEqual(['inference', 'neura', 'kube-system', 'rook-ceph'])
    expect(tree.map((n) => n.infrastructure)).toEqual([false, false, true, true])
  })
})

describe('OPERABLE_NAMESPACES', () => {
  // The drift guard, and the reason this list is not just
  // `!isInfrastructureNamespace(ns)`.
  //
  // Restart and scale are granted by a RoleBinding in each namespace that
  // carries `pod-security.kubernetes.io/enforce: baseline`
  // (deploy/kubernetes/base/rbac-workload-operator.yaml). A namespace that is
  // neither enforced nor obviously infrastructure — `sandbox`, say — would pass
  // a `!isInfrastructureNamespace` check, be offered a Restart button, and 403.
  // The only honest predicate is the enforced list itself, and the only way to
  // keep a second copy of it honest is to compare it to the first.
  //
  // A broken version — one namespace added to the manifest and not here, or
  // removed here and not there — prints the two sorted arrays side by side.
  it('is exactly the set of namespaces where baseline is enforced', () => {
    const fromManifest = [...podSecuritySource.matchAll(/^ {2}name: (\S+)$/gm)].map((m) => m[1])
    expect(fromManifest.length).toBeGreaterThan(0)
    expect([...fromManifest].sort()).toEqual([...OPERABLE_NAMESPACES].sort())
  })

  it('answers for a namespace on each side', () => {
    expect(canOperateWorkloads('neura')).toBe(true)
    expect(canOperateWorkloads('rook-ceph')).toBe(false)
    // Neither enforced nor infrastructure: the case a `!isInfrastructure`
    // predicate gets wrong, and the case that produces a button that 403s.
    expect(canOperateWorkloads('sandbox')).toBe(false)
  })

  // Not a tautology — it is the invariant that keeps the two lists from ever
  // being satisfiable at once. A namespace cannot both be folded as
  // infrastructure and carry the grant; if one ever does, one of the two
  // decisions is wrong and this says which pair to look at.
  it('never overlaps the folded infrastructure list', () => {
    for (const ns of OPERABLE_NAMESPACES) {
      expect(isInfrastructureNamespace(ns)).toBe(false)
    }
  })
})

describe('ownershipWarning', () => {
  it('names Argo CD from its tracking annotation', () => {
    const w = ownershipWarning({ annotations: { 'argocd.argoproj.io/tracking-id': 'neura:apps/Deployment:neura/api' } })
    expect(w).toContain('Argo CD')
    expect(w).toContain('reverted')
  })

  it('names Helm and its release', () => {
    const w = ownershipWarning({
      labels: { 'app.kubernetes.io/managed-by': 'Helm' },
      annotations: { 'meta.helm.sh/release-name': 'neura' },
    })
    expect(w).toContain('Helm')
    expect(w).toContain('neura')
  })

  // `app.kubernetes.io/instance` is a plain recommended label that half the
  // charts on this cluster set. Treating it as proof of Argo ownership would
  // put a warning on nearly every object, and a warning that is always there
  // is a warning nobody reads — including the one time it is true.
  it('says nothing for an object that merely carries the recommended labels', () => {
    expect(ownershipWarning({
      labels: { 'app.kubernetes.io/instance': 'neura', 'app.kubernetes.io/name': 'api' },
    })).toBeUndefined()
    expect(ownershipWarning({})).toBeUndefined()
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
npx vitest run src/lib/workloads.test.ts
```

Expected: `Failed to resolve import "./workloads"` — the module does not exist yet.

- [ ] **Step 3: Write the module**

Create `src/lib/workloads.ts`:

```ts
/**
 * What the Workloads screen decides, minus the rendering.
 *
 * It lives here rather than in the component because this is the only place a
 * test can reach: vitest runs `environment: 'node'` and only includes
 * `.test.ts` files, so a `.tsx` spec sits in the repository looking like
 * coverage and never runs. Everything below is a pure function over plain
 * data, and everything below is tested.
 */

export type WorkloadKind = 'Deployment' | 'StatefulSet' | 'DaemonSet' | 'Job'

/** The kinds the YAML editor is bounded to — the tree's four, plus the pod. */
export type EditableKind = WorkloadKind | 'Pod'

export interface OwnerRef {
  kind: string
  name: string
}

export interface WorkloadController {
  kind: WorkloadKind
  name: string
  namespace: string
  desiredReplicas: number
  readyReplicas: number
  /**
   * Deployment and StatefulSet only. A DaemonSet's replica count is the number
   * of matching nodes and a Job's is its parallelism; neither has a `scale`
   * subresource, so offering the button would be offering a 404.
   */
  scalable: boolean
}

export interface WorkloadPod {
  name: string
  namespace: string
  phase: string
  nodeName: string
  restarts: number
  containers: string[]
  createdAt?: string
  /** The pod's controller ownerReference, if it has one. */
  owner?: OwnerRef
}

export interface ControllerNode {
  controller: WorkloadController
  pods: WorkloadPod[]
}

export interface NamespaceNode {
  namespace: string
  infrastructure: boolean
  controllers: ControllerNode[]
  /** Pods with no controller among the kinds the tree shows. */
  barePods: WorkloadPod[]
  /** Every pod in the namespace, controlled or not — the collapsed-row count. */
  podCount: number
}

/**
 * Namespaces the tree folds behind a switch.
 *
 * Presentation, not authorization: RBAC remains the only thing that decides
 * what anyone can see, and this only decides what is open when the screen
 * loads. It lives here rather than in the component so it is testable and can
 * be amended without touching rendering code.
 */
export const INFRASTRUCTURE_NAMESPACES: readonly string[] = [
  'argo',
  'argocd',
  'cert-manager',
  'cluster-control',
  'falco',
  'frame-system',
  'gpu-operator',
  'ingress-nginx',
  'local-path-storage',
  'metallb-system',
  'monitoring',
  'node-feature-discovery',
  'rook-ceph',
  'tetragon',
  'velero',
]

/**
 * True for a namespace the cluster runs itself in.
 *
 * The two shape rules alongside the list are what make it hold up on a cluster
 * nobody enumerated: `kube-*` is reserved by Kubernetes, and `*-system` is the
 * convention every operator that ships its own namespace follows. A private
 * application namespace called `something-system` would be folded by mistake,
 * which costs one click on the switch.
 */
export function isInfrastructureNamespace(ns: string): boolean {
  if (INFRASTRUCTURE_NAMESPACES.includes(ns)) return true
  return ns.startsWith('kube-') || ns.endsWith('-system')
}

/**
 * Namespaces where restart and scale are actually granted.
 *
 * Mirrors `deploy/kubernetes/pod-security/namespaces.yaml` — the namespaces
 * carrying `pod-security.kubernetes.io/enforce: baseline`, which is exactly
 * where `deploy/kubernetes/base/rbac-workload-operator.yaml` binds the grant.
 * The two are kept in step by a test in `workloads.test.ts` that parses the
 * manifest, because a second hand-maintained list drifts.
 *
 * This is deliberately **not** `!isInfrastructureNamespace(ns)`. That predicate
 * decides what the tree folds and is a heuristic with two shape rules; this one
 * decides whether a button is offered, and offering one where the grant does
 * not reach produces a 403 the person cannot do anything about. A namespace
 * that is neither enforced nor recognisably infrastructure — a `sandbox`, a
 * one-off — must fall on the "no button" side, and only an explicit list does
 * that.
 */
export const OPERABLE_NAMESPACES: readonly string[] = [
  'default',
  'inference',
  'neura',
  'neura-batch',
  'neura-database',
  'neura-inference',
  'neura-training',
]

/**
 * True where the console may restart or scale a workload.
 *
 * False everywhere else, including namespaces the tree happily shows: reading
 * is cluster-wide, operating is not, and the screen says which is which rather
 * than offering a button that returns 403.
 */
export function canOperateWorkloads(ns: string): boolean {
  return OPERABLE_NAMESPACES.includes(ns)
}

export function controllerKey(namespace: string, kind: WorkloadKind, name: string): string {
  return `${namespace}/${kind}/${name}`
}

/**
 * The tree node a pod belongs under, or undefined for a pod nothing in the
 * tree controls.
 *
 * A Deployment's pods name a **ReplicaSet** as their owner, never the
 * Deployment, so the walk needs the ReplicaSet's own ownerReferences —
 * `replicaSetOwners`, keyed `namespace/name`. Stripping the pod-template hash
 * off the ReplicaSet's name would avoid that read and would be a guess: it
 * misfiles the pods of any Deployment whose own name ends in something
 * hash-shaped, and invents a controller for a ReplicaSet created by something
 * else.
 */
export function controllerKeyForPod(
  pod: WorkloadPod,
  replicaSetOwners: ReadonlyMap<string, OwnerRef>,
): string | undefined {
  const owner = pod.owner
  if (!owner) return undefined
  if (owner.kind === 'ReplicaSet') {
    const parent = replicaSetOwners.get(`${pod.namespace}/${owner.name}`)
    if (!parent || parent.kind !== 'Deployment') return undefined
    return controllerKey(pod.namespace, 'Deployment', parent.name)
  }
  if (owner.kind === 'StatefulSet' || owner.kind === 'DaemonSet' || owner.kind === 'Job') {
    return controllerKey(pod.namespace, owner.kind, owner.name)
  }
  return undefined
}

/**
 * namespace → controller → pods, with everything the tree could not place kept
 * visible rather than dropped.
 *
 * Built from both lists, not from the controllers alone: a namespace holding
 * only bare pods must still appear, because a pod nothing controls is exactly
 * the pod someone is looking for.
 */
export function buildWorkloadTree(input: {
  controllers: WorkloadController[]
  pods: WorkloadPod[]
  replicaSetOwners: ReadonlyMap<string, OwnerRef>
}): NamespaceNode[] {
  const { controllers, pods, replicaSetOwners } = input

  const byKey = new Map<string, ControllerNode>()
  const namespaces = new Map<string, NamespaceNode>()

  const nodeFor = (namespace: string): NamespaceNode => {
    let n = namespaces.get(namespace)
    if (!n) {
      n = {
        namespace,
        infrastructure: isInfrastructureNamespace(namespace),
        controllers: [],
        barePods: [],
        podCount: 0,
      }
      namespaces.set(namespace, n)
    }
    return n
  }

  for (const c of controllers) {
    const node: ControllerNode = { controller: c, pods: [] }
    byKey.set(controllerKey(c.namespace, c.kind, c.name), node)
    nodeFor(c.namespace).controllers.push(node)
  }

  for (const p of pods) {
    const ns = nodeFor(p.namespace)
    ns.podCount += 1
    const key = controllerKeyForPod(p, replicaSetOwners)
    const owner = key ? byKey.get(key) : undefined
    if (owner) owner.pods.push(p)
    else ns.barePods.push(p)
  }

  for (const ns of namespaces.values()) {
    ns.controllers.sort(
      (a, b) =>
        a.controller.kind.localeCompare(b.controller.kind) ||
        a.controller.name.localeCompare(b.controller.name),
    )
    for (const c of ns.controllers) c.pods.sort((a, b) => a.name.localeCompare(b.name))
    ns.barePods.sort((a, b) => a.name.localeCompare(b.name))
  }

  // Applications first, plumbing last, each alphabetical: what someone came
  // for is at the top without scrolling past kube-system.
  return [...namespaces.values()].sort(
    (a, b) =>
      Number(a.infrastructure) - Number(b.infrastructure) ||
      a.namespace.localeCompare(b.namespace),
  )
}

/**
 * A sentence to show above the editor when something else owns this object,
 * or undefined when nothing does.
 *
 * It does not forbid the edit. It prevents three hours spent debugging a
 * change that quietly disappeared at the next sync.
 *
 * Only markers that mean *ownership* count. `app.kubernetes.io/instance` is a
 * plain recommended label that most charts set and that proves nothing; using
 * it would put this warning on nearly every object on the cluster, and a
 * warning that is always there is one nobody reads on the day it is true.
 */
export function ownershipWarning(meta: {
  labels?: Record<string, string>
  annotations?: Record<string, string>
}): string | undefined {
  const labels = meta.labels ?? {}
  const annotations = meta.annotations ?? {}

  const argo =
    annotations['argocd.argoproj.io/tracking-id'] ?? labels['argocd.argoproj.io/instance']
  if (argo) {
    return `Argo CD manages this object (${argo}). Your edit will be reverted at the next sync.`
  }
  if (labels['app.kubernetes.io/managed-by'] === 'Helm') {
    const release = annotations['meta.helm.sh/release-name'] ?? 'an unnamed release'
    return `Helm manages this object (release ${release}). Your edit will be reverted at the next upgrade.`
  }
  return undefined
}
```

- [ ] **Step 4: Run the tests**

```bash
npx vitest run src/lib/workloads.test.ts
```

Expected: PASS, 16 cases. If the `?raw` import fails to resolve, the manifest from Task 6 is missing — this task depends on it, and the dependency is the point.

- [ ] **Step 5: Commit**

```bash
git add src/lib/workloads.ts src/lib/workloads.test.ts && git commit -m "feat(ui): the workload tree, the infrastructure list and the ownership warning"
```

---

### Task 9: `src/lib/exec-protocol.ts` — the wire format of a shell

Kubernetes multiplexes an exec over one WebSocket by prefixing every frame with a channel byte. That is pure logic, it is the part that is easy to get subtly wrong, and it belongs outside the component — which, being `.tsx`, no test in this repository executes.

The channels, for `v4.channel.k8s.io` (binary frames, not base64 — the base64 variants are the older `base64.channel.k8s.io`):

| Channel | Direction | Carries |
|---|---|---|
| 0 | browser → apiserver | stdin |
| 1 | apiserver → browser | stdout |
| 2 | apiserver → browser | stderr |
| 3 | apiserver → browser | a `metav1.Status` for the command's exit |
| 4 | browser → apiserver | a terminal resize, as JSON |

**Files:**
- Create: `src/lib/exec-protocol.ts`
- Create: `src/lib/exec-protocol.test.ts`

**Interfaces:**
- Consumes: `bearerProtocolPrefix` from Task 5 — the same constant, on the other side of the wire.
- Produces:
  ```ts
  export const EXEC_SUBPROTOCOL = 'v4.channel.k8s.io'
  export const BEARER_SUBPROTOCOL_PREFIX = 'base64url.bearer.authorization.k8s.io.'
  export const CHANNEL_STDIN = 0
  export const CHANNEL_STDOUT = 1
  export const CHANNEL_STDERR = 2
  export const CHANNEL_ERROR = 3
  export const CHANNEL_RESIZE = 4

  export interface ExecTarget {
    namespace: string
    pod: string
    container: string
    command: string[]
  }
  export interface ExecFrame { channel: number; payload: Uint8Array }

  export function execPath(target: ExecTarget): string
  export function execUrl(target: ExecTarget, origin: string): string
  export function bearerSubprotocol(token: string): string
  export function execSubprotocols(token: string): string[]
  export function decodeFrame(data: ArrayBuffer): ExecFrame | undefined
  export function frameText(frame: ExecFrame): string
  export function encodeStdin(text: string): Uint8Array
  export function encodeResize(cols: number, rows: number): Uint8Array
  export function execExitMessage(json: string): string | undefined
  ```

- [ ] **Step 1: Write the failing tests**

Create `src/lib/exec-protocol.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import {
  BEARER_SUBPROTOCOL_PREFIX,
  CHANNEL_ERROR,
  CHANNEL_RESIZE,
  CHANNEL_STDIN,
  CHANNEL_STDOUT,
  EXEC_SUBPROTOCOL,
  bearerSubprotocol,
  decodeFrame,
  encodeResize,
  encodeStdin,
  execExitMessage,
  execPath,
  execSubprotocols,
  execUrl,
  frameText,
} from './exec-protocol'

const TARGET = {
  namespace: 'neura',
  pod: 'api-7d9f8-x1',
  container: 'api',
  command: ['/bin/sh'],
}

function buf(...bytes: number[]): ArrayBuffer {
  return new Uint8Array(bytes).buffer
}

describe('execPath', () => {
  // Spelled out in full, not matched on a substring: the namespace segment is
  // where this kind of path goes wrong silently, and `.includes('/exec')` is
  // true of the wrong pod in the wrong namespace too.
  it('addresses the pod, the container and the command', () => {
    expect(execPath(TARGET)).toBe(
      '/api/v1/namespaces/neura/pods/api-7d9f8-x1/exec' +
        '?container=api&stdin=true&stdout=true&tty=true&command=%2Fbin%2Fsh',
    )
  })

  // The apiserver refuses `stderr=true` together with `tty=true` — a TTY
  // merges the two streams, so PodExecOptions treats the pair as invalid and
  // answers 400. Ask for both and the terminal never opens for anyone, with
  // the reason buried in an apiserver validation message.
  it('does not ask for stderr, which a TTY forbids', () => {
    expect(execPath(TARGET)).not.toContain('stderr')
  })

  it('carries a multi-word command as repeated parameters', () => {
    const p = execPath({ ...TARGET, command: ['/bin/sh', '-c', 'exec bash'] })
    expect(p).toContain('command=%2Fbin%2Fsh&command=-c&command=exec+bash')
  })
})

describe('execUrl', () => {
  it('turns the page origin into a WebSocket origin', () => {
    expect(execUrl(TARGET, 'https://frame.example.test')).toBe(
      'wss://frame.example.test' + execPath(TARGET),
    )
    expect(execUrl(TARGET, 'http://localhost:4200')).toBe(
      'ws://localhost:4200' + execPath(TARGET),
    )
  })
})

describe('bearerSubprotocol', () => {
  it('encodes the token as unpadded base64url', () => {
    expect(bearerSubprotocol('header.payload.sig')).toBe(
      BEARER_SUBPROTOCOL_PREFIX + 'aGVhZGVyLnBheWxvYWQuc2ln',
    )
  })

  // RFC 6455 subprotocol tokens cannot contain "=", "+" or "/". A padded or
  // standard-alphabet encoding makes `new WebSocket()` throw a SyntaxError in
  // the browser before a byte leaves the tab — no request, no server log,
  // nothing to find.
  it('produces a value a subprotocol token can legally hold', () => {
    // "aa" pads to two "=" under standard base64; "\xfb\xff" hits both the
    // "+" and "/" characters of the standard alphabet.
    for (const token of ['aa', 'a', 'ûÿ', 'x'.repeat(97)]) {
      const p = bearerSubprotocol(token)
      expect(p).not.toContain('=')
      expect(p).not.toContain('+')
      expect(p).not.toContain('/')
    }
  })

  it('offers the real protocol first, so the server can select it', () => {
    const list = execSubprotocols('a.b.c')
    expect(list[0]).toBe(EXEC_SUBPROTOCOL)
    expect(list[1]).toBe(bearerSubprotocol('a.b.c'))
  })
})

describe('decodeFrame', () => {
  // The whole point of the module. 0x01 is the stdout channel marker, not
  // output: leave it in the payload and the terminal prints a control
  // character at the head of every chunk the pod writes.
  it('splits the channel byte off the payload', () => {
    const f = decodeFrame(buf(CHANNEL_STDOUT, 0x68, 0x69))
    expect(f?.channel).toBe(CHANNEL_STDOUT)
    expect(frameText(f!)).toBe('hi')
  })

  it('reads the error channel', () => {
    const f = decodeFrame(buf(CHANNEL_ERROR, 0x7b, 0x7d))
    expect(f?.channel).toBe(CHANNEL_ERROR)
    expect(frameText(f!)).toBe('{}')
  })

  it('reports nothing for an empty frame', () => {
    expect(decodeFrame(new ArrayBuffer(0))).toBeUndefined()
  })

  // A frame carrying only the channel byte is legal and means "no output on
  // this channel". Returning undefined for it would be indistinguishable from
  // a malformed frame.
  it('reads a channel byte with no payload as empty output', () => {
    const f = decodeFrame(buf(CHANNEL_STDOUT))
    expect(f?.channel).toBe(CHANNEL_STDOUT)
    expect(frameText(f!)).toBe('')
  })
})

describe('encodeStdin', () => {
  it('prefixes the stdin channel', () => {
    expect(Array.from(encodeStdin('hi'))).toEqual([CHANNEL_STDIN, 0x68, 0x69])
  })

  it('sends non-ASCII as UTF-8, not as one byte per character', () => {
    expect(Array.from(encodeStdin('é'))).toEqual([CHANNEL_STDIN, 0xc3, 0xa9])
  })
})

describe('encodeResize', () => {
  // The field names are capitalised because the apiserver unmarshals them into
  // remotecommand.TerminalSize, a Go struct with no json tags. Lowercase keys
  // are accepted, ignored, and produce no error anywhere: the pane resizes in
  // the browser, the pty stays at its original size, and `top` renders into
  // the wrong width forever with nothing to look at.
  it('sends Width and Height capitalised, on the resize channel', () => {
    const bytes = encodeResize(120, 40)
    expect(bytes[0]).toBe(CHANNEL_RESIZE)
    expect(new TextDecoder().decode(bytes.subarray(1))).toBe('{"Width":120,"Height":40}')
  })
})

describe('execExitMessage', () => {
  it('says nothing when the command exited cleanly', () => {
    expect(execExitMessage('{"status":"Success"}')).toBeUndefined()
  })

  it('reports the message the apiserver sent when it did not', () => {
    expect(execExitMessage('{"status":"Failure","message":"command terminated with exit code 1"}'))
      .toBe('command terminated with exit code 1')
  })

  // The error channel is the only place a refused exec explains itself. A
  // body that is not JSON must still say something, or a failure arrives as
  // an empty terminal.
  it('falls back to the raw body when it is not a Status', () => {
    expect(execExitMessage('not json')).toBe('not json')
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
npx vitest run src/lib/exec-protocol.test.ts
```

Expected: `Failed to resolve import "./exec-protocol"`.

- [ ] **Step 3: Write the module**

Create `src/lib/exec-protocol.ts`:

```ts
/**
 * The wire format of a pod shell.
 *
 * Kubernetes multiplexes stdin, stdout, stderr, an exit status and terminal
 * resizes over one WebSocket by prefixing every binary frame with a channel
 * byte. This module is that framing and the URL that opens it, and nothing
 * else — no DOM, no xterm, no WebSocket. That is deliberate: the terminal
 * component is `.tsx`, which no test in this repository executes, so
 * everything that could be wrong in a way a person would not notice lives
 * here instead.
 *
 * `v4.channel.k8s.io` is the protocol: binary frames (the `base64.channel...`
 * variants are the older, text ones) plus channel 3 carrying a `metav1.Status`
 * when the command ends, which is how an exit code reaches the browser at all.
 */

/** The multiplexing protocol offered to the apiserver. */
export const EXEC_SUBPROTOCOL = 'v4.channel.k8s.io'

/**
 * How a bearer token rides a WebSocket.
 *
 * `new WebSocket(url, protocols)` is the only WebSocket a browser can open and
 * it accepts no request headers, so the `Authorization` header every other
 * request in the console carries has nowhere to go. Kubernetes' own convention
 * puts the token in an extra subprotocol instead; here `frame-uiproxy` is what
 * consumes and strips it (`bearerProtocolPrefix` in
 * `internal/uiproxy/websocket.go`), because the token is authd's and the
 * apiserver would neither accept it nor recognise the entry.
 */
export const BEARER_SUBPROTOCOL_PREFIX = 'base64url.bearer.authorization.k8s.io.'

export const CHANNEL_STDIN = 0
export const CHANNEL_STDOUT = 1
export const CHANNEL_STDERR = 2
export const CHANNEL_ERROR = 3
export const CHANNEL_RESIZE = 4

export interface ExecTarget {
  namespace: string
  pod: string
  container: string
  /** argv, e.g. `['/bin/sh']` or `['/bin/sh', '-c', 'exec bash']`. */
  command: string[]
}

export interface ExecFrame {
  channel: number
  /**
   * The frame's bytes, channel byte removed. Deliberately not decoded: a UTF-8
   * sequence can be split across two WebSocket frames, and xterm's `write`
   * takes a Uint8Array and reassembles them. Decoding per frame here would
   * render a replacement character at the seam.
   */
  payload: Uint8Array
}

/**
 * The apiserver path that opens a shell.
 *
 * `stderr` is absent on purpose: with `tty=true` the apiserver's
 * PodExecOptions validation rejects it — a TTY merges the two streams — and
 * the request comes back 400 before anything opens.
 */
export function execPath(target: ExecTarget): string {
  const q = new URLSearchParams()
  q.set('container', target.container)
  q.set('stdin', 'true')
  q.set('stdout', 'true')
  q.set('tty', 'true')
  for (const c of target.command) q.append('command', c)
  return `/api/v1/namespaces/${target.namespace}/pods/${target.pod}/exec?${q.toString()}`
}

/** The same path as a WebSocket URL on the page's own origin. */
export function execUrl(target: ExecTarget, origin: string): string {
  return `${origin.replace(/^http/, 'ws')}${execPath(target)}`
}

/**
 * The token as a subprotocol entry.
 *
 * Unpadded base64url, because RFC 6455 forbids `=`, `+` and `/` in a
 * subprotocol token: a standard-alphabet or padded value makes `new
 * WebSocket()` throw a SyntaxError in the browser before a byte leaves the
 * tab, which leaves no request and no server-side trace to diagnose.
 *
 * `btoa` is safe here because a JWT is ASCII by construction; the two
 * character substitutions and the padding strip are what turn base64 into
 * base64url.
 */
export function bearerSubprotocol(token: string): string {
  const b64 = btoa(token).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return BEARER_SUBPROTOCOL_PREFIX + b64
}

/**
 * What to pass as `new WebSocket(url, protocols)`.
 *
 * The real protocol comes first: the server selects one entry and echoes it,
 * and it must be able to choose something the browser will accept.
 */
export function execSubprotocols(token: string): string[] {
  return [EXEC_SUBPROTOCOL, bearerSubprotocol(token)]
}

export function decodeFrame(data: ArrayBuffer): ExecFrame | undefined {
  const bytes = new Uint8Array(data)
  if (bytes.length === 0) return undefined
  return { channel: bytes[0], payload: bytes.subarray(1) }
}

/** A frame's payload as text — for the error channel, which carries JSON. */
export function frameText(frame: ExecFrame): string {
  return new TextDecoder().decode(frame.payload)
}

export function encodeStdin(text: string): Uint8Array {
  const body = new TextEncoder().encode(text)
  const out = new Uint8Array(body.length + 1)
  out[0] = CHANNEL_STDIN
  out.set(body, 1)
  return out
}

/**
 * A terminal resize.
 *
 * `Width` and `Height` are capitalised because the apiserver unmarshals this
 * into `remotecommand.TerminalSize`, a Go struct with no json tags. Lowercase
 * keys are accepted, ignored, and produce no error on either side: the pane
 * resizes in the browser, the pty does not, and every full-screen program in
 * the shell renders into the wrong width with nothing to look at.
 */
export function encodeResize(cols: number, rows: number): Uint8Array {
  const body = new TextEncoder().encode(JSON.stringify({ Width: cols, Height: rows }))
  const out = new Uint8Array(body.length + 1)
  out[0] = CHANNEL_RESIZE
  out.set(body, 1)
  return out
}

/**
 * What channel 3 has to say, or undefined when the command ended cleanly.
 *
 * This is the only place a refused or failed exec explains itself — a
 * `metav1.Status` with `status: "Success"` for a clean exit and a message
 * otherwise — so a body that is not JSON is returned as itself rather than
 * swallowed, which is the difference between a diagnosis and a blank pane.
 */
export function execExitMessage(json: string): string | undefined {
  try {
    const status = JSON.parse(json) as { status?: string; message?: string }
    if (status.status === 'Success') return undefined
    return status.message ?? 'the command ended with an error'
  } catch {
    return json
  }
}
```

- [ ] **Step 4: Run the tests**

```bash
npx vitest run src/lib/exec-protocol.test.ts
```

Expected: PASS, 17 cases.

- [ ] **Step 5: Commit**

```bash
git add src/lib/exec-protocol.ts src/lib/exec-protocol.test.ts && git commit -m "feat(ui): the exec channel protocol, tested where the terminal cannot be"
```

---

### Task 10: `src/lib/pod-logs.ts` — the log path and the stream reader

`GET pods/<name>/log?follow=true` is an ordinary chunked HTTP response, not a WebSocket. Chunk boundaries fall wherever the network puts them, so turning that stream into lines is the piece worth testing, and it is the piece a component cannot carry.

**Logs leave no `FrameTask`.** The recorder ignores reads by design and a log read is a read, so there will be no record that anyone read one — which matters, because reading a pod's logs can mean reading a secret. That is stated in the spec, restated in `docs/deployment.md` by Task 14, and the mitigation is the operator-tier grant from Task 7, not the audit trail.

**Files:**
- Create: `src/lib/pod-logs.ts`
- Create: `src/lib/pod-logs.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```ts
  export interface PodLogQuery {
    namespace: string
    pod: string
    container: string
    /** Hold the connection open and stream new lines. */
    follow?: boolean
    /** Read the previous, dead instance of the container. */
    previous?: boolean
    tailLines?: number
  }
  export function podLogPath(q: PodLogQuery): string

  /** The one method pumpLogLines needs; a Response body reader satisfies it. */
  export interface ByteReader {
    read(): Promise<{ done: boolean; value?: Uint8Array }>
  }
  export function pumpLogLines(reader: ByteReader, onLine: (line: string) => void): Promise<void>
  ```

- [ ] **Step 1: Write the failing tests**

Create `src/lib/pod-logs.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { podLogPath, pumpLogLines, type ByteReader } from './pod-logs'

/** A reader that hands back the given chunks, one per read(), then ends. */
function readerOf(chunks: Uint8Array[]): ByteReader {
  let i = 0
  return {
    async read() {
      if (i >= chunks.length) return { done: true, value: undefined }
      return { done: false, value: chunks[i++] }
    },
  }
}

const enc = (s: string) => new TextEncoder().encode(s)

describe('podLogPath', () => {
  // The full path, namespace segment included. `.includes('/log')` is true of
  // the wrong pod in the wrong namespace, which is the failure this repo has
  // already shipped once.
  it('addresses one container of one pod', () => {
    expect(podLogPath({ namespace: 'neura', pod: 'api-0', container: 'api', tailLines: 500 })).toBe(
      '/api/v1/namespaces/neura/pods/api-0/log?container=api&tailLines=500',
    )
  })

  it('asks for a follow when told to', () => {
    expect(podLogPath({ namespace: 'neura', pod: 'api-0', container: 'api', follow: true }))
      .toBe('/api/v1/namespaces/neura/pods/api-0/log?container=api&follow=true')
  })

  // The tab that matters most: when a container has crash-looped, the cause is
  // in the instance that died, not in the one running now. Drop this parameter
  // and the screen answers a different question from the one asked, with no
  // sign that it did.
  it('reads the previous instance when asked', () => {
    expect(podLogPath({ namespace: 'neura', pod: 'api-0', container: 'api', previous: true }))
      .toBe('/api/v1/namespaces/neura/pods/api-0/log?container=api&previous=true')
  })

  // follow and previous together are a contradiction — the dead instance
  // writes nothing more — and the apiserver answers 400. Asking for both would
  // make "previous" appear broken for anyone who left follow on.
  it('never asks to follow a dead instance', () => {
    const p = podLogPath({
      namespace: 'neura', pod: 'api-0', container: 'api', follow: true, previous: true,
    })
    expect(p).toContain('previous=true')
    expect(p).not.toContain('follow=true')
  })
})

describe('pumpLogLines', () => {
  // The reason this is a function and not three lines in a component. Chunk
  // boundaries fall wherever the network puts them: emit one line per chunk
  // and "hello" arrives as "hel" and "lo".
  it('joins a line split across two chunks', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([enc('hel'), enc('lo\nworld\n')]), (l) => lines.push(l))
    expect(lines).toEqual(['hello', 'world'])
  })

  it('splits several lines out of one chunk', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([enc('a\nb\nc\n')]), (l) => lines.push(l))
    expect(lines).toEqual(['a', 'b', 'c'])
  })

  // A pod that is still writing has no trailing newline on its last line. Drop
  // the flush and the most recent line — the one being waited for — never
  // appears.
  it('emits the trailing partial line when the stream ends', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([enc('done\nhalf')]), (l) => lines.push(l))
    expect(lines).toEqual(['done', 'half'])
  })

  // A multi-byte character can be split across chunks. Decode each chunk
  // independently and it becomes two replacement characters, permanently, in
  // the middle of the log line.
  it('reassembles a UTF-8 character split across chunks', async () => {
    const bytes = enc('café\n')
    const lines: string[] = []
    await pumpLogLines(
      readerOf([bytes.subarray(0, 4), bytes.subarray(4)]),
      (l) => lines.push(l),
    )
    expect(lines).toEqual(['café'])
  })

  it('emits nothing for an empty stream', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([]), (l) => lines.push(l))
    expect(lines).toEqual([])
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
npx vitest run src/lib/pod-logs.test.ts
```

Expected: `Failed to resolve import "./pod-logs"`.

- [ ] **Step 3: Write the module**

Create `src/lib/pod-logs.ts`:

```ts
/**
 * Reading a pod's logs.
 *
 * `GET pods/<name>/log` is an ordinary HTTP response — chunked when
 * `follow=true` — not a WebSocket, so this needs no protocol, only a path and
 * a reader that copes with chunk boundaries falling anywhere.
 *
 * Worth stating where it will be read: **a log read leaves no FrameTask.** The
 * recorder ignores reads by design and this is a read, so there is no record
 * that anyone read one — and a log is a likely place for a credential to
 * appear in plain text. The mitigation is the RBAC tier (`pods/log` starts at
 * operator, `deploy/kubernetes/base/rbac.yaml`), not the audit trail.
 */

export interface PodLogQuery {
  namespace: string
  pod: string
  container: string
  /** Hold the connection open and stream new lines as the container writes them. */
  follow?: boolean
  /** Read the previous, terminated instance of the container instead. */
  previous?: boolean
  tailLines?: number
}

/**
 * The apiserver path for one container's logs.
 *
 * `follow` and `previous` are mutually exclusive and `previous` wins: the dead
 * instance will never write another byte, and the apiserver answers 400 for
 * the pair — which would make the previous-logs tab look broken for anyone who
 * had left follow switched on.
 */
export function podLogPath(q: PodLogQuery): string {
  const params = new URLSearchParams()
  params.set('container', q.container)
  if (q.previous) params.set('previous', 'true')
  else if (q.follow) params.set('follow', 'true')
  if (q.tailLines !== undefined) params.set('tailLines', String(q.tailLines))
  return `/api/v1/namespaces/${q.namespace}/pods/${q.pod}/log?${params.toString()}`
}

/**
 * The one method this needs from a stream. `response.body.getReader()`
 * satisfies it, and so does a two-line fake — which is why it is spelled out
 * rather than typed as a `ReadableStreamDefaultReader`.
 */
export interface ByteReader {
  read(): Promise<{ done: boolean; value?: Uint8Array }>
}

/**
 * Call `onLine` once per line, whatever the chunking.
 *
 * Two things it must do that a naive loop does not: keep a partial line across
 * reads (a chunk boundary lands mid-line constantly), and decode with
 * `{ stream: true }` so a UTF-8 character split across two chunks is
 * reassembled rather than turned into two replacement characters. The trailing
 * flush emits the last line of a stream that ended without a newline — which
 * on a live container is the line someone is waiting for.
 */
export async function pumpLogLines(
  reader: ByteReader,
  onLine: (line: string) => void,
): Promise<void> {
  const decoder = new TextDecoder()
  let buffer = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (value && value.length > 0) {
      buffer += decoder.decode(value, { stream: true })
      for (let nl = buffer.indexOf('\n'); nl !== -1; nl = buffer.indexOf('\n')) {
        onLine(buffer.slice(0, nl))
        buffer = buffer.slice(nl + 1)
      }
    }
    if (done) break
  }
  buffer += decoder.decode()
  if (buffer.length > 0) onLine(buffer)
}
```

- [ ] **Step 4: Run the tests**

```bash
npx vitest run src/lib/pod-logs.test.ts
```

Expected: PASS, 9 cases.

- [ ] **Step 5: Commit**

```bash
git add src/lib/pod-logs.ts src/lib/pod-logs.test.ts && git commit -m "feat(ui): pod log paths and a reader that survives chunk boundaries"
```

---

### Task 11: `src/lib/manifest-diff.ts` — the audit label for an edit

The YAML editor is the one action in this lot that could have made the audit trail useless. `update deployments/api` is the same string for a replica bump and for adding a `hostPath: /` volume. So the console computes the **changed field paths** between the object it read and the object being written, and puts them in the label.

Where the "object being written" comes from is Task 13's business: the editor holds YAML text, and the apiserver — not a parser in this repository — turns it into an object, via a `PUT ?dryRun=All` whose response is what would be stored. This module is only the comparison, and the cap.

**The cap is load-bearing.** `FrameTaskSpec.Action` is `maxLength: 200`. Over that, the apiserver refuses the `FrameTask` create; the recorder logs the error and the user's edit succeeds anyway. So the failure mode of an over-long label is **an edit with no record at all** — silently, and exactly on the large edits that most deserve one.

**Files:**
- Create: `src/lib/manifest-diff.ts`
- Create: `src/lib/manifest-diff.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```ts
  export const MAX_ACTION_LENGTH = 200
  export const IGNORED_PATHS: readonly string[]
  export function changedFieldPaths(before: unknown, after: unknown): string[]
  export function editActionLabel(
    kind: string,
    namespace: string,
    name: string,
    paths: string[],
  ): string
  ```

- [ ] **Step 1: Write the failing tests**

Create `src/lib/manifest-diff.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { MAX_ACTION_LENGTH, changedFieldPaths, editActionLabel } from './manifest-diff'

const base = {
  metadata: { name: 'api', namespace: 'neura', resourceVersion: '1', generation: 3 },
  spec: {
    replicas: 2,
    template: { spec: { containers: [{ name: 'api', image: 'neura/api:1.0' }] } },
  },
  status: { readyReplicas: 2 },
}

describe('changedFieldPaths', () => {
  it('reports nothing for an identical object', () => {
    expect(changedFieldPaths(base, structuredClone(base))).toEqual([])
  })

  it('names the leaf that changed', () => {
    const after = structuredClone(base)
    after.spec.replicas = 5
    expect(changedFieldPaths(base, after)).toEqual(['spec.replicas'])
  })

  it('indexes into arrays', () => {
    const after = structuredClone(base)
    after.spec.template.spec.containers[0].image = 'neura/api:1.1'
    expect(changedFieldPaths(base, after)).toEqual([
      'spec.template.spec.containers[0].image',
    ])
  })

  // The server writes these on every request, so a dry-run response differs
  // from the object that was read in all of them. Without the ignore list
  // every single edit's label leads with `metadata.generation,
  // metadata.managedFields, metadata.resourceVersion` — three paths, which is
  // the whole budget — and the change the person actually made is pushed into
  // the "+N more" counter. The label would be technically true and useless.
  it('ignores the fields the server owns', () => {
    const after = structuredClone(base) as Record<string, any>
    after.metadata.resourceVersion = '2'
    after.metadata.generation = 4
    after.metadata.managedFields = [{ manager: 'frame-uiproxy' }]
    after.status.readyReplicas = 0
    expect(changedFieldPaths(base, after)).toEqual([])
  })

  // An added subtree is one decision, not twelve. Descending into it would
  // spend the three-path budget listing the leaves of a block the person
  // pasted in as a unit.
  it('names an added subtree once, not every leaf inside it', () => {
    const after = structuredClone(base) as Record<string, any>
    after.spec.template.spec.tolerations = [
      { key: 'nvidia.com/gpu', operator: 'Exists', effect: 'NoSchedule' },
    ]
    expect(changedFieldPaths(base, after)).toEqual(['spec.template.spec.tolerations'])
  })

  it('names a removed field', () => {
    const after = structuredClone(base) as Record<string, any>
    delete after.spec.replicas
    expect(changedFieldPaths(base, after)).toEqual(['spec.replicas'])
  })

  it('sorts, so the same edit always produces the same label', () => {
    const after = structuredClone(base) as Record<string, any>
    after.spec.replicas = 9
    after.metadata.labels = { tier: 'api' }
    expect(changedFieldPaths(base, after)).toEqual(['metadata.labels', 'spec.replicas'])
  })
})

describe('editActionLabel', () => {
  it('names the object and the fields', () => {
    expect(editActionLabel('Deployment', 'neura', 'api', ['spec.replicas'])).toBe(
      'edit deployment neura/api: spec.replicas',
    )
  })

  it('counts the remainder past three paths', () => {
    expect(editActionLabel('Deployment', 'neura', 'api', ['a', 'b', 'c', 'd', 'e'])).toBe(
      'edit deployment neura/api: a, b, c +2 more',
    )
  })

  // The cap the CRD enforces. Over 200 characters the apiserver refuses the
  // FrameTask create outright; the recorder logs it and the edit goes through
  // anyway, so the whole edit is missing from the Tasks screen with nothing
  // anywhere saying why. A `paths.join(', ')` implementation produces ~600
  // characters here and passes every other test in this file.
  it('never exceeds the 200 characters the CRD allows', () => {
    const paths = Array.from({ length: 40 }, (_, i) => `spec.template.spec.containers[${i}].image`)
    const label = editActionLabel('Deployment', 'a-very-long-namespace-name', 'a-very-long-name', paths)
    expect(label.length).toBeLessThanOrEqual(MAX_ACTION_LENGTH)
  })

  // A truncation that lands on a single very long path must still fit, and
  // must still be a sentence rather than an empty string.
  it('truncates a single enormous path rather than dropping the label', () => {
    const label = editActionLabel('Pod', 'neura', 'api-0', ['x'.repeat(400)])
    expect(label.length).toBeLessThanOrEqual(MAX_ACTION_LENGTH)
    expect(label.startsWith('edit pod neura/api-0:')).toBe(true)
  })

  it('says so when nothing changed', () => {
    expect(editActionLabel('Pod', 'neura', 'api-0', [])).toBe('edit pod neura/api-0: no field changed')
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

```bash
npx vitest run src/lib/manifest-diff.test.ts
```

Expected: `Failed to resolve import "./manifest-diff"`.

- [ ] **Step 3: Write the module**

Create `src/lib/manifest-diff.ts`:

```ts
/**
 * What changed in an edited manifest, phrased for the audit trail.
 *
 * Without this, every edit records as `update deployments/api` — the same
 * string for a replica bump and for adding a `hostPath: /` volume, which is
 * the one write in the product where that distinction matters most.
 *
 * It compares objects, not text: the editor holds YAML, and the apiserver is
 * what turns YAML into an object (a `PUT ?dryRun=All`, whose response is what
 * would be stored). That is why this repository needs no YAML parser and why
 * the comparison is trustworthy — the "after" side is the server's own reading
 * of the text, not ours.
 */

/**
 * FrameTaskSpec.Action's CRD cap.
 *
 * Not advisory. A longer value makes the apiserver refuse the FrameTask
 * create; `TaskRecorder.Start` logs the error and returns "", and the user's
 * edit proceeds. So an over-long label does not produce a truncated record —
 * it produces no record, on exactly the large edits that most deserve one.
 */
export const MAX_ACTION_LENGTH = 200

/**
 * Paths the server owns, which therefore differ between a read and a dry run
 * on every single request.
 *
 * Left in, they are always the first three paths alphabetically after
 * `metadata.labels`, so they would spend the whole three-path budget and push
 * the actual change into the "+N more" counter on every edit.
 */
export const IGNORED_PATHS: readonly string[] = [
  'metadata.creationTimestamp',
  'metadata.generation',
  'metadata.managedFields',
  'metadata.resourceVersion',
  'metadata.uid',
  'status',
]

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function walk(before: unknown, after: unknown, path: string, out: string[]): void {
  if (path !== '' && IGNORED_PATHS.includes(path)) return
  if (Object.is(before, after)) return

  if (isRecord(before) && isRecord(after)) {
    for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) {
      walk(before[key], after[key], path === '' ? key : `${path}.${key}`, out)
    }
    return
  }
  if (Array.isArray(before) && Array.isArray(after)) {
    for (let i = 0; i < Math.max(before.length, after.length); i += 1) {
      walk(before[i], after[i], `${path}[${i}]`, out)
    }
    return
  }
  // Everything else — a scalar that moved, a field added or removed, a value
  // whose *shape* changed — is reported at this path and not descended into.
  // An added block is one decision a person made, and listing its leaves would
  // spend the label's budget describing a paste.
  if (JSON.stringify(before) === JSON.stringify(after)) return
  out.push(path === '' ? '(the whole object)' : path)
}

/** Every path at which `after` differs from `before`, sorted. */
export function changedFieldPaths(before: unknown, after: unknown): string[] {
  const out: string[] = []
  walk(before, after, '', out)
  return out.sort()
}

function truncate(s: string): string {
  return s.length <= MAX_ACTION_LENGTH ? s : s.slice(0, MAX_ACTION_LENGTH)
}

/**
 * The `X-Frame-Action` label for an edit.
 *
 * Three paths then a count: three is what fits alongside a namespaced name
 * inside the cap, and the count is more honest than a longer list truncated
 * mid-path.
 */
export function editActionLabel(
  kind: string,
  namespace: string,
  name: string,
  paths: string[],
): string {
  const head = `edit ${kind.toLowerCase()} ${namespace}/${name}`
  if (paths.length === 0) return truncate(`${head}: no field changed`)
  const shown = paths.slice(0, 3)
  const rest = paths.length - shown.length
  const body = rest > 0 ? `${shown.join(', ')} +${rest} more` : shown.join(', ')
  return truncate(`${head}: ${body}`)
}
```

- [ ] **Step 4: Run the tests**

```bash
npx vitest run src/lib/manifest-diff.test.ts
```

Expected: PASS, 12 cases.

- [ ] **Step 5: Commit**

```bash
git add src/lib/manifest-diff.ts src/lib/manifest-diff.test.ts && git commit -m "feat(ui): name the fields an edit changed, inside the label's 200 characters"
```

---

### Task 12: `WorkloadClient` — the reads and the four writes

The SDK half. Every write goes through `k8sFetch`, which is what supplies the 401-retry and the `X-Frame-Action` header; a bare `fetch` here would be a defect and `frame-sdk.test.ts` has a structural guard that catches one.

Two small additions to `k8sFetch` make the manifest editor possible without a YAML library: `rawBody`, so the user's text is sent verbatim rather than JSON-stringified, and `accept`, so a read can ask the apiserver to serialise YAML. The apiserver produces and consumes `application/yaml` for every resource, so the console never parses or emits YAML itself — which is also why the diff can be trusted: the "after" side of it is the apiserver's own reading of the text, obtained from a `PUT ?dryRun=All`.

**Files:**
- Modify: `src/lib/frame-sdk.ts` (`K8sFetchOptions`; `sendToApiserver`; a new `rawFetch`; a new `WorkloadClient` class; `FrameClient`)
- Test: `src/lib/frame-sdk.test.ts`

**Interfaces:**
- Consumes: `buildWorkloadTree`, `EditableKind`, `NamespaceNode`, `OwnerRef`, `WorkloadController`, `WorkloadKind`, `WorkloadPod` (Task 8); `podLogPath`, `PodLogQuery` (Task 10); `changedFieldPaths`, `editActionLabel` (Task 11); the dry-run rule from Task 4.
- Produces:
  ```ts
  // src/lib/frame-sdk.ts
  export function workloadPath(kind: EditableKind, namespace: string, name?: string): string
  export function workloadWatchPaths(): string[]

  export class WorkloadClient {
    tree(): Promise<NamespaceNode[]>
    logs(q: PodLogQuery): Promise<Response>
    object(kind: EditableKind, namespace: string, name: string): Promise<Record<string, unknown>>
    manifest(kind: EditableKind, namespace: string, name: string): Promise<string>
    applyManifest(o: {
      kind: EditableKind
      namespace: string
      name: string
      before: unknown
      yaml: string
    }): Promise<void>
    restart(kind: 'Deployment' | 'StatefulSet' | 'DaemonSet', namespace: string, name: string): Promise<void>
    scale(kind: 'Deployment' | 'StatefulSet', namespace: string, name: string, replicas: number): Promise<void>
    deletePod(namespace: string, name: string): Promise<void>
  }
  // FrameClient gains: readonly workloads: WorkloadClient
  ```

- [ ] **Step 1: Write the failing tests**

Append to `src/lib/frame-sdk.test.ts`:

```ts
// Every path is spelled out in full. A `url.includes('/deployments')` test
// passes against `/apis/apps/v1/namespaces/default/deployments`, which is the
// exact failure the Accounts screen shipped with — a list that comes back
// empty, with a 200 and no error to notice.
describe('WorkloadClient', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  interface Seen {
    url: string
    method: string
    headers: Record<string, string>
    body?: string
  }

  function capture(respond: (url: string) => Response): Seen[] {
    vi.stubGlobal('window', globalThis)
    const seen: Seen[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input)
        seen.push({
          url,
          method: init?.method ?? 'GET',
          headers: (init?.headers as Record<string, string>) ?? {},
          body: init?.body as string | undefined,
        })
        return respond(url)
      }),
    )
    return seen
  }

  const json = (body: unknown) =>
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    })

  it('reads the five collections the tree is built from, cluster-wide', async () => {
    const seen = capture(() => json({ items: [] }))
    await createFrameClient().workloads.tree()
    expect(seen.map((s) => s.url).sort()).toEqual([
      '/api/v1/pods',
      '/apis/apps/v1/daemonsets',
      '/apis/apps/v1/deployments',
      '/apis/apps/v1/replicasets',
      '/apis/apps/v1/statefulsets',
      '/apis/batch/v1/jobs',
    ])
  })

  it('attaches a Deployment pod through its ReplicaSet', async () => {
    const seen = capture((url) => {
      if (url === '/apis/apps/v1/deployments') {
        return json({
          items: [
            {
              metadata: { name: 'api', namespace: 'neura' },
              spec: { replicas: 2 },
              status: { readyReplicas: 2 },
            },
          ],
        })
      }
      if (url === '/apis/apps/v1/replicasets') {
        return json({
          items: [
            {
              metadata: {
                name: 'api-7d9f8',
                namespace: 'neura',
                ownerReferences: [{ kind: 'Deployment', name: 'api', controller: true }],
              },
            },
          ],
        })
      }
      if (url === '/api/v1/pods') {
        return json({
          items: [
            {
              metadata: {
                name: 'api-7d9f8-x1',
                namespace: 'neura',
                ownerReferences: [{ kind: 'ReplicaSet', name: 'api-7d9f8', controller: true }],
              },
              spec: { nodeName: 'w2', containers: [{ name: 'api' }] },
              status: { phase: 'Running', containerStatuses: [{ restartCount: 3 }] },
            },
          ],
        })
      }
      return json({ items: [] })
    })

    const tree = await createFrameClient().workloads.tree()
    expect(seen.length).toBe(6)
    expect(tree).toHaveLength(1)
    expect(tree[0].controllers[0].controller.name).toBe('api')
    expect(tree[0].controllers[0].pods.map((p) => p.name)).toEqual(['api-7d9f8-x1'])
    expect(tree[0].controllers[0].pods[0].restarts).toBe(3)
    expect(tree[0].barePods).toEqual([])
  })

  it('restarts by patching the pod template annotation, and says so', async () => {
    const seen = capture(() => json({}))
    await createFrameClient().workloads.restart('Deployment', 'neura', 'api')
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api')
    expect(seen[0].method).toBe('PATCH')
    expect(seen[0].headers['Content-Type']).toBe('application/strategic-merge-patch+json')
    expect(seen[0].headers['X-Frame-Action']).toBe('restart deployment neura/api')
    expect(seen[0].body).toContain('kubectl.kubernetes.io/restartedAt')
  })

  it('scales through the scale subresource, naming the target count', async () => {
    const seen = capture(() => json({}))
    await createFrameClient().workloads.scale('StatefulSet', 'neura', 'postgres', 0)
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/statefulsets/postgres/scale')
    expect(seen[0].method).toBe('PATCH')
    // Scaling to zero is a stop, and the label has to be able to say so — a
    // falsy check on `replicas` would drop the number and record "scale … to".
    expect(seen[0].headers['X-Frame-Action']).toBe('scale statefulset neura/postgres to 0')
    expect(seen[0].body).toBe('{"spec":{"replicas":0}}')
  })

  it('deletes one pod by name', async () => {
    const seen = capture(() => json({}))
    await createFrameClient().workloads.deletePod('neura', 'api-7d9f8-x1')
    expect(seen[0].url).toBe('/api/v1/namespaces/neura/pods/api-7d9f8-x1')
    expect(seen[0].method).toBe('DELETE')
    expect(seen[0].headers['X-Frame-Action']).toBe('delete pod neura/api-7d9f8-x1')
  })

  it('asks the apiserver for YAML rather than serialising any here', async () => {
    const seen = capture(
      () => new Response('kind: Deployment\n', { status: 200, headers: { 'content-type': 'application/yaml' } }),
    )
    const text = await createFrameClient().workloads.manifest('Deployment', 'neura', 'api')
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api')
    expect(seen[0].headers['Accept']).toBe('application/yaml')
    expect(text).toBe('kind: Deployment\n')
  })

  // The two-request shape is what makes the audit label possible without a
  // YAML parser: the dry run is the apiserver telling us what it would store,
  // and the diff is computed against that. Collapse it to one request and the
  // label can only ever be "update deployments/api" — the same string for a
  // replica bump and for adding a hostPath volume.
  it('validates the edit, then writes it with the fields that changed', async () => {
    const before = { metadata: { name: 'api', resourceVersion: '7' }, spec: { replicas: 2 } }
    const after = { metadata: { name: 'api', resourceVersion: '7' }, spec: { replicas: 5 } }
    const seen = capture((url) => (url.includes('dryRun') ? json(after) : json(after)))

    await createFrameClient().workloads.applyManifest({
      kind: 'Deployment',
      namespace: 'neura',
      name: 'api',
      before,
      yaml: 'spec:\n  replicas: 5\n',
    })

    expect(seen).toHaveLength(2)
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api?dryRun=All')
    expect(seen[0].method).toBe('PUT')
    expect(seen[0].headers['Content-Type']).toBe('application/yaml')
    // The dry run leaves no FrameTask (TaskRecorder.Start skips it), so a
    // label on it would be a label on nothing.
    expect(seen[0].headers['X-Frame-Action']).toBeUndefined()

    expect(seen[1].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api')
    expect(seen[1].method).toBe('PUT')
    expect(seen[1].headers['X-Frame-Action']).toBe('edit deployment neura/api: spec.replicas')
    // The user's text, byte for byte — not a re-serialisation of anything.
    expect(seen[1].body).toBe('spec:\n  replicas: 5\n')
  })

  // A log stream is a read, so it carries no action — but it must still carry
  // the token. The uiproxy rejects a request with no bearer before RBAC is
  // ever consulted, which is how every integration panel in this console once
  // answered 401 for everyone (see the suite below this one).
  it('sends the log request to the right container of the right pod, with the token', async () => {
    const seen = capture(() => new Response('line\n', { status: 200 }))
    ;(globalThis as Record<string, unknown>).__FRAME_TOKEN__ = 'tok'

    await createFrameClient().workloads.logs({
      namespace: 'neura', pod: 'api-0', container: 'api', previous: true,
    })

    expect(seen[0].url).toBe('/api/v1/namespaces/neura/pods/api-0/log?container=api&previous=true')
    expect(seen[0].headers['Authorization']).toBe('Bearer tok')
    expect(seen[0].headers['X-Frame-Action']).toBeUndefined()
  })
})
```

`capture` stubs `window` as `globalThis`, which is what makes `bearerToken()` in the SDK see `__FRAME_TOKEN__` — the same arrangement as `stubTokenAnd` further down this file. `vi.unstubAllGlobals()` in the `afterEach` does not clear a property set directly on `globalThis`, so delete it there too if a later suite is sensitive to it.

- [ ] **Step 2: Run them and watch them fail**

```bash
npx vitest run src/lib/frame-sdk.test.ts -t WorkloadClient
```

Expected: `TypeError: Cannot read properties of undefined (reading 'tree')` — `FrameClient` has no `workloads`.

- [ ] **Step 3: Widen `k8sFetch`**

In `src/lib/frame-sdk.ts`, add two fields to `K8sFetchOptions`:

```ts
  /**
   * A body sent verbatim, instead of `JSON.stringify(body)`.
   *
   * The manifest editor's body is the user's own YAML text, and there is no
   * YAML library in this repository to turn it into an object first — the
   * apiserver parses `application/yaml` itself. Re-serialising here would mean
   * writing something other than what the person read and edited.
   */
  rawBody?: string
  /**
   * An `Accept` header. Only the manifest editor sets it
   * (`application/yaml`); everything else wants the default JSON.
   */
  accept?: string
```

and use them in `sendToApiserver`:

```ts
  if (opts.accept) headers['Accept'] = opts.accept
  if (opts.body !== undefined || opts.rawBody !== undefined) {
    headers['Content-Type'] = opts.contentType ?? 'application/json'
  }
  // The X-Frame-Action line above stays exactly as it is — including the
  // latin-1 strip, which an action label assembled from user-chosen names
  // still needs.
  return globalThis.fetch(path, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.rawBody ?? (opts.body !== undefined ? JSON.stringify(opts.body) : undefined),
  })
```

Also widen the dedupe guard in `k8sFetch`, so a raw-bodied write is never mistaken for a cacheable GET:

```ts
  if (method === 'GET' && opts.body === undefined && opts.rawBody === undefined) {
```

Add `rawFetch` immediately after `proxyFetch`:

```ts
/**
 * `fetch` for an apiserver path whose response is not JSON — a YAML manifest,
 * or a log stream that must stay a stream.
 *
 * `k8sFetch` parses; these two callers must not be parsed. It still carries
 * the bearer token and still retries once on a 401 with a fresh one, which is
 * the whole reason it is not a bare `fetch`: a tab left open past a token's
 * life would otherwise show an empty log pane rather than reconnecting.
 */
async function rawFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const res = await proxyFetch(path, init)
  if (res.status !== 401) return res
  const token = await refreshTokenForRetry()
  if (!token) return res
  return proxyFetch(path, init)
}
```

- [ ] **Step 4: Write `WorkloadClient`**

Add the imports at the top of `src/lib/frame-sdk.ts`:

```ts
import {
  buildWorkloadTree,
  type EditableKind,
  type NamespaceNode,
  type OwnerRef,
  type WorkloadController,
  type WorkloadKind,
  type WorkloadPod,
} from './workloads'
import { podLogPath, type PodLogQuery } from './pod-logs'
import { changedFieldPaths, editActionLabel } from './manifest-diff'
```

and, immediately before `class NodeClient`, the client itself:

```ts
/** The collection endpoint for each kind the Workloads screen shows. */
const WORKLOAD_COLLECTIONS: Record<EditableKind, string> = {
  Pod: '/api/v1/pods',
  Deployment: '/apis/apps/v1/deployments',
  StatefulSet: '/apis/apps/v1/statefulsets',
  DaemonSet: '/apis/apps/v1/daemonsets',
  Job: '/apis/batch/v1/jobs',
}

/**
 * The namespaced path for one object of a kind the Workloads screen shows.
 *
 * Built from the cluster-wide collection above by splicing the namespace in,
 * so the two can never name different resources — the failure that made the
 * Accounts screen read `frameusers` out of the wrong namespace was exactly a
 * second place where a path was assembled.
 */
export function workloadPath(kind: EditableKind, namespace: string, name?: string): string {
  const collection = WORKLOAD_COLLECTIONS[kind]
  const cut = collection.lastIndexOf('/')
  const base = `${collection.slice(0, cut)}/namespaces/${namespace}${collection.slice(cut)}`
  return name ? `${base}/${name}` : base
}

/** The list paths the Workloads screen watches for live updates. */
export function workloadWatchPaths(): string[] {
  return [
    WORKLOAD_COLLECTIONS.Deployment,
    WORKLOAD_COLLECTIONS.StatefulSet,
    WORKLOAD_COLLECTIONS.DaemonSet,
    WORKLOAD_COLLECTIONS.Job,
    WORKLOAD_COLLECTIONS.Pod,
  ]
}

interface MetaCR {
  name: string
  namespace: string
  creationTimestamp?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  ownerReferences?: Array<{ kind: string; name: string; controller?: boolean }>
}

interface WorkloadItemCR {
  metadata: MetaCR
  spec?: { replicas?: number; parallelism?: number; completions?: number }
  status?: {
    readyReplicas?: number
    replicas?: number
    numberReady?: number
    desiredNumberScheduled?: number
    succeeded?: number
    active?: number
  }
}

interface PodItemCR {
  metadata: MetaCR
  spec?: { nodeName?: string; containers?: Array<{ name: string }> }
  status?: { phase?: string; containerStatuses?: Array<{ restartCount?: number }> }
}

/** The controlling ownerReference, which is the only one that means "belongs to". */
function controllerOwner(meta: MetaCR): OwnerRef | undefined {
  const refs = meta.ownerReferences ?? []
  const owner = refs.find((r) => r.controller) ?? refs[0]
  return owner ? { kind: owner.kind, name: owner.name } : undefined
}

function toController(kind: WorkloadKind, cr: WorkloadItemCR): WorkloadController {
  // Each kind counts its readiness in its own fields: a DaemonSet's desired
  // count is the number of matching nodes, not a replica setting, and a Job's
  // is its completions. Reading `spec.replicas` for all four would report 0/0
  // for half the tree.
  const desired =
    kind === 'DaemonSet'
      ? (cr.status?.desiredNumberScheduled ?? 0)
      : kind === 'Job'
        ? (cr.spec?.completions ?? cr.spec?.parallelism ?? 1)
        : (cr.spec?.replicas ?? 0)
  const ready =
    kind === 'DaemonSet'
      ? (cr.status?.numberReady ?? 0)
      : kind === 'Job'
        ? (cr.status?.succeeded ?? 0)
        : (cr.status?.readyReplicas ?? 0)
  return {
    kind,
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    desiredReplicas: desired,
    readyReplicas: ready,
    scalable: kind === 'Deployment' || kind === 'StatefulSet',
  }
}

function toPod(cr: PodItemCR): WorkloadPod {
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    phase: cr.status?.phase ?? 'Unknown',
    nodeName: cr.spec?.nodeName ?? '',
    restarts: (cr.status?.containerStatuses ?? []).reduce((n, s) => n + (s.restartCount ?? 0), 0),
    containers: (cr.spec?.containers ?? []).map((c) => c.name),
    createdAt: cr.metadata.creationTimestamp,
    owner: controllerOwner(cr.metadata),
  }
}

/**
 * Operating the cluster's workloads: the tree, the logs, the manifest, and the
 * four writes.
 *
 * Every write goes through `k8sFetch` with an `action`, which is what makes it
 * appear on the Tasks screen as something a person did rather than as the
 * request it was. A bare `fetch` here would skip the 401 retry and leave the
 * record unlabelled, which is why the structural guard at the bottom of
 * `frame-sdk.test.ts` exists.
 */
class WorkloadClient {
  async tree(): Promise<NamespaceNode[]> {
    const [deployments, statefulsets, daemonsets, jobs, replicasets, pods] = await Promise.all([
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.Deployment),
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.StatefulSet),
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.DaemonSet),
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.Job),
      k8sFetch<ListResponse<{ metadata: MetaCR }>>('/apis/apps/v1/replicasets'),
      k8sFetch<ListResponse<PodItemCR>>(WORKLOAD_COLLECTIONS.Pod),
    ])

    const controllers: WorkloadController[] = [
      ...(deployments.items ?? []).map((c) => toController('Deployment', c)),
      ...(statefulsets.items ?? []).map((c) => toController('StatefulSet', c)),
      ...(daemonsets.items ?? []).map((c) => toController('DaemonSet', c)),
      ...(jobs.items ?? []).map((c) => toController('Job', c)),
    ]

    // A Deployment's pods name a ReplicaSet as their owner, never the
    // Deployment, so the tree needs this one extra hop to place them.
    const replicaSetOwners = new Map<string, OwnerRef>()
    for (const rs of replicasets.items ?? []) {
      const owner = controllerOwner(rs.metadata)
      if (owner) replicaSetOwners.set(`${rs.metadata.namespace}/${rs.metadata.name}`, owner)
    }

    return buildWorkloadTree({
      controllers,
      pods: (pods.items ?? []).map(toPod),
      replicaSetOwners,
    })
  }

  /**
   * The raw log response, left as a stream so the caller can read it line by
   * line with `pumpLogLines` while the container keeps writing.
   *
   * This is a read, so it carries no `action` and leaves no FrameTask — see
   * the note at the top of `pod-logs.ts`.
   */
  logs(q: PodLogQuery): Promise<Response> {
    return rawFetch(podLogPath(q))
  }

  /** The object as JSON — the diff's baseline, and where the ownership labels are read. */
  object(kind: EditableKind, namespace: string, name: string): Promise<Record<string, unknown>> {
    return k8sFetch<Record<string, unknown>>(workloadPath(kind, namespace, name))
  }

  /**
   * The object as YAML, serialised by the apiserver.
   *
   * `Accept: application/yaml` is a supported representation for every
   * resource, so the console ships no YAML serialiser and no parser — which is
   * both two fewer dependencies and one fewer place for the editor's text to
   * be silently rewritten.
   */
  async manifest(kind: EditableKind, namespace: string, name: string): Promise<string> {
    const res = await rawFetch(workloadPath(kind, namespace, name), {
      headers: { Accept: 'application/yaml' },
    })
    const text = await res.text()
    if (!res.ok) throw new FrameAPIError(res.status, text)
    return text
  }

  /**
   * Write the edited manifest, labelled with what actually changed.
   *
   * Two requests, and both are necessary:
   *
   *  1. `PUT ?dryRun=All` — the apiserver parses the YAML and answers with the
   *     object it *would* store. That is how the changed field paths are
   *     computed with no YAML parser on this side, and it validates the edit
   *     before anything is written. It leaves no FrameTask
   *     (`TaskRecorder.Start` skips a dry run), so the trail shows one row per
   *     edit.
   *  2. the real `PUT`, carrying the label.
   *
   * `yaml` is the user's text, which still contains the `resourceVersion` that
   * was read — so a concurrent change produces a 409 rather than silently
   * overwriting someone else's work. Do not strip it.
   */
  async applyManifest(o: {
    kind: EditableKind
    namespace: string
    name: string
    before: unknown
    yaml: string
  }): Promise<void> {
    const path = workloadPath(o.kind, o.namespace, o.name)
    const after = await k8sFetch<unknown>(`${path}?dryRun=All`, {
      method: 'PUT',
      contentType: 'application/yaml',
      rawBody: o.yaml,
    })
    const action = editActionLabel(o.kind, o.namespace, o.name, changedFieldPaths(o.before, after))
    await k8sFetch<unknown>(path, {
      method: 'PUT',
      contentType: 'application/yaml',
      rawBody: o.yaml,
      action,
    })
  }

  /**
   * Rolling-restart by bumping the pod template's `restartedAt` annotation
   * rather than deleting pods, so the controller's own update strategy —
   * surge, maxUnavailable, ordinal order for a StatefulSet — is respected.
   */
  async restart(
    kind: 'Deployment' | 'StatefulSet' | 'DaemonSet',
    namespace: string,
    name: string,
  ): Promise<void> {
    await k8sFetch<undefined>(workloadPath(kind, namespace, name), {
      action: `restart ${kind.toLowerCase()} ${namespace}/${name}`,
      method: 'PATCH',
      contentType: 'application/strategic-merge-patch+json',
      body: {
        spec: {
          template: {
            metadata: {
              annotations: { 'kubectl.kubernetes.io/restartedAt': new Date().toISOString() },
            },
          },
        },
      },
    })
  }

  /**
   * Scale through the `scale` subresource, which can only change the replica
   * count — unlike a full patch, it cannot touch the pod template. That is why
   * the two are separate grants in `deploy/kubernetes/base/rbac.yaml`.
   */
  async scale(
    kind: 'Deployment' | 'StatefulSet',
    namespace: string,
    name: string,
    replicas: number,
  ): Promise<void> {
    await k8sFetch<undefined>(`${workloadPath(kind, namespace, name)}/scale`, {
      action: `scale ${kind.toLowerCase()} ${namespace}/${name} to ${replicas}`,
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec: { replicas } },
    })
  }

  async deletePod(namespace: string, name: string): Promise<void> {
    await k8sFetch<undefined>(workloadPath('Pod', namespace, name), {
      action: `delete pod ${namespace}/${name}`,
      method: 'DELETE',
    })
  }
}
```

In `FrameClient`, declare and construct it beside the others:

```ts
  readonly workloads: WorkloadClient
```
```ts
    this.workloads = new WorkloadClient()
```

- [ ] **Step 5: Run the tests**

```bash
npx vitest run src/lib/frame-sdk.test.ts && npm run build
```

Expected: the whole SDK suite green — including the `has no bare fetch() left in the module` guard, which is what proves `rawFetch` went through `proxyFetch` rather than calling `fetch` directly — and `tsc -b` clean.

- [ ] **Step 6: Commit**

```bash
git add src/lib/frame-sdk.ts src/lib/frame-sdk.test.ts && git commit -m "feat(sdk): read the workload tree and make the four writes it needs"
```

---

### Task 13: the Workloads screen and the Logs tab

The tree — namespace → controller → pods — with infrastructure namespaces collapsed behind a switch, a detail panel per pod, and the first of its three tabs.

**These files carry no test coverage, and that is structural, not an omission.** Vitest runs `environment: 'node'` and only picks up `.test.ts`, so a `.tsx` spec would sit in the repository looking like coverage and never execute. Every decision worth testing was moved into `src/lib` by Tasks 8, 10, 11 and 12; what is left here is rendering, and it is checked by `npm run build` and by looking at it.

**Files:**
- Create: `src/components/workloads/WorkloadsView.tsx`
- Create: `src/components/workloads/PodDetailPanel.tsx`
- Create: `src/components/workloads/LogsTab.tsx`
- Modify: `src/App.tsx` (`TabId`, the icon import, the lazy import, the NAV entry, the `renderTab` case)

**Interfaces:**
- Consumes: `createFrameClient().workloads` — `tree()`, `logs(q)` (Task 12); `workloadWatchPaths()` (Task 12); `NamespaceNode`, `ControllerNode`, `WorkloadController`, `WorkloadPod` (Task 8); `pumpLogLines` (Task 10).
- Produces:
  ```tsx
  // src/components/workloads/WorkloadsView.tsx
  export function WorkloadsView(): JSX.Element

  // src/components/workloads/PodDetailPanel.tsx
  export interface PodSelection { pod: WorkloadPod; controller?: WorkloadController }
  export function PodDetailPanel(props: {
    selection: PodSelection
    admin: boolean
    onChanged: () => void
    onClose: () => void
  }): JSX.Element

  // src/components/workloads/LogsTab.tsx
  export function LogsTab(props: { pod: WorkloadPod }): JSX.Element
  ```
  Task 14 adds `TerminalTab` and `YamlTab` and mounts them in `PodDetailPanel`'s remaining two tabs, and adds the write actions to its header.

- [ ] **Step 1: Write the tree**

Create `src/components/workloads/WorkloadsView.tsx`:

```tsx
import { useMemo, useState } from 'react'
import {
  ArrowClockwise,
  CaretDown,
  CaretRight,
  TreeStructure,
} from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { LiveStates } from '@/components/LiveStates'
import { PodDetailPanel, type PodSelection } from '@/components/workloads/PodDetailPanel'
import { useLiveResource } from '@/hooks/useLiveResource'
import { isAdminToken } from '@/lib/auth'
import { createFrameClient, workloadWatchPaths } from '@/lib/frame-sdk'
import type { NamespaceNode, WorkloadPod } from '@/lib/workloads'

const frame = createFrameClient()

const PHASE_TONE: Record<string, string> = {
  Running: 'text-accent',
  Succeeded: 'text-muted-foreground',
  Pending: 'text-primary',
  Failed: 'text-destructive',
  Unknown: 'text-destructive',
}

/**
 * Every workload on the cluster: namespace → controller → pods, with a detail
 * panel per pod.
 *
 * Infrastructure namespaces are folded by default (see
 * `isInfrastructureNamespace` in `@/lib/workloads`) so what someone came for is
 * at the top. That is presentation only — RBAC is the real filter, and a
 * namespace this list has never heard of still appears if the apiserver
 * returns it.
 */
export function WorkloadsView() {
  const { state, reload } = useLiveResource<NamespaceNode[]>(
    () => frame.workloads.tree(),
    [],
    workloadWatchPaths(),
  )
  const tree = state.phase === 'ready' ? state.data : []

  const [showInfrastructure, setShowInfrastructure] = useState(false)
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState<Set<string>>(new Set())
  const [selection, setSelection] = useState<PodSelection | undefined>()

  // The admin gate on the terminal tab. A courtesy, not a control: the token
  // is decoded rather than verified, and `create pods/exec` is refused
  // server-side for a non-admin whatever this renders.
  const admin = useMemo(() => {
    const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
    return typeof token === 'string' ? isAdminToken(token) : false
  }, [])

  const q = query.trim().toLowerCase()
  const visible = useMemo(
    () =>
      tree
        .filter((ns) => showInfrastructure || !ns.infrastructure)
        .map((ns) => {
          if (!q) return ns
          return {
            ...ns,
            controllers: ns.controllers.filter(
              (c) =>
                c.controller.name.toLowerCase().includes(q) ||
                ns.namespace.toLowerCase().includes(q) ||
                c.pods.some((p) => p.name.toLowerCase().includes(q)),
            ),
            barePods: ns.barePods.filter(
              (p) => p.name.toLowerCase().includes(q) || ns.namespace.toLowerCase().includes(q),
            ),
          }
        })
        .filter((ns) => !q || ns.controllers.length > 0 || ns.barePods.length > 0),
    [tree, showInfrastructure, q],
  )

  const toggle = (key: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  const podRow = (pod: WorkloadPod, controllerName?: string) => (
    <button
      key={`${pod.namespace}/${pod.name}`}
      type="button"
      onClick={() =>
        setSelection({
          pod,
          controller: tree
            .find((n) => n.namespace === pod.namespace)
            ?.controllers.find((c) => c.controller.name === controllerName)?.controller,
        })
      }
      className="w-full flex items-center gap-3 pl-12 pr-3 py-1.5 text-left hover:bg-muted/50 font-mono text-xs"
    >
      <span className={PHASE_TONE[pod.phase] ?? 'text-muted-foreground'}>●</span>
      <span className="flex-1 truncate">{pod.name}</span>
      {pod.restarts > 0 && (
        <Badge variant="outline" className="font-mono text-[10px] border-current text-destructive">
          {pod.restarts} restarts
        </Badge>
      )}
      <span className="text-muted-foreground w-24 truncate">{pod.nodeName || '—'}</span>
    </button>
  )

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <TreeStructure className="text-primary" />
            Workloads
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && (
          <CardContent className="space-y-3">
            <div className="flex flex-wrap items-center gap-4">
              <Input
                placeholder="Filter by namespace, controller or pod…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className="max-w-xs font-mono text-xs"
              />
              <div className="flex items-center gap-2">
                <Switch
                  id="show-infra"
                  checked={showInfrastructure}
                  onCheckedChange={setShowInfrastructure}
                />
                <Label htmlFor="show-infra" className="font-mono text-xs text-muted-foreground">
                  Show infrastructure namespaces
                </Label>
              </div>
            </div>

            <div className="border rounded-md divide-y">
              {visible.map((ns) => {
                const nsOpen = open.has(ns.namespace) || q !== ''
                return (
                  <div key={ns.namespace}>
                    <button
                      type="button"
                      onClick={() => toggle(ns.namespace)}
                      className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-muted/50 font-mono text-xs"
                    >
                      {nsOpen ? <CaretDown size={12} /> : <CaretRight size={12} />}
                      <span className="font-medium">{ns.namespace}</span>
                      {ns.infrastructure && (
                        <Badge variant="outline" className="font-mono text-[10px]">
                          infrastructure
                        </Badge>
                      )}
                      <span className="ml-auto text-muted-foreground">
                        {ns.controllers.length} controllers · {ns.podCount} pods
                      </span>
                    </button>

                    {nsOpen &&
                      ns.controllers.map((c) => {
                        const key = `${ns.namespace}/${c.controller.kind}/${c.controller.name}`
                        const cOpen = open.has(key) || q !== ''
                        return (
                          <div key={key}>
                            <button
                              type="button"
                              onClick={() => toggle(key)}
                              className="w-full flex items-center gap-2 pl-8 pr-3 py-1.5 text-left hover:bg-muted/50 font-mono text-xs"
                            >
                              {cOpen ? <CaretDown size={12} /> : <CaretRight size={12} />}
                              <Badge variant="outline" className="font-mono text-[10px]">
                                {c.controller.kind}
                              </Badge>
                              <span className="flex-1 truncate">{c.controller.name}</span>
                              <span className="text-muted-foreground">
                                {c.controller.readyReplicas}/{c.controller.desiredReplicas}
                              </span>
                            </button>
                            {cOpen && c.pods.map((p) => podRow(p, c.controller.name))}
                          </div>
                        )
                      })}

                    {nsOpen && ns.barePods.length > 0 && (
                      <div>
                        <div className="pl-8 pr-3 py-1.5 font-mono text-[10px] text-muted-foreground">
                          Pods with no controller
                        </div>
                        {ns.barePods.map((p) => podRow(p))}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No workloads are readable with your permissions." />

      {selection && (
        <PodDetailPanel
          selection={selection}
          admin={admin}
          onChanged={reload}
          onClose={() => setSelection(undefined)}
        />
      )}
    </div>
  )
}
```

- [ ] **Step 2: Write the detail panel**

Create `src/components/workloads/PodDetailPanel.tsx`. The Terminal and YAML tabs are placeholders in this task and are filled in by Task 14 — they are named here so the tab strip is built once:

```tsx
import { X } from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { LogsTab } from '@/components/workloads/LogsTab'
import { formatAge } from '@/lib/thresholds'
import type { WorkloadController, WorkloadPod } from '@/lib/workloads'

export interface PodSelection {
  pod: WorkloadPod
  /** The controller the pod belongs to, when the tree could place it. */
  controller?: WorkloadController
}

/**
 * One pod: what it is, and the three ways of working on it.
 *
 * `onChanged` is the tree's own reload — a write here changes what the tree
 * shows, and a panel that leaves a stale list behind is how someone restarts
 * the same thing twice.
 */
export function PodDetailPanel({
  selection,
  admin,
  onChanged,
  onClose,
}: {
  selection: PodSelection
  admin: boolean
  onChanged: () => void
  onClose: () => void
}) {
  const { pod } = selection

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <span className="truncate">
            {pod.namespace}/{pod.name}
          </span>
          <Badge variant="outline" className="font-mono text-[10px]">
            {pod.phase}
          </Badge>
          {pod.restarts > 0 && (
            <Badge variant="outline" className="font-mono text-[10px] border-current text-destructive">
              {pod.restarts} restarts
            </Badge>
          )}
          <Button variant="ghost" size="sm" className="ml-auto" onClick={onClose} aria-label="Close">
            <X />
          </Button>
        </CardTitle>
        <div className="font-mono text-[10px] text-muted-foreground flex flex-wrap gap-x-4">
          <span>node {pod.nodeName || '—'}</span>
          <span>containers {pod.containers.join(', ') || '—'}</span>
          <span>
            age {pod.createdAt ? formatAge(new Date(pod.createdAt).getTime()) : '—'}
          </span>
          {selection.controller && (
            <span>
              controlled by {selection.controller.kind.toLowerCase()} {selection.controller.name}
            </span>
          )}
        </div>
      </CardHeader>
      <CardContent>
        <Tabs defaultValue="logs" className="gap-4">
          <TabsList>
            <TabsTrigger value="logs" className="font-mono text-xs">
              Logs
            </TabsTrigger>
            <TabsTrigger value="terminal" className="font-mono text-xs">
              Terminal
            </TabsTrigger>
            <TabsTrigger value="yaml" className="font-mono text-xs">
              YAML
            </TabsTrigger>
          </TabsList>
          <TabsContent value="logs">
            <LogsTab pod={pod} />
          </TabsContent>
          <TabsContent value="terminal">
            {/* Filled in by Task 14. */}
            <p className="font-mono text-xs text-muted-foreground">Not built yet.</p>
          </TabsContent>
          <TabsContent value="yaml">
            {/* Filled in by Task 14. */}
            <p className="font-mono text-xs text-muted-foreground">Not built yet.</p>
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}
```

`admin` and `onChanged` are declared and unused in this task. `tsconfig.json` sets neither `noUnusedLocals` nor `noUnusedParameters`, so `tsc -b` accepts it; `npm run lint` may warn. **Do not delete the props** — the signature is what Task 14 consumes, and re-adding a prop is how a panel ends up with two ways of being told the same thing.

- [ ] **Step 3: Write the Logs tab**

Create `src/components/workloads/LogsTab.tsx`:

```tsx
import { useCallback, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { createFrameClient } from '@/lib/frame-sdk'
import { pumpLogLines } from '@/lib/pod-logs'
import type { WorkloadPod } from '@/lib/workloads'

const frame = createFrameClient()

/** How many lines to keep in the pane. A followed log is unbounded; the DOM is not. */
const MAX_LINES = 5_000

/**
 * One container's logs, live or from the instance that died.
 *
 * Previous-container logs are the tab that matters most and the one a person
 * has to be *offered*: when a container has crash-looped, the cause is in the
 * instance that died, not in the one running now, and nothing about the
 * default view says so.
 *
 * Nothing here is recorded. A log read is a read, the recorder ignores reads by
 * design, and so there will be no FrameTask saying anyone opened this — which
 * is worth knowing, because a log is a likely place for a secret to be printed.
 * The `pods/log` grant starts at the operator tier for that reason.
 */
export function LogsTab({ pod }: { pod: WorkloadPod }) {
  const [container, setContainer] = useState(pod.containers[0] ?? '')
  const [follow, setFollow] = useState(true)
  const [previous, setPrevious] = useState(false)
  const [lines, setLines] = useState<string[]>([])
  const [error, setError] = useState<string | undefined>()
  const bottom = useRef<HTMLDivElement | null>(null)

  const key = `${pod.namespace}/${pod.name}/${container}/${follow}/${previous}`

  const read = useCallback(
    async (signal: AbortSignal) => {
      setLines([])
      setError(undefined)
      try {
        const res = await frame.workloads.logs({
          namespace: pod.namespace,
          pod: pod.name,
          container,
          follow,
          previous,
          tailLines: 500,
        })
        if (!res.ok) {
          setError(`${res.status} ${await res.text()}`)
          return
        }
        const body = res.body
        if (!body) return
        const reader = body.getReader()
        signal.addEventListener('abort', () => void reader.cancel().catch(() => {}))
        await pumpLogLines(reader, (line) => {
          if (signal.aborted) return
          setLines((prev) => (prev.length >= MAX_LINES ? [...prev.slice(1), line] : [...prev, line]))
        })
      } catch (e) {
        if (!signal.aborted) setError(e instanceof Error ? e.message : String(e))
      }
    },
    [pod.namespace, pod.name, container, follow, previous],
  )

  useEffect(() => {
    if (!container) return
    const controller = new AbortController()
    void read(controller.signal)
    // Aborting is what stops a followed stream when the container, the pod or
    // the tab changes. Without it every switch leaves its reader running and
    // the pane interleaves two containers.
    return () => controller.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  useEffect(() => {
    if (follow) bottom.current?.scrollIntoView({ block: 'end' })
  }, [lines, follow])

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-4">
        <Select value={container} onValueChange={setContainer}>
          <SelectTrigger className="w-56 font-mono text-xs">
            <SelectValue placeholder="Container" />
          </SelectTrigger>
          <SelectContent>
            {pod.containers.map((c) => (
              <SelectItem key={c} value={c} className="font-mono text-xs">
                {c}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="flex items-center gap-2">
          <Switch id="log-follow" checked={follow} onCheckedChange={setFollow} disabled={previous} />
          <Label htmlFor="log-follow" className="font-mono text-xs text-muted-foreground">
            Follow
          </Label>
        </div>

        <div className="flex items-center gap-2">
          <Switch id="log-previous" checked={previous} onCheckedChange={setPrevious} />
          <Label htmlFor="log-previous" className="font-mono text-xs text-muted-foreground">
            Previous container
          </Label>
        </div>

        <Button
          variant="outline"
          size="sm"
          className="ml-auto font-mono"
          onClick={() => setLines([])}
        >
          Clear
        </Button>
      </div>

      <p className="font-mono text-[10px] text-muted-foreground">
        Reading logs is not recorded. Anything printed here — including a secret — leaves no trace
        that it was read.
      </p>

      {error && <p className="font-mono text-xs text-destructive">{error}</p>}

      <ScrollArea className="h-96 border rounded-md bg-muted/20">
        <pre className="p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all">
          {lines.join('\n')}
        </pre>
        <div ref={bottom} />
      </ScrollArea>
    </div>
  )
}
```

- [ ] **Step 4: Wire it into the app**

In `src/App.tsx`:

1. Add `TreeStructure` to the existing `@phosphor-icons/react` import block.
2. Add the lazy import beside `TasksView`:
   ```ts
   const WorkloadsView = lazy(() => import('@/components/workloads/WorkloadsView').then((m) => ({ default: m.WorkloadsView })))
   ```
3. Add `| 'workloads'` to the `TabId` union, next to `'applications'`.
4. Add the NAV entry as the **first** item of the `Workloads` group, above `applications`:
   ```tsx
       {
         id: 'workloads',
         label: 'Workloads',
         icon: <TreeStructure />,
         description: 'Every workload on the cluster — logs, a shell, restart, scale and the manifest',
         tabs: [{ id: 'workloads', label: 'Workloads' }],
       },
   ```
5. Add the `renderTab` case in the Workloads section:
   ```tsx
       case 'workloads':
         return <WorkloadsView />
   ```

The `TabId` union and the `default: const unhandled: never = tab` guard check each other, so a typo in either place is a compile error rather than a blank screen.

- [ ] **Step 5: Run the checks**

```bash
npm run build && npx vitest run
```

Expected: `tsc -b` clean and the whole vitest suite green. Nothing new is *covered* here — that is the point of the constraint at the top of this task — so a green run means "nothing that was covered broke", not "the screen works".

- [ ] **Step 6: Commit**

```bash
git add src/components/workloads src/App.tsx && git commit -m "feat(ui): a Workloads tree with a pod detail panel and live logs"
```

---

### Task 14: the Terminal tab, the YAML tab, and the four write actions

The rest of the panel: a real terminal, an editable manifest, and the four things a person came here to do — each behind a confirmation naming the object and the consequence, each carrying an `X-Frame-Action` label.

This is the task that adds the lot's only two dependencies.

**Files:**
- Modify: `package.json` (two entries in `dependencies`)
- Create: `src/components/workloads/TerminalTab.tsx`
- Create: `src/components/workloads/YamlTab.tsx`
- Create: `src/components/workloads/WorkloadActions.tsx`
- Modify: `src/components/workloads/PodDetailPanel.tsx` (mount the three, and the action bar)

**A distinction to get right, because two different screens are one word apart.** `canOperateWorkloads` gates the YAML tab's **Save button**, not the tab. Reading a manifest stays available everywhere the tree shows the workload — the `get` is granted cluster-wide and reading a DaemonSet's YAML in `kube-system` is one of the more useful things this screen does. Only the write is bounded. Hiding the tab would remove a capability nobody took away; leaving Save enabled would offer a button that 403s on click, after the person has typed an edit.

**Interfaces:**
- Consumes: `execSubprotocols`, `execUrl`, `decodeFrame`, `frameText`, `encodeStdin`, `encodeResize`, `execExitMessage`, `CHANNEL_STDOUT`, `CHANNEL_STDERR`, `CHANNEL_ERROR` (Task 9); `ownershipWarning`, `canOperateWorkloads`, `EditableKind` (Task 8); `workloads.manifest/object/applyManifest/restart/scale/deletePod` (Task 12); `ensureToken` from `@/lib/auth`; `PodSelection` (Task 13).
- Produces:
  ```tsx
  export function TerminalTab(props: { pod: WorkloadPod; admin: boolean }): JSX.Element
  export function YamlTab(props: {
    kind: EditableKind
    namespace: string
    name: string
    onSaved: () => void
  }): JSX.Element
  export function WorkloadActions(props: {
    selection: PodSelection
    onChanged: () => void
  }): JSX.Element
  ```

- [ ] **Step 1: Add the two dependencies**

In `package.json`, in `dependencies`, in alphabetical position (`@xterm/*` sorts after `@types/*`-free scoped entries and before `class-variance-authority`; the block is alphabetical, so they go after `@tailwindcss/vite`):

```json
        "@xterm/addon-fit": ">=0.11.0,<0.12",
        "@xterm/xterm": ">=6.0.0,<7",
```

Both bounds are explicit rather than a caret, because this repository's rule is that a dependency's ceiling is a decision someone wrote down. `@xterm/addon-fit` is pre-1.0, where a minor bump is the breaking one, so its ceiling is the next minor.

```bash
npm install
git diff --stat package.json package-lock.json
```

Expected: exactly two new entries in `package.json`. Confirm the lockfile added nothing else transitive that matters — both packages are dependency-free:

```bash
npm ls @xterm/xterm @xterm/addon-fit
```

- [ ] **Step 2: Write the terminal**

Create `src/components/workloads/TerminalTab.tsx`:

```tsx
import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

import { Button } from '@/components/ui/button'
import { ensureToken } from '@/lib/auth'
import {
  CHANNEL_ERROR,
  CHANNEL_STDERR,
  CHANNEL_STDOUT,
  decodeFrame,
  encodeResize,
  encodeStdin,
  execExitMessage,
  execSubprotocols,
  execUrl,
  frameText,
} from '@/lib/exec-protocol'
import type { WorkloadPod } from '@/lib/workloads'

/**
 * `sh` is the only shell every image on this cluster has. `-c 'exec bash …'`
 * upgrades where bash exists and falls back where it does not, in one command,
 * so nobody has to guess which image they are opening.
 */
const SHELL = ['/bin/sh', '-c', 'exec bash -l 2>/dev/null || exec sh -l']

/**
 * An interactive shell in a container.
 *
 * Rendered with xterm rather than by hand: colours, cursor addressing, resize
 * and anything full-screen (`top`, `vim`, a pager) are a terminal emulator's
 * problem, and it is not a problem worth solving twice.
 *
 * The connection is a WebSocket to the apiserver's `pods/exec`, through
 * `frame-uiproxy` like everything else. The token rides a subprotocol because
 * `new WebSocket()` accepts no headers (see `@/lib/exec-protocol`), and the
 * proxy opens a session-shaped FrameTask when the socket connects and closes it
 * when the socket does — so the record carries the duration.
 *
 * Nothing here is tested. It is `.tsx`, which vitest does not execute, and a
 * real socket against a real apiserver is not something a unit test reaches.
 * Everything that could be silently wrong lives in `@/lib/exec-protocol`,
 * which is.
 */
export function TerminalTab({ pod, admin }: { pod: WorkloadPod; admin: boolean }) {
  const host = useRef<HTMLDivElement | null>(null)
  const [container, setContainer] = useState(pod.containers[0] ?? '')
  const [open, setOpen] = useState(false)
  const [error, setError] = useState<string | undefined>()

  useEffect(() => {
    if (!open || !host.current || !container) return

    const term = new Terminal({
      convertEol: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 12,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host.current)
    fit.fit()

    let socket: WebSocket | undefined
    let observer: ResizeObserver | undefined
    let disposed = false

    void (async () => {
      const token = await ensureToken()
      if (disposed) return
      if (!token) {
        setError('No session — sign in again.')
        return
      }
      const ws = new WebSocket(
        execUrl(
          { namespace: pod.namespace, pod: pod.name, container, command: SHELL },
          globalThis.location.origin,
        ),
        execSubprotocols(token),
      )
      ws.binaryType = 'arraybuffer'
      socket = ws

      ws.onopen = () => {
        term.focus()
        ws.send(encodeResize(term.cols, term.rows))
      }
      ws.onmessage = (ev) => {
        const frame = decodeFrame(ev.data as ArrayBuffer)
        if (!frame) return
        if (frame.channel === CHANNEL_STDOUT || frame.channel === CHANNEL_STDERR) {
          term.write(frame.payload)
          return
        }
        if (frame.channel === CHANNEL_ERROR) {
          const message = execExitMessage(frameText(frame))
          if (message) term.write(`\r\n\x1b[31m${message}\x1b[0m\r\n`)
        }
      }
      ws.onerror = () => setError('The shell connection failed. Admin rights are required to open one.')
      ws.onclose = () => {
        term.write('\r\n\x1b[90m— session closed —\x1b[0m\r\n')
      }

      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(encodeStdin(data))
      })

      // Both halves of a resize, and both are needed: fit() reflows the
      // browser's view, and the resize frame is what tells the pty. Send only
      // the first and every full-screen program renders into the old width for
      // the life of the session, with nothing to look at.
      observer = new ResizeObserver(() => {
        fit.fit()
        if (ws.readyState === WebSocket.OPEN) ws.send(encodeResize(term.cols, term.rows))
      })
      if (host.current) observer.observe(host.current)
    })()

    return () => {
      disposed = true
      observer?.disconnect()
      socket?.close()
      term.dispose()
    }
  }, [open, container, pod.namespace, pod.name])

  if (!admin) {
    return (
      <p className="font-mono text-xs text-muted-foreground">
        Opening a shell requires an admin account. A shell carries every permission the container
        itself holds, so it is the one action in this screen that is not delegated further down.
      </p>
    )
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <select
          value={container}
          onChange={(e) => setContainer(e.target.value)}
          disabled={open}
          className="border rounded-md bg-background px-2 py-1 font-mono text-xs"
        >
          {pod.containers.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        <Button
          size="sm"
          variant={open ? 'outline' : 'default'}
          className="font-mono"
          onClick={() => {
            setError(undefined)
            setOpen((v) => !v)
          }}
        >
          {open ? 'Close session' : 'Open shell'}
        </Button>
      </div>

      {/* Said where the person opening the session can read it, before they
          open it — not in a document nobody opens. */}
      <p className="font-mono text-[10px] text-muted-foreground">
        This session is recorded: who opened it, which pod and container, when, and for how long.
        What is typed and what is displayed are <strong>not</strong> recorded — keystroke capture
        was considered and rejected, because it would put every secret typed or printed here into
        the audit trail.
      </p>

      {error && <p className="font-mono text-xs text-destructive">{error}</p>}

      <div ref={host} className="h-96 border rounded-md bg-black p-2" />
    </div>
  )
}
```

- [ ] **Step 3: Write the YAML tab**

Create `src/components/workloads/YamlTab.tsx`:

```tsx
import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { FrameAPIError, createFrameClient } from '@/lib/frame-sdk'
import { canOperateWorkloads, ownershipWarning, type EditableKind } from '@/lib/workloads'

const frame = createFrameClient()

/**
 * The resource as stored, editable.
 *
 * The text is the apiserver's own YAML (`Accept: application/yaml`) and goes
 * back as the apiserver's own YAML (`Content-Type: application/yaml`), so
 * nothing in this repository parses or emits YAML and nothing rewrites what a
 * person read. It also means the `resourceVersion` in the text is the one that
 * was read — leave it there: it is what turns a concurrent change into a 409
 * instead of a silent overwrite of someone else's work.
 *
 * The editor is bounded twice. By kind, to the five the Workloads tree shows:
 * "edit any resource" would mean granting admin `patch` across the whole
 * cluster, every Secret and ClusterRole included. And by namespace, to the ones
 * enforcing `baseline` Pod Security — because cluster-wide, `update` on a pod
 * template is `privileged: true` plus a `hostPath: /` volume in a namespace
 * where nothing refuses it, which is node root.
 *
 * That second bound applies to **saving**, never to reading. The tab renders
 * everywhere the tree shows a workload, because looking at a DaemonSet's
 * manifest in `kube-system` is one of the more useful things this screen does
 * and the `get` behind it is granted cluster-wide. Only the Save button is
 * disabled, and it says why — a button that 403s after someone has typed an
 * edit is worse than a button that was never offered.
 */
export function YamlTab({
  kind,
  namespace,
  name,
  onSaved,
}: {
  kind: EditableKind
  namespace: string
  name: string
  onSaved: () => void
}) {
  const [text, setText] = useState('')
  const [before, setBefore] = useState<Record<string, unknown> | undefined>()
  const [warning, setWarning] = useState<string | undefined>()
  const [error, setError] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)

  // Saving is granted only where Pod Security enforces `baseline`. Reading is
  // not gated at all — see this component's doc comment.
  const writable = canOperateWorkloads(namespace)

  const load = useCallback(async () => {
    setError(undefined)
    try {
      // Sequential, not Promise.all: both are GETs on the same path, and
      // reading them one after the other keeps the SDK's in-flight
      // de-duplication out of the question entirely.
      const object = await frame.workloads.object(kind, namespace, name)
      const yaml = await frame.workloads.manifest(kind, namespace, name)
      setBefore(object)
      setText(yaml)
      const meta = (object.metadata ?? {}) as {
        labels?: Record<string, string>
        annotations?: Record<string, string>
      }
      setWarning(ownershipWarning(meta))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [kind, namespace, name])

  useEffect(() => {
    void load()
  }, [load])

  const save = async () => {
    setConfirming(false)
    setBusy(true)
    try {
      await frame.workloads.applyManifest({ kind, namespace, name, before, yaml: text })
      toast.success(`${name} updated`)
      onSaved()
      await load()
    } catch (e) {
      if (e instanceof FrameAPIError && e.statusCode === 409) {
        // The reason the resourceVersion travels with the edit. Say what
        // happened rather than reporting a status code: the person's text is
        // still on screen and still valid, against a version that no longer
        // exists.
        setError(
          'Someone else changed this object while you were editing it. Nothing was written. ' +
            'Reload to get the current version, then re-apply your change.',
        )
      } else {
        setError(e instanceof Error ? e.message : String(e))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      {warning && (
        <p className="font-mono text-xs text-primary border border-primary/40 rounded-md p-2">
          {warning}
        </p>
      )}
      {error && <p className="font-mono text-xs text-destructive">{error}</p>}

      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        spellCheck={false}
        className="h-96 font-mono text-[11px] leading-relaxed"
      />

      <div className="flex items-center gap-2">
        <Button
          size="sm"
          className="font-mono"
          disabled={busy || !writable}
          onClick={() => setConfirming(true)}
        >
          Apply
        </Button>
        <Button size="sm" variant="outline" className="font-mono" disabled={busy} onClick={() => void load()}>
          Reload
        </Button>
        <span className="font-mono text-[10px] text-muted-foreground max-w-lg">
          {writable ? (
            'Applied as a whole-object update, carrying the version you read.'
          ) : (
            <>
              Read-only in <strong>{namespace}</strong>. Saving is granted only where Pod Security
              enforces <code>baseline</code>, because rewriting a pod template is root on the node
              anywhere a privileged pod is still admitted. Use <code>kubectl</code> for
              infrastructure workloads.
            </>
          )}
        </span>
      </div>

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Apply your changes to {kind.toLowerCase()} {namespace}/{name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              The manifest is validated against the apiserver first, as a dry run that stores
              nothing and is not itself recorded, and only written if it is accepted. One row
              appears on the Tasks screen, naming the fields you changed.
              {warning ? ` ${warning}` : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void save()}>Apply</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
```

- [ ] **Step 4: Write the three write actions**

Create `src/components/workloads/WorkloadActions.tsx`:

```tsx
import { useState } from 'react'
import { toast } from 'sonner'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { PodSelection } from '@/components/workloads/PodDetailPanel'
import { createFrameClient } from '@/lib/frame-sdk'
import { canOperateWorkloads } from '@/lib/workloads'

const frame = createFrameClient()

type Pending = 'restart' | 'scale' | 'delete' | undefined

/**
 * Restart, scale and delete-a-pod, each behind a dialog that names the object
 * and the consequence.
 *
 * The delete dialog says two different things, because it is two different
 * actions behind one button: under a controller the pod is replaced, on a bare
 * pod the deletion is final. The person clicking has to know which one they
 * have.
 *
 * Restart and Scale are not offered outside the namespaces where they are
 * granted, and the screen says why instead of leaving a button that returns
 * 403. The grant is a RoleBinding in each namespace enforcing `baseline` Pod
 * Security (`deploy/kubernetes/base/rbac-workload-operator.yaml`), because
 * patching a pod template is node root wherever a privileged pod is still
 * admitted — so the asymmetry is the security control, not a rollout gap, and
 * it will not go away.
 *
 * Delete pod is not gated: `pods delete` is a cluster-wide operator grant and
 * deleting a pod cannot mint a privileged one.
 */
export function WorkloadActions({
  selection,
  onChanged,
}: {
  selection: PodSelection
  onChanged: () => void
}) {
  const { pod, controller } = selection
  const [pending, setPending] = useState<Pending>()
  const [replicas, setReplicas] = useState(String(controller?.desiredReplicas ?? 1))
  const [busy, setBusy] = useState(false)

  const run = async (what: string, fn: () => Promise<void>) => {
    setPending(undefined)
    setBusy(true)
    try {
      await fn()
      toast.success(what)
      onChanged()
    } catch (e) {
      toast.error(`Failed: ${what}`, {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(false)
    }
  }

  // Whether the grant reaches this namespace at all, decided from the same
  // list the RoleBindings are generated from — not from "is this
  // infrastructure", which would offer the button in a namespace that is
  // merely unlabelled.
  const operable = canOperateWorkloads(pod.namespace)

  const restartable =
    operable &&
    controller &&
    (controller.kind === 'Deployment' ||
      controller.kind === 'StatefulSet' ||
      controller.kind === 'DaemonSet')

  const target = Number(replicas)
  const targetValid = Number.isInteger(target) && target >= 0

  return (
    <div className="flex flex-wrap items-center gap-2">
      {!operable && controller && (
        <span className="font-mono text-[10px] text-muted-foreground max-w-md">
          Restart and scale are not available in <strong>{pod.namespace}</strong>. They are granted
          only where Pod Security enforces <code>baseline</code>, because changing a pod template is
          root on the node anywhere a privileged pod is still admitted. Use <code>kubectl</code> for
          infrastructure workloads.
        </span>
      )}
      {restartable && (
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={busy}
          onClick={() => setPending('restart')}
        >
          Restart {controller.kind.toLowerCase()}
        </Button>
      )}
      {operable && controller?.scalable && (
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={busy}
          onClick={() => setPending('scale')}
        >
          Scale
        </Button>
      )}
      <Button
        size="sm"
        variant="outline"
        className="font-mono text-destructive"
        disabled={busy}
        onClick={() => setPending('delete')}
      >
        Delete pod
      </Button>

      <AlertDialog open={pending === 'restart'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Restart {controller?.kind.toLowerCase()} {pod.namespace}/{controller?.name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Every pod is replaced, one batch at a time, following this controller's own update
              strategy — the pod template's restart annotation is bumped rather than pods being
              deleted, so surge and maxUnavailable are respected. Expect a brief capacity dip.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                controller &&
                restartable &&
                void run(`${controller.name} restarting`, () =>
                  frame.workloads.restart(
                    controller.kind as 'Deployment' | 'StatefulSet' | 'DaemonSet',
                    controller.namespace,
                    controller.name,
                  ),
                )
              }
            >
              Restart
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={pending === 'scale'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Scale {controller?.kind.toLowerCase()} {pod.namespace}/{controller?.name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Currently {controller?.desiredReplicas} replicas.
              {target === 0
                ? ' Scaling to zero stops this workload entirely: every pod is removed and nothing serves until it is scaled back up.'
                : ' The new count is applied through the scale subresource, which cannot change anything but the replica count.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <Input
            value={replicas}
            inputMode="numeric"
            onChange={(e) => setReplicas(e.target.value)}
            className="font-mono text-xs w-24"
            aria-label="Replicas"
          />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={!targetValid}
              onClick={() =>
                controller?.scalable &&
                targetValid &&
                void run(`${controller.name} scaled to ${target}`, () =>
                  frame.workloads.scale(
                    controller.kind as 'Deployment' | 'StatefulSet',
                    controller.namespace,
                    controller.name,
                    target,
                  ),
                )
              }
            >
              {target === 0 ? 'Stop' : 'Scale'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={pending === 'delete'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Delete pod {pod.namespace}/{pod.name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              {controller
                ? `${controller.kind} ${controller.name} owns this pod, so it will be recreated straight away. This is how you restart one pod without restarting the rest.`
                : 'Nothing owns this pod. Deleting it is final — no controller will bring it back, and whatever it was doing stops for good.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                void run(`${pod.name} deleted`, () =>
                  frame.workloads.deletePod(pod.namespace, pod.name),
                )
              }
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
```

- [ ] **Step 5: Mount them in the panel**

In `src/components/workloads/PodDetailPanel.tsx`, add the imports:

```tsx
import { TerminalTab } from '@/components/workloads/TerminalTab'
import { WorkloadActions } from '@/components/workloads/WorkloadActions'
import { YamlTab } from '@/components/workloads/YamlTab'
```

add the action bar as the last child of `CardHeader`:

```tsx
        <WorkloadActions selection={selection} onChanged={onChanged} />
```

and replace the two placeholder `TabsContent` bodies:

```tsx
          <TabsContent value="terminal">
            <TerminalTab pod={pod} admin={admin} />
          </TabsContent>
          <TabsContent value="yaml">
            <YamlTab kind="Pod" namespace={pod.namespace} name={pod.name} onSaved={onChanged} />
          </TabsContent>
```

If Step 2 of Task 13 renamed `admin`/`onChanged` to `_admin`/`_onChanged` to satisfy `noUnusedParameters`, put the names back now — both are used here.

- [ ] **Step 6: Run the checks**

```bash
npm run build && npx vitest run
```

Expected: `tsc -b` clean — in particular the `@xterm/xterm` types resolving and the CSS import accepted by Vite — and the whole suite green.

Then confirm the dependency count is exactly what was promised:

```bash
git diff package.json | grep -E '^\+ ' | grep -c xterm
git diff package.json | grep -cE '^\+\s+"'
```

Expected: `2` and `2`. Anything higher means something else was added.

- [ ] **Step 7: Commit**

```bash
git add package.json package-lock.json src/components/workloads && git commit -m "feat(ui): a shell, an editable manifest, and the four writes behind confirmations"
```

---

### Task 15: the documentation, and the check that has never been run

**Files:**
- Modify: `docs/crd-reference.md` (the `## FrameTask` section, from line 608)
- Modify: `docs/api.md` (after `## Raw Kubernetes API`, line 205)
- Modify: `docs/deployment.md` (the `## RBAC` section, after "The viewer / editor / admin tiers")
- Modify: `docs/development.md` (the dev-loop caveats, after the `AUTH_PROXY_TARGET` paragraph)
- Modify: `docs/roadmap.md` (the "Current state" bullets)
- Modify: `docs/superpowers/specs/2026-09-09-lot2-workloads-design.md` (the `**Status:**` line)

**Interfaces:**
- Consumes: everything above. Produces no code.

- [ ] **Step 1: Write the failing check**

The check here is a reader's, not a compiler's, so make it explicit — these claims must be findable in the docs afterwards, and none of them is today:

```bash
grep -c 'subresource' docs/crd-reference.md
grep -c 'pods/exec' docs/api.md docs/deployment.md
grep -c 'taskNamespace' docs/deployment.md
grep -c 'base64url.bearer' docs/api.md
grep -c 'Operating a workload' docs/deployment.md
grep -c 'pod-security.kubernetes.io' docs/deployment.md
grep -c 'dryRun' docs/crd-reference.md docs/api.md docs/deployment.md
```

- [ ] **Step 2: Run it and watch it fail**

Expected: every count is `0` except `taskNamespace` in `docs/deployment.md`, which Task 1 already wrote — a `1` there and zeros everywhere else is the correct starting state.

- [ ] **Step 3: Write the documentation**

**`docs/crd-reference.md`**, in the `## FrameTask` section, add to the `spec.target` description:

> `target.subresource` is the trailing segment of the request path when there is one — `exec`, `log`, `scale`, `eviction` — and empty for a request on the object itself. It exists because without it a shell opened in a pod records as `create pods/<name>`, the same string a pod create produces, so the product's decision to record exec sessions would have had no visible effect. **Added post-freeze, on 2026-09-09 (lot 2), and it is a field on an existing kind** — the same class of break as `FrameUser.spec.state`, and cheaper: `FrameTask` is `v1beta1`-only with no conversion webhook, so there is no second version to keep lossless and no conversion function to write.
>
> One consequence worth stating: `verb` for an exec is `create`, not `get`, even though the request arrives as an HTTP GET. A browser opens an exec as a WebSocket upgrade and `new WebSocket()` can issue nothing else; the apiserver authorizes it as `create pods/exec` regardless, and the record uses the word the RBAC rule uses so the two can be read together.

and a paragraph on the session shape:

> **A `FrameTask` for an exec is a session, not a request.** It opens when the WebSocket is established and closes when the socket closes, so `status.startedAt` and `status.finishedAt` bracket the whole shell and a three-hour session is visible as three hours. `status.httpCode` is **101** for a session that opened — `Succeeded`, not `Failed`, despite being below 200. `spec.action` is built by the recorder rather than supplied by the console (`open a shell in <ns>/<pod> (<container>)`), because a WebSocket carries no `X-Frame-Action` header.
>
> **A dry run leaves no `FrameTask` at all.** The manifest editor validates every edit with a `PUT ?dryRun=All` before it writes, which is also how it learns which fields changed; a dry run stores nothing, so recording it would put two rows in the trail for one edit.

**`docs/api.md`**, a new section after `## Raw Kubernetes API`:

```markdown
## Operating a workload from the console

The Workloads screen talks to ordinary Kubernetes endpoints through
`frame-uiproxy`, like everything else. Nothing here is a Frame API.

| What | Request |
|---|---|
| The tree | `GET /apis/apps/v1/{deployments,statefulsets,daemonsets,replicasets}`, `GET /apis/batch/v1/jobs`, `GET /api/v1/pods` — all cluster-wide |
| Logs | `GET /api/v1/namespaces/{ns}/pods/{pod}/log?container=&follow=&previous=&tailLines=` |
| Shell | `GET /api/v1/namespaces/{ns}/pods/{pod}/exec?container=&stdin=true&stdout=true&tty=true&command=…`, upgraded to a WebSocket |
| Restart | `PATCH` the controller, `application/strategic-merge-patch+json`, bumping `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]` |
| Scale | `PATCH …/{name}/scale`, `application/merge-patch+json` |
| Delete a pod | `DELETE /api/v1/namespaces/{ns}/pods/{pod}` |
| Read a manifest | `GET` the object with `Accept: application/yaml` |
| Write a manifest | `PUT` the object with `Content-Type: application/yaml`, twice: once with `?dryRun=All`, then for real |

**The dry run leaves no `FrameTask`, and that is a rule rather than an
accident.** `TaskRecorder.Start` refuses any request carrying a `dryRun`
parameter (`internal/uiproxy/recorder.go`), because a dry run stores nothing —
recording it would put two rows in the trail for one edit, one of which changed
nothing. So an edit is **one** row. Reading the trail: two rows for one edit
means that rule was lost; *no* row after a successful edit means the action
label overflowed `FrameTaskSpec.Action`'s 200-character cap and the apiserver
refused the record, which the recorder logs and the user never sees.

Four things about that list are not obvious.

**YAML is the apiserver's, not the console's.** `application/yaml` is a
supported representation of every resource, for both `Accept` and
`Content-Type`, so the console ships no YAML parser and no serialiser. That is
also what makes the audit label trustworthy: the "after" side of the diff is
the apiserver's own reading of the edited text, returned by the dry run, rather
than this repository's guess at it.

**The edit carries the `resourceVersion` that was read**, because it is still in
the YAML the person edited. A concurrent change therefore produces a **409**
instead of silently overwriting someone else's work. Do not strip it.

**The shell's token rides a subprotocol.** `new WebSocket(url, protocols)`
accepts no request headers, so the bearer token cannot travel in
`Authorization`. The console offers two subprotocols —
`v4.channel.k8s.io` and
`base64url.bearer.authorization.k8s.io.<unpadded-base64url-token>` — which is
Kubernetes' own convention for this. Here `frame-uiproxy` consumes and
**strips** the second one: the token is authd's, the apiserver would neither
accept it nor recognise the entry, and forwarding it would write a live
credential into the apiserver's audit log on every shell.

Once upgraded, every frame is binary and begins with a channel byte: 0 stdin,
1 stdout, 2 stderr, 3 a `metav1.Status` for the exit, 4 a terminal resize
(`{"Width":n,"Height":n}` — capitalised, because the apiserver unmarshals it
into a Go struct with no json tags). `stderr` is never requested, because the
apiserver rejects it alongside `tty=true`.
```

**`docs/deployment.md`**, in `## RBAC`, a new subsection after "The viewer / editor / admin tiers":

```markdown
### Operating a workload: which tier gets what

Lot 2 (2026-09-09) added the Workloads screen, and with it a
`cluster-control-admin` ClusterRole — until then nothing in
`deploy/kubernetes/base/rbac.yaml` carried `tier: admin` at all, and the admin
tier was satisfied only by the twenty-seven per-kind Frame CRD roles.

| Tier | Gains | Where |
|---|---|---|
| viewer | `apps/daemonsets`, `apps/replicasets`, `batch/jobs` read | cluster-wide |
| operator | `pods/log` get, `pods` delete | cluster-wide |
| operator | `apps/deployments`+`statefulsets` **patch** and their `scale` subresource | **only the namespaces enforcing `baseline`** |
| admin | `pods/exec` create | cluster-wide |
| admin | `update`/`patch` on `pods`, `deployments`, `statefulsets`, `daemonsets`, `jobs` | **only the namespaces enforcing `baseline`** |

The rule behind that column is one line: **no verb that can write a pod template
is granted cluster-wide.** `patch`, `update` and the `scale` subresource are all
bounded to the namespaces where `baseline` refuses a privileged pod. `pods/exec`
is the single exception, because a shell creates no pod and bounding it would
close nothing. State the corollary plainly rather than leaving it to be
inferred: a shell **inherits** the target pod's privilege, so exec into one of
the deliberately privileged pods in an exempt namespace — Ceph's OSDs, the
node-tuning agent, the Talos tooling — is root on the node. Pod Security has no
bearing there precisely because the privilege is already present. That is the
argument for keeping exec admin-only and recorded, and anyone weighing whether
to widen it below admin should weigh it against that sentence, not against the
first half of it.

**Reads are not bounded.** The console shows any workload's YAML anywhere the
tree shows the workload, and writes only where the policy is enforced. The
Workloads screen's YAML tab renders everywhere and disables its Save button
outside the enforced namespaces, saying why.

**Logs start at operator, not viewer.** A viewer sees state; a log is the most
likely place in a cluster for a credential to appear in plain text. It is a
judgement, not a constraint — one line in `cluster-control-operator` moves it —
but note what makes it load-bearing: **reading logs leaves no `FrameTask`.** The
recorder ignores reads by design, so there will be no record that anyone read
one. The mitigation is this tier boundary, not the audit trail.

**`patch` on Deployments and StatefulSets is back, and it is earned rather than
accepted.** It was removed on 2026-08-10 because RBAC cannot bound a patch to
one JSON path: "may set the restartedAt annotation" and "may patch the pod
template to `privileged: true` with a `hostPath: /` volume" are the same grant,
and no namespace on this cluster carried a `pod-security.kubernetes.io/enforce`
label to refuse the second. That comment named the safe way to earn it back,
and lot 2 took it rather than accepting the risk: `baseline` Pod Security is
enforced on the application namespaces
(`deploy/kubernetes/pod-security/namespaces.yaml`), and the grant is a
**RoleBinding into exactly those namespaces**
(`deploy/kubernetes/base/rbac-workload-operator.yaml`), carrying **no tier
label** so the aggregation cannot make it cluster-wide.

The shape is the control. Infrastructure namespaces are not labelled — Ceph's
OSDs, the node-tuning agent's `hostPID`, node-exporter's `hostPath`, the CNI
and the Talos tooling all need what `baseline` forbids — so a cluster-wide
grant would reach precisely the namespaces where nothing refuses the payload.
`go test ./test/manifests/` asserts both halves: that the RoleBinding exists in
every enforced namespace, and that it exists in no other.

**Restart and scale therefore work on application workloads and return 403 on
infrastructure ones.** That asymmetry is permanent and deliberate; the
Workloads panel says so rather than offering a button that fails. `kubectl`
remains the way to restart an infrastructure workload.

**The YAML editor is bounded the same way, and by the same mechanism.** A
cluster-wide `update` on those kinds is the same escalation through a different
door: an admin writes `privileged: true` and a `hostPath: /` volume into a pod
template in an exempt namespace and holds node root. So its verbs live in a
second unaggregated ClusterRole in the same file,
`cluster-control-workload-admin`, bound by RoleBinding into the same seven
namespaces and subjected to `frame:admins` alone. Two roles rather than one
because a RoleBinding carries a single `roleRef` and a single subject list, and
restart is an operator action while the editor is not.

**This repaired the Applications screen's Restart button**, which had returned
403 for everyone including admins since that audit — in the enforced namespaces
only.

**Exec is admin-only, and the session is recorded but its contents are not.**
Who, which pod and container, when, and for how long — never what was typed or
displayed. Keystroke capture was considered and rejected: it would put every
secret typed or printed into the audit store, which would make the audit store
the thing that needs protecting. The terminal tab says so where the person
opening the session can read it.

**The YAML editor is bounded to five kinds** — `pods`, `deployments`,
`statefulsets`, `daemonsets`, `jobs`. "Edit any resource" would mean granting
admins `patch` across the whole cluster, every `Secret` and `ClusterRole`
included, which is far larger than the screen needs and could not be tied to a
call site the way that file requires. `go test ./test/manifests/` asserts both
halves: that admins have those five, and that they do not have `secrets`,
`configmaps`, `clusterroles` or `customresourcedefinitions`.
```

and, immediately before the end-to-end check, a subsection of its own:

```markdown
### Turning Pod Security on

`deploy/kubernetes/pod-security/` labels the application namespaces —
`default`, `inference`, `neura`, `neura-batch`, `neura-database`,
`neura-inference`, `neura-training` — with `baseline`, pinned to `v1.29` so a
cluster upgrade cannot silently tighten what is admitted. Infrastructure
namespaces are deliberately absent: `kube-system`, `monitoring`, `alluxio`,
`ptp`, `cilium`, `rook-ceph`, `gpu-operator`, `node-feature-discovery`,
`velero`, `falco`, `tetragon` and `checkpoint-system` all run pods that
`baseline` forbids, and labelling them would refuse those pods at their next
restart.

**Enforcing does not evict anything. It refuses the next admission.** A
workload that has always violated the policy keeps running and fails the next
time it restarts — which is during an incident, not during the change. So the
rollout is two steps and the middle one is not optional:

```bash
# 1. warn + audit only, which is what the committed manifest carries first
kubectl apply -k deploy/kubernetes/pod-security

# 2. ask the apiserver what enforcing WOULD refuse, without refusing anything
for ns in default inference neura neura-batch neura-database neura-inference neura-training; do
  echo "── $ns"
  kubectl label --dry-run=server --overwrite namespace "$ns" \
    pod-security.kubernetes.io/enforce=baseline \
    pod-security.kubernetes.io/enforce-version=v1.29
done
```

A clean namespace prints only `namespace/<ns> labeled (server dry run)`. A
dirty one prints a `Warning:` line per offending pod, naming the rule it
breaks. Resolve every one — replace a `hostPath` with a PVC, drop a
capability, or move the workload to an unlabelled namespace — and only then
add the `enforce` labels and apply again.

The `audit` label is the backstop for a violation created after that sweep: it
writes a `pod-security.kubernetes.io/audit-violations` annotation into the
apiserver audit log, which is readable only if audit logging is configured on
this cluster's apiserver. Treat the server-side dry run as the instrument and
the annotation as the safety net, not the other way round.

**What the label set decides.** Every write the console can make to a pod
template — restart, scale, and saving an edited manifest — is granted by a
RoleBinding into exactly the namespaces listed there and nowhere else
(`deploy/kubernetes/base/rbac-workload-operator.yaml`). Reads are not bounded
at all: **the console can show any workload's YAML anywhere the tree shows the
workload, and can write only where this policy is enforced.** That is why the
Workloads screen's YAML tab appears on an infrastructure pod but its Save
button is disabled, and why Restart and Scale do not appear there at all.

So removing a namespace from that manifest is not only a Pod Security change:
it also removes restart, scale and Save there, and `OPERABLE_NAMESPACES` in
`src/lib/workloads.ts` must lose it too or the panel offers controls that 403.
A test in each language fails on the mismatch, in both directions.
```

and, immediately after it, plainly labelled as **not yet executed**:

```markdown
### The check that has never once been run end to end

Everything below is proven in Go tests, in vitest and by reading manifests.
None of it has been done on the cluster, and an exec's failure modes are
physical — a session that will not close, a resize that leaves the display
offset, a `Ctrl-C` that does not reach the process. Until someone has done
this, the terminal is unproven.

1. Before anything else, prove Pod Security is doing its job — everything below
   assumes it. In an enforced namespace, ask for the exact payload the grant
   would otherwise permit:

   ```bash
   kubectl -n neura run pod-security-probe --image=busybox --restart=Never --dry-run=server \
     --overrides='{"spec":{"hostPID":true,"containers":[{"name":"probe","image":"busybox","securityContext":{"privileged":true}}]}}' \
     -- sleep 1
   ```

   Expected: a refusal naming `violates PodSecurity "baseline:v1.29"`. **If this
   succeeds, stop** — the label is not in force and the operator grant is node
   root, which is the state the 2026-08-10 removal existed to prevent.
2. Sign in as an operator. Open **Workloads**, expand `neura`, pick a pod, read
   its logs with **Follow** on. Confirm lines arrive without reloading, and that
   the pane is still live after five minutes of silence — that is the
   `proxy_read_timeout` in `deploy/docker/nginx.conf` being right or wrong.
3. Switch **Previous container** on for a pod that has restarted. Confirm the
   output is the dead instance's, not the running one's.
4. Confirm the **Terminal** tab tells that operator it needs an admin account,
   and confirm the apiserver agrees: opening one anyway must fail, not connect.
5. Sign in as an admin. Open a shell. Run `top`, resize the browser window, and
   confirm the program reflows — that is channel 4 reaching the pty. Press
   `Ctrl-C` and confirm it reaches the process. Close the tab.
6. Open **Tasks**. Confirm there is one row for the session, that it names the
   pod and container, that its outcome is `Succeeded · 101`, and that the
   interval between `startedAt` and `finishedAt` matches how long the shell was
   open. A row saying `500`, or no row at all, is `statusRecorder.Hijack` or
   `isExecUpgrade`.
7. Restart a Deployment in `neura` from the panel, then scale it to zero and
   back. Confirm each leaves one row on Tasks naming the action rather than the
   request — `restart deployment neura/api`, not `patch deployments/api`.
8. Open a pod in `rook-ceph` or `monitoring`. Confirm the panel shows **no**
   Restart or Scale button and says why; confirm the **YAML tab still opens and
   shows the manifest** — reads are not bounded — and that its **Save button is
   disabled** with a reason, rather than 403-ing after an edit has been typed.
   Then confirm the apiserver agrees:

   ```bash
   kubectl auth can-i patch  deployments.apps -n neura     --as-group=frame:operators --as=alice@example.com
   kubectl auth can-i patch  deployments.apps -n rook-ceph --as-group=frame:operators --as=alice@example.com
   kubectl auth can-i update deployments.apps -n rook-ceph --as-group=frame:admins    --as=root@example.com
   kubectl auth can-i get    deployments.apps -n rook-ceph --as-group=frame:viewers   --as=bob@example.com
   ```

   Expected: `yes`, `no`, `no`, `yes`. A `yes` on either middle line means a
   grant escaped its namespaces and node root is one pod template away. A `no`
   on the last means the reads were bounded by mistake and the console goes
   blank on every infrastructure namespace.
9. Open the **Applications** screen as an operator and restart something in
   `neura`. That button has returned 403 since 2026-08-10 and this lot is what
   repairs it; confirm it now succeeds and leaves a Tasks row.
10. Edit a Deployment's `spec.replicas` in the **YAML** tab and apply. Confirm
    the Tasks row reads `edit deployment neura/api: spec.replicas` and that
    there is exactly **one** row, not two. Two means the dry run is being
    recorded; **none at all after a successful apply** means the label
    overflowed the 200-character cap and the apiserver refused the record.
11. Open the same object in two browser tabs, apply in one, then apply in the
    other. Confirm the second reports that someone else changed it and wrote
    nothing. That is the `resourceVersion` doing its job; a success here means
    an edit can silently overwrite another.
12. Open an object Argo CD manages and confirm the editor says so before the
    edit, not after.
```

**`docs/development.md`**, after the `AUTH_PROXY_TARGET` paragraph:

```markdown
**The Terminal tab does not work under `kubectl proxy`.** A shell authenticates
by offering the authd token as a WebSocket subprotocol, which
`frame-uiproxy` consumes and strips. `kubectl proxy` is not that proxy: it
authenticates with your own kubeconfig and forwards the subprotocol untouched,
so the apiserver sees a bearer token it cannot verify and refuses the
handshake. Logs, the tree and every write work locally; the terminal needs the
real sidecar, which means a port-forward to a deployed `cluster-control-ui`
pod or the cluster itself.
```

**`docs/roadmap.md`**, in "Current state", beside the `FrameUser.spec.state` bullet:

```markdown
- ✅ **`FrameTask.spec.target.subresource`**, added post-freeze (2026-09-09,
  lot 2): the second field added to a frozen kind, and the cheapest of them —
  `FrameTask` is `v1beta1`-only with no conversion webhook, so nothing is lossy
  and no conversion function exists to keep in step. It is what makes a shell
  opened in a pod (`create pods/<name>/exec`) distinguishable from a pod being
  created (`create pods/<name>`), without which the decision to record exec
  sessions had no visible effect. See [crd-reference.md](crd-reference.md).
- ✅ **Pod Security is enforced on the application namespaces** (2026-09-09,
  lot 2): `baseline`, pinned to `v1.29`, on `default`, `inference` and the five
  `neura-*` namespaces
  (`deploy/kubernetes/pod-security/namespaces.yaml`). Infrastructure namespaces
  are deliberately exempt — Ceph, the node-tuning agent, the CNI, node-exporter
  and the Talos tooling all need what `baseline` forbids. This is what earned
  back the `apps/deployments` `patch` grant removed on 2026-08-10 as "the single
  grant that turned the unauthenticated UI into cluster-admin": it is now bound
  by a `RoleBinding` into exactly the enforced namespaces — and so is the YAML
  editor's `update`/`patch`, which cluster-wide is the same escalation through a
  different door. Restart, scale and saving a manifest therefore work on
  application workloads and 403 on infrastructure ones; reads are untouched, so
  the console shows any workload's YAML anywhere and writes only where the
  policy is enforced. `pods/exec` is the one workload grant that stays
  cluster-wide, because a shell creates no pod — but it inherits the target
  pod's, so a shell in a privileged infrastructure pod is node root, which is
  why exec is admin-only and every session is recorded.
  It also repaired the Applications screen's Restart button, dead since that
  removal. The
  cluster-wide grant that the 2026-08-09 security review called for on *every*
  namespace is still not that — the exempt list is real and permanent. See
  [deployment.md](deployment.md), "Turning Pod Security on".
- ✅ **The cluster is operable from the console** (2026-09-09, lot 2): a
  workload tree, pod logs, an interactive shell, rollout restart, scale, delete
  a pod and an editable manifest — under the signed-in person's own identity,
  with every write recorded. Port-forward, resource creation, log download,
  cross-pod log search and batch actions are deliberately out
  ([the design](superpowers/specs/2026-09-09-lot2-workloads-design.md)). The
  terminal has **never been opened against the cluster**; see
  [deployment.md](deployment.md), "The check that has never once been run end
  to end".
```

**The spec's status line**, in `docs/superpowers/specs/2026-09-09-lot2-workloads-design.md`:

```markdown
**Status:** design approved 2026-09-09; implemented on `feat/lot2-workloads`. The end-to-end cluster check in docs/deployment.md ("The check that has never once been run end to end") **has not been executed** — no shell has been opened from the console against a real apiserver, and no `FrameTask` for a session has been observed.
```

- [ ] **Step 4: Run everything, one last time**

```bash
grep -c 'subresource' docs/crd-reference.md
grep -c 'pods/exec' docs/api.md docs/deployment.md
grep -c 'base64url.bearer' docs/api.md
grep -c 'Operating a workload' docs/deployment.md
grep -c 'pod-security.kubernetes.io' docs/deployment.md
grep -c 'dryRun' docs/crd-reference.md docs/api.md
make test
go test ./test/manifests/
npm run build && npx vitest run
```

Expected: every count non-zero, and the whole tree green — Go, envtest, manifests and frontend — before the branch is offered for review.

- [ ] **Step 5: Commit**

```bash
git add docs && git commit -m "docs: operating a workload from the console, and what is still unproven"
```

---

## What this plan does not prove

Stated here rather than discovered in review:

- **No `.tsx` file in this lot is executed by any test.** Vitest's include is
  `.test.ts` only, so `WorkloadsView`, `PodDetailPanel`, `LogsTab`,
  `TerminalTab`, `YamlTab` and `WorkloadActions` carry no coverage at all. Every
  decision worth testing was moved into `src/lib`; what remains in those six
  files is rendering, checked by `tsc -b` and by looking at it.
- **No real WebSocket against a real apiserver.** Task 2's upgrade test uses a
  hand-rolled 101 between two `httptest` servers, which proves the proxy
  carries an upgrade and nothing about `v4.channel.k8s.io`, xterm, or a pty.
- **The RBAC tests read manifests, not a cluster.** `test/manifests` proves the
  shipped YAML aggregates what it should, that both workload grants are bound in
  every enforced namespace and in no other, that neither carries a tier label,
  and that the reads stay cluster-wide. Only `kubectl auth can-i --as-group` against the cluster proves the
  apiserver agrees.
- **Nothing in the test suite proves Pod Security is in force.** The manifest is
  checked for shape and the two lists are checked against each other, but
  "`baseline` refuses a privileged pod in `neura`" is a live-cluster fact. Task
  6's Step 6 probe and step 1 of the end-to-end check are the only things that
  establish it, and until one of them has been run the operator `patch` grant is
  a grant with no guard — which is the state the 2026-08-10 removal existed to
  prevent. **Task 7 must not be applied to a cluster where that probe has not
  been run.**
- **The violation sweep in Task 6 is a live-cluster step with an unknown
  answer.** The plan assumes nothing about how many violations the seven
  namespaces hold; resolving whatever is found is part of that task, and it may
  turn out that a namespace has to leave the enforced list — in which case it
  also leaves the RoleBinding set and `OPERABLE_NAMESPACES`.
- **The nginx change is checked by `nginx -t`, not by a WebSocket through it.**
  A syntactically valid `map` is not a working upgrade path.
- **The end-to-end check in `docs/deployment.md` is written down and labelled
  unexecuted**, the way lot 0c's was. Nothing in this plan executes it.
