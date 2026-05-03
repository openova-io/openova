/**
 * DecommissionPage — Sovereign self-decommission surface (issue #319).
 *
 * Reached from:
 *   • Catalyst-Zero ops shell:  /sovereign/decommission/$deploymentId
 *     (operator-driven decommission for an orphan / customer-unreachable
 *      Sovereign — invoked via the Sovereign Admin Dashboard's
 *      Decommission link. Catalyst-Zero still holds the deployment
 *      record + tofu state per the minimum-retention surface.)
 *   • Customer-side admin shell post-handover:
 *     console.<sovereign-fqdn>/sovereign/decommission/$deploymentId
 *     (customer-driven — once #317's handover finalisation has flipped
 *      `adoptedAt`, this same UI is the canonical decommission entry.)
 *
 * Anti-duplication: this page is a thin presentational wrapper around
 * the existing `WipeDeploymentModal` component's confirmation flow +
 * the existing `POST /api/v1/deployments/{id}/wipe` server endpoint.
 * The modal's confirmation logic (typed-FQDN, Hetzner-token re-prompt,
 * SSE-progress-rendering) is reused. The post-handover-specific delta
 * is the optional backup-destination selector and the copy.
 *
 * Scope contract with #317 (do NOT touch handover.go): #317 owns the
 * write side of `adoptedAt` (set by the handover handler). #319 owns
 * the read side (the redirect in router.tsx + this page) plus the
 * decommission flow that drains the minimum-retention surface.
 */

import { useState } from 'react'
import { useParams, useRouter, Link } from '@tanstack/react-router'
import { API_BASE } from '@/shared/config/urls'
import { useDeploymentEvents } from './useDeploymentEvents'
import type { WipeReport } from '@/components/CrudModals/WipeDeploymentModal'

/**
 * Backup destination types (issue body: "Optional: backup destination
 * for the final OpenBao + Gitea + state export").
 *
 * `none` — no backup; the sovereign's PVs are wiped along with the
 *   cluster. Recommended only when all Organizations have already
 *   migrated or have been declared lost per SOVEREIGN-PROVISIONING.md
 *   §10.1.
 * `s3` — push Velero/OpenBao/Gitea exports to an S3 endpoint. Customer
 *   supplies endpoint + bucket + access key + secret key in the form
 *   below.
 * `local` — bundle the exports into a tar.gz the operator downloads
 *   directly. Catalyst-Zero streams it back over the wipe SSE channel.
 */
export type BackupDestinationKind = 'none' | 's3' | 'local'

export interface BackupDestination {
  kind: BackupDestinationKind
  s3?: {
    endpoint: string
    bucket: string
    accessKey: string
    secretKey: string
  }
}

