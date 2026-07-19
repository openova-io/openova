/**
 * CatalogPage — the Catalog as its OWN left-nav page (#3601, EPIC #3597).
 *
 * Founder point 2 (2026-06-15): `/apps` had two tabs (Deployments +
 * Catalog) which was confusing. The split is:
 *
 *   • `/apps`    → AppsPage, tab-less, INSTALLED deployments only.
 *   • `/catalog` → THIS page, the catalog grid as its own surface.
 *
 * This page owns the catalog grid that used to be the "Catalog" tab on
 * AppsPage: the full `resolveApplications()` set (bootstrap-kit + selected
 * + transitive deps), each rendered as the SAME `AppCard` AppsPage uses
 * (so no markup drift), with the live publish/status overlay from
 * `GET /api/v1/sovereign/apps`. Clicking a catalog card opens the CLASS
 * page `/catalog/$blueprint` (CatalogDetail), exactly as the old tab did.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the catalog list
 * is computed by `resolveApplications()` from the wizard store + bootstrap
 * kit — there is no hand-maintained id list here.
 */

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { AppCard } from './AppsPage'
import { useNavigate } from '@tanstack/react-router'
import { resolveApplications, type ApplicationDescriptor } from './applicationCatalog'
import type { ApplicationStatus } from './eventReducer'
import { useCatalogAdmin } from '@/shared/lib/useCatalogAdmin'
import { listApps, type CommerceApp } from '@/lib/commerce.api'
import { useCatalog } from '@/lib/useCatalog'
import type { CatalogItem } from '@/lib/catalog.api'

const DEFAULT_APP_ENVIRONMENT = 'dev'

/** Stable empty list so the merge memo doesn't re-run on every render. */
const EMPTY_ITEMS: readonly CatalogItem[] = []

/**
 * #3668 (hw171 catalog walk) — project a live catalog API item into the same
 * `ApplicationDescriptor` shape the grid's `AppCard` consumes. Used ONLY for
 * blueprints the build-time component graph (`componentGroups.ts`) doesn't
 * already cover (e.g. `bp-wordpress`), so a seeded-but-uncatalogued Blueprint
 * still gets a grid card instead of being reachable only by deep-link.
 *
 * Generic over every blueprint (founder rule #4): pure field projection off
 * `spec.card`, no per-blueprint branch. The descriptor's `id` is the canonical
 * `bp-<slug>` so the card links to the CLASS page `/catalog/bp-<slug>` exactly
 * like the static descriptors do.
 */
function catalogItemToDescriptor(it: CatalogItem): ApplicationDescriptor | null {
  const id = it.name?.startsWith('bp-') ? it.name : it.name ? `bp-${it.name}` : ''
  if (!id) return null
  const bareId = id.replace(/^bp-/, '')
  const card = it.card ?? { title: bareId }
  const category = card.category ?? card.family ?? 'application'
  return {
    id,
    bareId,
    title: card.title || bareId,
    description: card.summary ?? card.description ?? card.tagline ?? '',
    familyId: category,
    familyName: category,
    tier: 'optional',
    logoUrl: null,
    dependencies: [],
    bootstrapKit: false,
  }
}

/**
 * #3603 (EPIC #3597) — the admin-editable overlay for one catalog entry,
 * read from the Organization commerce store (the same rows the #3602 read API
 * overlays onto the seed). Keyed by bare slug; drives the card's live name
 * + theme icons and the edit form's initial values.
 */
interface CatalogEditOverlay {
  name: string
  summary: string
  supportedTopologies: string[]
  iconLight: string
  iconDark: string
}

interface CatalogLiveData {
  statusById: Record<string, ApplicationStatus>
  publishedBySlug: Record<string, boolean>
  environmentById: Record<string, string>
  externalURLById: Record<string, string>
}

