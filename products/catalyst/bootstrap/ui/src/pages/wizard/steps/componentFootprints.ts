/**
 * componentFootprints.ts — per-component RAM / CPU floor for the wizard's
 * pre-launch footprint estimate (issue #767).
 *
 * ── Why this exists ──────────────────────────────────────────────────
 *
 * The wizard's StepReview surfaces an *estimate* of the bootstrap-kit's
 * total RAM/CPU footprint so the operator picks a worker pool that fits.
 * Live evidence motivating this (otech92): two cpx32 workers couldn't fit
 * the external-secrets-webhook Pod because the bootstrap-kit's RAM
 * aggregate (~14 GB across 35 HelmReleases) exceeded the 16 GB pool the
 * operator picked. With a pre-launch estimate, the wizard would have
 * warned the operator to bump to 3 workers BEFORE `tofu apply` ran.
 *
 * ── How the values are derived ───────────────────────────────────────
 *
 * Each entry is the RAM floor (MiB) + CPU floor (millicores) the
 * blueprint REQUESTS at install time on a fresh Sovereign — the value
 * that flows through to the kube-scheduler's bin-packing decision.
 *
 *   • RAM_MB    — sum of `resources.requests.memory` across every Pod
 *                 the chart instantiates at default install. NOT the
 *                 limit. NOT the actual steady-state usage.
 *   • CPU_MILLI — sum of `resources.requests.cpu` (in millicores)
 *                 across the same Pods.
 *
 * Sources for each value (in priority order):
 *   1. The Catalyst-curated `values.yaml` in `platform/<id>/chart/` when
 *      it sets `requests:` explicitly.
 *   2. The upstream chart's default `values.yaml` for the pinned version
 *      when (1) does not override the request.
 *   3. Empirical post-install `kubectl top` numbers from the most recent
 *      otechN run, rounded UP to the nearest 64 MiB / 50m to leave
 *      headroom for cold-start spikes (e.g. Java GC, post-install hooks).
 *
 * Per `docs/INVIOLABLE-PRINCIPLES.md` #4 (never hardcode), this table is
 * configuration, not application logic — the underlying truth is the
 * upstream chart values + Catalyst overlays. When a Catalyst overlay
 * tightens or loosens a request, refresh this table in the same PR.
 *
 * ── Components NOT in this table ─────────────────────────────────────
 *
 * Components without a bp-* HelmRelease in the bootstrap-kit (Phase 0
 * fixtures: opentofu, vcluster — provisioned outside Flux) carry a
 * sentinel `0` so the rollup math sees them as "not on the cluster
 * yet". The same convention covers any component the wizard surfaces
 * but does not install at Phase 0 (e.g. some RELAY components when not
 * selected — they only matter when the operator picks them).
 *
 * ── Tier semantics ───────────────────────────────────────────────────
 *
 * The "bootstrap-kit baseline" the StepReview rollup uses is the SUM of
 * footprints across every component whose tier is `mandatory` AFTER the
 * transitive-mandatory promotion in componentGroups.ts. The "selected
 * components add" line sums every recommended/optional component the
 * operator actively selected (i.e. component ids in the wizard store's
 * `componentGroups` map).
 */

export interface ComponentFootprint {
  /** RAM floor in MiB (sum of requests.memory across the chart's Pods). */
  ramMb: number
  /** CPU floor in millicores (sum of requests.cpu across the chart's Pods). */
  cpuMilli: number
}

/**
 * Catalog of per-component install-time floors. Values are educated
 * floors, not exact specs — the goal is to prevent the operator from
 * picking a clearly-too-small worker pool, not to predict steady-state
 * usage to the megabyte.
 *
 * Components not in this map default to `0` floors (see `footprintFor`).
 */
