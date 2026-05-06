import { PTPClusterStats, PTPNodeStatus, PTPPortState } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Radio, Clock, Warning } from '@phosphor-icons/react'

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

const MOCK_STATS: PTPClusterStats = {
  grandmasterNode: 'gpu-node-00',
  totalNodes: MOCK_NODES.length,
  lockedNodes: MOCK_NODES.filter(n => n.servoState === 's2').length,
  maxOffsetNs: Math.max(...MOCK_NODES.map(n => Math.abs(n.offsetNs))),
  meanOffsetNs: MOCK_NODES.filter(n => n.portState === 'SLAVE').reduce((s, n) => s + Math.abs(n.offsetNs), 0) / MOCK_NODES.filter(n => n.portState === 'SLAVE').length,
  nodes: MOCK_NODES,
}

// ── Helpers ───────────────────────────────────────────────────────────────────

const STATE_BADGE: Record<PTPPortState, string> = {
  MASTER:    'bg-primary/20 text-primary border-primary/30',
  SLAVE:     'bg-accent/20 text-accent border-accent/30',
  PASSIVE:   'bg-secondary text-muted-foreground border-border',
  LISTENING: 'bg-secondary text-muted-foreground border-border',
  FAULTY:    'bg-destructive/20 text-destructive border-destructive/30',
}

const SERVO_LABEL: Record<string, string> = { s0: 'Unlocked', s1: 'Freq-adj', s2: 'Locked' }
const SERVO_COLOR: Record<string, string> = { s0: 'text-destructive', s1: 'text-[oklch(0.75_0.18_75)]', s2: 'text-accent' }

function offsetColor(ns: number) {
  const abs = Math.abs(ns)
  if (abs <= 10)  return 'text-accent'
  if (abs <= 100) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-destructive'
}

// ── Node row ──────────────────────────────────────────────────────────────────

function PTPNodeRow({ node }: { node: PTPNodeStatus }) {
  const isMaster  = node.portState === 'MASTER'
  const isFaulty  = node.portState === 'FAULTY'
  const isPassive = node.portState === 'PASSIVE'

  return (
    <div className={`p-3 rounded-lg border ${isFaulty ? 'border-destructive/40 bg-destructive/5' : 'border-border bg-secondary/30'}`}>
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2">
          {isFaulty && <Warning size={12} className="text-destructive" />}
          <span className="font-mono text-xs font-bold">{node.nodeName}</span>
        </div>
        <div className="flex items-center gap-2">
          <span className={`font-mono text-[10px] ${SERVO_COLOR[node.servoState]}`}>{SERVO_LABEL[node.servoState]}</span>
          <Badge className={`text-[10px] border ${STATE_BADGE[node.portState]}`}>{node.portState}</Badge>
        </div>
      </div>

      {!isMaster && !isPassive && !isFaulty && (
        <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground">
          <div>
            <span className={`font-mono font-bold ${offsetColor(node.offsetNs)}`}>{node.offsetNs > 0 ? '+' : ''}{node.offsetNs} ns</span>
            <div>Offset</div>
          </div>
          <div>
            <span className="font-mono font-bold text-foreground">{node.pathDelayNs} ns</span>
            <div>Path delay</div>
          </div>
          <div>
            <span className="font-mono font-bold text-foreground">{node.freqAdjPpb > 0 ? '+' : ''}{node.freqAdjPpb} ppb</span>
            <div>Freq adj</div>
          </div>
        </div>
      )}

      {isMaster && (
        <div className="text-[10px] text-muted-foreground font-mono">
          Grandmaster · Sync {node.syncRateHz} Hz · ID: {node.grandmasterClockId}
        </div>
      )}

      {isFaulty && (
        <div className="text-[10px] text-destructive font-mono">Sync lost — offset {node.offsetNs.toLocaleString()} ns</div>
      )}
    </div>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function PTPSyncDashboard() {
  const s = MOCK_STATS
  const lockPct = (s.lockedNodes / s.totalNodes) * 100
  const faultyNodes = s.nodes.filter(n => n.portState === 'FAULTY')

  return (
    <div className="space-y-6">
      {/* Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Radio className="text-primary" />
            PTP / IEEE 1588 — Nanosecond Clock Sync
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Locked Nodes</div>
              <div className={`font-mono text-2xl font-bold ${s.lockedNodes === s.totalNodes ? 'text-accent' : 'text-[oklch(0.75_0.18_75)]'}`}>
                {s.lockedNodes}/{s.totalNodes}
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Max Offset</div>
              <div className={`font-mono text-2xl font-bold ${offsetColor(s.maxOffsetNs)}`}>
                <Clock size={18} className="inline" /> {s.maxOffsetNs} ns
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Mean Offset</div>
              <div className={`font-mono text-2xl font-bold ${offsetColor(s.meanOffsetNs)}`}>
                {s.meanOffsetNs.toFixed(1)} ns
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Grandmaster</div>
              <div className="font-mono text-sm font-bold text-primary">{s.grandmasterNode}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Faulty</div>
              <div className={`font-mono text-2xl font-bold ${faultyNodes.length > 0 ? 'text-destructive' : 'text-foreground'}`}>
                {faultyNodes.length}
              </div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span className="uppercase tracking-wide">Sync Lock Rate</span>
              <span className="font-mono">{lockPct.toFixed(0)}%</span>
            </div>
            <Progress value={lockPct} className="h-3" />
          </div>
        </CardContent>
      </Card>

      {/* Per-node grid */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Per-Node PTP Status</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {s.nodes.map(n => <PTPNodeRow key={n.nodeId} node={n} />)}
          </div>
        </CardContent>
      </Card>

      {/* Config reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">ptp4l / phc2sys Config</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# deploy/networking/ptp-sync.yaml  (excerpt)
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
# Reference: NVIDIA Collective Communications Library tuning guide`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
