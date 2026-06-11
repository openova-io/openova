/**
 * dataInstances.ts — the ADR-0010 "Data instances" model, ported into the
 * DEPLOYED React operator console (catalyst-ui).
 *
 * Background: the ADR-0010 reusable/shareable backing-services view first
 * shipped in the Svelte console (`core/console/src/lib/dataInstances.ts` +
 * `DataInstances.svelte`), but that console is built as the `console`
 * image and deployed UNROUTED in the `sme` namespace — the founder can't
 * reach it. This module + the companion `DataInstances.tsx` surface the
 * SAME view in catalyst-ui (served at console.<fqdn>), where the founder
 * actually looks (#3188 row-3/4 walk complaint).
 *
 * The model (ADR-0010): a `bp-postgres` install = one CNPG Cluster = one
 * DATA-INSTANCE. The single PostgreSQL ENGINE CLASS (the `bp-cnpg`
 * operator) is shown ONCE; each data-instance is a first-class card.
 * Consumers SHARE an instance via isolated databases + per-consumer roles.
 *
 * FEEDING ENDPOINT (verified, reused — NOT invented):
 *
 *   GET /catalyst/v1/catalog/{blueprint}/instances → ListBlueprintInstancesResponse
 *
 * served by catalyst-api (`endpoint_handler.go` HandleListBlueprintInstances,
 * registered at cmd/api/main.go without the /api/ prefix). Its row type
 * `ApplicationInstanceSummary` mirrors the Go `applicationSummary`
 * (endpoint_handler.go:119) VERBATIM — fields `id`, `name`, `blueprint`,
 * `org`, `topology`, `status`, `createdAt` (all lowercase camelCase per the
 * Go `json:` tags). This is the source of the engine-class card's instance
 * count and the per-instance cards.
 *
 * HONEST GAP (do not fabricate): NO catalyst-api endpoint exposes the
 * per-consumer BINDING rows (database · role · mode · secret). Those live
 * only in each bp-postgres install HR's chart `values.databases[]`, which
 * no API surfaces today. So a data-instance's `bindings` is empty here and
 * the Consumers table renders the honest "bindings not yet surfaced by the
 * API" state — never a fabricated row. When the model is gated off (the
 * default on a stock prov, `SOVEREIGN_ENABLE_SHARED_PG=false`) the
 * instances list is empty and the panel renders "No PostgreSQL data
 * instances yet." Surfacing live bindings is a backend endpoint first
 * (a different PR) — see #3188.
 */

import type { ApplicationInstanceSummary } from '@/lib/catalog.api'

/** One consumer's binding to a data-instance (one row in the table). */
export interface Binding {
  /** Consumer Blueprint / app id, e.g. "bp-harbor". */
  consumer: string
  /** Isolated database name on the shared Cluster, e.g. "registry". */
  database: string
  /** Per-consumer login role (CNPG-managed), e.g. "harbor". */
  role: string
  /** Binding provenance — private (dedicated) or shared. */
  mode: 'private' | 'shared'
  /** Connection Secret reflected into the consumer namespace. */
  secret: string
}

/** A data-instance = one CNPG Cluster (one bp-postgres App card). */
export interface DataInstance {
  /** Instance name = Cluster name = App-card title, e.g. "shared-pg". */
  name: string
  /** Engine class id (always bp-postgres here). */
  blueprint: string
  /** BCP topology of the instance (inherited by every consumer). */
  topology: 'singleton' | 'active-hot-standby'
  /** Live status of the instance (Ready/Failed/Provisioning/…). */
  status: string
  /** The consumers sharing this instance. */
  bindings: Binding[]
}

/** The engine CLASS card (the operator, shown once under Platform/engines). */
export interface EngineClass {
  /** Engine class id, e.g. "bp-cnpg". */
  blueprint: string
  /** Display title, e.g. "PostgreSQL". */
  title: string
  /** How many data-instances run this engine. */
  instanceCount: number
}

/**
 * postgresClass derives the single PostgreSQL engine-class card from the
 * set of data-instances. The engine (bp-cnpg operator) is shown ONCE; the
 * count is the number of data-instances using it. Mirrors the Svelte
 * `postgresClass` verbatim.
 */
export function postgresClass(instances: DataInstance[]): EngineClass {
  return {
    blueprint: 'bp-cnpg',
    title: 'PostgreSQL',
    instanceCount: instances.length,
  }
}

/**
 * isShared reports whether a data-instance is shared by >1 consumer (the
 * reuse case). Drives the "SHARED" pill on the data-instance card. Mirrors
 * the Svelte `isShared` verbatim.
 */
export function isShared(instance: DataInstance): boolean {
  return instance.bindings.length > 1
}

/**
 * normalizeTopology maps any topology string to the two display states.
 * The instances endpoint returns the per-install topology string; only
 * `active-hot-standby` is the multi-region HA shape, everything else
 * (singleton / single-region / empty) renders as `singleton`.
 */
function normalizeTopology(topology: string | undefined): DataInstance['topology'] {
  return topology === 'active-hot-standby' ? 'active-hot-standby' : 'singleton'
}

/**
 * fromInstanceSummaries builds the DataInstance[] the console renders from
 * the LIVE `GET /catalyst/v1/catalog/{blueprint}/instances` response rows
 * (the same `ApplicationInstanceSummary[]` the InstancesSection consumes).
 *
 * Each bp-postgres install = one data-instance card. The summary carries
 * NO binding data (the API doesn't expose the per-consumer database/role/
 * secret rows — see the module header), so `bindings` is always empty here;
 * the Consumers table renders the honest "not yet surfaced" state rather
 * than fabricating rows. The ENGINE CLASS card's count is `len(instances)`.
 *
 * CRITICAL (memory feedback_ts_field_casing_vs_go_json_tag): every field
 * read here (`name`, `topology`, `status`) matches the Go `json:` tag
 * VERBATIM. `res.json()` returns wire keys exactly — a casing mismatch
 * silently yields undefined (documented prior outage).
 */
export function fromInstanceSummaries(
  rows: ApplicationInstanceSummary[],
): DataInstance[] {
  return rows
    .map((r) => ({
      name: r.name,
      blueprint: 'bp-postgres',
      topology: normalizeTopology(r.topology),
      status: r.status ?? '',
      bindings: [] as Binding[],
    }))
    .sort((a, b) => a.name.localeCompare(b.name))
}
