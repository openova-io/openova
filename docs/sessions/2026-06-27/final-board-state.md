# Final board state — openova-io/openova open issues (2026-06-27)

> Definitive true-state audit of every open issue. **Labels were not trusted** —
> each verdict is grounded in the actual merge state of the referenced PR, the
> source on `origin/main`, and (where the acceptance is a live signal) the live
> state of the permanent env `91dc05917e44d1c1` (omantel.biz, 2-region Huawei,
> region-a + region-b kubeconfigs). Read-only on clusters; the only write was
> `gh issue` ops on the one issue closed below.
>
> Audit basis: `gh issue list --state open` (14 open at start) → 1 closed this
> session → **13 open remain**.

## The honest count

| Bucket | Count | Issues |
|---|---|---|
| **Open at audit start** | 14 | — |
| **Closeable-now → CLOSED this session** | **1** | #4515 |
| **Open remaining** | **13** | below |
| of which — **Anthropic-credential-gated** | 2 | #4277, #4111 |
| of which — **EIP-bump-gated** (needs a fresh 2-region prov) | 5 | #4513, #4293, #3969, #3379, #4529 |
| of which — **fix-in-flight (PR not yet merged)** | 4 | #4527, #4521, #4488, #4212 |
| of which — **genuine-still-open** (un-run live walk / no fix) | 2 | #4525, #4275 |

> Note: "EIP-bump-gated" and "keystone-fresh-prov-gated" are the **same gate** —
> a fresh disposable Sovereign cannot be created until the Huawei `publicip`
> quota rises, so every issue whose acceptance is a fresh-prov walk sits behind
> the same EIP lever. They are bucketed together as EIP-bump-gated below.

## The two founder levers

