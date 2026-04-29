/**
 * store.test.ts — vitest coverage for the wizard store mutations introduced
 * by #169 (BYO domain).
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useWizardStore } from './store'
import { INITIAL_WIZARD_STATE } from './model'

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

describe('wizard store — domain mode', () => {
  it('clears pool subdomain when switching to byo-manual', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignSubdomain: 'omantel-prod',
    })
    useWizardStore.getState().setSovereignDomainMode('byo-manual')
    expect(useWizardStore.getState().sovereignSubdomain).toBe('')
  })

  it('clears registrar credentials when switching to pool', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      sovereignByoDomain: 'acme.com',
      registrarType: 'cloudflare',
      registrarToken: 'secret-cf-token',
      registrarTokenValidated: true,
    })
    useWizardStore.getState().setSovereignDomainMode('pool')
    const s = useWizardStore.getState()
    expect(s.sovereignByoDomain).toBe('')
    expect(s.registrarType).toBeNull()
    expect(s.registrarToken).toBe('')
    expect(s.registrarTokenValidated).toBe(false)
  })

  it('keeps the typed BYO domain when switching from byo-manual to byo-api', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-manual',
      sovereignByoDomain: 'acme.com',
    })
    useWizardStore.getState().setSovereignDomainMode('byo-api')
    expect(useWizardStore.getState().sovereignByoDomain).toBe('acme.com')
  })
})

describe('wizard store — registrar credentials', () => {
  it('invalidates the validated flag on every token edit', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      registrarType: 'cloudflare',
      registrarToken: 'old',
      registrarTokenValidated: true,
    })
    useWizardStore.getState().setRegistrarToken('new')
    expect(useWizardStore.getState().registrarTokenValidated).toBe(false)
  })

  it('invalidates the validated flag on registrar swap', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      registrarType: 'cloudflare',
      registrarToken: 'cf-token',
      registrarTokenValidated: true,
    })
    useWizardStore.getState().setRegistrarType('godaddy')
    expect(useWizardStore.getState().registrarTokenValidated).toBe(false)
  })

  it('clearRegistrarCredentials wipes all three fields', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      registrarType: 'cloudflare',
      registrarToken: 'cf-token',
      registrarTokenValidated: true,
    })
    useWizardStore.getState().clearRegistrarCredentials()
    const s = useWizardStore.getState()
    expect(s.registrarType).toBeNull()
    expect(s.registrarToken).toBe('')
    expect(s.registrarTokenValidated).toBe(false)
  })
})

describe('wizard store — persistence hygiene', () => {
  it('drops registrarToken and registrarTokenValidated from the persist payload', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      registrarType: 'cloudflare',
      registrarToken: 'super-secret-token',
      registrarTokenValidated: true,
    })
    const raw = window.localStorage.getItem('openova-catalyst-wizard')
    expect(raw).toBeTruthy()
    if (raw) {
      const parsed = JSON.parse(raw) as { state: Record<string, unknown> }
      expect('registrarToken' in parsed.state).toBe(false)
      expect('registrarTokenValidated' in parsed.state).toBe(false)
      expect(parsed.state.registrarType).toBe('cloudflare')
    }
  })
})
