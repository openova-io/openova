/**
 * ExecPanel — EPIC-4 Slice E1+E2 (#1099). Pod Exec tab content.
 *
 * Flow:
 *   1. Operator clicks "Open Shell".
 *   2. POST /api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}/session
 *      → server provisions a Guacamole connection + returns
 *        {sessionId, connectionId, embedURL, fallbackWebSocketUrl}.
 *   3. UI mounts an iframe at embedURL.
 *   4. If the iframe doesn't fire `load` within 5s OR onerror trips,
 *      the UI falls through to xterm.js + the X1-style WebSocket exec
 *      at fallbackWebSocketUrl. A banner explains "Guacamole unavailable
 *      — using direct shell (recording disabled)".
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state) — fallback ships at first cut.
 *   #5 (RBAC) — the server is the authoritative gate; this UI hides
 *      the button when isTierAdmin is false (ResourceDetailPage's
 *      `isTierAdmin`-or-higher signal mirrors the developer-tier check).
 */

import { useEffect, useRef, useState } from 'react'
import { Terminal } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import 'xterm/css/xterm.css'

import {
  createExecSession,
  type ExecSessionResponse,
} from '@/pages/sovereign/cloud-list/resource.api'

export interface ExecPanelProps {
  deploymentId: string
  ns: string
  pod: string
  /** Container name. Pod's first container when omitted at call site. */
  container: string
  /** Caller's tier-developer-or-higher check. UI courtesy hide; the
   *  server is the authoritative gate per slice E brief. */
  canExec?: boolean
  /** Test seam — substitute the WebSocket constructor for E2 fallback. */
  websocketFactory?: (url: string) => WebSocket
  /** Test seam — override the iframe load timeout (ms). Default 5000. */
  iframeLoadTimeoutMs?: number
  /** Test seam — substitute the session creator. */
  createSession?: (
    deploymentId: string,
    ns: string,
    pod: string,
    container: string,
  ) => Promise<ExecSessionResponse>
  /** Test seam — disable terminal mounting. */
  disableTerminal?: boolean
}

type Phase =
  | 'idle'
  | 'creating'
  | 'iframe-loading'
  | 'iframe-ready'
  | 'fallback-loading'
  | 'fallback-ready'
  | 'error'

const DEFAULT_IFRAME_TIMEOUT_MS = 5000

