/**
 * Runtime configuration for every cluster component the UI reaches into.
 *
 * Frame talks to a dozen third-party tools (Ceph, Prometheus, DCGM, Falco…) by
 * finding their pods and proxying to a port. Where those pods live, how they
 * are labelled, and which port they answer on differ per cluster, and used to
 * be compiled in — so running Frame anywhere but the cluster it was built for
 * meant editing the SDK and rebuilding the image.
 *
 * This moves all of it into a ConfigMap the UI reads at boot and writes from
 * the Settings screen. Compiled values remain as defaults, so a fresh install
 * works with no ConfigMap at all, and a stored config that predates a new
 * integration still boots — the stored object is merged *over* the defaults
 * rather than replacing them.
 */

/** One component addressed by locating its pod and proxying to a port. */
export interface Integration {
  namespace: string
  /** Label selector in plain form (`app=foo`), URL-encoded at the call site. */
  selector: string
  /** Port to proxy to. Omitted for components read via their own API. */
  port?: number
}

export interface FrameConfig {
  /** Namespace holding the Frame CRs (FrameJob, FrameNode, SchedulingPolicy…). */
  frameNamespace: string
  integrations: {
    cephOsd: Integration
    cephMon: Integration
    alluxio: Integration
    nodeExporter: Integration
    dcgm: Integration
    llamacpp: Integration
    tei: Integration
    falcosidekick: Integration
    tetragon: Integration
    prometheus: Integration
    alertmanager: Integration
    /** Cluster-wide (namespace ignored) — sandbox pods run wherever Neura puts them. */
    neuraSandbox: Integration
  }
  /** Components addressed through their own API group, so only a namespace applies. */
  namespaces: {
    ceph: string
    velero: string
    argo: string
    /** Where Volcano queue open/close Commands are created. */
    volcanoCommands: string
  }
  network: {
    /** Interfaces charted on the Network screen; anything else is ignored. */
    devices: string[]
  }
  burstBuffer: {
    /** Filesystem mountpoint reported as the scratch tier. */
    mount: string
  }
}

export const DEFAULT_CONFIG: FrameConfig = {
  frameNamespace: 'default',
  integrations: {
    cephOsd: { namespace: 'rook-ceph', selector: 'app=rook-ceph-osd' },
    cephMon: { namespace: 'rook-ceph', selector: 'app=rook-ceph-mon' },
    alluxio: { namespace: 'alluxio', selector: 'app=alluxio', port: 19999 },
    nodeExporter: { namespace: 'monitoring', selector: 'app=node-exporter', port: 9100 },
    dcgm: { namespace: 'gpu-operator', selector: 'app=nvidia-dcgm-exporter', port: 9400 },
    llamacpp: { namespace: 'inference', selector: 'app=llamacpp', port: 8080 },
    tei: { namespace: 'inference', selector: 'app=tei', port: 80 },
    falcosidekick: {
      namespace: 'falco',
      selector: 'app.kubernetes.io/name=falcosidekick',
      port: 2801,
    },
    tetragon: { namespace: 'tetragon', selector: 'app.kubernetes.io/name=tetragon', port: 2112 },
    prometheus: { namespace: 'monitoring', selector: 'app.kubernetes.io/name=prometheus', port: 9090 },
    alertmanager: {
      namespace: 'monitoring',
      selector: 'app.kubernetes.io/name=alertmanager',
      port: 9093,
    },
    neuraSandbox: { namespace: '', selector: 'app=neura-sandbox' },
  },
  namespaces: {
    ceph: 'rook-ceph',
    velero: 'velero',
    argo: 'argo',
    volcanoCommands: 'default',
  },
  network: { devices: ['eth0', 'flannel.1', 'cni0'] },
  burstBuffer: { mount: '/burst-buffer' },
}

/** Where the stored config lives. Shipped by the kustomize base, so it always exists. */
export const CONFIG_NAMESPACE = 'cluster-control'
export const CONFIG_NAME = 'cluster-control-config'
const CONFIG_KEY = 'config.json'
const CONFIG_URL = `/api/v1/namespaces/${CONFIG_NAMESPACE}/configmaps/${CONFIG_NAME}`

