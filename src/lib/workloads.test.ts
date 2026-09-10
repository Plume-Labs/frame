/// <reference types="vite/client" />
import { describe, it, expect } from 'vitest'
import {
  OPERABLE_NAMESPACES,
  buildWorkloadTree,
  canEditManifest,
  canOperateWorkloads,
  controllerKey,
  controllerKeyForPod,
  isInfrastructureNamespace,
  ownershipWarning,
  type OwnerRef,
  type WorkloadController,
  type WorkloadPod,
} from './workloads'
// The manifest that decides where `baseline` Pod Security is enforced, and so
// where restart and scale are granted at all. Read as text rather than
// re-typed: see the test at the bottom of this file.
import podSecuritySource from '../../deploy/kubernetes/pod-security/namespaces.yaml?raw'

function pod(over: Partial<WorkloadPod> = {}): WorkloadPod {
  return {
    name: 'p',
    namespace: 'neura',
    phase: 'Running',
    nodeName: 'w2',
    restarts: 0,
    containers: ['api'],
    ...over,
  }
}

function controller(over: Partial<WorkloadController> = {}): WorkloadController {
  return {
    kind: 'Deployment',
    name: 'api',
    namespace: 'neura',
    desiredReplicas: 2,
    readyReplicas: 2,
    scalable: true,
    ...over,
  }
}

describe('isInfrastructureNamespace', () => {
  it('folds the namespaces this cluster runs its own plumbing in', () => {
    for (const ns of ['kube-system', 'rook-ceph', 'monitoring', 'cluster-control', 'frame-system']) {
      expect(isInfrastructureNamespace(ns)).toBe(true)
    }
  })

  // Added 2026-09-10, after the screen was opened against the real cluster for
  // the first time and showed these five as application namespaces. Each is a
  // component the platform runs for itself, and none ends in `-system` or starts
  // with `kube-`, so neither shape rule catches them — only the literal list
  // does. A version of the list without them passes every other test here.
  it('folds the platform components the shape rules cannot see', () => {
    for (const ns of [
      'alluxio',
      'jupyterhub',
      'postgres-operator',
      'registry',
      'volcano-monitoring',
    ]) {
      expect(isInfrastructureNamespace(ns)).toBe(true)
    }
  })

  it('leaves application namespaces open', () => {
    for (const ns of ['neura', 'default', 'inference', 'sandbox']) {
      expect(isInfrastructureNamespace(ns)).toBe(false)
    }
  })

  // The two shape rules, tested with names that are NOT in the list — a test
  // using `kube-system` or `volcano-system` would pass with the rules deleted,
  // because the literal list already covers them.
  it('folds anything shaped like plumbing, not only the names it knows', () => {
    expect(isInfrastructureNamespace('kube-flannel')).toBe(true)
    expect(isInfrastructureNamespace('cilium-system')).toBe(true)
  })
})

describe('controllerKeyForPod', () => {
  const rs: ReadonlyMap<string, OwnerRef> = new Map([
    ['neura/api-7d9f8', { kind: 'Deployment', name: 'api' }],
  ])

  // The case the whole ReplicaSet read exists for. A Deployment's pods are
  // owned by a ReplicaSet, never by the Deployment: resolve only the direct
  // owner and every Deployment in the tree shows zero pods while a phantom
  // "ReplicaSet api-7d9f8" holds them all.
  it('walks a pod through its ReplicaSet to the Deployment', () => {
    const p = pod({ name: 'api-7d9f8-x1', owner: { kind: 'ReplicaSet', name: 'api-7d9f8' } })
    expect(controllerKeyForPod(p, rs)).toBe(controllerKey('neura', 'Deployment', 'api'))
  })

  it('takes a StatefulSet, DaemonSet or Job owner directly', () => {
    expect(controllerKeyForPod(pod({ owner: { kind: 'StatefulSet', name: 'pg' } }), rs))
      .toBe(controllerKey('neura', 'StatefulSet', 'pg'))
    expect(controllerKeyForPod(pod({ owner: { kind: 'DaemonSet', name: 'agent' } }), rs))
      .toBe(controllerKey('neura', 'DaemonSet', 'agent'))
    expect(controllerKeyForPod(pod({ owner: { kind: 'Job', name: 'migrate' } }), rs))
      .toBe(controllerKey('neura', 'Job', 'migrate'))
  })

  it('reports no controller for a bare pod', () => {
    expect(controllerKeyForPod(pod(), rs)).toBeUndefined()
  })

  // A ReplicaSet the console never read — created by something outside the five
  // kinds, or RBAC-hidden. Guessing a Deployment name by stripping the trailing
  // hash would attach the pod to a controller that may not exist.
  it('reports no controller for a ReplicaSet it has not seen', () => {
    const p = pod({ owner: { kind: 'ReplicaSet', name: 'mystery-abc12' } })
    expect(controllerKeyForPod(p, rs)).toBeUndefined()
  })
})

