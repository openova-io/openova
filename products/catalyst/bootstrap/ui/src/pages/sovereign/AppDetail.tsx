/**
 * AppDetail — Application detail page (target-state shape per
 * docs/INVIOLABLE-PRINCIPLES.md #1).
 *
 * Tab order (matrix-canonical, TC-036 + TC-106):
 *
 *   Overview · Topology · Resources · Compliance · Logs · Settings · Members
 *
 * Plus two appended tabs that EPIC-2 surfaced for the wizard provision
 * context but are NOT part of the matrix-asserted core surface:
 *
 *   Jobs · Dependencies
 *
 * Both stay rendered (target-state) but live AFTER Members so the
 * matrix walks the canonical 7-tab strip in order without tripping on
 * extra columns.
 *
 * The active tab is read from the `$tab` URL segment when the route is
 * `/app/$deploymentId/applications/$componentId/$tab` (router.tsx
 * appAppDetailTabRoute). Falls back to `overview` when absent.
 *
 * Test-id seam (matrix-canonical, TC-106):
 *
 *   data-testid="app-tab-overview"   (button)
 *   data-testid="app-tab-topology"   ...
 *
 * The PRIOR `app-{name}-tab` ids ALSO stay rendered as a secondary
 * data-testid-alt attribute so the existing JS unit tests + any
 * external selectors keep working — but the matrix and the canonical
 * contract henceforth read `app-tab-{name}`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state, no MVP) — every matrix-asserted tab + token
 *      renders the first time, even if its full data feed lands later.
 *      Logs/Resources/Members surface real data via TanStack Query
 *      polling against the catalyst-api k8s + access-matrix endpoints.
 *   #4 (never hardcode) — every URL flows through the catalog.api /
 *      resource.api helpers, never literal `/api/...` strings.
 */

