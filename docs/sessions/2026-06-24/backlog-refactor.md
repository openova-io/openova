# Backlog refactor — 2026-06-24 (product-owner cleanup)

Founder mandate: produce a verdict for EVERY open issue (~76) and act on the clear-cut ones with cited evidence. Not feature work — dedup / obsolete / merge / close-done / keep.

**Evidence sources:** merged-PR search (`gh pr list --state merged --search "#N"`), the freshly-merged 215-row god-mode walk in `docs/ledger/UAT.md` on main (182 ✅ · 37 ❌ · 3 ⛔), `git grep` against `origin/main`, and live HTTP checks on the permanent omantel.biz Sovereign (console 200, marketplace 200).

**Verdict legend:** DONE (close, cited) · DUP-OF #X (close→canonical) · OBSOLETE (close, why) · MERGE (fold into epic) · KEEP (genuinely open) · PARKED (agenity/chepherd — founder parked, do NOT touch).

## Master verdict table

| # | Verdict | Canonical / Evidence |
|---|---|---|
| 3374 | KEEP | SSO epic. UAT rows 35/36/68/115/217 ✅ but the per-app regression-probe + ~11-app sweep is not fully walked (rows mixed). Real, distinct, open. |
| 3376 | KEEP | FUNNEL customer-journey epic. Front-half ✅ (rows 83-85, 216) but terminal app-RUNNING ❌ (rows 86/89/90). Distinct from #4179 (redirect E2E) — #3376 owns the "purchased app actually Runs" half. |
| 3379 | KEEP | SOVEREIGNTY cutover. UAT rows 162-166 ❌ (cutover group/step rows don't render honest status / re-run). Genuinely open. |
| 3383 | DONE | Machinery rename + CI naming guard. PRs #3707/#3801/#3822/#3882/#3967. CI guard `scripts/check-no-persona-machinery.sh` + `.github/workflows/naming-guard.yaml` live on main. UAT rows 116-122 ✅ (117 ✅ live). Canonical for machinery-rename. |
| 3581 | KEEP | UAT-set re-anchoring discipline (ongoing). Owns keeping UAT.md honest per-env — a standing process ticket, not a one-shot. Sibling of #4181. Keep one of {3581,4181} — see #4181 row. |
| 3724 | DONE | Dedicated `.github/workflows/cloud-init-size.yaml` on main with per-provider uniquely-named checks (`cloud-init-size / hetzner|huawei`) — exactly the spec. Marking it required-in-branch-protection is a GitHub-settings click outside the repo. |
| 3740 | KEEP | bp-cnpg-pair cross-region replica streams ASYNC (RPO≠0). No fix PR landed. Real Pillar-3 gap, distinct. |
| 3744 | DONE | Funnel double-fire dedup. PRs #3798 + #3885 (unit test) + #3899 merged — `uniq_inflight_tenant` dedup + stale-inflight reap on main. |
| 3785 | DONE | Purchased WordPress image kyverno-DENIED → Harbor-proxied. PRs #3788 + #3880 merged (proxy purchased + per-Org DB-backing images through Sovereign Harbor). |
| 3824 | OBSOLETE | hw165 (dep 9ee307969c92452e) — WIPED throwaway env. Premise (mgmt-vCluster fromHost.secrets missing rtz/*) superseded by the #3878/#3952/#4159 native-DB-secret-delivery line on the permanent env. |
| 3829 | KEEP | "Every app shows TRUE live state" invariant epic (supersedes #3687/#3375/#3646/#3828). The anti-scoping-hole owner. Distinct, open. |
| 3859 | DONE | sme→org-services rename orphaned-refs + per-Org vCluster defects. All 5 gaps on main; PRs #3860/#3882/#4103/#4140. Funnel reached Ready (walkorg). |
| 3878 | DONE | 4 pod-mounted DB secrets delivered natively into vc-mgmt. PRs #3879 (bp-postgres 0.2.4 / bp-mgmt-vcluster 0.2.24) + #4159 merged. |
| 3888 | DONE | Thin cloud-init: evict powerdns-api-credentials to Flux + revert #3884 over-cap. PR #3890 merged. |
| 3889 | DONE | hw168 disk-pressure orphan. PR #3893 (disk auto-prune CronJob + pre-fire preflight) merged — same fix as #3892. |
| 3891 | DONE | prov-time openbao-KV-seed mechanism. PR #3900 (bp-openbao 1.2.51) merged. |
| 3892 | DONE | Mothership disk self-maintenance + pre-fire preflight. PR #3893 merged. |
| 3895 | DONE | Jobs lifecycle label provider-accurate (Huawei ≠ "Provision Hetzner"). PR #3897 merged; `provisioner_display_test.go` on main proves the fallback logic. This-session DONE candidate, code-complete + tested. |
| 3896 | DONE | In-cluster convergence Jobs invisible during phase1-watching. PR #3897 (mother-side in-cluster reconciler ingestion) merged. |
| 3898 | DONE | Crashed/rolled provisioning Pod strands in-flight row. PR #3899 (reap stale in-flight provision) merged. |
| 3905 | DONE | Topology placement-edit dropdown contradicts matrix (openbao). PR #3940 merged (derives mode descriptions from TOPOLOGY_BY_ID matrix). |
| 3914 | KEEP | Provisioning UX: fill the ~30-min "Provision: Success" void with a live "Bootstrapping cluster" phase. No fix PR. Real product gap; overlaps #3925 convergence-monitor but distinct (the timeline-void specifically). |
| 3925 | DONE | Convergence Monitor redesign (2 surfaces + dashboard + readiness chip). PRs #3930/#3935/#3936/#3937 merged. UAT row 192 ✅. |
| 3926 | DONE | (already CLOSED) step-02 harbor-projects NXDOMAIN. PR #3927 merged. Listed for completeness. |
| 3944 | KEEP | step-03 harbor-prewarm stalls post-#3642. PR #3945 (--command-timeout) merged BUT the "host kubectl can't see in-vCluster openova-io images" half is post-#3642 and not UAT-walked on a cutover. Keep pending a cutover walk. |
| 3952 | DONE | #3642 secret+service cascade. PRs #3953/#4065 merged (harbor/seaweedfs/valkey/gitea-pg cross-boundary delivery). |
| 3955 | DONE | Route private OpenOva images through Harbor cache (kill /etc/hosts band-aid). Superseded-and-delivered by the #4048/#4049 bastion-Harbor-warmup line (every-image Huawei→Huawei). Direct-ghcr hole closed. |
| 3969 | KEEP | EPIC Application-centric Placement (delete declared/observed/effective/DR). In-flight, owned by another agent. |
| 3970 | DONE | Unified Cloud view lens=chip-set redesign. PR #3973 merged + code on main (lens chip-set, rich list, reconcilers). Already status/completed. Canonical for cloud-view. |
| 3978 | DONE | RESTORE rich per-kind list. PR #3981 merged; full cloud-list/ per-kind tree on main. Already status/completed. |
| 3979 | DUP-OF #3980 | Identical 4 graph fixes (default=graph, converge-then-stop, ArchiMate→Legend, 100% canvas). No own PR; landed via #3980's PR #3983. #3980 is canonical (has PR + completed). |
| 3980 | DONE | (already CLOSED) Cloud graph 4 fixes. PR #3983 merged + all 4 on main. |
| 3985 | DONE | Broad sme/tenant all-files sweep (MCP prereq). PRs #3995/#4017/#4039/#4043/#4141. UAT rows 214/215 ✅. Sibling of #3383 (not a clean line-dup; #3383=machinery+guard, #3985=broad sweep) — both landed. |
| 3988 | DONE | EPIC OpenOva MCP server. PR #4007 (first slice) + the RBAC-scoped tools. UAT rows 218-223 ✅ (live agent created a real Application via the RBAC-scoped openova-mcp). Epic delivered + walked. |
| 3996 | KEEP | ArgoCD-like recon management. UAT rows 193-194 ✅ but 195-197 ❌ (logs/reconcile/suspend 404) — the live blocker is #4193. Keep until #4193 lands. |
| 4000 | DONE | False topology singleton on multi-region. PRs #4001/#4015/#4021 merged. UAT rows 188-189 ✅. |
| 4002 | KEEP | Crossplane adoption seam. PR #4006 shipped scaffolding (XRD/Composition/generator/provider manifest) BUT live `get providers`→0, `get managed`→none; UAT rows 206/207 ⛔ feature-not-built. Genuinely open (built-but-inert + Huawei half unaddressed). |
| 4003 | DONE | /fleet/applications returns 0. PR #4005 merged (source from live HelmReleases). UAT row 205 ✅ (178 apps live). |
| 4010 | PARKED | EPIC bp-chepherd — agenity/chepherd. Founder parked; do NOT touch. |
| 4018 | KEEP | Crossplane Path B (provisioning instantiates XRCs) — the deferred heavier path. Body says Path A (#4019) shipped; Path B awaits founder A/B steer. Near-sibling of #4002; keep. |
| 4019 | DONE | This issue IS the merged Path-A PR (#3998) — front-door LB from deployment record. UAT rows 203/204 ✅. Close. |
| 4046 | DONE | Huawei janitor SG-leak + EIP-egress cooldown preflight. PR #4047 merged. |
| 4060 | DONE | Customer-Org CNPG pair hardcodes hcloud-volumes (Pillar-3 dead on Huawei). PR #4064 (provider-aware storageClass) merged. |
| 4061 | DONE | Permanent Sovereign auto-fires cutover on handover. PR #4063 merged (default CATALYST_FIRE_CUTOVER_ON_HANDOVER=false). Permanent env never auto-fired since. |
| 4069 | DONE | byo-api/PowerDNS DNS-write ignores ConsoleLoadBalancerIP. PR #4070 merged (route console/api/marketplace at console-LB IP in PowerDNS parent-zone write). |
| 4071 | DONE | catalyst-api can't watch Applications → Apps view PENDING. PR #4072 merged (disable WatchListClient gate); main.go boot wires disableWatchListClient. Downstream Apps-view rows 15/19 ✅ live (60 cards). |
| 4073 | DONE | "No pool parents available" blocked sub-Org creation. Resolved on permanent env: org-create form renders + UAT rows 8 + 216 ✅ (user creates Org via form/funnel; pool parents present on omantel.biz). |
| 4079 | PARKED | agenity-rtz-A RFC-1123 HR name — agenity install path. PR #4080 merged but agenity is founder-parked; do NOT touch. |
| 4084 | KEEP | Cloud graph settle-regression + list k9s color. PR #4090 merged (conditional re-warm gate + k9s tones) — distinct follow-up to #3980's regression, NOT a dup. status/uat awaiting walk. Keep until walked. |
| 4085 | KEEP | Per-resource Compliance tab renders own findings. PR #4087 merged but no ✅ UAT row confirming the live table. Keep until walked. |
| 4086 | DONE | Sovereign "Degraded" forever on healthy Huawei. PR #4088 merged (exclude provider-inapplicable HRs from roll-up). |
| 4089 | DONE | Re-home Parent Domains as #parent-domains settings section. PR #4093 merged. |
| 4091 | KEEP | Replace bare YAML/IaC rendering with shared code-editor. PR #4092 referenced but the CodeView.tsx rollout is broad; no ✅ row. Keep. |
| 4111 | PARKED | bp-agenity spawned agent can't auth/run — agenity. Founder parked; do NOT touch. |
| 4143 | KEEP | bp-cnpg multi-operator webhook-cert conflict (x509 blocks NEW Org CNPG). No fix PR. Real, distinct. (Related to the demo-Org webhook-hijack incident but the durable per-Org-vs-cluster-singleton fix is unshipped.) |
| 4158 | KEEP | Secondary-region mgmt-vCluster keycloak FailedMount. PRs #4159/#4162 in flight; status/in-progress; region-B still converging. Keep. |
| 4167 | DONE | Marketplace storefront leaks 'Tenant'. PR #4168 merged; index.astro/ReviewStep.svelte/AppDetail.svelte clean on main. (Residual CheckoutStep.svelte strings noted as a follow-up tail, out of #4167's named scope.) |
| 4170 | MERGE→#4196 | Narrow voucher-gate fix (drop showback gate + nav link), PR #4172 merged. #4196 is the canonical native-Billing-menu epic that SUPERSEDES the ad-hoc surface this patched. Fold as a done sub-row of #4196. |
| 4176 | DONE | P0 org-create redirect → pool domain. PRs #4177 + #4184 merged + rolled live; UAT rows 83-85 ✅, redirect lands on console.<slug>.<pool> (f4179walk.omani.works). Canonical funnel-E2E owner = #4179 (active). |
| 4179 | KEEP | FUNNEL E2E epic — active in-flight (other agent owns). Do NOT touch. Canonical for the redirect/E2E journey. |
| 4180 | PARKED | AGENITY new-dashboard durable install — agenity. Founder parked; do NOT touch. |
| 4181 | KEEP | UAT-215 drive-every-row-to-✅/❌ — active in-flight; the 215-walk that produced current UAT.md. Distinct from #3581 (set-wide re-anchoring discipline) by being the one-shot 215 drive. Keep both; do NOT touch (active). |
| 4182 | KEEP | SECURITY P0 token in URL query-string. status/in-progress — active in-flight (other agent). Do NOT touch. |
| 4186 | KEEP | Per-Org console session-handoff /api/auth routing on single-domain Sovereign. status/in-progress — active in-flight (Refs #4179). Do NOT touch. |
| 4187 | KEEP | Sidebar avatar reads generic 'User' (getMe calls org-mode /auth/me). status/in-progress. No merged fix. Real, distinct (UAT row 27). Keep. |
| 4188 | KEEP | Reap stale duplicate vc-demo StatefulSet. No fix PR. Real provisioning-cleanup gap (non-blocking, rows 9-12 pass off the live vcluster). NOT an agenity feature issue. Keep. |
| 4193 | KEEP | recon-tab actions/logs 404 on chrooted console (tofu-id vs console-id). No fix PR. The active blocker behind #3996 rows 195-197 ❌. Keep. |
| 4196 | KEEP | EPIC native Billing menu (voucher inside it). The canonical billing surface; absorbs #4170. Keep open as the epic. |

## Execution plan (closes with cited comments)

**Close as DONE (16):** 3383, 3724, 3744, 3785, 3878, 3888, 3889, 3891, 3892, 3895, 3896, 3898, 3905, 3925, 3952, 3955, 3985, 3988, 4000, 4003, 4019, 4046, 4060, 4061, 4069, 4071, 4073, 4086, 4089, 4167, 4176, 3859.

**Close as DUP-OF:** 3979 → #3980.

**Close as OBSOLETE:** 3824 (wiped hw165 env, premise superseded).

**Close as MERGE:** 4170 → fold into #4196 (canonical billing epic).

**KEEP (real open backlog):** see KEEP rows above.

**PARKED — do NOT touch (agenity/chepherd + active in-flight):** 4010, 4079, 4111, 4180 (agenity); 3969, 4179, 4181, 4182, 4186 (active in-flight).
