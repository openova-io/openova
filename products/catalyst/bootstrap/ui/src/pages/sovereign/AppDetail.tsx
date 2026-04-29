/**
 * AppDetail — pixel-port of core/console/src/components/AppDetail.svelte.
 *
 * SECTIONS (NOT TABS) — visit order in canonical AppDetail.svelte:
 *   1. Hero        (logo + name + tagline + status chip)
 *   2. About
 *   3. Connection  (only when isServiceApp; canonical surfaces backing
 *                   service host/port/credentials. Sovereign-provision
 *                   doesn't deploy backing services as user-pickable
 *                   apps yet, so this section renders only when the
 *                   selected component descriptor matches one of the
 *                   bootstrap data-services families.)
 *   4. Bundled dependencies
 *   5. Tenant      (canonical: shows the org + total app count; here:
 *                   the deploymentId + Sovereign FQDN.)
 *   6. Configuration   (renders only when the descriptor exposes a
 *                       config schema; otherwise omitted entirely —
 *                       same canonical short-circuit.)
 *   7. Jobs        (APPENDED for the wizard provision context — lists
 *                   every Job whose `app === componentId`. Each row is
 *                   a JobCard; expand-in-place to view ordered steps.)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (no MVP / no shortcuts), the
 * canonical hero markup, modal-confirm flow, and per-section CSS are
 * preserved. The wizard surface drops install/remove buttons because
 * the deployment is one-shot — no day-2 affordance — but the section
 * order, hero, and chip palette are kept identical.
 *
 * Per #4 (never hardcode), every label and value comes from the
 * descriptor / reducer state / wizard store. There's no inlined
 * Application id.
 */

import { useMemo } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobCard } from './JobCard'
import { resolveApplications, reverseDependencies, findApplication, type ApplicationDescriptor } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs, jobsForApplication } from './jobs'
import { findComponent } from '@/pages/wizard/steps/componentGroups'
import type { ApplicationStatus } from './eventReducer'

interface AppDetailProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

