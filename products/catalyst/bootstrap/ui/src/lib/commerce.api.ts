/**
 * lib/commerce.api.ts — typed client for the Organizations commerce
 * editors (issue #3378 DoD 7/8). Plans / add-ons / bundles / industries /
 * apps, edited over the EXISTING endpoints (§6 — no new business
 * endpoint):
 *
 *   • READ  — the public catalog list endpoints via the SME gateway:
 *       GET /api/catalog/{plans,addons,bundles,industries,apps}
 *     (core/services/gateway/main.go:50-54, Public:true).
 *   • WRITE — the catalyst-api commerce proxy that forwards to the
 *     superadmin-JWT /catalog/admin/* endpoints (sme_commerce.go):
 *       POST   /api/v1/sme/commerce/{kind}
 *       PUT    /api/v1/sme/commerce/{kind}/{id}
 *       DELETE /api/v1/sme/commerce/{kind}/{id}
 *
 * The struct shapes mirror core/services/catalog/store/store.go exactly
 * (Plan :134-149, AddOn :152-160, Bundle :116-124, Industry :104-113),
 * so the editors render every field the model carries — features (list),
 * included_quotas (k/v), product_slug, Bundle.apps + Industry.
 * suggested_apps (multi-select), Industry.bundle_id (live select).
 */

import { BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/* ── Wire shapes — verbatim mirror of store.go ─────────────────────── */

export interface CommercePlan {
  id?: string
  slug: string
  name: string
  description: string
  cpu: string
  memory: string
  storage: string
  price_omr: number
  popular: boolean
  sort_order: number
  features: string[]
  stripe_price_id?: string
  /** Product-scoped plans (e.g. sandbox) must NOT leak into the generic
   *  org-provisioning picker — #3156 regression guard. */
  product_slug?: string
  included_quotas?: Record<string, string>
}

export interface CommerceAddOn {
  id?: string
  slug: string
  name: string
  description: string
  price_omr: number
  included: boolean
  category: string
}

export interface CommerceBundle {
  id?: string
  slug: string
  name: string
  tagline: string
  apps: string[]
  discount: number
  recommended_size: string
}

export interface CommerceIndustry {
  id?: string
  slug: string
  name: string
  emoji: string
  description: string
  display_order: number
  suggested_apps: string[]
  bundle_id: string
}

export interface CommerceApp {
  id?: string
  slug: string
  name: string
  tagline?: string
  category?: string
  published?: boolean
}

export type CommerceKind = 'plans' | 'addons' | 'bundles' | 'industries' | 'apps'

/** API_BASE without the /api version suffix — commerce reads hit the
 *  public /api/catalog/* gateway path, writes hit /api/v1/sme/commerce/*. */
const apiRoot = `${BASE}api`

/* ── Reads (public list endpoints) ─────────────────────────────────── */

async function readList<T>(kind: CommerceKind): Promise<T[]> {
  // apps must NOT pass ?published=true here — the editor lists EVERY app
  // (published + unpublished) so the operator can toggle either way.
  const res = await authedFetch(`${apiRoot}/catalog/${kind}`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`load ${kind}: HTTP ${res.status}`)
  }
  const body = await res.json()
  // The catalog list endpoints return either a bare array or {apps:[...]}.
  if (Array.isArray(body)) return body as T[]
  if (body && Array.isArray(body[kind])) return body[kind] as T[]
  if (body && Array.isArray(body.items)) return body.items as T[]
  return []
}

export const listPlans = () => readList<CommercePlan>('plans')
export const listAddOns = () => readList<CommerceAddOn>('addons')
export const listBundles = () => readList<CommerceBundle>('bundles')
export const listIndustries = () => readList<CommerceIndustry>('industries')
export const listApps = () => readList<CommerceApp>('apps')

/* ── Writes (catalyst-api commerce proxy → /catalog/admin/*) ────────── */

async function writeCommerce<T>(
  method: 'POST' | 'PUT' | 'DELETE',
  kind: CommerceKind,
  id: string | undefined,
  body: T | undefined,
): Promise<unknown> {
  const path =
    id && id.length > 0
      ? `${apiRoot}/v1/sme/commerce/${kind}/${encodeURIComponent(id)}`
      : `${apiRoot}/v1/sme/commerce/${kind}`
  const res = await authedFetch(path, {
    method,
    headers: body
      ? { 'Content-Type': 'application/json', Accept: 'application/json' }
      : { Accept: 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`${method} ${kind}: HTTP ${res.status} ${detail}`.trim())
  }
  return res.status === 204 ? null : res.json().catch(() => null)
}

export const createCommerce = <T>(kind: CommerceKind, body: T) =>
  writeCommerce<T>('POST', kind, undefined, body)
export const updateCommerce = <T>(kind: CommerceKind, id: string, body: T) =>
  writeCommerce<T>('PUT', kind, id, body)
export const deleteCommerce = (kind: CommerceKind, id: string) =>
  writeCommerce<undefined>('DELETE', kind, id, undefined)
