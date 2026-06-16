import { useState, type CSSProperties } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  getCatalogItem,
  getApplication,
  saveCatalogBlueprintIaC,
  type CatalogItem,
} from '@/lib/catalog.api'
import { findComponent } from '@/pages/wizard/steps/componentGroups'
import { BLUEPRINT_BY_ID } from '@/shared/constants/catalog.generated'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useTheme } from '@/shared/lib/useTheme'
import { resolveCatalogIcon } from '@/shared/lib/resolveCatalogIcon'
import { YamlEditor } from '@/widgets/cloud-list/YamlEditor'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import { ALL_MODES, describeMode } from '@/widgets/topology/TopologyEditor'
import { type CatalogEntryEdit } from '@/lib/commerce.api'
import { InstancesSection } from './AppDetail/InstancesSection'
import { useCatalogAdmin } from '@/shared/lib/useCatalogAdmin'
import { CatalogInlineField } from './CatalogInlineField'
import { IconPicker } from './IconPicker'

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
  const { theme } = useTheme()
  const qc = useQueryClient()
  // #3668 (founder item §5A) — PER-FIELD inline editing replaces the single
  // global "Edit" button + monolithic form: each hero field (icon, name,
  // summary, supported-topologies) carries its own in-place edit affordance and
  // saves INDEPENDENTLY via the shared saveCatalogEdit Gitea/store seam. No
  // global edit-mode state — the per-field editors own their own open state.
  // #3668 §5D — the full-CR "Edit IaC" mode: mounts the shipping YamlEditor
  // on the WHOLE blueprint.yaml so spec.source / manifests / sso / placement
  // / endpoints / shareable / contextSchema (fields the 7-field card form
  // can't touch) are editable, committing to the SAME catalog-sovereign file.
  const [editingIaC, setEditingIaC] = useState(false)

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

  // Icon (#3668 §5B): resolve the IaC icon FIRST (theme-aware
  // `card.iconLight` / `iconDark`), falling back to the build-time vendored
  // asset (`componentGroups.logoUrl`) only when the IaC carries none, and the
  // letter-mark last. Before #3668 this read the build-time map and DISCARDED
  // the API's `card.iconLight`/`iconDark`, so an admin's icon edit landed in
  // IaC + the API response and was thrown away at the last render step. One
  // shared resolution path (resolveCatalogIcon) — no per-blueprint branch.
  const bundledLogo = findComponent(name)?.logoUrl ?? null
  const logoUrl = resolveCatalogIcon(card, theme, bundledLogo)

  // #3668 §5A — the FULL current catalog-edit values, the merge base every
  // per-field inline save round-trips so editing one field never clobbers a
  // sibling (saveCatalogEdit does a full upsert). Light-icon pre-fills from the
  // CURRENT IaC icon (`card.iconLight`), falling back to the bundled asset so
  // the icon picker opens showing the rendered default rather than blank.
  const currentEdit: CatalogEntryEdit = {
    name: title,
    tagline: card.tagline || card.summary || '',
    supported_topologies: topologies.map(toEditorMode),
    icon_light: card.iconLight || bundledLogo || '',
    icon_dark: card.iconDark || '',
  }
  // Refetch the catalog item after any per-field save so the new value renders
  // live (and the next field's merge base picks up the saved sibling).
  const refetchCatalog = () => {
    void qc.invalidateQueries({ queryKey: ['catalog-item', name] })
  }

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
        {/* #3668 §5B — the icon is itself a per-field inline editor for an
            admin: clicking the logo opens the visual icon picker (light + dark)
            in place; non-admins (and the editor's display) see the rendered
            logo. The picker writes the icon via the same saveCatalogEdit seam. */}
        {isAdmin ? (
          <CatalogInlineField<{ light: string; dark: string }>
            blueprintId={`bp-${name}`}
            fieldKey="icon"
            label="Icon (light + dark)"
            current={currentEdit}
            editable={isAdmin && !editingIaC}
            block
            initialDraft={{ light: currentEdit.icon_light, dark: currentEdit.icon_dark }}
            renderDisplay={() =>
              logoUrl ? (
                <img src={logoUrl} alt={title} className="hero-logo" loading="lazy" />
              ) : (
                <span className="hero-icon" style={{ background: '#1f2937' }}>
                  {title[0] ?? '?'}
                </span>
              )
            }
            renderEditor={(draft, setDraft) => (
              <div className="hero-icon-editors">
                <div className="hero-icon-editor">
                  <span className="hero-icon-editor-label">Light theme</span>
                  <IconPicker
                    which="light"
                    value={draft.light}
                    onChange={(src) => setDraft({ ...draft, light: src })}
                  />
                </div>
                <div className="hero-icon-editor">
                  <span className="hero-icon-editor-label">Dark theme</span>
                  <IconPicker
                    which="dark"
                    value={draft.dark}
                    onChange={(src) => setDraft({ ...draft, dark: src })}
                  />
                </div>
              </div>
            )}
            toPatch={(draft) => ({
              icon_light: draft.light.trim(),
              icon_dark: draft.dark.trim(),
            })}
            onSaved={refetchCatalog}
          />
        ) : logoUrl ? (
          <img src={logoUrl} alt={title} className="hero-logo" loading="lazy" />
        ) : (
          <span className="hero-icon" style={{ background: '#1f2937' }}>
            {title[0] ?? '?'}
          </span>
        )}
        <div className="hero-body">
          {/* #3668 §5A — the display name is a per-field inline editor. */}
          <h1>
            <CatalogInlineField<string>
              blueprintId={`bp-${name}`}
              fieldKey="name"
              label="Display name"
              current={currentEdit}
              editable={isAdmin && !editingIaC}
              initialDraft={currentEdit.name}
              renderDisplay={() => <span data-testid="catalog-title">{title}</span>}
              renderEditor={(draft, setDraft) => (
                <input
                  type="text"
                  className="cif-input"
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  placeholder="WordPress"
                  data-testid="cif-name-input"
                />
              )}
              toPatch={(draft) => ({ name: draft.trim() })}
              onSaved={refetchCatalog}
            />
          </h1>
          {isAdmin && !editingIaC ? (
            <div style={{ display: 'flex', gap: '0.5rem', alignSelf: 'flex-start', marginTop: '0.15rem' }}>
              {/* #3668 §5D — open the full-CR IaC editor on the whole
                  blueprint.yaml (spec.source / manifests / sso / placement /
                  endpoints / shareable / contextSchema). The card fields above
                  are now edited per-field in place — no global Edit button. */}
              <button
                type="button"
                data-testid="catalog-detail-edit-iac"
                onClick={() => setEditingIaC(true)}
                title="Edit the full blueprint IaC (advanced)"
                style={CATALOG_EDIT_BTN_STYLE}
              >
                Edit IaC ⟩
              </button>
            </div>
          ) : null}
          {/* #3668 §5A — the summary/tagline is a per-field inline editor.
              For an admin it always renders (so an empty summary can be added);
              for a non-admin it renders only when present. */}
          {isAdmin ? (
            <div className="hero-tagline">
              <CatalogInlineField<string>
                blueprintId={`bp-${name}`}
                fieldKey="summary"
                label="Summary"
                current={currentEdit}
                editable={isAdmin && !editingIaC}
                initialDraft={currentEdit.tagline}
                renderDisplay={() => (
                  <span data-testid="catalog-summary">
                    {card.tagline || card.summary || (
                      <span className="hero-tagline-empty">Add a one-line summary…</span>
                    )}
                  </span>
                )}
                renderEditor={(draft, setDraft) => (
                  <textarea
                    className="cif-input cif-textarea"
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                    rows={2}
                    placeholder="One-line description shown on the card"
                    data-testid="cif-summary-input"
                  />
                )}
                toPatch={(draft) => ({ tagline: draft.trim() })}
                onSaved={refetchCatalog}
              />
            </div>
          ) : card.tagline || card.summary ? (
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

      {/* #3668 §5A — the per-field card editors (icon / name / summary /
          supported-topologies) live INLINE on the hero + topologies sections
          above/below; the old single global "Edit" button + monolithic
          CatalogEditForm panel is gone (the exact non-standard UX the founder
          called out). The full-CR "Edit IaC" editor below covers the fields the
          card form can't (source / manifests / sso / placement / endpoints). */}

      {/* #3668 §5D — full-CR IaC editor. Reuses the shipping YamlEditor
          (the same widget the cloud-list uses) on the blueprint's full CR,
          committing the WHOLE blueprint.yaml to the SAME catalog-sovereign
          Gitea file the card edit writes. This is where source / manifests /
          sso / placement / endpoints / contextSchema are edited. */}
      {editingIaC ? (
        <section className="section" data-testid="catalog-edit-iac-section">
          <h2>Edit IaC — full blueprint</h2>
          <p className="section-hint">
            Editing the complete <code>blueprint.yaml</code> in Gitea
            (catalog-sovereign). Commit writes the IaC source of truth; Flux
            reconciles it into the in-cluster Blueprint. Both this editor and
            the card form above write the same file.
          </p>
          <YamlEditor
            deploymentId={deploymentId ?? ''}
            kind="Blueprint"
            ns={undefined}
            name={`bp-${name}`}
            obj={(cat.raw as K8sObject | undefined) ?? null}
            commitLabel="Commit IaC"
            onCommit={async (yaml) => {
              const resp = await saveCatalogBlueprintIaC(`bp-${name}`, yaml)
              if (!resp.committed) {
                throw new Error(resp.reason || 'IaC commit did not land')
              }
              void qc.invalidateQueries({ queryKey: ['catalog-item', name] })
              return `Committed to IaC ✓ (${resp.path || 'catalog-sovereign'})`
            }}
          />
          <div style={{ marginTop: '0.8rem' }}>
            <button
              type="button"
              data-testid="catalog-edit-iac-close"
              onClick={() => setEditingIaC(false)}
              style={CATALOG_EDIT_BTN_STYLE}
            >
              Close
            </button>
          </div>
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

      {/* Supported topologies. The read-only grid (with per-topology placement)
          is the display; for an admin a per-field inline editor (#3668 §5A)
          below it edits the supported-topologies SET independently. The section
          renders for an admin even with no topologies yet so the first can be
          added. */}
      {topologies.length > 0 || isAdmin ? (
        <section className="section" data-testid="catalog-section-topologies">
          <h2>Supported topologies</h2>
          {topologies.length > 0 ? (
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
          ) : (
            <p className="section-hint" data-testid="catalog-topologies-empty">
              No topologies declared yet.
            </p>
          )}
          {isAdmin ? (
            <div style={{ marginTop: '0.7rem' }}>
              <CatalogInlineField<string[]>
                blueprintId={`bp-${name}`}
                fieldKey="topologies"
                label="Supported topologies"
                current={currentEdit}
                editable={isAdmin && !editingIaC}
                block
                initialDraft={currentEdit.supported_topologies}
                renderDisplay={() => (
                  <span className="cif-topo-summary" data-testid="catalog-topologies-edit-summary">
                    {currentEdit.supported_topologies.length > 0
                      ? currentEdit.supported_topologies.join(', ')
                      : 'set supported topologies…'}
                  </span>
                )}
                renderEditor={(draft, setDraft) => (
                  <div className="cif-topos" data-testid="catalog-edit-topologies">
                    {ALL_MODES.map((mode) => (
                      <label key={mode} className="cif-topo" title={describeMode(mode)}>
                        <input
                          type="checkbox"
                          checked={draft.includes(mode)}
                          onChange={() =>
                            setDraft(
                              draft.includes(mode)
                                ? draft.filter((m) => m !== mode)
                                : [...draft, mode],
                            )
                          }
                          data-testid={`catalog-edit-topo-${mode}`}
                        />
                        <span className="cif-topo-label">{mode}</span>
                      </label>
                    ))}
                  </div>
                )}
                toPatch={(draft) => ({ supported_topologies: draft })}
                onSaved={refetchCatalog}
              />
            </div>
          ) : null}
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

/* ─── shared inline styles ───────────────────────────────────────── */

// #3668 — shared button style for the catalog detail-page edit affordances
// (Edit / Edit IaC / Close), matching the prior single Edit button.
const CATALOG_EDIT_BTN_STYLE: CSSProperties = {
  padding: '0.3rem 0.8rem',
  borderRadius: '8px',
  border: '1px solid var(--color-border)',
  background: 'transparent',
  color: 'var(--color-text)',
  fontSize: '0.8rem',
  fontWeight: 600,
  cursor: 'pointer',
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
.hero-tagline-empty { color: var(--color-text-dim); font-style: italic; opacity: 0.8; }

/* #3668 §5B — the inline icon editor (light + dark pickers side by side). */
.hero-icon-editors { display: flex; flex-wrap: wrap; gap: 1rem; }
.hero-icon-editor { display: flex; flex-direction: column; gap: 0.35rem; min-width: 240px; flex: 1; }
.hero-icon-editor-label { font-size: 0.72rem; font-weight: 600; color: var(--color-text-strong); }
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
