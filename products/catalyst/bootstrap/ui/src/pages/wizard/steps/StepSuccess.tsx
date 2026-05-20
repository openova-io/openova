/**
 * StepSuccess — terminal wizard step rendered AFTER all 11 bootstrap phases
 * finish green. Closes GitHub issue #126:
 *
 *   "[I] ux: success state — link to new console + show first-time admin
 *    login flow"
 *
 * Responsibilities (per the issue body + task brief):
 *
 *   1. Primary CTA: open the new Sovereign's console at
 *        https://console.<sovereign-fqdn>/
 *      Domain comes from wizard state via resolveSovereignDomain() — never
 *      hardcoded (Inviolable-Principle #4).
 *
 *   2. First-time admin login flow:
 *        - Username = `admin@<sovereign-fqdn>`
 *        - One-time login URL minted by the catalyst-api when the endpoint
 *          GET /api/v1/deployments/<id>/admin-login-url is implemented.
 *        - Until the endpoint exists, fall back to the documented Keycloak
 *          realm-master + reset-password procedure
 *          (docs/RUNBOOK-PROVISIONING.md §First-time-admin-login).
 *
 *   3. kubeconfig download — fetches /api/v1/deployments/<id>/kubeconfig.
 *      If the endpoint is not yet implemented, the button shows the
 *      "Coming soon — fetch via SSH" copy with a runbook link.
 *
 *   4. Voucher-issuance shortcut — secondary CTA pointing at
 *        https://console.<sovereign-fqdn>/bss/vouchers
 *      (BSS menu inside the operator console — voucher operations
 *      live under the BSS sidebar, NOT under any `admin.*`
 *      subdomain; see CLAUDE.md §0 canon + TBD-V20).
 *
 *   5. SSE final-state log tail (last 20 lines) collapsed/expandable.
 *
 *   6. Link to /docs in the new Sovereign for self-serve onboarding.
 *
 * URL hygiene: every external href is computed from `lastProvisionResult`
 * (preferred — supplied by the catalyst-api `done` event) or from
 * resolveSovereignDomain(state) as a fallback. NEVER hardcoded.
 *
 * Visual hygiene: matches the StepReview / StepOrg style — inline styles
 * keyed off the `--wiz-*` design-token CSS variables defined in the Wizard
 * shell, NOT global Tailwind classes (the wizard steps use the inline
 * pattern; the legacy /pages/success/SuccessPage.tsx uses Tailwind for the
 * SaaS variant, which is a different surface).
 */

import { useState } from 'react'
import {
  CheckCircle2,
  ExternalLink,
  Copy,
  Check,
  Download,
  Ticket,
  BookOpen,
  ChevronDown,
  ChevronUp,
  Terminal,
  KeyRound,
} from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  resolveSovereignDomain,
  type ProvisionResult,
} from '@/entities/deployment/model'
import { API_BASE } from '@/shared/config/urls'
import { StepShell } from './_shared'

/* ── Constants — central, not hardcoded literals scattered ─────────── */

/**
 * Anchor on the canonical runbook for the first-time admin login flow.
 * Per Inviolable-Principle #4, this URL is overridable via prop so air-gap
 * deployments can swap in their internal copy.
 */
export const DEFAULT_FIRST_LOGIN_DOCS_URL =
  'https://github.com/openova-io/openova/blob/main/docs/RUNBOOK-PROVISIONING.md#4-first-login'

/**
 * Anchor on the runbook for the SSH-based kubeconfig fetch (used while the
 * API endpoint is not yet implemented).
 */
export const DEFAULT_KUBECONFIG_DOCS_URL =
  'https://github.com/openova-io/openova/blob/main/docs/RUNBOOK-PROVISIONING.md#fetch-kubeconfig-via-ssh'

/* ── URL helpers — every URL flows through these so no hostname ever
 *    appears as a literal in the JSX. ───────────────────────────────── */

