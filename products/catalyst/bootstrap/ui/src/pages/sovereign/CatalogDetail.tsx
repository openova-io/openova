import { useState } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import { getCatalogItem, getApplication, type CatalogItem } from '@/lib/catalog.api'
import { findComponent } from '@/pages/wizard/steps/componentGroups'
import { BLUEPRINT_BY_ID } from '@/shared/constants/catalog.generated'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { InstancesSection } from './AppDetail/InstancesSection'
import { useCatalogAdmin } from '@/shared/lib/useCatalogAdmin'
import { CatalogEditForm } from './CatalogEditForm'

/**
 * CatalogDetail — the per-Blueprint CLASS page.
 *
 * Route: `/catalog/$blueprintName` (under consoleLayoutRoute) + the
 * `/provision/$deploymentId/catalog/$blueprintName` chroot/provision
 * variant.
 *
 * #3165 (2026-06-09) — rebuilt as a first-class, INSTANCES-CENTERED
 * console page. The prior version was a bare stub (thin header + a
 * topology `<ul>` + InstancesSection) that read as unstyled/standalone
 * next to the rest of the console. This rebuild adopts the AppDetail
 * design language verbatim (the `.hero` / `.section` / `.overview-grid`
 * / `.chip` classes, sourced from the shared CATALOG_DETAIL_CSS below
 * which mirrors AppDetail's APP_DETAIL_CSS) so the class page feels
 * integrated:
 *
 *   • Breadcrumb  ‹ Catalog › <Title>
 *   • Hero header — icon, title, version + category/family + singleton/
 *     multi-instance badges, summary/description, docs link, license,
 *     tag chips, and a prominent "+ New instance" action.
 *   • Instances table = the page BODY (the centerpiece, via the shared
 *     InstancesSection). Each row drills into the INSTANCE page
 *     `/app/$componentId`.
 *   • Supported topologies (default marked) + per-topology placement.
 *   • Bundled dependencies (from the blueprint spec).
 *   • Empty state for multi-instance Blueprints with no installs yet.
 *   • Bootstrap/singleton handling — platform components installed as
 *     HelmReleases (grafana/harbor/openbao/gitea/keycloak) have no
 *     per-tenant Application CR, so the instances list is empty by
 *     design; we surface a link to the existing bootstrap instance and
 *     label it "platform component (singleton)" rather than showing an
 *     empty table + an inapplicable "create instance" prompt.
 *
 * This is the CLASS page, NOT the per-instance view (that is
 * `/app/$componentId` → AppDetail).
 *
 * Backed by:
 *   GET /api/v1/catalog/{name}                                → CatalogItem
 *   GET /api/v1/sovereigns/{id}/applications/bp-{name}        → bootstrap detect
 *   GET /catalyst/v1/catalog/{blueprint}/instances            → BlueprintInstance[]
 *        (fetched inside InstancesSection)
 */
