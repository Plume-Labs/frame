import type { ReactNode } from 'react'
import { Cpu, HardDrives, Memory, Network } from '@phosphor-icons/react'

import { NEVER_READ_LABEL, type Machine } from '@/lib/machines'

/**
 * Static hardware inventory: what Redfish reported the last time the
 * controller successfully probed this machine.
 *
 * `inventory` is null whenever the machine has never been successfully
 * probed — a distinct state from "probed, and it genuinely has nothing" —
 * so this renders a sentence rather than an empty table, per the brief.
 */
export function InventoryTab({ machine }: { machine: Machine }) {
  const { inventory } = machine

  if (!inventory) {
    return (
      <div className="py-8 text-center font-mono text-sm text-muted-foreground">{NEVER_READ_LABEL}</div>
    )
  }

  return (
    <div className="space-y-4 font-mono text-xs">
      <div className="grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-3">
        <Field label="Manufacturer" value={inventory.manufacturer} />
        <Field label="Model" value={inventory.model} />
        <Field label="Serial" value={inventory.serialNumber} />
        <Field label="BIOS" value={inventory.biosVersion} />
        <Field label="BMC firmware" value={inventory.bmcFirmware} />
        <Field label="Memory" value={`${inventory.totalMemoryGiB} GiB`} />
      </div>

      <Section icon={<Cpu />} title={`Processors (${inventory.processors.length})`}>
        <Table
          columns={['Socket', 'Model', 'Cores', 'Threads']}
          rows={inventory.processors.map((p) => [p.socket, p.model, String(p.cores), String(p.threads)])}
        />
      </Section>

      <Section icon={<Memory />} title={`Memory modules (${inventory.memoryModules.length})`}>
        <Table
          columns={['Slot', 'Size', 'Type', 'Manufacturer']}
          rows={inventory.memoryModules.map((m) => [m.slot, `${m.sizeMiB} MiB`, m.type, m.manufacturer])}
        />
      </Section>

      <Section icon={<HardDrives />} title={`Drives (${inventory.drives.length})`}>
        <Table
          columns={['Name', 'Model', 'Size', 'Protocol', 'Health']}
          rows={inventory.drives.map((d) => [d.name, d.model, `${d.sizeGB} GB`, d.protocol, d.health])}
        />
      </Section>

      <Section icon={<Network />} title={`Network adapters (${inventory.networkAdapters.length})`}>
        <Table
          columns={['Name', 'MAC', 'Status']}
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
  if (rows.length === 0) {
    return <div className="text-muted-foreground pl-5">none reported</div>
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