/** Sub-host on the new Sovereign — `<sub>.<fqdn>` if fqdn present, else ''. */
export function sovereignSubURL(fqdn: string, sub: string): string {
  const f = (fqdn ?? '').trim()
  if (!f) return ''
  return `https://${sub}.${f}`
}

/** Catalyst-API one-time admin-login-URL endpoint, scoped to this deployment. */
export function adminLoginAPIPath(deploymentId: string | null): string | null {
  if (!deploymentId) return null
  return `${API_BASE}/v1/deployments/${deploymentId}/admin-login-url`
}

/** Catalyst-API kubeconfig endpoint, scoped to this deployment. */
export function kubeconfigAPIPath(deploymentId: string | null): string | null {
  if (!deploymentId) return null
  return `${API_BASE}/v1/deployments/${deploymentId}/kubeconfig`
}

/**
 * Resolve the kubeconfig context name the same way the catalyst-api
 * does on the GET /kubeconfig path (issue #765). Order:
 *
 *   1. SovereignSubdomain — when set by the wizard for pool-mode.
 *   2. First label of FQDN — for BYO-domain deployments.
 *   3. The literal "sovereign" — last-resort fallback.
 *
 * Used to label the download filename + the `k9s --context=...`
 * snippet in the merge command so the operator's mental model
 * matches what's actually in the file.
 *
 * The sanitiser is RFC-1123-ish (lowercase alnum + dash) to mirror
 * the backend's `sanitiseContextName` so both sides agree.
 */
export function sovereignContextName(subdomain: string, fqdn: string): string {
  const fromSubdomain = sanitiseLabel(subdomain)
  if (fromSubdomain) return fromSubdomain
  const trimmed = (fqdn ?? '').trim()
  if (trimmed) {
    const firstLabel = trimmed.split('.')[0]
    const fromFqdn = sanitiseLabel(firstLabel)
    if (fromFqdn) return fromFqdn
  }
  return 'sovereign'
}

function sanitiseLabel(input: string): string {
  const lower = (input ?? '').trim().toLowerCase()
  if (!lower) return ''
  let out = ''
  for (const ch of lower) {
    if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch === '-') {
      out += ch
    }
  }
  // Trim leading/trailing dashes per RFC-1123 label rules.
  return out.replace(/^-+|-+$/g, '')
}

/**
 * Build the one-liner the operator pastes into a shell to merge the
 * downloaded kubeconfig into `$HOME/.kube/config` and immediately
 * launch k9s on the new context (issue #765).
 *
 * The command:
 *   - Uses `KUBECONFIG=$HOME/.kube/config:<path>` to compose the
 *     existing config with the freshly-downloaded one.
 *   - Pipes through `kubectl config view --flatten` so the CA / token
 *     bytes inline (output is self-contained — no path references).
 *   - Writes back to a temp file then atomically moves it onto
 *     $HOME/.kube/config so a Ctrl-C mid-pipe never truncates the
 *     operator's existing config.
 *   - Sets mode 0600 (kubectl warns if perms are looser).
 *   - Uses `k9s --context=<name>` so the operator lands in the new
 *     cluster the same second they run the command.
 *
 * The operator drops their downloaded kubeconfig into
 * `~/Downloads/<contextName>.yaml` (the default browser-download
 * location matching the Content-Disposition filename the API sets).
 */
export function buildKubeconfigMergeCommand(downloadFilename: string, contextName: string): string {
  const path = `$HOME/Downloads/${downloadFilename}`
  return [
    `KUBECONFIG=$HOME/.kube/config:${path}`,
    `kubectl config view --flatten > $HOME/.kube/config.tmp`,
    `&& mv $HOME/.kube/config.tmp $HOME/.kube/config`,
    `&& chmod 600 $HOME/.kube/config`,
    `&& k9s --context=${contextName}`,
  ].join(' ')
}

/* ── Small UI helpers ─────────────────────────────────────────────── */

