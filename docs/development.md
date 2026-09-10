# Development

Two independent development loops: the **Go operator** and the **React UI**.

---

## Go operator

### Prerequisites

- Go 1.25+
- Docker (for `docker-build` and the e2e Kind cluster)
- `kubectl` 1.28+ with cluster access for `make install` / `make deploy`

The Makefile downloads pinned `controller-gen`, `kustomize`, `setup-envtest`, and `golangci-lint` into `bin/` on first use.

### Everyday commands

```bash
make help          # list all targets
make build         # manifests + generate + fmt + vet + binary
make run           # run the controller against your current kubeconfig
make test          # manifests/generate/fmt/vet + envtest unit/integration tests
make lint          # golangci-lint
```

`make test` and `make build` regenerate code and manifests first — they stay in sync automatically.

### Regenerating after API changes

Edit `api/frame/v1beta1/*_types.go` or `api/services/v1beta1/*_types.go` — the frozen, stored version — then:

```bash
make manifests     # regenerate CRDs, RBAC, webhook manifests
make generate      # regenerate zz_generated.deepcopy.go
```

Two rules the freeze added. **A change to a `v1beta1` type is a change to a frozen API**: new optional fields and looser validation are fine, renames and tightened bounds are not — see [upgrading.md](upgrading.md), "What the guarantee is". And if the change is not a pure addition, `api/frame/v1alpha1/conversion.go` (or its `services` twin) has to carry it, with a fuzzed round-trip case beside it.

**Never hand-edit** generated files: `config/crd/bases/*`, `config/rbac/role.yaml`, `config/webhook/manifests.yaml`, `**/zz_generated.*`, or `PROJECT`. Never delete `// +kubebuilder:scaffold:*` markers. Scaffold new APIs/webhooks with the `kubebuilder` CLI, not by hand. (See [AGENTS.md](../AGENTS.md).)

### Running against a real cluster

```bash
# Install CRDs
make install

# Run the controller locally (uses your current kubeconfig)
make run

# In another terminal, apply a sample CR
kubectl apply -f config/samples/frame_v1beta1_framejob.yaml
kubectl describe framejob <name>
```

### Tests

```bash
make test           # envtest: runs all tests under internal/controller and internal/webhook
make test-e2e       # Kind-based e2e (requires make setup-test-e2e first)
```

The CI enforces ≥ 45% envtest coverage on `internal/controller` (tracked in `.github/workflows/test.yml`).

### Why envtest reads `bin/crd-render`, not `config/crd/bases`

`config/crd/bases/` is controller-gen's output. It has no `spec.conversion`
stanza, because controller-gen has no marker that emits one — the conversion
webhook is wired by a kustomize patch under `config/crd/patches/`.

envtest can drive a conversion webhook: `WebhookInstallOptions` rewrites each
CRD's `clientConfig` to the locally-served webhook and injects the CA it
generated. But it only does that for a CRD that already declares
`strategy: Webhook`. Reading the bases would therefore have made every
conversion test pass while exercising no conversion at all.

`make crd-render` runs `kustomize build config/crd` into `bin/crd-render/`
(gitignored) and `make test` depends on it. If a suite fails with
"bin/crd-render is missing", run `make crd-render`.

---

## React UI

### Prerequisites

- Node.js 20+
- A cluster with Frame CRDs installed (or just CRDs for UI-only dev)
- `kubectl proxy` running
- something serving `/auth` — see below

### Dev loop

```bash
# Terminal 1: proxy the K8s API
kubectl proxy --port=8001

# Terminal 2: reach authd, which kubectl proxy does not serve
kubectl -n cluster-control port-forward svc/cluster-control-auth 8443:443

# Terminal 3: start the UI with HMR
npm install
npm run dev    # → http://localhost:5173
```

Vite proxies `/api/*` and `/apis/*` → `localhost:8001`, and `/auth/*` →
`https://localhost:8443` (see `vite.config.ts`). The UI reads and writes real
CRs.

**The second terminal is not optional.** Since the per-user identity lot the
console sits behind a session gate: `App` calls `currentSession()` — `POST
/auth/token` — before it renders anything, and `kubectl proxy` serves no
`/auth` at all. Without something answering there, `npm run dev` lands on a
login screen that cannot work, whatever credentials you type. Point
`AUTH_PROXY_TARGET` elsewhere if authd is somewhere other than a
port-forward:

```bash
AUTH_PROXY_TARGET=https://frame.example.internal npm run dev
```

The dev proxy sets `secure: false`, because authd's in-cluster certificate
names its Service, not `localhost`.

**The Terminal tab does not work under `kubectl proxy`.** A shell authenticates
by offering the authd token as a WebSocket subprotocol, which
`frame-uiproxy` consumes and strips. `kubectl proxy` is not that proxy: it
authenticates with your own kubeconfig and forwards the subprotocol untouched,
so the apiserver sees a bearer token it cannot verify and refuses the
handshake. Logs, the tree and every write work locally; the terminal needs the
real sidecar, which means a port-forward to a deployed `cluster-control-ui`
pod or the cluster itself.

Note that `kubectl proxy` authenticates as *your* kubeconfig, so the RBAC the
console sees locally is yours, not the impersonated `frame:` group's. A
screen that works locally can still 403 in the cluster; `deploy/kubernetes`'s
tier bindings are what decide that, and `go test ./test/manifests/` is what
checks them.

### Frontend tests

```bash
npm test             # vitest (unit tests in src/**/*.test.ts)
npm run coverage     # coverage report
npm run lint         # eslint
npm run build        # production build → dist/
```

---

## Adding a new controller

1. Scaffold with kubebuilder:
   ```bash
   kubebuilder create api --group frame --version v1beta1 --kind MyKind
   kubebuilder create webhook --group frame --version v1beta1 --kind MyKind --defaulting --programmatic-validation
   ```

   A brand-new kind starts at `v1beta1` in an existing frozen group and needs no `v1alpha1` spoke — there is nothing to convert from. A new *group* starts at `v1alpha1`; see [roadmap.md](roadmap.md), Phase E.

2. Edit `api/frame/v1beta1/mykind_types.go` — add `+kubebuilder:` markers, including `status.observedGeneration` and a `Ready` condition. Do not add a `status.phase`; see [crd-reference.md](crd-reference.md).

3. Run `make manifests generate` to regenerate CRDs, RBAC, and deepcopy.

4. Implement the reconciler in `internal/controller/mykind_controller.go`:
   - Add finalizer on first reconcile
   - Reconcile desired → actual state
   - Update `.status` conditions
   - Emit a `v1.Event` (`r.Recorder.Event(...)`)
   - Clean up on delete (in finalizer block)

5. Register in `cmd/main.go`.

6. Add envtest coverage in `internal/controller/mykind_controller_test.go`.

---

## CI

| Workflow | What it checks |
|---|---|
| `.github/workflows/build.yml` | `make build` — Go compile |
| `.github/workflows/test.yml` | `make test` — envtest + coverage threshold |
| `.github/workflows/lint.yml` | `make lint` — golangci-lint |
| `.github/workflows/test-e2e.yml` | Kind-based e2e (manual trigger / PR label) |
