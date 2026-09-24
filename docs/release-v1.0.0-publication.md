# Publishing v1.0.0

The release was **prepared locally** on 2026-09-24: branch `release/v1.0.0`
(from `main` at `0ebb2ac`), annotated tag `v1.0.0` on its last commit,
`Chart.yaml` at `version: 1.0.0` / `appVersion: 1.0.0`, and `CHANGELOG.md`.
Nothing was pushed, no image or chart was published, no cluster was touched.
Everything below is for a human to run.

**Read "V1 criteria not met" at the end before publishing.** Three of the six
definition-of-done items are not met today, including a red CI on GitHub. A
tag pushed now would publish a `v1.0.0` that the roadmap's own bar does not
accept.

All commands run from the repository root:

```bash
cd /path/to/frame
```

## 0. Check the prepared state

```bash
git switch release/v1.0.0
git log --oneline main..release/v1.0.0     # the release commits only
git show v1.0.0 --stat | head -20          # annotated tag, points at the branch tip
git rev-parse v1.0.0^{commit} release/v1.0.0   # the two SHAs must be equal
```

The working tree also carries changes that are **not part of the release** and
must stay out of it: `config/manager/kustomization.yaml` (modified) and
`deploy/samples/test-cluster/*.sealed.yaml` (untracked). Never `git add -A`.

## 1. Merge into main

`main` has not moved since the branch was cut, so this is a fast-forward and
the tag stays on the commit `main` points at:

```bash
git switch main
git merge --ff-only release/v1.0.0
git rev-parse main v1.0.0^{commit}   # must print the same SHA twice
```

If `main` has moved in the meantime, the fast-forward refuses. Then rebase the
branch, **move the tag** (`git tag -d v1.0.0` and recreate it on the new tip
with the same message, see step 6 below), and repeat.

## 2. Push main, then the tag

Local `main` was already 3 commits ahead of `origin/main` before the release
branch was cut (`git rev-list --count origin/main..main` = 3 on 2026-09-24);
this push publishes those too.

```bash
git push origin main
# Wait for CI on main to be green before pushing the tag ("V1 criteria not met", item 5).
git push origin v1.0.0
```

What pushing the tag triggers, from `.github/workflows/build.yml`
(`on.push.tags: ['v*']`):

- **Build** pushes the **UI** image (root `Dockerfile`) as
  `ghcr.io/plume-labs/frame:1.0.0`, `:1.0` and `:sha-…`. This is the naming
  defect the release-chain spec describes: the bare repository name carries
  the UI, and the operator, authd, uiproxy and provisiond are not pushed by CI
  at all.
- **Release** runs only if `build`, `integration` and `scan` all pass (which
  themselves need `quality` and `iac`). It renders
  `deploy/kubernetes/overlays/production` into `cluster-control-v1.0.0.yaml`
  and creates the GitHub release with that file and generated notes.

## 3. Publish the operator and provisiond images

The chart has no default operator image (`image.repository` is `required`), and
its `image.tag` defaults to `appVersion`, i.e. **`1.0.0` without a `v`**. The
provisiond image defaults to `frame-provisiond:latest`, a local name. Nothing
in CI publishes either, so they are pushed by hand, from the tagged commit:

```bash
git switch --detach v1.0.0
podman login ghcr.io -u <github-user>          # token with write:packages
make docker-build docker-push CONTAINER_TOOL=podman \
  IMG=ghcr.io/plume-labs/frame-controller:1.0.0
make docker-build-provisiond docker-push-provisiond CONTAINER_TOOL=podman \
  IMG_PROVISIOND=ghcr.io/plume-labs/frame-provisiond:1.0.0
# Optional, for the kustomize-deployed parts (UI, authd, uiproxy, agent):
make docker-build-ui       docker-push-ui       CONTAINER_TOOL=podman IMG_UI=ghcr.io/plume-labs/frame-ui:1.0.0
make docker-build-authd    docker-push-authd    CONTAINER_TOOL=podman IMG_AUTHD=ghcr.io/plume-labs/frame-authd:1.0.0
make docker-build-uiproxy  docker-push-uiproxy  CONTAINER_TOOL=podman IMG_UIPROXY=ghcr.io/plume-labs/frame-uiproxy:1.0.0
make docker-build-agent    docker-push-agent    CONTAINER_TOOL=podman IMG_AGENT=ghcr.io/plume-labs/frame-agent:1.0.0
git switch main
```

