# Pre-flight validation — hw165 (deployment `9ee307969c92452e`)

> Founder gate: *"before executing any environment run, perform a complete
> pre-flight validation and produce a written validation checklist."* This doc
> records **(A)** what was validated BEFORE firing hw165, **(B)** the walk plan +
> per-group status the fresh-prov walk fills, and **(C)** live convergence. The
> canonical 243-row matrix is [`docs/ledger/UAT.md`](ledger/UAT.md); the
> per-ticket browser runbooks are [`docs/ledger/uat-walkthrough/`](ledger/uat-walkthrough/).
> Stamped 2026-06-18.

## A. Fire gate — validated BEFORE firing hw165

The keystone precondition was the #3642 convergence gate (every post-#3642 prov
wedged ~42/64 HRs). Root-caused live on hw164 + fixed in **#3816**
(`bp-mgmt-vcluster 0.2.16`): the `replicateServices.toHost {from,to}` form
crash-loops the vcluster 0.23 syncer at runtime, so the whole spine wedged.

| # | Pre-flight check | How validated | Result |
|---|---|---|:--:|
| 1 | `replicateServices.toHost` removed (the syncer crash) | `git show origin/main:…/values.yaml` → `toHost: []` | ✅ |
| 2 | ExternalName aliases render (keycloak/gitea-http/openbao → mangled) | `helm template` → 3 Services, right ns + ports | ✅ |
| 3 | bp-mgmt-vcluster render.sh (publish gate) | `SOV_FQDN=… bash tests/render.sh` → 7/7 PASS | ✅ |
| 4 | Chart published to ghcr | `gh api …/bp-mgmt-vcluster/versions` → `0.2.16` present | ✅ |
| 5 | Slot-58 pin on origin/main | `git show origin/main:…/58-…yaml` → `version: 0.2.16` | ✅ |
| 6 | CI guards | lockstep ✓ · bootstrap-kit drift ✓ · blueprint schema ✓ · dependency-graph-audit ✓ | ✅ |
| 7 | VPC quota freed (hw164 wiped) | `/deployments/<hw164>` → `deployment not found` | ✅ |
| 8 | Mothership stable (no roll mid-fire) | catalyst-api pod age 167m (>5min); bp-* merge ⇒ no catalyst-api roll | ✅ |
| 9 | Handover key present | `/tmp/hw165-handover-priv.pem` (mothership key); mint → valid URL | ✅ |
| 10 | Pool DNS resolves | `console/grafana/gitea/…hw165.omani.works` → gateway EIP | ✅ |
| 11 | UAT ledger reset | `reset-uat.py hw165` → header stamped, stale evidence cleared | ✅ |

Fire: `POST /deployments` → `{"id":"9ee307969c92452e","status":"provisioning"}`.

## B. Walk plan — the 10 runbooks (fills the 243-row matrix)

Each is a browser walk (bare-URL → land-signed-in, screenshot evidence). Legend:
**⛔ ENV-DEP** = awaiting hw165 convergence (current state of ALL rows); flips to
✅/❌/GAP/☐ per-row during the walk.

| # | Runbook (ticket) | Browser surfaces (inputs) | Depends on (must serve) | Expected output | Status |
|---|---|---|---|---|:--:|
| 1 | object-model **#3687** | dashboard · apps grid · org/treemap | console (catalyst-api/ui) | PIN-less land on `/dashboard`; Orgs/Apps/Catalog render | ⛔ ENV-DEP |
| 2 | sso-zero-login **#3374** | console + grafana/gitea/harbor/bao/keycloak/guacamole/newapi/pdns | console + 9 apps + keycloak SSO | each bare-URL lands signed-in admin, no login form | ⛔ ENV-DEP |
| 3 | topology-dr **#3375** | cloud 2-region map · topology · region-kill | console + continuum + 2 regions | one vocabulary; mesh/cnpg-pair; region-kill promotes | ⛔ ENV-DEP |
| 4 | funnel-voucher **#3376** | marketplace · redeem · checkout · running app | console + marketplace + org-provision | voucher → due-zero checkout → running app | ⛔ ENV-DEP |
| 5 | ns1-migrate **#3642** | placement / vCluster treemap | console + 7 in-vCluster apps | 7 named apps resident in mgmt vCluster | ⛔ ENV-DEP |
| 6 | organizations **#3383** | console Organizations surfaces | console | no `sme`/`tenant` wording anywhere in UI | ⛔ ENV-DEP |
| 7 | catalog-edit **#3668** | catalog blueprint editor · IaC YAML editor | console + gitea | edit → CR moves (single-source IaC) | ⛔ ENV-DEP |
| 8 | cutover **#3379** | cutover console surfaces · handover health | console + cutover Jobs | durable cutoverComplete + deny-egress hold | ⛔ ENV-DEP |
| 9 | jobs **#3646** | `/jobs` canvas · failed-job re-run | console | one honest canvas, no fabrication, retry works | ⛔ ENV-DEP |
| 10 | regenerate-discipline **#3581** | meta (governs the walk itself) | n/a | the walk re-stamps to the current env | meta |

**Dispatch on console-up:** 10 parallel walker agents (Opus), one per runbook,
using [`/tmp/WALK-HARNESS.md`] (handover → `shot.js` → Read PNG → ✅/❌/GAP
verdict), returning per-row results → aggregated into UAT.md → the
success/fail/gap matrix vs the hw159 baseline (119 ✅ / 61 ❌ / 56 GAP / 5 ☐).

## C. hw165 convergence — live

| Milestone | Expected | Status |
|---|---|:--:|
| Phase-1 kubeconfig PUT | ~12–15 min | watching |
| `mgmt-vcluster-0` Running (NOT CrashLoop — the #3816 fix) | the keystone proof | checkpoint @ ~18 min |
| keycloak DB secret materialises (`fromHost` delivery) | clean vcluster ⇒ no churn | pending |
| spine Ready (keycloak → gitea → catalyst-platform) | ~25–35 min | pending |
| console serves (the walk gate) | ~35–45 min | watcher armed (`bf9w15znz`) |
