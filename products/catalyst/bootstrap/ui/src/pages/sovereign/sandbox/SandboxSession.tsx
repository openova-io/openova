/**
 * SandboxSession — native /sandbox/$id session view.
 *
 * Wave 3 UI scaffold. Hosts the xterm.js terminal that pipes the
 * agent CLI's ANSI stdout from the in-pod pty-server (see
 * `products/sandbox/docs/architecture.md` §1).
 *
 * Wire path (Wave 2):
 *   browser xterm.js ↔ WSS /api/v1/sandbox/sessions/{id}/attach
 *                              ↔ pty-server in the Sandbox pod
 *                              ↔ <agent> CLI in the same pod
 *
 * This PR ships the xterm.js HOST surface only — the WebSocket adapter
 * lands in Wave 2 when the pty-server endpoint is wired. The placeholder
 * banner makes the wave gap visible to the operator (per
 * INVIOLABLE-PRINCIPLES.md #1 — waterfall, first paint is the target-
 * state shape with the "API pending" pill where the backend isn't ready).
 *
 * xterm + @xterm/addon-fit are declared in package.json (already present
 * for the FlowCanvas tracer); the import only fires when this route is
 * navigated to so the bundle stays out of the Landing / Settings path.
 *
 * Per the design-system inheritance ruling, the chrome is PortalShell
 * (same header band as JobsPage / SettingsPage) with a SectionCard-style
 * surface around the terminal — no bespoke layout, no hex colours.
 */

import { useEffect, useRef } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { Terminal } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import 'xterm/css/xterm.css'

import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'

export interface SandboxSessionProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — disables the xterm.js mount so jsdom tests don't crash
   *  on canvas / measureText. Production call sites never set this. */
  disableTerminal?: boolean
}

export function SandboxSession({
  disableStream = false,
  disableTerminal = false,
}: SandboxSessionProps = {}) {
  const params = useParams({ strict: false }) as { id?: string }
  const sessionId = params.id ?? ''

  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const hostRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)

  useEffect(() => {
    if (disableTerminal) return
    const host = hostRef.current
    if (!host) return

    // Theme uses our design tokens — read live so a future tenant theme
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
      // jsdom / no-layout — ignore, the WebSocket attach in Wave 2 will
      // SIGWINCH again on first resize event.
    }
    termRef.current = term
    fitRef.current = fit

    // Placeholder banner — the operator sees the terminal chrome on
    // first paint with a clear hint that the WebSocket pipe lands in
    // Wave 2. The bytes are ANSI dim so they don't masquerade as agent
    // output; pressing keys is a no-op until the socket attaches.
    term.write(
      '\x1b[2m# Sandbox session ' +
        sessionId +
        '\r\n# xterm.js host ready. WebSocket attach lands in Wave 2.\r\n# pty-server URL: /api/v1/sandbox/sessions/' +
        sessionId +
        '/attach\x1b[0m\r\n',
    )

    function onResize() {
      try {
        fit.fit()
      } catch {
        // ignore
      }
    }
    window.addEventListener('resize', onResize)

    return () => {
      window.removeEventListener('resize', onResize)
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [sessionId, disableTerminal])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle={`Sandbox · ${sessionId || 'session'}`}
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
        <section
          aria-label="Sandbox terminal"
          data-testid="sandbox-session-card"
          data-pending-api="true"
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
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
            <span
              data-testid="sandbox-session-pending-api"
              className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-300"
              title="WebSocket attach lands in Wave 2"
            >
              API pending
            </span>
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
