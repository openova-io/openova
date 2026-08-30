/**
 * JobKindDropdown — the LIST-view job-kind selector (Refs #6703).
 *
 * The founder's call: the list shows exactly ONE kind at a time (each kind
 * has different columns/attributes), so a multi-chip strip with a "+ More"
 * overflow is the wrong, confusing pattern — it *looks* like you could add
 * several kinds at once. A single lean dropdown states the truth: pick one
 * type. (The GRAPH view keeps the multi-chip strip, where showing several
 * kinds at once is meaningful.)
 *
 * A native <select> is deliberate: no portal, no positioning math, no
 * off-screen clipping — and fully keyboard/screen-reader accessible for free.
 */

import type { JobChipKind, JobKindEntry } from './jobKinds'
import { JOB_KINDS } from './jobKinds'

interface JobKindDropdownProps {
  /** Currently selected kind (drives the list filter). */
  activeKind: JobChipKind
  /** Per-kind counts; a number renders as "(n)", null/absent renders bare. */
  counts: Record<string, number | null>
  /** Select a different kind. */
  onChange: (next: JobChipKind) => void
}

function optionLabel(k: JobKindEntry, count: number | null | undefined): string {
  return typeof count === 'number' ? `${k.label} (${count})` : k.label
}

export function JobKindDropdown({ activeKind, counts, onChange }: JobKindDropdownProps) {
  return (
    <label className="jobs-kind-dd" data-testid="jobs-kind-dropdown">
      <style>{JOBS_KIND_DD_CSS}</style>
      <span className="jobs-kind-dd-caption">Type</span>
      <span className="jobs-kind-dd-field">
        <select
          className="jobs-kind-dd-select"
          data-testid="jobs-kind-dropdown-select"
          aria-label="Show jobs of type"
          value={activeKind}
          onChange={(e) => onChange(e.target.value as JobChipKind)}
        >
          {JOB_KINDS.map((k) => (
            <option key={k.id} value={k.id}>
              {optionLabel(k, counts[k.id])}
            </option>
          ))}
        </select>
        <svg className="jobs-kind-dd-caret" viewBox="0 0 20 20" aria-hidden>
          <path d="M6 8l4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.6"
            strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </span>
    </label>
  )
}

const JOBS_KIND_DD_CSS = `
.jobs-kind-dd {
  display: inline-flex;
  align-items: center;
  gap: 0.55rem;
  flex: 1;
  min-width: 0;
}
.jobs-kind-dd-caption {
  font-size: 0.68rem;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--color-text-dim);
  font-weight: 600;
  flex: 0 0 auto;
}
.jobs-kind-dd-field { position: relative; display: inline-flex; align-items: center; }
.jobs-kind-dd-select {
  appearance: none;
  -webkit-appearance: none;
  height: 32px;
  padding: 0 2rem 0 0.7rem;
  min-width: 13rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-2);
  color: var(--color-text);
  font-size: 0.85rem;
  font-weight: 500;
  cursor: pointer;
  transition: border-color 0.12s ease;
}
.jobs-kind-dd-select:hover {
  border-color: color-mix(in srgb, var(--color-accent) 45%, var(--color-border));
}
.jobs-kind-dd-select:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 1px;
}
.jobs-kind-dd-caret {
  position: absolute;
  right: 0.55rem;
  width: 16px;
  height: 16px;
  color: var(--color-text-dim);
  pointer-events: none;
}
`
