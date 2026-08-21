# UAT evidence gap — what still lacks a live hw302 screenshot

_Generated from `docs/ledger/UAT.md` on hw302 (dep `9b16ad632b906d9b`). **272 of 286 rows now carry a live hw302 in-row screenshot; 14 do not.** Tally: 269 ✅ · 10 ❌ · 1 ⚠️ · 6 ⏳._

This session moved **205 → 272 rows evidenced** across seven walks. **Every merged screenshot was independently
re-verified against live cluster state** (vClusters 1/1, CNPG Ready, MCP `list_applications`=14 apps=14 CRs,
Org-delete cascade `kubectl … NotFound`, handover `/dashboard` PNG, Jobs Re-run gating PNG+kubectl, cutover
group-status PNG+configmap). No fabrication survived; three separate "blocked" mis-categorisations were caught and
corrected by actually looking (vcluster/MCP, then 174/175, then 164) and one honest defect was surfaced (163).

The 14 that remain need a deploy-gated fix, a destructive/one-way action, or a real UI defect fix.

---

## The 14 remaining — final root-caused map

### A. Real defect / live-engineering gap (8)
| Row(s) | V | Why | Unblock |
|---|---|---|---|
| G8 220 222 | ❌ | agenity StatefulSet 0/1 — `exceeded quota: plan-quota` (plan-S 4Gi/2cpu; per-Org keycloak 2Gi + MCP + oidc-gate fill it) AND a kyverno webhook denial on the 0.5.31 Helm upgrade. **Not a cert issue.** Tracked **#5393** (fix now scoped there: 3 options — raise plan-S quota / shrink-or-share the per-Org keycloak / gate agenity to plan-M+). | #5393 fix + a fresh prov |
| M4 | ✅ | Same block — agenity pod never schedules, ghcr pull not exercised | #5393 |
| 20 98 102 105 | ✅ | The `/dashboard` treemap is fed only by `jobs?inventory=full` (job-execution records, **no vcluster/org attribution**), so it can't render per-Org blocks. Isolation is real (proven 5/9/99/103/M3/238). | treemap org-attributed data source (#3642/#3687) |
| **163** | ✅ | **NEW defect (this session):** the `/jobs` JobsTable paints the 10 **never-ran** cutover steps FAILED (`result=""`, no Job) — a dishonest per-step status. The Settings→Sovereignty panel renders them correctly (pending). Posted to **#6093** (same JobsTable cutover projection). | Fix the JobsTable per-step status derivation (never-run → Pending), then re-walk |

### B. Needs a destructive / one-way action on this env (3)
| Row(s) | Why | Unblock |
|---|---|---|
| G12 228 | region-kill (Pillar-3) / wipe+re-prov orphan-VPC check — destructive (G12 proven e2e on hw292) | authorize + run |
| G7 | vcluster dual-door: funnel door proven (walkstrangertwo); the admin-create door needs a full new-Org provisioning cycle (already ✅ hw301-2026-08-20, deprioritized) | walk the admin-create door |

### C. Deeper source / pod-restart (2)
| Row(s) | Why |
|---|---|
| R18 R20 | R18 = handover-key self-publish guard (no re-publish on restart); R20 = deploy-bot per-line pin bump — restart/source-level investigations, not single-screenshot surfaces. |

---

## Findings surfaced this session (for follow-up fixes)
1. **#5393** (agenity plan-S quota) — scoped with the exact overflow arithmetic + 3 fix options + the kyverno-denial refinement. Blocks G8/220/222/M4.
2. **#6093** (JobsTable cutover projection) — added the 163 status-derivation defect (never-ran steps → FAILED instead of Pending).
3. **Org-delete identity residue** (sibling #924/#4250) — Org CR delete cascades K8s cleanly but leaves the gitea org + KC group (only the tenant-networking finalizer). Recorded in row R17's evidence.

---

### The honest ceiling — reached

**272/286 rows (95%) carry a live hw302 screenshot.** The 14 remaining need engineering or a one-way action:
- **§A (8):** agenity plan-S quota (#5393 → G8/220/222/M4), treemap org-attribution (20/98/102/105), the JobsTable status defect (163).
- **§B (3):** destructive region-kill/wipe (G12/228), the admin-door dual-walk (G7).
- **§C (2):** handover-key-republish + deploy-bot source walks (R18/R20).

**True 286/286 needs the live engineering**, ICE-ordered:
1. **#5393** plan-S quota fix (pick option 1/2/3) + reprov → the 3 genuine ❌ (G8/220/222) + M4.
2. **#6093** JobsTable per-step status fix → 163.
3. Treemap org-attributed data source (#3642/#3687) → 20/98/102/105.
4. Fire the cutover to completion / a controlled destructive walk → G12/228; the admin-door provision → G7.
