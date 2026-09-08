import { useState } from 'react'
import { TaskRecord, createFrameClient, frameListPath } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { useNavigation } from '@/hooks/useNavigation'
import { formatAge } from '@/lib/thresholds'
import {
  ArrowClockwise,
  ArrowSquareOut,
  CheckCircle,
  ListChecks,
  Spinner,
  XCircle,
} from '@phosphor-icons/react'

const frame = createFrameClient()

const PHASE: Record<TaskRecord['phase'], { tone: string; icon: React.ReactNode }> = {
  Running: { tone: 'text-primary', icon: <Spinner className="animate-spin" size={12} /> },
  Succeeded: { tone: 'text-accent', icon: <CheckCircle size={12} /> },
  Failed: { tone: 'text-destructive', icon: <XCircle size={12} /> },
}

/**
 * Where a `ref` (a FrameJob, a TalosUpgrade, a Velero backup, …) is shown on
 * the sidebar. Deliberately partial: a resource this map doesn't know about
 * still shows as plain text rather than a broken link.
 */
const REF_SCREEN: Record<string, { screen: string; tab?: string }> = {
  framejobs: { screen: 'jobs', tab: 'jobs' },
  framenodes: { screen: 'nodes', tab: 'provisioned-nodes' },
  nodes: { screen: 'nodes', tab: 'nodes' },
  talosupgrades: { screen: 'nodes', tab: 'provisioned-nodes' },
  talosmachineconfigs: { screen: 'nodes', tab: 'provisioned-nodes' },
  backups: { screen: 'capacity', tab: 'resilience' },
}

function RefLink({ taskRef: r }: { taskRef: TaskRecord['ref'] }) {
  const { navigate } = useNavigation()
  if (!r) return null
  const label = r.namespace ? `${r.namespace}/${r.resource}/${r.name}` : `${r.resource}/${r.name}`
  const target = REF_SCREEN[r.resource]
  if (!target) return <span className="text-muted-foreground">{label}</span>
  return (
    <button
      type="button"
      onClick={() => navigate(target.screen, target.tab)}
      className="inline-flex items-center gap-1 text-primary hover:underline"
    >
      {label}
      <ArrowSquareOut size={11} />
    </button>
  )
}

/**
 * Who did what through the UI proxy, and how it ended — the FrameTask trail
 * every mutating request leaves, including the refused ones. Modelled on
 * ClusterEventsView: one live-watched list, filtered client-side.
 */
export function TasksView() {
  const { state, reload } = useLiveResource<TaskRecord[]>(
    () => frame.tasks.list(200),
    [],
    [frameListPath('frametasks')],
  )
  const tasks = state.phase === 'ready' ? state.data : []
  const [query, setQuery] = useState('')

  const q = query.trim().toLowerCase()
  const filtered = q
    ? tasks.filter((t) => t.user.toLowerCase().includes(q) || t.action.toLowerCase().includes(q))
    : tasks

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ListChecks className="text-primary" />
            Tasks
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && tasks.length > 0 && (
          <CardContent className="space-y-3">
            <Input
              placeholder="Filter by user or action…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="max-w-xs font-mono text-xs"
            />
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>User</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>Outcome</TableHead>
                  <TableHead>Age</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((t) => {
                  const phase = PHASE[t.phase]
                  return (
                    <TableRow key={t.name}>
                      <TableCell className="font-mono text-xs">{t.user}</TableCell>
                      <TableCell className="font-mono text-xs">{t.action}</TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {t.ref ? <RefLink taskRef={t.ref} /> : t.target}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={`gap-1 font-mono text-[10px] border-current ${phase.tone}`}>
                          {phase.icon}
                          {t.phase}
                          {t.httpCode ? ` · ${t.httpCode}` : ''}
                        </Badge>
                      </TableCell>
                      <TableCell className="font-mono text-[10px] text-muted-foreground">
                        {t.startedAt ? formatAge(new Date(t.startedAt).getTime()) : '—'}
                      </TableCell>
                    </TableRow>
                  )
                })}
                {filtered.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={5} className="text-center text-xs text-muted-foreground py-6">
                      No tasks match “{query}”.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No writes made through the UI yet." />
    </div>
  )
}
