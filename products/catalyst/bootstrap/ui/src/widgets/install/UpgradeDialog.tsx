/**
 * UpgradeDialog — EPIC-2 Slice O (#1097): post-launch Application
 * upgrade dialog.
 *
 * Reads `Blueprint.spec.upgrades.from` paths from catalyst-catalog and
 * surfaces the upgradeable target versions. On select → posts to
 * /api/v1/sovereigns/{id}/applications/{name}/upgrade/preview to render
 * the target manifests; on confirm → PUTs the new
 * blueprintRef.version on the Application CR.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from API_BASE
 * via the catalog.api helpers. The dialog re-uses the install preview
 * response shape from slice I.
 */

import { useMemo, useState } from 'react'

import {
  getCatalogVersions,
  previewUpgrade,
  updateApplication,
  type ApplicationPreviewResponse,
} from '@/lib/catalog.api'
import { useQuery } from '@tanstack/react-query'

export interface UpgradeDialogProps {
  open: boolean
  onClose: () => void
  sovereignId: string
  applicationName: string
  /** Org namespace. */
  namespace?: string
  /** Current Blueprint name. */
  blueprintName: string
  /** Current Blueprint version. */
  currentVersion: string
  /** Fired after a successful upgrade PUT. */
  onUpgraded?: (newVersion: string) => void
  /** Test seam. */
  disableNetwork?: boolean
}

export function UpgradeDialog({
  open,
  onClose,
  sovereignId,
  applicationName,
  namespace,
  blueprintName,
  currentVersion,
  onUpgraded,
  disableNetwork = false,
}: UpgradeDialogProps) {
  const [target, setTarget] = useState<string>('')
  const [preview, setPreview] = useState<ApplicationPreviewResponse | null>(null)
  const [busy, setBusy] = useState<'preview' | 'apply' | null>(null)
  const [error, setError] = useState<string | null>(null)

  const versionsQ = useQuery({
    queryKey: ['catalog-versions', blueprintName],
    queryFn: () => getCatalogVersions(blueprintName),
    enabled: open && !disableNetwork && !!blueprintName,
    staleTime: 30_000,
  })

  // The catalog returns an upgradeMatrix keyed by from-version. We list
  // the targets reachable from the current version, plus any "newer
  // than current" semver-sorted as a fallback when the matrix is empty.
  const availableTargets = useMemo(() => {
    const matrix = versionsQ.data?.upgradeMatrix ?? {}
    const direct = matrix[currentVersion] ?? []
    if (direct.length > 0) return direct
    const all = (versionsQ.data?.versions ?? []).map((v) => v.version)
    return all.filter((v) => semverGreaterThan(v, currentVersion))
  }, [versionsQ.data, currentVersion])

  const handlePreview = async () => {
    if (!target) return
    if (disableNetwork) {
      setPreview({
        manifests: [{ path: 'preview/stub.yaml', content: '# stub' }],
        diff: '',
        blueprint: { name: blueprintName, version: target },
        warnings: [],
      })
      return
    }
    setBusy('preview')
    setError(null)
    try {
      const resp = await previewUpgrade(sovereignId, applicationName, target, {}, { namespace })
      setPreview(resp)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  const handleApply = async () => {
    if (!target) return
    if (disableNetwork) {
      onUpgraded?.(target)
      onClose()
      return
    }
    setBusy('apply')
    setError(null)
    try {
      await updateApplication(
        sovereignId,
        applicationName,
        { blueprintRef: { name: blueprintName, version: target } },
        { namespace },
      )
      onUpgraded?.(target)
      onClose()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  if (!open) return null

  return (
    <div
      role="dialog"
      aria-modal="true"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      data-testid="upgrade-dialog"
    >
      <div className="max-h-[80vh] w-full max-w-2xl overflow-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-5">
        <div className="mb-3 flex items-baseline justify-between">
          <h3 className="text-base font-semibold text-[var(--color-text)]">
            Upgrade {applicationName} ({blueprintName}@{currentVersion})
          </h3>
          <button
            type="button"
            className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
            data-testid="upgrade-dialog-close"
            onClick={onClose}
          >
            Close
          </button>
        </div>

        <label className="mb-3 block text-xs text-[var(--color-text-dim)]">
          Target version
          <select
            className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm"
            data-testid="upgrade-dialog-target"
            value={target}
            onChange={(e) => {
              setTarget(e.target.value)
              setPreview(null)
            }}
          >
            <option value="">— pick a version —</option>
            {availableTargets.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        </label>
        {availableTargets.length === 0 && versionsQ.data ? (
          <p className="mb-3 text-xs text-[var(--color-text-dim)]" data-testid="upgrade-dialog-no-targets">
            No upgradeable targets — {blueprintName} declares no upgrades from
            <code className="ml-1 font-mono">{currentVersion}</code>.
          </p>
        ) : null}

        {preview ? (
          <div className="mb-3" data-testid="upgrade-dialog-preview">
            <h4 className="mb-2 text-xs font-medium text-[var(--color-text-strong)]">
              Preview — {preview.blueprint.name}@{preview.blueprint.version}
            </h4>
            {preview.warnings.length > 0 ? (
              <ul className="mb-2 list-disc rounded-md border border-yellow-500/40 bg-yellow-500/10 px-4 py-2 pl-6 text-xs text-yellow-300">
                {preview.warnings.map((w, i) => (
                  <li key={i}>{w}</li>
                ))}
              </ul>
            ) : null}
            {preview.manifests.map((m) => (
              <div key={m.path} className="mb-2">
                <div className="text-xs font-mono text-[var(--color-text-dim)]">{m.path}</div>
                <pre className="mt-1 max-h-60 overflow-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg-elev)] p-3 text-[11px] leading-5 text-[var(--color-text)]">
                  {m.content}
                </pre>
              </div>
            ))}
          </div>
        ) : null}

        {error ? (
          <div
            className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
            data-testid="upgrade-dialog-error"
          >
            {error}
          </div>
        ) : null}

        <div className="flex justify-end gap-2">
          <button
            type="button"
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)] disabled:opacity-40"
            data-testid="upgrade-dialog-preview-btn"
            disabled={!target || busy !== null}
            onClick={handlePreview}
          >
            {busy === 'preview' ? 'Previewing…' : 'Preview'}
          </button>
          <button
            type="button"
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:opacity-40"
            data-testid="upgrade-dialog-apply-btn"
            disabled={!target || busy !== null}
            onClick={handleApply}
          >
            {busy === 'apply' ? 'Applying…' : 'Confirm upgrade'}
          </button>
        </div>
      </div>
    </div>
  )
}

// semverGreaterThan — naive comparator (major.minor.patch). Anything
// that doesn't parse loses (returns false) so unrecognized versions
// don't sneak in.
function semverGreaterThan(a: string, b: string): boolean {
  const pa = parseSemver(a)
  const pb = parseSemver(b)
  if (!pa || !pb) return false
  if (pa[0] !== pb[0]) return pa[0] > pb[0]
  if (pa[1] !== pb[1]) return pa[1] > pb[1]
  return pa[2] > pb[2]
}

function parseSemver(v: string): [number, number, number] | null {
  const cleaned = v.split(/[+-]/)[0] ?? v
  const parts = cleaned.split('.').map((p) => Number(p))
  if (parts.length !== 3 || parts.some((n) => Number.isNaN(n))) return null
  return [parts[0]!, parts[1]!, parts[2]!]
}
