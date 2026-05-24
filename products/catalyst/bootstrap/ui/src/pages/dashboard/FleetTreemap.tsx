/**
 * FleetTreemap — mothership-side, fleet-wide single-layer treemap
 * (TBD-E14).
 *
 *   /dashboard/treemap
 *
 * One cell per Sovereign across the entire fleet known to this
 * catalyst-api. Cell AREA defaults to `apps` (number of Applications
 * installed; floored to 1 so every Sovereign is at least visible),
 * cell COLOR defaults to `health` (green/yellow/red/unknown vocabulary
 * mapped via the existing TreemapColorBy='health' gradient).
 *
 * The wire shape (FleetTreemapResponse in fleet.api.ts) is identical
 * to the per-Sovereign /dashboard/treemap response, so the existing
 * squarified renderer + colour pipeline consume both endpoints with
 * zero new component code. Clicking a cell deep-links to that
 * Sovereign's chroot console, where the existing /dashboard treemap
 * already shows the full Region → Cluster → … → Application
 * hierarchy.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state) — page ships day-one. Empty fleet renders an
 *      explicit "no Sovereigns provisioned yet" CTA pointing at the
 *      wizard; this never blanks even before the backend is wired.
 *   #2 (no fixtures) — all data flows from /api/v1/fleet/treemap.
 *   #4 (never hardcode) — size/colour tokens flow from fleet.api.ts.
 *
 * ── Why a SKELETON shipped today (TBD-E14) ──────────────────────────
 *
 * The backend handler at /api/v1/fleet/treemap is intentionally
 * narrow today (see handler/fleet_treemap.go):
 *   • size_value floor = 1 (apps mode) until the per-Sov app count is
 *     wired through fleetSovereignSummary.
 *   • single layer only — Sovereign → Cluster → Application deep
 *     drill requires either per-Sov fan-out OR a rollup controller;
 *     those land in a follow-up slice.
 *
 * The page is reachable and renders meaningfully TODAY: the operator
 * sees one cell per Sovereign coloured by health, with a click-through
 * to the per-Sov chroot console where the existing treemap takes over.
 * That's the "user-visible value on day-one" target-state ship.
 */

import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowLeft, Plus, RefreshCw, Layers } from 'lucide-react'
import { Link } from '@tanstack/react-router'

import { Button } from '@/shared/ui/button'
import { Card, CardContent } from '@/shared/ui/card'
import { useFleet, useFleetTreemap } from '@/lib/useFleet'
import { sovereignChrootURL, type SovereignSummary } from '@/lib/fleet.api'
import {
  colorFunctionFor,
  type TreemapColorBy,
  type TreemapItem,
} from '@/lib/treemap.types'
import { computeSquarifiedLayout, type SquarifiedRect } from '@/lib/treemap-squarified'

const SURFACE_HEIGHT_PX = 480

/**
 * Map our fleet color_by token onto the per-Sov TreemapColorBy enum
 * the existing palette helpers consume. `health` and `age` map 1:1.
 */
function mapColorByToPerSov(colorBy: 'health' | 'age'): TreemapColorBy {
  return colorBy
}

