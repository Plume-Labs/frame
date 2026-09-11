import { useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { createFrameClient } from '@/lib/frame-sdk'
import {
  CONFIRM_SERIAL_HINT,
  CONFIRM_SERIAL_LABEL,
  confirmationMatches,
  machineOptionLabel,
  type InstallCreateSpec,
} from '@/lib/installs'
import type { Machine } from '@/lib/machines'

const frame = createFrameClient()

type LayoutKind = 'single-disk' | 'mirror'
type ClusterMode = 'init' | 'join'
type BootMode = 'UEFI' | 'Legacy'

interface DiskField {
  byID: string
  sizeBytes: string
}

const EMPTY_DISK: DiskField = { byID: '', sizeBytes: '' }

function diskCountFor(kind: LayoutKind): number {
  return kind === 'mirror' ? 2 : 1
}

/**
 * Starts a `FrameInstall`: picks the machine, names the node, chooses the
 * disk layout and cluster role, and requires the machine's own serial typed
 * back before the button unlocks.
 *
 * Only an inventoried `FrameMachine` is offered — one the operator has
 * already registered and the controller has successfully probed over
 * Redfish — because `confirmationMatches` needs a real serial to check
 * against, and a machine nobody has ever read a serial from cannot be
 * confirmed at all.
 *
 * **The serial is never displayed.** It is compared against, and that is
 * all. This dialog used to print it in the picker's label and again beside
 * the confirmation box, which made layer 2 of the destructive guard (design
 * §8) a typing exercise: the one control that answers *which machine* had
 * its answer on screen, so pointing at the wrong machine still confirmed
 * cleanly. Every machine-derived string this dialog renders comes from
 * `installs.ts` (`machineOptionLabel`, `CONFIRM_SERIAL_LABEL`,
 * `CONFIRM_SERIAL_HINT`), which is where vitest can assert that none of
 * them carries it.
 *
 * Disk `byID` fields are free text, not read off the machine's inventory:
 * `FrameMachine.status.inventory.drives[].name` is a Redfish drive name
 * (physical drives sit under HPE's OEM SmartStorage tree, which nothing in
 * this codebase walks — `internal/redfish` leaves `Drives` nil), and
 * `InstallDisk.byID` is CEL-forced to a `/dev/disk/by-id/...` path. The two
 * are different namespaces a BMC has no way to reconcile (see
 * `frameinstall_controller.go`'s comment on the removed `checkDisksKnown`
 * guard) — an operator reads the real by-id name from the machine itself
 * (`lsblk -d -o NAME,SIZE,MODEL,WWN`, on a rescue boot or a prior OS), the
 * same way the deployment runbook does.
 */
export function InstallDialog({
  open,
  onOpenChange,
  machines,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  machines: Machine[]
  onCreated: () => void
}) {
  const inventoried = useMemo(() => machines.filter((m) => m.inventory !== null), [machines])

  const [machineName, setMachineName] = useState('')
  const [hostname, setHostname] = useState('')
  const [address, setAddress] = useState('')
  const [gateway, setGateway] = useState('')
  const [layoutKind, setLayoutKind] = useState<LayoutKind>('single-disk')
  const [disks, setDisks] = useState<DiskField[]>([EMPTY_DISK])
  const [clusterMode, setClusterMode] = useState<ClusterMode>('init')
  const [serverURL, setServerURL] = useState('')
  const [joinTokenRef, setJoinTokenRef] = useState('')
  const [k3sVersion, setK3sVersion] = useState('')
  const [sshKeyRef, setSshKeyRef] = useState('')
  const [bootMode, setBootMode] = useState<BootMode>('UEFI')
  const [confirmSerial, setConfirmSerial] = useState('')
  const [busy, setBusy] = useState(false)

  // A dialog reopened for a second install must not carry the first one's
  // half-typed confirmation forward — that is exactly the state in which
  // "type the serial to confirm" stops meaning anything.
  useEffect(() => {
    if (!open) return
    setMachineName('')
    setHostname('')
    setAddress('')
    setGateway('')
    setLayoutKind('single-disk')
    setDisks([EMPTY_DISK])
    setClusterMode('init')
    setServerURL('')
    setJoinTokenRef('')
    setK3sVersion('')
    setSshKeyRef('')
    setBootMode('UEFI')
    setConfirmSerial('')
  }, [open])

  const selectedMachine = inventoried.find((m) => m.name === machineName)
  const serial = selectedMachine?.inventory?.serialNumber ?? ''

  const setLayout = (kind: LayoutKind) => {
    setLayoutKind(kind)
    setDisks((prev) => {
      const n = diskCountFor(kind)
      if (prev.length === n) return prev
      return Array.from({ length: n }, (_, i) => prev[i] ?? EMPTY_DISK)
    })
  }

  const updateDisk = (i: number, field: keyof DiskField, value: string) => {
    setDisks((prev) => prev.map((d, idx) => (idx === i ? { ...d, [field]: value } : d)))
  }

  const parsedDisks = disks.map((d) => ({
    byID: d.byID.trim(),
    sizeBytes: Number(d.sizeBytes),
  }))
  const disksValid =
    parsedDisks.length === diskCountFor(layoutKind) &&
    parsedDisks.every((d) => d.byID.startsWith('/dev/disk/by-id/') && Number.isFinite(d.sizeBytes) && d.sizeBytes > 0)

  const joinValid = clusterMode === 'init' || (serverURL.trim() !== '' && joinTokenRef.trim() !== '')

  const canSubmit =
    !!selectedMachine &&
    hostname.trim() !== '' &&
    address.trim() !== '' &&
    gateway.trim() !== '' &&
    sshKeyRef.trim() !== '' &&
    k3sVersion.trim() !== '' &&
    disksValid &&
    joinValid &&
    confirmationMatches(confirmSerial, serial) &&
    !busy

  async function handleCreate() {
    if (!selectedMachine || !canSubmit) return
    setBusy(true)
    try {
      const spec: InstallCreateSpec = {
        machineRef: selectedMachine.name,
        confirmSerial: confirmSerial.trim(),
        hostname: hostname.trim(),
        network: { address: address.trim(), gateway: gateway.trim() },
        layout: { kind: layoutKind, disks: parsedDisks },
        cluster:
          clusterMode === 'init'
            ? { mode: 'init', k3sVersion: k3sVersion.trim() }
            : {
                mode: 'join',
                serverURL: serverURL.trim(),
                joinTokenRef: joinTokenRef.trim(),
                k3sVersion: k3sVersion.trim(),
              },
        bootMode,
        sshKeyRef: sshKeyRef.trim(),
      }
      await frame.installs.create(hostname.trim(), spec)
      toast.success(`Install created for ${selectedMachine.name}`)
      onOpenChange(false)
      onCreated()
    } catch (e) {
      toast.error('Failed to create install', {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !busy && onOpenChange(o)}>
      <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="font-mono">New install</DialogTitle>
          <DialogDescription>
            This erases every disk named below on the selected machine and installs Debian from
            scratch. There is no undo — confirm only against a machine you mean to wipe.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label>Machine</Label>
            <Select value={machineName} onValueChange={setMachineName}>
              <SelectTrigger>
                <SelectValue placeholder="Select an inventoried machine…" />
              </SelectTrigger>
              <SelectContent>
                {inventoried.map((m) => (
                  <SelectItem key={m.name} value={m.name}>
                    {machineOptionLabel({
                      name: m.name,
                      model: m.inventory?.model ?? '',
                      serialNumber: m.inventory?.serialNumber ?? '',
                    })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {inventoried.length === 0 && (
              <p className="text-[10px] text-muted-foreground">
                No machine has been probed yet — register one and wait for its inventory before
                installing.
              </p>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="install-hostname">Hostname</Label>
              <Input id="install-hostname" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="w3" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="install-ssh-key-ref">SSH key Secret</Label>
              <Input
                id="install-ssh-key-ref"
                value={sshKeyRef}
                onChange={(e) => setSshKeyRef(e.target.value)}
                placeholder="frame-install-ssh"
              />
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="install-address">Address (CIDR)</Label>
              <Input
                id="install-address"
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="192.168.2.213/24"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="install-gateway">Gateway</Label>
              <Input id="install-gateway" value={gateway} onChange={(e) => setGateway(e.target.value)} placeholder="192.168.2.1" />
            </div>
          </div>

          <div className="space-y-1.5">
            <Label>Layout</Label>
            <Select value={layoutKind} onValueChange={(v) => setLayout(v as LayoutKind)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="single-disk">Single disk</SelectItem>
                <SelectItem value="mirror">Mirror (2 disks)</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {disks.map((d, i) => (
            <div key={i} className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor={`install-disk-${i}-byid`}>
                  Disk {i + 1} — /dev/disk/by-id/…
                </Label>
                <Input
                  id={`install-disk-${i}-byid`}
                  value={d.byID}
                  onChange={(e) => updateDisk(i, 'byID', e.target.value)}
                  placeholder="/dev/disk/by-id/scsi-3600..."
                  className="font-mono text-xs"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor={`install-disk-${i}-size`}>Size (bytes)</Label>
                <Input
                  id={`install-disk-${i}-size`}
                  type="number"
                  value={d.sizeBytes}
                  onChange={(e) => updateDisk(i, 'sizeBytes', e.target.value)}
                  placeholder="300000000000"
                />
              </div>
            </div>
          ))}

          <div className="space-y-1.5">
            <Label>Cluster</Label>
            <Select value={clusterMode} onValueChange={(v) => setClusterMode(v as ClusterMode)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="init">Start a new cluster (cluster-init)</SelectItem>
                <SelectItem value="join">Join an existing cluster</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {clusterMode === 'join' && (
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="install-server-url">Server URL</Label>
                <Input
                  id="install-server-url"
                  value={serverURL}
                  onChange={(e) => setServerURL(e.target.value)}
                  placeholder="https://192.168.2.201:6443"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="install-join-token-ref">Join token Secret</Label>
                <Input
                  id="install-join-token-ref"
                  value={joinTokenRef}
                  onChange={(e) => setJoinTokenRef(e.target.value)}
                  placeholder="frame-k3s-join-token"
                />
              </div>
            </div>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="install-k3s-version">k3s version</Label>
            <Input
              id="install-k3s-version"
              value={k3sVersion}
              onChange={(e) => setK3sVersion(e.target.value)}
              placeholder="v1.33.4+k3s1"
              className="font-mono text-xs"
            />
          </div>

          <div className="space-y-1.5">
            <Label>Boot mode</Label>
            <Select value={bootMode} onValueChange={(v) => setBootMode(v as BootMode)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="UEFI">UEFI</SelectItem>
                <SelectItem value="Legacy">Legacy BIOS</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-[10px] text-muted-foreground">
              Set on the machine, never inherited from it. An image that boots in the other mode
              says nothing — it stays on a black screen until the phase times out.
            </p>
          </div>

          <div className="space-y-1.5 pt-2 border-t border-border">
            <Label htmlFor="install-confirm-serial">{CONFIRM_SERIAL_LABEL}</Label>
            <Input
              id="install-confirm-serial"
              value={confirmSerial}
              onChange={(e) => setConfirmSerial(e.target.value)}
              disabled={!selectedMachine}
              className="font-mono text-xs"
              autoComplete="off"
            />
            <p className="text-[10px] text-muted-foreground">{CONFIRM_SERIAL_HINT}</p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" className="font-mono" onClick={() => onOpenChange(false)} disabled={busy}>
            Cancel
          </Button>
          <Button className="font-mono" disabled={!canSubmit} onClick={() => void handleCreate()}>
            {busy ? 'Creating…' : 'Erase and install'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
