/**
 * MenuSection — Settings → Menu: the sovereign-admin maps the console's
 * left menu (EPIC #6723 lane C).
 *
 * Founder (2026-08-31): "OpenOva is composed of applications; the left menu
 * or sub-menus of the sovereign console can be connected to the respective
 * applications, like Agenity; OpenOva should provide that flexibility in
 * its admin settings to map."
 *
 * Renders inside the SettingsPage `<SectionCard id="menu">` anchor — an
 * anchored section, NEVER a sub-route or a Settings sub-nav child (two
 * founder rulings, see SovereignSidebar.tsx "Settings: no sub-nav
 * children"). Same shape as MarketplaceSection: this component owns only
 * the inner content; the header / description come from the SectionCard.
 *
 * What the table shows: every candidate entry from the merged
 * /console-ui/sidebar-entries view — Blueprint spec.consoleUI defaults
 * (Agenity) and installed Applications with a user-UI endpoint (default
 * disabled). Per row the sovereign-admin can toggle it on/off, rename it,
 * re-route it (a console path or an https:// URL on one of this
 * Sovereign's parent domains), give it an order (0–100) and nest it under
 * a top-level item. Save → PUT /console-ui/sidebar-overrides; the API
 * validates and persists the mapping as ConfigMap
 * catalyst-system/console-ui-sidebar on the Sovereign's cluster, then the
 * sidebar query is invalidated so the rail re-renders at once.
 *
 * State machine mirrors MarketplaceSection: idle → saving → applied | error.
 * Client-side checks mirror the server's so the Save button is disabled
 * while a row is invalid, and a server 400 lists its problems verbatim.
 */

import { Fragment, useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2, Loader2, RotateCcw } from 'lucide-react'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { useConsoleScope } from '@/shared/lib/useConsoleScope'
import { getSidebarMenu, getSidebarOverrides, putSidebarOverrides, SidebarApiError } from '@/lib/console-ui.api'
import { SIDEBAR_PARENT_OPTIONS, type SidebarParentOption } from '../sovereignNav'
import {
  LABEL_MAX,
  ORDER_MAX,
  ORDER_MIN,
  TOP_LEVEL,
  overridesFromRows,
  resetRow,
  rowFromEntry,
  rowIsOverridden,
  rowProblem,
  type MenuRow,
} from './menuMapping'

type LoadState = { status: 'loading' } | { status: 'ready' } | { status: 'error'; message: string }

type SaveState =
  | { status: 'idle' }
  | { status: 'saving' }
  | { status: 'applied'; appliedAt: string }
  | { status: 'error'; message: string; problems: string[] }

/** The sidebar query key prefix SovereignSidebar reads under. */
const SIDEBAR_QUERY_PREFIX = ['console-ui-sidebar-entries'] as const

