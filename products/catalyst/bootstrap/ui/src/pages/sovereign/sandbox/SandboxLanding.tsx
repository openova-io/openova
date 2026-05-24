/**
 * SandboxLanding — native /sandbox landing.
 *
 * Wave 3 UI scaffold (issue: sandbox-wave3-ui-scaffold). Surfaces the
 * 6-agent grid (aider / claude-code / cursor-agent / little-coder /
 * opencode / qwen-code) the operator picks from to start a fresh
 * Sandbox session, plus a "Recent sessions" rail for resume.
 *
 * Per the founder design-system inheritance ruling (2026-05-17 —
 * feedback_subagents_inherit_design_system.md), the page MUST share the
 * same PortalShell chrome + design tokens as Dashboard / Apps / Jobs /
 * Settings — no bespoke layout, no hex colours, no new card components.
 * Card chrome mirrors SettingsPage's SectionCard verbatim (rounded-xl,
 * var(--color-bg-2) on var(--color-border), 5-unit padding).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full surface), every card renders from mount; the Recent sessions
 * rail flips from "—" to live rows as `getSandboxes()` resolves and
 * surfaces the SettingsPage "API pending" pill when the backend is not
 * yet wired (Wave 1b stubs).
 *
 * Per #4 (never hardcode) the agent grid is derived from `SANDBOX_AGENTS`
 * in `@/lib/sandbox.api` — adding a new agent is a one-line append on
 * that catalogue, no UI changes required.
 */

import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'
import {
  SANDBOX_AGENTS,
  createSandbox,
  getSandboxes,
  type SandboxAgent,
  type SandboxesResponse,
} from '@/lib/sandbox.api'

const QUERY_STALE_MS = 30_000

export interface SandboxLandingProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialSandboxesOverride?: SandboxesResponse
}

