/**
 * MultiGrantEditPage — multi-grant role assignment editor for the
 * Sovereign IAM (EPIC-3 #1098 slice U1).
 *
 * Replaces the legacy single-grant `UserAccessEditPage` (issue #323)
 * with the find-or-create-role contract from slice A1 (#1143):
 *
 *   1. Operator picks a tier (radio, with action-set tooltip preview).
 *   2. Operator adds scope chips (label-key + label-value, validated
 *      against NAMING-CONVENTION.md §6 vocab — INVIOLABLE-PRINCIPLES #4).
 *   3. Operator picks a Keycloak user via the U2 KCUserPicker widget.
 *   4. Apply → POST /rbac/assign — backend resolves to created /
 *      updated / no-op based on whether a UserAccess CR for the
 *      (user, scope) pair already exists.
 *
 * Per the canonical-seam map: this page extends the existing user-access
 * file tree in place rather than spawning a parallel surface. The
 * router still mounts UserAccessListPage at `/users` (read-only list);
 * the multi-grant editor lives at `/users/multi-grant` and the legacy
 * UserAccessEditPage stays at `/users/new` + `/users/$name` for the
 * deprecation grace period.
 *
 * Per the post-merge response semantics:
 *   - `applied: created` → 201; toast "Granted <tier> to <user>"
 *   - `applied: updated` → 200; toast "Updated <user> from <prevTier> to <tier>"
 *   - `applied: no-op`   → 200; toast "Already granted — no change"
 */

import { useMemo, useReducer, useState } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useMutation } from '@tanstack/react-query'

import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { KCUserPicker } from '@/widgets/rbac/KCUserPicker'
import {
  RBAC_TIERS,
  RBAC_SCOPE_KEYS,
  TIER_ACTION_SETS,
  TIER_AUTO_INJECTED_SCOPES,
  rbacAssign,
  validateScopeKey,
  validateScopeValue,
  type RBACAssignResponse,
  type RBACTier,
} from './rbac.api'
import {
  INITIAL_FORM_STATE,
  multiGrantReducer,
  validateMultiGrantForm,
} from './multiGrantState'

/* ── Page ─────────────────────────────────────────────────────────── */

export interface MultiGrantEditPageProps {
  /** Test seam — pre-fills the deployment id without TanStack Router. */
  initialDeploymentId?: string
  /** Test seam — prevents navigate-on-success. */
  disableNavigate?: boolean
}

