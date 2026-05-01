/**
 * CloudKindChips — compact horizontal chip strip rendered in the
 * CloudPage toolbar in list view (issue #366 item 1).
 *
 * Layout contract:
 *   • Top 6 primary chips render inline (Clusters, vClusters, Node
 *     Pools, PVCs, Load Balancers, Buckets — order from kinds.ts).
 *   • Trailing `+ More` button opens a Popover with the remaining
 *     overflow chips (Worker Nodes, Services, Ingresses, DNS Zones,
 *     Volumes, Storage Classes).
 *   • Each chip ≈ 28px tall: small Tabler-style icon + label + count
 *     badge. Active chip uses the accent colour + stroke.
 *   • Click → fires `onChange(kind)`. Tab between chips, Enter to
 *     activate (native button behaviour).
 *   • Replaces the legacy 12-tile card grid that pushed the list
 *     table below the fold.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every
 * label/icon flows through the `KINDS` catalogue in kinds.ts; this
 * component is a pure renderer.
 */

import { useEffect, useRef, useState } from 'react'
import {
  KINDS,
  type CloudKindEntry,
  type CloudListKind,
} from './kinds'

interface CloudKindChipsProps {
  /** Currently active kind. */
  activeKind: CloudListKind
  /** Per-kind count map. `null` means data unavailable (renders "—"). */
  counts: Record<CloudListKind, number | null>
  /** Switch to a different kind. */
  onChange: (next: CloudListKind) => void
}

export function CloudKindChips({ activeKind, counts, onChange }: CloudKindChipsProps) {
  const primary = KINDS.filter((k) => k.primary)
  const overflow = KINDS.filter((k) => !k.primary)
  const activeInOverflow = overflow.some((k) => k.id === activeKind)

  return (
    <div
      className="cloud-kind-chips"
      data-testid="cloud-kind-chips"
      role="tablist"
      aria-label="Resource kind"
    >
      <style>{CLOUD_KIND_CHIPS_CSS}</style>
      {primary.map((k) => (
        <KindChip
          key={k.id}
          kind={k}
          active={k.id === activeKind}
          count={counts[k.id]}
          onClick={() => onChange(k.id)}
        />
      ))}
      <MoreChipPopover
        items={overflow}
        counts={counts}
        activeKind={activeKind}
        activeInGroup={activeInOverflow}
        onChange={onChange}
      />
    </div>
  )
}

/* ── Single chip ────────────────────────────────────────────────── */

interface KindChipProps {
  kind: CloudKindEntry
  active: boolean
  count: number | null
  onClick: () => void
}

function KindChip({ kind, active, count, onClick }: KindChipProps) {
  const showCount = kind.hasData && count !== null
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      aria-pressed={active}
      data-testid={`cloud-kind-chip-${kind.id}`}
      data-kind={kind.id}
      data-active={active ? 'true' : 'false'}
      data-category={kind.category}
      className={`cloud-kind-chip${active ? ' cloud-kind-chip-active' : ''}`}
      onClick={onClick}
    >
      <span className="cloud-kind-chip-icon" aria-hidden>
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth={1.6}
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d={kind.icon} />
        </svg>
      </span>
      <span className="cloud-kind-chip-label">{kind.label}</span>
      <span
        className="cloud-kind-chip-count"
        data-testid={`cloud-kind-chip-${kind.id}-count`}
      >
        {showCount ? count : '—'}
      </span>
    </button>
  )
}

/* ── More-chip overflow popover ─────────────────────────────────── */

interface MoreChipPopoverProps {
  items: CloudKindEntry[]
  counts: Record<CloudListKind, number | null>
  activeKind: CloudListKind
  activeInGroup: boolean
  onChange: (next: CloudListKind) => void
}

