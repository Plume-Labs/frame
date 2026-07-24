import { PTPClusterStats, PTPNodeStatus, PTPPortState } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { Radio, Clock, Warning } from '@phosphor-icons/react'
import { EntityTile, MicroStats, TuningDashboard } from '@/components/TuningDashboard'
import { Tone, TONE_TEXT, inverseScoreTone } from '@/lib/thresholds'

// ── Simulated PTP state ───────────────────────────────────────────────────────

const GM_ID = '000000.ff.fe00.0001'

const MOCK_NODES: PTPNodeStatus[] = [
  { nodeId: 'gpu-0', nodeName: 'gpu-node-00', portState: 'MASTER',  offsetNs: 0,    pathDelayNs: 0,   freqAdjPpb: 0,     grandmasterClockId: GM_ID, syncRateHz: 128, servoState: 's2' },
  { nodeId: 'gpu-1', nodeName: 'gpu-node-01', portState: 'SLAVE',   offsetNs: 3,    pathDelayNs: 412, freqAdjPpb: -124,  grandmasterClockId: GM_ID, syncRateHz: 128, servoState: 's2' },
  { nodeId: 'gpu-2', nodeName: 'gpu-node-02', portState: 'SLAVE',   offsetNs: -2,   pathDelayNs: 388, freqAdjPpb: 87,    grandmasterClockId: GM_ID, syncRateHz: 128, servoState: 's2' },
  { nodeId: 'gpu-3', nodeName: 'gpu-node-03', portState: 'SLAVE',   offsetNs: 7,    pathDelayNs: 505, freqAdjPpb: -301,  grandmasterClockId: GM_ID, syncRateHz: 128, servoState: 's2' },
  { nodeId: 'gpu-4', nodeName: 'gpu-node-04', portState: 'SLAVE',   offsetNs: -12,  pathDelayNs: 467, freqAdjPpb: 214,   grandmasterClockId: GM_ID, syncRateHz: 128, servoState: 's2' },
  { nodeId: 'gpu-5', nodeName: 'gpu-node-05', portState: 'SLAVE',   offsetNs: 28,   pathDelayNs: 621, freqAdjPpb: -88,   grandmasterClockId: GM_ID, syncRateHz: 128, servoState: 's1' },
  { nodeId: 'gpu-6', nodeName: 'gpu-node-06', portState: 'SLAVE',   offsetNs: -4,   pathDelayNs: 398, freqAdjPpb: 52,    grandmasterClockId: GM_ID, syncRateHz: 128, servoState: 's2' },
  { nodeId: 'cpu-0', nodeName: 'cpu-node-00', portState: 'PASSIVE', offsetNs: 0,    pathDelayNs: 0,   freqAdjPpb: 0,     grandmasterClockId: GM_ID, syncRateHz: 0,   servoState: 's0' },
  { nodeId: 'cpu-1', nodeName: 'cpu-node-01', portState: 'FAULTY',  offsetNs: 4200, pathDelayNs: 0,   freqAdjPpb: 0,     grandmasterClockId: GM_ID, syncRateHz: 0,   servoState: 's0' },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

const STATE_BADGE: Record<PTPPortState, string> = {
  MASTER:    'bg-primary/20 text-primary border-primary/30',
  SLAVE:     'bg-accent/20 text-accent border-accent/30',
  PASSIVE:   'bg-secondary text-muted-foreground border-border',
  LISTENING: 'bg-secondary text-muted-foreground border-border',
  FAULTY:    'bg-destructive/20 text-destructive border-destructive/30',
}

const SERVO_LABEL: Record<string, string> = { s0: 'Unlocked', s1: 'Freq-adj', s2: 'Locked' }
const SERVO_TONE: Record<string, Tone> = { s0: 'destructive', s1: 'warning', s2: 'accent' }

/** Clock offset: <=10 ns locked, <=100 ns tolerable, beyond that the servo is lost. */
const OFFSET_GOOD_NS = 10
const OFFSET_OK_NS = 100

function offsetTone(ns: number): Tone {
  return inverseScoreTone(Math.abs(ns), OFFSET_GOOD_NS, OFFSET_OK_NS)
}

// ── Node row ──────────────────────────────────────────────────────────────────

function PTPNodeRow({ node }: { node: PTPNodeStatus }) {
  const isMaster  = node.portState === 'MASTER'
  const isFaulty  = node.portState === 'FAULTY'
  const isPassive = node.portState === 'PASSIVE'

  return (
    <EntityTile
      className={isFaulty ? 'border-destructive/40 bg-destructive/5' : undefined}
      name={
        <span className="flex items-center gap-2">
          {isFaulty && <Warning size={12} className="text-destructive" />}
          {node.nodeName}
        </span>
      }
      badge={
        <span className="flex items-center gap-2">
          <span className={`font-mono text-[10px] ${TONE_TEXT[SERVO_TONE[node.servoState]]}`}>{SERVO_LABEL[node.servoState]}</span>
          <Badge className={`text-[10px] border ${STATE_BADGE[node.portState]}`}>{node.portState}</Badge>
        </span>
      }
    >
      {!isMaster && !isPassive && !isFaulty && (
        <MicroStats
          items={[
            {
              value: `${node.offsetNs > 0 ? '+' : ''}${node.offsetNs} ns`,
              label: 'Offset',
              tone: offsetTone(node.offsetNs),
            },
            { value: `${node.pathDelayNs} ns`, label: 'Path delay' },
            { value: `${node.freqAdjPpb > 0 ? '+' : ''}${node.freqAdjPpb} ppb`, label: 'Freq adj' },
          ]}
        />
      )}

      {isMaster && (
        <div className="text-[10px] text-muted-foreground font-mono">
          Grandmaster · Sync {node.syncRateHz} Hz · ID: {node.grandmasterClockId}
        </div>
      )}

      {isFaulty && (
        <div className="text-[10px] text-destructive font-mono">Sync lost — offset {node.offsetNs.toLocaleString()} ns</div>
      )}
    </EntityTile>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function PTPSyncDashboard() {
  // Derived per render — this used to be a module-level constant, frozen before
  // React ever mounted the component.
  const slaves = MOCK_NODES.filter(n => n.portState === 'SLAVE')
  const s: PTPClusterStats = {
    grandmasterNode: 'gpu-node-00',
    totalNodes: MOCK_NODES.length,
    lockedNodes: MOCK_NODES.filter(n => n.servoState === 's2').length,
    maxOffsetNs: Math.max(...MOCK_NODES.map(n => Math.abs(n.offsetNs))),
    meanOffsetNs: slaves.reduce((acc, n) => acc + Math.abs(n.offsetNs), 0) / slaves.length,
    nodes: MOCK_NODES,
  }

  const lockPct = (s.lockedNodes / s.totalNodes) * 100
  const faultyNodes = s.nodes.filter(n => n.portState === 'FAULTY')

  return (
    <TuningDashboard<PTPNodeStatus>
      title="PTP / IEEE 1588 — Nanosecond Clock Sync"
      icon={<Radio className="text-primary" />}
      stats={[
        {
          label: 'Locked Nodes',
          value: `${s.lockedNodes}/${s.totalNodes}`,
          tone: s.lockedNodes === s.totalNodes ? 'accent' : 'warning',
        },
        {
          label: 'Max Offset',
          value: <><Clock size={18} className="inline" /> {s.maxOffsetNs} ns</>,
          tone: offsetTone(s.maxOffsetNs),
        },
        {
          label: 'Mean Offset',
          value: `${s.meanOffsetNs.toFixed(1)} ns`,
          tone: offsetTone(s.meanOffsetNs),
        },
        { label: 'Grandmaster', value: s.grandmasterNode, tone: 'primary', size: 'sm' },
        {
          label: 'Faulty',
          value: faultyNodes.length,
          tone: faultyNodes.length > 0 ? 'destructive' : 'foreground',
        },
      ]}
      progress={{
        value: lockPct,
        captionLeft: <span className="uppercase tracking-wide">Sync Lock Rate</span>,
        captionRight: <span className="font-mono">{lockPct.toFixed(0)}%</span>,
      }}
      entitiesTitle="Per-Node PTP Status"
      entities={s.nodes}
      entityKey={n => n.nodeId}
      renderEntity={n => <PTPNodeRow node={n} />}
      configTitle="ptp4l / phc2sys Config"
      config={`# deploy/networking/ptp-sync.yaml  (excerpt)
# PTP grandmaster on gpu-node-00 (BC-capable NIC: Mellanox CX-6 Dx)
[global]
dataset_comparison         G.8275.x
G.8275.defaultDS.localPriority 128
time_stamping              hardware
tx_timestamp_timeout       50
uds_address                /var/run/ptp4l

[eth0]
serverOnly                 0          # slave on uplink
delay_mechanism            E2E
network_transport          L2

# Bind phc2sys to NCCL CUDA stream so GPU kernel launches share the same clock
# Reference: NVIDIA Collective Communications Library tuning guide`}
    />
  )
}
