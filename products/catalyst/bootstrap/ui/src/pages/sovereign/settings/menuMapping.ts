/**
 * menuMapping.ts — pure helpers behind Settings → Menu (EPIC #6723 lane C).
 *
 * Kept out of MenuSection.tsx so the table's diff / validation logic is
 * unit-testable on its own and the component file exports only a component
 * (react-refresh rule). `rowProblem` mirrors the server's
 * validateSidebarOverrides (console_ui.go) and `overridesFromRows` is the
 * exact PUT body: only rows that differ from their defaults, only the
 * fields that differ.
 */

import type { SidebarEntry, SidebarOverride } from '@/lib/console-ui.api'

/** Editable projection of one merged entry. */
export interface MenuRow {
  id: string
  source: SidebarEntry['source']
  enabled: boolean
  label: string
  route: string
  order: number
  parent: string
  defaultEnabled: boolean
  defaultLabel: string
  defaultRoute: string
  defaultOrder: number
}

export const LABEL_MAX = 40
export const ORDER_MIN = 0
export const ORDER_MAX = 100
export const TOP_LEVEL = ''

export function rowFromEntry(e: SidebarEntry): MenuRow {
  return {
    id: e.id,
    source: e.source,
    enabled: e.enabled,
    label: e.label,
    route: e.route,
    order: e.order,
    parent: e.parent ?? TOP_LEVEL,
    defaultEnabled: e.defaultEnabled ?? e.enabled,
    defaultLabel: e.defaultLabel ?? e.label,
    defaultRoute: e.defaultRoute ?? e.route,
    defaultOrder: e.defaultOrder ?? e.order,
  }
}

export function resetRow(r: MenuRow): MenuRow {
  return {
    ...r,
    enabled: r.defaultEnabled,
    label: r.defaultLabel,
    route: r.defaultRoute,
    order: r.defaultOrder,
    parent: TOP_LEVEL,
  }
}

/** A row carries an override when anything differs from its defaults. */
export function rowIsOverridden(r: MenuRow): boolean {
  return (
    r.enabled !== r.defaultEnabled ||
    r.label.trim() !== r.defaultLabel ||
    r.route.trim() !== r.defaultRoute ||
    r.order !== r.defaultOrder ||
    r.parent !== TOP_LEVEL
  )
}

/** PUT body: only rows that differ from their defaults, only the fields that differ. */
export function overridesFromRows(rows: MenuRow[]): SidebarOverride[] {
  const out: SidebarOverride[] = []
  for (const r of rows) {
    if (!rowIsOverridden(r)) continue
    const o: SidebarOverride = { id: r.id, enabled: r.enabled }
    const label = r.label.trim()
    const route = r.route.trim()
    if (label !== r.defaultLabel) o.label = label
    if (route !== r.defaultRoute) o.route = route
    if (r.order !== r.defaultOrder) o.order = r.order
    if (r.parent !== TOP_LEVEL) o.parent = r.parent
    out.push(o)
  }
  return out
}

/** Client-side mirror of the server's validateSidebarOverrides — "" when the row is fine. */
export function rowProblem(r: MenuRow, allowedHosts: string[]): string {
  const label = r.label.trim()
  if (label === '') return 'Label is required.'
  if (label.length > LABEL_MAX) return `Label must be at most ${LABEL_MAX} characters.`
  const route = r.route.trim()
  if (route === '') return 'Route is required.'
  if (/\s/.test(route)) return 'Route must not contain spaces.'
  if (route.startsWith('/')) {
    if (route.startsWith('//')) return 'A console path must not start with //.'
  } else if (/^https:\/\//i.test(route)) {
    let host = ''
    try {
      host = new URL(route).hostname.toLowerCase()
    } catch {
      return 'Route is not a valid https:// URL.'
    }
    if (allowedHosts.length > 0) {
      const ok = allowedHosts.some((a) => {
        const allowed = a.toLowerCase()
        return host === allowed || host.endsWith(`.${allowed}`)
      })
      if (!ok) return `https:// routes must be on one of this Sovereign's parent domains (${allowedHosts.join(', ')}).`
    }
  } else {
    return 'Route must start with / or be an https:// URL on one of this Sovereign’s parent domains.'
  }
  if (!Number.isInteger(r.order) || r.order < ORDER_MIN || r.order > ORDER_MAX) {
    return `Order must be a whole number between ${ORDER_MIN} and ${ORDER_MAX}.`
  }
  return ''
}
