/**
 * ExecPanel.test.tsx — EPIC-4 Slice E1+E2 (#1099).
 *
 * TRANSPORT NOTE (why this file was migrated — #5404)
 * ---------------------------------------------------
 * G85 #2632 (2026-06-01) INVERTED the default transport — see the comment
 * at ExecPanel.tsx:99-107. The Guacamole embed URL pointed at
 * `guacamole.<dep>.sovereign.local`, a cluster-internal host the operator's
 * browser cannot resolve, so 100% of "Open Shell" clicks fell through to the
 * WebSocket fallback after a visible 5s spinner. `openShell()` now sets
 * `phase` straight to `'fallback-loading'` (ExecPanel.tsx:107) and the
 * iframe renders only in the `iframe-loading` / `iframe-ready` phases
 * (ExecPanel.tsx:289). The old iframe-first assertions were therefore
 * asserting a path the shipped code no longer takes.
 *
 * Vitest coverage for the shipped contract:
 *   - Open Shell button → POST /session → WS+xterm mounts immediately
 *   - No Guacamole iframe and no "Recording: ON" claim on the WS path
 *   - No iframe-load timeout to wait out (the #2632 regression guard)
 *   - WebSocket construction failure → visible error + retry
 *   - Disabled button when canExec=false (per RBAC contract)
 *   - Server error surface
 */

import { describe, it, expect, afterEach, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, fireEvent, act, waitFor } from '@testing-library/react'

import { ExecPanel } from './ExecPanel'
import type { ExecSessionResponse } from '@/pages/sovereign/cloud-list/resource.api'

afterEach(() => cleanup())
beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
})
afterEach(() => {
  vi.useRealTimers()
})

const HAPPY_SESSION: ExecSessionResponse = {
  sessionId: 'sess-1',
  connectionId: 'conn-1',
  embedURL: 'https://guac.local/#/client/conn-1',
  namespace: 'default',
  pod: 'wp-1',
  container: 'web',
  fallbackWebSocketUrl: 'ws://localhost/api/v1/sovereigns/dep/k8s/exec/default/wp-1/web?command=%2Fbin%2Fsh',
  recording: true,
  issued: '2026-05-09T12:00:00Z',
}

class FakeWebSocket {
  url: string
  binaryType = 'arraybuffer'
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: ArrayBuffer | string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((ev: { code: number }) => void) | null = null
  constructor(url: string) {
    this.url = url
  }
  // Stub — the panel only calls send() from the xterm stdin wiring, which
  // is off in these tests (disableTerminal). Takes no params so the
  // no-unused-vars rule stays green; the double-cast at the call site keeps
  // it assignable to WebSocket.
  send() {}
  close() {
    this.onclose?.({ code: 1000 })
  }
}

describe('ExecPanel', () => {
  it('renders Open Shell idle state', () => {
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        createSession={async () => HAPPY_SESSION}
        disableTerminal
      />,
    )
    expect(screen.getByTestId('exec-panel-open')).toBeTruthy()
  })

  it('disables button when canExec is false', () => {
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        canExec={false}
        createSession={async () => HAPPY_SESSION}
        disableTerminal
      />,
    )
    const btn = screen.getByTestId('exec-panel-open') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it('happy path opens the WebSocket shell directly — no Guacamole iframe (G85 #2632)', async () => {
    let wsInstance: FakeWebSocket | null = null
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        createSession={async () => HAPPY_SESSION}
        websocketFactory={(url) => {
          wsInstance = new FakeWebSocket(url)
          return wsInstance as unknown as WebSocket
        }}
        disableTerminal
      />,
    )
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-fallback-terminal')).toBeTruthy()
    })
    // The WS is dialled against the server-supplied fallback URL
    // (ExecPanel.tsx:131-140).
    expect(wsInstance).not.toBeNull()
    expect(wsInstance!.url).toBe(HAPPY_SESSION.fallbackWebSocketUrl)
    // …and the Guacamole iframe never mounts: ExecPanel.tsx:289 gates it on
    // the `iframe-*` phases and openShell no longer enters them
    // (ExecPanel.tsx:107).
    expect(screen.queryByTestId('exec-panel-iframe')).toBeNull()
    // The server reported recording:true, but a direct WS exec is NOT
    // recorded. The badge stays inside the iframe branch
    // (ExecPanel.tsx:291-295) and the WS banner states the opposite — the
    // UI must never claim a recording it isn't making.
    expect(screen.queryByTestId('exec-panel-recording-on')).toBeNull()
    expect(screen.getByTestId('exec-panel-fallback-banner').textContent).toContain(
      'recording disabled',
    )
  })

  it('reaches a usable shell without waiting out any iframe-load timeout', async () => {
    // Regression guard for the exact bug G85 #2632 fixed (ExecPanel.tsx:99-107):
    // every click used to burn a visible 5s spinner before falling through.
    // Nothing here advances timers before the assertions.
    let wsInstance: FakeWebSocket | null = null
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        createSession={async () => HAPPY_SESSION}
        iframeLoadTimeoutMs={100}
        websocketFactory={(url) => {
          wsInstance = new FakeWebSocket(url)
          return wsInstance as unknown as WebSocket
        }}
        disableTerminal
      />,
    )
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-fallback-banner')).toBeTruthy()
    })
    expect(wsInstance).not.toBeNull()
    expect(wsInstance!.url).toContain('/k8s/exec/default/wp-1/web')
    // Draining well past the iframe-load timeout must not disturb the live
    // shell — that timer is only armed in the `iframe-loading` phase
    // (ExecPanel.tsx:115-127), which this path never enters.
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(screen.getByTestId('exec-panel-fallback-terminal')).toBeTruthy()
    expect(screen.queryByTestId('exec-panel-iframe')).toBeNull()
  })

  it('surfaces a WebSocket dial failure instead of hanging on a blank terminal', async () => {
    // Replaces the old iframe-onerror coverage: the transport that can fail
    // on the default path is now the WebSocket, and ExecPanel.tsx:139-145
    // is the branch that has to degrade visibly. The iframe onError handler
    // (ExecPanel.tsx:236-242) is no longer reachable — see the file
    // docblock — so its coverage moves here.
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        createSession={async () => HAPPY_SESSION}
        websocketFactory={() => {
          throw new Error('SecurityError: ws dial blocked')
        }}
        disableTerminal
      />,
    )
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-error').textContent).toContain(
        'SecurityError: ws dial blocked',
      )
    })
    expect(screen.queryByTestId('exec-panel-fallback-terminal')).toBeNull()
    expect(screen.getByTestId('exec-panel-error').textContent).toContain('Retry')
  })

  it('surfaces server error and offers retry', async () => {
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        createSession={async () => {
          throw new Error('HTTP 502: guacamole-create-failed')
        }}
        disableTerminal
      />,
    )
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-error').textContent).toContain('502')
    })
  })
})
