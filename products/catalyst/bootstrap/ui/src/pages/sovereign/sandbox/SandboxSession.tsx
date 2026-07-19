/**
 * SandboxSession — native /sandbox/$id session view.
 *
 * Hosts the xterm.js terminal that pipes the agent CLI's ANSI stdout
 * from the in-pod pty-server (see `products/sandbox/docs/
 * architecture.md` §1).
 *
 * Wire path (Wave 13 — this PR):
 *
 *   browser xterm.js ↔ WSS sandbox.<sov-fqdn>/sessions/{id}/attach
 *                              ↔ pty-server in the Sandbox pod
 *                              ↔ <agent> CLI in the same pod
 *
 * PR #1621 shipped the xterm.js HOST surface with an "API pending"
 * placeholder banner. PR #1641 + #1657 wired the BE — the
 * sandbox-controller now renders the HTTPRoute on `sandbox.<sov-fqdn>`
 * and the pty-server exposes `WS /sessions/{id}/attach` (see
 * `products/sandbox/pty-server/internal/server/routes.go`). This PR
 * (sandbox-wave13-ui-websocket) replaces the placeholder with a real
 * WebSocket adapter:
 *
 *   - stdin   : term.onData → ws.send (binary frame)
 *   - stdout  : ws.onmessage → term.write (handles ArrayBuffer + string)
 *   - resize  : window resize → fit.fit() → POST /sessions/{id}/resize
 *               with {rows, cols}
 *   - replay  : on connect, pty-server ships a single binary frame with
 *               the ring-buffer contents; we just term.write it like
 *               any other stdout chunk (no special-case)
 *   - reconnect: on close / error, schedule a retry with exponential
 *                backoff (1s, 2s, 4s, 8s, 30s ceiling). A small banner
 *                in the card header surfaces the current state
 *                (Connecting / Connected / Reconnecting …).
 *
 * Auth: the pty-server has no in-pod auth — tenancy is enforced by the
 * Cilium Gateway / HTTPRoute path prefix (which the sandbox-controller
 * scopes to the owner's namespace). The Sovereign Console SPA is
 * already authenticated against the same origin, so no extra token is
 * attached to the WS URL today. If a token requirement lands later it
 * goes on as `?access_token=<jwt>` — the same channel useK8sStream and
 * LogsTab already use (`products/catalyst/bootstrap/ui/src/lib/
 * useK8sStream.ts:148`).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state) — first paint shows the terminal chrome plus a
 *      "Connecting…" banner; the operator never sees an empty surface.
 *   #4 (never hardcode) — colours come from CSS custom properties, the
 *      backoff schedule lives in a single const, and the WS URL is
 *      derived from the deployment's sovereignFQDN.
 *
 * Design-system inheritance: PortalShell wrapper (same chrome as
 * JobsPage / SettingsPage), CSS-variable colours, amber for the
 * pending-connect indicator (documented design-token usage).
 */

import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Terminal } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import 'xterm/css/xterm.css'

import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'
import {
  deleteSandbox,
  getSandbox,
  type Sandbox,
} from '@/lib/sandbox.api'
import {
  SandboxProvisioningPanel,
  isSandboxReady,
} from './SandboxProvisioningPanel'

/** ConnectionPhase — state the small header banner reflects. */
export type SandboxConnectionPhase =
  | 'idle'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'closed'

/**
 * Reconnect backoff schedule (ms). Doubling from 1s with a 30s ceiling
 * — the same shape useComplianceStream uses (`useComplianceStream.ts`).
 * Exposed as a constant so the tests can fast-forward without
 * monkey-patching setTimeout.
 */
const RECONNECT_BACKOFF_MS = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000]

/**
 * SANDBOX_POLL_INTERVAL_MS — poll cadence for the provisioning panel
 * while the Sandbox CR is still reconciling. 2s matches the
 * sandbox-controller's reconcile resyncPeriod (so a status flip on the
 * apiserver lands in the FE within one poll cycle in the typical case).
 */
const SANDBOX_POLL_INTERVAL_MS = 2_000

