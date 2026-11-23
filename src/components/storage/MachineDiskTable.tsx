import { Fragment, useState } from 'react'
import { smartCell, type DiskRow } from '@/lib/disk-inventory'
import { OSD_STATE_LABEL } from '@/lib/storage-labels'
import type { DiskClaim } from '@/lib/frame-sdk'
import { useI18n } from '@/i18n/context'
import type { Message } from '@/i18n'

type T = ReturnType<typeof useI18n>['t']

function osCell(t: T, d: DiskRow): string {
  if (d.presence === 'bmc-only') return t('mdt.absent')
  if (d.presence === 'os-unreported') return t('mdt.noAgentReport')
  if (d.presence === 'no-serial') return t('mdt.cannotJoin')
  return `${d.osPath ?? '—'} ${d.occupancy ?? ''}`.trim()
}

function io(d: DiskRow): string {
  if (d.io.utilPct === null && d.io.awaitMs === null) return '—'
  const u = d.io.utilPct === null ? '—' : `${d.io.utilPct.toFixed(1)} %`
  const w = d.io.awaitMs === null ? '—' : `${d.io.awaitMs.toFixed(1)} ms`
  return `${u} / ${w}`
}

/**
 * One row per disk of one machine, BMC columns and OS columns side by side
 * (joined on serial, never merged: a disk one side lacks still has a row).
 * A click opens the disk's sheet.
 */
export function MachineDiskTable({ rows, claims }: { rows: DiskRow[]; claims: DiskClaim[] }) {
  const { t, tm } = useI18n()
  const text = (x: string | Message) => (typeof x === 'string' ? x : tm(x))
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs font-mono">
        <thead className="text-muted-foreground text-left">
          <tr>
            <th className="py-1 pr-3">{t('mdt.col.bay')}</th>
            <th className="pr-3">{t('mdt.col.serial')}</th>
            <th className="pr-3">{t('mdt.col.model')}</th>
            <th className="pr-3 text-right">{t('osd.col.size')}</th>
            <th className="pr-3">{t('mdt.col.role')}</th>
            <th className="pr-3">BMC</th>
            <th className="pr-3">OS</th>
            <th className="pr-3">{t('mdt.col.temp')}</th>
            <th className="pr-3">SMART</th>
            <th>{t('mdt.col.io')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((d) => {
            const diskClaims = d.serial ? claims.filter((c) => c.serial === d.serial) : []
            const key = `${d.bay ?? ''}/${d.serial}`
            return (
              <Fragment key={key}>
                <tr
                  className={`border-t border-border cursor-pointer hover:bg-muted/50 ${d.presence !== 'both' && d.presence !== 'os-unreported' ? 'text-warning' : ''}`}
                  onClick={() => setOpen(open === key ? null : key)}
                >
                  <td className="py-1 pr-3">{d.bay ?? '—'}</td>
                  <td className="pr-3">
                    {d.serial || t('mdt.serialUnknown')}
                    {d.duplicateOn.length ? ` (${t('mdt.alsoOn', { list: d.duplicateOn.join(', ') })})` : ''}
                  </td>
                  <td className="pr-3">{d.model ?? '—'}</td>
                  <td className="pr-3 text-right">{d.sizeGB ? `${d.sizeGB} GB` : '—'}</td>
                  <td className="pr-3 font-bold">{text(d.role)}</td>
                  <td className="pr-3">{d.bmcHealth ?? t('mdt.notListed')}</td>
                  <td className="pr-3">{osCell(t, d)}</td>
                  <td
                    className={`pr-3 ${d.temperatureCelsius === null ? 'text-muted-foreground' : ''}`}
                    title={d.temperatureCelsius === null ? t('mdt.tempTitle') : undefined}
                  >
                    {d.temperatureCelsius === null ? t('mdt.notReported') : `${d.temperatureCelsius} °C`}
                  </td>
                  <td
                    className={`pr-3 ${d.smart?.passed === false ? 'text-warning font-bold' : d.smart?.passed === true ? '' : 'text-muted-foreground'}`}
                    title={d.smart?.error}
                  >
                    {tm(smartCell(d.smart))}
                  </td>
                  <td>{io(d)}</td>
                </tr>
                {open === key && (
                  <tr className="bg-secondary/30">
                    <td colSpan={10} className="p-3 space-y-1">
                      <div>
                        {d.bay ?? t('mdt.noBay')} · {d.serial} · {d.model ?? '—'} · {d.protocol ?? '—'} {d.mediaType ?? ''}
                        {d.linkSpeedMbps ? ` · ${t('mdt.link', { gbps: d.linkSpeedMbps / 1000 })}` : ''}
                        {d.rotationSpeedRPM ? ` · ${d.rotationSpeedRPM} rpm` : ''} · {t('mdt.fw', { fw: d.firmware ?? t('stv.notCollected') })}
                      </div>
                      <div>
                        WWN {d.wwn ?? t('mdt.wwnNotPublished')}
                        {d.byIdPath ? ` · ${d.byIdPath}` : ''}
                        {d.declaredInCeph === true ? ` · ${t('mdt.declared')}` : ''}
                        {d.declaredInCeph === false && d.osd ? ` · ${t('mdt.notDeclared')}` : ''}
                        {d.mountPoint ? ` · ${t('mdt.mountedOn', { path: d.mountPoint })}` : ''}
                        {d.members.length ? ` · ${t('mdt.memberOf', { list: d.members.join(', ') })}` : ''}
                      </div>
                      {d.osd && (
                        <div>
                          {t('mdt.roleLine', {
                            name: d.osd.name,
                            cls: d.osd.deviceClass,
                            host: d.osd.host,
                            state: t(OSD_STATE_LABEL[d.osd.state]),
                            pgs: d.osd.pgs ?? '—',
                            apply: d.osd.applyMs ?? '—',
                            commit: d.osd.commitMs ?? '—',
                          })}
                        </div>
                      )}
                      <div>
                        {t('mdt.sources', { bmc: d.bmcHealth ?? t('mdt.notListed') })}
                        {d.bmcReasons.length ? ` (${d.bmcReasons.join(', ')})` : ''} · OS {osCell(t, d)}
                        {d.presence === 'bmc-only' ? ` — ${t('mdt.masked')}` : ''}
                      </div>
                      {d.smart && (
                        <div>
                          SMART: {tm(smartCell(d.smart))}
                          {d.smart.readAt ? ` · ${t('lsc.readAt', { at: d.smart.readAt })}` : ''}
                          {d.smart.error ? ` · ${d.smart.error}` : ''}
                        </div>
                      )}
                      <div className="text-muted-foreground">
                        {t('mdt.smartDetail', { value: t('stv.notCollected'), hours: d.powerOnHours ?? t('mdt.notReportedLong') })}
                      </div>
                      <div>
                        FrameDiskClaims:{' '}
                        {diskClaims.length === 0
                          ? t('mdt.none')
                          : diskClaims.map((c) => `${c.name} → ${c.destination} (${c.phase})`).join(', ')}
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
