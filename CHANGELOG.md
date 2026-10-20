# Changelog

All notable changes to Frame are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the chart and
release tags follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed — Go module path (breaking for importers)

- The Go module is now `github.com/Plume-Labs/frame` (was the module path of
  the maintainer's former personal repository, which did not resolve to this
  repository).
  Every package path changes with it, including the API types
  (`github.com/Plume-Labs/frame/api/frame/v1beta1`, …) and the kubebuilder
  `PROJECT` file. Container images, CRDs, the chart and the on-cluster API
  group are unchanged. **Upgrade note:** code that imports Frame packages
  must replace the prefix. With `OLD` set to the Frame module path your
  `go.mod` currently requires:
  `go mod edit -droprequire "$OLD" && go get github.com/Plume-Labs/frame@<version>`
  then `find . -name '*.go' -exec sed -i "s#$OLD#github.com/Plume-Labs/frame#g" {} +`
  and `gofmt -w`.

### Changed

- **Installation independent of any one site.** `config/overlays/production`
  is now a generic recommended install: it **keeps frame-provisiond**
  (previously deleted from the render) and documents leaving it out as an
  option (the three delete patches, commented out in its
  `kustomization.yaml`). Its comments, `secret-access.yaml`, the Makefile,
  the samples, the UI's "not enabled" panels, the `--inference-gpu-memory-mib`
  help text, the docs and the deploy scripts no longer describe one
  maintainer cluster or its hardware as if it were yours.
  **Upgrade note:** `make deploy-production` and `make diff-production` now
  require `IMG_PROVISIOND=<registry>/frame-provisiond:<tag>` (no `:latest`),
  and on a cluster that used this overlay they now create the
  `frame-provisiond` Deployment and Services, including the NodePort 30581
  on every node. Run `make diff-production` first; to keep the previous
  behaviour, uncomment the delete patches in your copy of the overlay.
  `hack/render-overlay.sh` takes the provisiond image as an optional third
  argument.
- The manager's `--inference-gpu-memory-mib` (default `7680`, an 8 GB-class
  card) is now set explicitly in `config/manager/manager.yaml` and exposed
  in the chart as `inference.gpuMemoryMiB`, and documented in
  `docs/deployment.md`. Set it to your GPUs' usable memory; the rendered
  default is unchanged.
- `deploy/scripts/inference-up.sh` states that its defaults are examples
  tuned on one 8 GB Pascal node, each overridden by its environment
  variable.
- The `Installing` phase budget of `FrameInstall` and `frame bootstrap` is
  now **90 minutes** (was a provisional 45). The first install timed on
  hardware took 55 minutes from the live system to the installed one, plus
  a POST on each side. `frame bootstrap`'s `installTimeout` still overrides
  it. **Upgrade note:** a silent installer is now waited for up to 90 minutes
  before the install is marked `Failed`; the `InstallerResponding` condition
  still reports it early.

### Added

- `deploy/scripts/inference-up.sh`: `LLAMACPP_EXTRA_ARGS`,
  `LLAMACPP_REASONING_BUDGET_MESSAGE`, `LLAMACPP_IMAGE` and
  `INFER_RENDER_ONLY`, so a site keeps its hardware tuning in its own env
  file and can diff a run before applying it. Unset, the output is unchanged.

### Documentation

