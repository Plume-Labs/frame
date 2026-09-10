# Deployment

---

## Prerequisites

- Docker (to build the UI image)
- `kubectl` 1.28+ with cluster access
- `kustomize` 5.0+ (or use `kubectl kustomize`)
- cert-manager installed on the target cluster (required for webhook TLS)

---

## 1. Build and push the images

Four images, four Dockerfiles, four target pairs. **`make docker-build` builds
the controller, not the UI** — it uses `Dockerfile.controller`. The UI is the
unsuffixed `Dockerfile` (React + Vite, served by nginx) and has its own target.

```bash
make docker-build       docker-push        IMG=ghcr.io/<org>/frame-controller:v0.1.0
make docker-build-ui    docker-push-ui     IMG_UI=ghcr.io/<org>/frame-ui:v0.1.0
make docker-build-authd docker-push-authd  IMG_AUTHD=ghcr.io/<org>/frame-authd:v0.1.0
make docker-build-agent docker-push-agent  IMG_AGENT=ghcr.io/<org>/frame-agent:v0.1.0
```

Or directly:

```bash
docker build -t ghcr.io/<org>/frame-ui:dev .                          # UI
docker build -f Dockerfile.controller -t ghcr.io/<org>/frame-controller:dev .
docker build -f Dockerfile.agent      -t ghcr.io/<org>/frame-agent:dev .
```

For the agent, `deploy/scripts/node-tuning-install.sh` builds and pushes it for
you as part of the install.

---

## 2. Install the operator (CRDs + controller + webhooks)

```bash
# Full install: CRDs, RBAC, controller deployment, webhooks, cert-manager integration
kubectl apply -k config/default
```

This applies the `frame-system` namespace, all ten CRDs (across `frame.plume-labs.io` and `services.plume-labs.io`), RBAC (ClusterRoles + bindings), the controller manager deployment, webhook configuration, and cert-manager certificate resources.

It does **not** install the node-tuning agent — that is a DaemonSet with node prerequisites, and it has its own step below.

Verify:

```bash
kubectl -n frame-system get pods
# NAME                                         READY   STATUS    RESTARTS   AGE
# frame-controller-manager-<hash>              2/2     Running   0          30s

kubectl get crds | grep frame
# framejobs.frame.plume-labs.io
# framenodes.frame.plume-labs.io
# ...
```

---

## 3. Deploy the UI

### Development overlay

Single replica, debug logging, `ENV=development`:

```bash
kustomize build deploy/kubernetes/overlays/development | kubectl apply -f -
```

Update the image tag in `deploy/kubernetes/overlays/development/kustomization.yaml`:

```yaml
images:
  - name: cluster-control
    newName: ghcr.io/<your-org>/frame-ui
    newTag: dev
```

### Production overlay

```bash
kustomize build deploy/kubernetes/overlays/production | kubectl apply -f -
```

Update `deploy/kubernetes/overlays/production/kustomization.yaml` with your registry and tag before applying.

---

## 4. Configure the UI for in-cluster auth

In production, the UI needs a ServiceAccount token to call the K8s API. Two options:

### Option A — Projected ServiceAccount token (recommended)

Mount a projected token into the nginx pod and serve it as a small config endpoint, or inject it at build time via a ConfigMap.

### Option B — Static token in ConfigMap

Create a long-lived token for a `frame-editor` or `frame-viewer` ServiceAccount and inject it via an init container or nginx config that sets `window.__FRAME_TOKEN__`.

```bash
# Create a long-lived SA token (K8s 1.24+)
kubectl create token frame-ui-sa -n cluster-control --duration=8760h
```

The UI reads `window.__FRAME_TOKEN__` on startup. Set it before the `<script>` that loads the app bundle.

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

---

## 5. Ingress

The base kustomization includes an `Ingress` resource in `deploy/kubernetes/base/ingress.yaml`. Edit the host to match your cluster's ingress controller:

```yaml
spec:
  rules:
    - host: frame.your-cluster.example.com
```

Or use a port-forward for quick access:

```bash
kubectl -n cluster-control port-forward svc/cluster-control-ui 8080:8080
# → http://localhost:8080
```

---

## 6. Cert-manager (required for webhooks)

