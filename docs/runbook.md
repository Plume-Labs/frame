# Runbook

Operating the Frame operator: what healthy looks like, what breaks, and what
to do about it. Every timing and command here was measured against the k3s
test cluster, not inferred from the manifests.

Throughout: the manager is the Deployment `frame-controller-manager` in
namespace `frame-system`.

## Is it healthy?

Four things, in the order they fail:

```bash
# 1. The manager is running.
kubectl get deploy -n frame-system frame-controller-manager

# 2. Exactly one replica holds the leader lease, and it is being renewed.
kubectl get lease -n frame-system b9bf5a0e.plume-labs.io \
  -o custom-columns='HOLDER:.spec.holderIdentity,TRANSITIONS:.spec.leaseTransitions,RENEW:.spec.renewTime'

# 3. The webhook certificate is issued and not near expiry.
kubectl get certificate -n frame-system \
  -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,NOTAFTER:.status.notAfter,RENEW:.status.renewalTime'

# 4. The CA in the webhook configurations matches the certificate that was issued.
CA=$(kubectl get secret -n frame-system webhook-server-cert -o jsonpath='{.data.ca\.crt}')
for k in validatingwebhookconfiguration mutatingwebhookconfiguration; do
  for n in $(kubectl get $k -o name | grep frame); do
    B=$(kubectl get $n -o jsonpath='{.webhooks[0].clientConfig.caBundle}')
    [ "$B" = "$CA" ] && echo "$n OK" || echo "$n MISMATCH"
  done
done
```

The single check that covers all four at once, because it exercises the whole
path — apiserver to webhook over TLS, with `failurePolicy: Fail` behind it:

```bash
kubectl apply --dry-run=server -f config/samples/frame_v1beta1_framejob.yaml
```

A success means the apiserver reached both the mutating and the validating
webhook and got a verdict. Nothing is created.

## The blast radius when the operator is down

Every validating webhook is registered `failurePolicy: Fail` with a 10s
timeout. That is the right default — a silently unvalidated CR is worse than a
rejected one — but it means **an operator that cannot be reached blocks every
create and update of every Frame CRD, cluster-wide**. Existing objects keep
running; the controllers simply stop reconciling. Reads are unaffected.

If you must write a CR while the manager is unrecoverable, delete the webhook
configurations, do the write, and reinstall them:

```bash
kubectl delete validatingwebhookconfiguration frame-validating-webhook-configuration
kubectl delete mutatingwebhookconfiguration frame-mutating-webhook-configuration
# ... write the CR, unvalidated and undefaulted ...
make deploy            # or: helm upgrade, which recreates both
```

Reach for this only when the alternative is worse. The defaulting webhook is
what fills in fields the controllers assume are set, so an object written this
way can be one the reconcile loop has never had to handle.

**That escape hatch does not cover conversion, and conversion is worse.** Each
CRD also declares `conversion.strategy: Webhook` pointing at the same
manager's `/convert`, and CRD conversion is configured *on the CRD*, not in a
`ValidatingWebhookConfiguration` you can delete. With the manager
unreachable, every read and write of a Frame kind fails at **both** versions —
including `kubectl get`, which the paragraph above says is unaffected. It is
unaffected only for the single-version CRDs of the pre-freeze install.

There is no safe local escape hatch for that: setting `strategy: None` on a
live CRD makes the apiserver hand back stored `v1beta1` bytes to a `v1alpha1`
client unconverted, which is how fields get silently dropped. Fix the manager
instead; that is what `failurePolicy` and the leader lease's 16-second
takeover are for, and it is the argument for `replicaCount: 2` restated in
sharper terms.

## Failover

Leader election is wired (`--leader-elect`) and 2 replicas is a supported
configuration — `replicaCount: 2` in the chart, or
`kubectl scale deploy -n frame-system frame-controller-manager --replicas=2`.

Measured behaviour, force-deleting the leader pod (no graceful lease release,
the worst case):

- **takeover in 16 seconds**, consistent with controller-runtime's 15s lease
  duration. A graceful pod deletion releases the lease and is faster.
- the new leader starts all controllers and reconciles every existing CR
  immediately — it does not wait for the next resync.
- **admission keeps working throughout.** Both replicas serve the webhook
  regardless of which holds the lease, and the webhook Service has both as
  endpoints, so a second replica improves admission availability even though
  only one replica reconciles.

That last point is the real argument for running two: reconciliation pausing
for 16s is unremarkable, but with one replica the same 16s is a window where
*nobody can write a Frame CR at all*.

## Certificates

cert-manager issues the webhook cert from the `frame-selfsigned-issuer`
Issuer, and the cainjector keeps the CA bundle in both webhook configurations
in sync. Certificates are 90-day, renewed at 60 days — check `renewalTime`
above, not `notAfter`.

