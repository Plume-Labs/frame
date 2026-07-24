import { Tone } from '@/lib/thresholds'

/**
 * Fixtures shared by more than one tuning dashboard.
 *
 * The Volcano queue figures below were hardcoded twice — once in
 * `ElasticPoolDashboard` (as the `volcanoQueue`/`weight` fields of its pools)
 * and once in `SchedulerDashboard` (as its DRF queue-weight table) — so the two
 * views could drift apart while claiming to describe the same three queues.
 * This is that definition, once — imported by both `ElasticPoolDashboard`
 * (pool `volcanoQueue`/`weight`) and `SchedulerDashboard` (queue-weight table).
 */
export interface VolcanoQueueFixture {
  name: string
  /** DRF scheduling weight. */
  weight: number
  /** Allocated / capability CPU cores, as displayed. */
  cpuAlloc: string
  /** Allocated / capability memory, as displayed. */
  memAlloc: string
  tone: Tone
}

export const VOLCANO_QUEUES = {
  'neura-high': {
    name: 'neura-high',
    weight: 100,
    cpuAlloc: '180/200',
    memAlloc: '360/400 Gi',
    tone: 'destructive',
  },
  'neura-medium': {
    name: 'neura-medium',
    weight: 50,
    cpuAlloc: '82/100',
    memAlloc: '155/200 Gi',
    tone: 'warning',
  },
  'neura-low': {
    name: 'neura-low',
    weight: 10,
    cpuAlloc: '290/400',
    memAlloc: '580/800 Gi',
    tone: 'accent',
  },
} as const satisfies Record<string, VolcanoQueueFixture>

export type VolcanoQueueName = keyof typeof VOLCANO_QUEUES

/** Queue rows in display order, for the scheduler's weight table. */
export const VOLCANO_QUEUE_LIST: readonly VolcanoQueueFixture[] = [
  VOLCANO_QUEUES['neura-high'],
  VOLCANO_QUEUES['neura-medium'],
  VOLCANO_QUEUES['neura-low'],
]
