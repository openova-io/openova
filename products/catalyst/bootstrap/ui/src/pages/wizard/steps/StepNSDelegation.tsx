/**
 * StepNSDelegation — post-handover wizard step that guides the operator
 * through delegating the parent zone (e.g. `omani.works`) so the new
 * Sovereign FQDN (e.g. `omantel.omani.works`) is served by the Sovereign's
 * own PowerDNS instance instead of contabo's.
 *
 * Closes GitHub issue #374 — "NS delegation .omani.works → omantel.omani.works
 * onto omantel's PowerDNS".
 *
 * Position in the wizard
 * ----------------------
 * The provisioning flow ends at StepSuccess (the cluster is up, the bootstrap
 * kit is green, the catalyst-api `done` event has populated the load-balancer
 * IP and the GitOps repo). At that point the new Sovereign is reachable on
 * its load-balancer IP, but the *parent zone* still answers for the new
 * sub-zone — every `dig` for `<sub>.<parent>` returns NXDOMAIN until the
 * operator delegates the sub-zone NS to the Sovereign's own nameservers.
 *
 * This step is the "operator-facing handover gate" that closes that gap. It
 * is intentionally placed AFTER StepSuccess so the LB IP / nameserver record
 * for the Sovereign exists before we ask the operator to delegate to them.
 *
 * Modes (per issue #374 spec)
 * ---------------------------
 *  1. Emit-runbook (default, safe) — the step renders the exact `curl` POST
 *     to Dynadot's `set_dns2` endpoint with `add_dns_to_current_setting=yes`
 *     so the operator can copy-paste it into a sandboxed shell. NO live API
 *     call is made by the wizard. This matches the project memory rule
 *     "NEVER run exploratory `set_dns2` calls — each one wipes all records"
 *     (memory/feedback_dynadot_dns.md): the wizard MUST NOT call set_dns2
 *     opportunistically.
 *
 *  2. Auto-apply (off by default, double-confirm) — when the operator flips
 *     `autoApply` on AND types the parent zone into a confirmation field,
 *     the step POSTs to a new catalyst-api endpoint
 *     `POST /api/v1/dns/parent-zone/delegate` which talks to PDM (which is
 *     the only seam allowed to call `api.dynadot.com` per
 *     products/catalyst/bootstrap/api/internal/pdm/client.go header). The
 *     endpoint is wired as a thin stub today; live execution is deferred to
 *     Phase 8 of the omantel handover WBS (see docs/omantel-handover-wbs.md
 *     row #374).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md
 * ---------------------------------
 *  • #4  No hardcoded URLs — the parent-zone-delegate endpoint is computed
 *        from API_BASE; the runbook command takes the registrar host from a
 *        constant, the FQDN/IP from wizard state, and the Dynadot API key
 *        placeholder is left as a `$DYNADOT_API_KEY` env reference so the
 *        copy-pasted shell command never embeds the secret in the wizard.
 *  • #10 Credential hygiene — the wizard NEVER asks for the parent-zone
 *        registrar API key. The emitted runbook references the env var
 *        the operator already set in their shell. The auto-apply path
 *        relies on PDM's existing K8s-secret-mounted Dynadot credential
 *        (memory/MEMORY.md → "Dynadot DNS"); the FE never sees it.
 */

import { useMemo, useState } from 'react'
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Copy,
  Check,
  Globe,
  ShieldAlert,
  Terminal,
} from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  resolveSovereignDomain,
  REGISTRAR_OPTIONS,
  type RegistrarType,
} from '@/entities/deployment/model'
import { API_BASE } from '@/shared/config/urls'
import { StepShell } from './_shared'

/* ── Constants — central, never inlined ───────────────────────────────── */

/**
 * Default registrar for the parent zone. The OpenOva-managed pool domain
 * `omani.works` is registered at Dynadot (memory/MEMORY.md), so the wizard
 * defaults to Dynadot. The dropdown still exposes the full registrar set
 * for franchised pool domains hosted elsewhere.
 *
 * Per Inviolable-Principle #4 we expose this as a typed constant rather
 * than inlining the literal in the JSX.
 */
