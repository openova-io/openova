/**
 * LogViewer.test.tsx — EPIC-4 Slice X2 (#1099). Vitest coverage for:
 *   - WebSocket URL construction (with/without `since`)
 *   - Container picker rendering when Pod has multiple containers
 *   - Status badge transitions on open / message / close
 *   - Reconnect-with-since on disconnect
 */

import { describe, it, expect, afterEach, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, fireEvent, act } from '@testing-library/react'

import { LogViewer } from './LogViewer'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

afterEach(() => cleanup())
beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
})
afterEach(() => {
  vi.useRealTimers()
})

// FakeWebSocket is the controllable double. Tests drive its lifecycle
// via .triggerOpen / .triggerMessage / .triggerClose.
class FakeWebSocket {
  static lastUrl: string | null = null
  static instances: FakeWebSocket[] = []

  url: string
  binaryType = 'arraybuffer'
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: ArrayBuffer | string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((ev: { code: number }) => void) | null = null
  closed = false
  closeCode = 0

  constructor(url: string) {
    this.url = url
    FakeWebSocket.lastUrl = url
    FakeWebSocket.instances.push(this)
  }

  close(code?: number) {
    this.closed = true
    this.closeCode = code ?? 1000
    this.onclose?.({ code: this.closeCode })
  }

  triggerOpen() {
    this.onopen?.()
  }
  triggerMessage(data: string | ArrayBuffer) {
    this.onmessage?.({ data })
  }
  triggerClose(code = 1011) {
    this.closeCode = code
    this.onclose?.({ code })
  }
}

const podMulti: K8sObject = {
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: { name: 'wp-1', namespace: 'default' },
  spec: { containers: [{ name: 'web' }, { name: 'sidecar' }] },
} as unknown as K8sObject

const podSingle: K8sObject = {
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: { name: 'wp-1', namespace: 'default' },
  spec: { containers: [{ name: 'web' }] },
} as unknown as K8sObject

describe('LogViewer', () => {
  beforeEach(() => {
    FakeWebSocket.instances = []
    FakeWebSocket.lastUrl = null
  })

  it('renders status pill connecting → streaming on open', () => {
    let ws: FakeWebSocket | null = null
    const factory = (url: string) => {
      ws = new FakeWebSocket(url)
      return ws as unknown as WebSocket
    }
    render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podSingle}
        websocketFactory={factory}
        disableTerminal
      />,
    )
    expect(screen.getByTestId('log-viewer-status').textContent).toContain('connecting')
    act(() => {
      ws?.triggerOpen()
    })
    expect(screen.getByTestId('log-viewer-status').textContent).toContain('streaming')
  })

  it('builds WebSocket URL with the active container', () => {
    const factory = (url: string) => new FakeWebSocket(url) as unknown as WebSocket
    render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podMulti}
        initialContainer="sidecar"
        websocketFactory={factory}
        disableTerminal
      />,
    )
    expect(FakeWebSocket.lastUrl).toContain('/k8s/logs/default/wp-1/sidecar')
    expect(FakeWebSocket.lastUrl).toContain('follow=true')
    expect(FakeWebSocket.lastUrl).toContain('tailLines=100')
  })

  it('renders container picker when Pod has multiple containers', () => {
    const factory = (url: string) => new FakeWebSocket(url) as unknown as WebSocket
    render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podMulti}
        websocketFactory={factory}
        disableTerminal
      />,
    )
    expect(screen.getByTestId('log-viewer-container')).toBeTruthy()
    const select = screen.getByTestId('log-viewer-container') as HTMLSelectElement
    expect(select.options.length).toBe(2)
  })

  it('does not render picker when Pod has only one container', () => {
    const factory = (url: string) => new FakeWebSocket(url) as unknown as WebSocket
    render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podSingle}
        websocketFactory={factory}
        disableTerminal
      />,
    )
    expect(screen.queryByTestId('log-viewer-container')).toBeNull()
  })

  it('toggles search box on click', () => {
    const factory = (url: string) => new FakeWebSocket(url) as unknown as WebSocket
    render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podSingle}
        websocketFactory={factory}
        disableTerminal
      />,
    )
    expect(screen.queryByTestId('log-viewer-search-input')).toBeNull()
    fireEvent.click(screen.getByTestId('log-viewer-search-toggle'))
    expect(screen.getByTestId('log-viewer-search-input')).toBeTruthy()
  })

  it('reconnects on close-with-error and adds since parameter', () => {
    const factory = (url: string) => new FakeWebSocket(url) as unknown as WebSocket
    render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podSingle}
        websocketFactory={factory}
        disableTerminal
      />,
    )
    expect(FakeWebSocket.instances.length).toBe(1)
    const first = FakeWebSocket.instances[0]
    act(() => {
      first.triggerOpen()
      // Push a message so lastTimestampRef is populated.
      first.triggerMessage('hello\n')
    })
    act(() => {
      first.triggerClose(1011)
    })
    // Reconnect timer fires after backoff.
    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(FakeWebSocket.instances.length).toBeGreaterThanOrEqual(2)
    const second = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
    expect(second.url).toContain('since=')
  })

  it('closes WS on unmount', () => {
    const factory = (url: string) => new FakeWebSocket(url) as unknown as WebSocket
    const { unmount } = render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podSingle}
        websocketFactory={factory}
        disableTerminal
      />,
    )
    const ws = FakeWebSocket.instances[0]
    unmount()
    expect(ws.closed).toBe(true)
  })

  it('does not reconnect on clean close (1000)', () => {
    const factory = (url: string) => new FakeWebSocket(url) as unknown as WebSocket
    render(
      <LogViewer
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        obj={podSingle}
        websocketFactory={factory}
        disableTerminal
      />,
    )
    const first = FakeWebSocket.instances[0]
    act(() => {
      first.triggerOpen()
      first.triggerClose(1000)
    })
    act(() => {
      vi.advanceTimersByTime(10000)
    })
    expect(FakeWebSocket.instances.length).toBe(1)
  })
})
