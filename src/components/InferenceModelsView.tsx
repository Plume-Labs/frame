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
import {
  changed, EMPTY_SETTINGS, InferenceModel, KV_CACHE_TYPES, ModelSettings, nameProblem, settingsProblem,
} from '@/lib/inference-models'

const frame = createFrameClient()

const FIELDS: { key: keyof ModelSettings; label: string; hint?: string }[] = [
  { key: 'weightsMiB', label: 'Weights on the GPU (MiB)' },
  { key: 'layers', label: 'Layers holding a KV cache' },
  { key: 'kvHeads', label: 'KV heads' },
  { key: 'headDim', label: 'Head dimension' },
  { key: 'hostMemoryMiB', label: 'Host memory (MiB)', hint: 'twice the weights' },
  { key: 'contextLength', label: 'Default context', hint: '4096' },
  { key: 'parallel', label: 'Parallel slots', hint: "llama.cpp's choice" },
  { key: 'kvCacheType', label: 'KV cache type', hint: KV_CACHE_TYPES.join(', ') },
  { key: 'cpuMoELayers', label: 'Layers with experts on the CPU', hint: 'none' },
  { key: 'reasoningBudget', label: 'Reasoning budget', hint: '-1, 0 or tokens' },
  { key: 'reasoningBudgetMessage', label: 'Budget message' },
]

function SettingsForm({ id, draft, onChange }: { id: string; draft: ModelSettings; onChange: (next: ModelSettings) => void }) {
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {FIELDS.map((f) => (
        <div key={f.key} className="space-y-1">
          <Label htmlFor={`${id}-${f.key}`}>{f.label}</Label>
          <Input
            id={`${id}-${f.key}`}
            value={draft[f.key]}
            placeholder={f.hint}
            onChange={(e) => onChange({ ...draft, [f.key]: e.target.value })}
          />
        </div>
      ))}
    </div>
  )
}

async function attempt(name: string, done: string, run: () => Promise<void>, after: () => void) {
  try {
    await run()
    toast.success(`${name}: ${done}`)
    after()
  } catch (e) {
    toast.error(`Échec : ${name}`, { description: e instanceof Error ? e.message : String(e) })
  }
}

function ModelCard({ model, onSaved }: { model: InferenceModel; onSaved: () => void }) {
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
        {problem && <p className="text-xs text-warning">{problem}</p>}
        <div className="flex gap-2">
          <Button disabled={problem !== null || !changed(model, draft)} onClick={() => setConfirm('save')}>
            Apply
          </Button>
          <Button variant="outline" onClick={() => setConfirm('delete')}>
            Delete
          </Button>
        </div>
      </CardContent>
      <AlertDialog open={confirm !== null} onOpenChange={(open) => !open && setConfirm(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirm === 'delete' ? `Delete ${name}?` : `Apply to ${name}?`}</AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === 'delete'
                ? 'Refused while an instance serves it. The weights stay on the model cache.'
                : 'Every instance of this model restarts within ten minutes, on the new settings.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const action = confirm
                setConfirm(null)
                void (action === 'delete'
                  ? attempt(name, 'deleted', () => frame.inferenceModels.remove(name), onSaved)
                  : attempt(name, 'settings saved', () => frame.inferenceModels.save(name, draft), onSaved))
              }}
            >
              {confirm === 'delete' ? 'Delete' : 'Apply'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

function NewModelCard({ onSaved }: { onSaved: () => void }) {
  const [name, setName] = useState('')
  const [draft, setDraft] = useState<ModelSettings>(EMPTY_SETTINGS)
  const problem = name ? (nameProblem(name) ?? settingsProblem(draft)) : null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Add a model</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <Label htmlFor="new-model-name">Name (the weights are name.gguf on the model cache)</Label>
          <Input id="new-model-name" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <SettingsForm id="new-model" draft={draft} onChange={setDraft} />
        {problem && <p className="text-xs text-warning">{problem}</p>}
        <Button
          disabled={!name || problem !== null}
          onClick={() =>
            void attempt(name, 'added', () => frame.inferenceModels.create(name, draft), () => {
              setName('')
              setDraft(EMPTY_SETTINGS)
              onSaved()
            })
          }
        >
          Add
        </Button>
      </CardContent>
    </Card>
  )
}

/** The catalog FrameServices of type inference choose from. */
export function InferenceModelsView() {
  const { state, reload } = useLiveResource<InferenceModel[]>(() => frame.inferenceModels.list(), [], [frame.inferenceModels.watchPath()])
  return (
    <div className="space-y-4">
      <LiveStates state={state} emptyLabel="No InferenceModel yet." />
      {state.phase === 'ready' &&
        state.data.map((m) => (
          // Keyed on the saved values: a change from elsewhere replaces the form.
          <ModelCard key={JSON.stringify(m)} model={m} onSaved={reload} />
        ))}
      <NewModelCard onSaved={reload} />
    </div>
  )
}
