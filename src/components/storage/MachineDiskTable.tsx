import { Fragment, useState } from 'react'
import type { DiskRow } from '@/lib/disk-inventory'
import type { DiskClaim } from '@/lib/frame-sdk'
import { NOT_COLLECTED } from '@/lib/storage'

function osCell(d: DiskRow): string {
  if (d.presence === 'bmc-only') return 'ABSENT'
  if (d.presence === 'os-unreported') return 'no agent report'
  if (d.presence === 'no-serial') return 'cannot be joined (no serial)'
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
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs font-mono">
        <thead className="text-muted-foreground text-left">
          <tr>
            <th className="py-1 pr-3">bay</th>
            <th className="pr-3">serial</th>
            <th className="pr-3">model</th>
            <th className="pr-3 text-right">size</th>
            <th className="pr-3">role</th>
            <th className="pr-3">BMC</th>
            <th className="pr-3">OS</th>
            <th className="pr-3">temp</th>
            <th className="pr-3">SMART</th>
            <th>I/O util / await</th>
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
                    {d.serial || 'serial unknown'}
                    {d.duplicateOn.length ? ` (also on ${d.duplicateOn.join(', ')})` : ''}
                  </td>
                  <td className="pr-3">{d.model ?? '—'}</td>
                  <td className="pr-3 text-right">{d.sizeGB ? `${d.sizeGB} GB` : '—'}</td>
                  <td className="pr-3 font-bold">{d.role}</td>
                  <td className="pr-3">{d.bmcHealth ?? 'not listed'}</td>
                  <td className="pr-3">{osCell(d)}</td>
                  <td
                    className={`pr-3 ${d.temperatureCelsius === null ? 'text-muted-foreground' : ''}`}
                    title={d.temperatureCelsius === null ? 'Not reported by the BMC (0 °C is never shown)' : undefined}
                  >
                    {d.temperatureCelsius === null ? 'n/r' : `${d.temperatureCelsius} °C`}
                  </td>
                  <td className="pr-3 text-muted-foreground">{NOT_COLLECTED}</td>
                  <td>{io(d)}</td>
                </tr>
                {open === key && (
                  <tr className="bg-secondary/30">
                    <td colSpan={10} className="p-3 space-y-1">
                      <div>
                        {d.bay ?? 'no bay'} · {d.serial} · {d.model ?? '—'} · {d.protocol ?? '—'} {d.mediaType ?? ''}
                        {d.linkSpeedMbps ? ` · link ${d.linkSpeedMbps / 1000} Gb/s` : ''}
                        {d.rotationSpeedRPM ? ` · ${d.rotationSpeedRPM} rpm` : ''} · fw {d.firmware ?? NOT_COLLECTED}
                      </div>
                      <div>
                        WWN {d.wwn ?? 'not published'}
                        {d.byIdPath ? ` · ${d.byIdPath}` : ''}
                        {d.declaredInCeph === true ? ' · declared in the CephCluster spec' : ''}
                        {d.declaredInCeph === false && d.osd ? ' · NOT in the CephCluster device list' : ''}
                        {d.mountPoint ? ` · mounted on ${d.mountPoint}` : ''}
                        {d.members.length ? ` · member of ${d.members.join(', ')}` : ''}
                      </div>
                      {d.osd && (
                        <div>
                          Role: {d.osd.name} ({d.osd.deviceClass}) on {d.osd.host} · {d.osd.state} · {d.osd.pgs ?? '—'} PG ·
                          apply/commit {d.osd.applyMs ?? '—'}/{d.osd.commitMs ?? '—'} ms
                        </div>
                      )}
                      <div>
                        Sources: BMC {d.bmcHealth ?? 'not listed'}
                        {d.bmcReasons.length ? ` (${d.bmcReasons.join(', ')})` : ''} · OS {osCell(d)}
                        {d.presence === 'bmc-only' ? ' — seen by the BMC, not presented to the node (masked, not failed)' : ''}
                      </div>
                      <div className="text-muted-foreground">
                        SMART (reallocated, pending, grown defects): {NOT_COLLECTED} · power-on hours:{' '}
                        {d.powerOnHours ?? 'not reported'}
                      </div>
                      <div>
                        FrameDiskClaims:{' '}
                        {diskClaims.length === 0
                          ? 'none'
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
