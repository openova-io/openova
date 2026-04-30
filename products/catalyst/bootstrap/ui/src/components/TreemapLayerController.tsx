/**
 * TreemapLayerController — single compact toolbar row driving the
 * Sovereign Dashboard's resource-utilisation treemap.
 *
 * Shape (founder spec, verbatim):
 *   [Size ▾] [Color ▾] [Layer 1 ▾] [Layer 2 ▾] [+] [-]
 *
 * Up to 4 layers. Each layer select excludes dimensions already
 * picked by another layer so the toolbar never lets the operator
 * compose a redundant `application > application` drill path.
 *
 * When `sizeBy` is a capacity metric (cpu/memory/storage limits) the
 * `colorBy` select is auto-locked to `utilization` — the only colour
 * scale that makes sense alongside a capacity area metric (the box
 * size IS the limit; the colour is what fraction of that limit is
 * actually consumed). The disabled state surfaces this to the
 * operator instead of silently changing their selection.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every
 * dimension, label, and metric option is defined ONCE in this file
 * (the controller IS the source of truth for the picker chrome) and
 * the rest of the dashboard imports from `@/lib/treemap.types`.
 *
 * Visual chrome matches the rest of the Sovereign portal — Tailwind
 * utility classes + `var(--color-*)` design tokens, no Mantine. See
 * the rationale paragraph in components/ExecutionLogs.tsx.
 */

import { useMemo } from 'react'
import {
  CAPACITY_SIZE_METRICS,
  lockedColorBy,
  type TreemapColorBy,
  type TreemapDimension,
  type TreemapSizeBy,
} from '@/lib/treemap.types'

const SIZE_OPTIONS: { value: TreemapSizeBy; label: string }[] = [
  { value: 'cpu_limit',     label: 'CPU limit' },
  { value: 'memory_limit',  label: 'Memory limit' },
  { value: 'storage_limit', label: 'Storage limit' },
  { value: 'replica_count', label: 'Replica count' },
]

const COLOR_OPTIONS: { value: TreemapColorBy; label: string }[] = [
  { value: 'utilization', label: 'Utilisation' },
  { value: 'health',      label: 'Health' },
  { value: 'age',         label: 'Age' },
]

const DIMENSION_OPTIONS: { value: TreemapDimension; label: string }[] = [
  { value: 'sovereign',   label: 'Sovereign' },
  { value: 'cluster',     label: 'Cluster' },
  { value: 'family',      label: 'Family' },
  { value: 'namespace',   label: 'Namespace' },
  { value: 'application', label: 'Application' },
]

/** Hard upper bound — recharts treemap legibility falls off a cliff
 *  beyond 4 nesting layers; the founder spec caps at 4. */
export const MAX_LAYERS = 4
/** Lower bound — at least one layer, otherwise there's nothing to render. */
export const MIN_LAYERS = 1

export interface TreemapLayerControllerProps {
  layers: readonly TreemapDimension[]
  setLayers: (next: readonly TreemapDimension[]) => void
  colorBy: TreemapColorBy
  setColorBy: (next: TreemapColorBy) => void
  sizeBy: TreemapSizeBy
  setSizeBy: (next: TreemapSizeBy) => void
  /** Test seam — exposes the count of available dimensions for assertions. */
  'data-testid'?: string
}

