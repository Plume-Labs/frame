import { useEffect, useMemo, useState } from 'react'
import { ClusterNode } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Separator } from '@/components/ui/separator'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { parseDnsList } from '@/lib/nodeProvisioning'
import { createFrameClient, FrameNodeStatus } from '@/lib/frame-sdk'

const frame = createFrameClient()

interface NodeProvisionWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  racks: string[]
  controlPlaneCount: number
  onNodeProvisioned: (node: ClusterNode) => void
}

interface DiscoverData {
  hostname: string
  talosVersion: string
  disks: Array<{ name: string; size: string; type: string }>
  nics: Array<{ name: string; mac: string; speed: string }>
}

type Role = 'controlplane' | 'worker'

type ServiceClass = 'HIGH' | 'MEDIUM' | 'LOW'

const STEPS = [
  'Discovery',
  'Role',
  'Network',
  'Hardware',
  'Placement',
  'Review & Apply',
] as const
const MAX_STATUS_POLL_FAILURES = 5

export function NodeProvisionWizard({
  open,
  onOpenChange,
  racks,
  controlPlaneCount,
  onNodeProvisioned,
}: NodeProvisionWizardProps) {
  const [step, setStep] = useState(0)
  const [ip, setIp] = useState('')
  const [discovering, setDiscovering] = useState(false)
  const [discoverError, setDiscoverError] = useState<string | null>(null)
  const [discoverData, setDiscoverData] = useState<DiscoverData | null>(null)

  const [role, setRole] = useState<Role>('worker')
  const [networkAddress, setNetworkAddress] = useState('')
  const [networkGateway, setNetworkGateway] = useState('')
  const [dnsRaw, setDnsRaw] = useState('1.1.1.1,8.8.8.8')
  const [vlan, setVlan] = useState('')
  const [bond, setBond] = useState('')

  const [disk, setDisk] = useState('')
  const [rdmaInterface, setRdmaInterface] = useState('')
  const [hostnameOverride, setHostnameOverride] = useState('')

  const [rack, setRack] = useState(racks[0] ?? 'rack-01')
  // Local clusters are single-zone; the FrameNode CRD still carries an optional
  // spec.zone, so we send a stable 'local' rather than a user-picked value.
  const zone = 'local'
  const [serviceClass, setServiceClass] = useState<ServiceClass>('MEDIUM')

  const [applying, setApplying] = useState(false)
  const [provisionError, setProvisionError] = useState<string | null>(null)
  const [provisionNodeId, setProvisionNodeId] = useState<string | null>(null)
  const [provisionLogs, setProvisionLogs] = useState<string[]>([])
  const [provisionStatus, setProvisionStatus] = useState<'provisioning' | 'online' | 'offline' | null>(null)

  const progress = ((step + 1) / STEPS.length) * 100
  const dns = useMemo(() => parseDnsList(dnsRaw), [dnsRaw])

  useEffect(() => {
    if (!open) {
      setStep(0)
      setDiscoverData(null)
      setDiscoverError(null)
      setProvisionError(null)
      setProvisionNodeId(null)
      setProvisionLogs([])
      setProvisionStatus(null)
      setApplying(false)
      setDiscovering(false)
    }
  }, [open])

  // Poll FrameNode status until phase=Discovered (controller contacted maintenance API).
  useEffect(() => {
    if (!provisionNodeId || !discovering) return

    let cancelled = false
    let failureCount = 0

    const poll = async () => {
      if (cancelled) return
      try {
        const status: FrameNodeStatus = await frame.nodes.getStatus(provisionNodeId)
        failureCount = 0

        if (status.phase === 'Discovered') {
          const disks = status.discoveredDisks ?? []
          const nics  = status.discoveredNICs ?? []
          const discovered: DiscoverData = {
            hostname:     status.discoveredHostname ?? provisionNodeId,
            talosVersion: status.discoveredTalosVersion ?? 'unknown',
            disks,
            nics,
          }
          setDiscoverData(discovered)
          setDisk(disks[0]?.name ?? '')
          setRdmaInterface(nics.find((n) => n.name.startsWith('ib'))?.name ?? '')
          setHostnameOverride(discovered.hostname)
          if (!networkAddress) setNetworkAddress(`${ip}/24`)
          if (!networkGateway) {
            const octets = ip.split('.')
            if (octets.length === 4) setNetworkGateway(`${octets[0]}.${octets[1]}.${octets[2]}.1`)
          }
          setDiscovering(false)
          setStep(1)
          return
        }
        if (status.phase === 'Failed') {
          setDiscoverError('Discovery failed — node not reachable in maintenance mode')
          setDiscovering(false)
          return
        }
      } catch {
        failureCount += 1
        if (failureCount >= MAX_STATUS_POLL_FAILURES) {
          setDiscoverError('Cannot reach node status — check kubectl proxy is running')
          setDiscovering(false)
          return
        }
      }

      if (!cancelled) setTimeout(poll, 2000)
    }

    setTimeout(poll, 1000)
    return () => { cancelled = true }
  }, [provisionNodeId, discovering, ip, networkAddress, networkGateway])

  // Poll FrameNode phase after provisioning spec is applied.
  useEffect(() => {
    if (!provisionNodeId || !applying) return

    let failureCount = 0
    let timeoutId: ReturnType<typeof setTimeout> | undefined
    let cancelled = false
    let stopPolling = false

    const pollStatus = async () => {
      if (cancelled || stopPolling) return
      try {
        const status: FrameNodeStatus = await frame.nodes.getStatus(provisionNodeId)
        failureCount = 0
        const mapped =
          status.phase === 'Online'   ? 'online'  :
          status.phase === 'Failed' || status.phase === 'Offline' ? 'offline' :
          'provisioning'
        setProvisionStatus(mapped)
        setProvisionLogs([`Phase: ${status.phase}`])
        if (mapped !== 'provisioning') {
          setApplying(false)
          stopPolling = true
          return
        }
      } catch {
        failureCount += 1
        if (failureCount >= MAX_STATUS_POLL_FAILURES) {
          setApplying(false)
          setProvisionError('Provisioning status polling failed repeatedly')
          stopPolling = true
          return
        }
      } finally {
        if (!cancelled && !stopPolling) {
          timeoutId = setTimeout(pollStatus, 2000)
        }
      }
    }

    void pollStatus()

    return () => {
      cancelled = true
      if (timeoutId) clearTimeout(timeoutId)
    }
  }, [applying, provisionNodeId])

  async function runDiscovery() {
    setDiscovering(true)
    setDiscoverError(null)
    setDiscoverData(null)

    try {
      const { crName } = await frame.nodes.discover(ip)
      setProvisionNodeId(crName)
      // Discovery polling effect picks up once provisionNodeId + discovering are both set.
    } catch (e) {
      setDiscoverError(e instanceof Error ? e.message : 'Discovery failed — check kubectl proxy')
      setDiscovering(false)
    }
  }

  async function applyConfiguration() {
    if (!provisionNodeId) return
    setApplying(true)
    setProvisionError(null)
    setProvisionLogs(['Patching FrameNode spec — controller will apply Talos machineConfig...'])

    try {
      await frame.nodes.patchSpec(provisionNodeId, {
        ip,
        role,
        disk,
        hostname:      hostnameOverride || undefined,
        rack,
        zone,
        serviceClass,
        rdmaInterface: rdmaInterface || undefined,
        network: {
          address: networkAddress,
          gateway: networkGateway,
          dns,
          vlan:  vlan ? Number(vlan) : undefined,
          bond:  bond || undefined,
        },
      })

      setProvisionStatus('provisioning')
      setProvisionLogs((current) => [...current, `FrameNode ${provisionNodeId} updated — waiting for node to join cluster`])

      onNodeProvisioned({
        id:       provisionNodeId,
        name:     hostnameOverride || discoverData?.hostname || provisionNodeId,
        status:   'provisioning',
        metrics:  { cpu: 0, memory: 0, storage: 0, network: 0 },
        uptime:   0,
        lastSeen: Date.now(),
        network: {
          rxBytes: 0, txBytes: 0, latency: 0,
          rdmaActive: Boolean(rdmaInterface), rdmaQueuePairs: 0,
          bandwidth: 0, packetLoss: 0, sriovVFs: 0, dpdkEnabled: false,
          ciliumVersion: 'unknown', ebpfBypassActive: false,
        },
        storage: {
          cephOSDs: 0, cephPGs: 0, totalCapacity: 0, usedCapacity: 0,
          readIOPS: 0, writeIOPS: 0, replicationFactor: 3,
          dataFabricEnabled: false, metadataEntries: 0, activeDatasets: 0,
        },
        hardware: {
          cpuModel: 'pending', cpuCores: 0, memoryGB: 0, storageGB: 0,
          networkAdapters: discoverData?.nics.length ?? 1, pxeBooted: false,
          temperature: 0, deviceType: 'server', rackUnits: 1, numaNode: 0,
          cacheHitRate: 0, storageTier: 'nvme', gpuMIGInstances: 0,
          hugepagesGB: 0, cpuPinnedCores: 0, topologyManagerPolicy: 'none',
        },
        zone,
        rackId: rack,
        rackPosition: 1,
        serviceClass,
      })
    } catch (e) {
      setApplying(false)
      setProvisionError(e instanceof Error ? e.message : 'Failed to patch FrameNode spec')
    }
  }

  function canContinue(): boolean {
    if (step === 0) return Boolean(discoverData)
    if (step === 2) return Boolean(networkAddress && networkGateway && dns.length > 0)
    if (step === 3) return Boolean(disk)
    if (step === 4) return Boolean(rack)
    return true
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-3xl overflow-y-hidden p-0">
        <SheetHeader className="border-b border-primary/20 bg-background/95">
          <SheetTitle className="font-mono text-primary flex items-center justify-between">
            Node Provision Wizard
            <Badge className="font-mono bg-primary text-primary-foreground">Step {step + 1}/{STEPS.length}</Badge>
          </SheetTitle>
          <SheetDescription className="font-mono text-xs text-muted-foreground">
            Boot Talos vanilla ISO in maintenance mode, then apply machine configuration from Frame.
          </SheetDescription>
          <Progress value={progress} className="h-2" />
        </SheetHeader>

        <div className="p-4 space-y-4">
          <div className="font-mono text-xs text-muted-foreground">{STEPS[step]}</div>

          {step === 0 && (
            <div className="space-y-4">
              <div className="space-y-2">
                <Label className="font-mono text-xs">Node IP Address</Label>
                <div className="flex gap-2">
                  <Input
                    className="font-mono"
                    placeholder="192.168.10.25"
                    value={ip}
                    onChange={(e) => setIp(e.target.value)}
                  />
                  <Button className="font-mono" onClick={runDiscovery} disabled={!ip || discovering}>
                    {discovering ? 'Scanning...' : 'Scan Node'}
                  </Button>
                </div>
              </div>

              {discovering && !discoverData && (
                <div className="font-mono text-xs text-muted-foreground">
                  Creating FrameNode CR — waiting for controller to contact {ip}:50000...
                </div>
              )}

              {discoverError && <div className="font-mono text-xs text-warning">{discoverError}</div>}

              {discoverData && (
                <div className="space-y-3 rounded-md border border-primary/30 p-3">
                  <div className="font-mono text-xs">Hostname: <span className="text-primary">{discoverData.hostname}</span></div>
                  <div className="font-mono text-xs">Talos: {discoverData.talosVersion}</div>
                  <Separator />
                  <div className="grid grid-cols-2 gap-3 text-xs font-mono">
                    <div>
                      <div className="mb-1 text-muted-foreground">Disks</div>
                      {discoverData.disks.length === 0
                        ? <div className="text-muted-foreground">Enter disk manually below</div>
                        : discoverData.disks.map((entry) => (
                            <div key={entry.name}>{entry.name} • {entry.size} • {entry.type}</div>
                          ))}
                    </div>
                    <div>
                      <div className="mb-1 text-muted-foreground">NICs</div>
                      {discoverData.nics.length === 0
                        ? <div className="text-muted-foreground">Enter NIC manually below</div>
                        : discoverData.nics.map((entry) => (
                            <div key={entry.name}>{entry.name} • {entry.speed}</div>
                          ))}
                    </div>
                  </div>
                </div>
              )}
            </div>
          )}

          {step === 1 && (
            <div className="space-y-3">
              <div className="grid grid-cols-2 gap-3">
                <Button
                  type="button"
                  variant={role === 'controlplane' ? 'default' : 'outline'}
                  className="font-mono"
                  onClick={() => setRole('controlplane')}
                >
                  control-plane
                </Button>
                <Button
                  type="button"
                  variant={role === 'worker' ? 'default' : 'outline'}
                  className="font-mono"
                  onClick={() => setRole('worker')}
                >
                  worker
                </Button>
              </div>
              {role === 'controlplane' && controlPlaneCount === 1 && (
                <div className="rounded-md border border-warning/40 bg-warning/10 p-2 text-xs font-mono text-warning">
                  Warning: this would result in 2 control-plane nodes (even count). Odd control-plane counts are recommended.
                </div>
              )}
            </div>
          )}

          {step === 2 && (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <Field label="Address / CIDR" value={networkAddress} onChange={setNetworkAddress} />
              <Field label="Gateway" value={networkGateway} onChange={setNetworkGateway} />
              <Field label="DNS servers (comma separated)" value={dnsRaw} onChange={setDnsRaw} />
              <Field label="VLAN ID (optional)" value={vlan} onChange={setVlan} />
              <Field label="Bond interface (optional)" value={bond} onChange={setBond} />
            </div>
          )}

          {step === 3 && (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="space-y-2">
                <Label className="font-mono text-xs">Install Disk</Label>
                {(discoverData?.disks ?? []).length > 0 ? (
                  <Select value={disk} onValueChange={setDisk}>
                    <SelectTrigger className="w-full font-mono">
                      <SelectValue placeholder="Select disk" />
                    </SelectTrigger>
                    <SelectContent>
                      {(discoverData?.disks ?? []).map((entry) => (
                        <SelectItem key={entry.name} value={entry.name} className="font-mono">
                          {entry.name} ({entry.size})
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : (
                  <Field label="Disk path (e.g. /dev/nvme0n1)" value={disk} onChange={setDisk} />
                )}
              </div>
              <Field label="RDMA / InfiniBand interface (optional)" value={rdmaInterface} onChange={setRdmaInterface} />
              <Field label="Hostname override (optional)" value={hostnameOverride} onChange={setHostnameOverride} />
            </div>
          )}

          {step === 4 && (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="space-y-2">
                <Label className="font-mono text-xs">Rack</Label>
                <Select value={rack} onValueChange={setRack}>
                  <SelectTrigger className="w-full font-mono"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {racks.map((entry) => (
                      <SelectItem key={entry} value={entry} className="font-mono">{entry}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label className="font-mono text-xs">Service Class</Label>
                <Select value={serviceClass} onValueChange={(value) => setServiceClass(value as ServiceClass)}>
                  <SelectTrigger className="w-full font-mono"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="HIGH" className="font-mono">HIGH</SelectItem>
                    <SelectItem value="MEDIUM" className="font-mono">MEDIUM</SelectItem>
                    <SelectItem value="LOW" className="font-mono">LOW</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
          )}

          {step === 5 && (
            <div className="space-y-3">
              <ScrollArea className="h-[260px] rounded-md border border-primary/20 p-3">
                <div className="space-y-2 font-mono text-xs">
                  <SummaryLine label="Node IP" value={ip} />
                  <SummaryLine label="Role" value={role} />
                  <SummaryLine label="Network" value={`${networkAddress} via ${networkGateway}`} />
                  <SummaryLine label="DNS" value={dns.join(', ')} />
                  <SummaryLine label="Disk" value={disk} />
                  <SummaryLine label="RDMA" value={rdmaInterface || 'none'} />
                  <SummaryLine label="Hostname" value={hostnameOverride || discoverData?.hostname || 'auto'} />
                  <SummaryLine label="Rack" value={rack} />
                  <SummaryLine label="Service Class" value={serviceClass} />
                  <SummaryLine label="FrameNode CR" value={provisionNodeId ?? '—'} />
                </div>
              </ScrollArea>

              <Button className="w-full font-mono" onClick={applyConfiguration} disabled={applying}>
                {applying ? 'Applying Configuration...' : 'Apply Configuration'}
              </Button>

              {provisionNodeId && (
                <div className="rounded-md border border-accent/40 bg-accent/10 p-2 font-mono text-xs text-accent">
                  FrameNode {provisionNodeId} — status: {provisionStatus ?? 'provisioning'}
                </div>
              )}

              {provisionError && (
                <div className="rounded-md border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">
                  {provisionError}
                </div>
              )}

              {provisionLogs.length > 0 && (
                <ScrollArea className="h-[120px] rounded-md border border-primary/20 p-2">
                  <div className="space-y-1 font-mono text-xs">
                    {provisionLogs.map((line, index) => (
                      <div key={`${line}-${index}`}>{line}</div>
                    ))}
                  </div>
                </ScrollArea>
              )}
            </div>
          )}

          <Separator />
          <div className="flex items-center justify-between">
            <Button variant="outline" className="font-mono" disabled={step === 0 || applying} onClick={() => setStep((current) => Math.max(0, current - 1))}>
              Back
            </Button>
            <div className="flex gap-2">
              <Button variant="outline" className="font-mono" onClick={() => onOpenChange(false)} disabled={applying}>
                Close
              </Button>
              <Button className="font-mono" disabled={step >= STEPS.length - 1 || !canContinue() || applying} onClick={() => setStep((current) => Math.min(STEPS.length - 1, current + 1))}>
                Next
              </Button>
            </div>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function Field({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return (
    <div className="space-y-2">
      <Label className="font-mono text-xs">{label}</Label>
      <Input className="font-mono" value={value} onChange={(e) => onChange(e.target.value)} />
    </div>
  )
}

function SummaryLine({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <span className="text-muted-foreground">{label}</span>
      <span>{value}</span>
    </div>
  )
}
