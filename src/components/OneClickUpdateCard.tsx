import { useState } from 'react'
import { ArrowCircleUp, ArrowUUpLeft, CheckCircle, Warning } from '@phosphor-icons/react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'
import { createFrameClient, type ReleaseView, type UpdateRequestView } from '@/lib/frame-sdk'
import { activeRequest, availableRelease, currentVersion, rollbackTarget, type Disturbance } from '@/lib/updates'

const frame = createFrameClient()

type Channel = 'stable' | 'beta'

/**
 * The one-click update. The console decides nothing about safety: it files a
 * FrameUpdate and shows what the in-cluster updater answers. "Force" only
 * lifts the work-in-flight check, and asks for the version typed back first.
 */
export function OneClickUpdateCard({
  releases,
  requests,
  reasons,
  onChanged,
}: {
  releases: ReleaseView[]
  requests: UpdateRequestView[]
  reasons: Disturbance[]
  onChanged: () => void
}) {
  const { t } = useI18n()
  const [channel, setChannel] = useState<Channel>('stable')
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)

  const current = currentVersion(requests, releases)
  const next = availableRelease(releases, current, channel)
  const active = activeRequest(requests)
  const back = rollbackTarget(requests, releases, current)
  const needsForce = reasons.length > 0
  const confirmed = !needsForce || (next !== null && typed.trim() === next.version)

  async function run(action: () => Promise<void>, ok: MessageKey) {
    setBusy(true)
    try {
      await action()
      toast.success(t(ok))
      setTyped('')
      onChanged()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <ArrowCircleUp className="text-primary" />
          {t('updates.oneclick.title')}
          <select
            className="ml-auto h-8 rounded-md border border-border bg-background px-2 font-mono text-xs"
            value={channel}
            onChange={(e) => setChannel(e.target.value as Channel)}
            aria-label={t('updates.oneclick.channel')}
          >
            <option value="stable">stable</option>
            <option value="beta">beta</option>
          </select>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        {releases.length === 0 && <p className="text-muted-foreground">{t('updates.oneclick.none')}</p>}

        {releases.length > 0 && (
          <p className="flex items-center gap-2">
            {next ? <ArrowCircleUp className="text-primary" /> : <CheckCircle className="text-primary" />}
            {next
              ? t('updates.oneclick.available', { version: next.version, current: current || '?' })
              : t('updates.oneclick.upToDate', { version: current || '?' })}
          </p>
        )}

        {next && next.notes && <p className="whitespace-pre-line rounded-md border border-border bg-muted p-3 text-xs">{next.notes}</p>}

        {active && <Progress request={active} />}

        {next && !active && (
          <div className="space-y-2">
            {needsForce && (
              <div className="space-y-2">
                <p className="flex items-center gap-2 text-warning">
                  <Warning />
                  {t('updates.oneclick.force')}
                </p>
                <ul className="list-disc pl-6">
                  {reasons.map((r) => (
                    <li key={r.key}>{t(`updates.reason.${r.key}` as MessageKey, { count: r.count })}</li>
                  ))}
                </ul>
                <input
                  className="h-9 rounded-md border border-border bg-background px-3 font-mono text-sm"
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  placeholder={next.version}
                  aria-label={t('updates.oneclick.typeVersion', { version: next.version })}
                  spellCheck={false}
                />
              </div>
            )}
            <Button
              size="sm"
              className="font-mono gap-1.5"
              disabled={busy || !confirmed}
              onClick={() => run(() => frame.updates.requestUpdate(next.version, needsForce), 'updates.oneclick.requested')}
            >
              <ArrowCircleUp />
              {needsForce ? t('updates.oneclick.buttonForce') : t('updates.oneclick.button')}
            </Button>
          </div>
        )}

        {back && !active && (
          <Button
            variant="outline"
            size="sm"
            className="font-mono gap-1.5"
            disabled={busy}
            onClick={() => run(() => frame.updates.requestRollback(), 'updates.oneclick.rollbackRequested')}
          >
            <ArrowUUpLeft />
            {t('updates.oneclick.rollback', { version: back })}
          </Button>
        )}

        {requests.length > 0 && <History requests={requests.slice(0, 5)} />}
      </CardContent>
    </Card>
  )
}

function Progress({ request }: { request: UpdateRequestView }) {
  const { t } = useI18n()
  return (
    <div className="space-y-1 rounded-md border border-border p-3" role="status">
      <p className="font-mono">
        {t('updates.oneclick.inProgress', { version: request.targetVersion || '↩' })} · {request.phase || 'Pending'}
        {request.step ? ` · ${request.step}` : ''}
      </p>
      {request.components.length > 0 && (
        <ul className="font-mono text-xs">
          {request.components.map((c) => (
            <li key={c.name}>
              {c.name}: {c.state}
            </li>
          ))}
        </ul>
      )}
      {request.message && <p className="text-muted-foreground">{request.message}</p>}
    </div>
  )
}

function History({ requests }: { requests: UpdateRequestView[] }) {
  const { t } = useI18n()
  return (
    <div className="space-y-1">
      <p className="text-muted-foreground">{t('updates.oneclick.history')}</p>
      <ul className="space-y-1 font-mono text-xs">
        {requests.map((r) => (
          <li key={r.name} className="flex flex-wrap items-center gap-2">
            <Badge variant="outline">{r.phase || 'Pending'}</Badge>
            <span>
              {r.rollback ? '↩ ' : ''}
              {r.fromVersion || '?'} → {r.targetVersion || '?'}
            </span>
            {r.message && <span className="text-muted-foreground">{r.message}</span>}
          </li>
        ))}
      </ul>
    </div>
  )
}