export function CatalogPage() {
  const params = useResolvedDeploymentId() as { deploymentId: string | null }
  const deploymentId = params.deploymentId ?? ''
  const store = useWizardStore()
  const isSovereignMode = DETECTED_MODE.mode === 'sovereign'

  // Catalog = every Application this Sovereign knows about (the same
  // `resolveApplications` union AppsPage's Catalog tab rendered) — the
  // bootstrap-kit + selected components + their transitive deps, resolved
  // from the build-time component graph.
  const staticApps = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )

  // #3668 (hw171 catalog walk) — the grid must ALSO render every Blueprint
  // the live catalog serves, not only the build-time component graph. Some
  // seeded Blueprints (e.g. `bp-wordpress` and its per-Org variant, the
  // marketplace-funnel CMS) are NOT in `componentGroups.ts` — they ship as
  // catalog-seed CRs (products/catalyst/chart/templates/catalog-seed/
  // blueprints.yaml) but have no wizard-component entry, so they were
  // INVISIBLE in the grid even though their detail page (`/catalog/bp-<x>`,
  // which fetches the CR by name) rendered fully. Per founder rule #4 the
  // catalog renders FROM the catalog (the in-cluster Blueprint CRs), not a
  // hand-curated list — so union the live `/api/v1/catalog` items as extra
  // descriptors for any blueprint the static graph doesn't already cover.
  const catalogQuery = useCatalog({ enabled: isSovereignMode })
  const catalogItems = catalogQuery.data ?? EMPTY_ITEMS
  const catalogIconBySlug = useMemo(() => {
    const m: Record<string, { iconLight: string; iconDark: string }> = {}
    for (const it of catalogItems) {
      const slug = (it.name ?? '').replace(/^bp-/, '')
      if (!slug) continue
      m[slug] = {
        iconLight: it.card?.iconLight ?? '',
        iconDark: it.card?.iconDark ?? '',
      }
    }
    return m
  }, [catalogItems])

  const catalogApps = useMemo<ApplicationDescriptor[]>(() => {
    const seen = new Set(staticApps.map((a) => a.id))
    const extra: ApplicationDescriptor[] = []
    for (const it of catalogItems) {
      const bp = catalogItemToDescriptor(it)
      if (!bp || seen.has(bp.id)) continue
      seen.add(bp.id)
      extra.push(bp)
    }
    return [...staticApps, ...extra]
  }, [staticApps, catalogItems])

  // Live publish/status overlay — identical projection to AppsPage's
  // `sovereign-apps-live` query so a card's INSTALLED / AVAILABLE pill and
  // its Publish toggle read the same ground truth on both surfaces. The
  // queryKey is shared so the two pages hit one cache entry.
  const liveQuery = useQuery<CatalogLiveData>({
    queryKey: ['sovereign-apps-live'],
    enabled: isSovereignMode,
    refetchInterval: 5_000,
    retry: false,
    placeholderData: (prev) => prev,
    queryFn: async () => {
      const r = await authedFetch(`${API_BASE}/v1/sovereign/apps`, {
        headers: { Accept: 'application/json' },
      })
      if (!r.ok) throw new Error(`live apps fetch ${r.status}`)
      const body = (await r.json()) as {
        apps?: Array<{
          id: string
          slug?: string
          status?: string
          marketplacePublished?: boolean
          environment?: string
          externalURL?: string
          instance?: boolean
        }>
      }
      const statusById: Record<string, ApplicationStatus> = {}
      const publishedBySlug: Record<string, boolean> = {}
      const environmentById: Record<string, string> = {}
      const externalURLById: Record<string, string> = {}
      for (const a of body.apps ?? []) {
        if (!a.id) continue
        // Instance rows belong on the Deployments grid, not the catalog
        // (the catalog is the CLASS view).
        if (a.instance === true) continue
        if (typeof a.externalURL === 'string' && a.externalURL !== '') {
          externalURLById[a.id] = a.externalURL
        }
        switch (a.status) {
          case 'installed':
          case 'bootstrap':
            statusById[a.id] = 'installed'
            break
          case 'installing':
            statusById[a.id] = 'installing'
            break
          case 'available':
            statusById[a.id] = 'available'
            break
          default:
            statusById[a.id] = 'pending'
        }
        if (a.slug && typeof a.marketplacePublished === 'boolean') {
          publishedBySlug[a.slug] = a.marketplacePublished
        }
        if (typeof a.environment === 'string' && a.environment !== '') {
          environmentById[a.id] = a.environment
        }
      }
      return { statusById, publishedBySlug, environmentById, externalURLById }
    },
  })
  const liveStatus = liveQuery.data?.statusById ?? {}
  const publishedBySlug = liveQuery.data?.publishedBySlug ?? {}
  const environmentById = liveQuery.data?.environmentById ?? {}
  const externalURLById = liveQuery.data?.externalURLById ?? {}
  const refetchLive = liveQuery.refetch

  // #3603 (EPIC #3597) — admin gate for the per-card Edit affordance. The
  // write path is gated server-side too; this is the matching UI gate.
  const isAdmin = useCatalogAdmin()

  // #3603 — the admin-editable overlay (name / summary / topologies / theme
  // icons) per bare slug, read from the Organization commerce store. Drives the
  // card's live name + icon and the edit form's initial values. Refetched
  // after a save so the card updates without a full reload. Best-effort:
  // when the Organization catalog isn't deployed the list 404s/throws and the map is
  // empty — cards then render their build-time logo + seed name.
  const editsQuery = useQuery<Record<string, CatalogEditOverlay>>({
    queryKey: ['catalog-admin-edits'],
    retry: false,
    staleTime: 30_000,
    queryFn: async () => {
      const apps = await listApps()
      const bySlug: Record<string, CatalogEditOverlay> = {}
      for (const a of apps as CommerceApp[]) {
        const slug = (a.slug ?? '').replace(/^bp-/, '')
        if (!slug) continue
        bySlug[slug] = {
          name: a.name ?? '',
          summary: a.tagline ?? '',
          supportedTopologies: a.supported_topologies ?? [],
          iconLight: a.icon_light ?? '',
          iconDark: a.icon_dark ?? '',
        }
      }
      return bySlug
    },
  })
  const editsBySlug = editsQuery.data ?? {}

  // #3648 (founder item #1) — the popup editor was removed; the admin Edit
  // chip navigates to the catalog detail page, which edits in place.
  const navigate = useNavigate()

  const [query, setQuery] = useState('')

  const visibleApps = useMemo<ApplicationDescriptor[]>(() => {
    const filtered = query
      ? catalogApps.filter((a) => {
          const q = query.toLowerCase()
          return (
            a.title.toLowerCase().includes(q) ||
            (a.description ?? '').toLowerCase().includes(q) ||
            (a.familyName ?? '').toLowerCase().includes(q)
          )
        })
      : catalogApps
    return [...filtered].sort((a, b) => a.title.localeCompare(b.title))
  }, [catalogApps, query])

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Catalog">
      <style>{CATALOG_PAGE_CSS}</style>

      {/* Search */}
      <div className="apps-toolbar">
        <div className="search-wrap">
          <svg className="search-icon" viewBox="0 0 24 24" width={16} height={16} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
            <circle cx={11} cy={11} r={8} />
            <path d="m21 21-4.3-4.3" />
          </svg>
          <input
            type="text"
            placeholder={`Search ${catalogApps.length} apps…`}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="search-input"
            data-testid="sov-catalog-search"
          />
        </div>
      </div>

      {visibleApps.length === 0 ? (
        <div className="empty-state" data-testid="sov-catalog-empty">
          <p className="empty-title">No catalog entries match.</p>
          <p className="empty-sub">Clear the search to see the full catalog.</p>
        </div>
      ) : (
        <div className="apps-grid" data-testid="sov-catalog-grid">
          {visibleApps.map((app) => {
            const slug = app.id.replace(/^bp-/, '')
            const published = Object.prototype.hasOwnProperty.call(publishedBySlug, slug)
              ? publishedBySlug[slug]
              : null
            const environment = environmentById[app.id] ?? DEFAULT_APP_ENVIRONMENT
            const externalURL = externalURLById[app.id] ?? ''
            // #3603 — apply the admin edit (name + summary) onto the card
            // descriptor so a saved rename shows live; the theme icons flow
            // through the dedicated AppCard props below.
            const edit = editsBySlug[slug]
            const cardApp =
              edit && (edit.name || edit.summary)
                ? {
                    ...app,
                    title: edit.name || app.title,
                    description: edit.summary || app.description,
                  }
                : app
            // #3668 — IaC-first icon: the admin commerce-store edit wins
            // (manual override), else the live catalog item's `card.iconLight`/
            // `iconDark` (the seed/Gitea IaC value). Falls through to the
            // descriptor's build-time logo / letter-mark inside AppCard.
            const catalogIcon = catalogIconBySlug[slug]
            const iconLight = edit?.iconLight || catalogIcon?.iconLight
            const iconDark = edit?.iconDark || catalogIcon?.iconDark
            return (
              <AppCard
                key={app.id}
                app={cardApp}
                isCatalog
                environment={environment}
                externalURL={externalURL}
                status={(() => {
                  const live = liveStatus[app.id]
                  if (live) return live
                  if (isSovereignMode && liveQuery.isSuccess) return 'available'
                  return 'pending'
                })()}
                isService={false}
                marketplacePublished={published}
                slug={slug}
                iconLight={iconLight}
                iconDark={iconDark}
                onEdit={
                  isAdmin
                    ? () => {
                        // Chroot-aware: on the mothership provision monitor the
                        // catalog lives under /provision/$deploymentId/…; on the
                        // Sovereign's own console it is the bare /catalog/….
                        const detail = deploymentId
                          ? `/provision/${deploymentId}/catalog/${slug}`
                          : `/catalog/${slug}`
                        void navigate({ to: detail as never })
                      }
                    : undefined
                }
                onPublishedChange={async (next) => {
                  const r = await authedFetch(
                    `${API_BASE}/v1/sovereign/apps/${encodeURIComponent(slug)}/publish`,
                    {
                      method: 'PATCH',
                      headers: {
                        Accept: 'application/json',
                        'Content-Type': 'application/json',
                      },
                      body: JSON.stringify({ published: next }),
                    },
                  )
                  if (!r.ok) throw new Error(`publish toggle failed: ${r.status}`)
                  await refetchLive()
                }}
              />
            )
          })}
        </div>
      )}

      {/* #3648 (founder item #1) — the popup editor was removed. Editing now
          happens inline on the catalog detail page (/catalog/$blueprintName);
          the admin Edit chip above navigates there. */}
    </PortalShell>
  )
}

