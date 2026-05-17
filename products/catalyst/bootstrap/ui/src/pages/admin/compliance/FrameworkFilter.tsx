/**
 * FrameworkFilter — regulatory-framework chip strip (slice C11-009,
 * Wave-2 Family-E).
 *
 * Renders one chip per OpenOva-supported framework (PCI / ISO27001 /
 * SOC2 / GDPR / HIPAA / DORA / NIS2 / FedRAMP) so the Security-Lead
 * can scope the dashboard down to "what's in scope for THIS audit".
 *
 * Behaviour:
 *   • Multi-select. Clicking a chip toggles its membership in
 *     `selected`. Empty `selected` = no filter (all frameworks).
 *   • Selected state is held by the parent (this component is
 *     controlled) so the URL ?framework=pci,iso27001 deep-link
 *     keeps its meaning across navigation.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the framework list comes from
 * `COMPLIANCE_FRAMEWORKS` in compliance.api.ts — never inline strings
 * here.
 */

import { COMPLIANCE_FRAMEWORKS, type ComplianceFrameworkId } from './compliance.api'

export interface FrameworkFilterProps {
  /** Currently-selected framework ids. Empty = no filter (all). */
  selected: ReadonlySet<ComplianceFrameworkId>
  /** Toggle handler. */
  onToggle: (id: ComplianceFrameworkId) => void
  /** Reset-to-all handler (chip strip "Clear" button). */
  onClear?: () => void
  /** Optional override for the chip strip's data-testid. */
  testId?: string
}

export function FrameworkFilter({
  selected,
  onToggle,
  onClear,
  testId = 'compliance-framework-filter',
}: FrameworkFilterProps) {
  const anySelected = selected.size > 0
  return (
    <div
      className="flex flex-wrap items-center gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-[11px]"
      data-testid={testId}
    >
      <span className="mr-1 uppercase text-[var(--color-text-dim)]">Framework:</span>
      {COMPLIANCE_FRAMEWORKS.map((f) => {
        const active = selected.has(f.id)
        return (
          <button
            key={f.id}
            type="button"
            data-testid={`framework-chip-${f.id}`}
            onClick={() => onToggle(f.id)}
            title={f.description}
            aria-pressed={active}
            className="rounded-md border px-2 py-0.5 font-semibold uppercase tracking-wide transition"
            style={{
              background: active ? 'rgba(59, 130, 246, 0.12)' : 'transparent',
              color: active ? '#bfdbfe' : 'var(--color-text-dim)',
              borderColor: active ? 'rgba(59, 130, 246, 0.45)' : 'var(--color-border)',
            }}
          >
            {f.label}
          </button>
        )
      })}
      {onClear && anySelected ? (
        <button
          type="button"
          data-testid="framework-chip-clear"
          onClick={onClear}
          className="ml-1 rounded-md border border-[var(--color-border)] px-2 py-0.5 text-[10px] uppercase text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
        >
          Clear
        </button>
      ) : null}
      <span className="ml-auto text-[10px] text-[var(--color-text-dim)]" data-testid="framework-chip-summary">
        {anySelected ? `${selected.size} of ${COMPLIANCE_FRAMEWORKS.length} active` : 'All frameworks'}
      </span>
    </div>
  )
}
