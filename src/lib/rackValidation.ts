import { ClusterNode, DeviceType } from './types'
import { RackData, calculateRackPowerCooling } from './rack'

export interface RackConstraints {
  maxPowerWatts: number
  maxCoolingBTU: number
  maxUnits: number
  minSpacingUnits: number
  maxThermalDensity: number
  maxPowerDensity: number
  maxAmperage: number
  maxWeight: number
  minAirflowCFM: number
}

export interface ValidationResult {
  valid: boolean
  errors: ValidationError[]
  warnings: ValidationWarning[]
}

export interface ValidationError {
  type: 'power' | 'cooling' | 'spacing' | 'physical' | 'capacity'
  message: string
  severity: 'error' | 'critical'
  affectedUnits?: number[]
  currentValue?: number
  limitValue?: number
}

export interface ValidationWarning {
  type: 'power' | 'cooling' | 'thermal' | 'efficiency'
  message: string
  recommendation: string
  currentValue?: number
  thresholdValue?: number
  affectedUnits?: number[]
}

export const DEFAULT_RACK_CONSTRAINTS: RackConstraints = {
  maxPowerWatts: 12000,
  maxCoolingBTU: 40000,
  maxUnits: 42,
  minSpacingUnits: 0,
  maxThermalDensity: 15,
  maxPowerDensity: 400,
  maxAmperage: 50,
  maxWeight: 1500,
  minAirflowCFM: 2000
}

export const DEVICE_POWER_PROFILES: Record<DeviceType, { watts: number; weight: number }> = {
  server: { watts: 350, weight: 45 },
  storage: { watts: 280, weight: 55 },
  network: { watts: 150, weight: 25 },
  pdu: { watts: 50, weight: 15 },
  ups: { watts: 100, weight: 120 },
  blank: { watts: 0, weight: 0 }
}

export function calculateDevicePower(node: ClusterNode): number {
  const baseProfile = DEVICE_POWER_PROFILES[node.hardware.deviceType]
  const utilizationFactor = (node.metrics.cpu + node.metrics.memory) / 200
  return baseProfile.watts * (0.5 + utilizationFactor * 0.5)
}

export function calculateDeviceWeight(node: ClusterNode): number {
  return DEVICE_POWER_PROFILES[node.hardware.deviceType].weight * node.hardware.rackUnits
}

export function validatePowerConstraints(
  rack: RackData,
  constraints: RackConstraints
): { errors: ValidationError[]; warnings: ValidationWarning[] } {
  const errors: ValidationError[] = []
  const warnings: ValidationWarning[] = []
  
  const powerMetrics = calculateRackPowerCooling(rack)
  const totalPower = powerMetrics.power.currentDraw
  const powerUtilization = (totalPower / constraints.maxPowerWatts) * 100
  
  if (totalPower > constraints.maxPowerWatts) {
    errors.push({
      type: 'power',
      severity: 'critical',
      message: `Rack power exceeds maximum capacity`,
      currentValue: totalPower,
      limitValue: constraints.maxPowerWatts
    })
  } else if (totalPower > constraints.maxPowerWatts * 0.85) {
    errors.push({
      type: 'power',
      severity: 'error',
      message: `Rack power at ${powerUtilization.toFixed(1)}% capacity - no room for growth`,
      currentValue: totalPower,
      limitValue: constraints.maxPowerWatts
    })
  } else if (totalPower > constraints.maxPowerWatts * 0.75) {
    warnings.push({
      type: 'power',
      message: `Power utilization at ${powerUtilization.toFixed(1)}%`,
      recommendation: 'Consider load balancing or upgrading power infrastructure',
      currentValue: totalPower,
      thresholdValue: constraints.maxPowerWatts * 0.75
    })
  }
  
  const estimatedAmperage = totalPower / 208
  if (estimatedAmperage > constraints.maxAmperage) {
    errors.push({
      type: 'power',
      severity: 'critical',
      message: `Amperage draw (${estimatedAmperage.toFixed(1)}A) exceeds circuit capacity`,
      currentValue: estimatedAmperage,
      limitValue: constraints.maxAmperage
    })
  }
  
  if (powerMetrics.powerDensity > constraints.maxPowerDensity) {
    warnings.push({
      type: 'power',
      message: `High power density detected (${powerMetrics.powerDensity}W/U)`,
      recommendation: 'Distribute high-power devices across multiple racks',
      currentValue: powerMetrics.powerDensity,
      thresholdValue: constraints.maxPowerDensity
    })
  }
  
  return { errors, warnings }
}

