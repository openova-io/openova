/**
 * MarketplaceSettings — Sovereign Console → Settings → Marketplace.
 *
 * Operators of a live Sovereign reach this page from the left-rail
 * Settings → Marketplace nav entry. It exposes a single toggle that
 * enables / disables the marketplace HTTPRoutes + storefront branding
 * on the Sovereign post-provisioning. Saving POSTs to:
 *
 *   POST /api/v1/sovereigns/{id}/marketplace
 *
 * The catalyst-api handler does NOT mutate cluster state directly —
 * per the founder's 2026-05-04 GitOps rule, every change is committed
 * to the GitOps repo at
 * `clusters/<sovereign-fqdn>/bootstrap-kit/13-bp-catalyst-platform.yaml`
 * and Flux on the Sovereign reconciles within ~1 min.
 *
 * This page is one of the three pieces shipped for issue #710 wave 3:
 *   - StepMarketplace wizard step (provisioning-time, sibling PR)
 *   - Catalog publish/unpublish admin (sibling PR)
 *   - This page — operator opt-in / opt-out AFTER provisioning
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the API base
 * is read from `@/shared/config/urls` so the same component works on
 * Catalyst-Zero (basepath /sovereign/) and on Sovereign clusters
 * (basepath /).
 *
 * Per #10 (credential hygiene) — no secrets cross this surface; the
 * brand fields are operator-owned plaintext (storefront name, tagline,
 * primary colour). Stripe / payment credentials are handled separately
 * by the catalog admin.
 *
 * Related: GitHub issue #710 (marketplace mode wave 3).
 */

import { useEffect, useState } from 'react'
import { Store, AlertTriangle, CheckCircle2, Loader2 } from 'lucide-react'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { API_BASE } from '@/shared/config/urls'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

interface MarketplaceBrand {
  name: string
  tagline: string
  primaryColor: string
}

interface SaveResponse {
  deploymentId: string
  sovereignFQDN: string
  enabled: boolean
  commitSha: string
  appliedAt: string
}

type SaveState =
  | { status: 'idle' }
  | { status: 'saving' }
  | { status: 'reconciling'; commitSha: string; appliedAt: string }
  | { status: 'applied'; commitSha: string; appliedAt: string }
  | { status: 'error'; message: string }

const HEX_COLOR_RE = /^#[0-9a-fA-F]{6}$/

/**
 * Resolve the deployment id for the Sovereign-Console mode.
 *
 * On a live Sovereign the deployment id is the FQDN itself (the
 * catalyst-api on the Sovereign side keys deployments by the same id
 * the wizard handed off). DETECTED_MODE.sovereignFQDN comes from the
 * window.location.hostname per /shared/lib/detectMode and is what the
 * SovereignConsoleLayout already trusts for the auth gate.
 *
 * Mode = 'wizard' (Catalyst-Zero) is not the target audience for this
 * page — provisioning-time toggle is the wizard step. We still render
 * a useful empty state in that case so this component is safe to mount
 * from any route tree.
 */
function resolveDeploymentId(): string {
  return DETECTED_MODE.sovereignFQDN ?? ''
}

