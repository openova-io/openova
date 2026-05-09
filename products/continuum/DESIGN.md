# Continuum — Design Notes (K-Cont-1)

This file captures the architectural decisions made by slice K-Cont-1
of EPIC-6 (#1101). It is the entry point for the K-Cont-2, K-Cont-3,
and K-Cont-4 implementers.

## Module split — chart vs binary

Per ADR-0001 §3.2.8, Continuum is a **customer-facing capability**, so
the product lives at `products/continuum/`. But Continuum is *also* a
controller-runtime reconciler that wants to share `internal/labels`,
`internal/placement`, `pkg/gitea`, and the same canonical
controller-runtime scaffolding as the 5 Group C controllers.

We pick **Option A** from the K-Cont-1 brief: the **Go binary lives
under the shared `core/controllers/` module**, and the
**chart + Containerfile + DESIGN** live under `products/continuum/`:

```
core/controllers/
├── go.mod                                        # ONE module (CC1)
├── internal/{labels,semver,placement,render}/    # SHARED helpers
├── pkg/gitea/                                    # SHARED Gitea client
├── application/cmd/main.go                       # slice C4
├── ...                                           # slices C1–C5
└── continuum/                                    # NEW — K-Cont-1
    ├── cmd/main.go
    ├── internal/controller/continuum_controller.go
    ├── internal/events/                          # placeholder K-Cont-2
    └── Containerfile

products/continuum/
├── DESIGN.md                                     # this file
└── chart/
    ├── Chart.yaml                                # bp-continuum 0.1.0
    ├── values.yaml                               # default-OFF
    ├── crds/README.md                            # CRD lives in catalyst chart
    ├── templates/{_helpers.tpl,deployment,service,
    │              serviceaccount,rbac,networkpolicy}.yaml
    ├── blueprint.yaml                            # OpenOva Blueprint manifest
    └── README.md
```

This split mirrors the precedent set by `core/services/catalyst-catalog/`
(the Slice L pattern): controllers / services that are part of the
shared module live under `core/`, while customer-facing chart +
Blueprint metadata lives under `products/`.

If product separation requires real module isolation later (e.g. a
go-private build flag, or a dependency the other 5 controllers
shouldn't pull), a future slice can split this out — same trade-off
Slice L navigated for catalog-svc. Until then, sharing the module is
the cleanest path.

## What K-Cont-1 ships (skeleton ONLY)

- `cmd/main.go` — controller-runtime Manager bootstrap; leader
  election; `/healthz`, `/readyz`, `/metrics`.
- `internal/controller/continuum_controller.go` — `ContinuumReconciler`
  with a no-op `Reconcile()` and a `SetupWithManager()` watching the
  Continuum GVR via `unstructured.Unstructured` (per ADR-0001 §2.7 —
  no `controller-gen`).
- `internal/events/` — placeholder package with audit-event-type list
  for K-Cont-2 NATS publisher.
- `Containerfile` — multi-stage Go build → alpine runtime, UID 65534.
- `chart/` — full Helm chart shape: deployment, service, SA, RBAC,
  NetworkPolicy, blueprint.yaml. Default-OFF (`continuum.enabled:
  false`); fail-fast on empty `image.tag` (per Inviolable Principle
  #4a — no `:latest` in production).
- `.github/workflows/build-continuum-controller.yaml` — event-driven
  CI workflow (NO cron) mirroring the 5 sibling controllers'
  per-controller workflows.

## What K-Cont-2 fills in

The Reconcile body. Concretely:

1. **Per-Continuum-CR goroutine map** keyed by NamespacedName. On a
   Reconcile that surfaces a CR the controller hasn't seen, start a
   long-lived goroutine that maintains the lease (10s renew, 30s
   TTL), watches CNPG `cnpg.io/cluster.replicationLag` per replica,
   and emits status updates. On Reconcile-after-deletion, cancel the
   goroutine + release the lease.
2. **Application validation**: fetch the referenced Application,
   confirm `spec.placement == active-hotstandby` and
   `spec.regions[] ⊇ {primaryRegion} ∪ hotStandbyRegions`. On
   mismatch, set `status.phase=Failed` + Condition Ready=False.
3. **Lease state machine**: Pending → Acquiring → Healthy → Renewing
   loop; lease loss → Degraded → re-acquire or relinquish.
4. **Switchover sequencer** per `docs/EPICS-1-6-unified-design.md`
   §9.3 step-by-step.
5. **Status writer**: phase, primaryRegion, leaseHolder,
   leaseExpiresAt, replicationLag map, lastSwitchover, conditions,
   observedGeneration.
6. **NATS audit publisher** (`internal/events/` package fills out).

## What K-Cont-3 fills in (lease witness)

The `WitnessClient` interface (sketched in `chart/README.md`) and two
implementations:

- **cloudflare-kv** — POSTs to a Worker URL with a CF API token from a
  SealedSecret reference. Worker enforces TTL + atomic CAS via KV.
- **dns-quorum** — TXT-record reads against 3 resolvers (8.8.8.8 /
  1.1.1.1 / 9.9.9.9), 2-of-3 voting, with fence-token expiry. The
  controller writes the TXT record by calling PDM `/v1/commit` (same
  client K-Cont-2 uses for lua-records).

K-Cont-3 also wires the SealedSecret reference for the CF API token —
per CLAUDE.md credential-hygiene rules, the plaintext token never
appears in the Continuum CR.

## What K-Cont-4 fills in (Cloudflare Worker)

The TypeScript Worker source + tofu deploy module. The Worker exposes
a small API (acquire / renew / release / read) backed by a CF KV
namespace + a Durable Object for atomic CAS. Deployed via the existing
`tofu apply` pattern that already manages our Cloudflare zones
(MULTI-REGION-DNS.md §3).

## Lease witness API contract — sketch for K-Cont-2

K-Cont-2 wires its switchover sequencer against a `WitnessClient`
interface with the methods sketched in `chart/README.md`. K-Cont-2
ships an in-memory stub for tests; K-Cont-3 swaps in the real CF-KV
+ DNS-quorum clients.

## RBAC — least-privilege today, extension list for K-Cont-2

K-Cont-1 ships:

- `continuums.dr.openova.io` get/list/watch/update/patch + status +
  finalizers
- `applications.apps.openova.io` get/list/watch (read-only — K-Cont-2
  may upgrade to update for primaryRegion writes)
- `httproutes.gateway.networking.k8s.io` get/list/watch
- `coordination.k8s.io/leases` full (leader election)
- `""/namespaces` get/list/watch
- `""/events` create/patch

K-Cont-2 will EXTEND with:

- `clusters.postgresql.cnpg.io` get/list/watch + patch on annotations
- `clusters.postgresql.cnpg.io/status` get
- `httproutes.*` update/patch (weight=0 drain at switchover)
- `""/configmaps` full (per-CR state)
- `""/secrets` get (CF KV token, PDM token)

## Out of scope (per the K-Cont-1 brief)

- Reconcile body (K-Cont-2)
- Lease witness implementations (K-Cont-3)
- Cloudflare Worker source (K-Cont-4)
- bp-cnpg-pair Blueprint (C-DB-1)
- Application controller changes (C4 final)
- UI (U-DR-1)

— K-Cont-1 (#1101)
