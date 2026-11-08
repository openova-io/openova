/**
 * ContinuumDashboard — the central topology-transition view (ADR-0015 D8).
 *
 * Lists every protected group (an Application plus the Applications it
 * depends on) with its current or last transition and, per member, the
 * phase rows Continuum recorded: traffic-off → data-primary → peers →
 * traffic-on. Everything is read from the Continuum CRs' OWN status as
 * the live k8s stream delivers them (one copy per region): the Ready
 * condition and status.activity[] are rendered verbatim through the D7
 * StatusBadge / StatusReason — no derivation here. Each member links to
 * its own resource pages (the Application and its Continuum).
 *
 * There is no action on this page. A transition is started by changing the
 * Application's Primary in its placement (Applications → <app> →
 * Topology); Continuum carries it out as Git commits.
 */
import { useMemo } from 'react'
import { useCloud } from '../CloudPage'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import { TRANSITION_PHASES, groupContinuums, phaseEntry } from './continuumDashboard.model'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { StatusBadge, StatusReason } from '@/components/status/StatusBadge'
import { resourceDetailHref } from './resource.api'

export function ContinuumDashboard() {
  const { k8sSnapshot, k8sStatus, k8sRevision, deploymentId } = useCloud()
  const base = DETECTED_MODE.mode === 'sovereign' || !deploymentId ? '/cloud' : `/provision/${deploymentId}/cloud`

  const groups = useMemo(() => {
    const objs: K8sObject[] = []
    if (k8sSnapshot) {
      for (const [key, obj] of k8sSnapshot.entries()) {
        if (key.startsWith('continuum:')) objs.push(obj as K8sObject)
      }
    }
    return groupContinuums(objs)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [k8sSnapshot, k8sRevision])

  return (
    <div data-testid="continuum-dashboard">
      <div className="mb-4">
        <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">Topology transitions</h2>
        <p className="text-sm text-[var(--color-text-dim)]">
          Every protected group and its last primary move, phase by phase. Each row is read from the member's own
          Continuum; a move is started by changing Primary in the Application's placement.
        </p>
      </div>
      {groups.length === 0 ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]" data-testid="continuum-dashboard-empty">
          {k8sStatus === 'connecting' ? 'Connecting to live cluster stream…' : 'No Continuum objects — no Application is placed across regions with a primary to move.'}
        </div>
      ) : (
        groups.map((g) => {
          const t = g.transition
          const groupStatus = {
            state: g.members.every((m) => m.ready.state === 'Ready')
              ? 'Ready'
              : g.members.some((m) => m.ready.state === 'NotReady')
                ? 'NotReady'
                : 'Unknown',
            reason: t?.outcome,
            message: t?.message,
          }
          return (
            <section
              key={g.id}
              data-testid={`continuum-group-${g.id}`}
              className="mb-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
            >
              <header className="mb-2 flex flex-wrap items-baseline gap-3">
                <h3 className="font-mono text-sm font-semibold">{g.id}</h3>
                <StatusBadge status={groupStatus} testId={`continuum-group-${g.id}-status`} />
                <StatusReason status={groupStatus} testId={`continuum-group-${g.id}-reason`} />
              </header>
              <dl className="mb-3 grid grid-cols-2 gap-x-6 gap-y-1 text-xs text-[var(--color-text-dim)] sm:grid-cols-4">
                <div>
                  <dt>From</dt>
                  <dd className="font-mono" data-testid={`continuum-group-${g.id}-from`}>{t?.from ?? '—'}</dd>
                </div>
                <div>
                  <dt>To</dt>
                  <dd className="font-mono" data-testid={`continuum-group-${g.id}-to`}>{t?.to ?? '—'}</dd>
                </div>
                <div>
                  <dt>Started</dt>
                  <dd className="font-mono">{t?.startedAt ?? '—'}</dd>
                </div>
                <div>
                  <dt>Lease holder</dt>
                  <dd className="font-mono" data-testid={`continuum-group-${g.id}-lease`}>{g.leaseHolder || '—'}</dd>
                </div>
              </dl>
              <table className="w-full border-collapse text-sm" data-testid={`continuum-group-${g.id}-table`}>
                <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
                  <tr className="border-b border-[var(--color-border)]">
                    <th className="px-2 py-1 text-left font-medium">Member</th>
                    <th className="px-2 py-1 text-left font-medium">Status</th>
                    <th className="px-2 py-1 text-left font-medium">Primary</th>
                    {TRANSITION_PHASES.map((p) => (
                      <th key={p} className="px-2 py-1 text-left font-medium">{p}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {g.members.map((m) => (
                    <tr key={`${m.namespace}/${m.name}`} className="border-b border-[var(--color-border)] align-top" data-testid={`continuum-member-${m.namespace}-${m.name}`}>
                      <td className="px-2 py-1">
                        <a
                          className="font-mono text-[var(--color-accent)]"
                          href={resourceDetailHref(base, 'application', m.namespace, m.application)}
                          data-testid={`continuum-member-${m.namespace}-${m.name}-app-link`}
                        >
                          {m.namespace}/{m.application}
                        </a>
                        <div>
                          <a
                            className="text-xs text-[var(--color-text-dim)]"
                            href={resourceDetailHref(base, 'continuum', m.namespace, m.name)}
                            data-testid={`continuum-member-${m.namespace}-${m.name}-continuum-link`}
                          >
                            continuum {m.name}
                          </a>
                        </div>
                      </td>
                      <td className="px-2 py-1">
                        <StatusBadge status={m.ready} testId={`continuum-member-${m.namespace}-${m.name}-status`} />
                        <div className="text-xs">
                          <StatusReason status={m.ready} testId={`continuum-member-${m.namespace}-${m.name}-reason`} />
                        </div>
                      </td>
                      <td className="px-2 py-1 font-mono text-xs">
                        <div>{m.committed ?? '—'}</div>
                        {m.desired && m.desired !== m.committed ? <div className="text-[var(--color-text-dim)]">→ {m.desired}</div> : null}
                      </td>
                      {TRANSITION_PHASES.map((p) => {
                        const e = phaseEntry(m, p)
                        const st = e ? { state: e.state ?? 'Unknown', message: e.message } : null
                        return (
                          <td key={p} className="px-2 py-1" data-testid={`continuum-member-${m.namespace}-${m.name}-${p}`}>
                            {st ? (
                              <>
                                <StatusBadge status={st} testId={`continuum-member-${m.namespace}-${m.name}-${p}-state`} />
                                <div className="text-xs text-[var(--color-text-dim)]">{e?.message}</div>
                              </>
                            ) : (
                              <span className="text-[var(--color-text-dim)]">—</span>
                            )}
                          </td>
                        )
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          )
        })
      )}
    </div>
  )
}
