import { describe, it, expect } from 'vitest'
import { changed, changeWarnings, parametersPatch, settingsProblem, toDatabase, type FrameServiceCR } from './databases'

const cr = (over: Partial<FrameServiceCR> = {}): FrameServiceCR => ({
  metadata: { name: 'shop-db', namespace: 'shop' },
  spec: {
    type: 'postgres',
    parameters: { instances: '1', volumeSize: '5Gi', storageClass: 'fast', walBucketClaim: 'shop-wal', adoptUID: 'x' },
  },
  status: {
    conditions: [{ type: 'Ready', status: 'False', reason: 'SpecDrift', message: 'nothing written' }],
    binding: { endpoint: 'shop-db.shop.svc:5432' },
  },
  ...over,
})

describe('toDatabase', () => {
  it('montre le refus du fournisseur tel quel', () => {
    expect(toDatabase(cr())).toMatchObject({ ready: false, reason: 'SpecDrift', message: 'nothing written', adopted: true })
  })

  it('ne dit pas prêt sans condition', () => {
    expect(toDatabase(cr({ status: {} }))).toMatchObject({ ready: false, reason: 'NotReconciled' })
  })

  it('sépare ce qui bouge de ce qui est fixe', () => {
    const fixed = toDatabase(cr()).fixed.map((f) => f.value)
    expect(fixed).toContain('fast')
    expect(fixed).not.toContain('1')
  })
})

describe('settingsProblem', () => {
  const ok = { instances: '2', volumeSize: '20Gi', nodes: 'a,b' }
  it('accepte ce que le schéma accepte', () => {
    expect(settingsProblem(ok)).toBeNull()
    expect(settingsProblem({ ...ok, nodes: '' })).toBeNull()
  })
  it('refuse ce que le webhook refuserait', () => {
    expect(settingsProblem({ ...ok, instances: '0' })).not.toBeNull()
    expect(settingsProblem({ ...ok, volumeSize: '20G' })).not.toBeNull()
    expect(settingsProblem({ ...ok, nodes: 'a, b' })).not.toBeNull()
  })
})

describe('changeWarnings', () => {
  const db = toDatabase(cr())
  const same = { instances: db.instances, volumeSize: db.volumeSize, nodes: db.nodes }
  it('ne dit rien sans changement', () => {
    expect(changed(db, same)).toBe(false)
    expect(changeWarnings(db, same)).toEqual([])
  })
  it('compare les tailles par valeur, pas par texte', () => {
    expect(changeWarnings(db, { ...same, volumeSize: '4096Mi' })[0]).toContain('cannot shrink')
    expect(changeWarnings(db, { ...same, volumeSize: '1Ti' })[0]).toContain('cannot be undone')
  })
  it('annonce le redémarrage quand les nœuds changent', () => {
    expect(changeWarnings(db, { ...same, nodes: 'a,b' })).toEqual(['Changing the nodes restarts the pods.'])
  })
  it('dit ce que perd une baisse du nombre d’instances', () => {
    expect(changeWarnings({ ...db, instances: '2' }, same)[0]).toContain('redundancy')
  })
})

describe('parametersPatch', () => {
  it('retire nodes plutôt que de le vider', () => {
    expect(parametersPatch({ instances: '1', volumeSize: '5Gi', nodes: '' }).spec.parameters.nodes).toBeNull()
  })
})
