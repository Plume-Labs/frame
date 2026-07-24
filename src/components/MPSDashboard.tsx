import { MPSNodeStatus, MPSClientInfo } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { Cpu, User } from '@phosphor-icons/react'
import { EntityTile, TileMeter, TuningDashboard } from '@/components/TuningDashboard'
import { formatAge, inverseScoreClass, inverseScoreTone } from '@/lib/thresholds'

// ── Simulated MPS state ───────────────────────────────────────────────────────

const MOCK_NODES: MPSNodeStatus[] = [
  {
    nodeId: 'gpu-0', nodeName: 'gpu-node-00', enabled: true,
    pipeDir: '/tmp/nvidia-mps', gpuIndex: 0,
    activeThreadPct: 100, totalSMUtil: 78,
    activeClients: [
      { pid: 12301, cmdline: 'vllm-0', gpuIndex: 0, smUtilPct: 32, memUsedMB: 8192,  startedAt: Date.now() - 3600000 },
      { pid: 12302, cmdline: 'vllm-1', gpuIndex: 0, smUtilPct: 28, memUsedMB: 7680,  startedAt: Date.now() - 1800000 },
      { pid: 12303, cmdline: 'ollama', gpuIndex: 0, smUtilPct: 18, memUsedMB: 4096,  startedAt: Date.now() - 600000  },
    ],
  },
  {
    nodeId: 'gpu-1', nodeName: 'gpu-node-01', enabled: true,
    pipeDir: '/tmp/nvidia-mps', gpuIndex: 0,
    activeThreadPct: 75, totalSMUtil: 55,
    activeClients: [
      { pid: 13201, cmdline: 'vllm-0',  gpuIndex: 0, smUtilPct: 35, memUsedMB: 10240, startedAt: Date.now() - 7200000 },
      { pid: 13202, cmdline: 'neura-sm', gpuIndex: 0, smUtilPct: 20, memUsedMB: 3072,  startedAt: Date.now() - 120000  },
    ],
  },
  {
    nodeId: 'gpu-2', nodeName: 'gpu-node-02', enabled: false,
    pipeDir: '/tmp/nvidia-mps', gpuIndex: 0,
    activeThreadPct: 100, totalSMUtil: 0,
    activeClients: [],
  },
]

/** SM utilisation: <=70% healthy, <=90% warning, above that the GPU is saturated. */
const UTIL_GOOD = 70
const UTIL_OK = 90

function ClientRow({ client }: { client: MPSClientInfo }) {
  return (
    <div className="flex items-center justify-between text-xs p-2 rounded bg-secondary/40">
      <div className="flex items-center gap-2">
        <User size={10} className="text-muted-foreground" />
        <span className="font-mono font-bold">{client.cmdline}</span>
        <span className="text-muted-foreground">pid {client.pid}</span>
      </div>
      <div className="flex items-center gap-3">
        <span className={`font-mono ${inverseScoreClass(client.smUtilPct, UTIL_GOOD, UTIL_OK)}`}>{client.smUtilPct}% SM</span>
        <span className="font-mono text-muted-foreground">{(client.memUsedMB / 1024).toFixed(1)} GB</span>
        <span className="font-mono text-muted-foreground">{formatAge(client.startedAt)}</span>
      </div>
    </div>
  )
}

// ── Node card ─────────────────────────────────────────────────────────────────

function MPSNodeCard({ node }: { node: MPSNodeStatus }) {
  return (
    <EntityTile
      name={node.nodeName}
      badge={node.enabled
        ? <Badge className="text-[10px] border bg-accent/20 text-accent border-accent/30">MPS ON</Badge>
        : <Badge className="text-[10px] border bg-secondary text-muted-foreground border-border">MPS OFF</Badge>
      }
      className={node.enabled ? undefined : 'border-border/40 bg-secondary/10'}
    >
      <div className="font-mono text-[10px] text-muted-foreground">GPU {node.gpuIndex} · pipe: {node.pipeDir}</div>

      {node.enabled && (
        <>
          <TileMeter
            label={`Total SM Util (${node.activeClients.length} clients)`}
            value={node.totalSMUtil}
            tone={inverseScoreTone(node.totalSMUtil, UTIL_GOOD, UTIL_OK)}
          />

          <div className="space-y-1">
            {node.activeClients.map(c => <ClientRow key={c.pid} client={c} />)}
            {node.activeClients.length === 0 && (
              <div className="text-xs text-muted-foreground text-center py-2">No active clients</div>
            )}
          </div>

          <div className="text-[10px] text-muted-foreground font-mono">
            Active thread limit: {node.activeThreadPct}%
          </div>
        </>
      )}
    </EntityTile>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function MPSDashboard() {
  const nodes = MOCK_NODES
  const enabledNodes  = nodes.filter(n => n.enabled)
  const totalClients  = enabledNodes.reduce((s, n) => s + n.activeClients.length, 0)
  const avgSMUtil     = enabledNodes.length > 0
    ? enabledNodes.reduce((s, n) => s + n.totalSMUtil, 0) / enabledNodes.length
    : 0

  return (
    <TuningDashboard<MPSNodeStatus>
      title="NVIDIA MPS — Multi-Process Service"
      icon={<Cpu className="text-primary" />}
      stats={[
        { label: 'MPS Nodes', value: `${enabledNodes.length} / ${nodes.length}`, tone: 'accent' },
        { label: 'Active Clients', value: totalClients, tone: 'primary' },
        {
          label: 'Avg SM Util',
          value: `${avgSMUtil.toFixed(0)}%`,
          tone: inverseScoreTone(avgSMUtil, UTIL_GOOD, UTIL_OK),
        },
        { label: 'GPU Contention', value: 'None', tone: 'accent' },
      ]}
      progress={{
        value: avgSMUtil,
        captionLeft: <span className="uppercase tracking-wide">Avg GPU SM Utilization</span>,
        captionRight: <span className="font-mono">{avgSMUtil.toFixed(0)}%</span>,
      }}
      entitiesTitle="Per-Node MPS Status"
      entities={nodes}
      entityKey={n => n.nodeId}
      entityLayout="stack"
      renderEntity={n => <MPSNodeCard node={n} />}
      configTitle="MPS Setup Reference"
      config={`# deploy/kubernetes/base/nvidia-mps.yaml  (excerpt)
# DaemonSet enabling MPS on every GPU node
initContainers:
  - name: mps-init
    image: nvidia/cuda:12.4-base
    command: [sh, -c]
    args:
      - |
        nvidia-cuda-mps-control -d
        echo "start_server -uid 0" | nvidia-cuda-mps-control
    env:
      - name: CUDA_MPS_PIPE_DIRECTORY
        value: /tmp/nvidia-mps
      - name: CUDA_MPS_LOG_DIRECTORY
        value: /tmp/nvidia-log
      # Limit each client to 25% of SMs to prevent starvation
      - name: CUDA_MPS_ACTIVE_THREAD_PERCENTAGE
        value: "25"`}
    />
  )
}