export function TreemapLayerController({
  layers,
  setLayers,
  colorBy,
  setColorBy,
  sizeBy,
  setSizeBy,
  'data-testid': testid = 'treemap-layer-controller',
}: TreemapLayerControllerProps) {
  // Compute the auto-lock state. When a capacity metric is selected
  // the colour scale is forced to utilisation; the select disables.
  const lockedColor = lockedColorBy(sizeBy)
  const colorIsLocked = lockedColor !== null && colorBy === lockedColor
  const colorIsCapacityCoupled = CAPACITY_SIZE_METRICS.has(sizeBy)

  /** A dimension is taken if any *other* layer already picked it. The
   *  current layer's own value is always available so its <option>
   *  remains the selected one in the DOM. */
  function dimensionsForLayer(idx: number): TreemapDimension[] {
    const taken = new Set<TreemapDimension>()
    layers.forEach((d, i) => {
      if (i !== idx) taken.add(d)
    })
    return DIMENSION_OPTIONS.filter((d) => !taken.has(d.value)).map((d) => d.value)
  }

  function setLayer(idx: number, next: TreemapDimension) {
    const out = [...layers]
    out[idx] = next
    setLayers(out)
  }

  function addLayer() {
    if (layers.length >= MAX_LAYERS) return
    // Pick the first dimension not already in use as the default for the
    // new layer. Falls back to 'application' if (somehow) every dimension
    // is taken — guard rail for future dimension additions.
    const taken = new Set<TreemapDimension>(layers)
    const next =
      DIMENSION_OPTIONS.map((d) => d.value).find((d) => !taken.has(d)) ?? 'application'
    setLayers([...layers, next])
  }

  function removeLayer() {
    if (layers.length <= MIN_LAYERS) return
    setLayers(layers.slice(0, -1))
  }

  /** When sizeBy flips into a capacity metric, force colorBy to the
   *  matching utilisation. Done in the change handler so it's a single
   *  user-initiated state transition, never a render-time fight with
   *  React's render-pure rule. */
  function onSizeByChange(next: TreemapSizeBy) {
    setSizeBy(next)
    const lock = lockedColorBy(next)
    if (lock && colorBy !== lock) {
      setColorBy(lock)
    }
  }

  // Only render selects for layers that exist; the +/- buttons gate
  // adding/removing so the JSX list itself can stay simple.
  const visibleLayers = useMemo(() => layers.slice(0, MAX_LAYERS), [layers])

  return (
    <div
      data-testid={testid}
      className="flex flex-wrap items-center gap-2 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
      role="toolbar"
      aria-label="Treemap controls"
    >
      {/* Size by — area metric */}
      <CompactSelect
        label="Size"
        value={sizeBy}
        options={SIZE_OPTIONS}
        onChange={(v) => onSizeByChange(v as TreemapSizeBy)}
        testid="treemap-size-select"
      />

      {/* Color by — gradient meaning */}
      <CompactSelect
        label="Color"
        value={colorBy}
        options={COLOR_OPTIONS}
        onChange={(v) => setColorBy(v as TreemapColorBy)}
        disabled={colorIsLocked && colorIsCapacityCoupled}
        title={
          colorIsCapacityCoupled
            ? 'Locked: capacity area metrics pair with utilisation'
            : undefined
        }
        testid="treemap-color-select"
      />

      <span className="mx-1 h-6 w-px bg-[var(--color-border)]" aria-hidden />

      {visibleLayers.map((layer, idx) => {
        const allowed = dimensionsForLayer(idx)
        return (
          <CompactSelect
            key={`layer-${idx}`}
            label={`Layer ${idx + 1}`}
            value={layer}
            options={DIMENSION_OPTIONS.filter((d) => allowed.includes(d.value))}
            onChange={(v) => setLayer(idx, v as TreemapDimension)}
            testid={`treemap-layer-${idx}-select`}
          />
        )
      })}

      <span className="mx-1 h-6 w-px bg-[var(--color-border)]" aria-hidden />

      <ActionIconButton
        label="Add layer"
        symbol="+"
        onClick={addLayer}
        disabled={layers.length >= MAX_LAYERS}
        testid="treemap-add-layer"
      />
      <ActionIconButton
        label="Remove layer"
        symbol="−"
        onClick={removeLayer}
        disabled={layers.length <= MIN_LAYERS}
        testid="treemap-remove-layer"
      />
    </div>
  )
}

interface CompactSelectProps<T extends string> {
  label: string
  value: T
  options: { value: T; label: string }[]
  onChange: (next: string) => void
  disabled?: boolean
  title?: string
  testid: string
}

function CompactSelect<T extends string>({
  label,
  value,
  options,
  onChange,
  disabled = false,
  title,
  testid,
}: CompactSelectProps<T>) {
  return (
    <label
      className={`flex items-center gap-1.5 text-xs ${
        disabled ? 'opacity-60' : ''
      }`}
      title={title}
    >
      <span className="font-medium uppercase tracking-wide text-[var(--color-text-dim)]">
        {label}
      </span>
      <select
        data-testid={testid}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs text-[var(--color-text-strong)] focus:border-[var(--color-accent)] focus:outline-none disabled:cursor-not-allowed"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  )
}

interface ActionIconButtonProps {
  label: string
  symbol: string
  onClick: () => void
  disabled?: boolean
  testid: string
}

function ActionIconButton({
  label,
  symbol,
  onClick,
  disabled = false,
  testid,
}: ActionIconButtonProps) {
  return (
    <button
      type="button"
      data-testid={testid}
      aria-label={label}
      title={label}
      onClick={onClick}
      disabled={disabled}
      className="inline-flex h-7 w-7 items-center justify-center rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] text-sm font-bold text-[var(--color-text-strong)] transition-colors hover:bg-[var(--color-surface-hover)] disabled:cursor-not-allowed disabled:opacity-40"
    >
      {symbol}
    </button>
  )
}
