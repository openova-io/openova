# PATH TO 100% — **hw292 live, cc=true** (refreshed 2026-08-06)

> **Source of truth:** [`UAT.md`](UAT.md). **Current env = hw292** (`hw292.omani.works`, dep `1c56518035a83e03`, 2-region Huawei me-east-215-a/-b-1) — **fired 2026-08-03T04:04Z, converged, `cutoverComplete=true` 08:12:30Z**, and G12 region-kill re-proven **6/6 zero-touch** on 2026-08-04 (promotion T0+136s, failback with a clean re-clone, no split-brain — `docs/sessions/2026-08-04/hw292-g12-region-kill/`). hw291 was wiped 2026-07-31 08:54Z after banking cc=true.
> This file maps every non-green row to its gate + owner. A row stays non-green until the hw292 walk verifies it — **merge ≠ green** (founder rule). Nothing below is a walk stamp; this is the *fix map*, and it says exactly which fixes are already inside the image hw292 will boot.

---

## Where the ledger actually stands

The ledger was reset for this env — `scripts/reset-uat.py hw292` flushed 135 hw291 evidence cells to ☐/⏳ on 2026-07-31, per the founder's each-new-env-flushes-all-evidence law — and has been re-walked upward from there ever since. It is **no longer** near-zero, so the "a raw tally reads near-zero by design" note that stood here from 2026-07-31 has been retired rather than left to mislead.

**Live tally, `scripts/uat-tally.py` on this commit (2026-08-06): 156 ✅ / 286 data rows = 54.5%** — ✅ 156 · ⚠️ 47 · ◑ 5 · ❌ 19 · ☐ 12 · ⛔ 45 · N/A 2. Read the verdict from the status column only; the tally script does that and a whole-line glyph search does not (it over-reported by 23 rows when last measured, always optimistically).

The **denominator honesty** matters as much as the numerator: 45 rows are ⛔ — assertions superseded by a merged, founder-approved design decision — and they can never go green as written. The largest single block is the 11-row placement family (rows 98–108), voided by #4325's deliberate de-vcluster of the mgmt/rtz/dmz planes. Those are not product faults and must not be counted as gaps; equally, they are not passes. Until the founder rules on rewrite-vs-exclude (see "Not achievable as written" below), 54.5% is the honest raw figure and 156/241 = 64.7% is the honest figure with the ⛔ set excluded from the denominator. Quote whichever, but say which.

The last **walked** tally, on hw291 before the wipe, was **135 ✅ / 281 = 48.0% raw** (`dc18c6d9a`). The last walked north-star remains hw288 at **214 ✅ / 281 = 82.0%** (2026-07-26). Durable structural state — the number that survives a flush — stands at the last evidence-backed value; see the completion matrix, and change it only on walk evidence.

---

## The delivery gate is the whole story this cycle

Every fix listed below is **merged and inside the image hw292 provisions with**. That claim is not an assumption — it is checked mechanically. The catalyst-api / catalyst-ui image pin on `main` is commit `fb41faf`, and a fix is delivered iff:

    git merge-base --is-ancestor <fix-sha> fb41faf

