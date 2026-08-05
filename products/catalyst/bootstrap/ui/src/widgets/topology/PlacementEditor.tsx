/**
 * PlacementEditor — #3969 the ONE placement editor.
 *
 * The single editor for an Application's desired-state Placement, reused by
 * three entry points (one component, three entries):
 *   1. The Topology tab "Edit placement" affordance (§6.2).
 *   2. The wizard placement step (§6.5) — same editor, pre-filled.
 *   3. Switchover (§6.3) = flipping the Primary role on a target — there is
 *      NO separate imperative verb.
 *
 * Targets: each row is { Region ▾, Cluster ▾, vCluster ▾, role Primary/
 * Standby, standby type Hot/Cold }. [+ add target]. A live "Derived
 * pattern:" label (singleton/active-passive/active-hot-standby/active-active)
 * computed from the targets — never stored, never selected. The capability
 * line gates the Primary radio: a 2nd Primary is disabled when the blueprint
 * is primary+standby (MultiPrimaryNotSupported). The "Owned dependencies —
 * follow by default (override ok)" section renders per-dep [✓ follow]/[edit].
 *
 * There is NO mode picker, NO separate DR surface, NO declared/derived-class
 * rendering — all deleted by #3969.
 */

import { useEffect, useMemo, useState } from 'react'

import { updateApplication } from '@/lib/catalog.api'
import {
  type Capability,
  type DataRole,
  type OwnedDependencyOverride,
  type PlacementTarget,
  type StandbyType,
  PATTERN_NOT_REPORTED,
  canAddPrimary,
  derivePattern,
  describePattern,
  patternLabel,
  validatePlacement,
} from '@/shared/lib/placement'

/** An owned dependency surfaced in the cascade section. */
export interface OwnedDependencyInfo {
  /** The owned instance name (e.g. keycloak-pg). */
  name: string
  /** Follow the consumer placement by default (true) or decoupled (false). */
  follow: boolean
}

export interface PlacementEditorProps {
  /** Sovereign id. */
  sovereignId: string
  /** Application name. */
  applicationName: string
  /** Org namespace for the Application CR. */
  namespace?: string
  /** The current targets to seed the editor (empty ⇒ a single Primary row). */
  initialTargets: PlacementTarget[]
  /** The blueprint capability — gates >1 Primary. */
  capability: Capability
  /** Region ids available on this Sovereign (the Region ▾ options). */
  availableRegions: string[]
  /** Cluster ids available (the Cluster ▾ options). */
  availableClusters: string[]
  /** vCluster tiers available (the vCluster ▾ options). Default host/mgmt. */
  availableVClusters?: string[]
  /** The owned dependencies that cascade by default. */
  ownedDependencies?: OwnedDependencyInfo[]
  /** Fired after a successful Apply (or in embedded mode, on each change). */
  onApplied?: () => void
  /**
   * Embedded mode (wizard, §6.5): render the editor body without the
   * Apply/Cancel action bar, and report the live targets to the parent via
   * `onChange` so the wizard owns submission. Default false (standalone).
   */
  embedded?: boolean
  /** Embedded change callback — fires the live targets + owned overrides. */
  onChange?: (targets: PlacementTarget[], ownedDependencies: OwnedDependencyOverride[]) => void
  /** Cancel affordance (standalone mode). */
  onCancel?: () => void
  /** Test seam — bypass the network call. */
  disableNetwork?: boolean
}

const DEFAULT_VCLUSTERS = ['host', 'mgmt', 'dmz', 'rtz']