If a cert is stuck, force reissue by deleting the Secret; cert-manager
recreates it and the cainjector re-injects:

```bash
kubectl delete secret -n frame-system webhook-server-cert
kubectl rollout restart deploy -n frame-system frame-controller-manager
```

The restart matters: the manager reads the cert from a mounted volume, and
kubelet's Secret refresh is not immediate.

> `frame-metrics-certs` is also issued and auto-renewed, but the manager does
> not mount it — the metrics cert patch is commented out in
> `config/default/kustomization.yaml`, so metrics are served with
> controller-runtime's own self-signed cert. The Certificate is currently dead
> weight rather than a misconfiguration, but it looks load-bearing and is not.

## Migrating the storage version

`.status.storedVersions` on a CRD only grows. The apiserver appends the new
storage version the moment an apply makes it the storage version — whether or
not anything is ever stored at it — and **never removes an entry**: not on its
own, not once the last object at the old version has been rewritten, not ever.
A version cannot be dropped from `spec.versions` while it appears in
`storedVersions`, so the list has to be pruned by hand, and that patch is a
*claim* that nothing is stored at the old version any more. Objects are
rewritten at the storage version on any write, so a no-op annotation patch is
enough; a kind with **zero** objects has nothing to rewrite and the status
patch is its whole migration.

Order matters. Apply the CRDs and roll out the manager that serves `/convert`
**first**: a two-version CRD with `strategy: Webhook` and nothing answering
`/convert` fails every read and write of that kind.

```bash
./hack/migrate-storage-version.sh            # dry run, changes nothing
./hack/migrate-storage-version.sh --apply
```

**Run it as a cluster administrator.** It needs `patch` on every Frame
resource *and* `patch` on `customresourcedefinitions/status` — none of the
`*-admin-role` tiers grant the latter, and none should: that one verb changes
which version is stored for *any* CRD in the cluster, for any operator. There
is deliberately no narrow, bindable role for this; see "Running the
storage-version migration" in [deployment.md](deployment.md).

The script fails loudly rather than half-finishing: it refuses to touch a CRD
whose storage version is not the one it is migrating to, it aborts rather than
treat a failed listing as "no objects", and it exits non-zero if a CRD does not
end at a single stored version — in which case that CRD's old version must not
be removed.

**Check the Argo Workflows before running it against a cluster with completed
FrameJobs on it.** Rewriting a FrameJob re-triggers its controller, which is
wanted — the legacy stored `status.phase` does not survive conversion, and only
a reconcile puts a real `Ready` condition in its place — but if a job's Workflow
has been garbage-collected the controller's `IsNotFound` branch creates a new
one and silently re-runs a completed job.

```bash
kubectl get framejobs -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,WF:.status.argoWorkflowName
kubectl get workflows.argoproj.io -A
```

Afterwards, confirm the objects still read correctly through both versions and
that the projected phases came back:

```bash
kubectl get framejobs -A -o wide     # PHASE from the Ready condition's reason
kubectl get framenodes -A -o wide
```

A blank `PHASE` means the `Ready` condition carries a reason outside the
projection's vocabulary — compare what the controller wrote against the
`…PhaseFromConditions` helper for that kind.

## Backup and restore

The operator is stateless. Everything worth restoring is CRs in etcd, which
Velero handles as ordinary API objects — no volume snapshots, no plugins.

Verified round-trip on the test cluster (backup, delete the object, restore):

```bash
kubectl apply -f - <<'EOF'
apiVersion: velero.io/v1
kind: Backup
metadata: {name: frame-crs, namespace: velero}
spec:
  includedResources:
    - framejobs.frame.plume-labs.io
    - framenodes.frame.plume-labs.io
    - frameresourcequotas.frame.plume-labs.io
    - frameusers.frame.plume-labs.io
    - schedulingpolicies.frame.plume-labs.io
    - talosmachineconfigs.frame.plume-labs.io
    - talosupgrades.frame.plume-labs.io
    - frameservices.services.plume-labs.io
  includedNamespaces: ["*"]
  ttl: 720h0m0s
EOF

kubectl apply -f - <<'EOF'
apiVersion: velero.io/v1
kind: Restore
metadata: {name: frame-crs-restore, namespace: velero}
spec: {backupName: frame-crs}
EOF
```

Restored objects come back spec-identical and the controllers re-reconcile
them into a `Ready` condition without intervention.

**Check `status.progress.itemsBackedUp`, every time.** A Velero backup whose
selector matches nothing completes green with zero items — this cluster has
already been bitten once by backups that were silently empty for months.

