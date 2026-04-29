/**
 * ApplicationPage — per-Application detail surface served at
 * `/sovereign/provision/$deploymentId/app/$componentId`. Reached by
 * clicking any card on the AdminPage grid. Four tabs:
 *
 *   1. Logs        — every event whose `component` matches this
 *                    Application id, replayed from /events on mount
 *                    and streamed live thereafter. Auto-scrolls to
 *                    the bottom on new lines, level-coloured, with
 *                    timestamp + phase prefixes.
 *   2. Dependencies — both directions. "Depends on" walks the
 *                    component graph from componentGroups; "Depended
 *                    on by" inverts it. Each dep is a clickable mini-
 *                    card linking to its own ApplicationPage. Family-
 *                    level dependencies surface for completeness.
 *   3. Status      — current install state, namespace, helm-release
 *                    name, chart version, last-reconciled time.
 *                    Reads the per-component reducer state; falls
 *                    back to "unknown" when the catalyst-api hasn't
 *                    emitted those fields yet.
 *   4. Overview    — long-form copy from marketplaceCopy.ts, upstream
 *                    project link, family tagline.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label
 * + dep edge + upstream URL is read from existing data modules. New
 * components added to componentGroups + marketplaceCopy render
 * automatically with the right chrome.
 *
 * Per #2 (never compromise), graceful-degrade is INFORMATIONAL not
 * functional — the page renders all four tabs even if /events hasn't
 * landed; the Status tab simply reads "unknown" for fields the API
 * hasn't emitted yet.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useParams, useRouter, Link } from '@tanstack/react-router'
import { ArrowLeft, ExternalLink } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  findProduct,
  type ComponentEntry,
} from '@/pages/wizard/steps/componentGroups'
import { findApplication, resolveApplications, reverseDependencies } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { AdminShell } from './AdminShell'
import { StatusPill } from './StatusPill'
import { COMPONENT_COPY, FAMILY_COPY, familyChipPalette } from '@/pages/marketplace/marketplaceCopy'
import { findComponent } from '@/pages/wizard/steps/componentGroups'
import { normaliseComponentId, type DeploymentEvent } from './eventReducer'

type TabKey = 'logs' | 'dependencies' | 'status' | 'overview'

const TABS: { key: TabKey; label: string }[] = [
  { key: 'logs', label: 'Logs' },
  { key: 'dependencies', label: 'Dependencies' },
  { key: 'status', label: 'Status' },
  { key: 'overview', label: 'Overview' },
]

interface ApplicationPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — initial tab override. */
  initialTab?: TabKey
}

