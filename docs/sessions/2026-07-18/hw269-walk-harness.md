# hw269 walk harness — turnkey on convergence

**Env**: hw269, dep `a47b82b25fbd5baf`, `hw269.omani.works`, 2-region kom4dc.
**Train**: cutover 0.1.137 (#5191 toolchain fail-loud) + cnpg-pair 0.2.14 (#5178
dr-promoter liveness) + openova-mcp 0.1.4 (#5175 whoami-fallback) + main HEAD.
**Gate**: fires when monitor `b2v7bh1w5` reports `HW269-READY`.
**Order is LAW this time**: converge → cutover→cc=true FIRST → region-kill G12 → UAT sweep.

## Access (on ready)
- Kubeconfigs from the mothership PVC EARLY (avoid the region-a API-409 that caused
  the hw268 out-of-order detour): mothership `kubectl -n catalyst exec <catalyst-api-pod>
  -- cat /var/lib/catalyst/kubeconfigs/a47b82b25fbd5baf.yaml` (region-a) +
  `…a47b82b25fbd5baf-me-east-215-b-1.yaml` (region-b) → `/tmp/hw269-a.kubeconfig` + `-b`.
- Owner session: handover URL from `GET /sovereign/api/v1/deployments/a47b82b25fbd5baf`
  → `.handoverURL`; PIN-mint owner session for the authed console walk.

## Stage 0 — convergence verify (rows: substrate/robustness G-series)
- region-a 2×(cp+5w) + region-b Ready; ClusterMesh 1/1; `catalyst get applications -A` non-zero.
- §854: `kubectl get svc -A -o json | jq '[.items[]|select(.spec.type=="NodePort")]|length'` == 0 both regions → row 240.

## Stage 1 — auto-cutover verify → cutoverComplete=true (Pillar-5; VALIDATES #5191)
- Watch `self-sovereign-cutover-status` CM: 11/11 steps, `registriesYamlActive=v2`.
- **#5191 gate**: step-04 registry-pivot must reach N/N node acks with the fail-loud toolchain —
  if a node's `apk add curl jq` blips, the pod now CrashLoops+self-heals (never silent-wedge at 27%).
  Confirm all nodes ack `registriesYaml=v2` and the step completes (not the hw268 stuck-at-4/6).
- deny-egress 10-min hold green. Stamps: Pillar-5 sovereignty rows.

## Stage 2 — funnel E2E (VALIDATES #5104 + #5123) — marquee ✅ pair
- Owner BSS → voucher → redeem → S-plan WordPress → PIN → credit pay → per-Org console.
- **#5104 PASS**: `kubectl -n <orgns> get deploy,pods` wordpress+mysql 1/1; delivery kustomization Ready; app 200.
- **#5123-A PASS**: `GET https://console.<org>.omani.homes/api/v1/org/applications` (org session + X-Tenant-Host) → 200, WordPress card INSTALLED (projection registers the purchase).
- **#5123-B check**: on the member-session page loads, capture whether the console still fires owner-scoped
  `/deployments/{id}` + `/console-ui/sidebar-entries` → 403. If yes → that's the frontend guard to ship
  (now with a live member+owner session to validate). Stamps: funnel rows + close #5104/#5123.

## Stage 3 — MCP north-star (VALIDATES #5175 + #5167)
- Owner PIN session → real session token → MCP `list_applications` returns the Org's apps
  (the #5175 whoami-fallback resolves the session token #5167's rs256 path could not). tools/list RBAC-scoped.
- Stamps: rows 211-213 (MCP) + close #5175/#5167.

## Stage 4 — region-kill G12 (VALIDATES #5178 + #5137 + #5157) — AFTER cc=true
- Pre-kill: region-b `pg_stat_wal_receiver` streaming, timelines aligned (no flap — 0.2.14 fixed).
- Seed d31 counter 1..1000 → kill region-a (Huawei ECS os-stop the 6 me-east-215-a instances,
  bastion + region-b excluded) → #5178 liveness gate promotes ONLY on real region-a death
  (`pg_isready rc=2`) + durable suspend-latch → region-b RPO=0 (1000|1000) writable, NO flap.
- openbao region-b Sealed=false (#5157); keycloak realm preserved. Stamps: Pillar-3 rows + close #5178/#5137.

## Stage 5 — render sweep (~180 rows)
- Authed console: Dashboard treemap, Organizations, Cloud view, Jobs (typed Install #5019),
  Topology/DR, Settings, Compliance. Stamps: render block; flag any NEW regression.

## Consolidation
- Stamp UAT.md with per-row env=hw269 + date + evidence path (screenshots under
  `docs/sessions/2026-07-18/hw269-*/`). Split cells on `|`; cells[5]=Walk, cells[6]=Result, prepend cells[7] Evidence.
- Close status/uat issues whose rows flip ✅ with the evidence comment (agent owns the close).

**status/uat issues this sweep closes on ✅**: #5104 #5123 #5175 #5167 #5178 #5137 #5099 #5042 #5019
(each gated on the live walk above). #5191/#5193 validate structurally (cutover completes / wipe recovers).