export const DEFAULT_PARENT_REGISTRAR: RegistrarType = 'dynadot'

/**
 * Default parent zone in the form. Pre-fills with `omani.works` for the
 * common franchised-Sovereign case (issue #374 named omani.works as the
 * worked example) but the operator can edit it before continuing.
 */
export const DEFAULT_PARENT_ZONE = 'omani.works'

/**
 * Catalyst-API endpoint that wraps the live PDM `set_dns2` call. The
 * stub today returns 501 with an explicit "deferred to Phase 8" body; the
 * Phase 8 commit wires it through to PDM's
 * `RegistrarAdapter.AddNSDelegation`.
 */
export function parentZoneDelegateAPIPath(): string {
  return `${API_BASE}/v1/dns/parent-zone/delegate`
}

/* ── Helper: build the runbook curl command ───────────────────────────── */

/**
 * Build the Dynadot `set_dns2` curl snippet the operator copy-pastes.
 *
 * The command:
 *   - reads the API key from `$DYNADOT_API_KEY` (operator must export it
 *     in their shell BEFORE running — never embedded in the wizard or the
 *     copy-pasted snippet),
 *   - sets `add_dns_to_current_setting=yes` so existing records on the
 *     parent zone are preserved (the documented "appends without
 *     overwriting" mode per memory/MEMORY.md → Dynadot DNS),
 *   - delegates the sub-zone (`<sub>.<parent>`) to the Sovereign's three
 *     in-cluster PowerDNS replicas (the chart's anycast endpoint per
 *     platform/powerdns/chart/values.yaml § "anycast endpoint Service"),
 *     plus an A glue record pointing the Sovereign's load-balancer IP at
 *     the canonical name.
 *
 * This is exactly the shape of the call PDM will make in Phase 8 — the
 * operator running it manually in Phase 7 produces an identical wire
 * effect.
 */
export function buildDynadotRunbookCommand(opts: {
  parentZone: string
  sovereignFQDN: string
  loadBalancerIP: string
}): string {
  const { parentZone, sovereignFQDN, loadBalancerIP } = opts
  // Sub-label = portion of the FQDN that is below the parent zone.
  // Example: parentZone=omani.works, sovereignFQDN=omantel.omani.works → sub=omantel.
  // If the FQDN does not end with the parent zone we still emit the command
  // but warn (handled by the calling component); the helper itself stays
  // pure so it's trivial to unit-test.
  const sub = sovereignFQDN.endsWith('.' + parentZone)
    ? sovereignFQDN.slice(0, -1 - parentZone.length)
    : sovereignFQDN
  // The three PowerDNS NS hostnames the Sovereign's chart materialises.
  // Pattern matches the existing OPENOVA_NAMESERVERS convention but is
  // scoped to the Sovereign FQDN, not the openova.io home cluster.
  const ns = [1, 2, 3].map(i => `ns${i}.${sovereignFQDN}`)
  // Build the form-data body. Each `subdomainN` slot ships a record. We
  // bundle 3 NS records + 1 glue A record in subdomain0..3.
  const params: string[] = [
    `--data-urlencode "key=$DYNADOT_API_KEY"`,
    `--data-urlencode "command=set_dns2"`,
    `--data-urlencode "domain=${parentZone}"`,
    `--data-urlencode "add_dns_to_current_setting=yes"`,
    `--data-urlencode "subdomain0=${sub}"`,
    `--data-urlencode "sub_record_type0=NS"`,
    `--data-urlencode "sub_record0=${ns[0]}"`,
    `--data-urlencode "subdomain1=${sub}"`,
    `--data-urlencode "sub_record_type1=NS"`,
    `--data-urlencode "sub_record1=${ns[1]}"`,
    `--data-urlencode "subdomain2=${sub}"`,
    `--data-urlencode "sub_record_type2=NS"`,
    `--data-urlencode "sub_record2=${ns[2]}"`,
    // Glue A record so the parent zone's resolvers can find ns1.<fqdn>
    // even before the Sovereign's own PowerDNS answers authoritatively.
    `--data-urlencode "subdomain3=ns1.${sub}"`,
    `--data-urlencode "sub_record_type3=A"`,
    `--data-urlencode "sub_record3=${loadBalancerIP}"`,
  ]
  return [
    `# Delegate ${sovereignFQDN} → Sovereign-owned PowerDNS`,
    `# (parent zone: ${parentZone}, registrar: Dynadot)`,
    `# Pre-req: export DYNADOT_API_KEY=<your-key>  # never paste the key into the wizard`,
    `curl -sS https://api.dynadot.com/api3.json \\`,
    ...params.map(p => `  ${p} \\`),
  ].join('\n').replace(/\\\n$/, '')
}

