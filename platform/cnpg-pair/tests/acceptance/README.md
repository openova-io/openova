# D31 acceptance harness — Pillar 3 zero-tx-loss verification

> **Status:** code-complete, awaits operator walk on a fresh
> 2-region Sovereign. Refs `#2067` (TBD-V16). Closes the
> `platform/cnpg-pair/DESIGN.md:218-268` "Deferred — C-DB-3
> acceptance test plan" deferral.

## What it does

Per `CLAUDE.md §0` Pillar 3, the deterministic end-user step is:

> *Operator kills primary region → failover-controller flips traffic
> ≤ 30 s, replica CNPG promotes via ReplicaCluster, ClusterMesh keeps
> inter-region pod-to-pod alive, **zero transactions lost** (counter
> verifies continuity).*

This harness is the **automated verifier** for that bar. It:

1. Bootstraps a `regression_d31_counter (id BIGSERIAL PK, payload BYTEA, written_at TIMESTAMPTZ)` table on the **primary**.
2. Spawns 8 writer goroutines INSERTing 1 KB rows in 1 000-row batches against `<primary>-rw` until either (a) 1 000 000 rows are ACK'd or (b) the harness signals region-kill.
3. After 30 s of stable writes (`--pre-kill-warmup`), patches the **primary** Cluster CR's `spec.instances` to `0` — the canonical region-kill proxy per `DESIGN.md:236-243`. Notes the kill timestamp.
4. Patches the **replica** Cluster CR's `spec.replica.enabled` to `false` to trigger CNPG-managed promotion (the manual seam Continuum K-Cont-2 normally drives automatically).
5. Polls the **replica** Cluster CR's `status.currentPrimary` jsonpath until it populates, or fails with diagnostics after `--rto-deadline` (default 90 s = 3× the 30 s RTO bar).
6. Reconnects to `<replica>-rw` (now serving as the new primary post-promote) and `SELECT id FROM regression_d31_counter ORDER BY id`.
7. Asserts two invariants:
   - **floor**: `count(rows visible) ≥ writer ACK'd count at kill time`. Anything the writer received `COMMIT OK` on MUST be present.
   - **gap-free**: the row IDs form a contiguous 1..max sequence. `BIGSERIAL` guarantees dense allocation, so any missing ID means a tx was committed (sequence bumped) on the OLD primary but lost when the region died — exactly the failure mode `synchronous_commit = remote_apply` exists to prevent.

If both invariants pass, exit 0 + PASS line. Otherwise exit 1/2/3 with diagnostics — see "Exit codes" below.

## When to run it

After a fresh 2-region Sovereign + tenant Org + bp-wordpress-tenant Application have provisioned and both `wp-db-primary` / `wp-db-replica` Cluster CRs show `Ready=True`. This is **the last walk** in the D31 Pillar 3 verification chain — the upstream chain pieces are tracked by `#2064` (sync replication), `#2065` (bp-continuum chart), `#2066` (Continuum CR rendering), `#2068` (cnpg-pair install path).

## How to run it (the operator's path)

### A. Run as a one-shot Kubernetes Job

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: d31-acceptance
  namespace: tenant-1
spec:
  backoffLimit: 0           # No retry — a FAIL is signal, not noise.
  ttlSecondsAfterFinished: 86400
  template:
    spec:
      serviceAccountName: d31-acceptance  # Has the RBAC below
      restartPolicy: Never
      containers:
        - name: harness
          image: ghcr.io/openova-io/openova/d31-acceptance:<sha>
          args:
            - --namespace=tenant-1
            - --primary-cluster=wp-db-primary
            - --replica-cluster=wp-db-replica
            - --primary-host=wp-db-primary-rw.tenant-1.svc.cluster.local
            - --replica-host=wp-db-replica-rw.tenant-1.svc.cluster.local
            - --primary-db=app
            - --primary-user=app
            - --primary-sslmode=require
            - --replica-db=app
            - --replica-user=app
            - --replica-sslmode=require
            - --target-rows=1000000
            - --workers=8
            - --pre-kill-warmup=30s
            - --rto-deadline=90s
          env:
            - name: D31_PRIMARY_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: wp-db-primary-app
                  key:  password
            - name: D31_REPLICA_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: wp-db-primary-app   # CNPG re-uses the SAME app-creds Secret on the replica side (same user); see DESIGN.md
                  key:  password
