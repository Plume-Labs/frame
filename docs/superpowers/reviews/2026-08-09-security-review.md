# Frame — Phase D security review

**Date:** 2026-08-09
**Scope:** RBAC least-privilege, NetworkPolicies, workload hardening, secrets handling, supply chain, admission surface.
**Method:** source read of `internal/`, `api/`, `cmd/`, `config/`, `charts/`, `src/`, `.github/`, plus live verification against the test cluster (`kubectl auth can-i --list`, `kubectl get validatingwebhookconfiguration`). Nothing was changed, in the repo or on the cluster.
**Commit reviewed:** `7337037` (working tree: only `deploy/kubernetes/overlays/development/kustomization.yaml` modified, by another agent; `charts/` and `hack/` clean at read time).

> Where a claim in the docs and the code disagreed, the code and the live cluster won. Several findings below contradict `docs/deployment.md` and `docs/architecture.md`; those docs are stale, and that staleness is itself a finding (M10).

---

## Counts

| Rank | Count |
|---|---|
| **Critical** | 2 |
| **Important** | 11 |
| **Minor** | 16 |

**Exploitable today:** C1, C2, I1, I2 (on enable), I3, I5, I6, I7, I10, I11, and most Minors.
**Latent — becomes exploitable when X ships:** I4 and M1 (multi-tenancy / RBAC tiers); I8, I9, M2–M6 (when `authd` is actually wired to something).

Frame is single-tenant on a private cluster today. Findings that only bite under multi-tenancy say so explicitly and are ranked on what they cost *now*, not on what they would cost later.

---

## Critical

### C1 — The control plane is an unauthenticated path to cluster-admin

**Files:**
- `/home/rmocq/Neura/.externals/frame/deploy/docker/nginx.conf:18-27`
- `/home/rmocq/Neura/.externals/frame/deploy/kubernetes/base/deployment.yaml:93-99`
- `/home/rmocq/Neura/.externals/frame/deploy/kubernetes/base/ingress.yaml` (whole file)
- `/home/rmocq/Neura/.externals/frame/deploy/kubernetes/base/rbac.yaml:158-169`

The brief said the UI authenticates with "a single ServiceAccount Bearer token". **It does not.** The token plumbing exists in the client (`src/lib/frame-sdk.ts:611-612`, `654-655` — `window.__FRAME_TOKEN__`), but nothing in `deploy/` ever sets that global: no init container, no `config.js`, no nginx `sub_filter`. `bearerToken()` returns `undefined` and no `Authorization` header is sent.

Instead the credential is attached *server-side* by a `kubectl proxy` sidecar in the same pod:

```yaml
# deploy/kubernetes/base/deployment.yaml:93-99
- name: kube-proxy-api
  image: rancher/kubectl:v1.36.2
  args: [proxy, --port=8001, --address=127.0.0.1, --accept-hosts=^.*$]
```

and nginx blanket-proxies both API prefixes to it (`nginx.conf:18-27`), with no path filter and no verb filter. The Ingress is a plain public `/` route — grep across `deploy/kubernetes/` for `auth-url`, `auth-signin`, `basic-auth`, `oauth` returns zero hits.

**Net effect: anyone who can reach the Ingress or the Service issues arbitrary Kubernetes API calls as `system:serviceaccount:cluster-control:cluster-control-ui`, with no credential of any kind.** The nginx comment at `nginx.conf:12-17` frames "the browser never sees a token" as the security property; it is the opposite — it moves the credential from something you must steal to something you merely have to reach.

**Why it is exploitable, and how far it goes.** Verified live:

```
$ kubectl auth can-i --list --as=system:serviceaccount:cluster-control:cluster-control-ui
deployments.apps    [get list watch patch]
daemonsets.apps     [get list watch patch]
statefulsets.apps   [get list watch patch]
nodes               [get list watch patch]
pods/proxy          [get create delete]
...
```

`patch deployments.apps` cluster-wide is cluster-admin by a short path: patch any Deployment's pod template to add `securityContext.privileged: true` and a `hostPath: /` volume, get root on the node, read every kubelet-reachable Secret and the node's kubeconfig. **No namespace on this cluster carries a `pod-security.kubernetes.io/enforce` label** (verified — the PSA query returns empty), so nothing stops that patch from being admitted.

The shortest specific chain: patch `frame-system/frame-controller-manager` to add a sidecar, and inherit the manager SA's cluster-wide `secrets` CRUD (see I1).

Nothing about this requires XSS. XSS would be *redundant* here — an attacker who can reach the page can skip the page. (The XSS surface itself is clean: one `dangerouslySetInnerHTML` at `src/components/ui/chart.tsx:81` in unreferenced shadcn boilerplate, zero `innerHTML`/`eval`/`new Function`, all cluster data rendered as escaped JSX text.) The *stolen token* question is likewise moot: there is no token to steal, only a network position to occupy. That is strictly worse, because network position leaves no credential to revoke.

Secondary aggravators, same finding: `--accept-hosts=^.*$` disables `kubectl proxy`'s DNS-rebinding guard, and because auth is purely ambient there is no CSRF token anywhere — a page on any origin can drive simple-request POSTs at the endpoint (same-origin policy hides the response, not the effect).

**Smallest fix.** In priority order, each independently worth doing:

1. Put an authenticating proxy in front of the Ingress *today* — `nginx.ingress.kubernetes.io/auth-url` against oauth2-proxy, or basic-auth as a stopgap. One annotation, and it closes the unauthenticated hole without touching the app.
2. Drop the `cluster-control-operator` binding (`rbac.yaml:158-169`) and leave only `cluster-control-viewer` until (1) is in place. The UI degrades to read-only; nothing is destroyed.
3. Remove `patch` on `deployments/daemonsets/statefulsets` from `cluster-control-operator` and keep only the `*/scale` subresource patches, which the UI's scale button actually needs (`frame-sdk.ts:2236-2238`). Rolling restart (`:2215-2222`) needs full `patch`; if it must stay, scope it with `resourceNames`.
4. Add `pod-security.kubernetes.io/enforce: baseline` to every namespace, so a Deployment patch cannot mint a privileged pod.

The real fix is the roadmap's own item — wire `authd`, which is already built and deployed, and use per-user OIDC identity with `Impersonate-User` or per-user tokens. That is Phase B/E work; items 1–4 are hours.

---

### C2 — The UI ServiceAccount holds `pods/proxy` `get`/`create`/`delete` cluster-wide

