// Pure logic behind the Installs screen: phase ordering, staleness/stall
// judgment, the confirmation guard, and the RBAC tier check for creating a
// FrameInstall. No network, no DOM — this is the layer vitest can reach
// (only src/**/*.test.ts runs; the .tsx screen cannot be unit-tested here,
// so every decision belongs in this file, the same split lot 1 used for
// src/lib/machines.ts).

/** The eight values `FrameInstallStatus.phase` can carry (Task 6's CRD). */
export type InstallPhase =
  | 'Pending'
  | 'Preparing'
  | 'MediaAttached'
  | 'Installing'
  | 'Installed'
  | 'Joining'
  | 'Ready'
  | 'Failed'

/**
 * The pipeline's own order, exactly as the CRD's enum lists it
 * (`api/frame/v1beta1/frameinstall_types.go`). `Failed` sits last because it
 * is where the state machine can land from any earlier phase — it is not
 * "further along" than `Ready`, it is where progress stopped meaning
 * anything, which is why callers compare phases with `isTerminal` first and
 * only use `phaseIndex` to place a still-running phase on a progress strip.
 */
export const PHASE_ORDER: InstallPhase[] = [
  'Pending',
  'Preparing',
  'MediaAttached',
  'Installing',
  'Installed',
  'Joining',
  'Ready',
  'Failed',
]

export function phaseIndex(p: InstallPhase): number {
  return PHASE_ORDER.indexOf(p)
}

export function isTerminal(p: InstallPhase): boolean {
  return p === 'Ready' || p === 'Failed'
}

export function phaseTone(p: InstallPhase): 'pending' | 'running' | 'good' | 'bad' {
  if (p === 'Failed') return 'bad'
  if (p === 'Ready') return 'good'
  if (p === 'Pending') return 'pending'
  return 'running'
}

// One threshold for every phase either cries wolf on Installing, which
// legitimately takes twenty minutes, or never fires on Preparing, which takes
// seconds. So the budget is per phase.
const PHASE_BUDGET_MS: Partial<Record<InstallPhase, number>> = {
  Pending: 60_000,
  Preparing: 5 * 60_000,
  MediaAttached: 2 * 60_000,
  Installing: 60 * 60_000,
  Installed: 2 * 60_000,
  Joining: 10 * 60_000,
}

export function isStalledInPhase(p: InstallPhase, since: string | undefined, now: Date): boolean {
  if (isTerminal(p) || !since) return false
  const budget = PHASE_BUDGET_MS[p]
  if (budget === undefined) return false
  const started = Date.parse(since)
  if (Number.isNaN(started)) return false
  return now.getTime() - started > budget
}

/**
 * "Ns ago"-shaped, but forward: how long the install has been sitting in its
 * current phase, not how long since it was last heard from. `now` is a
 * parameter, never `new Date()` read internally — lot 1 shipped a freshness
 * marker computed at render time under a watch with no poll, and it froze
 * exactly when the operator stopped writing, which is exactly when it
 * mattered (see machines.ts's `stalenessLabel` and `HardwareView`'s ticking
 * `now`). The caller here is `InstallsTab`, which must do the same.
 */
export function elapsedLabel(since: string | undefined, now: Date): string {
  if (!since) return '—'
  const ms = now.getTime() - new Date(since).getTime()
  const totalSeconds = Math.floor(ms / 1000)
  if (totalSeconds < 60) return `${totalSeconds} s`
  const totalMinutes = Math.floor(totalSeconds / 60)
  if (totalMinutes < 60) return `${totalMinutes} min`
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  return minutes === 0 ? `${hours} h` : `${hours} h ${minutes}`
}

/**
 * The create-dialog's confirmation guard: the machine's serial, retyped by
 * hand, must match exactly. Trimmed (surrounding whitespace from a paste is
 * not the content of the confirmation) but never lowercased and never a
 * substring check — a comparison that accepts a near-miss is a comparison a
 * paste of the wrong serial also passes, and what creating a FrameInstall
 * does to the named disks is not recoverable.
 */
export function confirmationMatches(typed: string, serial: string): boolean {
  const trimmed = typed.trim()
  return trimmed.length > 0 && trimmed === serial
}

/**
 * Whether the signed-in account may create a FrameInstall.
 *
 * Takes the admin boolean, not a tier string, for the same reason
 * `canEditManifest` (workloads.ts) does: the only signal the console
 * actually has is `isAdminToken`, so a `tier === 'admin'` comparison had
 * exactly two reachable inputs — `'admin'` and `''` — and its other branches
 * were tested but unreachable, which reads as coverage and is not.
 *
 * Creating a FrameInstall wipes the named disks. The editor tier can restart
 * and scale workloads but does not hold this — only admin does
 * (`frameinstall_editor_role.yaml` carries no tier label, design §9) — and
 * the screen must agree with the RBAC rather than offer a button the
 * apiserver will refuse.
 */
export function canCreateInstall(admin: boolean): boolean {
  return admin
}

// ── What the create dialog is allowed to say about a machine ─────────────────

/** The fields of a `FrameMachine`'s inventory the create dialog can show. */
export interface MachineChoice {
  name: string
  model: string
  serialNumber: string
}

