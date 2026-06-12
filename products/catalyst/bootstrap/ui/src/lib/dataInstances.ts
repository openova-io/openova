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
 * The model (ADR-0010): ONE live CNPG Cluster = one DATA-INSTANCE. The
 * single PostgreSQL ENGINE CLASS (the `bp-cnpg` operator) is shown ONCE;
 * each data-instance is a first-class card. Consumers SHARE an instance
 * via isolated databases + per-consumer roles (many-to-many).
 *
 * FEEDING ENDPOINT (verified, reused — NOT invented):
 *
 *   GET /catalyst/v1/catalog/{blueprint}/instances → ListBlueprintInstancesResponse
 *
 * served by catalyst-api (`endpoint_handler.go` HandleListBlueprintInstances,
 * registered at cmd/api/main.go without the /api/ prefix). Its row type
 * `ApplicationInstanceSummary` mirrors the Go `applicationSummary`
 * (endpoint_handler.go:119) VERBATIM — fields `id`, `name`, `blueprint`,
 * `org`, `topology`, `status`, `createdAt`, `bindings` (all lowercase
 * camelCase per the Go `json:` tags). For the postgres engine family the
 * endpoint enumerates the live `postgresql.cnpg.io/v1` Cluster CRs — one
 * row per running instance — so the engine-class count and the per-instance
 * cards reflect EVERY live Postgres, not just the shared engine install.
 *
 * BINDINGS (#3188 many-to-many, now surfaced SERVER-SIDE): each row's
 * `bindings[]` carries the consumer rows (database · role · mode ·
 * secret · consumer) derived from the CNPG Database CRs (mode=shared) or
 * the instance's own app database (mode=dedicated). When a row carries no
 * `bindings` (older API, non-postgres blueprints) the Consumers table
 * still renders the honest "not surfaced" state — never a fabricated row.
 * When the instances list is empty the panel renders "No PostgreSQL data
 * instances yet."
 */

import type { ApplicationInstanceSummary } from '@/lib/catalog.api'

/** One consumer's binding to a data-instance (one row in the table). */
export interface Binding {
  /** Consumer app/namespace, e.g. "gitea". Empty when not derivable. */
  consumer: string
  /** Isolated database name on the Cluster, e.g. "registry". */
  database: string
  /** Per-consumer login role (CNPG-managed), e.g. "harbor". */
  role: string
  /**
   * Binding provenance — `shared` (Database CR on a shared instance),
   * `dedicated` (the per-app instance's own app database), or `private`
   * (legacy display fallback for unknown modes).
   */
  mode: 'shared' | 'dedicated' | 'private'
  /** Connection Secret reflected into the consumer namespace. */
  secret: string
}

/** A data-instance = one CNPG Cluster (one card). */
export interface DataInstance {
  /** Instance name = Cluster name = card title, e.g. "shared-pg". */
  name: string
  /** Engine class id (always bp-postgres here). */
  blueprint: string
  /** BCP topology of the instance (inherited by every consumer). */
  topology: 'singleton' | 'ha' | 'replica' | 'active-hot-standby'
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
 * normalizeTopology maps the wire topology string to the display states.
 * The instances endpoint emits `singleton`, `ha` (instances>1), `replica`
 * (spec.replica.enabled — the hot-standby secondary), or the per-install
 * `active-hot-standby`; anything else renders as `singleton`.
 */
function normalizeTopology(topology: string | undefined): DataInstance['topology'] {
  return topology === 'active-hot-standby' ||
    topology === 'ha' ||
    topology === 'replica'
    ? topology
    : 'singleton'
}

/**
 * normalizeMode keeps the wire `mode` verbatim for the two values the API
 * emits (`shared` / `dedicated`); anything unexpected renders with the
 * neutral legacy `private` styling rather than an unstyled class.
 */
function normalizeMode(mode: string | undefined): Binding['mode'] {
  return mode === 'shared' || mode === 'dedicated' ? mode : 'private'
}

/**
 * fromInstanceSummaries builds the DataInstance[] the console renders from
 * the LIVE `GET /catalyst/v1/catalog/{blueprint}/instances` response rows
 * (the same `ApplicationInstanceSummary[]` the InstancesSection consumes).
 *
 * Each row = one data-instance card. Rows from the postgres-family path
 * carry `bindings[]` (surfaced server-side from CNPG Database CRs / the
 * implicit per-app binding — see the module header); rows without it keep
 * `bindings` empty so the Consumers table renders the honest "not
 * surfaced" state rather than fabricating rows. The ENGINE CLASS card's
 * count is `len(instances)`.
 *
 * CRITICAL (memory feedback_ts_field_casing_vs_go_json_tag): every field
 * read here (`name`, `topology`, `status`, `bindings` + its `database` /
 * `role` / `mode` / `secret` / `consumer`) matches the Go `json:` tag
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
      bindings: (r.bindings ?? []).map(
        (b): Binding => ({
          consumer: b.consumer,
          database: b.database,
          role: b.role,
          mode: normalizeMode(b.mode),
          secret: b.secret,
        }),
      ),
    }))
    .sort((a, b) => a.name.localeCompare(b.name))
}