**File:** `/home/rmocq/Neura/.externals/frame/deploy/kubernetes/base/rbac.yaml:74-85`
**Client use:** `/home/rmocq/Neura/.externals/frame/src/lib/frame-sdk.ts:597` (`integrationProxy`), `:2121`, `:2130`

Granted with no `resourceNames` and no namespace scope. The comment in the file is candid about it:

> *"Same blast radius as the get above (any pod's HTTP endpoint, not just Alertmanager's — pod names are dynamic so a resourceNames restriction would be fragile)"*

`pods/proxy` with `create` is arbitrary authenticated GET/POST/DELETE against **every HTTP port of every pod in the cluster**. That includes any in-cluster admin API that trusts network position — Argo's server, Alertmanager, Rook's dashboards, the local registry at `192.168.2.201:30500`, and llama.cpp instances (whose API key becomes irrelevant if the endpoint is reachable in the clear, though here it is not).

Listed separately from C1 because it is a distinct primitive: it survives fixing the front door if the RBAC is left alone, and any future per-user viewer tier that inherits this role inherits it too.

**Why it is exploitable:** verified live — `pods/proxy [get create delete]` on the UI SA, unrestricted. Combined with C1 it is unauthenticated.

**Smallest fix:** the only consumer is Alertmanager silences (`frame-sdk.ts:2121`, `:2130`) and the configurable "integrations" proxy. Alertmanager is a Service, not a moving pod — replace `pods/proxy` with `services/proxy` scoped by `resourceNames: [alertmanager-operated]` in the monitoring namespace. That is a two-line RBAC change and one path change in `integrationProxy()`. If the generic integration proxy must stay, restrict it to a namespace allow-list in the ClusterRole via a namespaced Role + RoleBinding per integration namespace.

---

## Important

### I1 — Manager ClusterRole: cluster-wide `secrets` create/delete/get/list/patch/update/watch

**File:** `/home/rmocq/Neura/.externals/frame/config/rbac/role.yaml:40-53`
**Marker:** `/home/rmocq/Neura/.externals/frame/internal/controller/services/frameservice_controller.go:59`
**Live:** confirmed — `get/list/create/delete secrets` all `yes` for `system:serviceaccount:frame-system:frame-controller-manager`, in every namespace.

This is the grant the brief flagged as knowingly powerful, and it is. It is generated from one marker:

```go
// frameservice_controller.go:59
// +kubebuilder:rbac:groups="",resources=services;secrets,verbs=get;list;watch;create;update;patch;delete
```

which exists to serve the service catalog's cross-namespace credential projection (`internal/controller/services/binding.go`). That projection genuinely needs write access in arbitrary namespaces — `spec.binding.projectTo` is a free-form namespace list (`binding.go:145-157`), so the target set is not knowable at install time. So the grant is *structurally* justified, and I would not call it a bug.

What makes it Important rather than accepted-by-design is three things:

1. **`list` and `watch` on `secrets` cluster-wide are not needed by anything.** Cross-checking every client call: `binding.go` does `Get` (`claimNewCoordinates:200`), `CreateOrUpdate` (`writeSecret:299`) and `Delete` (`deleteSecrets:327`) at *named coordinates only* — it never lists. `inference.go` does `CreateOrUpdate` (`ensureAPIKey:355`) and `Get` (`readAPIKey:381`) at a named coordinate. `talos_client.go:46` does a single `Get`. **Nothing in the codebase lists or watches Secrets.** The `list`/`watch` verbs are pure over-grant, and they are the two that turn "can write specific Secrets" into "can enumerate and exfiltrate every Secret in the cluster" — including every ServiceAccount token secret and the Talos PKI.

   Caveat worth checking before removing them: controller-runtime's default cached client *does* establish an informer (list+watch) for any type read through `r.Get`. If `Secret` is read through the cached client, removing `list`/`watch` will break the cache at startup. The fix is `client.Options{Cache: {DisableFor: []client.Object{&corev1.Secret{}}}}` in `cmd/main.go` — the standard operator pattern, and it also stops the manager from holding every Secret in the cluster in memory.

2. **The two Talos controllers only need `get`.** `talosupgrade_controller.go:49` and `talosmachineconfig_controller.go:50` both declare `verbs=get;list;watch`, but the only call either makes is `buildTalosClient`'s single `Get` (`talos_client.go:46`). Two more `list;watch` grants with no caller.

3. **`update` is redundant with `patch`+`create` here.** `controllerutil.CreateOrUpdate` uses Update, so this one is real — keep it. Noted only so the audit trail is complete.

**Exploitable today?** Not on its own — it requires compromising the manager pod first. But C1 provides exactly that (`patch deployments.apps` → inject a sidecar into `frame-controller-manager`), so today it is the second half of a live chain rather than a theoretical blast radius.

**Smallest fix:**
```go
// frameservice_controller.go:59 — split the marker
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch;delete
```
plus `verbs=get` on `talosupgrade_controller.go:49` and `talosmachineconfig_controller.go:50`, plus `DisableFor: []client.Object{&corev1.Secret{}}` on the manager's client cache. Regenerate with `make manifests`. This removes cluster-wide Secret *enumeration* while leaving the projection feature fully working.

**Rest of the manager role — cross-checked, all justified.** For the record, since the brief asked for every marker to be traced to a caller:

| Grant (`role.yaml`) | Marker | Justifying call | Verdict |
|---|---|---|---|
| `configmaps` get/list/watch | `talosmachineconfig_controller.go:51` | `corev1.ConfigMap` read at `talosmachineconfig_controller.go:112` | justified (`list`/`watch` unused — same over-grant as above, minor) |
| `namespaces` get/list/watch | `frameresourcequota_controller.go:54`, `frameservice_controller.go:60` | `checkNamespaceExists` (`binding.go:262`) | justified |
| `pods` get/list/watch | `frameservice_controller.go:65` | readiness counting | justified |
| `events` create/patch | `frameservice_controller.go:61` | 20 `Recorder.Event` sites | justified |
| `nodes` get/list/patch/update/watch | `framenode_controller.go:66` | node labelling | justified |
| `persistentvolumeclaims` get | `frameservice_controller.go:73` | model-cache PVC check | justified, correctly narrow |
| `resourcequotas` full CRUD | `frameresourcequota_controller.go:53` | quota reconcile | justified |
| `deployments.apps` full CRUD | `frameservice_controller.go:58` | `inference.go:487` | justified |
| `workflows.argoproj.io` full CRUD | `framejob_controller.go:68` | `buildWorkflow` | justified but see I4 |
| `priorityclasses` full CRUD | `schedulingpolicy_controller.go:51` | cluster-scoped resource, no alternative | justified |
| `queues` (volcano, yunikorn) | `schedulingpolicy_controller.go:52-53` | queue reconcile | justified |
| `frameusers` get/list/watch | `frameuser_webhook.go:30` | `requireAnotherAdmin` List (`frameuser_webhook.go:~60`) | justified — the marker is *not* stale |
| all CRD groups full CRUD + `/status` + `/finalizers` | per-controller | standard | justified |

