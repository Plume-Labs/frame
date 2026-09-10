import { useMemo, useState } from 'react'
import {
  ArrowClockwise,
  CaretDown,
  CaretRight,
  TreeStructure,
} from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { LiveStates } from '@/components/LiveStates'
import { PodDetailPanel, type PodSelection } from '@/components/workloads/PodDetailPanel'
import { useLiveResource } from '@/hooks/useLiveResource'
import { isAdminToken } from '@/lib/auth'
import { createFrameClient, workloadWatchPaths } from '@/lib/frame-sdk'
import type { NamespaceNode, WorkloadPod } from '@/lib/workloads'

const frame = createFrameClient()

const PHASE_TONE: Record<string, string> = {
  Running: 'text-accent',
  Succeeded: 'text-muted-foreground',
  Pending: 'text-primary',
  Failed: 'text-destructive',
  Unknown: 'text-destructive',
}

/**
 * Every workload on the cluster: namespace → controller → pods, with a detail
 * panel per pod.
 *
 * Infrastructure namespaces are folded by default (see
 * `isInfrastructureNamespace` in `@/lib/workloads`) so what someone came for is
 * at the top. That is presentation only — RBAC is the real filter, and a
 * namespace this list has never heard of still appears if the apiserver
 * returns it.
 */
export function WorkloadsView() {
  const { state, reload } = useLiveResource<NamespaceNode[]>(
    () => frame.workloads.tree(),
    [],
    workloadWatchPaths(),
  )
  const tree = state.phase === 'ready' ? state.data : []

  const [showInfrastructure, setShowInfrastructure] = useState(false)
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState<Set<string>>(new Set())
  const [selection, setSelection] = useState<PodSelection | undefined>()

  // The admin gate on the terminal tab. A courtesy, not a control: the token
  // is decoded rather than verified, and `create pods/exec` is refused
  // server-side for a non-admin whatever this renders.
  const admin = useMemo(() => {
    const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
    return typeof token === 'string' ? isAdminToken(token) : false
  }, [])

  const q = query.trim().toLowerCase()
  const visible = useMemo(
    () =>
      tree
        .filter((ns) => showInfrastructure || !ns.infrastructure)
        .map((ns) => {
          if (!q) return ns
          return {
            ...ns,
            controllers: ns.controllers.filter(
              (c) =>
                c.controller.name.toLowerCase().includes(q) ||
                ns.namespace.toLowerCase().includes(q) ||
                c.pods.some((p) => p.name.toLowerCase().includes(q)),
            ),
            barePods: ns.barePods.filter(
              (p) => p.name.toLowerCase().includes(q) || ns.namespace.toLowerCase().includes(q),
            ),
          }
        })
        .filter((ns) => !q || ns.controllers.length > 0 || ns.barePods.length > 0),
    [tree, showInfrastructure, q],
  )

  const toggle = (key: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  const podRow = (pod: WorkloadPod, controllerName?: string) => (
    <button
      key={`${pod.namespace}/${pod.name}`}
      type="button"
      onClick={() =>
        setSelection({
          pod,
          controller: tree
            .find((n) => n.namespace === pod.namespace)
            ?.controllers.find((c) => c.controller.name === controllerName)?.controller,
        })
      }
      className="w-full flex items-center gap-3 pl-12 pr-3 py-1.5 text-left hover:bg-muted/50 font-mono text-xs"
    >
      <span className={PHASE_TONE[pod.phase] ?? 'text-muted-foreground'}>●</span>
      <span className="flex-1 truncate">{pod.name}</span>
      {pod.restarts > 0 && (
        <Badge variant="outline" className="font-mono text-[10px] border-current text-destructive">
          {pod.restarts} restarts
        </Badge>
      )}
      <span className="text-muted-foreground w-24 truncate">{pod.nodeName || '—'}</span>
    </button>
  )

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <TreeStructure className="text-primary" />
            Workloads
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
        {state.phase === 'ready' && (
          <CardContent className="space-y-3">
            <div className="flex flex-wrap items-center gap-4">
              <Input
                placeholder="Filter by namespace, controller or pod…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className="max-w-xs font-mono text-xs"
              />
              <div className="flex items-center gap-2">
                <Switch
                  id="show-infra"
                  checked={showInfrastructure}
                  onCheckedChange={setShowInfrastructure}
                />
                <Label htmlFor="show-infra" className="font-mono text-xs text-muted-foreground">
                  Show infrastructure namespaces
                </Label>
              </div>
            </div>

            <div className="border rounded-md divide-y">
              {visible.map((ns) => {
                const nsOpen = open.has(ns.namespace) || q !== ''
                return (
                  <div key={ns.namespace}>
                    <button
                      type="button"
                      onClick={() => toggle(ns.namespace)}
                      className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-muted/50 font-mono text-xs"
                    >
                      {nsOpen ? <CaretDown size={12} /> : <CaretRight size={12} />}
                      <span className="font-medium">{ns.namespace}</span>
                      {ns.infrastructure && (
                        <Badge variant="outline" className="font-mono text-[10px]">
                          infrastructure
                        </Badge>
                      )}
                      <span className="ml-auto text-muted-foreground">
                        {ns.controllers.length} controllers · {ns.podCount} pods
                      </span>
                    </button>

                    {nsOpen &&
                      ns.controllers.map((c) => {
                        const key = `${ns.namespace}/${c.controller.kind}/${c.controller.name}`
                        const cOpen = open.has(key) || q !== ''
                        return (
                          <div key={key}>
                            <button
                              type="button"
                              onClick={() => toggle(key)}
                              className="w-full flex items-center gap-2 pl-8 pr-3 py-1.5 text-left hover:bg-muted/50 font-mono text-xs"
                            >
                              {cOpen ? <CaretDown size={12} /> : <CaretRight size={12} />}
                              <Badge variant="outline" className="font-mono text-[10px]">
                                {c.controller.kind}
                              </Badge>
                              <span className="flex-1 truncate">{c.controller.name}</span>
                              <span className="text-muted-foreground">
                                {c.controller.readyReplicas}/{c.controller.desiredReplicas}
                              </span>
                            </button>
                            {cOpen && c.pods.map((p) => podRow(p, c.controller.name))}
                          </div>
                        )
                      })}

                    {nsOpen && ns.barePods.length > 0 && (
                      <div>
                        <div className="pl-8 pr-3 py-1.5 font-mono text-[10px] text-muted-foreground">
                          Pods with no controller
                        </div>
                        {ns.barePods.map((p) => podRow(p))}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No workloads are readable with your permissions." />

      {selection && (
        <PodDetailPanel
          selection={selection}
          admin={admin}
          onChanged={reload}
          onClose={() => setSelection(undefined)}
        />
      )}
    </div>
  )
}
