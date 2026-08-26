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

## What K-Cont-2 fills in (LANDED #1101)

Done. The Reconcile body now ships:

1. **Per-Continuum-CR goroutine map** keyed by NamespacedName,
   maintained in `r.activeContinuums` (mutex-protected). Reconcile
   spins up a goroutine on first observation; on Reconcile-after-
   delete the ctx is cancelled + the lease released best-effort.
2. **Lease state machine**: 10s renew interval, 30s TTL (overridable
   per-CR via `spec.leaseClient.config.{ttl,renew}Seconds`). On
   ErrLeaseLost the goroutine attempts re-acquire; if another holder
   owns it, status drops to Degraded.
3. **CNPG status watch** via `internal/cnpg`. Looks up the cluster-
   pair by labels `catalyst.openova.io/cnpg-pair` +
   `openova.io/cnpg-role`. The max replication lag across primary
   + replica is the signal.
4. **Switchover sequencer** in `internal/switchover/sequence.go` —
   7 steps per design doc §9.3, each step's success registers a
   rollback hook that fires in reverse order on a later step's
   failure.
5. **Lua-record body synthesizer** in `internal/dns/lua.go` — pure
   function from (Application, SynthParams) → []dns.Record;
   byte-stable across calls.
6. **PDM client** in `internal/pdm/client.go` — POSTs lua-records
   to `<PDM_API_URL>/v1/lua/commit` with optional X-Catalyst-Token
   auth.
7. **NATS JetStream audit publisher** in `internal/events/jetstream.go`
   — emits onto `catalyst.audit` with header `audit-type`. 9
   audit-type constants exported.
8. **Failback handler** with manual approval gate via
   `Sequencer.RequestFailback(ctx, plan, FailbackOptions{...})`:
   emits TypeFailbackPending, blocks on opts.ApprovalCh, then
   Execute + TypeFailbackCompleted.
9. **Status writer** patches phase, primaryRegion, leaseHolder,
   leaseExpiresAt, replicationLagSeconds, switchoverInProgress +
   Step, lastSwitchover, conditions {LeaseHeld, Ready},
   observedGeneration.
10. **WitnessClient interface** + **InMemoryClient** for tests +
    **DefaultSelector** (returns ErrNotImplemented for K-Cont-3
    paths). The `in-memory` kind is gated by env
    `WITNESS_IN_MEMORY=true` (TEST ONLY).

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
(see the multi-region DNS runbook in [`docs/RUNBOOKS.md`](../../docs/RUNBOOKS.md)).

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

---

## Slice F-1+F-2+F-3 — DR runbook + audit + dry-run + post-switchover health (LANDED #1101)

Slice F layers three concerns on top of K-Cont-2's reconciler + sequencer:

1. **F-1** — extend audit-emit coverage with three new audit-types
   the K-Cont-2 reconciler did NOT cover.
2. **F-2** — pre-switchover dry-run report (`Sequencer.DryRun`) + HTTP
   endpoint that U-DR-1's switchover dialog calls before showing
   [ Confirm Switchover ].
3. **F-3** — post-switchover health check (`Sequencer.PostSwitchoverHealth`)
   + HTTP endpoint that U-DR-1's status panel polls; the controller
   chains it ~30s after a successful switchover and writes the result
   to the Continuum CR's `LastSwitchoverHealthy` status condition.

### Slice F audit-type schemas

K-Cont-2 reserved 9 audit-type names + payload schemas. Slice F-1
adds 3 more:

| Constant            | String value               | Emitted when                                                                                                                                                | Payload (additions on top of base `events.Event`)                                                                                            |
|---------------------|----------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------|
| `TypeCRCreated`     | `continuum-cr-created`     | Reconcile observes a Continuum CR for the first time (per-CR goroutine spin-up); fires exactly once per CR lifetime in this controller-instance's process.  | `Reason="cr-observed"`. `Message` includes `primaryRegion` + `hotStandby[]` for the new CR.                                                  |
| `TypeConfigChanged` | `continuum-config-changed` | The per-CR goroutine observes a switchover-relevant spec change (`PrimaryRegion`, `HotStandbyRegions`, `LeaseClientKind`, `TTLSeconds`, `RenewSeconds`, `RTOSeconds`, `AutoFailover`, `CNPGPair/Namespace`, `HTTPRoute*`, `PDMZone`). | `Reason="config-drift"`. `Message` carries `prev=<fp> cur=<fp>` content fingerprints + the new `primaryRegion`/`hotStandby[]`.                |
| `TypeLeaseCollision` | `continuum-lease-collision` | The lease-renew loop's opportunistic re-acquire (after `ErrLeaseLost`) returns `ErrLeaseHeldByAnother` — the rare two-region rapid race that's split-brain warning material. | `Reason="lease-collision"`. `Message` includes the wrapped error.                                                                            |

Total reserved audit-types now: **12** (`AuditTypes` slice in
`internal/events/events.go`). U-DR-1's UI subscribes to
`audit-type=continuum-*` so it receives the three new types
automatically without code changes.

### F-2 — Dry-run report shape

`Sequencer.DryRun(ctx, plan) (DryRunReport, error)` walks the same 7
steps the actual `Execute` would run, but read-only end-to-end. The
returned `DryRunReport`:

```go
type DryRunReport struct {
    PlanFingerprint                 string       // 16-hex SHA-256 prefix; cache-key-friendly
    Steps                           []DryRunStep // exactly 7 entries, 1..7 order
    Warnings                        []string     // non-fatal advisories
    Blockers                        []string     // FATAL preconditions
    EstimatedDurationSeconds        int          // sum of per-step durations
    EstimatedWriteDisruptionSeconds int          // step-2 + step-3 + step-5
    GeneratedAt                     time.Time
}

type DryRunStep struct {
    Step                     int      // 1..7
    Name                     string   // matches Sequencer.steps()
    PreconditionsMet         bool
    EstimatedDurationSeconds int
    Notes                    []string // human-readable per-step observations
}
```

Per-step duration estimates are exported constants in `dryrun.go`
(`StepValidateLeaseSeconds = 1`, `StepDrainHTTPSeconds = 10`, etc.).

Read-only invariants enforced by tests:

- DryRun produces ZERO audit emits (asserted via Recorder before/after).
- DryRun never mutates CNPG annotations / HTTPRoute weights / DNS
  records / lease state.
- `PlanFingerprint` is content-stable: identical `SwitchoverPlan` →
  identical fingerprint (cache idempotency for the UI).

### F-3 — Post-switchover health-check report shape

`Sequencer.PostSwitchoverHealth(ctx, plan, opts) (HealthReport, error)`
runs 4 checks in fixed order:

```go
type HealthReport struct {
    NewPrimaryRegion string
    Checks           []HealthCheck // exactly 4 entries
    OverallHealthy   bool          // true iff every NON-DEFERRED check Passed
    GeneratedAt      time.Time
}

type HealthCheck struct {
    Name     string // one of: replicas-healthy | dns-probes | latency-normal | audit-posted
    Passed   bool
    Detail   string
    Deferred bool   // true → UI greys out the row
}
```

Check details:

1. **`replicas-healthy`** — Calls `cnpg.Reader.FindPair` + `Get` on
   both halves of the cluster-pair. After a successful switchover
   the CR labeled `cnpg-role=replica` has `replica.enabled=false`
   (it's the new primary) and the CR labeled `cnpg-role=primary` has
   `replica.enabled=true` (it's the new replica). Both must report
   `Ready=true` for the check to pass.
2. **`dns-probes`** — Multi-vantage-point DNS resolution. Default
   resolvers are 8.8.8.8 (Google) / 1.1.1.1 (Cloudflare) / 9.9.9.9
   (Quad9), each issuing UDP queries via `*net.Resolver` with
   `PreferGo=true`. For every (hostname × resolver) pair the IPs
   returned must include AT LEAST ONE IP from
   `plan.SynthParams.RegionToIPs[plan.ToRegion]`. The check is
   `Deferred=true` when no `MultiResolverDial` is wired — production
   wires `DefaultMultiResolverDial`; tests inject fakes.
3. **`latency-normal`** — Permanently `Deferred=true` in this slice;
   wiring to Cilium hubble metrics is out-of-scope. Recorded as
   `Deferred=true, Detail="deferred: Cilium hubble metrics scrape not wired..."`
   so the UI greys out the row but still surfaces it.
4. **`audit-posted`** — Queries the injected `AuditTail` for a
   `continuum-switchover` event matching `(ContinuumName, FromPrimary,
   ToPrimary)` within `AuditWindow` (default 5 minutes). `Deferred=true`
   when `AuditTail` nil — production will wire a NATS PullConsumer
   in a follow-up slice; the F-3 acceptance gate ships with this
   deferred.

Auto-runs `HealthDelay` (default 30s; env `CONTINUUM_HEALTH_DELAY_SECONDS`)
after every successful switchover. Surfaces via:

- Continuum CR `status.conditions[type=LastSwitchoverHealthy]` with
  `status: True | False | Unknown` and a one-line summary in `message`
  (e.g. `"healthy: 3 passed, 0 failed, 1 deferred (new primary=hel)"`).
- `GET /v1/continuums/{ns}/{name}/health` endpoint returns the latest
  cached HealthReport (cache miss → on-demand recomputation).

### F-2 + F-3 endpoints — controller HTTP server

The continuum-controller binary now exposes a small HTTP server on
`CONTINUUM_API_ADDR` (default `:8082`) with two endpoints:

| Verb | Path                                                | Body                                | Returns                              |
|------|-----------------------------------------------------|-------------------------------------|--------------------------------------|
| POST | `/v1/continuums/{ns}/{name}/dry-run`                | `{"toRegion":"hel","reason":"..."}` | `switchover.DryRunReport`            |
| GET  | `/v1/continuums/{ns}/{name}/health`                 | —                                   | `switchover.HealthReport`            |
| GET  | `/healthz`                                          | —                                   | `ok`                                 |

**Auth** — per INVIOLABLE-PRINCIPLES #5 these endpoints require owner
tier. The controller binary doesn't itself implement Keycloak JWT
validation (that's catalyst-api's job); instead it requires:

- `X-Catalyst-Owner-Tier: true` header on every call (catalyst-api
  stamps this after JWT validation), AND
- `Authorization: Bearer <CONTINUUM_API_TOKEN>` when
  `CONTINUUM_API_TOKEN` env var is non-empty (defence in depth — a
  misconfigured Ingress can't bypass auth).

**Path shape** — the brief specifies `/api/v1/sovereigns/{id}/...` as
the externally-facing shape; the catalyst-api proxies into the
continuum-controller's in-cluster Service via the existing `k8scache`
pattern. The continuum-controller exposes only the inner shape; the
outer envelope is the catalyst-api's responsibility (separate slice).

**Errors** — JSON `{"error":"<msg>"}` body:

| Status | Cause                                                                 |
|--------|-----------------------------------------------------------------------|
| 400    | Bad JSON / missing `toRegion`                                         |
| 401    | Missing/invalid `X-Catalyst-Owner-Tier` or wrong bearer token         |
| 404    | Unknown CR                                                            |
| 405    | Wrong HTTP method                                                     |
| 500    | Provider error (witness selector failure, etc.)                       |

### Out of scope for slice F

Per the slice F brief:

- WitnessClient interface changes (K-Cont-3 final)
- CF Worker changes (K-Cont-4 final)
- UI (U-DR-1 separate)
- 1M-row acceptance test (C-DB-3 separate)
- NATS PullConsumer wiring for `audit-posted` health check (the check
  is `Deferred=true` until follow-up)
- Cilium hubble metrics scrape for `latency-normal` health check
  (permanently deferred; SRE follow-up)

— Slice F-1+F-2+F-3 (#1101)
