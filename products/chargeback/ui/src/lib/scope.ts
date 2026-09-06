import type { Me } from '../api/types'

/**
 * API path prefixes per lens (#6867). The operator reads the Sovereign-wide
 * documents; a customer principal reads its own customer's twins, which the
 * server scopes regardless of what the page asks for.
 */
export interface Lens {
  operator: boolean
  customerId: string | null
  /** e.g. '/cost/summary' or '/customers/<id>/cost/summary' */
  cost: (suffix: 'summary' | 'explore' | 'export.csv' | 'dimensions') => string
  resources: string
  anomalies: string
  recommendations: string
  budgets: string
  /** UI route prefix: '' for the operator, '/my' for a customer. */
  route: string
}

export function lensFor(me: Me | null): Lens {
  const operator = me?.role === 'operator'
  const customerId = operator ? null : (me?.customer_id ?? null)
  const cust = customerId ? `/customers/${customerId}` : ''
  return {
    operator,
    customerId,
    cost: (suffix) => `${cust}/cost/${suffix}`,
    resources: `${cust}/resources`,
    anomalies: `${cust}/anomalies`,
    recommendations: `${cust}/recommendations`,
    budgets: `${cust}/budgets`,
    route: operator ? '' : '/my',
  }
}

export type LensPage = 'overview' | 'explore' | 'resources' | 'statements' | 'budgets' | 'anomalies' | 'recommendations' | 'discounts' | 'sources'

/**
 * A link to another page of the same lens. On the operator and /my lenses
 * pages are routes (`/explore?…`, `/my/budgets`); on a customer-pinned lens
 * they are tabs of the customer detail page (`/customers/<id>?tab=cost&…`),
 * and the two operator-only analyses point at the operator page filtered to
 * that customer.
 */
export function pageHref(lens: Lens, page: LensPage, query = ''): string {
  const q = query.replace(/^[?&]/, '')
  if (lens.operator && lens.customerId) {
    if (page === 'anomalies' || page === 'recommendations') return `/${page}?customer=${encodeURIComponent(lens.customerId)}${q ? '&' + q : ''}`
    const tab = page === 'explore' ? 'cost' : page
    return `${lens.route}?tab=${tab}${q ? '&' + q : ''}`
  }
  return `${lens.route}/${page}${q ? '?' + q : ''}`
}

/** A lens pinned to one customer (the operator's customer detail tabs). */
export function customerLens(customerId: string): Lens {
  const cust = `/customers/${customerId}`
  return {
    operator: true,
    customerId,
    cost: (suffix) => `${cust}/cost/${suffix}`,
    resources: `${cust}/resources`,
    anomalies: `${cust}/anomalies`,
    recommendations: `${cust}/recommendations`,
    budgets: `${cust}/budgets`,
    route: `/customers/${customerId}`,
  }
}