```

### Minimum RBAC for the Job's ServiceAccount

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: d31-acceptance
  namespace: tenant-1
rules:
  - apiGroups: ["postgresql.cnpg.io"]
    resources: ["clusters"]
    verbs: ["get", "patch"]   # patch for the kill + promote; get for the status poll
```

### B. Run locally against a kubectl context

If the operator has port-forwards or LB IPs to the two `-rw` Services and a current kubectl context, the binary can be run from a workstation with `psql` + `kubectl` available on PATH:

```bash
export D31_PRIMARY_PASSWORD=$(kubectl -n tenant-1 get secret wp-db-primary-app -o jsonpath='{.data.password}' | base64 -d)
export D31_REPLICA_PASSWORD="${D31_PRIMARY_PASSWORD}"

GOTOOLCHAIN=go1.23.0 go run ./cmd/d31-acceptance \
    --namespace=tenant-1 \
    --primary-cluster=wp-db-primary \
    --replica-cluster=wp-db-replica \
    --primary-host=127.0.0.1 --primary-port=15432 --primary-sslmode=disable \
    --replica-host=127.0.0.1 --replica-port=15433 --replica-sslmode=disable
```

## Exit codes

| code | meaning |
|---|---|
| `0` | **PASS** — RTO bar met, count floor met, no gaps. D31 GREEN. |
| `1` | **FAIL** — gap detected OR floor missed (zero-tx-loss bar broken). stderr lists the failure mode. |
| `2` | **FAIL** — RTO exceeded; replica did not promote within `--rto-deadline`. stderr includes a `kubectl get cluster -o yaml` diagnostics dump. |
| `3` | **FAIL** — harness error before failover (bad flags, schema bootstrap failed, etc.). Re-run after fixing the env. |

## What "shipped" means

This PR ships the **code**. D31 itself flips to `🟢 VERIFIED-PASS` in `docs/TRUST.md` (openova-private) **only after**:

1. The operator runs this harness on a fresh 2-region Sovereign;
2. Exit code `0` is observed;
3. The PASS output + corresponding kubectl status snapshots are attached to `#2067`.

Per `CLAUDE.md §0` anti-theater rules: PR-merged ≠ shipped; operator-walk-with-screenshot ≠ optional. The Refs/Closes split in this PR's body reflects that — `Refs #2067`, NOT `Closes #2067`.

## Determinism notes (failure modes the harness CANNOT mask)

- **CNPG operator absent in either region**: the replica-promote patch returns 200 but `status.currentPrimary` never populates. Caught by the RTO deadline → exit 2.
- **Synchronous replication NOT actually configured** (i.e. someone overrode `replication.mode=async` for the test): gap-check will flag the lost-tail rows → exit 1. This is the correct outcome — the harness is the contract enforcer, not a debugger.
- **ClusterMesh DOWN**: the writer's INSERTs to the primary BLOCK on COMMIT (sync replication waits for the replica ACK that can't traverse a dead mesh). `--pre-kill-warmup` then expires with very few ACK'd rows; the post-promote count check still passes (floor=very-low), but the operator sees the low row count and knows the mesh was broken. Future enhancement: emit a `mesh_healthy` precheck.

## Dependencies — what must already be GREEN before this run

| dep | item | issue | state |
|---|---|---|---|
| 1 | bp-cnpg-pair sync replication shipped | #2064 | shipped ✓ |
| 2 | bp-continuum chart shipped | #2065 | shipped ✓ |
| 3 | Continuum CR rendering | #2066 | in-flight |
| 4 | cnpg-pair install path on fresh prov | #2068 | in-flight |

The harness itself does NOT depend on Continuum being installed — phase 4 patches `replica.enabled=false` directly. Continuum's value is **automating** that flip on a region-kill signal; the harness uses the manual seam so the test is self-contained.
