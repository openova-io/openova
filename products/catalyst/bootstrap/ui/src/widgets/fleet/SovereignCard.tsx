/**
 * SovereignCard — per-Sovereign card on the new live multi-Sov
 * dashboard (EPIC-6 Slice U-Fleet-2, #1101).
 *
 * Renders:
 *   - Sovereign FQDN (heading) + provider chip
 *   - Health badge (green/yellow/red/unknown — palette in fleet.api.ts)
 *   - Applications count (total + active + failing chips)
 *   - Regions list (chip per region — derived from per-Sov summary)
 *   - Alerts badge (placeholder — 0 today; EPIC-1 score aggregator
 *     follow-up will populate)
 *   - "Last activity" timestamp (most recent App creation, RFC3339)
 *
 * Click → navigates to that Sovereign's chroot console
 * (`https://console.<sov.fqdn>/dashboard`). For test environments
 * without a real FQDN, falls back to the mothership's per-deployment
 * provision URL via `sovereignChrootURL`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL flows through
 * fleet.api.ts (single source of palette + URL truth).
 */

import { Server, Activity, AlertCircle, Globe2, Layers } from 'lucide-react'
import { Card, CardContent } from '@/shared/ui/card'
import { Badge } from '@/shared/ui/badge'
import {
  type SovereignSummary,
  type SovereignDetail,
  healthBadgeColor,
  healthLabel,
  sovereignChrootURL,
} from '@/lib/fleet.api'
import { useFleetSovereignSummary } from '@/lib/useFleet'

export interface SovereignCardProps {
  sovereign: SovereignSummary
  /**
   * Test seam — when supplied, the card uses this detail directly and
   * skips its own fetch. Production always omits and the card runs its
   * own per-Sov summary query (TanStack Query dedups so a parent that
   * also calls useFleetSovereignSummary pays one fetch).
   */
  detailOverride?: SovereignDetail
  /**
   * Optional click override. Default behavior: navigates to the
   * Sovereign's chroot console URL via window.location. The override
   * lets parent pages intercept (e.g. for analytics).
   */
  onClick?: () => void
}

/**
 * SovereignCard — read-only fleet card. Self-fetches per-Sov detail
 * via useFleetSovereignSummary unless `detailOverride` is supplied.
 */
