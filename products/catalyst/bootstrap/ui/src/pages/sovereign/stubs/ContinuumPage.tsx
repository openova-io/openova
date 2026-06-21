/**
 * ContinuumPage — the live Continuum DR surface (#3375 / #1101).
 *
 * #3969 moved DR OUT of the per-app Topology tab into this dedicated
 * page. This file replaces the original target-state STUB (which only
 * resolved the routes to a 200 + page-title token) with REAL data
 * wiring: it fetches the live Continuum CR / fleet roll-up off
 * catalyst-api and renders the COMPLETE continuum widgets the K-Cont-2
 * reconciler patches (StatusPanel, FailbackPanel, SwitchoverHistory).
 *
 * URL shapes mounted (UNCHANGED from the stub — the route-matrix test
 * asserts these resolve with the "Continuum" page-title token + the
 * continuumId derived from the URL, never hardcoded — INVIOLABLE
 * PRINCIPLES #4):
 *   /app/$deploymentId/continuum                        — fleet list
 *   /app/$deploymentId/continuum/$continuumId           — overview (DR status)
 *   /app/$deploymentId/continuum/$continuumId/audit     — switchover audit
 *   /app/$deploymentId/continuum/$continuumId/settings  — RPO/RTO summary
 *
 * Honest empty/loading/error states: a Continuum CR may not exist yet on
 * a single-region or pre-converged Sovereign. The backend returns 404
 * (getContinuum throws) — we render a calm "no DR pair yet" placeholder,
 * NEVER a crash or a fabricated green status.
 */

import { useMemo } from 'react'
import { useParams, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { useSession } from '@/shared/lib/useSession'
import { StatusPanel } from '@/widgets/continuum/StatusPanel'
import { FailbackPanel } from '@/widgets/continuum/FailbackPanel'
import { SwitchoverHistory } from '@/widgets/continuum/SwitchoverHistory'
import {
  getContinuum,
  listContinuumAudit,
  listFleetContinuums,
  type ContinuumGetResponse,
} from '@/lib/continuum.api'

export interface ContinuumPageProps {
  /** Sub-page mode: list / overview / audit / settings. */
  mode?: 'list' | 'overview' | 'audit' | 'settings'
}

/**
 * deriveTier — collapse the session's explicit tier claim (or the
 * catalyst-<tier> realm roles) into a single tier string. Mirrors the
 * AppDetail derivation so the FailbackPanel's owner/admin affordances
 * gate identically. The server remains the single source of truth for
 * authorization — this only shows/hides the convenience controls.
 */
function deriveTier(tier: string, roles: string[]): string {
  if (tier) return tier
  const lower = roles.map((r) => r.toLowerCase())
  if (lower.includes('catalyst-owner')) return 'owner'
  if (lower.includes('catalyst-admin')) return 'admin'
  if (lower.includes('catalyst-operator')) return 'operator'
  if (lower.includes('catalyst-developer')) return 'developer'
  if (lower.includes('catalyst-viewer')) return 'viewer'
  return ''
}

export function ContinuumPage({ mode = 'list' }: ContinuumPageProps = {}) {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    continuumId?: string
  }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const continuumId = params.continuumId ?? ''

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Continuum">
      <div className="p-6 space-y-4" data-testid="continuum-page">
        <h2 className="text-xl font-semibold text-[var(--color-text)]">DR — Continuum</h2>
        {mode === 'list' && <FleetList deploymentId={deploymentId} />}
        {mode === 'overview' && (
          <ContinuumOverview deploymentId={deploymentId} continuumId={continuumId} />
        )}
        {mode === 'audit' && (
          <ContinuumAudit deploymentId={deploymentId} continuumId={continuumId} />
        )}
        {mode === 'settings' && (
          <ContinuumSettings deploymentId={deploymentId} continuumId={continuumId} />
        )}
      </div>
    </PortalShell>
  )
}

/* ── list mode ─────────────────────────────────────────────────────── */

