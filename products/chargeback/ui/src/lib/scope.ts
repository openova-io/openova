import type { Me } from '../api/types'
import { isPartner } from './access'

/**
 * API path prefixes per lens (#6867). The Sovereign-wide documents are read
 * by the operator AND by a partner principal, whose every query the server
 * confines to the customers assigned to its partner (DESIGN.md §13.5); a
 * customer principal reads its own customer's twins, which the server scopes
 * regardless of what the page asks for.
 */
export interface Lens {
  /** A SOVEREIGN principal: the only one that may open the provider's own
   *  configuration (price books, a customer's sources and settings). */
  operator: boolean
  /** The lens spans MORE THAN ONE customer — the Sovereign's own, and a
   *  partner's over its customers. This, not `operator`, decides whether the
   *  `customer` dimension is offered and the Customer column drawn: a
   *  reseller groups its costs by customer exactly as the operator does. */
  crossCustomer: boolean
  customerId: string | null
  /** Where ONE customer's page lives on this lens ('' when the lens never
   *  links to another customer). */
  customerRoute: string
  /** e.g. '/cost/summary' or '/customers/<id>/cost/summary' */
  cost: (suffix: 'summary' | 'explore' | 'export.csv' | 'dimensions') => string
  resources: string
  anomalies: string
  recommendations: string
  budgets: string
  /** e.g. '/reports/schedules' or '/customers/<id>/reports/schedules' */
  reports: string
  /** UI route prefix: '' for the operator, '/partner' for a partner, '/my' for a customer. */
  route: string
}

/** Every lens is this shape; only the four inputs differ. */
function lensAt(opts: { operator: boolean; customerId: string | null; route: string; customerRoute: string }): Lens {
  const cust = opts.customerId ? `/customers/${opts.customerId}` : ''
  return {
    operator: opts.operator,
    crossCustomer: opts.customerId === null,
    customerId: opts.customerId,
    customerRoute: opts.customerRoute,
    cost: (suffix) => `${cust}/cost/${suffix}`,
    resources: `${cust}/resources`,
    anomalies: `${cust}/anomalies`,
    recommendations: `${cust}/recommendations`,
    budgets: `${cust}/budgets`,
    reports: `${cust}/reports/schedules`,
    route: opts.route,
  }
}

export function lensFor(me: Me | null): Lens {
  // A partner principal reports its PARTY as its customer_id, so it must be
  // recognised before that id would pin the lens to the partner's own
  // account: what it came to see is its customers.
  if (isPartner(me)) return partnerLens()
  const operator = me?.role === 'operator'
  const customerId = operator ? null : (me?.customer_id ?? null)
  return lensAt({ operator, customerId, route: operator ? '' : '/my', customerRoute: '/customers' })
}

/** The operator's Sovereign-wide lens (no customer pinned). */
export function operatorLens(): Lens {
  return lensFor({ email: '', role: 'operator' })
}

/**
 * The PARTNER lens (DESIGN.md §13.5): the Sovereign-wide cost documents,
 * which the server confines to the partner's own customers. It is NOT the
 * operator — it never reaches the provider's price books or a customer's
 * configuration — and it is not pinned to one customer either.
 */
export function partnerLens(): Lens {
  return lensAt({ operator: false, customerId: null, route: '/partner', customerRoute: '/partner/customers' })
}

export type LensPage = 'overview' | 'explore' | 'resources' | 'statements' | 'budgets' | 'reports' | 'anomalies' | 'recommendations' | 'discounts' | 'sources'

/**
 * A link to another page of the same lens. On the operator, partner and /my
 * lenses pages are routes (`/explore?…`, `/partner/resources`, `/my/budgets`);
 * on a customer-pinned lens they are tabs of the customer detail page
 * (`/customers/<id>?tab=cost&…`), and the two operator-only analyses point at
 * the operator page filtered to that customer.
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
  return lensAt({ operator: true, customerId, route: `/customers/${customerId}`, customerRoute: '/customers' })
}

/** One customer's own page, on whichever lens is looking at it. */
export function customerHref(lens: Lens, customerId: string): string {
  return `${lens.customerRoute}/${encodeURIComponent(customerId)}`
}
