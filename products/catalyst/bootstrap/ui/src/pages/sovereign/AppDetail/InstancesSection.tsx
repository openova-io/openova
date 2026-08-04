/**
 * InstancesSection + NewInstanceDialog — G117.2 #2741 CLASS-page widgets.
 *
 * These two components belong to the CATALOG / class surface
 * (`/catalog/$blueprintName` → CatalogDetail), NOT the per-instance
 * surface (`/app/$componentId` → AppDetail). They were originally
 * inlined in AppDetail.tsx, which made AppDetail wrongly render the
 * class instances-list + "+ New instance" button on what is supposed to
 * be a single deployed-instance page (#3090). Extracted here so both the
 * class page (CatalogDetail) can own them and AppDetail can drop the
 * class-level content while keeping its own per-cluster fan-out view.
 *
 * Backed by:
 *   GET  /catalyst/v1/catalog/{blueprint}/instances → instances list
 *   POST /catalyst/v1/apps/instances                → create instance
 */

import { useEffect, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getApplicationInstances,
  createApplicationInstance,
  getCatalogItemVersion,
  type ApplicationInstanceSummary,
  type BackingSelection,
  type CreateApplicationInstanceRequest,
} from '@/lib/catalog.api'
import { listOrganizations, type OrgRow } from '@/lib/organizations.api'
import { getHierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { ALL_MODES, canonicalizeMode, describeModeForComponent } from '@/widgets/topology/TopologyEditor'
import { BLUEPRINT_BY_ID, TOPOLOGY_BY_ID } from '@/shared/constants/catalog.generated'

// #3599 / #3600 / #5616 — the tier namespaces the backend can RESOLVE:
// catalyst-api validates `placement.vcluster ∈ {host, mgmt, dmz, rtz}`
// (instances/create.go ValidateShape) and the application-controller
// maps each tier key to the SAME-NAMED host namespace when addressing
// the per-cluster HelmRelease (VClusterPlacements defaults). #5616 —
// these are OFFERED only when the live topology reports a vCluster
// actually running in that namespace: a fabricated option lands the
// HelmRelease in a namespace that does not exist and the install
// Degrades (`namespaces "rtz" not found`, hw292 walk 2026-08-04).
// "host" is the one namespace-independent target (the controller
// normalizes it to the host-cluster install in the Org's own
// namespace), so it is always offered.
const RESOLVABLE_TIER_NAMESPACES = ['mgmt', 'dmz', 'rtz'] as const

/**
 * Modes that require ≥2 regions (the create-flow validation rule). ONE
 * canonical vocabulary (#3375 DoD-1) — active-passive is multi-region
 * too. Membership is tested on the canonical token (canonicalEditorMode).
 */
const MULTI_REGION_MODES = new Set(['active-active', 'active-hot-standby', 'active-passive'])

/**
 * InstancesSection — renders the per-Blueprint instances list (one row
 * per concrete install) plus a "+ New instance" button that opens the
 * topology-picker dialog inline (no navigation). This is the class →
 * instance drill-down the H10 walk requires: the CLASS surface lists
 * every concrete instance the operator can drill into.
 *
 * The optional `perCluster` block is preserved for callers that still
 * want to surface a CURRENT-Application fan-out table here, but the
 * canonical home for per-cluster status of a single instance is now the
 * instance page (AppDetail) itself — CatalogDetail does not pass it.
 *
 * On 404 / empty list, the list block renders an empty-state with the
 * "+ New instance" button still active so the operator can install the
 * first instance.
 */
export function InstancesSection({
  blueprint,
  appUID,
  perCluster,
  multiInstance,
}: {
  blueprint: string
  appUID?: string
  perCluster?: Array<Record<string, unknown>>
  /**
   * #3648 (founder #6) — whether this Blueprint allows >1 instance per Org.
   * When false (singleton-per-Org) and an instance already exists, the
   * "+ New instance" button is hidden — you can't create a second. Undefined
   * is treated as multi-instance (button shows once the list resolves).
   */
  multiInstance?: boolean
}) {
  const [dialogOpen, setDialogOpen] = useState(false)
  const qc = useQueryClient()

  const instancesQuery = useQuery({
    queryKey: ['blueprint-instances', blueprint],
    queryFn: () => getApplicationInstances(blueprint),
    enabled: !!blueprint,
    staleTime: 15_000,
    refetchInterval: 15_000,
    retry: 1,
  })

  const items: ApplicationInstanceSummary[] = instancesQuery.data?.items ?? []

  return (
    <section
      className="section"
      data-testid="sov-section-instances"
      style={{ marginTop: '0.6rem' }}
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '0.4rem',
        }}
      >
        <h2 style={{ margin: 0 }}>Instances</h2>
        {/* #3648 (founder #6) — never flash a definitive action before the
            data resolves, and never offer "New instance" for a singleton that
            already has its one instance. Show only once the list has loaded
            AND another instance is actually creatable. */}
        {!instancesQuery.isPending && (multiInstance !== false || items.length === 0) ? (
          <button
            type="button"
            data-testid="btn-new-instance"
            onClick={() => setDialogOpen(true)}
            className="btn btn-primary"
            style={{
              padding: '0.3rem 0.8rem',
              fontSize: '0.82rem',
              background: 'var(--color-accent)',
              color: 'white',
              border: '0',
              borderRadius: '6px',
              cursor: 'pointer',
              fontWeight: 600,
            }}
            aria-label="Create a new instance of this Blueprint"
          >
            + New instance
          </button>
        ) : null}
      </div>

      {instancesQuery.isPending ? (
        <p
          className="section-hint"
          data-testid="sov-instances-loading"
          style={{ margin: 0 }}
        >
          Loading instances…
        </p>
      ) : items.length === 0 ? (
        // #3165 — first-class empty state. Not a broken/blank table: a
        // clear "no instances yet" message with the "+ New instance" CTA
        // already rendered in the header above.
        <div
          className="instances-empty"
          data-testid="sov-instances-empty"
          style={{
            border: '1px dashed var(--color-border)',
            borderRadius: '10px',
            padding: '1.4rem 1rem',
            textAlign: 'center',
          }}
        >
          <p
            className="section-hint"
            style={{ margin: '0 0 0.7rem', fontSize: '0.88rem' }}
          >
            No instances of this Blueprint in this Organization yet.
          </p>
          <button
            type="button"
            data-testid="btn-new-instance-empty"
            onClick={() => setDialogOpen(true)}
            className="btn btn-primary"
            style={{
              padding: '0.4rem 0.9rem',
              fontSize: '0.85rem',
              background: 'var(--color-accent)',
              color: 'white',
              border: '0',
              borderRadius: '6px',
              cursor: 'pointer',
              fontWeight: 600,
            }}
            aria-label="Create the first instance of this Blueprint"
          >
            + New instance
          </button>
        </div>
      ) : (
        <table
          data-testid="sov-instances-table"
          className="instances-table"
          style={{
            width: '100%',
            borderCollapse: 'collapse',
            fontSize: '0.82rem',
          }}
        >
          <thead>
            <tr
              style={{
                textAlign: 'left',
                color: 'var(--color-text-dim)',
                borderBottom: '1px solid var(--color-border)',
              }}
            >
              <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Name</th>
              <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Org</th>
              <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Topology</th>
              <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Status</th>
              <th style={{ padding: '0.4rem 0.5rem', fontWeight: 600 }}>Version</th>
              <th
                style={{ padding: '0.4rem 0.5rem', fontWeight: 600, textAlign: 'right' }}
              >
                Actions
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((inst) => (
              <tr
                key={inst.id || inst.name}
                data-testid={`sov-instance-row-${inst.name}`}
                style={{ borderBottom: '1px solid var(--color-border)' }}
              >
                <td style={{ padding: '0.45rem 0.5rem' }}>
                  {/* #3370 — instance rows drill into THAT INSTANCE's
                      own application page `/app/<instance>`. The detail
                      handler resolves the name against Application CRs
                      AND HelmRelease releaseNames, so shared-pg /
                      shared-pg-b each land on their own page (the old
                      `bp-<blueprint>` link collapsed every instance
                      onto one class-named page). */}
                  <Link
                    to={'/app/$componentId' as never}
                    params={{ componentId: inst.name } as never}
                    data-testid={`sov-instance-link-${inst.name}`}
                    style={{ color: 'var(--color-accent)', fontWeight: 600 }}
                  >
                    <code>{inst.name}</code>
                  </Link>
                </td>
                <td style={{ padding: '0.45rem 0.5rem' }}>{inst.org}</td>
                <td style={{ padding: '0.45rem 0.5rem' }}>
                  {/* #3856 — canonicalize the topology token for DISPLAY so a
                      seeded/legacy CR carrying the banned un-hyphenated
                      `active-hotstandby` (or `single-region`) still renders the
                      ONE canonical chip (#3375 DoD-1: singleton | active-active |
                      active-hot-standby | active-passive). Belt-and-suspenders:
                      flips the Instances-table chip green even before the seed is
                      re-applied on a fresh prov. */}
                  <span className="chip chip-bp">{canonicalizeMode(inst.topology)}</span>
                </td>
                <td style={{ padding: '0.45rem 0.5rem' }}>
                  <span className={`chip ${statusChipClass(inst.status)}`}>
                    {inst.status}
                  </span>
                </td>
                <td
                  style={{ padding: '0.45rem 0.5rem', color: 'var(--color-text-dim)' }}
                >
                  {versionOf(inst) || '—'}
                </td>
                <td style={{ padding: '0.45rem 0.5rem', textAlign: 'right' }}>
                  <Link
                    to={'/app/$componentId' as never}
                    params={{ componentId: inst.name } as never}
                    data-testid={`sov-instance-open-${inst.name}`}
                    className="external-url-link"
                  >
                    Open →
                  </Link>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {instancesQuery.isError ? (
        <p
          className="section-hint"
          data-testid="sov-instances-error"
          style={{ margin: '0.4rem 0 0', color: 'var(--color-danger)' }}
        >
          Failed to load instances:{' '}
          {(instancesQuery.error as Error)?.message ?? 'unknown error'}
        </p>
      ) : null}

      {/* Per-cluster fan-out for the CURRENT Application (G117.6) —
          optional; CatalogDetail does not pass `perCluster`. */}
      {perCluster && perCluster.length > 0 ? (
        <div style={{ marginTop: '0.8rem' }} data-testid="sov-percluster-block">
          <h3
            style={{
              fontSize: '0.85rem',
              margin: '0 0 0.3rem',
              color: 'var(--color-text-strong)',
            }}
          >
            Per-cluster fan-out
            {appUID ? (
              <span
                style={{
                  marginLeft: '0.5rem',
                  fontSize: '0.7rem',
                  color: 'var(--color-text-dim)',
                  fontWeight: 400,
                }}
              >
                (uid {appUID.slice(0, 8)})
              </span>
            ) : null}
          </h3>
          <PerClusterTable perCluster={perCluster} />
        </div>
      ) : null}

      {dialogOpen ? (
        <NewInstanceDialog
          blueprint={blueprint}
          onClose={() => setDialogOpen(false)}
          onCreated={() => {
            setDialogOpen(false)
            qc.invalidateQueries({ queryKey: ['blueprint-instances', blueprint] })
          }}
        />
      ) : null}
    </section>
  )
}

/**
 * statusChipClass — map an instance status string to the shared chip
 * colour classes (same palette as AppDetail's phase chip) so the
 * instances table reads at-a-glance. Unknown/empty statuses fall back to
 * the neutral category chip.
 */
function statusChipClass(status: string | undefined): string {
  const s = (status ?? '').toLowerCase()
  if (s === 'ready' || s === 'installed' || s === 'active') return 'chip-installed'
  if (s === 'failed' || s === 'degraded' || s === 'error') return 'chip-failed'
  if (
    s === 'provisioning' ||
    s === 'installing' ||
    s === 'pending' ||
    s === 'reconciling'
  )
    return 'chip-pending'
  return 'chip-cat'
}

/**
 * versionOf — surface the instance's blueprint/chart version when the
 * wire envelope carries one. `ApplicationInstanceSummary` doesn't declare
 * a version field today, so read it tolerantly from the loosely-typed
 * row; renders "—" when absent (the table cell handles the fallback).
 */
function versionOf(inst: ApplicationInstanceSummary): string {
  const row = inst as ApplicationInstanceSummary & {
    version?: string
    blueprintVersion?: string
    chartVersion?: string
  }
  return row.version ?? row.blueprintVersion ?? row.chartVersion ?? ''
}

/**
 * PerClusterTable — renders an Application's `status.perCluster[]`
 * fan-out (cluster + role + status + hr) as a compact table. Shared so
 * BOTH the class-page InstancesSection (optional) and the per-instance
 * AppDetail page can surface the same view for a single Application
 * without duplicating the row markup (#3090).
 */
export function PerClusterTable({
  perCluster,
}: {
  perCluster: Array<Record<string, unknown>>
}) {
  return (
    <table
      data-testid="sov-percluster-table"
      style={{
        width: '100%',
        borderCollapse: 'collapse',
        fontSize: '0.78rem',
      }}
    >
      <thead>
        <tr style={{ textAlign: 'left', color: 'var(--color-text-dim)' }}>
          <th style={{ padding: '0.25rem 0.4rem' }}>Cluster</th>
          <th style={{ padding: '0.25rem 0.4rem' }}>Role</th>
          <th style={{ padding: '0.25rem 0.4rem' }}>Status</th>
          <th style={{ padding: '0.25rem 0.4rem' }}>HR</th>
        </tr>
      </thead>
      <tbody>
        {perCluster.map((row, idx) => {
          const cluster = String(row.cluster ?? row.region ?? '—')
          const role = String(row.role ?? '—')
          const status = String(row.status ?? '—')
          const hr = String(row.hr ?? row.helmRelease ?? '')
          return (
            <tr
              key={`${cluster}-${idx}`}
              data-testid={`sov-percluster-row-${cluster}`}
            >
              <td style={{ padding: '0.25rem 0.4rem' }}>
                <code>{cluster}</code>
              </td>
              <td style={{ padding: '0.25rem 0.4rem' }}>{role}</td>
              <td style={{ padding: '0.25rem 0.4rem' }}>{status}</td>
              <td
                style={{
                  padding: '0.25rem 0.4rem',
                  color: 'var(--color-text-dim)',
                }}
              >
                {hr || '—'}
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

/**
 * NewInstanceDialog — the create-from-catalog wizard (#2741, extended by
 * EPIC #3597 / #3599 / #3600). Opens on "+ New instance" click.
 *
 * Every fixed-set input is a dropdown / multiselect from LIVE data
 * (#3600) — free-text remains ONLY for the instance name:
 *
 *  - name        — free-text (lowercase k8s-name; the only free field).
 *  - org         — <select> from listOrganizations() (live Orgs).
 *  - topology    — <select> from the canonical editor mode set
 *                  (single-region / active-active / active-hotstandby),
 *                  constrained to the Blueprint's supported modes when
 *                  declared. REUSES the post-create Topology editor's
 *                  ALL_MODES option set (#3599 — no divergent vocab).
 *  - region(s)   — checkbox multiselect from the Sovereign's live cluster
 *                  regions (getHierarchicalInfrastructure topology tree).
 *  - vCluster    — <select> of REAL placement targets only (#5616):
 *                  "host" plus the live vClusters whose actual host
 *                  namespace the backend tier map resolves (mgmt/dmz/
 *                  rtz). Option value = the vCluster's real namespace,
 *                  never its display name.
 *  - backing     — the #3370 Create new / Reuse existing selectors.
 *
 * Placement (#3599) is written onto the Application CR: the submit sends
 * `topology` = the chosen mode + `placement = {vcluster, regions}`, which
 * the backend stamps as `spec.placement = {mode, vcluster, regions}` +
 * `spec.regions`. The post-create Topology tab reads those fields back, so
 * it reflects the placement chosen here.
 *
 * Validation (#3599): active-active / active-hotstandby require ≥2
 * regions; the Create button stays disabled until that holds.
 *
 * On submit POSTs to /catalyst/v1/apps/instances via
 * createApplicationInstance. On 4xx the error renders inline; on 201 the
 * dialog closes and the parent invalidates the instances cache.
 */
export function NewInstanceDialog({
  blueprint,
  onClose,
  onCreated,
}: {
  blueprint: string
  onClose: () => void
  onCreated: () => void
}) {
  const [name, setName] = useState('')
  const [org, setOrg] = useState('')
  const [topology, setTopology] = useState<string>('')
  const [regions, setRegions] = useState<string[]>([])
  const [vcluster, setVcluster] = useState<string>('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // #3370 — per-backing-service selection: "" = Create new (default),
  // any other value = the existing instance name to reuse.
  const [backingChoice, setBackingChoice] = useState<Record<string, string>>({})

  const { deploymentId } = useResolvedDeploymentId()

  // #3600 — live Organizations for the Org dropdown.
  const orgsQuery = useQuery<OrgRow[]>({
    queryKey: ['organizations-directory', deploymentId ?? ''],
    queryFn: listOrganizations,
    staleTime: 60_000,
    retry: 1,
  })
  const orgs = orgsQuery.data ?? []

  // #3599 / #3600 — live cluster regions + vclusters from the Sovereign's
  // infrastructure topology (the SAME source the post-create Topology tab
  // reads its available regions from). Regions use the catalyst id
  // (RegionSpec.name), which is what Application.spec.regions stores.
  const infraQuery = useQuery({
    queryKey: ['infrastructure-topology', deploymentId ?? ''],
    queryFn: () => getHierarchicalInfrastructure(deploymentId ?? ''),
    enabled: !!deploymentId,
    staleTime: 60_000,
    retry: 1,
  })
  const availableRegions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const r of infraQuery.data?.topology?.regions ?? []) {
      if (r.name) set.add(r.name)
    }
    return Array.from(set).sort()
  }, [infraQuery.data])
  // #5616 — REAL placement targets from the live topology. Each option's
  // VALUE is the vCluster's real host NAMESPACE (`vc.namespace` on the
  // wire) — the identity the backend consumes when addressing the
  // per-cluster HelmRelease — never its display name (`vc.name`: the
  // per-Org vCluster is NAMED "vcluster" but RUNS in the Org namespace,
  // so emitting the name produced `namespaces "vcluster" not found`).
  // Only namespaces the backend tier map can resolve (mgmt/dmz/rtz) are
  // offered; a live vCluster in any other namespace (e.g. the per-Org
  // boundary) has no valid `placement.vcluster` spelling today — the
  // blank default already lands those installs in the Org boundary
  // server-side, which is the working path the hw292 control proved.
  const liveVclusterTargets = useMemo<Array<{ namespace: string; name: string }>>(() => {
    const byNs = new Map<string, string>()
    for (const region of infraQuery.data?.topology?.regions ?? []) {
      for (const cluster of region.clusters ?? []) {
        for (const vc of cluster.vclusters ?? []) {
          const ns = vc.namespace ?? ''
          if (!ns) continue
          if (!(RESOLVABLE_TIER_NAMESPACES as readonly string[]).includes(ns)) continue
          if (!byNs.has(ns)) byNs.set(ns, vc.name || ns)
        }
      }
    }
    return Array.from(byNs.entries())
      .map(([namespace, name]) => ({ namespace, name }))
      .sort((a, b) => a.namespace.localeCompare(b.namespace))
  }, [infraQuery.data])

  // Resolve the Blueprint's supported topologies via the catalog
  // version endpoint (latest pin → full raw spec).
  const bpName = blueprint.replace(/^bp-/, '')
  const bpQuery = useQuery({
    queryKey: ['catalog-item-latest', bpName],
    queryFn: async () => {
      const { getCatalogItem } = await import('@/lib/catalog.api')
      const latest = await getCatalogItem(bpName)
      const full = await getCatalogItemVersion(bpName, latest.version)
      return full
    },
    staleTime: 60_000,
    retry: 1,
  })

  // The blueprint's declared supported topologies, normalised to the
  // editor mode vocab so they intersect with ALL_MODES. When the
  // blueprint declares none, every editor mode is allowed.
  //
  // #3922 — `supportedModes` is the ALLOWED (selectable) subset; the
  // <select> below renders the FULL canonical ALL_MODES set and merely
  // DISABLES the unsupported options. This mirrors the app-detail
  // "Change placement" radio picker (TopologyEditor), which always lists
  // all four canonical classes and greys out the ones the Blueprint
  // doesn't support — so the create dialog can no longer silently DROP
  // active-passive / active-active and disagree with the app-detail view.
  const supportedModes = useMemo<string[]>(() => {
    const raw = bpQuery.data?.raw as
      | { spec?: { topology?: { supported?: string[] } } }
      | undefined
    const declared = (raw?.spec?.topology?.supported ?? [])
      .filter((s): s is string => typeof s === 'string')
      .map(canonicalEditorMode)
    const allowed = declared.length > 0 ? new Set(declared) : null
    return ALL_MODES.filter((m) => (allowed ? allowed.has(m) : true))
  }, [bpQuery.data])

  // The selectable-mode gate the <select> consults to disable (not drop)
  // unsupported options.
  const allowedModeSet = useMemo<Set<string>>(() => new Set(supportedModes), [supportedModes])

  const defaultMode = useMemo<string>(() => {
    const raw = bpQuery.data?.raw as
      | { spec?: { topology?: { defaults?: Record<string, string> } } }
      | undefined
    const defs = raw?.spec?.topology?.defaults
    const declaredDefault = defs
      ? canonicalEditorMode(defs['multi-region'] ?? defs['single-region'] ?? '')
      : ''
    if (declaredDefault && supportedModes.includes(declaredDefault)) return declaredDefault
    // One vocabulary (#3375 DoD-1): prefer the canonical singleton, else
    // the first supported canonical mode.
    return supportedModes.includes('singleton') ? 'singleton' : supportedModes[0] ?? 'singleton'
  }, [bpQuery.data, supportedModes])

  useEffect(() => {
    if (!topology && defaultMode) setTopology(defaultMode)
  }, [defaultMode, topology])

  const requiresMultiRegion = MULTI_REGION_MODES.has(topology)

  // #3905 — the per-component topology matrix (same source the declared-
  // topology card reads) so the instance-create mode picker derives each
  // mode's helper text from THIS component's DR contract, never the generic
  // "backup-restore" string that contradicted the card.
  const componentTopology = TOPOLOGY_BY_ID[`bp-${bpName}`] ?? TOPOLOGY_BY_ID[bpName]

  // #3370 — required backing services (declared depends[] that are
  // themselves shareable). One generic selector each.
  const backingBlueprints = useMemo<string[]>(() => {
    const self = BLUEPRINT_BY_ID[`bp-${bpName}`]
    const depends = self?.depends ?? []
    return depends.filter((d) => BLUEPRINT_BY_ID[d]?.shareable === true)
  }, [bpName])

  const toggleRegion = (region: string) =>
    setRegions((prev) =>
      prev.includes(region) ? prev.filter((r) => r !== region) : [...prev, region],
    )

  // #3599 validation — multi-region modes need ≥2 regions.
  const placementValid = !requiresMultiRegion || regions.length >= 2
  const canSubmit = !submitting && !!name && !!org && placementValid

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!canSubmit) return
    setError(null)
    setSubmitting(true)
    try {
      const body: CreateApplicationInstanceRequest = {
        blueprint: bpName,
        org: org.trim(),
        name: name.trim(),
      }
      if (topology) body.topology = topology
      // #3599 — write the placement onto the Application CR. Only send
      // fields the operator set, so a silent single-region install with
      // no region/vcluster choice still falls back to the Blueprint
      // defaults server-side.
      if (regions.length > 0 || vcluster) {
        body.placement = {}
        if (vcluster) body.placement.vcluster = vcluster
        if (regions.length > 0) body.placement.regions = regions
      }
      if (backingBlueprints.length > 0) {
        body.backing = backingBlueprints.map((bp): BackingSelection => {
          const chosen = backingChoice[bp] ?? ''
          return chosen
            ? { blueprint: bp, mode: 'reuse', instance: chosen }
            : { blueprint: bp, mode: 'create' }
        })
      }
      await createApplicationInstance(body)
      onCreated()
    } catch (err) {
      const e = err as Error & { code?: string; status?: number }
      const code = e.code ? ` (${e.code})` : ''
      setError(`${e.message}${code}`)
    } finally {
      setSubmitting(false)
    }
  }

  const fieldStyle: React.CSSProperties = {
    width: '100%',
    padding: '0.4rem 0.6rem',
    background: 'var(--color-bg)',
    color: 'var(--color-text)',
    border: '1px solid var(--color-border)',
    borderRadius: '4px',
    fontSize: '0.85rem',
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="new-instance-dialog-title"
      data-testid="dialog-new-instance"
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.5)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        zIndex: 1000,
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <form
        onSubmit={onSubmit}
        style={{
          background: 'var(--color-surface)',
          color: 'var(--color-text)',
          border: '1px solid var(--color-border)',
          borderRadius: '8px',
          padding: '1.2rem',
          minWidth: '440px',
          maxWidth: '560px',
          maxHeight: '88vh',
          overflowY: 'auto',
          boxShadow: '0 10px 40px rgba(0,0,0,0.3)',
        }}
      >
        <h3
          id="new-instance-dialog-title"
          style={{ margin: '0 0 0.8rem', fontSize: '1.05rem' }}
        >
          New instance of {bpName}
        </h3>

        {/* Instance name — the ONLY free-text field (#3600). */}
        <label style={{ display: 'block', marginBottom: '0.6rem', fontSize: '0.85rem' }}>
          <span style={{ display: 'block', marginBottom: '0.2rem' }}>Instance name</span>
          <input
            type="text"
            data-testid="input-instance-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            pattern="^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$"
            title="Lowercase letters, digits, and dashes; 2-42 chars; no leading/trailing dash"
            placeholder="my-instance"
            style={fieldStyle}
          />
        </label>

        {/* Organization — dropdown from live Orgs (#3600). */}
        <label style={{ display: 'block', marginBottom: '0.6rem', fontSize: '0.85rem' }}>
          <span style={{ display: 'block', marginBottom: '0.2rem' }}>Organization</span>
          <select
            data-testid="select-instance-org"
            value={org}
            onChange={(e) => setOrg(e.target.value)}
            required
            style={fieldStyle}
          >
            <option value="" disabled>
              {orgsQuery.isPending ? 'Loading organizations…' : 'Select an organization…'}
            </option>
            {orgs.map((o) => (
              <option key={o.id} value={o.slug}>
                {o.displayName ? `${o.displayName} (${o.slug})` : o.slug}
              </option>
            ))}
          </select>
          {/* Environment is derived server-side as <org>-prod; surfaced
              read-only so the operator sees WHERE it lands (#3599). No
              free-text — there is no environments list to choose from. */}
          {org ? (
            <span
              data-testid="instance-environment-derived"
              style={{
                display: 'block',
                marginTop: '0.3rem',
                fontSize: '0.74rem',
                color: 'var(--color-text-dim)',
              }}
            >
              Environment: <code>{org}-prod</code>
            </span>
          ) : null}
        </label>

        {/* Topology mode — dropdown over the editor's full canonical
            ALL_MODES set (#3856 single vocabulary: singleton / active-passive /
            active-hot-standby / active-active). #3922 — render ALL FOUR and
            DISABLE the ones the Blueprint doesn't support, exactly like the
            app-detail "Change placement" radio picker; the create <select>
            previously DROPPED unsupported modes, so active-passive /
            active-active vanished from it while the app-detail radio still
            listed them. Disabled options carry a "(not supported)" suffix so
            the operator sees the full vocabulary + why a mode is unavailable. */}
        <label style={{ display: 'block', marginBottom: '0.6rem', fontSize: '0.85rem' }}>
          <span style={{ display: 'block', marginBottom: '0.2rem' }}>Topology mode</span>
          <select
            data-testid="select-instance-topology"
            value={topology}
            onChange={(e) => setTopology(e.target.value)}
            disabled={bpQuery.isPending}
            style={fieldStyle}
          >
            {ALL_MODES.map((m) => {
              const enabled = allowedModeSet.has(m)
              return (
                <option key={m} value={m} disabled={!enabled}>
                  {m} — {describeModeForComponent(m, componentTopology)}
                  {enabled ? '' : ' (not supported by this blueprint)'}
                </option>
              )
            })}
          </select>
        </label>

        {/* Region(s) — multiselect from live cluster regions (#3599/#3600). */}
        <fieldset
          data-testid="instance-regions"
          style={{
            border: '1px solid var(--color-border)',
            borderRadius: '4px',
            padding: '0.5rem 0.7rem',
            marginBottom: '0.6rem',
          }}
        >
          <legend style={{ fontSize: '0.8rem', padding: '0 0.3rem' }}>
            Region(s){requiresMultiRegion ? ' — pick at least 2' : ''}
          </legend>
          {infraQuery.isPending ? (
            <p style={{ fontSize: '0.78rem', margin: 0, color: 'var(--color-text-dim)' }}>
              Loading regions…
            </p>
          ) : availableRegions.length === 0 ? (
            <p
              data-testid="instance-regions-empty"
              style={{ fontSize: '0.78rem', margin: 0, color: 'var(--color-text-dim)' }}
            >
              No cluster regions reported for this Sovereign — the server will
              place the instance in its default region.
            </p>
          ) : (
            availableRegions.map((region) => (
              <label
                key={region}
                style={{ display: 'block', fontSize: '0.82rem', padding: '0.12rem 0', cursor: 'pointer' }}
              >
                <input
                  type="checkbox"
                  checked={regions.includes(region)}
                  onChange={() => toggleRegion(region)}
                  data-testid={`region-checkbox-${region}`}
                  style={{ marginRight: '0.4rem' }}
                />
                <code>{region}</code>
              </label>
            ))
          )}
          {requiresMultiRegion && regions.length < 2 ? (
            <p
              data-testid="instance-regions-validation"
              style={{ margin: '0.3rem 0 0', fontSize: '0.74rem', color: 'var(--color-danger)' }}
            >
              {topology} needs at least 2 regions.
            </p>
          ) : null}
        </fieldset>

        {/* vCluster / zone — REAL placement targets only (#5616): the
            namespace-independent "host" plus the live vClusters whose
            actual namespace the backend tier map resolves. Option VALUE
            = the vCluster's real host namespace (what the per-cluster
            HelmRelease is addressed with), never its display name. */}
        <label style={{ display: 'block', marginBottom: '0.6rem', fontSize: '0.85rem' }}>
          <span style={{ display: 'block', marginBottom: '0.2rem' }}>vCluster / zone</span>
          <select
            data-testid="select-instance-vcluster"
            value={vcluster}
            onChange={(e) => setVcluster(e.target.value)}
            style={fieldStyle}
          >
            <option value="">Default (server picks)</option>
            <option value="host">host — host cluster (no vCluster)</option>
            {liveVclusterTargets.map((t) => (
              <option key={t.namespace} value={t.namespace}>
                {t.namespace}
                {t.name && t.name !== t.namespace ? ` — vCluster ${t.name}` : ''}
              </option>
            ))}
          </select>
        </label>

        {/*
          #3370 — backing services. EVERY required backing service of
          this blueprint (declared depends[] whose target is shareable)
          renders ONE generic selector: Create new (default) / Reuse
          existing.
        */}
        {backingBlueprints.length > 0 ? (
          <fieldset
            data-testid="backing-services"
            style={{
              border: '1px solid var(--color-border)',
              borderRadius: '4px',
              padding: '0.5rem 0.7rem',
              marginBottom: '0.6rem',
            }}
          >
            <legend style={{ fontSize: '0.8rem', padding: '0 0.3rem' }}>
              Backing services
            </legend>
            {backingBlueprints.map((bp) => (
              <BackingServiceSelector
                key={bp}
                blueprint={bp}
                value={backingChoice[bp] ?? ''}
                onChange={(v) => setBackingChoice((prev) => ({ ...prev, [bp]: v }))}
              />
            ))}
          </fieldset>
        ) : null}

        {error ? (
          <p
            data-testid="dialog-error"
            style={{ color: 'var(--color-danger)', fontSize: '0.8rem', margin: '0 0 0.6rem' }}
          >
            {error}
          </p>
        ) : null}

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
          <button
            type="button"
            data-testid="btn-cancel-instance"
            onClick={onClose}
            disabled={submitting}
            style={{
              padding: '0.35rem 0.8rem',
              background: 'transparent',
              color: 'var(--color-text)',
              border: '1px solid var(--color-border)',
              borderRadius: '4px',
              fontSize: '0.82rem',
              cursor: 'pointer',
            }}
          >
            Cancel
          </button>
          <button
            type="submit"
            data-testid="btn-submit-instance"
            disabled={!canSubmit}
            style={{
              padding: '0.35rem 0.8rem',
              background: 'var(--color-accent)',
              color: 'white',
              border: '0',
              borderRadius: '4px',
              fontSize: '0.82rem',
              cursor: canSubmit ? 'pointer' : 'not-allowed',
              fontWeight: 600,
            }}
          >
            {submitting ? 'Creating…' : 'Create instance'}
          </button>
        </div>
      </form>
    </div>
  )
}

/**
 * canonicalEditorMode — fold a Blueprint topology token onto the ONE
 * canonical vocabulary (#3375 DoD-1: singleton | active-active |
 * active-hot-standby | active-passive) so the create-flow dropdown
 * intersects cleanly with ALL_MODES (now canonical). Legacy spellings
 * (single-region / active-hotstandby) and the `multi-region` deployment
 * posture (which is a Sovereign shape, not an instance mode) fold to a
 * valid canonical option; anything unknown falls back to singleton so
 * the dropdown always offers a valid choice.
 */
function canonicalEditorMode(raw: string): string {
  const s = (raw ?? '').trim().toLowerCase()
  switch (s) {
    case 'active-active':
    case 'active_active':
      return 'active-active'
    case 'active-hot-standby':
    case 'active-hotstandby':
    case 'active_hot_standby':
      return 'active-hot-standby'
    case 'active-passive':
    case 'active_passive':
      return 'active-passive'
    case 'singleton':
    case 'single-region':
    case 'single_region':
      return 'singleton'
    case 'multi-region':
    default:
      // multi-region is a deployment posture, not an instance mode; map
      // it (and anything unknown) to singleton so the dropdown still
      // offers a valid canonical option.
      return 'singleton'
  }
}

/**
 * BackingServiceSelector — #3370. ONE generic selector per required
 * backing service: Create new (default) / Reuse existing. The reuse
 * dropdown lists the existing instances of that blueprint, each with
 * its topology + Context count. Identical for every shareable
 * blueprint — the declaration drives everything.
 */
function BackingServiceSelector({
  blueprint,
  value,
  onChange,
}: {
  /** Full backing Blueprint id (bp-<name>). */
  blueprint: string
  /** "" = Create new; otherwise the existing instance name to reuse. */
  value: string
  onChange: (v: string) => void
}) {
  const slug = blueprint.replace(/^bp-/, '')
  const title = BLUEPRINT_BY_ID[blueprint]?.title ?? slug
  const kind = BLUEPRINT_BY_ID[blueprint]?.contextSchema?.kind ?? ''
  const instancesQuery = useQuery({
    queryKey: ['blueprint-instances', blueprint],
    queryFn: () => getApplicationInstances(blueprint),
    staleTime: 15_000,
    retry: 1,
  })
  const items: ApplicationInstanceSummary[] = instancesQuery.data?.items ?? []

  return (
    <div
      data-testid={`backing-selector-${slug}`}
      style={{ padding: '0.25rem 0', fontSize: '0.82rem' }}
    >
      <span style={{ display: 'block', marginBottom: '0.2rem', fontWeight: 600 }}>
        {title}
        {kind ? (
          <span style={{ marginLeft: '0.4rem', color: 'var(--color-text-dim)', fontWeight: 400 }}>
            (Context: {kind})
          </span>
        ) : null}
      </span>
      <label style={{ display: 'block', padding: '0.1rem 0', cursor: 'pointer' }}>
        <input
          type="radio"
          name={`backing-${slug}`}
          checked={value === ''}
          onChange={() => onChange('')}
          data-testid={`backing-create-${slug}`}
          style={{ marginRight: '0.4rem' }}
        />
        Create new (default) — provisions its own {title} instance with a Context for this app
      </label>
      <label style={{ display: 'block', padding: '0.1rem 0', cursor: 'pointer' }}>
        <input
          type="radio"
          name={`backing-${slug}`}
          checked={value !== ''}
          disabled={items.length === 0}
          onChange={() => {
            if (items.length > 0) onChange(items[0].name)
          }}
          data-testid={`backing-reuse-${slug}`}
          style={{ marginRight: '0.4rem' }}
        />
        Reuse existing
        {items.length === 0 ? (
          <span style={{ marginLeft: '0.35rem', color: 'var(--color-text-dim)' }}>
            (no instances yet)
          </span>
        ) : null}
      </label>
      {value !== '' && items.length > 0 ? (
        <select
          value={value}
          onChange={(e) => onChange(e.target.value)}
          data-testid={`backing-instance-${slug}`}
          style={{
            width: '100%',
            marginTop: '0.25rem',
            padding: '0.3rem 0.5rem',
            background: 'var(--color-bg)',
            color: 'var(--color-text)',
            border: '1px solid var(--color-border)',
            borderRadius: '4px',
            fontSize: '0.82rem',
          }}
        >
          {items.map((inst) => (
            <option key={inst.id || inst.name} value={inst.name}>
              {inst.name} · {canonicalizeMode(inst.topology)} · {(inst.contexts ?? []).length}{' '}
              context{(inst.contexts ?? []).length === 1 ? '' : 's'}
            </option>
          ))}
        </select>
      ) : null}
    </div>
  )
}
