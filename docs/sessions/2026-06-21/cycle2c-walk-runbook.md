# Cycle-2c walk runbook — hw177 (`068f217746ca7779`, hw177.omani.works)

**Purpose:** the moment hw177 converges past storage (watcher `b53aqb35j` reports `evs-ssd`),
run THIS to flip the 35 walk-gated ❌ rows + validate the cycle-1 wave **zero-touch on the
durable console** — the only acceptance that counts (a faked ✅ off an agent report or a temp
roll is the founder's explicit rebuke). Built from the complete 48-❌ enumeration (2026-06-21).

## Pre-walk health-gate (abort the walk if any fail — a transitional env yields only noise)
1. `evs-ssd` StorageClass present both regions + EVS-CSI driver pods Running (the #4031 proof).
2. `kubectl get pvc -A` → the 6 stateful PVCs (gitea/harbor/guacamole-pg-1, data-{dmz,mgmt,rtz}-vcluster-0) **Bound**.
3. region-a + region-b HRs climbing **past** 40/50 toward green; `bp-catalyst-platform` Ready.
4. `curl -sI https://console.hw177.omani.works/` → 200 (not 000).
Only when ALL pass: proceed. If transitional, wait — do not walk.

## Access (kom4dc SSH-blocked → via the mothership catalyst-api pod, ns catalyst)
- kubeconfigs: `/var/lib/catalyst/kubeconfigs/068f217746ca7779.yaml` (region-a) + `…-me-east-215-b-1.yaml` (region-b).
- Handover walk: mint RS256 JWT with `/var/lib/catalyst/handover-jwt-private.pem`, claims
  `{iss:https://console.openova.io, sub+email:emrah.baysal@openova.io, email_verified:true,
  aud:https://console.hw177.omani.works, sovereign_fqdn:hw177.omani.works,
  deployment_id:068f217746ca7779, role:sovereign-admin, jti:<uuid>, iat:<now>, exp:<now+300>}`
  → Playwright `…/auth/handover?token=<jwt>` (NO curl-precheck — one-time JTI; session ~5min, re-mint per burst).

## STEP A — batch-merge the held queue FIRST (now safe: roll-after-ready, not mid-prov)
Sequence to ride the deploy-bot treadmill (let each settle): #4028 (gateway) → #4021 (UI) →
#4029 (UI) → #4011 (bp-chepherd chart, last). All MERGEABLE+CLEAN as of pre-walk. After the
mothership rolls + settles, these flip: R92 (redeem-limit), the placement per-app tab, the
Continuum DR page, and put bp-chepherd in the catalog for the e2e journey.

## STEP B — walk the 35 rows (per-row pass criterion = the live screenshot, saved to evidence/)
| Rows | Surface | PASS = |
|---|---|---|
| SSO (cycle-1) | `/dashboard` via handover | signed-in, no login UI |
| storage R209,210 | live cluster | evs-ssd present, local-path absent, PVCs Bound |
| cloud R203,204 | `/cloud` | Region 2/2, WorkerNode N/N healthy, **LoadBalancer shows the real ELB** (#4019), not 0 |
| topology R50-71 | `/cloud` + `/app/<x>/topology` + the ContinuumPage (#4029) | 2-target active-active for multi-region apps; DR StatusPanel shows lease/lag/phase from the live 2-region pair |
| funnel R86-95 | marketplace → redeem coupon → Org provisions → per-Org console | Org ACTIVE, zero-click owner landing, WordPress serves; R92 = >5 rapid redeems → 429 |
| model R5-23 | `/organizations` + `/organizations/<slug>` | the funnel-driven Org renders KIND=customer, TIER (not sme), ISOLATION=vcluster, ACTIVE |
| orgs R120 | `/organizations/<slug>` | labels Slug/Kind/Tier/Billing/Isolation, no tenant/SME |
| sso R32 · placement R111 · meta R181 | `/harbor` | signed-in `/harbor/projects`, no login (Harbor up once its PVC Bound) |
| tenant-strings (cycle-1) | console sweep | no user-visible "tenant" |
| jobs-finite | `/jobs` | finite typed rows, no continuous reconcilers |
| recon | `/cloud?view=graph&lens=reconciliation` | the recon lens (note: R190-192 land via `fix/3916`/`fix/3996`) |

## STEP C — record (commit-per-file IMMEDIATELY as hatiyildiz; a linter reverts uncommitted ledger edits)
Re-stamp `docs/ledger/UAT.md` rows to hw177 with PASS/FAIL + the evidence path
`docs/sessions/2026-06-21/walk-hw177/evidence/`. A row flips ✅ ONLY with a pasted live screenshot.

## Remaining after the walk (NOT walk-flippable)
- adoption R206 — Crossplane-XRC Path B (#4006) — founder A-vs-B steer.
- e2e R216-223 — bp-chepherd (#4011, merging in STEP A) + openova-mcp (on main) + agent↔MCP wiring;
  walkable once a customer Org exists (post-funnel) and chepherd installs into it.
- R186 (meta) — auto-satisfied as the evidence cells above are populated.