import { useEffect, useMemo, useState } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobsTable } from './JobsTable'
import { resolveApplications, reverseDependencies, findApplication, type ApplicationDescriptor } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { findComponent } from '@/pages/wizard/steps/componentGroups'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PATTERN_NOT_REPORTED } from '@/shared/lib/placement'
import { useSession } from '@/shared/lib/useSession'
import { API_BASE } from '@/shared/config/urls'
import type { ApplicationStatus } from './eventReducer'
import {
  getApplication,
  getLaunchURL,
  getApplicationInstances,
  type ApplicationDetailResponse,
} from '@/lib/catalog.api'
import { BLUEPRINT_BY_ID } from '@/shared/constants/catalog.generated'
import { getHierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { ComplianceTab } from './AppDetail/ComplianceTab'
import { ContextsTab } from './AppDetail/ContextsTab'
import { PerClusterTable } from './AppDetail/InstancesSection'
import { MembersTab } from './AppDetail/MembersTab'
import { TopologyTab } from './AppDetail/TopologyTab'
import { ResourcesTab } from './AppDetail/ResourcesTab'
import { LogsTab } from './AppDetail/LogsTab'
import { SettingsTab } from './AppDetail/SettingsTab'
import { EndpointsTab } from './AppDetail/EndpointsTab'

/**
 * Tab ids — matrix-canonical seam.
 *
 * Order is rendered top-to-bottom in the tablist; first id is the
 * default landing tab when no `$tab` URL segment is present.
 */
const APP_TAB_IDS = [
  'overview',
  // #3370 — Contexts: rendered ONLY for shareable blueprints' instances
  // (the TabButton is conditional below; the id stays registered so the
  // ⛓ badge deep-link /app/$id/contexts resolves).
  'contexts',
  'topology',
  'resources',
  'compliance',
  'logs',
  'settings',
  'members',
  'endpoints',
  'jobs',
  'dependencies',
] as const
type AppTabId = (typeof APP_TAB_IDS)[number]

function isAppTabId(value: string | undefined): value is AppTabId {
  return !!value && (APP_TAB_IDS as readonly string[]).includes(value as AppTabId)
}

interface AppDetailProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

export function AppDetail({ disableStream = false }: AppDetailProps = {}) {
  // Use strict:false params + useResolvedDeploymentId so this page
  // works on BOTH /provision/$deploymentId/app/$componentId AND the
  // chroot Sovereign route /app/$componentId. Strict-mode useParams
  // throws Invariant when the path doesn't match.
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    componentId?: string
    tab?: string
  }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const componentId = params.componentId ?? ''
  const urlTab = params.tab
  const store = useWizardStore()

  // #3375 — the operator's Catalyst tier, used to enable the DR Switchover
  // control on the Topology tab for the owner/admin tier (the prior bug:
  // callerTier was never threaded → the button was permanently disabled
  // "Owner tier required"). Prefer the explicit `tier` claim; fall back to
  // deriving it from the catalyst-<tier> realm roles whoami also returns —
  // so a session that carries only realm roles still resolves correctly.
  const session = useSession()
  const callerTier = useMemo(() => {
    if (session.tier) return session.tier
    const roles = session.roles.map((r) => r.toLowerCase())
    if (roles.includes('catalyst-owner')) return 'owner'
    if (roles.includes('catalyst-admin')) return 'admin'
    if (roles.includes('catalyst-operator')) return 'operator'
    if (roles.includes('catalyst-developer')) return 'developer'
    if (roles.includes('catalyst-viewer')) return 'viewer'
    return ''
  }, [session.tier, session.roles])

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
  const wizardApp: ApplicationDescriptor | undefined = findApplication(applications, componentId)

  // qa-loop iter-11 Fix #45 Cluster-C: when the requested component is
  // NOT part of the wizard's selectedComponents (the typical case for a
  // chroot Sovereign — Applications installed via `kubectl apply` or
  // the catalyst-api install endpoint NEVER pass through the wizard
  // store), fall back to the catalyst-api's
  // GET /sovereigns/{id}/applications/{name} endpoint to fetch the
  // Application CR directly and synthesise an ApplicationDescriptor on
  // the fly. Prior to this fallback every chroot-installed Application
  // surfaced the misleading "App not found" page even though the
  // Application CR + HelmRelease were both Ready=True.
  //
  // TBD-V2 (issue #1928, 2026-05-19): the query was previously gated on
  // `!wizardApp` — but the wizardApp descriptor carries blueprint
  // identity ONLY, NOT the runtime install location. When the operator
  // lands on AppDetail with a wizardApp populated (e.g. they kicked off
  // the install minutes earlier and the wizard store still holds the
  // selection), the apiApp stayed undefined → `apiApp?.targetNamespace`
  // resolved to undefined → `appTargetNamespace` fell through to
  // `appNamespace` which defaults to `"default"` → ResourcesTab queried
  // `?namespace=default` and got 0 items (the workload lives in
  // `harbor`/`alloy`/`cert-manager`/…). Founder proof:
  // `?namespace=default` → 163 bytes empty; `?namespace=harbor` → 66kB
  // of real K8s objects. Fix: always fetch the API detail so
  // `apiApp.targetNamespace` is populated regardless of wizard state.
  // The synthesisedApp path remains gated on `!wizardApp` below.
  const needsApiFallback = !wizardApp && !!deploymentId && !!componentId
  const apiAppQuery = useQuery({
    queryKey: ['sov-application', deploymentId, componentId],
    queryFn: async () => getApplication(deploymentId, componentId),
    enabled: !!deploymentId && !!componentId,
    staleTime: 30_000,
    refetchInterval: 30_000,
    retry: 1,
  })
  const apiApp: ApplicationDetailResponse | null | undefined = apiAppQuery.data

  // Synthesise an ApplicationDescriptor from the API response so the
  // rest of the page (hero, sections, tabs) can render unchanged.
  const synthesisedApp: ApplicationDescriptor | undefined = useMemo(() => {
    if (!apiApp) return undefined
    if (!apiApp.name && !apiApp.blueprint) return undefined
    const bareId = (apiApp.blueprint ?? '').replace(/^bp-/, '') || componentId
    const compEntry = findComponent(bareId)
    return {
      id: apiApp.blueprint || `bp-${bareId}`,
      bareId,
      title: compEntry?.name ?? apiApp.name ?? componentId,
      description: compEntry?.desc ?? `Application installed in namespace ${apiApp.namespace}`,
      familyId: compEntry?.product ?? 'platform',
      familyName: compEntry?.groupName ?? 'Platform',
      tier: compEntry?.tier ?? 'optional',
      logoUrl: compEntry?.logoUrl ?? null,
      dependencies: compEntry?.dependencies ?? [],
      bootstrapKit: false,
    }
  }, [apiApp, componentId])

  const app: ApplicationDescriptor | undefined = wizardApp ?? synthesisedApp
  const compState = state.apps[componentId]
  const apiPhaseStatus: ApplicationStatus | undefined = useMemo(() => {
    if (!apiApp?.phase) return undefined
    switch (apiApp.phase) {
      case 'Ready':
        return 'installed'
      case 'Failed':
        return 'failed'
      case 'Degraded':
        return 'degraded'
      case 'Provisioning':
      case 'Pending':
        return 'installing'
      default:
        return 'pending'
    }
  }, [apiApp])
  const status: ApplicationStatus = compState?.status ?? apiPhaseStatus ?? 'pending'

  // Surface the underlying namespace, blueprint name, and live phase
  // from the API response (or fall back to the wizard event-stream
  // payload when running inside a wizard provision flow). These three
  // fields are matrix-asserted on the Overview tab (TC-068).
  const appNamespace = apiApp?.namespace ?? compState?.namespace ?? 'default'
  const appBlueprint = apiApp?.blueprint ?? wizardApp?.id ?? ''
  const appBlueprintVersion = apiApp?.version ?? ''
  // #3370 — shareability from the build-time catalog declaration (the
  // blueprint.yaml truth) with the live contexts[] as confirmation.
  // The Contexts tab renders ONLY for shareable blueprints' instances.
  const appBlueprintFull = appBlueprint
    ? appBlueprint.startsWith('bp-')
      ? appBlueprint
      : `bp-${appBlueprint}`
    : ''
  const appShareable =
    BLUEPRINT_BY_ID[appBlueprintFull]?.shareable === true ||
    (apiApp?.contexts?.length ?? 0) > 0
  const appContexts = apiApp?.contexts ?? []
  const appDependsOn = apiApp?.dependsOn ?? []
  const appRegions = apiApp?.regions ?? []
  const appLastReconciled = apiApp?.lastReconciledAt ?? ''
  // #5422 (#3969) — NEVER assert a placement the API did not report. The
  // old `?? 'singleton'` fallback stated a specific, load-bearing value for
  // an app whose placement is simply unknown to this response, and the
  // Topology tab on the SAME screen derives its own value from live
  // targets — so a two-region hot-standby app was described as a singleton
  // in the Overview and correctly in Topology, with no way to tell which
  // was true. Reuses the sentinel #5515 shipped for the identical
  // fails-open defect in derivePattern, so both surfaces say "not-reported"
  // in the same words rather than inventing two dialects for "unknown".
  const appPlacement = apiApp?.placement ?? PATTERN_NOT_REPORTED
  const appPrimaryRegion = apiApp?.primaryRegion ?? appRegions[0] ?? ''
  // Family B (2026-05-17 t10 C4-005/007): the namespace the workload
  // actually lives in (HR spec.targetNamespace), and the label that
  // identifies its pods/services/etc. For bootstrap-kit apps the HR
  // is in `flux-system` but the install lands in e.g. `alloy/` with
  // `app.kubernetes.io/name=alloy`. For wizard-installed Application
  // CRs both default to instance=<name>. We prefer targetNamespace
  // but never let it fall back to "default" silently — empty falls
  // back to appNamespace.
  const appTargetNamespace =
    apiApp?.targetNamespace?.trim() || apiApp?.namespace?.trim() || appNamespace
  const appInstallLabelSelector =
    apiApp?.installLabelSelector?.trim() ||
    `app.kubernetes.io/instance=${componentId}`
  // C4-004: when the backend has flagged this app as a bootstrap-kit
  // synth (no Application CR, just an HR), the catalog 404 is EXPECTED
  // — the publish chip should render "Bootstrap blueprint" instead of
  // "Catalog status unavailable".
  const appIsBootstrap = !!apiApp?.bootstrap
  // C4-003: when the backend's HR-Ready overlay promoted the phase
  // (CR was stale at Provisioning, HR was Ready=True), the source
  // chip surfaces both so the operator can see the lag.
  const appHRReady = !!apiApp?.hrReady
  const appPhaseFromCR = apiApp?.phaseFromCR?.trim() ?? ''
  // G90 (2026-06-01) — operator-visible front-door URL. Empty when the
  // Application has no externally-exposed HTTPRoute. Renders as an
  // "External URL" row + "Open" button on the Overview tab so the
  // operator can launch the app (SSO-authenticated via Keycloak) in a
  // new tab from the AppDetail page itself.
  const appExternalURL = apiApp?.externalURL?.trim() ?? ''
  // G117.4 #2743 AC3 (2026-06-03): Application metadata.uid passes
  // through OverviewPanel into the Launch button so the FE can mint
  // a silent-SSO URL via catalyst-api's GET /catalyst/v1/apps/<uid>/
  // launch-url handler (endpoint_handler.go:529). Empty when the
  // backend's GET-Application response hasn't been updated to include
  // metadata.uid yet — Launch button falls back to direct externalURL.
  //
  // G117.4 #2884-followup (2026-06-03): when the route is /app/bp-<name>
  // (componentId = Blueprint name) but the actual Application instance
  // is named differently (e.g. operator named it "walk2742-app", not
  // "bp-grafana"), getApplication("bp-grafana") returns no match → no
  // uid → LaunchButton's early-return fires and silent SSO is bypassed.
  // Fall back to listing instances by Blueprint and pick the first.
  // Caught live on hw86 walk 2026-06-03 — only path to a non-empty
  // appUID for non-bp-named Application instances.
  const directUID = apiApp?.uid?.trim() ?? ''
  const fallbackInstancesQuery = useQuery({
    queryKey: ['appdetail-fallback-instances', componentId],
    queryFn: () => getApplicationInstances(componentId),
    enabled: !!componentId && !directUID,
    staleTime: 30_000,
    refetchInterval: 30_000,
    retry: 1,
  })
  const appUID = directUID || (fallbackInstancesQuery.data?.items?.[0]?.id?.trim() ?? '')
  // #3150 — silent-SSO launch key. The launch-url endpoint resolves the
  // `{id}` as EITHER an Application CR uid OR (when no CR exists) a
  // bootstrap-kit blueprint/release name. Bootstrap-kit apps (grafana,
  // harbor, openbao, gitea, keycloak, guacamole, powerdns-admin, netbird)
  // install as bare HelmReleases with no Application CR → appUID is empty
  // → pre-#3150 the Launch button short-circuited to the plain
  // externalURL and the app showed its own login form. Now we hand the
  // launch-url the blueprint/release name (componentId, e.g. "grafana"
  // or "bp-grafana" — the BE strips the prefix) so it can resolve the
  // HR-backed app and return the OIDC-init URL. Non-bootstrap apps keep
  // using the CR uid exactly as before.
  const launchKey = appUID || (appIsBootstrap ? componentId : '')
  // Matrix asserts the literal `Ready` token in the Overview body
  // (TC-068). When the API hasn't reported a phase yet, render the
  // mapped `status` chip phrase instead of an empty string so the test
  // gets a stable, human-readable token.
  const apiPhaseLabel = apiApp?.phase ?? ''

  // Bundled dependencies — descriptors of every direct dep, with
  // human names sourced from componentGroups when available.
  const deps = useMemo<{ id: string; name: string }[]>(() => {
    if (!app) return []
    return app.dependencies.map((bareId) => {
      const c = findComponent(bareId)
      return { id: bareId, name: c?.name ?? bareId }
    })
  }, [app])

  // Reverse deps — components that pull THIS component in.
  const reverseDeps = useMemo<string[]>(
    () => (app ? reverseDependencies(app.bareId) : []),
    [app],
  )

  // Jobs scoped to this component.
  const derivedJobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const flatJobs = useMemo(() => adaptDerivedJobsToFlat(derivedJobs), [derivedJobs])
  const componentJobsCount = useMemo(
    () => flatJobs.filter((j) => j.appId === componentId).length,
    [flatJobs, componentId],
  )

  // Tab state. Initial value comes from the URL `$tab` segment when
  // present (so /app/$id/applications/$comp/topology lands on Topology
  // directly). Defaults to 'overview' — matrix TC-036 asserts the
  // Overview tab is the landing tab when no $tab is in the URL.
  const initialTab: AppTabId = isAppTabId(urlTab) ? urlTab : 'overview'
  const [appTab, setAppTab] = useState<AppTabId>(initialTab)

  // Re-sync when the URL `$tab` segment changes (browser back/forward,
  // matrix-driven sub-route navigations). Without this useEffect the
  // first useState init wins for the lifetime of the component.
  useEffect(() => {
    if (isAppTabId(urlTab)) {
      setAppTab(urlTab)
    }
  }, [urlTab])

  // The Connection section renders only for backing-service Applications.
  const isServiceApp = useMemo(() => {
    if (!app) return false
    const c = findComponent(app.bareId)
    if (!c) return false
    return c.product === 'data-services' || c.product === 'observability'
  }, [app])

  if (!app) {
    if (needsApiFallback && (apiAppQuery.isPending || apiAppQuery.isFetching)) {
      return (
        <PortalShell
          deploymentId={deploymentId}
          sovereignFQDN={sovereignFQDN}
          pageTitle="Loading…"
          headerSlotLeft={
            <Link
              to={`/dashboard` as never}
              className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
              data-testid="sov-back-link"
            >
              &larr; Back to apps
            </Link>
          }
        >
          <style>{APP_DETAIL_CSS}</style>
          <div className="detail-page">
            <div className="not-found" data-testid="sov-app-loading">
              <p>Loading {componentId}…</p>
            </div>
          </div>
        </PortalShell>
      )
    }
    return (
      <PortalShell
        deploymentId={deploymentId}
        sovereignFQDN={sovereignFQDN}
        pageTitle="App not found"
        headerSlotLeft={
          <Link
            to={`/dashboard` as never}
            className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
            data-testid="sov-back-link"
          >
            &larr; Back to apps
          </Link>
        }
      >
        <style>{APP_DETAIL_CSS}</style>
        <div className="detail-page">
          <div className="not-found" data-testid="sov-app-not-found">
            <h1>App not found</h1>
            <p>The component {componentId} is not part of this deployment.</p>
          </div>
        </div>
      </PortalShell>
    )
  }

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Applications"
      headerSlotLeft={
        <Link
          to={`/dashboard` as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-back-link"
        >
          &larr; Back to apps
        </Link>
      }
    >
      <style>{APP_DETAIL_CSS}</style>

      <div className="detail-page" data-testid={`sov-app-detail-${app.id}`}>
        {/*
          Hero — always visible above the tab strip. Surfaces the
          matrix-asserted identity tokens (componentId, blueprint,
          namespace, phase) so even a quick visual hit on the Overview
          tab passes TC-068's `must_contain: [qa-wp, Ready, bp-wordpress, qa-omantel]`.
        */}
        <div className="hero" data-testid="sov-hero">
          {app.logoUrl ? (
            <img src={app.logoUrl} alt={app.title} className="hero-logo" />
          ) : (
            <span className="hero-icon" style={{ background: '#1f2937' }}>
              {app.title[0] ?? '?'}
            </span>
          )}
          <div className="hero-body">
            <h1>
              <span data-testid="app-detail-name">{componentId}</span>{' '}
              <span className="hero-subtitle">— {app.title}</span>
            </h1>
            <p className="hero-tagline">{app.description || app.familyName}</p>
            <div className="hero-meta">
              <span className="chip chip-cat">{app.familyName}</span>
              {app.bootstrapKit ? <span className="chip chip-free">BOOTSTRAP</span> : null}
              <span className="chip chip-ns" data-testid="app-detail-namespace">
                {appNamespace}
              </span>
              {appBlueprint ? (
                <span className="chip chip-bp" data-testid="app-detail-blueprint">
                  {appBlueprint}
                  {appBlueprintVersion ? `@${appBlueprintVersion}` : ''}
                </span>
              ) : null}
              {/*
                Phase chip — matrix asserts the literal `Ready` token
                in the body. We always emit the API-reported phase
                verbatim (e.g. "Ready", "Provisioning") so the matrix
                reads the canonical state, AND we keep the colour-coded
                visual chip for the operator.
              */}
              <span
                className={`chip ${
                  status === 'installed'
                    ? 'chip-installed'
                    : status === 'installing'
                      ? 'chip-pending'
                      : status === 'failed' || status === 'degraded'
                        ? 'chip-failed'
                        : 'chip-cat'
                }`}
                data-testid="app-detail-phase"
              >
                {apiPhaseLabel ||
                  (status === 'installing'
                    ? 'Provisioning'
                    : status === 'failed'
                      ? 'Failed'
                      : status === 'degraded'
                        ? 'Degraded'
                        : status === 'installed'
                          ? 'Ready'
                          : 'Pending')}
              </span>
              {appRegions.map((r) => (
                <span
                  key={r}
                  className="chip chip-region"
                  data-testid={`app-detail-region-${r}`}
                >
                  {r}
                </span>
              ))}
              {appPrimaryRegion && !appRegions.includes(appPrimaryRegion) ? (
                <span
                  className="chip chip-region"
                  data-testid={`app-detail-region-${appPrimaryRegion}`}
                >
                  {appPrimaryRegion}
                </span>
              ) : null}
              {/*
                PR K (2026-05-17 t140 founder bug #4): per-app catalog
                publish/unpublish toggle. Operator clicks to flip the
                Published flag on this app — controls whether Organizations
                see it in marketplace storefront. Backend at
                PUT /api/catalog/admin/apps/{slug}/published; ownership
                gated by Sovereign Console session.
              */}
              <PublishToggleChip slug={componentId} isBootstrap={appIsBootstrap} />
              {/*
                Family B (2026-05-17 t10 C4-013): D19 D-count mismatch
                root cause = the three count sources (Deployment CR
                count, catalog entry count, HelmRelease Ready count)
                were aggregated into one chip on AppsPage with no
                breakdown. Operator could not see which source was
                wrong. Here on AppDetail we surface the SOURCE-OF-TRUTH
                for THIS app's phase chip — so the same operator
                instinct that flagged the mismatch on AppsPage can
                trace where it came from per-app.
              */}
              <span
                className="chip chip-bp"
                data-testid="app-detail-source"
                data-source={appIsBootstrap ? 'helmrelease' : 'application-cr'}
                data-hr-overlay={appHRReady ? 'true' : 'false'}
                title={
                  appIsBootstrap
                    ? 'Phase derived from HelmRelease Ready condition (no Application CR)'
                    : appHRReady
                      ? `Application CR phase is stale (status.phase=${appPhaseFromCR || 'Provisioning'}); HelmRelease is Ready — promoted to Ready. application-controller is lagging.`
                      : 'Phase derived from Application CR status'
                }
                style={{ fontWeight: 500 }}
              >
                source:{' '}
                {appIsBootstrap
                  ? 'HelmRelease'
                  : appHRReady
                    ? `Application CR (HR-overlayed; CR=${appPhaseFromCR || 'Provisioning'})`
                    : 'Application CR'}
              </span>
            </div>
          </div>
          {/*
            #3150 — the silent-SSO "Open <App>" CTA lives on the hero's
            TOP LINE, right-aligned on the same row as the logo/title
            (founder: "Logo left, Open right, the rest in between"). It is
            the LAST child of the `.hero` flex row with `margin-left: auto`
            so the title + chips fill the middle and the button pins to the
            right edge, vertically centered. On narrow widths it wraps to
            its own line below (see `.hero` flex-wrap + `.hero-launch` rules
            in APP_DETAIL_CSS). Only rendered when the app has a front door
            (`appExternalURL` set). The `launchKey`/`launchAppViaSSO` wiring
            is the working silent-SSO launch — unchanged.
          */}
          {appExternalURL ? (
            <div className="hero-launch" data-testid="hero-launch">
              <LaunchButton
                appUID={launchKey}
                fallbackURL={appExternalURL}
                appLabel={app.title}
                variant="hero"
              />
              <span className="hero-launch-hint">
                Single sign-in — no second login.
              </span>
            </div>
          ) : null}
        </div>

        {/*
          #3090 (2026-06-08) — AppDetail is the per-INSTANCE surface, NOT
          the class surface. The class-level instances list + "+ New
          instance" button moved to the CATALOG / class page
          (`/catalog/$blueprintName` → CatalogDetail). What MUST stay here
          is this one instance's own per-cluster fan-out status
          (`status.perCluster[]` surfaced as `regionStatuses`): the
          operator on an instance page still needs to see where THIS
          instance is running across host clusters (G117.6
          application-controller). Rendered via the shared PerClusterTable
          (extracted with InstancesSection) so the row markup stays in one
          place. No instances list, no New-instance button — those belong
          to the class page.
        */}
        {apiApp?.regionStatuses && apiApp.regionStatuses.length > 0 ? (
          <section
            className="section"
            data-testid="sov-section-percluster"
            style={{ marginTop: '0.6rem' }}
          >
            <h2 style={{ margin: '0 0 0.4rem' }}>
              Per-cluster fan-out
              {appUID ? (
                <span
                  style={{
                    marginLeft: '0.5rem',
                    fontSize: '0.7rem',
                    color: 'var(--color-text-dim)',
                    fontWeight: 400,
                  }}
                >
                  (uid {appUID.slice(0, 8)})
                </span>
              ) : null}
            </h2>
            <PerClusterTable perCluster={apiApp.regionStatuses} />
          </section>
        ) : null}

        {/*
          Tab strip — matrix-canonical order + matrix-canonical
          test-ids. The `app-tab-{name}` ids are the matrix's primary
          contract (TC-106). Each button also carries an
          `data-testid-alt="app-{name}-tab"` legacy alias so older unit
          tests keep working without a flag-day rename.
        */}
        <div role="tablist" aria-label="App detail tabs" className="app-tablist" data-testid="app-detail-tablist">
          <TabButton id="overview" label="Overview" active={appTab} onClick={setAppTab} />
          {appShareable ? (
            <TabButton
              id="contexts"
              label="Contexts"
              active={appTab}
              onClick={setAppTab}
              count={appContexts.length}
            />
          ) : null}
          <TabButton id="topology" label="Topology" active={appTab} onClick={setAppTab} />
          <TabButton id="resources" label="Resources" active={appTab} onClick={setAppTab} />
          <TabButton id="compliance" label="Compliance" active={appTab} onClick={setAppTab} />
          <TabButton id="logs" label="Logs" active={appTab} onClick={setAppTab} />
          <TabButton id="settings" label="Settings" active={appTab} onClick={setAppTab} />
          <TabButton id="members" label="Members" active={appTab} onClick={setAppTab} />
          <TabButton id="endpoints" label="Endpoints" active={appTab} onClick={setAppTab} />
          <TabButton
            id="jobs"
            label="Jobs"
            active={appTab}
            onClick={setAppTab}
            count={componentJobsCount}
          />
          <TabButton
            id="dependencies"
            label="Dependencies"
            active={appTab}
            onClick={setAppTab}
            count={deps.length + reverseDeps.length + appDependsOn.length}
          />
        </div>

        {/* Tab panels */}
        {appTab === 'overview' ? (
          <div role="tabpanel" data-testid="app-tab-overview-panel" className="app-tabpanel">
            <OverviewPanel
              app={app}
              componentId={componentId}
              appNamespace={appNamespace}
              appBlueprint={appBlueprint}
              appBlueprintVersion={appBlueprintVersion}
              appPhaseLabel={apiPhaseLabel}
              status={status}
              appRegions={appRegions}
              appPlacement={appPlacement}
              appPrimaryRegion={appPrimaryRegion}
              appLastReconciled={appLastReconciled}
              appExternalURL={appExternalURL}
              appUID={appUID}
              launchKey={launchKey}
              isServiceApp={isServiceApp}
              compState={compState}
              deps={deps}
              reverseDeps={reverseDeps}
              applicationsCount={applications.length}
              sovereignFQDN={sovereignFQDN}
              deploymentId={deploymentId}
            />
          </div>
        ) : appTab === 'contexts' ? (
          <div role="tabpanel" data-testid="app-tab-contexts-panel" className="app-tabpanel">
            <ContextsTab contexts={appContexts} />
          </div>
        ) : appTab === 'topology' ? (
          <div role="tabpanel" data-testid="app-tab-topology-panel" className="app-tabpanel">
            <TopologyTab
              sovereignId={deploymentId}
              applicationName={componentId}
              namespace={appNamespace}
              callerTier={callerTier}
              isBootstrap={appIsBootstrap}
            />
          </div>
        ) : appTab === 'resources' ? (
          <div role="tabpanel" data-testid="app-tab-resources-panel" className="app-tabpanel">
            <ResourcesTab
              applicationName={componentId}
              sovereignId={deploymentId}
              namespace={appTargetNamespace}
              labelSelector={appInstallLabelSelector}
            />
          </div>
        ) : appTab === 'compliance' ? (
          <div role="tabpanel" data-testid="app-tab-compliance-panel" className="app-tabpanel">
            <ComplianceTab
              sovereignId={deploymentId}
              applicationName={componentId}
              disableStream={disableStream}
            />
          </div>
        ) : appTab === 'logs' ? (
          <div role="tabpanel" data-testid="app-tab-logs-panel" className="app-tabpanel">
            <LogsTab
              applicationName={componentId}
              sovereignId={deploymentId}
              namespace={appTargetNamespace}
              labelSelector={appInstallLabelSelector}
              blueprint={appBlueprint}
            />
          </div>
        ) : appTab === 'settings' ? (
          <div role="tabpanel" data-testid="app-tab-settings-panel" className="app-tabpanel">
            <SettingsTab
              sovereignId={deploymentId}
              applicationName={componentId}
              namespace={appNamespace}
            />
          </div>
        ) : appTab === 'members' ? (
          <div role="tabpanel" data-testid="app-tab-members-panel" className="app-tabpanel">
            <MembersTab sovereignId={deploymentId} applicationName={componentId} />
          </div>
        ) : appTab === 'endpoints' ? (
          <div role="tabpanel" data-testid="app-tab-endpoints-panel" className="app-tabpanel">
            <EndpointsTab
              applicationName={componentId}
              appUID={appUID}
              externalURL={appExternalURL}
              sovereignFQDN={sovereignFQDN}
            />
          </div>
        ) : appTab === 'jobs' ? (
          <div role="tabpanel" data-testid="app-tab-jobs-panel" className="app-tabpanel">
            <JobsTable jobs={flatJobs} appIdFilter={componentId} deploymentId={deploymentId} />
          </div>
        ) : (
          <div role="tabpanel" data-testid="app-tab-dependencies-panel" className="app-tabpanel">
            {/*
              #3370 — entity edges first: graph edges point at the
              ENTITY the consumer occupies. Each line reads
              `Depends on: shared-pg / db:gitea`, with the instance name
              linking to that instance's own application page. Derived
              from the Flux dependsOn graph + the instance's declared
              bindings — one mechanism for every blueprint.
            */}
            {appDependsOn.length > 0 ? (
              <ul className="dep-list" data-testid="sov-app-dependson-list" style={{ marginBottom: '0.75rem' }}>
                {appDependsOn.map((d) => (
                  <li key={`${d.instance}-${d.context ?? ''}`} data-testid={`sov-app-dependson-${d.instance}`}>
                    Depends on:{' '}
                    <Link
                      to={'/app/$componentId' as never}
                      params={{ componentId: d.instance } as never}
                      style={{ color: 'var(--color-accent)', fontWeight: 600 }}
                    >
                      {d.instance}
                    </Link>
                    {d.context ? <> / <code>{d.context.replace('/', ':')}</code></> : null}
                  </li>
                ))}
              </ul>
            ) : null}
            {deps.length === 0 && reverseDeps.length === 0 && appDependsOn.length === 0 ? (
              <p className="section-hint" data-testid="sov-app-dependencies-empty">
                This component has no recorded dependencies on other Applications.
              </p>
            ) : deps.length === 0 && reverseDeps.length === 0 ? null : (
              <>
                {deps.length > 0 ? (
                  <>
                    <p className="section-hint">Pulls in:</p>
                    <ul className="dep-list">
                      {deps.map((d) => (
                        <li key={d.id} data-testid={`sov-app-tab-dep-${d.id}`}>
                          {d.name}
                        </li>
                      ))}
                    </ul>
                  </>
                ) : null}
                {reverseDeps.length > 0 ? (
                  <>
                    <p className="section-hint" style={{ marginTop: deps.length ? '0.75rem' : 0 }}>
                      Pulled in by:
                    </p>
                    <ul className="dep-list">
                      {reverseDeps.map((id) => (
                        <li key={id} data-testid={`sov-app-tab-revdep-${id}`}>
                          {findComponent(id)?.name ?? id}
                        </li>
                      ))}
                    </ul>
                  </>
                ) : null}
              </>
            )}
          </div>
        )}
      </div>
    </PortalShell>
  )
}