```bash
kubectl get backup -n velero frame-crs \
  -o jsonpath='{.status.phase} {.status.progress.itemsBackedUp}/{.status.progress.totalItems}{"\n"}'
```

Two things this does **not** cover. CRDs themselves are not in the list above,
because a restore is into a cluster where Frame is installed and the chart owns
the schema — restoring a stale CRD over a newer one would be a downgrade, not a
recovery. And `status` is restored as recorded, so an object mid-operation
comes back describing a world that has moved on; the controllers correct it on
the next reconcile, but a `TalosUpgrade` restored mid-reboot deserves a look
before you trust its status.

## Logs and metrics

```bash
kubectl logs -n frame-system deploy/frame-controller-manager -f
kubectl logs -n frame-system deploy/frame-controller-manager --previous   # after a crash
```

Metrics are served on `:8443` over TLS and require a bearer token with
`get` on `/metrics` — the `frame-metrics-reader` ClusterRole exists for this.
Health and readiness are plain HTTP on `:8081` (`/healthz`, `/readyz`).

Log lines carry `controller`, `controllerKind`, the object's
namespace/name, and a `reconcileID` that ties every line of one reconcile
together — grep that, not the object name, when two reconciles interleave.

## Containing the control-plane UI (RBAC + NetworkPolicy)

Prepared 2026-08-10, **not applied**. Two changes that shrink what the
`cluster-control` namespace can do and who can reach it, in response to findings
C1 and C2 of `docs/superpowers/reviews/2026-08-09-security-review.md`: the UI has
no authentication in front of it, its `kubectl proxy` sidecar carries a
cluster-wide ServiceAccount, and the `cluster-control-ui-lan` NodePort (30379)
now publishes that to the whole LAN.

Full rationale, and the list of every grant removed with the call site that
justified keeping the rest: `.superpowers/ui-containment-report.md`.

**What this does not do.** It does not add authentication. Anyone on the LAN can
still drive the Kubernetes API as `cluster-control-ui`. It removes the path from
there to node root, and it stops a compromised pod elsewhere in the cluster
(`jupyterhub`, `neura-sandbox`) from reaching the UI at all. Per-user auth is
still the real fix.

### Pre-flight — three things to confirm, all read-only

The NetworkPolicy is written against addresses measured on this cluster. If any
of them has moved, applying it takes the UI offline. None of these commands
changes anything.

```bash
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml

# 1. podCIDRs. The policy allows, per node, the flannel.1 address (the network
#    address of the podCIDR) and the cni0 address (the next one). Expected:
#    cp 10.42.0.0/24, w2 10.42.1.0/24, w1 10.42.2.0/24 -> six /32s in the file.
kubectl get nodes -o custom-columns=NAME:.metadata.name,POD_CIDR:.spec.podCIDR,IP:.status.addresses[0].address

# 2. The addresses the UI pods actually see. This is the measurement the policy
#    is built on -- browse to http://192.168.2.201:30379/ first, then:
kubectl -n cluster-control logs -l component=ui -c ui --tail=200 --prefix \
  | grep -oE '^\[[^]]+\] [0-9.]+' | sort | uniq -c
#    Every source must appear in cluster-control-allow-ui-ingress. Note that
#    /health sets `access_log off`, so kubelet probes are NOT in this output --
#    they arrive from the local node's cni0 (10.42.<n>.1), which is allowed.

# 3. NetworkPolicy is actually enforced. k3s embeds kube-router's netpol
#    controller (it is not a DaemonSet, so `kubectl get ds` will not show it);
#    it is off if the server was started with --disable-network-policy.
kubectl get netpol -A     # argocd/jupyterhub/neura-sandbox policies exist and are relied on
```

> **Not verified.** The rehearsal ran on Kind + Calico, not k3s + kube-router.
> The address measurement in step 2 is from the live cluster and is solid, but
> whether kube-router treats same-node host-to-pod traffic the way Calico does
> was not tested. Step 2 of the verification below catches it either way.

### Apply order

Do the RBAC first. It is the reversible half, it needs no address to be right,
and if the NetworkPolicy has to be rolled back the RBAC narrowing should stay.

