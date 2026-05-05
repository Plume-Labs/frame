import { MPSNodeStatus, MPSClientInfo } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Cpu, User } from '@phosphor-icons/react'

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

// ── Helpers ───────────────────────────────────────────────────────────────────

function utilColor(pct: number) {
  if (pct >= 90) return 'text-destructive'
  if (pct >= 70) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-foreground'
}

function formatAge(ms: number): string {
  const s = Math.floor((Date.now() - ms) / 1000)
  const m = Math.floor(s / 60)
  const h = Math.floor(m / 60)
  if (h > 0) return `${h}h`
  if (m > 0) return `${m}m`
  return `${s}s`
}

function ClientRow({ client }: { client: MPSClientInfo }) {
  return (
    <div className="flex items-center justify-between text-xs p-2 rounded bg-secondary/40">
      <div className="flex items-center gap-2">
        <User size={10} className="text-muted-foreground" />
        <span className="font-mono font-bold">{client.cmdline}</span>
        <span className="text-muted-foreground">pid {client.pid}</span>
      </div>
      <div className="flex items-center gap-3">
        <span className={`font-mono ${utilColor(client.smUtilPct)}`}>{client.smUtilPct}% SM</span>
        <span className="font-mono text-muted-foreground">{(client.memUsedMB / 1024).toFixed(1)} GB</span>
        <span className="font-mono text-muted-foreground">{formatAge(client.startedAt)}</span>
      </div>
    </div>
  )
}

// ── Node card ─────────────────────────────────────────────────────────────────

function MPSNodeCard({ node }: { node: MPSNodeStatus }) {
  return (
    <div className={`p-4 rounded-lg border ${node.enabled ? 'border-border bg-secondary/30' : 'border-border/40 bg-secondary/10'} space-y-3`}>
      <div className="flex items-center justify-between">
        <div>
          <div className="font-mono text-sm font-bold">{node.nodeName}</div>
          <div className="font-mono text-[10px] text-muted-foreground">GPU {node.gpuIndex} · pipe: {node.pipeDir}</div>
        </div>
        <div className="flex items-center gap-2">
          {node.enabled
            ? <Badge className="text-[10px] border bg-accent/20 text-accent border-accent/30">MPS ON</Badge>
            : <Badge className="text-[10px] border bg-secondary text-muted-foreground border-border">MPS OFF</Badge>
          }
        </div>
      </div>

      {node.enabled && (
        <>
          <div className="space-y-1">
            <div className="flex justify-between text-xs">
              <span className="text-muted-foreground">Total SM Util ({node.activeClients.length} clients)</span>
              <span className={`font-mono font-bold ${utilColor(node.totalSMUtil)}`}>{node.totalSMUtil}%</span>
            </div>
            <Progress value={node.totalSMUtil} className="h-1.5" />
          </div>

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
    </div>
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
    <div className="space-y-6">
      {/* Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Cpu className="text-primary" />
            NVIDIA MPS — Multi-Process Service
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">MPS Nodes</div>
              <div className="font-mono text-2xl font-bold text-accent">{enabledNodes.length} / {nodes.length}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Active Clients</div>
              <div className="font-mono text-2xl font-bold text-primary">{totalClients}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Avg SM Util</div>
              <div className={`font-mono text-2xl font-bold ${utilColor(avgSMUtil)}`}>{avgSMUtil.toFixed(0)}%</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">GPU Contention</div>
              <div className="font-mono text-2xl font-bold text-accent">None</div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span className="uppercase tracking-wide">Avg GPU SM Utilization</span>
              <span className="font-mono">{avgSMUtil.toFixed(0)}%</span>
            </div>
            <Progress value={avgSMUtil} className="h-3" />
          </div>
        </CardContent>
      </Card>

      {/* Per-node */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Per-Node MPS Status</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {nodes.map(n => <MPSNodeCard key={n.nodeId} node={n} />)}
        </CardContent>
      </Card>

      {/* Config reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">MPS Setup Reference</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# deploy/kubernetes/base/nvidia-mps.yaml  (excerpt)
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
        value: "25"`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
