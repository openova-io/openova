/**
 * UserAccessEditPage — create / edit form for a single UserAccess
 * Claim (issue #323).
 *
 * Form fields (matching the canonical CRD shape from #322):
 *   • Name (DNS-1123 string, immutable on edit)
 *   • Keycloak subject (free text — OIDC sub claim)
 *   • Keycloak groups (comma-separated free text)
 *   • Sovereign reference (free text — slug)
 *   • For now a single application grant row:
 *       - app slug
 *       - role (radio: admin / editor / viewer)
 *       - namespaces (comma-separated)
 *
 * The CRD supports multiple application entries per Claim; v1 of the
 * editor scopes to a single grant per Claim because that matches the
 * single-grant shape the catalyst-api expansion produces upstream
 * (see useraccess-sample.yaml fixture). Multi-grant authoring is a
 * follow-up; the JSON is still well-formed because applications is
 * always rendered as a 1-element array.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 the page renders fully on
 * first paint. Per #4, every URL flows through the central
 * userAccess.api.ts wrappers.
 */

import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import {
  createUserAccess,
  listUserAccess,
  updateUserAccess,
  type UserAccessItem,
  type UserAccessRequest,
  type UserAccessRole,
} from './userAccess.api'

export interface UserAccessEditPageProps {
  /** Test seam — pre-loaded existing entry for the edit path. */
  initialItem?: UserAccessItem | null
  /** Test seam — disables the fetch effect entirely. */
  disableFetch?: boolean
  /** Test seam — overrides the create/update handler so the form can
   *  be exercised without a network. */
  onSubmitOverride?: (req: UserAccessRequest) => Promise<void>
}

interface FormState {
  name: string
  keycloakSubject: string
  keycloakGroups: string
  sovereignRef: string
  app: string
  role: UserAccessRole
  namespaces: string
}

const DEFAULT_FORM: FormState = {
  name: '',
  keycloakSubject: '',
  keycloakGroups: '',
  sovereignRef: '',
  app: '',
  role: 'editor',
  namespaces: '',
}