/** How the active config was obtained — surfaced on the Settings screen. */
export type ConfigSource = 'configmap' | 'defaults'

let active: FrameConfig = DEFAULT_CONFIG
let source: ConfigSource = 'defaults'
let loadError: string | undefined

/** The config in force. Safe to call before `loadConfig()` — returns the defaults. */
export function config(): FrameConfig {
  return active
}

export function configStatus(): { source: ConfigSource; error?: string } {
  return { source, error: loadError }
}

/**
 * Merge a stored (possibly partial, possibly stale) config over the defaults.
 *
 * Only keys the defaults define are taken, so a ConfigMap someone hand-edited
 * cannot inject unknown fields, and an integration added in a later release
 * still gets its default rather than coming back undefined.
 */
function merge(stored: unknown): FrameConfig {
  const s = (stored ?? {}) as Partial<FrameConfig>
  const integrations = { ...DEFAULT_CONFIG.integrations }
  for (const key of Object.keys(DEFAULT_CONFIG.integrations) as Array<
    keyof FrameConfig['integrations']
  >) {
    const from = s.integrations?.[key]
    if (from) integrations[key] = { ...DEFAULT_CONFIG.integrations[key], ...from }
  }
  return {
    frameNamespace: s.frameNamespace || DEFAULT_CONFIG.frameNamespace,
    integrations,
    namespaces: { ...DEFAULT_CONFIG.namespaces, ...(s.namespaces ?? {}) },
    network: {
      devices: s.network?.devices?.length ? s.network.devices : DEFAULT_CONFIG.network.devices,
    },
    burstBuffer: { mount: s.burstBuffer?.mount || DEFAULT_CONFIG.burstBuffer.mount },
  }
}

/**
 * Read the ConfigMap and make it the active config.
 *
 * Deliberately fail-open: a missing ConfigMap, an RBAC denial, or malformed
 * JSON leaves the compiled defaults in force and records why, rather than
 * blocking boot. The UI is read-mostly, and a config problem should degrade
 * to "wrong namespaces" — visible and fixable on the Settings screen — not to
 * a blank page.
 */
export async function loadConfig(): Promise<void> {
  active = DEFAULT_CONFIG
  source = 'defaults'
  loadError = undefined
  try {
    const res = await fetch(CONFIG_URL, { headers: authHeaders() })
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
    const cm = (await res.json()) as { data?: Record<string, string> }
    const raw = cm.data?.[CONFIG_KEY]?.trim()
    // No key yet is the state of a fresh install, not a failure: the ConfigMap
    // ships empty and only gains content once someone saves. Reporting it as
    // an error would put a warning on every new deployment.
    if (!raw) return
    active = merge(JSON.parse(raw))
    source = 'configmap'
  } catch (e) {
    loadError = e instanceof Error ? e.message : String(e)
  }
}

/**
 * Write the config back and apply it in this tab.
 *
 * A merge-patch on `data` rather than a full replace, so unrelated keys anyone
 * else stores in the same ConfigMap survive. Other tabs and the other UI pods
 * pick the change up on their next load — this is cluster configuration, not
 * a live-broadcast setting.
 */
export async function saveConfig(next: FrameConfig): Promise<void> {
  const res = await fetch(CONFIG_URL, {
    method: 'PATCH',
    headers: { ...authHeaders(), 'Content-Type': 'application/merge-patch+json' },
    body: JSON.stringify({ data: { [CONFIG_KEY]: JSON.stringify(next, null, 2) } }),
  })
  if (!res.ok) {
    const body = await res.text()
    throw new Error(`cannot save config: ${res.status} ${body}`)
  }
  active = merge(next)
  source = 'configmap'
  loadError = undefined
}

function authHeaders(): Record<string, string> {
  const token = (window as unknown as Record<string, string>).__FRAME_TOKEN__
  return token ? { Authorization: `Bearer ${token}` } : {}
}
