/**
 * WipeDeploymentModal — deployment-level destructive op (issue #318).
 *
 * Distinct from DeleteCascadeConfirm in scope:
 *
 * • DeleteCascadeConfirm — DAY-2 path. Operator deletes a single
 *   resource (region / cluster / vCluster / pool / lb / peering / etc).
 *   The path runs through Crossplane: a DELETE request flips the
 *   underlying XRC's deletionPolicy to Delete, the Composition cascades
 *   to the matching managed resources, the cloud provider eventually
 *   reaps. Per docs/INVIOLABLE-PRINCIPLES.md #3 (Crossplane is the
 *   ONLY day-2 IaC).
 *
 * • WipeDeploymentModal (this file) — PHASE-0 RECOVERY path. The
 *   deployment failed before handover (catalyst-api restarted mid-apply,
 *   bootstrap-kit wedged, etc). No XRCs exist because Crossplane never
 *   adopted the resources. The wipe endpoint runs `tofu destroy` against
 *   the per-deployment workdir AND a Hetzner-direct orphan purge as a
 *   safety net (also drains PDM allocation + parent-zone NS for pool
 *   subdomains, deletes kubeconfig + on-disk record). This is the
 *   sanctioned Phase-0 fallback per feedback_idempotent_iac_purge.md.
 *
 * Both modals share the ModalShell + same testid prefix conventions; the
 * surface that opens this one is the wizard's failed-state banner
 * (AppsPage FailureCard) and the Cloud → Architecture canvas's `cloud`
 * node detail panel.
 */

import { useState } from 'react'
import { ModalShell } from './_shared'
import { API_BASE } from '@/shared/config/urls'

export interface WipeDeploymentModalProps {
  open: boolean
  deploymentId: string
  sovereignFQDN: string | null
  onClose: () => void
  /** Called after the operator clicks "Start fresh deployment" on the
   *  success view. Typically navigates back to /wizard. */
  onWiped: () => void
}

export interface WipeReport {
  deploymentId: string
  sovereignFQDN: string
  tofuDestroyed: boolean
  hetznerPurge: {
    servers?: string[]
    load_balancers?: string[]
    networks?: string[]
    firewalls?: string[]
    ssh_keys?: string[]
    /** Issue #706 — Hetzner Object Storage buckets the wipe handler
     *  emptied + deleted via the S3 API (tofu destroy can't remove
     *  them while objects exist). One entry per per-Sovereign bucket
     *  cleaned. */
    s3_buckets?: string[]
    /** Number of firewall-delete retries needed because the server
     *  detach was still in flight (issue #706). 0 = no retries
     *  necessary; >0 = the firewall was attached when we first tried
     *  but eventually went through. Surfaced for operator awareness;
     *  the actual deletion success is encoded in the firewalls list. */
    firewalls_retried?: number
    errors?: string[]
  }
  pdmReleased: boolean
  localCleaned: boolean
  errors?: string[]
  wipedAt: string
}

