/**
 * AdminShell — top-bar + sidebar chrome shared by AdminPage and
 * ApplicationPage. Adopts the existing wizard `--wiz-*` token set
 * (see `app/globals.css`) so the Sovereign admin surface inherits
 * the same dark / light theme and brand colour palette as the
 * wizard and marketplace pages.
 *
 * Layout contract:
 *   • Top bar (56px) — OOLogo + Sovereign FQDN + overall status
 *     pill + open-console CTA + theme toggle.
 *   • Sidebar (260px) — deployment metadata block + per-family
 *     install rollup (counts of pending / installing / installed /
 *     failed for each Catalyst product family).
 *   • Main — children render here. AdminPage owns the card grid;
 *     ApplicationPage owns the tabbed per-Application view. Both
 *     consume the same `useDeploymentEvents` hook and the AdminShell
 *     surfaces nothing dynamic by itself.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label
 * surfaced in this shell — region, control-plane SKU, worker count,
 * topology row labels — comes from the wizard store + model module,
 * never inlined here.
 */

import { type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { Sun, Moon, ExternalLink } from 'lucide-react'
import { OOLogo } from '@/shared/ui/OOLogo'
import { useTheme } from '@/shared/lib/useTheme'
import { TOPOLOGY_REGION_LABELS } from '@/entities/deployment/model'
import { useWizardStore } from '@/entities/deployment/store'
import { resolveSovereignDomain } from '@/entities/deployment/model'
import { GROUPS } from '@/pages/wizard/steps/componentGroups'
import { familyChipPalette } from '@/pages/marketplace/marketplaceCopy'
import {
  STATUS_PULSE_KEYFRAMES,
  STATUS_TONE,
  StatusPill,
  type PillStatus,
} from './StatusPill'
import {
  type ApplicationStatus,
  type ReducerState,
  computeOverallStatus,
} from './eventReducer'
import type { DeploymentSnapshot } from './useDeploymentEvents'
import type { ApplicationDescriptor } from './applicationCatalog'

interface AdminShellProps {
  deploymentId: string
  state: ReducerState
  snapshot: DeploymentSnapshot | null
  applications: readonly ApplicationDescriptor[]
  /** Optional crumb link rendered in the top bar (e.g. "← Sovereign"). */
  breadcrumb?: ReactNode
  startedAt: number | null
  finishedAt: number | null
  children: ReactNode
}

export function AdminShell({
  deploymentId,
  state,
  snapshot,
  applications,
  breadcrumb,
  startedAt,
  finishedAt,
  children,
}: AdminShellProps) {
  const { theme, toggle } = useTheme()
  const store = useWizardStore()
  const sovereignFQDN = snapshot?.result?.sovereignFQDN ?? snapshot?.sovereignFQDN ?? resolveSovereignDomain(store)
  const overall = computeOverallStatus(state)
  const overallPill: PillStatus =
    overall === 'installed' ? 'completed' : overall === 'installing' ? 'streaming' : overall

  const consoleURL = snapshot?.result?.consoleURL ?? null
  const consoleHostLabel = snapshot?.result?.sovereignFQDN ?? sovereignFQDN

  return (
    <div className="sov-shell" data-theme={theme}>
      <style>{adminCss}</style>
      {/* ── Top bar ─────────────────────────────────────────────── */}
      <header className="sov-topbar" data-testid="sov-topbar">
        <div className="sov-tb-left">
          <Link to="/" className="sov-tb-brand">
            <OOLogo h={20} id="sov-tb-logo" />
            <span className="sov-tb-wordmark">
              OpenOva <span className="sov-tb-wordmark-sub">Sovereign</span>
            </span>
          </Link>
          {breadcrumb && (
            <>
              <span className="sov-tb-sep" />
              {breadcrumb}
            </>
          )}
          <span className="sov-tb-sep" />
          <div className="sov-tb-fqdn">
            <span className="sov-tb-fqdn-label">Sovereign</span>
            <span className="sov-tb-fqdn-value" data-testid="sov-fqdn">
              {sovereignFQDN || `deployment ${deploymentId.slice(0, 8)}`}
            </span>
          </div>
        </div>
        <div className="sov-tb-right">
          <StatusPill status={overallPill} size="md" testId="sov-overall-status" />
          {consoleURL && overall === 'installed' && (
            <a
              className="sov-tb-cta"
              href={consoleURL}
              target="_blank"
              rel="noopener noreferrer"
              data-testid="sov-open-console"
            >
              Open {consoleHostLabel}
              <ExternalLink size={12} aria-hidden />
            </a>
          )}
          <button
            type="button"
            className="sov-tb-ibtn"
            onClick={toggle}
            title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
            aria-label="Toggle theme"
          >
            {theme === 'dark' ? <Sun size={14} aria-hidden /> : <Moon size={14} aria-hidden />}
          </button>
        </div>
      </header>

      {/* ── Body ────────────────────────────────────────────────── */}
      <div className="sov-body">
        <SidebarMeta
          deploymentId={deploymentId}
          state={state}
          snapshot={snapshot}
          applications={applications}
          startedAt={startedAt}
          finishedAt={finishedAt}
        />
        <main className="sov-main">{children}</main>
      </div>
    </div>
  )
}

interface SidebarMetaProps {
  deploymentId: string
  state: ReducerState
  snapshot: DeploymentSnapshot | null
  applications: readonly ApplicationDescriptor[]
  startedAt: number | null
  finishedAt: number | null
}

function SidebarMeta({
  deploymentId,
  state,
  snapshot,
  applications,
  startedAt,
  finishedAt,
}: SidebarMetaProps) {
  const store = useWizardStore()
  const region = snapshot?.region ?? store.regionCloudRegions[0] ?? 'pending'
  const provider =
    store.provider ?? store.regionProviders[0] ?? 'pending'
  const cpSize = store.regionControlPlaneSizes[0] ?? store.controlPlaneSize ?? 'pending'
  const workerSize = store.regionWorkerSizes[0] ?? store.workerSize ?? 'pending'
  const workerCount = store.regionWorkerCounts[0] ?? store.workerCount ?? 0
  const topology = store.topology ?? '—'
  const regionLabels = store.topology ? TOPOLOGY_REGION_LABELS[store.topology] : []

  // Family rollup — counts of pending / installing / installed / failed
  // per Catalyst product family. Computed from `applications` (the set
  // the AdminPage actually renders) crossed with the reducer's app
  // state map. Bootstrap-kit Applications get bucketed under the
  // synthetic "platform" family when their componentGroups owner isn't
  // present in the catalog.
  const rollup = new Map<string, { name: string; pending: number; installing: number; installed: number; failed: number; total: number }>()
  for (const app of applications) {
    let bucket = rollup.get(app.familyId)
    if (!bucket) {
      bucket = { name: app.familyName, pending: 0, installing: 0, installed: 0, failed: 0, total: 0 }
      rollup.set(app.familyId, bucket)
    }
    bucket.total += 1
    const s = state.apps[app.id]?.status ?? 'pending'
    if (s === 'installed') bucket.installed += 1
    else if (s === 'failed' || s === 'degraded') bucket.failed += 1
    else if (s === 'installing') bucket.installing += 1
    else bucket.pending += 1
  }
  // Sort families by GROUPS order so PILOT / SPINE / SURGE / SILO …
  // appear in their canonical order rather than alphabetically.
  const orderedFamilyIds = [
    ...GROUPS.map((g) => g.id),
    ...[...rollup.keys()].filter((k) => !GROUPS.some((g) => g.id === k)),
  ]

  const elapsed = elapsedLabel(startedAt, finishedAt)

  return (
    <aside className="sov-sb" data-testid="sov-sidebar">
      <section className="sov-sb-section">
        <h2 className="sov-sb-h">Deployment</h2>
        <dl className="sov-sb-dl">
          <div className="sov-sb-row">
            <dt>Id</dt>
            <dd className="sov-mono" data-testid="sov-meta-id">{deploymentId.slice(0, 12)}</dd>
          </div>
          <div className="sov-sb-row">
            <dt>Provider</dt>
            <dd>{provider}</dd>
          </div>
          <div className="sov-sb-row">
            <dt>Region</dt>
            <dd>{region}</dd>
          </div>
          <div className="sov-sb-row">
            <dt>Topology</dt>
            <dd>{String(topology).toUpperCase()}</dd>
          </div>
          <div className="sov-sb-row">
            <dt>CP SKU</dt>
            <dd className="sov-mono">{cpSize}</dd>
          </div>
          <div className="sov-sb-row">
            <dt>Workers</dt>
            <dd className="sov-mono">{workerCount} × {workerSize}</dd>
          </div>
          <div className="sov-sb-row">
            <dt>Started</dt>
            <dd className="sov-mono">{startedAt ? new Date(startedAt).toLocaleTimeString() : '—'}</dd>
          </div>
          <div className="sov-sb-row">
            <dt>Elapsed</dt>
            <dd className="sov-mono">{elapsed}</dd>
          </div>
        </dl>
        {regionLabels.length > 0 && (
          <ul className="sov-sb-regions">
            {regionLabels.map((label, i) => (
              <li key={i}>{label}</li>
            ))}
          </ul>
        )}
      </section>

      <section className="sov-sb-section" data-testid="sov-family-rollup">
        <h2 className="sov-sb-h">Family rollup</h2>
        <ul className="sov-sb-fams">
          {orderedFamilyIds
            .filter((id) => rollup.has(id))
            .map((familyId) => {
              const r = rollup.get(familyId)
              if (!r) return null
              const palette = familyChipPalette(familyId)
              const tone =
                r.failed > 0 ? STATUS_TONE.failed.fg
                : r.installing > 0 ? STATUS_TONE.installing.fg
                : r.installed === r.total ? STATUS_TONE.installed.fg
                : STATUS_TONE.pending.fg
              return (
                <li
                  key={familyId}
                  className="sov-sb-fam"
                  data-testid={`sov-fam-${familyId}`}
                >
                  <span
                    className="sov-sb-fam-chip"
                    style={{
                      background: palette.bg,
                      color: palette.fg,
                      border: `1px solid ${palette.border}`,
                    }}
                  >
                    {r.name}
                  </span>
                  <span className="sov-sb-fam-counts" style={{ color: tone }}>
                    {r.installed}/{r.total}
                  </span>
                  {r.failed > 0 && (
                    <span className="sov-sb-fam-fail" data-testid={`sov-fam-${familyId}-fail`}>
                      {r.failed} failed
                    </span>
                  )}
                  {r.installing > 0 && (
                    <span className="sov-sb-fam-busy">
                      {r.installing} installing
                    </span>
                  )}
                </li>
              )
            })}
        </ul>
      </section>
    </aside>
  )
}

/** Human-readable elapsed clock label. */
function elapsedLabel(startedAt: number | null, finishedAt: number | null): string {
  if (!startedAt) return '—'
  const end = finishedAt ?? Date.now()
  const sec = Math.max(0, Math.floor((end - startedAt) / 1000))
  return `${Math.floor(sec / 60)}m ${String(sec % 60).padStart(2, '0')}s`
}

/** Adopt status colours for unknown application status pills. */
export function applicationStatusToPill(s: ApplicationStatus): PillStatus {
  return s
}

/* ── CSS ──────────────────────────────────────────────────────────── */

const adminCss = `
${STATUS_PULSE_KEYFRAMES}
.sov-shell {
  background: var(--wiz-bg-page, var(--color-surface-0, #0b1220));
  color: var(--wiz-text-md);
  font-family: 'Inter', system-ui, sans-serif;
  display: flex;
  flex-direction: column;
  min-height: 100dvh;
  height: 100dvh;
  overflow: hidden;
}
.sov-shell *, .sov-shell *::before, .sov-shell *::after { box-sizing: border-box; }
.sov-topbar {
  flex-shrink: 0;
  height: 56px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 1rem;
  background: var(--wiz-bg-card);
  border-bottom: 1px solid var(--wiz-border-sub);
  z-index: 30;
}
.sov-tb-left, .sov-tb-right { display: flex; align-items: center; gap: 0.6rem; min-width: 0; }
.sov-tb-brand {
  display: flex; align-items: center; gap: 0.5rem;
  text-decoration: none; color: var(--wiz-text-hi);
}
.sov-tb-wordmark { font-size: 0.85rem; font-weight: 700; letter-spacing: 0.01em; }
.sov-tb-wordmark-sub { color: var(--wiz-text-sub); font-weight: 500; }
.sov-tb-sep { width: 1px; height: 22px; background: var(--wiz-border-sub); }
.sov-tb-fqdn { display: flex; flex-direction: column; min-width: 0; }
.sov-tb-fqdn-label {
  font-size: 0.55rem; letter-spacing: 0.16em; text-transform: uppercase;
  color: var(--wiz-text-hint);
}
.sov-tb-fqdn-value {
  font-size: 0.85rem; font-weight: 700; color: var(--wiz-text-hi);
  font-variant-numeric: tabular-nums;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 36ch;
}
.sov-tb-cta {
  display: inline-flex; align-items: center; gap: 0.3rem;
  padding: 0.35rem 0.7rem; border-radius: 8px;
  background: rgba(var(--wiz-accent-ch), 1); color: #fff;
  font-size: 0.75rem; font-weight: 700; text-decoration: none;
  transition: filter 0.15s;
}
.sov-tb-cta:hover { filter: brightness(0.92); }
.sov-tb-ibtn {
  width: 30px; height: 30px; display: inline-flex; align-items: center; justify-content: center;
  border-radius: 6px; border: 1px solid var(--wiz-border-sub);
  background: var(--wiz-bg-input); color: var(--wiz-text-md);
  cursor: pointer; transition: background 0.15s, color 0.15s;
}
.sov-tb-ibtn:hover { background: var(--wiz-bg-sub); color: var(--wiz-text-hi); }
.sov-body { flex: 1; display: flex; min-height: 0; overflow: hidden; }
.sov-sb {
  width: 260px; flex-shrink: 0; overflow-y: auto;
  border-right: 1px solid var(--wiz-border-sub);
  background: var(--wiz-bg-card);
}
.sov-sb-section { padding: 0.95rem 1rem; border-bottom: 1px solid var(--wiz-border-sub); }
.sov-sb-h {
  font-size: 0.6rem; letter-spacing: 0.14em; text-transform: uppercase;
  color: var(--wiz-text-hint); margin: 0 0 0.55rem; font-weight: 700;
}
.sov-sb-dl { display: grid; gap: 0.3rem; margin: 0; }
.sov-sb-row {
  display: grid; grid-template-columns: 5.5rem 1fr; gap: 0.5rem; align-items: baseline;
}
.sov-sb-row dt {
  font-size: 0.62rem; color: var(--wiz-text-sub);
  letter-spacing: 0.04em; text-transform: uppercase; font-weight: 600;
  margin: 0;
}
.sov-sb-row dd {
  margin: 0; font-size: 0.78rem; color: var(--wiz-text-hi); font-weight: 500;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.sov-mono { font-family: 'JetBrains Mono', monospace; font-size: 0.73rem !important; }
.sov-sb-regions {
  list-style: none; padding: 0.5rem 0 0; margin: 0; display: grid; gap: 0.2rem;
  font-size: 0.7rem; color: var(--wiz-text-md);
}
.sov-sb-fams { list-style: none; padding: 0; margin: 0; display: grid; gap: 0.4rem; }
.sov-sb-fam { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
.sov-sb-fam-chip {
  display: inline-flex; align-items: center; padding: 0.1rem 0.45rem;
  border-radius: 999px; font-size: 0.6rem; font-weight: 700;
  letter-spacing: 0.05em; text-transform: uppercase;
}
.sov-sb-fam-counts {
  font-size: 0.78rem; font-weight: 700; font-variant-numeric: tabular-nums;
  margin-left: auto;
}
.sov-sb-fam-fail, .sov-sb-fam-busy {
  font-size: 0.6rem; padding: 0.1rem 0.4rem; border-radius: 4px;
  letter-spacing: 0.04em; text-transform: uppercase;
}
.sov-sb-fam-fail { background: rgba(248,113,113,0.14); color: #F87171; }
.sov-sb-fam-busy { background: rgba(56,189,248,0.14); color: #38BDF8; }
.sov-main {
  flex: 1; min-width: 0; overflow: auto; padding: 1.25rem 1.5rem;
  display: flex; flex-direction: column; gap: 1rem;
}

/* ── Card geometry — mirrors corp-comp-card from StepComponents ── */
.sov-app-card.corp-comp-card {
  position: relative;
  background: var(--wiz-bg-sub);
  border: 1.5px solid var(--wiz-border-sub);
  border-radius: 12px;
  padding: 0.6rem;
  display: flex;
  align-items: stretch;
  gap: 0.75rem;
  transition: transform 0.15s, border-color 0.15s, background 0.15s;
  color: inherit; text-align: left; text-decoration: none; font: inherit;
  height: 108px; overflow: hidden;
  cursor: pointer;
}
.sov-app-card.corp-comp-card:hover {
  transform: translateY(-2px);
  border-color: rgba(var(--wiz-accent-ch), 0.7);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
}
.sov-app-card.corp-comp-card[data-status="installed"] {
  border-color: rgba(74,222,128,0.35);
  background: color-mix(in srgb, #4ADE80 5%, var(--wiz-bg-sub));
}
.sov-app-card.corp-comp-card[data-status="failed"],
.sov-app-card.corp-comp-card[data-status="degraded"] {
  border-color: rgba(248,113,113,0.45);
  background: color-mix(in srgb, #F87171 5%, var(--wiz-bg-sub));
}
.sov-app-card.corp-comp-card[data-status="installing"] {
  border-color: rgba(56,189,248,0.4);
  background: color-mix(in srgb, #38BDF8 4%, var(--wiz-bg-sub));
}
.sov-app-card .corp-comp-body {
  flex: 1; min-width: 0; display: flex; flex-direction: column;
  gap: 0.2rem; overflow: hidden;
}
.sov-app-card .corp-comp-top {
  display: flex; align-items: center; gap: 0.4rem; min-height: 22px;
}
.sov-app-card .corp-comp-name {
  color: var(--wiz-text-hi); font-size: 0.9rem; font-weight: 600;
  line-height: 1.2; overflow: hidden; text-overflow: ellipsis;
  white-space: nowrap; flex: 1 1 auto; min-width: 0;
}
.sov-app-card .corp-comp-family-chip {
  display: inline-flex; align-items: center;
  padding: 0.1rem 0.45rem; border-radius: 999px;
  font-size: 0.62rem; font-weight: 700; letter-spacing: 0.05em;
  text-transform: uppercase; flex-shrink: 0; line-height: 1.4;
}
.sov-app-card .corp-comp-desc {
  margin: 0; color: var(--wiz-text-md); font-size: 0.76rem;
  line-height: 1.4;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden;
}
.sov-app-card .corp-comp-chips {
  margin-top: 0.1rem; display: flex; flex-wrap: nowrap; gap: 0.25rem;
  overflow: hidden; min-height: 1.3rem; align-items: center;
}

/* ── Card grid + section heads ──────────────────────────────────── */
.sov-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 0.65rem;
}
.sov-sec-head {
  display: flex; align-items: baseline; justify-content: space-between;
  padding-bottom: 0.4rem; border-bottom: 1px solid var(--wiz-border-sub);
  margin-top: 0.25rem;
}
.sov-sec-h {
  margin: 0; font-size: 0.95rem; font-weight: 600; color: var(--wiz-text-hi);
}
.sov-sec-meta { color: var(--wiz-text-sub); font-size: 0.78rem; }

/* ── Phase banners ──────────────────────────────────────────────── */
.sov-phase-row { display: grid; grid-template-columns: 1fr 1fr; gap: 0.65rem; }
.sov-phase {
  border: 1px solid var(--wiz-border-sub);
  border-radius: 12px;
  padding: 0.85rem 1rem;
  background: var(--wiz-bg-card);
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}
.sov-phase[data-status="failed"] {
  border-color: rgba(248,113,113,0.45);
  background: color-mix(in srgb, #F87171 4%, var(--wiz-bg-card));
}
.sov-phase[data-status="running"] {
  border-color: rgba(56,189,248,0.4);
  background: color-mix(in srgb, #38BDF8 3%, var(--wiz-bg-card));
}
.sov-phase[data-status="done"] {
  border-color: rgba(74,222,128,0.35);
  background: color-mix(in srgb, #4ADE80 3%, var(--wiz-bg-card));
}
.sov-phase-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.sov-phase-name { font-size: 0.95rem; font-weight: 700; color: var(--wiz-text-hi); }
.sov-phase-sub { font-size: 0.7rem; color: var(--wiz-text-sub); }
.sov-phase-msg {
  font-family: 'JetBrains Mono', monospace; font-size: 0.7rem;
  color: var(--wiz-text-md); white-space: pre-wrap; word-break: break-word;
  margin: 0; padding: 0.4rem 0.55rem; border-radius: 6px;
  background: rgba(0,0,0,0.18); border: 1px solid var(--wiz-border-sub);
}
.sov-phase-toggle {
  align-self: flex-start;
  font-size: 0.7rem; color: var(--wiz-text-md);
  background: transparent; border: 1px solid var(--wiz-border-sub);
  border-radius: 6px; padding: 0.2rem 0.55rem; cursor: pointer;
  font-family: inherit; transition: color 0.15s, background 0.15s;
}
.sov-phase-toggle:hover { color: var(--wiz-text-hi); background: var(--wiz-bg-sub); }
.sov-phase-log {
  max-height: 240px; overflow-y: auto;
  font-family: 'JetBrains Mono', monospace; font-size: 0.7rem;
  background: rgba(0,0,0,0.25);
  border: 1px solid var(--wiz-border-sub); border-radius: 6px; padding: 0.45rem 0.6rem;
  display: flex; flex-direction: column; gap: 0.1rem;
}

/* ── Application page tabs + content ────────────────────────────── */
.sov-app-header {
  display: flex; align-items: center; gap: 1rem;
  padding: 0.75rem 0.25rem 1rem;
}
.sov-app-meta {
  display: flex; flex-direction: column; gap: 0.25rem; min-width: 0;
}
.sov-app-title { margin: 0; font-size: 1.4rem; color: var(--wiz-text-hi); font-weight: 700; }
.sov-app-sub { color: var(--wiz-text-sub); font-size: 0.85rem; }
.sov-tablist {
  display: flex; border-bottom: 1px solid var(--wiz-border-sub);
}
.sov-tab {
  background: transparent; border: 0; border-bottom: 2px solid transparent;
  padding: 0.65rem 1rem; font: inherit; font-size: 0.85rem; font-weight: 600;
  color: var(--wiz-text-sub); cursor: pointer;
  transition: color 0.15s, border-color 0.15s; margin-bottom: -1px;
}
.sov-tab:hover { color: var(--wiz-text-md); }
.sov-tab[aria-selected="true"] {
  color: var(--wiz-text-hi);
  border-bottom-color: rgba(var(--wiz-accent-ch), 1);
}
.sov-tabpanel { padding: 1rem 0.1rem; }
.sov-back-link {
  font-size: 0.75rem; color: var(--wiz-text-sub); text-decoration: none;
  display: inline-flex; align-items: center; gap: 0.3rem;
}
.sov-back-link:hover { color: var(--wiz-text-hi); }

/* ── Logs panel ─────────────────────────────────────────────────── */
.sov-log {
  height: 60vh; min-height: 320px;
  overflow-y: auto; font-family: 'JetBrains Mono', monospace;
  font-size: 0.72rem; line-height: 1.55;
  background: rgba(0,0,0,0.30);
  border: 1px solid var(--wiz-border-sub); border-radius: 8px;
  padding: 0.6rem 0.85rem; display: flex; flex-direction: column; gap: 0.05rem;
}
.sov-log-empty { color: var(--wiz-text-hint); font-size: 0.78rem; padding: 0.5rem 0; }
.sov-log-line { display: flex; gap: 0.6rem; align-items: flex-start; }
.sov-log-ts { color: var(--wiz-text-hint); flex-shrink: 0; min-width: 5.5rem; }
.sov-log-phase { color: var(--wiz-text-sub); font-size: 0.65rem; padding: 0 0.3rem; border-radius: 3px; background: var(--wiz-bg-sub); margin-right: 0.4rem; }
.sov-log-msg { flex: 1; word-break: break-word; white-space: pre-wrap; color: var(--wiz-text-md); }
.sov-log-line[data-level="error"] .sov-log-msg { color: #F87171; }
.sov-log-line[data-level="warn"] .sov-log-msg { color: #FBBF24; }

/* ── Status / overview panels ───────────────────────────────────── */
.sov-grid-sm { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 0.65rem; }
.sov-card {
  border: 1px solid var(--wiz-border-sub);
  background: var(--wiz-bg-card);
  border-radius: 10px; padding: 0.85rem 1rem;
  display: flex; flex-direction: column; gap: 0.4rem;
}
.sov-card h3 {
  margin: 0; font-size: 0.7rem; font-weight: 700; letter-spacing: 0.12em;
  text-transform: uppercase; color: var(--wiz-text-hint);
}
.sov-card p { margin: 0; color: var(--wiz-text-md); font-size: 0.85rem; line-height: 1.55; }
.sov-card a { color: rgba(var(--wiz-accent-ch), 1); text-decoration: none; }
.sov-card a:hover { text-decoration: underline; }

/* ── Failure card ───────────────────────────────────────────────── */
.sov-failure {
  border: 1px solid rgba(248,113,113,0.4);
  background: rgba(248,113,113,0.06);
  color: var(--wiz-text-md);
  border-radius: 12px; padding: 1rem 1.2rem;
  display: flex; flex-direction: column; gap: 0.5rem;
}
.sov-failure h3 {
  color: var(--wiz-text-hi); margin: 0; font-size: 1rem; font-weight: 700;
}
.sov-failure pre {
  font-family: 'JetBrains Mono', monospace; font-size: 0.75rem;
  background: rgba(0,0,0,0.30); border: 1px solid rgba(248,113,113,0.30);
  border-radius: 6px; padding: 0.6rem 0.8rem; margin: 0;
  white-space: pre-wrap; word-break: break-word; color: #F87171;
  max-height: 200px; overflow: auto;
}

@media (max-width: 900px) {
  .sov-sb { width: 220px; }
  .sov-phase-row { grid-template-columns: 1fr; }
}
@media (max-width: 720px) {
  .sov-body { flex-direction: column; }
  .sov-sb { width: 100%; max-height: 30vh; border-right: 0; border-bottom: 1px solid var(--wiz-border-sub); }
  .sov-main { padding: 1rem; }
}
`
