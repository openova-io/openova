# Walk hw173 — cutover (159-166) · jobs (167-177) · meta (178-186)

Env: **hw173** (depID `7bb723da8da06047`), `omantel` Org, region hw-me-east. Walk 2026-06-20.
Env state at walk: 62/65 HR Ready (3 non-ready = peripheral cluster-autoscaler/ccm/velero), console 200,
reconciliation 61/64, `operationInProgress:false`. Cutover NOT initiated on this env (clean prov).

Access: handover JWT → console cookie + Playwright DOM render + in-cluster kubectl via mothership
`catalyst-api` pod (`/var/lib/catalyst/kubeconfigs/7bb723da8da06047.yaml`).

## Summary: 21 ✅ / 1 ❌ / 6 ⚠️

| Row | Verdict | Evidence (HTTP/JSON/kubectl/DOM) | Note |
|-----|---------|----------------------------------|------|
| 159 | ✅ | `/settings` DOM: `[data-testid=sovereignty-card]` present; heading "Cluster sovereignty"; badge `[data-testid=sovereignty-badge]`="Tethered"; CTA `<button>Achieve True Sovereignty`; copy "Runs the 11[-step]… egress-block self-test". | TETHERED badge honest (cutover not run) |
| 160 | ✅ | `/settings` sidebar `<a href="#sovereignty">Sovereignty</a>`; `document.getElementById('sovereignty')` resolves → scrolls to the Cluster-sovereignty panel. | dedicated first-class anchor |
| 161 | ✅ | `/jobs` DOM: 84 rows render; "Live state stream re-attached. Refreshing from the catalyst-api every 5s." No spinner/empty/login. | zero-login (handover→/dashboard, no pw field) |
| 162 | ✅ | `/jobs` DOM: 11 `step`-kind rows under parentId `…:cutover` — Catalyst Api Env Patch, Crossplane Provider Pivot, Egress Block Test, Flux Gitrepository Patch, Gitea Mirror, Gitea Token Mint, Harbor Prewarm, Harbor Projects, Helmrepository Patches, Registry Pivot, Vcluster Registry Pivot. | full 11-step execution tree |
| 163 | ✅ | `/api/v1/.../jobs`: every `cutover-step-*` status=`pending` (cutover not started) — honest, no premature green. | |
| 164 | ✅ | jobs JSON: `cutover` group kind=group status=`pending` — reflects its all-pending children, not a fake Succeeded. | |
| 165 | ⚠️ | Bundle has `jobs-retry-btn` gated (server returns 409 "Not retryable" off-Failed); but NO `cutover-step-*` is Failed on this clean env, so no Re-run button can be exercised on a cutover step. | gate present, no failed step to demonstrate |
| 166 | ⚠️ | Cutover NOT initiated on hw173: `kubectl get jobs -A \| grep cutover` → none; no SovereignCutover CR; no `cutoverComplete` field in deployment record; all 11 steps `pending`. | cutover not initiated on this env (not ❌) |
| 167 | ✅ | handover URL → lands `/dashboard` signed-in; `input[type=password]`=none; avatar "E"; env "hw173.omani.works READY". | silent owner front door |
| 168 | ✅ | `/jobs` renders 84-row populated table (same evidence as 161). | |
| 169 | ✅ | `/jobs` header = **Name · Kind · App · Deps · Parent · Status · Runs · Started · Duration · Actions** — Kind column present, each row carries its kind. | |
| 170 | ⚠️ | No `install-openbao` host job row exists (`'install-openbao' in jobNames`==False). OpenBao moved into mgmt-vCluster (#3642): `kubectl get hr -n mgmt bp-openbao` → True "Helm install succeeded"; `openbao-0-x-openbao-x-mgmt-vcluster` 1/1 Running. | install green, but relocated to vCluster so no host install-openbao row to scroll to |
| 171 | ✅ | 49 `install`-kind rows each map to a real appId (velero, cnpg-pair, postgres-shared{,-b,-c}, kyverno-policies, sso-bridge, external-secrets, flux, cilium…). No synthetic/placeholder entries. | |
| 172 | ✅ | `/jobs` Status filter set to `failed` → table shows "No jobs match the current filters" (0 genuinely-failed rows on a 62/65-Ready env). Filter works; jobs JSON confirms 0 failed/failing/degraded. | honest empty (clean env) |
| 173 | ✅ | `/jobs` UI: "Live state stream re-attached. Refreshing from the catalyst-api every 5s"; reconciliation API `watching:true`, 61/64; 4 `running` + 13 `pending` rows poll-update in place. | live-tail mechanism active; most rows already terminal on converged env |
| 174 | ⚠️ | Bundle has `jobs-retry-btn`/`data-testid=jobs-retry-*`/`Requesting…`; but 0 Failed rows exist → no Failed row to show the per-row Re-run on. | control present in code, no failed row to exercise |
| 175 | ✅ | `/jobs` DOM: 0 retry buttons across all 84 succeeded/pending/running/healthy rows — control gated off non-Failed rows. | gating verified (negative) |
| 176 | ⚠️ | Retry POST → success-toast/`Requesting…` flow exists in bundle; cannot click because no Failed row exists on this clean env. | needs a Failed row to exercise click→toast |
| 177 | ✅ | Single retry mechanism: one `jobs-retry-btn` component (`data-kind` attr), server `POST …/jobs/{id}/retry` (403 RBAC / 409 not-retryable) — no per-kind UI. | single-table single-control shape |
| 178 | ✅ | Signed handover URL → `location.pathname=/dashboard`, env switcher "hw173.omani.works", avatar "E", `input[type=password]`=none, no login form. | |
| 179 | ✅ | Click avatar "E" → menu: "Signed in as **emrah.baysal@openova.io**" + "Sign out". | owner-admin identity; live shows concrete owner email (doc says "the owner") |
| 180 | ✅ | `grafana.hw173/` → `?orgId=1`, title "Home - Dashboards - Grafana", body "Welcome to Grafana", no login form. | SSO landed signed-in |
| 181 | ❌ | `harbor.hw173/` and `/harbor/projects` → **404 all paths**. Root: `harbor-core` `CreateContainerConfigError` (missing `harbor-core-x-harbor-x-mgmt-vcluster` ConfigMap, Optional:false) + `harbor-jobservice` CrashLoopBackOff (65 restarts) in mgmt-vCluster. HR reports "install succeeded" but app is down. | real env-independent app defect (not a peripheral HR) |
| 182 | ✅ | `gitea.hw173/` → page title "emrah.baysal - Dashboard - Catalyst Gitea", logged in, URL on :443. | |
| 183 | ✅ | `bao.hw173/` → final `/ui/vault/secrets` (NOT `/ui/vault/auth`); Secrets Engines list shows `cubbyhole/` + `secret/`; no token form. | authenticated Vault session |
| 184 | ✅ | UAT.md: 52 hw173 refs; only hw150/hw144/hw128 occurrences are the self-referential forbidden-list **inside row 184's own description text** — zero stale Walk-link hosts. | self-reference only |
| 185 | ✅ | No stale predecessor host strings outside row 184's own text (grep hw150/hw144/hw128/hw158/hw16x → only the row-184 literal). | |
| 186 | ❌ | All 60 Evidence cells link `../sessions/2026-06-19/evidence/walk/...`; files exist BUT `git log` shows that dir was authored for **hw167** ("hw167 live walk COMPLETE", #3866), not hw173. `docs/sessions/2026-06-20/evidence/walk` does not exist. Evidence is predecessor-env (hw167) — this walk's job is to re-stamp. | stale hw167 evidence; needs re-stamp to current env |

### Failed rows (data for coordinator)
- **181** ❌ Harbor 404 — `harbor-core` CreateContainerConfigError (missing synced ConfigMap) + `harbor-jobservice` CrashLoopBackOff in mgmt-vCluster; HR falsely "succeeded". Open a fix ticket (harbor vCluster config-sync).
- **186** ❌ UAT.md Evidence cells point at hw167's `2026-06-19/evidence/walk` dir (authored #3866), not the live hw173 env — re-stamp evidence to current env.

### ⚠️ rows (not failures)
- **165, 166** cutover steps: cutover not initiated on hw173 (clean prov) — no failed step / no completion to assert.
- **170** install-openbao: openbao green but relocated into mgmt-vCluster (#3642) → no host `install-openbao` row.
- **174, 176** retry click-flow: control present + gated in code, but zero Failed rows on this 62/65-Ready env to click.