export interface SandboxSessionProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /**
   * Test seam — disables the xterm.js mount so jsdom tests don't crash
   * on canvas / measureText. Production call sites never set this.
   *
   * When true the WebSocket lifecycle still runs so the contract part
   * the test cares about (open / message / close / resize POST) is
   * exercised end-to-end without a real DOM terminal.
   */
  disableTerminal?: boolean
  /**
   * Test seam — substitute the WebSocket constructor. Defaults to the
   * native browser WebSocket. The test passes a FakeWebSocket that
   * records frames and fires onopen / onmessage synchronously.
   */
  websocketFactory?: (url: string) => WebSocket
  /**
   * Test seam — substitute fetch for the resize POST. Defaults to
   * window.fetch with credentials:'include'.
   */
  resizeFetcher?: typeof fetch
  /**
   * Test seam — override the reconnect schedule (ms). Production uses
   * RECONNECT_BACKOFF_MS; tests pass [0,0,0] so the retry chain runs
   * synchronously under fake timers.
   */
  reconnectBackoffMs?: readonly number[]
  /**
   * Test seam — disable the auto-reconnect loop. The page still wires
   * the first connection and exposes the connection banner, but a
   * close does not schedule a retry. Production never sets this.
   */
  disableReconnect?: boolean
  /**
   * Test seam — bypass the React Query fetcher for the
   * provisioning-status poll with synthetic data. When set, the
   * provisioning panel renders against this value and `getSandbox` is
   * not called. Production never sets this.
   */
  initialSandboxOverride?: Sandbox | null
  /**
   * Test seam — disable the provisioning poll entirely. Used by the
   * existing WebSocket-contract tests so they don't have to mock the
   * `/sessions/{id}` endpoint. Defaults to false in production.
   */
  disableProvisioningPoll?: boolean
}