The operator's validating/defaulting webhooks require TLS managed by cert-manager. If cert-manager is not already installed:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=Available deployment --all --timeout=120s
```

Then install the operator:

```bash
kubectl apply -k config/default
```

---

## Node-tuning agent

`NodeTuning` needs a DaemonSet on every node. It is deliberately not part of
`kubectl apply -k config/default`, because two of its prerequisites cannot be
expressed as a manifest.

> **The Helm chart does not ship it either.** `charts/frame/` installs the
> `NodeTuning` CRD and the controller's RBAC, but the agent DaemonSet lives
> only under `deploy/kubernetes/base/node-tuning-agent/`. After a pure
> `helm install`, a `NodeTuning` you apply will sit with every node `Drifted`
> and no agent to observe or apply anything. Run the script below.

```bash
# Dry run first — it changes nothing and prints every action it would take.
deploy/scripts/node-tuning-install.sh
deploy/scripts/node-tuning-install.sh --apply
```

The script refuses to start if any node is not `Ready`, then does five things:

1. **Removes superseded DaemonSets.** `ksm-tuner` wrote the same KSM knobs from
   a shell script and could only report what it had written; the agent reports
   what the node measures. Deleting the manifest does not delete a running
   DaemonSet — this does. It also recreates `nvidia-mps` if its selector
   predates the injected common labels, since a DaemonSet selector is
   immutable.
2. **Installs `tuned` on nodes that lack it.** Required only if a NodeTuning
   sets `spec.tunedProfile`. It cannot ship in the agent image: every host
   command the agent runs is `nsenter`ed into PID 1's namespaces, so
   `tuned-adm` resolves to the node's own binary. Ubuntu Server does not ship
   it.
3. **Builds and pushes the agent image.**
4. **Applies the CRD and the agent** (`deploy/kubernetes/base/node-tuning-agent/`).
5. **Stops.** It creates no `NodeTuning` and enables nothing.

### Enabling KSM is a security decision, not a performance one

Merged pages make a write take a measurable copy-on-write fault, which lets one
container test whether another holds a given page. On a cluster running
notebooks or code sandboxes those are real neighbours. `ksm.enabled` therefore
defaults to **off**, and turning it on is a deliberate act. See
[SECURITY.md](../SECURITY.md).

### Tuning a set of nodes

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: NodeTuning
metadata: { name: workers }
spec:
  nodeSelector:
    matchLabels: { "kubernetes.io/os": linux }
  ksm: { enabled: true, pagesToScan: 4000, sleepMillisecs: 200 }
```

The controller parks each node at `RebootPending` and waits for an explicit,
per-node, per-generation approval:

```bash
kubectl annotate node <NODE> --overwrite \
  frame.plume-labs.io/tuning-approved=$(kubectl get nodetuning workers -o jsonpath='{.metadata.generation}')
```

Then watch it converge, reading `realization` rather than `phase` alone:

```bash
kubectl get nodetuning workers \
  -o jsonpath='{range .status.nodes[*]}{.name}{"\t"}{.phase}{"\t"}{.realization}{"\n"}{end}'
```

`Effective` means new containers get the setting; `FullyRealized` means every
container on the node does. For KSM, the gap between the two is most of the
benefit.

---

## RBAC

### The viewer / editor / admin tiers

Twenty-seven `ClusterRole`s, three per kind, across both API groups
(`frame.plume-labs.io` and `services.plume-labs.io`) — nine kinds of ten.
`FrameTask` postdates the freeze the same as `NodeTuning`, but got the usual
three (`config/rbac/frametask_*.yaml`, wired into `config/rbac/kustomization.yaml`)
so a `FrameTask` — the record of who did what — is readable and manageable
through the same viewer/editor/admin split as everything else, rather than
only by whoever holds the ServiceAccount that writes it. **`NodeTuning`
remains the sole exception, with no tier roles at all.** It postdates the
freeze, and access to it is currently whatever a cluster-admin holds. Anyone
able to write a `NodeTuning` and annotate a node can cause a rolling restart
of the cluster's kubelets; scope that accordingly until the tiers exist.
Each of the twenty-seven carries a `rbac.frame.plume-labs.io/tier: viewer|editor|admin`
label, and three aggregated `ClusterRole`s — `frame-viewer`, `frame-editor`,
`frame-admin` — pick them up, each selecting its own tier and every tier
below it (`deploy/kubernetes/base/rbac-tier-bindings.yaml`). Those three are
bound to three groups, `frame:viewers` / `frame:operators` / `frame:admins`
— never to a person directly.

**This is now enforced per person, which is the thing this lot exists to
close.** The `cluster-control-ui` ServiceAccount no longer authenticates the
UI's requests itself: the `frame-uiproxy` sidecar (`deploy/kubernetes/base/deployment.yaml`)
validates the bearer token authd issued, strips whatever `Impersonate-*`
headers the browser sent, and re-issues the request impersonating the
FrameUser's own email plus one group derived from `FrameUser.spec.role` —
`admin` → `frame:admins`, `operator` → `frame:operators`, `viewer` →
`frame:viewers` (note the mismatch: FrameUser roles are admin/operator/
viewer, RBAC tiers are admin/editor/viewer, and `operator` maps to the
*editor* tier — there is no "operator" tier and no "editor" role). The
apiserver evaluates that impersonated identity's own RBAC, not the
ServiceAccount's. Installing or upgrading the chart still grants nobody
anything by itself — a `FrameUser` has to exist and carry a role before its
holder can do anything through the console — but from here the binding that
matters is the one between that role and the impersonated group, not a token
shared by every browser.