function CopyChip({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard write can fail in air-gap browsers without permission;
      // fall back to a select-all hint by leaving the visual unchanged.
    }
  }
  return (
    <button
      type="button"
      onClick={copy}
      aria-label={label}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 4,
        padding: '3px 8px', borderRadius: 5,
        border: '1px solid var(--wiz-border-sub)', background: 'transparent',
        color: 'var(--wiz-text-md)', fontSize: 10, fontWeight: 600,
        cursor: 'pointer', fontFamily: 'Inter, sans-serif',
      }}
    >
      {copied ? <Check size={11} /> : <Copy size={11} />}
      {copied ? 'Copied' : label}
    </button>
  )
}

function Section({ title, children }: { title: React.ReactNode; children: React.ReactNode }) {
  return (
    <div
      style={{
        borderRadius: 10,
        border: '1px solid var(--wiz-border-sub)',
        background: 'var(--wiz-bg-xs)',
        overflow: 'hidden',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      <div style={{ padding: '6px 14px', borderBottom: '1px solid var(--wiz-border-sub)', flexShrink: 0 }}>
        <span style={{
          fontSize: 10, fontWeight: 700, letterSpacing: '0.12em',
          textTransform: 'uppercase', color: 'var(--wiz-text-sub)',
        }}>{title}</span>
      </div>
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>{children}</div>
    </div>
  )
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div style={{
      display: 'flex', alignItems: 'flex-start',
      padding: '7px 14px', borderBottom: '1px solid var(--wiz-border-sub)',
    }}>
      <span style={{
        width: 130, flexShrink: 0,
        fontSize: 10, fontWeight: 500, color: 'var(--wiz-text-sub)',
        lineHeight: 1.45,
      }}>{label}</span>
      <span style={{
        fontSize: 11, color: 'var(--wiz-text-md)',
        lineHeight: 1.45, wordBreak: 'break-all', flex: 1,
        fontFamily: 'JetBrains Mono, monospace',
      }}>{value}</span>
    </div>
  )
}

/* ── Public component ─────────────────────────────────────────────── */

export interface StepSuccessProps {
  /**
   * Override URL for the first-time-login runbook anchor. Defaults to the
   * canonical anchor on docs/RUNBOOK-PROVISIONING.md.
   */
  firstLoginDocsURL?: string
  /**
   * Override URL for the kubeconfig SSH-fetch runbook anchor. Defaults to
   * docs/RUNBOOK-PROVISIONING.md#fetch-kubeconfig-via-ssh.
   */
  kubeconfigDocsURL?: string
  /**
   * Last 20 lines of the SSE log tail — passed in by the parent
   * (StepProvisioning publishes `events.slice(-20).map(e => e.message)` once
   * the stream completes). When omitted, the section renders a placeholder.
   */
  finalLogTail?: string[]
  /**
   * Test-only override for the provision result. Production callers omit
   * this — the component reads `lastProvisionResult` from the wizard store.
   */
  resultOverride?: ProvisionResult | null
}

export function StepSuccess({
  firstLoginDocsURL = DEFAULT_FIRST_LOGIN_DOCS_URL,
  kubeconfigDocsURL = DEFAULT_KUBECONFIG_DOCS_URL,
  finalLogTail,
  resultOverride,
}: StepSuccessProps = {}) {
  const store = useWizardStore()

  // Derive the Sovereign FQDN. The catalyst-api `done` event populates
  // lastProvisionResult.sovereignFQDN authoritatively; if for some reason
  // we landed on this step before that event (e.g. user navigated by URL),
  // fall back to the wizard's chosen domain.
  const result = resultOverride !== undefined ? resultOverride : store.lastProvisionResult
  const fallbackFQDN = resolveSovereignDomain(store)
  const fqdn = result?.sovereignFQDN || fallbackFQDN

  const deploymentId = store.deploymentId

  // Computed URLs — every one of them goes through sovereignSubURL().
  const consoleURL = result?.consoleURL || sovereignSubURL(fqdn, 'console')
  const docsURL    = sovereignSubURL(fqdn, 'docs')
  // Voucher CTA points at the BSS menu inside the operator console.
  // Per CLAUDE.md §0 canon: voucher + billing operations live at
  // `console.<fqdn>/bss/vouchers` — NOT under any `admin.*` subdomain
  // (the legacy `admin.<sov>/billing/vouchers/new` URL is anti-canon).
  // See TBD-V20.
  const voucherURL = consoleURL ? `${consoleURL.replace(/\/$/, '')}/bss/vouchers` : ''

  const adminUsername = fqdn ? `admin@${fqdn}` : ''

  /* ── First-time admin login: fetch the one-time URL from the catalyst-api.
   *    If the endpoint returns 404/501, surface the Keycloak fallback. ── */

  const [oneTimeURL, setOneTimeURL] = useState<string | null>(null)
  const [oneTimeError, setOneTimeError] = useState<string | null>(null)
  const [loadingOneTime, setLoadingOneTime] = useState(false)

  async function fetchOneTimeURL() {
    const path = adminLoginAPIPath(deploymentId)
    if (!path) {
      setOneTimeError('No deployment id available — cannot mint a one-time URL.')
      return
    }
    setLoadingOneTime(true)
    setOneTimeError(null)
    try {
      const res = await fetch(path, { headers: { 'Accept': 'application/json' } })
      if (res.status === 404 || res.status === 501) {
        setOneTimeError('not-implemented')
        return
      }
      if (!res.ok) {
        setOneTimeError(`Backend returned ${res.status}`)
        return
      }
      const data = await res.json()
      if (data?.url && typeof data.url === 'string') {
        setOneTimeURL(data.url)
      } else {
        setOneTimeError('Backend response missing `url` field.')
      }
    } catch (err) {
      setOneTimeError(`Network error: ${String(err)}`)
    } finally {
      setLoadingOneTime(false)
    }
  }

  /* ── kubeconfig download + merge command (issue #765) ──────────────
   *
   * The catalyst-api GET /kubeconfig endpoint rewrites the cluster /
   * context / user names from k3s's hardcoded `default` to the
   * Sovereign's subdomain (e.g. `otech94`), so the operator can run
   *
   *   KUBECONFIG=$HOME/.kube/config:~/Downloads/otech94.yaml \
   *     kubectl config view --flatten > $HOME/.kube/config && \
   *     chmod 600 $HOME/.kube/config && k9s --context=otech94
   *
   * after a single click. The "Copy merge command" button below
   * copies that one-liner so the operator pastes it directly into a
   * shell — no manual sed pipeline.
   */

  const contextName = sovereignContextName(store.sovereignSubdomain, fqdn)
  const downloadFilename = contextName + '.yaml'
  const mergeCommand = buildKubeconfigMergeCommand(downloadFilename, contextName)

  const [kubeconfigError, setKubeconfigError] = useState<string | null>(null)
  const [downloadingKubeconfig, setDownloadingKubeconfig] = useState(false)
  const [mergeCopied, setMergeCopied] = useState(false)

  async function downloadKubeconfig() {
    const path = kubeconfigAPIPath(deploymentId)
    if (!path) {
      setKubeconfigError('No deployment id available — cannot fetch kubeconfig.')
      return
    }
    setDownloadingKubeconfig(true)
    setKubeconfigError(null)
    try {
      const res = await fetch(path, { headers: { 'Accept': 'application/yaml' } })
      if (res.status === 404 || res.status === 501) {
        setKubeconfigError('not-implemented')
        return
      }
      if (!res.ok) {
        setKubeconfigError(`Backend returned ${res.status}`)
        return
      }
      const yaml = await res.text()
      const blob = new Blob([yaml], { type: 'application/yaml' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = downloadFilename
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      setKubeconfigError(`Network error: ${String(err)}`)
    } finally {
      setDownloadingKubeconfig(false)
    }
  }

  async function copyMergeCommand() {
    try {
      await navigator.clipboard.writeText(mergeCommand)
      setMergeCopied(true)
      setTimeout(() => setMergeCopied(false), 2500)
    } catch {
      // Air-gap browser without clipboard permission — fall through
      // silently. The command stays visible in the inline <code>
      // block so the operator can select-copy.
    }
  }

  /* ── SSE final-state log tail expander ────────────────────────────── */

  const [logExpanded, setLogExpanded] = useState(false)
  const tail = (finalLogTail ?? []).slice(-20)

  /* ── StepShell wiring ─────────────────────────────────────────────── */

  // The success step's primary "Continue" button opens the new console.
  // There is no further wizard step — clicking Continue navigates the
  // browser to the Sovereign's console subdomain.
  function onNext() {
    if (consoleURL) window.location.href = consoleURL
  }

  return (
    <StepShell
      title="Your Sovereign is live"
      description="Bootstrap-kit phase 11 reported READY. The 6 cluster Environments are reconciling Blueprints from your new Gitea organisation. Use the buttons below to sign in for the first time, download a kubeconfig, and issue your first voucher."
      onNext={onNext}
      nextLabel={
        <>
          Open console
          <ExternalLink size={13} style={{ marginLeft: 5 }} />
        </>
      }
      nextDisabled={!consoleURL}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>

        {/* ── Hero ── */}
        <div
          role="status"
          aria-label="Sovereign provisioning succeeded"
          style={{
            display: 'flex', alignItems: 'flex-start', gap: 12,
            padding: '14px 16px', borderRadius: 10,
            border: '1px solid rgba(74,222,128,0.30)',
            background: 'rgba(74,222,128,0.06)',
          }}
        >
          <CheckCircle2 size={28} color="#4ADE80" style={{ flexShrink: 0, marginTop: 1 }} />
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 14, fontWeight: 700, color: 'var(--wiz-text-hi)' }}>
              {fqdn || 'sovereign'} is ready
            </span>
            <span style={{ fontSize: 11, color: 'var(--wiz-text-sub)', lineHeight: 1.55 }}>
              All 11 bootstrap-kit phases finished green. Your control-plane is reachable
              at the URLs below. The first administrator account
              <strong style={{ color: 'var(--wiz-text-hi)', fontWeight: 700 }}> {adminUsername || 'admin@<sovereign>'} </strong>
              must complete the first-time login flow before the cluster accepts user traffic.
            </span>
          </div>
        </div>

        {/* ── Primary CTA — open the new console ── */}
        <Section title="Console — primary entrypoint">
          <Row label="Console URL"      value={
            consoleURL
              ? <a data-testid="console-url" href={consoleURL} target="_blank" rel="noopener noreferrer"
                   style={{ color: 'var(--wiz-accent)', textDecoration: 'none' }}>
                  {consoleURL}
                  <ExternalLink size={10} style={{ marginLeft: 5, opacity: 0.7 }} />
                </a>
              : <span style={{ color: 'var(--wiz-text-hint)' }}>—</span>
          } />
          <Row label="Sovereign FQDN"   value={fqdn || <span style={{ color: 'var(--wiz-text-hint)' }}>—</span>} />
          {result?.controlPlaneIP && (
            <Row label="Control plane"  value={result.controlPlaneIP} />
          )}
          {result?.loadBalancerIP && (
            <Row label="Load balancer"  value={result.loadBalancerIP} />
          )}
          {result?.gitopsRepoURL && (
            <Row label="GitOps repo"    value={
              <a data-testid="gitops-url" href={result.gitopsRepoURL} target="_blank" rel="noopener noreferrer"
                 style={{ color: 'var(--wiz-accent)', textDecoration: 'none' }}>
                {result.gitopsRepoURL}
                <ExternalLink size={10} style={{ marginLeft: 5, opacity: 0.7 }} />
              </a>
            } />
          )}
        </Section>

        {/* ── First-time admin login ── */}
        <Section title={
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <KeyRound size={11} />First-time admin login
          </span>
        }>
          <Row label="Username" value={
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7 }}>
              <span data-testid="admin-username">{adminUsername || '—'}</span>
              {adminUsername && <CopyChip text={adminUsername} />}
            </span>
          } />
          <Row label="Login URL" value={
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
              {oneTimeURL ? (
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7 }}>
                  <a data-testid="one-time-url" href={oneTimeURL} target="_blank" rel="noopener noreferrer"
                     style={{ color: 'var(--wiz-accent)', textDecoration: 'none' }}>
                    {oneTimeURL}
                    <ExternalLink size={10} style={{ marginLeft: 5, opacity: 0.7 }} />
                  </a>
                  <CopyChip text={oneTimeURL} />
                </span>
              ) : (
                <button
                  type="button"
                  onClick={fetchOneTimeURL}
                  disabled={loadingOneTime || !deploymentId}
                  data-testid="mint-one-time-url"
                  style={{
                    alignSelf: 'flex-start',
                    display: 'inline-flex', alignItems: 'center', gap: 6,
                    padding: '5px 11px', borderRadius: 6,
                    border: '1px solid var(--wiz-border)',
                    background: 'var(--wiz-bg-sub)',
                    color: 'var(--wiz-text-hi)',
                    fontSize: 11, fontWeight: 600,
                    cursor: deploymentId ? 'pointer' : 'not-allowed',
                    opacity: deploymentId ? 1 : 0.55,
                    fontFamily: 'Inter, sans-serif',
                  }}
                >
                  <KeyRound size={11} />
                  {loadingOneTime ? 'Minting…' : 'Mint one-time login URL'}
                </button>
              )}
              {oneTimeError === 'not-implemented' && (
                <div role="alert" data-testid="one-time-fallback" style={{
                  fontSize: 10.5, color: 'var(--wiz-text-sub)',
                  lineHeight: 1.55,
                  background: 'rgba(56,189,248,0.05)',
                  border: '1px solid rgba(56,189,248,0.20)',
                  borderRadius: 6, padding: '7px 10px',
                  fontFamily: 'Inter, sans-serif',
                }}>
                  One-time URLs are not yet exposed by this catalyst-api build.{' '}
                  Use the Keycloak realm-master URL on the new Sovereign and trigger a
                  password-reset email — see{' '}
                  <a href={firstLoginDocsURL} data-testid="first-login-docs"
                     target="_blank" rel="noopener noreferrer"
                     style={{ color: 'var(--wiz-accent)' }}>
                    docs/RUNBOOK-PROVISIONING.md §First-time-admin-login
                    <ExternalLink size={9} style={{ marginLeft: 4 }} />
                  </a>.
                </div>
              )}
              {oneTimeError && oneTimeError !== 'not-implemented' && (
                <div role="alert" style={{
                  fontSize: 10.5, color: '#FCA5A5',
                  background: 'rgba(248,113,113,0.05)',
                  border: '1px solid rgba(248,113,113,0.25)',
                  borderRadius: 6, padding: '7px 10px',
                  fontFamily: 'Inter, sans-serif',
                }}>
                  {oneTimeError}
                </div>
              )}
            </div>
          } />
        </Section>

        {/* ── Cluster access — kubeconfig + merge command (issue #765) ── */}
        <Section title={
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <Terminal size={11} />Cluster access
          </span>
        }>
          <div style={{ padding: '10px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
            <span style={{
              fontSize: 10.5, color: 'var(--wiz-text-sub)', lineHeight: 1.55,
              fontFamily: 'Inter, sans-serif',
            }}>
              Step 1 — Download the kubeconfig (saves as
              {' '}<code style={{ fontFamily: 'JetBrains Mono, monospace', color: 'var(--wiz-text-md)' }}>
                {downloadFilename}
              </code> with context name
              {' '}<code style={{ fontFamily: 'JetBrains Mono, monospace', color: 'var(--wiz-text-md)' }}>
                {contextName}
              </code>).
            </span>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <button
                type="button"
                onClick={downloadKubeconfig}
                disabled={downloadingKubeconfig || !deploymentId}
                data-testid="download-kubeconfig"
                style={{
                  display: 'inline-flex', alignItems: 'center', gap: 6,
                  padding: '6px 12px', borderRadius: 6,
                  border: '1px solid var(--wiz-border)',
                  background: 'var(--wiz-bg-sub)',
                  color: 'var(--wiz-text-hi)',
                  fontSize: 11, fontWeight: 600,
                  cursor: deploymentId ? 'pointer' : 'not-allowed',
                  opacity: deploymentId ? 1 : 0.55,
                  fontFamily: 'Inter, sans-serif',
                }}
              >
                <Download size={11} />
                {downloadingKubeconfig ? 'Downloading…' : 'Download kubeconfig'}
              </button>
            </div>

            <span style={{
              fontSize: 10.5, color: 'var(--wiz-text-sub)', lineHeight: 1.55,
              fontFamily: 'Inter, sans-serif', marginTop: 4,
            }}>
              Step 2 — Paste this one-liner into a shell to merge the new
              context into <code style={{ fontFamily: 'JetBrains Mono, monospace' }}>$HOME/.kube/config</code>
              {' '}and launch k9s on it:
            </span>
            <div style={{
              display: 'flex', alignItems: 'flex-start', gap: 8,
              padding: '8px 10px', borderRadius: 6,
              border: '1px solid var(--wiz-border-sub)',
              background: 'var(--wiz-bg-xs)',
            }}>
              <code
                data-testid="kubeconfig-merge-command"
                style={{
                  flex: 1,
                  fontFamily: 'JetBrains Mono, monospace',
                  fontSize: 10.5, lineHeight: 1.55,
                  color: 'var(--wiz-text-md)',
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-all',
                }}
              >{mergeCommand}</code>
              <button
                type="button"
                onClick={copyMergeCommand}
                aria-label="Copy merge command"
                data-testid="copy-merge-command"
                style={{
                  flexShrink: 0,
                  display: 'inline-flex', alignItems: 'center', gap: 4,
                  padding: '4px 9px', borderRadius: 5,
                  border: '1px solid var(--wiz-border-sub)',
                  background: 'transparent',
                  color: 'var(--wiz-text-md)',
                  fontSize: 10, fontWeight: 600,
                  cursor: 'pointer',
                  fontFamily: 'Inter, sans-serif',
                }}
              >
                {mergeCopied ? <Check size={11} /> : <Copy size={11} />}
                {mergeCopied ? 'Copied' : 'Copy'}
              </button>
            </div>
            {kubeconfigError === 'not-implemented' && (
              <div role="alert" data-testid="kubeconfig-fallback" style={{
                fontSize: 10.5, color: 'var(--wiz-text-sub)',
                lineHeight: 1.55,
                background: 'rgba(245,158,11,0.05)',
                border: '1px solid rgba(245,158,11,0.20)',
                borderRadius: 6, padding: '7px 10px',
                fontFamily: 'Inter, sans-serif',
              }}>
                Coming soon — fetch via SSH. The HTTP endpoint is not yet wired
                up on this catalyst-api build. Run the SSH-based procedure
                documented at{' '}
                <a href={kubeconfigDocsURL} data-testid="kubeconfig-docs"
                   target="_blank" rel="noopener noreferrer"
                   style={{ color: 'var(--wiz-accent)' }}>
                  docs/RUNBOOK-PROVISIONING.md §Fetch kubeconfig via SSH
                  <ExternalLink size={9} style={{ marginLeft: 4 }} />
                </a>.
              </div>
            )}
            {kubeconfigError && kubeconfigError !== 'not-implemented' && (
              <div role="alert" style={{
                fontSize: 10.5, color: '#FCA5A5',
                background: 'rgba(248,113,113,0.05)',
                border: '1px solid rgba(248,113,113,0.25)',
                borderRadius: 6, padding: '7px 10px',
                fontFamily: 'Inter, sans-serif',
              }}>
                {kubeconfigError}
              </div>
            )}
          </div>
        </Section>

        {/* ── Voucher shortcut + Docs link ── */}
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
          <a
            data-testid="voucher-cta"
            href={voucherURL || '#'}
            target="_blank"
            rel="noopener noreferrer"
            aria-disabled={!voucherURL}
            onClick={(e) => { if (!voucherURL) e.preventDefault() }}
            style={{
              display: 'flex', flexDirection: 'column', gap: 4,
              padding: '12px 14px', borderRadius: 10,
              border: '1px solid var(--wiz-border-sub)',
              background: 'var(--wiz-bg-xs)',
              textDecoration: 'none',
              color: 'inherit',
              opacity: voucherURL ? 1 : 0.55,
              cursor: voucherURL ? 'pointer' : 'not-allowed',
            }}
          >
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6,
              fontSize: 10, fontWeight: 700, letterSpacing: '0.10em',
              textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>
              <Ticket size={10} />Issue first voucher
            </span>
            <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--wiz-text-hi)' }}>
              Now issue your first voucher
            </span>
            <span style={{ fontSize: 10.5, color: 'var(--wiz-text-md)',
              fontFamily: 'JetBrains Mono, monospace', wordBreak: 'break-all', lineHeight: 1.5 }}>
              {voucherURL || '—'}
            </span>
          </a>

          <a
            data-testid="docs-cta"
            href={docsURL || '#'}
            target="_blank"
            rel="noopener noreferrer"
            aria-disabled={!docsURL}
            onClick={(e) => { if (!docsURL) e.preventDefault() }}
            style={{
              display: 'flex', flexDirection: 'column', gap: 4,
              padding: '12px 14px', borderRadius: 10,
              border: '1px solid var(--wiz-border-sub)',
              background: 'var(--wiz-bg-xs)',
              textDecoration: 'none',
              color: 'inherit',
              opacity: docsURL ? 1 : 0.55,
              cursor: docsURL ? 'pointer' : 'not-allowed',
            }}
          >
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6,
              fontSize: 10, fontWeight: 700, letterSpacing: '0.10em',
              textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>
              <BookOpen size={10} />Sovereign docs
            </span>
            <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--wiz-text-hi)' }}>
              Read the on-cluster docs
            </span>
            <span style={{ fontSize: 10.5, color: 'var(--wiz-text-md)',
              fontFamily: 'JetBrains Mono, monospace', wordBreak: 'break-all', lineHeight: 1.5 }}>
              {docsURL || '—'}
            </span>
          </a>
        </div>

        {/* ── Final-state log tail (collapsed by default) ── */}
        <Section title="Final-state log tail">
          <button
            type="button"
            onClick={() => setLogExpanded((v) => !v)}
            data-testid="log-tail-toggle"
            aria-expanded={logExpanded}
            style={{
              display: 'flex', alignItems: 'center', gap: 6,
              padding: '8px 14px',
              border: 'none',
              background: 'transparent',
              color: 'var(--wiz-text-md)',
              fontSize: 11, fontWeight: 600,
              cursor: 'pointer',
              fontFamily: 'Inter, sans-serif',
              borderBottom: '1px solid var(--wiz-border-sub)',
              width: '100%',
              textAlign: 'left',
            }}
          >
            {logExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
            <span>{logExpanded ? 'Hide' : 'Show'} last {tail.length || 20} SSE log lines</span>
          </button>
          {logExpanded && (
            <pre data-testid="log-tail-pre" style={{
              margin: 0, padding: '10px 14px',
              fontSize: 10, lineHeight: 1.55,
              fontFamily: 'JetBrains Mono, monospace',
              color: 'var(--wiz-text-md)',
              background: 'var(--wiz-bg-xs)',
              maxHeight: 220, overflowY: 'auto',
              whiteSpace: 'pre-wrap', wordBreak: 'break-word',
            }}>
              {tail.length > 0
                ? tail.join('\n')
                : '(no log lines available — open the provisioning page in a separate tab to see the full SSE stream)'}
            </pre>
          )}
        </Section>
      </div>
    </StepShell>
  )
}
