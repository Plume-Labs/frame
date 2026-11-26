import { useState } from 'react'
import { ArrowClockwise, ArrowsClockwise, CheckCircle, Copy, Warning } from '@phosphor-icons/react'
import { toast } from 'sonner'
import { config, type UpdateApp } from '@/lib/frame-config'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'
import {
  FRAME_SYSTEM_NAMESPACE,
  UPDATE_WATCH_PATHS,
  createFrameClient,
  FrameAPIError,
  type ReleaseView,
  type RunningComponent,
  type UpdateRequestView,
} from '@/lib/frame-sdk'
import { OneClickUpdateCard } from '@/components/OneClickUpdateCard'
import { isTerminal as installTerminal } from '@/lib/installs'
import { isTerminal as replacementTerminal } from '@/lib/osd-replacement'
import {
  buildPreparation,
  countUnready,
  disturbances,
  isCommitId,
  isVersionRef,
  parseImage,
  summarizeVersions,
  type ActivityCounts,
} from '@/lib/updates'

const frame = createFrameClient()

/** One extra application: its workloads, or why they could not be read. */
interface AppReading {
  app: UpdateApp
  components: RunningComponent[]
  error?: string
}

interface UpdatesData {
  components: RunningComponent[]
  apps: AppReading[]
  activity: ActivityCounts
  releases: ReleaseView[]
  requests: UpdateRequestView[]
  /** Why the update CRs could not be read, other than "not installed". */
  updateError: string
}

async function readApp(app: UpdateApp): Promise<AppReading> {
  try {
    return { app, components: await frame.updates.running(app.namespace) }
  } catch (e) {
    // One namespace the console may not read must not blank the whole screen.
    return { app, components: [], error: e instanceof Error ? e.message : String(e) }
  }
}

/** An install that predates the update CRDs answers 404: no releases, no error. Anything else is shown. */
async function readUpdateCRs(): Promise<{ releases: ReleaseView[]; requests: UpdateRequestView[]; error: string }> {
  try {
    const [releases, requests] = await Promise.all([frame.updates.releases(), frame.updates.requests()])
    return { releases, requests, error: '' }
  } catch (e) {
    if (e instanceof FrameAPIError && e.statusCode === 404) return { releases: [], requests: [], error: '' }
    return { releases: [], requests: [], error: e instanceof Error ? e.message : String(e) }
  }
}

async function load(): Promise<UpdatesData> {
  const [components, jobs, installs, replacements, apps, cr] = await Promise.all([
    frame.updates.running(),
    frame.jobs.list(),
    frame.installs.list(),
    frame.osdReplacements.list(),
    Promise.all(config().updates.apps.map(readApp)),
    readUpdateCRs(),
  ])
  return {
    components,
    apps,
    releases: cr.releases,
    requests: cr.requests,
    updateError: cr.error,
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

export function UpdatesView() {
  const { t } = useI18n()
  const appNamespaces = config().updates.apps.map((a) => a.namespace).join(',')
  const { state, reload } = useLiveResource<UpdatesData>(load, [appNamespaces], [...UPDATE_WATCH_PATHS], 30000)
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

      {state.phase === 'ready' && state.data.components.length > 0 && <Body data={state.data} onChanged={() => reload()} />}

      {state.phase === 'ready' &&
        state.data.apps.map((r) => <AppSection key={r.app.namespace} reading={r} activity={state.data.activity} />)}
    </div>
  )
}

function Body({ data, onChanged }: { data: UpdatesData; onChanged: () => void }) {
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
          <ComponentsTable components={data.components} />
        </CardContent>
      </Card>
      <OneClickUpdateCard
        releases={data.releases}
        requests={data.requests}
        reasons={reasons}
        runningImages={data.components.map((c) => c.image)}
        error={data.updateError}
        onChanged={onChanged}
      />
      <PrepareCard
        components={data.components}
        activity={data.activity}
        gitopsUrl={config().updates.gitopsUrl}
        template={config().updates.prepareCommands}
      />
    </>
  )
}

