/**
 * DRSection — EPIC-6 Slice U-DR-1 (#1101).
 *
 * Composite Disaster-Recovery section embedded in the AppDetail
 * Topology tab when the Application's placement is `active-hotstandby`.
 * Composes:
 *
 *   - StatusPanel (live phase / lease / lag / switchover-in-progress)
 *   - LuaRecordView (read-only PowerDNS lua-record body)
 *   - "Switchover…" button → SwitchoverDialog
 *   - FailbackPanel (rendered when LastSwitchoverResult = Success)
 *   - SwitchoverHistory (audit-event table from /audit/continuum)
 *
 * Wires data via React Query: GET /continuums/{name} polled every 10s
 * for spec/status, and GET /audit/continuum polled every 15s for
 * history (the SSE stream is wired only in production; for the test
 * + initial-render-fast path the polling fallback is sufficient and
 * keeps the dependency surface minimal — slice F-2 may layer SSE on
 * top via the same handler endpoint).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from
 * `API_BASE` via the continuum.api helpers. Per #5 the underlying
 * handlers enforce tier server-side; the widget uses the caller's
 * Claims tier to render-or-hide entry-points.
 */

import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  getContinuum,
  listContinuumAudit,
  type ContinuumGetResponse,
} from '@/lib/continuum.api'
import { FailbackPanel } from './FailbackPanel'
import { LuaRecordView } from './LuaRecordView'
import { StatusPanel } from './StatusPanel'
import { SwitchoverDialog } from './SwitchoverDialog'
import { SwitchoverHistory } from './SwitchoverHistory'

export interface DRSectionProps {
  /** Sovereign id (deploymentId on chroot, mother). */
  sovereignId: string
  /** Continuum CR name — the AppDetail page derives this from the
   *  Application name (typical convention: `dr-<app>` or matching
   *  Application metadata.name). The parent passes whichever the
   *  Application controller created. */
  continuumName: string
  /** Application name surfaced in headings + audit filtering. */
  applicationName: string
  /** Org namespace. */
  namespace?: string
  /** Caller's tier — controls render of switchover/failback/approve buttons. */
  callerTier?: string
  /**
   * #3375 — the app's effective DR class (active-hot-standby / active-passive
   * / active-active), from the live CR or the declared blueprint topology.
   * Drives the heading + the honest "no live Continuum CR" copy.
   */
  declaredClass?: string
  /**
   * #3375 — the declared switchover mechanism (e.g. bp-continuum,
   * raft-transition) surfaced in the contract line when no live CR exists.
   */
  switchoverMechanism?: string | null
  /**
   * #3375 — whether this variant carries an operator-initiated switchover.
   * false for active-active apps that declare `switchover: none` (both
   * regions serve, no promotion) — the panel then shows the replication
   * posture but no Switchover button. Defaults true for back-compat.
   */
  hasSwitchover?: boolean
  /**
   * #3375 (DR-UI honesty) — whether a REAL live DR pair backs this app on
   * THIS Sovereign: a live Continuum record (a reconciled Continuum CR OR
   * the derived live-cnpg-pair projection) carrying a usable standby region.
   *
   * The Switchover control is ARMED only when this is true. When false — no
   * 2-region pair exists for the app on this Sovereign (single-region prov,
   * or the region-b half isn't up) — the button is DISABLED with an honest
   * reason, NEVER armed against a phantom `dr-<app>` Continuum that 404s on
   * click (the exact defect the hw158 walk caught: an armed Switchover
   * against a non-existent dr-grafana). The parent (TopologyTab) derives
   * this from the same GET /continuums/{name} this panel reads — so the gate
   * keys on live state, never on a build-time constant or the app name.
   *
   * Left `undefined` (the back-compat default) means "the parent did not
   * pass a live-state signal" — the panel then falls back to its own query
   * result (`!crMissing`), so an embedded DRSection rendered with a live
   * `initialContinuum` still arms correctly in isolation.
   */
  drPairLive?: boolean
  /** Test seam — pre-fill the Continuum CR + audit list, skip network. */
  initialContinuum?: ContinuumGetResponse
  /** Test seam — bypass the audit fetch + network calls. */
  disableNetwork?: boolean
}