**No RBAC binding may ever name an individual user.** The ServiceAccount
behind `frame-uiproxy` holds `impersonate` on `users` with **no
`resourceNames` restriction** — email addresses are not an enumerable set,
so there is no fixed list to bind it to — and only its `impersonate` on
`groups` is bounded, to exactly `frame:admins`, `frame:operators`,
`frame:viewers` (`deploy/kubernetes/base/rbac.yaml`,
`cluster-control-impersonator`). That split means the group restriction is
the *only* thing standing between a caller and whatever a bound
`ClusterRole` grants: a binding that names a `User` subject directly, the
way `kubectl create clusterrolebinding alice-frame-editor
--clusterrole=frame-framejob-editor-role --user=alice@example.com` used to
be shown here, would be reachable by anyone the proxy can be made to
impersonate as that literal string, with no `resourceNames` guard anywhere
in the path to stop it. Bind tiers to the three groups, never to a user:

```bash
kubectl create clusterrolebinding frame-operators-extra \
  --clusterrole=frame-framejob-editor-role --group=frame:operators
```

(`rbac.tierRoles.install=false` on the chart if you manage them yourself.)

Their verbs are enumerated explicitly rather than `'*'`. A wildcard also
covers verbs and subresources that do not exist yet, so an admin tier granted
`'*'` silently acquires whatever a future API version adds — which is not a
frozen tier. Admin is `create, delete, deletecollection, get, list, patch,
update, watch`; editor is the same minus `deletecollection`; viewer is `get,
list, watch`.

**FrameUser and Talos writes are not in the aggregation at all.**
`frameuser-viewer-role`, `frameuser-editor-role`,
`talosmachineconfig-editor-role` and `talosupgrade-editor-role` carry no
`rbac.frame.plume-labs.io/tier` label, so no `frame:` group holds them by
installing the chart. Account management is `frameuser-admin-role`, admin tier
only, and Talos writes are admin only — `cluster-control-operator`'s own
header excludes them by name. The reason is one sentence and it is the whole
of it: `get frameusers` is every password hash (see below), and `patch
frameusers` is a promotion to admin one token lifetime later.

That last one is closed twice over, deliberately. The label is RBAC — who may
send the request. The FrameUser validating webhook is admission — what the
request may say: `requireAdminRequester` refuses, from anyone who is not
already in `frame:admins` (or `system:masters`, the break-glass path the
rollout depends on): any change to `spec.role`, a **create** carrying
`spec.role: admin` (once any admin exists — the very first admin comes from
bootstrap, which has no admin requester by definition), and a **delete** of
an admin FrameUser. That last one matters on its own, not just as a
recreate-then-promote guard: without it, a non-admin holding `delete` could
remove an existing admin outright as long as a second one remained, with no
`spec.role` write involved at all. Re-add the label by hand and the webhook
still refuses — create, update and delete alike — disable webhooks and the
label still refuses. Removing both is what reopens it.

**`frameusers/status` is admin-only.** It carries an argon2id password hash.
`frameuser-editor-role` and `frameuser-viewer-role` carry **no `/status` rule
at all** — the only two tier roles of the twenty-seven that do not — and
`frameuser-admin-role` is the only admin tier with `patch`/`update` on a
`/status`, because setting or resetting a password is now only reachable
through the subresource.

**Know exactly what that buys, because it is less than it looks.** Both
halves below were measured against a real apiserver, not assumed:

- **Write protection: real, and it takes the webhook as well as the role.** An
  editor's `patch frameusers` cannot alter `status.passwordHash`. At `v1beta1`
  that is the apiserver: a merge patch carrying a spec change *and* a status
  change applies the spec half and drops the status half silently. At
  `v1alpha1` the same field is `spec.passwordHash`, and RBAC has no version
  dimension, so the role alone bought nothing for the whole deprecation
  window — the editor tier could set any account's password with one patch at
  the deprecated version, and erase one with a replace that merely omitted the
  field. The FrameUser validating webhook is what closes that: it refuses any
  main-resource write, at either version, that changes the hash. **Both halves
  are load-bearing.** If you disable webhooks (`webhooks.enabled: false`), the
  editor tier can set passwords again.
- **Confidentiality: not real.** A plain `GET frameusers` returns the whole
  object, status included. **A viewer or editor can still read the hash.** The
  Kubernetes status subresource splits *writes*, not reads; denying it removes
  the dedicated `/status` endpoint and nothing else.

So **treat `get frameusers` as equivalent to holding every password hash.**
Do not bind `frameuser-viewer-role` to anyone you would not hand the hashes
to — which is why it is not in the aggregation and `frame:viewers` does not
hold it. Moving the hash into a `Secret` is the only change that fixes this; it is
recorded as the destination in the CRD reference and is not part of the
freeze. The tiers are written in the shape they will need when it happens.

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