Then make each package public on GitHub (Packages -> package settings ->
Change visibility), or every install needs an `imagePullSecret`.

## 4. Publish the chart

**The repository provides no chart-publishing mechanism**: no workflow step,
no `hack/` script, no `make` target, and `docs/deployment.md` only installs
from the local `charts/frame` path. The commands below are a proposal — an
OCI chart on GHCR, next to the images — not an existing procedure.

```bash
git switch --detach v1.0.0
mkdir -p /tmp/frame-chart
helm lint charts/frame --set image.repository=ghcr.io/plume-labs/frame-controller
helm package charts/frame -d /tmp/frame-chart          # -> /tmp/frame-chart/frame-1.0.0.tgz
helm registry login ghcr.io -u <github-user>           # token with write:packages
helm push /tmp/frame-chart/frame-1.0.0.tgz oci://ghcr.io/plume-labs/charts
git switch main
```

Make the `charts/frame` package public as for the images. Check it can be
fetched:

```bash
helm show chart oci://ghcr.io/plume-labs/charts/frame --version 1.0.0
```

The resulting install command (not "one command" in the bare sense: two values
have no default and are required):

```bash
helm install frame oci://ghcr.io/plume-labs/charts/frame --version 1.0.0 \
  --namespace frame-system --create-namespace \
  --set image.repository=ghcr.io/plume-labs/frame-controller \
  --set provisiond.image.repository=ghcr.io/plume-labs/frame-provisiond \
  --set provisiond.image.tag=1.0.0 \
  --set provisiond.media.url=http://<management-ip>:30581
```

(`--set provisiond.enabled=false` instead of the three `provisiond.*` values on
a cluster that will never provision a machine.)

## 5. GitHub release

`gh` is not installed on the machine that prepared this release (`command -v gh` finds nothing); install it or use the GitHub web UI.

If the `Release` job of section 2 ran and created the release, attach the chart
archive and replace the generated notes with the changelog section:

```bash
awk '/^## \[1.0.0\]/{p=1;next} /^## \[/{p=0} /^\[1.0.0\]:/{p=0} p' CHANGELOG.md > /tmp/frame-v1.0.0-notes.md
gh release upload v1.0.0 /tmp/frame-chart/frame-1.0.0.tgz -R Plume-Labs/frame
gh release edit v1.0.0 -R Plume-Labs/frame --title "Frame v1.0.0" --notes-file /tmp/frame-v1.0.0-notes.md
```

If the `Release` job was skipped (a failing upstream job skips it silently),
create the release by hand:

```bash
kustomize build deploy/kubernetes/overlays/production/ > /tmp/cluster-control-v1.0.0.yaml
gh release create v1.0.0 -R Plume-Labs/frame --verify-tag \
  --title "Frame v1.0.0" --notes-file /tmp/frame-v1.0.0-notes.md \
  /tmp/frame-chart/frame-1.0.0.tgz /tmp/cluster-control-v1.0.0.yaml
```

Check: `gh release view v1.0.0 -R Plume-Labs/frame`.

## 6. Recreating the tag if the branch moves

```bash
git tag -d v1.0.0
git tag -a v1.0.0 -m "Frame v1.0.0" -m "See CHANGELOG.md, section 1.0.0."
```

Only before the tag is pushed. A pushed tag is not moved; cut `v1.0.1`.

## 7. Local verification run on 2026-09-24 (branch `release/v1.0.0`)

| Check | Result |
|---|---|
| `helm lint charts/frame` | 1 chart linted, 0 failed (INFO: icon recommended; WARN: `provisiond.media.url` required) |
| `helm template charts/frame >/dev/null` | **exit 1** by design: `provisiond.media.url` (and `image.repository`) are `required`. With both set: exit 0, 79 objects, image `…/frame-controller:1.0.0`, label `helm.sh/chart: frame-1.0.0` |
| `hack/helm-parity.sh` | exit 0, all 9 sections OK |
| `go build ./...` | exit 0 |
| `go test ./...` | first run: 4 envtest failures (FrameAlert/FrameAlertSubscription "no matches for kind"), caused by a stale gitignored `bin/crd-render`; after `make crd-render` (no tracked file changed): 23 packages ok, 0 failed |
| `npx vitest run` | 21 files, 380 tests passed |
| `npx vite build` | built, exit 0 (chunk-size warning only) |

