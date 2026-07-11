# Completion matrix + month-cycle retrospective — 2026-07-12

> Produced for the founder's direct ask: *"show me the complete matrix … which epic is in which completion percentage … the ones which are still not 100% — what is still wrong … what are you going to do differently?"*
> Method: 5 read-only forensic agents (canonical UAT parse, 89-issue blocker map, 34-day git/session forensics, dispatch-quality self-audit, hw242 live gap scan), every claim re-verified against live state or file evidence before inclusion. Sources: `origin/main:docs/ledger/UAT.md` @ `502ba76d6`, `gh` issue/PR state 2026-07-12, `docs/sessions/`, auto-memory, live hw242 (dep `f05b0718dac62fe9`).

## 1. Per-EPIC completion (281 ledger rows; pct = ✅/(total−⛔−N/A))

| EPIC family | tot | ✅ | ⚠️ | ◑ | ☐/⏳ | ❌ | ⛔ | pct | What is still wrong (root cause) |
|---|---|---|---|---|---|---|---|---|---|
| Console/UI/Cloud-view | 91 | 75 | 14 | 1 | 1 | 0 | 0 | **82%** | #4896 Edit-IaC Commit 400 name-mismatch (rows 148/154; unstarted — #4997 fixed only the read path); #4889 apps-grid renders healthy spine apps FAILED; recon/model detail rows ⚠️ |
| Multi-region/DR | 57 | 38 | 5 | 0 | 1 | 0 | 13 | **86%** | #4923 DR replication-status endpoint returns a SYNTHESIZED placeholder (rows 51/52/56/188/189; unstarted); #4901 no standby-absent condition; #5012 (new, hw242): region-B bootstrap stalled post-CNI → singleton shared-pg + fabricated dr-spine Healthy |
| Sovereignty-Cutover | 10 | 6 | 3 | 1 | 0 | 0 | 0 | **60%** | #3379 `cutoverComplete` never yet achieved on kom4dc 2-region — in flight on hw242 (chart 0.1.120, all 8 known wedge fixes aboard); #5011 (new, live-healed): step-01 `--mirror` prunes sovereign-local gitea branches |
| Marketplace+Funnel | 48 | 24 | 18 | 0 | 1 | 2 | 3 | **53%** | all named fixes merged (#4991/#4993/#4995/#5002/#5004); the 18 ⚠️ are code-done-awaiting-walk; hw242 walk unblocked by the #5011 branch heal |
| Robustness/Ops | 23 | 11 | 8 | 0 | 0 | 0 | 3 | **58%** | merged-not-walked majority; #4677 durable EVS drain unverified |
| Governance | 6 | 3 | 3 | 0 | 0 | 0 | 0 | **50%** | adoption rows gated on #4788 (one-line ProviderConfig apiVersion fix, never shipped) |
| SSO/Auth | 33 | 10 | 3 | 1 | 19 | 0 | 0 | **30%*** | *19 ☐ = mechanical UNVERIFIED flip from the cb87fdd merge; they were ✅ on hw241 — re-stamp only. #4913 closed 2026-07-12 on live hw242 wire evidence |
| Agentic/Sandbox | 13 | 2 | 4 | 0 | 0 | 7 | 0 | **15%** | founder-gated: #4277/#4111 await the founder's Anthropic credential; zero engineering remains per the issues' own audits |
| **Overall** | **281** | **169** | **58** | **3** | **22** | **9** | **19** | **64.8%** | best-ever observed: 67% (hw241) |

Treemap note (recurring phantom): rows 99–107 are all ⛔ RE-BASELINED (❌→⛔, #3642/hw224 superseded); row 17 ✅ via #4731. Source audit 2026-07-12 confirms all four historically-named treemap defects map to landed, test-locked work (#3687/#3692 Job-owned exclusion `dashboard.go:288-335`; #3646 reconciler ingestion; #934 locked-design `TreemapLayerController`; #4731 progress layers `treemap.types.ts:40-69` + tests). One ☐ fleet row remains — a walk stamp, not a code defect.

## 2. The real remaining blocker list (everything else is walk-evidence debt)

1. **#3379** — Pillar-5 cutoverComplete (in flight on hw242; every known wedge class pre-verified absent this fire)
2. **#5012** — region-B bootstrap stall (new; RCA on issue; blocks region-kill/DR re-proof on hw242)
3. **#5011** — cutover step-01 branch prune (live-healed on hw242; durable chart fix open)
4. **#4923** — DR panel synthesized placeholder (unstarted engineering)
5. **#4896** — Edit-IaC Commit 400 (unstarted engineering)
6. **#4788** — dead ProviderConfig GVK (one-line fix, unshipped)
7. **#4277/#4111** — founder-action-gated (Anthropic credential)

Of the 89 open issues, ~54 (60%) are LIKELY-DONE awaiting close evidence; 5 duplicates (#4541→#4600, #4569→#4999, #4761→#4758, #4836→#4950, #4111↔#4277) and 5 stale (hw220/221/228-era) identified for hygiene closure.

## 3. Why 34 days without 100% — verified forensics

**33 full ledger resets in 34 days; ~140 prov attempts; ~1,270 PRs merged; median env lifetime ~0.5 day; best completion 67%.**

- **S1 — The proof is destroyed every iteration.** Reset-on-fire flushes all evidence on every prov; hw239 ("FINAL prov") was walked 9h and flushed the same day. The loop was a Sisyphus machine by construction.
- **S2 — Defect discovery serialized at the tail.** The cutover reveals exactly one new defect per fire (hw146→150 step-march; #4885→#4955→#4967→#4971→#4975→#5007 in July). No offline harness exercises all 11 steps; contract cases were each written after their live failure.
- **S3 — Fix rate outran verification.** ~35 PRs/day re-flip walked rows; merged≠deployed lag burned whole envs re-proving stale trains (cloud-init baked into catalyst-api image; deploy-bump clobbers).
- **S4 — We destroyed our own test substrate.** Janitor self-reap, the Jun-28 production deletion, hw218 wiped while converged, hw226 bricked by a live patch, hw241 killed by pod-deletion thrashing. Self-inflicted losses (~5–7 days) exceeded external ones.
- **S5 — Orchestration optimized for dispatch volume.** 7 of 13 backlog dispatches produced already-done reports; walk evidence landed on stale branches; the canon itself moved (243→281 rows).

Days lost by class: fresh-prov bootstrap defects ~9–11; cutover-only defects ~8–10; live-thrash/operator error ~5–7; external (quota/LE/capacity) ~3–5; CI-catchable ~2–4.

## 4. Dispatch-quality self-audit vs the founder's three hypotheses

- **H1 delegation via well-formed tickets: PARTIAL.** Ticket evidence/root-cause quality high (4.8/4.5 of 5) but delegation dimensions weakest (DoD 3.0, access runbook 2.3, MUST-PRESERVE 2.7 vs gold #3370 = 24/25); tickets filed 7–27 min before their fix merged — changelogs, not dispatch instruments.
- **H2 UAT avoidance in the issue lifecycle: YES.** A 19-issue mass-close (2026-07-10T02:33Z) used the template "Verified: fix PR is MERGED" — verification redefined as merge, contra TRUST.md and merged≠deployed≠verified. Walks happen at scale, but closure was decoupled from them. Remedy in force: closes only on live evidence (#4913 pattern).
- **H3 duplicated/ad-hoc subagent work: YES on rework, NO on irrelevance.** The registry-pivot family: 3 issues, one root cause, 9+ PRs in 48h, two successive "definitive" fixes before #5010's systemic sweep; 30+ issues on the harbor-prewarm family since May 27. All pillar-aligned — the waste was rework, not misdirection.

## 5. What changes (in force as of this session, not promised)

1. **Cutover-FIRST ordering** — the riskiest env-mutating step runs before the walk. Already paid off twice on hw242: exposed #5011 (structurally hidden by walk-first) and pre-retired the hw241 DNS-wildcard killer.
2. **Close = live evidence only** (#4913 is the template); merged-but-unclosed backlog closes ride walk evidence, batched per surface.
3. **One systemic RCA per symptom-wave; consolidated tickets** (#5012 = one issue + checklist, not four).
4. **No thrash, no wipe-before-forensics** (hw242 region-B gets RCA, not a reflexive re-fire; hw241 lesson recorded in memory).
5. **Tickets carry access runbook + binary DoD + MUST-PRESERVE** — closing the delegation gap the audit measured.

## 6. hw242 live state at time of writing

Cutover step 3/11 (harbor-prewarm 102/134, zero pull errors); steps 04–08 wedge classes pre-verified absent (wildcard NXDOMAIN, 6/6 nodes with evs.csi driver, catalyst-api off-CP, #5008 pin + #5010 sweep aboard chart 0.1.120); #5011 branches healed and holding; region-A fully healthy (62/62 applicable HRs Ready, all CNPG healthy, all endpoints 200/302); region-B bare (#5012). On cutoverComplete: close #4961/#4973/#4975 with sweep evidence, then 3-walker sweep on region-A surfaces with region-B rows honestly annotated.
