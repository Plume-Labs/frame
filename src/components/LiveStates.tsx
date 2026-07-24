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
            The UI proxies <span className="font-mono">/api</span> to the apiserver via its
            ServiceAccount. In dev, run <span className="font-mono">kubectl proxy --port=8001</span>.
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
