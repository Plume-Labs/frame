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
request may say: `requireAdminRequester` refuses any change to `spec.role`
made by someone who is not already in `frame:admins` (or `system:masters`,
the break-glass path the rollout depends on). Re-add the label by hand and the
webhook still refuses; disable webhooks and the label still refuses. Removing
both is what reopens it.

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

### Rollout order

Turning per-user enforcement on is a four-step sequence, and the order is
load-bearing — not a preference, a dependency chain. Do them in this order:

1. **Bootstrap the first admin through authd's one-shot `/auth/bootstrap`
   Secret, while the old anonymous path still works.** `authd` is already
   running (Stage 1) but has no `FrameUser` to authenticate as yet; `POST
   /auth/bootstrap` with the token in the `frame-auth-bootstrap` Secret
   creates the first admin account and deletes the Secret so it cannot be
   replayed (`internal/authd/server_bootstrap.go`). Do this before step 3
   swaps the ServiceAccount's own RBAC for impersonation-only: if the swap
   happens first and this account does not exist yet, there is nobody
   `frame-uiproxy` can impersonate into anything, and the console is locked
   out for everyone with no `FrameUser` to sign in as. The old `kubectl
   proxy`-style access is still live at this point precisely so a failed or
   retried bootstrap call has a working fallback.

   **The very next thing to do, inside the same window, is enrol a
   passkey.** This is not optional housekeeping — it is what decides
   whether this rollout succeeds or strands the operator. The account
   bootstrap creates has `passwordAuth: disabled` and no credential of any
   kind (`internal/authd/server_bootstrap.go`); the only thing the caller
   walks away with is a session cookie, good for 12 hours (`SessionTTL`,
   default in `internal/authd/server.go`) — not the 15-minute `TOKEN_TTL`
   set in `deploy/kubernetes/authd/deployment.yaml`, which is the *bearer*
   token the UI silently re-mints from that still-valid cookie
   (`currentSession()` in `src/lib/auth.ts`). A passkey is the only
   credential this account can ever acquire, and it can only be enrolled
   from inside that 12-hour session — in the UI, via the "Passkeys" entry
   in the sidebar footer (`src/components/PasskeysDialog.tsx`, driving
   `POST /auth/register/begin` + `/finish`). Let the cookie lapse with none
   enrolled and there is no way back through the UI or through authd at
   all: recovery means hand-editing the `FrameUser`'s `status` or deleting
   the `ValidatingWebhookConfiguration` that guards the last admin, and
   both of those need the node's own kubeconfig anyway.
2. **Apply the tier bindings** (`rbac-tier-bindings.yaml`): the
   `ClusterRoleBinding`s that give `frame:admins` / `frame:operators` /
   `frame:viewers` their aggregated `ClusterRole`s. This has to land before
   step 3 for the same reason as step 1: once impersonation is live, an
   admin token that carries the `frame:admins` group buys nothing at all
   until that group is actually bound to `frame-admin`.
3. **Apply the deployment (the `uiproxy` sidecar swap) and the
   ServiceAccount's RBAC change together.** These two are one atomic change,
   not two: the new `deployment.yaml` requires impersonation to reach the
   apiserver at all, and the new `rbac.yaml` is what removes the
   ServiceAccount's direct `cluster-control-viewer`/`cluster-control-operator`
   binding and replaces it with `impersonate` on `users` and the three
   `frame:` groups. Applying one without the other either leaves the old
   anonymous path open behind a sidecar that no longer expects it, or cuts
   off API access before anything can impersonate its way back in.
4. **Point authd's `RP_ID` / `RP_ORIGIN` at the real hostname.** WebAuthn
   binds credentials to `RP_ID`; see the open question below — this cannot
   be step 1, because there is nothing to enrol a passkey against until an
   admin account and a session exist.

**Inverting the first two — doing the deployment/RBAC swap (step 3) before
both the bootstrap admin and the tier bindings exist — locks everyone out.**
Once step 3 lands, the ServiceAccount can no longer read or write anything
on its own account; every request has to arrive as an impersonated
`FrameUser` whose group is bound to a tier. With no admin account, or an
admin account whose group has no binding, that is nobody. The only way back
at that point is the node's own kubeconfig — cluster-admin access outside
Frame's RBAC entirely, used to either fix the missing piece by hand or
re-apply the previous kustomization.

**Rollback** is re-applying the previous kustomization (the old
`deployment.yaml` + `rbac.yaml`, before this lot). Accounts created in step
1 survive a rollback — `FrameUser` objects are ordinary CRs, untouched by
which sidecar or RBAC is currently applied — so a rollback and a later
re-attempt does not need to bootstrap again.

**Open question, not yet answered: is `frame.anna.ovh` the right `RP_ID`?**
`deploy/kubernetes/authd/deployment.yaml` currently sets `RP_ID:
frame.anna.ovh` and `RP_ORIGIN: https://frame.anna.ovh`. That value appears
nowhere else in the repo — every other manifest that needs a real hostname
uses a placeholder (`base/ingress.yaml` and both overlays use
`REPLACE_HOSTNAME` / `REPLACE_YOUR_DOMAIN`), so this is the one exception,
and it is unconfirmed. `RP_ID` is what WebAuthn binds a credential to: a
browser will only complete a passkey ceremony when the page's origin matches
`RP_ORIGIN` and its domain matches or is a suffix of `RP_ID`. This matters
more than a stray placeholder normally would, because passkeys are wired
into the UI now (`src/components/LoginView.tsx`'s "Sign in with a passkey",
`src/components/PasskeysDialog.tsx`'s enrolment dialog) and step 1 above
depends on one: the bootstrapped admin's *only* credential is a passkey
enrolled in the 12-hour window after bootstrap, and that enrolment ceremony
is exactly where a wrong `RP_ID` bites. It does not fail loudly, and it
does not fail invisibly-later either — it fails at the one moment that
matters, stranding the first admin before they have any other way in,
since password sign-in isn't a fallback for this specific account
(`passwordAuth: disabled`).

**This changes what "step 4" can mean in practice.** `authd` is already
running when step 1 executes (it has been since Stage 1), so the `RP_ID` /
`RP_ORIGIN` that actually govern the bootstrap admin's enrolment ceremony
are whatever is *already deployed* on that running pod at that moment — not
whatever step 4 will eventually set. **Confirm the currently-running value
is already correct before starting step 1**, against the console's real
hostname, and correct it now if the two differ. Step 4 remains the place to
*apply* a corrected value if that hadn't already been done — but by the
time this rollout reaches step 4, it is too late for it to help the account
created in step 1: a passkey enrolled against a wrong `RP_ID` does not
become valid retroactively when `RP_ID` is fixed later, because a passkey
is bound to the domain it was created for, not to the account.

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
