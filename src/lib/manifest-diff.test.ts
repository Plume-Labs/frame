import { describe, it, expect } from 'vitest'
import { MAX_ACTION_LENGTH, changedFieldPaths, editActionLabel } from './manifest-diff'

const base = {
  metadata: { name: 'api', namespace: 'neura', resourceVersion: '1', generation: 3 },
  spec: {
    replicas: 2,
    template: { spec: { containers: [{ name: 'api', image: 'neura/api:1.0' }] } },
  },
  status: { readyReplicas: 2 },
}

describe('changedFieldPaths', () => {
  it('reports nothing for an identical object', () => {
    expect(changedFieldPaths(base, structuredClone(base))).toEqual([])
  })

  it('names the leaf that changed', () => {
    const after = structuredClone(base)
    after.spec.replicas = 5
    expect(changedFieldPaths(base, after)).toEqual(['spec.replicas'])
  })

  it('indexes into arrays', () => {
    const after = structuredClone(base)
    after.spec.template.spec.containers[0].image = 'neura/api:1.1'
    expect(changedFieldPaths(base, after)).toEqual([
      'spec.template.spec.containers[0].image',
    ])
  })

  // The server writes these on every request, so a dry-run response differs
  // from the object that was read in all of them. Without the ignore list
  // every single edit's label leads with `metadata.generation,
  // metadata.managedFields, metadata.resourceVersion` — three paths, which is
  // the whole budget — and the change the person actually made is pushed into
  // the "+N more" counter. The label would be technically true and useless.
  it('ignores the fields the server owns', () => {
    const after = structuredClone(base) as Record<string, any>
    after.metadata.resourceVersion = '2'
    after.metadata.generation = 4
    after.metadata.managedFields = [{ manager: 'frame-uiproxy' }]
    after.status.readyReplicas = 0
    expect(changedFieldPaths(base, after)).toEqual([])
  })

  // An added subtree is one decision, not twelve. Descending into it would
  // spend the three-path budget listing the leaves of a block the person
  // pasted in as a unit.
  it('names an added subtree once, not every leaf inside it', () => {
    const after = structuredClone(base) as Record<string, any>
    after.spec.template.spec.tolerations = [
      { key: 'nvidia.com/gpu', operator: 'Exists', effect: 'NoSchedule' },
    ]
    expect(changedFieldPaths(base, after)).toEqual(['spec.template.spec.tolerations'])
  })

  it('names a removed field', () => {
    const after = structuredClone(base) as Record<string, any>
    delete after.spec.replicas
    expect(changedFieldPaths(base, after)).toEqual(['spec.replicas'])
  })

  it('sorts, so the same edit always produces the same label', () => {
    const after = structuredClone(base) as Record<string, any>
    after.spec.replicas = 9
    after.metadata.labels = { tier: 'api' }
    expect(changedFieldPaths(base, after)).toEqual(['metadata.labels', 'spec.replicas'])
  })
})

describe('editActionLabel', () => {
  it('names the object and the fields', () => {
    expect(editActionLabel('Deployment', 'neura', 'api', ['spec.replicas'])).toBe(
      'edit deployment neura/api: spec.replicas',
    )
  })

  it('counts the remainder past three paths', () => {
    expect(editActionLabel('Deployment', 'neura', 'api', ['a', 'b', 'c', 'd', 'e'])).toBe(
      'edit deployment neura/api: a, b, c +2 more',
    )
  })

  // The cap the CRD enforces. Over 200 characters the apiserver refuses the
  // FrameTask create outright; the recorder logs it and the edit goes through
  // anyway, so the whole edit is missing from the Tasks screen with nothing
  // anywhere saying why. A `paths.join(', ')` implementation produces ~600
  // characters here and passes every other test in this file.
  it('never exceeds the 200 characters the CRD allows', () => {
    const paths = Array.from({ length: 40 }, (_, i) => `spec.template.spec.containers[${i}].image`)
    const label = editActionLabel('Deployment', 'a-very-long-namespace-name', 'a-very-long-name', paths)
    expect(label.length).toBeLessThanOrEqual(MAX_ACTION_LENGTH)
  })

  // A truncation that lands on a single very long path must still fit, and
  // must still be a sentence rather than an empty string.
  it('truncates a single enormous path rather than dropping the label', () => {
    const label = editActionLabel('Pod', 'neura', 'api-0', ['x'.repeat(400)])
    expect(label.length).toBeLessThanOrEqual(MAX_ACTION_LENGTH)
    expect(label.startsWith('edit pod neura/api-0:')).toBe(true)
  })

  it('says so when nothing changed', () => {
    expect(editActionLabel('Pod', 'neura', 'api-0', [])).toBe('edit pod neura/api-0: no field changed')
  })
})
