/**
 * lib/console-ui.api.ts — Wave 5.69c (#2396): client helper for the
 * /api/v1/sovereigns/{id}/console-ui/sidebar-entries endpoint shipped
 * by Wave 5.69b (#2375).
 *
 * Lists Blueprints' opt-in sidebar nav entries (spec.consoleUI.
 * sidebarEntry=true) so SovereignSidebar.tsx can splice them between
 * the hardcoded BSS entry (order=60) and the pinned Settings entry.
 *
 * Returns empty list when no Blueprint opts in OR when the catalyst-
 * api endpoint is unavailable (graceful degradation — the hardcoded
 * nav still renders).
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/**
 * SidebarEntry — projection of one Blueprint's spec.consoleUI block.
 * Matches the Go SidebarEntry struct in
 * products/catalyst/bootstrap/api/internal/handler/console_ui.go.
 */
export interface SidebarEntry {
  id: string
  label: string
  route: string
  order: number
  icon?: string
}

interface SidebarEntriesResponse {
  entries: SidebarEntry[]
}

export async function getSidebarEntries(sovereignId: string): Promise<SidebarEntry[]> {
  if (!sovereignId) return []
  const res = await authedFetch(
    `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/console-ui/sidebar-entries`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    // Graceful degradation: if the endpoint 404s (older catalyst-api
    // not yet bumped) OR 502s, fall back to empty list. The
    // hardcoded sidebar nav still renders.
    return []
  }
  const body = (await res.json()) as SidebarEntriesResponse
  return Array.isArray(body?.entries) ? body.entries : []
}
