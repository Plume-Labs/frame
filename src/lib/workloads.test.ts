/// <reference types="vite/client" />
import { describe, it, expect } from 'vitest'
import {
  OPERABLE_NAMESPACES,
  buildWorkloadTree,
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

describe('OPERABLE_NAMESPACES', () => {
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
  // removed here and not there — prints the two sorted arrays side by side.
  it('is exactly the set of namespaces where baseline is enforced', () => {
    const fromManifest = [...podSecuritySource.matchAll(/^ {2}name: (\S+)$/gm)].map((m) => m[1])
    expect(fromManifest.length).toBeGreaterThan(0)
    expect([...fromManifest].sort()).toEqual([...OPERABLE_NAMESPACES].sort())
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
