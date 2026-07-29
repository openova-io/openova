/**
 * AppsPage — pixel-port of core/console/src/components/AppsPage.svelte.
 *
 * Layout (top-down, byte-identical to canonical class names):
 *   • Header row: <h1>Applications</h1> + tagline + (provisioning pill
 *     OR install-history link) right-aligned.
 *   • #3601 (EPIC #3597): the page is TAB-LESS. The old
 *     "Deployments | Catalog" tab strip is gone — /apps shows only the
 *     installed-deployments grid, and the catalog moved to its own
 *     left-nav page (`/catalog` → CatalogPage). No Deployments/Catalog
 *     tabs render here any more.
 *   • Search row: `.apps-toolbar > .search-wrap > .search-icon +
 *     .search-input` — visually identical to canonical AppsPage.
 *   • Card grid: `.apps-grid` (`grid-template-columns: repeat(auto-fit,
 *     minmax(360px, 1fr))`). Each `.app-card` is a clickable surface
 *     navigating to AppDetail. `state-installed / state-installing /
 *     state-failed / state-pending` modifier classes flow through.
 *   • Empty state: `.empty-state` when the Deployments tab is empty.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full grid), the cards render from the moment the page mounts, before
 * any /events have arrived. Each card starts in `pending` and flips
 * states through the reducer.
 *
 * Per #2 (no MVP / iterative shortcuts), the canonical empty-state
 * affordance is preserved verbatim — clicking "Open catalog →" flips
 * the tab without a separate spinner state.
 *
 * Per #4 (never hardcode), the application list is computed by
 * `resolveApplications()` from the wizard store + bootstrap kit; there
 * is no hand-maintained id list in this file.
 */

