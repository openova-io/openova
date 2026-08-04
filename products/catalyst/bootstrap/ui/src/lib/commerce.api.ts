/**
 * lib/commerce.api.ts — typed client for the Organizations commerce
 * editors (issue #3378 DoD 7/8). Plans / add-ons / bundles / industries /
 * apps, edited over the EXISTING endpoints (§6 — no new business
 * endpoint):
 *
 *   • READ  — the catalyst-api commerce read-proxy that forwards to the
 *     public catalog list endpoints (org_commerce.go HandleOrgCommerceList
 *     → /catalog/{kind}):
 *       GET /api/v1/org/commerce/{plans,addons,bundles,industries,apps}
 *     Reads go through catalyst-api (not a bare /api/catalog/*) because the
 *     Sovereign console host (console.<sovereign>) proxies /api/* to
 *     catalyst-api, which — unlike the Organization/marketplace gateway — does NOT
 *     route /api/catalog/* to the catalog service. A bare GET
 *     /api/catalog/plans therefore 404'd on the console even though the
 *     storefront showed the plan (issue #3378 plans-table 404).
 *   • WRITE — the catalyst-api commerce proxy that forwards to the
 *     superadmin-JWT /catalog/admin/* endpoints (org_commerce.go):
 *       POST   /api/v1/org/commerce/{kind}
 *       PUT    /api/v1/org/commerce/{kind}/{id}
 *       DELETE /api/v1/org/commerce/{kind}/{id}
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
  /**
   * #3603 (EPIC #3597) — the admin-editable catalog fields. Mirror the
   * store.App JSON tags VERBATIM (core/services/catalog/store.go): the
   * legacy single `icon` plus the per-theme `icon_light` / `icon_dark`
   * overrides and the admin-curated `supported_topologies` set. All
   * optional + additive over the seed.
   */
  description?: string
  icon?: string
  icon_light?: string
  icon_dark?: string
  supported_topologies?: string[]
}

export type CommerceKind = 'plans' | 'addons' | 'bundles' | 'industries' | 'apps'

/** API_BASE root — both commerce reads AND writes hit catalyst-api under
 *  /api/v1/org/commerce/*. Reads proxy the public /catalog/{kind} list;
 *  writes proxy the superadmin-JWT /catalog/admin/* endpoints. (See the
 *  file header for why reads must NOT use a bare /api/catalog/* path on the
 *  Sovereign console host — issue #3378 plans-table 404.) */
const apiRoot = `${BASE}api`

/* ── Reads (catalyst-api commerce read-proxy → public catalog list) ─── */

