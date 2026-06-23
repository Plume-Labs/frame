# Development

Covers the Go operator. For the UI/API server, see the quick start in the
top-level [README](../README.md) (`npm install && npm run dev / npm run server`).

## Prerequisites

- Go 1.25+
- Docker (for `docker-build` and the e2e Kind cluster)
- `kubectl` 1.28+, access to a cluster for `install` / `deploy`
- The Makefile downloads its own pinned `controller-gen`, `kustomize`,
  `setup-envtest`, and `golangci-lint` into `bin/`.

## Everyday commands

```bash
make help          # list all targets
make build         # manifests + generate + fmt + vet, then build manager binary
make run           # run the controller against your current kubeconfig
make test          # manifests/generate/fmt/vet + envtest unit/integration tests
make lint          # golangci-lint
```

`make test` and `make build` regenerate code and manifests first, so they stay
in sync automatically.

## Regenerating after API changes

Edit `api/v1alpha1/*_types.go`, then:

```bash
make manifests     # regenerate CRDs, RBAC, webhook manifests
make generate      # regenerate zz_generated.deepcopy.go
```

**Never hand-edit** generated files: `config/crd/bases/*`, `config/rbac/role.yaml`,
`config/webhook/manifests.yaml`, `**/zz_generated.*`, or `PROJECT`. Never delete
`// +kubebuilder:scaffold:*` markers. Scaffold new APIs/webhooks with the
`kubebuilder` CLI, not by hand. (See [AGENTS.md](../AGENTS.md).)

## Tests

- **Unit / integration:** `internal/controller/*_test.go` and
  `internal/webhook/v1alpha1/*_test.go` run under **envtest** (a real API server
  + etcd, no kubelet). `make test` provisions the binaries.
- **E2E:** `test/e2e/` expects a dedicated **Kind** cluster — never point it at a
  real cluster. Mirrors the GitHub Actions CI flow.

CI lives in `.github/workflows/` (`build`, `lint`, `test`, `test-e2e`).

## Deploying to a cluster

```bash
make install                       # CRDs only
make deploy IMG=<registry>/frame:tag
make build-installer IMG=<...>     # one consolidated install YAML
```

`make undeploy` / `make uninstall` reverse them.