**No `*` verb, no `escalate`, no `bind`, no `impersonate` in the manager role** — verified live: `create clusterrolebindings`, `impersonate users`, `escalate clusterroles`, `create pods`, `create serviceaccounts/token` all return `no`. That is genuinely good and worth protecting; the operator cannot self-escalate through RBAC, only through the workloads it creates (I4).

---

### I2 — The NetworkPolicies are broken twice over; enabling them would take the cluster's CRD writes offline

**Files:**
- `/home/rmocq/Neura/.externals/frame/config/network-policy/allow-webhook-traffic.yaml:26` (`port: 443`)
- `/home/rmocq/Neura/.externals/frame/config/network-policy/allow-metrics-traffic.yaml:26` (`port: 8443`)
- `/home/rmocq/Neura/.externals/frame/charts/frame/templates/networkpolicy.yaml:~62` (same bug, mirrored into the Helm chart)
- Commented out at `/home/rmocq/Neura/.externals/frame/config/default/kustomization.yaml:32`; `networkPolicy.enabled: false` in `charts/frame/values.yaml:78`

Nothing is enforced today, so this is not exploitable — it is a **trap laid for whoever enables it**, and the Helm chart just copied the trap forward verbatim.

**Bug 1 — wrong port.** A NetworkPolicy `ports` entry names the port **on the destination pod**, not the Service port. Verified live:

```
$ kubectl -n frame-system get svc frame-webhook-service -o jsonpath='{.spec.ports}'
[{"port":443,"protocol":"TCP","targetPort":9443}]

$ kubectl -n frame-system get deploy frame-controller-manager -o json | jq '.spec.template.spec.containers[0].ports'
[{"containerPort":8081,...},{"containerPort":9443,"name":"webhook-server",...}]
```

The policy allows 443. The webhook listens on **9443** (`config/default/manager_webhook_patch.yaml`, container port block). Nothing listens on 443 in that pod. Because a NetworkPolicy selecting a pod denies everything not explicitly allowed, applying this policy **denies all traffic to 9443**.

**Bug 2 — the source can never match.** `namespaceSelector` matches *pods* in labelled namespaces. On this cluster the API server is not a pod:

```
$ kubectl -n kube-system get pods | grep apiserver
(no output — k3s runs the apiserver as a host process)
```

Admission traffic therefore arrives from a node IP, which no `namespaceSelector` can ever select. Even with the port corrected to 9443, this policy blocks the API server. The correct source for admission traffic is an `ipBlock` covering the control-plane node IPs, or no ingress restriction on 9443 at all.

**What breaks when it is enabled.** All eight validating webhooks are `failurePolicy: Fail` with `namespaceSelector: {}` — verified live:

```
frame-validating-webhook-configuration | vframejob-v1alpha1.kb.io           | failurePolicy=Fail | timeout=10 | nsSel={}
... (framenode, frameresourcequota, frameservice, frameuser, schedulingpolicy, talosmachineconfig, talosupgrade)
```

So: apply the webhook NetworkPolicy → API server cannot reach 9443 → every webhook times out after 10s → `Fail` → **every CREATE and UPDATE of all 8 CRD kinds is rejected cluster-wide**, including the FrameUser DELETE path. The operator cannot even update its own CRs. Recovery requires deleting the NetworkPolicy or the ValidatingWebhookConfiguration by hand.

**What the metrics policy breaks.** Its port (8443) is correct. But it requires the scraping namespace to carry `metrics: enabled`, and none does:

```
monitoring  Active  15d  kubernetes.io/metadata.name=monitoring,name=monitoring
```

Enabling it silently stops Prometheus from scraping the manager. Not an outage, but a monitoring blind spot that will be diagnosed as "the operator stopped emitting metrics".

**Smallest fix (all three files):**
- webhook policy: `port: 443` → `port: 9443`, and replace the `namespaceSelector` with an `ipBlock` listing the control-plane node CIDR — or, simpler and still useful, keep `policyTypes: [Ingress]` but allow 9443 from anywhere while restricting 8443. The webhook's actual authentication is mTLS via cert-manager; the NetworkPolicy adds little there and costs a cluster outage if wrong.
- metrics policy: label the `monitoring` namespace `metrics: enabled` **in the same change** that enables the policy, or the scrape breaks.
- Add an e2e case that enables both policies and asserts a CR create still succeeds. This bug is exactly the kind that only a live test catches.

---

### I3 — The llama.cpp Deployment the operator creates has no securityContext at all

**File:** `/home/rmocq/Neura/.externals/frame/internal/services/provider/inference/inference.go:487-551` (Deployment build), `:654-668` (`setContainer`)

Grep for `SecurityContext`, `RunAsNonRoot`, `ReadOnlyRootFilesystem`, `Capabilities`, `AllowPrivilegeEscalation`, `Seccomp`, `AutomountServiceAccountToken` across `inference.go`: **zero hits.** The container is constructed as:

```go
// inference.go:662
c := corev1.Container{Name: name, Image: image, Args: args, VolumeMounts: mounts, Env: env}
```

and the pod spec sets only `Replicas`, `Selector`, `Template.Labels`, `NodeSelector`, one annotation, and one volume.

So every inference pod Frame provisions runs:
- as **root** (no `runAsNonRoot`, no `runAsUser`),
- with a **writable root filesystem**,
- with **all default capabilities** (`CHOWN`, `SETUID`, `SETGID`, `NET_RAW`, …),
- with **no seccomp profile** (unconfined on a cluster with no PSA — confirmed, no namespace has `pod-security.kubernetes.io/enforce`),
- with the namespace's **default ServiceAccount token automounted**.

The contrast is stark: the manager's own Deployment (`config/manager/manager.yaml:53-77`) gets `runAsNonRoot`, `seccompProfile: RuntimeDefault`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]` — and `Dockerfile.controller:23` / `Dockerfile.authd:25` both set `USER 65532`. The hardening discipline is present everywhere the project writes YAML by hand and absent everywhere it writes YAML in Go.

**Why it is exploitable.** llama.cpp's server parses untrusted input (GGUF weights, HTTP request bodies, chat templates) in C++ and has a real CVE history. A memory-safety bug there currently yields root in a container with `NET_RAW`, a writable filesystem, an automounted SA token, and an unconfined seccomp profile — i.e. immediate lateral movement. With the hardening below, the same bug yields an unprivileged process in a read-only container that cannot talk to the API server.