| Lever | What it is | Clears |
|---|---|---|
| **A — EIP quota bump 10 → ≥16** | Huawei National Cloud (Omantel) project `f27698137bdc4b00ad509cf27f1e5547`, region `me-east-215`, raise `publicip` quota from **10 → ≥16**. Today 7 EIPs are in use (bastion + the two permanent `omantel.biz` regions); only **3 free**, but a disposable 2-region validation prov needs **6**. Request drafted at `docs/sessions/2026-06-27/eip-quota-bump-request.md` (#4517). | **#4513, #4293, #3969, #3379, #4529** — every fresh-prov acceptance walk (cloud-init bootstrap, the one-vcluster dual-door walk, the placement-model migration, and BOTH Pillar-5 cutover steps + the cutover-integrity proof). Also unblocks the **#4275** destructive drill once it has a disposable 2-region target. |
| **B — Anthropic credential** | The single founder-supplied platform Anthropic credential, seeded once per Sovereign into `catalyst-system/sovereign-anthropic-credentials` → OpenBao `catalyst/anthropic/token`. Unset on 91dc today, so both agenity ExternalSecrets sit `SecretSyncedError`. | **#4277, #4111** — the agenity North-Star (chat→provision). All code/image/MCP-framing gaps are merged and live-verified (claude v2.1.185 baked into `bp-agenity:0.9.7`, seed init container present); the ONLY thing missing is a valid Anthropic OAuth credential to authenticate the spawned agent. |

## Closed this session

| Issue | Why closeable | Evidence |
|---|---|---|
| **#4515** Cilium parity test stale on main | Acceptance is a **test-only change** ("Update the stale assertion … Test-only change"). Fully met. | PR #4518 merged (`aefcf79a`); `cilium_values_parity_test.go:252-254` now accepts both `helm install` and `helm upgrade --install` while preserving the `-f cilium-values.yaml` guard; `go test -run TestCiliumValuesParity` → `ok`; the `Test — Bootstrap Kit` job is `success` on main HEAD. Board-red cleared. |

## Full board-gate table (13 open)

| # | Title (short) | Fix merged? | Gate | Why not closeable now |
|---|---|---|---|---|
| **4529** | cutover harbor-prewarm skopeo `--dest-tls-verify=true` x509 on LE-STAGING | **YES** — PR #4531 (0.1.82, merge `8be76f16`) + Case-30 contract test | **EIP-bump-gated** | Pillar-5 cutover step; the PR itself states "live 11-step → `cutoverComplete=true` validation needs a fresh prov whose cutover reaches the 600s egress hold — NOT live-verified". Case-30 guards the wiring (regression), it is not the issue's deny-egress acceptance. |
| **4527** | cutover registry-pivot v1 mirror pulls private ghcr anonymously → 401 | NO — **PR #4532 OPEN (CONFLICTING)** | **fix-in-flight-PR #4532** | Fix not merged; PR has merge conflicts to resolve. |
| **4525** | FUNNEL/Pillar-2 BCP picker offers hardcoded Hetzner regions on a Huawei Sovereign | NO — PR #4526 fixed only the #4524 Review-step display | **genuine-still-open** | The required `/catalog/regions` endpoint still does not exist (`core/services/catalog/handlers/routes.go` registers `apps/plans/addons/industries/bundles` only). Server-side region source is unbuilt. |
| **4521** | crossplane infra-config Kustomization batches Provider+ProviderConfig → atomic dry-run deadlock | NO — **PR #4528 OPEN (mergeable)** | **fix-in-flight-PR #4528** | Fix authored, not merged. Live: `providers.pkg.crossplane.io` → none; `workspaces.tf.upbound.io` CRD absent. |
| **4513** | fresh prov wedges at 0 HelmReleases (two-NAME `create namespace` + `endif~` jams infra-config YAML) | **YES** — PR #4520 (`15e8a0aa`) | **EIP-bump-gated** | Both bugs provably fixed on main (namespace loop at tftpl:1447; `%{ endif }` no-tilde at :955) and `TestCloudInit_InfrastructurePathPointsAtTemplate` passes — but the stated acceptance ("fresh prov reaches HelmReleases") is a runtime outcome requiring a fresh prov. Conservative: cloud-init wedges are the exact class that needs a live fresh-prov gate, not CI alone. |
| **4488** | provider-opentofu never installs on Huawei (xpkg pull via poisoned-EIP harbor) | PARTIAL — PR #4491 merged (xpkg off poisoned EIP); runtime still inert | **fix-in-flight-PR #4528** | #4491 landed the pull-route fix, but #4521 proved the adoption seam is STILL inert at runtime (atomic dry-run deadlock) → provider never installs. The remaining install fix is PR #4528 (open). |
| **4293** | EPIC one-vcluster-one-Org + de-vcluster planes | YES — A (#4295/#4476), B (#4298/#4335/#4495), C (#4325/#4391) all merged | **EIP-bump-gated** | All 3 workstreams merged + structurally live-verified (zero `org-<uuid>`/`vc-`/`*-vcluster-0` strays; 2 Org CRs). But the EPIC's acceptance hard-requires a **zero-touch fresh prov driving one Org through the funnel AND one through BSS** — that dual-door walk has not been recorded. |
| **4277** | FUNNEL/agenity auto-seed per-Org openbao `anthropic/token` at Org-create | **YES** — PR #4303 (producer `seedAnthropicToken`) + passing unit test | **Anthropic-credential-gated** | Producer merged + tested (it correctly loud-skips when no platform credential). Live: `uatwalk91` Org's `agenity-anthropic-token` ExternalSecret READY=**False/SecretSyncedError** because the platform Anthropic credential is unset on 91dc. "READY=True with no manual seed" cannot be reached until lever B. |
| **4275** | PILLAR-3 region-kill failover drill (D31) | N/A — **PR #4517 is runbook + EIP-quota request only; nothing killed** | **genuine-still-open** | Plumbing is live + healthy on 91dc (cnpg-pair Continuum Healthy, region-b replica 3/3 streaming, two independent clusters). But the load-bearing destructive failover proof has NEVER been executed; it must not run on the permanent env, and a disposable 2-region target is EIP-capacity-gated (lever A). |
| **4212** | EPIC ONE object-model/DR backbone | PARTIAL — DR/spine half merged + live-Healthy; adoption half inert | **fix-in-flight-PR #4488/#4521** | #3829 (spine producer) and #4018 (Path-B XRCs) are both CLOSED; live: 4 spine Applications `Ready`, 5 Continuum CRs Healthy with held leases. But the Crossplane-adoption half is inert (24 CloudAdoptions `Ready=False`, no provider) → blocked on the same #4488/#4521 fix. |
| **4111** | bp-agenity spawned claude-code cannot authenticate (no binary / no OAuth) | **YES** — claude binary baked (`bp-agenity:0.9.7`, #4115), seed init + MCP framing (#4115/#4233/#4261) | **Anthropic-credential-gated** | All three named gaps merged + live-verified (claude v2.1.185 in the running pod, seed-claude-creds init present); North Star was proven end-to-end on 2026-06-22. Current live re-proof blocked only by the unset Anthropic credential (lever B). |
| **3969** | EPIC Application-centric Placement (delete declared/observed/effective model) | PARTIAL — `targets[]` model + webhook + fan-out merged (#3972/#4314/#4381/#4498) | **EIP-bump-gated** | The defining deliverable (delete the legacy string model; live Apps carry `targets[]`) is unrealized at runtime: live `spine-keycloak` still carries `spec.placement: active-hot-standby` (legacy STRING), `spec.targets: None`. Rides on #4212's spine seam; needs a fresh prov to land + observe the structured model. |
| **3379** | SOVEREIGNTY cutover earns `cutoverComplete=true` via durable revert-immune fact + true deny-egress proof | PARTIAL — the cutover-integrity step fixes are landing; the proof itself is unwalked | **EIP-bump-gated** | Acceptance is explicitly a **600s deny-egress fresh-prov hold** that blocks the WHOLE mothership and survives a chart upgrade. That walk requires a disposable fresh prov (lever A) and depends on the in-flight cutover step fixes (#4527, #4529). |

## Notable record corrections found during the audit

- **#4212 / #4275 bodies cite the dead dep `4635277cae4ffed9`.** On the current
  permanent env `91dc05917e44d1c1` the DR continuums are all **Healthy with held
  leases** (not "Degraded / no continuums" as those bodies state).
- **#4212's title says #3829 and #4018 are "still unwired"** — both are now
  **CLOSED**. #3829 (spine producer) is live-proven (4 spine Applications
  `Ready`, adopted their bootstrap HRs). #4018's runtime outcome remains inert
  only because provider-opentofu does not install (#4488/#4521).
- **#4529's fix is merged** (PR #4531) with a dedicated Case-30 contract test —
  the regression is locked even though the full Pillar-5 walk is fresh-prov-gated.

## What each lever clears, at a glance

- **Pull lever A (EIP 10→≥16)** and the path opens to validate, on ONE disposable
  2-region prov: #4513 (bootstrap reaches HelmReleases), #4293 (dual-door Org
  walk), #3969 (placement-model migration observed live), #3379 + #4529 (the
  full cutover deny-egress proof), and to safely execute #4275 (region-kill
  drill) without touching the permanent env. **5 fresh-prov-gated issues + the
  #4275 drill.**
- **Pull lever B (Anthropic credential)** and #4277 + #4111 become live-walkable
  (the agenity North-Star authenticates a spawned agent end-to-end). **2 issues.**
- **Neither lever** touches #4527, #4521, #4488, #4212 (merge the in-flight PRs
  #4532/#4528 first) or #4525 (build the `/catalog/regions` endpoint — no fix
  exists yet).

_Refs #909._