export function SandboxSession({
  disableStream = false,
  disableTerminal = false,
  websocketFactory,
  resizeFetcher,
  reconnectBackoffMs = RECONNECT_BACKOFF_MS,
  disableReconnect = false,
  initialSandboxOverride,
  disableProvisioningPoll = false,
}: SandboxSessionProps = {}) {
  const params = useParams({ strict: false }) as { id?: string }
  const sessionId = params.id ?? ''

  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const navigate = useNavigate()
  const qc = useQueryClient()

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  /* ── Provisioning poll ─────────────────────────────────────────
   *
   * Polls GET /api/v1/sandbox/sessions/{id} every 2s while the
   * controller is still reconciling. Once `phase==='Ready'` the
   * `refetchInterval` short-circuits (returns `false`) so we stop
   * polling the apiserver — at that point the xterm.js panel renders
   * the WebSocket-attached terminal and the row's status is updated
   * lazily via the existing recent-sessions list refresh on the
   * landing page.
   *
   * The query tolerates 404 by returning `null` from `getSandbox` —
   * the provisioning panel surfaces a "session no longer exists" state
   * with a back-to-landing CTA in that case.
   */
  const sandboxQuery = useQuery<Sandbox | null>({
    queryKey: ['sandbox-session', sessionId],
    queryFn: () => getSandbox(sessionId),
    enabled:
      !disableProvisioningPoll &&
      initialSandboxOverride === undefined &&
      sessionId !== '',
    refetchInterval: (query) => {
      const data = query.state.data
      // Stop polling once we hit a terminal phase. The terminal
      // surface re-mounts when phase flips to Ready; if it flips back
      // to Failed (controller marks the sandbox dead), polling stays
      // off — operator's "Delete + retry" mutation refetches.
      if (data && (data.phase === 'Ready' || data.phase === 'Failed')) {
        return false
      }
      return SANDBOX_POLL_INTERVAL_MS
    },
    refetchIntervalInBackground: false,
    placeholderData: (prev) => prev,
    // Retry-once on transient errors; the panel surfaces the message
    // and the next poll tick re-tries automatically.
    retry: 1,
    staleTime: 0,
  })

  const sandbox: Sandbox | null =
    initialSandboxOverride !== undefined
      ? initialSandboxOverride
      : sandboxQuery.data ?? null
  const pollError = sandboxQuery.error
    ? (sandboxQuery.error as Error).message
    : null
  // While the very first request is in flight we still want the panel
  // to render the target-state chrome (per docs/INVIOLABLE-PRINCIPLES.md
  // #1) — pass an empty Sandbox so the stage list shows the canonical
  // 6-row waterfall with the first row as "in progress".
  const placeholderSandbox: Sandbox = {
    id: sessionId,
    name: sessionId,
    agent: 'claude-code',
    status: 'pending',
    phase: '',
    createdAt: '',
    repo: '',
    conditions: [],
  }
  const isInitialLoad =
    initialSandboxOverride === undefined &&
    !disableProvisioningPoll &&
    sandboxQuery.isLoading
  const displaySandbox: Sandbox | null =
    sandbox ?? (isInitialLoad ? placeholderSandbox : null)
  const ready = isSandboxReady(displaySandbox)
  // While provisioning we hide the xterm.js panel — but keep its host
  // div mounted (display:none) so the WebSocket effect's element ref
  // resolution doesn't crash if a snapshot arrives mid-provisioning.
  const showTerminal = disableProvisioningPoll || ready

  /* ── Retry mutation ────────────────────────────────────────────
   *
   * "Delete + retry" — fires DELETE /sessions/{id} then bounces the
   * operator back to the landing page where they re-pick an agent.
   * The mutation re-uses the existing deleteSandbox endpoint (the
   * handler is idempotent; repeated DELETEs return 204 even when the
   * CR is gone).
   */
  const retryMutation = useMutation({
    mutationFn: () => deleteSandbox(sessionId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['sandbox-sessions'] })
      void qc.invalidateQueries({ queryKey: ['sandbox-session', sessionId] })
      navigate({ to: '/sandbox' as never })
    },
  })

  const hostRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  const [phase, setPhase] = useState<SandboxConnectionPhase>('idle')

  // Mount the xterm.js terminal. The WebSocket lifecycle runs in a
  // separate effect keyed on sessionId + sovereignFQDN so a snapshot
  // arrival after first paint upgrades the connection without
  // re-mounting the terminal.
  useEffect(() => {
    if (disableTerminal) return
    // While the provisioning panel is up, the xterm host is hidden
    // (display:none) — skip the heavy Terminal mount + WS connect
    // until the controller flips phase to Ready. This avoids the
    // ~6-retry reconnect storm against a non-existent ingress that
    // the previous PR #1670 build exhibited on a fresh sandbox.
    if (!showTerminal) return
    const host = hostRef.current
    if (!host) return

    // Theme uses our design tokens — read live so a future per-Org theme
    // override propagates to the terminal background / foreground.
    const styles = getComputedStyle(document.documentElement)
    const bg = styles.getPropertyValue('--color-bg').trim() || '#0a0a0a'
    const fg = styles.getPropertyValue('--color-text').trim() || '#e5e7eb'

    const term = new Terminal({
      convertEol: true,
      cursorBlink: true,
      fontFamily:
        'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
      fontSize: 13,
      theme: { background: bg, foreground: fg },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    try {
      fit.fit()
    } catch {
      // jsdom / no-layout — ignore, the WS connect effect re-fits on
      // first message arrival.
    }
    termRef.current = term
    fitRef.current = fit

    function onResize() {
      try {
        fit.fit()
      } catch {
        // ignore
      }
      // POST /sessions/{id}/resize with the new dimensions so the
      // server-side PTY SIGWINCHs the agent. Best-effort — a transient
      // network error is benign (next user keystroke triggers the same
      // SIGWINCH via the running fit).
      if (sovereignFQDN && sessionId && term.rows > 0 && term.cols > 0) {
        const url = `https://sandbox.${sovereignFQDN}/sessions/${encodeURIComponent(
          sessionId,
        )}/resize`
        const fetcher = resizeFetcher ?? globalThis.fetch.bind(globalThis)
        void fetcher(url, {
          method: 'POST',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ rows: term.rows, cols: term.cols }),
        }).catch(() => {
          /* swallow — resize is non-fatal */
        })
      }
    }
    window.addEventListener('resize', onResize)

    return () => {
      window.removeEventListener('resize', onResize)
      try {
        term.dispose()
      } catch {
        /* noop */
      }
      termRef.current = null
      fitRef.current = null
    }
  }, [disableTerminal, sessionId, sovereignFQDN, resizeFetcher, showTerminal])

  // WebSocket connect + auto-reconnect loop. Re-runs when sessionId or
  // sovereignFQDN changes (the snapshot arrives async after first
  // paint; once it lands the URL is stable for the rest of the
  // session). Gated on `showTerminal` so we don't reconnect-storm a
  // sandbox.<fqdn> ingress that doesn't exist yet while the controller
  // is still provisioning the Sandbox CR.
  useEffect(() => {
    if (!sessionId || !sovereignFQDN) {
      setPhase('idle')
      return
    }
    if (!showTerminal) {
      // Still provisioning — keep the connection state idle so the
      // banner stays clean. The effect re-runs when showTerminal flips
      // true (via sandboxQuery's refetchInterval) and the WS dial
      // happens then.
      setPhase('idle')
      return
    }

    const url = `wss://sandbox.${sovereignFQDN}/sessions/${encodeURIComponent(
      sessionId,
    )}/attach`
    const factory = websocketFactory ?? ((u: string) => new WebSocket(u))

    let cancelled = false
    let attempt = 0
    let retryTimer: ReturnType<typeof setTimeout> | null = null
    let currentWs: WebSocket | null = null

    function scheduleRetry() {
      if (cancelled || disableReconnect) return
      const i = Math.min(attempt, reconnectBackoffMs.length - 1)
      const wait = reconnectBackoffMs[i] ?? 30_000
      attempt += 1
      setPhase('reconnecting')
      retryTimer = setTimeout(connect, wait)
    }

    function connect() {
      if (cancelled) return
      setPhase((p) => (p === 'reconnecting' ? 'reconnecting' : 'connecting'))
      let ws: WebSocket
      try {
        ws = factory(url)
      } catch {
        scheduleRetry()
        return
      }
      currentWs = ws
      wsRef.current = ws
      ws.binaryType = 'arraybuffer'

      ws.onopen = () => {
        if (cancelled) return
        attempt = 0
        setPhase('connected')
        // Re-fit immediately so the server-side PTY matches the
        // terminal we just attached. The replay frame the pty-server
        // ships first will land in onmessage right after this.
        const term = termRef.current
        const fit = fitRef.current
        if (term && fit && sovereignFQDN && sessionId) {
          try {
            fit.fit()
          } catch {
            /* noop */
          }
          if (term.rows > 0 && term.cols > 0) {
            const u = `https://sandbox.${sovereignFQDN}/sessions/${encodeURIComponent(
              sessionId,
            )}/resize`
            const fetcher = resizeFetcher ?? globalThis.fetch.bind(globalThis)
            void fetcher(u, {
              method: 'POST',
              credentials: 'include',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ rows: term.rows, cols: term.cols }),
            }).catch(() => {
              /* noop */
            })
          }
        }
      }

      ws.onmessage = (ev: MessageEvent<unknown>) => {
        if (cancelled) return
        const term = termRef.current
        if (!term) return
        const data = ev.data
        if (typeof data === 'string') {
          term.write(data)
        } else if (data instanceof ArrayBuffer) {
          term.write(new Uint8Array(data))
        } else if (data instanceof Uint8Array) {
          term.write(data)
        } else if (
          typeof Blob !== 'undefined' &&
          data instanceof Blob
        ) {
          void data.arrayBuffer().then((buf) => {
            if (!cancelled) term.write(new Uint8Array(buf))
          })
        }
      }

      ws.onerror = () => {
        // onclose always follows; defer the retry decision there so the
        // schedule isn't doubled.
      }

      ws.onclose = () => {
        if (cancelled) return
        currentWs = null
        if (wsRef.current === ws) wsRef.current = null
        if (disableReconnect) {
          setPhase('closed')
          return
        }
        scheduleRetry()
      }
    }

    // Stdin: every keystroke / paste → ws.send. The disposable is
    // installed once and stays attached for the lifetime of this
    // effect, sending through whichever WebSocket is currently open.
    const term = termRef.current
    const stdinDisposable = term
      ? term.onData((data: string) => {
          const w = wsRef.current
          if (!w || w.readyState !== WebSocket.OPEN) return
          try {
            w.send(new TextEncoder().encode(data))
          } catch {
            /* noop — onclose will trigger reconnect */
          }
        })
      : null

    connect()

    return () => {
      cancelled = true
      if (retryTimer != null) {
        clearTimeout(retryTimer)
        retryTimer = null
      }
      try {
        stdinDisposable?.dispose()
      } catch {
        /* noop */
      }
      const w = currentWs ?? wsRef.current
      try {
        w?.close(1000, 'unmount')
      } catch {
        /* noop */
      }
      wsRef.current = null
    }
  }, [
    sessionId,
    sovereignFQDN,
    websocketFactory,
    resizeFetcher,
    reconnectBackoffMs,
    disableReconnect,
    showTerminal,
    // disableTerminal isn't in the dep array on purpose — the WS pump
    // runs regardless so the connect / message contract is exercised
    // in tests that disable the visual terminal.
  ])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle={`Agenity · ${sessionId || 'session'}`}
      headerSlotLeft={
        <Link
          to={'/sandbox' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-sandbox-back-to-landing"
        >
          ← Back to sandbox
        </Link>
      }
    >
      <div className="mx-auto max-w-7xl" data-testid="sandbox-session-page">
        {/*
          Provisioning panel — shown while the sandbox-controller is
          still reconciling the Sandbox CR. Polls /sessions/{id} every
          2s, surfaces phase + per-stage progress, flips to a failure
          card + retry CTA when phase===Failed. Hidden once
          phase===Ready so the xterm.js panel takes over.

          Skipped entirely when `disableProvisioningPoll` is set
          (existing WebSocket-contract tests). The xterm host always
          stays mounted (display:none while provisioning) so the
          WebSocket effect's hostRef stays stable across the gate flip.
        */}
        {!disableProvisioningPoll && !showTerminal ? (
          <SandboxProvisioningPanel
            sandbox={displaySandbox}
            isPolling={sandboxQuery.isFetching}
            pollError={pollError}
            onRetry={() => retryMutation.mutate()}
            onBackToLanding={() => navigate({ to: '/sandbox' as never })}
            isRetrying={retryMutation.isPending}
          />
        ) : null}

        <section
          aria-label="Agenity terminal"
          data-testid="sandbox-session-card"
          data-connection-phase={phase}
          data-show-terminal={showTerminal ? 'true' : 'false'}
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
          // Keep the section in the DOM so the xterm host's ref + the
          // resize listener stay attached across provisioning → ready
          // transitions; toggle visibility instead of unmounting.
          style={showTerminal ? undefined : { display: 'none' }}
        >
          <header className="mb-4 flex items-start justify-between gap-3">
            <div>
              <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
                Terminal
              </h2>
              <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                Native ANSI surface — the agent CLI’s output renders here
                verbatim over a WebSocket to the in-pod{' '}
                <span className="font-mono">pty-server</span>.
              </p>
            </div>
            <ConnectionBadge phase={phase} />
          </header>

          <div
            ref={hostRef}
            data-testid="sandbox-session-terminal"
            className="h-[calc(100vh-14rem)] w-full overflow-hidden rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-2"
          />
        </section>
      </div>
    </PortalShell>
  )
}