export function UserAccessEditPage({
  initialItem = null,
  disableFetch = false,
  onSubmitOverride,
}: UserAccessEditPageProps = {}) {
  const params = useParams({ strict: false }) as {
    deploymentId: string
    name?: string
  }
  const deploymentId = params.deploymentId
  const editName = params.name ?? null
  const isEdit = !!editName
  const navigate = useNavigate()

  const [form, setForm] = useState<FormState>(() => fromItem(initialItem) ?? DEFAULT_FORM)
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [loading, setLoading] = useState(isEdit && !initialItem && !disableFetch)

  useEffect(() => {
    if (!isEdit || disableFetch || initialItem) return
    let cancelled = false
    setLoading(true)
    listUserAccess(deploymentId)
      .then((rows) => {
        if (cancelled) return
        const found = rows.find((r) => r.name === editName) ?? null
        if (!found) {
          setError(`UserAccess "${editName}" not found`)
        } else {
          setForm(fromItem(found) ?? DEFAULT_FORM)
        }
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [isEdit, disableFetch, deploymentId, editName, initialItem])

  const validationError = useMemo(() => validate(form, isEdit), [form, isEdit])

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setError(null)
    if (validationError) {
      setError(validationError)
      return
    }
    const req = toRequest(form)
    setSubmitting(true)
    try {
      if (onSubmitOverride) {
        await onSubmitOverride(req)
      } else if (isEdit) {
        await updateUserAccess(deploymentId, editName!, req)
      } else {
        await createUserAccess(deploymentId, req)
      }
      navigate({
        to: '/users' as never,
        params: { deploymentId } as never,
      })
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <PortalShell
      deploymentId={deploymentId}
      pageTitle={isEdit ? `Edit User Access — ${editName}` : 'New User Access'}
    >
      <div data-testid="user-access-edit-page" className="mx-auto max-w-2xl px-6 py-4">
        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          {error ? (
            <div data-testid="user-access-form-error" className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
              {error}
            </div>
          ) : null}

          <Field label="Name (Claim name)">
            <input
              data-testid="ua-input-name"
              type="text"
              required
              value={form.name}
              disabled={isEdit || loading}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm font-mono"
              placeholder="alice-helmwatch-omantel"
            />
          </Field>

          <Field label="Keycloak subject (OIDC sub)">
            <input
              data-testid="ua-input-subject"
              type="text"
              value={form.keycloakSubject}
              disabled={loading}
              onChange={(e) => setForm({ ...form, keycloakSubject: e.target.value })}
              className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
              placeholder="alice"
            />
          </Field>

          <Field label="Keycloak groups (comma-separated)">
            <input
              data-testid="ua-input-groups"
              type="text"
              value={form.keycloakGroups}
              disabled={loading}
              onChange={(e) => setForm({ ...form, keycloakGroups: e.target.value })}
              className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm"
              placeholder="sovereign-ops, helmwatch-editors"
            />
          </Field>

          <Field label="Sovereign reference">
            <input
              data-testid="ua-input-sovereign"
              type="text"
              required
              value={form.sovereignRef}
              disabled={loading}
              onChange={(e) => setForm({ ...form, sovereignRef: e.target.value })}
              className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm font-mono"
              placeholder="omantel"
            />
          </Field>

          <Field label="Application">
            <input
              data-testid="ua-input-app"
              type="text"
              required
              value={form.app}
              disabled={loading}
              onChange={(e) => setForm({ ...form, app: e.target.value })}
              className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm font-mono"
              placeholder="helmwatch"
            />
          </Field>

          <Field label="Role">
            <div data-testid="ua-input-role" className="flex gap-3 text-sm">
              {(['admin', 'editor', 'viewer'] as const).map((r) => (
                <label key={r} className="flex items-center gap-1">
                  <input
                    type="radio"
                    name="role"
                    value={r}
                    checked={form.role === r}
                    onChange={() => setForm({ ...form, role: r })}
                    disabled={loading}
                  />
                  <span>{r}</span>
                </label>
              ))}
            </div>
          </Field>

          <Field label="Namespaces (comma-separated; empty = expand from Application)">
            <input
              data-testid="ua-input-namespaces"
              type="text"
              value={form.namespaces}
              disabled={loading}
              onChange={(e) => setForm({ ...form, namespaces: e.target.value })}
              className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-sm font-mono"
              placeholder="helmwatch-prod, helmwatch-stg"
            />
          </Field>

          <div className="flex items-center justify-end gap-2">
            <button
              type="button"
              data-testid="ua-button-cancel"
              onClick={() =>
                navigate({
                  to: '/users' as never,
                  params: { deploymentId } as never,
                })
              }
              className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-sm hover:bg-[var(--color-bg-2)]"
            >
              Cancel
            </button>
            <button
              type="submit"
              data-testid="ua-button-save"
              disabled={submitting || loading || !!validationError}
              className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
            >
              {submitting ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </PortalShell>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs uppercase text-[var(--color-text-dim)]">{label}</span>
      {children}
    </div>
  )
}

function fromItem(item: UserAccessItem | null): FormState | null {
  if (!item) return null
  const firstApp = item.spec.applications[0]
  const role = firstApp?.role
  const knownRole: UserAccessRole = role === 'admin' || role === 'editor' || role === 'viewer' ? role : 'viewer'
  return {
    name: item.name,
    keycloakSubject: item.spec.user.keycloakSubject ?? '',
    keycloakGroups: (item.spec.user.keycloakGroups ?? []).join(', '),
    sovereignRef: item.spec.sovereignRef,
    app: firstApp?.app ?? '',
    role: knownRole,
    namespaces: (firstApp?.namespaces ?? []).join(', '),
  }
}

function splitCSV(s: string): string[] {
  return s
    .split(',')
    .map((p) => p.trim())
    .filter((p) => p.length > 0)
}

function toRequest(form: FormState): UserAccessRequest {
  const groups = splitCSV(form.keycloakGroups)
  const namespaces = splitCSV(form.namespaces)
  return {
    name: form.name,
    spec: {
      user: {
        ...(form.keycloakSubject.trim() ? { keycloakSubject: form.keycloakSubject.trim() } : {}),
        ...(groups.length > 0 ? { keycloakGroups: groups } : {}),
      },
      sovereignRef: form.sovereignRef.trim(),
      applications: [
        {
          app: form.app.trim(),
          role: form.role,
          ...(namespaces.length > 0 ? { namespaces } : {}),
        },
      ],
    },
  }
}

export function validate(form: FormState, isEdit: boolean): string | null {
  if (!isEdit && !form.name.trim()) return 'Name is required'
  if (!form.keycloakSubject.trim() && !splitCSV(form.keycloakGroups).length) {
    return 'Either Keycloak subject or at least one group is required'
  }
  if (!form.sovereignRef.trim()) return 'Sovereign reference is required'
  if (!form.app.trim()) return 'Application is required'
  return null
}