export function ExecPanel(props: ExecPanelProps) {
  const {
    deploymentId,
    ns,
    pod,
    container,
    canExec = true,
    websocketFactory,
    iframeLoadTimeoutMs = DEFAULT_IFRAME_TIMEOUT_MS,
    createSession,
    disableTerminal = false,
  } = props

  const [phase, setPhase] = useState<Phase>('idle')
  const [error, setError] = useState<string | null>(null)
  const [session, setSession] = useState<ExecSessionResponse | null>(null)

  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const iframeTimerRef = useRef<number | null>(null)
  const termContainerRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  async function openShell() {
    setError(null)
    setSession(null)
    setPhase('creating')
    try {
      const create = createSession ?? createExecSession
      const resp = await create(deploymentId, ns, pod, container)
      setSession(resp)
      // G85 #2632 (2026-06-01): default to WebSocket+xterm.js (was
      // iframe-Guacamole with 5s fallback). The Guacamole iframe path
      // pointed at `guacamole.<dep>.sovereign.local` which is a
      // cluster-internal URL the operator's browser cannot resolve.
      // Every "Open shell" click 100% fell through to fallback after a
      // visible 5s spinner. Make WS+xterm the primary path; iframe
      // remains the fallback for the rare case operators have
      // resolvable Guacamole + want server-side recording.
      setPhase('fallback-loading')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setPhase('error')
    }
  }

  // Iframe load timeout → fallback.
  useEffect(() => {
    if (phase !== 'iframe-loading' || !session) return
    iframeTimerRef.current = window.setTimeout(() => {
      // Iframe never fired `load`. Fall through.
      setPhase('fallback-loading')
    }, iframeLoadTimeoutMs)
    return () => {
      if (iframeTimerRef.current != null) {
        clearTimeout(iframeTimerRef.current)
        iframeTimerRef.current = null
      }
    }
  }, [phase, session, iframeLoadTimeoutMs])

  // Fallback xterm + WebSocket.
  useEffect(() => {
    if (phase !== 'fallback-loading' || !session?.fallbackWebSocketUrl) return

    // 1. Always open the WebSocket — even in test mode where the
    //    terminal is disabled, the WS lifecycle is the contract part
    //    we actually exercise. Production also relies on this order so
    //    a still-mounting terminal can't race the first frame.
    const factory = websocketFactory ?? ((u: string) => new WebSocket(u))
    let ws: WebSocket
    try {
      ws = factory(session.fallbackWebSocketUrl)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setPhase('error')
      return
    }
    wsRef.current = ws
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => setPhase('fallback-ready')
    ws.onerror = () => setError('WebSocket error')
    ws.onclose = () => {
      // Don't auto-reconnect — exec sessions are 1:1.
    }

    // 2. In tests we skip terminal mount; the WS is enough to exercise.
    if (disableTerminal) {
      setPhase('fallback-ready')
      return () => {
        try {
          ws.close(1000, 'unmount')
        } catch {
          /* noop */
        }
      }
    }

    if (!termContainerRef.current) {
      // Container not yet rendered — close the ws and bail; the next
      // render pass will re-mount.
      try {
        ws.close(1000, 'no-container')
      } catch {
        /* noop */
      }
      return
    }
    const term = new Terminal({
      convertEol: true,
      scrollback: 5_000,
      fontFamily:
        'JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 12,
      theme: { background: '#0c0d10', foreground: '#d8dee9' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(termContainerRef.current)
    try {
      fit.fit()
    } catch {
      /* noop */
    }
    termRef.current = term
    fitRef.current = fit
    ws.onmessage = (ev) => {
      const text = decodeFrame(ev.data)
      term.write(text)
    }
    // Wire stdin: every keystroke → WS.
    const stdinDisposable = term.onData((data: string) => {
      try {
        ws.send(new TextEncoder().encode(data))
      } catch {
        /* noop */
      }
    })

    return () => {
      try {
        stdinDisposable.dispose()
      } catch {
        /* noop */
      }
      try {
        ws.close(1000, 'unmount')
      } catch {
        /* noop */
      }
      try {
        term.dispose()
      } catch {
        /* noop */
      }
      termRef.current = null
      fitRef.current = null
    }
  }, [phase, session, websocketFactory, disableTerminal])

  function onIframeLoad() {
    if (iframeTimerRef.current != null) {
      clearTimeout(iframeTimerRef.current)
      iframeTimerRef.current = null
    }
    setPhase('iframe-ready')
  }

  function onIframeError() {
    if (iframeTimerRef.current != null) {
      clearTimeout(iframeTimerRef.current)
      iframeTimerRef.current = null
    }
    setPhase('fallback-loading')
  }

  return (
    <div className="space-y-3" data-testid="exec-panel">
      {phase === 'idle' && (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6">
          <p className="text-sm text-[var(--color-text-dim)]">
            Click <strong>Open Shell</strong> to launch a recorded
            kubectl-exec into <code>{pod}/{container}</code> via Apache Guacamole.
            The session is captured and replayable from the Sessions page.
          </p>
          <button
            type="button"
            data-testid="exec-panel-open"
            onClick={openShell}
            disabled={!canExec}
            className="mt-3 rounded bg-[var(--color-accent)] px-3 py-1.5 text-sm font-medium text-[var(--color-accent-text)] disabled:opacity-50"
          >
            Open Shell
          </button>
          {!canExec && (
            <p className="mt-2 text-xs text-rose-300">
              You need tier-developer or higher to open a shell session.
            </p>
          )}
        </div>
      )}

      {phase === 'creating' && (
        <div data-testid="exec-panel-creating" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
          Creating Guacamole session…
        </div>
      )}

      {phase === 'error' && (
        <div data-testid="exec-panel-error" className="rounded-lg border border-rose-500 bg-[var(--color-bg-2)] p-6 text-sm text-rose-300">
          Error: {error}
          <button
            type="button"
            onClick={openShell}
            className="ml-4 rounded border border-rose-400 px-2 py-1 text-xs text-rose-200"
          >
            Retry
          </button>
        </div>
      )}

      {(phase === 'iframe-loading' || phase === 'iframe-ready') && session?.embedURL && (
        <div className="space-y-2">
          {session.recording && (
            <div data-testid="exec-panel-recording-on" className="rounded bg-emerald-500/10 px-3 py-1 text-xs text-emerald-300">
              Recording: ON · Session ID {session.sessionId}
            </div>
          )}
          <iframe
            ref={iframeRef}
            data-testid="exec-panel-iframe"
            src={session.embedURL}
            title={`Guacamole shell into ${pod}/${container}`}
            onLoad={onIframeLoad}
            onError={onIframeError}
            className="h-[600px] w-full rounded border border-[var(--color-border)] bg-black"
          />
        </div>
      )}

      {(phase === 'fallback-loading' || phase === 'fallback-ready') && (
        <div className="space-y-2">
          <div data-testid="exec-panel-fallback-banner" className="rounded bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
            Guacamole unavailable — using direct shell (recording disabled).
          </div>
          <div
            data-testid="exec-panel-fallback-terminal"
            ref={termContainerRef}
            tabIndex={0}
            className="h-[480px] w-full rounded border border-[var(--color-border)] bg-[#0c0d10] p-1"
          />
        </div>
      )}
    </div>
  )
}

function decodeFrame(data: unknown): string {
  if (typeof data === 'string') return data
  if (data instanceof ArrayBuffer) return new TextDecoder().decode(data)
  return ''
}
