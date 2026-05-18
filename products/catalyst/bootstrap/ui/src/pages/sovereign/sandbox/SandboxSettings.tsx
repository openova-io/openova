/**
 * SandboxSettings — /sandbox/settings.
 *
 * Wave 3 UI scaffold. Surfaces BYOS ("Bring-Your-Own-Subscription")
 * controls so the operator can connect their Claude Max account to
 * Sandbox sessions running `claude-code`.
 *
 * Wire paths (Wave 1b backend stubs):
 *   GET    /api/v1/sandbox/byos/claude-code/status
 *   POST   /api/v1/sandbox/byos/claude-code/connect       → returns OAuth URL
 *   DELETE /api/v1/sandbox/byos/claude-code/disconnect
 *
 * The Connect button opens the returned OAuth URL in a new tab; the
 * BYOS callback handler persists tokens and flips the status pill on
 * the next status poll.
 *
 * Per the design-system inheritance ruling, the page mirrors
 * SettingsPage's SectionCard chrome verbatim (rounded-xl,
 * var(--color-bg-2) on var(--color-border), h2 + tagline). The
 * Disconnect button uses the same rose-500/40 family the Settings
 * Danger zone uses (per the inheritance block's exception list).
 */

import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'
import {
  connectByosClaudeCode,
  disconnectByosClaudeCode,
  getByosClaudeCodeConfig,
  getByosStatus,
  type ByosClaudeCodeConfig,
  type ByosClaudeCodeStatus,
} from '@/lib/sandbox.api'

const QUERY_STALE_MS = 30_000

export interface SandboxSettingsProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialByosOverride?: ByosClaudeCodeStatus
  /** Test seam — bypass the React Query fetcher for the BYOS config
   *  pre-flight (`/config` endpoint). Lets tests assert both the
   *  placeholder-disabled and configured-enabled branches without HTTP. */
  initialByosConfigOverride?: ByosClaudeCodeConfig
}

export function SandboxSettings({
  disableStream = false,
  initialByosOverride,
  initialByosConfigOverride,
}: SandboxSettingsProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Sandbox settings"
      headerSlotLeft={
        <Link
          to={'/sandbox' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-sandbox-settings-back"
        >
          ← Back to sandbox
        </Link>
      }
    >
      <div className="mx-auto max-w-7xl" data-testid="sandbox-settings-page">
        <ClaudeCodeByosCard
          initialByosOverride={initialByosOverride}
          initialByosConfigOverride={initialByosConfigOverride}
        />
      </div>
    </PortalShell>
  )
}

/* ── BYOS: Claude Code (Claude Max OAuth) ─────────────────────────── */

interface ClaudeCodeByosCardProps {
  initialByosOverride?: ByosClaudeCodeStatus
  initialByosConfigOverride?: ByosClaudeCodeConfig
}