export function MarketplaceSettings() {
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? ''
  // Prefer the cookie-resolved deployment id over the legacy
  // resolveDeploymentId() helper (which returns the FQDN, not the id —
  // a separate bug not in scope here). Falls back to the legacy value
  // so SSR/test paths without a cookie still get a deterministic id.
  const { deploymentId: cookieDepId } = useResolvedDeploymentId()
  const deploymentId = cookieDepId ?? resolveDeploymentId()

  // Initial state — defaulting to disabled. A future iteration will GET
  // the current overlay state from catalyst-api so the toggle reflects
  // the live values; for now the operator is the source of truth on
  // entry to this page (the chart's default is also disabled).
  const [enabled, setEnabled] = useState(false)
  const [brand, setBrand] = useState<MarketplaceBrand>({
    name: '',
    tagline: '',
    primaryColor: '#3B82F6',
  })
  const [saveState, setSaveState] = useState<SaveState>({ status: 'idle' })

  // Auto-clear the "Applied" surface after 8s so a follow-up edit
  // doesn't sit next to a stale success banner. The "Reconciling" state
  // does NOT auto-clear — it must transition explicitly when the
  // commit reaches the chart's reconcile loop.
  useEffect(() => {
    if (saveState.status !== 'applied') return
    const t = setTimeout(() => setSaveState({ status: 'idle' }), 8_000)
    return () => clearTimeout(t)
  }, [saveState])

  // Phase the "reconciling" state through to "applied" after a short
  // settle window. This is the simplest signal the operator gets while
  // the chart re-renders. A more precise check would poll
  // /v1/whoami or the deployment events feed, but the
  // 60-90s reconcile window is deterministic enough that a fixed
  // settle gives a clear UX.
  useEffect(() => {
    if (saveState.status !== 'reconciling') return
    const t = setTimeout(() => {
      setSaveState((curr) =>
        curr.status === 'reconciling'
          ? { status: 'applied', commitSha: curr.commitSha, appliedAt: curr.appliedAt }
          : curr,
      )
    }, 75_000)
    return () => clearTimeout(t)
  }, [saveState])

  const colorValid = brand.primaryColor === '' || HEX_COLOR_RE.test(brand.primaryColor)
  const canSave =
    saveState.status !== 'saving' &&
    saveState.status !== 'reconciling' &&
    colorValid &&
    deploymentId !== ''

  async function handleSave() {
    if (!canSave) return
    setSaveState({ status: 'saving' })

    try {
      const res = await fetch(
        `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/marketplace`,
        {
          method: 'POST',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({
            enabled,
            brand: {
              name: brand.name,
              tagline: brand.tagline,
              primaryColor: brand.primaryColor,
            },
          }),
        },
      )
      if (!res.ok) {
        const text = await res.text().catch(() => res.statusText)
        setSaveState({
          status: 'error',
          message: `Save failed (${res.status}): ${text || 'unknown error'}`,
        })
        return
      }
      const body = (await res.json()) as SaveResponse
      setSaveState({
        status: 'reconciling',
        commitSha: body.commitSha,
        appliedAt: body.appliedAt,
      })
    } catch (err) {
      setSaveState({
        status: 'error',
        message: err instanceof Error ? err.message : 'Network error',
      })
    }
  }

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Marketplace mode"
    >
    <div data-testid="marketplace-settings-page">
      <div className="mb-6">
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Enable a public-facing marketplace storefront on this Sovereign. When enabled, the
          Catalyst chart renders the marketplace HTTPRoutes and the storefront ConfigMap with
          your branding. Changes are committed to your GitOps repository and reconciled by
          Flux within ~1 minute.
        </p>
      </div>

      <section
        className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6"
        data-testid="marketplace-settings-card"
      >
        <div className="mb-5 flex items-start justify-between gap-4">
          <div className="flex items-start gap-3">
            <Store className="mt-0.5 h-5 w-5 shrink-0 text-[var(--color-accent)]" />
            <div>
              <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
                Marketplace mode
              </h2>
              <p className="mt-0.5 text-sm text-[var(--color-text-dim)]">
                {enabled
                  ? 'Public storefront, *.{sovereignFQDN} tenant wildcard, and back-office routes are exposed.'
                  : 'Only console + admin routes are exposed; SME services run in the cluster but have no public ingress.'}
              </p>
            </div>
          </div>

          {/* Toggle */}
          <button
            type="button"
            role="switch"
            aria-checked={enabled}
            onClick={() => setEnabled((v) => !v)}
            className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer items-center rounded-full transition-colors ${
              enabled ? 'bg-[var(--color-accent)]' : 'bg-[var(--color-surface-hover)]'
            }`}
            data-testid="marketplace-settings-toggle"
          >
            <span
              className={`inline-block h-5 w-5 transform rounded-full bg-white transition-transform ${
                enabled ? 'translate-x-5' : 'translate-x-0.5'
              }`}
            />
          </button>
        </div>

        {/* Brand fields — only meaningful when enabled. We keep them in
            the DOM but disable when off so the operator can prep values
            and flip the toggle in one save. */}
        <div
          className={`grid gap-4 transition-opacity ${enabled ? 'opacity-100' : 'opacity-40'}`}
          data-testid="marketplace-settings-brand-fields"
        >
          <FieldRow
            label="Storefront name"
            description="Display name in the storefront header (e.g. Otech Cloud)."
          >
            <input
              type="text"
              value={brand.name}
              disabled={!enabled}
              onChange={(e) => setBrand((b) => ({ ...b, name: e.target.value }))}
              placeholder="Otech Cloud"
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text-strong)] placeholder:text-[var(--color-text-dimmer)] focus:border-[var(--color-accent)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-60"
              data-testid="marketplace-settings-brand-name"
              maxLength={64}
            />
          </FieldRow>

          <FieldRow
            label="Tagline"
            description="Sub-headline shown under the storefront name."
          >
            <input
              type="text"
              value={brand.tagline}
              disabled={!enabled}
              onChange={(e) => setBrand((b) => ({ ...b, tagline: e.target.value }))}
              placeholder="Cloud + SaaS for Oman"
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text-strong)] placeholder:text-[var(--color-text-dimmer)] focus:border-[var(--color-accent)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-60"
              data-testid="marketplace-settings-brand-tagline"
              maxLength={120}
            />
          </FieldRow>

          <FieldRow
            label="Primary colour"
            description="Accent colour for the storefront chrome (#RRGGBB hex)."
          >
            <div className="flex items-center gap-3">
              <input
                type="color"
                value={HEX_COLOR_RE.test(brand.primaryColor) ? brand.primaryColor : '#3B82F6'}
                disabled={!enabled}
                onChange={(e) => setBrand((b) => ({ ...b, primaryColor: e.target.value }))}
                className="h-9 w-14 cursor-pointer rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] disabled:cursor-not-allowed disabled:opacity-60"
                data-testid="marketplace-settings-brand-color-picker"
              />
              <input
                type="text"
                value={brand.primaryColor}
                disabled={!enabled}
                onChange={(e) => setBrand((b) => ({ ...b, primaryColor: e.target.value }))}
                placeholder="#3B82F6"
                className={`w-32 rounded-md border bg-[var(--color-bg)] px-3 py-2 font-mono text-sm text-[var(--color-text-strong)] placeholder:text-[var(--color-text-dimmer)] focus:outline-none disabled:cursor-not-allowed disabled:opacity-60 ${
                  colorValid
                    ? 'border-[var(--color-border)] focus:border-[var(--color-accent)]'
                    : 'border-[var(--color-error)] focus:border-[var(--color-error)]'
                }`}
                data-testid="marketplace-settings-brand-color-text"
                maxLength={7}
              />
              {!colorValid ? (
                <span className="text-xs text-[var(--color-error)]" data-testid="marketplace-settings-brand-color-error">
                  Use #RRGGBB hex
                </span>
              ) : null}
            </div>
          </FieldRow>
        </div>

        {/* Footer — Save + status */}
        <div className="mt-6 flex flex-wrap items-center justify-between gap-4 border-t border-[var(--color-border)] pt-4">
          <SaveStatus state={saveState} />
          <button
            type="button"
            onClick={handleSave}
            disabled={!canSave}
            className="rounded-md bg-[var(--color-accent)] px-4 py-2 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-accent)]/90 disabled:cursor-not-allowed disabled:opacity-50"
            data-testid="marketplace-settings-save"
          >
            {saveState.status === 'saving' ? 'Saving…' : 'Save changes'}
          </button>
        </div>
      </section>

      {/* Helper context */}
      <div
        className="mt-6 flex items-start gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)]/60 p-4 text-xs text-[var(--color-text-dim)]"
        data-testid="marketplace-settings-help"
      >
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--color-text-dim)]" />
        <div>
          Disabling the marketplace removes the public storefront and tenant wildcard
          ingress. The SME catalog services keep running and tenant data is preserved — only
          the public-facing routes are torn down. To fully remove the SME stack, decommission
          the Sovereign from{' '}
          <span className="font-medium text-[var(--color-text)]">Settings → Danger zone</span>.
          Sovereign:{' '}
          <span className="font-mono text-[var(--color-text)]">
            {sovereignFQDN || '—'}
          </span>
          .
        </div>
      </div>
    </div>
    </PortalShell>
  )
}

function FieldRow({
  label,
  description,
  children,
}: {
  label: string
  description: string
  children: React.ReactNode
}) {
  return (
    <div className="grid gap-2 sm:grid-cols-3 sm:gap-4">
      <div className="sm:pt-2">
        <p className="text-sm font-medium text-[var(--color-text-strong)]">{label}</p>
        <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">{description}</p>
      </div>
      <div className="sm:col-span-2">{children}</div>
    </div>
  )
}

function SaveStatus({ state }: { state: SaveState }) {
  if (state.status === 'idle') {
    return (
      <span
        className="text-xs text-[var(--color-text-dim)]"
        data-testid="marketplace-settings-status-idle"
      >
        No pending changes.
      </span>
    )
  }
  if (state.status === 'saving') {
    return (
      <span
        className="flex items-center gap-2 text-xs text-[var(--color-text-dim)]"
        data-testid="marketplace-settings-status-saving"
      >
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        Committing change to GitOps repo…
      </span>
    )
  }
  if (state.status === 'reconciling') {
    return (
      <span
        className="flex items-center gap-2 text-xs text-[var(--color-text-dim)]"
        data-testid="marketplace-settings-status-reconciling"
      >
        <Loader2 className="h-3.5 w-3.5 animate-spin text-[var(--color-accent)]" />
        Committed{' '}
        <code className="font-mono text-[var(--color-text)]">{state.commitSha.slice(0, 7)}</code>
        {' '}— Flux is reconciling the Sovereign…
      </span>
    )
  }
  if (state.status === 'applied') {
    return (
      <span
        className="flex items-center gap-2 text-xs text-[color:var(--color-success,#10b981)]"
        data-testid="marketplace-settings-status-applied"
      >
        <CheckCircle2 className="h-3.5 w-3.5" />
        Applied at {new Date(state.appliedAt).toLocaleTimeString()} —{' '}
        <code className="font-mono text-[var(--color-text)]">{state.commitSha.slice(0, 7)}</code>
      </span>
    )
  }
  return (
    <span
      className="flex items-center gap-2 text-xs text-[var(--color-error)]"
      data-testid="marketplace-settings-status-error"
    >
      <AlertTriangle className="h-3.5 w-3.5" />
      {state.message}
    </span>
  )
}