/**
 * CSS — the catalog grid + search reuse AppsPage's class names verbatim
 * (`.apps-toolbar`, `.search-*`, `.apps-grid`, `.empty-state`). The
 * `.app-card` / `.chip` / `.status-chip` styles ride along with each
 * `AppCard` from AppsPage (AppCard injects no styles itself — the cards
 * render against the page-level rules here), so the catalog cards look
 * byte-identical to the cards the old Catalog tab rendered.
 */
const CATALOG_PAGE_CSS = `
.apps-toolbar { display: flex; gap: 0.75rem; align-items: center; margin: 1rem 0 0.75rem; }
.search-wrap { position: relative; flex: 1; }
.search-icon {
  position: absolute; left: 12px; top: 50%; transform: translateY(-50%);
  color: var(--color-text-dim); opacity: 0.6;
}
.search-input {
  width: 100%;
  padding: 0.6rem 0.85rem 0.6rem 2.2rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  color: var(--color-text);
  font: inherit;
  font-size: 0.88rem;
}
.search-input:focus { outline: 2px solid var(--color-accent); border-color: transparent; }

.apps-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
  gap: 0.65rem;
}

.empty-state { margin-top: 3rem; text-align: center; color: var(--color-text-dim); }
.empty-title { font-size: 1rem; color: var(--color-text-strong); margin: 0 0 0.3rem; font-weight: 600; }
.empty-sub { font-size: 0.85rem; margin: 0 0 1.2rem; }

.app-card {
  position: relative;
  background: var(--color-surface);
  border: 1.5px solid var(--color-border);
  border-radius: 12px;
  padding: 0.6rem;
  display: flex;
  align-items: stretch;
  gap: 0.75rem;
  transition: transform 0.15s, border-color 0.15s, box-shadow 0.15s;
  height: 108px;
  overflow: hidden;
  cursor: pointer;
  color: inherit;
  text-decoration: none;
}
.app-card:hover {
  transform: translateY(-2px);
  border-color: var(--color-accent);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
}
.app-card.state-installed { border-color: color-mix(in srgb, var(--color-success) 45%, var(--color-border)); }
.app-card.state-installing { border-color: color-mix(in srgb, var(--color-accent) 55%, var(--color-border)); }
.app-card.state-failed { border-color: color-mix(in srgb, var(--color-danger) 55%, var(--color-border)); }
.app-card.is-service { border-style: dashed; opacity: 0.9; }
.app-card.is-service:hover { opacity: 1; }

.app-logo {
  align-self: stretch;
  aspect-ratio: 1 / 1;
  height: auto;
  border-radius: 10px;
  object-fit: cover;
  flex-shrink: 0;
}
.app-icon {
  align-self: stretch;
  aspect-ratio: 1 / 1;
  height: auto;
  border-radius: 10px;
  display: inline-flex; align-items: center; justify-content: center;
  flex-shrink: 0; color: #fff; font-size: 1.3rem; font-weight: 700;
}

.app-body {
  flex: 1; min-width: 0;
  display: flex; flex-direction: column; gap: 0.25rem;
  padding-right: 4.5rem;
  overflow: hidden;
}
.app-top { display: flex; align-items: baseline; gap: 0.5rem; }
.app-name {
  color: var(--color-text-strong); font-size: 0.92rem; font-weight: 600; line-height: 1.2;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  flex: 1 1 auto; min-width: 0;
}
.app-cat {
  color: var(--color-text-dim); font-size: 0.68rem; text-transform: capitalize;
  background: color-mix(in srgb, var(--color-border) 50%, transparent);
  padding: 0.1rem 0.4rem; border-radius: 3px;
}
.app-desc {
  margin: 0; color: var(--color-text); font-size: 0.78rem; line-height: 1.45;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}
.app-chips {
  margin-top: 0.25rem;
  display: flex;
  flex-wrap: nowrap;
  gap: 0.25rem;
  overflow: hidden;
  mask-image: linear-gradient(to right, #000 85%, transparent);
  -webkit-mask-image: linear-gradient(to right, #000 85%, transparent);
  min-height: 1.4rem;
}
.chip {
  display: inline-flex; align-items: center; padding: 0.1rem 0.45rem;
  border-radius: 999px; font-size: 0.65rem; font-weight: 600; line-height: 1.4; white-space: nowrap;
}
.chip-free { background: color-mix(in srgb, var(--color-success) 14%, transparent); color: var(--color-success); }
.chip-dep { background: color-mix(in srgb, var(--color-accent) 12%, transparent); color: var(--color-accent); font-weight: 500; }
.chip-env {
  background: color-mix(in srgb, var(--color-text-strong) 12%, transparent);
  color: var(--color-text-strong);
  font-weight: 600;
  text-transform: lowercase;
  letter-spacing: 0.02em;
}

.status-corner { position: absolute; bottom: 0.5rem; right: 0.55rem; display: flex; gap: 0.4rem; align-items: center; }
.open-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.18rem 0.6rem;
  border-radius: 999px;
  border: 1px solid transparent;
  font-size: 0.66rem;
  font-weight: 700;
  letter-spacing: 0.02em;
  line-height: 1.4;
  font-family: inherit;
  cursor: pointer;
  pointer-events: auto;
  background: var(--color-accent);
  color: #fff;
  box-shadow: 0 2px 6px rgba(0, 0, 0, 0.22);
  transition: filter 120ms ease, transform 120ms ease;
}
.open-chip:hover { filter: brightness(0.92); transform: translateY(-1px); }
.open-chip:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; }
.open-chip:disabled { opacity: 0.6; cursor: wait; }
.open-chip svg { display: block; }
/* #3603 — admin "Edit" pill on the catalog card (sovereign-admin only). */
.edit-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.18rem 0.6rem;
  border-radius: 999px;
  border: 1px solid var(--color-border);
  font-size: 0.66rem;
  font-weight: 700;
  letter-spacing: 0.02em;
  line-height: 1.4;
  font-family: inherit;
  cursor: pointer;
  pointer-events: auto;
  background: color-mix(in srgb, var(--color-text-strong) 8%, transparent);
  color: var(--color-text-strong);
  transition: filter 120ms ease, transform 120ms ease, border-color 120ms ease;
}
.edit-chip:hover { border-color: var(--color-accent); transform: translateY(-1px); }
.edit-chip:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; }
.edit-chip svg { display: block; }
.publish-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.15rem 0.55rem;
  border-radius: 999px;
  border: 1px solid transparent;
  font-size: 0.65rem;
  font-weight: 600;
  line-height: 1.4;
  font-family: inherit;
  cursor: pointer;
  pointer-events: auto;
  transition: filter 120ms ease;
}
.publish-chip:hover { filter: brightness(1.15); }
.publish-chip .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; display: inline-block; }
.publish-chip.is-published {
  background: color-mix(in srgb, var(--color-accent) 14%, transparent);
  color: var(--color-accent);
  border-color: color-mix(in srgb, var(--color-accent) 35%, transparent);
}
.publish-chip.is-unpublished {
  background: color-mix(in srgb, var(--color-text-dim) 14%, transparent);
  color: var(--color-text-dim);
  border-color: color-mix(in srgb, var(--color-text-dim) 30%, transparent);
}
.status-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.15rem 0.55rem;
  border-radius: 999px;
  font-size: 0.65rem;
  font-weight: 600;
  line-height: 1.4;
}
.status-chip .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; display: inline-block; }
.status-chip .dot-spin { animation: sov-pulse 1.3s ease-in-out infinite; }
.s-installed { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
.s-installing { background: color-mix(in srgb, var(--color-accent) 16%, transparent); color: var(--color-accent); }
.s-pending { background: color-mix(in srgb, var(--color-text-dim) 16%, transparent); color: var(--color-text-dim); }
.s-available { background: color-mix(in srgb, var(--color-accent) 12%, transparent); color: var(--color-accent); border: 1px solid color-mix(in srgb, var(--color-accent) 40%, transparent); }
.s-failed { background: color-mix(in srgb, var(--color-danger) 16%, transparent); color: var(--color-danger); }

@keyframes sov-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.35; } }
`