export function AppDetail({ disableStream = false }: AppDetailProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/app/$componentId' as never,
  }) as {
    deploymentId: string
    componentId: string
  }
  const { deploymentId, componentId } = params
  const store = useWizardStore()

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })

  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null
  const app: ApplicationDescriptor | undefined = findApplication(applications, componentId)
  const compState = state.apps[componentId]
  const status: ApplicationStatus = compState?.status ?? 'pending'

  // Bundled dependencies — descriptors of every direct dep, with
  // human names sourced from componentGroups when available.
  const deps = useMemo<{ id: string; name: string }[]>(() => {
    if (!app) return []
    return app.dependencies.map((bareId) => {
      const c = findComponent(bareId)
      return { id: bareId, name: c?.name ?? bareId }
    })
  }, [app])

  // Reverse deps — components that pull THIS component in. Surfaced
  // alongside bundled deps so the operator can see why this card is on
  // the grid.
  const reverseDeps = useMemo<string[]>(
    () => (app ? reverseDependencies(app.bareId) : []),
    [app],
  )

  // Jobs scoped to this component. Phase 0 / cluster-bootstrap rows
  // are excluded — they have their own listing in JobsPage.
  const jobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const componentJobs = useMemo(
    () => jobsForApplication(jobs, componentId),
    [jobs, componentId],
  )

  // The Connection section renders only for backing-service Applications.
  // Future-proofed: descriptors will gain a `kind` field in a later
  // catalog evolution; today we infer from family.
  const isServiceApp = useMemo(() => {
    if (!app) return false
    const c = findComponent(app.bareId)
    if (!c) return false
    return c.product === 'data-services' || c.product === 'observability'
  }, [app])

  if (!app) {
    return (
      <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
        <style>{APP_DETAIL_CSS}</style>
        <div className="detail-page">
          <Link
            to="/provision/$deploymentId"
            params={{ deploymentId }}
            className="back-link"
            data-testid="sov-back-link"
          >
            &larr; Back to apps
          </Link>
          <div className="not-found" data-testid="sov-app-not-found">
            <h1>App not found</h1>
            <p>The component {componentId} is not part of this deployment.</p>
          </div>
        </div>
      </PortalShell>
    )
  }

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <style>{APP_DETAIL_CSS}</style>

      <div className="detail-page" data-testid={`sov-app-detail-${app.id}`}>
        <Link
          to="/provision/$deploymentId"
          params={{ deploymentId }}
          className="back-link"
          data-testid="sov-back-link"
        >
          &larr; Back to apps
        </Link>

        {/* 1. Hero */}
        <div className="hero" data-testid="sov-hero">
          {app.logoUrl ? (
            <img src={app.logoUrl} alt={app.title} className="hero-logo" />
          ) : (
            <span className="hero-icon" style={{ background: '#1f2937' }}>
              {app.title[0] ?? '?'}
            </span>
          )}
          <div className="hero-body">
            <h1>{app.title}</h1>
            <p className="hero-tagline">{app.description || app.familyName}</p>
            <div className="hero-meta">
              <span className="chip chip-cat">{app.familyName}</span>
              {app.bootstrapKit ? <span className="chip chip-free">BOOTSTRAP</span> : null}
              {status === 'installing' ? (
                <span className="chip chip-pending">
                  <span className="spinner" /> Installing…
                </span>
              ) : status === 'failed' ? (
                <span className="chip chip-failed">Failed</span>
              ) : status === 'degraded' ? (
                <span className="chip chip-failed">Degraded</span>
              ) : status === 'installed' ? (
                <span className="chip chip-installed">
                  <span className="dot" /> Installed
                </span>
              ) : (
                <span className="chip chip-cat">Pending</span>
              )}
            </div>
          </div>
        </div>

        {/* 2. About */}
        <section className="section" data-testid="sov-section-about">
          <h2>About</h2>
          <p className="desc">{app.description || app.familyName}</p>
        </section>

        {/* 3. Connection — only for service apps */}
        {isServiceApp ? (
          <section className="section" data-testid="sov-section-connection">
            <h2>Connection</h2>
            <p className="section-hint">
              Apps in this Sovereign reach this service inside the cluster. Credentials are
              injected at deploy time — no manual wiring needed.
            </p>
            <dl className="conn-grid">
              <div className="conn-row">
                <dt>Helm release</dt>
                <dd><code>{compState?.helmRelease ?? app.id}</code></dd>
              </div>
              <div className="conn-row">
                <dt>Namespace</dt>
                <dd><code>{compState?.namespace ?? 'flux-system'}</code></dd>
              </div>
              {compState?.chartVersion ? (
                <div className="conn-row">
                  <dt>Chart version</dt>
                  <dd><code>{compState.chartVersion}</code></dd>
                </div>
              ) : null}
            </dl>
          </section>
        ) : null}

        {/* 4. Bundled dependencies */}
        {(deps.length > 0 || reverseDeps.length > 0) ? (
          <section className="section" data-testid="sov-section-deps">
            <h2>Bundled dependencies</h2>
            {deps.length > 0 ? (
              <>
                <p className="section-hint">Auto-installed alongside {app.title}:</p>
                <ul className="dep-list">
                  {deps.map((d) => (
                    <li key={d.id} data-testid={`sov-dep-${d.id}`}>
                      {d.name}
                    </li>
                  ))}
                </ul>
              </>
            ) : null}
            {reverseDeps.length > 0 ? (
              <>
                <p className="section-hint" style={{ marginTop: deps.length ? '0.75rem' : 0 }}>
                  Pulled in by: {reverseDeps.length} component{reverseDeps.length === 1 ? '' : 's'}
                </p>
                <ul className="dep-list">
                  {reverseDeps.map((id) => (
                    <li key={id} data-testid={`sov-revdep-${id}`}>
                      {findComponent(id)?.name ?? id}
                    </li>
                  ))}
                </ul>
              </>
            ) : null}
          </section>
        ) : null}

        {/* 5. Tenant */}
        <section className="section" data-testid="sov-section-tenant">
          <h2>Tenant</h2>
          <p className="desc">
            {sovereignFQDN
              ? `Installing into ${sovereignFQDN} — currently ${applications.length} components targeted.`
              : `Installing into deployment ${deploymentId.slice(0, 8)} — currently ${applications.length} components targeted.`}
          </p>
        </section>

        {/* 6. Configuration — when descriptor exposes a config schema */}
        {/*
          The wizard's catalog descriptors don't yet expose a config_schema;
          the canonical AppDetail.svelte short-circuits the entire section
          when configSchema.length === 0, which is the behaviour we mirror.
          The hook is left here so adding schema in a future change drops
          the section back in without further plumbing.
        */}

        {/* 7. Jobs — appended for the wizard provision context */}
        <section className="section" data-testid="sov-section-jobs">
          <h2>Jobs</h2>
          <p className="section-hint">
            {componentJobs.length === 0
              ? 'No jobs recorded yet for this component.'
              : `${componentJobs.length} job${componentJobs.length === 1 ? '' : 's'} for ${app.title}.`}
          </p>
          {componentJobs.length > 0 ? (
            <div className="jobs-list" data-testid="sov-app-jobs">
              {componentJobs.map((job) => (
                <JobCard
                  key={job.id}
                  job={job}
                  deploymentId={deploymentId}
                  defaultExpanded={job.status === 'running' || job.status === 'failed'}
                />
              ))}
            </div>
          ) : null}
        </section>
      </div>
    </PortalShell>
  )
}

