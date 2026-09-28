/**
 * Operating the model servers from the console — lot 1 of the inference spec
 * (docs/specs/2026-09-28-gestion-inference.md, §2.1 option B, §2.2, §5).
 *
 * The servers are plain Deployments applied by hand or by a script; Frame
 * does not own them. Lot 1 only ever writes two things into them, both with
 * grants the console's operators already hold (`cluster-control-workload-operator`,
 * bound per namespace): the pod template's `restartedAt` annotation — the same
 * one `kubectl rollout restart` sets — and `/scale`. Nothing that changes an
 * argument, a model, or a resource belongs here: that waits for the servers to
 * become `FrameService`s (lot 4).
 *
 * Every decision lives in this file rather than in the panel's JSX because
 * vitest runs no `.tsx` spec here (see `src/lib/workloads.ts`), so a rule
 * embedded in a component could not be pinned by a test.
 */
import type { FrameConfig, Integration } from './frame-config'
import type { TaskRecord } from './frame-sdk'

/** The integrations the panel may act on. Anything absent here gets no button. */
export type InferenceServiceKey = 'llamacpp' | 'llamacppVision' | 'tei'

export interface InferenceServiceDef {
  key: InferenceServiceKey & keyof FrameConfig['integrations']
  label: string
  role: 'chat' | 'vision' | 'embeddings'
  /** Holds a GPU: may run 0 or 1 replica, never more (one card). */
  gpu: boolean
  engine: 'llamacpp' | 'tei'
  /** Typical model load when nothing better has been measured, in seconds. */
  typicalLoadSeconds: number
}

/**
 * The model servers, and only them. The gateway and the endpoint picker (EPP)
 * are deliberately not listed: their fields belong to other managers (the
 * gateway controller's server-side apply, Helm), so a console write there would
 * be undone by — or fight — whoever owns them. Keeping them off this list is
 * the first exclusion; {@link foreignManager} is the second, for a
 * configuration that points one of these three at such an object anyway.
 */
export const INFERENCE_SERVICES: readonly InferenceServiceDef[] = [
  { key: 'llamacpp', label: 'Chat LLM', role: 'chat', gpu: true, engine: 'llamacpp', typicalLoadSeconds: 360 },
  {
    key: 'llamacppVision',
    label: 'Vision',
    role: 'vision',
    gpu: false,
    engine: 'llamacpp',
    typicalLoadSeconds: 60,
  },
  { key: 'tei', label: 'Embeddings (TEI)', role: 'embeddings', gpu: false, engine: 'tei', typicalLoadSeconds: 180 },
]

// ── Kubernetes shapes (only the fields read here) ────────────────────────────

export interface ManagedFieldsEntry {
  manager?: string
  operation?: string
  subresource?: string
  fieldsV1?: Record<string, unknown>
}

export interface DeploymentLike {
  metadata: {
    name: string
    namespace?: string
    labels?: Record<string, string>
    annotations?: Record<string, string>
    ownerReferences?: Array<{ kind: string; name: string; controller?: boolean }>
    managedFields?: ManagedFieldsEntry[]
  }
  spec?: {
    replicas?: number
    selector?: { matchLabels?: Record<string, string>; matchExpressions?: unknown[] }
    template?: { metadata?: { annotations?: Record<string, string> }; spec?: { containers?: ContainerLike[] } }
  }
  status?: { replicas?: number; readyReplicas?: number; updatedReplicas?: number }
}

export interface ContainerLike {
  name: string
  image?: string
  command?: string[]
  args?: string[]
  env?: Array<{ name: string; value?: string; valueFrom?: unknown }>
}

export interface PodLike {
  metadata: { name: string; deletionTimestamp?: string }
  spec?: { nodeName?: string }
  status?: {
    phase?: string
    startTime?: string
    conditions?: Array<{ type: string; status: string; lastTransitionTime?: string }>
  }
}

export const RESTARTED_AT = 'kubectl.kubernetes.io/restartedAt'
const LAST_APPLIED = 'kubectl.kubernetes.io/last-applied-configuration'
const CLIENT_SIDE_APPLY = 'kubectl-client-side-apply'

// ── Selector → Deployment name ───────────────────────────────────────────────

/**
 * A plain equality selector (`app=foo`, `a=b,c==d`) as a label map, or null
 * for anything else. Set-based and inequality terms cannot be compared with a
 * Deployment's `matchLabels`, so they resolve to nothing rather than to a
 * guess.
 */