export function CatalogDetail() {
  const params = useParams({ strict: false }) as {
    blueprintName?: string
    deploymentId?: string
  }
  const name = (params.blueprintName ?? '').replace(/^bp-/, '')
  const { deploymentId } = useResolvedDeploymentId()
  const isAdmin = useCatalogAdmin()
  const qc = useQueryClient()
  // #3648 (founder item #1) — inline edit-in-place on the detail page,
  // replacing the separate edit chip + modal on the catalog grid.
  const [editing, setEditing] = useState(false)

  // Chroot-aware "back to Catalog" target. On the mothership provision
  // monitor (`/provision/$deploymentId/...`) the apps grid is at
  // `/provision/$deploymentId`; on the Sovereign's own console it is the
  // clean `/apps`. Mirrors AppCard's per-tab chroot scoping so the
  // breadcrumb never escapes to a non-existent route.
  const depParam = params.deploymentId ?? ''
  const catalogHomeTarget =
    DETECTED_MODE.mode === 'sovereign' || !depParam
      ? '/apps'
      : `/provision/${depParam}`

  const catalogQuery = useQuery<CatalogItem>({
    queryKey: ['catalog-item', name],
    queryFn: () => getCatalogItem(name),
    enabled: !!name,
    staleTime: 30_000,
    retry: 1,
  })

  // Bootstrap/singleton detection. Platform components (grafana, harbor,
  // openbao, gitea, keycloak …) install as bare HelmReleases with no
  // per-tenant Application CR, so the /catalog/{bp}/instances list is
  // empty for them. getApplication("bp-<name>") returns `bootstrap:true`
  // + the live phase/externalURL of the singleton install — we use it to
  // surface a link to the running instance instead of an empty table.
  // Returns null on 404 (no install / not a bootstrap app) — harmless.
  const bootstrapQuery = useQuery({
    queryKey: ['catalog-bootstrap', deploymentId, name],
    queryFn: () => getApplication(deploymentId ?? '', `bp-${name}`),
    enabled: !!name && !!deploymentId,
    staleTime: 30_000,
    retry: 1,
  })

  if (!name) {
    return (
      <section className="catalog-drilldown" data-testid="catalog-drilldown">
        <style>{CATALOG_DETAIL_CSS}</style>
        <div className="not-found" data-testid="catalog-error">
          <h1>Blueprint not found</h1>
          <p>No blueprint name was provided.</p>
        </div>
      </section>
    )
  }

  if (catalogQuery.isLoading) {
    return (
      <section
        className="catalog-drilldown"
        data-testid="catalog-drilldown"
        data-blueprint={name}
      >
        <style>{CATALOG_DETAIL_CSS}</style>
        <p className="section-hint" data-testid="catalog-loading">
          Loading {name}…
        </p>
      </section>
    )
  }

  if (catalogQuery.isError) {
    return (
      <section
        className="catalog-drilldown"
        data-testid="catalog-drilldown"
        data-blueprint={name}
      >
        <style>{CATALOG_DETAIL_CSS}</style>
        <div className="not-found" data-testid="catalog-error">
          <h1>Couldn’t load {name}</h1>
          <p>{(catalogQuery.error as Error)?.message ?? 'load failed'}</p>
        </div>
      </section>
    )
  }

  const cat = catalogQuery.data!
  const card = cat.card ?? { title: name }
  const title = card.title || name
  const multiInstance = readMultiInstance(cat)
  const topologies = readTopologies(cat)
  const defaultTopology = readDefaultTopology(cat)
  const perTopology = readPerTopologyPlacement(cat)
  const deps = readDependencies(cat)
  const tags = Array.isArray(card.tags) ? card.tags : []

  // #3370 — shareability declaration from the build-time catalog (the
  // same blueprint.yaml truth the catalog-seed locks against). Drives
  // the `shareable` badge; one generic mechanism for every blueprint.
  const generated = BLUEPRINT_BY_ID[`bp-${name}`]
  const shareable =
    generated?.shareable === true ||
    (cat.raw as { spec?: { shareable?: boolean } } | undefined)?.spec?.shareable === true
  const contextKind =
    generated?.contextSchema?.kind ??
    (cat.raw as { spec?: { contextSchema?: { kind?: string } } } | undefined)?.spec
      ?.contextSchema?.kind ??
    ''

  // Icon: reuse the same resolution as AppDetail / AppsPage — the
  // component-catalog `logoUrl` (a real public/ asset URL) keyed by the
  // bare blueprint id. `card.icon` from the API is only a bare SVG
  // filename with no console-resolvable base, so we don't render it as a
  // src; the letter-mark fallback covers components without a logo.
  const logoUrl = findComponent(name)?.logoUrl ?? null

  const bootstrapApp = bootstrapQuery.data
  const isBootstrapSingleton = !!bootstrapApp?.bootstrap

  return (
    <section
      className="catalog-drilldown detail-page"
      data-testid="catalog-drilldown"
      data-blueprint={name}
    >
      <style>{CATALOG_DETAIL_CSS}</style>

      {/* Breadcrumb */}
      <nav className="catalog-breadcrumb" aria-label="Breadcrumb" data-testid="catalog-breadcrumb">
        <Link to={catalogHomeTarget as never} className="crumb-link">
          ‹ Catalog
        </Link>
        <span className="crumb-sep">›</span>
        <span className="crumb-current">{title}</span>
      </nav>

      {/* Hero header */}
      <div className="hero" data-testid="catalog-hero">
        {logoUrl ? (
          <img src={logoUrl} alt={title} className="hero-logo" loading="lazy" />
        ) : (
          <span className="hero-icon" style={{ background: '#1f2937' }}>
            {title[0] ?? '?'}
          </span>
        )}
        <div className="hero-body">
          <h1>
            <span data-testid="catalog-title">{title}</span>
          </h1>
          {isAdmin && !editing ? (
            <button
              type="button"
              data-testid="catalog-detail-edit"
              onClick={() => setEditing(true)}
              title="Edit this catalog entry"
              style={{
                alignSelf: 'flex-start',
                marginTop: '0.15rem',
                padding: '0.3rem 0.8rem',
                borderRadius: '8px',
                border: '1px solid var(--color-border)',
                background: 'transparent',
                color: 'var(--color-text)',
                fontSize: '0.8rem',
                fontWeight: 600,
                cursor: 'pointer',
              }}
            >
              Edit
            </button>
          ) : null}
          {card.tagline || card.summary ? (
            <p className="hero-tagline">{card.tagline || card.summary}</p>
          ) : null}
          <div className="hero-meta">
            <span className="chip chip-bp" data-testid="catalog-version">
              v{cat.version}
            </span>
            {card.category ? (
              <span className="chip chip-cat" data-testid="catalog-category">
                {card.category}
              </span>
            ) : null}
            {card.family ? (
              <span className="chip chip-cat" data-testid="catalog-family">
                {card.family}
              </span>
            ) : null}
            <span
              className="chip chip-ns"
              data-testid={multiInstance ? 'badge-multi-instance' : 'badge-singleton'}
              title={
                multiInstance
                  ? 'Multiple instances of this Blueprint can run per Organization'
                  : 'One instance of this Blueprint per Organization'
              }
            >
              {multiInstance ? 'multi-instance' : 'singleton-per-Org'}
            </span>
            {shareable ? (
              <span
                className="chip chip-installed"
                data-testid="badge-shareable"
                title={`Instances support multi-application reuse: one instance serves many consumer applications, each through its own Context${contextKind ? ` (${contextKind})` : ''}`}
              >
                ⛓ shareable{contextKind ? ` · ${contextKind}` : ''}
              </span>
            ) : null}
            {isBootstrapSingleton ? (
              <span
                className="chip chip-free"
                data-testid="badge-platform-component"
                title="Installed at bootstrap as a platform HelmRelease (no per-tenant Application CR)"
              >
                platform component
              </span>
            ) : null}
            {card.license ? (
              <span className="chip chip-cat" data-testid="catalog-license">
                {card.license}
              </span>
            ) : null}
          </div>
          {tags.length > 0 ? (
            <div className="hero-tags" data-testid="catalog-tags">
              {tags.map((t) => (
                <span key={t} className="chip chip-bp" data-testid={`catalog-tag-${t}`}>
                  {t}
                </span>
              ))}
            </div>
          ) : null}
          {card.docs ? (
            <p className="hero-docs">
              <a
                href={card.docs}
                target="_blank"
                rel="noopener noreferrer"
                className="external-url-link"
                data-testid="catalog-docs-link"
              >
                Documentation
                <svg
                  viewBox="0 0 24 24"
                  width={11}
                  height={11}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={2.2}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  aria-hidden="true"
                  style={{ marginLeft: '0.3rem', verticalAlign: 'middle' }}
                >
                  <path d="M14 3h7v7" />
                  <path d="M10 14L21 3" />
                  <path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5" />
                </svg>
              </a>
            </p>
          ) : null}
        </div>
      </div>

      {/* #3648 (founder item #1) — inline edit-in-place panel. Replaces the
          separate edit chip + modal on the catalog grid: the operator edits
          the entry on its own page, where it is already shown. */}
      {editing ? (
        <section className="section" data-testid="catalog-edit-section">
          <h2>Edit catalog entry</h2>
          <CatalogEditForm
            blueprintId={`bp-${name}`}
            initial={{
              name: title,
              summary: card.tagline || card.summary || '',
              supportedTopologies: topologies.map(toEditorMode),
              iconLight: logoUrl ?? '',
              iconDark: '',
            }}
            onSaved={() => {
              setEditing(false)
              void qc.invalidateQueries({ queryKey: ['catalog-item', name] })
            }}
            onCancel={() => setEditing(false)}
          />
        </section>
      ) : null}

      {/* About */}
      {card.description ? (
        <section className="section" data-testid="catalog-section-about">
          <h2>About</h2>
          <p className="desc" data-testid="catalog-description">
            {card.description}
          </p>
        </section>
      ) : null}

      {/*
        Instances — the page BODY (centerpiece). For multi-instance and
        ordinary singleton-per-Org Blueprints this is the shared
        InstancesSection (list + "+ New instance" topology-picker dialog,
        inline — no navigation). For bootstrap platform components the
        list is empty by design; we surface a link to the running
        singleton install instead of an empty table + an inapplicable
        "create instance" prompt.
      */}
      {isBootstrapSingleton ? (
        <section
          className="section"
          data-testid="catalog-section-platform-singleton"
        >
          <h2>Instance</h2>
          <p className="section-hint">
            {title} is a platform component — it ships with the Sovereign as a
            singleton HelmRelease, so there is no per-Organization instance to
            create. Open the running install below.
          </p>
          <div className="singleton-card" data-testid="catalog-singleton-card">
            <div className="singleton-meta">
              <Link
                to={'/app/$componentId' as never}
                params={{ componentId: `bp-${name}` } as never}
                className="external-url-link"
                data-testid="catalog-singleton-link"
              >
                <code>bp-{name}</code>
              </Link>
              <span className="chip chip-cat" data-testid="catalog-singleton-namespace">
                {bootstrapApp?.namespace ?? name}
              </span>
              {bootstrapApp?.phase ? (
                <span
                  className={`chip ${
                    bootstrapApp.phase === 'Ready'
                      ? 'chip-installed'
                      : bootstrapApp.phase === 'Failed' || bootstrapApp.phase === 'Degraded'
                        ? 'chip-failed'
                        : 'chip-pending'
                  }`}
                  data-testid="catalog-singleton-phase"
                >
                  {bootstrapApp.phase}
                </span>
              ) : null}
            </div>
            <Link
              to={'/app/$componentId' as never}
              params={{ componentId: `bp-${name}` } as never}
              className="btn btn-primary singleton-open"
              data-testid="catalog-singleton-open"
            >
              Open instance →
            </Link>
          </div>
        </section>
      ) : (
        // #3090 / #3165 — shared CLASS-page instances list + "+ New
        // instance" (inline topology-picker dialog). Instance rows link
        // to the INSTANCE page `/app/$componentId`.
        <InstancesSection blueprint={`bp-${name}`} multiInstance={multiInstance} />
      )}

      {/* Supported topologies */}
      {topologies.length > 0 ? (
        <section className="section" data-testid="catalog-section-topologies">
          <h2>Supported topologies</h2>
          <dl className="overview-grid" data-testid="catalog-topologies">
            {topologies.map((t) => {
              const placement = perTopology[t]
              return (
                <div className="overview-row" key={t} data-testid={`catalog-topology-${t}`}>
                  <dt>
                    {t}
                    {t === defaultTopology ? (
                      <span
                        className="chip chip-installed"
                        data-testid={`catalog-topology-default-${t}`}
                        style={{ marginLeft: '0.4rem' }}
                      >
                        default
                      </span>
                    ) : null}
                  </dt>
                  <dd>
                    {placement ? (
                      <span data-testid={`catalog-topology-placement-${t}`}>{placement}</span>
                    ) : (
                      <span className="section-hint" style={{ margin: 0 }}>
                        —
                      </span>
                    )}
                  </dd>
                </div>
              )
            })}
          </dl>
        </section>
      ) : null}

      {/* Bundled dependencies */}
      {deps.length > 0 ? (
        <section className="section" data-testid="catalog-section-deps">
          <h2>Bundled dependencies</h2>
          <p className="section-hint">Auto-installed alongside {title}:</p>
          <ul className="dep-list" data-testid="catalog-deps">
            {deps.map((d) => {
              const friendly = findComponent(d.replace(/^bp-/, ''))?.name ?? d
              return (
                <li key={d} data-testid={`catalog-dep-${d}`}>
                  {friendly}
                </li>
              )
            })}
          </ul>
        </section>
      ) : null}
    </section>
  )
}

