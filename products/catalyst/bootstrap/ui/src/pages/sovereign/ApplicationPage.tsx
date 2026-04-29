/**
 * ApplicationPage — per-Application detail surface served at
 * `/sovereign/provision/$deploymentId/app/$componentId`. Reached by
 * clicking any card on the AdminPage grid.
 *
 * The shell is the canonical `AdminShell` (1:1 admin/nova/catalog
 * sidebar + main column). The page chrome inside main reuses the
 * same patterns admin/nova ships:
 *
 *   • Back-link affordance ("← All applications").
 *   • Page header (h1 + subtitle pair) — same typography rhythm as
 *     "Catalog Management" on the AdminPage.
 *   • Tabs row — `flex gap-1 rounded-lg border bg-surface p-1`, same
 *     class set as the canonical CatalogPage tabs. Four tabs:
 *     Logs / Dependencies / Status / Overview.
 *   • Tab body — every panel is a single-column grid of `.app-detail-card`
 *     boxes, mirroring the rounded `.rounded-xl border bg-surface`
 *     box admin uses for its plan / industry / addon cards.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #3 (follow architecture EXACTLY),
 * the data layer is unchanged — `useDeploymentEvents`, `applicationCatalog`,
 * `eventReducer`, `marketplaceCopy` are the same modules. Only the
 * visual layer is replaced.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { ArrowLeft, ExternalLink } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  findProduct,
  findComponent,
  type ComponentEntry,
} from '@/pages/wizard/steps/componentGroups'
import { findApplication, resolveApplications, reverseDependencies } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { AdminShell } from './AdminShell'
import { StatusPill } from './StatusPill'
import { COMPONENT_COPY, FAMILY_COPY } from '@/pages/marketplace/marketplaceCopy'
import { normaliseComponentId, type DeploymentEvent } from './eventReducer'

type TabKey = 'logs' | 'dependencies' | 'status' | 'overview'

const TABS: readonly { key: TabKey; label: string }[] = [
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

export function ApplicationPage({
  disableStream = false,
  initialTab = 'logs',
}: ApplicationPageProps = {}) {
  const params = useParams({ from: '/provision/$deploymentId/app/$componentId' as never }) as {
    deploymentId: string
    componentId: string
  }
  const deploymentId = params.deploymentId
  const componentId = normaliseComponentId(params.componentId) ?? params.componentId
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

  const { state, streamStatus } =
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
      <AdminShell activePage="catalog" deploymentId={deploymentId}>
        <BackLink deploymentId={deploymentId} />
        <div
          className="rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-4 text-sm text-[var(--color-danger)]"
          role="alert"
          data-testid="sov-app-not-found"
        >
          <strong>Unknown application</strong>
          <p style={{ margin: '4px 0 0', color: 'var(--color-text)' }}>
            <code>{componentId}</code> is not part of this Sovereign's installation set.
            The application list is computed from the bootstrap-kit and the wizard's
            selected components — components you didn't select don't appear here.
          </p>
        </div>
      </AdminShell>
    )
  }

  return (
    <AdminShell activePage="catalog" deploymentId={deploymentId}>
      <BackLink deploymentId={deploymentId} />

      <header className="app-detail-header" data-testid="sov-app-header">
        <div style={{ flex: 1, minWidth: 0 }}>
          <h1 className="app-detail-title" data-testid="sov-app-title">
            {application.title}
          </h1>
          <p className="app-detail-sub">
            <span data-testid="sov-app-family">{application.familyName}</span>
            <span> · </span>
            <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: '0.78rem' }}>
              {application.id}
            </code>
            {application.bootstrapKit && (
              <>
                <span> · </span>
                <span style={{ color: 'var(--color-text)' }}>bootstrap-kit</span>
              </>
            )}
          </p>
        </div>
        <StatusPill status={status} size="md" testId="sov-app-status" />
      </header>

      {/* Tabs — same class set as admin's CatalogPage tabs. */}
      <div
        className="mt-2 flex gap-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-1"
        role="tablist"
        data-testid="sov-tablist"
      >
        {TABS.map((t) => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={tab === t.key}
            onClick={() => setTab(t.key)}
            className={`flex-1 rounded-md px-3 py-2 text-sm font-medium transition-colors ${
              tab === t.key
                ? 'bg-[var(--color-accent)] text-white'
                : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
            }`}
            data-testid={`sov-tab-${t.key}`}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div className="mt-4" data-testid={`sov-tabpanel-${tab}`}>
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

function BackLink({ deploymentId }: { deploymentId: string }) {
  return (
    <Link
      to="/provision/$deploymentId"
      params={{ deploymentId }}
      className="app-back-link"
      data-testid="sov-app-back"
    >
      <ArrowLeft size={12} aria-hidden /> All applications
    </Link>
  )
}

/* ── Tab: Logs ─────────────────────────────────────────────────── */

interface LogsTabProps {
  events: readonly DeploymentEvent[]
  streamStatus: string
}

function LogsTab({ events, streamStatus }: LogsTabProps) {
  const ref = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.scrollTop = el.scrollHeight
  }, [events.length])

  return (
    <div className="app-log" ref={ref} data-testid="sov-app-log">
      {events.length === 0 ? (
        <div className="app-log-empty" data-testid="sov-app-log-empty">
          {streamStatus === 'connecting'
            ? 'Connecting to the catalyst-api event stream — logs will populate as events arrive.'
            : 'No events emitted for this application yet.'}
        </div>
      ) : (
        events.map((ev, i) => (
          <div key={i} className="app-log-line" data-level={ev.level ?? 'info'}>
            <span className="app-log-ts">{(ev.time ?? '').slice(11, 19) || '—'}</span>
            <span className="app-log-phase">{ev.phase}</span>
            <span className="app-log-msg">{ev.message ?? ''}</span>
          </div>
        ))
      )}
    </div>
  )
}

