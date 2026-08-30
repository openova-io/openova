/**
 * KindChipStrip — the SINGLE chip-strip implementation shared by the
 * /cloud (Resources) and /jobs surfaces.
 *
 * It is the generalisation of the original CloudKindChips renderer: the
 * chip button + `+ More` overflow popover + all CSS, made generic over
 * the kind-id type `K` and taking the kind CATALOGUE as a PROP instead of
 * importing a specific `./kinds` module. `CloudKindChips` and the /jobs
 * chip are now thin call-sites that pass their own catalogue + a distinct
 * `testidPrefix` / `storageKey`, so there is exactly one component to
 * reason about and every improvement lands on both surfaces at once.
 *
 * Layout contract (unchanged from CloudKindChips):
 *   • `primary` chips render inline; the rest live in the `+ More` popover.
 *   • A non-active chip whose count is exactly 0 is hidden (the founder
 *     rule — don't show a kind the operator can't meaningfully click). The
 *     ACTIVE chip stays visible even at 0 so context survives.
 *   • Single-select: clicking a chip fires `onChange(id)`.
 *
 * Curate visible chips (the founder's chosen behaviour, built in HERE so
 * BOTH surfaces gain it from one component):
 *   • Each inline chip carries a small ✕ to REMOVE it from the strip; a
 *     removed kind drops into the `+ More` popover.
 *   • The popover lists the natural overflow kinds AND the user-removed
 *     kinds; each user-removed kind shows an `add` (+) affordance that
 *     RE-ADDS it inline (removed items are visually distinguished by that
 *     affordance + `data-removed`).
 *   • The removed-set is persisted per surface in localStorage under
 *     `storageKey` (a JSON array of kind ids), read/written inside
 *     try/catch so private-mode / disabled-storage never throws.
 *   • The currently-active chip can NEVER be removed (its ✕ is not
 *     rendered, and the remove handler guards it) — removing the active
 *     filter would strand the view, exactly as the count-0 rule keeps the
 *     active chip visible.
 *
 * Every affordance is a real, keyboard-focusable `<button>` with an
 * aria-label so the strip stays fully accessible.
 */

import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

/** The minimal catalogue-entry shape the strip renders. Both
 *  `CloudKindEntry` and `JobKindEntry` are structural supersets of this. */
export interface KindChipEntry<K extends string> {
  id: K
  label: string
  /** SVG path data on the canonical 24x24 viewBox — Tabler-style. */
  icon: string
  /** Conceptual category — drives the chip-icon tint. */
  category?: string
  /** True → renders inline; false → `+ More` overflow popover. */
  primary: boolean
  /** When false the count is not collected → the chip renders "—". When
   *  omitted it defaults to true (the /jobs catalogue has no such flag). */
  hasData?: boolean
}

export interface KindChipStripProps<K extends string> {
  /** The kind catalogue — passed in, never imported, so the component is
   *  surface-agnostic. */
  catalogue: ReadonlyArray<KindChipEntry<K>>
  /** Currently active kind, or `null` for nothing highlighted (the graph
   *  view's default). */
  activeKind: K | null
  /** Per-kind count map. `null` renders "—". */
  counts: Record<K, number | null>
  /** Select a kind. */
  onChange: (next: K) => void
  /** localStorage key for the per-surface removed-set (distinct per
   *  surface, e.g. `sov-cloud-hidden-kinds` / `sov-jobs-hidden-kinds`). */
  storageKey: string
  /** testid + CSS-class prefix — e.g. `cloud-kind` / `jobs-kind`. Keeps the
   *  established `<prefix>-chips` / `<prefix>-chip-<id>` testids stable. */
  testidPrefix: string
  /** Fired whenever the VISIBLE set changes (mount + every curate) with the
   *  kinds still on the strip (catalogue minus the user-removed set). The
   *  /jobs graph uses this so removing a chip FILTERS its nodes off the
   *  canvas. Omitted on /cloud (curate there only hides the chip). */
  onVisibleChange?: (visible: ReadonlySet<K>) => void
}