export function validateCoolingConstraints(
  rack: RackData,
  constraints: RackConstraints
): { errors: ValidationError[]; warnings: ValidationWarning[] } {
  const errors: ValidationError[] = []
  const warnings: ValidationWarning[] = []
  
  const powerMetrics = calculateRackPowerCooling(rack)
  const thermalLoad = powerMetrics.thermalLoad
  
  if (thermalLoad > constraints.maxCoolingBTU) {
    errors.push({
      type: 'cooling',
      severity: 'critical',
      message: `Thermal load (${thermalLoad} BTU/h) exceeds cooling capacity`,
      currentValue: thermalLoad,
      limitValue: constraints.maxCoolingBTU
    })
  } else if (thermalLoad > constraints.maxCoolingBTU * 0.85) {
    errors.push({
      type: 'cooling',
      severity: 'error',
      message: `Thermal load at ${((thermalLoad / constraints.maxCoolingBTU) * 100).toFixed(1)}% of cooling capacity`,
      currentValue: thermalLoad,
      limitValue: constraints.maxCoolingBTU
    })
  } else if (thermalLoad > constraints.maxCoolingBTU * 0.75) {
    warnings.push({
      type: 'cooling',
      message: `Approaching cooling capacity limit`,
      recommendation: 'Monitor temperatures and consider additional cooling',
      currentValue: thermalLoad,
      thresholdValue: constraints.maxCoolingBTU * 0.75
    })
  }
  
  if (powerMetrics.cooling.outletTemp > 35) {
    errors.push({
      type: 'cooling',
      severity: 'critical',
      message: `Outlet temperature (${powerMetrics.cooling.outletTemp}°C) exceeds safe operating limit`,
      currentValue: powerMetrics.cooling.outletTemp,
      limitValue: 35
    })
  } else if (powerMetrics.cooling.outletTemp > 32) {
    warnings.push({
      type: 'thermal',
      message: `Elevated outlet temperature detected`,
      recommendation: 'Improve airflow or reduce thermal load',
      currentValue: powerMetrics.cooling.outletTemp,
      thresholdValue: 32
    })
  }
  
  if (powerMetrics.cooling.airflowCFM < constraints.minAirflowCFM) {
    warnings.push({
      type: 'cooling',
      message: `Insufficient airflow (${powerMetrics.cooling.airflowCFM} CFM)`,
      recommendation: 'Increase fan speed or remove airflow obstructions',
      currentValue: powerMetrics.cooling.airflowCFM,
      thresholdValue: constraints.minAirflowCFM
    })
  }
  
  const hotNodes = rack.nodes.filter(n => n.hardware.temperature > 75)
  if (hotNodes.length > 0) {
    const maxTemp = Math.max(...hotNodes.map(n => n.hardware.temperature))
    if (maxTemp > 80) {
      errors.push({
        type: 'cooling',
        severity: 'critical',
        message: `${hotNodes.length} device(s) exceeding thermal limits`,
        currentValue: maxTemp,
        limitValue: 80,
        affectedUnits: hotNodes.map(n => n.rackPosition)
      })
    } else {
      warnings.push({
        type: 'thermal',
        message: `${hotNodes.length} device(s) running hot`,
        recommendation: 'Check airflow and device placement',
        currentValue: maxTemp,
        thresholdValue: 75
      })
    }
  }
  
  return { errors, warnings }
}

