# hw293 TRAIN MANIFEST — the 10% gap, its order, and the no-empty-train rule

> Founder question 2026-08-05: *"regarding 10% gap left what are left? In which
> order are you going to resolve them? Where is the breakdown structure and their
> dependencies and how you are going to avoid the inefficiency and work on
> multiple things in parallel and ensure multiple things are loaded to the train
> and don't let the train take off empty or with few passengers?"*
> This document is that breakdown. It freezes before the fire; the fire does not
> happen before it freezes.

Scoreboard basis (live, 2026-08-05): UAT ledger 166 ✅ / 49 ⚠️ / 19 ❌ / ⛔+N-A
adjudicated; completion-matrix headline ~90%. Open issues 126; 48 in-progress,
5 blocked-ext, 1 status/uat.

## The gap in four blocks

### Block 1 — PROOF-GATED (largest; fixes merged, proof needs the fresh 2-region prov)

**Passengers already boarded** (merged AND pinned: images `abb570b`,
bootstrap-kit `1.4.1280`, cutover `0.1.165`):

| Passenger | Fix | Proves on hw293 (recipe anchor) |
|---|---|---|
| #5640 Day-2 image delivery | 0.1.165 `mode: request` | request-path e2e: pin → request CM → tag resolvable locally → readinessProbe 1/1 (#5640 last comment) |
| #5642 catalyst-api leak | #5645 | RSS flat over 2h (was 62 Mi/min); SSO PIN form stable across hours |
| #5359/#5635/#5511/#5649 region-b GitOps | #5656 (+#5647 console reconcile) | per-Org SNI listener present in BOTH regions; funnel-Org app FQDN fresh-TCP N/N (recipe on #5635) |
| #5617 oidc-gate egress | #5624 + #5653 | funnel Org → PIN login → `/oauth2/callback` 200 (recipe on #5617) |
| #5591 (merged-not-delivered queue) | merged | per its issue recipe |
| #5661 wizard single-region | #5662 | Review screen shows `BCP topology: single-region`; full proof on the next single-region fire (Hetzner revisit) |
| cutover preflight + observability | #5535 #5534 | cutover pre-flight clean; graph badges/jobs correct |

