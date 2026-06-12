/**
 * DataInstances.tsx — the ADR-0010 "Data instances" panel, ported from the
 * unrouted Svelte console (`core/console/src/components/DataInstances.svelte`)
 * into the DEPLOYED React operator console (catalyst-ui), where the founder
 * actually looks (#3188 row-3/4 walk).
 *
 * Renders the three ADR-0010 surfaces:
 *   1. ONE PostgreSQL ENGINE-CLASS card (the `bp-cnpg` engine, shown once),
 *      with the live data-instance count.
 *   2. The N DATA-INSTANCE cards — one per live CNPG Cluster (#3188
 *      many-to-many) — each with a Consumers (bindings) table:
 *      app · database · role · mode · secret.
 *   3. The honest empty states — "No PostgreSQL data instances yet" when
 *      nothing runs, and the per-instance "bindings not surfaced" line for
 *      rows the API returns without a `bindings[]` field.
 *
 * Fed by the LIVE, REUSED endpoint
 *   GET /catalyst/v1/catalog/bp-postgres/instances
 * via `getApplicationInstances` (the same call InstancesSection uses). The
 * per-consumer binding rows are now surfaced SERVER-SIDE on that endpoint
 * (#3188: CNPG Cluster CRs → instance rows, Database CRs → shared bindings,
 * bare per-app Clusters → one implicit dedicated binding); rows without
 * `bindings` still render the honest empty state — never a fabricated row.
 */

import { useQuery } from '@tanstack/react-query'

import { getApplicationInstances } from '@/lib/catalog.api'
import {
  type DataInstance,
  fromInstanceSummaries,
  postgresClass,
  isShared,
} from '@/lib/dataInstances'

/**
 * DataInstances — the panel. `blueprint` defaults to `bp-postgres` (the
 * data-instance Blueprint); callers on the bp-postgres / bp-cnpg catalog
 * detail page render it without props.
 */
