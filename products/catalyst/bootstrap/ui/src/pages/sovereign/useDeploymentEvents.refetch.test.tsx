/**
 * useDeploymentEvents.refetch.test.tsx — issue #782 lock-in.
 *
 * Ground truth:
 *   • An SSE stream closing without a terminal `event: done` is a
 *     transport drop, NOT a deployment failure.
 *   • Before flipping the UI to "Provisioning failed", the hook MUST
 *     re-fetch /deployments/{id} once and switch on the canonical
 *     status.
 *   • status="ready"           → streamStatus='completed', handoverReady set
 *   • status="failed"          → streamStatus='failed' with REAL error
 *   • status in IN_FLIGHT_*    → streamStatus='streaming' (spinner +
 *                                reconnect SSE), NEVER 'failed' with
 *                                "Deployment ended with status=phase1-watching"
 *
 * jsdom doesn't implement EventSource — we install a minimal fake that
 * captures every constructed instance + supports addEventListener for
 * the typed `done` + `handover-ready` channels.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { useDeploymentEvents } from './useDeploymentEvents'

interface ListenerMap {
  done: Array<(e: MessageEvent) => void>
  'handover-ready': Array<(e: MessageEvent) => void>
}

class FakeEventSource {
  static CLOSED = 2 as const
  static CONNECTING = 0 as const
  static OPEN = 1 as const
  static instances: FakeEventSource[] = []

  url: string
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState: number = FakeEventSource.CONNECTING
  listeners: ListenerMap = { done: [], 'handover-ready': [] }

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }
  addEventListener(event: string, handler: (e: MessageEvent) => void) {
    if (event === 'done' || event === 'handover-ready') {
      this.listeners[event].push(handler)
    }
  }
  removeEventListener(event: string, handler: (e: MessageEvent) => void) {
    if (event === 'done' || event === 'handover-ready') {
      this.listeners[event] = this.listeners[event].filter((h) => h !== handler)
    }
  }
  close() {
    this.readyState = FakeEventSource.CLOSED
  }
  /** Test helper — fire the connection-closed signal from the network. */
  simulateClose() {
    this.readyState = FakeEventSource.CLOSED
    this.onerror?.(new Event('error'))
  }
}

function lastES(): FakeEventSource {
  const inst = FakeEventSource.instances.at(-1)
  if (!inst) throw new Error('no EventSource constructed')
  return inst
}

function makeFetchResponses(...responses: Array<{ ok: boolean; status?: number; body: unknown }>) {
  let i = 0
  return ((..._args: unknown[]) => {
    const r = responses[Math.min(i, responses.length - 1)]!
    i += 1
    return Promise.resolve({
      ok: r.ok,
      status: r.status ?? (r.ok ? 200 : 500),
      json: () => Promise.resolve(r.body),
    } as unknown as Response)
  }) as typeof fetch
}

const APPS = ['cilium', 'cert-manager'] as const

beforeEach(() => {
  FakeEventSource.instances = []
  // @ts-expect-error — install on global
  globalThis.EventSource = FakeEventSource
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  // @ts-expect-error — clean up
  delete globalThis.EventSource
})

/** Drain pending microtasks (fetch.then chains) under real timers. */
async function flush() {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve()
  }
}