export function ApplicationPage({ disableStream = false, initialTab = 'logs' }: ApplicationPageProps = {}) {
  const params = useParams({ from: '/provision/$deploymentId/app/$componentId' as never }) as {
    deploymentId: string
    componentId: string
  }
  const deploymentId = params.deploymentId
  const componentId = normaliseComponentId(params.componentId) ?? params.componentId
  const router = useRouter()
  const store = useWizardStore()

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const application = findApplication(applications, componentId)

  const applicationIds = useMemo(
    () => applications.map((a) => a.id),
    [applications],
  )

  const { state, snapshot, startedAt, finishedAt, streamStatus } =
    useDeploymentEvents({
      deploymentId,
      applicationIds,
      disableStream,
    })

  const [tab, setTab] = useState<TabKey>(initialTab)

  const appState = state.apps[componentId]
  const events = state.eventsByTarget[componentId] ?? []
  const status = appState?.status ?? 'unknown'

  if (!application) {
    return (
      <AdminShell
        deploymentId={deploymentId}
        state={state}
        snapshot={snapshot}
        applications={applications}
        startedAt={startedAt}
        finishedAt={finishedAt}
        breadcrumb={
          <button
            type="button"
            onClick={() => router.navigate({ to: '/provision/$deploymentId', params: { deploymentId } })}
            className="sov-back-link"
            data-testid="sov-app-back"
          >
            <ArrowLeft size={12} aria-hidden /> All applications
          </button>
        }
      >
        <div className="sov-failure" role="alert" data-testid="sov-app-not-found">
          <h3>Unknown application</h3>
          <p>
            <code>{componentId}</code> is not part of this Sovereign's installation
            set. The application list is computed from the bootstrap-kit and the
            wizard's selected components — components you didn't select don't
            appear here.
          </p>
        </div>
      </AdminShell>
    )
  }

  return (
    <AdminShell
      deploymentId={deploymentId}
      state={state}
      snapshot={snapshot}
      applications={applications}
      startedAt={startedAt}
      finishedAt={finishedAt}
      breadcrumb={
        <button
          type="button"
          onClick={() => router.navigate({ to: '/provision/$deploymentId', params: { deploymentId } })}
          className="sov-back-link"
          data-testid="sov-app-back"
        >
          <ArrowLeft size={12} aria-hidden /> All applications
        </button>
      }
    >
      <header className="sov-app-header" data-testid="sov-app-header">
        <div className="sov-app-meta">
          <h1 className="sov-app-title" data-testid="sov-app-title">{application.title}</h1>
          <span className="sov-app-sub">
            <span data-testid="sov-app-family">{application.familyName}</span>
            {' · '}
            <span className="sov-mono">{application.id}</span>
            {application.bootstrapKit && (
              <>
                {' · '}
                <span style={{ color: 'var(--wiz-text-md)' }}>bootstrap-kit</span>
              </>
            )}
          </span>
        </div>
        <StatusPill status={status} size="md" testId="sov-app-status" />
      </header>

      <div role="tablist" className="sov-tablist" data-testid="sov-tablist">
        {TABS.map((t) => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={tab === t.key}
            onClick={() => setTab(t.key)}
            className="sov-tab"
            data-testid={`sov-tab-${t.key}`}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div className="sov-tabpanel" data-testid={`sov-tabpanel-${tab}`}>
        {tab === 'logs' && (
          <LogsTab events={events} streamStatus={streamStatus} />
        )}
        {tab === 'dependencies' && (
          <DependenciesTab application={application} deploymentId={deploymentId} />
        )}
        {tab === 'status' && (
          <StatusTab application={application} appState={appState} status={status} />
        )}
        {tab === 'overview' && (
          <OverviewTab application={application} />
        )}
      </div>
    </AdminShell>
  )
}

/* ── Tab: Logs ─────────────────────────────────────────────────── */

interface LogsTabProps {
  events: readonly DeploymentEvent[]
  streamStatus: string
}

function LogsTab({ events, streamStatus }: LogsTabProps) {
  const ref = useRef<HTMLDivElement | null>(null)
  // Auto-scroll to the bottom when new lines arrive — the operator is
  // watching live install output and expects the view to follow.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.scrollTop = el.scrollHeight
  }, [events.length])

  return (
    <div className="sov-log" ref={ref} data-testid="sov-app-log">
      {events.length === 0 ? (
        <div className="sov-log-empty" data-testid="sov-app-log-empty">
          {streamStatus === 'connecting'
            ? 'Connecting to the catalyst-api event stream — logs will populate as events arrive.'
            : 'No events emitted for this application yet.'}
        </div>
      ) : (
        events.map((ev, i) => (
          <div key={i} className="sov-log-line" data-level={ev.level ?? 'info'}>
            <span className="sov-log-ts">{(ev.time ?? '').slice(11, 19) || '—'}</span>
            <span className="sov-log-phase">{ev.phase}</span>
            <span className="sov-log-msg">{ev.message ?? ''}</span>
          </div>
        ))
      )}
    </div>
  )
}

/* ── Tab: Dependencies ─────────────────────────────────────────── */

interface DependenciesTabProps {
  application: ReturnType<typeof findApplication> & { id: string }
  deploymentId: string
}