export function WipeDeploymentModal({
  open,
  deploymentId,
  sovereignFQDN,
  onClose,
  onWiped,
}: WipeDeploymentModalProps) {
  const [confirmText, setConfirmText] = useState('')
  const [hetznerToken, setHetznerToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [report, setReport] = useState<WipeReport | null>(null)

  const requiredText = sovereignFQDN ?? deploymentId
  const ready = confirmText.trim() === requiredText && hetznerToken.trim().length > 20 && !busy

  async function performWipe() {
    setBusy(true)
    setError(null)
    try {
      const res = await fetch(
        `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/wipe`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
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
      setReport(parsed)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  // Two-stage modal: pre-wipe confirmation, post-wipe summary.
  if (!report) {
    return (
      <ModalShell
        id="wipe-deployment"
        open={open}
        title="Cancel & Wipe deployment"
        subtitle={requiredText}
        onClose={() => { if (!busy) onClose() }}
        primary={{
          label: busy ? 'Wiping…' : 'Wipe everything',
          onClick: performWipe,
          disabled: !ready,
          loading: busy,
          danger: true,
        }}
        secondary={{ label: 'Keep deployment', onClick: onClose }}
      >
        <p style={{ marginTop: 0 }}>
          This destroys every Hetzner resource tagged{' '}
          <code style={{ background: 'var(--color-bg-2)', padding: '0 4px', borderRadius: 3 }}>
            catalyst-deployment-id={deploymentId.slice(0, 12)}…
          </code>{' '}
          and removes all local state on Catalyst-Zero. Per the founder's
          minimum-retention principle, no operational footprint of this
          deployment will remain on console.openova.io.
        </p>
        <ul style={{ margin: '8px 0', paddingLeft: 20, fontSize: 12, color: 'var(--color-text-dim)' }}>
          <li>tofu destroy against the per-deployment workdir</li>
          <li>Hetzner orphan force-purge (servers, load balancers, networks, firewalls, ssh-keys, S3 buckets)</li>
          <li>PDM allocation release (pool-subdomain only)</li>
          <li>Kubeconfig + workdir + on-disk record removed</li>
        </ul>
        <label style={{ display: 'block', marginTop: 12 }}>
          <span style={{ fontSize: 12, color: 'var(--color-text-dim)' }}>
            Type <code style={{ background: 'var(--color-bg-2)', padding: '0 4px', borderRadius: 3 }}>{requiredText}</code> to confirm:
          </span>
          <input
            type="text"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            disabled={busy}
            data-testid="wipe-deployment-confirm-text"
            style={{
              marginTop: 4, width: '100%', padding: '4px 8px', fontFamily: 'monospace', fontSize: 13,
              background: 'var(--color-bg-2)', color: 'var(--color-text)',
              border: '1px solid var(--color-border)', borderRadius: 4,
            }}
          />
        </label>
        <label style={{ display: 'block', marginTop: 12 }}>
          <span style={{ fontSize: 12, color: 'var(--color-text-dim)' }}>
            Hetzner Cloud API token (re-prompt for security):
          </span>
          <input
            type="password"
            value={hetznerToken}
            onChange={(e) => setHetznerToken(e.target.value)}
            disabled={busy}
            placeholder="Paste your Hetzner Cloud API token"
            data-testid="wipe-deployment-hetzner-token"
            style={{
              marginTop: 4, width: '100%', padding: '4px 8px', fontFamily: 'monospace', fontSize: 13,
              background: 'var(--color-bg-2)', color: 'var(--color-text)',
              border: '1px solid var(--color-border)', borderRadius: 4,
            }}
          />
        </label>
        {error ? (
          <pre
            data-testid="wipe-deployment-error"
            style={{ marginTop: 8, padding: 8, fontSize: 11, background: 'var(--color-bg-2)', color: 'var(--color-danger)', borderRadius: 4, overflowX: 'auto' }}
          >
            {error}
          </pre>
        ) : null}
      </ModalShell>
    )
  }

  // Success view.
  return (
    <ModalShell
      id="wipe-deployment"
      open={open}
      title="Wipe complete"
      subtitle={report.sovereignFQDN}
      onClose={onWiped}
      primary={{
        label: 'Start fresh deployment',
        onClick: onWiped,
      }}
    >
      <p style={{ marginTop: 0, fontSize: 12, color: 'var(--color-text-dim)' }}>
        Hetzner resources removed:{' '}
        {(report.hetznerPurge.servers?.length ?? 0)} servers,{' '}
        {(report.hetznerPurge.load_balancers?.length ?? 0)} load balancers,{' '}
        {(report.hetznerPurge.networks?.length ?? 0)} networks,{' '}
        {(report.hetznerPurge.firewalls?.length ?? 0)} firewalls,{' '}
        {(report.hetznerPurge.ssh_keys?.length ?? 0)} ssh-keys,{' '}
        {(report.hetznerPurge.s3_buckets?.length ?? 0)} S3 buckets.
        {(report.hetznerPurge.firewalls_retried ?? 0) > 0 ? (
          <>
            {' '}
            <span>
              ({report.hetznerPurge.firewalls_retried} firewall delete retr
              {report.hetznerPurge.firewalls_retried === 1 ? 'y' : 'ies'} while
              server detach completed)
            </span>
          </>
        ) : null}
      </p>
      <p style={{ marginTop: 4, fontSize: 12, color: 'var(--color-text-dim)' }}>
        Tofu destroy: {report.tofuDestroyed ? '✓' : '✗'} · PDM released: {report.pdmReleased ? '✓' : 'n/a'} · Local state cleaned: {report.localCleaned ? '✓' : '✗'}
      </p>
      {report.errors && report.errors.length > 0 ? (
        <pre
          data-testid="wipe-deployment-report-errors"
          style={{ marginTop: 8, padding: 8, fontSize: 11, background: 'var(--color-bg-2)', color: 'var(--color-warn)', borderRadius: 4, overflowX: 'auto' }}
        >
          {report.errors.join('\n')}
        </pre>
      ) : null}
    </ModalShell>
  )
}
