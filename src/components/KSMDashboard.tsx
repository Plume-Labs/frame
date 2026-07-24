import { KSMClusterStats, KSMNodeStats } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { Stack, Speedometer } from '@phosphor-icons/react'
import { EntityTile, MicroStats, TileMeter, TuningDashboard } from '@/components/TuningDashboard'

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
    <EntityTile
      name={node.nodeName}
      badge={node.enabled
        ? <Badge className="text-[10px] border bg-accent/20 text-accent border-accent/30">KSM ON</Badge>
        : <Badge className="text-[10px] border bg-secondary text-muted-foreground border-border">KSM OFF</Badge>
      }
    >
      {node.enabled && (
        <>
          <TileMeter label="Merge ratio" value={ratio * 100} />

          <MicroStats
            items={[
              { value: node.pagesMerged.toLocaleString(), label: 'Pages merged' },
              { value: `${node.memorySavedMB} MB`, label: 'Saved', tone: 'accent' },
              { value: `${node.scanIntervalMs} ms`, label: 'Interval' },
            ]}
          />
        </>
      )}
    </EntityTile>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function KSMDashboard() {
  const s = CLUSTER_STATS

  return (
    <TuningDashboard<KSMNodeStats>
      title="KSM — Kernel Same-Page Merging"
      icon={<Stack className="text-primary" />}
      stats={[
        { label: 'Enabled Nodes', value: `${s.enabledNodes} / ${s.nodes.length}`, tone: 'accent' },
        {
          label: 'Memory Saved',
          value: <><Speedometer size={18} className="inline" /> {s.totalSavedGB.toFixed(1)} GB</>,
          tone: 'primary',
        },
        {
          label: 'Pages Merged',
          value: `${(s.totalPagesMerged / 1000).toFixed(0)}k`,
          tone: 'foreground',
        },
        { label: 'Target Workloads', value: 'Ollama / vLLM', tone: 'muted', size: 'sm' },
      ]}
      note={
        <div className="rounded-lg bg-secondary/40 p-3 text-xs text-muted-foreground space-y-1">
          <div className="font-mono font-bold text-foreground mb-1">How it works</div>
          <div>Multiple Ollama/vLLM instances loading the same model weights into RAM produce identical pages. KSM scans, de-duplicates, and maps them to a single physical page — saving several GB per node at the cost of ~1% CPU for scanning.</div>
        </div>
      }
      entitiesTitle="Per-Node KSM Stats"
      entities={s.nodes}
      entityKey={n => n.nodeId}
      renderEntity={n => <KSMNodeRow node={n} />}
      configTitle="Kernel Config Reference"
      config={`# Talos / kernel KSM settings (reference)
# Configure via Talos MachineConfig patches and reconcile through GitOps.

- name: Enable KSM
  shell: echo 1 > /sys/kernel/mm/ksm/run

- name: Set KSM pages_to_scan (balance CPU cost vs latency)
  shell: echo 4000 > /sys/kernel/mm/ksm/pages_to_scan

- name: Set scan interval (200 ms = good for inference workloads)
  shell: echo 200 > /sys/kernel/mm/ksm/sleep_millisecs

# Annotate pods to opt in to KSM (kernel 6.4+ MADV_MERGEABLE hint)
# spec.containers[].resources.limits: memory: ...
# Enable via: prctl(PR_SET_MEMORY_MERGE, 1) inside the container entrypoint`}
    />
  )
}
