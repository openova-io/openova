/**
 * ExecPanel — EPIC-4 Slice E2 (#1099). Pod Exec tab content.
 *
 * Flow:
 *   1. Operator clicks "Open Shell".
 *   2. The panel opens the WebSocket
 *      GET /api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}?command=/bin/sh
 *      (execWebSocketURL) and mounts xterm.js on it. The shell is not
 *      recorded.
 *   3. A socket error or a close of the socket by the server shows an error
 *      (with the close code and reason) and Retry.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #5 (RBAC) the server is the
 * authoritative gate (tier developer or higher); this UI disables the
 * button when canExec is false.
 */

import { useEffect, useRef, useState } from 'react'
import { Terminal } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import 'xterm/css/xterm.css'

import { execWebSocketURL } from '@/pages/sovereign/cloud-list/resource.api'

export interface ExecPanelProps {
  deploymentId: string
  ns: string
  pod: string
  /** Container name. Pod's first container when omitted at call site. */
  container: string
  /** Caller's tier-developer-or-higher check. UI courtesy hide; the
   *  server is the authoritative gate. */
  canExec?: boolean
  /** Test seam — substitute the WebSocket constructor. */
  websocketFactory?: (url: string) => WebSocket
  /** Test seam — disable terminal mounting. */
  disableTerminal?: boolean
}

type Phase = 'idle' | 'connecting' | 'ready' | 'error'

export function ExecPanel(props: ExecPanelProps) {
  const {
    deploymentId,
    ns,
    pod,
    container,
    canExec = true,
    websocketFactory,
    disableTerminal = false,
  } = props

  const [phase, setPhase] = useState<Phase>('idle')
  const [error, setError] = useState<string | null>(null)
  // Each click of Open Shell or Retry is one connection attempt. The
  // socket lives as long as its attempt: a phase change does not close it.
  const [attempt, setAttempt] = useState(0)
  // The effect dials again when the pod coordinates change after an attempt
  // (the caller passes a placeholder container until the pod loads), so the
  // phase starts again at connecting, which renders the terminal first.
  const target = `${deploymentId}/${ns}/${pod}/${container}`
  const [dialed, setDialed] = useState(target)
  if (dialed !== target) {
    setDialed(target)
    if (attempt > 0) {
      setError(null)
      setPhase('connecting')
    }
  }

  const termContainerRef = useRef<HTMLDivElement | null>(null)

  function openShell() {
    setError(null)
    setPhase('connecting')
    setAttempt((n) => n + 1)
  }

  useEffect(() => {
    if (attempt === 0) return

    // The handlers act only while this attempt is current: the panel's own
    // close (a new attempt, the unmount) is not a failure of the shell.
    let current = true

    // 1. Always open the WebSocket — even in test mode where the
    //    terminal is disabled, the WS lifecycle is the contract part
    //    we actually exercise.
    const factory = websocketFactory ?? ((u: string) => new WebSocket(u))
    let ws: WebSocket
    try {
      ws = factory(execWebSocketURL(deploymentId, ns, pod, container))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setPhase('error')
      return
    }
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => {
      if (current) setPhase('ready')
    }
    ws.onerror = () => {
      if (!current) return
      setError('WebSocket error')
      setPhase('error')
    }
    ws.onclose = (ev: CloseEvent) => {
      // The server ends the shell with a close frame (1000 when the exec
      // stream ends, 1011 with the apiserver error), which fires no error
      // event. Don't auto-reconnect — exec sessions are 1:1.
      if (!current) return
      const closed = `the shell closed: ${ev.code}${ev.reason ? ` ${ev.reason}` : ''}`
      setError((prev) => prev ?? closed)
      setPhase('error')
    }
    const release = (reason: string) => {
      current = false
      try {
        ws.close(1000, reason)
      } catch {
        /* noop */
      }
    }

    // 2. In tests we skip terminal mount; the WS is enough to exercise.
    if (disableTerminal) {
      return () => release('unmount')
    }

    if (!termContainerRef.current) {
      // No terminal to attach the shell to: never leave a socket open
      // that nothing renders.
      release('no-container')
      setError('the terminal could not be mounted')
      setPhase('error')
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
    ws.onmessage = (ev) => {
      term.write(decodeFrame(ev.data))
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
      release('unmount')
      try {
        term.dispose()
      } catch {
        /* noop */
      }
    }
  }, [attempt, deploymentId, ns, pod, container, websocketFactory, disableTerminal])

  return (
    <div className="space-y-3" data-testid="exec-panel" data-phase={phase}>
      {phase === 'idle' && (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6">
          <p className="text-sm text-[var(--color-text-dim)]">
            Click <strong>Open Shell</strong> to open a shell into{' '}
            <code>{pod}/{container}</code>. The session is not recorded.
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

      {(phase === 'connecting' || phase === 'ready') && (
        <div className="space-y-2">
          <div data-testid="exec-panel-banner" className="rounded bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
            {phase === 'connecting' ? 'Connecting…' : 'Connected'} — direct shell, not recorded.
          </div>
          <div
            data-testid="exec-panel-terminal"
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