function MoreChipPopover({
  items,
  counts,
  activeKind,
  activeInGroup,
  onChange,
}: MoreChipPopoverProps) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement | null>(null)

  // Click-outside dismissal — same pattern as AddChipPopover in the
  // architecture-graph widget.
  useEffect(() => {
    if (!open) return
    function onDoc(ev: MouseEvent) {
      const t = ev.target as HTMLElement | null
      if (!ref.current?.contains(t)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  return (
    <div className="cloud-kind-chip-more-wrap" ref={ref}>
      <button
        type="button"
        data-testid="cloud-kind-chip-more"
        data-active={activeInGroup ? 'true' : 'false'}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={`cloud-kind-chip cloud-kind-chip-more${
          activeInGroup ? ' cloud-kind-chip-active' : ''
        }`}
      >
        <span aria-hidden>+</span>
        <span className="cloud-kind-chip-label">More</span>
      </button>
      {open && (
        <div
          data-testid="cloud-kind-chip-more-popover"
          role="menu"
          className="cloud-kind-chip-more-pop"
        >
          {items.map((k) => {
            const c = counts[k.id]
            const showCount = k.hasData && c !== null
            const active = k.id === activeKind
            return (
              <button
                key={k.id}
                type="button"
                role="menuitemradio"
                aria-checked={active}
                data-testid={`cloud-kind-chip-more-item-${k.id}`}
                onClick={() => {
                  onChange(k.id)
                  setOpen(false)
                }}
                className={`cloud-kind-chip-more-item${
                  active ? ' cloud-kind-chip-more-item-active' : ''
                }`}
              >
                <span className="cloud-kind-chip-icon" aria-hidden>
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth={1.6}
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  >
                    <path d={k.icon} />
                  </svg>
                </span>
                <span className="cloud-kind-chip-more-item-label">{k.label}</span>
                <span className="cloud-kind-chip-count">{showCount ? c : '—'}</span>
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

/* ── Local CSS ──────────────────────────────────────────────────── */

const CLOUD_KIND_CHIPS_CSS = `
.cloud-kind-chips {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  flex-wrap: wrap;
}
.cloud-kind-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  height: 28px;
  padding: 0 0.6rem;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: var(--color-bg-2);
  color: var(--color-text-dim);
  font-size: 0.78rem;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease, border-color 0.12s ease;
  white-space: nowrap;
}
.cloud-kind-chip:hover {
  color: var(--color-text);
  border-color: color-mix(in srgb, var(--color-accent) 50%, var(--color-border));
}
.cloud-kind-chip:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.cloud-kind-chip-active {
  background: color-mix(in srgb, var(--color-accent) 14%, var(--color-bg-2));
  border-color: var(--color-accent);
  color: var(--color-accent);
  font-weight: 600;
}
.cloud-kind-chip-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
}
.cloud-kind-chip-icon svg {
  width: 16px;
  height: 16px;
}
.cloud-kind-chip[data-category="compute"] .cloud-kind-chip-icon { color: #60a5fa; }
.cloud-kind-chip[data-category="network"] .cloud-kind-chip-icon { color: #34d399; }
.cloud-kind-chip[data-category="storage"] .cloud-kind-chip-icon { color: #f59e0b; }
.cloud-kind-chip-active .cloud-kind-chip-icon {
  /* When active the chip foreground is the accent — inherit it on the
     icon too so the active state reads as a single colour. */
  color: var(--color-accent);
}
.cloud-kind-chip-label { line-height: 1; }
.cloud-kind-chip-count {
  font-size: 0.68rem;
  font-weight: 600;
  padding: 0.04rem 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}
.cloud-kind-chip-active .cloud-kind-chip-count {
  background: color-mix(in srgb, var(--color-accent) 22%, transparent);
  color: var(--color-accent);
}

.cloud-kind-chip-more-wrap { position: relative; }
.cloud-kind-chip-more { color: var(--color-text); }
.cloud-kind-chip-more-pop {
  position: absolute;
  right: 0;
  top: calc(100% + 6px);
  z-index: 30;
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
  min-width: 13rem;
  padding: 0.35rem;
  border-radius: 10px;
  border: 1px solid var(--color-border);
  background: var(--color-bg-2);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
}
.cloud-kind-chip-more-item {
  display: inline-flex;
  align-items: center;
  gap: 0.55rem;
  padding: 0.4rem 0.55rem;
  border: 0;
  background: transparent;
  border-radius: 6px;
  color: var(--color-text);
  font-size: 0.82rem;
  cursor: pointer;
  text-align: left;
  width: 100%;
}
.cloud-kind-chip-more-item:hover {
  background: var(--color-surface-hover);
}
.cloud-kind-chip-more-item-active {
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  font-weight: 600;
}
.cloud-kind-chip-more-item-label { flex: 1; }
`