describe('buildWorkloadTree', () => {
  const rs: ReadonlyMap<string, OwnerRef> = new Map([
    ['neura/api-7d9f8', { kind: 'Deployment', name: 'api' }],
  ])

  it('groups pods under their controller and keeps bare pods aside', () => {
    const tree = buildWorkloadTree({
      controllers: [controller()],
      pods: [
        pod({ name: 'api-7d9f8-x1', owner: { kind: 'ReplicaSet', name: 'api-7d9f8' } }),
        pod({ name: 'api-7d9f8-x2', owner: { kind: 'ReplicaSet', name: 'api-7d9f8' } }),
        pod({ name: 'debug-shell' }),
      ],
      replicaSetOwners: rs,
    })
    expect(tree).toHaveLength(1)
    expect(tree[0].namespace).toBe('neura')
    expect(tree[0].controllers).toHaveLength(1)
    expect(tree[0].controllers[0].pods.map((p) => p.name)).toEqual(['api-7d9f8-x1', 'api-7d9f8-x2'])
    expect(tree[0].barePods.map((p) => p.name)).toEqual(['debug-shell'])
    expect(tree[0].podCount).toBe(3)
  })

  // A namespace that holds only bare pods must still appear. Building the tree
  // from the controller list alone would drop it, and a pod nobody controls is
  // exactly the pod a person is most likely to be looking for.
  it('keeps a namespace that has pods but no controller', () => {
    const tree = buildWorkloadTree({
      controllers: [],
      pods: [pod({ namespace: 'sandbox', name: 'scratch' })],
      replicaSetOwners: new Map(),
    })
    expect(tree.map((n) => n.namespace)).toEqual(['sandbox'])
    expect(tree[0].barePods).toHaveLength(1)
  })

  // Applications first, plumbing last, each alphabetical — so opening the
  // screen puts what a person came for at the top without scrolling past
  // kube-system.
  it('sorts application namespaces before infrastructure ones', () => {
    const tree = buildWorkloadTree({
      controllers: [
        controller({ namespace: 'rook-ceph', name: 'mgr' }),
        controller({ namespace: 'neura', name: 'api' }),
        controller({ namespace: 'kube-system', name: 'coredns' }),
        controller({ namespace: 'inference', name: 'llamacpp' }),
      ],
      pods: [],
      replicaSetOwners: new Map(),
    })
    expect(tree.map((n) => n.namespace)).toEqual(['inference', 'neura', 'kube-system', 'rook-ceph'])
    expect(tree.map((n) => n.infrastructure)).toEqual([false, false, true, true])
  })
})

interface PodSecurityNamespaceDoc {
  name: string
  labels: Record<string, string>
}

// Splits the manifest into its per-Namespace documents and pulls out each
// one's name and `pod-security.kubernetes.io/*` labels — not just any
// `name:` line under `metadata`, which is what the drift guard below used to
// do and which cannot tell an enforced namespace from one carrying
// `warn`/`audit` only (the two-phase rollout's own documented first step, see
// namespaces.yaml's header). Whole-branch review Important 4: a namespace
// added mid-rollout, before its `enforce` label landed, would have passed
// silently and been offered a Restart button that 403s.
function parsePodSecurityNamespaces(source: string): PodSecurityNamespaceDoc[] {
  return source
    .split(/\n---\n/)
    .map((doc) => {
      const name = doc.match(/^ {2}name: (\S+)$/m)?.[1]
      if (!name) return undefined
      const labels: Record<string, string> = {}
      for (const m of doc.matchAll(/^ {4}(pod-security\.kubernetes\.io\/\S+): (\S+)$/gm)) {
        labels[m[1]] = m[2]
      }
      return { name, labels }
    })
    .filter((d): d is PodSecurityNamespaceDoc => d !== undefined)
}