/** Does this entry's count render as a real number (vs "—")? */
function hasRealCount<K extends string>(k: KindChipEntry<K>, count: number | null): boolean {
  return (k.hasData ?? true) && count !== null
}

export function KindChipStrip<K extends string>({
  catalogue,
  activeKind,
  counts,
  onChange,
  storageKey,
  testidPrefix,
  onVisibleChange,
}: KindChipStripProps<K>) {
  const base = testidPrefix

  // The user-removed set (persisted). Read once on mount inside try/catch;
  // an empty set (no stored value / private-mode / parse error) renders
  // the exact default strip, so behaviour is unchanged until the operator
  // curates.
  const [hidden, setHidden] = useState<Set<K>>(() => readHiddenSet(storageKey, catalogue))

  // Report the VISIBLE kinds (catalogue minus removed) to the consumer on
  // mount + every curate, so the graph can filter its nodes to match. Keyed
  // on the hidden set + catalogue identity so it fires exactly on change.
  useEffect(() => {
    if (!onVisibleChange) return
    const visible = new Set<K>()
    for (const k of catalogue) if (!hidden.has(k.id)) visible.add(k.id)
    onVisibleChange(visible)
  }, [hidden, catalogue, onVisibleChange])

  function persist(next: Set<K>) {
    if (typeof window === 'undefined') return
    try {
      window.localStorage.setItem(storageKey, JSON.stringify([...next]))
    } catch {
      /* private-mode / disabled storage — non-fatal */
    }
  }

  function removeKind(id: K) {
    // Guard: the active chip is never removable (mirrors the count-0 rule
    // that keeps the active chip visible). Removing it would strand the view.
    if (id === activeKind) return
    setHidden((prev) => {
      const next = new Set(prev)
      next.add(id)
      persist(next)
      return next
    })
  }

  function addKind(id: K) {
    setHidden((prev) => {
      const next = new Set(prev)
      next.delete(id)
      persist(next)
      return next
    })
  }

  // A primary kind is treated as removed only while it is NOT the active
  // kind — the active override keeps it inline (and un-removable) until the
  // operator selects a different kind, at which point it falls back into
  // the popover.
  const isRemoved = (k: KindChipEntry<K>) =>
    k.primary && hidden.has(k.id) && k.id !== activeKind

  // Inline chips: primary, not removed, and not hidden by the count-0 rule.
  const inlineChips = catalogue.filter((k) => {
    if (!k.primary) return false
    if (isRemoved(k)) return false
    const active = k.id === activeKind
    const count = counts[k.id]
    if (!active && hasRealCount(k, count) && count === 0) return false
    return true
  })

  // Popover: the natural overflow (never primary) followed by the
  // user-removed primary kinds (re-addable).
  const overflowItems = catalogue.filter((k) => !k.primary)
  const removedItems = catalogue.filter((k) => isRemoved(k))

  // `+ More` is highlighted when the active kind lives in the natural
  // overflow (a user-removed active kind renders inline via the override).
  const activeInGroup = overflowItems.some((k) => k.id === activeKind)

  return (
    <div
      className={`${base}-chips`}
      data-testid={`${base}-chips`}
      role="tablist"
      aria-label="Kind"
    >
      <style>{chipStripCss(base)}</style>
      {inlineChips.map((k) => (
        <KindChip
          key={k.id}
          base={base}
          kind={k}
          active={k.id === activeKind}
          count={counts[k.id]}
          removable={k.id !== activeKind}
          onClick={() => onChange(k.id)}
          onRemove={() => removeKind(k.id)}
        />
      ))}
      <MoreChipPopover
        base={base}
        overflowItems={overflowItems}
        removedItems={removedItems}
        counts={counts}
        activeKind={activeKind}
        activeInGroup={activeInGroup}
        onChange={onChange}
        onAdd={addKind}
      />
    </div>
  )
}

