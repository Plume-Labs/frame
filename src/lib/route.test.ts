import { describe, expect, it } from 'vitest'
import { formatRoute, parseRoute } from './route'

const known = new Set(['overview', 'nodes', 'accounts'])
const isScreen = (id: string) => known.has(id)

describe('route', () => {
  it('round-trips a screen and a tab', () => {
    expect(parseRoute(formatRoute('nodes'), isScreen)).toEqual({ screen: 'nodes' })
    expect(parseRoute(formatRoute('nodes', 'fleet'), isScreen)).toEqual({ screen: 'nodes', tab: 'fleet' })
  })

  it('ignores a hash that is not a route, including the invitation fragment', () => {
    expect(parseRoute('', isScreen)).toBeUndefined()
    expect(parseRoute('#token=abc', isScreen)).toBeUndefined()
    expect(parseRoute('#nodes', isScreen)).toBeUndefined()
  })

  it('refuses an unknown screen and anything outside the id alphabet', () => {
    expect(parseRoute('#/nope', isScreen)).toBeUndefined()
    expect(parseRoute('#/nodes/../x', isScreen)).toBeUndefined()
    expect(parseRoute('#/nodes/a b', isScreen)).toBeUndefined()
    expect(parseRoute('#/nodes/fleet/extra', isScreen)).toBeUndefined()
  })
})