/**
 * The label for one machine in the create dialog's picker.
 *
 * It names the machine and its model and **not its serial**. Layer 2 of the
 * destructive guard (design §8) exists to catch "I pointed at the wrong
 * machine", and it works by making the operator read the serial off the
 * machine or its BMC. A dialog that prints the answer next to the box asking
 * for it is a dialog where the check is a typing exercise — the one control
 * that says *which machine* would be satisfied by copying from the screen
 * that is already wrong.
 *
 * Lives here rather than inline in the JSX because no `.tsx` file in this
 * repo is ever executed by a test (see this file's header), so a rule
 * embedded in a template cannot be pinned by one.
 */
export function machineOptionLabel(m: MachineChoice): string {
  return `${m.name} — ${m.model}`
}

/** The label above the confirmation box. Names no serial, by construction. */
export const CONFIRM_SERIAL_LABEL = "Type the machine's serial to confirm"

/**
 * Where to read the serial from, since the console deliberately will not
 * show it. Both places are on the machine's side of the check, which is the
 * point.
 */
export const CONFIRM_SERIAL_HINT =
  'Read it from the chassis pull-tab or from the BMC — the console will not show it, ' +
  'because a serial you copied from this screen confirms nothing about which machine this is.'

/**
 * Every string `InstallDialog` renders that is derived from a machine, in
 * one list, so a test can assert what is *absent* from all of them at once.
 *
 * A check on the dialog's own JSX would be the better test and is not
 * available here; this is the closest thing that runs, and it only holds as
 * long as the dialog renders these values rather than rebuilding them
 * inline. That is why `InstallDialog` imports both of the constants above
 * instead of repeating their text.
 */
export function installDialogMachineTexts(choices: MachineChoice[]): string[] {
  return [CONFIRM_SERIAL_LABEL, CONFIRM_SERIAL_HINT, ...choices.map(machineOptionLabel)]
}

/**
 * Which of `texts` mention `needle`. Split out so the "the serial is absent"
 * assertion has a positive control: a search that finds nothing is
 * indistinguishable from a search that looks in the wrong place until
 * something proves the search itself works.
 */
export function textsMentioning(texts: string[], needle: string): string[] {
  if (needle === '') return []
  return texts.filter((t) => t.includes(needle))
}

// ── Domain shape ─────────────────────────────────────────────────────────────

/** One `FrameInstall`, as the Installs screen shows it. */
export interface Install {
  name: string
  namespace: string
  machineRef: string
  hostname: string
  address: string
  layoutKind: string
  clusterMode: string
  phase: InstallPhase
  phaseSince: string | undefined
  failedPhase: string
  message: string
  nodeName: string
  kubeconfigSecret: string
}

/** A `FrameInstall` CR as the apiserver returns it. */
export interface InstallCR {
  metadata: { name: string; namespace: string }
  spec: {
    machineRef: string
    hostname: string
    network: { address: string }
    layout: { kind: string }
    cluster: { mode: string }
  }
  status?: {
    phase?: string
    phaseSince?: string
    failedPhase?: string
    message?: string
    nodeName?: string
    kubeconfigSecret?: string
  }
}

/**
 * A raw `status.phase` string, normalized into `InstallPhase` the same way
 * `mapJobPhase`/`mapNodePhase` (frame-sdk.ts) normalize theirs: a switch
 * with a safe default, never an unvalidated cast. A freshly created
 * FrameInstall has no `status` at all until the controller's first
 * reconcile writes one — that reads as `Pending`, which is also where any
 * value this union does not know about lands, the same "unrecognized reads
 * as not-yet-done" call the other mappers make.
 */
export function mapInstallPhase(raw: string | undefined): InstallPhase {
  switch (raw) {
    case 'Preparing':
    case 'MediaAttached':
    case 'Installing':
    case 'Installed':
    case 'Joining':
    case 'Ready':
    case 'Failed':
      return raw
    default:
      return 'Pending'
  }
}

export function toInstall(cr: InstallCR): Install {
  const status = cr.status ?? {}
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    machineRef: cr.spec.machineRef,
    hostname: cr.spec.hostname,
    address: cr.spec.network?.address ?? '',
    layoutKind: cr.spec.layout?.kind ?? '',
    clusterMode: cr.spec.cluster?.mode ?? '',
    phase: mapInstallPhase(status.phase),
    phaseSince: status.phaseSince,
    failedPhase: status.failedPhase ?? '',
    message: status.message ?? '',
    nodeName: status.nodeName ?? '',
    kubeconfigSecret: status.kubeconfigSecret ?? '',
  }
}

// ── Create payload ───────────────────────────────────────────────────────────

/** One disk the layout consumes — `InstallDisk` (frameinstall_types.go). */
export interface InstallCreateDisk {
  byID: string
  sizeBytes: number
}

/**
 * The body `InstallDialog` submits. Shaped exactly like `FrameInstallSpec`
 * (frameinstall_types.go) — nothing here is renamed or restructured on the
 * way to the apiserver, so the CRD's own CEL rules (a mirror is exactly two
 * disks, joining needs both serverURL and joinTokenRef, …) are the only
 * validation this ever needs to agree with.
 */
export interface InstallCreateSpec {
  machineRef: string
  confirmSerial: string
  hostname: string
  network: {
    address: string
    gateway: string
    dns?: string[]
  }
  layout: {
    kind: 'single-disk' | 'mirror'
    disks: InstallCreateDisk[]
  }
  cluster: {
    mode: 'init' | 'join'
    serverURL?: string
    joinTokenRef?: string
    k3sVersion: string
  }
  bootMode?: 'UEFI' | 'Legacy'
  sshKeyRef: string
}