| issue | fix | delivered on hw292 | verification done pre-walk |
|---|---|---|---|
| #5515 derivePattern fails open into `singleton` | PR #5519 → `796e587b2` | ✅ ancestor | `placement.ts:107-108` returns `PATTERN_NOT_REPORTED` when `primaries === 0`, making the `singleton` return unreachable without a Primary; `placement.test.ts` 21/21 incl. a dedicated #5515 block pinning **both** directions |
| #5489 four surfaces report a vCluster that does not exist | PRs #5526 `8523ecf33`, #5490 `54614d3b3`, #5524 `908f76b0e` | ✅ all three ancestors | root fix at `organization_controller.go:1110` — `vclusterStatusFor` keys off the **same** `gitops.BoundaryIsVcluster` predicate that decides authorship, so status cannot disagree with reality; 10 tests green across handler + UI + controller |
| #5477 `replicationLagSeconds: 0` published as a measurement that was never taken | PR #5478 → `c9b2f9f4c` | ✅ — pinned as the controller image itself, `products/continuum/chart/values.yaml:41 tag: "c9b2f9f"` | code present; live re-check of the four Continuum CRs rides the walk |
| #5485 defects 1–3 (reconciler log prefix, showback Job rows, treemap ReplicaSet names) | `a854cb7ae`, `6844b38f6` | ✅ ancestors | defect 1 unit-proven; defects 2+3 **live-verified on hw291 before the wipe** (authed owner API: one platform row, zero itemized Job rows, zero hash names, cost conserves exactly) |
| #5531 gitea OAuth state lost on pod roll | PR #5533 → `fb41faf30` | ✅ (it *is* the tip) | `[session] PROVIDER = db`; bp-gitea **1.2.49 published + signed** after the publish blocker below was fixed |

**Publish blocker found and fixed this cycle:** bp-gitea 1.2.49 silently failed to publish because `httproute-render.sh` piped `echo` into `grep -q` under `pipefail` — `grep -q` closes the pipe on first match, `echo` dies with SIGPIPE 141, and the pipeline reported FAIL **on a passing value**. Herestrings removed the pipe (`bf59fb5f0`); blueprint-release green, 1.2.49 on ghcr. Any chart whose tests use that idiom is exposed to the same false failure.

**Formerly held pending hw292 readiness** (a deploybot roll mid-prov abandons the provisioning — prov-preflight gate 5). hw292 reported ready and the freeze thawed; **both merged**:

| PR | issue | state |
|---|---|---|
| #5534 | #5485 defects 4–6 (suspended HR shows healthy, `?status=` ignored, `/fleet/applications` duplicates) | **MERGED** |
| #5535 | #5488 cutover pre-flight aborts on a recoverable condition | **MERGED** |

Both are in `main` and inside published images — but read Gate 5 below before treating that as delivered to hw292, which it is not.

---

## Gate 1 — Pillar 5 (cutover): mechanism proven, hardening delivered

hw291 reached **`cutoverComplete=true` 2026-07-30T11:03:43Z**, all 11 steps, 600s deny-egress hold green in both regions. G11/166 banked. The gate is no longer "does it work" but "does it survive the awkward cases":

- **#5488** — after a catalyst-api restart (`strategy: Recreate`), the secondary-kubeconfigs pre-flight aborted with `expects 1 secondary region(s) but only 0 kubeconfig(s) are readable (missing/unreadable: )` while the Secret was already correct. Root cause runs deeper than the issue described: `chrootEnsureDeployment` minted a record with an **empty `ID`** under map key `""` with no guard, reachable from ~40 endpoints via unchecked URL params, and `chrootServesDeployment` discriminates on FQDN alone — so `sync.Map.Range` nondeterministically served the poisoned record. Fixed at mint, at resolution, and by accepting an already-materialized Secret. The #5359 fail-loud contract is **unweakened**: genuinely-missing data still aborts before step 1. PR #5535, held.
- **#5391 / #5393** — a per-Org customer app's HelmRelease can still gate the platform's cutover, and plan-s quota misses `bp-keycloak` by 50m CPU / 64Mi (one oidc-gate sidecar). **Founder decision, not an engineering call.**

## Gate 2 — founder-gated credential (#4277)

Rows 219, 220, 222 and G8/G9 need the Anthropic credential; `seedAnthropicToken` loud-skips without it. **No engineering work clears these.** The one genuinely external dependency in the ledger.

## Gate 3 — filed defects with named owners