/* ─── spec readers ───────────────────────────────────────────────── */

function readMultiInstance(cat: CatalogItem): boolean {
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const mi = spec?.multiInstance as { enabled?: boolean } | undefined
  return !!mi?.enabled
}

// #3648 — map BOTH the canonical matrix vocabulary (singleton /
// active-hot-standby) AND the editor dialect onto the editor dialect
// (single-region / active-hotstandby / active-active) the inline edit form's
// topology checkboxes use, so a Blueprint's declared topologies pre-check
// correctly regardless of which spelling the API returned.
const CANONICAL_TO_EDITOR: Record<string, string> = {
  singleton: 'single-region',
  'active-hot-standby': 'active-hotstandby',
  'active-active': 'active-active',
}
function toEditorMode(raw: string): string {
  const s = raw.trim().toLowerCase()
  return CANONICAL_TO_EDITOR[s] ?? s
}

function readTopologies(cat: CatalogItem): string[] {
  const modes = cat.placementSchema?.modes
  if (Array.isArray(modes) && modes.length > 0) return modes
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const topology = spec?.topology as { supported?: string[] } | undefined
  return Array.isArray(topology?.supported) ? topology!.supported! : []
}

function readDefaultTopology(cat: CatalogItem): string {
  const def = cat.placementSchema?.default
  if (typeof def === 'string' && def) return def
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const topology = spec?.topology as
    | { default?: string; defaults?: Record<string, string> }
    | undefined
  if (typeof topology?.default === 'string' && topology.default) return topology.default
  // Blueprint shape uses `defaults: { single-region, multi-region }` —
  // prefer the single-region default as the "default" marker.
  const defs = topology?.defaults
  return defs?.['single-region'] ?? defs?.['multi-region'] ?? ''
}

