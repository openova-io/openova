/**
 * AdminShell — pixel-port of `core/admin/src/components/AdminShell.svelte`
 * for the Sovereign Admin provision surface.
 *
 * The chrome is a 1:1 copy of https://admin.openova.io/nova/catalog:
 *   • 224px fixed sidebar with the OpenOva mark, "OpenOva Admin"
 *     wordmark, vertical nav, and a footer block with the operator's
 *     identifier + a Sign-out / Sign-out-all pair.
 *   • Main column offset by ml-56 (224px), padded p-8 (32px).
 *   • Dark-only — admin/nova has no theme toggle, so neither does the
 *     Sovereign provision surface. The existing `useTheme()` hook is
 *     intentionally NOT consumed here.
 *
 * Tokens come from the `.sov-admin-shell`-scoped block in
 * `app/globals.css` which mirrors `core/admin/src/styles/global.css`
 * exactly. That keeps the wizard's `@theme` tokens (used by the wizard
 * pages) intact while the admin-shell scope reads admin's hexes.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #3 (follow documented architecture
 * EXACTLY), the chrome is the canonical admin shell — no new card-
 * stack, no new top bar, no new "phase banner" row. Per #4 (never
 * hardcode), the operator identifier rendered in the footer comes from
 * the wizard store (orgEmail) when present, falling back to the
 * deployment id slice.
 */

import { type ReactNode } from 'react'
import { Link, useRouter } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { resolveSovereignDomain } from '@/entities/deployment/model'
import { STATUS_PULSE_KEYFRAMES } from './StatusPill'
import type { DeploymentSnapshot } from './useDeploymentEvents'
import type { ApplicationDescriptor } from './applicationCatalog'
import type { ReducerState } from './eventReducer'

export type AdminNavId = 'catalog' | 'overview' | 'topology' | 'logs' | 'settings'

interface NavItem {
  id: AdminNavId
  label: string
  /** SVG path string — heroicons outline 24, stroke=currentColor. */
  icon: string
}

/**
 * Five-item admin sidebar — same shape as core/admin/AdminShell.svelte
 * (Revenue / Catalog / Tenants / Orders / Billing) ported to the
 * Sovereign provision context. Order, icon stroke, label sentence
 * case all mirror the canonical admin nav.
 */
const NAV: readonly NavItem[] = [
  // Heroicons outline `chart-bar` — admin "Revenue" → here "Overview".
  { id: 'overview', label: 'Overview', icon: 'M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 013 19.875v-6.75zM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V8.625zM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V4.125z' },
  // Heroicons outline `inbox-stack` — admin "Catalog" identical here.
  { id: 'catalog', label: 'Catalog', icon: 'M20.25 7.5l-.625 10.632a2.25 2.25 0 01-2.247 2.118H6.622a2.25 2.25 0 01-2.247-2.118L3.75 7.5M10 11.25h4M3.375 7.5h17.25c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125z' },
  // Heroicons outline `building-office` — admin "Tenants" → here "Topology".
  { id: 'topology', label: 'Topology', icon: 'M2.25 21h19.5M3.75 3v18m0-18h16.5m-16.5 0L12 3m8.25 0v18m0-18L12 3m0 0v18' },
  // Heroicons outline `clipboard-document-list` — admin "Orders" → here "Logs".
  { id: 'logs', label: 'Logs', icon: 'M9 12h3.75M9 15h3.75M9 18h3.75m3 .75H18a2.25 2.25 0 002.25-2.25V6.108c0-1.135-.845-2.098-1.976-2.192a48.424 48.424 0 00-1.123-.08m-5.801 0c-.065.21-.1.433-.1.664 0 .414.336.75.75.75h4.5a.75.75 0 00.75-.75 2.25 2.25 0 00-.1-.664m-5.8 0A2.251 2.251 0 0113.5 2.25H15c1.012 0 1.867.668 2.15 1.586m-5.8 0c-.376.023-.75.05-1.124.08C9.095 4.01 8.25 4.973 8.25 6.108V8.25m0 0H4.875c-.621 0-1.125.504-1.125 1.125v11.25c0 .621.504 1.125 1.125 1.125h9.75c.621 0 1.125-.504 1.125-1.125V9.375c0-.621-.504-1.125-1.125-1.125H8.25z' },
  // Heroicons outline `cog-6-tooth` — admin "Billing" → here "Settings".
  { id: 'settings', label: 'Settings', icon: 'M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.324.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 011.37.49l1.296 2.247a1.125 1.125 0 01-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a6.759 6.759 0 010 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 01-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.57 6.57 0 01-.22.128c-.331.183-.581.495-.644.869l-.213 1.28c-.09.543-.56.941-1.11.941h-2.594c-.55 0-1.02-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 01-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 01-1.369-.49l-1.297-2.247a1.125 1.125 0 01.26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 010-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 01-.26-1.43l1.297-2.247a1.125 1.125 0 011.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.087.22-.128.332-.183.582-.495.644-.869l.213-1.28z M15 12a3 3 0 11-6 0 3 3 0 016 0z' },
]

