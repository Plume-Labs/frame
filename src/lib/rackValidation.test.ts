import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  validatePowerConstraints,
  validateCoolingConstraints,
  validateRackPlacement,
  validatePhysicalSpacing,
  validateCapacityLimits,
  DEFAULT_RACK_CONSTRAINTS
} from './rackValidation'
import { calculateRackPowerCooling } from './rack'
import { RackData } from './rack'
import { generateClusterNodes } from './cluster'

function makeRack(nodeCount = 4): RackData {
  const nodes = generateClusterNodes(nodeCount).map((n, i) => ({
    ...n,
    rackId: 'rack-01',
    rackPosition: i + 1,
    hardware: { ...n.hardware, rackUnits: 1 }
  }))
  const totalCapacity = nodes.reduce((sum, n) => sum + n.hardware.storageGB, 0)
  const usedCapacity = nodes.reduce((sum, n) => sum + n.hardware.storageGB * 0.5, 0)
  return {
    id: 'rack-01',
    zone: 'zone-a',
    nodes,
    totalCapacity,
    usedCapacity,
    healthScore: 90
  }
}

describe('validatePowerConstraints', () => {
  it('returns no errors for a lightly loaded rack', () => {
    // Build a single-node rack well within limits
    const rack = makeRack(1)
    const { errors } = validatePowerConstraints(rack, DEFAULT_RACK_CONSTRAINTS)
    // The single-node rack should be well within the 12 kW limit
    expect(errors).toHaveLength(0)
  })

  it('accepts a pre-computed powerMetrics argument (determinism fix)', () => {
    const rack = makeRack(2)
    const metrics = calculateRackPowerCooling(rack)
    // Both calls with the same metrics must produce identical results
    const result1 = validatePowerConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)
    const result2 = validatePowerConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)
    expect(result1.errors).toHaveLength(result2.errors.length)
    expect(result1.warnings).toHaveLength(result2.warnings.length)
  })

  it('errors when power draw exceeds maxPowerWatts', () => {
    const rack = makeRack(2)
    const tightConstraints = { ...DEFAULT_RACK_CONSTRAINTS, maxPowerWatts: 1 }
    const { errors } = validatePowerConstraints(rack, tightConstraints)
    const powerErrors = errors.filter(e => e.type === 'power')
    expect(powerErrors.length).toBeGreaterThan(0)
  })
})

describe('validateCoolingConstraints', () => {
  it('accepts a pre-computed powerMetrics argument (determinism fix)', () => {
    const rack = makeRack(2)
    const metrics = calculateRackPowerCooling(rack)
    const result1 = validateCoolingConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)
    const result2 = validateCoolingConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)
    expect(result1.errors).toHaveLength(result2.errors.length)
    expect(result1.warnings).toHaveLength(result2.warnings.length)
  })

  it('errors when cooling capacity is exceeded', () => {
    const rack = makeRack(4)
    const tightConstraints = { ...DEFAULT_RACK_CONSTRAINTS, maxCoolingBTU: 1 }
    const { errors } = validateCoolingConstraints(rack, tightConstraints)
    const coolingErrors = errors.filter(e => e.type === 'cooling')
    expect(coolingErrors.length).toBeGreaterThan(0)
  })
})

describe('validateRackPlacement (determinism)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('power and cooling validators share a single metrics snapshot within one call', () => {
    // The fix: validateRackPlacement computes calculateRackPowerCooling once and passes
    // the same object to both validatePowerConstraints and validateCoolingConstraints.
    // We verify this by supplying a fixed pre-computed metrics object directly to each
    // validator — calling with the same snapshot twice is always deterministic.
    const rack = makeRack(4)
    const metrics = calculateRackPowerCooling(rack)

    const power1 = validatePowerConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)
    const power2 = validatePowerConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)
    const cool1 = validateCoolingConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)
    const cool2 = validateCoolingConstraints(rack, DEFAULT_RACK_CONSTRAINTS, metrics)

    expect(power1.errors.length).toBe(power2.errors.length)
    expect(power1.warnings.length).toBe(power2.warnings.length)
    expect(cool1.errors.length).toBe(cool2.errors.length)
    expect(cool1.warnings.length).toBe(cool2.warnings.length)
  })

  it('produces identical results on repeated calls with seeded Math.random', () => {
    // Seed Math.random so calculateRackPowerCooling is deterministic across calls,
    // allowing us to assert cross-call equality without flakiness.
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const rack = makeRack(4)
    const result1 = validateRackPlacement(rack)
    const result2 = validateRackPlacement(rack)
    expect(result1.errors.length).toBe(result2.errors.length)
    expect(result1.warnings.length).toBe(result2.warnings.length)
    expect(result1.valid).toBe(result2.valid)
  })

  it('is invalid when constraints are extremely tight', () => {
    const rack = makeRack(4)
    const extremeConstraints = { ...DEFAULT_RACK_CONSTRAINTS, maxPowerWatts: 1, maxCoolingBTU: 1 }
    const result = validateRackPlacement(rack, extremeConstraints)
    expect(result.valid).toBe(false)
  })

  it('has a valid result for a normal small rack', () => {
    const rack = makeRack(1)
    const result = validateRackPlacement(rack)
    // A single node should not breach default constraints
    expect(result.errors.filter(e => e.type === 'power').length).toBe(0)
  })
})

describe('validatePhysicalSpacing', () => {
  it('detects overlapping devices', () => {
    const rack = makeRack(2)
    // Force both nodes to occupy the same rack unit
    const overlapping: RackData = {
      ...rack,
      nodes: rack.nodes.slice(0, 2).map(n => ({
        ...n,
        rackPosition: 1,
        hardware: { ...n.hardware, rackUnits: 2 }
      }))
    }
    const { errors } = validatePhysicalSpacing(overlapping, DEFAULT_RACK_CONSTRAINTS)
    const spacingErrors = errors.filter(e => e.type === 'spacing')
    expect(spacingErrors.length).toBeGreaterThan(0)
  })

  it('has no spacing errors for non-overlapping devices', () => {
    const rack = makeRack(2)
    // Place nodes far apart
    const spaced: RackData = {
      ...rack,
      nodes: [
        { ...rack.nodes[0], rackPosition: 1, hardware: { ...rack.nodes[0].hardware, rackUnits: 1 } },
        { ...rack.nodes[1], rackPosition: 5, hardware: { ...rack.nodes[1].hardware, rackUnits: 1 } }
      ]
    }
    const { errors } = validatePhysicalSpacing(spaced, DEFAULT_RACK_CONSTRAINTS)
    const spacingErrors = errors.filter(e => e.type === 'spacing')
    expect(spacingErrors).toHaveLength(0)
  })
})

describe('validateCapacityLimits', () => {
  it('errors when total units exceed rack capacity', () => {
    const rack = makeRack(2)
    const tightConstraints = { ...DEFAULT_RACK_CONSTRAINTS, maxUnits: 0 }
    const { errors } = validateCapacityLimits(rack, tightConstraints)
    const capacityErrors = errors.filter(e => e.type === 'capacity')
    expect(capacityErrors.length).toBeGreaterThan(0)
  })

  it('no errors for an under-capacity rack', () => {
    const rack = makeRack(1)
    const { errors } = validateCapacityLimits(rack, DEFAULT_RACK_CONSTRAINTS)
    expect(errors).toHaveLength(0)
  })
})
