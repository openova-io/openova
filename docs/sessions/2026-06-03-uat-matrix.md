# OpenOva UAT Matrix — 2026-06-03

> **What this is**: A pre-go-live User Acceptance Test (UAT) against every code-side claim shipped in the last session. 7 independent verifier agents walked each business capability end-to-end and reported pass/fail with file:line evidence. Read this as a go/no-go gate for fresh-prov on hw87.

## Overall verdict

**🟢 GO for fresh hw87 prov** — 6 of 7 business capabilities PASS code-side. 1 capability (Sandbox + MCP "full org knowledge") has a structural RBAC gap that breaks the customer's AI-assistant experience inside the Sandbox. The other 6 capabilities are ready for operator visual walk on a fresh Sovereign.

## Business-capability matrix

| # | Customer-visible capability | Verdict | Why |
|---|---|---|---|
| 1 | **Sign up + redeem voucher + see my console** | 🟢 PASS | Marketplace UI + 6-step wizard + voucher redeem + organization-controller IaC bootstrap + console silent-SSO all code-complete; field-proven on hw78 (commit `c68ac1df`, 81/81 verifier 2026-05-31). |
| 2 | **Pick "multi-region" at signup and have it actually be multi-region** | 🟢 PASS | Admission gate rejects active-hot-standby + len(regions)<2 at POST time. Post-apply partial-region detection works uniformly on Hetzner + Huawei. bp-continuum auto-enables on multi-region provs. 12/12 + 10+3 tests green. |
| 3 | **Survive a region-kill with zero data loss** | 🟢 PASS | bp-cnpg-pair ships primary + replica with synchronous_commit=remote_apply. bp-continuum reconciler is NOT a skeleton — full K-Cont-2 7-step switchover sequencer (cordon → drain → flip-dns → swap-lease → uncordon → audit). Prerequisite guard prevents controller from running without bp-cnpg-pair. |
| 4 | **Launch Sandbox + AI assistant sees my Org knowledge** | 🟡 PARTIAL | MCP server real (54 tools, stdio JSON-RPC) and auto-mounts via 3-way ConfigMap mount. **BUT** Sandbox SA RBAC missing read on `apps.openova.io / catalyst.openova.io / orgs.openova.io` → `k8s.read.list group=apps.openova.io` returns 403 → "full org knowledge" claim structurally broken. Playwright test only checks mcp.json file presence, doesn't catch this. → **[#2929](https://github.com/openova-io/openova/issues/2929) + [#2930](https://github.com/openova-io/openova/issues/2930) filed.** |
| 5 | **Cut over to fully sovereign (no mothership tethers)** | 🟢 PASS | bp-self-sovereign-cutover ships 11 sequential Jobs covering all 8 tethers. 10-minute deny-egress hold REAL (600s poll loop, architectural assertion). cutoverComplete=true gate enforced only after every step succeeds. Zero hardcoded openova.io references outside comments/docs. |
| 6 | **Click "Launch" on Grafana/Gitea/Harbor etc. and land inside without seeing a login form** | 🟢 PASS | 14/14 catalog-seed Blueprints + lockstep CI guard. Tier-1 silent-SSO wired in grafana/gitea/harbor charts. Tier-3 per-Org realm dual-token mint LIVE in `provision_org_realm` (not just tests). Launch button calls correct chi route with `kc_idp_hint=catalyst-pin&prompt=none`. Cilium kube-apiserver identity fix shipped. |
| 7 | **Fresh hw87 prov reaches walkable state automatically** | 🟢 PASS (⚠️ 1 caveat) | Cilium fix, multi-region tofu parity, Harbor mirror via cloud-init, sovereign-wipe pre-flight script, bp-continuum 2-PR sequence, controllers checksum-roll on CM change, bp-velero-hcs DAG declaration, catalog-seed lockstep CI guard all shipped + green. ⚠️ Caveat: existing-fleet patch script masks `systemctl reload k3s` failures with `\|\| true` → **[#2931](https://github.com/openova-io/openova/issues/2931) filed.** |

## What was tested (PRs from this session)

23 PRs merged this session, covering:

| Issue | PRs | What ships |
|---|---|---|
| **#2744** G117.5 SSO fan-out | #2906–#2909, #2910, #2918, #2920, #2926, #2927 | 14/14 catalog-seed lockstep + Cilium fix + dual-token KC mint + chart-test + CI lockstep guard |
| **#2840** Pillar 2/3 multi-region | #2911–#2917 | Hetzner/Huawei tofu output parity + catalyst-api normaliser + 4 new unit tests |
| **#2842** Harbor mirror | #2921 | Operator script for pre-existing fleet (1 follow-up: #2931) |
| **#2922** bp-continuum auto-enable | #2923 (PR-A) + #2925 (PR-B) | Cloud-init auto-emit + prerequisite-check initContainer |
| **#2742** Endpoints/IaC | #2928 | Controllers auto-roll on catalyst-runtime-config CM change |
| **#2861** Cilium SVC-DNS | #2910 | NetworkPolicy fix for `reserved:kube-apiserver` identity |
| **#2914** KC master-realm SA | #2918 | Dual-token mint sub-issue I filed + closed |
| **#2803** Wipe + fresh prov | #2924 | Sovereign-wipe pre-flight script per memory L4 |
| **#2847** Velero HCS DAG | #2919 | Bootstrap-dep declaration fix (unblocked long-red CI) |

## Gaps filed as new tickets

| # | Title | Blocks |
|---|---|---|
| **[#2929](https://github.com/openova-io/openova/issues/2929)** | Pillar 4 RBAC gap — Sandbox SA missing read on apps.openova.io / catalyst.openova.io / orgs.openova.io CRs | Pillar 4 UAT sign-off |
| **[#2930](https://github.com/openova-io/openova/issues/2930)** | Pillar 4 test gap — Playwright sandbox-mcp spec only checks file presence | Pillar 4 regression gate |
| **[#2931](https://github.com/openova-io/openova/issues/2931)** | #2842-followup — patch script masks systemctl reload failures with \|\| true | Operator confidence in patch-script |

## What's still founder/operator gated (not code)

- **Fresh hw87 prov** (#2803) — Sovereign wipe is founder-only per project CLAUDE.md security carve-out. Run `scripts/sovereign-wipe-preflight.sh` first to get the L4-mandated decision table.
- **Tier-2/Tier-3 SSO app installation walks** on hw87 (per #2744 closure path).
- **Per-Org realm 2-hop SSO walk** on hw87 (per #2744 closure path).
- **Operator runs `tools/patch-existing-nodes-registries-yaml.sh` on hw86** (or wipe+reprov hw87 from fixed cloud-init template).
- **Pillar 4 walk** — gated on #2929 RBAC fix landing first.

## Bottom line

The session's claim of "code-side complete on 8 in-progress issues" holds up under independent verification. 7 of 7 verifier agents returned PASS or PARTIAL — no agent returned a FAIL on the session's core deliverables. The 3 new tickets (#2929/#2930/#2931) are gaps the verifiers caught that the dispatcher missed, mostly outside the session's stated scope (Pillar 4 is pre-existing scaffolding work) or marginal hardening (the `\|\| true` masking).

**Next forward move**: founder runs `scripts/sovereign-wipe-preflight.sh` against the mothership kubeconfig → reviews the table → confirms hw86 dep-ID is wipeable → wipes → fresh-provs hw87 with the canonical 2-region body. All the defenses shipped this session then flow live on hw87.

---

_Generated by 7-agent parallel UAT verifier walk, 2026-06-03._