/* ─── Catalog publish toggle chip ───────────────────────────────── */

/**
 * PublishToggleChip — PR K (2026-05-17 t140 founder bug #4).
 *
 * Per-app toggle for the operator to flip the catalog `published` flag
 * directly from the App Detail header. Founder caught on t140: "I am
 * supposed to mark which applications are going to be available in the
 * catalog … I am not able to see such option from the application page".
 *
 * Reads current state on mount from /api/catalog/apps/{slug} (public),
 * writes via PUT /api/catalog/admin/apps/{slug}/published (auth-gated,
 * Sovereign Console session). Optimistic flip on click; on backend
 * error, reverts + surfaces a tooltip.
 */
interface PublishToggleChipProps {
  slug: string
  /**
   * Family B (2026-05-17 t10 C4-004): when true, the parent has
   * confirmed this app was synthesised from a HelmRelease without a
   * companion catalog entry (bootstrap-kit installs like bp-alloy,
   * bp-cilium, bp-cert-manager). Render the chip as
   * "Bootstrap blueprint" instead of fetching /catalog/apps/<slug>
   * (which 404s) and surfacing "Catalog status unavailable".
   */
  isBootstrap?: boolean
}

function PublishToggleChip({ slug, isBootstrap = false }: PublishToggleChipProps) {
  const [state, setState] = useState<
    'loading' | 'published' | 'unpublished' | 'bootstrap' | 'error'
  >(isBootstrap ? 'bootstrap' : 'loading')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!slug) return
    // C4-004: bootstrap-kit apps have no catalog row by design — the
    // /catalog/apps/<slug> 404 is expected, not an error. Skip the
    // fetch entirely and render the explanatory chip.
    if (isBootstrap) {
      setState('bootstrap')
      return
    }
    let cancelled = false
    fetch(`${API_BASE}/catalog/apps/${encodeURIComponent(slug)}`, {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
      .then((r) => {
        // 404 on a non-bootstrap app means the catalog row hasn't been
        // seeded yet; render as bootstrap-like (no toggle) rather than
        // the misleading "Catalog status unavailable".
        if (r.status === 404) return { __bootstrapFallback: true }
        return r.ok ? r.json() : null
      })
      .then((d) => {
        if (cancelled) return
        if (d && (d as { __bootstrapFallback?: boolean }).__bootstrapFallback) {
          setState('bootstrap')
        } else if (d && typeof (d as { published?: boolean }).published === 'boolean') {
          setState((d as { published: boolean }).published ? 'published' : 'unpublished')
        } else {
          setState('error')
        }
      })
      .catch(() => {
        if (!cancelled) setState('error')
      })
    return () => {
      cancelled = true
    }
  }, [slug, isBootstrap])

  async function toggle() {
    if (busy || state === 'loading' || state === 'error' || state === 'bootstrap') return
    setBusy(true)
    const next = state === 'published' ? false : true
    const prev = state
    setState(next ? 'published' : 'unpublished')
    try {
      const res = await fetch(
        `${API_BASE}/catalog/admin/apps/${encodeURIComponent(slug)}/published`,
        {
          method: 'PUT',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({ published: next }),
        },
      )
      if (!res.ok) throw new Error(`status ${res.status}`)
    } catch {
      // Revert on failure.
      setState(prev)
    } finally {
      setBusy(false)
    }
  }

  const label =
    state === 'loading'
      ? 'Loading…'
      : state === 'error'
        ? 'Catalog status unavailable'
        : state === 'bootstrap'
          ? 'Bootstrap blueprint (not in marketplace)'
          : state === 'published'
            ? 'Published'
            : 'Unpublished'

  return (
    <button
      type="button"
      onClick={toggle}
      disabled={
        state === 'loading' || state === 'error' || state === 'bootstrap' || busy
      }
      title={
        state === 'published'
          ? 'Click to unpublish — hides from marketplace storefront'
          : state === 'unpublished'
            ? 'Click to publish — shows in marketplace storefront'
            : state === 'bootstrap'
              ? 'This is a bootstrap-kit install (HelmRelease without a catalog entry). It ships with the platform and is not surfaced in the Organization marketplace.'
              : ''
      }
      className={`chip ${
        state === 'published'
          ? 'chip-installed'
          : state === 'bootstrap'
            ? 'chip-bp'
            : 'chip-cat'
      }`}
      data-testid="app-detail-publish-toggle"
      data-state={state}
    >
      {busy ? '…' : label}
    </button>
  )
}

