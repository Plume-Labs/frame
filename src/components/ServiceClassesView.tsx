import { useState } from 'react'
import { toast } from 'sonner'
import { ResourceQuota, ServiceClass, ServiceClassSummary, createFrameClient, frameListPath } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ShieldCheck, ArrowClockwise, PencilSimple } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import { richText } from '@/i18n/rich'

const frame = createFrameClient()

type Data = { classes: ServiceClassSummary[]; quotas: ResourceQuota[] }

const CLASS_TONE: Record<string, string> = {
  HIGH: 'text-destructive',
  MEDIUM: 'text-warning',
  LOW: 'text-accent',
}

function EditQuotaDialog({
  serviceClass,
  quota,
  onSaved,
}: {
  serviceClass: ServiceClass
  quota?: ResourceQuota
  onSaved: () => void
}) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [maxCPU, setMaxCPU] = useState(quota?.maxCPU ?? '')
  const [maxMemory, setMaxMemory] = useState(quota?.maxMemory ?? '')
  const [maxGPUs, setMaxGPUs] = useState(String(quota?.maxGPUs ?? 0))

  async function submit() {
    setSaving(true)
    try {
      await frame.resources.setQuota(quota?.namespace ?? 'default', {
        serviceClass,
        maxCPU,
        maxMemory,
        maxGPUs: Number(maxGPUs) || 0,
      })
      toast.success(t('sc.updated', { cls: serviceClass }))
      setOpen(false)
      onSaved()
    } catch (e) {
      toast.error(t('sc.updateFailed', { cls: serviceClass }), { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" className="font-mono gap-1.5 w-full">
          <PencilSimple />
          {t('sc.edit')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="font-mono">{t('sc.dialogTitle', { cls: serviceClass })}</DialogTitle>
          <DialogDescription>{t('sc.dialogDesc')}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor={`${serviceClass}-cpu`}>{t('sc.maxCpu')}</Label>
            <Input id={`${serviceClass}-cpu`} value={maxCPU} onChange={(e) => setMaxCPU(e.target.value)} placeholder="16" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`${serviceClass}-mem`}>{t('sc.maxMemory')}</Label>
            <Input id={`${serviceClass}-mem`} value={maxMemory} onChange={(e) => setMaxMemory(e.target.value)} placeholder="64Gi" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`${serviceClass}-gpu`}>{t('sc.maxGpus')}</Label>
            <Input id={`${serviceClass}-gpu`} type="number" value={maxGPUs} onChange={(e) => setMaxGPUs(e.target.value)} />
          </div>
        </div>
        <DialogFooter>
          <Button onClick={submit} disabled={saving} className="font-mono">
            {saving ? t('common.saving') : t('common.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function ServiceClassesView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<Data>(
    async () => {
      const [classes, quotas] = await Promise.all([
        frame.resources.listServiceClasses(),
        frame.resources.listQuotas(),
      ])
      return { classes: classes.items, quotas: quotas.items }
    },
    [],
    [frameListPath('framenodes'), frameListPath('frameresourcequotas')],
  )

  const data = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ShieldCheck className="text-primary" />
            {t('sc.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {richText(t('sc.desc'), {
              node: <span className="font-mono">FrameNode</span>,
              quota: <span className="font-mono">FrameResourceQuota</span>,
            })}
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel={t('sc.empty')} />

      {data && (
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          {data.classes.map((c) => {
            const quota = data.quotas.find((q) => q.serviceClass === c.serviceClass)
            return (
              <Card key={c.serviceClass}>
                <CardHeader>
                  <CardTitle className={`font-mono text-lg ${CLASS_TONE[c.serviceClass]}`}>
                    {c.serviceClass}
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-2 text-xs font-mono">
                  <Row label={t('sc.nodes')} value={`${c.nodeCount}`} />
                  <Row label={t('sc.gpus')} value={`${c.totalGPUs}`} />
                  {quota && (
                    <>
                      <Row label={t('sc.maxCpu')} value={quota.maxCPU} />
                      <Row label={t('sc.maxMemory')} value={quota.maxMemory} />
                      <Row label={t('sc.maxGpus')} value={`${quota.maxGPUs}`} />
                    </>
                  )}
                  <div className="pt-2">
                    <EditQuotaDialog serviceClass={c.serviceClass} quota={quota} onSaved={reload} />
                  </div>
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-muted-foreground">{label}</span>
      <Badge variant="outline" className="font-mono text-[10px]">
        {value}
      </Badge>
    </div>
  )
}