/**
 * ConnectionBadge — pill that mirrors the WebSocket lifecycle phase.
 *
 * Colours stick to documented design tokens:
 *   - connected     → emerald (steady-state)
 *   - connecting    → amber (transient, before first onopen)
 *   - reconnecting  → amber (backoff in progress)
 *   - closed        → rose (terminal — reconnect disabled or unmounted)
 *   - idle          → neutral border-only (no session id resolved yet)
 *
 * Per the design-system inheritance ruling the amber + rose ramps are
 * the same shades the Sovereign-console already uses (matches
 * sandbox-session-pending-api on PR #1621, ResourceDetailPage health
 * pills, etc.) so the chrome stays consistent across surfaces.
 */
function ConnectionBadge({ phase }: { phase: SandboxConnectionPhase }) {
  let label: string
  let tone: string
  switch (phase) {
    case 'connected':
      label = 'Connected'
      tone =
        'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
      break
    case 'connecting':
      label = 'Connecting…'
      tone = 'border-amber-500/40 bg-amber-500/10 text-amber-300'
      break
    case 'reconnecting':
      label = 'Reconnecting…'
      tone = 'border-amber-500/40 bg-amber-500/10 text-amber-300'
      break
    case 'closed':
      label = 'Disconnected'
      tone = 'border-rose-500/40 bg-rose-500/10 text-rose-300'
      break
    case 'idle':
    default:
      label = 'Pending'
      tone =
        'border-[var(--color-border)] bg-transparent text-[var(--color-text-dim)]'
      break
  }
  return (
    <span
      data-testid="sandbox-session-connection-badge"
      data-connection-phase={phase}
      className={
        'rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide ' +
        tone
      }
    >
      {label}
    </span>
  )
}
