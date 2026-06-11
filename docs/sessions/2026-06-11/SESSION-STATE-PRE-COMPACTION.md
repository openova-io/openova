# SESSION STATE — 2026-06-11 (pre-compaction durable save)

> Single source of truth to resume from after context compression. Everything valuable from this session, protected. Authored on founder order ("save the entire session permanently with all valuable information protected, then we'll compress").

---

## 0. TL;DR — what this session actually delivered

The headline: after 3 prov fires (2 failed for known reasons, the 3rd converged clean), **hw128 became the first clean zero-touch 2-region Sovereign of the session to produce founder-visible, screenshotted PASSES** — directly answering the founder's standing verdict ("no visible end-user improvement, almost nothing… theater").

**Founder-visible PASSES (real email+PIN, Playwright, on live hw128):**
- ✅ **SSO-into-app (#3272)** — real PIN (840152) lands the user *inside* Grafana (`grafana.hw128.omani.works/?orgId=1`, authenticated), NOT bounced to console/dashboard. Contrast: hw126 pre-fix landed console/dashboard = FAIL (#3270). Evidence `docs/sessions/2026-06-11/evidence/hw128-grafana-sso-PASS-landed-in-app.png` (PR #3287). **#3271 CLOSED.**
- ✅ **jobs-both-regions (#3278)** — sovereign `/jobs` shows a Region column + filter; me-east-215-a (16) + me-east-215-b-1 (690) install-job rows. Evidence `hw128-jobs-both-regions-PASS.png` (PR #3288). **#3276 CLOSED.**
- ✅ **operator console working** — treemap (101 items), Apps catalog (Deployments 49 / Catalog 63, all bootstrap apps INSTALLED w/ dependency graphs). Evidence `hw128-apps-page.png` + `hw128-console-surfaces-working.md` (PR #3289).
- 📋 Recorded in **UAT §0d** (`docs/ledger/UAT.md`, PR #3291).

---

## 1. Live environment (CURRENT — do not lose)

| Field | Value |
|---|---|
| Live Sovereign | **hw128** `5cc5f21df5f64ea7` — `hw128.omani.works`, status **ready**, 2-region (me-east-215-a + me-east-215-b-1) |
| Cached kubeconfigs | `/tmp/hw128-a.kubeconfig` (primary), `/tmp/hw128-b.kubeconfig` (secondary). Re-pull via catalyst-api PVC `/var/lib/catalyst/kubeconfigs/5cc5f21df5f64ea7.yaml` (+`-me-east-215-b-1.yaml`). |
| Convergence | 57/60 HRs Ready (3 N/A: bp-cluster-autoscaler-hcloud, bp-hcloud-ccm, bp-velero — hetzner-only). |
| Console | `console.hw128.omani.works` 200 · `auth.hw128…` KC 200 · `grafana.hw128…` 302→SSO. |
| Mothership | catalyst-api on `ghcr.io/openova-io/openova/catalyst-api:2365ebc`. JWT key `/tmp/hw-priv.pem` (handover RS256, email emrah.baysal@openova.io role catalyst-owner). |
| Stalwart mailbox | emrah.baysal@openova.io PIN inbox; IMAP `mail.openova.io:993`, password in k8s secret **`stalwart-user-credentials` key `emrah.baysal`** (NOT stalwart-emrah-real which is empty), mothership kubeconfig `/home/openova/.kube/config` ns `stalwart`. ~480 prior PIN emails — the PIN walk is NOT founder-gated. |
| Wiped this session | hw126 (`c986326a77d391d4`), hw127 (`5a4c1469ea0139a8`), hw127-attempt-1 (`141ab732dfec4e34`). All gone. |

---

## 2. The 3 fires (why 2 failed — critical lessons)

1. **Fire 1 `141ab732` (hw127, SHARED_PG=true) — FAILED ~4min, mid-apply.** Cause: merged #3278+#3279 (catalyst-api PRs) right before firing → deploy-bot rolled the mothership during tofu-apply → provisioning goroutine (runs INSIDE mothership catalyst-api) abandoned. **Lesson saved to memory `feedback_never_merge_catalyst_api_prs_right_before_firing_a_prov.md`.** Gate fires on `0 catalyst-builds in_progress` + pod-age>5min.
2. **Fire 2 `5a4c1469` (hw127, SHARED_PG=true) — WEDGED.** Cause: the #3188 shared-PG model has a fatal cascade-deadlock (#3283) — bp-postgres-shared placed connection Secrets directly in gitea/harbor namespaces that don't exist (their charts dependsOn bp-postgres-shared) → console down. **De-risk lesson: don't gate the first walk on an unproven gated feature.**
3. **Fire 3 `5cc5f21d` (hw128, NO SHARED_PG) — CONVERGED CLEAN.** Produced the PASSES above.

---

## 3. PRs — merged & open (this session)

**MERGED (the work):** #3266 (evidence), #3267 (KC kyverno compliance → IdP recovered), #3269 (DNS re-publish path), #3270 (hw126 walk evidence), #3272 (**SSO-landing fix**), #3273 (**systemic kyverno label-mutate**, fixes 30/34 charts), #3274 (**#3188 data-instances panel** in deployed console), #3279 (defect-B handover job sweep), #3280 (mothership defect-B walk evidence), #3284 (**#3188 deadlock fix** — reflector-source pattern), #3287/#3288/#3289/#3291 (hw128 walk evidence + UAT §0d), #3278 (**jobs-both-regions**).

**OPEN (need landing):**
- **#3290** `fix/clustermesh-primary-kubeconfig-fallback-3241` — **region-kill fix (#3241)**. ⚠️ **CI FAILING on `test` AND `integration` (kind+gitea)** as of last check. `test` fail = flaky `TestGetDeploymentEvents_ReturnsComponentEventsInBuffer` (phase1_watch_test.go, unrelated to clustermesh.go); the **integration fail needs investigation** — confirm it's flaky vs the fix breaking something before merging. On merge → mothership rolls → `restoreFromStore`→`runAutoEstablishClusterMesh` re-establishes hw128 mesh zero-touch (no re-prov) → cnpg-pair flips → region-kill walkable.
- **#3286** `fix/shared-pg-consumer-wiring-3285` — **#3188 consumer-wiring** (gitea/harbor read the reflected shared-PG secret in SHARED mode; own-cluster byte-identical). Render-proven, chart-only (safe to merge). Needs the dedicated SHARED_PG prov to validate live.
- **#3282** `fix/provision-dashboard-null-deref-3281` — dashboard null-deref crash fix (provisioning state). HELD during convergence; safe to merge now (catalyst-ui roll won't hurt ready hw128).

---

## 4. #3188 (founder's #1 item) — FULLY root-caused, all 4 causes fixed in code

Why it "never worked" — four compounding causes, now all addressed:
1. **Gated safe-by-default OFF** (#3197) + the gate `SOVEREIGN_ENABLE_SHARED_PG` wasn't even wired to a fire-body input → **wired by #3275 (MERGED)**: `POST {"enableSharedPostgres": true}`. The `scripts/sovereign-lifecycle.sh fire` supports `SHARED_PG=true` env.
2. **Panel built in the wrong console** — `DataInstances` was in `core/console` (Svelte, deployed unrouted as `sme/console`), NOT the deployed React `catalyst-ui` → **ported by #3274 (MERGED)** into CatalogDetail.
3. **Fatal cascade-deadlock** (#3283) — connection Secrets targeted non-existent consumer namespaces → **fixed by #3284 (MERGED)**: reflector-source pattern (Secret in shared-data + emberstack auto-namespaces annotations, bp-reflector pushes to consumers).
4. **Consumers didn't read the reflected Secret** (#3285) — gitea/harbor read their bundled `*-pg-app` → **fixed by #3286 (OPEN)**.

**To validate #3188 end-to-end:** merge #3286 → one dedicated `SHARED_PG=true scripts/sovereign-lifecycle.sh fire hwNNN omani.works` prov → walk: catalog data-instances card shows the shared Postgres with 2 consumers (gitea+harbor) bound. (Remember Fire-1/2 lessons: 0 builds pending before firing; SHARED_PG is now safe because #3284 fixed the deadlock.)

---

## 5. #3241 region-kill — root cause (north-star row 1)

ClusterMesh never establishes on a 2-region prov → cnpg-pair flip never fires → no region-kill. **Root cause (live hw128 orchestrator logs):** `clustermesh.go:339-341` sets `primaryKubeconfigPath = dep.Result.KubeconfigPath` which is EMPTY (not re-hydrated after a catalyst-api restart). Secondaries have a PVC-path fallback (`pickPath` → `<kubeconfigsDir>/<id>-<region>.yaml`); the PRIMARY had none. Primary kubeconfig IS on the PVC at `<kubeconfigsDir>/<id>.yaml`. **Fix = #3290** (primary PVC fallback via the existing `resolvePrimaryKubeconfigPath` helper + buildRegionSlots backstop). NOT the NodePort/ELB theory from earlier in the session.

---

## 6. Other systemic findings (durable)

- **#3268 — kyverno landmine inventory:** last night's kyverno Enforce fixes (#3250) made the baseline actually enforce for the first time → 34 always-on charts are DOWN-on-roll landmines (bare images + missing `policy.cilium.io/enforced` label). #3273 (MERGED) auto-stamps the label (mutate-before-validate, fixes 30/34). The 27 image-violators stay per-chart (a static image-rewrite would hardcode mothership harbor → re-tether → Pillar-5 violation). Listed on #3268.
- **#3281 — provision dashboard null-deref** (treemapData.items null during provisioning). Fix #3282 OPEN.
- **DNS re-publish (#3269)** — the record-writer only ran at prov-time; #3269 added `POST /api/v1/deployments/{id}/republish-dns`.
- **KC was down on hw126** (kyverno harbor-proxy-pull denying the bitnami images on roll) — #3267 fixed it. This was the root of the hw126 SSO-matrix failures.

---

## 7. Remaining queue (honest, ordered)

1. **#3290 region-kill** — investigate the integration CI fail; merge; verify mesh self-establishes on hw128 (`cilium-clustermesh` secret appears + `SOVEREIGN_ENABLE_CNPG_PAIR=true` + CNPG clusters in `cnpg` ns); then walk region-kill (≤60s failover, north-star row 1).
2. **#3188** — merge #3286; fire one dedicated SHARED_PG prov; walk the shared-PG card.
3. **Per-app SSO walks** on hw128 — OpenBao (S1, #3226), Harbor, Gitea, Guacamole. The shared #3272 fix applies; per-app risk is app-side (bound_claims/redirect_uri). hw128 console is live + walkable NOW.
4. **#3282** dashboard fix — merge.
5. Funnel F1 (voucher→checkout→Org→tenant-login), vCluster containment (T4) — not yet walked.

---

## 8. Standing directives (DO NOT violate)

- **North Star:** row1 provable BCP/DR region-kill ≤60s · row2 revenue funnel · row3 vCluster containment · row4 cutover+10min egress · row5 Hetzner leg. T-track (T1 mesh ✓, T2 cnpg streaming, T3 region-kill, T4 containment, T5 shared PG), S-track (S1-S4 SSO), F1 funnel.
- **Sandbox / Pillar-4 / EPIC-4 = OUT OF SCOPE** (another project owns it; founder corrected 5+ times). Never list it.
- **Autonomy mandate** — never `AskUserQuestion`; auto-pick highest-ICE + dispatch. Wipe/fire Sovereigns is FULLY autonomous (only protected: bastion-openova / EIP 212.72.24.20).
- **The supervisor is buggy** (founder-confirmed: "bugging you with wrong instructions, we'll fix it" + "dont hesitate to push back… if it is asking non reasonable asks"). It repeatedly demanded walks on the WIPED hw126 + suggested out-of-scope EPIC-4. Push back with evidence; don't burn the window re-proving wiped clusters.
- **kom4dc** = ONE physical region; 2-region MIMICKED via 2 VPCs (me-east-215-a/-b). VPC quota (~5/~10 EIP) forces wipe-then-prov. VPC deletion lags status=wiped ~15min.
- **Anti-theater:** code-merge ≠ done; only a founder-visible clickable artifact on a fresh prov counts. Every sub-agent report is a CLAIM — re-query live.
- Commit identity `hatiyildiz` (269457768+hatiyildiz@users.noreply.github.com); `Refs` not Closes/Fixes; banned MVP/Iteration/out-of-scope/Blocker.

## 9. In-flight at save time

- Background watch `bzlbyj0dg` — #3290 auto-merge-when-green (but CI is FAILING, so it won't merge until CI is fixed; effectively stalled — re-evaluate #3290 CI on resume).
- No active sub-agents (all completed).
- Browser closed.

## 10. Evidence index (docs/sessions/2026-06-11/evidence/)

hw128-grafana-sso-PASS-landed-in-app.png · hw128-sso-into-app-PASS.md · hw128-jobs-both-regions-PASS.png/.md · hw128-apps-page.png · hw128-console-surfaces-working.md · hw128-preWipe-convergence-snapshot.txt · hw126-* (prior walk) · prov-diagnostics/ (cloud-init logs).
