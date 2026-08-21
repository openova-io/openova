# UAT evidence gap — what still lacks a live hw302 screenshot

_Generated from `docs/ledger/UAT.md` on hw302 (dep `9b16ad632b906d9b`). **247 of 286 rows now carry a live hw302 in-row screenshot; 39 do not.** Tally: 269 ✅ · 10 ❌ · 1 ⚠️ · 6 ⏳._

Three reachable-rows walks are complete this session (205 → 248 screenshots):
- agent `a2b8c32c` — 16 rows (console/wire re-walks)
- agent `ab6ddf0c` — **§A**: all 22 catalog Edit-IaC 132-158 + wizard W1/W3/W4/W5
- agent `aac9dd1f` — **§B**: the full customer funnel, end-to-end, on **two fresh Orgs**

**Browser-walkable screenshots are now essentially exhausted.** What remains is structural —
it needs an env change or a live code fix, not more walking (a few provisioning/admin rows aside).

---

## 🎉 §B customer funnel — COMPLETE, end-to-end, independently verified

The funnel was driven all the way through as a customer on **two fresh Orgs**:
`walkstrangerone.omani.rest` and `walkstrangertwo.omani.homes` (two different pool TLDs).
Voucher issue → redeem → email+PIN sign-in → checkout (credit applied) → provisioning timeline
→ per-Org console (zero-click owner) → purchased app **serves**. Rate-limit (5/10s per-IP) and
returning-user redirect verified. Rows 74/75/77/81/83/84/85/86/87/88/89/90/91/92/93/94/95/216/217/226/233 all screenshotted.

**KEY REVERSAL — the `wordpress-db` defect is `uatco`-specific, NOT a funnel blocker.** The
prior gap doc listed 90/95/226/233 as blocked by a broken per-Org CNPG. That was true only for
the *pre-existing* `uatco` Org. **Both fresh funnel Orgs provisioned WordPress fully healthy** —
orchestrator-verified, not taken on the agent's word:
- `curl https://wordpress.walkstrangerone.omani.rest/` → **HTTP 200** (WP install wizard), pod `…-x-walkstrangerone-x-vcluster` **1/1 Running**
- `curl https://wordpress.walkstrangertwo.omani.homes/` → **HTTP 200**, `console.walkstrangertwo.omani.homes` → 200, pod `…-x-walkstrangertwo-x-vcluster` **1/1 Running**

So rows 89/90/94/95/226/233 are green with real live evidence. (The `uatco` CNPG-probe-NP gap
is still worth fixing for that specific Org — see §C — but it does not block the funnel.)

### Two cosmetic bugs surfaced by the funnel walk (info — verdicts unaffected, worth a ticket)
1. **Sign-out lands on an internal DNS host.** Console/marketplace *Sign out* redirects to `keycloak.keycloak.svc.cluster.local` → chrome-error page. The session IS cleared, but the logout landing is broken (should be a public URL).
2. **Returning-user redirect target 404s in the SPA.** Row 91's redirect sends an owning customer to `console.<slug>/auth/org-handover`, whose SPA route renders "Not Found" (session already established; the redirect *host* is correct, so row 91's assertion holds).

---

## The 39 remaining rows — all structural

### A. Real defect / live-engineering gap (8)
| Row(s) | Verdict | Why | Unblock |
|---|---|---|---|
| G8 220 222 | ❌ | Per-Org console `console.<org>.hw302` fails TLS (`ERR_CERT_COMMON_NAME_INVALID`) + redirectURI **#6509 OPEN** — the agenity/agentic journey can't run | Per-Org cert SAN + merge #6509 |
| M4 | ✅→needs-shot | agenity `uatco-agenity` StatefulSet blocked at admission by kyverno `probes-present` → no pod → ghcr image-pull not exercisable | Add probes to the agenity StatefulSet |
| 186 211 | ✅ | Sovereign MCP route 404s at `/`; authed half needs a minted token | Fix/confirm the MCP route + mint a token |
| 163 164 | ✅ | Cutover NOT fired on hw302 (pre-cutover); no `cutoverComplete` state | Fire the 11-step cutover (destructive, one-way — needs go) |

### B. Not demonstrable on THIS env — needs an env change / destructive action (26)
| Row(s) | Why | Unblock |
|---|---|---|
| 5 9 10 11 12 20 98 99 102 103 105 107 238 G7 M3 | Both persistent hw302 Orgs are **plan-S** (namespace-isolated); no per-Org vCluster to show ISOLATION=vcluster / per-vCluster treemap / two-surface placement | Provision a plan-M+ Org, OR adjudicate for S-plan |
| G12 R17 228 | Requires a destructive action (region-kill / Org-delete / wipe) | Authorize + run it on hw302 |
| 174 175 | hw302 has zero failed job rows → Re-run control can't show without a vacuous absent-button pass | Inject a genuinely failed job |
| 96 123 | Needs a freshly minted signed handover URL for this specific clause | Mint a handover URL + walk the redirect |
| 44 | Group→`/sovereign-admins`→role mapping lives in the Keycloak admin console | Walk the Keycloak admin console (reachable but heavier) |
| R11 R18 R20 | Need a pod-restart / handover-key re-publish / deploy-bot source walk | Targeted fault-injection |
| 50 59 61 | Topology: provision a singleton/HA instance then screenshot its Topology tab — a reversible provisioning mutation (reachable, heavier) | A provisioning-mutation walk |

### C. Held for adjudication (2)
| Row | Finding |
|---|---|
| 18 | Treemap Progress/Kind lens shows Catalyst **job-execution records** (`data-job-id`) matching row-18's forbidden patterns; these are job-progress records (#934), NOT raw ephemeral Job pods (#869 excludes those). Genuine interpretation question — not forced. |
| 231 | Adjudicated ✅ (obsolete-assertion, #4411); left untouched. |

---

### The honest ceiling

**247/286 rows carry a live screenshot.** Of the 39 remaining, only a handful are still
browser-reachable via a heavier walk (44 keycloak-admin, 50/59/61 provisioning, 96/123 a minted
handover URL). The rest — **~30 rows — are structural**: plan-S has no vCluster (15), cutover
isn't fired (163/164), the agenity/console cert + #6509 is open (G8/220/222/M4), the MCP route
404s (186/211), and the destructive/no-failed-jobs rows need a controlled action.

**True 286/286 needs the live engineering, not more walking:**
1. Per-Org cert SAN covering `console.<org>.<pool>` + merge **#6509** → unblocks G8/220/222 (agenity/agentic journey).
2. Fix the sovereign **MCP route** (404 at `/`) + mint a token → 186/211.
3. Provision one **plan-M+ Org** (vCluster isolation) → the 15 plan-S rows.
4. (Optional, bigger) fire the **cutover** → 163/164; run a **destructive** walk → G12/R17/228.
