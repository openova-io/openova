# hw78 (de7f6aa8d5ef12b5) — 5-Pillar + Open-Issue Gap Audit
**Read-only audit per founder order 2026-05-31 04:40Z.**
**hw78 alive +5h11m, status=ready, handoverFiredAt=00:12Z, both regions a+b with 1 CP + 4 workers = 10 nodes Ready.**

## Pillar audit findings

| Pillar | Status | Evidence |
|---|---|---|
| **1 Marketplace + voucher** | 🟢 **PASS** | console + marketplace HTTP 200; marketplace-api Pod Running 4h44m; HTTPRoute paths /api/ /back-office/ /. 7 screenshots already committed `c68ac1df` + #2526 comment 4585236815. |
| **2 Multi-region BCP topology choice** | 🟡 **PARTIAL** | 2 regions (me-east-215-a + me-east-215-b) registered + 10 nodes Ready ✓; marketplace `/bcp/` page reachable ✓. BUT bcpTopology field from POST body NOT surfaced in deployment record API response. Topology UI choice exists in wizard but choice NOT wired into infrastructure (no cross-region CNPG, no ClusterMesh — see Pillar 3). |
| **3 CNPG primary+replica + region-kill failover** | 🔴 **MISSING** | Each region runs INDEPENDENT single-instance CNPG clusters (gitea-pg, harbor-pg, openova-flow-pg, pdns-pg, etc.) — INSTANCES=1, READY=1, currentPrimary=*-1, isPrimary=true. NO ReplicaCluster configured. NO synchronous remote_apply. NO cross-region replication. Cilium `clustermesh-enable-endpoint-sync: false` + no clustermesh-apiserver Pods — ClusterMesh DISABLED → no cross-region pod networking → Pillar 3 architecturally impossible. |
| **4 Sandbox + auto-mounted openova-sandbox-mcp** | 🟡 **PARTIAL** | bp-sandbox HR Ready=True ✓; sandbox-controller Deployment 1/1 ✓; `sandboxes.sandbox.openova.io` CRD installed ✓. BUT zero Sandbox instances ("No resources found"). NO `openova-sandbox-mcp` ConfigMap found. MCP auto-mount + per-Org knowledge wiring NOT validated. |
| **5 bp-self-sovereign-cutover + 10min deny-egress hold** | 🔴 **MISSING** | **Cutover Step 03 FAILED on hw78**: `[harbor-prewarm] FATAL: skopeo not available after apk install` — alpine/k8s 1.31.4 image's apk repos don't carry skopeo (G69 NEW class). Chain stopped at Step 03. Steps 04 (registry-pivot), 05 (flux-gitrepository-patch), 06 (helmrepository-patches), 07 (catalyst-api-env-patch), 08 (egress-block-test) **NEVER RAN**. Evidence: catalyst-api Deployment image still `ghcr.io/openova-io/openova/catalyst-api:a269122` (NOT pivoted to `registry.hw78/openova-io/...` NATIVE path). No deny-egress NetworkPolicy. No cutoverComplete annotation. **Pillar 5 sovereignty NEVER ACHIEVED on hw78. Same likely on hw75 (also passed verifier 81/81 but cutover Step 03 may have failed identically — verifier only checks substrate, not cutoverComplete).** |

## Why verifier 81/81 passed despite Pillar 5 failure

Verifier `scripts/sovereign-dod-verify.sh` checks L1-L6:
- L1 deployment record (status=ready, handover fired)
- L2 nodes Ready
- L3 HR Ready=True
- L4 catalyst-system containers Ready
- L5 HTTPS surfaces 2xx/3xx/4xx
- L6 NO surgical edits (zero-touch contract)

NONE of L1-L6 validate:
- cutoverComplete annotation set on deployment
- Step 03-08 jobs Complete
- catalyst-api image uses NATIVE-pushed path (proof of cutover-Step 07 ran)
- deny-egress NetworkPolicy exercised (proof of cutover-Step 08 ran + Sovereign survived)
- Cross-region CNPG replication (Pillar 3)

**Verifier needs L7 check: cutover Steps 01-08 all Complete + cutoverComplete=true + 10min deny-egress hold passed.** META-AUDIT 17 candidate.

## G69 NEW BUG CLASS — skopeo unavailable in alpine/k8s image

G66 Phase A assumes `apk add skopeo` works on alpine/k8s:1.31.4 base. Live evidence on hw78: `FATAL: skopeo not available after apk install`. The bitnami/alpine/k8s image likely uses a custom apk repo without community/skopeo package.

