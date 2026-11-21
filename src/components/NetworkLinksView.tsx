import { useState } from 'react'
import { toast } from 'sonner'
import { Plus, ShareNetwork, Trash } from '@phosphor-icons/react'

import { createFrameClient } from '@/lib/frame-sdk'
import { useI18n } from '@/i18n/context'
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
import { checkVerdicts, NETWORK_CHECKS, VERDICT_WORD } from '@/lib/storage-health'
import { TONE_TEXT } from '@/lib/thresholds'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { useNow } from '@/hooks/useNow'

const frame = createFrameClient()

/** An empty number field is an absent optional, not a zero. */
function optionalNumber(raw: string): number | undefined {
  return raw === '' ? undefined : Number(raw)
}

function ExpectationsCard() {
  const { t, tm } = useI18n()
  const { state, reload } = useLiveResource<NetworkExpectations>(
    () => frame.networkHealthPolicy.get(),
    [],
    [frame.networkHealthPolicy.watchPath()],
  )
  const saved = state.phase === 'ready' ? state.data : null
  const savedKey = saved && JSON.stringify(saved)
  const [draft, setDraft] = useState<NetworkExpectations | null>(null)
  const [draftOf, setDraftOf] = useState<string | null>(null)
  // By value: a watch event re-reads an identical object, and that must not
  // wipe what is being typed. A saved value that differs replaces the draft in
  // the same render, so the form never shows the old one for a frame.
  if (saved && savedKey !== draftOf) {
    setDraftOf(savedKey)
    setDraft(saved)
  }

  const problem = draft && expectationsProblem(draft)
  const setLink = (i: number, patch: Partial<ExpectedLink>) =>
    draft && setDraft({ ...draft, links: draft.links.map((l, j) => (i === j ? { ...l, ...patch } : l)) })

  async function save() {
    if (!draft) return
    try {
      await frame.networkHealthPolicy.save(draft)
      toast.success(t('net.exp.saved'))
      reload()
    } catch (e) {
      toast.error(t('net.exp.saveFailed'), { description: e instanceof Error ? e.message : String(e) })
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <ShareNetwork className="text-primary" />
          {t('net.exp.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel="" />
        {draft && (
          <>
            <div className="space-y-1 max-w-xs">
              <Label htmlFor="min-speed">{t('net.exp.minSpeedLabel')}</Label>
              <Input
                id="min-speed"
                type="number"
                min={1}
                value={Number.isNaN(draft.minLinkSpeedMbps) ? '' : draft.minLinkSpeedMbps}
                onChange={(e) => setDraft({ ...draft, minLinkSpeedMbps: e.target.valueAsNumber })}
              />
              <p className="text-xs text-muted-foreground">{t('net.exp.minSpeedHint')}</p>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 max-w-lg">
              <div className="space-y-1">
                <Label htmlFor="saturated">{t('net.exp.saturatedLabel')}</Label>
                <Input
                  id="saturated"
                  type="number"
                  min={1}
                  max={100}
                  value={Number.isNaN(draft.linkSaturatedPercent) ? '' : draft.linkSaturatedPercent}
                  onChange={(e) => setDraft({ ...draft, linkSaturatedPercent: e.target.valueAsNumber })}
                />
                <p className="text-xs text-muted-foreground">{t('net.exp.saturatedHint')}</p>
              </div>
              <div className="space-y-1">
                <Label htmlFor="errors">{t('net.exp.errorsLabel')}</Label>
                <Input
                  id="errors"
                  type="number"
                  min={1}
                  value={Number.isNaN(draft.linkErrorsPerMinute) ? '' : draft.linkErrorsPerMinute}
                  onChange={(e) => setDraft({ ...draft, linkErrorsPerMinute: e.target.valueAsNumber })}
                />
                <p className="text-xs text-muted-foreground">{t('net.exp.errorsHint')}</p>
              </div>
            </div>
            {draft.links.map((l, i) => (
              <div key={i} className="grid grid-cols-2 sm:grid-cols-5 gap-2 items-end">
                <div className="space-y-1">
                  <Label htmlFor={`node-${i}`}>{t('net.exp.node')}</Label>
                  <Input id={`node-${i}`} value={l.node} onChange={(e) => setLink(i, { node: e.target.value })} />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`if-${i}`}>{t('net.exp.interface')}</Label>
                  <Input id={`if-${i}`} value={l.interface} onChange={(e) => setLink(i, { interface: e.target.value })} />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`speed-${i}`}>{t('net.exp.speedLabel')}</Label>
                  <Input id={`speed-${i}`} type="number" min={1} value={l.speedMbps ?? ''} onChange={(e) => setLink(i, { speedMbps: optionalNumber(e.target.value) })} />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`members-${i}`}>{t('net.exp.bondMembers')}</Label>
                  <Input id={`members-${i}`} type="number" min={1} max={16} value={l.members ?? ''} onChange={(e) => setLink(i, { members: optionalNumber(e.target.value) })} />
                </div>
                <Button
                  variant="outline"
                  aria-label={l.interface ? t('net.exp.remove', { name: l.interface }) : t('net.exp.removeThis')}
                  onClick={() => setDraft({ ...draft, links: draft.links.filter((_, j) => j !== i) })}
                >
                  <Trash />
                </Button>
              </div>
            ))}
            {problem && <p className="text-xs text-warning">{tm(problem)}</p>}
            <div className="flex gap-2">
              <Button variant="outline" onClick={() => setDraft({ ...draft, links: [...draft.links, { node: '', interface: '' }] })}>
                <Plus /> {t('net.exp.add')}
              </Button>
              <Button onClick={save} disabled={problem !== null || JSON.stringify(draft) === savedKey}>
                {t('common.save')}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function MachineLinksCard({ machine }: { machine: Machine }) {
  const { t, tm, tn } = useI18n()
  const now = useNow()
  const n = machine.network
  const verdicts = checkVerdicts(machine, NETWORK_CHECKS)
  return (
    <Card>
      <CardHeader className="space-y-2">
        <CardTitle className="font-mono text-base">{machine.nodeRef || machine.name}</CardTitle>
        <div className="flex flex-wrap gap-2">
          {verdicts.map((v) => (
            <Badge key={v.type} variant="outline" className={`font-mono text-[10px] border-current ${TONE_TEXT[v.tone]}`}>
              {t(v.label)}: {VERDICT_WORD[v.word] ? t(VERDICT_WORD[v.word]) : v.word}
            </Badge>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-2 text-xs font-mono">
        {networkNotes(n, now).map((note) => {
          const text = tm(note)
          return (
            <p key={text} className="text-warning">
              {text}
            </p>
          )
        })}
        {(n?.bonds ?? []).map((b) => (
          <div key={b.name} className="rounded-md border border-border p-2 space-y-1">
            <div className="flex flex-wrap gap-x-4">
              <span className="font-bold">{b.name}</span>
              <span className="text-muted-foreground">{b.mode}</span>
              <span className={b.miiStatus === 'up' ? 'text-accent' : b.miiStatus ? 'text-destructive' : 'text-muted-foreground'}>{b.miiStatus ?? '?'}</span>
              <span className="text-muted-foreground">{t('net.mtu', { mtu: b.mtu ?? '—' })}</span>
            </div>
            {lacpLine(b) && <div className="text-muted-foreground">{tm(lacpLine(b)!)}</div>}
            {memberLine(b).map((m) => (
              <div key={m.name} className="flex gap-3 pl-3">
                <span>{m.name}</span>
                <span className={TONE_TEXT[m.tone]}>{[m.status, ...m.notes.map(tm)].join(' · ')}</span>
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
            <span className="text-muted-foreground">{t('net.mtu', { mtu: i.mtu ?? '—' })}</span>
            {i.master && <span className="text-muted-foreground">{t('net.inMaster', { master: i.master })}</span>}
            <span className={portErrors(i) > 0 ? 'text-warning' : 'text-muted-foreground'}>{tn('net.errors', portErrors(i))}</span>
            <span className={portDrops(i) > 0 ? 'text-warning' : 'text-muted-foreground'}>{tn('net.drops', portDrops(i))}</span>
          </div>
        ))}
        {verdicts
          .filter((v) => v.detail)
          .map((v) => (
            <p key={v.type} className={TONE_TEXT[v.tone]}>
              {t(v.label)}: {v.detail}
            </p>
          ))}
      </CardContent>
    </Card>
  )
}

/** Each node's ports and bonds as its agent reads them, and what they should be. */
export function NetworkLinksView() {
  const { t } = useI18n()
  const { state } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)
  return (
    <div className="space-y-6">
      <ExpectationsCard />
      <LiveStates state={state} emptyLabel={t('net.empty.machines')} />
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
