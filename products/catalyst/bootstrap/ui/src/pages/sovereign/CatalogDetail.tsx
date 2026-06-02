import { useParams, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import {
  getCatalogItem,
  listBlueprintInstancesByName,
  type CatalogItem,
  type ApplicationInstanceSummary,
} from '@/lib/catalog.api'

/**
 * G117.2 #2741 — Catalog drill-down (class detail).
 *
 * Route: `/catalog/$blueprintName` (under consoleLayoutRoute).
 *
 * Renders the per-Blueprint CLASS page — header with metadata, list of
 * instances, "+ New instance" action. NOT the per-instance view (that
 * is `/app/$componentId` → AppDetail).
 *
 * Ports the Astro+Svelte scaffold at
 * `products/catalyst/console/src/routes/catalog/[name]/+page.svelte`
 * to the production React UI. The scaffold never reached operators
 * because the catalyst-ui Containerfile copies only `bootstrap/ui/`.
 *
 * Backed by:
 *   GET /api/v1/catalog/{name}                        → CatalogItem
 *   GET /catalyst/v1/catalog/{blueprint}/instances    → BlueprintInstance[]
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

  const instancesQuery = useQuery<ApplicationInstanceSummary[]>({
    queryKey: ['blueprint-instances', name],
    queryFn: () => listBlueprintInstancesByName(name),
    enabled: !!name,
    staleTime: 15_000,
    refetchInterval: 15_000,
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
  const instances = instancesQuery.data ?? []
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

      <section className="instances">
        <header className="instances-header">
          <h3>Instances ({instances.length})</h3>
          {multiInstance ? (
            <Link
              to={'/catalog/$blueprintName/new' as never}
              params={{ blueprintName: name } as never}
              className="btn-new"
              data-testid="btn-new-instance"
            >
              + New instance
            </Link>
          ) : instances.length === 0 ? (
            <Link
              to={'/catalog/$blueprintName/new' as never}
              params={{ blueprintName: name } as never}
              className="btn-new"
              data-testid="btn-install-singleton"
            >
              Install
            </Link>
          ) : (
            <span className="hint">Singleton-per-Org — already installed.</span>
          )}
        </header>

        {instances.length === 0 ? (
          <p className="empty">No instances yet.</p>
        ) : (
          <table data-testid="instance-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Org</th>
                <th>Topology</th>
                <th>Status</th>
                <th>Created</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {instances.map((inst) => (
                <tr key={inst.id} data-testid={`instance-row-${inst.id}`}>
                  <td>
                    <Link
                      to={'/app/$componentId' as never}
                      params={{ componentId: `bp-${inst.blueprint}` } as never}
                      data-testid={`instance-link-${inst.id}`}
                    >
                      {inst.name}
                    </Link>
                  </td>
                  <td>{inst.org}</td>
                  <td><code>{inst.topology}</code></td>
                  <td>
                    <span className={`status status-${inst.status.toLowerCase()}`}>
                      {inst.status}
                    </span>
                  </td>
                  <td>{inst.createdAt ?? ''}</td>
                  <td>
                    <Link
                      to={'/app/$componentId' as never}
                      params={{ componentId: `bp-${inst.blueprint}` } as never}
                      search={{ tab: 'endpoints' } as never}
                    >
                      Endpoints
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
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