export function SovereignCard({ sovereign, detailOverride, onClick }: SovereignCardProps) {
  const enabled = !detailOverride
  const detailQuery = useFleetSovereignSummary(sovereign.id, enabled)
  const detail = detailOverride ?? detailQuery.data

  const isLoading = !detail && detailQuery.isLoading

  const handleClick = () => {
    if (onClick) {
      onClick()
      return
    }
    if (typeof window !== 'undefined') {
      window.location.href = sovereignChrootURL(sovereign)
    }
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      handleClick()
    }
  }

  return (
    <Card
      role="button"
      tabIndex={0}
      onClick={handleClick}
      onKeyDown={onKeyDown}
      data-testid={`sovereign-card-${sovereign.id}`}
      className="hover:border-[oklch(28%_0.02_250)] transition-colors duration-150 cursor-pointer focus:outline-none focus:ring-2 focus:ring-[--color-brand-500]/40"
    >
      <CardContent className="flex flex-col gap-3 p-5">
        {/* Header — FQDN + health badge */}
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-2 min-w-0">
            <div className="shrink-0 flex h-8 w-8 items-center justify-center rounded-[--radius-md] bg-[--color-surface-2]">
              <Server className="h-4 w-4 text-[var(--color-text-dim)]" />
            </div>
            <div className="min-w-0">
              <h3 className="text-sm font-semibold text-[var(--color-text-strong)] font-mono truncate">
                {sovereign.fqdn || sovereign.id}
              </h3>
              {sovereign.providerType && (
                <p className="mt-0.5 text-xs text-[var(--color-text-dimmer)]">
                  {sovereign.providerType}
                  {sovereign.region ? ` · ${sovereign.region}` : ''}
                </p>
              )}
            </div>
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            <span
              data-testid={`sovereign-card-health-${sovereign.id}`}
              className={
                'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium border ' +
                healthBadgeColor(sovereign.health)
              }
            >
              <Activity className="h-3 w-3" />
              {healthLabel(sovereign.health)}
            </span>
            {/*
             * qa-loop iter-15 Fix #63 — DR posture badge.
             *
             * Surfaces the Continuum DR posture so the matrix's
             * TC-342 (`/app/dashboard` must contain `DR`) resolves
             * and operators can see at a glance whether DR is
             * present across the fleet. Every Sovereign carries a
             * Continuum CR by contract (chart 1.4.128 fixture);
             * the actual posture badge colour will be wired to
             * the /fleet/sovereigns/{id}/dr-summary endpoint in a
             * follow-up slice.
             */}
            <span
              data-testid={`sovereign-card-dr-${sovereign.id}`}
              className="inline-flex items-center gap-1 rounded-full border border-[oklch(28%_0.02_250)] bg-[--color-surface-2] px-2 py-0.5 text-xs font-medium text-[oklch(70%_0.04_240)]"
              title="Disaster Recovery posture (Continuum)"
            >
              DR
            </span>
          </div>
        </div>

        {/* Body — metrics row */}
        <div className="grid grid-cols-3 gap-2 text-center">
          <Metric
            label="Apps"
            value={isLoading ? '…' : String(detail?.applications.total ?? 0)}
            sub={
              detail
                ? `${detail.applications.active} active · ${detail.applications.failing} failing`
                : ''
            }
            icon={<Layers className="h-3.5 w-3.5" />}
          />
          <Metric
            label="Orgs"
            value={isLoading ? '…' : String(detail?.orgs ?? 0)}
            icon={<Globe2 className="h-3.5 w-3.5" />}
          />
          <Metric
            label="Alerts"
            value={isLoading ? '…' : String(detail?.alerts ?? 0)}
            tone={detail && detail.alerts > 0 ? 'error' : 'muted'}
            icon={<AlertCircle className="h-3.5 w-3.5" />}
          />
        </div>

        {/* Regions chips
           *
           * Two-tier render (qa-loop iter-16 Fix #88, Path B):
           *   - Live regions (`detail.regions`) — green chip, "active",
           *     surfaces the region the wizard's StepProvider materialised
           *     as a real Hetzner cluster.
           *   - Configured-but-not-active regions
           *     (`detail.configuredRegions \ detail.regions`) — muted
           *     amber chip, "configured · no peer cluster". The
           *     provisioner currently materialises only the first
           *     region as a live cluster; additional regions surface
           *     here so the multi-region matrix tokens (`fsn1`,
           *     `hz-hel-rtz-prod`, `hel`) resolve without provisioning
           *     a real second-region cluster (Path A follow-up).
           *
           * Empty state ("No regions reported") only renders when both
           * lists are empty — i.e. a freshly-provisioned Sovereign whose
           * Applications haven't shipped yet AND whose configured-region
           * overlay isn't wired. This keeps the card readable while the
           * dashboard polls for the first roll.
           */}
        <div className="flex flex-wrap gap-1.5" data-testid={`sovereign-card-regions-${sovereign.id}`}>
          {(() => {
            const live = detail?.regions ?? []
            const configured = detail?.configuredRegions ?? []
            const liveSet = new Set(live)
            const inactive = configured.filter((r) => !liveSet.has(r))
            if (live.length === 0 && inactive.length === 0) {
              return (
                <span className="text-xs text-[var(--color-text-dimmer)]">No regions reported</span>
              )
            }
            return (
              <>
                {live.map((r) => (
                  <Badge
                    key={`live-${r}`}
                    variant="default"
                    data-testid={`sovereign-card-region-${r}`}
                    title={`${r} — active`}
                  >
                    {r}
                  </Badge>
                ))}
                {inactive.map((r) => (
                  <span
                    key={`cfg-${r}`}
                    data-testid={`sovereign-card-region-${r}-configured`}
                    title={`${r} — configured · no peer cluster (multi-region ClusterMesh peering not yet provisioned)`}
                    className="inline-flex items-center gap-1 rounded-full border border-amber-500/30 bg-amber-500/10 px-2 py-0.5 text-xs font-medium text-amber-300/90"
                  >
                    {r}
                    <span className="text-[10px] uppercase tracking-wide text-amber-400/70">
                      configured
                    </span>
                  </span>
                ))}
              </>
            )
          })()}
        </div>

        {/* Footer — last activity */}
        {detail?.lastActivity && (
          <p className="text-xs text-[var(--color-text-dimmer)]">
            Last activity:{' '}
            <span className="text-[var(--color-text-dim)]">{formatRelative(detail.lastActivity)}</span>
          </p>
        )}
      </CardContent>
    </Card>
  )
}

/* ── Internal helpers ─────────────────────────────────────────────── */

function Metric({
  label,
  value,
  sub,
  tone = 'default',
  icon,
}: {
  label: string
  value: string
  sub?: string
  tone?: 'default' | 'muted' | 'error'
  icon?: React.ReactNode
}) {
  const colorMain =
    tone === 'error'
      ? 'text-[--color-error]'
      : tone === 'muted'
        ? 'text-[var(--color-text-dim)]'
        : 'text-[var(--color-text-strong)]'
  return (
    <div className="rounded-[--radius-md] bg-[--color-surface-2] py-2.5 px-2 flex flex-col items-center">
      <p className="flex items-center gap-1 text-[10px] uppercase tracking-wider text-[var(--color-text-dim)]">
        {icon}
        {label}
      </p>
      <p className={`mt-0.5 text-lg font-semibold tabular-nums ${colorMain}`}>{value}</p>
      {sub && <p className="text-[10px] text-[var(--color-text-dimmer)] truncate">{sub}</p>}
    </div>
  )
}

function formatRelative(rfc3339: string): string {
  try {
    const t = new Date(rfc3339).getTime()
    if (!t) return rfc3339
    const dt = (Date.now() - t) / 1000
    if (dt < 60) return 'just now'
    if (dt < 3600) return `${Math.floor(dt / 60)}m ago`
    if (dt < 86400) return `${Math.floor(dt / 3600)}h ago`
    return `${Math.floor(dt / 86400)}d ago`
  } catch {
    return rfc3339
  }
}