Resource limits *are* set correctly (`setContainerResources:703-715` — CPU/memory in both limits and requests, GPU in limits), so this is specifically a securityContext gap, not a resource gap.

**Smallest fix** — in the `CreateOrUpdate` closure at `inference.go:~495`, and in `setContainer`:

```go
deployment.Spec.Template.Spec.AutomountServiceAccountToken = ptr.To(false)
deployment.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
    RunAsNonRoot:   ptr.To(true),
    RunAsUser:      ptr.To(int64(65532)),
    SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
}
// in setContainer, on both the found and appended branches:
c.SecurityContext = &corev1.SecurityContext{
    AllowPrivilegeEscalation: ptr.To(false),
    ReadOnlyRootFilesystem:   ptr.To(true),
    Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
}
```

Two caveats the author of that file will care about, given the extremely careful `CreateOrUpdate`-idempotency reasoning in its comments (`inference.go:406-425`): (a) `readOnlyRootFilesystem: true` may require an `emptyDir` at `/tmp` for llama.cpp — verify against a real pod, not the fake client; (b) set these fields *unconditionally* in the closure rather than "only if nil", so they match the file's existing convention of owning the fields it sets.

The same treatment is needed if the Argo Workflow path ever inlines a pod spec — today it does not (see I4).

---

### I4 — FrameJob → Argo Workflow is a confused deputy; the webhook's known-list is warn-only

**Files:**
- `/home/rmocq/Neura/.externals/frame/internal/webhook/frame/v1alpha1/framejob_webhook.go:33-37`, `:77-89`
- `/home/rmocq/Neura/.externals/frame/api/frame/v1alpha1/framejob_types.go:53-64`
- `/home/rmocq/Neura/.externals/frame/internal/controller/frame/framejob_controller.go` (`buildWorkflow`)
- `/home/rmocq/Neura/.externals/frame/config/rbac/role.yaml:66-77`

This is the "constraint enforced only for a known-list of values" the brief anticipated, and it is weaker than that phrasing suggests — the known-list is not enforced at all:

```go
// framejob_webhook.go:77-84
func validateFrameJob(job *framev1alpha1.FrameJob) (admission.Warnings, error) {
	if !knownPipelines[job.Spec.Pipeline] {
		...
		return admission.Warnings{fmt.Sprintf("pipeline %q not in known list %v; ...", ...)}, nil
	}
	if job.Spec.GPUCount > 0 && job.Spec.ServiceClass == "LOW" {
```

An unknown pipeline returns `(warnings, nil)` — **admitted**, with a kubectl warning nobody reads in automation. And because it `return`s, the GPU/serviceClass check below it never runs for any pipeline outside the three-entry list. The type comment at `framejob_types.go:28-37` documents this bypass accurately and explains why it was not mirrored as CEL; the reasoning about stranding stored objects is sound, but the result is that the *only* enforcement of that GPU rule is unreachable for `"training"` — the pipeline this project's own sample and e2e suite use.

`buildWorkflow` then passes both user-controlled fields straight through:

```go
"workflowTemplateRef": map[string]any{"name": job.Spec.Pipeline},
...
"metadata": map[string]any{"name": job.Name, "namespace": job.Spec.Namespace, ...},
```

`spec.namespace` is validated only as a DNS label (`framejob_types.go:61-63`) and defaults to `"default"` — it is explicitly *not* required to match the FrameJob's own namespace, and the type comment says so:

> *"the controller creates the backing ArgoWorkflow here with cluster-wide RBAC, so a FrameJob in one namespace can direct workflow creation into another. That cross-namespace reach is deliberate-for-now"*

**The escalation.** The manager holds `create workflows.argoproj.io` cluster-wide (`role.yaml:66-77`). So a principal who can create a FrameJob **in any one namespace** can make the operator create an Argo Workflow **in any namespace**, referencing **any WorkflowTemplate** in that namespace. Argo then executes that template under whatever ServiceAccount the template or the namespace default specifies. That is the operator lending its cluster-wide reach to a caller who has none — textbook confused deputy, and the same shape the service-catalog binding code was already hardened against (`binding.go:36-44`, `:80-100` — that fix is excellent and worth reading as the model for this one).

**Is it exploitable today?** **No — latent.** Frame is single-tenant: the only principals that can create FrameJobs are the UI SA and cluster admins, and the UI SA is already exposed by C1 (which gives a much shorter path to the same place). **This becomes live the moment the RBAC tiers ship and a `framejob-editor` is bound to a real, less-privileged user** — which is exactly the Phase B item the type comment defers to. Flagging it Important so the tier work does not land on top of it.

**Smallest fix,** to apply *with* the tier work, not before:
1. Make the pipeline check an error, not a warning, and drive the list from a ConfigMap or a `WorkflowTemplate` existence check rather than a hardcoded map. If a hard error is too disruptive, at minimum move the GPU/serviceClass check *above* the pipeline check so it runs unconditionally — that is a two-line reorder with no behaviour change for known pipelines.
2. Reject `spec.namespace != job.Namespace` in `ValidateCreate` unless the CR carries an explicit opt-in annotation, or verify the requesting user can create Workflows in the target namespace via a `SubjectAccessReview` (the manager already has `create subjectaccessreviews`, granted by `metrics_auth_role.yaml` and confirmed live).

---

### I5 — No image scanning, SBOM, signing, or provenance anywhere in the build

**Files:** all of `/home/rmocq/Neura/.externals/frame/.github/workflows/`, `/home/rmocq/Neura/.externals/frame/Makefile`

Confirmed by grep across all four workflows and the Makefile: zero hits for `trivy`, `grype`, `snyk`, `syft`, `cyclonedx`, `spdx`, `cosign`, `sigstore`, `attest`, `provenance`, `sbom`, `govulncheck`, `osv-scanner`.

`docs/superpowers/specs/2026-08-09-frame-release-chain-design.md:62-65` deferred this here explicitly:

> **What this does not do** — No signing, no SBOM, no provenance attestation. They belong with Phase D's security review, which already lists SBOM and image scanning, and adding them here would be building the release process for a product Frame is not yet.

The timing is good: the release workflow does not exist yet, so this goes in at authoring time rather than as a retrofit. The spec's three-image matrix (`frame-controller` / `frame-ui` / `frame-authd`, `:32-38`) and its `sha-<commit>`-on-main vs `vX.Y.Z`-on-tag split (`:44-46`) give a natural severity policy.

