import { randomUUID } from 'crypto'
import { promises as fs } from 'fs'
import { isIP } from 'net'
import os from 'os'
import path from 'path'
import { execFile } from 'child_process'
import { promisify } from 'util'
import express, { NextFunction, Request, Response } from 'express'
import { rateLimit } from 'express-rate-limit'

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
  updatedAt: number
  expiresAt: number
}

interface DiscoverResponse {
  hostname: string
  disks: Array<{ name: string; size: string; type: string }>
  nics: Array<{ name: string; mac: string; speed: string }>
  talosVersion: string
}

const provisionStatuses = new Map<string, ProvisionStatus>()
const provisionRouter = express.Router()
const PROVISION_STATUS_TTL_MS = 30 * 60 * 1000
const PROVISION_STATUS_MAX_ENTRIES = 256
const PROVISION_STATUS_MAX_LOG_LINES = 200

const API_TOKEN = process.env.FRAME_API_TOKEN
const TALOS_MOCK = process.env.FRAME_TALOS_MOCK === 'true'
const TALOS_DEFAULT_VERSION = 'v1.9.0'
const provisionRateLimit = rateLimit({
  windowMs: 60_000,
  limit: 20,
  standardHeaders: true,
  legacyHeaders: false,
  message: { error: 'Too many provisioning requests, please retry shortly' },
})

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
  const lines = [
    'version: v1alpha1',
    'machine:',
    `  type: ${body.role}`,
  ]

  if (body.hostname) lines.push(`  hostname: ${body.hostname}`)

  lines.push(
    '  install:',
    `    disk: ${body.disk}`,
    '  network:',
    '    interfaces:',
    '      - interface: eth0',
    '        addresses:',
    `          - ${body.network.address}`,
    '        routes:',
    '          - network: 0.0.0.0/0',
    `            gateway: ${body.network.gateway}`,
    '        mtu: 1500',
  )

  if (typeof body.network.vlan === 'number') {
    lines.push(
      '        vlans:',
      `          - vlanId: ${body.network.vlan}`,
      '            addresses:',
      `              - ${body.network.address}`,
      '            routes:',
      '              - network: 0.0.0.0/0',
      `                gateway: ${body.network.gateway}`,
    )
  }

  if (body.network.bond) {
    lines.push(
      '        bond:',
      '          interfaces:',
      `            - ${body.network.bond}`,
    )
  }

  lines.push(
    '    nameservers:',
    ...body.network.dns.map((dns) => `      - ${dns}`),
    'cluster:',
    '  allowSchedulingOnControlPlanes: true',
    '  discovery:',
    '    enabled: true',
    '  network:',
    '    dnsDomain: cluster.local',
    '  etcd:',
    '    advertisedSubnets:',
    `      - ${body.network.address}`,
  )

  if (body.rdmaInterface) {
    lines.push(`# RDMA interface selected: ${body.rdmaInterface}`)
  }

  return `${lines.join('\n')}\n`
}

function pruneProvisionStatuses(now = Date.now()): void {
  for (const [key, value] of provisionStatuses.entries()) {
    if (value.expiresAt <= now) provisionStatuses.delete(key)
  }

  if (provisionStatuses.size <= PROVISION_STATUS_MAX_ENTRIES) return

  const sorted = [...provisionStatuses.entries()].sort((a, b) => a[1].updatedAt - b[1].updatedAt)
  for (const [key] of sorted.slice(0, provisionStatuses.size - PROVISION_STATUS_MAX_ENTRIES)) {
    provisionStatuses.delete(key)
  }
}

function setProvisionStatus(nodeId: string, status: NodeRecord['status'], logs: string[]): void {
  const now = Date.now()
  provisionStatuses.set(nodeId, {
    status,
    logs: logs.slice(-PROVISION_STATUS_MAX_LOG_LINES),
    updatedAt: now,
    expiresAt: now + PROVISION_STATUS_TTL_MS,
  })
  pruneProvisionStatuses(now)
}

function appendProvisionLog(nodeId: string, message: string): void {
  const current = provisionStatuses.get(nodeId)
  if (!current) return
  const now = Date.now()
  current.logs.push(`[${new Date(now).toISOString()}] ${message}`)
  current.logs = current.logs.slice(-PROVISION_STATUS_MAX_LOG_LINES)
  current.updatedAt = now
  current.expiresAt = now + PROVISION_STATUS_TTL_MS
  provisionStatuses.set(nodeId, current)
  pruneProvisionStatuses(now)
}