export function DRSection({
  sovereignId,
  continuumName,
  applicationName,
  namespace,
  callerTier,
  declaredClass,
  switchoverMechanism,
  hasSwitchover = true,
  drPairLive,
  initialContinuum,
  disableNetwork = false,
}: DRSectionProps) {
  const qc = useQueryClient()
  const [showSwitchover, setShowSwitchover] = useState(false)

  const continuumQ = useQuery({
    queryKey: ['continuum', sovereignId, continuumName, namespace],
    queryFn: () => getContinuum(sovereignId, continuumName, { namespace }),
    enabled: !initialContinuum && !disableNetwork && !!sovereignId && !!continuumName,
    refetchInterval: 10_000,
  })

  const auditQ = useQuery({
    queryKey: ['continuum-audit', sovereignId, applicationName],
    queryFn: () => listContinuumAudit(sovereignId, { limit: 100 }),
    enabled: !disableNetwork && !!sovereignId,
    refetchInterval: 15_000,
  })

  const cr: ContinuumGetResponse | undefined = initialContinuum ?? continuumQ.data
  const spec = (cr?.spec ?? {}) as Record<string, unknown>
  const status = (cr?.status ?? {}) as Record<string, unknown>

  const primaryRegion = useMemo(() => {
    const fromStatus = status['primaryRegion'] as string | undefined
    if (fromStatus) return fromStatus
    return (spec['primaryRegion'] as string | undefined) ?? ''
  }, [spec, status])

  const hotStandbys = useMemo(() => {
    const arr = spec['hotStandbyRegions']
    return Array.isArray(arr) ? (arr as string[]) : []
  }, [spec])

  const failoverTarget = useMemo(() => {
    for (const r of hotStandbys) {
      if (r !== primaryRegion) return r
    }
    return ''
  }, [hotStandbys, primaryRegion])

  const failbackSpec = (spec['failback'] as Record<string, unknown> | undefined) ?? {}
  const failbackRequested = Boolean(failbackSpec['requested'] ?? false)
  const failbackApproved = Boolean(failbackSpec['approved'] ?? false)
  const approvalRequired = Boolean(failbackSpec['approvalRequired'] ?? false)

  const lastSwitchover = (status['lastSwitchover'] as Record<string, unknown> | undefined) ?? undefined
  const lastResult = lastSwitchover ? (lastSwitchover['result'] as string | undefined) : undefined

  const tier = (callerTier ?? '').toLowerCase()
  const isOwner = tier === 'owner' || tier === 'admin'
  const isSovereignAdmin = tier === 'admin' || tier === 'owner'

  // #3375 — the live Continuum query resolves to "loaded but not found"
  // (404) for apps that have no Continuum CR yet (bootstrap-kit apps, or a
  // single-region prov). Distinguish that from "still loading" so the panel
  // never spins forever — the matrix asserts a real DR surface, not a
  // permanent "Loading Continuum status…".
  const crLoading = !initialContinuum && !disableNetwork && continuumQ.isLoading
  const crMissing = !cr && !crLoading
  const heading =
    declaredClass === 'active-passive'
      ? 'Disaster Recovery (active-passive)'
      : declaredClass === 'active-active'
        ? 'Disaster Recovery (active-active)'
        : 'Disaster Recovery (active-hot-standby)'

  // #3375 (DR-UI honesty) — the Switchover button is ARMED only when a REAL
  // live DR pair backs this app on this Sovereign. The truth signal:
  //
  //   • the parent (TopologyTab) passed `drPairLive` — the authoritative
  //     live-state flag it derives from the SAME GET /continuums/{name} this
  //     panel reads (a reconciled Continuum CR OR the derived live-cnpg-pair
  //     projection with a usable standby). We honor it directly; OR
  //   • the panel is embedded standalone with a live record of its own
  //     (`!crMissing`) — e.g. a test/fixture passing `initialContinuum`.
  //
  // Critically, the OLD `|| crMissing` arming is GONE: when there is no live
  // pair (single-region prov, or the region-b half isn't up) the button must
  // be DISABLED with an honest reason, never armed against a phantom
  // `dr-<app>` Continuum that 404s on click (the hw158 defect: an armed
  // Switchover against a non-existent dr-grafana). Suppressed entirely when
  // the variant declares no switchover (active-active / none).
  const livePairPresent = drPairLive !== undefined ? drPairLive : !crMissing
  const canSwitchover = hasSwitchover && isOwner && livePairPresent

  return (
    <section
      className="continuum-dr-section mt-6 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-4"
      data-testid="continuum-dr-section"
    >
      <div className="mb-3 flex items-baseline justify-between">
        <h3 className="text-sm font-semibold text-[var(--color-text-strong)]">
          {heading}
        </h3>
        {!hasSwitchover ? (
          <span
            data-testid="continuum-dr-no-switchover"
            className="text-xs text-[var(--color-text-dim)]"
          >
            Both regions serve — no switchover
          </span>
        ) : canSwitchover ? (
          <button
            type="button"
            data-testid="continuum-dr-switchover-btn"
            className="rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90"
            onClick={() => setShowSwitchover(true)}
          >
            Switchover…
          </button>
        ) : !isOwner ? (
          <span
            data-testid="continuum-dr-switchover-disabled"
            className="text-xs text-[var(--color-text-dim)]"
          >
            Owner tier required to switchover
          </span>
        ) : !livePairPresent ? (
          // #3375 (DR-UI honesty) — owner tier, but no live DR pair exists on
          // this Sovereign. Disable with the honest reason instead of arming
          // against a phantom Continuum. A title attribute spells out why so
          // the operator never wonders if the control is "broken".
          <span
            data-testid="continuum-dr-switchover-no-pair"
            className="cursor-not-allowed text-xs text-[var(--color-text-dim)]"
            title={`No live DR pair for ${applicationName} on this Sovereign — switchover activates once the app runs ${
              declaredClass ?? 'active-hot-standby'
            } with a healthy standby in the second region.`}
          >
            No live DR pair — switchover unavailable
          </span>
        ) : (
          <span
            data-testid="continuum-dr-switchover-no-target"
            className="text-xs text-[var(--color-text-dim)]"
          >
            No standby region available
          </span>
        )}
      </div>

      {crLoading ? (
        <p
          data-testid="continuum-dr-loading"
          className="text-xs text-[var(--color-text-dim)]"
        >
          Loading Continuum status…
        </p>
      ) : crMissing ? (
        <div data-testid="continuum-dr-no-cr" className="text-xs text-[var(--color-text-dim)]">
          <p>
            No live Continuum record for{' '}
            <code className="font-mono text-[var(--color-text)]">{applicationName}</code> yet —
            the cross-region DR machinery activates once the app is placed{' '}
            <strong className="text-[var(--color-text)]">{declaredClass ?? 'active-hot-standby'}</strong>{' '}
            on a 2-region Sovereign.
          </p>
          {switchoverMechanism ? (
            <p className="mt-1.5" data-testid="continuum-dr-declared-mechanism">
              Declared switchover mechanism:{' '}
              <code className="font-mono text-[var(--color-text)]">{switchoverMechanism}</code>.
            </p>
          ) : null}
          <div className="mt-4">
            <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
              Switchover history
            </h4>
            <SwitchoverHistory
              events={auditQ.data?.items ?? []}
              applicationName={applicationName}
            />
          </div>
        </div>
      ) : (
        <>
          <StatusPanel
            status={status}
            primaryRegion={primaryRegion}
            hotStandbyRegions={hotStandbys}
          />

          <div className="mt-4">
            <LuaRecordView status={status} />
          </div>

          {lastResult === 'Success' || failbackRequested ? (
            <div className="mt-4">
              <FailbackPanel
                sovereignId={sovereignId}
                continuumName={continuumName}
                namespace={namespace}
                isOwner={isOwner}
                isSovereignAdmin={isSovereignAdmin}
                approvalRequired={approvalRequired}
                failbackRequested={failbackRequested}
                failbackApproved={failbackApproved}
                disableNetwork={disableNetwork}
                onChanged={() => {
                  void qc.invalidateQueries({ queryKey: ['continuum', sovereignId, continuumName] })
                }}
              />
            </div>
          ) : null}

          <div className="mt-4">
            <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
              Switchover history
            </h4>
            <SwitchoverHistory
              events={auditQ.data?.items ?? []}
              applicationName={applicationName}
            />
          </div>
        </>
      )}

      {showSwitchover ? (
        <SwitchoverDialog
          sovereignId={sovereignId}
          continuumName={continuumName}
          namespace={namespace}
          fromRegion={primaryRegion}
          // May be empty when no live CR exists — the catalyst-api switchover
          // handler then resolves the target to the first declared hot-standby
          // region server-side (continuum.go HandleContinuumSwitchoverRequest).
          // The dialog renders a "the standby region" placeholder for the
          // empty case but submits the empty string verbatim.
          toRegion={failoverTarget}
          applicationName={applicationName}
          disableNetwork={disableNetwork}
          onClose={() => setShowSwitchover(false)}
          onConfirmed={() => {
            void qc.invalidateQueries({ queryKey: ['continuum', sovereignId, continuumName] })
            void qc.invalidateQueries({ queryKey: ['continuum-audit', sovereignId] })
          }}
        />
      ) : null}
    </section>
  )
}