function ComponentsTable({ components }: { components: RunningComponent[] }) {
  const { t } = useI18n()
  return (
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
        {components.map((c) => {
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
  )
}

/** Another application on the cluster: what runs in its namespace, and its own preparation card. */
function AppSection({ reading, activity }: { reading: AppReading; activity: ActivityCounts }) {
  const { t } = useI18n()
  const { app, components, error } = reading
  const summary = summarizeVersions(components)
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base">{app.name}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 overflow-x-auto text-sm">
          {error ? (
            <p role="alert" className="flex items-center gap-2 text-destructive">
              <Warning />
              {t('updates.app.unreadable', { ns: app.namespace })}
            </p>
          ) : components.length === 0 ? (
            <p className="text-muted-foreground">{t('updates.app.empty', { ns: app.namespace })}</p>
          ) : (
            <>
              <p className="flex items-center gap-2">
                {summary.mixed ? <Warning className="text-warning" /> : <CheckCircle className="text-primary" />}
                {summary.mixed
                  ? t('updates.version.mixed', { versions: summary.versions.join(', ') })
                  : t('updates.version.single', { version: summary.dominant })}
              </p>
              <ComponentsTable components={components} />
            </>
          )}
        </CardContent>
      </Card>
      <PrepareCard
        name={app.name}
        components={components}
        activity={activity}
        gitopsUrl={app.gitopsUrl}
        template={app.prepareCommands}
        allowVersionRef
      />
    </>
  )
}

/**
 * Prepares an update, never performs one. The output is text to copy: the
 * cluster changes only through the site's GitOps pipeline, after review.
 * Hidden until the site configures it, since the steps are the site's.
 */
function PrepareCard({
  name,
  components,
  activity,
  gitopsUrl,
  template: prepareCommands,
  allowVersionRef,
}: {
  name?: string
  components: RunningComponent[]
  activity: ActivityCounts
  gitopsUrl: string
  template: string
  allowVersionRef?: boolean
}) {
  const { t } = useI18n()
  const [target, setTarget] = useState('')
  const [text, setText] = useState('')
  if (!gitopsUrl && !prepareCommands) return null

  const id = target.trim().toLowerCase()
  const valid = allowVersionRef ? isVersionRef(target.trim()) : isCommitId(id)

  function prepare() {
    setText(
      buildPreparation({
        name,
        allowVersionRef,
        target: allowVersionRef ? target.trim() : id,
        components,
        reasons: disturbances(activity),
        template: prepareCommands,
        gitopsUrl,
        now: new Date(),
      }),
    )
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      toast.success(t('updates.prepare.copied'))
    } catch {
      toast.error(t('updates.prepare.copyFailed'))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base">
          {name ? t('updates.prepare.titleFor', { name }) : t('updates.prepare.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <p className="text-muted-foreground">{allowVersionRef ? t('updates.prepare.introRef') : t('updates.prepare.intro')}</p>
        <div className="flex flex-wrap items-center gap-2">
          <input
            className="h-9 rounded-md border border-border bg-background px-3 font-mono text-sm"
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder={allowVersionRef ? t('updates.prepare.targetRef') : t('updates.prepare.target')}
            aria-label={allowVersionRef ? t('updates.prepare.targetRef') : t('updates.prepare.target')}
            spellCheck={false}
          />
          <Button size="sm" className="font-mono" onClick={prepare} disabled={!valid || !prepareCommands}>
            {t('updates.prepare.button')}
          </Button>
          {gitopsUrl && (
            <a className="text-primary underline" href={gitopsUrl} target="_blank" rel="noreferrer noopener">
              {t('updates.prepare.repo')}
            </a>
          )}
        </div>
        {target !== '' && !valid && (
          <p className="text-destructive">
            {allowVersionRef ? t('updates.prepare.invalidRef') : t('updates.prepare.invalid')}
          </p>
        )}
        {text && (
          <div className="space-y-2">
            <pre className="overflow-x-auto rounded-md border border-border bg-muted p-3 text-xs">{text}</pre>
            <Button variant="outline" size="sm" className="gap-1.5 font-mono" onClick={copy}>
              <Copy />
              {t('updates.prepare.copy')}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
