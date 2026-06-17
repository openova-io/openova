# UAT walk — Pillar-5 self-sovereign cutover (#3379)

**Surface:** `bp-self-sovereign-cutover` 11-step engine → 600s deny-egress hold → `cutoverComplete=true`.
**State:** **VERIFIED-FAIL** on hw138 (`4b5ff7852e33fc15`), 2026-06-14. Reproduces the hw136 `egress-block-test` failure on a second env.
**Acceptance:** the operator finalises handover, watches the 11 step-Jobs to Completion, witnesses the deny-egress CCNP present during a full 600s hold while HRs stay Ready, and reads `cutoverComplete=true`.

Each row = one operator action + the observed result on hw138.

| # | Operator action | Expected | Observed on hw138 | ✓/✗ |
|---|-----------------|----------|-------------------|-----|
| 1 | Sign the RS256 owner JWT with the deployment's handover key; `POST https://console.openova.io/sovereign/api/v1/handover/finalise/4b5ff7852e33fc15` with `Authorization: Bearer <jwt>` | 200 JSON finalise ledger; `secret catalyst/tofu-phase0-archive` sealed in the Sovereign's OpenBao | finalise ran; archive sealed (cutover engine fired `source=handover` at 16:12:08Z, which is gated on the seal) | ✅ |
| 2 | `kubectl -n catalyst get cm self-sovereign-cutover-status` — watch `cutoverStartedAt` | non-empty start time; engine running | `cutoverStartedAt=2026-06-14T16:12:08Z` | ✅ |
| 3 | Watch step-Jobs 01→04 to Completion (`kubectl -n catalyst get jobs \| grep cutover`) | all Complete | gitea-mirror / harbor-projects / harbor-prewarm / registry-pivot all Complete | ⚠️ (03 false-green) |
| 4 | Confirm step-03 actually populated local Harbor: `GET https://registry.<fqdn>/api/v2.0/projects/openova-io` | `repo_count` ≈ 29 (catalyst-api etc. native-pushed) | **`repo_count=0`** — manifest HEAD for `catalyst-api:9de0b65` = **404**. skopeo `level=fatal` (no `/etc/containers/policy.json`) masked by `\| sed` → push_ok=29 push_fail=0 but **nothing pushed** | ✗ |
| 5 | Confirm registry tether pivoted: decode `flux-system/ghcr-pull` dockerconfigjson | auths keys = `registry.<fqdn>` (ghcr.io stripped) | auths = `["registry.hw138.omani.works"]` | ✅ |
| 6 | Confirm Flux GitRepository pivoted: `kubectl -n flux-system get gitrepository openova` | URL = local Gitea, `Ready=True` | URL = local Gitea ✅ but **`Ready=False` (`authentication required`)**, `secretRef` empty, stuck at stale artifact `5e645666` | ✗ |
| 7 | Confirm HelmRepositories pivoted: `kubectl -n flux-system get helmrepository -o ...url` | all `oci://registry.<fqdn>/openova-io` | **61 still `oci://ghcr.io/openova-io`** — step-06's Gitea edits never reconciled (Flux can't pull, row 6) | ✗ |
| 8 | Watch step-07 (catalyst-api-env-patch) | Complete; catalyst-api rolls cleanly to the local-Harbor image | HR patched, catalyst-api rolled → **ImagePullBackOff** (image absent in local Harbor, row 4) → cutover engine died with the old pod; status CM orphaned at idx=7 | ✗ |
| 9 | Watch step-08 (egress-block-test); `kubectl get ccnp cutover-egress-block` during the window | CCNP present for a full 600s; HRs stay Ready; step Job Completes | step Job **`failed=1`** — `FAIL — at least one HelmRepository did not pivot within the bounded reconcile wait (#3526)`; **CCNP never applied** (NotFound). The #3526 guard correctly refused to deny-egress with sources un-pivoted | ✗ |
| 10 | Watch steps 09–11 (gitea-token-mint, vcluster-registry-pivot, crossplane-provider-pivot) | each Completes | **never reached** (engine wedged at step 08) | ✗ |
| 11 | `kubectl -n catalyst get cm self-sovereign-cutover-status -o jsonpath='{.data.cutoverComplete}'` | `true` | **`false`** (Job-level terminal: step-08 failed; status CM orphaned `running`) | ✗ |
| 12 | Confirm the Sovereign survived | `console.<fqdn>` 200 | `console.hw138.omani.works` = **200** — env survived because deny-egress never applied; catalyst-api reverted to the ghcr.io image (Flux can't pull local Gitea) | ✅ (but tether NOT zero) |

## Verdict

**Pillar-5 cutover does NOT complete zero-touch.** It fails at **step 08 (egress-block-test)**; the deny-egress hold never executes and `cutoverComplete=false`. Two independent source defects:

- **Defect A (fixed, chart 0.1.59):** step-03 harbor-prewarm skopeo native-push is a silent no-op — skopeo `level=fatal` (missing `/etc/containers/policy.json`) AND `skopeo … \| sed` masks the exit code. Local Harbor `openova-io` stays empty → post-cutover ImagePullBackOff.
- **Defect B (follow-up under #3379):** step-05 pivots the GitRepository to authenticated local Gitea with no `secretRef`; the Gitea pull-token is only minted at step-09 (after step-08). Flux loses git access, the HelmRepository pivot never reconciles, and step-08's bounded wait can't pass.

Evidence: `docs/sessions/2026-06-14/evidence/3379-cutover/` (10 files + `hw138-CUTOVER-KEYSTONE-CATCH.md`).