export function parseSelector(selector: string): Record<string, string> | null {
  const out: Record<string, string> = {}
  const terms = selector.split(',').map((t) => t.trim())
  if (terms.length === 0 || terms.every((t) => t === '')) return null
  for (const term of terms) {
    const m = /^([^=!\s(),]+)\s*==?\s*([^=!\s(),]*)$/.exec(term)
    if (!m) return null
    if (m[1] in out) return null
    out[m[1]] = m[2]
  }
  return out
}

function sameLabels(a: Record<string, string>, b: Record<string, string>): boolean {
  const ka = Object.keys(a)
  const kb = Object.keys(b)
  return ka.length === kb.length && ka.every((k) => Object.prototype.hasOwnProperty.call(b, k) && a[k] === b[k])
}

/**
 * Why Frame must not write into this Deployment, or undefined when it may.
 *
 * The gateway's Deployment is owned by its `Gateway` and written by the
 * gateway controller with server-side apply; the EPP's by Helm, also with
 * server-side apply. A console write there is either undone on the next
 * reconcile or steals a field from its owner. Each test below catches one of
 * those shapes, so a Deployment is refused if any one of them matches.
 */
export function foreignManager(d: DeploymentLike): string | undefined {
  const owner = d.metadata.ownerReferences?.find((o) => o.controller)
  if (owner) return `controlled by ${owner.kind} ${owner.name}`
  const labels = d.metadata.labels ?? {}
  if (labels['gateway.networking.k8s.io/gateway-name']) return 'a Gateway API gateway'
  if (labels['app.kubernetes.io/managed-by'] === 'Helm' || d.metadata.annotations?.['meta.helm.sh/release-name']) {
    return 'managed by Helm'
  }
  const ssa = d.metadata.managedFields?.find((f) => f.operation === 'Apply' && !f.subresource)
  if (ssa) return `fields held by server-side apply (${ssa.manager ?? 'unknown manager'})`
  return undefined
}

export type Resolution =
  | { ok: true; deployment: DeploymentLike }
  | { ok: false; reason: string }

/**
 * The Deployment behind an integration.
 *
 * `integration.deployment` wins when set. Otherwise the answer is the single
 * Deployment in the integration's namespace whose `matchLabels` equal the
 * configured selector exactly — equal, not "contains", so `app=llamacpp` never
 * lands on a `app=llamacpp-vision` Deployment. None or several: no button, and
 * the reason says how to fix it.
 */
export function resolveDeployment(i: Integration, deployments: DeploymentLike[]): Resolution {
  let found: DeploymentLike | undefined
  if (i.deployment) {
    found = deployments.find((d) => d.metadata.name === i.deployment)
    if (!found) return { ok: false, reason: `Deployment ${i.namespace}/${i.deployment} not found.` }
  } else {
    const want = parseSelector(i.selector)
    if (!want) {
      return { ok: false, reason: `Selector "${i.selector}" is not a plain equality selector; set its Deployment in Settings.` }
    }
    const matches = deployments.filter(
      (d) =>
        !(d.spec?.selector?.matchExpressions?.length) &&
        sameLabels(d.spec?.selector?.matchLabels ?? {}, want),
    )
    if (matches.length === 0) {
      return { ok: false, reason: `No Deployment in ${i.namespace} selects exactly "${i.selector}".` }
    }
    if (matches.length > 1) {
      return {
        ok: false,
        reason: `${matches.length} Deployments in ${i.namespace} select "${i.selector}"; set the one to operate in Settings.`,
      }
    }
    found = matches[0]
  }
  const foreign = foreignManager(found)
  if (foreign) return { ok: false, reason: `${found.metadata.name} is ${foreign}: Frame does not write into it.` }
  return { ok: true, deployment: found }
}

// ── State ────────────────────────────────────────────────────────────────────

export type PodState = 'stopped' | 'stopping' | 'starting' | 'loading' | 'ready'

export interface ServiceState {
  desired: number
  ready: number
  pod: PodState
  podName?: string
  node?: string
  podStartedAt?: string
  /** The template's `restartedAt`, i.e. the last console or `kubectl rollout restart`. */
  lastRestart?: string
  /** Seconds from the serving pod's start to Ready — the last load actually measured. */
  lastLoadSeconds?: number
  /**
   * `spec.replicas` is held by `kubectl-client-side-apply`: the next
   * `kubectl apply` of the manifest writes it back, undoing a stop.
   */
  replicasHeldByApply: boolean
  /** `spec.replicas` in the last applied manifest, when it records one. */
  lastAppliedReplicas?: number
  /** Container fields that differ from the last-applied manifest ({@link templateDrift}). */
  drift: string[]
}

