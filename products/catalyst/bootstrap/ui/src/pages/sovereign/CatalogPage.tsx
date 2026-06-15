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
import { resolveApplications, type ApplicationDescriptor } from './applicationCatalog'
import type { ApplicationStatus } from './eventReducer'

const DEFAULT_APP_ENVIRONMENT = 'dev'

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
  // `resolveApplications` union AppsPage's Catalog tab rendered).
  const catalogApps = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )

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
            return (
              <AppCard
                key={app.id}
                app={app}
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
