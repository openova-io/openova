/**
 * multiGrantState.ts — pure form-state reducer + validator for the
 * EPIC-3 (#1098) slice U1 multi-grant editor.
 *
 * Extracted from MultiGrantEditPage.tsx so the file's exports stay
 * component-only (lint react-refresh/only-export-components).
 */

import {
  validateScopeKey,
  validateScopeValue,
  type KCUser,
  type RBACScope,
  type RBACTier,
} from './rbac.api'

export interface MultiGrantFormState {
  user: KCUser | null
  tier: RBACTier
  scope: RBACScope[]
  // Pending fields for the "add scope" composer below the chip list.
  pendingKey: string
  pendingValue: string
}

export type MultiGrantAction =
  | { type: 'set-user'; user: KCUser | null }
  | { type: 'set-tier'; tier: RBACTier }
  | { type: 'add-scope'; key: string; value: string }
  | { type: 'remove-scope'; index: number }
  | { type: 'set-pending-key'; value: string }
  | { type: 'set-pending-value'; value: string }
  | { type: 'reset' }

export const INITIAL_FORM_STATE: MultiGrantFormState = {
  user: null,
  tier: 'viewer',
  scope: [],
  pendingKey: '',
  pendingValue: '',
}

export function multiGrantReducer(
  state: MultiGrantFormState,
  action: MultiGrantAction,
): MultiGrantFormState {
  switch (action.type) {
    case 'set-user':
      return { ...state, user: action.user }
    case 'set-tier':
      return { ...state, tier: action.tier }
    case 'add-scope': {
      const key = action.key.trim()
      const value = action.value.trim()
      // Dedupe by (key, value).
      if (state.scope.some((s) => s.key === key && s.value === value)) {
        return { ...state, pendingKey: '', pendingValue: '' }
      }
      return {
        ...state,
        scope: [...state.scope, { key, value }],
        pendingKey: '',
        pendingValue: '',
      }
    }
    case 'remove-scope':
      return {
        ...state,
        scope: state.scope.filter((_, i) => i !== action.index),
      }
    case 'set-pending-key':
      return { ...state, pendingKey: action.value }
    case 'set-pending-value':
      return { ...state, pendingValue: action.value }
    case 'reset':
      return INITIAL_FORM_STATE
  }
}

/** validateMultiGrantForm — pure function exposed for tests. */
export function validateMultiGrantForm(state: MultiGrantFormState): string | null {
  if (!state.user) return 'Pick a Keycloak user'
  for (const s of state.scope) {
    const ke = validateScopeKey(s.key)
    if (ke) return ke
    const ve = validateScopeValue(s.value)
    if (ve) return ve
  }
  return null
}
