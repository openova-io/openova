# hw261 Region-Kill G12 (Pillar-3) — zero-tx-loss failover PASS

**Env:** hw261.omani.works, dep `fe9eb3995b594b79`, 2-region Huawei kom4dc (me-east-215-a/b), post-`cutoverComplete=true` (2nd zero-touch keystone on the complete 0.2.11 train).
**Date:** 2026-07-16.

## Sequence (RUNBOOKS §6.1)
1. **Sentinel write** — `hw261-regionkill-1784190648` inserted into the region-a `cnpg-pair` PRIMARY with `synchronous_commit=on` (sync standby = region-b). Verified present on the region-b sync replica *before* the kill (baseline).
2. **Real region kill** — Huawei ECS `os-stop` HARD of ALL 4 region-a servers (cp1 + 3 workers) → `status=SHUTOFF`; region-a apiserver UNREACHABLE. Bastion `212.72.24.20` (`ba2097b9-601`) hard-guarded and untouched.
3. **failover-readiness 1/1** — the `cnpg-pair-failover-readiness` probe reported the region-b replica **promotable** (this is the #5134 fix validated live; the pre-0.2.11 probe was stuck 0/1 on a false `lag=999999`).
4. **Promotion** — operator failover: `spec.replica.enabled=false` on the region-b replica cluster → `pg_is_in_recovery=false` WRITABLE primary; post-failover write (id 34) succeeded.
5. **ZERO-TX-LOSS PASS** — the pre-kill sentinel survived on the promoted region-b primary.
6. **Recovery** — region-a `os-start` issued (HTTP 200).

## Finding
Promotion was **operator-triggered, not automatic** — the Continuum controller + `dr` CR are single-region on region-a and died with the killed region → **#5137** (P2). Zero-tx-loss (DoD Pillar 3) holds via the operator path per RUNBOOKS §6.1 step 3.