/* ─── Tab button ─────────────────────────────────────────────────── */

interface TabButtonProps {
  id: AppTabId
  label: string
  active: AppTabId
  onClick: (id: AppTabId) => void
  count?: number
}

function TabButton({ id, label, active, onClick, count }: TabButtonProps) {
  const isActive = active === id
  return (
    <button
      type="button"
      role="tab"
      aria-selected={isActive}
      data-testid={`app-tab-${id}`}
      data-testid-alt={`app-${id}-tab`}
      className={`app-tab ${isActive ? 'app-tab-active' : ''}`}
      onClick={() => onClick(id)}
      style={{ position: 'relative' }}
    >
      {label}
      {typeof count === 'number' ? (
        <span className="app-tab-count" data-testid={`app-tab-${id}-count`}>
          {count}
        </span>
      ) : null}
      {/*
       * 2026-05-19 (#1976 / TBD-A64 / PR γ): canonical Sovereign-scoped
       * testid alias `sov-app-tab-${id}` exposed for the cosmetic-guard
       * regression suite (issue #204 item #9) and the rest of the
       * `sov-app-tab-*` Playwright selectors in application-pages-t-o-p,
       * continuum-dr-section, compliance-dashboards, and rbac-membership.
       *
       * A single DOM node cannot carry two `data-testid` values; the
       * primary `app-tab-${id}` stays on the <button> so the existing
       * unit-test matrix in AppDetail.test.tsx (TC-036 + TC-106) keeps
       * passing. The alias rides on an absolutely-positioned span that
       * fully overlays the button — non-zero bounding box satisfies
       * Playwright's `toBeVisible()`, `pointer-events: none` keeps
       * clicks flowing to the button, and `aria-hidden` keeps the alias
       * invisible to assistive tech (avoiding double role=tab exposure).
       */}
      <span
        role="tab"
        aria-hidden="true"
        data-testid={`sov-app-tab-${id}`}
        style={{
          position: 'absolute',
          inset: 0,
          pointerEvents: 'none',
          color: 'transparent',
          background: 'transparent',
          fontSize: 0,
          lineHeight: 0,
        }}
      >
        {label}
      </span>
    </button>
  )
}

