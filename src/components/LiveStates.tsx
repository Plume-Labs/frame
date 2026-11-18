import { Card, CardContent } from '@/components/ui/card'
import { Warning } from '@phosphor-icons/react'
import { LiveState } from '@/hooks/useLiveResource'

/**
 * Renders the loading / error / empty chrome for a live cluster view. Returns
 * null once data is ready and non-empty, so the caller renders its own content.
 */
export function LiveStates<T>({
  state,
  emptyLabel,
}: {
  state: LiveState<T>
  emptyLabel: string
}) {
  if (state.phase === 'loading') {
    return (
      <Card>
        <CardContent className="py-10 text-center font-mono text-sm text-muted-foreground">
          Reading from the Kubernetes API…
        </CardContent>
      </Card>
    )
  }

  if (state.phase === 'error') {
    return (
      <Card className="border-destructive/40">
        <CardContent className="py-8 space-y-3 text-center">
          <Warning className="mx-auto text-destructive" size={28} />
          <div className="font-mono text-sm text-foreground">Cannot reach the Kubernetes API</div>
          <div className="text-xs text-muted-foreground max-w-md mx-auto">{state.message}</div>
          <div className="text-xs text-muted-foreground">
            The UI reaches <span className="font-mono">/api</span> through the in-pod
            <span className="font-mono"> frame-uiproxy</span>, under your own identity. Check that
            the proxy is running; local development is covered in docs/development.md.
          </div>
        </CardContent>
      </Card>
    )
  }

  if (Array.isArray(state.data) && state.data.length === 0) {
    return (
      <Card>
        <CardContent className="py-10 text-center font-mono text-sm text-muted-foreground">
          {emptyLabel}
        </CardContent>
      </Card>
    )
  }

  return null
}

/**
 * Shown above a view whose last refresh failed but which still has data: the
 * numbers below are the last good read, and the view says so.
 */
export function StaleNotice({ state }: { state: { phase: string; staleError?: string; lastRead?: number } }) {
  if (state.phase !== 'ready' || !state.staleError) return null
  return (
    <div
      role="status"
      className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs font-mono"
    >
      <Warning size={16} className="mt-0.5 shrink-0 text-warning" />
      <span>
        Refresh failed, showing the last successful read
        {state.lastRead ? ` (${new Date(state.lastRead).toLocaleTimeString()})` : ''}: {state.staleError}
      </span>
    </div>
  )
}
