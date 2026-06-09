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
  type CreateApplicationInstanceRequest,
} from '@/lib/catalog.api'

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
}: {
  blueprint: string
  appUID?: string
  perCluster?: Array<Record<string, unknown>>
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
                  {/* #3090 — instance rows drill into the INSTANCE page
                      `/app/$componentId`. The Application instance is
                      addressed by its Blueprint-scoped component id
                      (`bp-<blueprint>`), matching consoleAppDetailRoute. */}
                  <Link
                    to={'/app/$componentId' as never}
                    params={{ componentId: `bp-${inst.blueprint}` } as never}
                    data-testid={`sov-instance-link-${inst.name}`}
                    style={{ color: 'var(--color-accent)', fontWeight: 600 }}
                  >
                    <code>{inst.name}</code>
                  </Link>
                </td>
                <td style={{ padding: '0.45rem 0.5rem' }}>{inst.org}</td>
                <td style={{ padding: '0.45rem 0.5rem' }}>
                  <span className="chip chip-bp">{inst.topology}</span>
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
                    params={{ componentId: `bp-${inst.blueprint}` } as never}
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
 * NewInstanceDialog — G117.2 #2741 topology-picker dialog. Opens on
 * "+ New instance" click. Fields:
 *
 *  - name (string, lowercase k8s-name)
 *  - org (string, free-text; defaults to "" — operator types the Org
 *    slug they're installing into. TBD-followup: replace with a real
 *    Org picker once the OrgList endpoint is wired into the FE).
 *  - topology (radio: singleton / active-hot-standby / active-active /
 *    active-passive, gated by Blueprint.spec.topology.supported[] from
 *    getCatalogItemVersion). Defaults to the Blueprint's preferred
 *    default if exposed.
 *
 * On submit POSTs to /catalyst/v1/apps/instances via
 * createApplicationInstance. On 4xx error message renders inline; on
 * 201 the dialog closes and the parent invalidates the instances
 * cache.
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
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Resolve the Blueprint's supported topologies via the catalog
  // version endpoint. We don't have a fixed version pin from the
  // AppDetail context, so call `getCatalogItem` first to read the
  // latest version and then re-fetch with that version for the
  // configSchema. For the dialog we only need `raw.spec.topology`, so
  // a single getCatalogItemVersion call against the latest version is
  // sufficient.
  const bpName = blueprint.replace(/^bp-/, '')
  const bpQuery = useQuery({
    queryKey: ['catalog-item-latest', bpName],
    queryFn: async () => {
      // First fetch — get the latest version pin.
      const { getCatalogItem } = await import('@/lib/catalog.api')
      const latest = await getCatalogItem(bpName)
      // Second fetch — get the full raw spec (topology, configSchema).
      const full = await getCatalogItemVersion(bpName, latest.version)
      return full
    },
    staleTime: 60_000,
    retry: 1,
  })

  const supportedTopologies = useMemo<string[]>(() => {
    const raw = bpQuery.data?.raw as
      | { spec?: { topology?: { supported?: string[]; defaults?: Record<string, string> } } }
      | undefined
    const list = raw?.spec?.topology?.supported ?? []
    if (!Array.isArray(list)) return []
    return list.filter((s) => typeof s === 'string')
  }, [bpQuery.data])

  const defaultTopology = useMemo<string>(() => {
    const raw = bpQuery.data?.raw as
      | { spec?: { topology?: { defaults?: Record<string, string> } } }
      | undefined
    const defs = raw?.spec?.topology?.defaults
    // Prefer multi-region default if set; else single-region; else
    // first supported entry.
    return defs?.['multi-region'] ?? defs?.['single-region'] ?? supportedTopologies[0] ?? ''
  }, [bpQuery.data, supportedTopologies])

  useEffect(() => {
    if (!topology && defaultTopology) {
      setTopology(defaultTopology)
    }
  }, [defaultTopology, topology])

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (submitting) return
    setError(null)
    setSubmitting(true)
    try {
      const body: CreateApplicationInstanceRequest = {
        blueprint: bpName,
        org: org.trim(),
        name: name.trim(),
      }
      if (topology) body.topology = topology
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
          minWidth: '420px',
          maxWidth: '520px',
          boxShadow: '0 10px 40px rgba(0,0,0,0.3)',
        }}
      >
        <h3
          id="new-instance-dialog-title"
          style={{ margin: '0 0 0.8rem', fontSize: '1.05rem' }}
        >
          New instance of {bpName}
        </h3>

        <label
          style={{ display: 'block', marginBottom: '0.6rem', fontSize: '0.85rem' }}
        >
          <span style={{ display: 'block', marginBottom: '0.2rem' }}>
            Instance name
          </span>
          <input
            type="text"
            data-testid="input-instance-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            pattern="^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$"
            title="Lowercase letters, digits, and dashes; 2-42 chars; no leading/trailing dash"
            placeholder="my-instance"
            style={{
              width: '100%',
              padding: '0.4rem 0.6rem',
              background: 'var(--color-bg)',
              color: 'var(--color-text)',
              border: '1px solid var(--color-border)',
              borderRadius: '4px',
              fontSize: '0.85rem',
            }}
          />
        </label>

        <label
          style={{ display: 'block', marginBottom: '0.6rem', fontSize: '0.85rem' }}
        >
          <span style={{ display: 'block', marginBottom: '0.2rem' }}>
            Organization
          </span>
          <input
            type="text"
            data-testid="input-instance-org"
            value={org}
            onChange={(e) => setOrg(e.target.value)}
            required
            placeholder="acme"
            style={{
              width: '100%',
              padding: '0.4rem 0.6rem',
              background: 'var(--color-bg)',
              color: 'var(--color-text)',
              border: '1px solid var(--color-border)',
              borderRadius: '4px',
              fontSize: '0.85rem',
            }}
          />
        </label>

        <fieldset
          style={{
            border: '1px solid var(--color-border)',
            borderRadius: '4px',
            padding: '0.5rem 0.7rem',
            marginBottom: '0.6rem',
          }}
        >
          <legend style={{ fontSize: '0.8rem', padding: '0 0.3rem' }}>
            Topology
          </legend>
          {bpQuery.isPending ? (
            <p style={{ fontSize: '0.78rem', margin: 0, color: 'var(--color-text-dim)' }}>
              Loading…
            </p>
          ) : supportedTopologies.length === 0 ? (
            <p
              style={{ fontSize: '0.78rem', margin: 0, color: 'var(--color-text-dim)' }}
              data-testid="topology-radio-empty"
            >
              Blueprint does not declare supported topologies — server will
              pick the default.
            </p>
          ) : (
            supportedTopologies.map((t) => (
              <label
                key={t}
                style={{
                  display: 'block',
                  fontSize: '0.82rem',
                  padding: '0.15rem 0',
                  cursor: 'pointer',
                }}
              >
                <input
                  type="radio"
                  name="topology"
                  value={t}
                  checked={topology === t}
                  onChange={() => setTopology(t)}
                  data-testid={`topology-radio-${t}`}
                  style={{ marginRight: '0.4rem' }}
                />
                {t}
              </label>
            ))
          )}
        </fieldset>

        {error ? (
          <p
            data-testid="dialog-error"
            style={{
              color: 'var(--color-danger)',
              fontSize: '0.8rem',
              margin: '0 0 0.6rem',
            }}
          >
            {error}
          </p>
        ) : null}

        <div
          style={{
            display: 'flex',
            justifyContent: 'flex-end',
            gap: '0.5rem',
          }}
        >
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
            disabled={submitting || !name || !org}
            style={{
              padding: '0.35rem 0.8rem',
              background: 'var(--color-accent)',
              color: 'white',
              border: '0',
              borderRadius: '4px',
              fontSize: '0.82rem',
              cursor: submitting ? 'not-allowed' : 'pointer',
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
