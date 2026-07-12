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

Edit `api/v1alpha1/*_types.go`, then:

```bash
make manifests     # regenerate CRDs, RBAC, webhook manifests
make generate      # regenerate zz_generated.deepcopy.go
```

**Never hand-edit** generated files: `config/crd/bases/*`, `config/rbac/role.yaml`, `config/webhook/manifests.yaml`, `**/zz_generated.*`, or `PROJECT`. Never delete `// +kubebuilder:scaffold:*` markers. Scaffold new APIs/webhooks with the `kubebuilder` CLI, not by hand. (See [AGENTS.md](../AGENTS.md).)

### Running against a real cluster

```bash
# Install CRDs
make install

# Run the controller locally (uses your current kubeconfig)
make run

# In another terminal, apply a sample CR
kubectl apply -f config/samples/frame_v1alpha1_framejob.yaml
kubectl describe framejob <name>
```

### Tests

```bash
make test           # envtest: runs all tests under internal/controller and internal/webhook
make test-e2e       # Kind-based e2e (requires make setup-test-e2e first)
```

The CI enforces ≥ 45% envtest coverage on `internal/controller` (tracked in `.github/workflows/test.yml`).

---

## React UI

### Prerequisites

- Node.js 20+
- A cluster with Frame CRDs installed (or just CRDs for UI-only dev)
- `kubectl proxy` running

### Dev loop

```bash
# Terminal 1: proxy the K8s API
kubectl proxy --port=8001

# Terminal 2: start the UI with HMR
npm install
npm run dev    # → http://localhost:5173
```

Vite proxies `/apis/*` → `localhost:8001` (see `vite.config.ts`). The UI reads and writes real CRs.

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
   kubebuilder create api --group frame --version v1alpha1 --kind MyKind
   kubebuilder create webhook --group frame --version v1alpha1 --kind MyKind --defaulting --programmatic-validation
   ```

2. Edit `api/v1alpha1/mykind_types.go` — add `+kubebuilder:` markers.

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