/* ── Single chip ────────────────────────────────────────────────── */

interface KindChipProps<K extends string> {
  base: string
  kind: KindChipEntry<K>
  active: boolean
  count: number | null
  removable: boolean
  onClick: () => void
  onRemove: () => void
}

function KindChip<K extends string>({
  base,
  kind,
  active,
  count,
  removable,
  onClick,
  onRemove,
}: KindChipProps<K>) {
  const showCount = hasRealCount(kind, count)
  return (
    <div className={`${base}-chip-wrap`} data-testid={`${base}-chip-wrap-${kind.id}`}>
      <button
        type="button"
        role="tab"
        aria-selected={active}
        aria-pressed={active}
        data-testid={`${base}-chip-${kind.id}`}
        data-kind={kind.id}
        data-active={active ? 'true' : 'false'}
        data-category={kind.category}
        className={`${base}-chip${active ? ` ${base}-chip-active` : ''}${
          removable ? ` ${base}-chip-removable` : ''
        }`}
        onClick={onClick}
      >
        <span className={`${base}-chip-icon`} aria-hidden>
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
        <span className={`${base}-chip-label`}>{kind.label}</span>
        <span className={`${base}-chip-count`} data-testid={`${base}-chip-${kind.id}-count`}>
          {showCount ? count : '—'}
        </span>
      </button>
      {removable ? (
        <button
          type="button"
          className={`${base}-chip-remove`}
          data-testid={`${base}-chip-${kind.id}-remove`}
          aria-label={`Remove ${kind.label} from the strip`}
          title={`Remove ${kind.label} from the strip`}
          onClick={onRemove}
        >
          <span aria-hidden>×</span>
        </button>
      ) : null}
    </div>
  )
}

/* ── More-chip overflow popover ─────────────────────────────────── */

interface MoreChipPopoverProps<K extends string> {
  base: string
  overflowItems: ReadonlyArray<KindChipEntry<K>>
  removedItems: ReadonlyArray<KindChipEntry<K>>
  counts: Record<K, number | null>
  activeKind: K | null
  activeInGroup: boolean
  onChange: (next: K) => void
  onAdd: (id: K) => void
}

