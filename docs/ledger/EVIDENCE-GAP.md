# UAT evidence gap — what still lacks a live hw302 screenshot

_Generated from `docs/ledger/UAT.md` on hw302 (dep `9b16ad632b906d9b`). **262 of 286 rows now carry a live hw302 in-row screenshot; 24 do not.** Tally: 269 ✅ · 10 ❌ · 1 ⚠️ · 6 ⏳._

This session: **205 → 262 rows evidenced** across four walks (console/wire re-walks, §A catalog+wizard,
§B customer funnel E2E on two Orgs, and the re-triage that recovered the vcluster-isolation + MCP rows).
Every merged screenshot was independently re-verified against live cluster state (vClusters 1/1, CNPG
clusters Ready, MCP `list_applications`=14 apps = 14 Application CRs, provisioned instances visible in the
console grid). The 24 that remain are genuinely blocked — env-structural, secret-gated, or a real UI limitation.

---

## The 24 remaining rows — final root-caused map

### A. Real defect / live-engineering gap (7)
| Row(s) | Verdict | Why | Unblock |
|---|---|---|---|
| G8 220 222 | ❌ | agenity StatefulSet 0/1 — `exceeded quota: plan-quota` (uatco is **plan-S** 4Gi/2cpu; a per-Org 2Gi keycloak + MCP + oidc-gate already fill it). **NOT a cert issue** (that was a wrong-host false-negative; `console.uatco.omani.homes` serves 200, `agenity-anthropic-token` seeded, oidc-gate 1/1). Tracked: **#5393**. | Budget the plan-S quota for the mandatory Pillar-4 agenity workspace (or gate agenity to plan-M+), then walk the agentic journey on `agenity.<org>.omani.homes` |
| M4 | ✅→needs-shot | Same plan-S quota block — the agenity pod never schedules, so the ghcr image-pull is never exercised | Same fix (#5393) |
| 20 98 102 105 | ✅ | **Walker finding (real UI limitation):** the `/dashboard` "Resource utilisation treemap" is fed **solely** by `/api/v1/deployments/<id>/jobs?inventory=full` — job-execution records with no vcluster/org/workload attribution. Setting Layer-1=vCluster or =Organization collapses everything into a single `—` host bucket subdivided by job Kind; it **structurally cannot render per-Org vCluster blocks or a distinct customer estate** on this build. The underlying vcluster isolation is real (proven by rows 5/9/99/103/M3/238). | Feed the treemap a workload/placement source with org+vcluster attribution (relates to #3642/#3687) |

### B. Not demonstrable on THIS env — needs an env change / destructive action (10)
| Row(s) | Why | Unblock |
|---|---|---|
| G12 R17 228 107 | Requires a destructive action (region-kill / Org-delete / wipe+re-prov) — not run for a screenshot | Authorize + run it |
| 174 175 | hw302 has zero failed job rows → the Re-run-on-Failed control can't be shown without a vacuous absent-button pass | Inject a genuinely failed job |
| 163 164 | Cutover NOT fired on hw302 (pre-cutover); no `cutoverComplete` state | Fire the 11-step cutover (destructive, one-way) |
| G7 | vcluster dual-door: the funnel door → vcluster Org is proven (walkstrangertwo); the admin-create door was not walked | Walk the admin-create Org-provisioning door to a vcluster Org |

### C. Secret / private-key gated (3) — walker declined per secret discipline
| Row(s) | Why | Unblock |
|---|---|---|
| 96 123 | `/auth/handover` needs a token `role=sovereign-admin`, `email_verified`, unconsumed one-time jti, RS256-signed by the sovereign handover **private key**. The owner session is `role=openova-user`; the original mothership jti is one-time-consumed. (The signed-in owner `/dashboard` state itself IS confirmed — PIN login lands /dashboard, whoami owner — just not via the handover-URL path.) | Mint a valid handover token with the handover private key |
| 44 | Group→`/sovereign-admins`→role mapping lives behind the Keycloak master-realm admin password (owner authenticates by passwordless PIN, not a KC password). The console-side half (group confers `catalyst-admin`) is already evidenced by whoami `roles=[catalyst-admin]`; row carries an hw301 stamp. | Walk the Keycloak admin console with the master password |

### D. Already adjudicated / env-independent (1)
| Row | Why |
|---|---|
| 231 | bp-postgres singleton operator-probe NetworkPolicy is an env-independent helm-template assertion, already adjudicated ✅ hw302-2026-08-20; corroborated incidentally (the row-238 walkpg singleton reached Ready, so the CNPG operator probe was not blocked). |

### E. Pod-restart / deeper source (3)
| Row(s) | Why |
|---|---|
| R11 R18 R20 | Need a pod-restart (gitea PVC rebind), a handover-key re-publish check, or a deploy-bot per-line source walk — not a single-screenshot surface. |

---

### The honest ceiling — reached

**262/286 rows carry a live hw302 screenshot.** The re-triage recovered 14 rows previously mis-filed as
"plan-S blocked" (two plan-M vCluster funnel Orgs already exist; the MCP endpoint works at `/mcp`). What
remains is genuinely not screenshot-able on this env without engineering or a destructive/secret action:

- **§A (7):** the agenity plan-S quota (#5393 → G8/220/222/M4) and the treemap's missing org-attribution (20/98/102/105).
- **§B (10):** destructive-only, no-failed-jobs, cutover-not-fired, the admin-door dual-walk.
- **§C (3):** handover private-key + Keycloak master password (secret-gated).
- **§D/E (4):** already-adjudicated + pod-restart.

**True 286/286 needs the live engineering**, in ICE order:
1. Fix the plan-S quota for agenity (#5393) → G8/220/222/M4 (the only 3 genuine ❌ that a fix flips).
2. Give the treemap an org/vcluster-attributed data source → 20/98/102/105 (relates to #3642/#3687).
3. Fire the cutover → 163/164; run a controlled destructive walk → G12/R17/228/107/174/175.
4. Mint a handover token / walk the KC admin console → 96/123/44.
