import { useState } from 'react'
import { toast } from 'sonner'
import {
  CONFIG_NAME,
  CONFIG_NAMESPACE,
  DEFAULT_CONFIG,
  type FrameConfig,
  type Integration,
  config,
  configStatus,
  saveConfig,
  isGitopsUrl,
  isPrepareCommands,
} from '@/lib/frame-config'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Gear, FloppyDisk, ArrowCounterClockwise, Warning } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'
import { INFERENCE_SERVICES } from '@/lib/inference-services'

type IntegrationKey = keyof FrameConfig['integrations']

/** Which screen each integration feeds, so a wrong value is traceable to what broke. */
const FEEDS: Record<IntegrationKey, MessageKey> = {
  cephOsd: 'settings.feeds.cephOsd',
  cephMon: 'settings.feeds.cephMon',
  alluxio: 'settings.feeds.alluxio',
  nodeExporter: 'settings.feeds.nodeExporter',
  dcgm: 'settings.feeds.dcgm',
  llamacpp: 'settings.feeds.llamacpp',
  llamacppVision: 'settings.feeds.llamacppVision',
  tei: 'settings.feeds.tei',
  falcosidekick: 'settings.feeds.falcosidekick',
  tetragon: 'settings.feeds.tetragon',
  prometheus: 'settings.feeds.prometheus',
  alertmanager: 'settings.feeds.alertmanager',
  sandboxPods: 'settings.feeds.sandboxPods',
}

/** The integrations the Inference panel operates, which alone take a Deployment name. */
const DEPLOYMENT_KEYS: ReadonlySet<IntegrationKey> = new Set(INFERENCE_SERVICES.map((s) => s.key))

const NAMESPACE_HELP: Record<keyof FrameConfig['namespaces'], MessageKey> = {
  ceph: 'settings.ns.ceph',
  velero: 'settings.ns.velero',
  argo: 'settings.ns.argo',
  volcanoCommands: 'settings.ns.volcanoCommands',
}

/**
 * Edit the cluster wiring the UI uses. Everything here was compiled in until
 * it moved to a ConfigMap, so this screen is what makes Frame portable to a
 * cluster whose components sit in different namespaces or answer on different
 * ports.
 */