import { useEffect, useMemo, useState } from 'react'
import { useRouter, Link, useParams, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { sovereignPathOrDeployments } from '@/shared/lib/sovereignPaths'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'
import { useWizardStore } from '@/entities/deployment/store'
import { useConsoleScope } from '@/shared/lib/useConsoleScope'
import { PortalShell } from './PortalShell'
import { resolveApplications, type ApplicationDescriptor } from './applicationCatalog'
import { findComponent } from '@/pages/wizard/steps/componentGroups'
import { BLUEPRINT_BY_ID } from '@/shared/constants/catalog.generated'
import { getLaunchURL } from '@/lib/catalog.api'
import { useDeploymentEvents } from './useDeploymentEvents'
import type { ApplicationStatus } from './eventReducer'
import { WipeDeploymentModal } from '@/components/CrudModals/WipeDeploymentModal'
import { useNotifications } from '@/shared/ui/notifications'
import { isDeploymentID } from '@/shared/types/deployment'
import { useTheme } from '@/shared/lib/useTheme'
import { resolveCatalogIcon } from '@/shared/lib/resolveCatalogIcon'

/**
 * topologyLabel — #4897. The apps-list `topology` field is normally the
 * canonical placement-posture STRING (the BFF projects it via readTopology).
 * The Application's `spec.placement` is dual-form, though: a legacy posture
 * string OR an object `{ mode, regions }` — the shape a New-instance
 * active-hot-standby create writes. This normaliser keeps the topology badge
 * resilient to BOTH shapes reaching the grid:
 *   • string  → used as-is (e.g. "active-hot-standby", "singleton")
 *   • object  → read `.mode` (the posture)
 *   • neither → '' (no badge)
 * The load-bearing fix is server-side (the BFF now projects the object
 * form's mode); this guard keeps the grid correct if the raw dual-form
 * placement ever reaches the FE.
 */
export function topologyLabel(value: unknown): string {
  if (typeof value === 'string') return value
  if (
    value &&
    typeof value === 'object' &&
    typeof (value as { mode?: unknown }).mode === 'string'
  ) {
    return (value as { mode: string }).mode
  }
  return ''
}

interface AppsPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

export function AppsPage({ disableStream = false }: AppsPageProps = {}) {
  const params = useResolvedDeploymentId() as {
    deploymentId: string | null
  }
  // The route param is a `string` from TanStack — validate it against
  // the branded `DeploymentID` shape at the boundary so a 15-char
  // truncation OR a 17-char overflow is impossible to thread further
  // down. We use `isDeploymentID` (rather than throwing parse) so the
  // page can render its own malformed-id banner without crashing the
  // route. Closes issues #749 + #754.
  const rawDeploymentId = params.deploymentId ?? ''
  const deploymentIdValid = isDeploymentID(rawDeploymentId)
  const deploymentId = rawDeploymentId
  const router = useRouter()
  const store = useWizardStore()

  // #4113 / #4119 — Org-console scope. On an Org-scoped customer session
  // (whoami.orgScoped, host console.<org>.<pool>) the page must render the
  // Org's OWN application estate (from /api/v1/org/applications) and must NOT
  // mount the Sovereign provisioning/deployment-stream surface (which 403s for
  // an Org session → the "couldn't reach the deployment stream" banner). While
  // the scope is still loading we treat the session as sovereign (the common
  // case) — but the deployment-stream stays disabled until proven sovereign so
  // a customer never flashes the provisioning banner; the catalyst-api 403s
  // the data regardless (hide AND enforce).
  const { orgScoped, loading: scopeLoading } = useConsoleScope()
  const deploymentStreamEnabled = !orgScoped && !scopeLoading

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot, streamStatus, streamError, retry, handoverReady } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
    // #4119 — never attach the deployment-stream on an Org session.
    enabled: deploymentStreamEnabled,
  })

  // On Sovereign mode the imported reducer state is FROZEN at mother's
  // last snapshot (every app reads "pending"). Layer in live status
  // from /api/v1/sovereign/apps so the AppCards reflect ground truth.
  // Server status enum: "installed" | "installing" | "bootstrap" |
  // "available" — map to the UI's ApplicationStatus vocabulary.
  // Caught on omantel.biz 2026-05-06.
  const isSovereignMode = DETECTED_MODE.mode === 'sovereign'
  interface LiveAppsData {
    statusById: Record<string, ApplicationStatus>
    publishedBySlug: Record<string, boolean>
    /**
     * environmentById — per-app environment label (e.g. "dev", "prod",
     * "staging") sourced from the Application CR's spec.environmentRef
     * by the BE handler. Always populated for every row (BE falls back
     * to "dev" when no CR matches), so the FE always has a chip to
     * render. Closes qa-loop iter-7 TC-090.
     */
    environmentById: Record<string, string>
    /**
     * externalURLById — front-door URL (https://<host>) the operator
     * clicks to open this installed Application in a new browser tab
     * with their existing Keycloak SSO session active. Populated by the
     * BE by joining each HR's (targetNamespace, releaseName) against
     * the cluster's HTTPRoute set. Missing when the app has no
     * externally-exposed route (controllers, operators, internal
     * components). G90 2026-06-01.
     */
    externalURLById: Record<string, string>
    /**
     * instances — #3370. One entry per RUNNING INSTANCE (Application
     * CR) the BE projects (`instance: true` rows): a blueprint with N
     * instances ⇒ exactly N cards on the Deployments tab, each with
     * its topology chip + ⛓ contexts badge.
     */
    instances: LiveInstanceRow[]
  }
  interface LiveInstanceRow {
    id: string
    slug: string
    blueprint: string
    topology: string
    contextCount: number
    status: ApplicationStatus
    environment: string
    externalURL: string
    bootstrapKit: boolean
  }
  const liveAppsQuery = useQuery<LiveAppsData>({
    // #4113 — the query key carries the scope so flipping between a sovereign
    // and an org session (or scope resolving) re-fetches against the right
    // endpoint rather than serving a cached cross-scope estate.
    queryKey: ['sovereign-apps-live', orgScoped ? 'org' : 'sovereign'],
    // Hold the fetch until the scope is known so an Org session never briefly
    // hits /sovereign/apps (the full catalog) before /org/applications.
    enabled: isSovereignMode && !scopeLoading,
    refetchInterval: 5_000,
    queryFn: async () => {
      // #4113 — Org-scoped session reads its OWN estate from
      // /api/v1/org/applications (the per-Org projection: only Application CRs
      // + HelmReleases in the Org's own namespace, each with its open URL).
      // A sovereign-admin session keeps the cluster-wide /sovereign/apps feed.
      const endpoint = orgScoped ? '/v1/org/applications' : '/v1/sovereign/apps'
      const headers: HeadersInit = orgScoped
        ? {
            Accept: 'application/json',
            // The org-scoped projection resolves the caller's own Org from the
            // request host; X-Tenant-Host carries it (same contract as the
            // org/users + org/applications-install clients).
            'X-Tenant-Host': typeof window !== 'undefined' ? window.location.host : '',
          }
        : { Accept: 'application/json' }
      const r = await authedFetch(`${API_BASE}${endpoint}`, { headers })
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
          blueprint?: string
          // #4897 — normally the projected posture string; tolerate the raw
          // dual-form placement object ({ mode, regions }) too (topologyLabel).
          topology?: string | { mode?: string }
          contextCount?: number
          bootstrapKit?: boolean
        }>
      }
      const statusById: Record<string, ApplicationStatus> = {}
      const publishedBySlug: Record<string, boolean> = {}
      const environmentById: Record<string, string> = {}
      const externalURLById: Record<string, string> = {}
      const instances: LiveInstanceRow[] = []
      for (const a of body.apps ?? []) {
        if (!a.id) continue
        if (a.instance === true) {
          // #3370 — instance rows render as their OWN cards below;
          // they are not part of the blueprint-status overlay.
          instances.push({
            id: a.id,
            slug: a.slug ?? '',
            blueprint: a.blueprint ?? '',
            topology: topologyLabel(a.topology),
            contextCount: a.contextCount ?? 0,
            status: a.status === 'installed' ? 'installed' : 'installing',
            environment: a.environment ?? 'dev',
            externalURL: a.externalURL ?? '',
            bootstrapKit: a.bootstrapKit === true,
          })
          continue
        }
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
            // Catalog item not yet installed → render as "AVAILABLE"
            // with an "Install" affordance, NOT a misleading "PENDING"
            // pill. Caught on omantel.biz 2026-05-07 — KEDA showed
            // PENDING with no install action.
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
      return { statusById, publishedBySlug, environmentById, externalURLById, instances }
    },
    retry: false,
    placeholderData: (prev) => prev,
  })
  const liveAppStatus = liveAppsQuery.data?.statusById ?? {}
  const publishedBySlug = liveAppsQuery.data?.publishedBySlug ?? {}
  const environmentById = liveAppsQuery.data?.environmentById ?? {}
  const externalURLById = liveAppsQuery.data?.externalURLById ?? {}
  const liveInstances = liveAppsQuery.data?.instances ?? []
  const refetchLive = liveAppsQuery.refetch
  // UNRESOLVED_APP_ENVIRONMENT is the honest stand-in when no Environment
  // so even when the live API hasn't responded yet (cold load) every
  // card still renders an environment chip — the matrix's TC-090
  // contract is invariant to fetch state. Closes qa-loop iter-7 TC-090.
  // #5489 — this was 'dev', a name no Environment on any Sovereign carries.
  // On hw291 it painted 42 of 50 cards with a membership that does not exist
  // (both Environment CRs are envType: prod), so the chip could not
  // distinguish a resolved binding from an absent one. TC-090's contract is
  // that a chip RENDERS, not that it names a real Environment when none
  // resolved — an explicit unresolved marker satisfies that honestly.
  const UNRESOLVED_APP_ENVIRONMENT = 'unassigned'

  const isFailed = streamStatus === 'failed' || streamStatus === 'unreachable'
  const failureMessage = streamError ?? snapshot?.error ?? null
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  // Wipe modal is owned by AppsPage so the "Cancel & Wipe" toast action
  // can flip it open. The toast itself is mounted by the global tray; the
  // modal stays here because it owns the destructive POST + spinner.
  const [showWipeModal, setShowWipeModal] = useState(false)
  const onWipeComplete = () => {
    setShowWipeModal(false)
    router.navigate({ to: '/wizard' })
  }

  // Surface terminal-failure + phase-1-skipped state as global toasts
  // (founder #475 — Apps page must render only the apps grid). Same
  // copy + same actions as the legacy banner, just delivered via the
  // `useNotifications` seam so they stay visible across tabs and don't
  // fight the apps grid for vertical space.
  const { notify, dismiss } = useNotifications()
  useEffect(() => {
    const id = `deployment-failure:${deploymentId}`
    // #4119 — the deployment-stream failure toast (with the "Retry stream /
    // Cancel & Wipe / Back to wizard" actions) is a SOVEREIGN provisioning
    // surface. It must never render for an Org-scoped customer session. The
    // hook is already disabled for org sessions (so isFailed stays false), but
    // we hard-gate here too so a customer can never see it.
    if (!isFailed || !deploymentStreamEnabled) {
      dismiss(id)
      return
    }
    const isUnreachable = streamStatus === 'unreachable'
    notify({
      id,
      level: 'error',
      title: isUnreachable
        ? 'Couldn’t reach the deployment stream'
        : 'Provisioning failed',
      body: isUnreachable
        ? `The catalyst-api is unreachable, or deployment ${deploymentId} is unknown to the backend.`
        : `The catalyst-api emitted a terminal failure for deployment ${deploymentId}.`,
      raw: failureMessage ?? undefined,
      actions: [
        {
          label: 'Retry stream',
          variant: 'primary',
          testId: 'sov-failure-retry',
          onClick: retry,
          dismissOnClick: false,
        },
        {
          label: 'Cancel & Wipe',
          variant: 'danger',
          testId: 'sov-failure-wipe',
          onClick: () => setShowWipeModal(true),
          dismissOnClick: false,
        },
        {
          label: 'Back to wizard',
          variant: 'ghost',
          testId: 'sov-failure-back',
          onClick: () => router.navigate({ to: '/wizard' }),
        },
      ],
    })
    // The dismiss-on-recovery branch above handles cleanup; no return
    // closure needed because notification ids are stable per deployment.
  }, [isFailed, streamStatus, failureMessage, deploymentId, notify, dismiss, retry, router, deploymentStreamEnabled])

  useEffect(() => {
    const id = `phase1-unavailable:${deploymentId}`
    // DoD D18 (t129 2026-05-16): suppress on chroot. The Sovereign-side
    // catalyst-api runs IN the cluster it's reporting on; the
    // Phase-1-watch-skipped event is a mothership-only concept (mother's
    // observer pod couldn't fetch the new cluster's kubeconfig). The
    // chroot has the in-cluster ServiceAccount + the bundled
    // sovereignDynamicClient and watches HelmReleases directly via the
    // informer cache. Firing the "monitoring unavailable" notification
    // here misinforms the operator and tells them to drop to kubectl
    // when the data is already on the page.
    if (!state.phase1WatchSkipped || DETECTED_MODE.mode === 'sovereign') {
      dismiss(id)
      return
    }
    const target = sovereignFQDN ?? 'the new Sovereign cluster'
    notify({
      id,
      level: 'warn',
      title: 'Per-component install monitoring is unavailable for this deployment',
      body: `The Catalyst API couldn’t fetch the new cluster’s kubeconfig. Use kubectl directly to check Helm releases on ${target}.`,
      raw: state.phase1WatchSkippedReason ?? undefined,
    })
  }, [
    state.phase1WatchSkipped,
    state.phase1WatchSkippedReason,
    sovereignFQDN,
    deploymentId,
    notify,
    dismiss,
  ])

  // Malformed-id banner (issues #749 + #754). When the URL carries a
  // value that isn't a 16-char lowercase hex id, surface it
  // immediately so the operator sees the FULL invalid value rather
  // than a misleading "deployment <truncated> is unknown to the
  // backend" message that hides the truncation. Uses a `notify` (not
  // `throw`) so the rest of the page still renders — preserves the
  // canonical empty-grid + Cancel-and-Wipe affordance.
  //
  // CHROOT EXEMPTION (DoD D17, caught on t129 2026-05-16): on the
  // Sovereign Console the canonical deployment id returned by
  // /api/v1/sovereign/self is `sovereign-<fqdn>` (not a 16-char hex).
  // Likewise the AppsPage on the SovereignConsoleLayout has no
  // `:deploymentId` URL param to inspect — the id is resolved via
  // sovereign-self, NOT the URL. Firing the "malformed" notification
  // on every `/apps` and `/app/<name>` visit on the chroot is a
  // false-positive. Suppress on chroot (DETECTED_MODE.mode==='sovereign')
  // since the chroot id-resolution path is not URL-driven.
  const suppressMalformedBanner = DETECTED_MODE.mode === 'sovereign'
  useEffect(() => {
    const id = `deployment-id-malformed:${rawDeploymentId}`
    if (deploymentIdValid || suppressMalformedBanner) {
      dismiss(id)
      return
    }
    notify({
      id,
      level: 'error',
      title: 'Deployment id in the URL is malformed',
      body:
        `The path segment "${rawDeploymentId}" is not a valid deployment id ` +
        `(expected 16 lowercase hex characters; got ${rawDeploymentId.length}). ` +
        `Re-check the link you opened or return to the wizard.`,
      actions: [
        {
          label: 'Back to wizard',
          variant: 'primary',
          testId: 'sov-malformed-id-back',
          onClick: () => router.navigate({ to: '/wizard' }),
        },
      ],
    })
  }, [deploymentIdValid, suppressMalformedBanner, rawDeploymentId, notify, dismiss, router])

  // Issues #764 + #768 — Sovereign-is-ready handover surface. Once
  // catalyst-api auto-fires the JWT mint (markPhase1Done →
  // fireHandover, gated on Phase-1 OutcomeReady), the SSE stream
  // delivers a typed `handover-ready` event AND the
  // /deployments/{id} record acquires `handoverURL` +
  // `handoverFiredAt`. This effect drives:
  //
  //   1. A persistent global toast "Sovereign is ready —
  //      redirecting…" with an "Open Sovereign console →" action
  //      (founder feedback 2026-05-04: the operator must see the
  //      affordance immediately, not wait silently).
  //   2. A 5-second window before the browser auto-redirects via
  //      window.location.href = handoverURL. The countdown is
  //      managed by the second effect below; this effect just
  //      records the URL on the toast so the operator can click
  //      sooner.
  //   3. A document-title flip to "✓ Sovereign ready — <fqdn>" so
  //      a backgrounded tab surfaces the completion in the OS task
  //      switcher.
  const isHandoverReady = handoverReady !== null && handoverReady.handoverURL !== ''
  const handoverURL = handoverReady?.handoverURL ?? ''
  useEffect(() => {
    const id = `handover-ready:${deploymentId}`
    // Suppress handover toast on chroot — operator is already on the
    // Sovereign Console; "Sovereign is ready" is the mother's wizard
    // UX leaking through via the imported deployment's event history.
    if (!isHandoverReady || isSovereignMode) {
      dismiss(id)
      return
    }
    const target = sovereignFQDN ?? 'your new Sovereign'
    notify({
      id,
      level: 'success',
      title: 'Sovereign is ready — redirecting…',
      body: `${target} provisioned. Opening the Sovereign console in 5 seconds.`,
      actions: [
        {
          label: 'Open your Sovereign console →',
          variant: 'primary',
          testId: 'sov-handover-open-now',
          onClick: () => {
            window.location.href = handoverURL
          },
          dismissOnClick: false,
        },
        {
          label: 'Stay on this page',
          variant: 'ghost',
          testId: 'sov-handover-stay',
          onClick: () => {
            // Marker — see the redirect-timer effect below; the
            // dismiss IS the cancel signal because the timer reads
            // the toast presence.
            dismiss(id)
          },
        },
      ],
    })
  }, [isHandoverReady, handoverURL, sovereignFQDN, deploymentId, notify, dismiss])

  // Auto-redirect timer — 5 seconds from the moment handoverReady
  // arrives. Mother-only: on the chroot the operator is already at
  // the destination; auto-redirecting back to the same URL would loop.
  useEffect(() => {
    if (!isHandoverReady || isSovereignMode) return
    const target = handoverURL
    const timeoutMs = 5000
    const timer = window.setTimeout(() => {
      // Final navigation — this leaves the SPA. The Sovereign-side
      // /auth/handover handler validates the JWT, mints a session,
      // and 302s to /console/dashboard. No SPA-side state survives.
      window.location.href = target
    }, timeoutMs)
    return () => {
      window.clearTimeout(timer)
    }
  }, [isHandoverReady, handoverURL, isSovereignMode])

  // Document-title flip on handover-ready (founder feedback
  // 2026-05-04 — a backgrounded tab must surface the completion in
  // the OS task switcher).
  useEffect(() => {
    if (!isHandoverReady) return
    const previous = document.title
    const target = sovereignFQDN ?? 'Sovereign'
    document.title = `✓ Sovereign ready — ${target}`
    return () => {
      document.title = previous
    }
  }, [isHandoverReady, sovereignFQDN])

  // #3601 (EPIC #3597) — /apps is now TAB-LESS and shows ONLY installed
  // deployments. The catalog grid moved to its own left-nav page
  // (`/catalog` → CatalogPage). `catalogApps` is still the full
  // `resolveApplications` union; here we only use it to derive the
  // installed subset (it remains the source of truth CatalogPage renders).
  const catalogApps = applications
  // Deployments = every catalog entry that has at least one event
  // attributed to it OR was explicitly selected by the operator
  // (bootstrap-kit always counts). Mirrors canonical `installedIds`.
  const deployedIds = useMemo<Set<string>>(() => {
    const out = new Set<string>()
    for (const app of applications) {
      if (app.bootstrapKit) {
        out.add(app.id)
        continue
      }
      const compState = state.apps[app.id]
      if (compState && compState.status !== 'pending') out.add(app.id)
    }
    return out
  }, [applications, state.apps])
  const installedApps = useMemo(
    () =>
      // #4113 — on an Org-scoped session the blueprint/bootstrap-kit cards
      // (cilium, flux, keycloak … the SOVEREIGN spine, derived from the wizard
      // catalog) must NEVER render: a customer sees ONLY their own estate,
      // which arrives exclusively as the `liveInstances` rows projected by
      // /api/v1/org/applications. The bootstrap-kit `out.add(app.id)` above
      // would otherwise leak the sovereign spine onto the Org Apps page.
      orgScoped ? [] : catalogApps.filter((a) => deployedIds.has(a.id)),
    [catalogApps, deployedIds, orgScoped],
  )

  const [query, setQuery] = useState<string>('')

  // #3601 — the grid is the installed-deployments set only; no tab switch.
  const visibleApps = useMemo<ApplicationDescriptor[]>(() => {
    const filtered = query
      ? installedApps.filter((a) => {
          const q = query.toLowerCase()
          return (
            a.title.toLowerCase().includes(q) ||
            (a.description ?? '').toLowerCase().includes(q) ||
            (a.familyName ?? '').toLowerCase().includes(q)
          )
        })
      : installedApps
    return [...filtered].sort((a, b) => a.title.localeCompare(b.title))
  }, [installedApps, query])

  // #4119 — the "Provisioning" pill + "View jobs" link is a sovereign
  // provisioning affordance; never render it for an Org-scoped session.
  const isProvisioning =
    deploymentStreamEnabled && (streamStatus === 'connecting' || streamStatus === 'streaming')

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Applications"
      headerSlotRight={
        isProvisioning ? (
          <div
            className="flex items-center gap-2 rounded-lg border border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 px-2 py-1 text-[11px] text-[var(--color-accent)]"
            data-testid="sov-provisioning-pill"
          >
            <div className="h-3 w-3 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent" />
            Provisioning
            {/* #4704 — watching a live prov is the Dashboard's job (its
                Progress ⇄ Treemap pane), not the finite-jobs table. The
                helper also guards the id-less mothership case (never emit
                a bare path that collapses into /provision/<literal>). */}
            <Link
              to={sovereignPathOrDeployments('dashboard', { deploymentId }) as never}
              className="ml-1 underline text-[var(--color-accent)]"
            >
              View progress
            </Link>
          </div>
        ) : streamStatus === 'completed' ? (
          <div className="flex items-center gap-2">
            {/* #3611 — a ready multi-region Sovereign whose secondary
                region is degraded surfaces an amber badge here instead of
                only the bare "View install history" link, so the operator
                sees the secondary lag at the deployment level. Surface-
                only: the Sovereign IS ready (the primary converged). */}
            {snapshot?.secondaryDegraded ? (
              <span
                data-testid="sov-secondary-degraded-pill"
                title="Secondary region is significantly behind the primary in HelmRelease convergence"
                className="rounded-lg border border-[#F59E0B]/40 bg-[#F59E0B]/10 px-2 py-1 text-[11px] font-semibold text-[#F59E0B]"
              >
                Secondary degraded
              </span>
            ) : null}
            <Link
              to={sovereignPathOrDeployments('jobs', { deploymentId }) as never}
              className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
            >
              View install history
            </Link>
          </div>
        ) : null
      }
    >
      <style>{APPS_PAGE_CSS}</style>

      {/*
       * Failure + phase-1-unavailable banners used to render here above
       * the apps grid. Per founder #475 they now fire as global toasts
       * via the NotificationProvider mounted in RootLayout, leaving the
       * Apps page to render only the grid + tabs + search box.
       *
       * The Cancel-&-Wipe action on the failure toast still needs a
       * page-scoped modal mount, so WipeDeploymentModal lives below.
       */}

      {showWipeModal ? (
        <WipeDeploymentModal
          open={showWipeModal}
          deploymentId={deploymentId}
          sovereignFQDN={sovereignFQDN}
          onClose={() => setShowWipeModal(false)}
          onWiped={onWipeComplete}
        />
      ) : null}

      {/*
       * Issues #764 + #768 — Sovereign-is-ready handover banner. Always
       * rendered the moment catalyst-api auto-fires the JWT mint
       * (handoverReady != null), in addition to the global toast. The
       * banner is a redundant affordance per the founder's
       * 2026-05-04 feedback ("the operator must SEE the redirection
       * link"): a wide green panel directly above the apps grid that
       * cannot be missed even if the toast tray is collapsed.
       *
       * The button is a plain anchor (not a SPA Link) — clicking it
       * fires a real browser navigation to the cross-domain Sovereign
       * console, which is exactly what the auto-redirect timer does
       * after 5 seconds.
       */}
      {/* Handover banner is the mother's wizard UX — "you can jump to
       * your new Sovereign now." On the chroot the operator IS already
       * on the Sovereign Console; there's nowhere to "redirect" to,
       * and the banner is content from a foreign context that bleeds
       * through because the imported deployment record carries the
       * mother's handover-ready event. Suppress on chroot.
       */}
      {isHandoverReady && !isSovereignMode ? (
        <div className="handover-ready-banner" data-testid="sov-handover-ready-banner">
          <div className="handover-ready-body">
            <span className="handover-ready-title">
              ✓ Sovereign is ready{sovereignFQDN ? ` — ${sovereignFQDN}` : ''}
            </span>
            <span className="handover-ready-sub" data-testid="sov-handover-ready-sub">
              Redirecting to your Sovereign console in a moment. You can open it now:
            </span>
          </div>
          <a
            className="handover-ready-cta"
            href={handoverURL}
            data-testid="sov-handover-ready-cta"
          >
            Open your Sovereign console →
          </a>
        </div>
      ) : null}

      {/* #3601 — Deployments/Catalog tabs removed: /apps is the installed
          grid only; the catalog lives at /catalog (left-nav CatalogPage). */}

      {/* Search + Environment filter */}
      <div className="apps-toolbar">
        <div className="search-wrap">
          <svg className="search-icon" viewBox="0 0 24 24" width={16} height={16} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
            <circle cx={11} cy={11} r={8} />
            <path d="m21 21-4.3-4.3" />
          </svg>
          <input
            type="text"
            placeholder={`Search your ${installedApps.length} apps…`}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="search-input"
            data-testid="sov-search"
          />
        </div>
        {/*
         * qa-loop iter-1 prefetch Fix #92 (TC-090) — environment filter
         * row. The matrix asserts the Apps page exposes the canonical
         * environment vocabulary (`dev`, `staging`, `prod`) so the
         * operator can filter the list. URL state preservation is
         * deferred to a follow-up slice; for now the filter is local
         * to the page and `dev` is the default selection (the canonical
         * single-environment EPIC-2 chroot bucket).
         */}
        <div className="env-filter" data-testid="sov-env-filter">
          <span className="env-filter-label">Environment:</span>
          <span className="env-filter-chip" data-environment="dev">dev</span>
          <span className="env-filter-chip" data-environment="staging">staging</span>
          <span className="env-filter-chip" data-environment="prod">prod</span>
        </div>
      </div>

      {installedApps.length === 0 && liveInstances.length === 0 ? (
        <div className="empty-state" data-testid="sov-empty-deployments">
          <p className="empty-title">No applications installed yet.</p>
          <p className="empty-sub">Provisioning has not produced any deployments — open the catalog to see what will install.</p>
          <Link to={'/catalog' as never} className="btn btn-primary" data-testid="sov-empty-open-catalog">
            Open catalog →
          </Link>
        </div>
      ) : (
        <div className="apps-grid" data-testid="sov-apps-grid">
          {/*
            #3370 — instance cards: one card per RUNNING INSTANCE
            (Application CR). `shared-pg · PostgreSQL · singleton ·
            ● Ready · ⛓ 3 contexts`. The ⛓ badge deep-links to the
            instance's Contexts tab; the breakup never renders on
            tiles. #3601 — /apps is installed-only, so these always
            render (no tab gate).
          */}
          {liveInstances
            .filter((inst) =>
              query
                ? inst.id.toLowerCase().includes(query.toLowerCase()) ||
                  inst.slug.toLowerCase().includes(query.toLowerCase())
                : true,
            )
            .map((inst) => {
              const comp = findComponent(inst.slug)
              const descriptor: ApplicationDescriptor = {
                id: inst.id,
                bareId: inst.slug,
                title: inst.id,
                description: `${comp?.name ?? inst.slug} instance`,
                familyId: comp?.product ?? 'platform',
                familyName: comp?.name ?? inst.slug,
                tier: comp?.tier ?? 'mandatory',
                logoUrl: comp?.logoUrl ?? null,
                dependencies: [],
                bootstrapKit: inst.bootstrapKit,
              }
              return (
                <AppCard
                  key={`instance-${inst.id}`}
                  app={descriptor}
                  isCatalog={false}
                  environment={inst.environment}
                  externalURL={inst.externalURL}
                  // #3656 — declared user-UI signal keyed on the instance's
                  // Blueprint id; urlPending tracks the live-apps query so a
                  // declared-UI instance shows a disabled "Open…" placeholder
                  // until its externalURL lands (no late pop-in).
                  hasUserUI={BLUEPRINT_BY_ID[inst.blueprint]?.hasUserUIEndpoint === true}
                  urlPending={liveAppsQuery.isPending}
                  status={inst.status}
                  isService={false}
                  marketplacePublished={null}
                  slug={inst.slug}
                  topology={inst.topology}
                  contextCount={inst.contextCount}
                />
              )
            })}
          {visibleApps
            .filter(
              (app) =>
                // #3370 — a blueprint whose instances render their own
                // cards must not ALSO render a blueprint-level card on
                // the Deployments grid (N instances ⇒ exactly N cards).
                !liveInstances.some((inst) => inst.blueprint === app.id),
            )
            .map((app) => {
            const slug = app.id.replace(/^bp-/, '')
            const published = Object.prototype.hasOwnProperty.call(publishedBySlug, slug)
              ? publishedBySlug[slug]
              : null
            const environment = environmentById[app.id] ?? UNRESOLVED_APP_ENVIRONMENT
            const externalURL = externalURLById[app.id] ?? ''
            return (
              <AppCard
                key={app.id}
                app={app}
                isCatalog={false}
                environment={environment}
                externalURL={externalURL}
                // #3656 — declared user-UI signal from the generated catalog
                // (projected from the Blueprint's endpoints[]); urlPending
                // tracks the live-apps query so a declared-UI app shows a
                // disabled "Open…" placeholder until its URL resolves.
                hasUserUI={BLUEPRINT_BY_ID[app.id]?.hasUserUIEndpoint === true}
                urlPending={liveAppsQuery.isPending}
                status={(() => {
                  // Live API status wins when present.
                  const live = liveAppStatus[app.id]
                  if (live) return live
                  // SSE reducer state — but ignore the default
                  // "pending" init which fires for every app at
                  // boot regardless of installation status. Only
                  // honour explicit non-pending transitions
                  // (installing / installed / failed / degraded).
                  const reducerStatus = state.apps[app.id]?.status
                  if (reducerStatus && reducerStatus !== 'pending') {
                    return reducerStatus
                  }
                  // On Sovereign mode (chroot post-cutover) the
                  // live query has loaded — if app.id is missing
                  // from its keys, the wizard catalog references a
                  // Blueprint the BE catalog doesn't ship (data
                  // drift between componentGroups.ts and
                  // blueprints.json). Render 'available' with the
                  // AVAILABLE pill rather than the misleading
                  // PENDING pill that suggests install in progress.
                  if (isSovereignMode && liveAppsQuery.isSuccess) {
                    return 'available'
                  }
                  return reducerStatus ?? 'pending'
                })()}
                isService={app.familyId === 'platform' && !app.bootstrapKit ? false : !app.bootstrapKit && app.tier === 'optional' ? false : false}
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
                  if (!r.ok) {
                    throw new Error(`publish toggle failed: ${r.status}`)
                  }
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
 * launchAppViaSSO — #3374. The ONE silent-SSO launch path for the
 * per-card Open button. Mirrors AppDetail's launchAppViaSSO: resolve a
 * one-shot launch-url via GET /catalyst/v1/apps/{key}/launch-url (the BE
 * appends `prompt=none&kc_idp_hint=catalyst-pin`) and open it in a new
 * tab. Falls back to the raw `externalURL` ONLY when no launch key is
 * resolvable or the endpoint errors — so the DEFAULT is zero-click SSO,
 * never the app's own login form.
 *
 * The launch key is the Application CR uid when known, else the blueprint
 * /release name (bootstrap-kit HelmRelease apps — grafana, harbor,
 * openbao, gitea, keycloak, guacamole — carry no Application CR; the BE's
 * launch-url resolver strips the `bp-` prefix and matches the HR). This
 * is the same resolution AppDetail.launchKey performs.
 */
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

interface AppCardProps {
  app: ApplicationDescriptor
  status: ApplicationStatus
  /**
   * #3090 — which tab this card is rendered under. On the Catalog tab a
   * card is a Blueprint CLASS and must link to the class page
   * `/catalog/$blueprint` (instances list + "New instance"). On the
   * Deployments tab a card is a concrete INSTANCE and must link to the
   * instance page `/app/$id` (Open button, no New-instance). Before this
   * split BOTH tabs hard-linked to `/app/$id`, so the class page was
   * unreachable and the instance page wrongly showed class content.
   */
  isCatalog: boolean
  /**
   * Mirror of canonical `is-service`. The wizard catalog doesn't carry
   * an explicit service flag yet — keep the prop so adding one later
   * is a one-line change. For now, every card is treated as an
   * Application surface.
   */
  isService: boolean
  /**
   * Environment label rendered as a chip beside the FREE/BOOTSTRAP
   * chips. Sourced from the BE's per-Application `environmentRef`,
   * with "dev" as the always-on fallback so single-environment
   * Sovereigns still display a usable chip. Closes qa-loop iter-7
   * TC-090.
   */
  environment: string
  /**
   * Marketplace publish state for this app's slug. `null` ⇒ this app
   * isn't in the Organization marketplace catalog (bootstrap component, or
   * marketplace not deployed on this Sovereign) → don't render the
   * Publish chip. true/false ⇒ render the toggle.
   */
  marketplacePublished?: boolean | null
  slug?: string
  onPublishedChange?: (next: boolean) => Promise<void>
  /**
   * G90 (2026-06-01) — operator-visible front-door URL for this
   * Application (e.g. https://gitea.hw86.omani.works). Empty when the
   * app has no externally-exposed HTTPRoute (controllers, operators).
   * Renders as a prominent "Open" launch button inside the
   * `.status-corner` next to the INSTALLED badge; clicking opens a new
   * tab to the URL, which Keycloak SSO-authenticates the operator into
   * the target app (every bp-* chart with a UI surface is already
   * registered as an OIDC client on the Sovereign).
   */
  externalURL?: string
  /**
   * #3656 (founder #6) — whether this app's Blueprint declares a
   * user-facing UI front door (projected `hasUserUIEndpoint`). When true
   * and `externalURL` is still resolving (`urlPending`), the card shows a
   * disabled "Open…" placeholder instead of nothing — so the enabled Open
   * settles in place rather than popping in late. False/undefined ⇒
   * headless app ⇒ no placeholder (and no transient chip). Default
   * undefined keeps the legacy behaviour for callers that don't pass it.
   */
  hasUserUI?: boolean
  /**
   * #3656 — true while the live-apps query (which carries `externalURL`)
   * has not resolved yet. Drives the disabled "Open…" placeholder window
   * for a declared-UI app. Ignored entirely once `externalURL` is known.
   */
  urlPending?: boolean
  /**
   * #3370 — instance-card extras. `topology` renders the placement chip
   * (`singleton`, `single-region`, `active-hotstandby`); `contextCount`
   * renders the ⛓ badge that deep-links to the instance's Contexts tab.
   * Both undefined on blueprint/catalog cards.
   */
  topology?: string
  contextCount?: number
  /**
   * #3603 (EPIC #3597) — admin-editable catalog icons. When set, the card
   * renders the theme-correct override (iconLight in the light theme,
   * iconDark in the dark theme) in place of the build-time logo, falling
   * back: theme icon → the other theme's icon → app.logoUrl → initial.
   * Empty on un-edited entries (the card keeps its build-time logo).
   */
  iconLight?: string
  iconDark?: string
  /**
   * #3603 — admin Edit affordance. When provided, the card renders an
   * "Edit" button in the status-corner (the Catalog page passes this only
   * for sovereign-admins). Clicking opens the edit form; the button stops
   * the card-link navigation. Absent ⇒ no Edit button (non-admins, the
   * Deployments grid).
   */
  onEdit?: () => void
}

// Exported for the #3374 render test (AppsPage.open-button.test.tsx) — the
// per-card Open button gate + silent-SSO click routing are leaf behaviour
// best asserted directly on the card, without the live-apps query plumbing.
export function AppCard({ app, status, isCatalog, isService, environment, marketplacePublished, slug, onPublishedChange, topology, contextCount, externalURL, hasUserUI, urlPending, iconLight, iconDark, onEdit }: AppCardProps) {
  const stateClass = `state-${status}`
  const navigate = useNavigate()
  // #3603 — render the theme-correct admin icon override when present:
  // light icon in the light theme, dark icon in the dark theme. Fall back
  // to the other theme's icon, then the build-time logo. Empty on
  // un-edited entries → keeps app.logoUrl.
  // #3603 / #3668 §5B — render the theme-correct IaC icon override when
  // present (light icon in the light theme, dark icon in the dark theme),
  // then the build-time logo, then the letter-mark. Resolved through the
  // SAME shared resolveCatalogIcon path the catalog detail page uses, so the
  // grid card and the hero never diverge (one mechanism, no per-blueprint
  // branch — DoD §9.9). The iconLight/iconDark props carry the IaC edit
  // (mirrored from Gitea via the read overlay).
  const { theme } = useTheme()
  const resolvedIcon =
    resolveCatalogIcon({ iconLight, iconDark }, theme, app.logoUrl ?? null) ?? ''
  // #3374 — the per-card Open button resolves a silent-SSO launch via the
  // app's CR uid when known, else the bootstrap-kit blueprint/release name
  // (the BE launch-url resolver strips `bp-` and matches the HR). Mirrors
  // AppDetail.launchKey. Empty externalURL ⇒ no front door ⇒ no button.
  const launchKey = app.id.replace(/^bp-/, '')
  const [launching, setLaunching] = useState(false)
  // #3370 — class-level shareable badge on Catalog-tab cards, from the
  // build-time blueprint.yaml declaration.
  const shareable = isCatalog && BLUEPRINT_BY_ID[app.id]?.shareable === true
  // Chroot-aware target: on the mother monitor surface
  // (console.openova.io/sovereign/provision/<id>/...) every link MUST stay
  // scoped under /provision/<id>/... — escaping to the clean root /app/<id>
  // produces the broken /sovereign/app/<id> path the founder hit on
  // otech122. On the Sovereign's adult hostname the deploymentId is
  // implicit so /app/<id> is correct.
  //
  // #3090 — per-tab segment: a Catalog card opens the CLASS page
  // (`/catalog/$blueprint`), a Deployments card opens the INSTANCE page
  // (`/app/$id`). Same chroot/provision chroot-scoping logic, only the
  // path segment differs.
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const depId = params.deploymentId ?? ''
  const seg = isCatalog ? 'catalog' : 'app'
  const target =
    DETECTED_MODE.mode === 'sovereign' || !depId
      ? `/${seg}/${app.id}`
      : `/provision/${depId}/${seg}/${app.id}`
  return (
    <Link
      to={target as never}
      className={`app-card ${stateClass}${isService ? ' is-service' : ''}`}
      data-testid={`sov-app-card-${app.id}`}
      data-status={status}
    >
      {resolvedIcon ? (
        <img
          src={resolvedIcon}
          alt={app.title}
          className="app-logo"
          loading="lazy"
          data-testid={`sov-app-icon-${app.id}`}
          data-icon-theme={theme}
        />
      ) : (
        <span className="app-icon" style={{ background: '#1f2937' }}>
          {app.title[0] ?? '?'}
        </span>
      )}
      <div className="app-body">
        <div className="app-top">
          <span className="app-name">{app.title}</span>
          <span className="app-cat">{app.familyName}</span>
        </div>
        <p className="app-desc">{app.description || app.familyName}</p>
        <div className="app-chips">
          <span
            className="chip chip-env"
            title={`Environment: ${environment}`}
            data-testid={`sov-app-env-${app.id}`}
            data-environment={environment}
          >
            {environment}
          </span>
          <span className="chip chip-free">FREE</span>
          {app.bootstrapKit ? (
            <span className="chip chip-dep" title="Bootstrap-kit component (always installed)">
              BOOTSTRAP
            </span>
          ) : null}
          {topology ? (
            <span
              className="chip chip-dep"
              title={`Topology: ${topology}`}
              data-testid={`sov-app-topology-${app.id}`}
            >
              {topology}
            </span>
          ) : null}
          {typeof contextCount === 'number' && contextCount > 0 ? (
            <button
              type="button"
              className="chip chip-env"
              data-testid={`sov-app-contexts-${app.id}`}
              title={`${contextCount} Contexts on this instance — open the Contexts tab`}
              onClick={(e) => {
                // The whole card is a Link; the ⛓ badge deep-links to
                // the Contexts tab instead.
                e.preventDefault()
                e.stopPropagation()
                void navigate({ to: `/app/${app.id}/contexts` as never })
              }}
              style={{ cursor: 'pointer', border: 0 }}
            >
              ⛓ {contextCount} context{contextCount === 1 ? '' : 's'}
            </button>
          ) : null}
          {shareable ? (
            <span
              className="chip chip-env"
              data-testid={`sov-catalog-shareable-${app.id}`}
              title="Instances support multi-application reuse via Contexts"
            >
              ⛓ shareable
            </span>
          ) : null}
          {app.dependencies.slice(0, 3).map((d) => (
            <span key={d} className="chip chip-dep" title="Bundled dependency">
              + {d}
            </span>
          ))}
        </div>
      </div>

      <div className="status-corner">
        {/* #3603 (EPIC #3597) — admin Edit affordance. Only rendered when
         * onEdit is provided (the Catalog page passes it for
         * sovereign-admins). The whole card is a Link, so the button stops
         * the navigation and opens the edit form instead. Non-admins and
         * the Deployments grid pass no onEdit ⇒ no button. */}
        {onEdit ? (
          <button
            type="button"
            className="edit-chip"
            data-testid={`sov-app-edit-${app.id}`}
            title={`Edit ${app.title} catalog entry`}
            aria-label={`Edit ${app.title} catalog entry`}
            onClick={(e) => {
              e.preventDefault()
              e.stopPropagation()
              onEdit()
            }}
          >
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
            >
              <path d="M12 20h9" />
              <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
            </svg>
            Edit
          </button>
        ) : null}
        {/* #3374 — per-card "Open" button RESTORED (founder flagged its
         * disappearance as "very important"). The #2743 "V4 verdict"
         * deleted it because it opened the RAW externalURL with no
         * prompt=none&kc_idp_hint=catalyst-pin, bypassing silent-SSO. The
         * fix keeps the one-click affordance but routes it through
         * launchAppViaSSO → GET /catalyst/v1/apps/{key}/launch-url, which
         * returns the signed silent-SSO URL — so the operator lands in the
         * app already signed in, never on a login form.
         *
         * #3656 (founder #6) — faithful Open chip: never assert-then-retract.
         * The live externalURL arrives async (liveAppsQuery), so a real
         * front-door app used to render NOTHING then POP the chip in late.
         * The card now reads the STATIC declared `hasUserUI` signal
         * (BLUEPRINT_BY_ID[...].hasUserUIEndpoint, projected from the
         * Blueprint's endpoints[] — the SAME source the BE gates externalURL
         * on) so it can show a confident disabled "Open…" placeholder for a
         * declared-UI app WHILE its URL is still resolving. Three faithful
         * states, no flash / pop-in / retract:
         *   • externalURL resolved → enabled Open (silent-SSO launch).
         *   • declared UI + URL still pending → disabled "Open…" placeholder.
         *   • headless (no declared UI) → NOTHING (no transient chip). */}
        {externalURL ? (
          <button
            type="button"
            className="open-chip"
            data-testid={`sov-app-open-${app.id}`}
            data-external-url={externalURL}
            disabled={launching}
            title={`Open ${app.title} — single sign-in, no second login`}
            aria-label={`Open ${app.title} — single-click silent sign-in, opens in a new tab`}
            onClick={(e) => {
              // The whole card is a Link — keep the click on the button.
              e.preventDefault()
              e.stopPropagation()
              if (launching) return
              setLaunching(true)
              void launchAppViaSSO(launchKey, externalURL).finally(() => setLaunching(false))
            }}
          >
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
            >
              <path d="M14 3h7v7" />
              <path d="M10 14L21 3" />
              <path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5" />
            </svg>
            {launching ? 'Opening…' : 'Open'}
          </button>
        ) : hasUserUI && urlPending ? (
          // Declared-UI app, URL not resolved yet → confident disabled
          // placeholder. NOT clickable (no URL to launch). Replaced in
          // place by the enabled Open the moment liveAppsQuery resolves —
          // same slot, so the operator sees a settle, never a pop-in.
          <button
            type="button"
            className="open-chip is-pending"
            data-testid={`sov-app-open-pending-${app.id}`}
            disabled
            aria-disabled="true"
            title={`Open ${app.title} — resolving the front-door URL…`}
            aria-label={`Open ${app.title} — resolving the front-door URL`}
            onClick={(e) => {
              e.preventDefault()
              e.stopPropagation()
            }}
          >
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
            >
              <path d="M14 3h7v7" />
              <path d="M10 14L21 3" />
              <path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5" />
            </svg>
            Open…
          </button>
        ) : null}
        {marketplacePublished !== null && marketplacePublished !== undefined && slug ? (
          <button
            type="button"
            className={`publish-chip ${marketplacePublished ? 'is-published' : 'is-unpublished'}`}
            data-testid={`sov-app-publish-${slug}`}
            data-published={marketplacePublished}
            title={
              marketplacePublished
                ? 'Click to unpublish from marketplace'
                : 'Click to publish to marketplace'
            }
            onClick={(e) => {
              e.preventDefault()
              e.stopPropagation()
              if (onPublishedChange) {
                void onPublishedChange(!marketplacePublished)
              }
            }}
          >
            <span className="dot" />
            {marketplacePublished ? 'PUBLISHED' : 'UNPUBLISHED'}
          </button>
        ) : null}
        {status === 'installed' ? (
          <span className="status-chip s-installed">
            <span className="dot" /> INSTALLED
          </span>
        ) : status === 'installing' ? (
          <span className="status-chip s-installing">
            <span className="dot dot-spin" /> INSTALLING
          </span>
        ) : status === 'failed' ? (
          <span className="status-chip s-failed">
            <span className="dot" /> FAILED
          </span>
        ) : status === 'degraded' ? (
          <span className="status-chip s-failed">
            <span className="dot" /> DEGRADED
          </span>
        ) : status === 'available' ? (
          <span className="status-chip s-available">
            <span className="dot" /> AVAILABLE
          </span>
        ) : (
          <span className="status-chip s-pending">
            <span className="dot" /> PENDING
          </span>
        )}
      </div>
    </Link>
  )
}

/**
 * Pixel-ported `<style>` block from the canonical AppsPage.svelte.
 * Same selector tree, same values — the only Tailwind-vs-CSS diff is
 * that React injects via <style> rather than a Svelte scoped block.
 */
const APPS_PAGE_CSS = `
/*
 * Issues #764 + #768 — handover-ready banner. Spans the full width of
 * the page surface, sits above the tabs, and uses the success-token
 * gradient so the affordance is impossible to miss. The CTA is a real
 * <a> link styled as a primary button.
 */
.handover-ready-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.85rem 1rem;
  margin: 0.75rem 0 1rem;
  border: 1.5px solid color-mix(in srgb, var(--color-success) 55%, var(--color-border));
  border-radius: 12px;
  background: color-mix(in srgb, var(--color-success) 12%, var(--color-surface));
}
.handover-ready-body {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
  flex: 1 1 auto;
  min-width: 0;
}
.handover-ready-title {
  color: var(--color-success);
  font-size: 0.95rem;
  font-weight: 700;
  letter-spacing: 0.01em;
}
.handover-ready-sub {
  color: var(--color-text);
  font-size: 0.82rem;
  line-height: 1.45;
}
.handover-ready-cta {
  flex: 0 0 auto;
  background: var(--color-success);
  color: #fff;
  padding: 0.55rem 1.1rem;
  border-radius: 8px;
  font-size: 0.88rem;
  font-weight: 600;
  text-decoration: none;
  white-space: nowrap;
  border: 1px solid transparent;
  transition: filter 0.15s, box-shadow 0.15s;
}
.handover-ready-cta:hover {
  filter: brightness(0.92);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.18);
}
.handover-ready-cta:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

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
/*
 * .chip-env — per-app environment chip ("dev", "prod", etc.).
 * Distinct hue from FREE/BOOTSTRAP so the operator can scan environment
 * affinity at a glance. Placed first in .app-chips so it always renders
 * even when the BE response is delayed (FE falls back to "dev"). Closes
 * qa-loop iter-7 TC-090.
 */
.chip-env {
  background: color-mix(in srgb, var(--color-text-strong) 12%, transparent);
  color: var(--color-text-strong);
  font-weight: 600;
  text-transform: lowercase;
  letter-spacing: 0.02em;
}

.status-corner { position: absolute; bottom: 0.5rem; right: 0.55rem; display: flex; gap: 0.4rem; align-items: center; }
/*
 * #3374 — per-card "Open" launch button. Accent-filled so it reads as the
 * primary call-to-action on the card (the founder flagged the prior
 * removal as losing the one-click launch affordance). Sits leftmost in the
 * status-corner, ahead of the publish + status chips. pointer-events:auto
 * so it stays clickable even though the whole card is an <a> Link.
 */
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
/* #3656 (founder #6) — the "Open…" placeholder shown for a declared-UI app
 * while its live front-door URL is still resolving. Rendered MUTED + outlined
 * (not the solid accent of the live Open) so it reads as "loading, not yet
 * actionable" rather than a real launch control. Default cursor + no hover
 * lift make clear it isn't clickable; it is replaced in place by the enabled
 * Open the moment the URL lands (no flash / pop-in). */
.open-chip.is-pending {
  background: transparent;
  color: var(--color-text-dim);
  border: 1px dashed var(--color-border);
  box-shadow: none;
  cursor: default;
  opacity: 0.85;
}
.open-chip.is-pending:hover { filter: none; transform: none; }
.open-chip svg { display: block; }
/* #3603 — admin "Edit" pill (Catalog page, sovereign-admin only). Muted
 * surface tone so it reads as a secondary affordance next to the accent
 * Open button. pointer-events:auto so it stays clickable inside the card
 * <a> Link. */
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

.btn {
  padding: 0.5rem 1rem; border-radius: 8px; border: none;
  font: inherit; font-size: 0.85rem; font-weight: 600; cursor: pointer;
  text-decoration: none;
}
.btn-primary { background: var(--color-accent); color: #fff; }
.btn-primary:hover { filter: brightness(0.9); }
`
