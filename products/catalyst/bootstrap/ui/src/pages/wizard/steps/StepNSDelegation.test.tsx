/**
 * StepNSDelegation.test.tsx — coverage for the post-handover NS-delegation
 * step (issue #374). Spec lives in StepNSDelegation.tsx header.
 *
 * Test scopes:
 *   1. buildDynadotRunbookCommand — pure helper, exhaustive shape checks.
 *   2. Render — defaults render the runbook, auto-apply gated, no network
 *      call without explicit double-confirm.
 *   3. Auto-apply — only fires when toggle ON + confirm text matches.
 *   4. Stub-aware — 501 from the catalyst-api endpoint surfaces the
 *      "deferred to Phase 8" hint, not a generic error.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import {
  StepNSDelegation,
  buildDynadotRunbookCommand,
  parentZoneDelegateAPIPath,
  DEFAULT_PARENT_ZONE,
  DEFAULT_PARENT_REGISTRAR,
} from './StepNSDelegation'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

/* ── 1. Pure helper ─────────────────────────────────────────────────── */

describe('buildDynadotRunbookCommand', () => {
  const base = {
    parentZone: 'omani.works',
    sovereignFQDN: 'omantel.omani.works',
    loadBalancerIP: '203.0.113.42',
  }

  it('embeds the canonical Dynadot endpoint', () => {
    const cmd = buildDynadotRunbookCommand(base)
    expect(cmd).toContain('https://api.dynadot.com/api3.json')
  })

  it('uses set_dns2 + add_dns_to_current_setting=yes (record-preserving)', () => {
    const cmd = buildDynadotRunbookCommand(base)
    expect(cmd).toContain('command=set_dns2')
    expect(cmd).toContain('add_dns_to_current_setting=yes')
  })

  it('reads the API key from $DYNADOT_API_KEY (never embeds the secret)', () => {
    const cmd = buildDynadotRunbookCommand(base)
    expect(cmd).toContain('key=$DYNADOT_API_KEY')
    // Must not contain anything that looks like a real key.
    expect(cmd).not.toMatch(/key=[A-Za-z0-9]{20,}/)
  })

  it('strips the parent zone from the FQDN to form the sub-label', () => {
    const cmd = buildDynadotRunbookCommand(base)
    expect(cmd).toContain('subdomain0=omantel')
    expect(cmd).toContain('sub_record_type0=NS')
  })

  it('emits 3 NS records pointing at ns{1,2,3}.<fqdn>', () => {
    const cmd = buildDynadotRunbookCommand(base)
    expect(cmd).toContain('sub_record0=ns1.omantel.omani.works')
    expect(cmd).toContain('sub_record1=ns2.omantel.omani.works')
    expect(cmd).toContain('sub_record2=ns3.omantel.omani.works')
  })

  it('emits a glue A record for ns1.<sub> at the load-balancer IP', () => {
    const cmd = buildDynadotRunbookCommand(base)
    expect(cmd).toContain('subdomain3=ns1.omantel')
    expect(cmd).toContain('sub_record_type3=A')
    expect(cmd).toContain('sub_record3=203.0.113.42')
  })

  it('handles a multi-label parent zone (e.g. eu.omani.works)', () => {
    const cmd = buildDynadotRunbookCommand({
      parentZone: 'eu.omani.works',
      sovereignFQDN: 'frankfurt.eu.omani.works',
      loadBalancerIP: '198.51.100.7',
    })
    expect(cmd).toContain('subdomain0=frankfurt')
    expect(cmd).toContain('sub_record0=ns1.frankfurt.eu.omani.works')
  })

  it('falls back to the full FQDN when the FQDN is not under the parent zone', () => {
    // Mismatched input — helper stays pure (no throw); the UI shows the
    // mismatch warning separately.
    const cmd = buildDynadotRunbookCommand({
      parentZone: 'omani.works',
      sovereignFQDN: 'oops.example.com',
      loadBalancerIP: '203.0.113.1',
    })
    expect(cmd).toContain('subdomain0=oops.example.com')
  })
})

/* ── 2. Render defaults ─────────────────────────────────────────────── */

