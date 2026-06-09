// Data-instance binding model (ADR-0010).
//
// A bp-postgres install = one CNPG Cluster = one DATA-INSTANCE. Consumers
// SHARE it via isolated databases + per-consumer roles. This module is the
// pure, framework-free model the console renders:
//
//   - the PostgreSQL ENGINE CLASS card (the engine, shown once);
//   - each DATA-INSTANCE App card with its Consumers (bindings) table
//     (app · database · role · mode · secret);
//   - each consumer's "Backing services" row naming its instance.
//
// The binding data is DECLARATIVE — it comes from the bp-postgres
// instance's `databases[]` values (the same list the chart renders into N
// Database CRs + N managed.roles). NO controller produces it; this module
// only shapes it for display.

/** One consumer's binding to a data-instance (one row in the table). */
export interface Binding {
  /** Consumer Blueprint / app id, e.g. "bp-harbor". */
  consumer: string;
  /** Isolated database name on the shared Cluster, e.g. "registry". */
  database: string;
  /** Per-consumer login role (CNPG-managed), e.g. "harbor". */
  role: string;
  /** Binding provenance — private (dedicated) or shared. */
  mode: 'private' | 'shared';
  /** Connection Secret reflected into the consumer namespace. */
  secret: string;
}

/** A data-instance = one CNPG Cluster (one bp-postgres App card). */
export interface DataInstance {
  /** Instance name = Cluster name = App-card title, e.g. "shared-pg". */
  name: string;
  /** Engine class id (always bp-postgres here). */
  blueprint: string;
  /** BCP topology of the instance (inherited by every consumer). */
  topology: 'singleton' | 'active-hot-standby';
  /** The consumers sharing this instance. */
  bindings: Binding[];
}

/** The engine CLASS card (the operator, shown once under Platform/engines). */
export interface EngineClass {
  /** Engine class id, e.g. "bp-cnpg". */
  blueprint: string;
  /** Display title, e.g. "PostgreSQL". */
  title: string;
  /** How many data-instances run this engine. */
  instanceCount: number;
}

/**
 * postgresClass derives the single PostgreSQL engine-class card from the
 * set of data-instances. The engine (bp-cnpg operator) is shown ONCE; the
 * count is the number of data-instances using it.
 */
export function postgresClass(instances: DataInstance[]): EngineClass {
  return {
    blueprint: 'bp-cnpg',
    title: 'PostgreSQL',
    instanceCount: instances.length,
  };
}

/**
 * isShared reports whether a data-instance is shared by >1 consumer (the
 * reuse case). Drives the "SHARED" pill on the data-instance card.
 */
export function isShared(instance: DataInstance): boolean {
  return instance.bindings.length > 1;
}

/**
 * consumerBackingRow returns the "Backing services" row for a CONSUMER app:
 * the data-instance it is bound to (replacing today's coarse "depends on
 * bp-cnpg" with the actual instance name). Returns null when the consumer
 * has no postgres binding.
 */
export function consumerBackingRow(
  consumer: string,
  instances: DataInstance[],
): { instance: string; database: string; mode: string } | null {
  for (const inst of instances) {
    const b = inst.bindings.find((x) => x.consumer === consumer);
    if (b) {
      return { instance: inst.name, database: b.database, mode: b.mode };
    }
  }
  return null;
}

/**
 * fromInstanceValues builds a DataInstance from the bp-postgres instance's
 * Helm `databases[]` values (the declarative binding source). Mirrors
 * platform/postgres/chart/values.yaml verbatim so the console renders
 * exactly what Flux applied.
 */
export function fromInstanceValues(raw: {
  name: string;
  blueprint?: string;
  topology?: string;
  databases?: Array<{
    name: string;
    owner: string;
    consumer?: { blueprint?: string; mode?: string };
    reflect?: { secretName?: string };
  }>;
}): DataInstance {
  const topology = raw.topology === 'active-hot-standby' ? 'active-hot-standby' : 'singleton';
  const bindings: Binding[] = (raw.databases || []).map((d) => ({
    consumer: d.consumer?.blueprint || d.owner,
    database: d.name,
    role: d.owner,
    mode: d.consumer?.mode === 'shared' ? 'shared' : 'private',
    secret: d.reflect?.secretName || `${d.name}-database-secret`,
  }));
  return {
    name: raw.name,
    blueprint: raw.blueprint || 'bp-postgres',
    topology,
    bindings,
  };
}