/**
 * readPerTopologyPlacement — flatten `spec.topology.perTopology[t].placement`
 * into a human-readable one-liner per topology (e.g.
 * "mgmt: mgmt-A (active), mgmt-B (passive)"). Returns {} when the
 * blueprint declares no per-topology placement.
 */
function readPerTopologyPlacement(cat: CatalogItem): Record<string, string> {
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const topology = spec?.topology as { perTopology?: Record<string, unknown> } | undefined
  const per = topology?.perTopology
  const out: Record<string, string> = {}
  if (!per || typeof per !== 'object') return out
  for (const [topo, val] of Object.entries(per)) {
    const placement = (val as { placement?: unknown } | undefined)?.placement as
      | { tier?: string; clusters?: string[]; roles?: Record<string, string> }
      | undefined
    if (!placement) continue
    const clusters = Array.isArray(placement.clusters) ? placement.clusters : []
    const roles = placement.roles ?? {}
    const clusterStr = clusters
      .map((c) => (roles[c] ? `${c} (${roles[c]})` : c))
      .join(', ')
    const tierPrefix = placement.tier ? `${placement.tier}: ` : ''
    const summary = `${tierPrefix}${clusterStr}`.trim()
    if (summary) out[topo] = summary
  }
  return out
}

/**
 * readDependencies — bundled dependency blueprint ids from the spec.
 * Tolerant of both the `[string]` and `[{name}]` shapes the various
 * blueprint authors use, and de-dupes.
 */