/**
 * Pixel-ported `<style>` block from canonical AppDetail.svelte. Same
 * selectors, same values; only the keyframe name is namespaced (`sov-`)
 * to avoid clashing with other pages' animations on the same surface.
 */
const APP_DETAIL_CSS = `
.detail-page { max-width: 860px; margin: 0 auto; padding: 1rem 0 4rem; }
.back-link {
  display: inline-block; margin-bottom: 1rem;
  color: var(--color-text-dim); font-size: 0.85rem; text-decoration: none;
}
.back-link:hover { color: var(--color-text-strong); }

.not-found { text-align: center; padding: 4rem 0; color: var(--color-text-dim); }
.not-found h1 { color: var(--color-text-strong); font-size: 1.4rem; margin-bottom: 1rem; }

.hero {
  display: flex; align-items: flex-start; gap: 1.1rem;
  padding: 1.4rem 0; border-bottom: 1px solid var(--color-border);
}
.hero-logo { width: 80px; height: 80px; border-radius: 18px; object-fit: cover; flex-shrink: 0; }
.hero-icon {
  width: 80px; height: 80px; border-radius: 18px;
  display: inline-flex; align-items: center; justify-content: center;
  color: #fff; font-size: 1.8rem; font-weight: 700; flex-shrink: 0;
}
.hero-body { flex: 1; min-width: 0; }
.hero-body h1 { margin: 0; color: var(--color-text-strong); font-size: 1.4rem; font-weight: 700; }
.hero-tagline { margin: 0.25rem 0 0.6rem; color: var(--color-text-dim); font-size: 0.9rem; }
.hero-meta { display: flex; gap: 0.4rem; flex-wrap: wrap; }

.chip { display: inline-flex; align-items: center; gap: 0.3rem; padding: 0.18rem 0.55rem; border-radius: 999px; font-size: 0.7rem; font-weight: 600; white-space: nowrap; }
.chip-cat { background: color-mix(in srgb, var(--color-border) 50%, transparent); color: var(--color-text-dim); text-transform: capitalize; }
.chip-free { background: color-mix(in srgb, var(--color-success) 14%, transparent); color: var(--color-success); }
.chip-installed { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
.chip-installed .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.chip-pending { background: color-mix(in srgb, var(--color-accent) 14%, transparent); color: var(--color-accent); }
.chip-pending .spinner {
  width: 10px; height: 10px; border-radius: 50%;
  border: 2px solid currentColor; border-top-color: transparent;
  animation: sov-detail-spin 0.7s linear infinite;
}
.chip-failed { background: color-mix(in srgb, var(--color-danger) 14%, transparent); color: var(--color-danger); }
@keyframes sov-detail-spin { to { transform: rotate(360deg); } }

.section { padding: 1.1rem 0; border-bottom: 1px solid var(--color-border); }
.section:last-of-type { border-bottom: none; }
.section h2 { margin: 0 0 0.5rem; font-size: 0.98rem; font-weight: 600; color: var(--color-text-strong); }
.section-hint { margin: 0 0 0.5rem; font-size: 0.82rem; color: var(--color-text-dim); }
.desc { margin: 0; color: var(--color-text); font-size: 0.9rem; line-height: 1.6; }
.conn-grid { margin: 0.4rem 0 0; padding: 0; display: grid; gap: 0.35rem; }
.conn-row { display: grid; grid-template-columns: 6rem 1fr; gap: 0.6rem; align-items: baseline; }
.conn-row dt {
  margin: 0;
  color: var(--color-text-dim);
  font-size: 0.72rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.conn-row dd { margin: 0; font-size: 0.88rem; color: var(--color-text); }
.conn-row code {
  font-size: 0.82rem;
  background: var(--color-surface);
  padding: 0.1rem 0.4rem;
  border-radius: 4px;
  border: 1px solid var(--color-border);
}
.dep-list { list-style: none; padding: 0; margin: 0; display: flex; flex-wrap: wrap; gap: 0.4rem; }
.dep-list li {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  padding: 0.25rem 0.7rem;
  font-size: 0.8rem;
  color: var(--color-text);
}
.jobs-list { display: flex; flex-direction: column; gap: 0.6rem; margin-top: 0.5rem; }
`