**UAT rows this block flips:** W1, W2, 35, 57, 115, R17, 86, 90, 92, 219, 234
(+ SSO 26–38 stability class once #5645 is live).

### Block 2 — SESSION-WALLED rows (walk on hw293, same pass, authenticated)

96, 123, 178 (fresh handover URL) · 20, 23 (second Org through checkout —
funnel E2E leg of the walk plan) · 235 (3 fresh PINs) · 173 (jobs live tail) ·
211 (MCP sovereign-admin token). All bundled into the post-prov walk waves —
never walked separately on hw292 where #5642 makes sessions flap.

### Block 3 — FOUNDER-ONLY keys (the only items not parallelizable away)

1. **hw292 personal walk** — the gate to the wipe (their stated precondition).
2. **#4277 founder cred** — recorded gate to 100%.
3. **iogrid#844** merge (#5088 solver-NodePort remainder).
4. Break-glass PR verdicts (#5329 review-gated, #5356).

### Block 4 — REPO-SIDE, env-independent (runs NOW, in parallel with everything)

1. **48-issue in-progress sweep** (agent-dispatched): each issue → verify merge
   state → honest label (blocked-ext with recipe / genuinely-in-progress /
   close-with-evidence) → recipe appended HERE. This is how the train fills:
   the sweep converts "48 in-progress" into an explicit passenger list.
2. Small still-code-needed fixes surfaced by the sweep → 2-3 worktree agents
   (cap per canon), merged BEFORE the fire so they board this train, not the next.
3. Dependabot queue (11 PRs) — CI-green serial merges.
4. INCONCLUSIVE rows 20/92 — assertion refinement so the walk can verdict them.

## Order (dependency graph, no idle waits)

```
T0 (NOW, parallel):  Block 4 sweep + small fixes + dependabot
                     Founder: hw292 walk (anytime; only Block-3 item blocking T1)
T1 (walk done AND manifest frozen AND lockstep guards green):
                     wipe hw292 (canonical endpoint) + reset-uat → FIRE hw293
T2 (converged):      walk waves in parallel (2-3 walkers, worktree-isolated,
                     manifest recipes as briefs; consolidator stamps UAT centrally):
                       wave-A SSO bank + session-walled rows
                       wave-B funnel E2E (creates the Org that rows 20/23/86/90 need)
                       wave-C Day-2 request-path + region-b listener proofs
T3 (waves banked):   cutover → cc=true → G12 region-kill re-proof → matrix update
Residue after T3:    #4277-gated rows + whatever T2 newly discovers (each gets
                     issue+recipe same-day; next train, not this one)
```

## The no-empty-train rules (mechanism, not intention)

1. **Freeze gate:** the fire waits for this manifest to freeze (sweep complete),
   never the reverse. A train that leaves mid-sweep strands passengers for a
   full prov cycle — the exact inefficiency the founder named.
2. **Lockstep gate:** before fire — five-lockstep-sites pin sync + go guards +
   ghcr tag existence check (canon: bootstrap-kit charts need five lockstep sites).
3. **Boarding rule:** any fix merged after freeze does NOT delay the fire; it
   queues for the next train. No "one more passenger" slips.
4. **Recipe rule:** a passenger without a written verification recipe is not
   boarded — unverifiable fixes are the "merged ≠ delivered" theater the ledger
   exists to prevent.
5. **Caps:** 2-3 parallel agents, worktree isolation for file-editors, ONE
   environment at a time, zero-touch convergence (a wedge = defect + re-fire,
   never a hand-patch).

Refs #960 #5640 #5642 #5635 #5617 #5359 #4277

## Passenger list — 48-issue sweep result (2026-08-05, agent-verified per-issue)

Counts: **27 MERGED-AWAITING-PROOF** (boarded — proof on hw293) · **8 NEEDS-CODE-SMALL**
(boarding candidates, agents dispatched) · **8 NEEDS-CODE-LARGE** (next train, except
#5359 — see below) · **4 STALE** (closed with evidence this session) · **1 FOUNDER-GATED** (#5328).

**Boarded (merged, recipe on each issue):** #5265 #5420 #5422 #5429 #5436 #5437
#5443 #5445 #5450 #5451 #5484 #5488 #5489 #5496 #5499 #5500 #5501 #5502 #5504
#5505 #5510 #5514 #5515 #5516 #5527 #5591 #5640.

**Boarded late (merged post-sweep):** #5394+#5341 via **PR #5666** (2026-08-04T16:41Z)
— the region-b MCP edge slot never armed `ingress.hosts.mcp.host`
(13e-bp-catalyst-secondary-edge-routes.yaml; the 13d suspend is deliberate and
untouched). Recipe: fresh-TCP sampling of `mcp.<fqdn>` N/N from both regions +
the new `TestBootstrapKit_McpSecondaryEdgeArmedBothLegs` guard (both-directions
falsifiability proven pre-merge).

**Boarded late (merged post-sweep, cont.):** #5466 via **PR #5667** (2026-08-04T16:56Z)
— newapi SESSION_SECRET/CRYPTO_SECRET was minted per-region behind the shared
VIP (A16 class); fixed by riding the #5416 bp-sso-bridge→OpenBao→ExternalSecret
carrier (bp-newapi 1.4.147 + bp-sso-bridge 0.2.27, all lockstep sites).
Recipe: matching secret hashes across regions via fresh-TCP, then UAT rows
37/38 signed-in landings. NOTE: new tags publish on the post-merge
blueprint-release run — the pre-fire ghcr-tag gate must confirm both exist.

**Boarded late (merged post-sweep, cont.):** #5508 via **PR #5669**
(2026-08-04T17:15Z) — TopologyTab now consumes healthGates: unverified lag
renders as unknown (em-dash), never 0.0s-green; overcorrection-locked both
ways (Fail gate keeps red numerics; genuine high lag keeps its number).
Recipe: UAT DR rows — an app with unverified gates shows unknown, verified
shows numeric.

**Boarding candidates (agents in flight):** #5359 (linchpin, see below) ·
#5509 (guacamole postgresql-shared advertisement). Queued: #5364 #5391
#5485-remainder.

**Next-train (large):** #5476 #5480 (15 region-blind generator templates)
#5513 #5646 #5649 #5358 #5086 (parked with Hetzner).

**⚠️ LINCHPIN — #5359 (cutover pivots only the control-plane region).** Gates
#5527, #5591, #5649 AND the whole region-b listener row set (86/90/219/234
class). State: #5379's per-region legs ran and stamped success but region-b
Flux reverted the pivot in ~5 min (GitRepository generation 2 /
observedGeneration 1 — source-controller never observed the new spec); #5656
now makes step-05 refuse the stale Ready (loud instead of silent). The chart
already carries secondary-region machinery (cilium global-service annotation +
`giteaFallbackToPublicDoor` → `https://gitea.<fqdn>`). Open question a dispatched
agent is resolving pre-freeze: WHY observedGeneration sticks (wedged
source-controller vs suspended object vs unreachable URL) and which pivot-target
default is §854-clean and region-reachable. If unresolved by freeze, the train
departs anyway — #5656 guarantees hw293's cutover fails LOUDLY at step-05
rather than silently half-pivoting, which is itself the diagnostic we need.
