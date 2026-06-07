import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { getCatalogItem, type CatalogItem } from '@/lib/catalog.api'
import { InstancesSection } from './AppDetail/InstancesSection'

/**
 * G117.2 #2741 — Catalog drill-down (class detail).
 *
 * Route: `/catalog/$blueprintName` (under consoleLayoutRoute) + the
 * `/provision/$deploymentId/catalog/$blueprintName` chroot/provision
 * variant.
 *
 * Renders the per-Blueprint CLASS page — header with metadata, supported
 * topologies, the list of installed instances, and a "+ New instance"
 * action. NOT the per-instance view (that is `/app/$componentId` →
 * AppDetail).
 *
 * #3090: this page was registered but ORPHANED — every app card (Catalog
 * AND Deployments tab) hard-linked to `/app/$id`, so the class page was
 * never reached and the instance page wrongly rendered the class
 * instances-list + "+ New instance" button. AppsPage now links Catalog
 * cards here. The instances list + New-instance dialog are the shared
 * `InstancesSection` (lifted out of AppDetail) so the class behaviour
 * lives in exactly one place; the "+ New instance" button opens the
 * topology-picker dialog INLINE (no navigation) — the previous
 * `/catalog/$blueprintName/new` link had no route and 404'd.
 *
 * Backed by:
 *   GET /api/v1/catalog/{name}                        → CatalogItem
 *   GET /catalyst/v1/catalog/{blueprint}/instances    → BlueprintInstance[]
 *        (fetched inside InstancesSection)
 */
export function CatalogDetail() {
  const { blueprintName } = useParams({ strict: false }) as { blueprintName?: string }
  const name = (blueprintName ?? '').replace(/^bp-/, '')

  const catalogQuery = useQuery<CatalogItem>({
    queryKey: ['catalog-item', name],
    queryFn: () => getCatalogItem(name),
    enabled: !!name,
    staleTime: 30_000,
    retry: 1,
  })

  if (!name) {
    return (
      <section className="catalog-drilldown" data-testid="catalog-drilldown">
        <p className="error" data-testid="catalog-error">missing blueprint name</p>
      </section>
    )
  }

  if (catalogQuery.isLoading) {
    return (
      <section className="catalog-drilldown" data-testid="catalog-drilldown" data-blueprint={name}>
        <p data-testid="catalog-loading">Loading {name}…</p>
      </section>
    )
  }

  if (catalogQuery.isError) {
    return (
      <section className="catalog-drilldown" data-testid="catalog-drilldown" data-blueprint={name}>
        <p className="error" data-testid="catalog-error">
          {(catalogQuery.error as Error)?.message ?? 'load failed'}
        </p>
      </section>
    )
  }

  const cat = catalogQuery.data!
  const multiInstance = readMultiInstance(cat)
  const topologies = readTopologies(cat)
  const defaultTopology = readDefaultTopology(cat)

  return (
    <section
      className="catalog-drilldown"
      data-testid="catalog-drilldown"
      data-blueprint={name}
    >
      <header className="bp-header">
        <div>
          <h2 data-testid="catalog-title">{cat.card?.title ?? name}</h2>
          <p className="desc">{cat.card?.description ?? ''}</p>
        </div>
        <div className="badges">
          <span className="badge" data-testid="catalog-version">v{cat.version}</span>
          {cat.card?.family ? (
            <span className="badge" data-testid="catalog-family">{cat.card.family}</span>
          ) : null}
          <span
            className="badge"
            data-testid={multiInstance ? 'badge-multi-instance' : 'badge-singleton'}
          >
            {multiInstance ? 'multi-instance' : 'singleton-per-Org'}
          </span>
        </div>
      </header>

      {topologies.length > 0 ? (
        <section className="topologies">
          <h3>Supported topologies</h3>
          <ul>
            {topologies.map((t) => (
              <li key={t}>
                <code>{t}</code>
                {t === defaultTopology ? <em> (default)</em> : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {/* #3090 — shared CLASS-page instances list + "+ New instance"
          (inline topology-picker dialog). Instance rows link to the
          INSTANCE page `/app/$componentId`. */}
      <InstancesSection blueprint={`bp-${name}`} />
    </section>
  )
}

function readMultiInstance(cat: CatalogItem): boolean {
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const mi = spec?.multiInstance as { enabled?: boolean } | undefined
  return !!mi?.enabled
}

function readTopologies(cat: CatalogItem): string[] {
  const modes = cat.placementSchema?.modes
  if (Array.isArray(modes) && modes.length > 0) return modes
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const topology = spec?.topology as { supported?: string[] } | undefined
  return topology?.supported ?? []
}

function readDefaultTopology(cat: CatalogItem): string {
  const def = cat.placementSchema?.default
  if (typeof def === 'string' && def) return def
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const topology = spec?.topology as { default?: string } | undefined
  return topology?.default ?? ''
}
