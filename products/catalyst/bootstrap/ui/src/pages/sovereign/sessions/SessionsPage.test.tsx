/**
 * SessionsPage.test.tsx — EPIC-4 Slice E3 (#1099). Vitest coverage for:
 *   - List render + table rows
 *   - Empty state
 *   - Filter form posts new search
 *   - Replay button gated by canReplay
 *   - Replay click → modal opens with iframe
 *   - Replay error surface
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

import { SessionsPage } from './SessionsPage'
import type {
  SessionListResponse,
  SessionReplayResponse,
} from '@/pages/sovereign/cloud-list/resource.api'

afterEach(() => cleanup())

const SEED: SessionListResponse = {
  items: [
    {
      sessionId: 's-1',
      namespace: 'default',
      pod: 'wp-1',
      container: 'web',
      user: 'alice@example.com',
      started: '2026-05-09T12:00:00Z',
      durationSeconds: 90,
      recordingAvailable: true,
    },
    {
      sessionId: 's-2',
      namespace: 'default',
      pod: 'api-2',
      container: 'main',
      user: 'bob@example.com',
      started: '2026-05-09T11:00:00Z',
      durationSeconds: 30,
      recordingAvailable: false,
    },
  ],
  total: 2,
  page: 1,
  pageSize: 25,
}

describe('SessionsPage', () => {
  it('renders list with rows', async () => {
    render(
      <SessionsPage
        deploymentId="dep"
        canReplay
        listFn={async () => SEED}
        replayFn={async () => ({} as SessionReplayResponse)}
      />,
    )
    await waitFor(() => {
      expect(screen.getByTestId('sessions-table')).toBeTruthy()
    })
    expect(screen.getByTestId('sessions-row-s-1')).toBeTruthy()
    expect(screen.getByTestId('sessions-row-s-2')).toBeTruthy()
  })

  it('renders empty state when no sessions', async () => {
    render(
      <SessionsPage
        deploymentId="dep"
        listFn={async () => ({ items: [], total: 0, page: 1, pageSize: 25 })}
        replayFn={async () => ({} as SessionReplayResponse)}
      />,
    )
    await waitFor(() => {
      expect(screen.getByTestId('sessions-empty')).toBeTruthy()
    })
  })

  it('hides replay button when canReplay is false', async () => {
    render(
      <SessionsPage
        deploymentId="dep"
        canReplay={false}
        listFn={async () => SEED}
        replayFn={async () => ({} as SessionReplayResponse)}
      />,
    )
    await waitFor(() => {
      expect(screen.getByTestId('sessions-table')).toBeTruthy()
    })
    expect(screen.queryByTestId('sessions-row-replay-s-1')).toBeNull()
    expect(screen.getByTestId('sessions-row-replay-locked-s-1')).toBeTruthy()
  })

  it('opens modal with iframe on replay click', async () => {
    const replayFn = vi.fn(async (_dep: string, sessionId: string) => ({
      sessionId,
      embedURL: `https://guac.local/#/replay/${sessionId}`,
      available: true,
    }))
    render(
      <SessionsPage
        deploymentId="dep"
        canReplay
        listFn={async () => SEED}
        replayFn={replayFn}
      />,
    )
    await waitFor(() => {
      expect(screen.getByTestId('sessions-row-s-1')).toBeTruthy()
    })
    fireEvent.click(screen.getByTestId('sessions-row-replay-s-1'))
    await waitFor(() => {
      expect(screen.getByTestId('sessions-replay-modal')).toBeTruthy()
    })
    expect(screen.getByTestId('sessions-replay-iframe')).toBeTruthy()
    expect(replayFn).toHaveBeenCalledWith('dep', 's-1')
  })

  it('disables replay button on rows without recording', async () => {
    render(
      <SessionsPage
        deploymentId="dep"
        canReplay
        listFn={async () => SEED}
        replayFn={async () => ({} as SessionReplayResponse)}
      />,
    )
    await waitFor(() => {
      expect(screen.getByTestId('sessions-row-replay-s-2')).toBeTruthy()
    })
    const btn = screen.getByTestId('sessions-row-replay-s-2') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it('filter form re-fetches with pod/user', async () => {
    const listFn = vi.fn(async () => SEED)
    render(
      <SessionsPage
        deploymentId="dep"
        canReplay
        listFn={listFn}
        replayFn={async () => ({} as SessionReplayResponse)}
      />,
    )
    await waitFor(() => {
      expect(listFn).toHaveBeenCalledTimes(1)
    })
    fireEvent.change(screen.getByTestId('sessions-filter-pod'), { target: { value: 'wp-1' } })
    fireEvent.click(screen.getByTestId('sessions-filter-apply'))
    await waitFor(() => {
      expect(listFn).toHaveBeenCalledTimes(2)
    })
    const lastCall = listFn.mock.calls[listFn.mock.calls.length - 1] as unknown as [string, { pod?: string; user?: string; page?: number }]
    expect(lastCall[1]).toMatchObject({ pod: 'wp-1', page: 1 })
  })

  it('shows error banner on list failure', async () => {
    render(
      <SessionsPage
        deploymentId="dep"
        listFn={async () => {
          throw new Error('HTTP 403: forbidden')
        }}
        replayFn={async () => ({} as SessionReplayResponse)}
      />,
    )
    await waitFor(() => {
      expect(screen.getByTestId('sessions-error').textContent).toContain('403')
    })
  })

  it('replay error surfaces to UI', async () => {
    render(
      <SessionsPage
        deploymentId="dep"
        canReplay
        listFn={async () => SEED}
        replayFn={async () => {
          throw new Error('HTTP 403: replay forbidden')
        }}
      />,
    )
    await waitFor(() => {
      expect(screen.getByTestId('sessions-row-s-1')).toBeTruthy()
    })
    fireEvent.click(screen.getByTestId('sessions-row-replay-s-1'))
    await waitFor(() => {
      expect(screen.getByTestId('sessions-replay-error').textContent).toContain('403')
    })
  })
})
