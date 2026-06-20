/**
 * MarketplaceSection — Marketplace toggle + branding fields rendered
 * inside the SettingsPage `<SectionCard id="marketplace">` anchor.
 *
 * Wave 5 (2026-05-17, founder UX-polish review): replaces the
 * standalone /settings/marketplace page + Settings sub-nav child item.
 * Founder ruling: *"if market place is just a toggle etting under
 * setting it dosnt need tohave a sdicated page and it doesnt need to
 * have child left pane menu item ... it shoudl be somewher e here
 * I ugess https://console.<sov>/sovereign/provision/<id>/settings#dns
 * similar to other setting"*.
 *
 * This component renders ONLY the inner content (toggle, brand fields,
 * save status, save button). The PortalShell chrome + section header /
 * description / `data-pending-api` pill are owned by the parent
 * SectionCard in SettingsPage.tsx.
 *
 * Save flow is unchanged from the prior MarketplaceSettings page —
 * POSTs to /api/v1/sovereigns/{id}/marketplace which commits the
 * per-Sovereign overlay change to the GitOps repo so Flux reconciles
 * the chart. See issue #710 wave 3b for the backend wiring.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the API base
 * is read from `@/shared/config/urls` so the same component works on
 * Catalyst-Zero (basepath /sovereign/) and on Sovereign clusters
 * (basepath /).
 */

import { useEffect, useState } from 'react'
import { AlertTriangle, CheckCircle2, Loader2 } from 'lucide-react'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { API_BASE } from '@/shared/config/urls'
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

export function MarketplaceSection() {
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? ''
  const { deploymentId: cookieDepId } = useResolvedDeploymentId()
  const deploymentId = cookieDepId ?? sovereignFQDN

  const [enabled, setEnabled] = useState(false)
  const [brand, setBrand] = useState<MarketplaceBrand>({
    name: '',
    tagline: '',
    primaryColor: '#3B82F6',
  })
  const [saveState, setSaveState] = useState<SaveState>({ status: 'idle' })

  // Fetch current enabled state on mount so the toggle reflects the
  // actual deployment value (PR J pattern from prior MarketplaceSettings).
  useEffect(() => {
    if (!deploymentId) return
    let cancelled = false
    fetch(`${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/marketplace`, {
      method: 'GET',
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
      .then((res) => (res.ok ? res.json() : null))
      .then((d) => {
        if (cancelled || !d) return
        if (typeof d.enabled === 'boolean') setEnabled(d.enabled)
        if (d.brand && typeof d.brand === 'object') {
          setBrand((prev) => ({
            name: d.brand.name || prev.name,
            tagline: d.brand.tagline || prev.tagline,
            primaryColor: d.brand.primaryColor || prev.primaryColor,
          }))
        }
      })
      .catch(() => {
        // Best-effort — leave defaults on fetch failure.
      })
    return () => {
      cancelled = true
    }
  }, [deploymentId])

  useEffect(() => {
    if (saveState.status !== 'applied') return
    const t = setTimeout(() => setSaveState({ status: 'idle' }), 8_000)
    return () => clearTimeout(t)
  }, [saveState])

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
    <div data-testid="settings-marketplace-section">
      {/* Toggle row */}
      <div className="mb-5 flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <p className="text-sm text-[var(--color-text)]">
            {enabled
              ? 'Public storefront, *.{sovereignFQDN} Organization wildcard, and back-office routes are exposed.'
              : 'Only console + admin routes are exposed; Organization services run in the cluster but have no public ingress.'}
          </p>
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={enabled}
          onClick={() => setEnabled((v) => !v)}
          className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer items-center rounded-full transition-colors ${
            enabled ? 'bg-[var(--color-accent)]' : 'bg-[var(--color-surface-hover)]'
          }`}
          data-testid="settings-marketplace-toggle"
        >
          <span
            className={`inline-block h-5 w-5 transform rounded-full bg-white transition-transform ${
              enabled ? 'translate-x-5' : 'translate-x-0.5'
            }`}
          />
        </button>
      </div>

      {/* Brand fields — only meaningful when enabled. */}
      <div
        className={`grid gap-4 transition-opacity ${enabled ? 'opacity-100' : 'opacity-40'}`}
        data-testid="settings-marketplace-brand-fields"
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
            data-testid="settings-marketplace-brand-name"
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
            data-testid="settings-marketplace-brand-tagline"
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
              data-testid="settings-marketplace-brand-color-picker"
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
              data-testid="settings-marketplace-brand-color-text"
              maxLength={7}
            />
            {!colorValid ? (
              <span
                className="text-xs text-[var(--color-error)]"
                data-testid="settings-marketplace-brand-color-error"
              >
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
          data-testid="settings-marketplace-save"
        >
          {saveState.status === 'saving' ? 'Saving…' : 'Save changes'}
        </button>
      </div>
    </div>
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
        data-testid="settings-marketplace-status-idle"
      >
        No pending changes.
      </span>
    )
  }
  if (state.status === 'saving') {
    return (
      <span
        className="flex items-center gap-2 text-xs text-[var(--color-text-dim)]"
        data-testid="settings-marketplace-status-saving"
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
        data-testid="settings-marketplace-status-reconciling"
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
        data-testid="settings-marketplace-status-applied"
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
      data-testid="settings-marketplace-status-error"
    >
      <AlertTriangle className="h-3.5 w-3.5" />
      {state.message}
    </span>
  )
}
