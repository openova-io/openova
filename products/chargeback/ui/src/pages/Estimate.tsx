import type { Estimate } from '../api/types'

/**
 * The public cost calculator's page-level helpers (DESIGN.md §12): the
 * limits the server enforces, the embed protocol and the share link. The
 * estimate itself — the service catalogue, the configurators and the items
 * — lives in `src/panels/estimate/model.ts`; nothing in either module is
 * money arithmetic, because every figure the page shows comes from the
 * server (POST /public/estimates?preview=1), priced by the one function the
 * invoices use.
 */

/** The default month of an estimate: 8760 / 12, as the platform books use. */
export const HOURS_PER_MONTH = '730'
export const MAX_HOURS = 744
export const MAX_MONTHS = 12
export const MAX_QUANTITY = 1e9
export const MAX_LINES = 200

// ── embed mode ─────────────────────────────────────────────────────────
// The page is framed by the marketplace (and by a partner site on the
// configured origins), which cannot measure a cross-origin document: it is
// told the height instead.

/** `?embed=1` — the page drops its header and reports its height. */
export function isEmbed(search: string): boolean {
  const value = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search).get('embed')
  return value === '1' || value === 'true'
}

export const EMBED_MESSAGE = 'openova-estimate-height'

export interface EmbedHeightMessage {
  type: typeof EMBED_MESSAGE
  height: number
}

/** The message the framed page posts to its parent on every size change. */
export function heightMessage(height: number): EmbedHeightMessage {
  return { type: EMBED_MESSAGE, height: Math.max(0, Math.ceil(height)) }
}

/** What a saved estimate's link is, for the Share box. */
export function shareLink(estimate: Pick<Estimate, 'id' | 'share_url'>, origin?: string): string {
  if (estimate.share_url) return estimate.share_url
  return `${origin ?? ''}/estimate/${estimate.id}`
}
