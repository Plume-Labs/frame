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
kubectl apply --dry-run=server -f config/samples/frame_v1alpha1_framejob.yaml
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