export function MultiGrantEditPage({
  initialDeploymentId,
  disableNavigate = false,
}: MultiGrantEditPageProps = {}) {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = initialDeploymentId ?? params.deploymentId ?? resolvedId ?? ''
  const navigate = useNavigate()

  const [form, dispatch] = useReducer(multiGrantReducer, INITIAL_FORM_STATE)
  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; msg: string } | null>(null)
  const [pendingErr, setPendingErr] = useState<string | null>(null)

  const validationError = useMemo(() => validateMultiGrantForm(form), [form])

  const mutation = useMutation({
    mutationFn: async () => {
      const body = {
        user: form.user!.email
          ? { email: form.user!.email, keycloakSubject: form.user!.id }
          : { keycloakSubject: form.user!.id },
        tier: form.tier,
        scope: form.scope,
      }
      return rbacAssign(deploymentId, body)
    },
    onSuccess: (resp: RBACAssignResponse) => {
      const userLabel = form.user?.email ?? form.user?.username ?? '<user>'
      switch (resp.applied) {
        case 'created':
          setToast({
            kind: 'ok',
            msg: `Granted ${form.tier} to ${userLabel}. Bound to ${resp.tierClusterRole}.`,
          })
          break
        case 'updated':
          setToast({
            kind: 'ok',
            msg: `Updated ${userLabel}'s grant. Now ${form.tier} (was a different tier on the same scope).`,
          })
          break
        case 'no-op':
          setToast({
            kind: 'ok',
            msg: `Already granted — ${userLabel} already has ${form.tier} on this scope. No change.`,
          })
          break
      }
      // Navigate back to the list view after a short delay, unless the
      // test seam pins us in place.
      if (disableNavigate) return
      setTimeout(() => {
        navigate({
          to: (DETECTED_MODE.mode === 'sovereign' || !deploymentId
            ? '/users'
            : `/provision/${deploymentId}/users`) as never,
          params: { deploymentId } as never,
        })
      }, 800)
    },
    onError: (err: Error) => {
      setToast({ kind: 'err', msg: err.message })
    },
  })

  function onAddScope(e: React.FormEvent) {
    e.preventDefault()
    setPendingErr(null)
    const ke = validateScopeKey(form.pendingKey)
    if (ke) {
      setPendingErr(ke)
      return
    }
    const ve = validateScopeValue(form.pendingValue)
    if (ve) {
      setPendingErr(ve)
      return
    }
    dispatch({ type: 'add-scope', key: form.pendingKey, value: form.pendingValue })
  }

  function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (validationError) {
      setToast({ kind: 'err', msg: validationError })
      return
    }
    setToast(null)
    mutation.mutate()
  }

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="New User Access Grant">
      <div data-testid="multi-grant-edit-page" className="mx-auto max-w-2xl px-6 py-4">
        <p className="mb-4 text-sm text-[var(--color-text-dim)]">
          Pick a tier, add scopes, choose a Keycloak user. We resolve to the right UserAccess CR
          (find-or-create) so re-granting the same user on the same scope is idempotent.
        </p>

        {toast ? (
          <div
            data-testid={toast.kind === 'ok' ? 'multi-grant-toast-ok' : 'multi-grant-toast-err'}
            className={`mb-3 rounded-md border px-3 py-2 text-sm ${
              toast.kind === 'ok'
                ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
                : 'border-red-500/40 bg-red-500/10 text-red-300'
            }`}
          >
            {toast.msg}
          </div>
        ) : null}

        <form onSubmit={onSubmit} className="flex flex-col gap-5">
          {/* Tier picker */}
          <fieldset data-testid="multi-grant-tier-picker">
            <legend className="text-xs uppercase text-[var(--color-text-dim)]">Tier</legend>
            <div className="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-5">
              {RBAC_TIERS.map((t) => (
                <TierRadio
                  key={t}
                  tier={t}
                  selected={form.tier === t}
                  onSelect={() => dispatch({ type: 'set-tier', tier: t })}
                />
              ))}
            </div>
            <TierActionPreview tier={form.tier} />
          </fieldset>

          {/* Scope chips */}
          <fieldset data-testid="multi-grant-scope-picker">
            <legend className="text-xs uppercase text-[var(--color-text-dim)]">Scope</legend>

            <div className="mt-2 flex flex-wrap gap-2">
              {form.scope.length === 0 ? (
                <span data-testid="multi-grant-scope-empty" className="text-xs text-[var(--color-text-dim)]">
                  No scopes — grant will be GLOBAL across this Sovereign.
                </span>
              ) : (
                form.scope.map((s, i) => (
                  <span
                    key={`${s.key}=${s.value}`}
                    data-testid={`multi-grant-scope-chip-${i}`}
                    className="inline-flex items-center gap-1 rounded-full border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-0.5 text-xs"
                  >
                    <span className="font-mono">
                      {s.key}={s.value}
                    </span>
                    <button
                      type="button"
                      data-testid={`multi-grant-scope-remove-${i}`}
                      aria-label={`Remove scope ${s.key}=${s.value}`}
                      onClick={() => dispatch({ type: 'remove-scope', index: i })}
                      className="rounded-full px-1 text-[var(--color-text-dim)] hover:text-red-400"
                    >
                      ×
                    </button>
                  </span>
                ))
              )}
            </div>

            {/* Auto-injected scope notice (e.g. developer→env-type=dev). */}
            {TIER_AUTO_INJECTED_SCOPES[form.tier]?.length ? (
              <p className="mt-2 text-xs text-[var(--color-text-dim)]" data-testid="multi-grant-auto-scope-notice">
                Auto-injected by the controller for tier <code className="font-mono">{form.tier}</code>:{' '}
                {TIER_AUTO_INJECTED_SCOPES[form.tier]!
                  .map((s) => `${s.key}=${s.value}`)
                  .join(', ')}
              </p>
            ) : null}

            <div className="mt-3 flex flex-wrap gap-2">
              <select
                data-testid="multi-grant-scope-key-input"
                value={form.pendingKey}
                onChange={(e) => dispatch({ type: 'set-pending-key', value: e.target.value })}
                className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs font-mono"
                aria-label="Scope key"
              >
                <option value="">Select key…</option>
                {RBAC_SCOPE_KEYS.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </select>
              <input
                data-testid="multi-grant-scope-value-input"
                type="text"
                value={form.pendingValue}
                onChange={(e) => dispatch({ type: 'set-pending-value', value: e.target.value })}
                placeholder="value (e.g. wordpress)"
                className="flex-1 rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs font-mono"
                aria-label="Scope value"
              />
              <button
                type="button"
                data-testid="multi-grant-scope-add"
                onClick={onAddScope}
                className="rounded-md border border-[var(--color-border)] px-2 py-1 text-xs hover:bg-[var(--color-bg-2)]"
              >
                + Add scope
              </button>
            </div>
            {pendingErr ? (
              <p data-testid="multi-grant-scope-err" className="mt-1 text-xs text-red-300">
                {pendingErr}
              </p>
            ) : null}
          </fieldset>

          {/* User picker (U2) */}
          <fieldset data-testid="multi-grant-user-fieldset">
            <legend className="text-xs uppercase text-[var(--color-text-dim)]">User</legend>
            <div className="mt-2">
              <KCUserPicker
                sovereignId={deploymentId}
                onSelect={(u) => dispatch({ type: 'set-user', user: u })}
                initialQuery={form.user?.email ?? form.user?.username ?? ''}
              />
            </div>
            {form.user ? (
              <p data-testid="multi-grant-user-pinned" className="mt-1 text-xs text-[var(--color-text-dim)]">
                Selected:{' '}
                <code className="font-mono text-[var(--color-text)]">
                  {form.user.email ?? form.user.username}
                </code>{' '}
                (source: <code className="font-mono">{form.user.source}</code>)
              </p>
            ) : null}
          </fieldset>

          {/* Submit row */}
          <div className="flex items-center justify-end gap-2 pt-2">
            <button
              type="button"
              data-testid="multi-grant-cancel"
              onClick={() => {
                if (disableNavigate) return
                navigate({
                  to: (DETECTED_MODE.mode === 'sovereign' || !deploymentId
                    ? '/users'
                    : `/provision/${deploymentId}/users`) as never,
                  params: { deploymentId } as never,
                })
              }}
              className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-sm hover:bg-[var(--color-bg-2)]"
            >
              Cancel
            </button>
            <button
              type="submit"
              data-testid="multi-grant-apply"
              disabled={!!validationError || mutation.isPending}
              className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
              title={validationError ?? 'Apply this grant'}
            >
              {mutation.isPending ? 'Applying…' : 'Apply'}
            </button>
          </div>
        </form>
      </div>
    </PortalShell>
  )
}