| rows | defect | state |
|---|---|---|
| 110, 112, 114, 115 | #5389 per-app Open/launch does not land in the app | **root-caused 2026-08-06** (see below); fix in flight. Rows 110/112/114 have since walked ✅ on hw292 — those are *platform* apps on `<app>.<SovereignFQDN>`, which is the one shape that resolves. Row 115 is still ❌ but for an unrelated pair of blockers (PIN wall #5642 + zero seeded guacamole connections, #5598), not for the hostname defect |
| 35, R9 | #5358 guacamole blank page after the SSO round-trip completes | filed, reopened on runtime evidence |
| G12 | #5388 region-kill failback left a data split-brain | first fix merged (cnpg-pair 0.2.23 surfaces peer-probe starvation); re-proof rides hw292 |
| R17 | #5364 org-delete leaves a half-teardown | filed, reopened on runtime evidence |
| 212, 213 | per-Org MCP — #5516 proved Org-scoped reads never worked on any env; fix PR #5522 routes to the own-org seam | merged, delivered on hw292 |
| — | #5385 deployment health aggregate reads stale-degraded (trust kubectl, not the badge) | filed, affects walk trust |

### #5389 root cause — the endpoint hostname vocabulary cannot express a per-Org app host

Found 2026-08-06, measured live on hw292 (dep `1c56518035a83e03`, `cutoverComplete=true`). The earlier row-114 fix (`bp-newapi` declaring `endpoints: []`) was correct but addressed only a *missing declaration*. The residual defect is deeper: for a per-Org app the declared hostname **evaluates to a host that does not exist**, so the launch button is not mis-styled — it points nowhere.

**Per-Org apps are served on the tenant POOL domain.** Every per-Org hostname actually served on hw292:

```
agenity.uatco.omani.homes
console.uatco.omani.homes
console.r17probe.omani.homes
wordpress.uatco.omani.homes
```

Across all 21 live `HTTPRoute`s in both regions, **zero** hostnames match `<app>.<org>.hw292.omani.works`. Platform apps sit on `<app>.hw292.omani.works` (`gitea.`, `grafana.`, `registry.`, `auth.`, `bao.`, `newapi.`, …) and resolve fine — which is why rows 110/112/114 pass and the per-Org rows do not.

**But every per-Org blueprint declares the SovereignFQDN shape.** 30 `hostnameTemplate` declarations across 26 blueprints use `<app>.{{.OrgSlug}}.{{.SovereignFQDN}}` — including the two apps that are actually live per-Org on hw292:

- `products/agenity/blueprint.yaml:175` → `agenity.{{.OrgSlug}}.{{.SovereignFQDN}}` → evaluates to `agenity.uatco.hw292.omani.works`; **served host is `agenity.uatco.omani.homes`**
- `platform/wordpress-tenant/blueprint.yaml:278` → `{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}` → evaluates to `wordpress.uatco.hw292.omani.works`; **served host is `wordpress.uatco.omani.homes`**

plus `neo4j` (x2), `llm-gateway`, `librechat`, `opensearch` (x2), `stalwart-tenant` (x3), `valkey`, `milvus`, `matrix`, `temporal`, `langfuse`, `livekit`, `openmeter`, `flink`, `stunner`, `litmus`, `clickhouse`, `ferretdb`, `iceberg`, `kserve`, `strimzi`, `vllm`, `bge`, `anthropic-adapter`.

**No blueprint can currently be written correctly, because the vocabulary has no token for the pool domain.** `evaluateHostnameTemplate` (`products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go:1889`) substitutes exactly three tokens — `{SovereignFQDN}`, `{OrgSlug}`, `{AppName}` (plus their `{{.X}}` aliases) — and both call sites pass the Sovereign FQDN and never the Org's pool domain:

- `:713` — `evaluateHostnameTemplate(ep.HostnameTemplate, h.endpointSovereignFQDN(), org, appName)` (silent-SSO launch URL)
- `:1816` — same three arguments, over `bp.Endpoints` (resolved-endpoint list)

So the per-Org host is not merely mis-templated in 26 files; it is **inexpressible**. Fixing the blueprints alone cannot work — the resolver needs an Org-pool-domain token and the callers need to supply it.