function ClaudeCodeByosCard({
  initialByosOverride,
  initialByosConfigOverride,
}: ClaudeCodeByosCardProps) {
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)

  const query = useQuery<ByosClaudeCodeStatus>({
    queryKey: ['sandbox-byos-claude-code'],
    queryFn: getByosStatus,
    staleTime: QUERY_STALE_MS,
    enabled: !initialByosOverride,
    placeholderData: (prev) => prev,
  })

  // Pre-flight: /config tells us whether the chart's OAuth client_id is
  // still the `PLACEHOLDER-AWAITING-FOUNDER-REGISTRATION` sentinel from
  // PR #1619. When it is, the Connect button MUST be disabled — clicking
  // it would fire an OAuth URL that 400s at Anthropic and confuse the
  // operator about whether BYOS is genuinely broken vs awaiting setup.
  const configQuery = useQuery<ByosClaudeCodeConfig>({
    queryKey: ['sandbox-byos-claude-code-config'],
    queryFn: getByosClaudeCodeConfig,
    staleTime: QUERY_STALE_MS,
    enabled: !initialByosConfigOverride,
    placeholderData: (prev) => prev,
  })

  const data = initialByosOverride ?? query.data ?? null
  const config = initialByosConfigOverride ?? configQuery.data ?? null
  // Default to "not configured" while the pre-flight is in-flight so we
  // never momentarily render an enabled button against an un-registered
  // client_id (would 400 at Anthropic on click).
  const clientIdConfigured = config?.clientIdConfigured ?? false
  const configPendingApi = config?.pendingApi ?? true
  const pendingApi = (data?.pendingApi ?? true) || configPendingApi
  const status = data?.status ?? 'disconnected'
  const accountLabel = data?.accountLabel ?? ''
  const connectedAt = data?.connectedAt ?? ''
  const isConnected = status === 'connected'

  const connect = useMutation({
    mutationFn: connectByosClaudeCode,
    onSuccess: ({ authorizeUrl }) => {
      setError(null)
      if (authorizeUrl) {
        // Open OAuth flow in a new tab; the callback persists tokens
        // server-side, then the next status poll flips the pill.
        window.open(authorizeUrl, '_blank', 'noopener,noreferrer')
      }
    },
    onError: (err) => setError((err as Error).message),
  })

  const disconnect = useMutation({
    mutationFn: disconnectByosClaudeCode,
    onSuccess: () => {
      setError(null)
      void qc.invalidateQueries({ queryKey: ['sandbox-byos-claude-code'] })
    },
    onError: (err) => setError((err as Error).message),
  })

  return (
    <section
      id="byos-claude-code"
      data-testid="sandbox-byos-claude-code-card"
      data-pending-api={pendingApi ? 'true' : undefined}
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
    >
      <header className="mb-4 flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
            Claude Code — Claude Max
          </h2>
          <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
            Bring your own Anthropic subscription. When connected, Sandbox
            sessions running <span className="font-mono">claude-code</span>{' '}
            use your Claude Max quota instead of platform credits.
          </p>
        </div>
        {pendingApi ? (
          <span
            data-testid="sandbox-byos-claude-code-pending-api"
            className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-300"
            title={
              !clientIdConfigured && !configPendingApi
                ? 'Anthropic OAuth client_id not yet registered — contact your Sovereign operator'
                : 'Backend API not yet wired — display only'
            }
          >
            {!clientIdConfigured && !configPendingApi
              ? 'Operator setup pending'
              : 'API pending'}
          </span>
        ) : null}
      </header>

      {error ? (
        <div
          data-testid="sandbox-byos-claude-code-error"
          className="mb-3 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-300"
        >
          {error}
        </div>
      ) : null}

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <StatusDot status={status} />
            <span
              className="text-sm font-medium text-[var(--color-text)]"
              data-testid="sandbox-byos-claude-code-status"
            >
              {humanStatus(status)}
            </span>
          </div>
          {isConnected ? (
            <p
              className="text-xs text-[var(--color-text-dim)]"
              data-testid="sandbox-byos-claude-code-account"
            >
              {accountLabel || '—'}
              {connectedAt ? (
                <span className="ml-2 font-mono text-[10px] text-[var(--color-text-dimmer)]">
                  since {new Date(connectedAt).toLocaleString()}
                </span>
              ) : null}
            </p>
          ) : !clientIdConfigured && !configPendingApi ? (
            <p
              className="text-xs text-[var(--color-text-dim)]"
              data-testid="sandbox-byos-claude-code-operator-pending"
            >
              Anthropic OAuth client_id not yet registered — contact your
              Sovereign operator to enable BYOS.
            </p>
          ) : (
            <p className="text-xs text-[var(--color-text-dim)]">
              No Anthropic account linked.
            </p>
          )}
        </div>

        <div className="flex items-center gap-2">
          {isConnected ? (
            <button
              type="button"
              onClick={() => disconnect.mutate()}
              disabled={disconnect.isPending}
              data-testid="sandbox-byos-claude-code-disconnect"
              className="rounded-md border border-rose-500/60 bg-rose-500/5 px-3 py-1.5 text-xs text-rose-300 no-underline hover:bg-rose-500/15 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {disconnect.isPending ? 'Disconnecting…' : 'Disconnect'}
            </button>
          ) : (
            <button
              type="button"
              onClick={() => connect.mutate()}
              disabled={connect.isPending || !clientIdConfigured}
              data-testid="sandbox-byos-claude-code-connect"
              data-clientid-configured={clientIdConfigured ? 'true' : 'false'}
              title={
                !clientIdConfigured
                  ? 'Anthropic OAuth client_id not yet registered — contact your Sovereign operator'
                  : undefined
              }
              aria-label={
                !clientIdConfigured
                  ? 'Connect Claude Max — disabled, Anthropic OAuth client_id not yet registered'
                  : undefined
              }
              className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs font-medium text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {connect.isPending ? 'Opening…' : 'Connect Claude Max'}
            </button>
          )}
        </div>
      </div>
    </section>
  )
}

function humanStatus(status: ByosClaudeCodeStatus['status']): string {
  switch (status) {
    case 'connected':
      return 'Connected'
    case 'pending':
      return 'Authorization pending'
    case 'error':
      return 'Connection error'
    case 'disconnected':
    default:
      return 'Not connected'
  }
}

function StatusDot({ status }: { status: ByosClaudeCodeStatus['status'] }) {
  // Token-driven dot palette — accent for connected (matches the
  // SettingsPage SectionCard accent treatment), rose-500 for error and
  // amber-500 for pending (same family the rest of the page uses for
  // destructive / pending-api). Disconnected is the dimmer-text token so
  // it reads as "no signal" without colour-coding a neutral state.
  const cls =
    status === 'connected'
      ? 'bg-[var(--color-accent)]'
      : status === 'error'
        ? 'bg-rose-500'
        : status === 'pending'
          ? 'bg-amber-500'
          : 'bg-[var(--color-text-dimmer)]'
  return (
    <span
      aria-hidden
      className={`inline-block h-2 w-2 rounded-full ${cls}`}
      data-testid="sandbox-byos-claude-code-dot"
    />
  )
}
