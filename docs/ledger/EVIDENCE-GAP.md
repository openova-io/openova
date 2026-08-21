# UAT evidence gap — what still lacks a live hw302 screenshot

_Generated from `docs/ledger/UAT.md` on hw302 (dep `9b16ad632b906d9b`). **269 of 286 rows now carry a live hw302 in-row screenshot; 17 do not.** Tally: 269 ✅ · 10 ❌ · 1 ⚠️ · 6 ⏳._

This session moved **205 → 269 rows evidenced** across five walks (console/wire re-walks · §A catalog+wizard ·
§B customer funnel E2E on two Orgs · the re-triage that recovered vcluster-isolation + MCP · the authorized-action
walk: handover-token mint, KC-admin, throwaway-Org delete cascade, gitea pod-restart, helm render). **Every merged
screenshot was independently re-verified against live cluster state** (vClusters 1/1, CNPG clusters Ready, MCP
`list_applications`=14 apps=14 CRs, Org-delete cascade confirmed by `kubectl … NotFound`, handover `/dashboard`
landing confirmed by the PNG). No fabrication survived; no genuinely-failing row was forced green.

The 17 that remain are genuinely blocked — a deploy-gated fix, a destructive/one-way action, or a real UI limitation.

---

## The 17 remaining — final root-caused map

### A. Real defect / live-engineering gap (8)
| Row(s) | Verdict | Why | Unblock |
|---|---|---|---|
| G8 220 222 | ❌ | agenity StatefulSet 0/1 — `exceeded quota: plan-quota` (uatco is **plan-S** 4Gi/2cpu; per-Org keycloak 2Gi + MCP + oidc-gate fill it). **NOT a cert issue** (wrong-host false-negative; `console.uatco.omani.homes` serves 200, `agenity-anthropic-token` seeded, oidc-gate 1/1). Tracked **#5393**. | Budget plan-S quota for the mandatory Pillar-4 agenity workspace (or gate agenity to plan-M+), then walk `agenity.<org>.omani.homes` |
| M4 | ✅→needs-shot | Same plan-S quota block — the agenity pod never schedules, so ghcr image-pull is never exercised | Same fix (#5393) |
| 20 98 102 105 | ✅ | The `/dashboard` treemap is fed **solely** by `/api/v1/deployments/<id>/jobs?inventory=full` — job-execution records with **no vcluster/org/workload attribution**, so Layer-1=vCluster/Organization collapses to a single host bucket and cannot render per-Org blocks or a distinct customer estate. Isolation is real (proven by 5/9/99/103/M3/238); this surface just can't render it. | Feed the treemap a workload/placement source with org+vcluster attribution (relates #3642/#3687) |

### B. Needs a destructive / one-way action on this env (7)
| Row(s) | Why | Unblock |
|---|---|---|
| G12 228 | region-kill (Pillar-3) / wipe+re-prov orphan-VPC check — destructive, not run for a screenshot (G12 was proven e2e on hw292) | Authorize + run |
| 163 164 | Cutover NOT fired on hw302 (pre-cutover); no `cutoverComplete` state | Fire the 11-step cutover (one-way) |
| 174 175 | hw302 has zero failed job rows → the Re-run-on-Failed control can't be shown without a vacuous absent-button pass | Inject a genuinely failed job |
| G7 | vcluster dual-door: funnel door → vcluster Org proven (walkstrangertwo); the admin-create door needs a full new-Org provisioning cycle (already ✅ hw301-2026-08-20, deprioritized as stretch) | Walk the admin-create door to a vcluster Org |

### C. Deeper source / pod-restart (2)
| Row(s) | Why |
|---|---|
| R18 R20 | R18 = handover-key self-publish guard (Sovereign doesn't re-publish its handover key on restart); R20 = deploy-bot bumps image pins per-line — both need a restart/source-level investigation, not a single-screenshot surface. |

---

## Finding surfaced by the authorized-action walk (worth a fix)

**Org-delete leaves external identity residue (delete-cascade gap).** Deleting `organization walkstrangerone`
cascaded the K8s surfaces cleanly (ns/app/DNS/vCluster-sts all `NotFound` — R17/107 clauses pass, kubectl-verified),
but the Org CR carried **only** the `orgs.openova.io/tenant-networking` finalizer, so **two external identity tethers
persist as orphans**: the gitea org dir `/walkstrangerone` and the Keycloak group `/walkstrangerone`. This is a
sibling of the org-controller delete-cascade leak (#924/#4250) — the CR-delete path should also sweep the gitea org +
KC group. Stamped transparently into row R17's Evidence; the residue is harmless on our own env but the finalizer
should be extended.

---

### The honest ceiling — reached

**269/286 rows (94%) carry a live hw302 screenshot.** The 17 remaining are not screenshot-able on this env's
current state without engineering or a destructive/one-way action:
- **§A (8):** the agenity plan-S quota (#5393 → G8/220/222/M4) and the treemap's missing org-attribution (20/98/102/105).
- **§B (7):** region-kill/wipe, cutover-not-fired, no-failed-jobs, the admin-door dual-walk.
- **§C (2):** handover-key-republish + deploy-bot source walks.

**True 286/286 needs the live engineering**, ICE-ordered:
1. Fix the plan-S quota for agenity (#5393) → the 3 genuine ❌ (G8/220/222).
2. Give the treemap an org/vcluster-attributed data source → 20/98/102/105 (#3642/#3687).
3. Fire the cutover → 163/164; run a controlled destructive walk → G12/228/174/175.
4. (Bonus) extend the org-delete finalizer to sweep gitea/KC identity tethers (#924/#4250 sibling).
