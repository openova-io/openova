/**
 * TopologyEditor — EPIC-2 Slice T (#1097): Application topology editor.
 *
 * Lets a tier-admin or higher operator change a running Application's
 * placement (single-region | active-active | active-hotstandby) and the
 * region set. Constrained by `Blueprint.spec.placementSchema.modes[]`
 * — modes the Blueprint marks as unsupported are disabled.
 *
 * Three panels:
 *
 *  1. Mode picker (radio group)
 *  2. Region multi-select (checkboxes from the Sovereign's clusters)
 *  3. Action bar — Preview (opens diff modal) + Apply (PUT to /applications/{name})
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from API_BASE
 * via the catalog.api helpers. Per #5 the underlying handler enforces
 * tier-admin or higher; this widget renders even for viewers but
 * disables Apply when the auth context lacks the role (the server is
 * still the gate).
 *
 * Integrates with `previewTopologyChange` from catalog.api so the
 * Preview affordance reuses the install-preview response shape (the
 * same UI a user sees pre-install). The `onApplied` callback fires on
 * successful PUT so the parent (TopologyTab) can refetch the live
 * status panel.
 */

import { useEffect, useMemo, useState } from 'react'

import {
  previewTopologyChange,
  updateApplication,
  type ApplicationPreviewResponse,
  type CatalogItem,
} from '@/lib/catalog.api'

export interface TopologyEditorProps {
  sovereignId: string
  applicationName: string
  /** Current placement mode (`single-region` | `active-active` | `active-hotstandby`). */
  currentMode: string
  /** Current regions (deployed-to). */
  currentRegions: string[]
  /** Available regions for the Sovereign — typically from /clusters. */
  availableRegions: string[]
  /** Org namespace for the Application CR. */
  namespace?: string
  /** Blueprint card backing this Application — used to constrain modes. */
  blueprint?: CatalogItem
  /**
   * #3648 — the Blueprint's canonical SupportedTopologies (singleton /
   * active-active / active-hot-standby / active-passive). When provided, the
   * picker offers ONLY the modes the Blueprint supports, never one it
   * doesn't (the AppDetail "active-active on a hot-standby-only app"
   * contradiction). Falls back to placementSchema.modes / ALL_MODES.
   */
  supportedCanonical?: string[]
  /** Fired after a successful Apply. */
  onApplied?: () => void
  /** Test seam — bypass the network call for snapshot testing. */
  disableNetwork?: boolean
}

/**
 * ALL_MODES — the topology-mode option set for the placement picker.
 * Exported (#3599, EPIC #3597) so the create-from-catalog placement step
 * reuses the SAME option set the post-create Topology editor offers,
 * instead of reinventing it.
 *
 * ONE canonical vocabulary (#3375 DoD-1). The editor RENDERS and SELECTS
 * the four canonical classes — `singleton` / `active-active` /
 * `active-hot-standby` / `active-passive` — the exact set the catalog
 * placementSchema serves, the Application CR stores, and the
 * application-controller's resolver accepts. The editor previously spoke
 * the legacy dialect (`single-region` / `active-hotstandby`) which (a)
 * could not represent `active-passive`/`singleton` and (b) drifted from
 * the catalog + CR. canonicalizeMode still folds any legacy value the
 * server returns, so loading an older CR keeps working. The
 * `allowedModes`/`supportedCanonical` gate hides any class the blueprint
 * doesn't declare; this set only makes every canonical class
 * REPRESENTABLE.
 */
export const ALL_MODES = ['singleton', 'active-active', 'active-hot-standby', 'active-passive'] as const

export type TopologyMode = (typeof ALL_MODES)[number]

