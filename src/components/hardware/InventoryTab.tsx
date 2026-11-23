import type { ReactNode } from 'react'
import { Cpu, HardDrives, Memory, Network } from '@phosphor-icons/react'

import { NEVER_READ_LABEL, type Machine } from '@/lib/machines'
import { useI18n } from '@/i18n/context'

/**
 * Static hardware inventory: what Redfish reported the last time the
 * controller successfully probed this machine.
 *
 * `inventory` is null whenever the machine has never been successfully
 * probed — a distinct state from "probed, and it genuinely has nothing" —
 * so this renders a sentence rather than an empty table, per the brief.
 */
export function InventoryTab({ machine }: { machine: Machine }) {
  const { t, tm } = useI18n()
  const { inventory } = machine

  if (!inventory) {
    return (
      <div className="py-8 text-center font-mono text-sm text-muted-foreground">{tm(NEVER_READ_LABEL)}</div>
    )
  }

  return (
    <div className="space-y-4 font-mono text-xs">
      <div className="grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-3">
        <Field label={t('hw.inv.manufacturer')} value={inventory.manufacturer} />
        <Field label={t('hw.inv.model')} value={inventory.model} />
        <Field label={t('hw.inv.serial')} value={inventory.serialNumber} />
        <Field label={t('hw.inv.bios')} value={inventory.biosVersion} />
        <Field label={t('hw.inv.bmc')} value={inventory.bmcFirmware} />
        <Field label={t('hw.inv.memory')} value={`${inventory.totalMemoryGiB} GiB`} />
      </div>

      <Section icon={<Cpu />} title={t('hw.inv.processors', { n: inventory.processors.length })}>
        <Table
          columns={[t('hw.col.socket'), t('hw.inv.model'), t('hw.col.cores'), t('hw.col.threads')]}
          rows={inventory.processors.map((p) => [p.socket, p.model, String(p.cores), String(p.threads)])}
        />
      </Section>

      <Section icon={<Memory />} title={t('hw.inv.modules', { n: inventory.memoryModules.length })}>
        <Table
          columns={[t('hw.col.slot'), t('hw.col.size'), t('hw.col.type'), t('hw.inv.manufacturer')]}
          rows={inventory.memoryModules.map((m) => [m.slot, `${m.sizeMiB} MiB`, m.type, m.manufacturer])}
        />
      </Section>

      {/* internal/redfish deliberately never walks HPE's SmartStorage OEM
          tree (see client.go's comment at Drives' population site), so
          `drives` is always empty on this BMC family — not because the
          machine has no disks. "Drives (0) — none reported" would be a
          false statement about the machine; this says what is actually
          true, which is a gap in what we collect, not in what is there. */}
      <Section icon={<HardDrives />} title={t('hw.inv.drives')}>
        {inventory.drives.length === 0 ? (
          <div className="text-muted-foreground pl-5">
            {t('hw.inv.drivesNotCollected')}
          </div>
        ) : (
          <Table
            columns={[t('hw.col.name'), t('hw.inv.model'), t('hw.col.size'), t('hw.col.protocol'), t('hw.col.health')]}
            rows={inventory.drives.map((d) => [d.name, d.model, `${d.sizeGB} GB`, d.protocol, d.health])}
          />
        )}
      </Section>

      <Section icon={<Network />} title={t('hw.inv.adapters', { n: inventory.networkAdapters.length })}>
        <Table
          columns={[t('hw.col.name'), t('hw.col.mac'), t('hw.col.status')]}
          rows={inventory.networkAdapters.map((n) => [n.name, n.mac, n.status])}
        />
      </Section>
    </div>
  )
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-muted-foreground">{label}</div>
      <div className="truncate">{value || '—'}</div>
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

function Table({ columns, rows }: { columns: string[]; rows: string[][] }) {
  const { t } = useI18n()
  if (rows.length === 0) {
    return <div className="text-muted-foreground pl-5">{t('hw.noneReported')}</div>
  }
  return (
    <div className="border rounded-md overflow-x-auto">
      <table className="w-full text-xs">
        <thead>
          <tr className="border-b bg-muted/30">
            {columns.map((c) => (
              <th key={c} className="text-left px-2 py-1 font-medium">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i} className="border-b last:border-0">
              {row.map((cell, j) => (
                <td key={j} className="px-2 py-1 truncate max-w-[16rem]">
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