**Concretely, what to add and where:**

| What | Where | How |
|---|---|---|
| **SBOM + provenance** | the release workflow's build step (and `build.yml:117-124` today) | `docker/build-push-action` already supports it: add `sbom: true` and `provenance: mode=max` to the `with:` block. Both land as OCI referrers beside the image on GHCR. Zero new actions. |
| **Image scan** | new step after each push | `aquasecurity/trivy-action` in `image` mode against the pushed digest. Policy: `exit-code: 1` on `CRITICAL,HIGH` for tag builds; `exit-code: 0` (report only) for `main` builds, so `main` never blocks. |
| **Signing** | after push | `sigstore/cosign-installer` + `cosign sign --yes <digest>`, keyless via OIDC. Requires adding `id-token: write` to that job's `permissions`. |
| **Go vuln scan** | `test.yml`, after `make test` | `govulncheck ./...`. Install it through the Makefile's existing `go-install-tool` macro (`Makefile:309-319`) so it is version-pinned and checksum-verified like every other tool — no new install pattern. |
| **Release artifact attestation** | release job | `actions/attest-build-provenance` over the rendered install manifests. |
| **Makefile targets** | after `Makefile:184` | a `##@ Security` section with `vuln:`, `sbom:`, `scan:` so the same checks run locally. |

One thing that makes a blocking gate adoptable on day one: the spec retires the bare `ghcr.io/plume-labs/frame` name (`:40-42`) rather than reusing it, so there is no legacy image carrying an accumulated CVE backlog that would force the gate to be disabled immediately.

**Base-image reality check before turning on a `HIGH` gate:** `nginx:alpine` and `golang:1.26` will both produce findings. Expect to start with `CRITICAL` only and tighten.

---

### I6 — `build.yml` is a generation behind the other three workflows on every CI hardening axis

**File:** `/home/rmocq/Neura/.externals/frame/.github/workflows/build.yml`

`lint.yml`, `test.yml`, and `test-e2e.yml` are exemplary: `permissions: {}` at the top with per-job re-grants (`lint.yml:7,11-12`), every action pinned to a full commit SHA (`lint.yml:17,22,27`), `persist-credentials: false` (`:19`). `build.yml` has none of it.

1. **No top-level `permissions:` block** (`:1-16`). Only `build` (`:86-88`) and `release` (`:182-183`) declare one. `quality`, `iac`, and `integration` inherit the **repository default** — write-all unless the org has changed it — while executing untrusted PR code: `npm ci` at `:32` (which runs install scripts for 532 packages) and `docker build` at `:135` against the PR's own Dockerfile.

2. **`curl | bash` of a moving `master` ref inside a `contents: write` job:**
   ```yaml
   # build.yml:189, in the `release` job (permissions: contents: write)
   curl -sL "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh" | bash
   ```
   Whoever controls that file at run time gets code execution in a job holding a repo-write token. This is the single highest-severity supply-chain item in the file.

3. **Every third-party action on a floating tag** — `actions/checkout@v4`, `docker/build-push-action@v6`, `softprops/action-gh-release@v2`, and six more (`:23,26,51,54,91,94,98,106,117,132,138,185,197`). Nine distinct actions, zero SHAs. Renovate cannot even bump these usefully, since there is no SHA to move.

4. `test-e2e.yml:28` downloads `kind` from a `latest` URL with no version pin and no checksum, then `sudo mv`. `build.yml:57-60` pins kubeconform to `v0.6.7` but verifies no checksum.

The mitigation that keeps this Important rather than Critical: the trigger is plain `pull_request`, not `pull_request_target`, so fork PRs get a read-only token and no secrets. The exposure is same-repo branch PRs and anything that can influence the dependency graph.

**Smallest fix:** copy `lint.yml`'s header into `build.yml` — `permissions: {}` at top, `contents: read` per job, `persist-credentials: false` on every checkout. Replace `:189` with `go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)`, which the Makefile already does correctly at `Makefile:258`. Pin the nine actions to SHAs (one `gh api` loop, or let Renovate do it after the first manual pin).

---

### I7 — `overrides` forces `lodash@4.18.0`, a release npm itself marks as bad

**Files:** `/home/rmocq/Neura/.externals/frame/package.json:98-105`, `/home/rmocq/Neura/.externals/frame/package-lock.json:6515-6521`

```json
"overrides": { "picomatch": "4.0.4", "path-to-regexp": "8.4.0", "lodash": "4.18.0", ... }
```

resolves to a lockfile entry carrying npm's own deprecation string:

```json
"deprecated": "Bad release. Please use lodash@4.17.21 instead."
```

`4.18.0` reads as newer than `4.17.21` but is a release the maintainers disowned. Because it is in `overrides` *and* the lockfile, `npm ci` installs it faithfully in CI (`build.yml:32`) and in the image (`Dockerfile:7`), forever, across the whole tree.

Also: `path-to-regexp` and `picomatch` are pinned with no comment saying which advisory they pin past, so nobody will know when the pin is safe to drop.

**Smallest fix:** `"lodash": "4.17.21"` and `npm install` to regenerate the lock. Add a one-line comment above each remaining override naming the advisory it addresses.

Adjacent, not separately ranked: all 47 runtime npm deps use caret ranges (no bare `>=` without a ceiling — the pattern the brief anticipated is genuinely absent), `package-lock.json` is committed, and `npm ci` is used in both CI and the Dockerfile. That part is right.

---

### I8 — `authd`: three unauthenticated requests OOM-kill the pod

**Files:** `/home/rmocq/Neura/.externals/frame/internal/authd/password.go:18`, `/home/rmocq/Neura/.externals/frame/internal/authd/server_session.go:46,56-78`, `/home/rmocq/Neura/.externals/frame/cmd/authd/main.go:126-130`, `/home/rmocq/Neura/.externals/frame/internal/authd/server_bootstrap.go:59`, `/home/rmocq/Neura/.externals/frame/deploy/kubernetes/authd/deployment.yaml:77`

`/auth/login/password` performs **exactly one argon2id verification on every path**, including unknown emails — correct, deliberate timing-oracle defence (`server_session.go:56-78`, with a dummy-hash fallback via `hashIsUsable` at `password.go:65-68`). But each verification allocates `argonMemory = 64 * 1024` KiB = **64 MiB** (`password.go:18`), the container limit is **128Mi**, and there is **no rate limiter and no concurrency bound anywhere**.

Three concurrent POSTs with arbitrary JSON bodies — no valid account, no credentials — exceed the limit and the pod is OOMKilled. Both replicas, indefinitely, from any pod in the cluster.

