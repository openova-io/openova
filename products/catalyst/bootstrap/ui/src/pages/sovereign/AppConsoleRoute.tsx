/**
 * AppConsoleRoute — serves `/apps/<blueprint-id>/dashboard`, the route a
 * Blueprint declares in `spec.consoleUI.sidebarRoute` (#2370 contract,
 * EPIC #6723 lane C: "the left menu or sub-menus of the sovereign console
 * can be connected to the respective applications, like Agenity").
 *
 * Two shipped Blueprints declare exactly that shape —
 * products/agenity/blueprint.yaml (`/apps/bp-agenity/dashboard`) and
 * products/chargeback/blueprint.yaml (`/apps/bp-chargeback/dashboard`) —
 * and the router served no `/apps/$appId/...` route at all: only `/apps`
 * (the grid) and `/app/$componentId[/$tab]` (singular). So every
 * Blueprint-registered sidebar entry landed on the SPA's Not Found page.
 * Reported live on hw307 for both entries (2026-09-02).
 *
 * Landing here means the operator asked to OPEN the application, so this
 * route resolves its front door the same way the /apps card's Open button
 * does — silent-SSO launch-url first (#3374, prompt=none&kc_idp_hint), the
 * app's own externalURL as the fallback — and navigates in this tab. It
 * never renders a second copy of the app's chrome.
 *
 * When the front door cannot be resolved the page says which of the three
 * real states it is in, because they need different actions from the
 * operator: not in the catalog at all, in the catalog but not installed,
 * or installed and still coming up (the bp-chargeback case on hw307 while
 * its pre-install hook could not pull its image).
 */