export const COMPONENT_FOOTPRINTS: Record<string, ComponentFootprint> = {
  /* ── PILOT — GitOps & IaC ─────────────────────────────────────── */
  flux:           { ramMb: 768,  cpuMilli: 300 }, // 4 controllers (source / kustomize / helm / notification) × ~192 MiB
  crossplane:     { ramMb: 512,  cpuMilli: 200 },
  gitea:          { ramMb: 768,  cpuMilli: 200 }, // gitea + cnpg side handled separately via cnpg
  // opentofu / vcluster — Phase 0 fixtures, not installed by Flux.
  opentofu:       { ramMb: 0,    cpuMilli: 0 },
  vcluster:       { ramMb: 0,    cpuMilli: 0 },

  /* ── SPINE — Networking & Service Mesh ────────────────────────── */
  cilium:         { ramMb: 768,  cpuMilli: 300 }, // operator + agent DaemonSet (per-node) — single-node assumed
  coraza:         { ramMb: 256,  cpuMilli: 100 },
  powerdns:       { ramMb: 512,  cpuMilli: 200 }, // pdns-auth + cnpg-pg side
  'external-dns': { ramMb: 64,   cpuMilli: 50  },
  envoy:          { ramMb: 256,  cpuMilli: 200 },
  frpc:           { ramMb: 64,   cpuMilli: 50  },
  netbird:        { ramMb: 256,  cpuMilli: 100 },
  strongswan:     { ramMb: 128,  cpuMilli: 50  },

  /* ── SURGE — Scaling & Resilience ─────────────────────────────── */
  vpa:            { ramMb: 512,  cpuMilli: 150 }, // recommender + updater + admission-controller
  keda:           { ramMb: 384,  cpuMilli: 200 }, // operator + metrics-adapter
  reloader:       { ramMb: 64,   cpuMilli: 50  },
  continuum:      { ramMb: 128,  cpuMilli: 100 },

  /* ── SILO — Storage & Registry ────────────────────────────────── */
  seaweedfs:      { ramMb: 1024, cpuMilli: 500 }, // master + volume + filer
  velero:         { ramMb: 256,  cpuMilli: 100 },
  harbor:         { ramMb: 1536, cpuMilli: 500 }, // core + jobservice + registry + portal + redis + chartmuseum + notary

  /* ── GUARDIAN — Security & Identity ───────────────────────────── */
  falco:          { ramMb: 512,  cpuMilli: 200 },
  kyverno:        { ramMb: 768,  cpuMilli: 300 }, // admission + reports + cleanup + background controllers
  trivy:          { ramMb: 512,  cpuMilli: 200 },
  'syft-grype':   { ramMb: 256,  cpuMilli: 100 },
  sigstore:       { ramMb: 512,  cpuMilli: 200 }, // fulcio + rekor + ctlog
  keycloak:       { ramMb: 1536, cpuMilli: 500 }, // keycloak + cnpg-pg side
  openbao:        { ramMb: 768,  cpuMilli: 300 }, // 3-node Raft cluster on the same host
  'external-secrets': { ramMb: 384, cpuMilli: 100 }, // controller + webhook + cert-controller
  'cert-manager': { ramMb: 384,  cpuMilli: 150 }, // cainjector + webhook + controller

  /* ── INSIGHTS — AIOps & Observability ─────────────────────────── */
  grafana:        { ramMb: 512,  cpuMilli: 200 },
  opentelemetry:  { ramMb: 384,  cpuMilli: 200 },
  alloy:          { ramMb: 384,  cpuMilli: 200 },
  loki:           { ramMb: 1024, cpuMilli: 400 },
  mimir:          { ramMb: 1024, cpuMilli: 400 },
  tempo:          { ramMb: 768,  cpuMilli: 300 },
  opensearch:     { ramMb: 2048, cpuMilli: 500 },
  litmus:         { ramMb: 256,  cpuMilli: 100 },
  openmeter:      { ramMb: 512,  cpuMilli: 200 },
  specter:        { ramMb: 512,  cpuMilli: 200 },

  /* ── FABRIC — Data & Integration ──────────────────────────────── */
  cnpg:           { ramMb: 512,  cpuMilli: 200 }, // operator only; per-DB instances accrue via consumers
  valkey:         { ramMb: 256,  cpuMilli: 100 },
  strimzi:        { ramMb: 768,  cpuMilli: 300 }, // operator + kafka + zookeeper
  debezium:       { ramMb: 512,  cpuMilli: 200 },
  flink:          { ramMb: 1024, cpuMilli: 400 },
  temporal:       { ramMb: 768,  cpuMilli: 300 }, // frontend + history + matching + worker + cnpg
  clickhouse:     { ramMb: 1024, cpuMilli: 500 },
  ferretdb:       { ramMb: 256,  cpuMilli: 100 },
  iceberg:        { ramMb: 256,  cpuMilli: 100 },
  superset:       { ramMb: 768,  cpuMilli: 300 },

  /* ── CORTEX — AI & Machine Learning ───────────────────────────── */
  kserve:         { ramMb: 384,  cpuMilli: 200 },
  knative:        { ramMb: 768,  cpuMilli: 300 }, // serving + eventing
  axon:           { ramMb: 256,  cpuMilli: 100 },
  neo4j:          { ramMb: 1024, cpuMilli: 400 },
  vllm:           { ramMb: 4096, cpuMilli: 1000 }, // inference; very GPU-bound but the requests are RAM-heavy
  milvus:         { ramMb: 1536, cpuMilli: 500 },
  bge:            { ramMb: 1024, cpuMilli: 400 },
  langfuse:       { ramMb: 512,  cpuMilli: 200 },
  librechat:      { ramMb: 512,  cpuMilli: 200 },

  /* ── RELAY — Communication ────────────────────────────────────── */
  stalwart:       { ramMb: 512,  cpuMilli: 200 },
  livekit:        { ramMb: 768,  cpuMilli: 300 },
  stunner:        { ramMb: 256,  cpuMilli: 100 },
  matrix:         { ramMb: 1024, cpuMilli: 400 },
  ntfy:           { ramMb: 128,  cpuMilli: 50  },
}