describe('useDeploymentEvents — SSE drop re-fetches /deployments/{id} (issue #782)', () => {
  it('renders SUCCESS when SSE drops mid-flight AND poll returns status=ready', async () => {
    // First fetch: history-replay (no events, not done — in-flight start).
    // Second fetch: triggered by the SSE drop, returns ready+handover.
    globalThis.fetch = makeFetchResponses(
      { ok: true, body: { events: [], state: undefined, done: false } },
      {
        ok: true,
        body: {
          id: 'd-ready',
          status: 'ready',
          sovereignFQDN: 'otech.example.com',
          handoverURL: 'https://console.otech.example.com/auth/handover?token=AAA',
          handoverFiredAt: '2026-05-04T16:17:09Z',
          phase1Outcome: 'ready',
          componentStates: { cilium: 'installed', 'cert-manager': 'installed' },
        },
      },
    )

    const { result } = renderHook(() =>
      useDeploymentEvents({ deploymentId: 'd-ready', applicationIds: APPS }),
    )

    // Open + drop the SSE — emulates a reverse-proxy idle timeout
    // dropping the stream after the catalyst-api has already auto-fired
    // the handover JWT mint. The hook MUST poll /deployments/{id}
    // BEFORE rendering "Provisioning failed".
    await act(async () => {
      await flush()
      lastES().onopen?.(new Event('open'))
      lastES().simulateClose()
      await flush()
    })

    await waitFor(() => {
      expect(result.current.streamStatus).toBe('completed')
    })
    expect(result.current.streamError).toBeNull()
    expect(result.current.handoverReady?.handoverURL).toBe(
      'https://console.otech.example.com/auth/handover?token=AAA',
    )
    expect(result.current.snapshot?.status).toBe('ready')
  })

  it('renders SPINNER (status=streaming) when SSE drops mid-flight AND poll returns status=phase1-watching', async () => {
    globalThis.fetch = makeFetchResponses(
      { ok: true, body: { events: [], state: undefined, done: false } },
      {
        ok: true,
        body: {
          id: 'd-watching',
          status: 'phase1-watching',
          sovereignFQDN: 'otech.example.com',
          phase1Outcome: '',
        },
      },
    )

    const { result } = renderHook(() =>
      useDeploymentEvents({ deploymentId: 'd-watching', applicationIds: APPS }),
    )

    await act(async () => {
      await flush()
      lastES().onopen?.(new Event('open'))
      lastES().simulateClose()
      await flush()
    })

    // CRITICAL: must NEVER be 'failed' with the stale phase id message.
    expect(result.current.streamStatus).not.toBe('failed')
    expect(String(result.current.streamError ?? '')).not.toContain('phase1-watching')
    expect(String(result.current.streamError ?? '')).not.toContain(
      'Deployment ended with status=',
    )
    // The UI stays in the spinner state — either 'streaming' (after
    // reconnect kicks off) or 'connecting'. Both render the spinner.
    expect(['streaming', 'connecting']).toContain(result.current.streamStatus)
  })

  it('reconnects the SSE stream when the canonical state is still in-flight', async () => {
    globalThis.fetch = makeFetchResponses(
      { ok: true, body: { events: [], state: undefined, done: false } },
      // Same poll response for any number of subsequent polls.
      {
        ok: true,
        body: { id: 'd-reconnect', status: 'phase1-watching' },
      },
    )

    renderHook(() =>
      useDeploymentEvents({ deploymentId: 'd-reconnect', applicationIds: APPS }),
    )

    await act(async () => {
      await flush()
      lastES().onopen?.(new Event('open'))
      lastES().simulateClose()
      await flush()
    })
    // First-attempt reconnect backoff is 1000ms — wait it out under
    // real timers. We don't use fake timers here because the fetch
    // promise chain mixes poorly with vi.advanceTimersByTimeAsync().
    await act(async () => {
      await new Promise((r) => setTimeout(r, 1500))
    })

    // A second EventSource MUST have been constructed against the same
    // /logs URL — the proof the hook reconnects rather than terminating.
    expect(FakeEventSource.instances.length).toBeGreaterThanOrEqual(2)
    expect(FakeEventSource.instances[1]!.url).toBe(FakeEventSource.instances[0]!.url)
  })

  it('renders FAILURE with the REAL error message when SSE drops AND poll returns status=failed', async () => {
    const realError = 'tofu apply: Server returned 502'
    globalThis.fetch = makeFetchResponses(
      { ok: true, body: { events: [], state: undefined, done: false } },
      {
        ok: true,
        body: {
          id: 'd-failed',
          status: 'failed',
          sovereignFQDN: 'otech.example.com',
          error: realError,
          phase1Outcome: '',
        },
      },
    )

    const { result } = renderHook(() =>
      useDeploymentEvents({ deploymentId: 'd-failed', applicationIds: APPS }),
    )

    await act(async () => {
      await flush()
      lastES().onopen?.(new Event('open'))
      lastES().simulateClose()
      await flush()
    })

    await waitFor(() => {
      expect(result.current.streamStatus).toBe('failed')
    })
    // The REAL error is surfaced — not the stale "Deployment ended with
    // status=phase1-watching" copy.
    expect(result.current.streamError).toBe(realError)
    expect(String(result.current.streamError ?? '')).not.toContain('phase1-watching')
    expect(String(result.current.streamError ?? '')).not.toContain(
      'Deployment ended with status=',
    )
  })

  it('never hard-codes "Deployment ended with status=phase1-watching" in the failure message', async () => {
    // The legacy code's stale-banner copy was:
    //   `Deployment ended with status=${snap.status}` → "Deployment
    //   ended with status=phase1-watching" — fired purely off the SSE
    //   close. This test pins the regression: the SSE drop alone MUST
    //   NEVER produce that copy, even if the canonical poll never
    //   resolves.
    globalThis.fetch = makeFetchResponses(
      { ok: true, body: { events: [], state: undefined, done: false } },
      // Network failure on the canonical poll → unreachable, but with
      // a clean message, never "ended with status=phase1-watching".
      { ok: false, status: 500, body: {} },
    )

    const { result } = renderHook(() =>
      useDeploymentEvents({ deploymentId: 'd-no-poll', applicationIds: APPS }),
    )

    await act(async () => {
      await flush()
      lastES().onopen?.(new Event('open'))
      lastES().simulateClose()
      await flush()
    })

    expect(String(result.current.streamError ?? '')).not.toContain('phase1-watching')
    expect(String(result.current.streamError ?? '')).not.toContain(
      'Deployment ended with status=',
    )
  })

  it('promotes to completed if the canonical poll surfaces a non-empty handoverURL even before status flips to ready', async () => {
    // catalyst-api stamps handoverURL on the deployment record AT the
    // same moment Phase-1 reaches OutcomeReady. There can be a tiny
    // window where the top-level status is still "phase1-watching" but
    // handoverURL is already populated — the founder's incident report
    // for #782 explicitly described this state. The hook must render
    // success in that case, not failure.
    globalThis.fetch = makeFetchResponses(
      { ok: true, body: { events: [], state: undefined, done: false } },
      {
        ok: true,
        body: {
          id: 'd-handover-early',
          status: 'phase1-watching',
          sovereignFQDN: 'otech.example.com',
          handoverURL: 'https://console.otech.example.com/auth/handover?token=BBB',
          handoverFiredAt: '2026-05-04T16:17:09Z',
          componentStates: { cilium: 'installed', 'cert-manager': 'installed' },
        },
      },
    )

    const { result } = renderHook(() =>
      useDeploymentEvents({ deploymentId: 'd-handover-early', applicationIds: APPS }),
    )

    await act(async () => {
      await flush()
      lastES().onopen?.(new Event('open'))
      lastES().simulateClose()
      await flush()
    })

    await waitFor(() => {
      expect(result.current.streamStatus).toBe('completed')
    })
    expect(result.current.handoverReady?.handoverURL).toBe(
      'https://console.otech.example.com/auth/handover?token=BBB',
    )
    expect(result.current.streamError).toBeNull()
  })
})
