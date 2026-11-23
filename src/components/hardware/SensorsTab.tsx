import type { ReactNode } from 'react'
import { Fan, Lightning, Thermometer, Warning } from '@phosphor-icons/react'

import { TONE_TEXT } from '@/lib/thresholds'
import {
  eventSeverity,
  machineTemperatureReadings,
  sensorAvailability,
  stalenessLabel,
  type Machine,
  type SensorSeverity,
} from '@/lib/machines'
import { useI18n } from '@/i18n/context'

const SEVERITY_CLASS: Record<SensorSeverity, string> = {
  ok: TONE_TEXT.accent,
  warning: TONE_TEXT.warning,
  critical: TONE_TEXT.destructive,
  unknown: TONE_TEXT.muted,
}

/**
 * Sensor readings for one machine — temperatures, fans, power supplies.
 *
 * The BMC on this hardware replays its last cached reading identically
 * whether the machine is on, mid-POST, or has been off for twenty minutes:
 * measured on the real ML350 Gen9, CPU1 reported 40°C healthy with 46
 * sensors in all three states. So the controller only ever stores a reading
 * it can vouch for, clearing `machine.sensors` to null everywhere else — a
 * machine with no sensors is the *normal* case for five of the six states
 * `sensorAvailability` distinguishes, not a fetch failure. This renders each
 * one as a sentence an operator can act on, never as a blank panel or the
 * word "unavailable".
 *
 * `last-known` is the one state that still carries data: the controller
 * deliberately keeps the last valid reading beside its age rather than
 * blanking the panel on a transient BMC hiccup, so this renders the
 * retained sensors *and* `stalenessLabel(machine.sensorsValidAt, …)`
 * prominently beside them — showing the reading without its age would be
 * worse than not showing it. `sensorsValidAt` is a different clock from
 * `machine.lastProbeAt` (the machine's own liveness, rendered by
 * `MachineDetail`) and the two are never mixed here.
 */
export function SensorsTab({ machine }: { machine: Machine }) {
  const { t, tm } = useI18n()
  const availability = sensorAvailability(machine)

  switch (availability.kind) {
    case 'never-read':
      return (
        <Empty>{t('hw.sensors.neverRead')}</Empty>
      )
    case 'powered-off':
      return <Empty>{t('hw.sensors.off')}</Empty>
    case 'in-post':
      return (
        <Empty>{t('hw.sensors.post')}</Empty>
      )
    case 'unreachable':
      return (
        <Empty tone="warning">{t('hw.sensors.unreachable')}</Empty>
      )
    case 'last-known':
      return (
        <div className="space-y-4">
          <div className="flex items-center gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 font-mono text-xs">
            <Warning className="text-warning shrink-0" />
            <span>
              {t('hw.sensors.lastKnown')}{' '}
              <span className="font-medium">{tm(stalenessLabel(machine.sensorsValidAt, new Date()))}</span>
            </span>
          </div>
          <Readings machine={machine} />
        </div>
      )
    case 'available':
      return <Readings machine={machine} />

    default: {
      // Compile error if SensorAvailability (machines.ts) gains a kind with
      // no case above — without this, an unhandled kind falls through to
      // `undefined` and the screen renders a blank panel, the exact failure
      // this component exists to prevent (see App.tsx's `renderTab` for the
      // same pattern).
      const unhandled: never = availability
      return unhandled
    }
  }
}

function Empty({ children, tone }: { children: ReactNode; tone?: 'warning' }) {
  return (
    <div
      className={`py-8 text-center font-mono text-sm ${tone === 'warning' ? 'text-warning' : 'text-muted-foreground'}`}
    >
      {children}
    </div>
  )
}

/** Renders `machine.sensors`. Only reached from a case that already knows it is non-null. */
function Readings({ machine }: { machine: Machine }) {
  const { t } = useI18n()
  const sensors = machine.sensors
  if (!sensors) return null

  const temperatures = machineTemperatureReadings(machine)

  return (
    <div className="space-y-4 font-mono text-xs">
      <div>
        <span className="text-muted-foreground">{t('hw.sensors.power')}</span>{' '}
        <span>{sensors.powerConsumedWatts} W</span>
      </div>

      <Section icon={<Thermometer />} title={t('hw.sensors.temps', { n: temperatures.length })}>
        {temperatures.length === 0 ? (
          <div className="text-muted-foreground pl-5">{t('hw.noneReported')}</div>
        ) : (
          <div className="border rounded-md divide-y">
            {temperatures.map(({ reading, severity }) => (
              <div key={reading.name} className="flex items-center gap-3 px-3 py-1.5">
                <span className="flex-1 truncate">{reading.name}</span>
                {reading.upperCritical != null && (
                  <span className="text-muted-foreground">{t('hw.sensors.max', { v: reading.upperCritical })}</span>
                )}
                <span className={SEVERITY_CLASS[severity]}>{reading.celsius} °C</span>
              </div>
            ))}
          </div>
        )}
      </Section>

      {/* Fans render `${reading} ${units}` — never the bare number. On this
          hardware 23 means 23 percent; on other Redfish implementations the
          same field is RPM, and the unit is the only thing that tells them
          apart. */}
      <Section icon={<Fan />} title={t('hw.sensors.fans', { n: sensors.fans.length })}>
        {sensors.fans.length === 0 ? (
          <div className="text-muted-foreground pl-5">{t('hw.noneReported')}</div>
        ) : (
          <div className="border rounded-md divide-y">
            {sensors.fans.map((fan) => (
              <div key={fan.name} className="flex items-center gap-3 px-3 py-1.5">
                <span className="flex-1 truncate">{fan.name}</span>
                <span className={SEVERITY_CLASS[eventSeverity(fan.health)]}>
                  {fan.reading} {fan.units}
                </span>
              </div>
            ))}
          </div>
        )}
      </Section>

      <Section icon={<Lightning />} title={t('hw.sensors.psus', { n: sensors.powerSupplies.length })}>
        {sensors.powerSupplies.length === 0 ? (
          <div className="text-muted-foreground pl-5">{t('hw.noneReported')}</div>
        ) : (
          <div className="border rounded-md divide-y">
            {sensors.powerSupplies.map((psu) => (
              <div key={psu.name} className="flex items-center gap-3 px-3 py-1.5">
                <span className="flex-1 truncate">{psu.name}</span>
                <span className="text-muted-foreground">{psu.state}</span>
                <span className={SEVERITY_CLASS[eventSeverity(psu.health)]}>
                  {psu.lastPowerOutputWatts} W
                </span>
              </div>
            ))}
          </div>
        )}
      </Section>
    </div>
  )
}

function Section({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-1.5 text-muted-foreground">
        {icon}
        {title}
      </div>
      {children}
    </div>
  )
}
