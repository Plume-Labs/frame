# Ceph Distributed Storage

This directory contains Kubernetes manifests for deploying Ceph storage using the Rook operator.

## Overview

Ceph provides distributed block, object, and file storage for the Kubernetes cluster. Key features:
- Self-healing and self-managing
- No single point of failure
- Scales horizontally
- CRUSH algorithm for data placement
- Multiple storage types (RBD, CephFS, RGW)

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Ceph Cluster                         │
├─────────────────────────────────────────────────────────┤
│                                                           │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐           │
│  │   MON-01  │  │   MON-02  │  │   MON-03  │           │
│  │  Monitor  │  │  Monitor  │  │  Monitor  │           │
│  └───────────┘  └───────────┘  └───────────┘           │
│                                                           │
│  ┌───────────┐  ┌───────────┐                           │
│  │   MGR-01  │  │   MGR-02  │                           │
│  │  Manager  │  │  Manager  │                           │
│  └───────────┘  └───────────┘                           │
│                                                           │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐           │
│  │  OSD-01   │  │  OSD-02   │  │  OSD-03   │           │
│  │ /dev/nvme │  │ /dev/nvme │  │ /dev/nvme │  ...      │
│  └───────────┘  └───────────┘  └───────────┘           │
│                                                           │
└─────────────────────────────────────────────────────────┘
        │                  │                  │
        └──────────────────┴──────────────────┘
                    192.168.2.0/24
                    (Ceph Network)
```

## Components

### Monitors (MON)
- Maintain cluster state and membership
- Quorum-based decision making
- Typically 3 or 5 monitors for HA

### Managers (MGR)
- Runtime metrics and management
- Dashboard and REST API
- Plugin modules (pg_autoscaler, prometheus)

### OSDs (Object Storage Daemons)
- Store actual data
- One OSD per physical disk
- Handles replication and recovery

### MDS (Metadata Servers)
- Required only for CephFS
- Manages filesystem metadata

## Storage Classes

### ceph-rbd (Block Storage)
- RWO (ReadWriteOnce) volumes
- Best for databases, VMs
- High performance
- Thin provisioning

### cephfs (Filesystem)
- RWX (ReadWriteMany) volumes
- Best for shared data
- POSIX-compliant
- Good for AI/ML datasets

## Deployment

### Prerequisites

1. Install Rook operator (pinned to a release, not `master`):
```bash
ROOK=https://raw.githubusercontent.com/rook/rook/release-1.20/deploy/examples
kubectl create namespace rook-ceph
kubectl apply -f $ROOK/crds.yaml --server-side
kubectl apply -f $ROOK/common.yaml
# Rook 1.20 uses the ceph-csi-operator, whose csi.ceph.io CRDs (CephConnection)
# are NOT in crds.yaml — without them the CephCluster reconcile fails at
# "no matches for kind CephConnection". Install them too:
kubectl apply --server-side -f $ROOK/csi-operator.yaml
kubectl apply -f $ROOK/operator.yaml
```

> After a Ceph major upgrade, finalize with
> `kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd require-osd-release tentacle`
> or the cluster stays HEALTH_WARN with OSD_UPGRADE_FINISHED.

2. Verify operator is running:
```bash
kubectl -n rook-ceph get pod
```

### Deploy Ceph Cluster

```bash
kubectl apply -k deploy/ceph/
```

This deploys:
- CephCluster with 3 monitors
- Block pool with 3x replication
- CephFS with metadata pool
- Storage classes for RBD and CephFS
- Ceph toolbox for debugging

### Verify Cluster

```bash
kubectl -n rook-ceph get cephcluster
kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph status
```

Expected output:
```
cluster:
  id:     <cluster-id>
  health: HEALTH_OK

services:
  mon: 3 daemons, quorum a,b,c
  mgr: a(active), standbys: b
  osd: 8 osds: 8 up, 8 in

data:
  pools:   2 pools, 64 pgs
  objects: 0 objects, 0 B
  usage:   8 GiB used, 7.2 TiB / 7.2 TiB avail
```

## Usage

### Create a PVC

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-data
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: ceph-rbd
  resources:
    requests:
      storage: 10Gi
```

### Use in Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
spec:
  containers:
    - name: app
      image: nginx
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: my-data
```

## Performance Tuning

### OSD Configuration

In `cluster.yaml`, tune OSD resources:

```yaml
resources:
  osd:
    limits:
      cpu: "4"
      memory: "8Gi"
    requests:
      cpu: "2"
      memory: "4Gi"
```

### Network Separation

Use separate networks for:
- Public network (client traffic): 192.168.1.0/24
- Cluster network (replication): 192.168.2.0/24

Configure in `cluster.yaml`:

```yaml
network:
  provider: host
  selectors:
    public: 192.168.1.0/24
    cluster: 192.168.2.0/24
```

### CRUSH Rules

Create custom CRUSH rules for data placement:

```bash
ceph osd crush rule create-replicated nvme-rule default host nvme
```

## Monitoring

### Prometheus Integration

Ceph metrics are automatically exposed at:
- MGR: `rook-ceph-mgr.rook-ceph.svc:9283`

Prometheus config includes Ceph scrape target.

### Dashboard

Access Ceph dashboard:

```bash
kubectl -n rook-ceph get service rook-ceph-mgr-dashboard
kubectl -n rook-ceph port-forward svc/rook-ceph-mgr-dashboard 8443:8443
```

Get admin password:

```bash
kubectl -n rook-ceph get secret rook-ceph-dashboard-password -o jsonpath="{.data.password}" | base64 -d
```

Visit: https://localhost:8443

## Troubleshooting

### Check OSD Status

```bash
kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph osd tree
```

### Check PG Status

```bash
kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph pg stat
```

### OSD Not Starting

Check device preparation:
```bash
kubectl -n rook-ceph logs -l app=rook-ceph-osd-prepare
```

### Cluster Stuck

Check monitor logs:
```bash
kubectl -n rook-ceph logs -l app=rook-ceph-mon
```

## Disaster Recovery

### Backup Cluster Configuration

```bash
kubectl -n rook-ceph get cephcluster -o yaml > cephcluster-backup.yaml
kubectl -n rook-ceph get cephblockpool -o yaml > pools-backup.yaml
```

### Export PVC Data

Use Velero or similar backup tool to backup PVCs.

## Maintenance

### Add OSD

Update `cluster.yaml` with new node and devices, then apply:

```bash
kubectl apply -k deploy/ceph/
```

### Remove OSD

```bash
kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph osd out <osd-id>
kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph osd purge <osd-id> --yes-i-really-mean-it
```

### Upgrade Ceph

Update ceph version in `cluster.yaml`:

```yaml
cephVersion:
  image: quay.io/ceph/ceph:v20.2.2
```

Apply and Rook will perform rolling upgrade.
