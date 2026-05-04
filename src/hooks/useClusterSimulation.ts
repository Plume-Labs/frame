import { useState, useEffect, useRef } from 'react'
import { ClusterNode, SystemEvent } from '@/lib/types'
import {
  generateClusterNodes,
  updateNodeMetrics,
  simulateStatusChange,
  generateSystemEvent
} from '@/lib/cluster'

/**
 * Manages cluster node state: generates initial nodes and runs the 2-second
 * simulation tick that updates metrics and emits system events.
 *
 * Returns a stable `nodesRef` that always points to the latest nodes array
 * so other hooks can read the current value inside intervals without
 * including `nodes` in their dependency arrays.
 */
export function useClusterSimulation(count: number = 32) {
  const [nodes, setNodes] = useState<ClusterNode[]>(() => generateClusterNodes(count))
  const [events, setEvents] = useState<SystemEvent[]>([])

  // Always reflects the latest nodes without causing re-subscriptions
  const nodesRef = useRef<ClusterNode[]>(nodes)
  // Initialized with initial nodes so generateSystemEvent has a valid comparison
  // baseline on the very first tick (avoids undefined-index reads into an empty array)
  const previousNodesRef = useRef<ClusterNode[]>(nodes)

  // Keep nodesRef current on every render so interval callbacks see fresh data
  nodesRef.current = nodes

  useEffect(() => {
    const interval = setInterval(() => {
      setNodes((currentNodes) => {
        const updated = currentNodes.map((node) => {
          let updatedNode = updateNodeMetrics(node)
          updatedNode = simulateStatusChange(updatedNode)
          return updatedNode
        })

        const newEvent = generateSystemEvent(updated, previousNodesRef.current)
        if (newEvent) {
          setEvents((currentEvents) => [newEvent, ...currentEvents].slice(0, 100))
        }

        previousNodesRef.current = updated
        nodesRef.current = updated
        return updated
      })
    }, 2000)

    return () => clearInterval(interval)
  }, [])

  return { nodes, setNodes, events, nodesRef }
}