export function DataInstances({
  blueprint = 'bp-postgres',
}: {
  blueprint?: string
}) {
  const instancesQuery = useQuery({
    queryKey: ['data-instances', blueprint],
    queryFn: () => getApplicationInstances(blueprint),
    staleTime: 15_000,
    refetchInterval: 15_000,
    retry: 1,
  })

  const instances: DataInstance[] = fromInstanceSummaries(
    instancesQuery.data?.items ?? [],
  )
  const engine = postgresClass(instances)

  return (
    <section className="section data-instances" data-testid="data-instances-panel">
      <style>{DATA_INSTANCES_CSS}</style>
      <h2>Data instances</h2>
      <p className="section-hint di-sub">
        Shareable PostgreSQL engines. Each instance is one CNPG cluster; apps
        share an instance via isolated databases + per-app roles. Pure
        Flux-applied YAML — no controller, no Crossplane.
      </p>

      {/* 1. Engine CLASS card (shown once) */}
      <div className="di-engines">
        <div className="di-engine-card" data-testid="engine-class-card">
          <span className="di-engine-icon">🐘</span>
          <div className="di-engine-body">
            <span className="di-engine-title">{engine.title}</span>
            <span className="di-engine-sub">Engine · {engine.blueprint}</span>
          </div>
          <span
            className="di-engine-count"
            data-testid="engine-instance-count"
            title="data-instances running this engine"
          >
            {engine.instanceCount} instance{engine.instanceCount === 1 ? '' : 's'}
          </span>
        </div>
      </div>

      {instancesQuery.isPending ? (
        <p className="section-hint" data-testid="data-instances-loading">
          Loading data instances…
        </p>
      ) : instancesQuery.isError ? (
        <p
          className="section-hint di-error"
          data-testid="data-instances-error"
        >
          Failed to load data instances:{' '}
          {(instancesQuery.error as Error)?.message ?? 'unknown error'}
        </p>
      ) : instances.length === 0 ? (
        <div className="di-empty" data-testid="data-instances-empty">
          No PostgreSQL data instances yet.
        </div>
      ) : (
        // 2. Data-instance cards, each with a Consumers (bindings) table.
        <div className="di-instances">
          {instances.map((inst) => (
            <div
              key={inst.name}
              className="di-instance-card"
              data-testid="data-instance-card"
              data-instance={inst.name}
            >
              <div className="di-instance-head">
                <span className="di-instance-name">{inst.name}</span>
                <span className="di-pill di-pill-bp">{inst.blueprint}</span>
                <span className="di-pill di-pill-topo">{inst.topology}</span>
                {inst.status ? (
                  <span className="di-pill di-pill-status">{inst.status}</span>
                ) : null}
                {isShared(inst) ? (
                  <span
                    className="di-pill di-pill-shared"
                    title="Shared by more than one app"
                  >
                    SHARED
                  </span>
                ) : null}
              </div>

              <div className="di-consumers">
                <div className="di-consumers-head">Consumers (bindings)</div>
                {inst.bindings.length === 0 ? (
                  // Honest empty state: this row arrived WITHOUT a
                  // `bindings[]` field (non-postgres blueprint or an older
                  // catalyst-api). Never fabricate rows client-side.
                  <div
                    className="di-no-consumers"
                    data-testid="data-instance-no-bindings"
                  >
                    Bindings (database · role · mode · secret) are not
                    surfaced for this instance.
                  </div>
                ) : (
                  <table
                    className="di-bindings"
                    data-testid="consumers-table"
                  >
                    <thead>
                      <tr>
                        <th>App</th>
                        <th>Database</th>
                        <th>Role</th>
                        <th>Mode</th>
                        <th>Secret</th>
                      </tr>
                    </thead>
                    <tbody>
                      {inst.bindings.map((b) => (
                        <tr
                          key={`${b.consumer}/${b.database}`}
                          data-testid="binding-row"
                        >
                          <td>{b.consumer}</td>
                          <td>
                            <code>{b.database}</code>
                          </td>
                          <td>
                            <code>{b.role}</code>
                          </td>
                          <td>
                            <span className={`di-mode di-mode-${b.mode}`}>
                              {b.mode}
                            </span>
                          </td>
                          <td>
                            <code className="di-secret">{b.secret}</code>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

/* ─── CSS — mirrors DataInstances.svelte's styles, adapted to the
 *      catalyst-ui `.section` / `--color-*` token language. ──────────── */
const DATA_INSTANCES_CSS = `
.data-instances .di-sub { max-width: 52rem; }

.di-engines { margin: 0.4rem 0 1rem; }
.di-engine-card {
  display: flex; align-items: center; gap: 0.75rem;
  background: var(--color-surface);
  border: 1.5px dashed var(--color-border);
  border-radius: 12px;
  padding: 0.6rem 0.9rem;
  max-width: 28rem;
}
.di-engine-icon { font-size: 1.4rem; }
.di-engine-body { display: flex; flex-direction: column; min-width: 0; }
.di-engine-title { font-size: 0.92rem; font-weight: 600; color: var(--color-text-strong); }
.di-engine-sub {
  font-size: 0.7rem; color: var(--color-text-dim);
  font-family: var(--font-mono, ui-monospace, monospace);
}
.di-engine-count {
  margin-left: auto;
  font-size: 0.72rem; color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}

.di-empty {
  padding: 0.8rem 1rem;
  background: var(--color-surface);
  border: 1px dashed var(--color-border);
  border-radius: 10px;
  color: var(--color-text-dim);
  font-size: 0.82rem;
}
.di-error { color: var(--color-danger); }

.di-instances {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
  gap: 0.9rem;
}
.di-instance-card {
  background: var(--color-surface);
  border: 1.5px solid var(--color-border);
  border-radius: 12px;
  padding: 0.75rem 0.9rem;
}
.di-instance-head {
  display: flex; flex-wrap: wrap; align-items: center; gap: 0.5rem;
  margin-bottom: 0.6rem;
}
.di-instance-name {
  font-size: 0.95rem; font-weight: 600; color: var(--color-text-strong);
}
.di-pill {
  padding: 0.05rem 0.45rem; border-radius: 4px; font-size: 0.62rem;
  font-weight: 600; letter-spacing: 0.03em; text-transform: uppercase;
}
.di-pill-bp {
  background: color-mix(in srgb, var(--color-border) 50%, transparent);
  color: var(--color-text-dim);
  font-family: var(--font-mono, ui-monospace, monospace);
}
.di-pill-topo {
  background: color-mix(in srgb, var(--color-accent) 14%, transparent);
  color: var(--color-accent);
}
.di-pill-status {
  background: color-mix(in srgb, var(--color-border) 40%, transparent);
  color: var(--color-text-dim);
}
.di-pill-shared {
  background: color-mix(in srgb, var(--color-success) 16%, transparent);
  color: var(--color-success);
}

.di-consumers-head {
  font-size: 0.72rem; font-weight: 600; letter-spacing: 0.02em;
  color: var(--color-text-dim); text-transform: uppercase;
  margin-bottom: 0.35rem;
}
.di-no-consumers { font-size: 0.8rem; color: var(--color-text-dim); }

table.di-bindings {
  width: 100%; border-collapse: collapse; font-size: 0.78rem;
}
table.di-bindings th {
  text-align: left; font-weight: 600; color: var(--color-text-dim);
  padding: 0.25rem 0.5rem 0.25rem 0;
  border-bottom: 1px solid var(--color-border);
}
table.di-bindings td {
  padding: 0.3rem 0.5rem 0.3rem 0;
  border-bottom: 1px solid color-mix(in srgb, var(--color-border) 40%, transparent);
  color: var(--color-text-strong);
}
table.di-bindings code {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.72rem;
}
.di-secret { color: var(--color-text-dim); }
.di-mode {
  padding: 0.04rem 0.4rem; border-radius: 4px; font-size: 0.62rem;
  font-weight: 600; text-transform: uppercase;
}
.di-mode-shared {
  background: color-mix(in srgb, var(--color-success) 16%, transparent);
  color: var(--color-success);
}
.di-mode-private,
.di-mode-dedicated {
  background: color-mix(in srgb, var(--color-text-dim) 14%, transparent);
  color: var(--color-text-dim);
}
`
