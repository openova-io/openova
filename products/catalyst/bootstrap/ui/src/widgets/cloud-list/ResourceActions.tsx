/**
 * ResourceActions — EPIC-4 Slice R6 (#1099) — per-row actions
 * (scale / restart / delete) shared between the K8sListPage row and
 * the ResourceDetailPage Overview tab.
 *
 * An object that Git owns shows "Managed by Git" with the owner, and no
 * scale or delete (the API answers 409 managed-by-git). Any other object
 * is scaled or deleted by break-glass, with the reason the widget sends.
 *
 * Authorization: server-side handlers require tier-admin or higher.
 * The UI hides the buttons when `disabled` is true, but the server is
 * the authoritative gate per INVIOLABLE-PRINCIPLES.md #5 — UI hiding
 * is convenience only.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode) — kind allow-lists come from resource.api.ts.
 */

import { useState } from 'react'

import {
  ManagedByGitError,
  RESTARTABLE_KINDS,
  SCALABLE_KINDS,
  deleteResource,
  restartResource,
  scaleResource,
} from '@/pages/sovereign/cloud-list/resource.api'

export interface ResourceActionsProps {
  deploymentId: string
  kind: string
  ns: string | undefined
  name: string
  /** Current replicas, when known — seeds the scale input. */
  currentReplicas?: number
  /** When true, every action button is hidden (UI convenience for the
   *  viewer tier). Server-side still enforces. */
  disabled?: boolean
  /** The owner in Git of the object (gitOwner of resource.api), or null.
   *  When set, scale and delete are hidden and "Managed by Git" is shown. */
  gitOwner?: string | null
  /** Optional callback fired after a successful action so callers can
   *  invalidate caches / refetch. */
  onActionComplete?: (op: 'scale' | 'restart' | 'delete') => void
  /** Test seam — opens the delete confirmation in the modal-open state
   *  on first render. */
  startWithDeleteOpen?: boolean
}

