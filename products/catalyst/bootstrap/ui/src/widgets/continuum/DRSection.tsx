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

  return (
    <section
      className="continuum-dr-section mt-6 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-4"
      data-testid="continuum-dr-section"
    >
      <div className="mb-3 flex items-baseline justify-between">
        <h3 className="text-sm font-semibold text-[var(--color-text-strong)]">
          Disaster Recovery (active-hotstandby)
        </h3>
        {isOwner && failoverTarget ? (
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
        ) : (
          <span
            data-testid="continuum-dr-switchover-no-target"
            className="text-xs text-[var(--color-text-dim)]"
          >
            No standby region available
          </span>
        )}
      </div>

      {!cr ? (
        <p
          data-testid="continuum-dr-loading"
          className="text-xs text-[var(--color-text-dim)]"
        >
          Loading Continuum status…
        </p>
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

      {showSwitchover && failoverTarget ? (
        <SwitchoverDialog
          sovereignId={sovereignId}
          continuumName={continuumName}
          namespace={namespace}
          fromRegion={primaryRegion}
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
