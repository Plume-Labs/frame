import type { AttentionItem, AttentionSeverity } from '@/lib/storage-attention'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { WarningCircle, CheckCircle } from '@phosphor-icons/react'

const DOT: Record<AttentionSeverity, string> = {
  critical: 'bg-destructive',
  warning: 'bg-warning',
  info: 'bg-muted-foreground',
}

const TEXT: Record<AttentionSeverity, string> = {
  critical: 'text-destructive',
  warning: 'text-warning',
  info: 'text-muted-foreground',
}

/** The "À traiter" list, worst first. Empty only when every source was read and nothing fired. */
export function AttentionBanner({ items }: { items: AttentionItem[] }) {
  return (
    <Card className={items.some((i) => i.severity === 'critical') ? 'border-destructive' : undefined}>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          {items.length === 0 ? <CheckCircle className="text-accent" /> : <WarningCircle className="text-warning" />}
          To address {items.length > 0 && <span className="text-muted-foreground text-xs">({items.length})</span>}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {items.length === 0 ? (
          <p className="text-xs font-mono text-muted-foreground">Nothing to address: every source read, no finding.</p>
        ) : (
          <ul className="space-y-1 text-xs font-mono">
            {items.map((i, n) => (
              <li key={`${i.code}-${n}`} className={`flex items-start gap-2 ${TEXT[i.severity]}`}>
                <span className={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${DOT[i.severity]}`} />
                <span>{i.text}</span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