export function validatePhysicalSpacing(
  rack: RackData,
  constraints: RackConstraints
): { errors: ValidationError[]; warnings: ValidationWarning[] } {
  const errors: ValidationError[] = []
  const warnings: ValidationWarning[] = []
  
  const sortedNodes = [...rack.nodes].sort((a, b) => a.rackPosition - b.rackPosition)
  
  for (let i = 0; i < sortedNodes.length - 1; i++) {
    const current = sortedNodes[i]
    const next = sortedNodes[i + 1]
    
    const currentEndPosition = current.rackPosition + current.hardware.rackUnits
    const spacing = next.rackPosition - currentEndPosition
    
    if (spacing < 0) {
      errors.push({
        type: 'spacing',
        severity: 'critical',
        message: `Device overlap detected between U${current.rackPosition} and U${next.rackPosition}`,
        affectedUnits: [current.rackPosition, next.rackPosition]
      })
    } else if (spacing < constraints.minSpacingUnits) {
      errors.push({
        type: 'spacing',
        severity: 'error',
        message: `Insufficient spacing between devices (${spacing}U gap)`,
        currentValue: spacing,
        limitValue: constraints.minSpacingUnits,
        affectedUnits: [current.rackPosition, next.rackPosition]
      })
    }
  }
  
  const highThermalDevices = sortedNodes.filter(n => 
    n.hardware.deviceType === 'server' && n.hardware.temperature > 70
  )
  
  for (const device of highThermalDevices) {
    const adjacentDevices = sortedNodes.filter(n => {
      const distance = Math.abs(n.rackPosition - device.rackPosition)
      return n.id !== device.id && distance <= device.hardware.rackUnits + 2
    })
    
    const adjacentHighThermal = adjacentDevices.filter(n => 
      n.hardware.temperature > 70 && n.hardware.deviceType === 'server'
    )
    
    if (adjacentHighThermal.length >= 2) {
      warnings.push({
        type: 'thermal',
        message: `Dense thermal concentration near U${device.rackPosition}`,
        recommendation: 'Add spacing or blank panels between high-power devices',
        affectedUnits: [device.rackPosition]
      })
    }
  }
  
  return { errors, warnings }
}

export function validateCapacityLimits(
  rack: RackData,
  constraints: RackConstraints
): { errors: ValidationError[]; warnings: ValidationWarning[] } {
  const errors: ValidationError[] = []
  const warnings: ValidationWarning[] = []
  
  const totalUnitsUsed = rack.nodes.reduce((sum, node) => sum + node.hardware.rackUnits, 0)
  const totalWeight = rack.nodes.reduce((sum, node) => sum + calculateDeviceWeight(node), 0)
  
  if (totalUnitsUsed > constraints.maxUnits) {
    errors.push({
      type: 'capacity',
      severity: 'critical',
      message: `Rack unit capacity exceeded (${totalUnitsUsed}U used of ${constraints.maxUnits}U)`,
      currentValue: totalUnitsUsed,
      limitValue: constraints.maxUnits
    })
  }
  
  const occupancyRate = (totalUnitsUsed / constraints.maxUnits) * 100
  if (occupancyRate > 90) {
    warnings.push({
      type: 'efficiency',
      message: `Rack is ${occupancyRate.toFixed(1)}% full`,
      recommendation: 'Consider planning for additional rack space',
      currentValue: totalUnitsUsed,
      thresholdValue: constraints.maxUnits * 0.9
    })
  }
  
  if (totalWeight > constraints.maxWeight) {
    errors.push({
      type: 'physical',
      severity: 'critical',
      message: `Rack weight limit exceeded (${totalWeight}kg vs ${constraints.maxWeight}kg max)`,
      currentValue: totalWeight,
      limitValue: constraints.maxWeight
    })
  }
  
  return { errors, warnings }
}

