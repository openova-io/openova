/**
 * JobKindChips — compact horizontal chip strip rendered in the JobsPage
 * toolbar in list view (P1b, Refs #6703). A pixel-for-pixel clone of
 * CloudKindChips: the /jobs surface mirrors the /cloud (Resources) UX.
 *
 * Layout contract:
 *   • Primary chips (JOB_KINDS `primary: true`) render inline; a trailing
 *     `+ More` button opens a portaled popover with the overflow chips.
 *   • Each chip ≈ 28px tall: small icon + REAL engine label + count badge.
 *     Active chip uses the accent colour + stroke.
 *   • Click → fires `onChange(kind)`; single-select, exactly like Cloud.
 *   • A non-active chip whose count is exactly 0 is hidden (the founder
 *     rule: don't show a chip the operator can't meaningfully click). The
 *     active chip stays visible even at 0 so context isn't lost.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) every label/icon
 * flows through the JOB_KINDS catalogue in jobKinds.ts; this is a pure
 * renderer. Labels are the engine names (HelmRelease / Kustomization / …).
 */

import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { JobKind } from '@/lib/jobs.types'
import {
  JOB_KINDS,
  type JobKindEntry,
  type JobChipKind,
} from './jobKinds'

interface JobKindChipsProps {
  /** Currently active kind. */
  activeKind: JobChipKind
  /** Per-kind count map. `null` renders "—" (jobs counts are never null). */
  counts: Record<JobKind, number | null>
  /** Switch to a different kind. */
  onChange: (next: JobChipKind) => void
}

export function JobKindChips({ activeKind, counts, onChange }: JobKindChipsProps) {
  const primary = JOB_KINDS.filter((k) => k.primary)
  const overflow = JOB_KINDS.filter((k) => !k.primary)
  const activeInOverflow = overflow.some((k) => k.id === activeKind)

  return (
    <div
      className="jobs-kind-chips"
      data-testid="jobs-kind-chips"
      role="tablist"
      aria-label="Job kind"
    >
      <style>{JOB_KIND_CHIPS_CSS}</style>
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
  kind: JobKindEntry
  active: boolean
  count: number | null
  onClick: () => void
}

function KindChip({ kind, active, count, onClick }: KindChipProps) {
  const showCount = count !== null
  // Hide a non-active chip whose count is exactly 0 — the operator can't
  // meaningfully click an engine with no rows. The active chip stays
  // visible even at 0 so context survives navigating to an empty kind.
  if (!active && showCount && count === 0) {
    return null
  }
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      aria-pressed={active}
      data-testid={`jobs-kind-chip-${kind.id}`}
      data-kind={kind.id}
      data-active={active ? 'true' : 'false'}
      data-category={kind.category}
      className={`jobs-kind-chip${active ? ' jobs-kind-chip-active' : ''}`}
      onClick={onClick}
    >
      <span className="jobs-kind-chip-icon" aria-hidden>
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
      <span className="jobs-kind-chip-label">{kind.label}</span>
      <span
        className="jobs-kind-chip-count"
        data-testid={`jobs-kind-chip-${kind.id}-count`}
      >
        {showCount ? count : '—'}
      </span>
    </button>
  )
}

/* ── More-chip overflow popover ─────────────────────────────────── */

interface MoreChipPopoverProps {
  items: JobKindEntry[]
  counts: Record<JobKind, number | null>
  activeKind: JobChipKind
  activeInGroup: boolean
  onChange: (next: JobChipKind) => void
}

