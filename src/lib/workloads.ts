/**
 * What the Workloads screen decides, minus the rendering.
 *
 * It lives here rather than in the component because this is the only place a
 * test can reach: vitest runs `environment: 'node'` and only includes
 * `.test.ts` files, so a `.tsx` spec sits in the repository looking like
 * coverage and never runs. Everything below is a pure function over plain
 * data, and everything below is tested.
 */

export type WorkloadKind = 'Deployment' | 'StatefulSet' | 'DaemonSet' | 'Job'

/** The kinds the YAML editor is bounded to — the tree's four, plus the pod. */
export type EditableKind = WorkloadKind | 'Pod'

export interface OwnerRef {
  kind: string
  name: string
}

export interface WorkloadController {
  kind: WorkloadKind
  name: string
  namespace: string
  desiredReplicas: number
  readyReplicas: number
  /**
   * Deployment and StatefulSet only. A DaemonSet's replica count is the number
   * of matching nodes and a Job's is its parallelism; neither has a `scale`
   * subresource, so offering the button would be offering a 404.
   */
  scalable: boolean
}

export interface WorkloadPod {
  name: string
  namespace: string
  phase: string
  nodeName: string
  restarts: number
  containers: string[]
  createdAt?: string
  /** The pod's controller ownerReference, if it has one. */
  owner?: OwnerRef
}

export interface ControllerNode {
  controller: WorkloadController
  pods: WorkloadPod[]
}

export interface NamespaceNode {
  namespace: string
  infrastructure: boolean
  controllers: ControllerNode[]
  /** Pods with no controller among the kinds the tree shows. */
  barePods: WorkloadPod[]
  /** Every pod in the namespace, controlled or not — the collapsed-row count. */
  podCount: number
}

/**
 * Namespaces the tree folds behind a switch.
 *
 * Presentation, not authorization: RBAC remains the only thing that decides
 * what anyone can see, and this only decides what is open when the screen
 * loads. It lives here rather than in the component so it is testable and can
 * be amended without touching rendering code.
 */
export const INFRASTRUCTURE_NAMESPACES: readonly string[] = [
  'alluxio',
  'argo',
  'argocd',
  'cert-manager',
  'cluster-control',
  'falco',
  'frame-system',
  'gpu-operator',
  'ingress-nginx',
  'jupyterhub',
  'local-path-storage',
  'metallb-system',
  'monitoring',
  'node-feature-discovery',
  'postgres-operator',
  'registry',
  'rook-ceph',
  'tetragon',
  'velero',
  'volcano-monitoring',
]

/**
 * True for a namespace the cluster runs itself in.
 *
 * The two shape rules alongside the list are what make it hold up on a cluster
 * nobody enumerated: `kube-*` is reserved by Kubernetes, and `*-system` is the
 * convention every operator that ships its own namespace follows. A private
 * application namespace called `something-system` would be folded by mistake,
 * which costs one click on the switch.
 */
export function isInfrastructureNamespace(ns: string): boolean {
  if (INFRASTRUCTURE_NAMESPACES.includes(ns)) return true
  return ns.startsWith('kube-') || ns.endsWith('-system')
}

/**
 * Namespaces where restart and scale are actually granted.
 *
 * Mirrors `deploy/kubernetes/pod-security/namespaces.yaml` — the namespaces
 * carrying `pod-security.kubernetes.io/enforce: baseline`, which is exactly
 * where `deploy/kubernetes/base/rbac-workload-operator.yaml` binds the grant.
 * The two are kept in step by a test in `workloads.test.ts` that parses the
 * manifest, because a second hand-maintained list drifts.
 *
 * This is deliberately **not** `!isInfrastructureNamespace(ns)`. That predicate
 * decides what the tree folds and is a heuristic with two shape rules; this one
 * decides whether a button is offered, and offering one where the grant does
 * not reach produces a 403 the person cannot do anything about. A namespace
 * that is neither enforced nor recognisably infrastructure — a `sandbox`, a
 * one-off — must fall on the "no button" side, and only an explicit list does
 * that.
 */
export const OPERABLE_NAMESPACES: readonly string[] = [
  'default',
  'inference',
  'neura',
  'neura-batch',
  'neura-database',
  'neura-inference',
  'neura-training',
]

/**
 * True where the console may restart or scale a workload.
 *
 * False everywhere else, including namespaces the tree happily shows: reading
 * is cluster-wide, operating is not, and the screen says which is which rather
 * than offering a button that returns 403.
 */
export function canOperateWorkloads(ns: string): boolean {
  return OPERABLE_NAMESPACES.includes(ns)
}

/**
 * True where the YAML editor's Apply button may actually succeed.
 *
 * Two independent gates, both required. `admin` is the tier
 * `cluster-control-workload-admin` is bound to — `frame:admins` only, per
 * `test/manifests/rbac_workload_operator_test.go:53` — and
 * `canOperateWorkloads(namespace)` is the namespace that grant's RoleBindings
 * actually reach. Whole-branch review Important 3: `YamlTab.tsx` used to gate
 * Apply on `canOperateWorkloads(namespace)` alone, which is the *operator*
 * grant's predicate (restart/scale), not the admin grant's — so an operator
 * standing in an operable namespace saw an enabled Apply button that 403s
 * after they had already typed an edit.
 *
 * Lives here rather than inline in the component for the same reason
 * `canOperateWorkloads` does: vitest never runs a `.tsx` spec (see this
 * file's header), so a decision embedded in JSX cannot be pinned by a test.
 */