describe('OPERABLE_NAMESPACES', () => {
  const namespaceDocs = parsePodSecurityNamespaces(podSecuritySource)
  const enforcedFromManifest = namespaceDocs
    .filter((d) => d.labels['pod-security.kubernetes.io/enforce'] === 'baseline')
    .map((d) => d.name)

  // The drift guard, and the reason this list is not just
  // `!isInfrastructureNamespace(ns)`.
  //
  // Restart and scale are granted by a RoleBinding in each namespace that
  // carries `pod-security.kubernetes.io/enforce: baseline`
  // (deploy/kubernetes/base/rbac-workload-operator.yaml). A namespace that is
  // neither enforced nor obviously infrastructure — `sandbox`, say — would pass
  // a `!isInfrastructureNamespace` check, be offered a Restart button, and 403.
  // The only honest predicate is the enforced list itself, and the only way to
  // keep a second copy of it honest is to compare it to the first.
  //
  // A broken version — one namespace added to the manifest and not here, or
  // removed here and not there — prints the two sorted arrays side by side. So
  // does a namespace carrying `warn`/`audit` but not yet `enforce`: it now has
  // to be excluded from `enforcedFromManifest` above, and the assertion below
  // fails until either the label lands or OPERABLE_NAMESPACES drops it.
  it('is exactly the set of namespaces that carry pod-security.kubernetes.io/enforce: baseline', () => {
    expect(namespaceDocs.length).toBeGreaterThan(0)
    expect(enforcedFromManifest.length).toBeGreaterThan(0)
    expect([...enforcedFromManifest].sort()).toEqual([...OPERABLE_NAMESPACES].sort())
  })

  // The other half of Important 4: `enforce: baseline` with no version pin
  // (or `enforce-version: latest`) would pass the assertion above and still
  // let a cluster upgrade silently change what baseline admits, in exactly
  // the namespaces this grant depends on it not changing.
  it('pins enforce-version for every operable namespace', () => {
    for (const ns of OPERABLE_NAMESPACES) {
      const doc = namespaceDocs.find((d) => d.name === ns)
      expect(doc, `${ns} is in OPERABLE_NAMESPACES but not in the manifest`).toBeDefined()
      expect(doc?.labels['pod-security.kubernetes.io/enforce']).toBe('baseline')
      expect(doc?.labels['pod-security.kubernetes.io/enforce-version']).toMatch(/^v\d+\.\d+$/)
    }
  })

  it('answers for a namespace on each side', () => {
    expect(canOperateWorkloads('neura')).toBe(true)
    expect(canOperateWorkloads('rook-ceph')).toBe(false)
    // Neither enforced nor infrastructure: the case a `!isInfrastructure`
    // predicate gets wrong, and the case that produces a button that 403s.
    expect(canOperateWorkloads('sandbox')).toBe(false)
  })

  // Not a tautology — it is the invariant that keeps the two lists from ever
  // being satisfiable at once. A namespace cannot both be folded as
  // infrastructure and carry the grant; if one ever does, one of the two
  // decisions is wrong and this says which pair to look at.
  it('never overlaps the folded infrastructure list', () => {
    for (const ns of OPERABLE_NAMESPACES) {
      expect(isInfrastructureNamespace(ns)).toBe(false)
    }
  })
})

describe('canEditManifest', () => {
  // Whole-branch review Important 3: YamlTab.tsx used to gate its Apply
  // button on `canOperateWorkloads(namespace)` alone — the operator grant's
  // predicate — while the manifest editor's actual grant,
  // cluster-control-workload-admin, is bound to frame:admins only. This case
  // is the bug: an operator (admin: false) standing in an operable namespace
  // would have seen Apply enabled and 403 after typing an edit. A version
  // that reverted to `namespace-only` would pass every other case here and
  // still fail this one.
  it('requires the admin tier even in an operable namespace', () => {
    expect(canEditManifest(false, 'neura')).toBe(false)
    expect(canEditManifest(true, 'neura')).toBe(true)
  })

  it('requires an operable namespace even for an admin', () => {
    expect(canEditManifest(true, 'rook-ceph')).toBe(false)
  })

  it('is false when neither gate is open', () => {
    expect(canEditManifest(false, 'rook-ceph')).toBe(false)
  })
})

describe('ownershipWarning', () => {
  it('names Argo CD from its tracking annotation', () => {
    const w = ownershipWarning({ annotations: { 'argocd.argoproj.io/tracking-id': 'neura:apps/Deployment:neura/api' } })
    expect(w).toContain('Argo CD')
    expect(w).toContain('reverted')
  })

  it('names Helm and its release', () => {
    const w = ownershipWarning({
      labels: { 'app.kubernetes.io/managed-by': 'Helm' },
      annotations: { 'meta.helm.sh/release-name': 'neura' },
    })
    expect(w).toContain('Helm')
    expect(w).toContain('neura')
  })

  // `app.kubernetes.io/instance` is a plain recommended label that half the
  // charts on this cluster set. Treating it as proof of Argo ownership would
  // put a warning on nearly every object, and a warning that is always there
  // is a warning nobody reads — including the one time it is true.
  it('says nothing for an object that merely carries the recommended labels', () => {
    expect(ownershipWarning({
      labels: { 'app.kubernetes.io/instance': 'neura', 'app.kubernetes.io/name': 'api' },
    })).toBeUndefined()
    expect(ownershipWarning({})).toBeUndefined()
  })
})