function MoreChipPopover({
  items,
  counts,
  activeKind,
  activeInGroup,
  onChange,
}: MoreChipPopoverProps) {
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const popoverRef = useRef<HTMLDivElement | null>(null)
  const [coords, setCoords] = useState<{ left: number; top: number } | null>(null)

  // Click-outside dismissal — match against BOTH the +More button AND the
  // portaled popover (different DOM trees).
  useEffect(() => {
    if (!open) return
    function onDoc(ev: MouseEvent) {
      const t = ev.target as Node | null
      if (!wrapRef.current?.contains(t) && !popoverRef.current?.contains(t)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  // Position the portaled popover under the +More button; recompute on
  // open + on viewport scroll/resize so the anchor stays correct.
  useLayoutEffect(() => {
    // When closed the popover render is gated on `open`, so stale coords
    // are harmless — no need to setState here (avoids a cascading render).
    if (!open) return
    function reposition() {
      const btn = wrapRef.current?.querySelector(
        '[data-testid="jobs-kind-chip-more"]',
      ) as HTMLElement | null
      if (!btn) return
      const r = btn.getBoundingClientRect()
      setCoords({ left: r.right, top: r.bottom + 6 })
    }
    reposition()
    window.addEventListener('resize', reposition)
    window.addEventListener('scroll', reposition, true)
    return () => {
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
    }
  }, [open])

  const popover = open && coords && typeof document !== 'undefined' ? (
    createPortal(
      <div
        ref={popoverRef}
        data-testid="jobs-kind-chip-more-popover"
        role="menu"
        className="jobs-kind-chip-more-pop"
        style={{
          position: 'fixed',
          left: coords.left,
          top: coords.top,
          transform: 'translateX(-100%)',
        }}
      >
        {items.map((k) => {
          const c = counts[k.id]
          const showCount = c !== null
          const active = k.id === activeKind
          return (
            <button
              key={k.id}
              type="button"
              role="menuitemradio"
              aria-checked={active}
              data-testid={`jobs-kind-chip-more-item-${k.id}`}
              onClick={() => {
                onChange(k.id)
                setOpen(false)
              }}
              className={`jobs-kind-chip-more-item${
                active ? ' jobs-kind-chip-more-item-active' : ''
              }`}
            >
              <span className="jobs-kind-chip-icon" aria-hidden>
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
              <span className="jobs-kind-chip-more-item-label">{k.label}</span>
              <span className="jobs-kind-chip-count">{showCount ? c : '—'}</span>
            </button>
          )
        })}
      </div>,
      document.body,
    )
  ) : null

  return (
    <div className="jobs-kind-chip-more-wrap" ref={wrapRef}>
      <button
        type="button"
        data-testid="jobs-kind-chip-more"
        data-active={activeInGroup ? 'true' : 'false'}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={`jobs-kind-chip jobs-kind-chip-more${
          activeInGroup ? ' jobs-kind-chip-active' : ''
        }`}
      >
        <span aria-hidden>+</span>
        <span className="jobs-kind-chip-label">More</span>
      </button>
      {popover}
    </div>
  )
}

/* ── Local CSS ──────────────────────────────────────────────────── */

const JOB_KIND_CHIPS_CSS = `
.jobs-kind-chips {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  flex-wrap: wrap;
}
.jobs-kind-chip {
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
.jobs-kind-chip:hover {
  color: var(--color-text);
  border-color: color-mix(in srgb, var(--color-accent) 50%, var(--color-border));
}
.jobs-kind-chip:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.jobs-kind-chip-active {
  background: color-mix(in srgb, var(--color-accent) 14%, var(--color-bg-2));
  border-color: var(--color-accent);
  color: var(--color-accent);
  font-weight: 600;
}
.jobs-kind-chip-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
}
.jobs-kind-chip-icon svg {
  width: 16px;
  height: 16px;
}
/* Reconciler engines (HelmRelease / Kustomization / Deployment) — violet,
   matching the Cloud reconciler family so the cross-surface family reads. */
.jobs-kind-chip[data-category="reconciler"] .jobs-kind-chip-icon { color: #a78bfa; }
/* Bounded processes (Job step / Job task / CronJob) — blue. */
.jobs-kind-chip[data-category="process"] .jobs-kind-chip-icon { color: #60a5fa; }
/* Crossplane mutations — amber. */
.jobs-kind-chip[data-category="mutation"] .jobs-kind-chip-icon { color: #f59e0b; }
/* OpenTofu lifecycle — green. */
.jobs-kind-chip[data-category="lifecycle"] .jobs-kind-chip-icon { color: #34d399; }
.jobs-kind-chip-active .jobs-kind-chip-icon {
  color: var(--color-accent);
}
.jobs-kind-chip-label { line-height: 1; }
.jobs-kind-chip-count {
  font-size: 0.68rem;
  font-weight: 600;
  padding: 0.04rem 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}
.jobs-kind-chip-active .jobs-kind-chip-count {
  background: color-mix(in srgb, var(--color-accent) 22%, transparent);
  color: var(--color-accent);
}

.jobs-kind-chip-more-wrap { position: relative; z-index: 50; }
.jobs-kind-chip-more { color: var(--color-text); }
.jobs-kind-chip-more-pop {
  position: absolute;
  right: 0;
  top: calc(100% + 6px);
  z-index: 2000;
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
.jobs-kind-chip-more-item {
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
.jobs-kind-chip-more-item:hover {
  background: var(--color-surface-hover);
}
.jobs-kind-chip-more-item-active {
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  font-weight: 600;
}
.jobs-kind-chip-more-item-label { flex: 1; }
`