import { useEffect, useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'
import { getLaunchURL } from '@/lib/catalog.api'
import { PortalShell } from './PortalShell'

/** One row of `GET /api/v1/sovereign/apps`, narrowed to what this route reads. */
export interface AppRow {
  id: string
  status?: string
  externalURL?: string
  instance?: boolean
  blueprint?: string
}

/** The state the page renders when it cannot hand the operator a front door. */
export type AppConsoleState =
  | { kind: 'launch'; url: string }
  | { kind: 'installing'; appId: string; status: string }
  | { kind: 'not-installed'; appId: string }
  | { kind: 'unknown-app'; appId: string }

/**
 * resolveAppConsoleState — pure decision, kept out of the component so the
 * three no-front-door states are testable without a router or a network.
 *
 * `launchURL` is the silent-SSO URL when the resolver produced one (null
 * when the app has no SSO endpoint — the documented 404/409/503 branch of
 * getLaunchURL), and `row` is the app's row from the live estate, or
 * undefined when the estate does not carry it.
 */
export function resolveAppConsoleState(
  appId: string,
  row: AppRow | undefined,
  launchURL: string | null,
  inCatalog: boolean,
): AppConsoleState {
  if (launchURL) return { kind: 'launch', url: launchURL }
  if (row?.externalURL) return { kind: 'launch', url: row.externalURL }
  if (row) return { kind: 'installing', appId, status: row.status ?? 'pending' }
  if (inCatalog) return { kind: 'not-installed', appId }
  return { kind: 'unknown-app', appId }
}

/** The launch key the backend resolver expects — it strips the `bp-` prefix. */
export function launchKeyFor(appId: string): string {
  return appId.replace(/^bp-/, '')
}

/** Human label for a Blueprint id, e.g. `bp-chargeback` → `Chargeback`. */
export function appLabel(appId: string): string {
  const bare = launchKeyFor(appId)
  if (!bare) return appId
  return bare.charAt(0).toUpperCase() + bare.slice(1)
}

export function AppConsoleRoute() {
  const params = useParams({ strict: false }) as { appId?: string }
  const appId = params.appId ?? ''
  const [launchError, setLaunchError] = useState('')

  const estate = useQuery<{ rows: AppRow[]; catalog: Set<string> }>({
    queryKey: ['app-console-route', appId],
    enabled: appId !== '',
    // The installing state is the one an operator sits on; keep it live so the
    // page flips to the app by itself the moment the front door appears.
    refetchInterval: 5_000,
    queryFn: async () => {
      const res = await authedFetch(`${API_BASE}/v1/sovereign/apps`, {
        headers: { Accept: 'application/json' },
      })
      if (!res.ok) throw new Error(`sovereign apps fetch ${res.status}`)
      const body = (await res.json()) as { apps?: AppRow[] }
      const apps = body.apps ?? []
      return {
        rows: apps.filter((a) => a.id === appId && a.instance !== true),
        catalog: new Set(apps.map((a) => a.id)),
      }
    },
  })

  const row = estate.data?.rows[0]
  const launch = useQuery<string | null>({
    queryKey: ['app-console-launch', appId, row?.status ?? ''],
    // Only ask for a launch URL once the estate says the app is there — the
    // resolver 404s for an app that is not installed and that answer carries
    // no information the estate has not already given us.
    enabled: appId !== '' && !!row,
    retry: false,
    queryFn: async () => {
      const resolved = await getLaunchURL(launchKeyFor(appId))
      return resolved?.url ?? null
    },
  })

  const state = resolveAppConsoleState(
    appId,
    row,
    launch.data ?? null,
    estate.data?.catalog.has(appId) ?? false,
  )

  useEffect(() => {
    if (state.kind !== 'launch') return
    try {
      // Leaves the SPA: the application serves its own dashboard on its own
      // host. `replace` so Back returns to where the operator came from
      // rather than bouncing through this resolver again.
      window.location.replace(state.url)
    } catch (err) {
      setLaunchError(err instanceof Error ? err.message : String(err))
    }
  }, [state.kind, state.kind === 'launch' ? state.url : ''])

  const label = appLabel(appId)
  const loading = estate.isLoading || (!!row && launch.isLoading)

  return (
    <PortalShell>
      <div data-testid="app-console-route" style={{ padding: '2rem', maxWidth: '46rem' }}>
        <h1 style={{ fontSize: '1.4rem', marginBottom: '0.75rem' }}>{label}</h1>
        {loading && <p data-testid="app-console-loading">Opening {label}…</p>}
        {!loading && state.kind === 'launch' && (
          <p data-testid="app-console-launching">
            Opening {label} at <code>{state.url}</code>…
          </p>
        )}
        {!loading && state.kind === 'installing' && (
          <div data-testid="app-console-installing">
            <p>
              {label} is installed but has no front door yet — its current state is{' '}
              <strong>{state.status}</strong>. This page opens it as soon as one appears.
            </p>
            <p>
              <Link to={`/app/${appId}` as never} data-testid="app-console-detail-link">
                Open {label}&rsquo;s application detail →
              </Link>
            </p>
          </div>
        )}
        {!loading && state.kind === 'not-installed' && (
          <div data-testid="app-console-not-installed">
            <p>{label} is in the catalog but is not installed on this Sovereign yet.</p>
            <p>
              <Link to={`/install/${appId}` as never} data-testid="app-console-install-link">
                Install {label} →
              </Link>
            </p>
          </div>
        )}
        {!loading && state.kind === 'unknown-app' && (
          <div data-testid="app-console-unknown">
            <p>
              No Blueprint with the id <code>{appId}</code> is available on this Sovereign, so there
              is nothing to open. A sidebar entry pointing here comes from a Blueprint&rsquo;s
              <code> spec.consoleUI.sidebarRoute</code>.
            </p>
            <p>
              <Link to={'/catalog' as never} data-testid="app-console-catalog-link">
                Browse the catalog →
              </Link>
            </p>
          </div>
        )}
        {estate.isError && (
          <p data-testid="app-console-error">
            Could not read this Sovereign&rsquo;s applications: {String(estate.error)}
          </p>
        )}
        {launchError !== '' && <p data-testid="app-console-launch-error">{launchError}</p>}
      </div>
    </PortalShell>
  )
}
