import { Card, CardContent } from '@/components/ui/card'
import { Warning } from '@phosphor-icons/react'
import { LiveState } from '@/hooks/useLiveResource'
import { describeLoadError } from '@/lib/load-error'

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
    const copy = describeLoadError(state.status)
    return (
      <Card className="border-destructive/40" role="alert">
        <CardContent className="py-8 space-y-3 text-center">
          <Warning className="mx-auto text-destructive" size={28} />
          <div className="font-mono text-sm text-foreground">{copy.title}</div>
          <div className="text-xs text-muted-foreground max-w-md mx-auto">{state.message}</div>
          <div className="text-xs text-muted-foreground max-w-md mx-auto">{copy.hint}</div>
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
 * numbers below are the last good read, and the view says so. `label` names the
 * source when a screen shows several. The live region stays mounted (empty when
 * all is well) so screen readers announce the text when it appears.
 */
export function StaleNotice({
  state,
  label,
}: {
  state: { phase: string; staleError?: string; lastRead?: number }
  label?: string
}) {
  const stale = state.phase === 'ready' && state.staleError ? state : null
  return (
    <div role="status" aria-live="polite">
      {stale && (
        <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs font-mono">
          <Warning size={16} aria-hidden="true" className="mt-0.5 shrink-0 text-warning" />
          <span>
            {label ? `${label}: ` : ''}refresh failed, showing the last successful read
            {stale.lastRead ? ` (${new Date(stale.lastRead).toLocaleTimeString()})` : ''}: {stale.staleError}
          </span>
        </div>
      )}
    </div>
  )
}
