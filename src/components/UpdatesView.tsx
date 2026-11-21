import { ArrowClockwise, ArrowsClockwise, CheckCircle, Warning } from '@phosphor-icons/react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'
import { FRAME_SYSTEM_NAMESPACE, UPDATE_WATCH_PATHS, createFrameClient, type RunningComponent } from '@/lib/frame-sdk'
import { isTerminal as installTerminal } from '@/lib/installs'
import { isTerminal as replacementTerminal } from '@/lib/osd-replacement'
import { countUnready, disturbances, parseImage, summarizeVersions, type ActivityCounts } from '@/lib/updates'

const frame = createFrameClient()

interface UpdatesData {
  components: RunningComponent[]
  activity: ActivityCounts
}

async function load(): Promise<UpdatesData> {
  const [components, jobs, installs, replacements] = await Promise.all([
    frame.updates.running(),
    frame.jobs.list(),
    frame.installs.list(),
    frame.osdReplacements.list(),
  ])
  return {
    components,
    activity: {
      runningJobs: jobs.items.filter((j) => j.status === 'running').length,
      installsInProgress: installs.filter((i) => !installTerminal(i.phase)).length,
      osdReplacementsActive: replacements.filter((r) => !replacementTerminal(r.phase)).length,
      unreadyComponents: countUnready(components),
    },
  }
}

// Component changes arrive by watch; jobs, installs and replacements by a slow
// poll, since the screen only needs a recent answer to "is now a good moment".
const WATCHED = [...UPDATE_WATCH_PATHS]

export function UpdatesView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<UpdatesData>(load, [], WATCHED, 30000)
  const ns = FRAME_SYSTEM_NAMESPACE

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ArrowsClockwise className="text-primary" />
            {t('updates.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={() => reload()}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('updates.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">{t('updates.intro', { ns })}</p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel={t('updates.empty', { ns })} />

      {state.phase === 'ready' && state.data.components.length === 0 && (
        <Card>
          <CardContent className="py-10 text-center font-mono text-sm text-muted-foreground">
            {t('updates.empty', { ns })}
          </CardContent>
        </Card>
      )}

      {state.phase === 'ready' && state.data.components.length > 0 && <Body data={state.data} />}
    </div>
  )
}

function Body({ data }: { data: UpdatesData }) {
  const { t } = useI18n()
  const summary = summarizeVersions(data.components)
  const reasons = disturbances(data.activity)

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base">{t('updates.version.title')}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          <p className="flex items-center gap-2">
            {summary.mixed ? (
              <Warning className="text-warning" />
            ) : (
              <CheckCircle className="text-primary" />
            )}
            {summary.mixed
              ? t('updates.version.mixed', { versions: summary.versions.join(', ') })
              : t('updates.version.single', { version: summary.dominant })}
          </p>
          <p className="text-muted-foreground">{t('updates.available')}</p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base">{t('updates.moment.title')}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          {reasons.length === 0 ? (
            <p className="flex items-center gap-2">
              <CheckCircle className="text-primary" />
              {t('updates.moment.ok')}
            </p>
          ) : (
            <>
              <p className="flex items-center gap-2">
                <Warning className="text-warning" />
                {t('updates.moment.not')}
              </p>
              <ul className="list-disc pl-6">
                {reasons.map((r) => (
                  <li key={r.key}>{t(`updates.reason.${r.key}` as MessageKey, { count: r.count })}</li>
                ))}
              </ul>
            </>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base">{t('updates.components.title')}</CardTitle>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <table className="w-full text-sm font-mono">
            <thead>
              <tr className="text-left text-muted-foreground">
                <th className="py-1 pr-4">{t('updates.col.name')}</th>
                <th className="py-1 pr-4">{t('updates.col.image')}</th>
                <th className="py-1 pr-4">{t('updates.col.version')}</th>
                <th className="py-1">{t('updates.col.ready')}</th>
              </tr>
            </thead>
            <tbody>
              {data.components.map((c) => {
                const img = parseImage(c.image)
                return (
                  <tr key={`${c.kind}/${c.name}`} className="border-t border-border">
                    <td className="py-1 pr-4">{c.name}</td>
                    <td className="py-1 pr-4 break-all">{img.repository}</td>
                    <td className="py-1 pr-4">
                      <Badge variant="outline">{img.version || t('updates.untagged')}</Badge>
                    </td>
                    <td className="py-1">
                      {c.readyReplicas}/{c.desiredReplicas}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </CardContent>
      </Card>
    </>
  )
}
