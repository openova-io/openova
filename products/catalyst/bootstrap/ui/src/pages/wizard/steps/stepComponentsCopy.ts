/**
 * stepComponentsCopy.ts — UX strings for the StepComponents page.
 *
 * Centralising every operator-facing string here lets translators replace
 * the file (or a single function in it) without touching JSX. Per the
 * `docs/INVIOLABLE-PRINCIPLES.md` #4 ("never hardcode") principle, no
 * StepComponents.tsx call site builds an operator-visible string with
 * inline literals — the toast / modal / button text all flow through the
 * helpers exported here.
 *
 * The module is intentionally a flat function table rather than an `i18next`
 * resource bundle; we have one locale today (en-US) and a separate ticket
 * (out of scope for #175) tracks introducing an actual i18n library. When
 * that ticket lands, replace each function body with a call to `t(...)` —
 * the call sites in StepComponents.tsx do not change.
 */

import type { ComponentEntry, Product } from './componentGroups'

/** Names list joined for inline copy. Truncates at 5 with "and N more". */
function joinNames(items: { name: string }[], limit = 5): string {
  if (items.length <= limit) {
    if (items.length <= 1) return items[0]?.name ?? ''
    if (items.length === 2) return `${items[0].name} and ${items[1].name}`
    return items.slice(0, -1).map(i => i.name).join(', ') + ', and ' + items[items.length - 1].name
  }
  const head = items.slice(0, limit).map(i => i.name).join(', ')
  return `${head}, and ${items.length - limit} more`
}

export const STEP_COMPONENTS_COPY = {
  /* ── Tab labels ──────────────────────────────────────────────── */
  tabChooseLabel: (count: number) => `Choose Your Stack (${count})`,
  tabAlwaysLabel: (count: number) => `Always Included (${count})`,

  /* ── Always Included blurb ──────────────────────────────────── */
  alwaysIncludedBlurb:
    `These platform components run on every Sovereign. They’re foundational ` +
    `— you don’t pay extra for them.`,

  /* ── Toolbar / counter ──────────────────────────────────────── */
  selectedCounter: (selected: number, total: number) => `Selected (${selected}) of ${total}`,
  searchPlaceholder: (count: number) => `Search ${count} components…`,
  resetTooltip: 'Restore the default selection (all mandatory + recommended + their dependencies)',
  resetButton: 'Reset to defaults',
  sectionTitle: 'All components',
  sectionSummary: (visible: number, selected: number) => `${visible} shown · ${selected} selected`,
  emptyState: 'No components match your filters.',

  /* ── Card / chip ────────────────────────────────────────────── */
  selectedPill: 'SELECTED',
  pillMandatory: 'MANDATORY',
  pillRecommended: 'RECOMMENDED',
  pillOptional: 'OPTIONAL',
  pillInfra: 'INFRASTRUCTURE',
  depsChip: (count: number) => `+ ${count} dep${count > 1 ? 's' : ''}`,
  includesPrefix: 'Includes:',

  /* ── Toasts (cascade add / remove) ──────────────────────────── */
  toastAddedSimple: (entry: ComponentEntry) => ({
    primary: `${entry.name} added`,
  }),

  toastAddedWithDeps: (entry: ComponentEntry, deps: ComponentEntry[]) => ({
    primary: `${entry.name} added`,
    extra: `Also added: ${joinNames(deps)}`,
  }),

  toastAddedWithFamily: (entry: ComponentEntry, product: Product, members: ComponentEntry[]) => ({
    primary: `${entry.name} added`,
    extra:
      members.length > 0
        ? `Also added ${product.name} family: ${joinNames(members)}`
        : `Selecting ${entry.name} also selects the ${product.name} family.`,
  }),

  toastProductAdded: (product: Product, members: ComponentEntry[]) => ({
    primary: `${product.name} family added`,
    extra:
      members.length > 0
        ? `Components: ${joinNames(members)}`
        : `Every ${product.name} component is now selected.`,
  }),

  toastRemoved: (entry: ComponentEntry, dependents: ComponentEntry[]) => ({
    primary: `${entry.name} removed`,
    extra:
      dependents.length > 0
        ? `Also removed: ${joinNames(dependents)}`
        : undefined,
  }),

  toastProductRemoved: (product: Product, members: ComponentEntry[]) => ({
    primary: `${product.name} family removed`,
    extra:
      members.length > 0
        ? `Components removed: ${joinNames(members)}`
        : undefined,
  }),

  toastMandatory: (entry: ComponentEntry) => ({
    primary: `${entry.name} is mandatory`,
    extra: 'Core platform components are always installed.',
  }),

  /* ── Confirm modal (cascade remove) ─────────────────────────── */
  confirmTitle: (entry: ComponentEntry) => `Remove ${entry.name}?`,
  confirmIntro: (entry: ComponentEntry, dependentCount: number) =>
    `${entry.name} is used by ${dependentCount} other component${dependentCount > 1 ? 's' : ''}. ` +
    `Removing it will also remove:`,
  confirmKeep: 'Keep',
  confirmRemove: 'Remove all',

  /* ── Product header ─────────────────────────────────────────── */
  productSelectAll: (product: Product) => `Select entire ${product.name} family`,
  productDeselectAll: (product: Product) => `Remove ${product.name} family`,
  productCardSubtitle: (_product: Product, selectedMembers: number, totalMembers: number) =>
    `${selectedMembers} of ${totalMembers} components selected`,

  /* ── Step intro / description ───────────────────────────────── */
  stepTitle: 'Platform Components',
  stepDescription:
    'Choose Your Stack lists the components you can opt into for this Sovereign — search, filter ' +
    'by product, and click to add or remove. Cascading dependencies are handled automatically ' +
    '(Harbor pulls in cnpg, seaweedfs, valkey). Always Included shows the foundational platform ' +
    'components every Sovereign runs.',
} as const