export function SettingsView() {
  const { t } = useI18n()
  const [draft, setDraft] = useState<FrameConfig>(() => structuredClone(config()))
  const [saving, setSaving] = useState(false)
  const [resetOpen, setResetOpen] = useState(false)
  const status = configStatus()

  const updatesInvalid =
    (draft.updates.gitopsUrl.trim() !== '' && !isGitopsUrl(draft.updates.gitopsUrl)) ||
    (draft.updates.prepareCommands !== '' && !isPrepareCommands(draft.updates.prepareCommands))
  const dirty = JSON.stringify(draft) !== JSON.stringify(config())

  function setIntegration(key: IntegrationKey, patch: Partial<Integration>) {
    setDraft((d) => ({
      ...d,
      integrations: { ...d.integrations, [key]: { ...d.integrations[key], ...patch } },
    }))
  }

  async function save() {
    setSaving(true)
    try {
      await saveConfig(draft)
      toast.success(t('settings.saved'), {
        description: t('settings.savedHint'),
      })
      setDraft(structuredClone(config()))
    } catch (e) {
      toast.error(t('settings.saveFailed'), {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Gear className="text-primary" />
            {t('settings.title')}
            <div className="ml-auto flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                className="font-mono gap-1.5"
                onClick={() => setResetOpen(true)}
              >
                <ArrowCounterClockwise />
                {t('common.defaults')}
              </Button>
              <Button size="sm" className="font-mono gap-1.5" onClick={save} disabled={!dirty || saving || updatesInvalid}>
                <FloppyDisk />
                {saving ? t('common.saving') : t('common.save')}
              </Button>
            </div>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {t('settings.intro.before')}{' '}
            <span className="font-mono text-foreground">
              {CONFIG_NAMESPACE}/{CONFIG_NAME}
            </span>{' '}
            {t('settings.intro.after')}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant="outline"
              className={`font-mono text-[10px] border-current ${
                status.source === 'configmap' ? 'text-accent' : 'text-warning'
              }`}
            >
              {status.source === 'configmap'
                ? t('settings.source.configmap')
                : status.error
                  ? t('settings.source.unreadable')
                  : t('settings.source.nothingSaved')}
            </Badge>
            {status.error && (
              <span className="font-mono text-[10px] text-warning flex items-center gap-1">
                <Warning size={12} />
                {status.error}
              </span>
            )}
            {dirty && (
              <Badge variant="outline" className="font-mono text-[10px] text-warning border-current">
                {t('settings.unsaved')}
              </Badge>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">{t('settings.podProxied')}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {(Object.keys(draft.integrations) as IntegrationKey[]).map((key) => {
            const i = draft.integrations[key]
            return (
              <div key={key} className="rounded border border-border bg-secondary/30 p-3 space-y-2">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="font-mono text-sm font-bold">{key}</span>
                  <span className="font-mono text-[10px] text-muted-foreground">{t(FEEDS[key])}</span>
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field
                    id={`${key}-ns`}
                    label={t('settings.namespace')}
                    value={i.namespace}
                    placeholder={key === 'sandboxPods' ? t('settings.allNamespaces') : undefined}
                    onChange={(v) => setIntegration(key, { namespace: v })}
                  />
                  <Field
                    id={`${key}-sel`}
                    label={t('settings.selector')}
                    value={i.selector}
                    onChange={(v) => setIntegration(key, { selector: v })}
                  />
                  {i.port !== undefined ? (
                    <Field
                      id={`${key}-port`}
                      label={t('settings.port')}
                      type="number"
                      value={String(i.port)}
                      onChange={(v) => setIntegration(key, { port: Number(v) || 0 })}
                    />
                  ) : (
                    <div className="space-y-1.5">
                      <Label className="text-[10px] font-mono text-muted-foreground">{t('settings.port')}</Label>
                      <p className="text-[10px] font-mono text-muted-foreground pt-2">
                        {t('settings.noProxy')}
                      </p>
                    </div>
                  )}
                </div>
                {DEPLOYMENT_KEYS.has(key) && (
                  <Field
                    id={`${key}-deploy`}
                    label={t('settings.deployment')}
                    hint={t('settings.deploymentHint')}
                    value={i.deployment ?? ''}
                    placeholder={t('settings.deploymentPlaceholder')}
                    onChange={(v) => setIntegration(key, { deployment: v || undefined })}
                  />
                )}
              </div>
            )
          })}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">{t('settings.apiNamespaces')}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Field
            id="frame-ns"
            label={t('settings.frameCrs')}
            hint={t('settings.frameCrsHint')}
            value={draft.frameNamespace}
            onChange={(v) => setDraft((d) => ({ ...d, frameNamespace: v }))}
          />
          <Field
            id="task-ns"
            label={t('settings.frameTasks')}
            hint={t('settings.frameTasksHint')}
            value={draft.taskNamespace}
            onChange={(v) => setDraft((d) => ({ ...d, taskNamespace: v }))}
          />
          {(Object.keys(draft.namespaces) as Array<keyof FrameConfig['namespaces']>).map((key) => (
            <Field
              key={key}
              id={`ns-${key}`}
              label={key}
              hint={t(NAMESPACE_HELP[key])}
              value={draft.namespaces[key]}
              onChange={(v) => setDraft((d) => ({ ...d, namespaces: { ...d.namespaces, [key]: v } }))}
            />
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">{t('settings.nodeMetrics')}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Field
            id="net-devices"
            label={t('settings.netDevices')}
            hint={t('settings.netDevicesHint')}
            value={draft.network.devices.join(', ')}
            onChange={(v) =>
              setDraft((d) => ({
                ...d,
                network: { devices: v.split(',').map((s) => s.trim()).filter(Boolean) },
              }))
            }
          />
          <Field
            id="burst-mount"
            label={t('settings.burstMount')}
            hint={t('settings.burstMountHint')}
            value={draft.burstBuffer.mount}
            onChange={(v) => setDraft((d) => ({ ...d, burstBuffer: { mount: v } }))}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">{t('settings.workloads')}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Field
            id="extra-operable-namespaces"
            label={t('settings.extraNs')}
            hint={t('settings.extraNsHint')}
            value={draft.workloads.extraOperableNamespaces.join(', ')}
            onChange={(v) =>
              setDraft((d) => ({
                ...d,
                workloads: {
                  extraOperableNamespaces: v.split(',').map((s) => s.trim()).filter(Boolean),
                },
              }))
            }
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">{t('settings.updates')}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 gap-4">
          <Field
            id="updates-gitops-url"
            label={t('settings.gitopsUrl')}
            hint={t('settings.gitopsUrlHint')}
            placeholder="https://github.com/your-org/your-gitops-repo"
            value={draft.updates.gitopsUrl}
            invalid={draft.updates.gitopsUrl.trim() !== '' && !isGitopsUrl(draft.updates.gitopsUrl)}
            onChange={(v) => setDraft((d) => ({ ...d, updates: { ...d.updates, gitopsUrl: v } }))}
          />
          {draft.updates.gitopsUrl.trim() !== '' && !isGitopsUrl(draft.updates.gitopsUrl) && (
            <p role="alert" className="text-[10px] text-destructive">{t('settings.gitopsUrlInvalid')}</p>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="updates-prepare-commands" className="text-[10px] font-mono text-muted-foreground">
              {t('settings.prepareCommands')}
            </Label>
            <Textarea
              id="updates-prepare-commands"
              rows={6}
              value={draft.updates.prepareCommands}
              placeholder={'git -C ~/gitops checkout -b hearthrack-{sha}\n# bump image tags from {current} to {sha}\n'}
              onChange={(e) => setDraft((d) => ({ ...d, updates: { ...d.updates, prepareCommands: e.target.value } }))}
              aria-invalid={draft.updates.prepareCommands !== '' && !isPrepareCommands(draft.updates.prepareCommands)}
              className="font-mono text-xs"
            />
            <p className="text-[10px] text-muted-foreground">{t('settings.prepareCommandsHint')}</p>
            {draft.updates.prepareCommands !== '' && !isPrepareCommands(draft.updates.prepareCommands) && (
              <p role="alert" className="text-[10px] text-destructive">{t('settings.prepareCommandsInvalid')}</p>
            )}
          </div>
        </CardContent>
      </Card>

      <AlertDialog open={resetOpen} onOpenChange={setResetOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('settings.restoreTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('settings.restoreBody')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setDraft(structuredClone(DEFAULT_CONFIG))
                setResetOpen(false)
              }}
            >
              {t('common.restore')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Field({
  id,
  label,
  value,
  hint,
  type,
  placeholder,
  invalid,
  onChange,
}: {
  id: string
  label: string
  value: string
  hint?: string
  type?: string
  placeholder?: string
  invalid?: boolean
  onChange: (v: string) => void
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id} className="text-[10px] font-mono text-muted-foreground">
        {label}
      </Label>
      <Input
        id={id}
        type={type}
        value={value}
        placeholder={placeholder}
        aria-invalid={invalid || undefined}
        onChange={(e) => onChange(e.target.value)}
        className="font-mono text-xs"
      />
      {hint && <p className="text-[10px] text-muted-foreground">{hint}</p>}
    </div>
  )
}