export function SandboxLanding({
  disableStream = false,
  initialSandboxesOverride,
}: SandboxLandingProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  // Wave 5.71 (chepherd lift, #2316) — when bp-chepherd is installed
  // on the Sovereign AND build-time env CATALYST_SANDBOX_REDIRECT_TO_CHEPHERD
  // is "true", the /sandbox route 302s to /apps/bp-chepherd/dashboard.
  // chepherd-side replaces the openova-native Sandbox surface; this
  // single-line redirect avoids dual UX during the lift transition.
  // Operator can disable per-Sovereign via env override (Inviolable
  // Principle #4 — every knob operator-tunable).
  const redirectToChepherd =
    typeof import.meta !== 'undefined' &&
    (import.meta as { env?: Record<string, string> }).env?.VITE_SANDBOX_REDIRECT_TO_CHEPHERD === 'true'
  if (redirectToChepherd && typeof window !== 'undefined') {
    window.location.replace('/apps/bp-chepherd/dashboard')
    return null
  }

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const query = useQuery<SandboxesResponse>({
    queryKey: ['sandbox-sessions', deploymentId],
    queryFn: getSandboxes,
    staleTime: QUERY_STALE_MS,
    enabled: !initialSandboxesOverride,
    placeholderData: (prev) => prev,
  })

  const data = initialSandboxesOverride ?? query.data ?? null
  const pendingApi = data?.pendingApi ?? true
  const sandboxes = data?.sandboxes ?? []

  const [modalAgent, setModalAgent] = useState<SandboxAgent | null>(null)

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Sandbox"
      headerSlotLeft={
        <Link
          to={'/sandbox/settings' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-sandbox-settings-link"
        >
          Settings →
        </Link>
      }
    >
      <div className="mx-auto max-w-7xl" data-testid="sandbox-landing-page">
        {/* Agent grid — 6 cards, one per supported agent CLI */}
        <section
          aria-label="Choose an agent"
          data-testid="sandbox-agent-grid"
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
        >
          {SANDBOX_AGENTS.map((agent) => (
            <AgentCard
              key={agent.id}
              agent={agent}
              onStart={() => setModalAgent(agent)}
            />
          ))}
        </section>

        {/* Recent sessions rail — resume an existing Sandbox */}
        <section
          aria-label="Recent sessions"
          data-testid="sandbox-recent-rail"
          data-pending-api={pendingApi ? 'true' : undefined}
          className="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
        >
          <header className="mb-4 flex items-start justify-between gap-3">
            <div>
              <h2
                className="text-base font-semibold text-[var(--color-text-strong)]"
                data-testid="sandbox-recent-title"
              >
                Recent sessions
              </h2>
              <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                Sandbox sessions persist across browser tabs — closing this
                tab does not stop the agent. Pick a session to reattach.
              </p>
            </div>
            {pendingApi ? (
              <span
                data-testid="sandbox-recent-pending-api"
                className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-300"
                title="Backend API not yet wired — display only"
              >
                API pending
              </span>
            ) : null}
          </header>

          {sandboxes.length === 0 ? (
            <p
              className="text-sm text-[var(--color-text-dim)]"
              data-testid="sandbox-recent-empty"
            >
              No sandbox sessions yet. Pick an agent above to start one.
            </p>
          ) : (
            <ul className="flex flex-col gap-2" data-testid="sandbox-recent-list">
              {sandboxes.map((s) => (
                <li
                  key={s.id}
                  data-testid={`sandbox-recent-row-${s.id}`}
                  className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2"
                >
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-[var(--color-text)]">
                      {s.name}
                    </p>
                    <p className="truncate text-xs text-[var(--color-text-dim)]">
                      <span className="font-mono">{s.agent}</span>
                      {s.repo ? <> · {s.repo}</> : null}
                      {' · '}
                      <span className="uppercase tracking-wide">{s.status}</span>
                    </p>
                  </div>
                  <Link
                    to={'/sandbox/$id' as never}
                    params={{ id: s.id } as never}
                    className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-1.5 text-xs text-[var(--color-text)] no-underline hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
                    data-testid={`sandbox-recent-attach-${s.id}`}
                  >
                    Attach →
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      {modalAgent ? (
        <StartSandboxModal
          agent={modalAgent}
          onClose={() => setModalAgent(null)}
        />
      ) : null}
    </PortalShell>
  )
}

/* ── Agent card ──────────────────────────────────────────────────── */

interface AgentCardProps {
  agent: SandboxAgent
  onStart: () => void
}

function AgentCard({ agent, onStart }: AgentCardProps) {
  const isClaude = agent.id === 'claude-code'
  return (
    <article
      data-testid={`sandbox-agent-card-${agent.id}`}
      className="flex flex-col gap-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
    >
      <header className="flex items-start justify-between gap-3">
        <div>
          <h2
            className="text-base font-semibold text-[var(--color-text-strong)]"
            data-testid={`sandbox-agent-title-${agent.id}`}
          >
            {agent.label}
          </h2>
          <p className="mt-0.5 text-xs font-mono text-[var(--color-text-dim)]">
            {agent.id}
          </p>
        </div>
        <AgentIcon agentId={agent.id} />
      </header>
      <p
        className="text-sm text-[var(--color-text-dim)]"
        data-testid={`sandbox-agent-blurb-${agent.id}`}
      >
        {agent.blurb}
      </p>
      <div className="mt-auto flex items-center gap-2">
        <button
          type="button"
          onClick={onStart}
          data-testid={`sandbox-agent-start-${agent.id}`}
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-xs text-[var(--color-text)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
        >
          Start session
        </button>
        {isClaude ? (
          <Link
            to={'/sandbox/settings' as never}
            data-testid="sandbox-agent-claude-connect"
            className="rounded-md border border-[var(--color-accent)]/60 bg-[var(--color-accent)]/10 px-3 py-1.5 text-xs font-medium text-[var(--color-accent)] no-underline hover:bg-[var(--color-accent)]/20"
          >
            Connect Claude Max
          </Link>
        ) : null}
      </div>
    </article>
  )
}

/* ── Start-session modal — Add-modal shape from ParentDomainsPage ──
 *
 * Mirrors AddDomainModal's full-screen overlay + click-outside-to-close
 * + form layout. Inputs use the same border/bg classes; submit uses the
 * SettingsPage accent treatment.
 */

interface StartSandboxModalProps {
  agent: SandboxAgent
  onClose: () => void
}

function StartSandboxModal({ agent, onClose }: StartSandboxModalProps) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [repo, setRepo] = useState('')
  const [error, setError] = useState<string | null>(null)

  // Wave 15 (PR sandbox-wave15-provisioning-ui): on successful create
  // we navigate the operator straight to /sandbox/$id. The session
  // page polls GET /sessions/{id} every 2s via SandboxProvisioningPanel
  // until phase===Ready, then auto-attaches the xterm.js panel. The
  // landing's recent-sessions list still picks up the new row via the
  // ['sandbox-sessions'] invalidation so a returning operator can re-
  // attach later.
  const create = useMutation({
    mutationFn: createSandbox,
    onSuccess: (sandbox) => {
      void qc.invalidateQueries({ queryKey: ['sandbox-sessions'] })
      // Seed the per-session cache with the freshly-created row so the
      // SandboxProvisioningPanel renders the canonical 6-stage
      // waterfall on first paint without waiting for the first 2s
      // poll tick to land.
      qc.setQueryData(['sandbox-session', sandbox.id], sandbox)
      onClose()
      // Navigate to /sandbox/$id. TanStack params are typed via the
      // route's param shape; the `as never` mirrors the existing
      // Link({ to: '/sandbox/$id', params: { id } }) pattern used by
      // the Recent sessions rail above.
      navigate({
        to: '/sandbox/$id' as never,
        params: { id: sandbox.id } as never,
      })
    },
    onError: (err) => setError((err as Error).message),
  })

  function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    create.mutate({
      agent: agent.id,
      name: name.trim() || undefined,
      repo: repo.trim() || undefined,
    })
  }

  return (
    <div
      data-testid="sandbox-start-modal"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={onClose}
      role="presentation"
    >
      <form
        onSubmit={onSubmit}
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-6 shadow-xl"
      >
        <h2 className="mb-1 text-lg font-semibold text-[var(--color-text-strong)]">
          Start {agent.label} session
        </h2>
        <p className="mb-4 text-xs text-[var(--color-text-dim)]">
          Provisions a Sandbox pod in your Org vcluster running{' '}
          <span className="font-mono">{agent.id}</span>. The pod persists
          across browser tabs — closing the tab does not stop the agent.
        </p>

        {error && (
          <div
            data-testid="sandbox-start-error"
            className="mb-3 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-300"
          >
            {error}
          </div>
        )}

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Session name <span className="text-[var(--color-text-dimmer)]">(optional)</span>
          </div>
          <input
            data-testid="sandbox-start-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={`${agent.id}-${shortRand()}`}
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            autoFocus
          />
        </label>

        <label className="mb-4 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Repository <span className="text-[var(--color-text-dimmer)]">(optional)</span>
          </div>
          <input
            data-testid="sandbox-start-repo"
            type="text"
            value={repo}
            onChange={(e) => setRepo(e.target.value)}
            placeholder="org/site"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm font-mono text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          />
          <div className="mt-1 text-[10px] text-[var(--color-text-dim)]">
            Cloned into <span className="font-mono">/repo/&lt;name&gt;</span> at start.
          </div>
        </label>

        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md px-3 py-1.5 text-sm text-[var(--color-text-dim)] hover:bg-[var(--color-bg-2)]"
          >
            Cancel
          </button>
          <button
            type="submit"
            data-testid="sandbox-start-submit"
            disabled={create.isPending}
            className="rounded-md bg-[var(--color-accent)] px-4 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {create.isPending ? 'Starting…' : 'Start session'}
          </button>
        </div>
      </form>
    </div>
  )
}

/* ── Icons ─────────────────────────────────────────────────────────
 *
 * Single-stroke SVG path-d glyphs matching the SovereignSidebar icon
 * family. No emoji — per the design-system inheritance ruling. The
 * agent-specific glyph picks something thematically adjacent:
 *
 *   aider           — square brackets (text edits)
 *   claude-code     — chat bubble (Anthropic)
 *   cursor-agent    — cursor arrow
 *   little-coder    — code chevron
 *   opencode        — terminal prompt
 *   qwen-code       — sparkle / star (AI)
 */

const AGENT_ICONS: Record<SandboxAgent['id'], string> = {
  aider: 'M8 4H6a2 2 0 00-2 2v12a2 2 0 002 2h2M16 4h2a2 2 0 012 2v12a2 2 0 01-2 2h-2',
  'claude-code':
    'M21 12a8.5 8.5 0 11-3.32-6.74L21 4v6h-6',
  'cursor-agent':
    'M5 3l14 7-7 2-2 7-5-16z',
  'little-coder':
    'M9 17l-5-5 5-5M15 7l5 5-5 5',
  opencode:
    'M4 4h16v16H4z M8 9l3 3-3 3 M13 15h4',
  'qwen-code':
    'M12 3l2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5L12 3z',
}

function AgentIcon({ agentId }: { agentId: SandboxAgent['id'] }) {
  return (
    <svg
      className="h-5 w-5 shrink-0 text-[var(--color-text-dim)]"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
      aria-hidden
    >
      <path strokeLinecap="round" strokeLinejoin="round" d={AGENT_ICONS[agentId]} />
    </svg>
  )
}

function shortRand(): string {
  // 4-char base36 placeholder — server generates the canonical id; this
  // is only the placeholder text shown in the empty name input.
  return Math.random().toString(36).slice(2, 6)
}