/* ── Sub-components ───────────────────────────────────────────────── */

function TierRadio({
  tier,
  selected,
  onSelect,
}: {
  tier: RBACTier
  selected: boolean
  onSelect: () => void
}) {
  return (
    <label
      data-testid={`multi-grant-tier-${tier}`}
      className={`cursor-pointer rounded-md border px-3 py-2 text-center text-sm transition-colors ${
        selected
          ? 'border-emerald-500/60 bg-emerald-500/10 text-emerald-200'
          : 'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text)] hover:border-[var(--color-border)]'
      }`}
      title={TIER_ACTION_SETS[tier].join('\n')}
    >
      <input
        type="radio"
        name="tier"
        value={tier}
        checked={selected}
        onChange={onSelect}
        className="sr-only"
      />
      <span className="block font-mono uppercase">{tier}</span>
    </label>
  )
}

function TierActionPreview({ tier }: { tier: RBACTier }) {
  return (
    <div
      data-testid={`multi-grant-tier-preview-${tier}`}
      className="mt-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2"
    >
      <p className="text-xs uppercase text-[var(--color-text-dim)]">
        Actions granted by <code className="font-mono">{tier}</code>:
      </p>
      <ul className="mt-1 list-disc pl-5 text-xs text-[var(--color-text-dim)]">
        {TIER_ACTION_SETS[tier].map((a) => (
          <li key={a} className="font-mono">
            {a}
          </li>
        ))}
      </ul>
    </div>
  )
}
