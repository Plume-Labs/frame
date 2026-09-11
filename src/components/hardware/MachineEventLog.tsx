import { ClockCounterClockwise, Warning } from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { TONE_TEXT } from '@/lib/thresholds'
import { eventSeverity, type Machine, type SensorSeverity } from '@/lib/machines'

const SEVERITY_CLASS: Record<SensorSeverity, string> = {
  ok: TONE_TEXT.accent,
  warning: TONE_TEXT.warning,
  critical: TONE_TEXT.destructive,
  unknown: TONE_TEXT.muted,
}

// Fixed severity ordering for the counts row — most severe first, so
// "Critical: 4" is never buried behind a longer list of "OK" entries. Any
// key `eventSeverity` doesn't recognise (a Redfish severity this console has
// never seen) sorts after the four it does, rather than being dropped.
const SEVERITY_ORDER: Record<SensorSeverity, number> = { critical: 0, warning: 1, ok: 2, unknown: 3 }

/**
 * The machine's Redfish event log — most recent 25 entries, newest first
 * (guaranteed by the controller: `mapEventLog` in
 * `framemachine_controller.go` retains the head of `readLog`'s
 * newest-first sort, so this renders `machine.eventLog` in the order it
 * arrives rather than re-sorting it).
 *
 * `eventLogCounts` covers only the entries the last probe actually retrieved
 * from the BMC — one page, not necessarily the whole log. The redfish client
 * pages to the machine's last page when the log spans more than one, so this
 * is normally the newest page, but the BMC's own page size is not fixed and
 * the last page is typically shorter than the others: on the captured iLO4
 * the machine reports 175 entries total (`eventLogTotal`) across pages of
 * 30 except a final page of 25, so `eventLogCounts` sums to 25, not 175. The
 * sentence above the counts says exactly that — entries retrieved vs. the
 * machine's own total — rather than claiming coverage this field doesn't
 * have. `eventLogPossiblyStale` covers the case where the client couldn't
 * even confirm it reached that last page — see the banner below.
 */
export function MachineEventLog({ machine }: { machine: Machine }) {
  const counts = Object.entries(machine.eventLogCounts).sort(
    ([a], [b]) => (SEVERITY_ORDER[eventSeverity(a)] ?? 3) - (SEVERITY_ORDER[eventSeverity(b)] ?? 3),
  )

  return (
    <div className="space-y-3 font-mono text-xs">
      {machine.eventLogPossiblyStale && (
        <div className={`rounded-md border px-3 py-2 ${TONE_TEXT.warning}`}>
          Le BMC n'a pas confirmé que la page lue est bien la plus récente : les entrées
          ci-dessous pourraient être les plus anciennes du journal, pas les plus récentes.
        </div>
      )}
      <div className="rounded-md border bg-muted/30 px-3 py-2 space-y-2">
        <div className="text-muted-foreground">
          {machine.eventLogTotal} entrée{machine.eventLogTotal === 1 ? '' : 's'} au total sur le journal
          du BMC. Les {machine.eventLog.length} entrées ci-dessous sont celles que la dernière
          lecture a effectivement récupérées — les compteurs qui suivent portent sur ces
          entrées récupérées, pas sur le journal entier de la machine.
        </div>
        {counts.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {counts.map(([severity, count]) => (
              <Badge key={severity} variant="outline" className="font-mono text-[10px]">
                <span className={SEVERITY_CLASS[eventSeverity(severity)]}>{severity}</span>: {count}
              </Badge>
            ))}
          </div>
        )}
      </div>

      {machine.eventLog.length === 0 ? (
        <div className="py-8 text-center text-muted-foreground">
          Aucune entrée retenue dans le journal.
        </div>
      ) : (
        <div className="border rounded-md divide-y">
          {machine.eventLog.map((entry) => {
            const severity = eventSeverity(entry.severity)
            return (
              <div key={entry.id} className="flex items-start gap-3 px-3 py-1.5">
                {severity === 'critical' ? (
                  <Warning className={`shrink-0 mt-0.5 ${SEVERITY_CLASS[severity]}`} />
                ) : (
                  <ClockCounterClockwise className="shrink-0 mt-0.5 text-muted-foreground" />
                )}
                <div className="flex-1 min-w-0">
                  <div className="truncate">{entry.message}</div>
                  <div className="text-muted-foreground text-[10px]">{entry.created}</div>
                </div>
                <span className={`shrink-0 ${SEVERITY_CLASS[severity]}`}>{entry.severity}</span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
