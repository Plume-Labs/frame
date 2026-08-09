import { PostureStatus, SecurityStatus, TetragonActivity, createFrameClient, crdListPath } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ShieldWarning, ArrowClockwise, Warning, Package, Wrench, Network } from '@phosphor-icons/react'

const frame = createFrameClient()

const SEV_TONE: Record<string, string> = {
  critical: 'text-destructive',
  high: 'text-warning',
  medium: 'text-accent',
  low: 'text-muted-foreground',
}

// Falco priority → tailwind text colour. Lower rank = more severe.
const TONE: Record<string, string> = {
  emergency: 'text-destructive',
  alert: 'text-destructive',
  critical: 'text-destructive',
  error: 'text-warning',
  warning: 'text-warning',
  notice: 'text-accent',
  informational: 'text-muted-foreground',
  debug: 'text-muted-foreground',
}

export function SecurityView() {
  // Falco's Prometheus metrics, not a Kubernetes object — nothing to watch.
  // Runtime detections are worth surfacing faster than a passive storage
  // gauge, so this sits below the 30s default.
  const { state, reload } = useLiveResource<SecurityStatus | null>(() => frame.cluster.security(), [], [], 20_000)
  const sec = state.phase === 'ready' ? state.data : null
  // trivy-operator's VulnerabilityReport/ConfigAuditReport are real CRs.
  const { state: postureState, reload: reloadPosture } = useLiveResource<PostureStatus | null>(
    () => frame.cluster.posture(),
    [],
    [
      crdListPath('aquasecurity.github.io', 'v1alpha1', 'vulnerabilityreports'),
      crdListPath('aquasecurity.github.io', 'v1alpha1', 'configauditreports'),
    ],
  )
  const posture = postureState.phase === 'ready' ? postureState.data : null
  // Tetragon's own Prometheus metrics — nothing to watch, same cadence as Falco.
  const { state: tetraState, reload: reloadTetra } = useLiveResource<TetragonActivity | null>(
    () => frame.cluster.tetragon(),
    [],
    [],
    20_000,
  )
  const tetra = tetraState.phase === 'ready' ? tetraState.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ShieldWarning className="text-primary" />
            Security — runtime threats (Falco)
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={() => {
                reload()
                reloadPosture()
                reloadTetra()
              }}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {sec && (
          <CardContent className="flex flex-wrap items-center gap-3">
            <div className="mr-4">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Detections</div>
              <div className="font-mono font-bold text-2xl text-foreground">{sec.total}</div>
            </div>
            {Object.entries(sec.byPriority)
              .sort()
              .map(([p, n]) => (
                <Badge key={p} variant="outline" className={`font-mono text-[11px] border-current ${TONE[p.toLowerCase()] ?? 'text-foreground'}`}>
                  {n} {p}
                </Badge>
              ))}
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No Falco detections (or Falco not deployed)." />

      {sec && sec.events.length > 0 && (
        <Card>
          <CardHeader><CardTitle className="font-mono text-base">Detections by rule</CardTitle></CardHeader>
          <CardContent className="space-y-2">
            {sec.events.map((e, i) => (
              <div key={`${e.rule}-${e.pod}-${i}`} className="flex flex-wrap items-center gap-2 rounded border border-border bg-secondary/30 px-3 py-2">
                <Warning size={14} className={TONE[e.priority.toLowerCase()] ?? 'text-foreground'} />
                <span className="font-mono text-xs font-bold flex-1 min-w-[12rem]">{e.rule}</span>
                <Badge variant="outline" className={`font-mono text-[10px] border-current ${TONE[e.priority.toLowerCase()] ?? 'text-foreground'}`}>{e.priority}</Badge>
                <span className="font-mono text-[10px] text-muted-foreground">{e.source}</span>
                <span className="font-mono text-[10px] text-muted-foreground truncate max-w-[16rem]">
                  {e.namespace ? `${e.namespace}/${e.pod}` : e.node}
                </span>
                <Badge variant="secondary" className="font-mono text-[10px]">×{e.count}</Badge>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      {/* ── Posture (trivy-operator): image CVEs + workload misconfigs ── */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <Package size={16} className="text-primary" /> Image vulnerabilities
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <LiveStates state={postureState} emptyLabel="trivy-operator not deployed." />
            {posture && (
              <>
                <SevRow s={posture.vulns} suffix={`across ${posture.vulns.images} images`} />
                {posture.topImages.length === 0 ? (
                  <p className="text-[11px] text-muted-foreground font-mono">
                    No critical/high CVEs (or image scans still running).
                  </p>
                ) : (
                  posture.topImages.map((img) => (
                    <div key={img.image} className="flex items-center gap-2 text-[11px] font-mono">
                      <span className="flex-1 truncate">{img.image}</span>
                      {img.critical > 0 && <Badge variant="outline" className="border-current text-destructive text-[10px]">{img.critical} crit</Badge>}
                      {img.high > 0 && <Badge variant="outline" className="border-current text-warning text-[10px]">{img.high} high</Badge>}
                    </div>
                  ))
                )}
              </>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <Wrench size={16} className="text-primary" /> Workload misconfigs
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <LiveStates state={postureState} emptyLabel="trivy-operator not deployed." />
            {posture && (
              <>
                <SevRow s={posture.misconfigs} suffix={`across ${posture.misconfigs.resources} resources`} />
                {posture.topChecks.map((c) => (
                  <div key={c.id} className="flex items-center gap-2 text-[11px] font-mono">
                    <span className="flex-1 truncate" title={c.id}>{c.title}</span>
                    <Badge variant="outline" className={`border-current text-[10px] ${SEV_TONE[c.severity.toLowerCase()] ?? 'text-foreground'}`}>{c.severity.toLowerCase()}</Badge>
                    <Badge variant="secondary" className="text-[10px]">×{c.count}</Badge>
                  </div>
                ))}
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* ── Network & process activity (Tetragon eBPF) ── */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base flex items-center gap-2">
            <Network size={16} className="text-primary" /> Network &amp; process activity (Tetragon)
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <LiveStates state={tetraState} emptyLabel="Tetragon not deployed." />
          {tetra && (
            <>
              <div className="flex flex-wrap gap-2 text-[11px] font-mono">
                <Badge variant="outline" className="border-current text-accent">{tetra.network.toLocaleString()} net connects</Badge>
                <Badge variant="outline" className="border-current text-foreground">{tetra.exec.toLocaleString()} process exec</Badge>
                <Badge variant="outline" className="border-current text-muted-foreground">{tetra.exit.toLocaleString()} exit</Badge>
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div className="space-y-1">
                  <div className="text-[10px] text-muted-foreground uppercase tracking-wide">Top network talkers</div>
                  {tetra.topNetwork.length === 0 ? (
                    <p className="text-[11px] text-muted-foreground font-mono">No network connects captured yet.</p>
                  ) : (
                    tetra.topNetwork.map((n) => (
                      <div key={`${n.workload}-${n.namespace}`} className="flex items-center gap-2 text-[11px] font-mono">
                        <span className="flex-1 truncate">{n.namespace}/{n.workload}</span>
                        <Badge variant="secondary" className="text-[10px]">{n.count}</Badge>
                      </div>
                    ))
                  )}
                </div>
                <div className="space-y-1">
                  <div className="text-[10px] text-muted-foreground uppercase tracking-wide">Top exec'd binaries</div>
                  {tetra.topExec.map((e) => (
                    <div key={`${e.binary}-${e.workload}`} className="flex items-center gap-2 text-[11px] font-mono">
                      <span className="flex-1 truncate">{e.binary}</span>
                      <span className="text-muted-foreground truncate max-w-[8rem]">{e.workload}</span>
                      <Badge variant="secondary" className="text-[10px]">{e.count}</Badge>
                    </div>
                  ))}
                </div>
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function SevRow({ s, suffix }: { s: { critical: number; high: number; medium: number; low: number }; suffix: string }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      {(['critical', 'high', 'medium', 'low'] as const).map((k) => (
        <Badge key={k} variant="outline" className={`font-mono text-[11px] border-current ${SEV_TONE[k]}`}>
          {s[k]} {k}
        </Badge>
      ))}
      <span className="text-[10px] text-muted-foreground font-mono">{suffix}</span>
    </div>
  )
}