export function FleetTreemap() {
  // Today: size=apps, color=health — locked into defaults because the
  // backend skeleton only honours those. The selectors render disabled
  // with a "Coming soon" tooltip so the surface signals what's
  // wired vs deferred.
  const sizeBy: 'apps' | 'age' = 'apps'
  const colorBy: 'health' | 'age' = 'health'

  const treemap = useFleetTreemap({ sizeBy, colorBy })
  // Side-fetch the Sovereign list so a click can resolve back to the
  // per-Sov chroot URL (the treemap items only carry id + name).
  const fleet = useFleet({ pageSize: 100 })

  const sovById = useMemo(() => {
    const map = new Map<string, SovereignSummary>()
    for (const s of fleet.data?.sovereigns ?? []) map.set(s.id, s)
    return map
  }, [fleet.data?.sovereigns])

  const items = useMemo<TreemapItem[]>(() => {
    const src = treemap.data?.items ?? []
    return src.map((it) => ({
      id: it.id,
      name: it.name,
      count: it.count,
      percentage: it.percentage,
      size_value: it.size_value,
      // Single-layer for now; treemap-squarified.ts handles
      // `children: undefined` as a flat row.
      children: undefined,
    }))
  }, [treemap.data?.items])

  const total = items.length
  const totalApps = items.reduce((sum, it) => sum + it.count, 0)
  const healthy = items.filter((it) => (it.percentage ?? -1) === 100).length
  const failed = items.filter((it) => (it.percentage ?? -1) === 0).length

  return (
    <div
      className="flex flex-col gap-6 p-8 max-w-7xl mx-auto"
      data-testid="fleet-treemap-page"
    >
      {/* Header */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3 }}
        className="flex items-start justify-between gap-4 flex-wrap"
      >
        <div>
          <Link
            to="/dashboard"
            className="text-xs text-[var(--color-text-dim)] hover:underline flex items-center gap-1"
          >
            <ArrowLeft className="h-3 w-3" />
            Back to Sovereign Fleet
          </Link>
          <h1 className="text-xl font-semibold text-[var(--color-text-strong)] mt-1">
            Fleet Treemap
          </h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            One cell per Sovereign across the fleet — area scaled by
            installed Applications, colour mapped to health. Click a
            cell to drill into that Sovereign&apos;s per-Region treemap.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Link to={'/dashboard/applications' as never}>
            <Button variant="secondary" size="md">
              <Layers className="h-4 w-4" />
              Applications view
            </Button>
          </Link>
          <Link to="/wizard">
            <Button size="md">
              <Plus className="h-4 w-4" />
              New Sovereign
            </Button>
          </Link>
        </div>
      </motion.div>

      {/* Stats */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3, delay: 0.05 }}
        className="grid grid-cols-2 sm:grid-cols-4 gap-4"
      >
        <StatCard label="Sovereigns" value={total} />
        <StatCard label="Healthy" value={healthy} sub={`of ${total}`} />
        <StatCard label="Failed" value={failed} sub="needs attention" />
        <StatCard label="Total apps" value={totalApps} />
      </motion.div>

      {/* Controls — disabled placeholders until backend fans out */}
      <div className="flex flex-wrap items-center gap-4 text-xs text-[oklch(55%_0.01_250)]">
        <span className="rounded-md border border-[oklch(28%_0.02_250)] px-2 py-1">
          Size: <strong className="text-[var(--color-text)]">apps</strong>
        </span>
        <span className="rounded-md border border-[oklch(28%_0.02_250)] px-2 py-1">
          Colour: <strong className="text-[var(--color-text)]">health</strong>
        </span>
        <span className="text-[var(--color-text-dimmer)]">
          (size_by=memory_aggregate, layer=cluster/app — coming soon, see TBD-E14)
        </span>
      </div>

      {/* Treemap surface — skeleton-friendly: always renders the wrapper
          even when items=[] so visual layout is stable across states. */}
      <Card>
        <CardContent className="pt-5">
          {treemap.isLoading ? (
            <p className="text-sm text-[var(--color-text-dim)]" data-testid="fleet-treemap-loading">
              Loading fleet…
            </p>
          ) : treemap.error ? (
            <div className="flex items-center justify-between" data-testid="fleet-treemap-error">
              <p className="text-sm text-rose-400">
                Failed to load fleet treemap — {String(treemap.error)}
              </p>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => treemap.refetch()}
              >
                <RefreshCw className="h-3 w-3" />
                Retry
              </Button>
            </div>
          ) : items.length === 0 ? (
            <div className="text-center py-12" data-testid="fleet-treemap-empty">
              <p className="text-sm text-[var(--color-text-dim)]">
                No Sovereigns provisioned yet.
              </p>
              <Link to="/wizard">
                <Button className="mt-4">
                  <Plus className="h-4 w-4" />
                  Provision the first Sovereign
                </Button>
              </Link>
            </div>
          ) : (
            <FleetTreemapSurface
              items={items}
              colorBy={mapColorByToPerSov(colorBy)}
              sovById={sovById}
            />
          )}
        </CardContent>
      </Card>

      {/* Legend mirrors the per-Sov dashboard so colour semantics are
          consistent across both treemap surfaces. */}
      <div className="flex items-center gap-4 text-xs text-[oklch(55%_0.01_250)]">
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-3 w-3 rounded-sm bg-emerald-500" />
          Healthy
        </span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-3 w-3 rounded-sm bg-amber-500" />
          Yellow
        </span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-3 w-3 rounded-sm bg-rose-500" />
          Failed
        </span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-3 w-3 rounded-sm bg-slate-500" />
          Unknown
        </span>
      </div>
    </div>
  )
}

