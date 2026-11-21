import { useState } from 'react'
import { toast } from 'sonner'
import { Cube } from '@phosphor-icons/react'

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
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { createFrameClient } from '@/lib/frame-sdk'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'
import {
  changed, EMPTY_SETTINGS, InferenceModel, KV_CACHE_TYPES, ModelSettings, nameProblem, settingsProblem,
} from '@/lib/inference-models'

const frame = createFrameClient()

type Translate = ReturnType<typeof useI18n>['t']

// Hints that are values (a number, an enum list) stay literal; only worded ones are keyed.
const FIELDS: { key: keyof ModelSettings; label: MessageKey; hint?: string; hintKey?: MessageKey }[] = [
  { key: 'weightsMiB', label: 'infm.f.weights' },
  { key: 'layers', label: 'infm.f.layers' },
  { key: 'kvHeads', label: 'infm.f.kvHeads' },
  { key: 'headDim', label: 'infm.f.headDim' },
  { key: 'hostMemoryMiB', label: 'infm.f.hostMem', hintKey: 'infm.h.hostMem' },
  { key: 'contextLength', label: 'infm.f.context', hint: '4096' },
  { key: 'parallel', label: 'infm.f.parallel', hintKey: 'infm.h.parallel' },
  { key: 'kvCacheType', label: 'infm.f.kvType', hint: KV_CACHE_TYPES.join(', ') },
  { key: 'cpuMoELayers', label: 'infm.f.cpuMoE', hintKey: 'infm.h.none' },
  { key: 'threads', label: 'infm.f.threads', hint: '4' },
  { key: 'reasoningBudget', label: 'infm.f.budget', hintKey: 'infm.h.budget' },
  { key: 'reasoningBudgetMessage', label: 'infm.f.budgetMsg' },
]

function SettingsForm({ id, draft, onChange }: { id: string; draft: ModelSettings; onChange: (next: ModelSettings) => void }) {
  const { t } = useI18n()
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {FIELDS.map((f) => (
        <div key={f.key} className="space-y-1">
          <Label htmlFor={`${id}-${f.key}`}>{t(f.label)}</Label>
          <Input
            id={`${id}-${f.key}`}
            value={draft[f.key]}
            placeholder={f.hintKey ? t(f.hintKey) : f.hint}
            onChange={(e) => onChange({ ...draft, [f.key]: e.target.value })}
          />
        </div>
      ))}
    </div>
  )
}

async function attempt(t: Translate, name: string, done: string, run: () => Promise<void>, after: () => void) {
  try {
    await run()
    toast.success(t('infm.done', { name, what: done }))
    after()
  } catch (e) {
    toast.error(t('infm.failed', { name }), { description: e instanceof Error ? e.message : String(e) })
  }
}

function ModelCard({ model, onSaved }: { model: InferenceModel; onSaved: () => void }) {
  const { t, tm } = useI18n()
  const { name, ...saved } = model
  const [draft, setDraft] = useState<ModelSettings>(saved)
  const [confirm, setConfirm] = useState<'save' | 'delete' | null>(null)
  const problem = settingsProblem(draft)

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Cube className="text-primary" />
          {name}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <SettingsForm id={name} draft={draft} onChange={setDraft} />
        {problem && <p className="text-xs text-warning">{tm(problem)}</p>}
        <div className="flex gap-2">
          <Button disabled={problem !== null || !changed(model, draft)} onClick={() => setConfirm('save')}>
            {t('infm.apply')}
          </Button>
          <Button variant="outline" onClick={() => setConfirm('delete')}>
            {t('infm.delete')}
          </Button>
        </div>
      </CardContent>
      <AlertDialog open={confirm !== null} onOpenChange={(open) => !open && setConfirm(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirm === 'delete' ? t('infm.confirmDelete', { name }) : t('infm.confirmApply', { name })}</AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === 'delete'
                ? t('infm.deleteNote')
                : t('infm.applyNote')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const action = confirm
                setConfirm(null)
                void (action === 'delete'
                  ? attempt(t, name, t('infm.deleted'), () => frame.inferenceModels.remove(name), onSaved)
                  : attempt(t, name, t('infm.saved'), () => frame.inferenceModels.save(name, draft), onSaved))
              }}
            >
              {confirm === 'delete' ? t('infm.delete') : t('infm.apply')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

function NewModelCard({ onSaved }: { onSaved: () => void }) {
  const { t, tm } = useI18n()
  const [name, setName] = useState('')
  const [draft, setDraft] = useState<ModelSettings>(EMPTY_SETTINGS)
  const problem = name ? (nameProblem(name) ?? settingsProblem(draft)) : null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t('infm.addTitle')}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <Label htmlFor="new-model-name">{t('infm.nameLabel')}</Label>
          <Input id="new-model-name" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <SettingsForm id="new-model" draft={draft} onChange={setDraft} />
        {problem && <p className="text-xs text-warning">{tm(problem)}</p>}
        <Button
          disabled={!name || problem !== null}
          onClick={() =>
            void attempt(t, name, t('infm.added'), () => frame.inferenceModels.create(name, draft), () => {
              setName('')
              setDraft(EMPTY_SETTINGS)
              onSaved()
            })
          }
        >
          {t('infm.add')}
        </Button>
      </CardContent>
    </Card>
  )
}

/** The catalog FrameServices of type inference choose from. */
export function InferenceModelsView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<InferenceModel[]>(() => frame.inferenceModels.list(), [], [frame.inferenceModels.watchPath()])
  return (
    <div className="space-y-4">
      <LiveStates state={state} emptyLabel={t('infm.empty')} />
      {state.phase === 'ready' &&
        state.data.map((m) => (
          // Keyed on the saved values: a change from elsewhere replaces the form.
          <ModelCard key={JSON.stringify(m)} model={m} onSaved={reload} />
        ))}
      <NewModelCard onSaved={reload} />
    </div>
  )
}