Not run locally: `make test-e2e` (Kind).

## V1 criteria not met

From `docs/roadmap.md`, "V1 definition of done". State checked on 2026-09-24,
GitHub state read from the public Actions API.

1. **API frozen at `v1beta1` with tested conversion and a deprecation policy — met.**
   `+kubebuilder:storageversion` on every `api/*/v1beta1` type; conversion
   tests in `api/*/v1alpha1/conversion_test.go` and
   `internal/controller/*/conversion_{roundtrip,envtest}_test.go`; policy in
   `docs/upgrading.md` section 3. Caveat stated there: the storage migration
   has never been run on a real cluster.
2. **Watch streams, no polling or simulation in the default path — met.**
   `src/lib/k8s-watch.ts`; the last simulation left the default path in
   `0ebb2ac`. Remaining `setInterval`s are the session token refresh
   (`src/App.tsx`), a clock (`InstallsTab.tsx`) and the metrics fallback in
   `useLiveResource.ts`. `simulateStatusChange` in `src/lib/cluster.ts` is
   dead code, called only by tests.
3. **Every controller proven end-to-end on Kind in CI, with metrics and
   events — not met.**
   - The Kind e2e workflow is **red on GitHub**: `E2E Tests` failed on
     `origin/main` (`747c72a`, 2026-09-17, step "Running Test e2e"); the last
     green e2e run is `f9dbd6e` (2026-08-14).
   - `test/e2e` covers the eight original controllers (NodeTuning included,
     in `nodetuning_test.go`), but none of the controllers added since:
     FrameMachine, FrameInstall, FrameStorage, FrameDiskClaim, the alert relay.
   - Custom Prometheus metrics exist for FrameJob, SchedulingPolicy,
     TalosMachineConfig, TalosUpgrade, NodeTuning and FrameService only;
     FrameNode and FrameResourceQuota rely on controller-runtime's generic
     reconcile metrics. FrameStorage and FrameDiskClaim emit no Events.
4. **One-command install from a published, versioned Helm chart — not met.**
   The chart is versioned (1.0.0) but published nowhere, the repository has no
   mechanism to publish it (section 4 is a proposal), no operator image is
   published, and the install needs at least `image.repository` and
   `provisiond.media.url` (or `provisiond.enabled=false`). Phase D's exit —
   install on a fresh cluster, e2e, failover, upgrade from the prior release —
   cannot be met for the upgrade part, since there is no prior release.
5. **CI green across build, lint, unit/envtest and Kind e2e — not met.**
   On `origin/main` (`747c72a`): `Lint` failed, `Tests` failed, `E2E Tests`
   failed, `CI/CD Pipeline` failed (`Quality`: npm lint; `IaC Validation`:
   deploy script syntax; `Build`, `Scan`, `Integration`, `Release` skipped).
   Reproduced locally on the release branch:
   - `npm run lint`: 15 problems (9 errors, 6 warnings), in `src/App.tsx`,
     `src/components/ui/*.tsx`, `src/lib/manifest-diff.test.ts` and the
     gitignored local build output `packages/frame-sdk/dist/frame-sdk.d.ts`
     (absent from a CI checkout);
   - `bin/golangci-lint run`: 51 issues (lll 19, modernize 16, unparam 7,
     staticcheck 4, prealloc 2, misspell 1, revive 1, unused 1);
   - `bash -n deploy/scripts/registry-up.sh`: unexpected EOF looking for a
     matching `'` at line 78.
   The `Tests` failure was not reproduced locally (`go test ./...` is green
   once `bin/crd-render` is fresh; CI runs `make test`, which renders it).
   Consequence for publication: with `quality` and `iac` red, the tag push
   builds nothing and the `Release` job is skipped.
6. **Security and upgrade paths reviewed and documented — met, with caveats.**
   `docs/upgrading.md`, the RBAC section of `docs/deployment.md`,
   `SECURITY.md`. The security review itself
   (`docs/superpowers/reviews/2026-08-09-security-review.md`) is no longer in
   the tree, only in git history (`git show 82e0733^:docs/superpowers/reviews/2026-08-09-security-review.md`),
   and the roadmap still lists open items from Phase B (the editor tier can
   promote an account to `role: admin`; `helm upgrade` applies the
   two-version CRDs before the new manager serves `/convert`).