async function readList<T>(kind: CommerceKind): Promise<T[]> {
  // apps must NOT pass ?published=true here — the editor lists EVERY app
  // (published + unpublished) so the operator can toggle either way.
  const res = await authedFetch(`${apiRoot}/v1/org/commerce/${kind}`, {
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
      ? `${apiRoot}/v1/org/commerce/${kind}/${encodeURIComponent(id)}`
      : `${apiRoot}/v1/org/commerce/${kind}`
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

/* ── #3603 (EPIC #3597) — catalog entry edit ───────────────────────── */

/** The mutable subset of a catalog entry the #3603 edit form changes. */
export interface CatalogEntryEdit {
  name: string
  tagline: string
  supported_topologies: string[]
  icon_light: string
  icon_dark: string
}

/**
 * CatalogEntryPatch — the fields ONE per-field inline save changes (#5510).
 *
 * A per-field save must carry ONLY the keys the operator actually edited. A
 * key that is ABSENT means "leave whatever the store holds alone"; a key that
 * is PRESENT-but-empty means "the operator cleared this field" and is written.
 * `undefined` is therefore load-bearing — never spread a full 5-field record
 * into a patch to "keep the shape regular".
 */
export type CatalogEntryPatch = Partial<CatalogEntryEdit>

/**
 * toStoreColumns — project a CatalogEntryPatch onto the store's column names,
 * carrying ONLY the keys the patch actually declares (#5510).
 *
 * This is the whole point of the patch type: the returned object is spread
 * over the authoritative store row, so a key we omit here is a column the
 * store keeps. Written key-by-key on purpose — a generic key loop would need
 * a cast to satisfy the per-key value types, and a cast here is exactly how a
 * stray `undefined` would sneak back into the wire body.
 */
function toStoreColumns(patch: CatalogEntryPatch): Partial<CommerceApp> {
  const columns: Partial<CommerceApp> = {}
  if (patch.name !== undefined) columns.name = patch.name
  if (patch.tagline !== undefined) columns.tagline = patch.tagline
  if (patch.supported_topologies !== undefined) {
    columns.supported_topologies = patch.supported_topologies
  }
  if (patch.icon_light !== undefined) columns.icon_light = patch.icon_light
  if (patch.icon_dark !== undefined) columns.icon_dark = patch.icon_dark
  return columns
}

/**
 * CatalogSaveVerdict — the #3668/#5113 dual-write verdict for one catalog
 * card-save. The catalyst-api `apps` create/update proxy wraps the upstream
 * store row in a `{stored, committed, reason}` envelope
 * (writeCatalogEditEnvelope in handler/org_commerce.go, honest since #5115):
 *   • stored    — the commerce store (the cache) accepted the edit.
 *   • committed — the IaC source-of-truth git commit (catalog-sovereign
 *     Gitea) durably landed AND its Flux reconcile leg is armed. `null`
 *     when the server relayed a legacy (pre-envelope) body — the UI then
 *     reports the verdict as unavailable rather than fabricating a green.
 *   • reason    — the server's why, when the commit leg failed.
 */
export interface CatalogSaveVerdict {
  slug: string
  stored: boolean
  committed: boolean | null
  reason?: string
}

/** parseCatalogEditEnvelope — fold a commerce `apps` write response body
 *  onto the CatalogSaveVerdict. Tolerates the legacy raw-row shape (no
 *  envelope) by returning committed:null — verdict unknown, never a
 *  fabricated `committed:true`. */
export function parseCatalogEditEnvelope(body: unknown, slug: string): CatalogSaveVerdict {
  if (body && typeof body === 'object' && typeof (body as { committed?: unknown }).committed === 'boolean') {
    const env = body as { stored?: unknown; committed: boolean; reason?: unknown }
    return {
      slug,
      stored: env.stored !== false,
      committed: env.committed,
      reason: typeof env.reason === 'string' && env.reason ? env.reason : undefined,
    }
  }
  return { slug, stored: true, committed: null }
}

/**
 * saveCatalogEdit persists ONE per-field catalog-entry edit to the
 * Organization commerce catalog store.
 *
 * ── #5510: this is a PARTIAL PATCH, not a whole-record replace ──────────
 *
 * The wire endpoint cannot express a partial patch: `PUT /catalog/admin/apps/
 * {id}` decodes the body into a `store.App` and `Store.UpdateApp` issues a
 * full `$set` of every column (core/services/catalog/store/store.go), so
 * whatever the body omits is written as its zero value. Partial-patch
 * SEMANTICS are therefore implemented here, client-side, as read-modify-write
 * against the authoritative store row:
 *
 *   1. list every store App and find the row whose slug matches this
 *      catalog entry (slugs are bare — the catalog id's `bp-` prefix is
 *      stripped by the caller);
 *   2. if it exists, overlay ONLY the keys `patch` declares onto the FULL
 *      existing row (the runtime object still carries every store column
 *      even though the CommerceApp type only declares a few) and PUT it back
 *      by its `_id`, so no column is lost;
 *   3. if no row exists yet (a seed-only catalog entry the commerce store
 *      never had), POST a new row from `createSeed` + the patch — there is
 *      no stored value to preserve on this path, so seeding the row with the
 *      values the page currently renders keeps the create non-destructive.
 *
 * The bug this shape fixes: the caller used to build a FULL 5-field record
 * from the IaC card and PUT `{...existing, ...allFiveFields}`, so every field
 * the store held but the IaC lacked was overwritten with an IaC-derived
 * default on the next per-field save — a Summary-only save silently reverted
 * `name` and `icon_light`, with a green toast and HTTP 200. The same body is
 * also what catalyst-api commits to git (commitCatalogAppEditToGit decodes
 * these exact JSON tags), so a fat body destroyed the value in BOTH legs.
 * Passing only the edited keys removes the class in both.
 *
 * Returns the CatalogSaveVerdict (#5113 facet-a / UAT row 132): the caller
 * surfaces "Saved to IaC ✓" vs "Saved to store — IaC commit failed: …"
 * instead of closing silently.
 *
 * @param slug       catalog entry id (`bp-`-prefixed or bare).
 * @param patch      ONLY the fields this save edits (#5510).
 * @param createSeed values to seed a brand-new store row with, used ONLY
 *                  when no row exists yet. Never applied over a stored value.
 */
export async function saveCatalogEdit(
  slug: string,
  patch: CatalogEntryPatch,
  createSeed: CatalogEntryPatch = {},
): Promise<CatalogSaveVerdict> {
  const bare = slug.replace(/^bp-/, '')
  const columns = toStoreColumns(patch)
  const apps = await listApps()
  const existing = apps.find((a) => (a.slug ?? '').replace(/^bp-/, '') === bare)

  if (existing && existing.id) {
    // Overlay the edited keys onto the full runtime row: the untouched
    // columns keep their STORED values through the full-$set UpdateApp.
    const merged: CommerceApp = { ...existing, ...columns }
    const body = await updateCommerce('apps', existing.id, merged)
    return parseCatalogEditEnvelope(body, bare)
  }

  // No store row yet — create one from the seed + the edit. slug is required
  // by the store; name falls back to the seed, then to the slug when neither
  // the edit nor the seed names the entry.
  const created: CommerceApp = {
    ...toStoreColumns(createSeed),
    ...columns,
    slug: bare,
    name: patch.name ?? createSeed.name ?? bare,
  }
  const body = await createCommerce<CommerceApp>('apps', created)
  return parseCatalogEditEnvelope(body, bare)
}
