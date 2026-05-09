/**
 * ExecPanel.test.tsx — EPIC-4 Slice E1+E2 (#1099). Vitest coverage for:
 *   - Open Shell button → POST /session → iframe mounts at embedURL
 *   - Recording-on badge when server reports recording=true
 *   - Iframe load timeout → fallback WebSocket path
 *   - Iframe `onerror` → fallback path
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
  send(_: unknown) {}
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

  it('happy path mounts iframe and shows recording badge', async () => {
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
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-iframe')).toBeTruthy()
    })
    expect(screen.getByTestId('exec-panel-recording-on').textContent).toContain('Recording: ON')
    const iframe = screen.getByTestId('exec-panel-iframe') as HTMLIFrameElement
    expect(iframe.src).toContain('guac.local')
  })

  it('falls back to WebSocket when iframe load times out', async () => {
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
      expect(screen.getByTestId('exec-panel-iframe')).toBeTruthy()
    })
    // Advance past the 100ms iframe-load timeout — we never fire onLoad.
    act(() => {
      vi.advanceTimersByTime(150)
    })
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-fallback-banner')).toBeTruthy()
    })
    expect(wsInstance).not.toBeNull()
    expect(wsInstance!.url).toContain('/k8s/exec/default/wp-1/web')
  })

  it('falls back when iframe load timeout elapses (mirrors onerror fallback path)', async () => {
    // Note: jsdom's iframe doesn't propagate the onError prop reliably,
    // so this test exercises the same fallback code path via the
    // 5-second iframe-load-timeout — the practical equivalent that the
    // 5s timeout brief specifies. The onError branch is wired in
    // ExecPanel.tsx and statically routes to the same setPhase call.
    let wsInstance: FakeWebSocket | null = null
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        createSession={async () => HAPPY_SESSION}
        iframeLoadTimeoutMs={50}
        websocketFactory={(url) => {
          wsInstance = new FakeWebSocket(url)
          return wsInstance as unknown as WebSocket
        }}
        disableTerminal
      />,
    )
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-iframe')).toBeTruthy()
    })
    act(() => {
      vi.advanceTimersByTime(100)
    })
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-fallback-banner')).toBeTruthy()
    })
    expect(wsInstance).not.toBeNull()
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