export function MenuSection() {
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? ''
  const { deploymentId: cookieDepId } = useResolvedDeploymentId()
  const deploymentId = cookieDepId ?? sovereignFQDN
  const { orgScoped } = useConsoleScope()
  const queryClient = useQueryClient()

  const [rows, setRows] = useState<MenuRow[]>([])
  const [baseline, setBaseline] = useState<MenuRow[]>([])
  const [apiParents, setApiParents] = useState<string[] | null>(null)
  const [allowedHosts, setAllowedHosts] = useState<string[]>([])
  const [readOnlyReason, setReadOnlyReason] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const [loadState, setLoadState] = useState<LoadState>({ status: 'loading' })
  const [saveState, setSaveState] = useState<SaveState>({ status: 'idle' })
  const [reloadTick, setReloadTick] = useState(0)

  // A missing Sovereign id is a render-time fact, not fetched state — derive
  // it so the effect below never sets state synchronously.
  const effectiveLoad: LoadState =
    deploymentId === '' ? { status: 'error', message: 'No Sovereign resolved for this session.' } : loadState

  // Load the merged view (never throws) and the stored overrides (throws
  // on 403 — an Org-scoped or non-admin session — which turns the table
  // read-only with the server's reason rather than hiding the surface).
  // Re-runs after a successful save (reloadTick) WITHOUT flipping back to
  // "loading": the rows are replaced in place once the re-read resolves, so
  // the "Applied" footer stays on screen.
  useEffect(() => {
    if (!deploymentId) return
    let cancelled = false
    ;(async () => {
      const menu = await getSidebarMenu(deploymentId)
      let parents: string[] | null = menu.parents.length > 0 ? menu.parents : null
      let hosts: string[] = []
      let readOnly: string | null = null
      try {
        const ov = await getSidebarOverrides(deploymentId)
        hosts = ov.allowedHosts
        if (ov.parents.length > 0) parents = ov.parents
      } catch (err) {
        if (err instanceof SidebarApiError && (err.status === 403 || err.status === 401)) {
          readOnly = err.message
        }
        // Any other failure keeps the table editable — the PUT reports its
        // own problems if the store is genuinely unreachable.
      }
      if (cancelled) return
      const next = menu.entries.map(rowFromEntry)
      setRows(next)
      setBaseline(next)
      setApiParents(parents)
      setAllowedHosts(hosts)
      setReadOnlyReason(readOnly)
      setLoadState({ status: 'ready' })
    })().catch((err: unknown) => {
      if (cancelled) return
      setLoadState({ status: 'error', message: err instanceof Error ? err.message : 'Failed to load the menu.' })
    })
    return () => {
      cancelled = true
    }
  }, [deploymentId, reloadTick])

  useEffect(() => {
    if (saveState.status !== 'applied') return
    const t = setTimeout(() => setSaveState({ status: 'idle' }), 8_000)
    return () => clearTimeout(t)
  }, [saveState])

  // Parent dropdown = FLAT_NAV options ∩ what the API accepts (when known).
  const parentOptions: readonly SidebarParentOption[] = useMemo(() => {
    if (!apiParents) return SIDEBAR_PARENT_OPTIONS
    const allowed = new Set(apiParents)
    return SIDEBAR_PARENT_OPTIONS.filter((p) => allowed.has(p.id))
  }, [apiParents])

  const problems = useMemo(() => {
    const out = new Map<string, string>()
    for (const r of rows) {
      const p = rowProblem(r, allowedHosts)
      if (p) out.set(r.id, p)
    }
    return out
  }, [rows, allowedHosts])

  const dirty = useMemo(() => {
    if (rows.length !== baseline.length) return true
    const byId = new Map(baseline.map((r) => [r.id, r]))
    return rows.some((r) => {
      const b = byId.get(r.id)
      return (
        !b ||
        b.enabled !== r.enabled ||
        b.label !== r.label ||
        b.route !== r.route ||
        b.order !== r.order ||
        b.parent !== r.parent
      )
    })
  }, [rows, baseline])

  const visibleRows = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return rows
    return rows.filter(
      (r) => r.id.toLowerCase().includes(q) || r.label.toLowerCase().includes(q) || r.route.toLowerCase().includes(q),
    )
  }, [rows, filter])

  const canSave =
    effectiveLoad.status === 'ready' &&
    !readOnlyReason &&
    dirty &&
    problems.size === 0 &&
    saveState.status !== 'saving' &&
    deploymentId !== ''

  function update(id: string, patch: Partial<MenuRow>) {
    setRows((prev) => prev.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  }

  async function handleSave() {
    if (!canSave) return
    setSaveState({ status: 'saving' })
    try {
      const res = await putSidebarOverrides(deploymentId, overridesFromRows(rows))
      setSaveState({ status: 'applied', appliedAt: res.appliedAt })
      // The rail reads the merged view under this key; invalidating it
      // re-renders the sidebar without a page reload.
      void queryClient.invalidateQueries({ queryKey: [...SIDEBAR_QUERY_PREFIX] })
      setReloadTick((t) => t + 1)
    } catch (err) {
      if (err instanceof SidebarApiError) {
        setSaveState({ status: 'error', message: `Save failed (${err.status}): ${err.message}`, problems: err.problems })
      } else {
        setSaveState({
          status: 'error',
          message: err instanceof Error ? err.message : 'Network error',
          problems: [],
        })
      }
    }
  }

  function handleDiscard() {
    setRows(baseline)
    setSaveState({ status: 'idle' })
  }

  return (
    <div data-testid="settings-menu-section">
      {orgScoped ? (
        <p className="text-sm text-[var(--color-text-dim)]" data-testid="settings-menu-org-scoped">
          The console menu is mapped by the sovereign-admin of this Sovereign. An Organization console
          shows its own fixed menu.
        </p>
      ) : null}

      {!orgScoped && effectiveLoad.status === 'loading' ? (
        <p
          className="flex items-center gap-2 text-sm text-[var(--color-text-dim)]"
          data-testid="settings-menu-loading"
        >
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Loading menu entries…
        </p>
      ) : null}

      {!orgScoped && effectiveLoad.status === 'error' ? (
        <p
          className="flex items-center gap-2 text-sm text-[var(--color-error)]"
          data-testid="settings-menu-error"
        >
          <AlertTriangle className="h-3.5 w-3.5" />
          {effectiveLoad.message}
        </p>
      ) : null}

      {!orgScoped && effectiveLoad.status === 'ready' ? (
        <>
          {readOnlyReason ? (
            <p
              className="mb-4 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200"
              data-testid="settings-menu-readonly"
            >
              Read-only: {readOnlyReason}
            </p>
          ) : null}

          <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
            <div className="min-w-0 flex-1">
              <p className="text-sm text-[var(--color-text)]">
                Every Blueprint entry and every installed Application with a user interface is a
                candidate. Enable the ones that belong on the left menu, rename or re-route them,
                choose an order (0 first, 100 last) and nest them under a top-level item to form a
                sub-menu. Changes apply to this Sovereign only.
              </p>
              {allowedHosts.length > 0 ? (
                <p className="mt-1 text-xs text-[var(--color-text-dim)]" data-testid="settings-menu-allowed-hosts">
                  Routes are console paths (<code className="font-mono">/app/…</code>) or{' '}
                  <code className="font-mono">https://</code> URLs on{' '}
                  {allowedHosts.map((h, i) => (
                    <span key={h}>
                      {i > 0 ? ', ' : ''}
                      <code className="font-mono">{h}</code>
                    </span>
                  ))}
                  .
                </p>
              ) : null}
            </div>
            <input
              type="search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter entries…"
              aria-label="Filter menu entries"
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text-strong)] placeholder:text-[var(--color-text-dimmer)] focus:border-[var(--color-accent)] focus:outline-none sm:w-56"
              data-testid="settings-menu-filter"
            />
          </div>

          {rows.length === 0 ? (
            <p className="text-sm text-[var(--color-text-dim)]" data-testid="settings-menu-empty">
              No candidate entries yet. A Blueprint that declares <code className="font-mono">consoleUI</code>{' '}
              or an installed Application with a user interface appears here once it is on this
              Sovereign.
            </p>
          ) : (
            <div className="overflow-x-auto rounded-md border border-[var(--color-border)]">
              <table className="w-full min-w-[880px] text-left text-sm" data-testid="settings-menu-table">
                <thead className="bg-[var(--color-bg)] text-[11px] uppercase tracking-wide text-[var(--color-text-dimmer)]">
                  <tr>
                    <th scope="col" className="px-3 py-2">On</th>
                    <th scope="col" className="px-3 py-2">Label</th>
                    <th scope="col" className="px-3 py-2">Source</th>
                    <th scope="col" className="px-3 py-2">Route</th>
                    <th scope="col" className="px-3 py-2">Parent</th>
                    <th scope="col" className="px-3 py-2">Order</th>
                    <th scope="col" className="px-3 py-2">
                      <span className="sr-only">Reset</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {visibleRows.map((r) => {
                    const problem = problems.get(r.id) ?? ''
                    const overridden = rowIsOverridden(r)
                    const disabled = Boolean(readOnlyReason) || saveState.status === 'saving'
                    return (
                      <Fragment key={r.id}>
                        <tr
                          className={`border-t border-[var(--color-border)] align-top ${r.enabled ? '' : 'opacity-70'}`}
                          data-testid={`settings-menu-row-${r.id}`}
                          data-enabled={r.enabled ? 'true' : 'false'}
                          data-overridden={overridden ? 'true' : undefined}
                        >
                          <td className="px-3 py-2">
                            <button
                              type="button"
                              role="switch"
                              aria-checked={r.enabled}
                              aria-label={`Show ${r.label} in the menu`}
                              disabled={disabled}
                              onClick={() => update(r.id, { enabled: !r.enabled })}
                              className={`relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-60 ${
                                r.enabled ? 'bg-[var(--color-accent)]' : 'bg-[var(--color-surface-hover)]'
                              }`}
                              data-testid={`settings-menu-enabled-${r.id}`}
                            >
                              <span
                                className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                                  r.enabled ? 'translate-x-4' : 'translate-x-0.5'
                                }`}
                              />
                            </button>
                          </td>
                          <td className="px-3 py-2">
                            <input
                              type="text"
                              value={r.label}
                              maxLength={LABEL_MAX + 1}
                              disabled={disabled}
                              onChange={(e) => update(r.id, { label: e.target.value })}
                              aria-label={`Label for ${r.id}`}
                              className="w-40 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5 text-sm text-[var(--color-text-strong)] focus:border-[var(--color-accent)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-60"
                              data-testid={`settings-menu-label-${r.id}`}
                            />
                            <p className="mt-0.5 truncate font-mono text-[10px] text-[var(--color-text-dimmer)]" title={r.id}>
                              {r.id}
                            </p>
                          </td>
                          <td className="px-3 py-2">
                            <span
                              className={`rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide ${
                                r.source === 'application'
                                  ? 'border-sky-500/40 bg-sky-500/10 text-sky-300'
                                  : 'border-violet-500/40 bg-violet-500/10 text-violet-300'
                              }`}
                              data-testid={`settings-menu-source-${r.id}`}
                            >
                              {r.source}
                            </span>
                          </td>
                          <td className="px-3 py-2">
                            <input
                              type="text"
                              value={r.route}
                              disabled={disabled}
                              onChange={(e) => update(r.id, { route: e.target.value })}
                              aria-label={`Route for ${r.id}`}
                              className="w-64 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5 font-mono text-xs text-[var(--color-text-strong)] focus:border-[var(--color-accent)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-60"
                              data-testid={`settings-menu-route-${r.id}`}
                            />
                          </td>
                          <td className="px-3 py-2">
                            <select
                              value={r.parent}
                              disabled={disabled}
                              onChange={(e) => update(r.id, { parent: e.target.value })}
                              aria-label={`Parent for ${r.id}`}
                              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5 text-sm text-[var(--color-text-strong)] focus:border-[var(--color-accent)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-60"
                              data-testid={`settings-menu-parent-${r.id}`}
                            >
                              <option value={TOP_LEVEL}>Top level</option>
                              {parentOptions.map((p) => (
                                <option key={p.id} value={p.id}>
                                  {p.label}
                                </option>
                              ))}
                            </select>
                          </td>
                          <td className="px-3 py-2">
                            <input
                              type="number"
                              min={ORDER_MIN}
                              max={ORDER_MAX}
                              step={1}
                              value={Number.isFinite(r.order) ? r.order : ''}
                              disabled={disabled}
                              onChange={(e) =>
                                update(r.id, {
                                  order: e.target.value === '' ? Number.NaN : Number(e.target.value),
                                })
                              }
                              aria-label={`Order for ${r.id}`}
                              className="w-20 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5 text-sm text-[var(--color-text-strong)] focus:border-[var(--color-accent)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-60"
                              data-testid={`settings-menu-order-${r.id}`}
                            />
                          </td>
                          <td className="px-3 py-2 text-right">
                            <button
                              type="button"
                              disabled={disabled || !overridden}
                              onClick={() => setRows((prev) => prev.map((x) => (x.id === r.id ? resetRow(x) : x)))}
                              title="Reset this entry to its Blueprint / Application default"
                              aria-label={`Reset ${r.id} to default`}
                              className="inline-flex items-center gap-1 rounded-md border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text-dim)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:cursor-not-allowed disabled:opacity-40"
                              data-testid={`settings-menu-reset-${r.id}`}
                            >
                              <RotateCcw className="h-3 w-3" />
                              Reset
                            </button>
                          </td>
                        </tr>
                        {problem ? (
                          <tr className="bg-rose-500/5" data-testid={`settings-menu-row-error-${r.id}`}>
                            <td colSpan={7} className="px-3 pb-2 text-xs text-[var(--color-error)]">
                              {problem}
                            </td>
                          </tr>
                        ) : null}
                      </Fragment>
                    )
                  })}
                  {visibleRows.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="px-3 py-3 text-xs text-[var(--color-text-dim)]" data-testid="settings-menu-filter-empty">
                        No entries match the filter.
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          )}

          <div className="mt-6 flex flex-wrap items-center justify-between gap-4 border-t border-[var(--color-border)] pt-4">
            <SaveStatus state={saveState} />
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={handleDiscard}
                disabled={!dirty || saveState.status === 'saving'}
                className="rounded-md border border-[var(--color-border)] px-3 py-2 text-sm text-[var(--color-text-dim)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:cursor-not-allowed disabled:opacity-50"
                data-testid="settings-menu-discard"
              >
                Discard
              </button>
              <button
                type="button"
                onClick={handleSave}
                disabled={!canSave}
                className="rounded-md bg-[var(--color-accent)] px-4 py-2 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-accent)]/90 disabled:cursor-not-allowed disabled:opacity-50"
                data-testid="settings-menu-save"
              >
                {saveState.status === 'saving' ? 'Saving…' : 'Save menu'}
              </button>
            </div>
          </div>
        </>
      ) : null}
    </div>
  )
}

function SaveStatus({ state }: { state: SaveState }) {
  if (state.status === 'idle') {
    return (
      <span className="text-xs text-[var(--color-text-dim)]" data-testid="settings-menu-status-idle">
        No pending changes.
      </span>
    )
  }
  if (state.status === 'saving') {
    return (
      <span
        className="flex items-center gap-2 text-xs text-[var(--color-text-dim)]"
        data-testid="settings-menu-status-saving"
      >
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        Saving the menu mapping to this Sovereign…
      </span>
    )
  }
  if (state.status === 'applied') {
    return (
      <span
        className="flex items-center gap-2 text-xs text-[color:var(--color-success,#10b981)]"
        data-testid="settings-menu-status-applied"
      >
        <CheckCircle2 className="h-3.5 w-3.5" />
        Applied at {new Date(state.appliedAt).toLocaleTimeString()} — the left menu is updated.
      </span>
    )
  }
  return (
    <span className="flex flex-col gap-1 text-xs text-[var(--color-error)]" data-testid="settings-menu-status-error">
      <span className="flex items-center gap-2">
        <AlertTriangle className="h-3.5 w-3.5" />
        {state.message}
      </span>
      {state.problems.length > 0 ? (
        <ul className="list-disc pl-5" data-testid="settings-menu-status-problems">
          {state.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      ) : null}
    </span>
  )
}
