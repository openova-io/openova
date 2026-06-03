# bp-continuum

OpenOva Continuum — DR orchestration for active-hotstandby
Applications. Slice K-Cont-1 of EPIC-6 (#1101).

## What this ships

K-Cont-1 ships the **skeleton**: chart + Containerfile + binary
scaffold + GHA workflow. The Reconcile() body is a no-op. K-Cont-2
fills in the reconcile loop:

- per-Continuum-CR goroutine maintaining a lease (10s renew, 30s TTL)
- watches CNPG replication metrics
- switchover sequencer (drain HTTPRoute → flip lua-record → flip CNPG
  primary → audit on NATS)
- failback handler (manual approval gate)

K-Cont-3 wires the lease witness (Cloudflare KV per SRE.md §2.4, with
3-DNS-quorum fallback). K-Cont-4 ships the Cloudflare Worker source.

## Install

```bash
# Default-OFF gate keeps the controller stopped until the sovereign-admin
# installs bp-cnpg-pair + bp-powerdns and configures the witness.
helm install bp-continuum ./products/continuum/chart \
  --namespace openova-system \
  --create-namespace
```

To enable on a per-Sovereign overlay:

```yaml
# clusters/<sovereign>/values-continuum.yaml
continuum:
  enabled: true
  image:
    tag: "<sha-stamped-by-CI>"
  pdmURL: "http://pool-domain-manager.openova-system.svc.cluster.local:8080"
  natsURL: "nats://nats.openova-system.svc.cluster.local:4222"
```

## CRD

The `Continuum.dr.openova.io/v1` CRD lives in the Catalyst chart at
`products/catalyst/chart/crds/continuum.yaml` (slice B8 of EPIC-0,
#1110). It is **not duplicated** here — see `crds/README.md`.

## Reconcile contract (K-Cont-2)

The K-Cont-2 reconciler will:

1. Fetch the Continuum CR. Validate the referenced Application has
   `placement: active-hotstandby`. Validate `primaryRegion` ∈
   `Application.spec.regions[]`.
2. Acquire / renew the lease via the witness (kind from
   `spec.leaseClient.kind`).
3. Watch CNPG `cnpg.io/cluster.replicationLag` per replica.
4. On switchover (sovereign-admin-initiated or auto-failover when health
   check + witness quorum agree primary is unreachable), execute the
   sequence in `docs/ARCHITECTURE.md` §9.3.
5. Patch status (phase, primaryRegion, leaseHolder, leaseExpiresAt,
   replicationLag map, conditions, lastSwitchover).

## Reading order for K-Cont-2 implementer

1. `docs/ARCHITECTURE.md` §9 (full Continuum DR spec)
2. `docs/SRE.md` §2 (DR runbook + lease witness pattern)
3. `docs/ARCHITECTURE.md` §8 (PowerDNS lua-record geo + health-checked failover DNS pattern)
4. `products/catalyst/chart/crds/continuum.yaml` (CRD shape)
5. `core/controllers/continuum/DESIGN.md` (this slice's design notes)
6. `core/controllers/internal/placement/` (Plan + RegionPlan — reuse
   for failover region resolution)

## Lease witness API contract (K-Cont-3 sketch)

The K-Cont-2 reconciler talks to a `WitnessClient` interface; K-Cont-3
will land the cloudflare-kv + dns-quorum implementations. Sketched
shape (subject to finalization in K-Cont-3):

```go
type WitnessClient interface {
    // Acquire attempts to claim the lease for `holder` with the
    // configured TTL. Returns the granted lease (with lease-id +
    // expiry) or an error. ErrLeaseHeldByAnother is returned when
    // another holder already owns the lease.
    Acquire(ctx context.Context, key string, holder string, ttl time.Duration) (Lease, error)

    // Renew extends the lease for the same holder. Returns
    // ErrLeaseLost if the lease was reassigned.
    Renew(ctx context.Context, lease Lease) (Lease, error)

    // Release relinquishes the lease. Idempotent — releasing a lost
    // lease is not an error.
    Release(ctx context.Context, lease Lease) error

    // Read returns the current lease holder + expiry without
    // attempting to acquire. Used for diagnostic / dry-run.
    Read(ctx context.Context, key string) (LeaseInfo, error)
}
```

K-Cont-2 wires against this interface with a stub implementation;
K-Cont-3 swaps in the real cloudflare-kv + dns-quorum clients.

— K-Cont-1 (#1101)