**Bounded to the namespace is not bounded to the field, and that gap is
real.** `baseline` Pod Security refuses `securityContext.privileged`,
`hostPath` volumes, the host namespaces (`hostNetwork`/`hostPID`/`hostIPC`),
`hostPort` and added capabilities — host escape, specifically. It does **not**
refuse a `volumes.secret` mount, `envFrom.secretRef`,
`containers[].command`/`args`, `serviceAccountName` or `runAsUser`, and
`restricted` bounds Secret mounts no more than `baseline` does. So an account
in `frame:operators`, holding `patch` on Deployments in the seven enforced
namespaces plus the cluster-wide `pods/log get` above, can patch a pod
template to mount any Secret in that namespace, add
`command: ["sh","-c","env; sleep 1d"]`, and read the result back out of the
logs — or swap `serviceAccountName` to assume any ServiceAccount identity in
the namespace. No tier in this repository is granted `secrets` anywhere, so
this design grants indirectly, through a pod's own spec, exactly what it
refuses to grant directly. This is documented in full in the file itself
(`deploy/kubernetes/base/rbac-workload-operator.yaml`); closing it needs a
validating webhook on the patch body, or moving restart and scale to the admin
tier — both open decisions, not something this lot resolves.

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
call site the way that file requires. Two tests in `go test ./test/manifests/`
cover this, and they bind different roles: `TestWorkloadRolesAreUnaggregatedAndNarrow`
checks the namespaced `cluster-control-workload-admin` role itself for the
five-kind grant's presence and for the absence of `secrets`, `configmaps` and
`serviceaccounts`; `TestTheManifestEditorIsNotAClusterWideGrant` separately
checks the aggregated cluster-wide `admin` tier for the absence of `secrets`,
`configmaps`, `clusterroles` and `customresourcedefinitions`. The first proves
the unaggregated role is narrow; the second proves aggregation never widens
it.

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

**Only three of the seven exist on this cluster today.** `default`,
`inference` and `neura` are live, and `kubectl label --dry-run=server` came
back clean for all three before `enforce` was added to them on 2026-09-10.
`neura-batch`, `neura-database`, `neura-inference` and `neura-training` do not
exist yet — they are declared here because this repository's own manifests
name them (`deploy/kubernetes/scheduling/elastic-lpar-pools.yaml`,
`deploy/jobs/speculative-decoding.yaml`), so labelling them now means the
first pod ever admitted into each, whenever it is created, is already
checked, rather than leaving a namespace this rollout has to remember to come
back to. Two further application namespaces are live on the cluster today and
deliberately **absent** from this list: `neura-jobs` and `neura-sandbox` are
owned by Neura's own Helm chart, and a `kind: Namespace` document here would
make this kustomization claim label ownership of a namespace another chart
already owns — the server-side-apply ownership conflict this project has
already been bitten by once. Neither is Pod-Security-enforced, and the
Workloads screen shows no Restart or Scale button in either — reads still
work, the same as any other unenforced namespace.

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

### The check that has never once been run end to end

Everything below is proven in Go tests, in vitest and by reading manifests.
None of it has been done on the cluster, and an exec's failure modes are
physical — a session that will not close, a resize that leaves the display
offset, a `Ctrl-C` that does not reach the process. Until someone has done
this, the terminal is unproven.

Two more gaps ship unexercised by anything above, worth knowing before
running this list. `deploy/docker/nginx.conf` and `vite.config.ts` carry no
automated test: the WebSocket upgrade path — `proxy_http_version 1.1`, the
`Upgrade`/`Connection` header pair, the 3600s read timeout — is checked today
by reading the directives, not by a WebSocket that actually passes through
them, so step 5 below is also the first time either file is exercised. And
switching pods while a shell is open or a YAML edit is unsaved discards both
silently, with no confirmation: `PodDetailPanel` remounts fully on every pod
selection change, by design, for every other piece of pod-scoped state. Known
and deferred, not a defect found here — worth remembering before moving
between steps 5, 8 and 10 in one sitting.

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

### Rollout order

Turning per-user enforcement on is a five-step sequence, and the order is
load-bearing — not a preference, a dependency chain.

**This rollout cannot start until the console's real hostname is decided.**
It has been asked about and is not yet answered. `RP_ID` governs the *only*
credential the bootstrapped admin can ever hold (step 5 below), and a wrong
value there strands them with no way back short of the node's own
kubeconfig — so `deploy/kubernetes/authd/deployment.yaml` ships `RP_ID` /
`RP_ORIGIN` as `REPLACE_HOSTNAME`, the same placeholder token
`base/ingress.yaml` uses, rather than a guessed hostname someone could
deploy by accident believing it was configured. See step 2 below for where
to set it once the hostname is chosen, and the section after this sequence
for why the value matters as much as it does.

This order was rewritten on 2026-09-08 (whole-branch review, I5 and C5). The
previous one could not be executed: its step 1 said to bootstrap the first
admin *and* enrol a passkey "while the old anonymous path still works", but
the "Passkeys" entry, `LoginView` and `PasskeysDialog` all ship in the new UI
image, which lives in the same `Deployment` object as the `uiproxy` sidecar.
Applying it *is* the step that closes the anonymous path, so the two halves
of that step could not both hold. It also never said to build the uiproxy
image at all, so an operator following it reached the atomic step with the
pod in `ImagePullBackOff` and the ServiceAccount's direct RBAC already gone.

