import { useState } from 'react'
import { toast } from 'sonner'
import { Database as DatabaseIcon } from '@phosphor-icons/react'

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
import { changed, changeWarnings, Database, DatabaseSettings, settingsProblem } from '@/lib/databases'
import { createFrameClient } from '@/lib/frame-sdk'

const frame = createFrameClient()

function DatabaseCard({ db, onSaved }: { db: Database; onSaved: () => void }) {
  const [draft, setDraft] = useState<DatabaseSettings>({ instances: db.instances, volumeSize: db.volumeSize, nodes: db.nodes })
  const [confirming, setConfirming] = useState(false)
  const problem = settingsProblem(draft)

  async function save() {
    setConfirming(false)
    try {
      await frame.databases.save(db, draft)
      toast.success(`${db.name}: settings saved`)
      onSaved()
    } catch (e) {
      toast.error(`Échec : ${db.name}`, { description: e instanceof Error ? e.message : String(e) })
    }
  }

  const fields = [
    { key: 'instances', label: 'Instances' },
    { key: 'volumeSize', label: 'Volume size' },
    { key: 'nodes', label: 'Nodes (comma-separated)' },
  ] as const

  return (
    <Card>
      <CardHeader className="space-y-2">
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <DatabaseIcon className="text-primary" />
          {db.namespace}/{db.name}
          <Badge variant="outline" className={`ml-auto font-mono text-[10px] border-current ${db.ready ? 'text-accent' : 'text-warning'}`}>
            {db.reason}
          </Badge>
        </CardTitle>
        {!db.ready && db.message && <p className="text-xs font-mono text-warning">{db.message}</p>}
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          {fields.map((f) => (
            <div key={f.key} className="space-y-1">
              <Label htmlFor={`${db.name}-${f.key}`}>{f.label}</Label>
              <Input
                id={`${db.name}-${f.key}`}
                value={draft[f.key]}
                onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })}
              />
            </div>
          ))}
        </div>
        {problem && <p className="text-xs text-warning">{problem}</p>}
        <Button disabled={problem !== null || !changed(db, draft)} onClick={() => setConfirming(true)}>
          Apply
        </Button>
        <dl className="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1 text-xs font-mono">
          {db.endpoint && (
            <div className="flex gap-2">
              <dt className="text-muted-foreground">Endpoint</dt>
              <dd>{db.endpoint}</dd>
            </div>
          )}
          {db.fixed.map((f) => (
            <div key={f.key} className="flex gap-2">
              <dt className="text-muted-foreground">{f.key}</dt>
              <dd>{f.value}</dd>
            </div>
          ))}
          {db.adopted && <div className="text-muted-foreground">Adopted cluster: Frame never recreates it.</div>}
        </dl>
      </CardContent>
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Apply to {db.name}?</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <ul className="list-disc pl-4 space-y-1">
                {changeWarnings(db, draft).map((w) => (
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

/** Postgres clusters run through FrameService, and the settings that can move. */
export function DatabasesView() {
  const { state, reload } = useLiveResource<Database[]>(() => frame.databases.list(), [], [frame.databases.watchPath()])
  return (
    <div className="space-y-4">
      <LiveStates state={state} emptyLabel="No FrameService of type postgres yet." />
      {state.phase === 'ready' &&
        state.data.map((db) => (
          // Keyed on the saved values: a change from elsewhere replaces the form.
          <DatabaseCard key={`${db.namespace}/${db.name}/${db.instances}/${db.volumeSize}/${db.nodes}`} db={db} onSaved={reload} />
        ))}
    </div>
  )
}
