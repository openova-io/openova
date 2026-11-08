/**
 * continuumDashboard.model — pure readers for the topology-transition
 * dashboard (ADR-0015 D8). Every value is read verbatim from the Continuum
 * CR's own status; see ContinuumDashboard.tsx.
 */
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

export const TRANSITION_PHASES = ['traffic-off', 'data-primary', 'peers', 'traffic-on'] as const

export interface Activity {
  id: string
  phase?: string
  member?: string
  state?: string
  startedAt?: string
  finishedAt?: string
  message?: string
}

export interface TransitionRecord {
  id?: string
  group?: string
  from?: string
  to?: string
  startedAt?: string
  outcome?: string
  message?: string
  lossMode?: boolean
}

export interface ContinuumMember {
  namespace: string
  name: string
  application: string
  region?: string
  desired?: string
  committed?: string
  leaseHolder?: string
  ready: { state: string; reason?: string; message?: string }
  transition?: TransitionRecord
  activity: Activity[]
}

export interface ContinuumGroup {
  id: string
  members: ContinuumMember[]
  transition?: TransitionRecord
  leaseHolder?: string
}

function rec(v: unknown): Record<string, unknown> {
  return v && typeof v === 'object' ? (v as Record<string, unknown>) : {}
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' && v !== '' ? v : undefined
}

/** The object's own Ready condition, verbatim (the D7 statusOf contract). */
export function readyOf(o: K8sObject): { state: string; reason?: string; message?: string } {
  const conds = rec(o.status)['conditions']
  if (Array.isArray(conds)) {
    for (const c of conds) {
      const m = rec(c)
      if (m['type'] !== 'Ready') continue
      const state = m['status'] === 'True' ? 'Ready' : m['status'] === 'False' ? 'NotReady' : 'Unknown'
      return { state, reason: str(m['reason']), message: str(m['message']) }
    }
  }
  return { state: 'Unknown' }
}

export function memberOf(o: K8sObject): ContinuumMember {
  const spec = rec(o.spec)
  const status = rec(o.status)
  const appRef = str(spec['applicationRef']) ?? ''
  const application = appRef.includes('/') ? appRef.split('/', 2)[1] : appRef
  const acts = Array.isArray(status['activity']) ? (status['activity'] as unknown[]) : []
  return {
    namespace: o.metadata?.namespace ?? '',
    name: o.metadata?.name ?? '',
    application,
    region: o.clusterId,
    desired: str(status['desiredPrimaryRegion']) ?? str(spec['desiredPrimaryRegion']),
    committed: str(status['primaryRegion']),
    leaseHolder: str(status['leaseHolder']),
    ready: readyOf(o),
    transition: status['transition'] ? (rec(status['transition']) as TransitionRecord) : undefined,
    activity: acts.map((a) => rec(a) as unknown as Activity).filter((a) => typeof a.id === 'string'),
  }
}

/** a carries a more recent transition record than b (record > none, then startedAt, then entries). */
function newer(a: ContinuumMember, b: ContinuumMember): boolean {
  const ra = a.transition?.id ? 1 : 0
  const rb = b.transition?.id ? 1 : 0
  if (ra !== rb) return ra > rb
  const sa = a.transition?.startedAt ?? ''
  const sb = b.transition?.startedAt ?? ''
  if (sa !== sb) return sa > sb
  return a.activity.length > b.activity.length
}

/**
 * groupContinuums folds the per-region copies of every Continuum into one
 * member (the copy carrying the most recent transition record wins — every
 * region mirrors the same record from Git) and groups members by
 * status.transition.group.
 */
export function groupContinuums(objs: K8sObject[]): ContinuumGroup[] {
  const byMember = new Map<string, ContinuumMember>()
  for (const o of objs) {
    const m = memberOf(o)
    const key = `${m.namespace}/${m.name}`
    const prev = byMember.get(key)
    if (!prev || newer(m, prev)) byMember.set(key, m)
  }
  const groups = new Map<string, ContinuumGroup>()
  for (const m of byMember.values()) {
    const id = m.transition?.group ?? `${m.namespace}/${m.name}`
    const g = groups.get(id) ?? { id, members: [] }
    g.members.push(m)
    if (m.transition?.id && (!g.transition?.startedAt || (m.transition.startedAt ?? '') > g.transition.startedAt)) {
      g.transition = m.transition
    }
    if (m.leaseHolder) g.leaseHolder = m.leaseHolder
    if (!g.transition && m.transition) g.transition = m.transition
    groups.set(id, g)
  }
  const out = [...groups.values()]
  for (const g of out) g.members.sort((a, b) => a.name.localeCompare(b.name))
  out.sort((a, b) => a.id.localeCompare(b.id))
  return out
}

/** The member's entry for one phase, verbatim. */
export function phaseEntry(m: ContinuumMember, phase: string): Activity | undefined {
  return m.activity.find((a) => a.phase === phase)
}