function MoreChipPopover<K extends string>({
  base,
  overflowItems,
  removedItems,
  counts,
  activeKind,
  activeInGroup,
  onChange,
  onAdd,
}: MoreChipPopoverProps<K>) {
  const [open, setOpen] = useState(false)
  // The wrapper hosts the +More button; the popover renders via portal to
  // document.body so an ancestor's `overflow: auto` (the horizontal-scroll
  // wrapper) cannot clip it.
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

  // Position the portaled popover under the +More button; recompute on open
  // + on viewport scroll/resize so the anchor stays correct. When closed the
  // popover render is gated on `open`, so stale coords are harmless — no
  // setState here (avoids a cascading-render lint error).
  useLayoutEffect(() => {
    if (!open) return
    function reposition() {
      const btn = wrapRef.current?.querySelector(
        `[data-testid="${base}-chip-more"]`,
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
  }, [open, base])

  const popover =
    open && coords && typeof document !== 'undefined'
      ? createPortal(
          <div
            ref={popoverRef}
            data-testid={`${base}-chip-more-popover`}
            role="menu"
            className={`${base}-chip-more-pop`}
            style={{
              // Position (computed) + the critical box styles INLINE so the
              // popover is always legible even if the templated <style> tag
              // has not applied to this portaled node yet (the transparent/
              // cramped failure mode). The class still themes hover/colors.
              position: 'fixed',
              left: coords.left,
              top: coords.top,
              transform: 'translateX(-100%)',
              zIndex: 2000,
              display: 'flex',
              flexDirection: 'column',
              gap: '0.15rem',
              minWidth: '15rem',
              maxHeight: '60vh',
              overflowY: 'auto',
              padding: '0.35rem',
              borderRadius: '10px',
              border: '1px solid var(--color-border)',
              background: 'var(--color-bg-1, #0d1117)',
              boxShadow: '0 8px 24px rgba(0, 0, 0, 0.45)',
            }}
          >
            {overflowItems.map((k) => (
              <MorePopoverItem
                key={k.id}
                base={base}
                kind={k}
                count={counts[k.id]}
                active={k.id === activeKind}
                removed={false}
                onSelect={() => {
                  onChange(k.id)
                  setOpen(false)
                }}
                onAdd={() => onAdd(k.id)}
              />
            ))}
            {removedItems.map((k) => (
              <MorePopoverItem
                key={k.id}
                base={base}
                kind={k}
                count={counts[k.id]}
                active={k.id === activeKind}
                removed
                onSelect={() => {
                  onChange(k.id)
                  setOpen(false)
                }}
                onAdd={() => onAdd(k.id)}
              />
            ))}
          </div>,
          document.body,
        )
      : null

  return (
    <div className={`${base}-chip-more-wrap`} ref={wrapRef}>
      <button
        type="button"
        data-testid={`${base}-chip-more`}
        data-active={activeInGroup ? 'true' : 'false'}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={`${base}-chip ${base}-chip-more${
          activeInGroup ? ` ${base}-chip-active` : ''
        }`}
      >
        <span aria-hidden>+</span>
        <span className={`${base}-chip-label`}>More</span>
      </button>
      {popover}
    </div>
  )
}

interface MorePopoverItemProps<K extends string> {
  base: string
  kind: KindChipEntry<K>
  count: number | null
  active: boolean
  removed: boolean
  onSelect: () => void
  onAdd: () => void
}

function MorePopoverItem<K extends string>({
  base,
  kind,
  count,
  active,
  removed,
  onSelect,
  onAdd,
}: MorePopoverItemProps<K>) {
  const showCount = hasRealCount(kind, count)
  const selectButton = (
    <button
      type="button"
      role="menuitemradio"
      aria-checked={active}
      data-testid={`${base}-chip-more-item-${kind.id}`}
      onClick={onSelect}
      className={`${base}-chip-more-item${active ? ` ${base}-chip-more-item-active` : ''}`}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '0.55rem',
        width: '100%',
        padding: '0.4rem 0.55rem',
        border: 0,
        background: 'transparent',
        borderRadius: '6px',
        color: 'var(--color-text)',
        fontSize: '0.82rem',
        cursor: 'pointer',
        textAlign: 'left',
        whiteSpace: 'nowrap',
      }}
    >
      <span className={`${base}-chip-icon`} aria-hidden>
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
      <span className={`${base}-chip-more-item-label`} style={{ flex: 1 }}>
        {kind.label}
      </span>
      <span className={`${base}-chip-count`}>{showCount ? count : '—'}</span>
    </button>
  )

  // Natural overflow items render exactly as before (single button). Only
  // user-removed items are wrapped in a row that adds the re-add affordance,
  // so the established popover testids/structure are byte-identical until
  // the operator curates.
  if (!removed) return selectButton

  return (
    <div
      className={`${base}-chip-more-item-row`}
      data-testid={`${base}-chip-more-item-row-${kind.id}`}
      data-removed="true"
      style={{ display: 'flex', alignItems: 'center', gap: '0.25rem' }}
    >
      {selectButton}
      <button
        type="button"
        className={`${base}-chip-more-item-add`}
        data-testid={`${base}-chip-more-item-${kind.id}-add`}
        aria-label={`Add ${kind.label} back to the strip`}
        title={`Add ${kind.label} back to the strip`}
        onClick={onAdd}
      >
        <span aria-hidden>+</span>
      </button>
    </div>
  )
}

/* ── Persistence helpers ────────────────────────────────────────── */

function readHiddenSet<K extends string>(
  storageKey: string,
  catalogue: ReadonlyArray<KindChipEntry<K>>,
): Set<K> {
  if (typeof window === 'undefined') return new Set()
  try {
    const raw = window.localStorage.getItem(storageKey)
    if (!raw) return new Set()
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return new Set()
    const known = new Set<string>(catalogue.map((k) => k.id))
    return new Set(parsed.filter((x): x is K => typeof x === 'string' && known.has(x)))
  } catch {
    return new Set()
  }
}

