/**
 * EnterOrgButton — the B2 Enter-org support action (issue #3378 §6 B2 +
 * DoD 6).
 *
 * One button per sub-org → an audited, time-boxed (≤60min) support
 * session that lands in the org's OWN console as a support principal in
 * the org's realm (never the owner's identity). POSTs to the catalyst-api
 * Enter-org endpoint, then opens the returned handover URL in a new tab;
 * the org's console redeems it (the same handover-redemption flow the
 * mothership→Sovereign handover uses) and lands the support principal
 * logged in with the in-console support banner.
 *
 * Rendered ONLY on sub-org rows — the parent row hides it ("you are
 * already inside it", §5). The org-detail page enforces that; this
 * component is defensive and also no-ops for a parent org.
 */

import { useState } from 'react'
import { enterOrg, type EnterOrgResult, type OrgRow } from '@/lib/organizations.api'

export interface EnterOrgButtonProps {
  org: OrgRow
  /** Test seam — replace the enterOrg call + the window.open side-effect. */
  onEnterOverride?: (slug: string) => Promise<EnterOrgResult>
  openOverride?: (url: string) => void
}

export function EnterOrgButton({ org, onEnterOverride, openOverride }: EnterOrgButtonProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [last, setLast] = useState<EnterOrgResult | null>(null)

  // Defensive: never offer the support session for the parent (§5).
  if (org.isParent) return null

  async function onClick() {
    setBusy(true)
    setError(null)
    try {
      const result = onEnterOverride ? await onEnterOverride(org.slug) : await enterOrg(org.slug)
      setLast(result)
      const open = openOverride ?? ((u: string) => window.open(u, '_blank', 'noopener,noreferrer'))
      open(result.handoverURL)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <button
        type="button"
        data-testid="enter-org-button"
        disabled={busy}
        onClick={onClick}
        className="inline-flex items-center gap-1.5 rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)]/10 px-3 py-1.5 text-xs font-medium text-[var(--color-accent)] disabled:opacity-50"
      >
        <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M11 16l-4-4m0 0l4-4m-4 4h14M3 4v16" />
        </svg>
        {busy ? 'Entering…' : 'Enter org'}
      </button>
      {error ? (
        <span data-testid="enter-org-error" className="text-[10px] text-rose-400">{error}</span>
      ) : null}
      {last ? (
        <span data-testid="enter-org-confirm" className="text-[10px] text-[var(--color-text-dim)]">
          Support session as {last.supportPrincipal} · expires{' '}
          {new Date(last.expiresAt).toLocaleTimeString()}
        </span>
      ) : null}
    </div>
  )
}
