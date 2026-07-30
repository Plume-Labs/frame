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
} from '@/lib/frame-config'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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

type IntegrationKey = keyof FrameConfig['integrations']

/** Which screen each integration feeds, so a wrong value is traceable to what broke. */
const FEEDS: Record<IntegrationKey, string> = {
  cephOsd: 'Storage',
  cephMon: 'Storage',
  alluxio: 'Storage (Alluxio tiers)',
  nodeExporter: 'Network · Burst Buffer · KSM · PTP',
  dcgm: 'GPU',
  llamacpp: 'KV-Cache · Inference',
  tei: 'Inference',
  falcosidekick: 'Security (runtime)',
  tetragon: 'Security (network)',
  prometheus: 'Capacity (forecast)',
  alertmanager: 'Alerts',
  neuraSandbox: 'Jobs (Neura sandbox pods)',
}

const NAMESPACE_HELP: Record<keyof FrameConfig['namespaces'], string> = {
  ceph: 'CephCluster and CephBlockPool CRs',
  velero: 'Backup CRs — listing and on-demand triggers',
  argo: 'Workflow CRs behind the Lineage screen',
  volcanoCommands: 'Where queue open/close Commands are created',
}

/**
 * Edit the cluster wiring the UI uses. Everything here was compiled in until
 * it moved to a ConfigMap, so this screen is what makes Frame portable to a
 * cluster whose components sit in different namespaces or answer on different
 * ports.
 */
export function SettingsView() {
  const [draft, setDraft] = useState<FrameConfig>(() => structuredClone(config()))
  const [saving, setSaving] = useState(false)
  const [resetOpen, setResetOpen] = useState(false)
  const status = configStatus()

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
      toast.success('Configuration saved', {
        description: 'Other tabs and pods pick it up on their next load.',
      })
      setDraft(structuredClone(config()))
    } catch (e) {
      toast.error('Failed to save configuration', {
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
            Settings — cluster wiring
            <div className="ml-auto flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                className="font-mono gap-1.5"
                onClick={() => setResetOpen(true)}
              >
                <ArrowCounterClockwise />
                Defaults
              </Button>
              <Button size="sm" className="font-mono gap-1.5" onClick={save} disabled={!dirty || saving}>
                <FloppyDisk />
                {saving ? 'Saving…' : 'Save'}
              </Button>
            </div>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Where each cluster component lives, how its pods are labelled, and which port it answers
            on. Stored in the{' '}
            <span className="font-mono text-foreground">
              {CONFIG_NAMESPACE}/{CONFIG_NAME}
            </span>{' '}
            ConfigMap and read at page load.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant="outline"
              className={`font-mono text-[10px] border-current ${
                status.source === 'configmap' ? 'text-accent' : 'text-warning'
              }`}
            >
              {status.source === 'configmap'
                ? 'loaded from ConfigMap'
                : status.error
                  ? 'compiled defaults — ConfigMap unreadable'
                  : 'compiled defaults — nothing saved yet'}
            </Badge>
            {status.error && (
              <span className="font-mono text-[10px] text-warning flex items-center gap-1">
                <Warning size={12} />
                {status.error}
              </span>
            )}
            {dirty && (
              <Badge variant="outline" className="font-mono text-[10px] text-warning border-current">
                unsaved changes
              </Badge>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Pod-proxied components</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {(Object.keys(draft.integrations) as IntegrationKey[]).map((key) => {
            const i = draft.integrations[key]
            return (
              <div key={key} className="rounded border border-border bg-secondary/30 p-3 space-y-2">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="font-mono text-sm font-bold">{key}</span>
                  <span className="font-mono text-[10px] text-muted-foreground">{FEEDS[key]}</span>
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field
                    id={`${key}-ns`}
                    label="Namespace"
                    value={i.namespace}
                    placeholder={key === 'neuraSandbox' ? 'all namespaces' : undefined}
                    onChange={(v) => setIntegration(key, { namespace: v })}
                  />
                  <Field
                    id={`${key}-sel`}
                    label="Label selector"
                    value={i.selector}
                    onChange={(v) => setIntegration(key, { selector: v })}
                  />
                  {i.port !== undefined ? (
                    <Field
                      id={`${key}-port`}
                      label="Port"
                      type="number"
                      value={String(i.port)}
                      onChange={(v) => setIntegration(key, { port: Number(v) || 0 })}
                    />
                  ) : (
                    <div className="space-y-1.5">
                      <Label className="text-[10px] font-mono text-muted-foreground">Port</Label>
                      <p className="text-[10px] font-mono text-muted-foreground pt-2">
                        read via its own API — no proxy
                      </p>
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">API-addressed namespaces</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Field
            id="frame-ns"
            label="Frame CRs"
            hint="FrameJob, FrameNode, SchedulingPolicy, ResourceQuota"
            value={draft.frameNamespace}
            onChange={(v) => setDraft((d) => ({ ...d, frameNamespace: v }))}
          />
          {(Object.keys(draft.namespaces) as Array<keyof FrameConfig['namespaces']>).map((key) => (
            <Field
              key={key}
              id={`ns-${key}`}
              label={key}
              hint={NAMESPACE_HELP[key]}
              value={draft.namespaces[key]}
              onChange={(v) => setDraft((d) => ({ ...d, namespaces: { ...d.namespaces, [key]: v } }))}
            />
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Node metrics</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Field
            id="net-devices"
            label="Network interfaces"
            hint="Comma-separated; charted on the Network screen"
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
            label="Burst-buffer mount"
            hint="Mountpoint reported as the scratch tier"
            value={draft.burstBuffer.mount}
            onChange={(v) => setDraft((d) => ({ ...d, burstBuffer: { mount: v } }))}
          />
        </CardContent>
      </Card>

      <AlertDialog open={resetOpen} onOpenChange={setResetOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restore compiled defaults?</AlertDialogTitle>
            <AlertDialogDescription>
              Replaces the form with the values Frame ships with. Nothing is written until you press
              Save, so the stored ConfigMap is untouched until then.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setDraft(structuredClone(DEFAULT_CONFIG))
                setResetOpen(false)
              }}
            >
              Restore
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
  onChange,
}: {
  id: string
  label: string
  value: string
  hint?: string
  type?: string
  placeholder?: string
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
        onChange={(e) => onChange(e.target.value)}
        className="font-mono text-xs"
      />
      {hint && <p className="text-[10px] text-muted-foreground">{hint}</p>}
    </div>
  )
}
