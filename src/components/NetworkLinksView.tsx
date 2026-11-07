import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Plus, ShareNetwork, Trash } from '@phosphor-icons/react'

import { createFrameClient } from '@/lib/frame-sdk'
import type { Machine } from '@/lib/machines'
import {
  ExpectedLink,
  expectationsProblem,
  lacpLine,
  memberLine,
  networkNotes,
  NetworkExpectations,
  portDrops,
  portErrors,
  portTone,
  speedLabel,
} from '@/lib/network-health'
import { checkVerdicts, NETWORK_CHECKS } from '@/lib/storage-health'
import { TONE_TEXT } from '@/lib/thresholds'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'

const frame = createFrameClient()

/** An empty number field is an absent optional, not a zero. */
function optionalNumber(raw: string): number | undefined {
  return raw === '' ? undefined : Number(raw)
}

function ExpectationsCard() {
  const { state, reload } = useLiveResource<NetworkExpectations>(
    () => frame.networkHealthPolicy.get(),
    [],
    [frame.networkHealthPolicy.watchPath()],
  )
  const saved = state.phase === 'ready' ? state.data : null
  const savedKey = saved && JSON.stringify(saved)
  const [draft, setDraft] = useState<NetworkExpectations | null>(null)
  // By value: a watch event re-reads an identical object, and that must not
  // wipe what is being typed.
  useEffect(() => {
    if (saved) setDraft(saved)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedKey])

  const problem = draft && expectationsProblem(draft)
  const setLink = (i: number, patch: Partial<ExpectedLink>) =>
    draft && setDraft({ ...draft, links: draft.links.map((l, j) => (i === j ? { ...l, ...patch } : l)) })

  async function save() {
    if (!draft) return
    try {
      await frame.networkHealthPolicy.save(draft)
      toast.success('Expectations saved')
      reload()
    } catch (e) {
      toast.error('Échec : expectations not saved', { description: e instanceof Error ? e.message : String(e) })
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <ShareNetwork className="text-primary" />
          Expected links
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel="" />
        {draft && (
          <>
            <div className="space-y-1 max-w-xs">
              <Label htmlFor="min-speed">Minimum link speed (Mb/s)</Label>
              <Input
                id="min-speed"
                type="number"
                min={1}
                value={Number.isNaN(draft.minLinkSpeedMbps) ? '' : draft.minLinkSpeedMbps}
                onChange={(e) => setDraft({ ...draft, minLinkSpeedMbps: e.target.valueAsNumber })}
              />
              <p className="text-xs text-muted-foreground">For every physical link that is up. A link named below is also required.</p>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 max-w-lg">
              <div className="space-y-1">
                <Label htmlFor="saturated">Saturated at (%)</Label>
                <Input
                  id="saturated"
                  type="number"
                  min={1}
                  max={100}
                  value={Number.isNaN(draft.linkSaturatedPercent) ? '' : draft.linkSaturatedPercent}
                  onChange={(e) => setDraft({ ...draft, linkSaturatedPercent: e.target.valueAsNumber })}
                />
                <p className="text-xs text-muted-foreground">Averaged over five minutes, in the busier direction.</p>
              </div>
              <div className="space-y-1">
                <Label htmlFor="errors">Errors a minute</Label>
                <Input
                  id="errors"
                  type="number"
                  min={1}
                  value={Number.isNaN(draft.linkErrorsPerMinute) ? '' : draft.linkErrorsPerMinute}
                  onChange={(e) => setDraft({ ...draft, linkErrorsPerMinute: e.target.valueAsNumber })}
                />
                <p className="text-xs text-muted-foreground">Over five minutes. Drops are shown, not alerted on.</p>
              </div>
            </div>
            {draft.links.map((l, i) => (
              <div key={i} className="grid grid-cols-2 sm:grid-cols-5 gap-2 items-end">
                <div className="space-y-1">
                  <Label htmlFor={`node-${i}`}>Node</Label>
                  <Input id={`node-${i}`} value={l.node} onChange={(e) => setLink(i, { node: e.target.value })} />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`if-${i}`}>Interface</Label>
                  <Input id={`if-${i}`} value={l.interface} onChange={(e) => setLink(i, { interface: e.target.value })} />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`speed-${i}`}>Speed (Mb/s)</Label>
                  <Input id={`speed-${i}`} type="number" min={1} value={l.speedMbps ?? ''} onChange={(e) => setLink(i, { speedMbps: optionalNumber(e.target.value) })} />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`members-${i}`}>Bond members</Label>
                  <Input id={`members-${i}`} type="number" min={1} max={16} value={l.members ?? ''} onChange={(e) => setLink(i, { members: optionalNumber(e.target.value) })} />
                </div>
                <Button
                  variant="outline"
                  aria-label={`Remove ${l.interface || 'this link'}`}
                  onClick={() => setDraft({ ...draft, links: draft.links.filter((_, j) => j !== i) })}
                >
                  <Trash />
                </Button>
              </div>
            ))}
            {problem && <p className="text-xs text-warning">{problem}</p>}
            <div className="flex gap-2">
              <Button variant="outline" onClick={() => setDraft({ ...draft, links: [...draft.links, { node: '', interface: '' }] })}>
                <Plus /> Add a link
              </Button>
              <Button onClick={save} disabled={problem !== null || JSON.stringify(draft) === savedKey}>
                Save
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function MachineLinksCard({ machine }: { machine: Machine }) {
  const n = machine.network
  const verdicts = checkVerdicts(machine, NETWORK_CHECKS)
  return (
    <Card>
      <CardHeader className="space-y-2">
        <CardTitle className="font-mono text-base">{machine.nodeRef || machine.name}</CardTitle>
        <div className="flex flex-wrap gap-2">
          {verdicts.map((v) => (
            <Badge key={v.type} variant="outline" className={`font-mono text-[10px] border-current ${TONE_TEXT[v.tone]}`}>
              {v.label}: {v.word}
            </Badge>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-2 text-xs font-mono">
        {networkNotes(n, Date.now()).map((note) => (
          <p key={note} className="text-warning">
            {note}
          </p>
        ))}
        {(n?.bonds ?? []).map((b) => (
          <div key={b.name} className="rounded-md border border-border p-2 space-y-1">
            <div className="flex flex-wrap gap-x-4">
              <span className="font-bold">{b.name}</span>
              <span className="text-muted-foreground">{b.mode}</span>
              <span className={b.miiStatus === 'up' ? 'text-accent' : b.miiStatus ? 'text-destructive' : 'text-muted-foreground'}>{b.miiStatus ?? '?'}</span>
              <span className="text-muted-foreground">MTU {b.mtu ?? '—'}</span>
            </div>
            {lacpLine(b) && <div className="text-muted-foreground">{lacpLine(b)}</div>}
            {memberLine(b).map((m) => (
              <div key={m.name} className="flex gap-3 pl-3">
                <span>{m.name}</span>
                <span className={TONE_TEXT[m.tone]}>{m.detail}</span>
              </div>
            ))}
          </div>
        ))}
        {(n?.interfaces ?? []).map((i) => (
          <div key={i.name} className="flex flex-wrap gap-x-4">
            <span className="font-bold">{i.name}</span>
            <span className={TONE_TEXT[portTone(i)]}>{i.operState ?? '?'}</span>
            <span>{speedLabel(i.speedMbps)}</span>
            <span className="text-muted-foreground">{i.duplex}</span>
            <span className="text-muted-foreground">MTU {i.mtu ?? '—'}</span>
            {i.master && <span className="text-muted-foreground">in {i.master}</span>}
            <span className={portErrors(i) > 0 ? 'text-warning' : 'text-muted-foreground'}>{portErrors(i)} errors</span>
            <span className={portDrops(i) > 0 ? 'text-warning' : 'text-muted-foreground'}>{portDrops(i)} drops</span>
          </div>
        ))}
        {verdicts
          .filter((v) => v.detail)
          .map((v) => (
            <p key={v.type} className={TONE_TEXT[v.tone]}>
              {v.label}: {v.detail}
            </p>
          ))}
      </CardContent>
    </Card>
  )
}

/** Each node's ports and bonds as its agent reads them, and what they should be. */
export function NetworkLinksView() {
  const { state } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)
  return (
    <div className="space-y-6">
      <ExpectationsCard />
      <LiveStates state={state} emptyLabel="No FrameMachine declared yet." />
      {state.phase === 'ready' && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          {state.data.map((m) => (
            <MachineLinksCard key={`${m.namespace}/${m.name}`} machine={m} />
          ))}
        </div>
      )}
    </div>
  )
}
