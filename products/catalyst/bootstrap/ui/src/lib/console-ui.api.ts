/**
 * lib/console-ui.api.ts — client for the Sovereign-console sidebar mapping
 * surface (EPIC #6723 lane C on top of Wave 5.69b/c #2375/#2396).
 *
 *   GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries
 *       the MERGED menu: Blueprint spec.consoleUI defaults + installed
 *       Applications with a user-UI endpoint (candidates, default disabled)
 *       ⊕ the per-Sovereign overrides. SovereignSidebar renders enabled
 *       entries; Settings → Menu edits them.
 *   GET/PUT /api/v1/sovereigns/{id}/console-ui/sidebar-overrides
 *       the raw sovereign-admin mapping, persisted as ConfigMap
 *       catalyst-system/console-ui-sidebar on the Sovereign's cluster.
 *
 * Graceful degradation is the contract for the sidebar reads: an older
 * catalyst-api (404), a 403 on an Org-scoped console, or a network error
 * all yield an empty list and the hardcoded nav still renders. The
 * overrides calls THROW so the Settings section can show the real reason.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

export type SidebarEntrySource = 'blueprint' | 'application'

/**
 * SidebarEntry — one merged menu entry. Mirrors the Go SidebarEntry struct
 * in products/catalyst/bootstrap/api/internal/handler/console_ui.go.
 * `default*` echo the un-overridden values so the Menu table can diff and
 * reset without a second request.
 */
export interface SidebarEntry {
  id: string
  label: string
  route: string
  order: number
  icon?: string
  source: SidebarEntrySource
  enabled: boolean
  /** FLAT_NAV id the entry nests under; absent = top-level. */
  parent?: string
  overridden?: boolean
  defaultLabel?: string
  defaultRoute?: string
  defaultOrder?: number
  defaultEnabled?: boolean
}

export interface SidebarMenu {
  entries: SidebarEntry[]
  /** FLAT_NAV ids the API accepts as `parent` (the mappable top-level items). */
  parents: string[]
}

/** One stored mapping decision — the PUT body row. */
export interface SidebarOverride {
  id: string
  enabled: boolean
  label?: string
  route?: string
  order?: number
  parent?: string
}

export interface SidebarOverridesResponse {
  entries: SidebarOverride[]
  parents: string[]
  /** Hosts an https:// route may target (the Sovereign FQDN + parent-domain pool). */
  allowedHosts: string[]
  namespace: string
  name: string
}

export interface SidebarOverridesSaveResponse {
  entries: SidebarOverride[]
  appliedAt: string
  namespace: string
  name: string
}

/** Thrown by the overrides calls; `problems` carries the server's per-field list on a 400. */
export class SidebarApiError extends Error {
  readonly status: number
  readonly problems: string[]
  constructor(status: number, message: string, problems: string[] = []) {
    super(message)
    this.name = 'SidebarApiError'
    this.status = status
    this.problems = problems
  }
}

function base(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/console-ui`
}

/** Older catalyst-api builds emit only {id,label,route,order,icon}; fill the mapping fields. */
function normaliseEntry(raw: Partial<SidebarEntry> & { id: string }): SidebarEntry {
  const label = typeof raw.label === 'string' && raw.label !== '' ? raw.label : raw.id
  const route = typeof raw.route === 'string' && raw.route !== '' ? raw.route : `/apps/${raw.id}`
  const order = typeof raw.order === 'number' ? raw.order : 50
  return {
    id: raw.id,
    label,
    route,
    order,
    icon: raw.icon,
    source: raw.source === 'application' ? 'application' : 'blueprint',
    enabled: raw.enabled !== false,
    parent: typeof raw.parent === 'string' && raw.parent !== '' ? raw.parent : undefined,
    overridden: raw.overridden === true,
    defaultLabel: typeof raw.defaultLabel === 'string' ? raw.defaultLabel : label,
    defaultRoute: typeof raw.defaultRoute === 'string' ? raw.defaultRoute : route,
    defaultOrder: typeof raw.defaultOrder === 'number' ? raw.defaultOrder : order,
    defaultEnabled: typeof raw.defaultEnabled === 'boolean' ? raw.defaultEnabled : raw.enabled !== false,
  }
}

export async function getSidebarMenu(sovereignId: string): Promise<SidebarMenu> {
  const empty: SidebarMenu = { entries: [], parents: [] }
  if (!sovereignId) return empty
  try {
    const res = await authedFetch(`${base(sovereignId)}/sidebar-entries`, {
      headers: { Accept: 'application/json' },
    })
    if (!res.ok) return empty
    const body = (await res.json()) as { entries?: unknown; parents?: unknown }
    const entries = Array.isArray(body?.entries)
      ? (body.entries as Array<Partial<SidebarEntry> & { id?: unknown }>)
          .filter((e): e is Partial<SidebarEntry> & { id: string } => typeof e?.id === 'string' && e.id !== '')
          .map(normaliseEntry)
      : []
    const parents = Array.isArray(body?.parents)
      ? (body.parents as unknown[]).filter((p): p is string => typeof p === 'string')
      : []
    return { entries, parents }
  } catch {
    return empty
  }
}

/** The sidebar's read: every merged entry (the caller hides enabled=false). */
export async function getSidebarEntries(sovereignId: string): Promise<SidebarEntry[]> {
  return (await getSidebarMenu(sovereignId)).entries
}

async function readError(res: Response): Promise<SidebarApiError> {
  let message = `${res.status} ${res.statusText}`.trim()
  let problems: string[] = []
  try {
    const text = await res.text()
    try {
      const body = JSON.parse(text) as { detail?: unknown; error?: unknown; problems?: unknown }
      if (Array.isArray(body?.problems)) {
        problems = (body.problems as unknown[]).filter((p): p is string => typeof p === 'string')
      }
      if (typeof body?.detail === 'string' && body.detail) message = body.detail
      else if (typeof body?.error === 'string' && body.error) message = body.error
    } catch {
      if (text.trim()) message = text.trim()
    }
  } catch {
    // keep the status line
  }
  return new SidebarApiError(res.status, message, problems)
}

export async function getSidebarOverrides(sovereignId: string): Promise<SidebarOverridesResponse> {
  if (!sovereignId) throw new SidebarApiError(0, 'no Sovereign resolved')
  const res = await authedFetch(`${base(sovereignId)}/sidebar-overrides`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) throw await readError(res)
  const body = (await res.json()) as Partial<SidebarOverridesResponse>
  return {
    entries: Array.isArray(body.entries) ? body.entries : [],
    parents: Array.isArray(body.parents) ? body.parents : [],
    allowedHosts: Array.isArray(body.allowedHosts) ? body.allowedHosts : [],
    namespace: body.namespace ?? '',
    name: body.name ?? '',
  }
}

export async function putSidebarOverrides(
  sovereignId: string,
  entries: SidebarOverride[],
): Promise<SidebarOverridesSaveResponse> {
  if (!sovereignId) throw new SidebarApiError(0, 'no Sovereign resolved')
  const res = await authedFetch(`${base(sovereignId)}/sidebar-overrides`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ entries }),
  })
  if (!res.ok) throw await readError(res)
  return (await res.json()) as SidebarOverridesSaveResponse
}