- `docs/storage.md`: disk serials behind a RAID controller in HBA mode
  (udev reports the WWN; lsblk and Frame's agent still report the serial).

### Removed — configuration of the maintainers' own site

Frame shipped values that described the cluster it was developed on: its
registry, node addresses and names, ingress hostname, application namespaces
and Argo CD application. None of it belongs to the project. It is removed or
replaced by neutral defaults and documented variables; every feature stays
configurable.

- `deploy/kubernetes/pod-security/namespaces.yaml` and
  `deploy/kubernetes/base/rbac-workload-operator.yaml` no longer declare the
  five application namespaces of that site or their RoleBindings. Frame ships
  `default` and `inference` only.
- That site's Argo CD application (under `deploy/gitops/argocd/applications/`),
  `deploy/scripts/test-cluster-up.sh` (its full-stack bring-up) and an empty
  kubeconfig stub under `.test-cluster/` are removed.
- The sample alert rules and alert subscription of
  `deploy/samples/test-cluster/`, named after that site, become
  `example-alerts.yaml` and `frame-alert-subscription-example.yaml`, both
  written for a placeholder application (`my-app`).

### Changed — neutral defaults

- **Console** — the sandbox-pods integration is now
  `integrations.sandboxPods`, default selector `app=sandbox` (1.0 stored it
  under a key and a default selector named after the maintainers' former
  downstream application). The 1.0 key is **no longer read**: see the
  upgrade note below. The Jobs screen's panel is "Sandbox jobs (pods)".
- **Console** — new runtime setting `workloads.extraOperableNamespaces`
  (Settings → Workloads): namespaces, beyond `default` and `inference`, where
  restart, scale and the manifest editor are offered. The compiled list no
  longer names any site's namespaces.
- **authd** (`deploy/kubernetes/authd`): the kustomization no longer pins a
  registry and tag (set them with `make set-image-authd` or in your overlay),
  and `frame-auth-ca` is now issued by a namespaced self-signed `Issuer`
  shipped in the same target (`frame-auth-selfsigned`) instead of a
  `ClusterIssuer` named after one site.
- **Containment** (`deploy/kubernetes/containment/networkpolicy-cluster-control.yaml`):
  node IPs are example values from RFC 5737 (`192.0.2.0/24`) and node names
  are `node-cp`/`node-w1`/`node-w2`; the header says to patch them.
- **Workflow templates** — the inference and training DAG templates, named
  after that site in 1.0, are renamed `inference-dag` / `training-dag` (files
  and `WorkflowTemplate` names), and the FrameJob webhook's known pipelines
  follow.
- **Samples and scripts** — example tenant names use `acme`, storage objects
  `frame-*` (`frame-s3`, `frame-store`…), documentation addresses
  `192.0.2.0/24`, hostnames `example.com`. Scripts no longer default to a
  maintainer's kubeconfig or registry: `node-systemctl.sh`,
  `node-tuning-install.sh` and `traefik-timeouts-up.sh` require `KUBECONFIG`;
  `node-tuning-install.sh` requires `REGISTRY`; `backup-up.sh` requires
  `BACKUP_NAMESPACES` and takes `BACKUP_S3_URL` (schedule renamed `daily`);
  `security-up.sh` takes an optional `INSECURE_REGISTRY`;
  the HPC bootstrap script, named after that site in 1.0, is renamed
  `hpc-bootstrap.sh`.
- **Test fixtures** — the captured iLO 4 responses
  (`internal/redfish/testdata/ilo4-real`) keep their shape, but serial
  numbers, MAC addresses, UUIDs, hostnames, IP addresses and the licence-key
  suffix are replaced by fictitious values (see its `PROVENANCE.md`).

### Upgrade notes — if you relied on those values

- **Application namespaces — read before syncing.** If you deployed
  `deploy/kubernetes/overlays/production` (or `base`) and the five
  application Namespaces it declared in 1.0 exist on your cluster, they were rendered by Frame and are now
  absent from its render. **A GitOps sync with pruning deletes a Namespace
  that left the render, and everything in it**, unless the live object
  carries `argocd.argoproj.io/sync-options: Prune=false`. Before syncing this
  version: add to your own overlay the Namespaces you keep (same Pod Security
  labels) and the two RoleBindings `rbac-workload-operator.yaml` gives
  `default`, re-pointed at each; render your overlay and diff it against the
  live cluster; then list the namespaces in Settings → Workloads so the
  console offers restart, scale and the editor there. See
  docs/deployment.md, "Turning Pod Security on".
- **Sandbox pods.** The console no longer reads the 1.0 key of the
  sandbox-pods integration. If you saved the Settings screen under 1.0, your
  `config.json` (ConfigMap read by the console) holds the selector under
  `integrations.<1.0 key>`: rename that key to `sandboxPods`, or set the
  selector again in Settings → Integrations → "Jobs (sandbox pods)" and save.
  If you never saved the Settings screen and your sandbox pods still carry
  the 1.0 default label, set the selector there too: the default is now
  `app=sandbox`. Until then the Jobs screen's sandbox panel is empty; nothing
  else is affected.
- **authd issuer.** On a cluster where `frame-auth-ca` already exists, keep
  your issuer: patch `issuerRef` of the `frame-auth-ca` Certificate back to
  it in your overlay. Switching issuers re-issues the CA, and an apiserver
  started with `--oidc-ca-file` then distrusts authd until the file is
  redistributed.
- **Pipelines.** FrameJobs naming the 1.0 template names keep working
  against WorkflowTemplates of those names you already have; the webhook only warns that the pipeline is not one Frame
  ships. New jobs should use `inference-dag` / `training-dag`.
- **Containment.** Do not apply the shipped NetworkPolicy as is: patch the
  node addresses first (docs/runbook.md, pre-flight).

## [1.0.0] - 2026-09-24

First tagged release. V1 is a narrowing release: a frozen API, live status,
a packaged operator and a reviewed security posture — see
[docs/roadmap.md](docs/roadmap.md), "The V1 path", for the phases and their
exit criteria, and "Known limitations" below for what V1 does not do.

### API

- `frame.plume-labs.io` and `services.plume-labs.io` are frozen at
  **`v1beta1`**, which is the storage version and conversion hub in both
  groups. `v1alpha1` stays served and deprecated on the eight kinds that
  predate the freeze, with a per-kind deprecation warning naming what changed.
- Conversion webhook `v1alpha1` <-> `v1beta1`, tested at three layers: a fuzzed
  round-trip per kind, envtest specs writing at one version and reading at the
  other, and a Kind e2e spec through a real apiserver with a
  cert-manager-injected CA. `hack/migrate-storage-version.sh` migrates stored
  objects and narrows `status.storedVersions`.
- Written deprecation and migration policy in
  [docs/upgrading.md](docs/upgrading.md): `v1alpha1` is served for at least
  two minor chart releases after the one that introduced `v1beta1`.
- CEL / kubebuilder validation, printer columns, defaults and
  `status.observedGeneration` on every frozen kind; speculative fields
  removed before the freeze (`status.phase`, `FrameJob.spec.namespace`,
  `TalosSecretReference.namespace`, the FrameJob GPU/`serviceClass: LOW`
  constraint).
- Viewer/editor/admin RBAC tier `ClusterRole`s per kind, with explicit verb
  lists instead of `*`.
- Kinds added after the freeze, `v1beta1`-only: `NodeTuning`, `FrameTask`,
  `FrameMachine`, `FrameInstall`, `FrameStorage`, `FrameDiskClaim`,
  `FrameAlert`, `FrameAlertSubscription`.

### Real-time status

- The UI and SDK read Kubernetes watch streams straight off the apiserver
  (`src/lib/k8s-watch.ts`), with re-list on `resourceVersion` expiry,
  reconnect with backoff, and an interval fallback only when a watch cannot be
  established. Twenty-two views converted; intervals remain only for metrics
  and proxied third parties.
- The cluster simulation is gone from the default path: no fake nodes, no
  simulated MTBF or checkpoint feed.

### Packaging

- Versioned Helm chart `charts/frame` (`version`/`appVersion` 1.0.0) for the
  operator, provisiond and their CRDs, RBAC, webhooks and cert-manager
  resources. CRDs render from `templates/` so they upgrade with the chart and
  carry `helm.sh/resource-policy: keep`. Object names match
  `kustomize build config/default` exactly, so an existing kustomize install
  can be adopted with `helm install --take-ownership`;
  `hack/helm-parity.sh` guards that parity by name and by content.
- The UI and authd still deploy from the kustomize manifests under
  `deploy/kubernetes/`.

### Service catalog

- `FrameService` (`services.plume-labs.io`) with one provider, **inference**:
  a llama.cpp server declared as a resource, admitted only if it fits the
  card, scheduled through a `serviceClass` -> `PriorityClass` mapping shared
  with `FrameJob`, and bound through `status.binding` (a same-namespace
  credentials `Secret` plus a credential-free endpoint), projected into other
  namespaces only on request (`spec.binding.projectTo`).

### Security

- Containment of the 2026-08-09 security review: the UI's shared
  ServiceAccount path and its `kubectl proxy` sidecar are gone, baseline Pod
  Security is enforced on the application namespaces, and an admission
  webhook refuses any write of `FrameUser.status.passwordHash` outside the
  status subresource, at either API version.
- Five images scanned by Trivy in CI with a gate on fixed CRITICAL/HIGH
  findings; SBOM and provenance attached to the pushed image.
- Private vulnerability reporting channel in [SECURITY.md](SECURITY.md).
- Falco exception for the GPU stack Frame deploys (nvidia-mps, GPU operator
  driver build) in
  `deploy/samples/test-cluster/falco-gpu-exceptions-values.yaml`, matched on
  `k8smeta.ns.name` because `k8s.ns.name` stays empty on GPU nodes.

### Per-user identity

- authd: Argon2id passwords, an ES256 OIDC issuer with JWKS, usernameless
  WebAuthn passkeys, invitations that die at first enrolment, key listing and
  revocation, disabling a `FrameUser` without losing its keys, and a
  last-admin guard.
- `frame-uiproxy` sidecar: validates authd's token against its JWKS, strips
  caller-supplied impersonation headers and forwards every request to the
  apiserver impersonating the signed-in `FrameUser`, so RBAC tiers are
  enforced per person through `frame:` groups.

### FrameTask

- Every write made through the UI — including the refused ones and shell
  sessions, never a dry run — is recorded as a `FrameTask`, with the
  subresource, so an exec is not mistaken for a pod create. A Tasks view
  lists them.

### Also in this release

- Hardware and provisioning: `FrameMachine` over a hand-written Redfish client,
  `FrameInstall` driving a Debian netinst remastered per machine by
  `frame-provisiond`, disk inventory joined between the BMC and the node agent.
- Storage: `FrameStorage` creates or adopts a StorageClass and never deletes a
  PVC.
- Alerting: an Alertmanager receiver writing `FrameAlert`s and a relay to
  `FrameAlertSubscription`s with retries and purge.
- `NodeTuning` and its node agent for node-local kernel and service settings.

### Known limitations

- **FrameTask audit is purged after 7 days** (`TASK_RETENTION`, default
  `168h`, in `frame-uiproxy`). It is an operational trail, not a durable audit
  log.
- **Single site.** Frame manages one local cluster; `zones` and `racks` are
  failure-domain labels within it. Multi-site federation is V2.
- **No multi-team tenancy.** RBAC tiers are per kind and per person, but there
  is no team or tenant boundary: `FrameJob.spec.namespace` was removed rather
  than gated on a SubjectAccessReview, and nothing partitions resources
  between teams.
- **Pascal GPUs run llama.cpp only.** The inference provider targets
  llama.cpp; vLLM and KubeAI do not run on `sm_6.1` (Tesla P4), and
  `deploy/caching/vllm-rdma-kvcache.yaml` cannot run on that hardware.
- `get frameusers` still returns password hashes: moving the hash to `status`
  protects writes, not reads.
- The chart has no default operator image (`image.repository` is required)
  and requires `provisiond.media.url` when provisiond is enabled.
- `TalosMachineConfig` and `TalosUpgrade` remain frozen and reconciled but
  drive a mechanism the estate no longer runs since the move to Debian nodes;
  nothing upgrades a Debian node's OS yet.
- The Kind e2e suite covers the eight original controllers; `FrameMachine`,
  `FrameInstall`, `FrameStorage`, `FrameDiskClaim` and the alert relay are
  covered by envtest only.

[1.0.0]: https://github.com/Plume-Labs/frame/releases/tag/v1.0.0
