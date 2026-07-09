/**
 * normaliseKind — canonicalise a raw `kind` URL search param to a valid
 * `CloudListKind` id (kinds.ts `KIND_IDS`) BEFORE the cloud route's React
 * tree mounts.
 *
 * Consumed by BOTH cloud routes' `validateSearch` (`provisionCloudRoute`
 * `/provision/$deploymentId/cloud` and `consoleCloudRoute` `/cloud`). Doing
 * the normalisation at route-parse time means the URL the React tree observes
 * is already canonical on the very first render — no `CloudListView`
 * URL-replace storm on a `search.kind !== activeKind` mismatch, no
 * "/dashboard drift" (the D17 Wave-1 symptom test agents E + C2 saw on
 * t10.omantel.biz).
 *
 * SINGLE SOURCE OF TRUTH FIRST (#4820): `normaliseCloudKind` resolves the
 * lowered value against `kinds.ts` `KIND_IDS` (`isValidKind`) BEFORE
 * consulting the alias map. Any kind with a first-class `CloudListKind`
 * registration therefore wins outright and can NEVER be shadowed by a stale
 * alias again.
 *
 * That ordering is the fix for #4820: `httproutes` / `networkpolicies` /
 * `ciliumnetworkpolicies` / `ciliumclusterwidenetworkpolicies` were
 * first-classed by #3998 (registrations + per-kind pages in kinds.ts +
 * `KIND_TO_REGISTRY` mappings), but a stale D17 alias `→ services` — written
 * back when those kinds were "not in the registry" — still hijacked the
 * `kind` param to the Services list at `validateSearch` time. The chip counts
 * populated correctly (they read the live SSE snapshot directly), yet every
 * per-kind list nav landed on Services. Deleting the stale `→ services`
 * aliases AND resolving `KIND_IDS` first closes both the immediate bug and
 * the whole class of alias-shadows-a-real-kind drift.
 *
 * The alias map now holds ONLY genuine spelling variants — kubectl-natural
 * no-hyphen forms and singular→canonical-plural — that an operator might type
 * in the URL. It must never map a first-class kind to a DIFFERENT kind.
 */

import { isValidKind } from './kinds'

/**
 * Alias map: kubectl-natural / no-hyphen / singular forms → the canonical
 * plural `CloudListKind` id. ONLY genuine spelling aliases live here; a kind
 * that has its own `CloudListKind` registration resolves via `KIND_IDS`
 * first (see `normaliseCloudKind`), so it never needs — and must never get —
 * an alias that points somewhere else.
 */
export const CLOUD_KIND_ALIASES: Record<string, string> = {
  // Hyphen vs no-hyphen (kubectl natural form)
  loadbalancers: 'load-balancers',
  loadbalancer: 'load-balancers',
  nodepools: 'node-pools',
  nodepool: 'node-pools',
  workernodes: 'worker-nodes',
  workernode: 'worker-nodes',
  storageclasses: 'storage-classes',
  storageclass: 'storage-classes',
  dnszones: 'dns-zones',
  dnszone: 'dns-zones',
  // Singular → canonical plural
  pvc: 'pvcs',
  pv: 'persistentvolumes',
  persistentvolume: 'persistentvolumes',
  cluster: 'clusters',
  vcluster: 'vclusters',
  service: 'services',
  ingress: 'ingresses',
  bucket: 'buckets',
  volume: 'volumes',
  pod: 'pods',
  deployment: 'deployments',
  statefulset: 'statefulsets',
  daemonset: 'daemonsets',
  replicaset: 'replicasets',
  configmap: 'configmaps',
  secret: 'secrets',
  namespace: 'namespaces',
  node: 'nodes',
  endpointslice: 'endpointslices',
  // Gateway-API front door + NetworkPolicy security posture (#3998). These
  // are first-class kinds — the PLURAL forms resolve via KIND_IDS directly;
  // these entries only carry the SINGULAR kubectl form to the canonical
  // plural. (Previously the plural forms were stale-aliased to `services`,
  // hijacking every per-kind nav — the #4820 regression.)
  gateway: 'gateways',
  httproute: 'httproutes',
  networkpolicy: 'networkpolicies',
  ciliumnetworkpolicy: 'ciliumnetworkpolicies',
  ciliumclusterwidenetworkpolicy: 'ciliumclusterwidenetworkpolicies',
  // Kyverno PolicyReport CRDs (#1583 / C11-005/C11-006) — singular →
  // canonical plural (both plurals are first-class kinds).
  policyreport: 'policyreports',
  clusterpolicyreport: 'clusterpolicyreports',
}

/**
 * Canonicalise a raw URL `kind` param.
 *
 * Resolution order:
 *   1. lowercase the raw value (URLs may carry `HTTPRoutes`, `Services`, …);
 *   2. if the lowered value is already a valid `CloudListKind` → return it
 *      (single source of truth — first-class kinds win, no alias can shadow);
 *   3. else map a kubectl-natural / singular spelling alias to its canonical
 *      plural;
 *   4. else return the lowered value unchanged — `CloudListView`'s
 *      `isValidKind` fallback to `DEFAULT_KIND` then applies (no drift).
 */
export function normaliseCloudKind(raw: string): string {
  const lower = raw.toLowerCase()
  if (isValidKind(lower)) return lower
  return CLOUD_KIND_ALIASES[lower] ?? lower
}