function FleetList({ deploymentId }: { deploymentId: string }) {
  const navigate = useNavigate()
  const q = useQuery({
    queryKey: ['continuum-fleet'],
    queryFn: () => listFleetContinuums(),
    retry: false,
    refetchInterval: 30_000,
  })

  if (q.isLoading) {
    return (
      <div data-testid="continuum-list" className="text-sm text-[var(--color-text-dim)]">
        <p data-testid="continuum-list-loading">Loading DR pairs…</p>
      </div>
    )
  }
  if (q.isError) {
    return (
      <div data-testid="continuum-list" className="text-sm">
        <p data-testid="continuum-list-error" className="text-[var(--color-text-dim)]">
          Could not load the Continuum fleet. {(q.error as Error).message}
        </p>
      </div>
    )
  }

  const items = q.data?.items ?? []
  if (items.length === 0) {
    return (
      <div data-testid="continuum-list" className="text-sm">
        <p data-testid="continuum-list-empty" className="text-[var(--color-text-dim)]">
          No Continuum DR pairs yet. A pair appears once an Application is placed
          active-hot-standby across two regions.
        </p>
      </div>
    )
  }

  return (
    <div data-testid="continuum-list">
      <table className="mt-2 w-full text-sm">
        <thead className="text-left text-[var(--color-text-dim)]">
          <tr>
            <th className="pb-1.5">Name</th>
            <th className="pb-1.5">Phase</th>
            <th className="pb-1.5">Primary region</th>
            <th className="pb-1.5">Lag</th>
          </tr>
        </thead>
        <tbody>
          {items.map((it, i) => (
            <tr
              key={`${it.namespace}-${it.name}-${i}`}
              data-testid={`continuum-list-row-${i}`}
              className="cursor-pointer border-t border-[var(--color-border)] hover:bg-[var(--color-bg-2)]"
              onClick={() =>
                void navigate({
                  to: `/app/${deploymentId}/continuum/${it.name}` as never,
                })
              }
            >
              <td className="py-1.5 font-mono text-[var(--color-text)]">{it.name}</td>
              <td className="py-1.5">
                <span
                  className={`inline-flex rounded-md px-1.5 py-0.5 text-[10px] font-semibold uppercase ${
                    it.healthy ? 'bg-green-500/10 text-green-400' : 'bg-yellow-500/10 text-yellow-400'
                  }`}
                >
                  {it.phase || (it.healthy ? 'Healthy' : 'Degraded')}
                </span>
              </td>
              <td className="py-1.5 font-mono text-[var(--color-text-dim)]">
                {it.primaryRegion || '—'}
              </td>
              <td className="py-1.5 font-mono text-[var(--color-text-dim)]">
                {Number.isFinite(it.walLagSeconds) ? `${it.walLagSeconds}s` : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/* ── overview mode ─────────────────────────────────────────────────── */

/** useContinuum — shared GET /continuums/{name} hook used by every per-CR mode. */
function useContinuum(deploymentId: string, continuumId: string) {
  return useQuery<ContinuumGetResponse>({
    queryKey: ['continuum', deploymentId, continuumId],
    queryFn: () => getContinuum(deploymentId, continuumId),
    enabled: !!deploymentId && !!continuumId,
    retry: false,
    refetchInterval: 15_000,
  })
}

/**
 * isNoCRError — the backend 404s (getContinuum throws "HTTP 404") when
 * there is genuinely no 2-region cnpg-pair / Continuum CR for this app.
 * That is an HONEST empty-state, not a failure — render the calm "no DR
 * pair yet" placeholder rather than an error banner.
 */
function isNoCRError(err: unknown): boolean {
  return err instanceof Error && /HTTP 404/.test(err.message)
}

function ContinuumOverview({
  deploymentId,
  continuumId,
}: {
  deploymentId: string
  continuumId: string
}) {
  const session = useSession()
  const tier = deriveTier(session.tier, session.roles)
  const isOwner = ['owner', 'admin'].includes(tier)
  const isSovereignAdmin = ['owner', 'admin'].includes(tier)

  const q = useContinuum(deploymentId, continuumId)

  const spec = (q.data?.spec ?? {}) as Record<string, unknown>
  const status = (q.data?.status ?? {}) as Record<string, unknown>
  const primaryRegion =
    (status['primaryRegion'] as string | undefined) ??
    (spec['primaryRegion'] as string | undefined) ??
    undefined
  const hotStandbyRegions = useMemo(() => {
    const fromSpec = spec['hotStandbyRegions']
    if (Array.isArray(fromSpec)) return fromSpec.map((r) => String(r))
    const replica = status['replicaRegion'] as string | undefined
    return replica ? [replica] : []
  }, [spec, status])

  const failback = (spec['failback'] as Record<string, unknown> | undefined) ?? {}
  const last = (status['lastSwitchover'] as Record<string, unknown> | undefined) ?? undefined
  const switchoverSucceeded =
    (last?.['result'] as string | undefined) === 'Success' ||
    (status['phase'] as string | undefined) === 'FailedOver'

  if (q.isLoading) {
    return (
      <div data-testid="continuum-overview">
        <p data-testid="continuum-overview-loading" className="text-sm text-[var(--color-text-dim)]">
          Loading DR status for <code>{continuumId}</code>…
        </p>
      </div>
    )
  }

  // Honest empty-state: no Continuum CR / no 2-region pair yet.
  if (q.isError && isNoCRError(q.error)) {
    return (
      <div data-testid="continuum-overview">
        <div
          data-testid="continuum-overview-no-cr"
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 text-sm text-[var(--color-text-dim)]"
        >
          <p className="font-semibold text-[var(--color-text)]">No DR pair yet</p>
          <p className="mt-1">
            <code>{continuumId}</code> has no live Continuum record. A DR pair
            (primary + hot standby) appears once this Application is placed
            active-hot-standby across two regions.
          </p>
        </div>
      </div>
    )
  }

  // Genuine fetch error (not a 404) — surface it honestly.
  if (q.isError) {
    return (
      <div data-testid="continuum-overview">
        <p data-testid="continuum-overview-error" className="text-sm text-red-400">
          Could not load DR status for <code>{continuumId}</code>:{' '}
          {(q.error as Error).message}
        </p>
      </div>
    )
  }

  return (
    <div data-testid="continuum-overview" className="space-y-4">
      <StatusPanel
        status={status}
        primaryRegion={primaryRegion}
        hotStandbyRegions={hotStandbyRegions}
      />
      {switchoverSucceeded ? (
        <FailbackPanel
          sovereignId={deploymentId}
          continuumName={continuumId}
          namespace={q.data?.namespace}
          isOwner={isOwner}
          isSovereignAdmin={isSovereignAdmin}
          approvalRequired={Boolean(failback['approvalRequired'] ?? false)}
          failbackRequested={Boolean(failback['requested'] ?? false)}
          failbackApproved={Boolean(failback['approved'] ?? false)}
          onChanged={() => void q.refetch()}
        />
      ) : null}
    </div>
  )
}

/* ── audit mode ────────────────────────────────────────────────────── */

function ContinuumAudit({
  deploymentId,
  continuumId,
}: {
  deploymentId: string
  continuumId: string
}) {
  const q = useQuery({
    queryKey: ['continuum-audit', deploymentId, continuumId],
    queryFn: () => listContinuumAudit(deploymentId, { limit: 200 }),
    enabled: !!deploymentId,
    retry: false,
    refetchInterval: 30_000,
  })

  if (q.isLoading) {
    return (
      <div data-testid="continuum-audit">
        <p data-testid="continuum-audit-loading" className="text-sm text-[var(--color-text-dim)]">
          Loading switchover audit for <code>{continuumId}</code>…
        </p>
      </div>
    )
  }
  if (q.isError) {
    return (
      <div data-testid="continuum-audit">
        <p data-testid="continuum-audit-error" className="text-sm text-[var(--color-text-dim)]">
          Could not load the switchover audit. {(q.error as Error).message}
        </p>
      </div>
    )
  }

  const events = q.data?.items ?? []
  return (
    <div data-testid="continuum-audit" className="space-y-2">
      <p className="text-sm text-[var(--color-text-dim)]">
        Switchover audit trail for <code>{continuumId}</code>.
      </p>
      <SwitchoverHistory events={events} />
    </div>
  )
}

/* ── settings mode ─────────────────────────────────────────────────── */

/**
 * ContinuumSettings — read-only RPO/RTO summary off the live CR spec.
 *
 * The catalyst-api DOES expose a PUT /continuum/{name} for rpo/rto, but
 * this slice ships the READ-ONLY summary only (per the brief: do NOT
 * fabricate write controls). Wiring the live editor is a follow-up.
 */
function ContinuumSettings({
  deploymentId,
  continuumId,
}: {
  deploymentId: string
  continuumId: string
}) {
  const q = useContinuum(deploymentId, continuumId)
  const spec = (q.data?.spec ?? {}) as Record<string, unknown>

  if (q.isLoading) {
    return (
      <div data-testid="continuum-settings">
        <p data-testid="continuum-settings-loading" className="text-sm text-[var(--color-text-dim)]">
          Loading DR policy for <code>{continuumId}</code>…
        </p>
      </div>
    )
  }
  if (q.isError && isNoCRError(q.error)) {
    return (
      <div data-testid="continuum-settings">
        <p data-testid="continuum-settings-no-cr" className="text-sm text-[var(--color-text-dim)]">
          No DR policy yet — <code>{continuumId}</code> has no live Continuum record.
        </p>
      </div>
    )
  }
  if (q.isError) {
    return (
      <div data-testid="continuum-settings">
        <p data-testid="continuum-settings-error" className="text-sm text-red-400">
          Could not load DR policy: {(q.error as Error).message}
        </p>
      </div>
    )
  }

  const rpo = spec['rpoSeconds'] ?? spec['rpo'] ?? '—'
  const rto = spec['rtoSeconds'] ?? spec['rto'] ?? '—'
  const autoFailover = Boolean(spec['autoFailover'] ?? false)

  return (
    <div data-testid="continuum-settings" className="space-y-2">
      <p className="text-sm text-[var(--color-text-dim)]">
        DR policy for <code>{continuumId}</code> (read-only).
      </p>
      <dl className="grid max-w-md grid-cols-2 gap-2 text-sm">
        <dt className="text-[var(--color-text-dim)]">RPO (seconds)</dt>
        <dd className="font-mono text-[var(--color-text)]" data-testid="continuum-settings-rpo">
          {String(rpo)}
        </dd>
        <dt className="text-[var(--color-text-dim)]">RTO (seconds)</dt>
        <dd className="font-mono text-[var(--color-text)]" data-testid="continuum-settings-rto">
          {String(rto)}
        </dd>
        <dt className="text-[var(--color-text-dim)]">Auto-failover</dt>
        <dd className="font-mono text-[var(--color-text)]" data-testid="continuum-settings-autofailover">
          {autoFailover ? 'enabled' : 'disabled'}
        </dd>
      </dl>
    </div>
  )
}