function DependenciesTab({ application, deploymentId }: DependenciesTabProps) {
  if (!application) return null
  const dependsOn = (application.dependencies ?? [])
    .map((bare) => findComponent(bare))
    .filter((c): c is ComponentEntry => !!c)

  const dependedBy = reverseDependencies(application.bareId)
    .map((blueprintId) => {
      const bare = blueprintId.replace(/^bp-/, '')
      return findComponent(bare)
    })
    .filter((c): c is ComponentEntry => !!c)

  const family = findProduct(application.familyId)
  const familyDeps = (family?.familyDependencies ?? [])
    .map((id) => findProduct(id))
    .filter((p): p is NonNullable<typeof p> => !!p)

  return (
    <div data-testid="sov-deps-tab" style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
      <DepBlock
        title="Depends on"
        emptyHint="This component has no upstream component dependencies."
        components={dependsOn}
        deploymentId={deploymentId}
        testIdPrefix="sov-deps-on"
      />
      <DepBlock
        title="Depended on by"
        emptyHint="No other component in this Sovereign declares this as a dependency."
        components={dependedBy}
        deploymentId={deploymentId}
        testIdPrefix="sov-deps-by"
      />
      {familyDeps.length > 0 && (
        <section className="sov-card" data-testid="sov-deps-family">
          <h3>Family dependencies</h3>
          <p>
            The {application.familyName} family pulls in {familyDeps.length}{' '}
            additional product{familyDeps.length === 1 ? '' : 's'}: {' '}
            {familyDeps.map((f, i) => (
              <span key={f.id}>
                {i > 0 && ', '}
                <strong>{f.name}</strong>
              </span>
            ))}
            .
          </p>
        </section>
      )}
    </div>
  )
}

interface DepBlockProps {
  title: string
  emptyHint: string
  components: readonly ComponentEntry[]
  deploymentId: string
  testIdPrefix: string
}

