import { randomUUID } from 'crypto'
import { promises as fs } from 'fs'
import os from 'os'
import path from 'path'
import { execFile } from 'child_process'
import { promisify } from 'util'
import express, { NextFunction, Request, Response } from 'express'

const execFileAsync = promisify(execFile)

interface NodeRecord {
  id: string
  name: string
  status: 'online' | 'degraded' | 'offline' | 'provisioning'
  serviceClass: 'HIGH' | 'MEDIUM' | 'LOW'
  zone: string
  rackId: string
  cpu: number
  memory: number
  storage: number
  gpuCount: number
  gpuModel: string
}

interface DiscoverBody {
  ip?: string
}

interface ProvisionBody {
  ip?: string
  role?: 'controlplane' | 'worker'
  network?: {
    address?: string
    gateway?: string
    dns?: string[]
    vlan?: number
    bond?: string
  }
  disk?: string
  rdmaInterface?: string
  hostname?: string
  rack?: string
  zone?: string
  serviceClass?: 'HIGH' | 'MEDIUM' | 'LOW'
}

interface ProvisionPayload {
  ip: string
  role: 'controlplane' | 'worker'
  network: {
    address: string
    gateway: string
    dns: string[]
    vlan?: number
    bond?: string
  }
  disk: string
  rdmaInterface?: string
  hostname?: string
  rack: string
  zone: string
  serviceClass: 'HIGH' | 'MEDIUM' | 'LOW'
}

interface ProvisionStatus {
  status: NodeRecord['status']
  logs: string[]
}

interface DiscoverResponse {
  hostname: string
  disks: Array<{ name: string; size: string; type: string }>
  nics: Array<{ name: string; mac: string; speed: string }>
  talosVersion: string
}

const provisionStatuses = new Map<string, ProvisionStatus>()
const provisionRouter = express.Router()

const API_TOKEN = process.env.FRAME_API_TOKEN
const TALOS_MOCK = process.env.FRAME_TALOS_MOCK === 'true'