export function PlacementEditor({
  sovereignId,
  applicationName,
  namespace,
  initialTargets,
  capability,
  availableRegions,
  availableClusters,
  availableVClusters,
  ownedDependencies = [],
  onApplied,
  embedded = false,
  onChange,
  onCancel,
  disableNetwork = false,
}: PlacementEditorProps) {
  // Seed the editor state once at mount. The parent keys this component on
  // applicationName, so a different Application remounts the editor and the
  // initializers below re-run — no re-seed effect (the lint-correct pattern).
  const [targets, setTargets] = useState<PlacementTarget[]>(() => {
    if (initialTargets.length > 0) return initialTargets.map((t) => ({ ...t }))
    // A fresh placement starts as a single Primary target.
    return [
      {
        region: availableRegions[0] ?? '',
        cluster: availableClusters[0] ?? '',
        vcluster: (availableVClusters ?? DEFAULT_VCLUSTERS)[1] ?? 'mgmt',
        role: 'Primary' as DataRole,
      },
    ]
  })
  const [owned, setOwned] = useState<OwnedDependencyInfo[]>(ownedDependencies)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [info, setInfo] = useState<string | null>(null)

  const vclusters = availableVClusters ?? DEFAULT_VCLUSTERS
  const pattern = useMemo(() => derivePattern(targets), [targets])
  const validation = useMemo(() => validatePlacement(targets, capability), [targets, capability])

  // Report live state up in embedded (wizard) mode.
  useEffect(() => {
    if (embedded && onChange) {
      onChange(
        targets,
        owned.map((o) => ({ name: o.name, follow: o.follow })),
      )
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targets, owned, embedded])

  const setTarget = (i: number, patch: Partial<PlacementTarget>) => {
    setTargets((prev) =>
      prev.map((t, idx) => {
        if (idx !== i) return t
        const next = { ...t, ...patch }
        // Role/standbyType invariants: Primary carries no type; Standby
        // defaults to Hot when switched from Primary.
        if (next.role === 'Primary') {
          delete next.standbyType
        } else if (next.role === 'Standby' && !next.standbyType) {
          next.standbyType = 'Hot'
        }
        return next
      }),
    )
  }

  const addTarget = () => {
    setTargets((prev) => {
      // A new target defaults to a Hot Standby (the common multi-region add).
      const used = new Set(prev.map((t) => t.region))
      const region = availableRegions.find((r) => !used.has(r)) ?? availableRegions[1] ?? availableRegions[0] ?? ''
      const usedClusters = new Set(prev.map((t) => t.cluster))
      const cluster = availableClusters.find((c) => !usedClusters.has(c)) ?? availableClusters[1] ?? availableClusters[0] ?? ''
      return [
        ...prev,
        { region, cluster, vcluster: prev[0]?.vcluster ?? 'mgmt', role: 'Standby' as DataRole, standbyType: 'Hot' as StandbyType },
      ]
    })
  }

  const removeTarget = (i: number) => {
    setTargets((prev) => (prev.length <= 1 ? prev : prev.filter((_, idx) => idx !== i)))
  }

  const apply = async () => {
    if (validation) {
      setError(validation.message)
      return
    }
    if (disableNetwork) {
      setInfo('Placement applied (test seam — no network).')
      onApplied?.()
      return
    }
    setBusy(true)
    setError(null)
    setInfo(null)
    try {
      await updateApplication(
        sovereignId,
        applicationName,
        {
          placement: {
            targets: targets.map((t) => ({
              region: t.region,
              cluster: t.cluster,
              vcluster: t.vcluster,
              role: t.role,
              ...(t.role === 'Standby' && t.standbyType ? { standbyType: t.standbyType } : {}),
            })),
            ownedDependencies: owned.map((o) => ({ name: o.name, follow: o.follow })),
          },
        },
        { namespace },
      )
      setInfo('Placement submitted; the controller is reconciling.')
      onApplied?.()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const capabilityLine =
    capability === 'multi-primary'
      ? 'multi-primary (multiple Primary targets allowed)'
      : 'primary + standby (multi-Primary NOT allowed)'

  return (
    <div className="placement-editor" data-testid="placement-editor">
      <p className="mb-3 text-xs text-[var(--color-text-dim)]" data-testid="placement-editor-capability">
        Capability: <span className="font-medium text-[var(--color-text)]">{capabilityLine}</span>
      </p>

      <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">Targets</h4>
      <ul className="mb-3 space-y-2" data-testid="placement-editor-targets">
        {targets.map((t, i) => {
          // The Primary radio is disabled for a target that is currently
          // NOT Primary when the capability already has its one Primary
          // (primary+standby gate). A target already Primary stays selectable.
          const primaryDisabled = t.role !== 'Primary' && !canAddPrimary(targets, capability)
          // #5639 — the option list is `[current, ...available]` minus
          // falsy entries. When the infra topology query returned nothing
          // (it is gated on sovereignId, 401s without a session, and throws
          // on any non-2xx) AND the target carries no region, BOTH halves
          // are falsy and this renders a <select> with ZERO options: a blank
          // box that reads identically to "still loading". Compute it once
          // so the empty case can say so out loud instead.
          const regionOptions = [
            t.region,
            ...availableRegions.filter((r) => r !== t.region),
          ].filter(Boolean)
          return (
            <li
              key={i}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2"
              data-testid={`placement-editor-target-${i}`}
            >
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <label className="flex items-center gap-1">
                  <span className="text-[var(--color-text-dim)]">Region</span>
                  <select
                    className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-1.5 py-1 font-mono"
                    value={t.region}
                    onChange={(e) => setTarget(i, { region: e.target.value })}
                    data-testid={`placement-editor-target-${i}-region`}
                  >
                    {regionOptions.length === 0 ? (
                      <option value="" data-testid={`placement-editor-target-${i}-region-empty`}>
                        — no regions reported —
                      </option>
                    ) : null}
                    {regionOptions.map((r) => (
                      <option key={r} value={r}>
                        {r}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="flex items-center gap-1">
                  <span className="text-[var(--color-text-dim)]">Cluster</span>
                  <select
                    className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-1.5 py-1 font-mono"
                    value={t.cluster}
                    onChange={(e) => setTarget(i, { cluster: e.target.value })}
                    data-testid={`placement-editor-target-${i}-cluster`}
                  >
                    {[t.cluster, ...availableClusters.filter((c) => c !== t.cluster)].filter(Boolean).map((c) => (
                      <option key={c} value={c}>
                        {c}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="flex items-center gap-1">
                  <span className="text-[var(--color-text-dim)]">vCluster</span>
                  <select
                    className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-1.5 py-1 font-mono"
                    value={t.vcluster ?? 'mgmt'}
                    onChange={(e) => setTarget(i, { vcluster: e.target.value })}
                    data-testid={`placement-editor-target-${i}-vcluster`}
                  >
                    {vclusters.map((v) => (
                      <option key={v} value={v}>
                        {v}
                      </option>
                    ))}
                  </select>
                </label>
                {targets.length > 1 ? (
                  <button
                    type="button"
                    className="ml-auto rounded px-1.5 py-1 text-[var(--color-text-dim)] hover:text-red-400"
                    onClick={() => removeTarget(i)}
                    data-testid={`placement-editor-target-${i}-remove`}
                    aria-label={`remove target ${i + 1}`}
                  >
                    ✕
                  </button>
                ) : null}
              </div>

              <div className="mt-2 flex flex-wrap items-center gap-3 text-xs">
                <span className="text-[var(--color-text-dim)]">Role</span>
                <label className={`flex items-center gap-1 ${primaryDisabled ? 'opacity-40' : 'cursor-pointer'}`}>
                  <input
                    type="radio"
                    name={`role-${i}`}
                    checked={t.role === 'Primary'}
                    disabled={primaryDisabled}
                    onChange={() => setTarget(i, { role: 'Primary' })}
                    data-testid={`placement-editor-target-${i}-role-primary`}
                  />
                  <span>Primary</span>
                </label>
                <label className="flex cursor-pointer items-center gap-1">
                  <input
                    type="radio"
                    name={`role-${i}`}
                    checked={t.role === 'Standby'}
                    onChange={() => setTarget(i, { role: 'Standby' })}
                    data-testid={`placement-editor-target-${i}-role-standby`}
                  />
                  <span>Standby</span>
                </label>

                {t.role === 'Standby' ? (
                  <span className="flex items-center gap-2" data-testid={`placement-editor-target-${i}-standby-type`}>
                    <span className="text-[var(--color-text-dim)]">Type</span>
                    <label className="flex cursor-pointer items-center gap-1">
                      <input
                        type="radio"
                        name={`stype-${i}`}
                        checked={t.standbyType === 'Hot'}
                        onChange={() => setTarget(i, { standbyType: 'Hot' })}
                        data-testid={`placement-editor-target-${i}-standby-hot`}
                      />
                      <span>Hot</span>
                    </label>
                    <label className="flex cursor-pointer items-center gap-1">
                      <input
                        type="radio"
                        name={`stype-${i}`}
                        checked={t.standbyType === 'Cold'}
                        onChange={() => setTarget(i, { standbyType: 'Cold' })}
                        data-testid={`placement-editor-target-${i}-standby-cold`}
                      />
                      <span>Cold</span>
                    </label>
                  </span>
                ) : null}

                {primaryDisabled ? (
                  <span
                    className="text-[10px] text-yellow-400"
                    data-testid={`placement-editor-target-${i}-multiprimary-reason`}
                  >
                    multi-Primary not supported by this blueprint
                  </span>
                ) : null}
              </div>
            </li>
          )
        })}
      </ul>

      <button
        type="button"
        className="mb-3 rounded-md border border-[var(--color-border)] px-2.5 py-1 text-xs hover:border-[var(--color-accent)]"
        onClick={addTarget}
        data-testid="placement-editor-add-target"
      >
        + add target
      </button>

      {/* #5515 — the un-derivable case (every target removed / no Primary)
          reads as prose in the dim colour; never a confident pattern name. */}
      <p className="mb-3 text-xs" data-testid="placement-editor-derived-pattern">
        <span className="text-[var(--color-text-dim)]">Derived pattern: </span>
        <span
          className={
            pattern === PATTERN_NOT_REPORTED
              ? 'font-medium italic text-[var(--color-text-dim)]'
              : 'font-medium text-[var(--color-accent)]'
          }
        >
          {patternLabel(pattern)}
        </span>
        <span className="ml-2 text-[var(--color-text-dim)]">{describePattern(pattern)}</span>
      </p>

      {owned.length > 0 ? (
        <div className="mb-3" data-testid="placement-editor-owned-deps">
          <h4 className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
            Owned dependencies — follow by default (override ok)
          </h4>
          <ul className="space-y-1">
            {owned.map((d, i) => (
              <li
                key={d.name}
                className="flex items-center gap-3 text-xs"
                data-testid={`placement-editor-owned-${d.name}`}
              >
                <code className="font-mono text-[var(--color-text)]">{d.name}</code>
                <label className="flex cursor-pointer items-center gap-1">
                  <input
                    type="checkbox"
                    checked={d.follow}
                    onChange={(e) =>
                      setOwned((prev) => prev.map((o, idx) => (idx === i ? { ...o, follow: e.target.checked } : o)))
                    }
                    data-testid={`placement-editor-owned-${d.name}-follow`}
                  />
                  <span className={d.follow ? 'text-green-400' : 'text-yellow-400'}>
                    {d.follow ? '✓ follow' : 'decoupled (pinned)'}
                  </span>
                </label>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {validation ? (
        <div
          className="mb-3 rounded-md border border-yellow-500/40 bg-yellow-500/10 px-3 py-2 text-xs text-yellow-300"
          data-testid="placement-editor-validation"
        >
          {validation.message}
        </div>
      ) : null}

      {error ? (
        <div
          className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
          data-testid="placement-editor-error"
        >
          {error}
        </div>
      ) : null}
      {info ? (
        <div
          className="mb-3 rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2 text-xs text-green-400"
          data-testid="placement-editor-info"
        >
          {info}
        </div>
      ) : null}

      {!embedded ? (
        <div className="flex gap-2" data-testid="placement-editor-actions">
          {onCancel ? (
            <button
              type="button"
              className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)]"
              onClick={onCancel}
              data-testid="placement-editor-cancel"
            >
              Cancel
            </button>
          ) : null}
          <button
            type="button"
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:opacity-40"
            disabled={busy || validation !== null}
            onClick={apply}
            data-testid="placement-editor-apply"
          >
            {busy ? 'Applying…' : 'Apply'}
          </button>
        </div>
      ) : null}
    </div>
  )
}
