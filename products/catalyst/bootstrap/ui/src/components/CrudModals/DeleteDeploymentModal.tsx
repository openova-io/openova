/**
 * DeleteDeploymentModal — operator-facing delete from the deployments
 * admin list (issue #178). Reuses the same ModalShell scaffold +
 * type-to-confirm pattern as WipeDeploymentModal, but offers TWO modes
 * via a radio group:
 *
 *   1. record-only — calls DELETE /api/v1/deployments/{id}. Removes the
 *      catalyst-api state (in-memory map + on-disk store + kubeconfig)
 *      but LEAVES THE HETZNER SOVEREIGN RUNNING. Useful when the
 *      Sovereign is fine but the breadcrumb row is no longer wanted.
 *
 *   2. deep — calls POST /api/v1/deployments/{id}/wipe FIRST (which
 *      destroys every Hetzner resource tagged for this deployment AND
 *      deletes the on-disk record on success), then no second call is
 *      needed. The Hetzner token is collected up-front so the wipe
 *      handler can authenticate `tofu destroy` + the orphan purge.
 *
 * Both modes require typing the deployment FQDN to confirm — the
 * destructive deep-delete additionally requires the Hetzner token,
 * mirroring WipeDeploymentModal's defensive posture (issue #166 + #914).
 *
 * The "deep" mode is the founder's "kill the kid delivered by the
 * mother" semantic; record-only is "remove the records from the
 * mother only".
 */

import { useState } from 'react'
import { ModalShell } from './_shared'
import { API_BASE } from '@/shared/config/urls'
import type { WipeReport } from './WipeDeploymentModal'

export type DeleteMode = 'record-only' | 'deep'

export interface DeleteDeploymentModalProps {
  open: boolean
  deploymentId: string
  sovereignFQDN: string | null
  onClose: () => void
  /** Fired after a successful delete (either mode) so the caller can
   *  refresh the list. */
  onDeleted: (mode: DeleteMode) => void
}

interface DeleteResponse {
  deploymentId: string
  sovereignFQDN?: string
  mode: string
  storeDeleted: boolean
  localCleaned: boolean
  note?: string
}