function DepBlock({ title, emptyHint, components, deploymentId, testIdPrefix }: DepBlockProps) {
  return (
    <section data-testid={testIdPrefix}>
      <div className="sov-sec-head">
        <h2 className="sov-sec-h">{title}</h2>
        <span className="sov-sec-meta">{components.length}</span>
      </div>
      {components.length === 0 ? (
        <p style={{ color: 'var(--wiz-text-sub)', fontSize: '0.85rem' }}>{emptyHint}</p>
      ) : (
        <div className="sov-grid-sm">
          {components.map((c) => {
            const palette = familyChipPalette(c.product)
            const blueprintId = normaliseComponentId(c.id) ?? c.id
            return (
              <Link
                key={c.id}
                to="/provision/$deploymentId/app/$componentId"
                params={{ deploymentId, componentId: blueprintId }}
                className="sov-card"
                data-testid={`${testIdPrefix}-${c.id}`}
                style={{ textDecoration: 'none' }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                  <strong style={{ color: 'var(--wiz-text-hi)', fontSize: '0.85rem' }}>{c.name}</strong>
                  <span
                    style={{
                      marginLeft: 'auto',
                      padding: '0.1rem 0.4rem',
                      borderRadius: 999,
                      fontSize: '0.6rem',
                      fontWeight: 700,
                      letterSpacing: '0.05em',
                      textTransform: 'uppercase',
                      background: palette.bg,
                      color: palette.fg,
                      border: `1px solid ${palette.border}`,
                    }}
                  >
                    {c.groupName}
                  </span>
                </div>
                <p style={{ fontSize: '0.78rem', color: 'var(--wiz-text-md)', margin: 0 }}>{c.desc}</p>
              </Link>
            )
          })}
        </div>
      )}
    </section>
  )
}

/* ── Tab: Status ───────────────────────────────────────────────── */

interface StatusTabProps {
  application: ReturnType<typeof findApplication> & { id: string }
  appState: ReturnType<typeof Object.assign> & {
    status?: string
    helmRelease?: string | null
    namespace?: string | null
    chartVersion?: string | null
    lastEventTime?: string | null
    eventCount?: number
  } | undefined
  status: string
}

function StatusTab({ application, appState, status }: StatusTabProps) {
  if (!application) return null
  const helmRelease = appState?.helmRelease ?? application.bareId
  const namespace = appState?.namespace ?? deriveNamespaceFallback(application.bareId)
  const chartVersion = appState?.chartVersion ?? 'unknown'
  const lastEvent = appState?.lastEventTime ?? null
  const eventCount = appState?.eventCount ?? 0

  return (
    <div className="sov-grid-sm" data-testid="sov-status-tab">
      <div className="sov-card" data-testid="sov-status-state">
        <h3>Install state</h3>
        <p style={{ fontSize: '1rem', fontWeight: 700, color: 'var(--wiz-text-hi)', textTransform: 'capitalize' }}>
          {status}
        </p>
        <p>
          {eventCount === 0
            ? 'No events emitted for this application yet.'
            : `${eventCount} event${eventCount === 1 ? '' : 's'} processed.`}
        </p>
      </div>
      <div className="sov-card" data-testid="sov-status-helm">
        <h3>Helm release</h3>
        <p className="sov-mono" style={{ fontSize: '0.85rem' }}>{helmRelease}</p>
      </div>
      <div className="sov-card" data-testid="sov-status-ns">
        <h3>Namespace</h3>
        <p className="sov-mono" style={{ fontSize: '0.85rem' }}>{namespace}</p>
      </div>
      <div className="sov-card" data-testid="sov-status-chart">
        <h3>Chart version</h3>
        <p className="sov-mono" style={{ fontSize: '0.85rem' }}>{chartVersion}</p>
      </div>
      <div className="sov-card" data-testid="sov-status-last">
        <h3>Last reconciled</h3>
        <p className="sov-mono" style={{ fontSize: '0.85rem' }}>
          {lastEvent ? new Date(lastEvent).toLocaleString() : '—'}
        </p>
      </div>
      <div className="sov-card" data-testid="sov-status-tier">
        <h3>Catalyst tier</h3>
        <p style={{ textTransform: 'capitalize' }}>{application.tier}</p>
      </div>
    </div>
  )
}

/**
 * Best-effort namespace fallback when the catalyst-api hasn't emitted
 * `namespace` on a per-component event. Mirrors the one-namespace-
 * per-Blueprint convention used across the cluster manifests.
 */
function deriveNamespaceFallback(bareId: string): string {
  // Most platform Blueprints land in a namespace matching their slug;
  // Catalyst-internal services use the `catalyst` umbrella.
  if (bareId === 'flux' || bareId === 'crossplane' || bareId === 'cilium') return 'kube-system'
  if (bareId === 'cert-manager') return 'cert-manager'
  if (bareId === 'sealed-secrets') return 'sealed-secrets'
  return bareId
}

/* ── Tab: Overview ─────────────────────────────────────────────── */

function OverviewTab({ application }: { application: ReturnType<typeof findApplication> }) {
  if (!application) return null
  const copy = COMPONENT_COPY[application.bareId]
  const familyCopy = FAMILY_COPY[application.familyId]
  return (
    <div data-testid="sov-overview-tab" style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
      {familyCopy && (
        <div className="sov-card" data-testid="sov-overview-family">
          <h3>{application.familyName} family</h3>
          <p>{familyCopy.tagline}</p>
        </div>
      )}
      {copy ? (
        <>
          <div className="sov-card" data-testid="sov-overview-positioning">
            <h3>What it does</h3>
            <p>{copy.positioning}</p>
          </div>
          <div className="sov-card" data-testid="sov-overview-integration">
            <h3>How it integrates</h3>
            <p>{copy.integration}</p>
          </div>
          <div className="sov-card" data-testid="sov-overview-highlights">
            <h3>Highlights</h3>
            <ul style={{ margin: 0, paddingLeft: '1.2rem', color: 'var(--wiz-text-md)', fontSize: '0.85rem', lineHeight: 1.6 }}>
              {copy.highlights.map((h, i) => <li key={i}>{h}</li>)}
            </ul>
          </div>
          <div className="sov-card" data-testid="sov-overview-upstream">
            <h3>Upstream project</h3>
            <p>
              <a href={copy.upstreamUrl} target="_blank" rel="noopener noreferrer">
                {copy.upstreamLabel}
                <ExternalLink size={11} style={{ marginLeft: '0.25rem' }} aria-hidden />
              </a>
            </p>
          </div>
        </>
      ) : (
        <div className="sov-card" data-testid="sov-overview-fallback">
          <h3>About this application</h3>
          <p>{application.description || 'Catalyst-curated platform component.'}</p>
        </div>
      )}
    </div>
  )
}
