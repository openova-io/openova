/**
 * MultiGrantEditPage.test.ts — pure-function tests for the form-state
 * reducer + validator (EPIC-3 #1098 slice U1).
 *
 * The full DOM render path is exercised in the Playwright E2E suite
 * (per the brief — vitest covers the testable seams in isolation
 * because TanStack Router is hard to bootstrap inside jsdom).
 */

import { describe, expect, it } from 'vitest'

import {
  INITIAL_FORM_STATE,
  multiGrantReducer,
  validateMultiGrantForm,
} from './multiGrantState'
import type { KCUser } from './rbac.api'

const ALICE: KCUser = {
  id: 'kc-uuid-alice',
  username: 'alice',
  email: 'alice@acme.com',
  source: 'keycloak',
}

describe('multiGrantReducer', () => {
  it('starts empty (viewer tier, no scope, no user)', () => {
    expect(INITIAL_FORM_STATE.tier).toBe('viewer')
    expect(INITIAL_FORM_STATE.scope).toEqual([])
    expect(INITIAL_FORM_STATE.user).toBeNull()
  })

  it('set-tier replaces the tier without touching scope/user', () => {
    const s1 = multiGrantReducer(INITIAL_FORM_STATE, { type: 'set-tier', tier: 'admin' })
    expect(s1.tier).toBe('admin')
    expect(s1.scope).toEqual([])
    expect(s1.user).toBeNull()
  })

  it('set-user pins the user', () => {
    const s1 = multiGrantReducer(INITIAL_FORM_STATE, { type: 'set-user', user: ALICE })
    expect(s1.user).toEqual(ALICE)
  })

  it('add-scope appends a (key,value) chip', () => {
    const s1 = multiGrantReducer(INITIAL_FORM_STATE, {
      type: 'add-scope',
      key: 'openova.io/application',
      value: 'wordpress',
    })
    expect(s1.scope).toEqual([{ key: 'openova.io/application', value: 'wordpress' }])
  })

  it('add-scope dedupes (key,value) — re-adding is a no-op', () => {
    const s1 = multiGrantReducer(INITIAL_FORM_STATE, {
      type: 'add-scope',
      key: 'openova.io/application',
      value: 'wordpress',
    })
    const s2 = multiGrantReducer(s1, {
      type: 'add-scope',
      key: 'openova.io/application',
      value: 'wordpress',
    })
    expect(s2.scope).toHaveLength(1)
  })

  it('add-scope clears pending fields', () => {
    const s1: typeof INITIAL_FORM_STATE = {
      ...INITIAL_FORM_STATE,
      pendingKey: 'openova.io/env-type',
      pendingValue: 'dev',
    }
    const s2 = multiGrantReducer(s1, {
      type: 'add-scope',
      key: 'openova.io/env-type',
      value: 'dev',
    })
    expect(s2.pendingKey).toBe('')
    expect(s2.pendingValue).toBe('')
  })

  it('remove-scope removes by index', () => {
    let s = INITIAL_FORM_STATE
    s = multiGrantReducer(s, { type: 'add-scope', key: 'openova.io/application', value: 'a' })
    s = multiGrantReducer(s, { type: 'add-scope', key: 'openova.io/application', value: 'b' })
    s = multiGrantReducer(s, { type: 'remove-scope', index: 0 })
    expect(s.scope).toEqual([{ key: 'openova.io/application', value: 'b' }])
  })

  it('reset returns to INITIAL_FORM_STATE', () => {
    let s = INITIAL_FORM_STATE
    s = multiGrantReducer(s, { type: 'set-user', user: ALICE })
    s = multiGrantReducer(s, { type: 'set-tier', tier: 'admin' })
    s = multiGrantReducer(s, { type: 'reset' })
    expect(s).toEqual(INITIAL_FORM_STATE)
  })
})

describe('validateMultiGrantForm', () => {
  it('rejects when no user is picked', () => {
    expect(validateMultiGrantForm(INITIAL_FORM_STATE)).toBe('Pick a Keycloak user')
  })

  it('passes with user + viewer + no scope (global grant)', () => {
    const s = multiGrantReducer(INITIAL_FORM_STATE, { type: 'set-user', user: ALICE })
    expect(validateMultiGrantForm(s)).toBeNull()
  })

  it('rejects unknown scope keys', () => {
    let s = multiGrantReducer(INITIAL_FORM_STATE, { type: 'set-user', user: ALICE })
    s = { ...s, scope: [{ key: 'unknownkey', value: 'foo' }] }
    expect(validateMultiGrantForm(s)).toMatch(/unknown scope key/)
  })

  it('rejects empty scope value', () => {
    let s = multiGrantReducer(INITIAL_FORM_STATE, { type: 'set-user', user: ALICE })
    s = { ...s, scope: [{ key: 'openova.io/application', value: '' }] }
    expect(validateMultiGrantForm(s)).toBe('scope value is required')
  })

  it('passes a fully composed grant', () => {
    let s = multiGrantReducer(INITIAL_FORM_STATE, { type: 'set-user', user: ALICE })
    s = multiGrantReducer(s, { type: 'set-tier', tier: 'developer' })
    s = multiGrantReducer(s, {
      type: 'add-scope',
      key: 'openova.io/application',
      value: 'wordpress',
    })
    expect(validateMultiGrantForm(s)).toBeNull()
  })
})