The key fact that makes the new order work, and that the old one missed:
**`/auth/bootstrap` does not go through the uiproxy.** The browser reaches it
through nginx's `location /auth/`, and authd creates the `FrameUser` under
its own ServiceAccount. So bootstrapping *after* impersonation is live works
fine — there is no chicken-and-egg.

1. **Build, push and retag every image the new `Deployment` names.** Nothing
   is applied yet.

   ```bash
   make docker-build-ui docker-push-ui           IMG_UI=<registry>/frame-ui:<tag>
   make docker-build-uiproxy docker-push-uiproxy IMG_UIPROXY=<registry>/frame-uiproxy:<tag>
   make set-image-ui set-image-uiproxy           IMG_UI=… IMG_UIPROXY=…   # or -prod
   ```

   `frame-uiproxy` is a second image in the same pod, and it was missing from
   the base and both overlays' `images:` until this was written — so
   `frame-uiproxy:latest` was never retagged and the kubelet went looking for
   it on Docker Hub. Render the overlay and check both images before
   applying: `kubectl kustomize deploy/kubernetes/overlays/production | grep
   'image:'`.

2. **Replace `REPLACE_HOSTNAME` with the real hostname in both `RP_ID` and
   `RP_ORIGIN`** (`deploy/kubernetes/authd/deployment.yaml`). Before step 4,
   not after: `RP_ID` is what WebAuthn binds a credential to, and a wrong one
   fails at the enrolment ceremony — the one moment where the first admin has
   no other way in. This is server configuration and depends on nothing else,
   so there is no reason for it to be late — and this whole rollout cannot
   reach this step at all until the hostname is decided (see above). See the
   open question below for what a wrong value costs.

3. **Apply the tier bindings** (`rbac-tier-bindings.yaml`) and the per-kind
   tier `ClusterRole`s. Before step 4 for the same reason as always: once
   impersonation is live, a token carrying `frame:admins` buys nothing until
   that group is actually bound to `frame-admin`.

   Check what a tier grants before trusting it. `go test ./test/manifests/`
   asserts the invariants that matter — an operator can cordon, a viewer
   cannot, and no tier below admin reaches `frameusers` or a Talos write.

4. **Apply the new `Deployment` (the `uiproxy` sidecar swap and the new UI
   image) and the ServiceAccount's RBAC change together.** These are one
   atomic change, not two: the new `deployment.yaml` requires impersonation to
   reach the apiserver at all, and the new `rbac.yaml` is what removes the
   ServiceAccount's direct `cluster-control-viewer`/`cluster-control-operator`
   binding and replaces it with `impersonate` on `users` and the three
   `frame:` groups.

   **Two preconditions, or the console does not start at all — not just
   `/auth/`.** Both are verified against a real `nginx -t`, not assumed, and
   both are explained at length in `deploy/docker/nginx.conf` and
   `deploy/kubernetes/base/deployment.yaml` themselves:

   - **The authd `Service` must already exist and resolve.** `nginx.conf`'s
     `location /auth/` proxies to it with a literal hostname, which nginx
     resolves once, at startup — not at request time. If the Service object
     is missing (authd is Stage 1; something would have had to delete it),
     nginx's config test fails with `[emerg] host not found in upstream` and
     the pod never becomes ready, taking every route down, not only `/auth/`.
   - **The `frame-auth-tls` Secret must exist and carry `ca.crt`.** It is what
     `deploy/kubernetes/base/deployment.yaml` projects into
     `/etc/frame-auth-ca/ca.crt`, which `nginx.conf`'s
     `proxy_ssl_trusted_certificate` reads to verify authd's TLS certificate.
     cert-manager populates it from `deploy/kubernetes/authd/certificate.yaml`
     (Stage 1, applied first) — if it is missing, or present without
     `ca.crt`, nginx fails with `[emerg] cannot load certificate
     "/etc/frame-auth-ca/ca.crt"`, same outcome: the whole console fails to
     start.

   **Between this step and the next, nobody can use the console.** Say it out
   loud rather than discovering it: every request now has to arrive as an
   impersonated `FrameUser`, and none exists yet. That is expected, it is
   minutes long, and step 5 ends it.

