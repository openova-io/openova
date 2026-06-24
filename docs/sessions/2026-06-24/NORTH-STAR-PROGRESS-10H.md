# North-Star progress — last 10h (omantel.biz PERMANENT Sovereign)

**Env:** omantel.biz, dep `4635277cae4ffed9`, me-east-215-a/-b (kom4dc). Never-wiped milestone env.
**UAT.md authoritative tally (2026-06-24 god-mode walk):** **182 ✅ · 37 ❌ · 3 ⛔.**
**Backlog:** 19 open (23 at window start) — 14 `status/uat` (merged-fix awaiting screenshot-evidenced close), 3 `status/in-progress`, 2 no-status (UAT-meta #4181, Crossplane Path-B #4018).

## Substantive fix-PRs merged this window (11 — all chart-versioned, so fresh provs inherit)

| PR | What it fixes | North-star pillar | Issue |
|----|---------------|-------------------|-------|
| #4251 | catalyst-api registers funnel-Org console hosts from the Org CR → signed-in landing accepts the session (the LAST #4179 layer) | P1 funnel | #4179 |
| #4252 | org-provisioning transient parent-index read no longer prunes sibling Org namespaces (env-holds) | P1/P4 env | #4250 |
| #4248 | per-Org HR `disableWait` passthrough + real Sovereign cert issuer — unstick demo-Org tertiary apps | P1 funnel | #4246/#4220 |
| #4245 | chart bump delivers org-controller per-Org TLS (#4242) to the Sovereign | P1 funnel | #4179 |
| #4254 | continuum k8s-lease witness + region fallback → 2-region pair yields Ready/leaseHolder (Topology = DR, not singleton) | P2 BCP | #3829/#3969 |
| #4256 | pool-domain defaults to FULL offered TLD set (omani.rest/trade/homes get central zones) | P1 funnel | — |
| #4258 | continuum chart 0.1.8→0.1.9 forces Flux re-pull of the #4254 controller | P2 BCP | #4257 |
| #4259 | agenity `/app` serves the workspaces (v0.9.4) dashboard — rebuilt from agenity#752, appVersion 0.9.7 | P4 sandbox/agentic | #4180 |
| #4260 | catalyst-api high PriorityClass → disk-pressure evicts Day-2 scanners first, not the EVS-pinned control-plane | P1–P5 stability | #4231 |
| #4261 | agenity detects expired claude-code OAuth at seed → kills silent "runtime offline / 0 workers" | P4 agentic | #4111 |
| #4262 | builds the missing bp-openclaw controller image (real JWT-validating controller) | P1 funnel | #4249 |

Plus 6 issues **closed** with live evidence (#3740 RPO=0 sync, #4255 pool-TLD delegation, #4236/#4241/#4218 funnel chain, #4179 the #1 bug).

## The 5 north-star outcomes — explicit pass/fail/pending

### Pillar 1 — Marketplace + voucher onboarding (the funnel) → **PASS (core), PENDING (install-gauntlet)**
- ✅ #4179 the #1 customer bug **CLOSED** — 8-step funnel walked LIVE on omantel.biz (redeem→plans→apps→domain `f4179walk.omani.works`→BCP→review→checkout signed-in, 0 OMR due→Launch) → redirect to `console.f4179walk.omani.works/jobs` (the POOL host, NOT omantel.biz); landed host HTTP 200 + valid LE cert. UAT rows 83/84/85 ✅.
- ✅ Billing + voucher live-validated (#4196/#4235); BSS menu present + working.
- ✅ All three previously-skipped funnel steps now land (pool-DNS #4236, per-Org 2-label TLS #4241/#4242, tenant-registry registration #4251).
- ⏳ PENDING: demo-Org tertiary-app install-gauntlet (#4220 WordPress, #3785 kyverno-deny) — **env-gated** on the demo Org holding (see meta-blocker below). Fix merged (#4248), verification gated.

### Pillar 2 — Multi-region BCP topology choice at signup → **PENDING-VERIFY (fix merged+rolled)**
- ✅ Funnel exposes the BCP topology step (present in the 8-step walk).
- ✅ Root cause of "Topology tab shows singleton" found + fixed: the DR lease was never acquired (dns-quorum witness had nil TXTWriter + `CATALYST_REGION` unset). #4254 adds a native k8s-lease witness + primaryRegion fallback; #4258 forced the roll.
- ⏳ PENDING: the live Topology re-walk to confirm Ready/leaseHolder (was assigned to the re-walk agent that died on API-Overload; re-fires once the demo Org holds so it has a walkable Org).

### Pillar 3 — Two independent CNPG clusters + region-kill failover → **PASS (sync), PENDING (kill-drill)**
- ✅ #3740 **CLOSED** — RPO=0 cross-region sync proven (standbyNames pre-seeded, clustermesh rw selector); the 3 multi-region shared-pg clusters healthy (a/p in rtz).
- ✅ All 11 CNPG clusters Ready; the webhook-hijack P0 class has a proven recovery (#4143 kept open as a recurrence guard).
- ⏳ PENDING: the live region-kill failover drill on omantel.biz (proven on prior throwaway envs hw144/hw148; re-walk gated on env quiescence).

### Pillar 4 — Sandbox + openova-sandbox-mcp + agentic agenity (refined North Star) → **PARTIAL → PASS-trending**
- ✅ The agentic MCP-RBAC journey proven LIVE — UAT rows 220/221/222/223 ✅: a live agenity agent created a real Application via the RBAC-scoped openova-mcp.
- ✅ The NEW workspaces dashboard now serves at `/app` durably (#4259 + agenity#752 — archaic Dashboard.svelte removed, smoke-gate hardened so it can't silently regress), appVersion 0.9.7.
- ✅ The silent "0 workers" chat-runtime failure now surfaces at seed (#4261 expired-OAuth detector).
- ⏳ PENDING: the durable agenity install ON the demo Org + incognito workspaces-UI proof (#4180) — gated on the demo Org converging.

### Pillar 5 — Sovereign independence post-`bp-self-sovereign-cutover` → **PENDING-VERIFY (proven on throwaway, not yet on permanent)**
- ✅ The cutover machinery is proven: `cutoverComplete=true` first-ever achieved on hw148 via the durable 8-step pivot + 600s deny-egress hold against github.com/ghcr.io/harbor.openova.io.
- ⏳ PENDING: re-prove on the omantel.biz PERMANENT env (#3379 durable cutover, #3374 root-URL SSO, #3376 voucher-stranger→own-Org SSO) — the post-roll SSO+cutover agent is driving these; gated on env quiescence.

## The meta-blocker that gates the 4 PENDING verifications
**#4250 — demo Org app-namespace chronic teardown.** Root cause found + fixed (#4252: the consumer.go parent-index rebuild fabricated empty `resources:[]` on transient Gitea read errors → Flux prune GC'd sibling Org namespaces). A second layer is in active repair: the org-controller reports the `demo` Org Ready with an empty `status.namespace` (the #856 "Ready-over-orphaned" class) so the app-ns never recreates. The **demo-Org-recovery agent** is root-causing + fixing that now. When it lands, the env holds → agenity-workspaces (P4), WordPress (P1), the Topology re-walk (P2), and the region-kill + cutover walks (P3/P5) all become verifiable and close together.

## Honest bottom line
- **Structural/permanent work: done and merged** — 11 fixes this window, all chart-versioned so fresh provs inherit them.
- **Acceptance posture:** P1 PASS (core funnel + the #1 bug), P3 PASS (cross-region sync). P2/P4/P5 fixes merged+rolled but **PENDING live re-walk**, all gated on the one meta-blocker (demo Org holding).
- **The convergence is not "find more root causes" — it's "the deploy/recovery cycle delivering the merged fixes + the gated re-walks running on a held env."** Four background agents are driving exactly that.
