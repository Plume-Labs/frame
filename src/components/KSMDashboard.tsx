import { KSMClusterStats, KSMNodeStats } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Stack, Speedometer } from '@phosphor-icons/react'

// ── Simulated KSM state ───────────────────────────────────────────────────────

const MOCK_NODES: KSMNodeStats[] = [
  { nodeId: 'gpu-0', nodeName: 'gpu-node-00', enabled: true,  pagesMerged: 187420, pagesSharing: 312800, memorySavedMB: 732,  scanRatePagesPerSec: 4000, scanIntervalMs: 200 },
  { nodeId: 'gpu-1', nodeName: 'gpu-node-01', enabled: true,  pagesMerged: 201300, pagesSharing: 348200, memorySavedMB: 786,  scanRatePagesPerSec: 4000, scanIntervalMs: 200 },
  { nodeId: 'gpu-2', nodeName: 'gpu-node-02', enabled: true,  pagesMerged: 155800, pagesSharing: 291000, memorySavedMB: 608,  scanRatePagesPerSec: 4000, scanIntervalMs: 200 },
  { nodeId: 'gpu-3', nodeName: 'gpu-node-03', enabled: true,  pagesMerged: 220100, pagesSharing: 380400, memorySavedMB: 860,  scanRatePagesPerSec: 4000, scanIntervalMs: 200 },
  { nodeId: 'gpu-4', nodeName: 'gpu-node-04', enabled: false, pagesMerged: 0,      pagesSharing: 0,      memorySavedMB: 0,    scanRatePagesPerSec: 0,    scanIntervalMs: 0   },
  { nodeId: 'cpu-0', nodeName: 'cpu-node-00', enabled: true,  pagesMerged: 98400,  pagesSharing: 167500, memorySavedMB: 384,  scanRatePagesPerSec: 2000, scanIntervalMs: 400 },
]

const CLUSTER_STATS: KSMClusterStats = {
  nodes: MOCK_NODES,
  totalSavedGB: MOCK_NODES.reduce((s, n) => s + n.memorySavedMB, 0) / 1024,
  totalPagesMerged: MOCK_NODES.reduce((s, n) => s + n.pagesMerged, 0),
  enabledNodes: MOCK_NODES.filter(n => n.enabled).length,
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function mergeRatio(node: KSMNodeStats): number {
  return node.pagesSharing > 0 ? node.pagesMerged / node.pagesSharing : 0
}

// ── Node row ──────────────────────────────────────────────────────────────────

function KSMNodeRow({ node }: { node: KSMNodeStats }) {
  const ratio = mergeRatio(node)

  return (
    <div className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2">
      <div className="flex items-center justify-between">
        <span className="font-mono text-xs font-bold">{node.nodeName}</span>
        {node.enabled
          ? <Badge className="text-[10px] border bg-accent/20 text-accent border-accent/30">KSM ON</Badge>
          : <Badge className="text-[10px] border bg-secondary text-muted-foreground border-border">KSM OFF</Badge>
        }
      </div>

      {node.enabled && (
        <>
          <div className="space-y-1">
            <div className="flex justify-between text-xs">
              <span className="text-muted-foreground">Merge ratio</span>
              <span className="font-mono font-bold text-accent">{(ratio * 100).toFixed(0)}%</span>
            </div>
            <Progress value={ratio * 100} className="h-1" />
          </div>

          <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground">
            <div>
              <span className="font-mono font-bold text-foreground">{node.pagesMerged.toLocaleString()}</span>
              <div>Pages merged</div>
            </div>
            <div>
              <span className="font-mono font-bold text-accent">{node.memorySavedMB} MB</span>
              <div>Saved</div>
            </div>
            <div>
              <span className="font-mono font-bold text-foreground">{node.scanIntervalMs} ms</span>
              <div>Interval</div>
            </div>
          </div>
        </>
      )}
    </div>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function KSMDashboard() {
  const s = CLUSTER_STATS

  return (
    <div className="space-y-6">
      {/* Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Stack className="text-primary" />
            KSM — Kernel Same-Page Merging
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Enabled Nodes</div>
              <div className="font-mono text-2xl font-bold text-accent">{s.enabledNodes} / {s.nodes.length}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Memory Saved</div>
              <div className="font-mono text-2xl font-bold text-primary">
                <Speedometer size={18} className="inline" /> {s.totalSavedGB.toFixed(1)} GB
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Pages Merged</div>
              <div className="font-mono text-2xl font-bold text-foreground">{(s.totalPagesMerged / 1000).toFixed(0)}k</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Target Workloads</div>
              <div className="font-mono text-sm font-bold text-muted-foreground">Ollama / vLLM</div>
            </div>
          </div>

          <div className="rounded-lg bg-secondary/40 p-3 text-xs text-muted-foreground space-y-1">
            <div className="font-mono font-bold text-foreground mb-1">How it works</div>
            <div>Multiple Ollama/vLLM instances loading the same model weights into RAM produce identical pages. KSM scans, de-duplicates, and maps them to a single physical page — saving several GB per node at the cost of ~1% CPU for scanning.</div>
          </div>
        </CardContent>
      </Card>

      {/* Per-node */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Per-Node KSM Stats</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {s.nodes.map(n => <KSMNodeRow key={n.nodeId} node={n} />)}
          </div>
        </CardContent>
      </Card>

      {/* Config reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Kernel Config Reference</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# deploy/ansible/playbooks/ksm.yml  (excerpt)
# Enable KSM and tune scan parameters for model-weight de-dup

- name: Enable KSM
  shell: echo 1 > /sys/kernel/mm/ksm/run

- name: Set KSM pages_to_scan (balance CPU cost vs latency)
  shell: echo 4000 > /sys/kernel/mm/ksm/pages_to_scan

- name: Set scan interval (200 ms = good for inference workloads)
  shell: echo 200 > /sys/kernel/mm/ksm/sleep_millisecs

# Annotate pods to opt in to KSM (kernel 6.4+ MADV_MERGEABLE hint)
# spec.containers[].resources.limits: memory: ...
# Enable via: prctl(PR_SET_MEMORY_MERGE, 1) inside the container entrypoint`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