function isReady(p: PodLike): boolean {
  return p.status?.conditions?.some((c) => c.type === 'Ready' && c.status === 'True') ?? false
}

function podState(p: PodLike): PodState {
  if (p.metadata.deletionTimestamp) return 'stopping'
  if (p.status?.phase === 'Running') return isReady(p) ? 'ready' : 'loading'
  return 'starting'
}

const RANK: Record<PodState, number> = { ready: 4, loading: 3, starting: 2, stopping: 1, stopped: 0 }

function lastAppliedObject(d: DeploymentLike): DeploymentLike | undefined {
  const raw = d.metadata.annotations?.[LAST_APPLIED]
  if (!raw) return undefined
  try {
    return JSON.parse(raw) as DeploymentLike
  } catch {
    return undefined
  }
}

function lastApplied(d: DeploymentLike): number | undefined {
  const r = lastAppliedObject(d)?.spec?.replicas
  return typeof r === 'number' ? r : undefined
}

function listDrift(path: string, live: string[] | undefined, applied: string[] | undefined): string[] {
  const a = live ?? []
  const b = applied ?? []
  const out: string[] = []
  for (let i = 0; i < Math.max(a.length, b.length); i++) if (a[i] !== b[i]) out.push(`${path}[${i}]`)
  return out
}

/**
 * The container fields where the live Deployment differs from its
 * last-applied manifest — each one a change the next `kubectl apply` reverts
 * (spec §3, "Dérive"). Read-only: the panel names them, it fixes nothing.
 *
 * Compared: image, command, args (by index) and env (by variable name), per
 * container matched by name, plus containers present on one side only. Not
 * the whole template: fields the apiserver defaults (`imagePullPolicy`,
 * `terminationMessagePath`…) are absent from a hand-written manifest and would
 * read as drift on every Deployment.
 */
export function templateDrift(d: DeploymentLike): string[] {
  const applied = lastAppliedObject(d)
  if (!applied) return []
  const live = d.spec?.template?.spec?.containers ?? []
  const want = applied.spec?.template?.spec?.containers ?? []
  const out: string[] = []
  for (const c of live) {
    const w = want.find((x) => x.name === c.name)
    const at = `containers[${c.name}]`
    if (!w) {
      out.push(at)
      continue
    }
    if ((c.image ?? '') !== (w.image ?? '')) out.push(`${at}.image`)
    out.push(...listDrift(`${at}.command`, c.command, w.command))
    out.push(...listDrift(`${at}.args`, c.args, w.args))
    const envLive = new Map((c.env ?? []).map((e) => [e.name, JSON.stringify([e.value ?? '', e.valueFrom ?? null])]))
    const envWant = new Map((w.env ?? []).map((e) => [e.name, JSON.stringify([e.value ?? '', e.valueFrom ?? null])]))
    for (const name of new Set([...envLive.keys(), ...envWant.keys()])) {
      if (envLive.get(name) !== envWant.get(name)) out.push(`${at}.env[${name}]`)
    }
  }
  for (const w of want) if (!live.some((c) => c.name === w.name)) out.push(`containers[${w.name}]`)
  return out
}

export function serviceState(d: DeploymentLike, pods: PodLike[]): ServiceState {
  const desired = d.spec?.replicas ?? 1
  const ranked = pods
    .filter((p) => p.status?.phase !== 'Succeeded' && p.status?.phase !== 'Failed')
    .map((p) => ({ p, s: podState(p) }))
    .sort((a, b) => RANK[b.s] - RANK[a.s])
  const best = ranked[0]
  let pod: PodState
  if (best) pod = best.s
  else pod = desired === 0 ? 'stopped' : 'starting'

  let lastLoadSeconds: number | undefined
  if (best?.s === 'ready') {
    const readyAt = best.p.status?.conditions?.find((c) => c.type === 'Ready')?.lastTransitionTime
    const start = best.p.status?.startTime
    if (readyAt && start) {
      const s = (Date.parse(readyAt) - Date.parse(start)) / 1000
      if (Number.isFinite(s) && s > 0) lastLoadSeconds = s
    }
  }

  const replicasHeldByApply =
    d.metadata.managedFields?.some(
      (f) =>
        f.manager === CLIENT_SIDE_APPLY &&
        !f.subresource &&
        !!(f.fieldsV1?.['f:spec'] as Record<string, unknown> | undefined)?.['f:replicas'],
    ) ?? false

  return {
    desired,
    ready: d.status?.readyReplicas ?? 0,
    pod,
    podName: best?.p.metadata.name,
    node: best?.p.spec?.nodeName,
    podStartedAt: best?.p.status?.startTime,
    lastRestart: d.spec?.template?.metadata?.annotations?.[RESTARTED_AT],
    lastLoadSeconds,
    replicasHeldByApply,
    lastAppliedReplicas: lastApplied(d),
    drift: templateDrift(d),
  }
}