interface AdminShellProps {
  /** Active nav item — defaults to `catalog`, matching the canonical admin landing. */
  activePage?: AdminNavId
  deploymentId: string
  /** Reducer state (kept on the prop surface for future header chips). */
  state?: ReducerState
  snapshot?: DeploymentSnapshot | null
  applications?: readonly ApplicationDescriptor[]
  startedAt?: number | null
  finishedAt?: number | null
  children: ReactNode
}

export function AdminShell({
  activePage = 'catalog',
  deploymentId,
  children,
}: AdminShellProps) {
  const router = useRouter()
  const store = useWizardStore()
  const sovereignFQDN = resolveSovereignDomain(store)
  const operatorEmail = store.orgEmail || `deployment-${deploymentId.slice(0, 8)}`

  function signOut() {
    router.navigate({ to: '/wizard' })
  }

  function signOutAll() {
    if (!window.confirm('Sign out of every session on every device?')) return
    router.navigate({ to: '/wizard' })
  }

  return (
    <div className="sov-admin-shell" data-testid="sov-admin-shell">
      <style>{adminCss}</style>
      <div className="flex min-h-screen">
        {/* ── Sidebar ─────────────────────────────────────────────────── */}
        <aside
          className="fixed left-0 top-0 flex h-screen w-56 flex-col border-r border-[var(--color-border)] bg-[var(--color-bg-2)]"
          data-testid="sov-sidebar"
        >
          <div className="flex h-14 items-center gap-2 border-b border-[var(--color-border)] px-4">
            {/* Canonical OpenOva mark — exact copy of admin/nova/catalog
                shell logo (700×400 viewBox, sky→indigo gradient). */}
            <svg viewBox="0 0 700 400" width={36} height={20} className="flex-shrink-0" fill="none" aria-hidden>
              <defs>
                <linearGradient id="sov-admin-logo-grad" x1="0%" y1="0%" x2="100%" y2="0%">
                  <stop offset="0%" stopColor="#3B82F6" />
                  <stop offset="100%" stopColor="#818CF8" />
                </linearGradient>
              </defs>
              <path
                d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
                fill="none"
                stroke="url(#sov-admin-logo-grad)"
                strokeWidth={100}
                strokeLinecap="butt"
              />
            </svg>
            <span className="text-sm font-semibold text-[var(--color-text-strong)]">
              OpenOva <span className="font-normal text-[var(--color-text-dim)]">Admin</span>
            </span>
          </div>

          <nav className="flex-1 overflow-y-auto py-3" data-testid="sov-nav">
            {NAV.map((item) => {
              const isActive = activePage === item.id
              const isCatalog = item.id === 'catalog'
              const linkClass = `flex items-center gap-3 mx-2 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${
                isActive
                  ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
                  : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
              }`
              return isCatalog ? (
                <Link
                  key={item.id}
                  to="/provision/$deploymentId"
                  params={{ deploymentId }}
                  className={linkClass}
                  data-testid={`sov-nav-${item.id}`}
                  data-active={isActive}
                >
                  <NavIcon path={item.icon} />
                  {item.label}
                </Link>
              ) : (
                <span
                  key={item.id}
                  className={linkClass}
                  data-testid={`sov-nav-${item.id}`}
                  data-active={isActive}
                  aria-disabled
                  style={{ cursor: 'default' }}
                  title={`${item.label} — opens once Sovereign is reachable`}
                >
                  <NavIcon path={item.icon} />
                  {item.label}
                </span>
              )
            })}
          </nav>

          <div className="border-t border-[var(--color-border)] p-3 flex flex-col gap-2">
            <p className="truncate text-xs text-[var(--color-text-dim)]" data-testid="sov-operator-email">
              {operatorEmail}
            </p>
            {sovereignFQDN && (
              <p
                className="truncate text-[10px] text-[var(--color-text-dimmer)]"
                data-testid="sov-fqdn"
                title={sovereignFQDN}
              >
                {sovereignFQDN}
              </p>
            )}
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={signOut}
                className="flex-1 rounded-md border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
                title="Sign out"
                aria-label="Sign out"
                data-testid="sov-signout"
              >
                Sign out
              </button>
              <button
                type="button"
                onClick={signOutAll}
                className="flex-1 rounded-md border border-[var(--color-danger)]/40 px-2 py-1 text-xs text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
                title="Sign out of every session on every device"
                aria-label="Sign out everywhere"
                data-testid="sov-signout-all"
              >
                Sign out all
              </button>
            </div>
          </div>
        </aside>

        {/* ── Main column ──────────────────────────────────────────── */}
        <main className="ml-56 flex-1 p-8" data-testid="sov-main">
          {children}
        </main>
      </div>
    </div>
  )
}