5. **Bootstrap the first admin from the browser, then enrol a passkey
   immediately.** From the browser, not `curl`, and this is the whole point:
   `/auth/bootstrap` answers with a `Set-Cookie` for a 12-hour session
   (`SessionTTL`), and that session is the *only* way to enrol a passkey
   (`/auth/register/begin` reads the account off that cookie). A `curl` leaves
   the cookie in a jar the browser will never see.

   Open the console — it shows the login screen — and in the devtools console,
   on that same origin:

   ```js
   await fetch('/auth/bootstrap', {
     method: 'POST',
     headers: { 'Content-Type': 'application/json' },
     body: JSON.stringify({ token: '<the frame-auth-bootstrap Secret>', email: 'you@example.com' }),
   })   // 204 on success
   ```

   Then reload. `currentSession()` mints a bearer token from the cookie and
   the console opens as an admin. **The very next thing to do is enrol a
   passkey**, via the "Passkeys" entry in the sidebar footer
   (`src/components/PasskeysDialog.tsx`). The account bootstrap creates has
   `passwordAuth: disabled` and no credential of any kind
   (`internal/authd/server_bootstrap.go`); a passkey is the only credential it
   can ever acquire, and it can only be enrolled from inside that 12-hour
   window. Let the cookie lapse with none enrolled and there is no way back
   through the UI or through authd at all: recovery means hand-editing the
   `FrameUser`'s status or deleting the `ValidatingWebhookConfiguration` that
   guards the last admin, and both need the node's own kubeconfig.

   If an admin `FrameUser` already exists, `/auth/bootstrap` answers 404 by
   design (`AdminCount() > 0` closes it before the token is even checked) —
   sign in as that account instead.

**Rollback** is re-applying the previous kustomization (the old
`deployment.yaml` + `rbac.yaml`, before this lot). Accounts created in step 5
survive it — `FrameUser` objects are ordinary CRs, untouched by which sidecar
or RBAC is currently applied — so a rollback and a later re-attempt does not
need to bootstrap again.

**If you are stranded**, the way back is the node's own kubeconfig:
cluster-admin outside Frame's RBAC entirely, used either to fix the missing
piece by hand or to re-apply the previous kustomization. That identity is in
`system:masters`, which is why the FrameUser webhook's role-change guard
admits it — see the RBAC section above.

