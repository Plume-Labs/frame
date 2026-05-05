import { ClusterNode } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { HardDrives, Database, MagnifyingGlass } from '@phosphor-icons/react'

interface DataFabricDashboardProps {
  nodes: ClusterNode[]
}

function formatBytes(gb: number): string {
  if (gb >= 1024) return `${(gb / 1024).toFixed(1)} PB`
  if (gb >= 1) return `${gb.toFixed(1)} TB`
  return `${(gb * 1024).toFixed(0)} GB`
}

const SAMPLE_DATASETS = [
  { name: 'neura-training-v3', size: 12.4, tier: 'nvme', accessRate: 1240, type: 'parquet' },
  { name: 'inference-cache-v2', size: 2.1, tier: 'ram', accessRate: 8900, type: 'feather' },
  { name: 'historical-logs-2024', size: 180.0, tier: 'object', accessRate: 32, type: 'json.gz' },
  { name: 'model-weights-v3', size: 85.0, tier: 'nvme', accessRate: 410, type: 'safetensors' },
  { name: 'eval-dataset-q4', size: 4.8, tier: 'nvme', accessRate: 220, type: 'parquet' },
]

const TIER_COLORS: Record<string, string> = {
  ram:    'text-accent',
  nvme:   'text-primary',
  object: 'text-[oklch(0.75_0.18_75)]',
}

export function DataFabricDashboard({ nodes }: DataFabricDashboardProps) {
  const activeNodes = nodes.filter(n => n.status !== 'offline')

  // Aggregate storage metrics across all nodes
  const totalCapacityTB = activeNodes.reduce((s, n) => s + n.storage.totalCapacity, 0) / 1024
  const usedCapacityTB = activeNodes.reduce((s, n) => s + n.storage.usedCapacity, 0) / 1024
  const usedPct = totalCapacityTB > 0 ? (usedCapacityTB / totalCapacityTB) * 100 : 0

  const dataFabricNodes = activeNodes.filter(n => n.storage.dataFabricEnabled)
  const totalMetadataEntries = dataFabricNodes.reduce((s, n) => s + n.storage.metadataEntries, 0)
  const totalActiveDatasets = dataFabricNodes.reduce((s, n) => s + n.storage.activeDatasets, 0)
  const totalReadIOPS = activeNodes.reduce((s, n) => s + n.storage.readIOPS, 0)
  const totalWriteIOPS = activeNodes.reduce((s, n) => s + n.storage.writeIOPS, 0)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Data Fabric — Unified Storage
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Total Capacity</div>
              <div className="font-mono text-2xl font-bold text-primary">{totalCapacityTB.toFixed(1)} TB</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Used</div>
              <div className="font-mono text-2xl font-bold">{usedCapacityTB.toFixed(1)} TB</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Metadata Entries</div>
              <div className="font-mono text-2xl font-bold text-accent">{totalMetadataEntries.toLocaleString()}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Active Datasets</div>
              <div className="font-mono text-2xl font-bold text-primary">{totalActiveDatasets}</div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span>Cluster storage usage</span>
              <span className="font-mono">{usedPct.toFixed(1)}%</span>
            </div>
            <Progress value={usedPct} className="h-2" />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div className="p-3 rounded-lg bg-secondary/30">
              <div className="text-xs text-muted-foreground uppercase tracking-wide mb-1">Read IOPS (cluster)</div>
              <div className="font-mono text-lg font-bold text-primary">{totalReadIOPS.toLocaleString()}</div>
            </div>
            <div className="p-3 rounded-lg bg-secondary/30">
              <div className="text-xs text-muted-foreground uppercase tracking-wide mb-1">Write IOPS (cluster)</div>
              <div className="font-mono text-lg font-bold">{totalWriteIOPS.toLocaleString()}</div>
            </div>
          </div>

          <div className="flex items-center gap-3 text-xs text-muted-foreground">
            <div className="flex items-center gap-1">
              <div className="w-2 h-2 rounded-full bg-accent" />
              <span>Data fabric enabled: {dataFabricNodes.length} / {activeNodes.length} nodes</span>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <MagnifyingGlass className="text-primary" />
            Dataset Catalog
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-2">
            {SAMPLE_DATASETS.map(ds => (
              <div key={ds.name} className="flex items-center justify-between p-3 rounded-lg bg-secondary/30 hover:bg-secondary/50 transition-colors">
                <div className="space-y-0.5">
                  <div className="font-mono text-sm font-semibold">{ds.name}</div>
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <span>{ds.type}</span>
                    <span>·</span>
                    <span className={`font-mono font-bold ${TIER_COLORS[ds.tier]}`}>{ds.tier.toUpperCase()}</span>
                  </div>
                </div>
                <div className="text-right">
                  <div className="font-mono text-sm font-semibold">{formatBytes(ds.size)}</div>
                  <div className="text-xs text-muted-foreground">{ds.accessRate.toLocaleString()} req/s</div>
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <Database className="text-primary" />
            Storage Tier Summary
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-3">
            {[
              { tier: 'RAM Cache (Alluxio)', desc: 'Hot data — sub-ms access', usedTB: 0.06, totalTB: 2.0, color: 'text-accent', barColor: 'bg-accent' },
              { tier: 'NVMe (Local + Ceph)', desc: 'Warm data — μs access', usedTB: usedCapacityTB * 0.5, totalTB: totalCapacityTB * 0.5, color: 'text-primary', barColor: 'bg-primary' },
              { tier: 'Object Storage (MinIO)', desc: 'Cold data — ms access', usedTB: usedCapacityTB * 0.5, totalTB: totalCapacityTB * 0.5, color: 'text-[oklch(0.75_0.18_75)]', barColor: 'bg-[oklch(0.75_0.18_75)]' },
            ].map(row => (
              <div key={row.tier} className="space-y-1">
                <div className="flex items-center justify-between text-sm">
                  <div>
                    <span className={`font-mono font-bold ${row.color}`}>{row.tier}</span>
                    <span className="text-xs text-muted-foreground ml-2">{row.desc}</span>
                  </div>
                  <span className="font-mono text-xs">{row.usedTB.toFixed(1)} / {row.totalTB.toFixed(1)} TB</span>
                </div>
                <div className="w-full h-1.5 rounded-full bg-secondary">
                  <div className={`h-1.5 rounded-full ${row.barColor}`} style={{ width: `${Math.min(100, (row.usedTB / row.totalTB) * 100)}%` }} />
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
