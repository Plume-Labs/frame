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

interface NodeProvisionWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  racks: string[]
  zones: string[]
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

export function NodeProvisionWizard({
  open,
  onOpenChange,
  racks,
  zones,
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

  const [rack, setRack] = useState(racks[0] ?? 'zone-a-rack-01')
  const [zone, setZone] = useState(zones[0] ?? 'zone-a')
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
    }
  }, [open])

  useEffect(() => {
    if (!provisionNodeId || !applying) return

    const interval = setInterval(async () => {
      try {
        const response = await fetch(`/api/nodes/${provisionNodeId}/provision-status`)
        if (!response.ok) return
        const payload = await response.json() as { status: 'provisioning' | 'online' | 'offline'; lastLogLines: string[] }
        setProvisionStatus(payload.status)
        setProvisionLogs(payload.lastLogLines)
        if (payload.status !== 'provisioning') {
          setApplying(false)
          clearInterval(interval)
        }
      } catch {
        // keep polling quietly during provisioning
      }
    }, 1200)

    return () => clearInterval(interval)
  }, [applying, provisionNodeId])

  async function runDiscovery() {
    setDiscovering(true)
    setDiscoverError(null)

    try {
      const response = await fetch('/api/nodes/discover', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ip }),
      })
      const payload = await response.json()
      if (!response.ok) {
        setDiscoverError(payload.error ?? 'Discovery failed')
        return
      }

      const discovered = payload as DiscoverData
      setDiscoverData(discovered)
      setDisk(discovered.disks[0]?.name ?? '')
      setRdmaInterface(discovered.nics.find((nic) => nic.name.startsWith('ib'))?.name ?? '')
      setHostnameOverride(discovered.hostname)
      if (!networkAddress) setNetworkAddress(`${ip}/24`)
      if (!networkGateway) {
        const octets = ip.split('.')
        if (octets.length === 4) {
          setNetworkGateway(`${octets[0]}.${octets[1]}.${octets[2]}.1`)
        }
      }
      setStep(1)
    } catch {
      setDiscoverError('Node not reachable in maintenance mode')
    } finally {
      setDiscovering(false)
    }
  }

  async function applyConfiguration() {
    setApplying(true)
    setProvisionError(null)
    setProvisionLogs(['Applying Talos machineConfig...'])

    try {
      const response = await fetch('/api/nodes/provision', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ip,
          role,
          network: {
            address: networkAddress,
            gateway: networkGateway,
            dns,
            vlan: vlan ? Number(vlan) : undefined,
            bond: bond || undefined,
          },
          disk,
          rdmaInterface: rdmaInterface || undefined,
          hostname: hostnameOverride || undefined,
          rack,
          zone,
          serviceClass,
        }),
      })

      const payload = await response.json() as { nodeId?: string; error?: string }
      if (!response.ok || !payload.nodeId) {
        setApplying(false)
        setProvisionError(payload.error ?? 'Failed to apply configuration')
        return
      }

      setProvisionNodeId(payload.nodeId)
      setProvisionStatus('provisioning')
      setProvisionLogs((current) => [...current, `Node ${payload.nodeId} entered provisioning state`])

      onNodeProvisioned({
        id: payload.nodeId,
        name: hostnameOverride || discoverData?.hostname || payload.nodeId,
        status: 'provisioning',
        metrics: { cpu: 0, memory: 0, storage: 0, network: 0 },
        uptime: 0,
        lastSeen: Date.now(),
        network: {
          rxBytes: 0,
          txBytes: 0,
          latency: 0,
          rdmaActive: Boolean(rdmaInterface),
          rdmaQueuePairs: 0,
          bandwidth: 0,
          packetLoss: 0,
          sriovVFs: 0,
          dpdkEnabled: false,
          ciliumVersion: 'unknown',
          ebpfBypassActive: false,
        },
        storage: {
          cephOSDs: 0,
          cephPGs: 0,
          totalCapacity: 0,
          usedCapacity: 0,
          readIOPS: 0,
          writeIOPS: 0,
          replicationFactor: 3,
          dataFabricEnabled: false,
          metadataEntries: 0,
          activeDatasets: 0,
        },
        hardware: {
          cpuModel: 'pending',
          cpuCores: 0,
          memoryGB: 0,
          storageGB: 0,
          networkAdapters: discoverData?.nics.length ?? 1,
          pxeBooted: false,
          temperature: 0,
          deviceType: 'server',
          rackUnits: 1,
          numaNode: 0,
          cacheHitRate: 0,
          storageTier: 'nvme',
          gpuMIGInstances: 0,
          hugepagesGB: 0,
          cpuPinnedCores: 0,
          topologyManagerPolicy: 'none',
        },
        zone,
        rackId: rack,
        rackPosition: 1,
        serviceClass,
      })
    } catch {
      setApplying(false)
      setProvisionError('Failed to apply configuration')
    }
  }

  function canContinue(): boolean {
    if (step === 0) return Boolean(discoverData)
    if (step === 2) return Boolean(networkAddress && networkGateway && dns.length > 0)
    if (step === 3) return Boolean(disk)
    if (step === 4) return Boolean(rack && zone)
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

              {discoverError && <div className="font-mono text-xs text-warning">{discoverError}</div>}

              {discoverData && (
                <div className="space-y-3 rounded-md border border-primary/30 p-3">
                  <div className="font-mono text-xs">Hostname: <span className="text-primary">{discoverData.hostname}</span></div>
                  <div className="font-mono text-xs">Talos: {discoverData.talosVersion}</div>
                  <Separator />
                  <div className="grid grid-cols-2 gap-3 text-xs font-mono">
                    <div>
                      <div className="mb-1 text-muted-foreground">Disks</div>
                      {discoverData.disks.map((entry) => (
                        <div key={entry.name}>{entry.name} • {entry.size} • {entry.type}</div>
                      ))}
                    </div>
                    <div>
                      <div className="mb-1 text-muted-foreground">NICs</div>
                      {discoverData.nics.map((entry) => (
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
                  Warning: this would create a 2-control-plane cluster. Odd control-plane counts are recommended.
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
              </div>
              <Field label="RDMA / InfiniBand interface (optional)" value={rdmaInterface} onChange={setRdmaInterface} />
              <Field label="Hostname override (optional)" value={hostnameOverride} onChange={setHostnameOverride} />
            </div>
          )}

          {step === 4 && (
            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
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
                <Label className="font-mono text-xs">Zone</Label>
                <Select value={zone} onValueChange={setZone}>
                  <SelectTrigger className="w-full font-mono"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {zones.map((entry) => (
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
                  <SummaryLine label="Rack / Zone" value={`${rack} / ${zone}`} />
                  <SummaryLine label="Service Class" value={serviceClass} />
                </div>
              </ScrollArea>

              <Button className="w-full font-mono" onClick={applyConfiguration} disabled={applying}>
                {applying ? 'Applying Configuration...' : 'Apply Configuration'}
              </Button>

              {provisionNodeId && (
                <div className="rounded-md border border-accent/40 bg-accent/10 p-2 font-mono text-xs text-accent">
                  Node {provisionNodeId} status: {provisionStatus ?? 'provisioning'}
                </div>
              )}

              {provisionError && (
                <div className="rounded-md border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">
                  {provisionError}
                </div>
              )}

              {provisionLogs.length > 0 && (
                <ScrollArea className="h-[160px] rounded-md border border-primary/20 p-2">
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