**Open question, not yet answered: what is the console's real hostname?**
`deploy/kubernetes/authd/deployment.yaml` sets `RP_ID: REPLACE_HOSTNAME` and
`RP_ORIGIN: https://REPLACE_HOSTNAME` — the same placeholder token
`base/ingress.yaml` uses, deliberately, so that nobody can apply this
manifest by accident believing it is configured. It has been asked and is
not yet answered. `RP_ID` is what WebAuthn binds a credential to: a browser
will only complete a passkey ceremony when the page's origin matches
`RP_ORIGIN` and its domain matches or is a suffix of `RP_ID`. This matters
more than an unresolved placeholder normally would, because passkeys are
wired into the UI now (`src/components/LoginView.tsx`'s "Sign in with a
passkey", `src/components/PasskeysDialog.tsx`'s enrolment dialog) and step 5
above depends on one: the bootstrapped admin's *only* credential is a
passkey enrolled in the 12-hour window after bootstrap, and that enrolment
ceremony is exactly where a wrong `RP_ID` bites. It does not fail loudly,
and it does not fail invisibly-later either — it fails at the one moment
that matters, stranding the first admin before they have any other way in,
since password sign-in isn't a fallback for this specific account
(`passwordAuth: disabled`).

**This changes what "step 4" can mean in practice.** `authd` is already
running when step 1 executes (it has been since Stage 1), so the `RP_ID` /
`RP_ORIGIN` that actually govern the bootstrap admin's enrolment ceremony
are whatever is *already deployed* on that running pod at that moment — not
whatever step 4 will eventually set. **Confirm the currently-running value
is the decided hostname before starting step 1**, once the hostname has
been decided, and correct it now if the two differ. Step 4 remains the
place to *apply* a corrected value if that hadn't already been done — but by
the time this rollout reaches step 4, it is too late for it to help the
account created in step 1: a passkey enrolled against a wrong `RP_ID` does not
become valid retroactively when `RP_ID` is fixed later, because a passkey
is bound to the domain it was created for, not to the account.

### Inviting a second person

1. Sign in as an admin, open **Accounts**, choose **Invite**, give an address
   and `viewer` or `operator`.
2. Copy the link and send it however you already talk to that person. Frame
   sends no mail: there is no mail path in this cluster, and adding one is a
   separate project.
3. They open the link and enrol a key. The link then dies — refused because
   the account holds a credential, not because a flag was flipped. Nothing
   needs cleaning up. **They do not land signed in.** The link's session is
   an enrolment session, sealed under its own purpose and good only for the
   two WebAuthn enrolment routes — it cannot mint a token and it cannot call
   `/auth/invite`. Once the key is enrolled, they hold no session at all and
   sign in normally, the same as anyone else (see `src/components/InviteAcceptView.tsx`).
4. To make someone an admin, invite them as an operator or viewer first and
   change the role in the table. An invitation cannot create an admin:
   `authd` acts under its own ServiceAccount, and admission refuses a
   `spec.role: admin` create from anyone who is not already an admin — the
   guard that stops everything holding `create frameusers` from minting one.
5. To cut someone off, set their `spec.state` to `disabled` in the Accounts
   table. Their keys stay enrolled; re-enabling restores them without a new
   ceremony. An open session stops working within one token lifetime (15
   minutes by default), because every identity-issuing route in `authd`
   checks `spec.state` before it will mint or re-mint anything for that
   account (see [crd-reference.md](crd-reference.md), "FrameUser").

**None of the writes in steps 1-3 appear on the Tasks screen.** Invitation,
acceptance, enrolment, revocation and the original bootstrap all go to
`authd` directly, by `fetch` from the browser to `/auth/*`. They never pass
through `frame-uiproxy`, and it is the proxy — not the apiserver, and not
`authd` — that records a `FrameTask` for each write it forwards
(`internal/uiproxy/recorder.go`). So creating someone's account and removing
their sign-in credential, the two most privilege-affecting things this
console can do, leave no `FrameTask` at all.

They are not unrecorded: `authd` logs each of them, so
`kubectl -n cluster-control logs deploy/cluster-control-auth` is where that
trail lives. But the Tasks screen advertises itself as "every write made
through the UI", and for these five routes it is not — a reader who takes
that promise literally will look at an empty Tasks list and conclude the
invitation never happened. Role and state changes from the Accounts table
*do* appear there, because those go through the proxy to the apiserver; only
the `authd` routes are missing.

Making `authd` write `FrameTask`s would close the gap, and is deliberately
not done here: it would give the identity provider a second write path into
the cluster API and a reason to hold `create frametasks`, which is a design
change rather than a documentation fix.

> **The check that has never once been run end to end.** Invite a viewer.
> Accept it in a separate browser profile and enrol a key — per step 3
> above, that ends the enrolment session, not a signed-in one. Sign in as
> the viewer with the new key. While signed in, open the passkeys dialog and
> use the revoke confirmation — it is the first place in this codebase that
> opens a confirmation dialog on top of an already-open dialog
> (`src/components/PasskeysDialog.tsx`), nothing else in the repo does that,
> and a `.tsx` component cannot be exercised by the test suites available
> here, so it needs a human's eyes once. Expect a **409**, not a deletion:
> the viewer holds exactly one key at this point, and `RemoveCredential`
> refuses to strip a passkey-only account of its last credential
> (`server_credentials.go:116-124`) — the confirmation dialog still opens
> and stacks correctly, which is what is under test, and the refusal is
> correct, not a bug to chase. Then, still signed in as the viewer, attempt
> to cordon a node and confirm a **403** and a `FrameTask` recording the
> refusal. Finally, disable the account from the admin's Accounts screen and
> confirm the viewer's console returns to the login gate within one token
> lifetime without anyone clearing a cookie. **Until all of this has been
> done on the cluster, per-user identity is proven only in envtest and by
> `SubjectAccessReview`.**

### Running the storage-version migration

`hack/migrate-storage-version.sh` is **not** runnable under any of these
tiers, deliberately. It needs `patch` on every Frame resource *and* `patch` on
`customresourcedefinitions/status` in `apiextensions.k8s.io` — a group no
Frame tier mentions, and one where that single verb means "change which
version is stored for any CRD in the cluster", for any operator, not just
this one. That is cluster-admin power wearing a narrow name, and shipping a
bindable `ClusterRole` for it would invite exactly the mistake this section
is otherwise arguing against. Run the migration as a cluster administrator.

---

## Installing the operator via Helm

Alternative to step 2 above, for the operator only. The chart at
`charts/frame/` packages the CRDs, the controller Deployment, RBAC, the
webhook configuration, and (optionally) the cert-manager `Issuer`/
`Certificate` that issues the webhook's TLS. **It does not install the UI or
authd** — those still come from `deploy/kubernetes/` via kustomize (steps 3-5
above). Read `charts/frame/README.md` before changing anything in the chart:
it documents two decisions (fixed `frame-` resource names instead of the
usual release-name-derived ones, and CRDs rendered from `templates/` instead
of Helm's `crds/` directory) that look like bugs and are not.

If you already have the operator running from `kubectl apply -k config/default`
and want to move it onto this chart instead of doing a fresh install, see
[upgrading.md](upgrading.md) — that migration has its own procedure and its
own caveats.

### Prerequisites

- A Kubernetes cluster, 1.29+ (`Chart.yaml`'s `kubeVersion` gate). Verified
  in this session against a kind `v1.34.0` cluster and (read-only, via
  `helm template`/`--dry-run`) against the k3s `v1.36.2+k3s1` test cluster.
- Helm — verified with v4.2.3 locally; the chart pins no minimum Helm
  version.
- cert-manager installed on the target cluster. The chart's webhooks depend
  on it by default (`certManager.enabled: true`): it issues the webhook
  serving certificate and injects the CA into both `WebhookConfiguration`s.
  See the "Cert-manager" section above for the install command. Running
  without cert-manager is possible (`certManager.enabled: false`) but shifts
  certificate issuance onto you — see "Installing without cert-manager" in
  `charts/frame/README.md` before choosing that path.
- A built operator image (`Dockerfile.controller`, **not** the root
  `Dockerfile`, which builds the UI). There is no published image yet, so
  `image.repository` has no default — the chart's `required` guard fails the
  install with an actionable message rather than installing into
  `ImagePullBackOff`.

### Install

```bash
helm install frame charts/frame \
  --namespace frame-system --create-namespace \
  --set image.repository=<your-registry>/frame-controller \
  --set image.tag=<tag>
```

The release name (`frame` above) is cosmetic — every object this chart
renders uses a fixed `frame-` prefix regardless of it, so any release name
produces the same `frame-controller-manager`, `frame-webhook-service`, etc.
(`charts/frame/README.md` explains why this is deliberate). This exact
invocation, with a real image swapped in, is what was run end-to-end
(install → CRDs present → cert-manager issues the cert → `helm upgrade` →
`helm uninstall`) against a disposable local kind cluster while writing this
guide.

### Values worth knowing on a first install

| Value | Default | Why it matters here |
|---|---|---|
| `image.repository` | `""`, **required** | No published image exists yet; must point at an image built from `Dockerfile.controller`. |
| `replicaCount` | `1` | `2` is a supported HA configuration (leader election is always on) — see the runbook's "Failover" section for measured takeover behaviour. |
| `crds.install` | `true` | Set `false` only if CRDs are managed some other way; the chart fails the render if `true` and no CRD files are found, so this is not a silent no-op. |
| `certManager.enabled` | `true` | `false` means you provision `webhooks.certSecretName` and `webhooks.caBundle` yourself — see "Installing without cert-manager" in `charts/frame/README.md`. |
| `metrics.serviceMonitor.enabled` | `false` | Turn on only if the Prometheus Operator CRDs are already installed. |
| `networkPolicy.enabled` | `false` | Off by default, matching kustomize. If enabled, the webhook rule is intentionally open on port 9443 to any source — `charts/frame/README.md` explains why a source-restricted rule breaks admission on real clusters. |
| `rbac.tierRoles.install` | `true` | The 27 viewer/editor/admin convenience `ClusterRole`s (three per CRD); not required by the manager itself, and bound to nobody by the chart — the group bindings that make them reachable are `deploy/kubernetes/base/rbac-tier-bindings.yaml`, part of the UI/authd kustomize path, not this chart — see "RBAC" above. |

The full, commented list is in `charts/frame/values.yaml`; `charts/frame/README.md`
has the complete table with the reasoning behind each default.

### Verifying the install

```bash
kubectl -n frame-system rollout status deployment/frame-controller-manager
kubectl get crd | grep plume-labs.io
kubectl -n frame-system get certificate frame-serving-cert   # if certManager.enabled
```

All three are printed by the chart's own `NOTES.txt` after `helm install`
completes. For anything beyond "did the install come up" — leader lease,
certificate expiry, webhook CA match, the single dry-run command that
exercises the whole admission path — see the "Is it healthy?" section of
[runbook.md](runbook.md), which was written and measured against a running
cluster and is not repeated here.

### Uninstalling

```bash
helm uninstall frame -n frame-system
```

Every CRD this chart installs carries `helm.sh/resource-policy: keep`, so
`helm uninstall` removes the Deployment, RBAC and webhook configuration but
**leaves the CRDs (and any CRs) in place** — verified in this session: after
`helm uninstall` on a disposable kind cluster, all eight `plume-labs.io` CRDs
then present were still there afterwards, and `helm uninstall` printed each one
under "kept due to the resource policy." (`NodeTuning` and `FrameTask` both
postdate that run and carry the same annotation, but neither was part of the
verified set.) Removing the CRDs themselves is a deliberate, separate,
manual step (`kubectl delete crd <name>`) — not exercised here, since on a
real cluster it cascade-deletes every CR of that kind. See "CRDs are
rendered from `templates/`, not Helm's `crds/` directory" in
`charts/frame/README.md` for why the chart is built this way.

---

## Kustomize overlay reference

```
deploy/kubernetes/
├── base/
│   ├── namespace.yaml
│   ├── rbac.yaml
│   ├── deployment.yaml
│   ├── service.yaml
│   ├── ingress.yaml
│   ├── hpa.yaml
│   └── pdb.yaml
└── overlays/
    ├── development/      # 1 replica, debug logging
    └── production/       # production replica count, image tag
```

---

## Makefile targets

| Target | What it does |
|---|---|
| `make docker-build` | Build the controller image |
| `make docker-push` | Push the controller image |
| `make docker-build-ui` | Build the UI image (the unsuffixed `Dockerfile`) |
| `make docker-build-authd` / `make docker-push-authd` | Build/push the authd image (`IMG_AUTHD`) |
| `make docker-build-agent` / `make docker-push-agent` | Build/push the node-tuning agent image (`IMG_AGENT`, default `frame-agent:latest` — it matches the name the DaemonSet declares, so `kustomize edit set image frame-agent=…` works) |
| `make install` | Apply CRDs to the cluster |
| `make deploy` | Apply the full operator (config/default) |
| `make undeploy` | Remove the operator from the cluster |
| `make setup-test-e2e` | Create a Kind cluster for e2e tests |
| `make test-e2e` | Run e2e tests against the Kind cluster |