function StatCard({
  label,
  value,
  sub,
}: {
  label: string
  value: string | number
  sub?: string
}) {
  return (
    <Card>
      <CardContent className="pt-5">
        <p className="text-xs text-[var(--color-text-dimmer)] uppercase tracking-wider font-medium">
          {label}
        </p>
        <p className="mt-1 text-2xl font-bold text-[var(--color-text-strong)] tabular-nums">
          {value}
        </p>
        {sub && <p className="mt-0.5 text-xs text-[var(--color-text-dimmer)]">{sub}</p>}
      </CardContent>
    </Card>
  )
}

interface FleetTreemapSurfaceProps {
  items: readonly TreemapItem[]
  colorBy: TreemapColorBy
  sovById: Map<string, SovereignSummary>
}

function FleetTreemapSurface({ items, colorBy, sovById }: FleetTreemapSurfaceProps) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(0)

  useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return
    function measure() {
      if (!el) return
      setWidth(el.clientWidth)
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const rects = useMemo<SquarifiedRect[]>(() => {
    if (width <= 0) return []
    return computeSquarifiedLayout(items as TreemapItem[], width, SURFACE_HEIGHT_PX)
  }, [items, width])

  const colorFn = colorFunctionFor(colorBy)

  function onCellClick(item: TreemapItem) {
    if (!item.id) return
    const sov = sovById.get(item.id)
    if (!sov) return
    // sovereignChrootURL with no opts returns
    // `https://console.<fqdn>/dashboard` — exactly the per-Sov treemap.
    const url = sovereignChrootURL(sov)
    if (url) window.open(url, '_blank', 'noopener,noreferrer')
  }

  return (
    <div
      ref={containerRef}
      data-testid="fleet-treemap-surface"
      style={{ width: '100%', height: SURFACE_HEIGHT_PX }}
    >
      {width > 0 && (
        <svg
          width={width}
          height={SURFACE_HEIGHT_PX}
          role="img"
          aria-label="Fleet treemap"
          style={{ display: 'block' }}
        >
          {rects.map((r) => {
            // SquarifiedRect exposes (x0,y0,x1,y1) — derive (x,y,w,h)
            // for SVG `<rect>` + `<text>` consumption. Keeping the
            // local aliases at the top of the cell avoids littering
            // the JSX with `r.x1 - r.x0` arithmetic.
            const rx = r.x0
            const ry = r.y0
            const rw = r.x1 - r.x0
            const rh = r.y1 - r.y0
            const pct = r.item.percentage
            const fill = pct == null ? '#475569' /* slate-600 */ : colorFn(pct)
            return (
              <g
                key={r.item.id ?? r.item.name}
                style={{ cursor: r.item.id ? 'pointer' : 'default' }}
                onClick={() => onCellClick(r.item)}
              >
                <rect
                  x={rx}
                  y={ry}
                  width={rw}
                  height={rh}
                  fill={fill}
                  stroke="#0f172a"
                  strokeWidth={1.5}
                />
                {rw > 60 && rh > 24 && (
                  <text
                    x={rx + 8}
                    y={ry + 18}
                    fill="#0f172a"
                    fontSize={12}
                    fontWeight={600}
                  >
                    {r.item.name}
                  </text>
                )}
                {rw > 60 && rh > 44 && (
                  <text
                    x={rx + 8}
                    y={ry + 36}
                    fill="#0f172a"
                    fontSize={10}
                  >
                    {r.item.count} app{r.item.count === 1 ? '' : 's'}
                    {pct != null ? ` · ${Math.round(pct)}%` : ''}
                  </text>
                )}
              </g>
            )
          })}
        </svg>
      )}
    </div>
  )
}
