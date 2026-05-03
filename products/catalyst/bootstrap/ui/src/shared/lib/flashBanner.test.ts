/**
 * flashBanner.test.ts — round-trip + clear-on-read coverage for the
 * sessionStorage-backed banner seam (issue #689).
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { setProvisionFlashBanner, consumeProvisionFlashBanner } from './flashBanner'

beforeEach(() => {
  sessionStorage.clear()
})

describe('flashBanner', () => {
  it('returns null when no banner is queued', () => {
    expect(consumeProvisionFlashBanner()).toBeNull()
  })

  it('round-trips a single message', () => {
    setProvisionFlashBanner('Sign in to view your deployments')
    expect(consumeProvisionFlashBanner()).toBe('Sign in to view your deployments')
  })

  it('clears the banner on read so a second consume returns null', () => {
    setProvisionFlashBanner('hello')
    consumeProvisionFlashBanner() // first read
    expect(consumeProvisionFlashBanner()).toBeNull()
  })

  it('overwrites a previous unread banner', () => {
    setProvisionFlashBanner('first')
    setProvisionFlashBanner('second')
    expect(consumeProvisionFlashBanner()).toBe('second')
  })
})