export function canEditManifest(admin: boolean, namespace: string): boolean {
  return admin && canOperateWorkloads(namespace)
}

export function controllerKey(namespace: string, kind: WorkloadKind, name: string): string {
  return `${namespace}/${kind}/${name}`
}

/**
 * The tree node a pod belongs under, or undefined for a pod nothing in the
 * tree controls.
 *
 * A Deployment's pods name a **ReplicaSet** as their owner, never the
 * Deployment, so the walk needs the ReplicaSet's own ownerReferences —
 * `replicaSetOwners`, keyed `namespace/name`. Stripping the pod-template hash
 * off the ReplicaSet's name would avoid that read and would be a guess: it
 * misfiles the pods of any Deployment whose own name ends in something
 * hash-shaped, and invents a controller for a ReplicaSet created by something
 * else.
 */
export function controllerKeyForPod(
  pod: WorkloadPod,
  replicaSetOwners: ReadonlyMap<string, OwnerRef>,
): string | undefined {
  const owner = pod.owner
  if (!owner) return undefined
  if (owner.kind === 'ReplicaSet') {
    const parent = replicaSetOwners.get(`${pod.namespace}/${owner.name}`)
    if (!parent || parent.kind !== 'Deployment') return undefined
    return controllerKey(pod.namespace, 'Deployment', parent.name)
  }
  if (owner.kind === 'StatefulSet' || owner.kind === 'DaemonSet' || owner.kind === 'Job') {
    return controllerKey(pod.namespace, owner.kind, owner.name)
  }
  return undefined
}

/**
 * namespace → controller → pods, with everything the tree could not place kept
 * visible rather than dropped.
 *
 * Built from both lists, not from the controllers alone: a namespace holding
 * only bare pods must still appear, because a pod nothing controls is exactly
 * the pod someone is looking for.
 */
export function buildWorkloadTree(input: {
  controllers: WorkloadController[]
  pods: WorkloadPod[]
  replicaSetOwners: ReadonlyMap<string, OwnerRef>
}): NamespaceNode[] {
  const { controllers, pods, replicaSetOwners } = input

  const byKey = new Map<string, ControllerNode>()
  const namespaces = new Map<string, NamespaceNode>()

  const nodeFor = (namespace: string): NamespaceNode => {
    let n = namespaces.get(namespace)
    if (!n) {
      n = {
        namespace,
        infrastructure: isInfrastructureNamespace(namespace),
        controllers: [],
        barePods: [],
        podCount: 0,
      }
      namespaces.set(namespace, n)
    }
    return n
  }

  for (const c of controllers) {
    const node: ControllerNode = { controller: c, pods: [] }
    byKey.set(controllerKey(c.namespace, c.kind, c.name), node)
    nodeFor(c.namespace).controllers.push(node)
  }

  for (const p of pods) {
    const ns = nodeFor(p.namespace)
    ns.podCount += 1
    const key = controllerKeyForPod(p, replicaSetOwners)
    const owner = key ? byKey.get(key) : undefined
    if (owner) owner.pods.push(p)
    else ns.barePods.push(p)
  }

  for (const ns of namespaces.values()) {
    ns.controllers.sort(
      (a, b) =>
        a.controller.kind.localeCompare(b.controller.kind) ||
        a.controller.name.localeCompare(b.controller.name),
    )
    for (const c of ns.controllers) c.pods.sort((a, b) => a.name.localeCompare(b.name))
    ns.barePods.sort((a, b) => a.name.localeCompare(b.name))
  }

  // Applications first, plumbing last, each alphabetical: what someone came
  // for is at the top without scrolling past kube-system.
  return [...namespaces.values()].sort(
    (a, b) =>
      Number(a.infrastructure) - Number(b.infrastructure) ||
      a.namespace.localeCompare(b.namespace),
  )
}

/**
 * A sentence to show above the editor when something else owns this object,
 * or undefined when nothing does.
 *
 * It does not forbid the edit. It prevents three hours spent debugging a
 * change that quietly disappeared at the next sync.
 *
 * Only markers that mean *ownership* count. `app.kubernetes.io/instance` is a
 * plain recommended label that most charts set and that proves nothing; using
 * it would put this warning on nearly every object on the cluster, and a
 * warning that is always there is one nobody reads on the day it is true.
 */
export function ownershipWarning(meta: {
  labels?: Record<string, string>
  annotations?: Record<string, string>
}): string | undefined {
  const labels = meta.labels ?? {}
  const annotations = meta.annotations ?? {}

  const argo =
    annotations['argocd.argoproj.io/tracking-id'] ?? labels['argocd.argoproj.io/instance']
  if (argo) {
    return `Argo CD manages this object (${argo}). Your edit will be reverted at the next sync.`
  }
  if (labels['app.kubernetes.io/managed-by'] === 'Helm') {
    const release = annotations['meta.helm.sh/release-name'] ?? 'an unnamed release'
    return `Helm manages this object (release ${release}). Your edit will be reverted at the next upgrade.`
  }
  return undefined
}
