import { describe, expect, it } from 'vitest'

import {
  changed, EMPTY_SETTINGS, nameProblem, settingsProblem, toCreateSpec, toInferenceModel, toSpec, type ModelSettings,
} from './inference-models'

const base: ModelSettings = { ...EMPTY_SETTINGS, weightsMiB: '4696', layers: '32', kvHeads: '8', headDim: '128' }

describe('toInferenceModel', () => {
  it('reads numbers as the form strings, an absent option as empty', () => {
    const m = toInferenceModel({
      metadata: { name: 'm' },
      spec: { weightsMiB: 1, layers: 2, kvHeads: 3, headDim: 4, reasoningBudget: 0 },
    })
    expect(m).toMatchObject({ name: 'm', weightsMiB: '1', reasoningBudget: '0', parallel: '' })
  })
})

describe('settingsProblem', () => {
  it('passes a minimal model', () => {
    expect(settingsProblem(base)).toBeNull()
  })

  it('refuses a missing sizing figure', () => {
    expect(settingsProblem({ ...base, layers: '' })).toMatch(/layers/)
  })

  it('takes every budget llama.cpp does, and no other', () => {
    for (const b of ['-1', '0', '1024']) expect(settingsProblem({ ...base, reasoningBudget: b })).toBeNull()
    expect(settingsProblem({ ...base, reasoningBudget: '-2' })).toMatch(/reasoningBudget/)
  })

  it('refuses a message with no budget, as the CRD does', () => {
    expect(settingsProblem({ ...base, reasoningBudgetMessage: 'stop' })).toMatch(/needs reasoningBudget/)
  })

  it('refuses a KV type the CRD does not list', () => {
    expect(settingsProblem({ ...base, kvCacheType: 'q2_k' })).toMatch(/kvCacheType/)
  })
})

describe('toSpec', () => {
  it('removes a cleared option and keeps a budget of 0', () => {
    expect(toSpec({ ...base, reasoningBudget: '0' })).toMatchObject({ reasoningBudget: 0, parallel: null, kvCacheType: null })
  })

  it('leaves absent options out of a create', () => {
    expect(toCreateSpec(base)).toEqual({ weightsMiB: 4696, layers: 32, kvHeads: 8, headDim: 128 })
  })
})

describe('nameProblem', () => {
  it('follows the model names in use', () => {
    expect(nameProblem('llama-3.1-8b-instruct')).toBeNull()
    expect(nameProblem('Llama 3')).toMatch(/Name/)
  })
})

describe('changed', () => {
  it('sees an option moving', () => {
    const m = { ...base, name: 'm' }
    expect(changed(m, base)).toBe(false)
    expect(changed(m, { ...base, parallel: '2' })).toBe(true)
  })
})