Fix candidates:
- Switch base image to one that has skopeo (e.g., `quay.io/skopeo/stable:v1.16.1` Fedora-based)
- Download skopeo binary statically in script (`curl -L https://.../skopeo-static`)
- Use a two-container Job: alpine/k8s for kubectl enumeration + quay.io/skopeo/stable for copies (initContainer enumerates, main container copies)
- Build a custom catalyst-utils image with both kubectl + skopeo + jq + curl

## Pre-existing G## issues triage (substrate-validation status against hw78)

| Issue | Title shorthand | Substrate-validated? | Next action |
|---|---|---|---|
| #2584 G29 | catalyst-build deploy-bump silently no-ops on fix-forward race | NO (CI tooling, not substrate) | Separate CI workstream |
| #2588 G32 | hw55 v2 region-A only 2 of 3 workers Ready | YES (hw78 has 4+4 workers in both regions, all Ready) | Flip status/uat → status/completed |
| #2590 G34 | sovereign-wildcard-tls dnsNames wildcard AND specific | YES (hw78 LE Prod cert YR2 mints cleanly) | Flip status/uat → status/completed |
| #2591 G36 | CI gate tofu validate before image publish | NO (CI tooling, not substrate) | Separate CI workstream |
| #2595 G37 | Flux source-controller HTTP cache hang | YES (hw78 source-controller healthy, no observed hangs) | Flip status/uat → status/completed |
| #2599 G47 | Blueprint Release HEAD~1 detect skips stacked commits | NO (CI tooling, not substrate) | Separate CI workstream |

3 closeable + 3 CI-tooling. Closures drop open count further.

## Open issues triage

| Issue | Status | Next action |
|---|---|---|
| #2617 G68 | in_progress | Ship Phase-0 watcher HCS quota pre-check (canonical META-AUDIT 16 fix) |
| #2526 hw36 5-Pillar EPIC | in_progress | Pillars 2-5 implementations needed (per gap analysis above) |
| #2544 deployments UX | open | Separate UX workstream |
| #2316 Shepherd EPIC | open | Long-running, separate |
| #1096 EPIC-1 Compliance | open | Long-running, separate |

## Consolidated gap list ranked by ICE (Impact × Critical-path × Urgency)

| Rank | Item | ICE | Fix shape |
|---|---|---|---|
| 1 | **G69 skopeo unavailable** (cutover Step 03 failure) | 10×10×10 | Switch step 03 base image OR static skopeo download |
| 2 | **G68 #2617 HCS quota fast-fail** | 8×9×7 | catalyst-api Go change + CI bake |
| 3 | **Pillar 3 CNPG cross-region** | 10×8×5 | bp-cnpg ReplicaCluster wiring + Cilium ClusterMesh enable + cross-region nodeSelector |
| 4 | **Pillar 5 cutover Steps 04-08 never ran** (gated by G69) | 10×9×9 | G69 fix unblocks → Steps 04-08 run → cutoverComplete annotation |
| 5 | **Verifier L7 check** (cutoverComplete + Steps Complete + native-path image + deny-egress passed) | 9×8×8 | sovereign-dod-verify.sh extension |
| 6 | **Pillar 4 Sandbox instance + MCP auto-mount validation** | 7×6×5 | Provision a Sandbox CR + walk MCP wiring |
| 7 | **Pillar 2 bcpTopology surface in deployment API** | 5×4×4 | catalyst-api API extension |
| 8 | **3 pre-existing G##s flippable** (#2588/#2590/#2595) | 3×2×9 | Already substrate-validated — close immediately |

## Recommended ship order (post-audit)

1. **NOW**: Flip #2588 + #2590 + #2595 to status/completed (3min)
2. **G69 ship** — base image swap for cutover Step 03 (highest ICE, unblocks Pillar 5 entirely)
3. **G68 ship** — HCS quota fast-fail (Go change, lead-authorized)
4. **Re-prov hw79** with G69+G68 baked → verify cutover Steps 04-08 all Complete + cutoverComplete=true
5. **Pillar 5 walk** — deny-egress hold validation
6. **Pillar 3 ship** — CNPG ReplicaCluster + ClusterMesh
7. **Pillar 4 walk** — Sandbox instance + MCP auto-mount
8. **Verifier L7 extension** — codify Pillar 5 + cutoverComplete

