import type { Lens } from './scope'

/**
 * Cross-page links that depend on the lens (#6867). Four lenses exist: the
 * operator's own pages (''), a partner's ('/partner'), the customer's
 * ('/my'), and the operator looking at ONE customer inside the customer
 * detail — whose sub-pages are `?tab=` panels of /customers/{id}, not routes
 * of their own. Everything but that last one is `lens.route` + the page.
 */

function join(base: string, q: string | URLSearchParams, sep: '?' | '&'): string {
  const s = typeof q === 'string' ? q : q.toString()
  return s ? `${base}${sep}${s}` : base
}

/** The cost explorer for this lens, with a query string of explorer params. */
export function explorerHref(lens: Lens, params: string | URLSearchParams = ''): string {
  if (lens.operator && lens.customerId) return join(`/customers/${lens.customerId}?tab=explore`, params, '&')
  return join(`${lens.route}/explore`, params, '?')
}

/** The resources list for this lens. */
export function resourcesHref(lens: Lens, params: string | URLSearchParams = ''): string {
  if (lens.operator && lens.customerId) return join(`/customers/${lens.customerId}?tab=resources`, params, '&')
  return join(`${lens.route}/resources`, params, '?')
}

/**
 * The resource detail page. Every operator lens (the customer-detail tab
 * included) opens the operator's page; a partner and a customer open their
 * own lens's.
 */
export function resourceHref(lens: Lens, sourceId: string, resourceId: string): string {
  const base = lens.operator && lens.customerId ? '' : lens.route
  return `${base}/resources/${encodeURIComponent(sourceId)}/${encodeURIComponent(resourceId)}`
}