export function DeleteDeploymentModal({
  open,
  deploymentId,
  sovereignFQDN,
  onClose,
  onDeleted,
}: DeleteDeploymentModalProps) {
  const [mode, setMode] = useState<DeleteMode>('record-only')
  const [confirmText, setConfirmText] = useState('')
  const [hetznerToken, setHetznerToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [wipeReport, setWipeReport] = useState<WipeReport | null>(null)
  const [recordReport, setRecordReport] = useState<DeleteResponse | null>(null)

  const requiredText = sovereignFQDN ?? deploymentId
  // record-only mode: only the FQDN gate. deep mode: FQDN gate AND a
  // Hetzner token (>20 chars — same heuristic as WipeDeploymentModal).
  const ready =
    !busy &&
    confirmText.trim() === requiredText &&
    (mode === 'record-only' || hetznerToken.trim().length > 20)

  async function performDelete() {
    setBusy(true)
    setError(null)
    try {
      if (mode === 'deep') {
        // Deep delete: POST /wipe — that handler runs tofu destroy +
        // Hetzner orphan purge + PDM release + record delete in one
        // pass. No separate record-only DELETE call afterwards.
        const res = await fetch(
          `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/wipe`,
          {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
            credentials: 'include',
            body: JSON.stringify({ hetznerToken: hetznerToken.trim() }),
          },
        )
        const text = await res.text()
        if (!res.ok) {
          setError(`HTTP ${res.status}: ${text.slice(0, 400)}`)
          setBusy(false)
          return
        }
        const parsed = JSON.parse(text) as WipeReport
        setWipeReport(parsed)
      } else {
        // record-only delete: DELETE /api/v1/deployments/{id}.
        const res = await fetch(
          `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}`,
          {
            method: 'DELETE',
            headers: { Accept: 'application/json' },
            credentials: 'include',
          },
        )
        const text = await res.text()
        if (!res.ok) {
          setError(`HTTP ${res.status}: ${text.slice(0, 400)}`)
          setBusy(false)
          return
        }
        const parsed = JSON.parse(text) as DeleteResponse
        setRecordReport(parsed)
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  // Success view — render after either mode succeeds.
  if (wipeReport || recordReport) {
    const isDeep = wipeReport != null
    return (
      <ModalShell
        id="delete-deployment"
        open={open}
        title={isDeep ? 'Deployment wiped' : 'Record deleted'}
        subtitle={requiredText}
        onClose={() => onDeleted(isDeep ? 'deep' : 'record-only')}
        primary={{
          label: 'Done',
          onClick: () => onDeleted(isDeep ? 'deep' : 'record-only'),
        }}
      >
        {isDeep && wipeReport ? (
          <>
            <p style={{ marginTop: 0, fontSize: 12, color: 'var(--color-text-dim)' }}>
              Hetzner resources removed:{' '}
              {(wipeReport.hetznerPurge.servers?.length ?? 0)} servers,{' '}
              {(wipeReport.hetznerPurge.load_balancers?.length ?? 0)} load balancers,{' '}
              {(wipeReport.hetznerPurge.networks?.length ?? 0)} networks,{' '}
              {(wipeReport.hetznerPurge.firewalls?.length ?? 0)} firewalls,{' '}
              {(wipeReport.hetznerPurge.ssh_keys?.length ?? 0)} ssh-keys,{' '}
              {(wipeReport.hetznerPurge.s3_buckets?.length ?? 0)} S3 buckets.
            </p>
            <p style={{ marginTop: 4, fontSize: 12, color: 'var(--color-text-dim)' }}>
              Tofu destroy: {wipeReport.tofuDestroyed ? '✓' : '✗'} · PDM released:{' '}
              {wipeReport.pdmReleased ? '✓' : 'n/a'} · Local cleaned:{' '}
              {wipeReport.localCleaned ? '✓' : '✗'}
            </p>
            {wipeReport.errors && wipeReport.errors.length > 0 ? (
              <pre
                data-testid="delete-deployment-report-errors"
                style={{
                  marginTop: 8, padding: 8, fontSize: 11,
                  background: 'var(--color-bg-2)', color: 'var(--color-warn)',
                  borderRadius: 4, overflowX: 'auto',
                }}
              >
                {wipeReport.errors.join('\n')}
              </pre>
            ) : null}
          </>
        ) : (
          <p style={{ marginTop: 0, fontSize: 12, color: 'var(--color-text-dim)' }}>
            Deployment record removed from catalyst-api. The Sovereign
            cluster at <code>{requiredText}</code> is still running in
            Hetzner — destroy it from the Hetzner Console if you want
            to release the cloud resources too.
          </p>
        )}
      </ModalShell>
    )
  }

  // Pre-delete confirmation view.
  return (
    <ModalShell
      id="delete-deployment"
      open={open}
      title="Delete deployment"
      subtitle={requiredText}
      onClose={() => { if (!busy) onClose() }}
      primary={{
        label: busy ? 'Working…' : (mode === 'deep' ? 'Wipe Sovereign and delete record' : 'Delete record only'),
        onClick: performDelete,
        disabled: !ready,
        loading: busy,
        danger: mode === 'deep',
      }}
      secondary={{ label: 'Cancel', onClick: onClose }}
    >
      <fieldset
        data-testid="delete-deployment-mode"
        style={{ border: 'none', padding: 0, margin: 0, marginBottom: 12 }}
      >
        <legend style={{ fontSize: 12, color: 'var(--color-text-dim)', marginBottom: 6 }}>
          What should I delete?
        </legend>
        <label
          style={{
            display: 'flex', alignItems: 'flex-start', gap: 8, padding: 8,
            border: '1px solid var(--color-border)', borderRadius: 4,
            marginBottom: 6, cursor: 'pointer',
            background: mode === 'record-only' ? 'var(--color-bg-2)' : 'transparent',
          }}
        >
          <input
            type="radio"
            name="delete-mode"
            value="record-only"
            checked={mode === 'record-only'}
            onChange={() => setMode('record-only')}
            disabled={busy}
            data-testid="delete-deployment-mode-record-only"
            style={{ marginTop: 3 }}
          />
          <span>
            <strong style={{ display: 'block', fontSize: 13 }}>
              Delete record only (mother)
            </strong>
            <span style={{ fontSize: 11, color: 'var(--color-text-dim)' }}>
              Removes the deployment from catalyst-api (in-memory + on-disk
              store + kubeconfig). The Sovereign cluster KEEPS RUNNING in
              Hetzner — you'll need to destroy it from the Hetzner Console
              separately if you want to release the cloud resources.
            </span>
          </span>
        </label>
        <label
          style={{
            display: 'flex', alignItems: 'flex-start', gap: 8, padding: 8,
            border: '1px solid var(--color-border)', borderRadius: 4,
            cursor: 'pointer',
            background: mode === 'deep' ? 'var(--color-bg-2)' : 'transparent',
          }}
        >
          <input
            type="radio"
            name="delete-mode"
            value="deep"
            checked={mode === 'deep'}
            onChange={() => setMode('deep')}
            disabled={busy}
            data-testid="delete-deployment-mode-deep"
            style={{ marginTop: 3 }}
          />
          <span>
            <strong style={{ display: 'block', fontSize: 13, color: 'var(--color-danger)' }}>
              Delete record AND wipe Sovereign (kill the kid)
            </strong>
            <span style={{ fontSize: 11, color: 'var(--color-text-dim)' }}>
              Runs tofu destroy + Hetzner orphan purge + PDM release +
              record cleanup. Every Hetzner resource tagged{' '}
              <code style={{ background: 'var(--color-bg-2)', padding: '0 4px', borderRadius: 3 }}>
                catalyst-deployment-id={deploymentId.slice(0, 12)}…
              </code>{' '}
              is destroyed. THIS CANNOT BE UNDONE.
            </span>
          </span>
        </label>
      </fieldset>

      <label style={{ display: 'block', marginTop: 8 }}>
        <span style={{ fontSize: 12, color: 'var(--color-text-dim)' }}>
          Type{' '}
          <code style={{ background: 'var(--color-bg-2)', padding: '0 4px', borderRadius: 3 }}>
            {requiredText}
          </code>{' '}
          to confirm:
        </span>
        <input
          type="text"
          value={confirmText}
          onChange={(e) => setConfirmText(e.target.value)}
          disabled={busy}
          data-testid="delete-deployment-confirm-text"
          style={{
            marginTop: 4, width: '100%', padding: '4px 8px',
            fontFamily: 'monospace', fontSize: 13,
            background: 'var(--color-bg-2)', color: 'var(--color-text)',
            border: '1px solid var(--color-border)', borderRadius: 4,
          }}
        />
      </label>

      {mode === 'deep' ? (
        <label style={{ display: 'block', marginTop: 12 }}>
          <span style={{ fontSize: 12, color: 'var(--color-text-dim)' }}>
            Hetzner Cloud API token (required for wipe — never logged):
          </span>
          <input
            type="password"
            value={hetznerToken}
            onChange={(e) => setHetznerToken(e.target.value)}
            disabled={busy}
            placeholder="Paste your Hetzner Cloud API token"
            data-testid="delete-deployment-hetzner-token"
            style={{
              marginTop: 4, width: '100%', padding: '4px 8px',
              fontFamily: 'monospace', fontSize: 13,
              background: 'var(--color-bg-2)', color: 'var(--color-text)',
              border: '1px solid var(--color-border)', borderRadius: 4,
            }}
          />
        </label>
      ) : null}

      {error ? (
        <pre
          data-testid="delete-deployment-error"
          style={{
            marginTop: 8, padding: 8, fontSize: 11,
            background: 'var(--color-bg-2)', color: 'var(--color-danger)',
            borderRadius: 4, overflowX: 'auto',
          }}
        >
          {error}
        </pre>
      ) : null}
    </ModalShell>
  )
}