/* ── Small UI helpers (mirrors StepSuccess pattern) ───────────────────── */

function CopyChip({ text, label = 'Copy command' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard write fails silently in browsers without permission;
      // operator can still select the <pre> contents manually.
    }
  }
  return (
    <button
      type="button"
      onClick={copy}
      aria-label={label}
      data-testid="copy-runbook"
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 5,
        padding: '4px 10px', borderRadius: 6,
        border: '1px solid var(--wiz-border-sub)', background: 'transparent',
        color: 'var(--wiz-text-md)', fontSize: 11, fontWeight: 600,
        cursor: 'pointer', fontFamily: 'Inter, sans-serif',
      }}
    >
      {copied ? <Check size={12} /> : <Copy size={12} />}
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
      <div style={{ padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>{children}</div>
    </div>
  )
}

/* ── Public component ─────────────────────────────────────────────────── */

export interface StepNSDelegationProps {
  /** Test-only override for the parent zone (sidesteps the Zustand store). */
  parentZoneOverride?: string
  /** Test-only override for the registrar (sidesteps the Zustand store). */
  registrarOverride?: RegistrarType
  /**
   * Test-only override for the resolved Sovereign FQDN. Production callers
   * omit this; the component reads `lastProvisionResult.sovereignFQDN`
   * (preferred) or falls back to `resolveSovereignDomain(state)`.
   */
  fqdnOverride?: string
  /**
   * Test-only override for the load-balancer IP. Production callers omit;
   * the component reads `lastProvisionResult.loadBalancerIP`.
   */
  lbIPOverride?: string
}