function NavIcon({ path }: { path: string }) {
  return (
    <svg
      width={18}
      height={18}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
      aria-hidden
    >
      <path strokeLinecap="round" strokeLinejoin="round" d={path} />
    </svg>
  )
}

/* ── CSS ──────────────────────────────────────────────────────────── */
const adminCss = `
${STATUS_PULSE_KEYFRAMES}

.sov-admin-shell {
  background: var(--color-bg);
  color: var(--color-text);
  font-family: 'Inter', system-ui, -apple-system, sans-serif;
  -webkit-font-smoothing: antialiased;
  min-height: 100vh;
}
.sov-admin-shell *, .sov-admin-shell *::before, .sov-admin-shell *::after { box-sizing: border-box; }

/* ── apps-grid + app-card (verbatim port from CatalogPage.svelte) ─ */
.sov-admin-shell .apps-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
  gap: 0.65rem;
}
.sov-admin-shell .app-card {
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
.sov-admin-shell .app-card:hover {
  transform: translateY(-2px);
  border-color: var(--color-accent);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
}
.sov-admin-shell .app-logo {
  align-self: stretch;
  aspect-ratio: 1 / 1;
  height: auto;
  border-radius: 10px;
  object-fit: cover;
  flex-shrink: 0;
}
.sov-admin-shell .app-icon {
  align-self: stretch;
  aspect-ratio: 1 / 1;
  height: auto;
  border-radius: 10px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  color: #fff;
  font-size: 1.3rem;
  font-weight: 700;
}
.sov-admin-shell .app-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
.sov-admin-shell .app-top {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
}
.sov-admin-shell .app-name {
  color: var(--color-text-strong);
  font-size: 0.92rem;
  font-weight: 600;
  line-height: 1.2;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1 1 auto;
  min-width: 0;
}
.sov-admin-shell .app-cat {
  color: var(--color-text-dim);
  font-size: 0.68rem;
  text-transform: capitalize;
  background: color-mix(in srgb, var(--color-border) 50%, transparent);
  padding: 0.1rem 0.4rem;
  border-radius: 3px;
}
.sov-admin-shell .app-desc {
  margin: 0;
  color: var(--color-text);
  font-size: 0.78rem;
  line-height: 1.45;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.sov-admin-shell .app-chips {
  margin-top: 0.25rem;
  display: flex;
  flex-wrap: nowrap;
  gap: 0.25rem;
  overflow: hidden;
  mask-image: linear-gradient(to right, #000 85%, transparent);
  -webkit-mask-image: linear-gradient(to right, #000 85%, transparent);
  min-height: 1.4rem;
}
.sov-admin-shell .chip {
  display: inline-flex;
  align-items: center;
  padding: 0.1rem 0.45rem;
  border-radius: 999px;
  font-size: 0.65rem;
  font-weight: 600;
  line-height: 1.4;
  white-space: nowrap;
}
.sov-admin-shell .chip-free {
  background: color-mix(in srgb, var(--color-success) 14%, transparent);
  color: var(--color-success);
}
.sov-admin-shell .chip-system {
  background: color-mix(in srgb, var(--color-text-dim) 18%, transparent);
  color: var(--color-text-dim);
}
.sov-admin-shell .chip-dep {
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  font-weight: 500;
}

/* ── Per-application detail surface (Logs/Deps/Status/Overview) ─ */
.sov-admin-shell .app-detail-header {
  display: flex; align-items: center; gap: 1rem;
  padding: 0 0 1rem;
}
.sov-admin-shell .app-detail-title {
  margin: 0; font-size: 1.5rem; font-weight: 700;
  color: var(--color-text-strong);
}
.sov-admin-shell .app-detail-sub {
  font-size: 0.85rem; color: var(--color-text-dim); margin: 4px 0 0;
}
.sov-admin-shell .app-detail-card {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 12px;
  padding: 1rem 1.25rem;
  display: flex; flex-direction: column; gap: 0.5rem;
}
.sov-admin-shell .app-detail-card h3 {
  margin: 0; font-size: 0.7rem; letter-spacing: 0.12em;
  text-transform: uppercase; color: var(--color-text-dim); font-weight: 700;
}
.sov-admin-shell .app-detail-card p {
  margin: 0; color: var(--color-text); font-size: 0.85rem; line-height: 1.5;
}
.sov-admin-shell .app-detail-card a {
  color: var(--color-accent); text-decoration: none;
}
.sov-admin-shell .app-detail-card a:hover { text-decoration: underline; }
.sov-admin-shell .app-detail-grid-sm {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 0.65rem;
}
.sov-admin-shell .app-log {
  height: 60vh; min-height: 320px;
  overflow-y: auto;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.72rem; line-height: 1.55;
  background: rgba(0,0,0,0.30);
  border: 1px solid var(--color-border); border-radius: 8px;
  padding: 0.6rem 0.85rem;
  display: flex; flex-direction: column; gap: 0.05rem;
}
.sov-admin-shell .app-log-empty { color: var(--color-text-dimmer); font-size: 0.8rem; padding: 0.5rem 0; }
.sov-admin-shell .app-log-line { display: flex; gap: 0.6rem; align-items: flex-start; }
.sov-admin-shell .app-log-ts { color: var(--color-text-dimmer); flex-shrink: 0; min-width: 5.5rem; }
.sov-admin-shell .app-log-phase { color: var(--color-text-dim); font-size: 0.65rem; padding: 0 0.3rem; border-radius: 3px; background: var(--color-surface-hover); margin-right: 0.4rem; }
.sov-admin-shell .app-log-msg { flex: 1; word-break: break-word; white-space: pre-wrap; color: var(--color-text); }
.sov-admin-shell .app-log-line[data-level="error"] .app-log-msg { color: var(--color-danger); }
.sov-admin-shell .app-log-line[data-level="warn"] .app-log-msg { color: var(--color-warn); }

.sov-admin-shell .app-back-link {
  display: inline-flex; align-items: center; gap: 0.3rem;
  font-size: 0.8rem; color: var(--color-text-dim);
  text-decoration: none; margin-bottom: 0.5rem;
  background: transparent; border: 0; padding: 0; font-family: inherit; cursor: pointer;
}
.sov-admin-shell .app-back-link:hover { color: var(--color-text); }
`