/* ── Tab: Dependencies ─────────────────────────────────────────── */

interface DependenciesTabProps {
  application: NonNullable<ReturnType<typeof findApplication>>
  deploymentId: string
}

function DependenciesTab({ application, deploymentId }: DependenciesTabProps) {
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
        <section className="app-detail-card" data-testid="sov-deps-family">
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
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginBottom: '0.4rem' }}>
        <h2 style={{ margin: 0, fontSize: '0.95rem', fontWeight: 600, color: 'var(--color-text-strong)' }}>{title}</h2>
        <span style={{ fontSize: '0.78rem', color: 'var(--color-text-dim)' }}>{components.length}</span>
      </div>
      {components.length === 0 ? (
        <p style={{ color: 'var(--color-text-dim)', fontSize: '0.85rem', margin: 0 }}>{emptyHint}</p>
      ) : (
        <div className="app-detail-grid-sm">
          {components.map((c) => {
            const blueprintId = normaliseComponentId(c.id) ?? c.id
            return (
              <Link
                key={c.id}
                to="/provision/$deploymentId/app/$componentId"
                params={{ deploymentId, componentId: blueprintId }}
                className="app-detail-card"
                data-testid={`${testIdPrefix}-${c.id}`}
                style={{ textDecoration: 'none' }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                  <strong style={{ color: 'var(--color-text-strong)', fontSize: '0.85rem' }}>{c.name}</strong>
                  <span
                    style={{
                      marginLeft: 'auto',
                      padding: '0.1rem 0.4rem',
                      borderRadius: 3,
                      fontSize: '0.62rem',
                      textTransform: 'capitalize',
                      background: 'color-mix(in srgb, var(--color-border) 50%, transparent)',
                      color: 'var(--color-text-dim)',
                    }}
                  >
                    {c.groupName}
                  </span>
                </div>
                <p>{c.desc}</p>
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
  application: NonNullable<ReturnType<typeof findApplication>>
  appState:
    | {
        status?: string
        helmRelease?: string | null
        namespace?: string | null
        chartVersion?: string | null
        lastEventTime?: string | null
        eventCount?: number
      }
    | undefined
  status: string
}

function StatusTab({ application, appState, status }: StatusTabProps) {
  const helmRelease = appState?.helmRelease ?? application.bareId
  const namespace = appState?.namespace ?? deriveNamespaceFallback(application.bareId)
  const chartVersion = appState?.chartVersion ?? 'unknown'
  const lastEvent = appState?.lastEventTime ?? null
  const eventCount = appState?.eventCount ?? 0

  return (
    <div className="app-detail-grid-sm" data-testid="sov-status-tab">
      <div className="app-detail-card" data-testid="sov-status-state">
        <h3>Install state</h3>
        <p style={{ fontSize: '1rem', fontWeight: 700, color: 'var(--color-text-strong)', textTransform: 'capitalize' }}>
          {status}
        </p>
        <p>
          {eventCount === 0
            ? 'No events emitted for this application yet.'
            : `${eventCount} event${eventCount === 1 ? '' : 's'} processed.`}
        </p>
      </div>
      <div className="app-detail-card" data-testid="sov-status-helm">
        <h3>Helm release</h3>
        <p style={{ fontFamily: 'JetBrains Mono, monospace' }}>{helmRelease}</p>
      </div>
      <div className="app-detail-card" data-testid="sov-status-ns">
        <h3>Namespace</h3>
        <p style={{ fontFamily: 'JetBrains Mono, monospace' }}>{namespace}</p>
      </div>
      <div className="app-detail-card" data-testid="sov-status-chart">
        <h3>Chart version</h3>
        <p style={{ fontFamily: 'JetBrains Mono, monospace' }}>{chartVersion}</p>
      </div>
      <div className="app-detail-card" data-testid="sov-status-last">
        <h3>Last reconciled</h3>
        <p style={{ fontFamily: 'JetBrains Mono, monospace' }}>
          {lastEvent ? new Date(lastEvent).toLocaleString() : '—'}
        </p>
      </div>
      <div className="app-detail-card" data-testid="sov-status-tier">
        <h3>Catalyst tier</h3>
        <p style={{ textTransform: 'capitalize' }}>{application.tier}</p>
      </div>
    </div>
  )
}

function deriveNamespaceFallback(bareId: string): string {
  if (bareId === 'flux' || bareId === 'crossplane' || bareId === 'cilium') return 'kube-system'
  if (bareId === 'cert-manager') return 'cert-manager'
  if (bareId === 'sealed-secrets') return 'sealed-secrets'
  return bareId
}

/* ── Tab: Overview ─────────────────────────────────────────────── */

function OverviewTab({ application }: { application: NonNullable<ReturnType<typeof findApplication>> }) {
  const copy = COMPONENT_COPY[application.bareId]
  const familyCopy = FAMILY_COPY[application.familyId]
  return (
    <div data-testid="sov-overview-tab" style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
      {familyCopy && (
        <div className="app-detail-card" data-testid="sov-overview-family">
          <h3>{application.familyName} family</h3>
          <p>{familyCopy.tagline}</p>
        </div>
      )}
      {copy ? (
        <>
          <div className="app-detail-card" data-testid="sov-overview-positioning">
            <h3>What it does</h3>
            <p>{copy.positioning}</p>
          </div>
          <div className="app-detail-card" data-testid="sov-overview-integration">
            <h3>How it integrates</h3>
            <p>{copy.integration}</p>
          </div>
          <div className="app-detail-card" data-testid="sov-overview-highlights">
            <h3>Highlights</h3>
            <ul style={{ margin: 0, paddingLeft: '1.2rem', color: 'var(--color-text)', fontSize: '0.85rem', lineHeight: 1.6 }}>
              {copy.highlights.map((h, i) => (
                <li key={i}>{h}</li>
              ))}
            </ul>
          </div>
          <div className="app-detail-card" data-testid="sov-overview-upstream">
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
        <div className="app-detail-card" data-testid="sov-overview-fallback">
          <h3>About this application</h3>
          <p>{application.description || 'Catalyst-curated platform component.'}</p>
        </div>
      )}
    </div>
  )
}