/**
 * The warning the panel must always carry, and its sharper form when the
 * object itself shows the trap is armed (spec §2.1).
 */
export function applyWarning(
  s: Pick<ServiceState, 'desired' | 'replicasHeldByApply' | 'lastAppliedReplicas'> & { drift?: string[] },
): string {
  const base =
    'Stop only lasts until the next `kubectl apply` of this manifest or of its install script: that write sets replicas back and starts the server again, silently.'
  const drift = s.drift?.length
    ? ` The live object also differs from its last-applied manifest on ${s.drift.join(', ')}: the same apply reverts those too.`
    : ''
  if (s.desired === 0 && s.lastAppliedReplicas !== undefined && s.lastAppliedReplicas > 0) {
    return `Stopped here, but the applied manifest says replicas: ${s.lastAppliedReplicas}. ${base}${drift}`
  }
  if (s.replicasHeldByApply) return `replicas is held by kubectl-client-side-apply. ${base}${drift}`
  return base + drift
}

// ── Permissions ──────────────────────────────────────────────────────────────

export type Permission =
  | { state: 'checking' }
  | { state: 'allowed' }
  | { state: 'denied'; reason: string }
  /** The check itself failed for another reason; the action stays offered and a 403 is handled on click. */
  | { state: 'unknown'; reason: string }

export interface Permissions {
  /** `patch` on the Deployment — restart. */
  patch: Permission
  /** `patch` on `deployments/scale` — stop and start. */
  scale: Permission
}

/** A permission probe's error, read as a permission: a 403 is a denial, anything else is not an answer. */
export function permissionFromError(e: unknown, what: string, namespace: string): Permission {
  const status = (e as { statusCode?: number } | null)?.statusCode
  if (status === 403) {
    return { state: 'denied', reason: `Your role may not ${what} in ${namespace} (operators and admins only).` }
  }
  return { state: 'unknown', reason: e instanceof Error ? e.message : String(e) }
}

// ── Actions ──────────────────────────────────────────────────────────────────

export type ActionKind = 'restart' | 'stop' | 'start'

export interface ActionOffer {
  kind: ActionKind
  enabled: boolean
  /** Why the button is disabled, shown beside it. */
  why?: string
}

function gate(p: Permission): { enabled: boolean; why?: string } {
  switch (p.state) {
    case 'allowed':
      return { enabled: true }
    case 'unknown':
      return { enabled: true }
    case 'checking':
      return { enabled: false, why: 'Checking your permission…' }
    case 'denied':
      return { enabled: false, why: p.reason }
  }
}

/** Which buttons a service shows, and whether each is clickable. */
export function actionsFor(state: Pick<ServiceState, 'desired'>, perms: Permissions): ActionOffer[] {
  if (state.desired === 0) return [{ kind: 'start', ...gate(perms.scale) }]
  return [
    { kind: 'restart', ...gate(perms.patch) },
    { kind: 'stop', ...gate(perms.scale) },
  ]
}

/**
 * The replica count an action writes. Stop is 0, start is 1 — for a GPU
 * server 0 and 1 are the only counts that exist (one card, `Recreate`), and
 * lot 1 offers nothing else for the others either.
 */
export function replicasFor(def: InferenceServiceDef, action: 'stop' | 'start'): number {
  const n = action === 'stop' ? 0 : 1
  if (def.gpu && n !== 0 && n !== 1) throw new Error(`a GPU server runs 0 or 1 replica, not ${n}`)
  return n
}

// ── Confirmations ────────────────────────────────────────────────────────────

export interface Confirmation {
  title: string
  /** What the write does. */
  effect: string
  /** How long nothing answers, and who notices. */
  outage: string
  /** The name that must be typed back before the confirm button unlocks. */
  typeToConfirm?: string
  requireReason: boolean
  confirmLabel: string
}

