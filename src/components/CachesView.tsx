import { useState } from 'react'
import { toast } from 'sonner'
import { Lightning } from '@phosphor-icons/react'

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
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { Cache, CacheSettings, changed, changeWarnings, settingsProblem } from '@/lib/caches'
import { createFrameClient } from '@/lib/frame-sdk'

const frame = createFrameClient()

function CacheCard({ cache, onSaved }: { cache: Cache; onSaved: () => void }) {
  const [draft, setDraft] = useState<CacheSettings>({ maxMemory: cache.maxMemory, volumeSize: cache.volumeSize })
  const [confirming, setConfirming] = useState(false)
  const problem = settingsProblem(cache, draft)

  async function save() {
    setConfirming(false)
    try {
      await frame.caches.save(cache, draft)
      toast.success(`${cache.name}: settings saved`)
      onSaved()
    } catch (e) {
      toast.error(`Échec : ${cache.name}`, { description: e instanceof Error ? e.message : String(e) })
    }
  }

  const fields = [
    { key: 'maxMemory', label: 'Max memory' },
    ...(cache.profile === 'queue' ? [{ key: 'volumeSize', label: 'Volume size' } as const] : []),
  ] as const

  return (
    <Card>
      <CardHeader className="space-y-2">
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Lightning className="text-primary" />
          {cache.namespace}/{cache.name}
          <Badge variant="outline" className="font-mono text-[10px]">
            {cache.profile}
          </Badge>
          <Badge variant="outline" className={`ml-auto font-mono text-[10px] border-current ${cache.ready ? 'text-accent' : 'text-warning'}`}>
            {cache.reason}
          </Badge>
        </CardTitle>
        {!cache.ready && cache.message && <p className="text-xs font-mono text-warning">{cache.message}</p>}
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          {fields.map((f) => (
            <div key={f.key} className="space-y-1">
              <Label htmlFor={`${cache.name}-${f.key}`}>{f.label}</Label>
              <Input
                id={`${cache.name}-${f.key}`}
                value={draft[f.key]}
                onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })}
              />
            </div>
          ))}
        </div>
        {problem && <p className="text-xs text-warning">{problem}</p>}
        <Button disabled={problem !== null || !changed(cache, draft)} onClick={() => setConfirming(true)}>
          Apply
        </Button>
        <dl className="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1 text-xs font-mono">
          {cache.endpoint && (
            <div className="flex gap-2">
              <dt className="text-muted-foreground">Endpoint</dt>
              <dd>{cache.endpoint}</dd>
            </div>
          )}
          {cache.storageClass && (
            <div className="flex gap-2">
              <dt className="text-muted-foreground">Storage class</dt>
              <dd>{cache.storageClass}</dd>
            </div>
          )}
        </dl>
      </CardContent>
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Apply to {cache.name}?</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <ul className="list-disc pl-4 space-y-1">
                {changeWarnings(cache, draft).map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={save}>Apply</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

/** Valkey instances run through FrameService, and the settings that can move. */
export function CachesView() {
  const { state, reload } = useLiveResource<Cache[]>(() => frame.caches.list(), [], [frame.caches.watchPath()])
  return (
    <div className="space-y-4">
      <LiveStates state={state} emptyLabel="No FrameService of type cache yet." />
      {state.phase === 'ready' &&
        state.data.map((c) => (
          // Keyed on the saved values: a change from elsewhere replaces the form.
          <CacheCard key={`${c.namespace}/${c.name}/${c.maxMemory}/${c.volumeSize}`} cache={c} onSaved={reload} />
        ))}
    </div>
  )
}