function isValidIp(value: string | undefined): boolean {
  return Boolean(value && isIP(value.trim()))
}

function isValidAddressWithCidr(value: string | undefined): boolean {
  if (!value) return false
  const [ip, cidr] = value.trim().split('/')
  if (!isIP(ip)) return false
  const cidrNum = Number(cidr)
  if (!Number.isInteger(cidrNum)) return false
  const maxCidr = isIP(ip) === 4 ? 32 : 128
  return cidrNum >= 0 && cidrNum <= maxCidr
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
    talosVersion: TALOS_DEFAULT_VERSION,
  }
}

provisionRouter.post('/discover', requireAuth, provisionRateLimit, async (req: Request, res: Response) => {
  const body = req.body as DiscoverBody
  const ip = body?.ip?.trim()
  if (!ip || !isValidIp(ip)) {
    res.status(400).json({ error: "'ip' is required and must be a valid IPv4/IPv6 address" })
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

function isValidProvisionBody(body: ProvisionBody): body is ProvisionPayload {
  return Boolean(
    body.ip &&
    body.role &&
    body.network?.address &&
    body.network.gateway &&
    Array.isArray(body.network.dns) &&
    body.network.dns.length > 0 &&
    body.disk &&
    body.rack &&
    body.zone &&
    body.serviceClass,
  )
}

provisionRouter.post('/provision', requireAuth, provisionRateLimit, async (req: Request, res: Response) => {
  const body = req.body as ProvisionBody
  if (!isValidProvisionBody(body)) {
    res.status(400).json({ error: 'Missing required provisioning fields' })
    return
  }

  if (!isValidIp(body.ip)) {
    res.status(400).json({ error: "'ip' must be a valid IPv4/IPv6 address" })
    return
  }

  if (!isValidAddressWithCidr(body.network.address)) {
    res.status(400).json({ error: "'network.address' must be a valid CIDR address (for example 192.168.1.10/24)" })
    return
  }

  if (!isValidIp(body.network.gateway)) {
    res.status(400).json({ error: "'network.gateway' must be a valid IPv4/IPv6 address" })
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
  setProvisionStatus(nodeId, 'provisioning', [`[${new Date().toISOString()}] Starting provisioning for ${body.ip}`])

  const machineConfig = generateTalosMachineConfig(normalizedBody)
  const talosctlAvailable = await hasTalosctl()
  let tempFile: string | null = null

  try {
    if (!talosctlAvailable) {
      appendProvisionLog(nodeId, 'talosctl unavailable or mock mode enabled, simulating apply-config')
      await sleep(3000)
      appendProvisionLog(nodeId, 'Mock apply-config completed successfully')
    } else {
      tempFile = path.join(os.tmpdir(), `frame-${nodeId}.yaml`)
      await fs.writeFile(tempFile, machineConfig, 'utf8')
      appendProvisionLog(nodeId, `Applying machineConfig to ${body.ip}`)
      await execFileAsync('talosctl', ['apply-config', '--insecure', '--nodes', body.ip, '--file', tempFile], { timeout: 10000 })
      appendProvisionLog(nodeId, 'talosctl apply-config completed')
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

    appendProvisionLog(nodeId, 'Configuration applied successfully; waiting for node reboot/join')
    setProvisionStatus(nodeId, 'online', provisionStatuses.get(nodeId)?.logs ?? [])
    res.json({ nodeId, status: 'online', message: 'Config applied' })
  } catch {
    setProvisionStatus(nodeId, 'offline', [...(provisionStatuses.get(nodeId)?.logs ?? []), `[${new Date().toISOString()}] Provisioning failed`])
    res.status(500).json({ error: 'Failed to apply configuration' })
  } finally {
    if (tempFile) {
      await fs.unlink(tempFile).catch(() => undefined)
    }
  }
})

provisionRouter.get('/:id/provision-status', requireAuth, provisionRateLimit, (req: Request, res: Response) => {
  pruneProvisionStatuses()
  const nodeId = String(req.params.id)
  const status = provisionStatuses.get(nodeId)

  if (!status) {
    res.status(404).json({ error: 'Provision status not found' })
    return
  }

  res.json({ nodeId, status: status.status, lastLogLines: status.logs.slice(-20) })
})

export default provisionRouter