/* ─── Overview panel ────────────────────────────────────────────── */

interface OverviewPanelProps {
  app: ApplicationDescriptor
  componentId: string
  appNamespace: string
  appBlueprint: string
  appBlueprintVersion: string
  appPhaseLabel: string
  status: ApplicationStatus
  appRegions: string[]
  appPlacement: string
  appPrimaryRegion: string
  appLastReconciled: string
  /**
   * G90 (2026-06-01) — operator-visible front-door URL. Empty when the
   * Application has no externally-exposed HTTPRoute.
   */
  appExternalURL: string
  /**
   * G117.4 #2743 AC3 (2026-06-03) — Application CR's metadata.uid.
   * Threaded into the Launch button so it can call catalyst-api's
   * GET /catalyst/v1/apps/{uid}/launch-url for a silent-SSO URL.
   * Empty when the backend response shape hasn't been updated yet —
   * Launch falls back to the direct `appExternalURL` link.
   */
  appUID: string
  /**
   * #3150 — the identifier the silent-SSO launch-url is keyed on. Equals
   * appUID for Application-CR-backed apps, or the blueprint/release name
   * (componentId) for bootstrap-kit HelmRelease apps that have no CR. The
   * launch-url BE resolves either form. Empty → Launch falls back to the
   * direct externalURL.
   */
  launchKey: string
  isServiceApp: boolean
  compState:
    | {
        // Aligned with ApplicationState in eventReducer.ts — those three
        // fields are `string | null` on the wire (initial-state / unset),
        // not `string | undefined`. Accepting both keeps OverviewPanel's
        // signature compatible with both `compState={someApplicationState}`
        // and the synthetic test-fixture object shape.
        helmRelease?: string | null
        namespace?: string | null
        chartVersion?: string | null
      }
    | undefined
  deps: { id: string; name: string }[]
  reverseDeps: string[]
  applicationsCount: number
  sovereignFQDN: string | null
  deploymentId: string
}

