/**
 * ConsoleSettingsPage — Sovereign Console /console/settings
 *
 * Sovereign-scoped settings surface. Reuses the same section structure
 * as SettingsPage.tsx but without a deploymentId param — in Sovereign
 * mode the cluster is implicit from the hostname.
 *
 * Sections:
 *   1. Organization   — org name, billing email
 *   2. Sovereign      — FQDN, region (read-only)
 *   3. Danger zone    — decommission link
 *
 * Phase 8b: sections are placeholders. API wiring is Phase 4 work.
 *
 * Related: GitHub issue #607
 */

import { Settings } from 'lucide-react'
import { DETECTED_MODE } from '@/shared/lib/detectMode'

export function ConsoleSettingsPage() {
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? '—'

  return (
    <div data-testid="console-settings-page">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Settings</h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Sovereign configuration and administration.
        </p>
      </div>

      <div className="space-y-6">
        {/* Organization */}
        <SettingsSection
          title="Organization"
          description="Name and billing contact for this Sovereign."
          testId="settings-org-section"
        >
          <FieldRow label="Sovereign FQDN" value={sovereignFQDN} testId="setting-fqdn" />
        </SettingsSection>

        {/* Danger zone */}
        <SettingsSection
          title="Danger zone"
          description="Irreversible actions."
          testId="settings-danger-section"
          danger
        >
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm font-medium text-[var(--color-text-strong)]">Decommission Sovereign</p>
              <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                Permanently remove all resources and data. This cannot be undone.
              </p>
            </div>
            <a
              href="/decommission"
              className="rounded-lg border border-[var(--color-error)]/40 bg-[var(--color-error)]/10 px-4 py-2 text-sm font-semibold text-[var(--color-error)] transition-colors hover:bg-[var(--color-error)]/20"
              data-testid="settings-decommission-link"
            >
              Decommission
            </a>
          </div>
        </SettingsSection>
      </div>

      {/* Integration placeholder */}
      <div
        className="mt-8 flex items-center gap-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
        data-testid="settings-api-placeholder"
      >
        <Settings className="h-5 w-5 shrink-0 text-[var(--color-text-dim)]" />
        <p className="text-xs text-[var(--color-text-dim)]">
          Additional settings (API tokens, cloud credentials, DNS, notifications) pending API integration (Phase 4).
        </p>
      </div>
    </div>
  )
}

function SettingsSection({
  title,
  description,
  testId,
  danger,
  children,
}: {
  title: string
  description: string
  testId: string
  danger?: boolean
  children: React.ReactNode
}) {
  return (
    <section
      className={`rounded-xl border p-6 ${danger ? 'border-[var(--color-error)]/30 bg-[var(--color-error)]/5' : 'border-[var(--color-border)] bg-[var(--color-bg-2)]'}`}
      data-testid={testId}
    >
      <div className="mb-4">
        <h2
          className={`text-base font-semibold ${danger ? 'text-[var(--color-error)]' : 'text-[var(--color-text-strong)]'}`}
        >
          {title}
        </h2>
        <p className="mt-0.5 text-sm text-[var(--color-text-dim)]">{description}</p>
      </div>
      {children}
    </section>
  )
}

function FieldRow({
  label,
  value,
  testId,
}: {
  label: string
  value: string
  testId: string
}) {
  return (
    <div className="flex items-center justify-between py-2" data-testid={testId}>
      <span className="text-sm text-[var(--color-text-dim)]">{label}</span>
      <span className="text-sm font-medium text-[var(--color-text-strong)]">{value}</span>
    </div>
  )
}
