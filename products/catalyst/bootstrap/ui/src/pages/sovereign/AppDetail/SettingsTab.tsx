/**
 * SettingsTab — per-Application Settings (EPIC-2 #1097 slice O):
 *
 *  • Parameters editor (REUSES slice I's <InstallForm> in edit mode
 *    pointing at the Application's spec.parameters)
 *  • Upgrade dialog launcher (UpgradeDialog widget)
 *  • Uninstall dialog launcher (UninstallDialog widget)
 *
 * Re-validates against `Blueprint.spec.configSchema` via slice I's
 * `pkg/validate` (server-side) — the form's onSubmit calls
 * updateApplication which the catalyst-api validates before patching
 * the CR.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from API_BASE
 * via the catalog.api helpers.
 */

import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  getApplicationStatus,
  getCatalogItem,
  getCatalogItemVersion,
  updateApplication,
  type CatalogItem,
} from '@/lib/catalog.api'
import { InstallForm } from '@/widgets/install/InstallForm'
import { UpgradeDialog } from '@/widgets/install/UpgradeDialog'
import { UninstallDialog } from '@/widgets/install/UninstallDialog'

export interface SettingsTabProps {
  sovereignId: string
  applicationName: string
  /** Org namespace. */
  namespace?: string
  /** Test seam — pre-populated CR (skip status fetch). */
  initialApp?: ApplicationStatus
  /** Test seam. */
  disableNetwork?: boolean
}

interface ApplicationStatus {
  name: string
  namespace: string
  phase?: string
  status?: Record<string, unknown>
  spec?: ApplicationSpec
}

interface ApplicationSpec {
  blueprintRef?: { name?: string; version?: string }
  parameters?: Record<string, unknown>
  // DUAL-FORM, like `Application.spec.placement` itself (#3373 / UAT row 16):
  // the posture string, or the #3969 object carrying targets[]. This tab does
  // not read the field — the declaration exists so the shared
  // ApplicationStatusResponse assigns cleanly — but it must not narrow the
  // wire contract, which is how the object form got flattened in the first
  // place.
  placement?: string | Record<string, unknown>
  regions?: string[]
  environmentRef?: string
}

