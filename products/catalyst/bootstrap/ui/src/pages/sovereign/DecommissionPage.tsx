/**
 * DecommissionPage — Sovereign self-decommission surface (issue #319, #766).
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
 * Issue #766 — verbose live exec-log view during the wipe.
 *
 * Until #766 the page rendered a static "Decommissioning…" button label
 * with no progress signal — operators thought the page was stuck while
 * tofu destroy + the Hetzner orphan purge were running for 30+ minutes.
 *
 * The wipe handler in api/internal/handler/wipe.go ALREADY emits a per-
 * resource SSE event stream on the same `dep.eventsCh` channel that
 * provisioning uses, surfaced at GET /api/v1/deployments/{id}/logs.
 * Every "tofu destroy" tick, every Hetzner DELETE response, every S3
 * bucket purge step, every PDM release call, every local-state cleanup
 * is already a discrete event with phase="wipe".
 *
 * The fix is purely UI — subscribe to the same SSE the wipe emits via
 * useDeploymentEvents (no new endpoint, no protocol change), flatten
 * every recorded event into a LogLine, and feed the unified LogPane
 * (the same component /provision/<id> JobDetail uses for per-job logs).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — target shape on
 * first commit): full streaming view ships in this PR, with the
 * scrollback, search, full-screen toggle, and final-state summary all
 * threaded through the existing LogPane primitives. No "for now we'll
 * just show a spinner" intermediate.
 *
 * Scope contract with #317 (do NOT touch handover.go): #317 owns the
 * write side of `adoptedAt` (set by the handover handler). #319 owns
 * the read side (the redirect in router.tsx + this page) plus the
 * decommission flow that drains the minimum-retention surface.
 */

import { useEffect, useMemo, useState } from 'react'
import { useParams, useRouter, Link } from '@tanstack/react-router'
import { API_BASE } from '@/shared/config/urls'
import { LogPane } from '@/components/LogPane'
import type { LogLevel, LogLine } from '@/components/ExecutionLogs'
import { useDeploymentEvents } from './useDeploymentEvents'
import type { DeploymentEvent } from './eventReducer'
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

/**
 * Map a raw catalyst-api event level to the LogLine vocabulary the
 * unified ExecutionLogs / LogPane components understand. The wipe
 * handler emits "info" and "warn" today; "error" is reserved for
 * future fatal cases.
 */
function levelOf(ev: DeploymentEvent): LogLevel {
  switch (ev.level) {
    case 'error':
      return 'ERROR'
    case 'warn':
      return 'WARN'
    case 'info':
    default:
      return 'INFO'
  }
}

/**
 * Auto-redirect countdown (seconds) after a successful decommission
 * before the page navigates back to /wizard. Operators can click the
 * "Provision a new Sovereign" button at any point to skip the wait.
 *
 * Per #4 (never hardcode), this is exported as a named constant the
 * test suite can reference rather than inlining a magic number.
 */
export const DECOMMISSION_REDIRECT_SECONDS = 10