**It also fails open, which is why this reached a walk instead of a 500.** `strings.NewReplacer` leaves an unrecognised token untouched, the result is then `strings.ToLower`-ed, and `buildLaunchURL` emits it as a URL regardless. An unresolvable template therefore produces a confident-looking dead link rather than an error — the same failure-open shape called out in the [render-guard](reference) and fail-open-CI lessons.

**Separately, `bp-openclaw` reproduces the row-114 dark-button defect exactly.** `platform/openclaw/blueprint.yaml:25` is `visibility: listed`, the chart ships `platform/openclaw/chart/templates/httproute.yaml`, and the blueprint declares **no `endpoints` key at all** (zero occurrences) — a listed, route-bearing app with nothing for the console to render a launch control from.

Full write-up on **#5389**; fix in flight. Do not stamp the per-Org launch rows green on a link that renders — check that the host it points at is one the Sovereign actually serves.

## Gate 4 — the ⚠️ partials

Still the largest actionable group and still the highest-leverage triage target. Two were resolved by adjudication rather than code this cycle, which is the pattern to repeat where the assertion — not the platform — is what is stale:

- **row 22** (showback platform-overhead roll-up) → ✅. Tenant-ns Job pods routing to `__platform__` is the *deliberate, test-encoded* contract of #5493 (`org_consumption_test.go:186`, `:35`, `:63`) and matches the row's own wording ("holding all control-plane/Job workloads"). The predecessor-env symptom — a *named* tenant app inside the platform app list — is structurally unrenderable after the one-shot fold.
- **row 55** (topology single value) stays ⚠️ honestly: all three topology clauses pass, and the residual is a **region** field, not a topology value — host-native singletons carry `primaryRegion: platform-bootstrap-owned-host` because the data layer emits the bootstrap host label as a region. The read-side fix (`b41c93b3c`) is delivered; the data-layer emit is the open half.

## Not achievable as written (⛔)

Placement rows **98–108** are superseded by the #4325 de-vcluster reclassification (founder verdict); row 109 is also ⛔ but for an unrelated reason (the KC account-console REST 401 is an accepted consequence of passwordless-PIN, #688 — its #3642 link was a mis-link and is corrected in the ledger). Plus R1/M1/G5 (janitor), R19 (sandbox — concept removed 2026-06-30), R20 (delivery), 94/95 (funnel). For the ledger to reach 100% these need either rewriting to match the shipped design or formal exclusion from the denominator. **Founder call.**

The 98–108 supersession is **re-anchored to live state 2026-08-06**, not resting on the merged PR alone — this family was re-flipped ❌ once before by a walker who re-asserted the dead expectation (corrected in `d8761bf2b`), so every one of the 11 rows now carries a dated hw292 live re-confirmation and `env=hw292` in place of its wiped-env stamp. Measured on dep `1c56518035a83e03`: across 53 namespaces the only vcluster-related one is `vcluster-system` (the controller ns, itself holding zero workloads), no `mgmt` / `rtz` / `dmz` namespace exists, and all 7 named platform apps run in host namespaces with live pods (`gitea`, `grafana`, `guacamole`, `harbor`, `keycloak`, `newapi`, `openbao`). Row 107 in particular asserts the exact **opposite** of the correct post-#4325 design — "none of the 7 named apps appear under `host`" — when host is precisely where they now correctly belong. **This is verdict classification against a merged decision plus live state; no walk is claimed by any of these 11 rows.**

## §854 disposition — closed, and re-proven this cycle