function readDependencies(cat: CatalogItem): string[] {
  const raw = cat.raw as Record<string, unknown> | undefined
  const spec = (raw?.spec as Record<string, unknown> | undefined) ?? undefined
  const fromSpec = spec?.dependencies ?? (raw?.dependencies as unknown)
  const list = Array.isArray(fromSpec) ? fromSpec : []
  const ids = list
    .map((d) => {
      if (typeof d === 'string') return d
      if (d && typeof d === 'object') {
        const o = d as { name?: string; blueprint?: string; ref?: string }
        return o.name ?? o.blueprint ?? o.ref ?? ''
      }
      return ''
    })
    .filter((s): s is string => typeof s === 'string' && s.length > 0)
  return Array.from(new Set(ids))
}

/* ─── CSS — mirrors AppDetail's APP_DETAIL_CSS so the class page shares
 *      the same `.hero` / `.section` / `.chip` / `.overview-grid` design
 *      language. The class page lives under consoleLayoutRoute (the
 *      sidebar chrome is provided by the parent route), so we only inject
 *      the in-page detail styles here, not the PortalShell wrapper. ──── */
const CATALOG_DETAIL_CSS = `
.detail-page { max-width: 860px; margin: 0 auto; padding: 1rem 0 4rem; }

.catalog-breadcrumb {
  display: flex; align-items: center; gap: 0.4rem;
  font-size: 0.82rem; color: var(--color-text-dim); margin-bottom: 0.6rem;
}
.catalog-breadcrumb .crumb-link { color: var(--color-text-dim); text-decoration: none; }
.catalog-breadcrumb .crumb-link:hover { color: var(--color-text-strong); }
.catalog-breadcrumb .crumb-sep { color: var(--color-text-dim); }
.catalog-breadcrumb .crumb-current { color: var(--color-text-strong); font-weight: 600; }

.not-found { text-align: center; padding: 4rem 0; color: var(--color-text-dim); }
.not-found h1 { color: var(--color-text-strong); font-size: 1.4rem; margin-bottom: 1rem; }

.hero {
  display: flex; align-items: flex-start; gap: 1.1rem;
  padding: 1.4rem 0; border-bottom: 1px solid var(--color-border);
}
.hero-logo { width: 80px; height: 80px; border-radius: 18px; object-fit: contain; background: var(--color-surface); padding: 8px; flex-shrink: 0; border: 1px solid var(--color-border); }
.hero-icon {
  width: 80px; height: 80px; border-radius: 18px;
  display: inline-flex; align-items: center; justify-content: center;
  color: #fff; font-size: 1.8rem; font-weight: 700; flex-shrink: 0;
}
.hero-body { flex: 1; min-width: 0; }
.hero-body h1 { margin: 0; color: var(--color-text-strong); font-size: 1.4rem; font-weight: 700; }
.hero-tagline { margin: 0.25rem 0 0.6rem; color: var(--color-text-dim); font-size: 0.9rem; }
.hero-meta { display: flex; gap: 0.4rem; flex-wrap: wrap; align-items: center; }
.hero-tags { display: flex; gap: 0.35rem; flex-wrap: wrap; margin-top: 0.55rem; }
.hero-docs { margin: 0.6rem 0 0; font-size: 0.85rem; }

.chip { display: inline-flex; align-items: center; gap: 0.3rem; padding: 0.18rem 0.55rem; border-radius: 999px; font-size: 0.7rem; font-weight: 600; white-space: nowrap; }
.chip-cat { background: color-mix(in srgb, var(--color-border) 50%, transparent); color: var(--color-text-dim); text-transform: capitalize; }
.chip-free { background: color-mix(in srgb, var(--color-success) 14%, transparent); color: var(--color-success); }
.chip-installed { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
.chip-pending { background: color-mix(in srgb, var(--color-accent) 14%, transparent); color: var(--color-accent); }
.chip-failed { background: color-mix(in srgb, var(--color-danger) 14%, transparent); color: var(--color-danger); }
.chip-ns { background: color-mix(in srgb, var(--color-accent) 10%, transparent); color: var(--color-accent); }
.chip-bp { background: color-mix(in srgb, var(--color-border) 70%, transparent); color: var(--color-text); }
.chip-region { background: color-mix(in srgb, var(--color-success) 10%, transparent); color: var(--color-success); }

.section { padding: 1.1rem 0; border-bottom: 1px solid var(--color-border); }
.section:last-of-type { border-bottom: none; }
.section h2 { margin: 0 0 0.5rem; font-size: 0.98rem; font-weight: 600; color: var(--color-text-strong); }
.section-hint { margin: 0 0 0.5rem; font-size: 0.82rem; color: var(--color-text-dim); }
.desc { margin: 0; color: var(--color-text); font-size: 0.9rem; line-height: 1.6; }

.overview-grid { margin: 0.4rem 0 0; padding: 0; display: grid; gap: 0.4rem; }
.overview-row { display: grid; grid-template-columns: 12rem 1fr; gap: 0.7rem; align-items: baseline; }
.overview-row dt {
  margin: 0;
  color: var(--color-text-dim);
  font-size: 0.78rem;
  font-weight: 600;
  display: inline-flex;
  align-items: center;
}
.overview-row dd { margin: 0; font-size: 0.85rem; color: var(--color-text); }

.external-url-link {
  color: var(--color-accent);
  font-weight: 600;
  text-decoration: none;
  word-break: break-all;
}
.external-url-link:hover { text-decoration: underline; }

.dep-list { list-style: none; padding: 0; margin: 0; display: flex; flex-wrap: wrap; gap: 0.4rem; }
.dep-list li {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  padding: 0.25rem 0.7rem;
  font-size: 0.8rem;
  color: var(--color-text);
}

.singleton-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.8rem;
  flex-wrap: wrap;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  padding: 0.8rem 1rem;
  background: var(--color-surface);
}
.singleton-meta { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
.singleton-meta code {
  font-size: 0.85rem;
  background: var(--color-bg);
  padding: 0.15rem 0.45rem;
  border-radius: 4px;
  border: 1px solid var(--color-border);
}
.btn { cursor: pointer; }
.btn-primary {
  background: var(--color-accent);
  color: #fff;
  border: 0;
  border-radius: 6px;
  font-weight: 600;
  font-size: 0.82rem;
  padding: 0.4rem 0.85rem;
  text-decoration: none;
  display: inline-flex;
  align-items: center;
}
.btn-primary:hover { filter: brightness(1.05); }
.singleton-open { white-space: nowrap; }
`
