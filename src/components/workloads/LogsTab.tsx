import { useCallback, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { createFrameClient } from '@/lib/frame-sdk'
import { pumpLogLines } from '@/lib/pod-logs'
import type { WorkloadPod } from '@/lib/workloads'

const frame = createFrameClient()

/** How many lines to keep in the pane. A followed log is unbounded; the DOM is not. */
const MAX_LINES = 5_000

/**
 * One container's logs, live or from the instance that died.
 *
 * Previous-container logs are the tab that matters most and the one a person
 * has to be *offered*: when a container has crash-looped, the cause is in the
 * instance that died, not in the one running now, and nothing about the
 * default view says so.
 *
 * Nothing here is recorded. A log read is a read, the recorder ignores reads by
 * design, and so there will be no FrameTask saying anyone opened this — which
 * is worth knowing, because a log is a likely place for a secret to be printed.
 * The `pods/log` grant starts at the operator tier for that reason.
 */
export function LogsTab({ pod }: { pod: WorkloadPod }) {
  const [container, setContainer] = useState(pod.containers[0] ?? '')
  const [follow, setFollow] = useState(true)
  const [previous, setPrevious] = useState(false)
  const [lines, setLines] = useState<string[]>([])
  const [error, setError] = useState<string | undefined>()
  const bottom = useRef<HTMLDivElement | null>(null)

  const key = `${pod.namespace}/${pod.name}/${container}/${follow}/${previous}`

  const read = useCallback(
    async (signal: AbortSignal) => {
      setLines([])
      setError(undefined)
      try {
        const res = await frame.workloads.logs({
          namespace: pod.namespace,
          pod: pod.name,
          container,
          follow,
          previous,
          tailLines: 500,
        })
        if (!res.ok) {
          setError(`${res.status} ${await res.text()}`)
          return
        }
        const body = res.body
        if (!body) return
        const reader = body.getReader()
        signal.addEventListener('abort', () => void reader.cancel().catch(() => {}))
        await pumpLogLines(reader, (line) => {
          if (signal.aborted) return
          setLines((prev) => (prev.length >= MAX_LINES ? [...prev.slice(1), line] : [...prev, line]))
        })
      } catch (e) {
        if (!signal.aborted) setError(e instanceof Error ? e.message : String(e))
      }
    },
    [pod.namespace, pod.name, container, follow, previous],
  )

  useEffect(() => {
    if (!container) return
    const controller = new AbortController()
    void read(controller.signal)
    // Aborting is what stops a followed stream when the container, the pod or
    // the tab changes. Without it every switch leaves its reader running and
    // the pane interleaves two containers.
    return () => controller.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  useEffect(() => {
    if (follow) bottom.current?.scrollIntoView({ block: 'end' })
  }, [lines, follow])

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-4">
        <Select value={container} onValueChange={setContainer}>
          <SelectTrigger className="w-56 font-mono text-xs">
            <SelectValue placeholder="Container" />
          </SelectTrigger>
          <SelectContent>
            {pod.containers.map((c) => (
              <SelectItem key={c} value={c} className="font-mono text-xs">
                {c}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="flex items-center gap-2">
          <Switch id="log-follow" checked={follow} onCheckedChange={setFollow} disabled={previous} />
          <Label htmlFor="log-follow" className="font-mono text-xs text-muted-foreground">
            Follow
          </Label>
        </div>

        <div className="flex items-center gap-2">
          <Switch id="log-previous" checked={previous} onCheckedChange={setPrevious} />
          <Label htmlFor="log-previous" className="font-mono text-xs text-muted-foreground">
            Previous container
          </Label>
        </div>

        <Button
          variant="outline"
          size="sm"
          className="ml-auto font-mono"
          onClick={() => setLines([])}
        >
          Clear
        </Button>
      </div>

      <p className="font-mono text-[10px] text-muted-foreground">
        Reading logs is not recorded. Anything printed here — including a secret — leaves no trace
        that it was read.
      </p>

      {error && <p className="font-mono text-xs text-destructive">{error}</p>}

      <ScrollArea className="h-96 border rounded-md bg-muted/20">
        <pre className="p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all">
          {lines.join('\n')}
        </pre>
        <div ref={bottom} />
      </ScrollArea>
    </div>
  )
}