/** Empty-footprint sentinel used for components missing from the catalog. */
const EMPTY: ComponentFootprint = { ramMb: 0, cpuMilli: 0 }

/**
 * Per-id footprint accessor. Returns the catalog entry for `id` or the
 * `EMPTY` sentinel when the component is not in the table — keeps the
 * caller code branch-free.
 */
export function footprintFor(id: string): ComponentFootprint {
  return COMPONENT_FOOTPRINTS[id] ?? EMPTY
}

/**
 * Sum the RAM + CPU footprints across an iterable of component ids.
 * The output is the cluster-wide REQUESTS floor (NOT limits) — the value
 * the kube-scheduler bin-packs against.
 */
export function sumFootprints(ids: Iterable<string>): ComponentFootprint {
  let ramMb = 0
  let cpuMilli = 0
  for (const id of ids) {
    const f = footprintFor(id)
    ramMb += f.ramMb
    cpuMilli += f.cpuMilli
  }
  return { ramMb, cpuMilli }
}

/**
 * Catalyst control-plane overhead — the RAM/CPU the k3s control plane
 * itself consumes (apiserver + controller-manager + scheduler + etcd +
 * kubelet) before any blueprint installs. Empirical floor on a freshly
 * provisioned cpx22 control plane: ~1 GiB RAM, 500 mC CPU. Surfaced in
 * the rollup so the operator's "available capacity" math accounts for
 * the CP node's own consumption.
 */
export const CONTROL_PLANE_OVERHEAD: ComponentFootprint = {
  ramMb: 1024,
  cpuMilli: 500,
}

/**
 * Convert a Hetzner-style CPU/RAM SKU spec into worker-pool capacity.
 * `vcpu` is in whole CPUs (each = 1000 millicores); `ramGb` is in
 * gibibytes (each = 1024 MiB). Used by the StepReview footprint section
 * to compute "how many workers does the operator need to fit the
 * estimated footprint?".
 */
export function workerCapacity(vcpu: number, ramGb: number): ComponentFootprint {
  return {
    ramMb: ramGb * 1024,
    cpuMilli: vcpu * 1000,
  }
}

/**
 * Recommend a minimum worker count given a per-worker SKU + estimated
 * total footprint. Returns the ceil(footprint / per-worker capacity)
 * across BOTH dimensions (RAM and CPU); the binding constraint wins.
 *
 *   recommendedWorkers({vcpu:4,ramGb:8}, {ramMb:14336,cpuMilli:6000})
 *     → ceil(14336 / 8192) = 2  (RAM-bound)
 *       ceil(6000 / 4000)  = 2  (CPU-bound)
 *       max(2, 2) = 2
 *
 * Always returns at least 1 — every Sovereign needs at least one worker
 * to schedule the bootstrap-kit. Returns 0 only when both numerators
 * are 0 (empty selection — operator hasn't picked anything yet).
 */
export function recommendedWorkers(
  perWorker: ComponentFootprint,
  total: ComponentFootprint,
): number {
  if (total.ramMb === 0 && total.cpuMilli === 0) return 0
  const ramWorkers = perWorker.ramMb > 0 ? Math.ceil(total.ramMb / perWorker.ramMb) : 0
  const cpuWorkers = perWorker.cpuMilli > 0 ? Math.ceil(total.cpuMilli / perWorker.cpuMilli) : 0
  return Math.max(1, ramWorkers, cpuWorkers)
}