export function validateRackPlacement(
  rack: RackData,
  constraints: RackConstraints = DEFAULT_RACK_CONSTRAINTS
): ValidationResult {
  const powerValidation = validatePowerConstraints(rack, constraints)
  const coolingValidation = validateCoolingConstraints(rack, constraints)
  const spacingValidation = validatePhysicalSpacing(rack, constraints)
  const capacityValidation = validateCapacityLimits(rack, constraints)
  
  const allErrors = [
    ...powerValidation.errors,
    ...coolingValidation.errors,
    ...spacingValidation.errors,
    ...capacityValidation.errors
  ]
  
  const allWarnings = [
    ...powerValidation.warnings,
    ...coolingValidation.warnings,
    ...spacingValidation.warnings,
    ...capacityValidation.warnings
  ]
  
  return {
    valid: allErrors.length === 0,
    errors: allErrors,
    warnings: allWarnings
  }
}

export function validateDeviceMove(
  sourceRack: RackData,
  targetRack: RackData,
  device: ClusterNode,
  targetPosition: number,
  constraints: RackConstraints = DEFAULT_RACK_CONSTRAINTS
): ValidationResult {
  const errors: ValidationError[] = []
  const warnings: ValidationWarning[] = []
  
  if (targetPosition < 1 || targetPosition > constraints.maxUnits) {
    errors.push({
      type: 'capacity',
      severity: 'error',
      message: `Invalid position U${targetPosition} (valid range: 1-${constraints.maxUnits})`,
      currentValue: targetPosition,
      limitValue: constraints.maxUnits
    })
  }
  
  if (targetPosition + device.hardware.rackUnits - 1 > constraints.maxUnits) {
    errors.push({
      type: 'capacity',
      severity: 'error',
      message: `Device extends beyond rack (U${targetPosition} + ${device.hardware.rackUnits}U exceeds U${constraints.maxUnits})`,
      currentValue: targetPosition + device.hardware.rackUnits - 1,
      limitValue: constraints.maxUnits
    })
  }
  
  const occupiedUnits = new Set<number>()
  for (const node of targetRack.nodes) {
    if (node.id === device.id) continue
    
    for (let u = node.rackPosition; u < node.rackPosition + node.hardware.rackUnits; u++) {
      occupiedUnits.add(u)
    }
  }
  
  for (let u = targetPosition; u < targetPosition + device.hardware.rackUnits; u++) {
    if (occupiedUnits.has(u)) {
      errors.push({
        type: 'spacing',
        severity: 'critical',
        message: `Position U${targetPosition} conflicts with existing device at U${u}`,
        affectedUnits: [targetPosition, u]
      })
      break
    }
  }
  
  const simulatedNodes = targetRack.nodes
    .filter(n => n.id !== device.id)
    .concat([{ ...device, rackId: targetRack.id, rackPosition: targetPosition }])
  
  const simulatedRack: RackData = {
    ...targetRack,
    nodes: simulatedNodes
  }
  
  const rackValidation = validateRackPlacement(simulatedRack, constraints)
  
  return {
    valid: errors.length === 0 && rackValidation.valid,
    errors: [...errors, ...rackValidation.errors],
    warnings: [...warnings, ...rackValidation.warnings]
  }
}

export function getValidationSummary(result: ValidationResult): string {
  if (result.valid && result.warnings.length === 0) {
    return 'All constraints satisfied'
  }
  
  const criticalErrors = result.errors.filter(e => e.severity === 'critical').length
  const errors = result.errors.filter(e => e.severity === 'error').length
  const warnings = result.warnings.length
  
  const parts: string[] = []
  if (criticalErrors > 0) parts.push(`${criticalErrors} critical`)
  if (errors > 0) parts.push(`${errors} errors`)
  if (warnings > 0) parts.push(`${warnings} warnings`)
  
  return parts.join(', ')
}