/* ── Local CSS (templated on the surface's class prefix) ─────────── */

function chipStripCss(b: string): string {
  return `
.${b}-chips {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  flex-wrap: wrap;
}
.${b}-chip-wrap { position: relative; display: inline-flex; align-items: center; }
.${b}-chip {
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
/* Extra right padding makes room for the absolutely-positioned ✕ so the
   remove affordance sits INSIDE the pill without a nested button. */
.${b}-chip-removable { padding-right: 1.45rem; }
.${b}-chip:hover {
  color: var(--color-text);
  border-color: color-mix(in srgb, var(--color-accent) 50%, var(--color-border));
}
.${b}-chip:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.${b}-chip-active {
  background: color-mix(in srgb, var(--color-accent) 14%, var(--color-bg-2));
  border-color: var(--color-accent);
  color: var(--color-accent);
  font-weight: 600;
}
.${b}-chip-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
}
.${b}-chip-icon svg { width: 16px; height: 16px; }
.${b}-chip[data-category="compute"] .${b}-chip-icon { color: #60a5fa; }
.${b}-chip[data-category="network"] .${b}-chip-icon { color: #34d399; }
.${b}-chip[data-category="storage"] .${b}-chip-icon { color: #f59e0b; }
.${b}-chip[data-category="reconciler"] .${b}-chip-icon { color: #a78bfa; }
.${b}-chip[data-category="process"] .${b}-chip-icon { color: #60a5fa; }
.${b}-chip[data-category="mutation"] .${b}-chip-icon { color: #f59e0b; }
.${b}-chip[data-category="lifecycle"] .${b}-chip-icon { color: #34d399; }
.${b}-chip-active .${b}-chip-icon { color: var(--color-accent); }
.${b}-chip-label { line-height: 1; }
.${b}-chip-count {
  font-size: 0.68rem;
  font-weight: 600;
  padding: 0.04rem 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}
.${b}-chip-active .${b}-chip-count {
  background: color-mix(in srgb, var(--color-accent) 22%, transparent);
  color: var(--color-accent);
}

/* ✕ remove affordance — overlaid inside the pill's right padding. */
.${b}-chip-remove {
  position: absolute;
  right: 3px;
  top: 50%;
  transform: translateY(-50%);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 15px;
  height: 15px;
  padding: 0;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: var(--color-text-dim);
  font-size: 0.85rem;
  line-height: 1;
  cursor: pointer;
}
.${b}-chip-remove:hover {
  background: color-mix(in srgb, var(--color-border) 70%, transparent);
  color: var(--color-text);
}
.${b}-chip-remove:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 1px;
}

.${b}-chip-more-wrap { position: relative; z-index: 50; }
.${b}-chip-more { color: var(--color-text); }
.${b}-chip-more-pop {
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
.${b}-chip-more-item {
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
.${b}-chip-more-item:hover { background: var(--color-surface-hover); }
.${b}-chip-more-item-active {
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  font-weight: 600;
}
.${b}-chip-more-item-label { flex: 1; }

/* A user-removed kind: the select item + a re-add (+) affordance. */
.${b}-chip-more-item-row { display: flex; align-items: center; gap: 0.25rem; }
.${b}-chip-more-item-row .${b}-chip-more-item { flex: 1; }
.${b}-chip-more-item-add {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  flex-shrink: 0;
  border: 1px solid var(--color-border);
  border-radius: 6px;
  background: transparent;
  color: var(--color-text-dim);
  font-size: 0.95rem;
  line-height: 1;
  cursor: pointer;
}
.${b}-chip-more-item-add:hover {
  border-color: var(--color-accent);
  color: var(--color-accent);
}
.${b}-chip-more-item-add:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 1px;
}
`
}