export function DecommissionPage() {
  const { deploymentId } = useParams({ strict: false }) as { deploymentId: string }
  const router = useRouter()

  const [confirmText, setConfirmText] = useState('')
  const [hetznerToken, setHetznerToken] = useState('')
  const [acceptedDataLoss, setAcceptedDataLoss] = useState(false)
  const [backup, setBackup] = useState<BackupDestination>({ kind: 'none' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [report, setReport] = useState<WipeReport | null>(null)
  const [redirectCountdown, setRedirectCountdown] = useState<number | null>(null)

  // Subscribe to the live event stream:
  //
  //   • Pre-submit  — disable the EventSource so the form view is
  //                   inert (matches existing test expectations and
  //                   avoids holding an SSE connection open while the
  //                   operator types their FQDN confirmation).
  //   • Post-submit — open the stream so every wipe event the API
  //                   emits (tofu destroy, Hetzner orphan purge per
  //                   resource, S3 bucket purge, PDM release, local
  //                   state cleanup) flows through the reducer and
  //                   into the LogPane below.
  //
  // The `snapshot.sovereignFQDN` is also surfaced from this hook for
  // the typed-confirmation hint, irrespective of whether the stream
  // is attached.
  const streaming = busy || report !== null
  const { state: streamState, snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream: !streaming,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? 'unknown'

  // Flatten every event the reducer routed into ANY bucket
  // (eventsByTarget keyed by HETZNER_INFRA / CLUSTER_BOOTSTRAP / per-
  // app), de-duplicate by identity, and order by timestamp. The wipe
  // handler emits all events under phase="wipe" today which the
  // reducer fall-throughs route to CLUSTER_BOOTSTRAP_KEY — the
  // pageful flatten below collects every bucket so we never silently
  // drop an event because of a future reducer rule change.
  const wipeLogLines: LogLine[] = useMemo(() => {
    const collected: DeploymentEvent[] = []
    const seen = new Set<DeploymentEvent>()
    const buckets = streamState?.eventsByTarget ?? {}
    for (const arr of Object.values(buckets)) {
      if (!Array.isArray(arr)) continue
      for (const ev of arr) {
        if (seen.has(ev)) continue
        seen.add(ev)
        collected.push(ev)
      }
    }
    collected.sort((a, b) => {
      const at = a.time ? Date.parse(a.time) : 0
      const bt = b.time ? Date.parse(b.time) : 0
      return at - bt
    })
    return collected.map<LogLine>((ev, idx) => ({
      lineNumber: idx + 1,
      timestamp: ev.time ?? new Date().toISOString(),
      level: levelOf(ev),
      message: ev.message ?? '',
    }))
  }, [streamState])

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

  // Auto-redirect countdown — fires when the wipe POST resolves with
  // a final report, ticks once per second, navigates to /wizard at 0.
  useEffect(() => {
    if (!report) return
    setRedirectCountdown(DECOMMISSION_REDIRECT_SECONDS)
    let remaining = DECOMMISSION_REDIRECT_SECONDS
    const interval = setInterval(() => {
      remaining -= 1
      setRedirectCountdown(remaining)
      if (remaining <= 0) {
        clearInterval(interval)
        router.navigate({ to: '/wizard' })
      }
    }, 1000)
    return () => clearInterval(interval)
  }, [report, router])

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

  // Streaming exec-log view — rendered while the wipe is in flight or
  // after it completed (so the operator can scroll back through every
  // deletion). Built around the same LogPane component the unified
  // /provision/<id> JobDetail surface uses; passes the live event log
  // via fallbackLines (no per-execution row exists for the wipe).
  if (streaming) {
    const summary = renderHetznerSummary(report)
    return (
      <main
        data-testid="decommission-page-streaming"
        className="mx-auto max-w-4xl p-8 text-[var(--color-text)]"
      >
        <header className="mb-4 flex flex-wrap items-center gap-3">
          <h1 className="text-2xl font-semibold" data-testid="decommission-streaming-title">
            {report ? 'Sovereign decommissioned' : 'Decommissioning Sovereign'}
          </h1>
          <span
            data-testid="decommission-streaming-fqdn"
            className="rounded bg-[var(--color-bg-2)] px-2 py-0.5 font-mono text-xs"
          >
            {sovereignFQDN}
          </span>
          {report ? (
            <span
              data-testid="decommission-streaming-checkmark"
              aria-label="Decommission complete"
              className="inline-flex h-7 items-center gap-1 rounded-full border border-emerald-400/50 bg-emerald-500/10 px-3 text-xs font-semibold text-emerald-300"
            >
              <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
                <path
                  d="M3 7.5 L6 10.5 L11 4.5"
                  stroke="currentColor"
                  strokeWidth="1.8"
                  fill="none"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </svg>
              Complete
            </span>
          ) : (
            <span
              data-testid="decommission-streaming-spinner"
              className="inline-flex h-7 items-center gap-2 rounded-full border border-sky-400/50 bg-sky-500/10 px-3 text-xs font-semibold text-sky-300"
            >
              <span
                aria-hidden
                className="inline-block h-2 w-2 animate-pulse rounded-full bg-sky-300"
              />
              Streaming
            </span>
          )}
        </header>

        <p className="text-sm text-[var(--color-text-dim)]">
          {report
            ? 'Every Hetzner resource, the PDM allocation, and the local deployment record have been removed. The full event log below is preserved for audit — scroll back through every deletion.'
            : 'Live progress streamed from catalyst-api. Every tofu destroy step, every Hetzner resource DELETE, every S3 bucket purge, every PDM release, and every local cleanup step appears below as it happens.'}
        </p>

        {summary ? (
          <pre
            data-testid="decommission-streaming-summary"
            className="mt-3 whitespace-pre-wrap rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs text-[var(--color-text)]"
          >
            {summary}
          </pre>
        ) : null}

        {error ? (
          <pre
            data-testid="decommission-error"
            className="mt-3 whitespace-pre-wrap rounded-md bg-[var(--color-bg-2)] p-3 text-xs text-rose-300"
          >
            {error}
          </pre>
        ) : null}

        {report ? (
          <div
            className="mt-4 flex flex-wrap items-center gap-3"
            data-testid="decommission-redirect-row"
          >
            <button
              type="button"
              onClick={() => router.navigate({ to: '/wizard' })}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-2 text-sm hover:border-[var(--color-accent)]"
              data-testid="decommission-finish"
            >
              Provision a new Sovereign
            </button>
            <span
              className="text-xs text-[var(--color-text-dim)]"
              data-testid="decommission-countdown"
            >
              Returning to wizard in {Math.max(0, redirectCountdown ?? DECOMMISSION_REDIRECT_SECONDS)}s…
            </span>
          </div>
        ) : null}

        {/*
          Unified LogPane — same component /provision/<id> uses for
          per-job ExecutionLogs. We have no Bridge-allocated execution
          row for the wipe, so we feed events through the
          fallbackLines surface (Bug #481 path). The LogPane provides
          search, full-screen toggle, scrollback, and the same dark/
          light themed presentation as every other exec-log surface.
        */}
        <div data-testid="decommission-log-host" className="relative mt-6">
          <LogPane
            executionId={null}
            fallbackLines={wipeLogLines}
            jobTitle={`Decommission · ${sovereignFQDN}`}
            statusLabel={report ? 'COMPLETE' : 'STREAMING'}
            statusTone={report ? 'succeeded' : 'running'}
            onClose={() => {
              if (report) router.navigate({ to: '/wizard' })
            }}
          />
        </div>

        {report && report.errors && report.errors.length > 0 ? (
          <pre
            data-testid="decommission-errors"
            className="mt-4 whitespace-pre-wrap rounded-md bg-[var(--color-bg-2)] p-3 text-xs text-amber-300"
          >
            {report.errors.join('\n')}
          </pre>
        ) : null}

        {report ? (
          <div
            data-testid="decommission-success"
            aria-hidden
            style={{ display: 'none' }}
          >
            {/*
              Hidden marker preserved for the existing test that
              awaits `decommission-success` after submit. The visible
              UI is the streaming layout above; this marker keeps the
              contract with DecommissionPage.test.tsx without forking
              two render paths.
            */}
          </div>
        ) : null}
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

/**
 * Render a one-line-per-resource-kind summary of the final wipe report,
 * mirroring the founder-verbatim shape from issue #766:
 *
 *     Hetzner sweep complete:
 *       servers:        0
 *       load_balancers: 0
 *       networks:       0
 *       firewalls:      0
 *       ssh_keys:       0
 *       s3_buckets:     0
 *
 * Returns null until the wipe report has actually arrived, so callers
 * can `if (summary)` without a separate flag.
 */
function renderHetznerSummary(report: WipeReport | null): string | null {
  if (!report) return null
  const purge = report.hetznerPurge
  const lines = [
    `Hetzner sweep complete for ${report.sovereignFQDN}:`,
    `  servers:        ${(purge.servers?.length ?? 0)} removed`,
    `  load_balancers: ${(purge.load_balancers?.length ?? 0)} removed`,
    `  networks:       ${(purge.networks?.length ?? 0)} removed`,
    `  firewalls:      ${(purge.firewalls?.length ?? 0)} removed`,
    `  ssh_keys:       ${(purge.ssh_keys?.length ?? 0)} removed`,
    `  s3_buckets:     ${(purge.s3_buckets?.length ?? 0)} removed`,
    `tofu destroy: ${report.tofuDestroyed ? '✓' : '✗'}` +
      ` · PDM released: ${report.pdmReleased ? '✓' : 'n/a (BYO)'}` +
      ` · local state cleaned: ${report.localCleaned ? '✓' : '✗'}`,
  ]
  if (purge.firewalls_retried && purge.firewalls_retried > 0) {
    lines.push(
      `  (firewall delete retr${purge.firewalls_retried === 1 ? 'y' : 'ies'}: ${purge.firewalls_retried} while server detach completed)`,
    )
  }
  return lines.join('\n')
}