/**
 * canonicalizeMode (#3648, #3375 DoD-1) folds BOTH the legacy editor
 * dialect (single-region / active-hotstandby) AND the canonical
 * vocabulary (singleton / active-active / active-hot-standby /
 * active-passive) onto the ONE canonical token, so any value the server
 * returns (an older CR, a legacy POST) compares correctly against the
 * canonical ALL_MODES / SupportedTopologies. Mirrors the Go single
 * source of truth (placement.Canonicalize) — the CI drift test asserts
 * the FE + Go + catalyst-api agree.
 */
export function canonicalizeMode(raw: string): string {
  switch (raw.trim().toLowerCase()) {
    case 'active-hot-standby':
    case 'active-hotstandby':
      return 'active-hot-standby'
    case 'active-passive':
      return 'active-passive'
    case 'active-active':
      return 'active-active'
    case 'singleton':
    case 'single-region':
      return 'singleton'
    default:
      return raw.trim().toLowerCase()
  }
}

export function TopologyEditor({
  sovereignId,
  applicationName,
  currentMode,
  currentRegions,
  availableRegions,
  namespace,
  blueprint,
  supportedCanonical,
  onApplied,
  disableNetwork = false,
}: TopologyEditorProps) {
  // One vocabulary (#3375 DoD-1): the editor works in canonical tokens.
  // Any legacy currentMode the server returns is folded to canonical so
  // the radio pre-selects correctly and the POST carries canonical.
  const [mode, setMode] = useState<string>(currentMode ? canonicalizeMode(currentMode) : 'singleton')
  const [regions, setRegions] = useState<string[]>(currentRegions)
  const [preview, setPreview] = useState<ApplicationPreviewResponse | null>(null)
  const [busy, setBusy] = useState<'preview' | 'apply' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [info, setInfo] = useState<string | null>(null)
  const [forceConfirm, setForceConfirm] = useState(false)

  // Reset when a different Application is loaded into the tab.
  useEffect(() => {
    setMode(currentMode ? canonicalizeMode(currentMode) : 'singleton')
    setRegions(currentRegions)
    setPreview(null)
    setError(null)
    setInfo(null)
    setForceConfirm(false)
  }, [currentMode, applicationName, currentRegions])

  const allowedModes = useMemo(() => {
    // #3648 — prefer the Blueprint's canonical SupportedTopologies (the
    // topology-matrix truth) so the picker never offers a mode the Blueprint
    // doesn't support (e.g. active-active on a hot-standby-only app — the
    // AppDetail contradiction the operator flagged 3×). Compare canonically
    // so the editor dialect (active-hotstandby) matches the canonical form
    // (active-hot-standby).
    if (Array.isArray(supportedCanonical) && supportedCanonical.length > 0) {
      const canon = new Set(supportedCanonical.map(canonicalizeMode))
      return new Set<string>(ALL_MODES.filter((m) => canon.has(canonicalizeMode(m))))
    }
    const fromSchema = blueprint?.placementSchema?.modes
    if (Array.isArray(fromSchema) && fromSchema.length > 0) {
      // One vocabulary (#3375 DoD-1): canonicalise the declared modes so a
      // Blueprint that uses the legacy spelling (single-region /
      // active-hotstandby) in placementSchema.modes still enables the
      // matching CANONICAL radio (ALL_MODES is canonical).
      const canon = new Set(fromSchema.map(canonicalizeMode))
      return new Set<string>(ALL_MODES.filter((m) => canon.has(canonicalizeMode(m))))
    }
    return new Set<string>(ALL_MODES)
  }, [supportedCanonical, blueprint])

  // Compare canonically so a canonical `mode` vs a legacy `currentMode`
  // (older CR) is not falsely flagged dirty (#3375 DoD-1).
  const isDirty = canonicalizeMode(mode) !== canonicalizeMode(currentMode) || !sameRegionSet(regions, currentRegions)

  // Compute whether the proposed change is destructive (regions drop or
  // mode collapse) — surfaces the force-confirm UI client-side. Compare
  // on the canonical token so the guard fires regardless of spelling.
  const requiresForce = useMemo(() => {
    const cur = canonicalizeMode(currentMode)
    if (
      canonicalizeMode(mode) === 'singleton' &&
      (cur === 'active-active' || cur === 'active-hot-standby' || cur === 'active-passive')
    ) {
      return true
    }
    if (regions.length < currentRegions.length) {
      return true
    }
    return false
  }, [mode, regions, currentMode, currentRegions])

  const onToggleRegion = (region: string) => {
    setRegions((prev) => (prev.includes(region) ? prev.filter((r) => r !== region) : [...prev, region]))
  }

  const onPreview = async () => {
    if (disableNetwork) {
      setPreview({
        manifests: [
          {
            path: `clusters/${regions[0] ?? '?'}/applications/${applicationName}/helmrelease.yaml`,
            content: '# stub preview (test seam)',
          },
        ],
        diff: '',
        blueprint: { name: blueprint?.name ?? 'unknown', version: blueprint?.version ?? '0.0.0' },
        warnings: [],
      })
      return
    }
    setBusy('preview')
    setError(null)
    try {
      const resp = await previewTopologyChange(
        sovereignId,
        applicationName,
        { placement: { mode, regions } },
        { namespace },
      )
      setPreview(resp)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  const onApply = async () => {
    if (disableNetwork) {
      setInfo('Topology change applied (test seam — no network).')
      onApplied?.()
      return
    }
    setBusy('apply')
    setError(null)
    setInfo(null)
    try {
      await updateApplication(
        sovereignId,
        applicationName,
        { placement: { mode, regions } },
        { namespace, force: requiresForce && forceConfirm },
      )
      setInfo('Topology change submitted; controller is reconciling.')
      setPreview(null)
      onApplied?.()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="topology-editor" data-testid="topology-editor">
      <h3 className="mb-3 text-sm font-medium text-[var(--color-text-strong)]">Topology mode</h3>
      <fieldset className="mb-4 space-y-2" data-testid="topology-editor-modes">
        {ALL_MODES.map((m) => {
          const enabled = allowedModes.has(m)
          return (
            <label
              key={m}
              className={`flex cursor-pointer items-baseline gap-2 rounded-md border border-[var(--color-border)] px-3 py-2 ${
                mode === m ? 'bg-[var(--color-accent)]/10 border-[var(--color-accent)]' : ''
              } ${enabled ? '' : 'opacity-40 cursor-not-allowed'}`}
              data-testid={`topology-editor-mode-${m}`}
            >
              <input
                type="radio"
                name="topology-mode"
                value={m}
                checked={mode === m}
                disabled={!enabled}
                onChange={() => setMode(m)}
                data-testid={`topology-editor-mode-${m}-radio`}
              />
              <span className="text-sm font-medium text-[var(--color-text)]">{m}</span>
              <span className="ml-2 text-xs text-[var(--color-text-dim)]">{describeMode(m)}</span>
            </label>
          )
        })}
      </fieldset>

      <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Regions</h3>
      {availableRegions.length === 0 ? (
        <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-editor-regions-empty">
          No data-plane clusters listed for this Sovereign.
        </p>
      ) : (
        <ul className="mb-4 grid grid-cols-1 gap-1 md:grid-cols-2" data-testid="topology-editor-regions">
          {availableRegions.map((region) => (
            <li key={region}>
              <label
                className={`flex cursor-pointer items-baseline gap-2 rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs ${
                  regions.includes(region) ? 'bg-[var(--color-accent)]/10 border-[var(--color-accent)]' : ''
                }`}
                data-testid={`topology-editor-region-${region}`}
              >
                <input
                  type="checkbox"
                  checked={regions.includes(region)}
                  onChange={() => onToggleRegion(region)}
                  data-testid={`topology-editor-region-${region}-checkbox`}
                />
                <code className="font-mono text-[var(--color-text)]">{region}</code>
              </label>
            </li>
          ))}
        </ul>
      )}

      {requiresForce ? (
        <div
          className="mb-4 rounded-md border border-yellow-500/40 bg-yellow-500/10 px-3 py-2 text-xs text-yellow-300"
          data-testid="topology-editor-force-warning"
        >
          <strong>Destructive change detected</strong> — regions drop or replica scale-down.
          <label className="mt-2 flex items-baseline gap-2">
            <input
              type="checkbox"
              checked={forceConfirm}
              onChange={(e) => setForceConfirm(e.target.checked)}
              data-testid="topology-editor-force-confirm"
            />
            <span>I understand replicas will be removed; proceed with force=true.</span>
          </label>
        </div>
      ) : null}

      <div className="mb-4 flex gap-2" data-testid="topology-editor-actions">
        <button
          type="button"
          className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)]"
          data-testid="topology-editor-preview-btn"
          disabled={!isDirty || busy !== null}
          onClick={onPreview}
        >
          {busy === 'preview' ? 'Previewing…' : 'Preview'}
        </button>
        <button
          type="button"
          className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:opacity-40"
          data-testid="topology-editor-apply-btn"
          disabled={!isDirty || busy !== null || (requiresForce && !forceConfirm) || regions.length === 0}
          onClick={onApply}
        >
          {busy === 'apply' ? 'Applying…' : 'Apply'}
        </button>
      </div>

      {error ? (
        <div
          className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
          data-testid="topology-editor-error"
        >
          {error}
        </div>
      ) : null}
      {info ? (
        <div
          className="mb-3 rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2 text-xs text-green-400"
          data-testid="topology-editor-info"
        >
          {info}
        </div>
      ) : null}

      {preview ? (
        <div
          role="dialog"
          aria-modal="true"
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
          data-testid="topology-editor-preview-modal"
        >
          <div className="max-h-[80vh] w-full max-w-3xl overflow-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-5">
            <div className="mb-3 flex items-baseline justify-between">
              <h3 className="text-base font-semibold text-[var(--color-text)]">
                Topology preview — {preview.blueprint.name}@{preview.blueprint.version}
              </h3>
              <button
                type="button"
                className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
                data-testid="topology-editor-preview-close"
                onClick={() => setPreview(null)}
              >
                Close
              </button>
            </div>
            {preview.warnings.length > 0 ? (
              <ul className="mb-3 list-disc rounded-md border border-yellow-500/40 bg-yellow-500/10 px-4 py-2 pl-6 text-xs text-yellow-300">
                {preview.warnings.map((w, i) => (
                  <li key={i}>{w}</li>
                ))}
              </ul>
            ) : null}
            {preview.manifests.map((m) => (
              <div key={m.path} className="mb-3" data-testid={`topology-editor-preview-manifest-${m.path}`}>
                <div className="text-xs font-mono text-[var(--color-text-dim)]">{m.path}</div>
                <pre className="mt-1 overflow-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg-elev)] p-3 text-[11px] leading-5 text-[var(--color-text)]">
                  {m.content}
                </pre>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  )
}

/**
 * describeMode — one-line human description per topology mode. Exported
 * (#3599) so the create-flow placement step shows the same helper text
 * the post-create editor shows for each mode. Canonicalises first so it
 * describes both the canonical tokens and any legacy spelling
 * (#3375 DoD-1).
 */
export function describeMode(mode: string): string {
  switch (canonicalizeMode(mode)) {
    case 'singleton':
      return 'one cluster; lowest cost; no failover'
    case 'active-active':
      return 'every region serves traffic; horizontal scaling'
    case 'active-hot-standby':
      return 'primary serves; standby ready for switchover'
    case 'active-passive':
      return 'primary serves; passive cold/warm standby (backup-restore)'
    default:
      return ''
  }
}

function sameRegionSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const setA = new Set(a)
  for (const x of b) {
    if (!setA.has(x)) return false
  }
  return true
}