export function StepNSDelegation({
  parentZoneOverride,
  registrarOverride,
  fqdnOverride,
  lbIPOverride,
}: StepNSDelegationProps = {}) {
  const store = useWizardStore()
  const result = store.lastProvisionResult

  const fqdn = fqdnOverride
    ?? result?.sovereignFQDN
    ?? resolveSovereignDomain(store)

  const lbIP = lbIPOverride ?? result?.loadBalancerIP ?? ''

  // Local form state — defaults seeded from the FQDN so the wizard works
  // out-of-the-box for the omantel.omani.works case.
  const [registrar, setRegistrar] = useState<RegistrarType>(
    registrarOverride ?? DEFAULT_PARENT_REGISTRAR,
  )
  const [parentZone, setParentZone] = useState<string>(
    parentZoneOverride
    ?? (fqdn && fqdn.includes('.') ? fqdn.split('.').slice(1).join('.') : DEFAULT_PARENT_ZONE),
  )

  // Auto-apply gating — defaults OFF per issue #374. Flipping the toggle
  // alone is not enough; the operator must also type the parent zone into
  // the confirmation field. This is the same shape WipeDeploymentModal
  // uses for destructive ops (issue #318) so the operator UX is consistent.
  const [autoApply, setAutoApply] = useState(false)
  const [confirmText, setConfirmText] = useState('')
  const confirmed = autoApply && confirmText.trim() === parentZone.trim() && parentZone.trim() !== ''

  // Auto-apply state machine: idle → submitting → done | error.
  type SubmitState = { kind: 'idle' } | { kind: 'submitting' } | { kind: 'done' } | { kind: 'error'; msg: string }
  const [submit, setSubmit] = useState<SubmitState>({ kind: 'idle' })

  // Memoise the runbook so the <pre> doesn't re-build on every keystroke.
  const runbook = useMemo(
    () => buildDynadotRunbookCommand({ parentZone, sovereignFQDN: fqdn, loadBalancerIP: lbIP }),
    [parentZone, fqdn, lbIP],
  )

  const [runbookExpanded, setRunbookExpanded] = useState(true)

  // Warn when the FQDN is not actually a child of the parent zone the
  // operator typed — that would yield a malformed sub-record.
  const fqdnMismatch = !!fqdn && !!parentZone && !fqdn.endsWith('.' + parentZone)

  async function applyDelegation() {
    if (!confirmed) return
    setSubmit({ kind: 'submitting' })
    try {
      const res = await fetch(parentZoneDelegateAPIPath(), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({
          parentZone,
          registrar,
          sovereignFQDN: fqdn,
          loadBalancerIP: lbIP,
        }),
      })
      if (res.status === 501) {
        // Stub path — Phase 8 will wire this through PDM. Surface a clear
        // "not yet — copy the runbook" hint instead of a generic error.
        setSubmit({ kind: 'error', msg: 'Live delegation deferred to Phase 8 — copy the runbook above and run it manually.' })
        return
      }
      if (!res.ok) {
        const body = await res.text().catch(() => '')
        setSubmit({ kind: 'error', msg: `Backend returned ${res.status}: ${body.slice(0, 200)}` })
        return
      }
      setSubmit({ kind: 'done' })
    } catch (err) {
      setSubmit({ kind: 'error', msg: `Network error: ${String(err)}` })
    }
  }

  function onNext() {
    // Terminal step — clicking "Done" closes the wizard back to the home
    // dashboard. A future "skip handover" link can also call this.
    window.location.href = '/sovereign'
  }

  return (
    <StepShell
      title="Delegate the parent zone"
      description="The new Sovereign answers DNS for itself, but the parent zone (e.g. omani.works) still has to forward queries to the Sovereign's own nameservers. Pick the registrar, confirm the parent zone, and either copy the runbook below or — once Phase-8 wiring lands — flip the auto-apply toggle to let PDM make the call for you. We never run set_dns2 opportunistically: each call replaces the parent zone's full record set, so the operator confirms first, every time."
      onNext={onNext}
      nextLabel="Done"
      nextDisabled={false}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>

        {/* ── Hero — what's happening + why ── */}
        <div
          role="status"
          aria-label="Parent-zone NS delegation"
          data-testid="ns-delegation-hero"
          style={{
            display: 'flex', alignItems: 'flex-start', gap: 12,
            padding: '14px 16px', borderRadius: 10,
            border: '1px solid rgba(96,165,250,0.30)',
            background: 'rgba(96,165,250,0.06)',
          }}
        >
          <Globe size={26} color="#60A5FA" style={{ flexShrink: 0, marginTop: 1 }} />
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 14, fontWeight: 700, color: 'var(--wiz-text-hi)' }}>
              {fqdn || 'Sovereign FQDN'} · NS handover
            </span>
            <span style={{ fontSize: 11, color: 'var(--wiz-text-sub)', lineHeight: 1.55 }}>
              Once this delegation is in place, <code>dig +trace {fqdn || '<sovereign>'}</code>
              {' '}will end at the new Sovereign's own PowerDNS instead of contabo's.
              cert-manager-powerdns-webhook (#373) can then issue Let's Encrypt certificates
              against the local zone, and the redirect from
              <code> console.openova.io/sovereign/&lt;id&gt;</code> (#319) starts to make sense.
            </span>
          </div>
        </div>

        {/* ── Form — registrar + parent zone + Sovereign read-back ── */}
        <Section title="Delegation parameters">
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.08em',
                            textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>
              Parent-zone registrar
            </span>
            <select
              data-testid="ns-registrar"
              value={registrar}
              onChange={(e) => setRegistrar(e.target.value as RegistrarType)}
              style={{
                padding: '7px 10px', borderRadius: 6,
                border: '1px solid var(--wiz-border-sub)',
                background: 'var(--wiz-bg-md)', color: 'var(--wiz-text-md)',
                fontSize: 12, fontFamily: 'Inter, sans-serif',
              }}
            >
              {REGISTRAR_OPTIONS.map(r => (
                <option key={r.id} value={r.id}>{r.label}</option>
              ))}
            </select>
          </label>

          <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.08em',
                            textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>
              Parent zone
            </span>
            <input
              data-testid="ns-parent-zone"
              type="text"
              value={parentZone}
              onChange={(e) => setParentZone(e.target.value.trim())}
              placeholder={DEFAULT_PARENT_ZONE}
              style={{
                padding: '7px 10px', borderRadius: 6,
                border: '1px solid var(--wiz-border-sub)',
                background: 'var(--wiz-bg-md)', color: 'var(--wiz-text-md)',
                fontSize: 12, fontFamily: 'JetBrains Mono, monospace',
              }}
            />
          </label>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 3,
                         padding: '8px 10px', borderRadius: 6,
                         background: 'var(--wiz-bg-md)',
                         border: '1px solid var(--wiz-border-sub)' }}>
            <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.08em',
                            textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>
              Sovereign FQDN (target)
            </span>
            <span data-testid="ns-fqdn-readback"
                  style={{ fontSize: 12, fontFamily: 'JetBrains Mono, monospace',
                           color: fqdn ? 'var(--wiz-text-md)' : 'var(--wiz-text-hint)' }}>
              {fqdn || '—  (provisioning result not yet available)'}
            </span>
            <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 2 }}>
              LB IP: <code data-testid="ns-lb-ip">{lbIP || '—'}</code>
            </span>
          </div>

          {fqdnMismatch && (
            <div role="alert" data-testid="ns-fqdn-mismatch"
                 style={{ display: 'flex', alignItems: 'flex-start', gap: 8,
                          padding: '8px 10px', borderRadius: 6,
                          border: '1px solid rgba(250,204,21,0.30)',
                          background: 'rgba(250,204,21,0.06)' }}>
              <AlertTriangle size={14} color="#FACC15" style={{ flexShrink: 0, marginTop: 2 }} />
              <span style={{ fontSize: 11, color: 'var(--wiz-text-md)', lineHeight: 1.5 }}>
                The Sovereign FQDN <code>{fqdn}</code> does not end with the parent zone
                <code> {parentZone}</code>. Either edit the parent zone above or check the
                wizard's domain step — the runbook below will be malformed otherwise.
              </span>
            </div>
          )}
        </Section>

        {/* ── Runbook — operator copy-paste path (default, safe) ── */}
        <Section title={
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <Terminal size={11} />Runbook  ·  copy-paste (recommended)
          </span>
        }>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, justifyContent: 'space-between' }}>
            <span style={{ fontSize: 11, color: 'var(--wiz-text-sub)' }}>
              Run on a host with <code>$DYNADOT_API_KEY</code> exported. Uses
              <code> add_dns_to_current_setting=yes</code> so existing records are preserved.
            </span>
            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <CopyChip text={runbook} />
              <button
                type="button"
                onClick={() => setRunbookExpanded(x => !x)}
                aria-label={runbookExpanded ? 'Collapse runbook' : 'Expand runbook'}
                style={{
                  display: 'inline-flex', alignItems: 'center', gap: 4,
                  padding: '4px 8px', borderRadius: 6,
                  border: '1px solid var(--wiz-border-sub)', background: 'transparent',
                  color: 'var(--wiz-text-md)', fontSize: 11, cursor: 'pointer',
                }}
              >
                {runbookExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
                {runbookExpanded ? 'Hide' : 'Show'}
              </button>
            </div>
          </div>
          {runbookExpanded && (
            <pre
              data-testid="ns-runbook"
              style={{
                margin: 0, padding: 12,
                borderRadius: 6,
                background: 'var(--wiz-bg-md)',
                border: '1px solid var(--wiz-border-sub)',
                color: 'var(--wiz-text-md)',
                fontSize: 11, fontFamily: 'JetBrains Mono, monospace',
                whiteSpace: 'pre-wrap', wordBreak: 'break-all', lineHeight: 1.5,
                maxHeight: 320, overflow: 'auto',
              }}
            >{runbook}</pre>
          )}
        </Section>

        {/* ── Auto-apply (off by default, double-confirm) ── */}
        <Section title={
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <ShieldAlert size={11} />Auto-apply  ·  double-confirm (advanced)
          </span>
        }>
          <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, fontSize: 11,
                           color: 'var(--wiz-text-md)', lineHeight: 1.55 }}>
            <input
              data-testid="ns-auto-apply"
              type="checkbox"
              checked={autoApply}
              onChange={(e) => {
                setAutoApply(e.target.checked)
                if (!e.target.checked) {
                  setConfirmText('')
                  setSubmit({ kind: 'idle' })
                }
              }}
              style={{ marginTop: 2 }}
            />
            <span>
              I understand that PDM will call <code>set_dns2</code> on
              <code> {parentZone || '<parent zone>'}</code> and that an incorrect parent zone
              will erase live records (Dynadot's API replaces the full record set on each
              call).
            </span>
          </label>

          {autoApply && (
            <>
              <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.08em',
                                 textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>
                  Type the parent zone to confirm
                </span>
                <input
                  data-testid="ns-confirm"
                  type="text"
                  value={confirmText}
                  onChange={(e) => setConfirmText(e.target.value)}
                  placeholder={parentZone}
                  style={{
                    padding: '7px 10px', borderRadius: 6,
                    border: `1px solid ${confirmed ? 'rgba(74,222,128,0.45)' : 'var(--wiz-border-sub)'}`,
                    background: 'var(--wiz-bg-md)', color: 'var(--wiz-text-md)',
                    fontSize: 12, fontFamily: 'JetBrains Mono, monospace',
                  }}
                />
              </label>

              <button
                type="button"
                data-testid="ns-apply"
                onClick={applyDelegation}
                disabled={!confirmed || submit.kind === 'submitting'}
                style={{
                  alignSelf: 'flex-start',
                  padding: '7px 14px', borderRadius: 6,
                  border: '1px solid var(--wiz-border-sub)',
                  background: confirmed ? 'var(--wiz-accent)' : 'var(--wiz-bg-md)',
                  color: confirmed ? '#0B0F19' : 'var(--wiz-text-hint)',
                  fontSize: 11, fontWeight: 700,
                  cursor: confirmed && submit.kind !== 'submitting' ? 'pointer' : 'not-allowed',
                  fontFamily: 'Inter, sans-serif',
                }}
              >
                {submit.kind === 'submitting' ? 'Applying…' : 'Apply NS delegation'}
              </button>

              {submit.kind === 'done' && (
                <div role="status" data-testid="ns-apply-done"
                     style={{ display: 'flex', alignItems: 'center', gap: 6,
                              fontSize: 11, color: '#4ADE80' }}>
                  <CheckCircle2 size={12} />
                  Delegation applied. Run <code>dig +trace {fqdn}</code> to verify.
                </div>
              )}
              {submit.kind === 'error' && (
                <div role="alert" data-testid="ns-apply-error"
                     style={{ display: 'flex', alignItems: 'flex-start', gap: 6,
                              fontSize: 11, color: 'var(--wiz-text-md)', lineHeight: 1.5 }}>
                  <AlertTriangle size={12} color="#FACC15" style={{ flexShrink: 0, marginTop: 2 }} />
                  {submit.msg}
                </div>
              )}
            </>
          )}
        </Section>
      </div>
    </StepShell>
  )
}