function requireAuth(req: Request, res: Response, next: NextFunction) {
  if (!API_TOKEN) { next(); return }
  const header = req.headers.authorization
  if (!header || !header.startsWith('Bearer ')) {
    res.status(401).json({ error: 'Unauthorized: Bearer token required' })
    return
  }
  const token = header.slice(7)
  if (token !== API_TOKEN) {
    res.status(403).json({ error: 'Forbidden: invalid token' })
    return
  }
  next()
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function hasTalosctl(): Promise<boolean> {
  if (TALOS_MOCK) return false
  try {
    await execFileAsync('talosctl', ['version'], { timeout: 1500 })
    return true
  } catch {
    return false
  }
}

function generateTalosMachineConfig(body: ProvisionPayload): string {
  const dnsServers = body.network.dns.map((dns) => `      - ${dns}`).join('\n')
  const vlanBlock = typeof body.network.vlan === 'number'
    ? `\n      vlans:\n        - vlanId: ${body.network.vlan}\n          addresses:\n            - ${body.network.address}\n          routes:\n            - network: 0.0.0.0/0\n              gateway: ${body.network.gateway}`
    : ''
  const bondBlock = body.network.bond
    ? `\n      bond:\n        interfaces:\n          - ${body.network.bond}`
    : ''
  const rdmaComment = body.rdmaInterface ? `\n    # RDMA interface selected: ${body.rdmaInterface}` : ''
  const hostnameLine = body.hostname ? `  hostname: ${body.hostname}` : ''

  return `version: v1alpha1
machine:
  type: ${body.role}
${hostnameLine}
  install:
    disk: ${body.disk}
  network:
    hostname: ${body.hostname ?? 'frame-node'}
    interfaces:
      - interface: eth0
        addresses:
          - ${body.network.address}
        routes:
          - network: 0.0.0.0/0
            gateway: ${body.network.gateway}
        mtu: 1500${vlanBlock}${bondBlock}
    nameservers:
      - ${body.network.gateway}
${dnsServers ? `${dnsServers}\n` : ''}cluster:
  allowSchedulingOnControlPlanes: true
  discovery:
    enabled: true
  network:
    dnsDomain: cluster.local
  etcd:
    advertisedSubnets:
      - ${body.network.address}
${rdmaComment}
`
}

function getNodesFromApp(req: Request): NodeRecord[] {
  const nodes = req.app.locals.nodes as NodeRecord[] | undefined
  if (!nodes) throw new Error('Node store is unavailable')
  return nodes
}

function mockDiscovery(ip: string): DiscoverResponse {
  const suffix = ip.split('.').at(-1) ?? '10'
  return {
    hostname: `talos-maint-${suffix}`,
    disks: [
      { name: '/dev/nvme0n1', size: '1.9TB', type: 'nvme' },
      { name: '/dev/sda', size: '480GB', type: 'ssd' },
    ],
    nics: [
      { name: 'eno1', mac: `52:54:00:00:10:${suffix.padStart(2, '0')}`, speed: '25G' },
      { name: 'ib0', mac: `52:54:00:00:20:${suffix.padStart(2, '0')}`, speed: '100G' },
    ],
    talosVersion: 'v1.9.0',
  }
}

provisionRouter.post('/discover', requireAuth, async (req: Request, res: Response) => {
  const body = req.body as DiscoverBody
  const ip = body?.ip?.trim()
  if (!ip) {
    res.status(400).json({ error: "'ip' is required" })
    return
  }

  const talosctlAvailable = await hasTalosctl()
  if (!talosctlAvailable) {
    await sleep(3000)
    res.json(mockDiscovery(ip))
    return
  }

  try {
    const { stdout } = await execFileAsync(
      'talosctl',
      ['get', 'members', '--insecure', '--nodes', ip, '-o', 'json'],
      { timeout: 5000 }
    )

    const parsed = JSON.parse(stdout) as { hostname?: string; spec?: { addresses?: string[] } }
    const discovered = mockDiscovery(ip)
    res.json({
      ...discovered,
      hostname: parsed.hostname ?? discovered.hostname,
    })
  } catch {
    res.status(504).json({ error: 'Node not reachable in maintenance mode' })
  }
})

provisionRouter.post('/provision', requireAuth, async (req: Request, res: Response) => {
  const body = req.body as ProvisionBody
  if (!body.ip || !body.role || !body.network?.address || !body.network.gateway || !Array.isArray(body.network.dns) || !body.disk || !body.rack || !body.zone || !body.serviceClass) {
    res.status(400).json({ error: 'Missing required provisioning fields' })
    return
  }

  const { ip, role, disk, rack, zone, serviceClass } = body
  const { address, gateway, dns, vlan, bond } = body.network
  const normalizedBody: ProvisionPayload = {
    ip,
    role,
    disk,
    rack,
    zone,
    serviceClass,
    rdmaInterface: body.rdmaInterface,
    hostname: body.hostname,
    network: {
      address,
      gateway,
      dns,
      vlan,
      bond,
    },
  }

  const nodeId = `node-${randomUUID().slice(0, 8)}`
  const nodeName = body.hostname || `frame-${body.role}-${nodeId.slice(-4)}`
  provisionStatuses.set(nodeId, {
    status: 'provisioning',
    logs: [`[${new Date().toISOString()}] Starting provisioning for ${body.ip}`],
  })

  const machineConfig = generateTalosMachineConfig(normalizedBody)
  const talosctlAvailable = await hasTalosctl()

  try {
    if (!talosctlAvailable) {
      provisionStatuses.get(nodeId)?.logs.push(`[${new Date().toISOString()}] talosctl unavailable or mock mode enabled, simulating apply-config`)
      await sleep(3000)
      provisionStatuses.get(nodeId)?.logs.push(`[${new Date().toISOString()}] Mock apply-config completed successfully`)
    } else {
      const tempFile = path.join(os.tmpdir(), `frame-${nodeId}.yaml`)
      await fs.writeFile(tempFile, machineConfig, 'utf8')
      provisionStatuses.get(nodeId)?.logs.push(`[${new Date().toISOString()}] Applying machineConfig to ${body.ip}`)
      await execFileAsync('talosctl', ['apply-config', '--insecure', '--nodes', body.ip, '--file', tempFile], { timeout: 10000 })
      provisionStatuses.get(nodeId)?.logs.push(`[${new Date().toISOString()}] talosctl apply-config completed`)
      await fs.unlink(tempFile).catch(() => undefined)
    }

    const nodes = getNodesFromApp(req)
    nodes.push({
      id: nodeId,
      name: nodeName,
      status: 'provisioning',
      serviceClass: body.serviceClass,
      zone: body.zone,
      rackId: body.rack,
      cpu: 0,
      memory: 0,
      storage: 0,
      gpuCount: 0,
      gpuModel: 'Unknown',
    })

    res.json({ nodeId, status: 'provisioning', message: 'Config applied' })
  } catch {
    provisionStatuses.set(nodeId, {
      status: 'offline',
      logs: [...(provisionStatuses.get(nodeId)?.logs ?? []), `[${new Date().toISOString()}] Provisioning failed`],
    })
    res.status(500).json({ error: 'Failed to apply configuration' })
  }
})

provisionRouter.get('/:id/provision-status', (req: Request, res: Response) => {
  const nodeId = String(req.params.id)
  const status = provisionStatuses.get(nodeId)

  if (!status) {
    res.status(404).json({ error: 'Provision status not found' })
    return
  }

  res.json({ nodeId, status: status.status, lastLogLines: status.logs.slice(-20) })
})

export default provisionRouter