`scripts/check-no-nodeports.sh` on `main`, 2026-07-31: **zero NodePorts across sources + rendered charts**, every chart rendered. The guard is itself vacuity-tested (#5518, merged): it self-checks that its pattern matches 4/4 banned forms, rejects 2/2 allowed forms, and scans ≥200 files before trusting a pass — so a green result can no longer mean "the guard matched nothing". A raw `grep NodePort` across the repo returns hundreds of hits; every one inspected is the ban's own prose — comments explaining cilium's internal BPF-masquerade requirement, the documented removal of the #4691 fallback, and the guard workflow itself. **Raw grep is not the audit; the render-and-adjudicate guard is.**

**Correction, 2026-08-04 — the enforcement half of that claim held in only ONE region.** This file previously stated flatly that Kyverno `forbid-nodeport-service` "runs Enforce with a fail-closed webhook, made tamper-evident by #5386". Measured live on hw292:

| region | HR `values.compliancePolicies` | ClusterPolicies at Enforce | `forbid-nodeport-service` |
|---|---|---|---|
| region-a | `{"bootstrapMode": false}` | 9 of 25 | **Enforce** |
| region-b | `{}` — never patched | 1 of 25 | **Audit only** |

So on a 2-region Sovereign the NodePort ban was *advisory* in half the fleet: region-b would record a NodePort Service, not block it. The §854 literal scan still passes in both regions (0 NodePorts; 176 svc region-a / 159 region-b **as counted on 2026-08-04** — region-a has since grown to 192, see the 2026-08-06 re-measurement below), so nothing was violating it — the gap is that region-b would not have *stopped* one. Root cause is **#5591**: the Wave 5.90 phase-2b `bootstrapMode` flip reached only the primary region. The source fix is merged (`bb8ceec71` #5592, compile-repaired `ef2d59767` #5619), unit-tested (`TestPolicyEnforceFlip_FlipsEveryRegion_5591`), and delivered in published images — but hw292's phase-2b ran *before* delivery, so its region-b remains unflipped until remediated.

The lesson generalizes past this instance and matches the [per-region split class](reference): a security posture verified in one region is not a fleet property. Assert it per-region or do not assert it.

**Update, 2026-08-06 — the gap is now CAUGHT BY A GUARD, not merely described here.** The correction above stated the split but left nothing enforcing it, so the same drift would have gone unnoticed on the next env. `scripts/check-live-nodeports.sh` gained a **Phase 2 enforcement-posture check** (PR **#5696**, merged `712860075`): it reads `forbid-nodeport-service`'s `spec.validationFailureAction` in the cluster it is pointed at and **fails closed** — `Enforce` → pass, `Audit` → FAIL, policy absent → FAIL, any unrecognised action → FAIL. Its verdict classifier is vacuity-self-tested in-script (`Enforce`→PASS, `Audit`→not-PASS, `""`→ABSENT, `weird`→not-PASS), so a degenerate always-pass cannot survive its own file. Phase 1 (the literal scan) passing no longer implies compliance; the script says so in its own failure text.

Re-measured live on hw292 (dep `1c56518035a83e03`) 2026-08-06, and the guard demonstrably discriminates **in both directions on the live fleet** — which is what makes this a proof rather than an assertion:

| region | Services scanned | live NodePorts | `forbid-nodeport-service` | `check-live-nodeports.sh` exit |
|---|---|---|---|---|
| region-a (`me-east-215-a`) | 192 | 0 | **Enforce** | **0 — pass** |
| region-b (`me-east-215-b-1`) | 159 | 0 | **Audit** | **1 — FAIL** |

Both regions are still literally clean (0 NodePorts), and that is exactly the point: the only thing separating them is the *posture*, and only Phase 2 can see it. The source-side every-region flip (#5592/#5619) remains correct and merged — hw292 simply provisioned before delivery, so its region-b stays unflipped until remediated (item 3 below). Fleet enforcement is proven by running the guard **once per region kubeconfig**; running it once against the primary is the exact mistake that hid this for days.

---

## Gate 5 — the delivery chain itself (found 2026-08-04, the largest single cause of stalled progress)

Steps 1–3 of this plan were never the constraint. **The chain from `main` to a running binary was broken in two independent places, and both looked like "the fix didn't work".**

**5a. `build-ui` was failing, so nothing published at all.** Across the last ten `catalyst-build` runs on `main`, six had `build-ui=failure` with `deploy=skipped` — meaning six merges published *no image*. The cause was a genuine race in `ExecPanel.test.tsx` (#5633): the component renders byte-identical markup for `fallback-loading` and `fallback-ready` and dials its socket in a passive effect, so under CPU-starved CI the scheduler yields between commit and effect flush and the assertion runs before the socket exists. Reproduced 8/8 under load, 0/12 after the fix. **Fixed and merged (#5651); two consecutive greens since, each with `deploy=success`.** #5626 and #5633 closed on that evidence.

Behind it sat the reason nobody noticed for two months: the vitest step ran `npm test || echo WARN` from `f61a52ab6` until #5553, so the gate **exited 0 unconditionally**. When it finally told the truth, four source defects surfaced at once. A fail-open gate is worse than no gate — it manufactures the appearance of coverage.

**5b. Even a published image cannot reach a cutover Sovereign.** hw292's local Harbor holds exactly `["fad88bd"]` for catalyst-api — the step-03 prewarm tag, digest byte-identical to the running pod. **18 catalyst-api tags have published to ghcr since; zero arrived.** Three independent severances: `proxy-ghcr` has `registry_id = None` (a push-populated project, not a pull-through cache), `mirrorResync.enabled: false` is the deliberate Principle-14 git severance, and #5644's Day-2 IMAGE leg ships in cutover chart `0.1.161`/`0.1.162` while hw292 runs `0.1.159` — *the remedy cannot pass through the gap it remedies*. Note also that the shipped default `mode: detect` writes the missing set to a ConfigMap and copies nothing; only `mode: warm` delivers, and it needs an operator credential. Tracked as **#5640**.

The practical consequence: **#5642**'s catalyst-api leak fix (`Factory.AddCluster` rebuilt all 42 informers on byte-identical kubeconfigs) is correct, merged, published — and has never run. The live pod still leaks **62.1 Mi/min**, monotonic, 21 restarts at a ~59-minute cadence. Raising the memory limit remains the wrong fix: at that rate an 8Gi ceiling buys three hours instead of one and hides the defect.

## Gate 6 — sovereignty proves the wrong property (#5650)

Step-08's deny-egress hold is genuinely deny-*all* (`defaultDenyEgress: true` since #3678) — an earlier claim in this session that it blocked a named list was wrong and was corrected on the issue. But the hold is **time-boxed and then torn down**, so it proves nothing external was *reachable* for 600s. It cannot prove the Sovereign has no external *dependency*. Live: `vcluster-system/loft` → `https://charts.loft.sh`, interval **15m**, referenced by a live HelmRelease, artifact re-fetched from the public internet ~10h *after* `cutoverComplete=true`. A 15-minute interval straddles a 10-minute window entirely.

Fixed in cutover chart **0.1.162** (PR #5652, merged, **published to ghcr and verified**): a pre-hold lint asserting every non-suspended `HelmRepository`/`GitRepository`/`OCIRepository` resolves to a Sovereign-local host, `allowHosts` empty by default so it fails closed. Its test drives the function extracted from the *render* in both directions and caught a fail-open bug in the first draft, where an uncreatable scratch file made the lint return PASS having examined nothing.

## What actually moves the number next

1. **Walk hw292.** It is live, cc=true, and G12-proven; every "delivered" row above becomes a green stamp only by walking it. This is the only thing that legitimately moves the durable number.
2. **Close the delivery chain (#5640)** — without it, every future fix repeats 5b: correct in source, invisible in production.
3. **Remediate #5591 on region-b** (one HR patch) so the NodePort ban is enforcing fleet-wide rather than in half of it.
4. Merge #5534 + #5535, now unblocked — the env is ready and the merge freeze is thawed.