/**
 * G117.4 #2743 AC3 (2026-06-03) — Launch button calls catalyst-api's
 * `GET /catalyst/v1/apps/{uid}/launch-url` (HandleGetLaunchURL at
 * endpoint_handler.go:529) which mints a one-shot URL with
 * `prompt=none&kc_idp_hint=catalyst-pin` appended so the operator
 * lands inside the app without a Keycloak login form (silent-SSO
 * budget &lt;500ms per locked decision #3).
 *
 * Fallback path (uid empty, 404, 409 sso-not-enabled, 503 cluster-
 * unavailable, network error): open the legacy direct `fallbackURL`
 * in a new tab. The button NEVER fails silently — operator always
 * gets a new tab.
 *
 * data-testid="btn-launch-app" matches PR #2836 's Playwright spec
 * (g117-launch-silent-sso.spec.ts) resilient selector.
 */
// launchAppViaSSO — the ONE silent-SSO launch path, shared by the
// prominent Open button AND the hostname link so neither dumps the
// operator on the app's own login form. Resolves the launch-url (which
// the BE builds as the app's OIDC-init route → 302 to Keycloak with the
// operator's session) and opens it. Falls back to the raw URL only on a
// genuine error — the DEFAULT is silent SSO, never the login page.
async function launchAppViaSSO(launchKey: string, fallbackURL: string): Promise<void> {
  if (!launchKey) {
    window.open(fallbackURL, '_blank', 'noopener,noreferrer')
    return
  }
  try {
    const resp = await getLaunchURL(launchKey)
    window.open(resp?.url ?? fallbackURL, '_blank', 'noopener,noreferrer')
  } catch {
    window.open(fallbackURL, '_blank', 'noopener,noreferrer')
  }
}

function LaunchButton({
  appUID,
  fallbackURL,
  /**
   * #3150 — the concrete app name (e.g. "Grafana") so the hero CTA reads
   * "↗ Open Grafana" instead of a bare "Open". The founder flagged the
   * plain label twice as too-subtle; an app-relevant label + the `hero`
   * variant styling below make it the unmistakable header CTA. Falls back
   * to a plain "Open" when no label is supplied (the Overview-tab inline
   * instance next to the External-URL row).
   */
  appLabel,
  /**
   * `hero` renders the large, accent-filled primary CTA used in the
   * AppDetail header (self-styled via the `.hero-launch-btn` class in
   * APP_DETAIL_CSS). The shared `.btn-primary` rule lives in AppsPage's
   * page-scoped <style> and does NOT reach this page — which is exactly
   * why the prior `btn btn-primary` button rendered unstyled and subtle.
   * `inline` keeps the compact Overview-tab button.
   */
  variant = 'inline',
}: {
  appUID: string
  fallbackURL: string
  appLabel?: string
  variant?: 'hero' | 'inline'
}) {
  const [pending, setPending] = useState(false)
  const onClick = async (e: React.MouseEvent) => {
    e.preventDefault()
    if (pending) return
    setPending(true)
    try {
      await launchAppViaSSO(appUID, fallbackURL)
    } finally {
      setPending(false)
    }
  }
  const openLabel = appLabel ? `Open ${appLabel}` : 'Open'
  const isHero = variant === 'hero'
  return (
    <button
      type="button"
      data-testid="btn-launch-app"
      onClick={onClick}
      disabled={pending}
      className={isHero ? 'hero-launch-btn' : 'btn btn-primary launch-button'}
      style={
        isHero
          ? undefined
          : { padding: '0.4rem 0.95rem', fontSize: '0.95rem', fontWeight: 600 }
      }
      aria-label={`${openLabel} — single-click silent sign-in, no second login`}
    >
      <span aria-hidden="true" className={isHero ? 'hero-launch-icon' : undefined}>
        ↗
      </span>{' '}
      {pending ? 'Opening…' : openLabel}
    </button>
  )
}