describe('StepNSDelegation — render', () => {
  it('renders the hero, the runbook, and the auto-apply gate', () => {
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    expect(screen.getByTestId('ns-delegation-hero')).toBeTruthy()
    expect(screen.getByTestId('ns-runbook')).toBeTruthy()
    expect(screen.getByTestId('ns-auto-apply')).toBeTruthy()
  })

  it('defaults the registrar to Dynadot and the parent zone to omani.works for an omani.works FQDN', () => {
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    const reg = screen.getByTestId('ns-registrar') as HTMLSelectElement
    expect(reg.value).toBe(DEFAULT_PARENT_REGISTRAR)
    const zone = screen.getByTestId('ns-parent-zone') as HTMLInputElement
    expect(zone.value).toBe(DEFAULT_PARENT_ZONE)
  })

  it('renders the Sovereign FQDN and LB IP read-back from props', () => {
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    expect(screen.getByTestId('ns-fqdn-readback').textContent).toContain('omantel.omani.works')
    expect(screen.getByTestId('ns-lb-ip').textContent).toContain('203.0.113.42')
  })

  it('does NOT render the confirm field or apply button until autoApply is checked', () => {
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    expect(screen.queryByTestId('ns-confirm')).toBeNull()
    expect(screen.queryByTestId('ns-apply')).toBeNull()
  })
})

/* ── 3. Auto-apply gating ───────────────────────────────────────────── */

describe('StepNSDelegation — auto-apply', () => {
  it('reveals the confirm field only after the toggle is checked', () => {
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    fireEvent.click(screen.getByTestId('ns-auto-apply'))
    expect(screen.getByTestId('ns-confirm')).toBeTruthy()
    expect(screen.getByTestId('ns-apply')).toBeTruthy()
  })

  it('keeps the apply button disabled until confirm text matches the parent zone', () => {
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    fireEvent.click(screen.getByTestId('ns-auto-apply'))
    const btn = screen.getByTestId('ns-apply') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('ns-confirm'), { target: { value: 'wrong' } })
    expect(btn.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('ns-confirm'), { target: { value: DEFAULT_PARENT_ZONE } })
    expect(btn.disabled).toBe(false)
  })

  it('does not call the catalyst-api when the operator only flips the toggle', async () => {
    const fetchSpy = vi.spyOn(global, 'fetch').mockResolvedValue(new Response('{}', { status: 200 }))
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    fireEvent.click(screen.getByTestId('ns-auto-apply'))
    // No confirm typed → no fetch even if the user tries to click.
    fireEvent.click(screen.getByTestId('ns-apply'))
    await waitFor(() => {
      expect(fetchSpy).not.toHaveBeenCalled()
    })
  })

  it('POSTs to the parent-zone-delegate endpoint when fully confirmed', async () => {
    const fetchSpy = vi.spyOn(global, 'fetch').mockResolvedValue(new Response('{"ok":true}', { status: 200 }))
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    fireEvent.click(screen.getByTestId('ns-auto-apply'))
    fireEvent.change(screen.getByTestId('ns-confirm'), { target: { value: DEFAULT_PARENT_ZONE } })
    fireEvent.click(screen.getByTestId('ns-apply'))
    await waitFor(() => {
      expect(fetchSpy).toHaveBeenCalledTimes(1)
    })
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe(parentZoneDelegateAPIPath())
    expect((init as RequestInit).method).toBe('POST')
    const body = JSON.parse(String((init as RequestInit).body))
    expect(body).toMatchObject({
      parentZone: DEFAULT_PARENT_ZONE,
      sovereignFQDN: 'omantel.omani.works',
      loadBalancerIP: '203.0.113.42',
    })
  })
})

/* ── 4. Stub-aware error message ────────────────────────────────────── */

describe('StepNSDelegation — Phase-8 stub awareness', () => {
  it('surfaces the deferred-to-Phase-8 hint on a 501 response', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(new Response('Not Implemented', { status: 501 }))
    render(<StepNSDelegation fqdnOverride="omantel.omani.works" lbIPOverride="203.0.113.42" />)
    fireEvent.click(screen.getByTestId('ns-auto-apply'))
    fireEvent.change(screen.getByTestId('ns-confirm'), { target: { value: DEFAULT_PARENT_ZONE } })
    fireEvent.click(screen.getByTestId('ns-apply'))
    await waitFor(() => {
      const err = screen.getByTestId('ns-apply-error')
      expect(err.textContent).toContain('Phase 8')
    })
  })
})
