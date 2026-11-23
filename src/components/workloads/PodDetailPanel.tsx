import { X } from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { LogsTab } from '@/components/workloads/LogsTab'
import { TerminalTab } from '@/components/workloads/TerminalTab'
import { WorkloadActions } from '@/components/workloads/WorkloadActions'
import { YamlTab } from '@/components/workloads/YamlTab'
import { formatAge } from '@/lib/thresholds'
import type { WorkloadController, WorkloadPod } from '@/lib/workloads'
import { useI18n } from '@/i18n/context'

export interface PodSelection {
  pod: WorkloadPod
  /** The controller the pod belongs to, when the tree could place it. */
  controller?: WorkloadController
}

/**
 * One pod: what it is, and the three ways of working on it.
 *
 * `onChanged` is the tree's own reload — a write here changes what the tree
 * shows, and a panel that leaves a stale list behind is how someone restarts
 * the same thing twice.
 */
export function PodDetailPanel({
  selection,
  admin,
  onChanged,
  onClose,
}: {
  selection: PodSelection
  admin: boolean
  onChanged: () => void
  onClose: () => void
}) {
  const { t, tn } = useI18n()
  const { pod } = selection

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <span className="truncate">
            {pod.namespace}/{pod.name}
          </span>
          <Badge variant="outline" className="font-mono text-[10px]">
            {pod.phase}
          </Badge>
          {pod.restarts > 0 && (
            <Badge variant="outline" className="font-mono text-[10px] border-current text-destructive">
              {tn('wl.restarts', pod.restarts)}
            </Badge>
          )}
          <Button variant="ghost" size="sm" className="ml-auto" onClick={onClose} aria-label={t('common.close')}>
            <X />
          </Button>
        </CardTitle>
        <div className="font-mono text-[10px] text-muted-foreground flex flex-wrap gap-x-4">
          <span>{t('wl.node', { v: pod.nodeName || '—' })}</span>
          <span>{t('wl.containers', { v: pod.containers.join(', ') || '—' })}</span>
          <span>
            {t('wl.age', { v: pod.createdAt ? formatAge(new Date(pod.createdAt).getTime()) : '—' })}
          </span>
          {selection.controller && (
            <span>
              {t('wl.controlledBy', { kind: selection.controller.kind.toLowerCase(), name: selection.controller.name })}
            </span>
          )}
        </div>
        <WorkloadActions selection={selection} onChanged={onChanged} />
      </CardHeader>
      <CardContent>
        <Tabs defaultValue="logs" className="gap-4">
          <TabsList>
            <TabsTrigger value="logs" className="font-mono text-xs">
              {t('wl.tab.logs')}
            </TabsTrigger>
            <TabsTrigger value="terminal" className="font-mono text-xs">
              {t('wl.tab.terminal')}
              <span className="text-[9px] uppercase tracking-wider text-muted-foreground">{t('wl.experimental')}</span>
            </TabsTrigger>
            <TabsTrigger value="yaml" className="font-mono text-xs">
              YAML
            </TabsTrigger>
          </TabsList>
          <TabsContent value="logs">
            <LogsTab pod={pod} />
          </TabsContent>
          <TabsContent value="terminal">
            <TerminalTab pod={pod} admin={admin} />
          </TabsContent>
          <TabsContent value="yaml">
            <YamlTab kind="Pod" namespace={pod.namespace} name={pod.name} admin={admin} onSaved={onChanged} />
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}
