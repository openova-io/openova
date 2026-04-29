/**
 * AdminPage — pixel-port of `core/admin/src/components/CatalogPage.svelte`.
 * Served at `/sovereign/provision/$deploymentId` (StepReview's redirect
 * target). Renders the canonical admin/nova/catalog catalog surface
 * with the wizard's deployment data plumbed in.
 *
 * Visual contract (1:1 with https://admin.openova.io/nova/catalog):
 *   • H1 "Catalog Management" + subtitle "Manage apps, plans, industries,
 *     and add-ons" left, primary "+ Add App" button right (kept as a
 *     no-op affordance — admin parity; provisioning context doesn't
 *     create catalog entries from this surface).
 *   • Tabs row — Apps / Plans / Industries / Add-ons. Apps active by
 *     default; the other three render a muted "managed centrally"
 *     placeholder card for visual parity with the canonical layout.
 *   • Apps tab — auto-fit card grid (minmax 360px / 1fr) of every
 *     Application in the deployment's installation set: bootstrap-kit
 *     ∪ user-selected ∪ transitive deps. Each card mirrors the admin
 *     `.app-card` geometry (108px tall, brand-coloured logo tile,
 *     name + category chip on line 1, 2-line description, chip row
 *     with a status pill + dependency chips).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #3 (follow architecture EXACTLY),
 * the data flow is unchanged — `useDeploymentEvents` + `eventReducer`
 * + `applicationCatalog.resolveApplications` are the same modules the
 * previous implementation consumed. Only the visual layer is replaced.
 *
 * Per #1 (waterfall, target-state shape on first commit), the grid
 * renders the FULL set from first paint, every card defaulting to
 * `pending`. Cards flip status as `/events` SSE delivers per-component
 * events. There is no "loading…" branch hiding the grid.
 */

import { useMemo, useState } from 'react'
import { useParams, useRouter } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { AdminShell } from './AdminShell'
import { ApplicationCard } from './ApplicationCard'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'

type Tab = 'apps' | 'plans' | 'industries' | 'addons'

const TABS: readonly { id: Tab; label: string }[] = [
  { id: 'apps', label: 'Apps' },
  { id: 'plans', label: 'Plans' },
  { id: 'industries', label: 'Industries' },
  { id: 'addons', label: 'Add-ons' },
]

const ADD_BUTTON_LABEL: Record<Tab, string> = {
  apps: '+ Add App',
  plans: '+ Add Plan',
  industries: '+ Add Industry',
  addons: '+ Add Add-on',
}

interface AdminPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

export function AdminPage({ disableStream = false }: AdminPageProps = {}) {
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

  const applicationIds = useMemo(
    () => applications.map((a) => a.id),
    [applications],
  )

  const { state, streamStatus, streamError, retry } =
    useDeploymentEvents({
      deploymentId,
      applicationIds,
      disableStream,
    })

  const [activeTab, setActiveTab] = useState<Tab>('apps')

  const isFailed = streamStatus === 'failed' || streamStatus === 'unreachable'

  return (
    <AdminShell activePage="catalog" deploymentId={deploymentId}>
      <div data-testid="sov-admin-catalog">
        {/* ── Header ───────────────────────────────────────────────── */}
        <div className="flex items-center justify-between">
          <div>
            <h1
              className="text-2xl font-bold text-[var(--color-text-strong)]"
              data-testid="sov-page-title"
            >
              Catalog Management
            </h1>
            <p className="mt-1 text-sm text-[var(--color-text-dim)]">
              Manage apps, plans, industries, and add-ons
            </p>
          </div>
          <button
            type="button"
            onClick={() => {
              router.navigate({ to: '/wizard' })
            }}
            className="rounded-lg bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-[var(--color-accent-hover)]"
            data-testid="sov-add-button"
          >
            {ADD_BUTTON_LABEL[activeTab]}
          </button>
        </div>

        {/* ── Error banner (admin pattern) ─────────────────────────── */}
        {isFailed && (
          <div
            className="mt-4 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-3 text-sm text-[var(--color-danger)]"
            role="alert"
            data-testid="sov-error-banner"
          >
            <strong>
              {streamStatus === 'unreachable'
                ? 'Couldn’t reach the deployment stream'
                : 'Provisioning failed'}
            </strong>
            <span style={{ marginLeft: 6 }}>
              {streamError ?? `Deployment ${deploymentId.slice(0, 8)} reported a terminal failure.`}
            </span>
            <button
              type="button"
              onClick={retry}
              className="ml-3 underline"
              data-testid="sov-error-retry"
            >
              retry
            </button>
          </div>
        )}

        {/* ── Tabs ─────────────────────────────────────────────────── */}
        <div
          className="mt-6 flex gap-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-1"
          role="tablist"
          data-testid="sov-tabs"
        >
          {TABS.map((tab) => (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={activeTab === tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`flex-1 rounded-md px-3 py-2 text-sm font-medium transition-colors ${
                activeTab === tab.id
                  ? 'bg-[var(--color-accent)] text-white'
                  : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
              }`}
              data-testid={`sov-tab-${tab.id}`}
            >
              {tab.label}
            </button>
          ))}
        </div>

        {/* ── Apps tab — the main grid ─────────────────────────────── */}
        {activeTab === 'apps' && (
          <div className="apps-grid mt-4" data-testid="sov-apps-grid">
            {applications.map((app) => (
              <ApplicationCard
                key={app.id}
                app={app}
                status={state.apps[app.id]?.status ?? 'pending'}
                deploymentId={deploymentId}
              />
            ))}
          </div>
        )}

        {activeTab === 'plans' && (
          <PlaceholderGrid
            tab="Plans"
            note="Subscription plans are managed in the Sovereign pool admin and inherited by every Sovereign in this fleet."
            testId="sov-plans-grid"
          />
        )}
        {activeTab === 'industries' && (
          <PlaceholderGrid
            tab="Industries"
            note="Industry presets are curated centrally and selected from the wizard's Components step."
            testId="sov-industries-grid"
          />
        )}
        {activeTab === 'addons' && (
          <PlaceholderGrid
            tab="Add-ons"
            note="Add-ons are managed in the Sovereign pool admin and applied per-tenant from the operator console."
            testId="sov-addons-grid"
          />
        )}
      </div>
    </AdminShell>
  )
}

interface PlaceholderGridProps {
  tab: string
  note: string
  testId: string
}

function PlaceholderGrid({ tab, note, testId }: PlaceholderGridProps) {
  return (
    <div
      className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3"
      data-testid={testId}
    >
      <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
        <p className="text-lg font-bold text-[var(--color-text-strong)]">{tab}</p>
        <p className="mt-2 text-xs text-[var(--color-text-dim)]">{note}</p>
      </div>
    </div>
  )
}