Three compounding gaps in the same surface:
- **No HTTP server timeouts** (`main.go:126-130`): no `ReadHeaderTimeout`/`ReadTimeout`/`WriteTimeout`/`IdleTimeout`, all defaulting to unlimited. Slowloris; this is gosec G112.
- **Unbounded request bodies** on the two JSON endpoints: `server_bootstrap.go:59` and `server_session.go:46` both call `json.NewDecoder(r.Body).Decode(&body)` with no `http.MaxBytesReader`. The WebAuthn handlers get this right (`io.LimitReader(r.Body, 1<<20)` at `server_webauthn.go:44,96`); these two were missed.
- **Uncached client** (`main.go:90` uses `client.New`, not the manager's cached client), so `Store.list` (`store.go:27-33`) issues a live `LIST frameusers` to the API server on *every* request including unauthenticated failures — an API-server load amplifier bolted to an unauthenticated endpoint.

**Exploitable today?** In-cluster only — `authd` has a ClusterIP Service and **no Ingress** (`deploy/kubernetes/authd/service.yaml`), and nothing consumes it, so knocking it over currently breaks nothing. **Latent-Important:** it becomes a live availability finding the moment `authd` is on the login path, and by then it will be the thing standing between users and the cluster.

**Smallest fix:** a buffered-channel semaphore sized to `memoryLimit / argonMemory - 1` around the KDF call, `http.MaxBytesReader(w, r.Body, 1<<20)` on both JSON handlers, `ReadHeaderTimeout: 5*time.Second` plus the other three timeouts on the `http.Server`, and a per-IP token-bucket limiter on `/auth/login/password`. Raising the memory limit alone only raises the request count.

---

### I9 — `authd`: WebAuthn user verification is never requested, so passkeys are single-factor

**File:** `/home/rmocq/Neura/.externals/frame/internal/authd/webauthn.go:47-57`, `:87-90`, `:123`

`NewAuthenticator` never sets `Config.AuthenticatorSelection.UserVerification`, and neither ceremony passes a UV option:

```go
// webauthn.go:87-90
options, session, err := a.web.BeginRegistration(webauthnUser{u},
    webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
// webauthn.go:123
options, session, err := a.web.BeginDiscoverableLogin()
```

Against the pinned `go-webauthn v0.17.4`, `Config.validate()` defaults timeouts but not UV, so `session.UserVerification` stays `""`, `shouldVerifyUser` computes to `false`, and the UV bit in authenticator data is never checked. The browser is never asked for a PIN or a biometric.

**Consequence:** whoever physically holds an enrolled security key signs in as cluster admin by touching it — a stolen laptop with a plugged-in YubiKey, or a key in a drawer. Registration also uses default `PreferNoAttestation`, so a software authenticator is equally acceptable.

**Exploitable today?** No — `authd` is consumed by nothing (`deploy/kubernetes/authd/kustomization.yaml:1-7` excludes it from `base`, and no frontend code references it). **Latent-Important**, and the single most impactful gap in `authd` once it is wired.

**Smallest fix — two lines:**
```go
// login (webauthn.go:123)
a.web.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
// registration (webauthn.go:87)
webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
    ResidentKey:      protocol.ResidentKeyRequirementRequired,
    UserVerification: protocol.VerificationRequired,
})
```

---

### I10 — `SECURITY.md` routes Frame vulnerability reports to GitHub Inc.

**File:** `/home/rmocq/Neura/.externals/frame/SECURITY.md:5,11,15`

Unmodified GitHub boilerplate: *"GitHub takes the security of our software products…"* (`:5`), *"in any GitHub-owned repository"* (`:11`), *"please send an email to opensource-security[@]github.com"* (`:15`), plus a link to GitHub's bounty scope and Safe Harbor.

A researcher following this file reports a Frame vulnerability to a security team with no relationship to the project, who will discard it. There is no maintainer contact, no Private Vulnerability Reporting mention, no response-time commitment, no supported-versions table.

Ranked Important rather than Minor because the repo is public and this is the *only* documented reporting channel — it converts a good-faith disclosure into a silent drop.

**Smallest fix:** keep the "what to include" list at `:19-26`, replace every contact point with the maintainer's address or GitHub Private Vulnerability Reporting, delete the bounty and Safe Harbor links, add a supported-versions line.

---

### I11 — The UI container runs as root on an unversioned base

**File:** `/home/rmocq/Neura/.externals/frame/Dockerfile:2,12-20`

`node:20-alpine` builder → `nginx:alpine` final. **No `USER` directive**, so nginx's master runs as root; a full Alpine distro rather than distroless; and `nginx:alpine` carries no version at all, so it moves with every nginx minor.

The Go images get this right — `Dockerfile.controller:20,23` and `Dockerfile.authd:22,25` are both `gcr.io/distroless/static:nonroot` with `USER 65532:65532`. The UI image is the outlier.

Partially mitigated in the manifest: `deploy/kubernetes/base/deployment.yaml` sets `runAsNonRoot`, `readOnlyRootFilesystem`, `drop: ALL` on both containers, so the deployed pod is not actually root. But the image is only safe because the manifest rescues it — anyone running it with `docker run`, or on a cluster with a laxer manifest, gets root.

**Smallest fix:** switch to `nginxinc/nginx-unprivileged:1.27-alpine` (listens on 8080 already, matching `EXPOSE 8080`) and add `USER 101`. Pin the tag to a minor version at minimum, a digest ideally.

---

## Minor

**M1 — `FrameUser.spec.passwordHash` is credential material in a widely-readable spec field.**
`/home/rmocq/Neura/.externals/frame/api/frame/v1alpha1/frameuser_types.go:65-68`. Argon2id at these parameters makes offline cracking expensive, not impossible, and any principal with `get frameusers` retrieves it with `kubectl get -o yaml` — including the manager (`role.yaml:119-126`). The codebase's own stated rule argues against this: `inference.go:330-336` says *"the token must never reach svc.Status (conditions and status are logged and displayed; a credential must not be)"*. Move it to a Secret, or at minimum to `status`, which has tighter write RBAC. *Latent — matters once more than one person can read FrameUsers.*

**M2 — `authd` challenge cookie is not cleared on success; assertions replay for 5 minutes.**
`/home/rmocq/Neura/.externals/frame/internal/authd/server_webauthn.go:38-65`, `:85-106`. `challengeTTL = 5m` (`webauthn.go:21`). The counter check does not save it: platform and synced passkeys report `signCount == 0` on every assertion, and `UpdateCounter` only warns when `authDataCount <= SignCount && (authDataCount != 0 || SignCount != 0)` — so 0-vs-0 never fires. Statelessness genuinely prevents server-side single-use enforcement (`challenge.go:13-19`, an acknowledged tradeoff), but clearing the cookie with a `MaxAge: -1` `Set-Cookie` on success closes the ordinary case for free. *Latent.*

**M3 — `authd` credential IDs are not globally unique; a viewer can lock an admin out of passkey login.**
`/home/rmocq/Neura/.externals/frame/internal/authd/store.go:93-101` dedupes only within one FrameUser; `:64-77` resolves an assertion by first match across all users. Credential IDs are client-supplied (default `PreferNoAttestation`) and readable from `status.credentials[].id`. An attacker registering the admin's credential ID on their own account, with a name that sorts first, breaks admin passkey login. DoS only — the reverse direction fails safely. Fix: reject a credential ID already enrolled on *any* FrameUser. *Latent.*

**M4 — `authd` sessions are unrevocable bearer tokens for 12h.**
`/home/rmocq/Neura/.externals/frame/internal/authd/server_session.go:99-155`, `internal/authd/server.go:52-54`. The cookie is `HMAC(purpose, exp || email)` with no session ID and no server-side record; logout only sends an expiring `Set-Cookie`. The only kill switches are deleting the FrameUser or rotating `challenge-hmac` (which nukes every session cluster-wide). `SessionTTL` defaults to 12h and is not wired to any env var, so 12h is the only value production can ever have. Cookie flags themselves are correct: `HttpOnly`, `Secure`, `SameSite=Strict`. *Latent.*

**M5 — `authd` signing key has no rotation path.**
`/home/rmocq/Neura/.externals/frame/internal/authd/issuer.go:17` (`keyID = "frame-authd"`, a compile-time constant), `:88-90` (JWKS publishes exactly one key). Swapping `signing.pem` under the same `kid` means the API server's cached JWKS holds the old public key while authd signs with the new private key — every token in that window is rejected. Neither key is reloaded on file change, so a Secret rotation is invisible until pod restart. Fix: derive `kid` from the key's thumbprint and publish both during an overlap window. *Latent.*

**M6 — `authd` bootstrap `AdminCount` is TOCTOU across replicas.**
`/home/rmocq/Neura/.externals/frame/internal/authd/server_bootstrap.go:40-88`, with `replicas: 2`. One token, fired concurrently with distinct emails, yields two admins. Also, `cfg.BootstrapSecret` stays in memory for the pod's life (`main.go:95`), so if every admin is later deleted the in-memory token goes live again on that pod. Requires the token, so low. Separately, `deploy/kubernetes/authd/rbac.yaml:14-15` claims *"the admission webhook is what stops it being abused to mint a second admin"* — it does not; `FrameUserCustomValidator.ValidateCreate` is a no-op (`frameuser_webhook.go:51-53`). Fix the comment, or move the guard into `ValidateCreate` where it would be atomic. *Latent.*

> **Worth recording:** the rest of the bootstrap path is materially better than most implementations of this pattern. `AdminCount() > 0 → 404` *before* the token is examined; the empty-token guard at `:50-53` is present and correctly ordered (without it `subtle.ConstantTimeCompare("", "")` returns 1 and any caller mints an unauthenticated admin — that was fixed in `783d7fd`); constant-time compare; the created admin gets `PasswordDisabled` and no credential, so no default password exists; the Secret is deleted under `context.WithoutCancel`. **No backdoor.**

**M7 — No base image is digest-pinned; Makefile images default to `:latest`.**
`Dockerfile:2,12`, `Dockerfile.controller:5,20`, `Dockerfile.authd:7,22`; `Makefile:2,4,6` (`controller:latest`, `frame-ui:latest`, `cluster-control-auth:latest`), baked into rendered manifests by `Makefile:183,204`. The llama.cpp image the operator deploys is likewise floating: `ghcr.io/ggml-org/llama.cpp:server-cuda` (`inference.go:~551`) — worth pinning, since it is the one image the operator pulls on a user's behalf. The release-chain spec already fixes the Makefile half with its `sha-<commit>` scheme.

**M8 — CI runs `go mod tidy` with no drift check, and `govulncheck` runs nowhere.**
`.github/workflows/test.yml:28`, `test-e2e.yml:37`. CI silently accepts a mutated `go.mod`/`go.sum`; a PR can drift the dependency set and CI re-resolves rather than failing. The repo already uses the right idiom elsewhere — `Makefile:227` does `git diff --exit-code` for CRDs. Add `&& git diff --exit-code go.mod go.sum`.

**M9 — Two dependency bots with overlapping scope and gaps between them.**
`.github/dependabot.yml:1-11` covers npm and `devcontainers` — and `.devcontainer/` is gitignored (`.gitignore:32`), so that entry watches a directory that is not in the repo. `renovate.json` is bare `config:recommended`: no `vulnerabilityAlerts`, no `osvVulnerabilityAlerts`, no security-only fast path, no automerge. Go modules, GitHub Actions, and Dockerfile base images are covered by Renovate only.

**M10 — `docs/deployment.md` still tells operators to mint a year-long token into a JS global.**
`/home/rmocq/Neura/.externals/frame/docs/deployment.md:98,103,105` — *"Static token in ConfigMap"*, `kubectl create token frame-ui-sa --duration=8760h`. Echoed at `docs/api.md:23`, `docs/architecture.md:76`, `README.md:18`. Anyone following it puts a year-long cluster credential in a page global readable by every script on the page. The shipped deployment does not do this (see C1), so the docs describe a *worse* system than the one that exists. Delete the Option B section.

**M11 — No Content-Security-Policy on the UI, and no NetworkPolicy on `cluster-control`.**
`deploy/docker/nginx.conf:39-41` sets `X-Frame-Options`, `X-Content-Type-Options`, `X-XSS-Protection` but no CSP — the one header that would contain a future XSS on a page holding cluster authority. And `deploy/kubernetes/base/kustomization.yaml:6-14` lists no NetworkPolicy, so nothing restricts in-cluster reach to the UI Service (which, per C1, is an open API proxy). A default-deny ingress policy on that namespace, allowing only the ingress controller, is the single cheapest partial mitigation for C1.

**M12 — `integrationProxy()` interpolates ConfigMap-supplied values into API URLs without encoding.**
`/home/rmocq/Neura/.externals/frame/src/lib/frame-sdk.ts:597` splices `i.namespace`, `pod`, and `i.port` straight into a path, while sibling call sites (`:592`, `:968`, `:1992`) correctly use `encodeURIComponent`. Anyone who can PATCH `cluster-control-config` — which the UI itself can, from its own Settings screen (`frame-config.ts:184`) — can inject `../` to redirect those authenticated calls. Same class: `frameNs()` (`:580-584`) splices the unvalidated `window.__FRAME_NAMESPACE__` global into API paths.

**M13 — The tier ClusterRoles ship with `*` verbs and are bound to nobody.**
`config/rbac/framejob_admin_role.yaml:20-21` (`verbs: ['*']`), and the same in the other six `*_admin_role.yaml` files. Verified live: no ClusterRoleBinding references any of them. These are kubebuilder scaffolding, correctly labelled *"not used by the project frame itself"*, and `*` on a single CRD resource is a defensible admin role. Flagged only because Phase B's tier lock-down will start from these files, and `*` is the wrong default to inherit — enumerate the verbs before binding them to anyone.

**M14 — No PodSecurity admission enforcement on any namespace.**
Verified live: no namespace carries a `pod-security.kubernetes.io/enforce` label. This is what makes C1's Deployment-patch path reach node root, and what makes I3's unhardened inference pods unconstrained. `enforce: baseline` cluster-wide (or `restricted` on `frame-system` and `cluster-control`) is a one-label-per-namespace change and blunts both.

**M15 — Small RBAC/UI mismatches in `cluster-control-operator`.**
`deploy/kubernetes/base/rbac.yaml:42` grants `exposedsecretreports` (Trivy's *exposed secret* reports), which nothing in `src/` reads — an unused grant on a resource whose entire purpose is surfacing leaked credentials. Conversely `:141-143` grants only read on `talosmachineconfigs`/`talosupgrades`, while the UI offers Talos write actions (`frame-sdk.ts:2490,2513,2530`) that will 403 at runtime. Drop the former; either remove or enable the latter.

**M16 — `buildTalosInsecureClient` uses `InsecureSkipVerify: true`.**
`/home/rmocq/Neura/.externals/frame/internal/controller/frame/talos_client.go:74-79`. **Correctly justified and not a finding in itself**: Talos maintenance mode uses a per-boot ephemeral self-signed cert whose CA cannot be known in advance, exactly as `talosctl --insecure` does, and the comment at `:66-73` says so. Recorded here only so the audit is complete and so the trust assumption is explicit: it trusts network position on the provisioning LAN. The provisioned path (`buildTalosClient`, `:40-64`) correctly uses mutual TLS against a CA from the referenced Secret.

---

## What is already right

Recorded deliberately, because several of these are the reason worse findings are absent — and because the next change to these files should not undo them.

- **The manager cannot self-escalate through RBAC.** Verified live: no `*` verb, no `escalate`, no `bind`, no `impersonate`, no `create pods`, no `create serviceaccounts/token`, no `create clusterrolebindings`. Every one returns `no`.
- **Secret-logging sweep is clean.** 108 non-test logging sites, all 20 `Recorder.Event` sites, and every `.Status.` assignment across `internal/`, `cmd/`, `api/` were checked. **No secret reaches a log line, a Kubernetes Event, or a CR status field.** That is rare. `cmd/authd/main.go:58` (`fmt.Fprintln(os.Stderr, "authd:", err)`) was traced through all three key loaders — each wraps with a path, never the bytes.
- **The inference provider's credential handling is exemplary** (`inference.go:303-336`, `:513`, `:533-537`): `crypto/rand`, stored only in its own Secret, injected by `secretKeyRef` rather than a literal env value or an `--api-key` argument, and the rollout-trigger annotation carries a SHA-256 digest rather than the key — with a written explanation of why status must never hold it.
- **The service catalog's ownership model was already hardened against exactly the confused-deputy shape I4 still has** (`binding.go:36-44`, `:80-119`). Ownership is decided by `status.binding.projected`, written only through the status subresource, never by a forgeable label. The record-before-write ordering and its residual window are both documented honestly. This is the model I4 should be fixed against.
- **`authd`'s password path is correct**: Argon2id at `t=3, m=64MiB, p=2` (above RFC 9106's second recommendation), `crypto/rand` salt, PHC encoding, `subtle.ConstantTimeCompare`, and a genuinely correct timing-equalisation design where exactly one KDF invocation runs on every branch including unknown accounts.
- **`authd`'s challenge codec is stateless and correctly domain-separated** (`challenge.go:20-93`) — HMAC-sealed into a cookie with a NUL-separated purpose prefix, so it works across replicas and cannot be confused between ceremonies. Counter-regression is handled by reading the typed `CloneWarning` and refusing the login *without* deleting the credential — the right call, since revoking there would be an unauthenticated DoS.
- **`lint.yml` / `test.yml` / `test-e2e.yml`** are the standard `build.yml` should be held to: `permissions: {}`, per-job least privilege, full SHA pinning, `persist-credentials: false`.
- **The Makefile installs every tool through checksum-verified `go install`** (`Makefile:309-319`) rather than curl-pipe-sh. The two `curl | bash` instances live only in workflow files.
- **`go.sum` and `package-lock.json` committed, `npm ci` in both CI and the Dockerfile, no `replace` directives, no `GOPRIVATE`/`GONOSUMDB`** — the checksum database is active.
- **Both Go images are distroless-nonroot** with `USER 65532`.
- **No credential is committed anywhere.** Zero `eyJ…` JWTs repo-wide, zero PEM private keys, zero `VITE_*` build-time inlines; `dist/` is gitignored and untracked.

---

## Suggested order of work

1. **C1** — an ingress auth annotation and dropping the `cluster-control-operator` binding. Hours, and it closes the only unauthenticated path to cluster-admin.
2. **C2, I1** — RBAC narrowing (`pods/proxy` → `services/proxy` by name; split the `services;secrets` marker; `DisableFor` Secrets on the manager cache). Half a day, mechanical, testable.
3. **I3, M14** — securityContext on operator-created pods, PSA labels on namespaces. Half a day, and together they bound the damage from everything else.
4. **I2** — fix the port and the source selector *before* anyone enables the policies. Add the e2e case.
5. **I5, I6, I7, M8** — supply chain, folded into the release-chain workflow as it is authored rather than retrofitted.
6. **I10, M10** — documentation that is actively wrong: `SECURITY.md`, and the `deployment.md` token instructions.
7. **I4, M1** — schedule with the Phase B RBAC-tier work; they are that work's prerequisites, not separate items.
8. **I8, I9, M2–M6** — schedule with wiring `authd`. I9 (two lines) and I8's `MaxBytesReader`/timeouts are cheap enough to do now regardless.
