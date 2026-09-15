/**
 * Console projection of the alert registry (FrameAlert) and of subscription
 * health (FrameAlertSubscription). Pure, so the screen's reading of the
 * objects is testable without an apiserver.
 */

export type AlertDeliveryView = { subscription: string; delivered: boolean; lastError: string; permanent: boolean }

export type RegistryAlert = {
  name: string
  fingerprint: string
  alertName: string
  severity: string
  namespace: string
  state: 'Firing' | 'Resolved'
  startsAt: string
  endsAt: string
  deliveries: AlertDeliveryView[]
}

export type AlertSubscriptionHealth = {
  name: string
  url: string
  paused: boolean
  pending: number
  lastSuccessAt: string
  lastError: string
}

type RawAlert = {
  metadata?: { name?: string }
  spec?: { fingerprint?: string; alertName?: string; severity?: string; namespace?: string; startsAt?: string; endsAt?: string }
  status?: {
    state?: string
    deliveries?: { subscription?: string; deliveredState?: string; lastError?: string; permanentFailure?: boolean; excluded?: boolean }[]
  }
}

export function projectAlerts(items: unknown[]): RegistryAlert[] {
  return (items as RawAlert[])
    .filter((i) => i.status?.state === 'Firing' || i.status?.state === 'Resolved')
    .map((i) => {
      const state = i.status!.state as 'Firing' | 'Resolved'
      return {
        name: i.metadata?.name ?? '',
        fingerprint: i.spec?.fingerprint ?? '',
        alertName: i.spec?.alertName ?? 'Unknown',
        severity: i.spec?.severity || 'none',
        namespace: i.spec?.namespace ?? '',
        state,
        startsAt: i.spec?.startsAt ?? '',
        endsAt: i.spec?.endsAt ?? '',
        // Excluded deliveries were recorded without ever being sent (the
        // subscription's filter did not match when the alert resolved) —
        // spec §5.3: they must not appear as a badge the tenant never saw.
        deliveries: (i.status?.deliveries ?? [])
          .filter((d) => !d.excluded)
          .map((d) => ({
            subscription: d.subscription ?? '',
            delivered: d.deliveredState === state,
            lastError: d.lastError ?? '',
            permanent: d.permanentFailure ?? false,
          })),
      }
    })
    .sort((a, b) => (a.state === b.state ? b.startsAt.localeCompare(a.startsAt) : a.state === 'Firing' ? -1 : 1))
}

type RawSubscription = {
  metadata?: { name?: string }
  spec?: { url?: string; paused?: boolean }
  status?: { pendingDeliveries?: number; lastSuccessAt?: string; lastError?: string }
}

export function projectSubscriptions(items: unknown[]): AlertSubscriptionHealth[] {
  return (items as RawSubscription[]).map((s) => ({
    name: s.metadata?.name ?? '',
    url: s.spec?.url ?? '',
    paused: s.spec?.paused ?? false,
    pending: s.status?.pendingDeliveries ?? 0,
    lastSuccessAt: s.status?.lastSuccessAt ?? '',
    lastError: s.status?.lastError ?? '',
  }))
}