export function formatDuration(seconds: number): string {
  const m = Math.round(seconds / 60)
  if (m >= 1) return `about ${m} min`
  return `about ${Math.max(1, Math.round(seconds))} s`
}

function loadText(def: InferenceServiceDef, measured?: number): string {
  return measured
    ? `${formatDuration(measured)} (last load measured)`
    : `${formatDuration(def.typicalLoadSeconds)} (typical)`
}

function consumers(def: InferenceServiceDef): string {
  switch (def.role) {
    case 'chat':
      return 'Every client of the chat LLM gets no answer'
    case 'vision':
      return 'Image understanding gets no answer'
    case 'embeddings':
      return 'Embeddings, and so search and indexing, get no answer'
  }
}

export function confirmationFor(
  action: ActionKind,
  def: InferenceServiceDef,
  target: { namespace: string; name: string },
  ctx: { lastLoadSeconds?: number; inFlight?: number } = {},
): Confirmation {
  const ref = `${target.namespace}/${target.name}`
  const load = loadText(def, ctx.lastLoadSeconds)
  switch (action) {
    case 'restart':
      return {
        title: `Restart ${def.label} (${ref})?`,
        effect:
          `Bumps the pod template's ${RESTARTED_AT} annotation, as \`kubectl rollout restart\` does. ` +
          (def.gpu
            ? 'The server runs one pod on one GPU, so the old pod stops before the new one starts.'
            : 'The Deployment replaces its pod following its own update strategy.') +
          (ctx.inFlight ? ` ${ctx.inFlight} request(s) in flight now will be cut.` : ''),
        outage: `${consumers(def)} while the model loads again from its cache: ${load}.`,
        requireReason: false,
        confirmLabel: 'Restart',
      }
    case 'stop':
      return {
        title: `Stop ${def.label} (${ref})?`,
        effect: `Scales the Deployment to 0 replicas: the pod is deleted${def.gpu ? ' and its GPU freed' : ''}.`,
        outage: `${consumers(def)} until someone starts it again, then for one more load: ${load}.`,
        typeToConfirm: target.name,
        requireReason: true,
        confirmLabel: 'Stop',
      }
    case 'start':
      return {
        title: `Start ${def.label} (${ref})?`,
        effect: 'Scales the Deployment to 1 replica.',
        outage: `Nothing answers until the model has loaded: ${load}.`,
        requireReason: false,
        confirmLabel: 'Start',
      }
  }
}

/**
 * The reason as it will be recorded. `X-Frame-Action` is latin-1 and the SDK
 * drops everything outside printable ASCII, so accents are folded first
 * (« arrêt » → « arret ») rather than deleted, whitespace collapsed, and
 * whatever is still not ASCII (emoji…) removed here — where the dialog can see
 * it — instead of silently at the header.
 */
export function sanitizeReason(reason: string): string {
  return reason
    .normalize('NFD')
    .replace(/\p{M}/gu, '')
    .replace(/\s+/g, ' ')
    .replace(/[^\x20-\x7e]/g, '')
    .trim()
}

/** The Stop label before its reason; the SDK builds the same string. */
export function stopLabelPrefix(namespace: string, name: string): string {
  return `scale deployment ${namespace}/${name} to 0: `
}

/**
 * How many characters of reason fit before the FrameTask label hits its cap
 * (`MAX_ACTION_LENGTH`, where `boundedActionLabel` would cut it silently).
 */
export function reasonMaxLength(namespace: string, name: string, cap: number): number {
  return Math.max(0, cap - stopLabelPrefix(namespace, name).length)
}

/** Whether the typed name and the reason unlock the confirm button. */
export function canConfirm(c: Confirmation, typed: string, reason: string): boolean {
  if (c.typeToConfirm !== undefined && typed.trim() !== c.typeToConfirm) return false
  if (c.requireReason && sanitizeReason(reason) === '') return false
  return true
}

// ── Trail ────────────────────────────────────────────────────────────────────

/** The newest FrameTask that wrote to this Deployment or its scale subresource. */
export function lastTaskFor(tasks: TaskRecord[], namespace: string, name: string): TaskRecord | undefined {
  const base = `${namespace}/deployments/${name}`
  return tasks
    .filter((t) => t.target === base || t.target === `${base}/scale`)
    .sort((a, b) => (b.startedAt ?? '').localeCompare(a.startedAt ?? ''))[0]
}