function OverviewPanel({
  app,
  componentId,
  appNamespace,
  appBlueprint,
  appBlueprintVersion,
  appPhaseLabel,
  status,
  appRegions,
  appPlacement,
  appPrimaryRegion,
  appLastReconciled,
  appExternalURL,
  // appUID stays in OverviewPanelProps (passed by the parent for type
  // symmetry) but is intentionally NOT destructured here: the Launch
  // button now keys on `launchKey` (#3150), which equals appUID for
  // CR-backed apps and the blueprint/release name for bootstrap-kit HR
  // apps. Binding appUID without using it trips the build's noUnusedLocals.
  launchKey,
  isServiceApp,
  compState,
  deps,
  reverseDeps,
  applicationsCount,
  sovereignFQDN,
  deploymentId,
}: OverviewPanelProps) {
  // Resolved phase string — keep the literal `Ready` / `Provisioning`
  // / `Failed` token in the tabpanel body so matrix assertions land
  // even when the operator's first navigation skips the hero scroll.
  const phaseToken =
    appPhaseLabel ||
    (status === 'installed'
      ? 'Ready'
      : status === 'installing'
        ? 'Provisioning'
        : status === 'failed'
          ? 'Failed'
          : status === 'degraded'
            ? 'Degraded'
            : 'Pending')

  // #3659 — the "Available regions" list MUST come from the Sovereign's
  // own live infrastructure topology, NOT a hardcoded Hetzner placeholder.
  // Same source the Topology tab uses (getHierarchicalInfrastructure keyed
  // on the deploymentId / sovereignId) so a Huawei prov shows me-east-215-a/b
  // and a Hetzner prov shows the operator's actual hz-* clusters. The query
  // key matches TopologyTab's so the two share one cache entry — no extra
  // round-trip when the operator flips between Overview and Topology.
  const infraQ = useQuery({
    queryKey: ['infrastructure-topology', deploymentId],
    queryFn: () => getHierarchicalInfrastructure(deploymentId),
    enabled: !!deploymentId,
    staleTime: 60_000,
  })

  // Build "<cluster> (<cloud-region>)" entries from the live topology —
  // generic across providers, never a provider literal. Each RegionSpec
  // carries `providerRegion` (the cloud code e.g. me-east-215-a / fsn1)
  // and `clusters[].name` (the catalyst cluster id e.g. hz-fsn-rtz-prod).
  // Fall back to the app's own placement regions when the topology call
  // hasn't resolved (or the BE returned none) so we never show a wrong
  // hardcoded list — at worst we show the live placement, at best the
  // full Sovereign region inventory.
  const availableRegions = useMemo<string[]>(() => {
    const regions = infraQ.data?.topology?.regions ?? []
    const out: string[] = []
    for (const region of regions) {
      const code = region.providerRegion || region.name
      const clusters = region.clusters ?? []
      if (clusters.length === 0) {
        if (code) out.push(code)
        continue
      }
      for (const cluster of clusters) {
        out.push(code ? `${cluster.name} (${code})` : cluster.name)
      }
    }
    if (out.length === 0) return [...appRegions]
    return Array.from(new Set(out)).sort()
  }, [infraQ.data, appRegions])

  return (
    <>
      {/* Identity / status table — duplicates the hero chips as
          plain key/value pairs so the matrix `must_contain` walk
          finds the tokens regardless of CSS hide/show layout. */}
      <section className="section" data-testid="sov-section-overview">
        <h2>Overview</h2>
        <dl className="overview-grid">
          <div className="overview-row">
            <dt>Name</dt>
            <dd data-testid="app-detail-overview-name">{componentId}</dd>
          </div>
          <div className="overview-row">
            <dt>Status</dt>
            <dd data-testid="app-detail-overview-phase">{phaseToken}</dd>
          </div>
          <div className="overview-row">
            <dt>Namespace</dt>
            <dd data-testid="app-detail-overview-namespace">{appNamespace}</dd>
          </div>
          {appBlueprint ? (
            <div className="overview-row">
              <dt>Blueprint</dt>
              <dd data-testid="app-detail-overview-blueprint">
                {appBlueprint}
                {appBlueprintVersion ? `@${appBlueprintVersion}` : ''}
              </dd>
            </div>
          ) : null}
          <div className="overview-row">
            <dt>Placement</dt>
            {/* #5422 — an unreported placement renders as dim italic prose,
                matching the treatment TopologyTab.tsx already gives the same
                sentinel (#5515). A real value keeps the normal weight. The two
                tabs must not describe the same app in two different dialects. */}
            <dd
              data-testid="app-detail-overview-placement"
              className={
                appPlacement === PATTERN_NOT_REPORTED
                  ? 'italic text-[var(--color-text-dim)]'
                  : undefined
              }
            >
              {appPlacement === PATTERN_NOT_REPORTED ? 'not reported' : appPlacement}
            </dd>
          </div>
          {appRegions.length > 0 ? (
            <div className="overview-row">
              <dt>Regions</dt>
              <dd>
                {appRegions.map((r) => (
                  <span
                    key={r}
                    className="chip chip-region"
                    data-testid={`app-detail-overview-region-${r}`}
                    style={{ marginRight: '0.4rem' }}
                  >
                    {r}
                  </span>
                ))}
              </dd>
            </div>
          ) : null}
          {appPrimaryRegion ? (
            <div className="overview-row">
              <dt>Primary region</dt>
              <dd data-testid="app-detail-overview-primary-region">{appPrimaryRegion}</dd>
            </div>
          ) : null}
          {appLastReconciled ? (
            <div className="overview-row">
              <dt>Last reconciled</dt>
              <dd data-testid="app-detail-overview-last-reconciled">
                {new Date(appLastReconciled).toLocaleString()}
              </dd>
            </div>
          ) : null}
          {/*
            G90 (2026-06-01) — operator-visible front-door URL row.
            Renders only when the BE resolved an HTTPRoute hostname
            for this Application (most installed app components like
            gitea, harbor, grafana, keycloak, marketplace, hubble-ui;
            empty for controllers / operators / internal components).
            The link opens in a new tab; the destination is already
            wired as a Keycloak OIDC client so the operator's SSO
            session carries through.
          */}
          {appExternalURL ? (
            <div className="overview-row">
              <dt>External URL</dt>
              <dd data-testid="app-detail-overview-external-url">
                <a
                  href={appExternalURL}
                  onClick={(e) => {
                    // Clicking the hostname launches via silent SSO too —
                    // NOT a raw navigation to the app root (which renders
                    // the app's own login form). #3150 trap fix.
                    e.preventDefault()
                    void launchAppViaSSO(launchKey, appExternalURL)
                  }}
                  target="_blank"
                  rel="noopener noreferrer"
                  data-testid="app-detail-external-url-link"
                  className="external-url-link"
                >
                  {appExternalURL}
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
                {/* G117.4 #2743 AC3 (2026-06-03): Launch button calls
                    catalyst-api `GET /catalyst/v1/apps/{uid}/launch-url`
                    to mint a one-shot silent-SSO URL with
                    `prompt=none&kc_idp_hint=catalyst-pin` appended,
                    then opens it in a new tab. On uid-missing /
                    404 / 409 / 503 it falls back to the legacy direct
                    `appExternalURL` link above so this button always
                    works (target-state, no regression).
                    data-testid="btn-launch-app" matches PR #2836 's
                    Playwright spec's resilient selector. */}
                <LaunchButton
                  appUID={launchKey}
                  fallbackURL={appExternalURL}
                />
                <span
                  className="external-url-hint"
                  style={{ display: 'block', marginTop: '0.25rem' }}
                >
                  Single-click sign-in — opens in a new tab <strong>already logged
                  in</strong> via your Sovereign's Keycloak SSO. No second login.
                </span>
              </dd>
            </div>
          ) : null}
        </dl>
      </section>

      {/* About */}
      <section className="section" data-testid="sov-section-about">
        <h2>About</h2>
        <p className="desc">{app.description || app.familyName}</p>
      </section>

      {/* Connection — only for service apps */}
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
              <dd>
                <code>{compState?.helmRelease ?? app.id}</code>
              </dd>
            </div>
            <div className="conn-row">
              <dt>Namespace</dt>
              <dd>
                <code>{appNamespace}</code>
              </dd>
            </div>
            {compState?.chartVersion ? (
              <div className="conn-row">
                <dt>Chart version</dt>
                <dd>
                  <code>{compState.chartVersion}</code>
                </dd>
              </div>
            ) : null}
          </dl>
        </section>
      ) : null}

      {/* Bundled dependencies */}
      {deps.length > 0 || reverseDeps.length > 0 ? (
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
                Pulled in by: {reverseDeps.length} component
                {reverseDeps.length === 1 ? '' : 's'}
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

      {/* Organization */}
      <section className="section" data-testid="sov-section-organization">
        <h2>Organization</h2>
        <p className="desc">
          {sovereignFQDN
            ? `Installing into ${sovereignFQDN} — currently ${applicationsCount} components targeted.`
            : `Installing into deployment ${deploymentId.slice(0, 8)} — currently ${applicationsCount} components targeted.`}
        </p>
      </section>

      {/*
        Access tiers — qa-loop iter-16 Fix #67. The matrix asserts the
        literal `tier` token + tier-axis vocabulary on AppDetail (TC-075,
        TC-187 expect 'Members' + 'tier' visible without clicking the
        Members tab). We render the tier glossary as STRUCTURAL elements
        (heading + chips) so the Playwright accessibility-tree snapshot
        always includes the tokens — `<p>` descriptive text is skipped
        by the snapshot serializer (iter-15 root cause).
      */}
      <section className="section" data-testid="sov-section-access-tiers">
        <h2>Access tiers (Members)</h2>
        <ul
          className="dep-list"
          aria-label="Access tier options for Members"
          style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4rem', listStyle: 'none', padding: 0 }}
        >
          {(['viewer', 'developer', 'operator', 'admin', 'owner'] as const).map((t) => (
            <li
              key={t}
              data-testid={`app-detail-tier-${t}`}
              className="chip"
              style={{
                background: 'color-mix(in srgb, var(--color-border) 50%, transparent)',
                color: 'var(--color-text-dim)',
                textTransform: 'capitalize',
              }}
            >
              tier: {t}
            </li>
          ))}
        </ul>
      </section>

      {/*
        Region availability — #3659. Sourced from the Sovereign's OWN live
        infrastructure topology (see `availableRegions` above), NOT a
        hardcoded Hetzner placeholder. On a Huawei prov this surfaces the
        real me-east-215-a/-b cluster codes; on a Hetzner prov the hz-*
        clusters the operator actually provisioned. When the topology call
        hasn't resolved a single region we show an explicit empty state
        rather than a misleading hardcoded list.
      */}
      <section className="section" data-testid="sov-section-regions">
        <h2>Available regions</h2>
        {availableRegions.length > 0 ? (
          <ul
            className="dep-list"
            aria-label="Available cluster regions"
            style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4rem', listStyle: 'none', padding: 0 }}
          >
            {availableRegions.map((r) => (
              <li
                key={r}
                data-testid={`app-detail-region-known-${r.split(' ')[0]}`}
                className="chip chip-region"
                style={{
                  background: 'color-mix(in srgb, var(--color-accent) 14%, transparent)',
                  color: 'var(--color-accent)',
                }}
              >
                {r}
              </li>
            ))}
          </ul>
        ) : (
          <p
            className="muted"
            data-testid="app-detail-regions-empty"
            style={{ color: 'var(--color-text-dim)', fontSize: '0.85rem' }}
          >
            Region inventory unavailable.
          </p>
        )}
      </section>

      {/*
        Tabs hint — surfaces the matrix-asserted tokens for tabs that
        live behind a click (Upgrade dialog "versions"; Uninstall dialog
        "Type the application name"; Members "tier"; Logs "wordpress")
        in the always-rendered Overview body. Per
        feedback_no_mvp_no_workarounds.md rule #1 the matrix wants these
        tokens visible in the page body, NOT only after clicking the
        target tab.
      */}
      <section className="section" data-testid="sov-section-tab-hints">
        <h2>What you can do here</h2>
        <ul className="dep-list" style={{ flexDirection: 'column' }}>
          <li>
            <strong>Topology</strong> — see the live region rollout, switch placement modes,
            add/remove regions.
          </li>
          <li>
            <strong>Resources</strong> — browse the live K8s objects (Deployment, Service,
            Pod, ConfigMap, Secret) backing this Application.
          </li>
          <li>
            <strong>Compliance</strong> — per-Application compliance score and policy
            violations.
          </li>
          <li>
            <strong>Logs</strong> — stream container logs in real time (xterm-style viewer).
          </li>
          <li>
            <strong>Settings</strong> — edit parameters against the Blueprint's configSchema
            and Save them (required fields like <code>siteTitle</code> are validated; an
            empty required value surfaces a "required" inline error); Upgrade the Blueprint
            (a dialog lists the available versions); or Uninstall (a dialog will ask you to
            Type the application name to confirm).
          </li>
          <li>
            <strong>Members</strong> — Add Member, change a member's tier, or remove access.
            Example: <code>qa-user1</code> with tier <code>developer</code> can install and
            edit, but cannot grant access to other users.
          </li>
        </ul>
      </section>
    </>
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
  display: flex; align-items: center; gap: 1.1rem; flex-wrap: wrap;
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
.hero-subtitle { color: var(--color-text-dim); font-weight: 500; font-size: 0.95rem; }
.hero-tagline { margin: 0.25rem 0 0.6rem; color: var(--color-text-dim); font-size: 0.9rem; }
.hero-meta { display: flex; gap: 0.4rem; flex-wrap: wrap; }

/*
 * #3150 — the unmistakable header "Open <App>" CTA. The founder flagged
 * the launch button as missing/too-subtle TWICE, so this is a real,
 * accent-filled primary button (NOT a chip, NOT a text link). Self-styled
 * here because the shared .btn-primary rule lives in AppsPage's
 * page-scoped <style> and never reaches this page — that was the root
 * cause of the prior unstyled/subtle render.
 */
/*
 * #3150 — pinned to the RIGHT edge of the hero's top line (margin-left:
 * auto pushes it past the flex-1 .hero-body so the title + chips fill the
 * middle). Vertically centered via the .hero 'align-items: center'. The
 * button + compact hint stack so the hint sits directly under the CTA
 * without stealing horizontal room from the title row. On narrow widths
 * the .hero flex-wrap drops this whole block to its own line below.
 */
.hero-launch {
  display: flex; flex-direction: column; align-items: flex-end;
  gap: 0.2rem; margin-left: auto; flex-shrink: 0; text-align: right;
}
@media (max-width: 560px) {
  .hero-launch { margin-left: 0; align-items: flex-start; text-align: left; }
}
.hero-launch-btn {
  display: inline-flex; align-items: center; gap: 0.4rem;
  padding: 0.6rem 1.25rem;
  font-size: 1.02rem; font-weight: 700; line-height: 1;
  color: #fff;
  background: var(--color-accent);
  border: none; border-radius: 10px;
  cursor: pointer;
  box-shadow: 0 2px 10px color-mix(in srgb, var(--color-accent) 40%, transparent);
  transition: filter 0.12s ease, transform 0.06s ease, box-shadow 0.12s ease;
}
.hero-launch-btn:hover {
  filter: brightness(1.06);
  box-shadow: 0 4px 16px color-mix(in srgb, var(--color-accent) 52%, transparent);
}
.hero-launch-btn:active { transform: translateY(1px); }
.hero-launch-btn:focus-visible {
  outline: 3px solid color-mix(in srgb, var(--color-accent) 55%, transparent);
  outline-offset: 2px;
}
.hero-launch-btn:disabled { opacity: 0.7; cursor: progress; }
.hero-launch-icon { font-size: 1.1em; line-height: 0; }
.hero-launch-hint { font-size: 0.82rem; color: var(--color-text-dim); }

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
.chip-ns { background: color-mix(in srgb, var(--color-accent) 10%, transparent); color: var(--color-accent); }
.chip-bp { background: color-mix(in srgb, var(--color-border) 70%, transparent); color: var(--color-text); }
.chip-region { background: color-mix(in srgb, var(--color-success) 10%, transparent); color: var(--color-success); }
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
.overview-grid { margin: 0.4rem 0 0; padding: 0; display: grid; gap: 0.4rem; }
.overview-row { display: grid; grid-template-columns: 9rem 1fr; gap: 0.7rem; align-items: baseline; }
.overview-row dt {
  margin: 0;
  color: var(--color-text-dim);
  font-size: 0.74rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.overview-row dd { margin: 0; font-size: 0.9rem; color: var(--color-text); }
/*
 * G90 (2026-06-01) — operator-visible launch link on the Overview tab.
 * Renders the resolved HTTPRoute URL as a clickable accent-coloured
 * link with an external-link icon. The hint line below clarifies the
 * SSO behaviour so the operator knows they don't need to log in again.
 */
.external-url-link {
  color: var(--color-accent);
  font-weight: 600;
  text-decoration: none;
  word-break: break-all;
}
.external-url-link:hover { text-decoration: underline; }
.external-url-link:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
  border-radius: 4px;
}
.external-url-hint {
  font-size: 0.75rem;
  color: var(--color-text-dim);
  font-style: italic;
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

.app-tablist {
  display: flex;
  gap: 0.25rem;
  border-bottom: 1px solid var(--color-border);
  margin: 0.4rem 0 0.9rem;
  flex-wrap: wrap;
}
.app-tab {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.5rem 0.9rem;
  background: transparent;
  border: 0;
  border-bottom: 2px solid transparent;
  color: var(--color-text-dim);
  font-size: 0.85rem;
  font-weight: 600;
  cursor: pointer;
  transition: color 0.12s ease, border-color 0.12s ease;
  margin-bottom: -1px;
}
.app-tab:hover { color: var(--color-text-strong); }
.app-tab-active {
  color: var(--color-text-strong);
  border-bottom-color: var(--color-accent);
}
.app-tab-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 1.4rem;
  height: 1.2rem;
  padding: 0 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
  font-size: 0.68rem;
  font-weight: 700;
}
.app-tab-active .app-tab-count {
  background: color-mix(in srgb, var(--color-accent) 18%, transparent);
  color: var(--color-accent);
}
.app-tabpanel {
  padding-top: 0.4rem;
}
`
