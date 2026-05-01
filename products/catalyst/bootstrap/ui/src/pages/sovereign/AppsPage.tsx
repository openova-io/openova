/**
 * AppsPage — pixel-port of core/console/src/components/AppsPage.svelte.
 *
 * Layout (top-down, byte-identical to canonical class names):
 *   • Header row: <h1>Applications</h1> + tagline + (provisioning pill
 *     OR install-history link) right-aligned.
 *   • Tabs: "Deployments" | "Catalog" — same `.tabs / .tab / .tab-count`
 *     CSS the canonical surface uses, with the `.active` state on the
 *     selected tab. Counts read from `installedApps.length` /
 *     `catalogApps.length`.
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
import { useParams, useRouter, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { resolveApplications, type ApplicationDescriptor } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import type { ApplicationStatus } from './eventReducer'
import { WipeDeploymentModal } from '@/components/CrudModals/WipeDeploymentModal'
import { useNotifications } from '@/shared/ui/notifications'

interface AppsPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

type TabId = 'installed' | 'catalog'

export function AppsPage({ disableStream = false }: AppsPageProps = {}) {
  const params = useParams({ from: '/provision/$deploymentId' as never }) as {
    deploymentId: string
  }
  const deploymentId = params.deploymentId
  const router = useRouter()
  const store = useWizardStore()

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot, streamStatus, streamError, retry } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })

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
    if (!isFailed) {
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
  }, [isFailed, streamStatus, failureMessage, deploymentId, notify, dismiss, retry, router])

  useEffect(() => {
    const id = `phase1-unavailable:${deploymentId}`
    if (!state.phase1WatchSkipped) {
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

  // Catalog = every Application this deployment knows about (canonical
  // calls this "every app in the org's catalog"; for the wizard surface
  // it's the union of bootstrap-kit + transitive deps + selected). Same
  // descriptor shape, so the card markup is shared between tabs.
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
    () => catalogApps.filter((a) => deployedIds.has(a.id)),
    [catalogApps, deployedIds],
  )

  const [tab, setTab] = useState<TabId>('installed')
  const [query, setQuery] = useState<string>('')

  const visibleApps = useMemo<ApplicationDescriptor[]>(() => {
    const list = tab === 'installed' ? installedApps : catalogApps
    const filtered = query
      ? list.filter((a) => {
          const q = query.toLowerCase()
          return (
            a.title.toLowerCase().includes(q) ||
            (a.description ?? '').toLowerCase().includes(q) ||
            (a.familyName ?? '').toLowerCase().includes(q)
          )
        })
      : list
    return [...filtered].sort((a, b) => {
      if (tab === 'catalog') {
        const aIn = deployedIds.has(a.id) ? 0 : 1
        const bIn = deployedIds.has(b.id) ? 0 : 1
        if (aIn !== bIn) return aIn - bIn
      }
      return a.title.localeCompare(b.title)
    })
  }, [tab, installedApps, catalogApps, query, deployedIds])

  const isProvisioning = streamStatus === 'connecting' || streamStatus === 'streaming'

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
            <Link
              to="/provision/$deploymentId/jobs"
              params={{ deploymentId }}
              className="ml-1 underline text-[var(--color-accent)]"
            >
              View jobs
            </Link>
          </div>
        ) : streamStatus === 'completed' ? (
          <Link
            to="/provision/$deploymentId/jobs"
            params={{ deploymentId }}
            className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          >
            View install history
          </Link>
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

      {/* Tabs */}
      <div className="tabs" role="tablist" data-testid="sov-tabs">
        <button
          type="button"
          className={`tab${tab === 'installed' ? ' active' : ''}`}
          onClick={() => setTab('installed')}
          role="tab"
          aria-selected={tab === 'installed'}
          data-testid="sov-tab-installed"
        >
          Deployments <span className="tab-count">{installedApps.length}</span>
        </button>
        <button
          type="button"
          className={`tab${tab === 'catalog' ? ' active' : ''}`}
          onClick={() => setTab('catalog')}
          role="tab"
          aria-selected={tab === 'catalog'}
          data-testid="sov-tab-catalog"
        >
          Catalog <span className="tab-count">{catalogApps.length}</span>
        </button>
      </div>

      {/* Search */}
      <div className="apps-toolbar">
        <div className="search-wrap">
          <svg className="search-icon" viewBox="0 0 24 24" width={16} height={16} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
            <circle cx={11} cy={11} r={8} />
            <path d="m21 21-4.3-4.3" />
          </svg>
          <input
            type="text"
            placeholder={
              tab === 'installed'
                ? `Search your ${installedApps.length} apps…`
                : `Search ${catalogApps.length} apps…`
            }
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="search-input"
            data-testid="sov-search"
          />
        </div>
      </div>

      {tab === 'installed' && installedApps.length === 0 ? (
        <div className="empty-state" data-testid="sov-empty-deployments">
          <p className="empty-title">No applications installed yet.</p>
          <p className="empty-sub">Provisioning has not produced any deployments — open the catalog to see what will install.</p>
          <button type="button" className="btn btn-primary" onClick={() => setTab('catalog')}>
            Open catalog →
          </button>
        </div>
      ) : (
        <div className="apps-grid" data-testid="sov-apps-grid">
          {visibleApps.map((app) => (
            <AppCard
              key={app.id}
              app={app}
              status={state.apps[app.id]?.status ?? 'pending'}
              deploymentId={deploymentId}
              isService={app.familyId === 'platform' && !app.bootstrapKit ? false : !app.bootstrapKit && app.tier === 'optional' ? false : false}
            />
          ))}
        </div>
      )}
    </PortalShell>
  )
}

interface AppCardProps {
  app: ApplicationDescriptor
  status: ApplicationStatus
  deploymentId: string
  /**
   * Mirror of canonical `is-service`. The wizard catalog doesn't carry
   * an explicit service flag yet — keep the prop so adding one later
   * is a one-line change. For now, every card is treated as an
   * Application surface.
   */
  isService: boolean
}

function AppCard({ app, status, deploymentId, isService }: AppCardProps) {
  const stateClass = `state-${status}`
  return (
    <Link
      to="/provision/$deploymentId/app/$componentId"
      params={{ deploymentId, componentId: app.id }}
      className={`app-card ${stateClass}${isService ? ' is-service' : ''}`}
      data-testid={`sov-app-card-${app.id}`}
      data-status={status}
    >
      {app.logoUrl ? (
        <img src={app.logoUrl} alt={app.title} className="app-logo" loading="lazy" />
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
          <span className="chip chip-free">FREE</span>
          {app.bootstrapKit ? (
            <span className="chip chip-dep" title="Bootstrap-kit component (always installed)">
              BOOTSTRAP
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

.tabs {
  display: flex;
  gap: 0.25rem;
  margin: 1rem 0 0.5rem;
  border-bottom: 1px solid var(--color-border);
}
.tab {
  background: transparent;
  border: none;
  padding: 0.6rem 0.9rem;
  color: var(--color-text-dim);
  font: inherit;
  font-size: 0.88rem;
  font-weight: 500;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
}
.tab:hover { color: var(--color-text); }
.tab.active {
  color: var(--color-text-strong);
  border-bottom-color: var(--color-accent);
  font-weight: 600;
}
.tab-count {
  font-size: 0.7rem;
  padding: 0.08rem 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
  font-weight: 600;
}
.tab.active .tab-count {
  background: color-mix(in srgb, var(--color-accent) 18%, transparent);
  color: var(--color-accent);
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

.status-corner { position: absolute; bottom: 0.5rem; right: 0.55rem; pointer-events: none; }
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
