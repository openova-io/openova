/**
 * ExecPanel.test.tsx — EPIC-4 Slice E2 (#1099).
 *
 * The panel opens the exec WebSocket of the pod directly: no request
 * creates a session first, and no Guacamole iframe exists.
 *
 * ORDERING NOTE (#5633)
 * ---------------------
 * The WebSocket is dialled in a passive effect that React flushes in a
 * scheduler task AFTER the one that commits the terminal markup. So no
 * `waitFor` on a DOM node alone can imply that the socket exists. Every wait
 * in this file that precedes a socket assertion names the socket in the wait
 * condition itself.
 *
 * Coverage:
 *   - Open Shell dials /k8s/exec/{ns}/{pod}/{container}?command=/bin/sh and
 *     sends no HTTP request
 *   - the socket stays open once it is open; unmount closes it
 *   - a dial failure and a socket error show an error with Retry; Retry dials
 *     again
 *   - a close of the open socket by the server shows its code and reason
 *     and Retry, and no longer says Connected
 *   - a change of the container after an attempt dials the new container
 *     and shows Connecting again
 *   - the button is disabled when canExec=false
 */

import { describe, it, expect, afterEach, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, fireEvent, act, waitFor } from '@testing-library/react'

import { ExecPanel } from './ExecPanel'

const EXEC_PATH = '/v1/sovereigns/dep/k8s/exec/default/wp-1/web?command=%2Fbin%2Fsh'

class FakeWebSocket {
  url: string
  binaryType = 'arraybuffer'
  closed = false
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: ArrayBuffer | string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((ev: { code: number; reason?: string }) => void) | null = null
  constructor(url: string) {
    this.url = url
  }
  // Stub — the panel only calls send() from the xterm stdin wiring, which
  // is off in these tests (disableTerminal).
  send() {}
  close() {
    this.closed = true
    this.onclose?.({ code: 1000 })
  }
}

let fetchSpy: ReturnType<typeof vi.fn>
beforeEach(() => {
  fetchSpy = vi.fn(() => Promise.reject(new Error('no HTTP request is expected')))
  vi.stubGlobal('fetch', fetchSpy)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function renderPanel(sockets: FakeWebSocket[], canExec = true) {
  return render(
    <ExecPanel
      deploymentId="dep"
      ns="default"
      pod="wp-1"
      container="web"
      canExec={canExec}
      websocketFactory={(url) => {
        const ws = new FakeWebSocket(url)
        sockets.push(ws)
        return ws as unknown as WebSocket
      }}
      disableTerminal
    />,
  )
}

describe('ExecPanel', () => {
  it('renders Open Shell idle state that claims no recording', () => {
    renderPanel([])
    expect(screen.getByTestId('exec-panel-open')).toBeTruthy()
    expect(screen.getByTestId('exec-panel').textContent).toContain('The session is not recorded.')
    expect(screen.getByTestId('exec-panel').textContent).not.toContain('Guacamole')
  })

  it('disables button when canExec is false', () => {
    renderPanel([], false)
    const btn = screen.getByTestId('exec-panel-open') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it('Open Shell dials the exec WebSocket directly and sends no HTTP request', async () => {
    const sockets: FakeWebSocket[] = []
    renderPanel(sockets)
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-terminal')).toBeTruthy()
      expect(sockets.length).toBe(1)
    })
    const url = new URL(sockets[0].url)
    expect(['ws:', 'wss:']).toContain(url.protocol)
    expect(url.pathname + url.search).toBe('/api' + EXEC_PATH)
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(screen.queryByTestId('exec-panel-iframe')).toBeNull()
  })

  it('keeps the socket open once it is open, and closes it on unmount', async () => {
    const sockets: FakeWebSocket[] = []
    const view = renderPanel(sockets)
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => expect(sockets.length).toBe(1))
    expect(screen.getByTestId('exec-panel').getAttribute('data-phase')).toBe('connecting')
    act(() => sockets[0].onopen?.())
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel').getAttribute('data-phase')).toBe('ready')
    })
    expect(screen.getByTestId('exec-panel-banner').textContent).toBe('Connected — direct shell, not recorded.')
    expect(sockets[0].closed).toBe(false)
    expect(sockets.length).toBe(1)
    view.unmount()
    expect(sockets[0].closed).toBe(true)
  })

  it('surfaces a WebSocket dial failure and Retry dials again', async () => {
    let dials = 0
    render(
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container="web"
        websocketFactory={() => {
          dials++
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
    expect(dials).toBe(1)
    expect(screen.queryByTestId('exec-panel-terminal')).toBeNull()
    fireEvent.click(screen.getByText('Retry'))
    await waitFor(() => expect(dials).toBe(2))
  })

  it('shows an error when the open socket fails', async () => {
    const sockets: FakeWebSocket[] = []
    renderPanel(sockets)
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => expect(sockets.length).toBe(1))
    act(() => sockets[0].onopen?.())
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel').getAttribute('data-phase')).toBe('ready')
    })
    act(() => sockets[0].onerror?.())
    // A browser follows the error event with an abnormal close.
    act(() => sockets[0].onclose?.({ code: 1006, reason: '' }))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-error').textContent).toContain('WebSocket error')
    })
    expect(screen.queryByTestId('exec-panel-terminal')).toBeNull()
  })

  it('shows the close code and reason and Retry when the server closes the open socket', async () => {
    const sockets: FakeWebSocket[] = []
    renderPanel(sockets)
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => expect(sockets.length).toBe(1))
    act(() => sockets[0].onopen?.())
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-banner').textContent).toBe('Connected — direct shell, not recorded.')
    })
    act(() => sockets[0].onclose?.({ code: 1011, reason: 'container not found' }))
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel-error').textContent).toContain(
        'the shell closed: 1011 container not found',
      )
    })
    expect(screen.getByTestId('exec-panel').textContent).not.toContain('Connected')
    expect(screen.queryByTestId('exec-panel-terminal')).toBeNull()
    fireEvent.click(screen.getByText('Retry'))
    await waitFor(() => expect(sockets.length).toBe(2))
  })

  it('dials the new container and shows Connecting when the container changes after an attempt', async () => {
    const sockets: FakeWebSocket[] = []
    const factory = (url: string) => {
      const ws = new FakeWebSocket(url)
      sockets.push(ws)
      return ws as unknown as WebSocket
    }
    const panel = (container: string) => (
      <ExecPanel
        deploymentId="dep"
        ns="default"
        pod="wp-1"
        container={container}
        websocketFactory={factory}
        disableTerminal
      />
    )
    const view = render(panel('main'))
    fireEvent.click(screen.getByTestId('exec-panel-open'))
    await waitFor(() => expect(sockets.length).toBe(1))
    act(() => sockets[0].onopen?.())
    await waitFor(() => {
      expect(screen.getByTestId('exec-panel').getAttribute('data-phase')).toBe('ready')
    })
    view.rerender(panel('web'))
    await waitFor(() => expect(sockets.length).toBe(2))
    expect(sockets[0].closed).toBe(true)
    const url = new URL(sockets[1].url)
    expect(url.pathname + url.search).toBe('/api' + EXEC_PATH)
    expect(screen.getByTestId('exec-panel').getAttribute('data-phase')).toBe('connecting')
    expect(screen.getByTestId('exec-panel-banner').textContent).toBe('Connecting… — direct shell, not recorded.')
  })
})
