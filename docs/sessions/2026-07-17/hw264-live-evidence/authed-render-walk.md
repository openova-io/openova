# hw264 — authed console render walk (2026-07-17)

**Env:** `hw264.omani.works` · dep `4585c8a9f92d4e8e` · region-a me-east-215-a
63/63 HR ready (region-b absent — post-region-kill / standby-absent single-region
survivor). **cutoverComplete=true** (re-verified live, see below).

**Auth:** owner session via the mothership handover flow — `GET
/sovereign/api/v1/deployments/{id}` → fresh single-use RS256 handover token
(`sub=emrah.baysal@openova.io`, `role=sovereign-admin`) → console
`/auth/handover` set the session cookie → landed `/dashboard` signed in. No
login form, no password entry. Browser-driven (Playwright), **no cluster
mutation, no NodePort, bastion untouched.** Screenshots in this directory
(`hw264-01..06-*.png`).

## Live re-verification (independent of the summary)

`self-sovereign-cutover-status` ConfigMap (catalyst ns), read live:
- `cutoverComplete: true` · `progressPercent: 100` · `currentStepIndex: 11`
- `cutoverFinishedAt: 2026-07-17T07:14:04Z` · `failedStep: (empty)` · `lastError: (empty)`
- all 11 steps `result: success`, incl. `egress-block-test` (the 10-min deny-egress hold)
  and `crossplane-provider-pivot`.
- `registriesYamlActive: v2` + every node `registriesYaml: v2` (registry pivot applied).

→ **Pillar 5 (sovereign cutover) genuinely proven on hw264**, confirmed from the
cluster's own status, not a prior report. (G11 already stamped ✅.)

## Surface-by-surface (authed render)

| # | Surface | Rendered (real data) | Verdict |
|---|---|---|---|
| 1 | **Dashboard** `/dashboard` | Full sidebar nav; identity chip **EB · emrah.baysal@openova.io · hw264.omani.works**; header "197 items" + Decommission link; **FleetTreemap** real cells (Done/Install/Task/Step/Reconcile/Cron/Lifecycle/Pending) under Progress→Kind layers + status legend. Sovereign status **Degraded** (honest — region-b absent post-kill). | ✅ signed-in, no login form |
| 2 | **Cloud** `/cloud?view=graph` | Architecture graph canvas renders under authed session; layer/size/color controls present. | ✅ renders |
| 3 | **Catalog** `/catalog` | **93 Blueprint cards** in tile grid — Agenity (AI-RUNTIME), Alloy (INSIGHTS), Axon (CORTEX), BGE Embeddings, catalyst-platform (PLATFORM), Cert-Manager (GUARDIAN)… each icon+summary+tier badge (FREE/BOOTSTRAP) + Edit/PENDING. | ✅ grid + Alloy card visible |
| 4 | **Jobs** `/jobs` | "Live state stream re-attached. Refreshing from the catalyst-api every 5s"; STATUS/**KIND (cron·install·lifecycle·step·task)**/APP filters; ~170 activity rows incl. every `cutover-*` job (egress-block-test, gitea-mirror, harbor-prewarm, crossplane-provider-pivot…) — corroborates cutoverComplete. | ✅ populated live list |
| 5 | **Organizations** `/organizations` | Canon copy: "each customer is one Organization with its own identity, RBAC, and cost attribution" — **no "tenant"**. Commerce tabs (Plans/Add-ons/Bundles/Industries/Apps/Billing/Vouchers/Domains). **Showback** real: `__platform__` 20451.91 units / 20055m CPU / 40.85 GiB mem / 934 GiB storage + per-app attribution table (cnpg-pair-…). | ✅ heading "Organizations", showback live |
| 6 | **Apps** `/apps` | Environment dev/staging/prod selector; real install states — Alloy/catalyst-platform/Cert-Manager/Cilium/CloudNative PG **INSTALLED**, cluster-autoscaler PENDING; page title → **"✓ Sovereign ready — hw264.omani.works"**. | ✅ real install states |

## Honestly NOT drilled this session (left for a deeper walk)

- Catalog **Alloy detail page** (row 125) — grid confirmed, detail hero not clicked.
- Jobs **per-row Kind column** (row 169) — KIND *filter* with all 5 kinds confirmed;
  per-row column header not separately read.
- Orgs **card column headers** (row 117) — directory + showback confirmed; column
  header labels not separately read.
- No customer Org onboarded on this fresh env (funnel not walked) → per-Org sample rows N/A here.

## §854

See `854-nodeport-audit.md` — hw264 sovereign has **0 NodePort services**;
Kyverno `24-forbid-nodeport-service` upheld. Platform is §854-clean.

**No fabrication. Every ✅ above corresponds to a rendered surface I observed
authenticated on hw264 with a screenshot.**
