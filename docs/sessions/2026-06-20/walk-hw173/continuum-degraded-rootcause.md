# Continuum Degraded / no-lease — root cause (hw173, depID 7bb723da8da06047)

**Verdict: the Continuum is Degraded NOT because the data path is unhealthy, but because the lease witness backend (`dns-quorum`) is an unfinished Phase-1 POC — its write side is a hardcoded `nil`-writer stub and its read side points at resolvers that don't host the lease records. The lease can therefore NEVER be acquired, so the controller patches `phase=Degraded` forever.** This is a feature-completeness gap (K-Cont-4/5 never shipped), env-independent — it will reproduce on every active-hot-standby prov.

## Live evidence (mothership-kubectl, region-a kubeconfig)

- Continuum `cnpg-pair/cnpg-pair-bp-cnpg-pair-continuum`:
  - `spec.leaseClient.kind = dns-quorum`, `spec.leaseClient.config.resolvers = [10.43.0.10, 10.43.0.11, 10.43.0.12]`, `ttlSeconds=30`, `renewSeconds=10`.
  - `status.phase = Degraded`; conditions `LeaseHeld=False (reason Reconciled, message "")` + `Ready=False (reason Reconciled, message "")`; `replicationLagSeconds=0`; no `leaseHolder`. (Empty condition messages — that's why the prior walk saw "empty condition messages".)
- Controller pod `catalyst-system/continuum-controller-7c6bcd5bb5-np2kt` (region-a), **1/1 Running**, logs every ~10s:
  ```
  witness read-quorum unavailable — check leaseClient resolvers/wiring
  err="witness: read quorum unavailable (backends unreachable or misconfigured)"
  ```
- Meanwhile the DATA PATH is healthy: `cnpg-pair-bp-cnpg-pair-primary` 3/3 (region-a), `cnpg-pair-bp-cnpg-pair-replica` 3/3 (region-b), replica `spec.replica.enabled=true source=…-primary`, streaming over `…-primary-mesh:5432`. So the Degraded status is purely a **lease/witness** failure, not a replication failure.

## The exact code chain (file:line)

1. **Witness `kind` defaulted to `dns-quorum`**
   `platform/cnpg-pair/chart/values.yaml:289-293` → `leaseClient.kind: dns-quorum`, `resolvers: [...]` (the live values, with the three `10.43.0.x` cluster-DNS resolvers). Templated into the CR at `platform/cnpg-pair/chart/templates/continuum.yaml:63-73` (note: the live Continuum is rendered by **bp-cnpg-pair**, NOT `products/continuum/`; the cnpg-pair-side template at `platform/openbao/chart/templates/continuum.yaml:116` carries the same `default "dns-quorum"`).

2. **Read quorum can never succeed against these resolvers**
   `core/controllers/continuum/internal/witness/dnsquorum/client.go:367-424` `readQuorum()` does a parallel `LookupTXT("<slot>.lease.openova.io")` against each resolver via the std-lib reader (`ReadTXT`, line 592). The resolvers `10.43.0.10/11/12` are **kube-dns ClusterIPs**, which are NOT authoritative for `lease.openova.io` and hold no lease TXT record. Every lookup returns NXDOMAIN/no-data → tally of the real lease value is `0` → `best < quorum` (line 416) → returns `ok=false`.

3. **`Acquire` surfaces `ErrQuorumUnavailable`**
   `client.go:266-273`: when `readQuorum` returns `!ok`, `Acquire` returns `witness.ErrQuorumUnavailable` (defined `core/controllers/continuum/internal/witness/witness.go:53`). It NEVER reaches the write path.

4. **Even if read succeeded, the WRITE path is a `nil`-writer stub**
   `client.go:163-214` `factory()` constructs the client with `New(servers, tsigKey, domain, slot, nil, nil)` — **Writer is explicitly `nil`** (comment lines 208-213: *"Writer is nil here — production wiring injects a real PowerDNS-API writer separately… DNS-quorum requires PDM /v1/txt endpoint which is K-Cont-{4|5}"*). `writeQuorum()` (`client.go:433-435`) returns `"dnsquorum: Writer not configured (Phase-1 POC needs PDM /v1/txt — K-Cont-{4|5})"`. **There is NO production injection point for the dns-quorum TXTWriter** — `core/controllers/continuum/cmd/main.go:95-96` blank-imports the dnsquorum factory but never wires a writer; the comment at `main.go:15` confirms "K-Cont-4 ships the [writer]." K-Cont-4 was never shipped.

5. **Controller logs the wiring problem and patches Degraded**
   `core/controllers/continuum/internal/controller/continuum_controller.go:299-318`: on `Renew → ErrLeaseLost → Acquire → ErrQuorumUnavailable`, it logs (line 311-312) the "check leaseClient resolvers/wiring" line (#3195/hw130 lineage — distinct sentinel, same no-promote safety) and calls `patchStatusFromCR(ctx, cr, spec, witness.State{}, …)` with an **empty lease state** (line 316). On the very first loop `runPerCR` (line 251) `Acquire` also fails, so the lease state is empty from the start.
   `patchStatusFromCR` (`continuum_controller.go:695-723`): `heldByUs := lease.IsHeldBy(r.HoldingRegion, now)` on an empty `witness.State` → `false` → `phase = PhaseDegraded` (line 707-709). `LeaseHolder` = `lease.Holder` = `""`, `ReplicationLagSeconds` = `MaxLagSeconds(...)` = `0`. → exactly the observed status.

## Why empty condition messages

The Degraded patch carries no `message` because the controller treats quorum-unavailable as a steady-state (no-promote) condition, not a hard failure — `patchStatus` writes `LeaseHeld=False`/`Ready=False` with `reason=Reconciled` and no message. The diagnostic detail lives ONLY in the controller log line, not in the CR conditions. (Secondary observability gap.)

## Is this an hw173 bug? No — it reproduces on every active-hot-standby prov

The `dns-quorum` witness is the **default** (`values.yaml:290`), the resolvers are cluster-DNS IPs on every prov, and the writer is a compile-time `nil`. So any 2-region active-hot-standby Sovereign produces a Degraded Continuum the moment the controller starts, regardless of region-b health. The healthy replica + healthy primary on hw173 prove the *data* path is fine; only the *witness/lease* feature is unfinished.

## Available witness backends (the fix surface)

`core/controllers/continuum/internal/witness/`:
- **`inmemory.go`** — process-local map, no durability/no network. `DefaultSelector` gates it behind `WITNESS_IN_MEMORY=false` (`main.go:154`) — TEST ONLY, must not be used in prod (a controller restart loses the lease; a 2-region split can't coordinate).
- **`cloudflarekv/`** — a REAL client (HTTP→Cloudflare Workers KV), but the **Worker itself** is the unshipped K-Cont-4 deliverable (`cloudflarekv/client.go:2-37`), and it needs a CF API token + Worker URL in a Secret. Not wired on hw173.
- **`dnsquorum/`** — the configured one; client read path works against a REAL authoritative TXT server, but (a) the live resolvers aren't authoritative for the lease zone and (b) the writer is `nil` (needs PDM `/v1/txt`, the unshipped K-Cont-5).

## Recommended fix (NOT applied here — needs a feature slice, not a one-liner)

This is **not** a contained config bug — making the Continuum reach Ready requires completing a witness backend end-to-end. The minimal real fix is one of:

1. **Finish dns-quorum (K-Cont-5)**: add the PDM `/v1/txt` write endpoint, inject an `HTTPTXTWriter` at `main.go` (the `factory()` at `client.go:208` must accept a wired writer), and point `leaseClient.config.resolvers` at the Sovereign's **PowerDNS authoritative** servers (not kube-dns) so reads see the records the writer creates. This keeps the lease in-cluster (no external dependency) — matches the `values.yaml:285` "in-cluster fallback" intent.
2. **Finish cloudflare-kv (K-Cont-4)**: deploy the Worker, seed the CF API token Secret in `catalyst-controllers`, set `leaseClient.kind=cloudflare-kv` + the Worker URL. External dependency (breaks the post-cutover sovereignty contract — Principle #11), so (1) is preferred for sovereign DR.

Either is a scoped feature slice with its own UAT (lease acquire → Continuum Ready → armed Switchover → region-kill), tracked separately. A `fix/continuum-lease-degraded` branch that only re-points resolvers would NOT help, because the writer is still `nil` — the lease can be read but never written, so it can never be acquired. **No partial patch is honest here; this needs the witness-completion slice.**

## What this unblocks once fixed

UAT rows 55, 57, 62, 64, 71 (the "live Continuum Ready / armed Switchover / measured lag / region-a lease" rows) are ALL gated on this single witness-completion. The region-b cluster + replica are already live (see `topology-rewalk.md` — 9 rows already flip to ✅), so the Continuum lease is the last gate between "2-region pair exists" and "DR is armed and walkable."
