// The Inference → Models screen's reading of an InferenceModel. Pure.
//
// The form works in strings, the CR in numbers: toSpec is the one place that
// converts, and an empty field means the option is absent.

export interface InferenceModelCR {
  metadata: { name: string }
  spec: {
    weightsMiB: number
    layers: number
    kvHeads: number
    headDim: number
    hostMemoryMiB?: number
    contextLength?: number
    parallel?: number
    kvCacheType?: string
    cpuMoELayers?: number
    threads?: number
    reasoningBudget?: number
    reasoningBudgetMessage?: string
  }
}

export type ModelSettings = Record<keyof InferenceModelCR['spec'], string>

export interface InferenceModel extends ModelSettings {
  name: string
}

export const EMPTY_SETTINGS: ModelSettings = {
  weightsMiB: '', layers: '', kvHeads: '', headDim: '', hostMemoryMiB: '', contextLength: '', parallel: '',
  kvCacheType: '', cpuMoELayers: '', threads: '', reasoningBudget: '', reasoningBudgetMessage: '',
}

export const KV_CACHE_TYPES = ['f16', 'q8_0', 'q4_0']

const REQUIRED = ['weightsMiB', 'layers', 'kvHeads', 'headDim'] as const
const OPTIONAL = ['hostMemoryMiB', 'contextLength', 'parallel', 'cpuMoELayers', 'threads'] as const

export function toInferenceModel(cr: InferenceModelCR): InferenceModel {
  const out: InferenceModel = { ...EMPTY_SETTINGS, name: cr.metadata.name }
  for (const [k, v] of Object.entries(cr.spec)) out[k as keyof ModelSettings] = String(v)
  return out
}

/** The form's mistakes, before the apiserver's message. Its maximums stay the apiserver's. */
export function settingsProblem(s: ModelSettings): string | null {
  for (const key of REQUIRED) if (!/^[1-9][0-9]*$/.test(s[key])) return `${key}: a positive whole number.`
  for (const key of OPTIONAL) if (s[key] && !/^[1-9][0-9]*$/.test(s[key])) return `${key}: a positive whole number, or empty.`
  if (s.kvCacheType && !KV_CACHE_TYPES.includes(s.kvCacheType)) return `kvCacheType: one of ${KV_CACHE_TYPES.join(', ')}, or empty.`
  if (s.reasoningBudget && !/^(-1|0|[1-9][0-9]*)$/.test(s.reasoningBudget)) return 'reasoningBudget: -1, 0 or a number of tokens.'
  if (s.reasoningBudgetMessage && !s.reasoningBudget) return 'reasoningBudgetMessage needs reasoningBudget.'
  if (s.cpuMoELayers && !s.hostMemoryMiB) return 'cpuMoELayers needs hostMemoryMiB: the experts live there.'
  return null
}

export function nameProblem(name: string): string | null {
  return /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(name) ? null : 'Name: lowercase letters, digits, dots and dashes. The weights are <name>.gguf.'
}

/** A whole spec: sent as a merge patch, null is what removes an option. */
export function toSpec(s: ModelSettings): Record<string, number | string | null> {
  const num = (v: string) => (v === '' ? null : Number(v))
  return {
    weightsMiB: Number(s.weightsMiB),
    layers: Number(s.layers),
    kvHeads: Number(s.kvHeads),
    headDim: Number(s.headDim),
    hostMemoryMiB: num(s.hostMemoryMiB),
    contextLength: num(s.contextLength),
    parallel: num(s.parallel),
    kvCacheType: s.kvCacheType || null,
    cpuMoELayers: num(s.cpuMoELayers),
    threads: num(s.threads),
    reasoningBudget: num(s.reasoningBudget),
    reasoningBudgetMessage: s.reasoningBudgetMessage || null,
  }
}

/** For a create: an absent option is left out, not null. */
export function toCreateSpec(s: ModelSettings): Record<string, number | string> {
  return Object.fromEntries(Object.entries(toSpec(s)).filter(([, v]) => v !== null)) as Record<string, number | string>
}

export function changed(m: InferenceModel, next: ModelSettings): boolean {
  return (Object.keys(EMPTY_SETTINGS) as (keyof ModelSettings)[]).some((k) => m[k] !== next[k])
}
