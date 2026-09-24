# Changelog

All notable changes to Frame are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the chart and
release tags follow [Semantic Versioning](https://semver.org/).

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