export function DecommissionPage() {
  const { deploymentId } = useParams({ strict: false }) as { deploymentId: string }
  const router = useRouter()
  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream: true,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? 'unknown'

  const [confirmText, setConfirmText] = useState('')
  const [hetznerToken, setHetznerToken] = useState('')
  const [acceptedDataLoss, setAcceptedDataLoss] = useState(false)
  const [backup, setBackup] = useState<BackupDestination>({ kind: 'none' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [report, setReport] = useState<WipeReport | null>(null)

  const fqdnConfirmed = confirmText.trim() === sovereignFQDN
  const hetznerOK = hetznerToken.trim().length > 20
  const backupOK =
    backup.kind === 'none' ||
    backup.kind === 'local' ||
    (backup.kind === 's3' &&
      !!backup.s3?.endpoint &&
      !!backup.s3?.bucket &&
      !!backup.s3?.accessKey &&
      !!backup.s3?.secretKey)

  const ready = fqdnConfirmed && hetznerOK && acceptedDataLoss && backupOK && !busy

  async function performDecommission() {
    setBusy(true)
    setError(null)
    try {
      const res = await fetch(
        `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/wipe`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
          // The wipe endpoint accepts the optional backup field; absent
          // means "no backup" which matches the pre-handover failure
          // recovery semantics. The customer-side decommission ALWAYS
          // surfaces the backup selector — defaulting to `none` — so
          // the omission is an explicit choice, not an implicit one.
          body: JSON.stringify({
            hetznerToken: hetznerToken.trim(),
            backup,
          }),
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

  if (report) {
    return (
      <main
        data-testid="decommission-success"
        className="mx-auto max-w-2xl p-8 text-[var(--color-text)]"
      >
        <h1 className="text-2xl font-semibold">Sovereign decommissioned</h1>
        <p className="mt-2 text-sm text-[var(--color-text-dim)]">
          {report.sovereignFQDN} has been wiped. Hetzner resources removed:{' '}
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
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          PDM allocation released: {report.pdmReleased ? 'yes' : 'n/a (BYO)'} ·
          tofu destroy: {report.tofuDestroyed ? '✓' : '✗'} · local state cleaned: {report.localCleaned ? '✓' : '✗'}
        </p>
        {report.errors && report.errors.length > 0 ? (
          <pre
            data-testid="decommission-errors"
            className="mt-4 rounded-md bg-[var(--color-bg-2)] p-3 text-xs text-amber-300 whitespace-pre-wrap"
          >
            {report.errors.join('\n')}
          </pre>
        ) : null}
        <div className="mt-6">
          <button
            type="button"
            onClick={() => router.navigate({ to: '/wizard' })}
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-2 text-sm hover:border-[var(--color-accent)]"
            data-testid="decommission-finish"
          >
            Provision a new Sovereign
          </button>
        </div>
      </main>
    )
  }

  return (
    <main
      data-testid="decommission-page"
      className="mx-auto max-w-2xl p-8 text-[var(--color-text)]"
    >
      <h1 className="text-2xl font-semibold">Decommission Sovereign</h1>
      <p className="mt-2 text-sm text-[var(--color-text-dim)]">
        This destroys every Hetzner resource for{' '}
        <code className="rounded bg-[var(--color-bg-2)] px-1">{sovereignFQDN}</code>,
        releases the PDM allocation (pool subdomains only), removes the parent-zone NS
        delegation, and wipes the deployment record from Catalyst-Zero. There is no
        undo path — the only way to recover is to provision a new Sovereign.
      </p>
      <ul className="mt-3 list-disc pl-5 text-xs text-[var(--color-text-dim)]">
        <li>tofu destroy against the per-deployment workdir</li>
        <li>Hetzner orphan force-purge (servers, load balancers, networks, firewalls, ssh-keys, S3 buckets)</li>
        <li>PDM allocation release (pool-subdomain only)</li>
        <li>Kubeconfig + workdir + on-disk record removed</li>
      </ul>

      {/* Backup destination */}
      <fieldset
        className="mt-6 rounded-md border border-[var(--color-border)] p-3"
        data-testid="decommission-backup-fieldset"
      >
        <legend className="px-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
          Backup destination (optional)
        </legend>
        <p className="text-xs text-[var(--color-text-dim)]">
          Velero PV snapshot, OpenBao seal-and-export, and Gitea bundle of every
          Application repo are pushed here BEFORE the wipe runs. Skip only if all
          Organizations have already migrated or accepted data loss.
        </p>
        <div className="mt-2 flex gap-3 text-sm">
          <label className="flex items-center gap-1">
            <input
              type="radio"
              name="backup-kind"
              value="none"
              checked={backup.kind === 'none'}
              onChange={() => setBackup({ kind: 'none' })}
              data-testid="decommission-backup-none"
            />
            None (data is permanently lost)
          </label>
          <label className="flex items-center gap-1">
            <input
              type="radio"
              name="backup-kind"
              value="s3"
              checked={backup.kind === 's3'}
              onChange={() =>
                setBackup({ kind: 's3', s3: { endpoint: '', bucket: '', accessKey: '', secretKey: '' } })
              }
              data-testid="decommission-backup-s3"
            />
            S3
          </label>
          <label className="flex items-center gap-1">
            <input
              type="radio"
              name="backup-kind"
              value="local"
              checked={backup.kind === 'local'}
              onChange={() => setBackup({ kind: 'local' })}
              data-testid="decommission-backup-local"
            />
            Local download
          </label>
        </div>
        {backup.kind === 's3' && (
          <div className="mt-3 grid grid-cols-2 gap-2 text-xs">
            <input
              type="text"
              placeholder="https://fsn1.your-objectstorage.com"
              value={backup.s3?.endpoint ?? ''}
              onChange={(e) =>
                setBackup((b) => ({ kind: 's3', s3: { ...(b.s3 ?? { bucket: '', accessKey: '', secretKey: '', endpoint: '' }), endpoint: e.target.value } }))
              }
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1"
              data-testid="decommission-backup-s3-endpoint"
            />
            <input
              type="text"
              placeholder="bucket-name"
              value={backup.s3?.bucket ?? ''}
              onChange={(e) =>
                setBackup((b) => ({ kind: 's3', s3: { ...(b.s3 ?? { bucket: '', accessKey: '', secretKey: '', endpoint: '' }), bucket: e.target.value } }))
              }
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1"
              data-testid="decommission-backup-s3-bucket"
            />
            <input
              type="text"
              placeholder="access key"
              value={backup.s3?.accessKey ?? ''}
              onChange={(e) =>
                setBackup((b) => ({ kind: 's3', s3: { ...(b.s3 ?? { bucket: '', accessKey: '', secretKey: '', endpoint: '' }), accessKey: e.target.value } }))
              }
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1"
              data-testid="decommission-backup-s3-access"
            />
            <input
              type="password"
              placeholder="secret key"
              value={backup.s3?.secretKey ?? ''}
              onChange={(e) =>
                setBackup((b) => ({ kind: 's3', s3: { ...(b.s3 ?? { bucket: '', accessKey: '', secretKey: '', endpoint: '' }), secretKey: e.target.value } }))
              }
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1"
              data-testid="decommission-backup-s3-secret"
            />
          </div>
        )}
      </fieldset>

      {/* Acknowledgement */}
      <label className="mt-6 flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={acceptedDataLoss}
          onChange={(e) => setAcceptedDataLoss(e.target.checked)}
          data-testid="decommission-acknowledge"
        />
        <span>
          I confirm all Organizations on this Sovereign have been migrated or have
          accepted data loss (per SOVEREIGN-PROVISIONING.md §10.1).
        </span>
      </label>

      {/* Typed FQDN */}
      <label className="mt-4 block text-sm">
        Type{' '}
        <code className="rounded bg-[var(--color-bg-2)] px-1 text-xs">{sovereignFQDN}</code>{' '}
        to confirm:
        <input
          type="text"
          value={confirmText}
          onChange={(e) => setConfirmText(e.target.value)}
          disabled={busy}
          data-testid="decommission-confirm-text"
          className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 font-mono text-sm"
        />
      </label>

      {/* Hetzner token */}
      <label className="mt-4 block text-sm">
        Hetzner Cloud API token:
        <input
          type="password"
          value={hetznerToken}
          onChange={(e) => setHetznerToken(e.target.value)}
          disabled={busy}
          placeholder="Paste your Hetzner Cloud API token"
          data-testid="decommission-hetzner-token"
          className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 font-mono text-sm"
        />
      </label>

      {error ? (
        <pre
          data-testid="decommission-error"
          className="mt-4 rounded-md bg-[var(--color-bg-2)] p-3 text-xs text-rose-300 whitespace-pre-wrap"
        >
          {error}
        </pre>
      ) : null}

      <div className="mt-6 flex gap-3">
        <button
          type="button"
          onClick={performDecommission}
          disabled={!ready}
          data-testid="decommission-submit"
          className="rounded-md bg-rose-600 px-4 py-2 text-sm font-medium text-white disabled:cursor-not-allowed disabled:opacity-40"
        >
          {busy ? 'Decommissioning…' : 'Decommission Sovereign'}
        </button>
        <Link
          to="/provision/$deploymentId"
          params={{ deploymentId }}
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-2 text-sm hover:border-[var(--color-accent)]"
          data-testid="decommission-cancel"
        >
          Cancel
        </Link>
      </div>
    </main>
  )
}
