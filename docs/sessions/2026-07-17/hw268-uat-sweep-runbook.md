# hw268 UAT sweep runbook — turnkey on convergence

**Env**: hw268, dep `7e7a0a1f42b2fdf4`, `hw268.omantel.biz`, 2-region kom4dc.
**Train**: openova-mcp 0.1.4 (#5175) + cnpg-pair 0.2.14 (#5178) + main HEAD.
**Gate**: fires the instant monitor `b5tvhwvic` reports `status=ready`. Owner
JWT: `bash /tmp/kstone-mint.sh`. Handover→session: `GET /sovereign/api/v1/deployments/7e7a0a1f42b2fdf4` → `.handoverURL`.

Each stage below lists: the live command, the pass-evidence, and the exact
UAT.md rows it stamps. Reset ledger is "pending hw268" — every ✅ carries
env=hw268 + date + evidence path.

## Stage 0 — convergence verify (on `ready`)
- `kubectl --kubeconfig /tmp/hw268.kubeconfig get nodes` → 2×(cp+5w) Ready.
- ClusterMesh 1/1, Flux HRs green, `catalyst get applications -A` non-zero.
- Stamps: the substrate/robustness rows (G-series).

## Stage 1 — auto-cutover verify → `cutoverComplete=true`
- Watch `self-sovereign-cutover-status` CM: 11/11 steps, deny-egress hold green,
  `registriesYamlActive=v2`.
- Stamps: Pillar-5 sovereignty rows (cutover, registry-pivot, egress-block).

## Stage 2 — funnel E2E (validates #5104 + #5123) — the marquee ✅ pair
- Owner BSS → mint voucher → redeem → S-plan WordPress → PIN login → credit pay
  → per-Org console.
- **#5104 PASS**: `kubectl -n <orgns> get deploy,pods` → wordpress+mysql 1/1;
  delivery kustomization Ready; app HTTP 200. (mirrors the hw266 walk in
  `docs/sessions/2026-07-17/hw266-funnel-e2e/`.)
- **#5123 PASS**: `GET https://console.<org>.omani.homes/api/v1/org/applications`
  (org session + X-Tenant-Host) → 200, WordPress card INSTALLED.
- Stamps: funnel-E2E rows + #5104/#5123 status/uat → close.

## Stage 3 — MCP north-star (validates #5175 + #5167)
- Owner PIN session → real session token → MCP `list_applications` returns the
  Org's apps (the #5175 whoami-fallback resolves the session token that #5167's
  rs256 path could not). tools/list RBAC-scoped.
- Stamps: rows 211-213 (MCP) + #5175/#5167 status/uat.

## Stage 4 — region-kill G12 (validates #5178 + #5137 + #5157) — the blocked proof
- Reuse `docs/sessions/2026-07-17/hw266-region-kill-G12/` procedure BUT confirm
  the **fixed** dr-promoter (0.2.14): pre-kill region-b `pg_stat_wal_receiver`
  status=streaming (no flap), timelines aligned (both TL1).
- Seed d31_counter 1..1000 → kill region-a (cordon+force-delete primary) → the
  #5178 liveness gate must NOT false-promote; a genuine region-A-down triggers
  the durable suspend-latch promote → region-b RPO=0 (1000|1000) + writable.
- openbao region-b stays `Sealed=false` (#5157); keycloak realm preserved.
- Stamps: Pillar-3 region-kill rows (D31, G12) + #5178/#5137/#5157 status/uat.

## Stage 5 — render-row sweep (~180 rows)
- Authed console walk: Dashboard treemap, Organizations directory, Cloud view,
  Jobs (typed Install rows #5019), Topology/DR, Settings, Compliance.
- Stamps: the render-row block; flag any NEW regression as a finding.

## Consolidation
- `python3 scripts/uat-drift-guard.py --env hw268` then commit UAT.md with the
  per-row env+date+evidence. Close status/uat issues whose rows flipped ✅ with
  the evidence comment (agent owns the close per PROTOCOL).

**Held status/uat issues this sweep closes on ✅**: #5104 #5123 #5175 #5167
#5178 #5137 #5099 #5042 #5019 (each gated on the live hw268 walk above).