export function ResourceActions({
  deploymentId,
  kind,
  ns,
  name,
  currentReplicas,
  disabled,
  gitOwner,
  onActionComplete,
  startWithDeleteOpen,
}: ResourceActionsProps) {
  const canonical = kind.toLowerCase()
  const canScale = SCALABLE_KINDS.has(canonical)
  const canRestart = RESTARTABLE_KINDS.has(canonical)
  const [replicas, setReplicas] = useState<string>(String(currentReplicas ?? ''))
  const [busy, setBusy] = useState<'scale' | 'restart' | 'delete' | null>(null)
  const [msg, setMsg] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<boolean>(!!startWithDeleteOpen)
  const [deleteText, setDeleteText] = useState('')
  const [reason, setReason] = useState('')
  // The owner named by a 409 managed-by-git answer: Git owns the object
  // although its labels did not say so (a kind of the intent lock).
  const [answeredOwner, setAnsweredOwner] = useState<string | null>(null)
  const owner = gitOwner ?? answeredOwner

  if (disabled) {
    return (
      <div data-testid="resource-actions-disabled" className="text-xs italic text-[var(--color-text-dim)]">
        Sign in as a tier-admin (or higher) to mutate cluster resources.
      </div>
    )
  }

  function fail(err: unknown) {
    if (err instanceof ManagedByGitError) setAnsweredOwner(err.owner)
    else setError(err instanceof Error ? err.message : String(err))
  }

  async function onScale() {
    setMsg(null)
    setError(null)
    const n = parseInt(replicas, 10)
    if (Number.isNaN(n) || n < 0) {
      setError('Replicas must be a non-negative integer.')
      return
    }
    setBusy('scale')
    try {
      const resp = await scaleResource(deploymentId, kind, ns, name, n, reason)
      setMsg(`Scaled ${resp.name} to ${resp.replicas}.`)
      onActionComplete?.('scale')
    } catch (err) {
      fail(err)
    } finally {
      setBusy(null)
    }
  }

  async function onRestart() {
    setMsg(null)
    setError(null)
    setBusy('restart')
    try {
      const resp = await restartResource(deploymentId, kind, ns, name)
      setMsg(`Restart requested at ${resp.restartedAt}.`)
      onActionComplete?.('restart')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function onDelete() {
    if (deleteText.trim() !== name) {
      setError(`Type "${name}" exactly to confirm.`)
      return
    }
    setMsg(null)
    setError(null)
    setBusy('delete')
    try {
      const resp = await deleteResource(deploymentId, kind, ns, name, reason)
      setMsg(resp.message)
      setConfirmDelete(false)
      onActionComplete?.('delete')
    } catch (err) {
      fail(err)
    } finally {
      setBusy(null)
    }
  }

  return (
    <div data-testid="resource-actions" className="space-y-2">
      {owner !== null && (
        <div data-testid="resource-actions-managed-by-git" className="text-xs text-[var(--color-text)]">
          Managed by Git: <code className="font-mono">{owner}</code>. Change it in Git; scale and delete are not offered.
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {canScale && owner === null && (
          <div className="flex items-center gap-1">
            <label htmlFor={`replicas-${name}`} className="text-xs text-[var(--color-text-dim)]">
              Replicas
            </label>
            <input
              id={`replicas-${name}`}
              data-testid="resource-actions-replicas"
              type="number"
              min={0}
              value={replicas}
              onChange={(e) => setReplicas(e.target.value)}
              className="w-16 rounded border border-[var(--color-border)] bg-[var(--color-bg-1)] px-2 py-1 text-sm text-[var(--color-text)]"
            />
            <button
              type="button"
              onClick={onScale}
              disabled={busy !== null}
              data-testid="resource-actions-scale"
              className="rounded border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text)] hover:bg-[var(--color-bg-3)] disabled:opacity-60"
            >
              {busy === 'scale' ? 'Scaling…' : 'Scale'}
            </button>
          </div>
        )}
        {canRestart && (
          <button
            type="button"
            onClick={onRestart}
            disabled={busy !== null}
            data-testid="resource-actions-restart"
            className="rounded border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text)] hover:bg-[var(--color-bg-3)] disabled:opacity-60"
          >
            {busy === 'restart' ? 'Restarting…' : 'Restart'}
          </button>
        )}
        {owner === null && (
          <button
            type="button"
            onClick={() => setConfirmDelete(true)}
            disabled={busy !== null}
            data-testid="resource-actions-delete"
            className="rounded border border-rose-500 bg-rose-700 px-2 py-1 text-xs text-white hover:bg-rose-600 disabled:opacity-60"
          >
            Delete
          </button>
        )}
      </div>
      {owner === null && (
        <input
          data-testid="resource-actions-reason"
          aria-label="Reason"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="Reason for scale or delete: at least 10 characters, recorded in the audit log"
          className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg-1)] px-2 py-1 text-sm text-[var(--color-text)]"
        />
      )}
      {confirmDelete && owner === null && (
        <div data-testid="resource-actions-delete-modal" className="rounded-lg border border-rose-500 bg-[var(--color-bg-2)] p-3">
          <div className="text-xs text-[var(--color-text)]">
            This will <strong className="text-rose-300">delete</strong> <code className="font-mono">{kind}/{name}</code>{' '}
            in namespace <code className="font-mono">{ns ?? '—'}</code>. Type <code className="font-mono">{name}</code> to confirm.
          </div>
          <input
            data-testid="resource-actions-delete-confirm"
            value={deleteText}
            onChange={(e) => setDeleteText(e.target.value)}
            className="mt-2 w-full rounded border border-[var(--color-border)] bg-[var(--color-bg-1)] px-2 py-1 text-sm text-[var(--color-text)]"
            placeholder={name}
            aria-label="Confirm resource name"
          />
          <div className="mt-2 flex gap-2">
            <button
              type="button"
              onClick={onDelete}
              disabled={busy !== null || deleteText.trim() !== name}
              data-testid="resource-actions-delete-commit"
              className="rounded border border-rose-500 bg-rose-700 px-2 py-1 text-xs text-white hover:bg-rose-600 disabled:opacity-50"
            >
              {busy === 'delete' ? 'Deleting…' : 'Confirm delete'}
            </button>
            <button
              type="button"
              onClick={() => {
                setConfirmDelete(false)
                setDeleteText('')
                setError(null)
              }}
              data-testid="resource-actions-delete-cancel"
              className="rounded border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text)] hover:bg-[var(--color-bg-3)]"
            >
              Cancel
            </button>
          </div>
        </div>
      )}
      {msg && (
        <div data-testid="resource-actions-msg" className="text-xs text-emerald-300">
          {msg}
        </div>
      )}
      {error && (
        <div data-testid="resource-actions-err" className="text-xs text-rose-300">
          {error}
        </div>
      )}
    </div>
  )
}