```bash
cd /home/rmocq/Neura/.externals/frame
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml

# Keep a rollback copy of live state first.
kubectl get clusterrole cluster-control-viewer   -o yaml > /tmp/rb-viewer.yaml
kubectl get clusterrole cluster-control-operator -o yaml > /tmp/rb-operator.yaml

# 1/3 -- narrowed ClusterRoles.
#    `apply -f`, never `apply -k deploy/kubernetes/base/`: the base's
#    deployment.yaml image and service.yaml type are overlay-only placeholders,
#    and a bare base apply resets the running UI image and turns the LAN
#    NodePort back into a ClusterIP.
kubectl apply -f deploy/kubernetes/base/rbac.yaml

# 2/3 -- namespaced pod-proxy Roles (six namespaces) ONLY. Rendering the whole
#    containment target here would apply the NetworkPolicies too; keep them
#    separate so the risky half lands on its own.
./bin/kustomize build deploy/kubernetes/containment/ \
  | kubectl apply -f - --prune=false --selector app.kubernetes.io/part-of=cluster-control-containment \
    --dry-run=client -o name        # inspect first
kubectl apply -f deploy/kubernetes/containment/rbac-integration-proxy.yaml

# --- stop here and use the UI. Every screen except the Restart button on the
# --- Applications screen must still work. Then continue.

# 3/3 -- NetworkPolicies. This is the step that can black out the UI.
kubectl apply -f deploy/kubernetes/containment/networkpolicy-cluster-control.yaml
```

### What to watch, in the 60 seconds after step 3/3

```bash
# Probe failures are the loud failure mode: if the ingress ipBlock is wrong the
# kubelet cannot reach /health, every replica goes NotReady, and the Deployment
# rolls itself into an outage. Watch for READY dropping below 2/2.
kubectl -n cluster-control get pods -w

# The quiet failure mode: pods stay Ready but the sidecar cannot reach the
# apiserver, so the UI loads and every panel is empty.
kubectl -n cluster-control logs -l component=ui -c kube-proxy-api --tail=20
curl -sS -o /dev/null -w '%{http_code}\n' http://192.168.2.201:30379/
curl -sS -o /dev/null -w '%{http_code}\n' http://192.168.2.201:30379/api/v1/nodes
```

A `000`/hang on the second curl with pods Ready means egress to the apiserver is
blocked — the `cluster-control-allow-apiserver-egress` ipBlock does not match.
A hang on the first means ingress is blocked — `cluster-control-allow-ui-ingress`
does not match. Distinguish a policy drop from a dead endpoint by the shape of
the failure: a drop **hangs until timeout**, a missing endpoint returns
**connection refused** immediately. Reading a refusal as a drop (or the reverse)
is how a previous task drew a right conclusion from a wrong measurement.

### Rollback

Each half is independent, and the NetworkPolicy half is the one likely to need
it. Deleting a NetworkPolicy takes effect immediately; no pod restart is needed.

```bash
# NetworkPolicy only -- restores unrestricted pod networking, keeps the RBAC cuts.
kubectl delete -f deploy/kubernetes/containment/networkpolicy-cluster-control.yaml

# ...or, if the file is not to hand and the UI is dark:
kubectl -n cluster-control delete netpol -l app.kubernetes.io/part-of=cluster-control-containment

# RBAC -- restore the ClusterRoles captured in pre-flight, then drop the
# namespaced Roles (harmless if left, but they are dead weight once the
# cluster-wide pods/proxy grant is back).
kubectl apply -f /tmp/rb-viewer.yaml -f /tmp/rb-operator.yaml
kubectl delete -f deploy/kubernetes/containment/rbac-integration-proxy.yaml

# Full revert straight from git, if the working tree has moved on:
git show 82163d1:deploy/kubernetes/base/rbac.yaml | kubectl apply -f -
```

There is nothing to restart and no state to reconcile: RBAC is evaluated per
request and NetworkPolicy per packet, so both halves revert the moment the API
objects change.

### Known cost, and the two ways this can surprise you later

1. **The Restart button on the Applications screen now returns 403.** Cutting
   cluster-wide `patch` on Deployments/StatefulSets is what removes the path to
   node root, and RBAC cannot allow "patch the restartedAt annotation" without
   also allowing "patch in a privileged pod template". `kubectl rollout restart`
   still works. To trade it back deliberately, see the commented block in
   `deploy/kubernetes/base/rbac.yaml`; to earn it back safely, enforce
   PodSecurity `baseline` on every namespace first.
2. **Repointing an integration on the Settings screen can 403.** The pod-proxy
   grant is now per namespace (`monitoring`, `inference`, `gpu-operator`,
   `falco`, `tetragon`, `alluxio`). Moving, say, Alertmanager to another
   namespace in the `cluster-control-config` ConfigMap needs a matching Role and
   RoleBinding.
3. **Adding a node invalidates the ingress ipBlock** for that node — the UI goes
   dark from it and only from it. Re-run pre-flight step 1 and add the new
   node's two /32s. Likewise, flipping `cluster-control-ui-lan` to
   `externalTrafficPolicy: Local` stops the masquerade, so the real LAN client
   address arrives and no rule matches.
