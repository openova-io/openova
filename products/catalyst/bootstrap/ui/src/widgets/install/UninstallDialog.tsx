/**
 * UninstallDialog — EPIC-2 Slice O (#1097): post-launch Application
 * uninstall confirmation.
 *
 * Requires the operator to type the application name to confirm —
 * deleting an Application is destructive (the application-controller
 * cascades the delete across every region's Helm release + Org Gitea
 * repo cleanup).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #5 the underlying handler enforces
 * tier-admin or higher; the typed-confirm gate is a UI safety net on
 * top of that.
 */

import { useState } from 'react'

import { deleteApplication } from '@/lib/catalog.api'

export interface UninstallDialogProps {
  open: boolean
  onClose: () => void
  sovereignId: string
  applicationName: string
  /** Org namespace. */
  namespace?: string
  /** Fired after a successful DELETE. */
  onUninstalled?: () => void
  /** Test seam. */
  disableNetwork?: boolean
}

export function UninstallDialog({
  open,
  onClose,
  sovereignId,
  applicationName,
  namespace,
  onUninstalled,
  disableNetwork = false,
}: UninstallDialogProps) {
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const confirmed = typed === applicationName

  const handleUninstall = async () => {
    if (!confirmed) return
    if (disableNetwork) {
      onUninstalled?.()
      onClose()
      return
    }
    setBusy(true)
    setError(null)
    try {
      await deleteApplication(sovereignId, applicationName, { namespace })
      onUninstalled?.()
      onClose()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  return (
    <div
      role="dialog"
      aria-modal="true"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      data-testid="uninstall-dialog"
    >
      <div className="w-full max-w-lg rounded-lg border border-red-500/40 bg-[var(--color-bg)] p-5">
        <h3 className="mb-2 text-base font-semibold text-red-400">Uninstall {applicationName}?</h3>
        <p className="mb-3 text-xs text-[var(--color-text-dim)]">
          This deletes the Application CR. The application-controller cascades the removal
          across every region's HelmRelease and cleans up the per-Org Gitea repo. The
          underlying data (PVCs / databases / object storage) survives unless the Blueprint
          declares otherwise.
        </p>
        <p className="mb-2 text-xs text-[var(--color-text-dim)]">
          Type <code className="font-mono text-red-300">{applicationName}</code> to confirm:
        </p>
        <input
          type="text"
          className="mb-3 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
          data-testid="uninstall-dialog-confirm-input"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          autoFocus
        />
        {error ? (
          <div
            className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
            data-testid="uninstall-dialog-error"
          >
            {error}
          </div>
        ) : null}
        <div className="flex justify-end gap-2">
          <button
            type="button"
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)]"
            data-testid="uninstall-dialog-cancel"
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            className="rounded-md bg-red-500 px-3 py-1.5 text-xs text-white hover:opacity-90 disabled:opacity-30"
            data-testid="uninstall-dialog-confirm"
            disabled={!confirmed || busy}
            onClick={handleUninstall}
          >
            {busy ? 'Uninstalling…' : 'Uninstall'}
          </button>
        </div>
      </div>
    </div>
  )
}