export function SettingsTab({
  sovereignId,
  applicationName,
  namespace,
  initialApp,
  disableNetwork = false,
}: SettingsTabProps) {
  const qc = useQueryClient()
  const [upgradeOpen, setUpgradeOpen] = useState(false)
  const [uninstallOpen, setUninstallOpen] = useState(false)
  const [info, setInfo] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const statusQ = useQuery({
    queryKey: ['application-status', sovereignId, applicationName, namespace],
    queryFn: () => getApplicationStatus(sovereignId, applicationName, namespace),
    enabled: !initialApp && !!sovereignId && !!applicationName,
    refetchInterval: 10_000,
  })
  const app: ApplicationStatus | undefined = initialApp ?? statusQ.data

  const blueprintRef = app?.spec?.blueprintRef
  const blueprintVersionQ = useQuery({
    queryKey: ['catalog-item-version', blueprintRef?.name, blueprintRef?.version],
    queryFn: () => getCatalogItemVersion(blueprintRef!.name!, blueprintRef!.version!),
    enabled: !!blueprintRef?.name && !!blueprintRef?.version,
    staleTime: 60_000,
  })
  const blueprintQ = useQuery({
    queryKey: ['catalog-item', blueprintRef?.name],
    queryFn: () => getCatalogItem(blueprintRef!.name!),
    enabled: !!blueprintRef?.name,
    staleTime: 60_000,
  })

  const blueprintForForm: CatalogItem | undefined = blueprintVersionQ.data ?? blueprintQ.data

  const initialParams: Record<string, unknown> | undefined = useMemo(() => {
    return (app?.spec?.parameters as Record<string, unknown>) ?? undefined
  }, [app])

  const handleSaveParameters = async (parameters: Record<string, unknown>) => {
    if (disableNetwork) {
      setInfo('Parameters saved (test seam — no network).')
      return
    }
    setBusy(true)
    setError(null)
    setInfo(null)
    try {
      await updateApplication(sovereignId, applicationName, { parameters }, { namespace })
      setInfo('Parameters saved; controller is reconciling.')
      void qc.invalidateQueries({ queryKey: ['application-status'] })
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="settings-tab" data-testid="app-tab-settings-panel-content">
      <p className="mb-4 text-xs text-[var(--color-text-dim)]" data-testid="settings-tab-intro">
        Edit parameters and Save them to the Application CR; Upgrade the Blueprint to a new
        version (a list of available versions opens in a dialog); or Uninstall this
        Application — the dialog will ask you to Type the application name to confirm.
      </p>

      {/* Parameters editor */}
      <section className="mb-6 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3">
        <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Parameters</h3>
        <p className="mb-3 text-xs text-[var(--color-text-dim)]">
          Edit values against the Blueprint's <code className="font-mono">configSchema</code>.
          Click Save to commit the changes to the Application CR; the controller re-renders
          the manifests. Required fields are marked.
        </p>
        {!blueprintForForm ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="settings-tab-no-blueprint">
            Loading Blueprint…
          </p>
        ) : (
          <InstallForm
            blueprint={blueprintForForm}
            initialFormData={initialParams}
            disabled={busy}
            onSubmit={handleSaveParameters}
            submitLabel="Save"
          />
        )}
        {info ? (
          <div
            className="mt-3 rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2 text-xs text-green-400"
            data-testid="settings-tab-info"
          >
            {info}
          </div>
        ) : null}
        {error ? (
          <div
            className="mt-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
            data-testid="settings-tab-error"
          >
            {error}
          </div>
        ) : null}
      </section>

      {/* Upgrade affordance */}
      <section className="mb-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3">
        <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Upgrade</h3>
        <p className="mb-3 text-xs text-[var(--color-text-dim)]">
          Currently:{' '}
          <code className="font-mono">
            {blueprintRef?.name ?? '?'}@{blueprintRef?.version ?? '?'}
          </code>
        </p>
        <button
          type="button"
          className="rounded-md border border-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-accent)] hover:bg-[var(--color-accent)]/10"
          data-testid="settings-tab-upgrade-btn"
          onClick={() => setUpgradeOpen(true)}
          disabled={!blueprintRef?.name || !blueprintRef?.version}
        >
          Upgrade Blueprint…
        </button>
      </section>

      {/* Danger zone */}
      <section className="rounded-md border border-red-500/40 bg-red-500/5 p-3">
        <h3 className="mb-2 text-sm font-medium text-red-400">Danger zone</h3>
        <p className="mb-3 text-xs text-[var(--color-text-dim)]">
          Uninstalling deletes the Application across every region. The data layer
          survives unless the Blueprint declares cascade-delete.
        </p>
        <button
          type="button"
          className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-1.5 text-xs text-red-300 hover:bg-red-500/20"
          data-testid="settings-tab-uninstall-btn"
          onClick={() => setUninstallOpen(true)}
        >
          Uninstall…
        </button>
      </section>

      <UpgradeDialog
        open={upgradeOpen}
        onClose={() => setUpgradeOpen(false)}
        sovereignId={sovereignId}
        applicationName={applicationName}
        namespace={namespace}
        blueprintName={blueprintRef?.name ?? ''}
        currentVersion={blueprintRef?.version ?? ''}
        disableNetwork={disableNetwork}
        onUpgraded={(v) => {
          setInfo(`Upgraded to ${v}; controller is reconciling.`)
          void qc.invalidateQueries({ queryKey: ['application-status'] })
        }}
      />
      <UninstallDialog
        open={uninstallOpen}
        onClose={() => setUninstallOpen(false)}
        sovereignId={sovereignId}
        applicationName={applicationName}
        namespace={namespace}
        disableNetwork={disableNetwork}
        onUninstalled={() => {
          setInfo('Uninstall queued; controller is cascading region cleanup.')
        }}
      />
    </div>
  )
}
