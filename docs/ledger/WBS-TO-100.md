# WBS — the sequenced path to 100% STONE

> Companion to `PATH-TO-100.md`. That file is the per-row **fix map** (what each
> non-green row needs). This file is the **schedule**: what order the work has to
> happen in, what depends on what, and where the critical path actually runs.
> Built 2026-08-09 from live hw292 measurement, not from the prior narrative.

## 0. The arithmetic, stated once  (snapshot at `87c994af2`; re-run `scripts/uat-tally.py`)

| tier | rows |
|---|---|
| ✅ PASS | 206 |
| ❌ FAIL | 77 |
| ☐ NOT RUN | 3 |
| **total** | **286** |

**STONE = 206/286 = 72.0%** *at that commit.*

**Treat every number in this section as a snapshot, not a fact.** It moved twice
within one hour on 2026-08-10 while this file was being edited, because walks and
a cron write UAT.md continuously. `scripts/uat-tally.py` is the count; this table
is a reading of it.

The table previously read `208 ✅ / 78 ❌` under the line *"the ledger is binary …
no partials, no parked, no unwalked"*. That claim is not durable either: a
retire-and-replace swap resets the replaced clause to ☐ (a NEW clause inheriting
the old one's ✅ would be fabrication), and an SSO-chain merge flips its rows back
to unverified by design — so the third tier reappears whenever either happens,
and it was 18 rows wide a few hours before this snapshot. It is shown separately
rather than folded into ❌ because the two demand different work: a ☐ row needs a
walk, a ❌ row needs a fix, a deploy or a different env.

The earlier `189/250 = 75.6%` figure excluded 36 ⛔ rows from the denominator.
That was flattering and wrong: a row you cannot answer is not a row you may
drop. Excluding failures raises the score by removing the evidence against it,
which is the floating-denominator behaviour the frozen 286 exists to prevent.

## 1-Z. 2026-08-12T15:33Z — the single highest-leverage unlock, measured on hw295

**Nine of the 43 remaining non-green rows are gated on one thing that does not
exist yet: a customer Organization.** Not on a fix, not on a re-walk — on a
purchase never having been made.

| row | epic |
|---|---|
| R19 | agenity |
| G2 | apps |
| 87 · 90 · 95 | funnel |
| 213 | mcp |
| 218 · 223 | e2e-journey |
| 234 | apps |

**Membership corrected 15:58Z — my first pass got two of these wrong.** It listed
**16** and omitted **213**. Row 16 (change an app's topology and Save) needs a
*mutation*, not a customer Org — it is walkable on `shared-pg`, whose Topology
tab was measured today; it belongs in the mutation-gated cluster. Row 213
(cross-Org `get_application` refused as not-found) genuinely cannot be exercised
with a single Organization, because there is no *other* Org to be refused
against — my first predicate searched for "per-Org"/"customer Org" phrasing and
missed "cross-Org" entirely. Same count, different rows: a classifier that
matches on vocabulary rather than on the requirement will land on the right
total for the wrong reasons.

Row 213 carries a second, independent blocker worth recording: the MCP surface is
not exposed on this Sovereign at all — `mcp.hw295…` and `openova-mcp.hw295…`
both answer **404**, and `bp-openova-mcp` is `suspend: true` in region B. So 213
needs the Org *and* a reachable MCP endpoint.

The precondition is confirmed absent rather than assumed: on hw295 the region-B
apiserver lists only platform namespaces (`alloy … vpa`, plus `org-services`)
with **no per-Org namespace at all**, and `statefulset,deployment -A` matches
**zero** `agenity` workloads. There is nothing for a per-Org row to observe.

Why this is the best next move rather than one of the other clusters: it is the
largest single-event unlock left. One purchase driven through checkout creates
the Org, its namespace, its Agenity workspace and its app, and makes all nine
observable in one pass.

**Corrected 15:32Z after walking it — the purchase has a prerequisite I had not
stated.** I drove the funnel live to the last step: plan M pre-selects,
WordPress adds to the cart (`org-cart` = plan M, `tld: omani.rest`), review
renders the stack, and `/checkout` then stops at **"Sign in to complete your
order — Send sign-in code"**. The funnel is healthy end-to-end; it is simply
PIN-gated at the final step, exactly like every other authenticated surface.

So the real head of this chain is not the purchase. It is **a mailbox whose PIN
this walk can read**:

- The founder's own mailbox is off-limits for walk automation. A prior agent
  rotated its password to read a PIN and locked the founder out of email; the
  standing rule is a **dedicated throwaway test mailbox** instead, or minting
  the session via the handover signer.
- That same capability also unlocks the mothership wizard rows (W1/W2/W5 are
  currently ⚠️ on source+test evidence precisely because no live wizard walk was
  possible), and the janitor rows R1/M1/G4/G5 which need mothership logs.

**One capability — a readable test mailbox (or handover-signer minting) —
therefore gates the nine per-Org rows AND the wizard AND the janitor set.** That
is the highest-leverage thing to arrange before the next walk, and it is worth
more than any single row on the list.

The other two structural blockers are smaller and both need an event, not a fix:
the cutover has not been fired here (its 11 steps render but have not run —
row 162), and no job has failed, so the Re-run control has nothing to act on
(row 176, correctly gated).

## 1-A. 2026-08-12 re-partition — by the mechanism that holds each row shut

Read this before §1. The 2026-08-09 partition below buckets by *kind of work*
(deploy / build / env / walkable). That cut stopped being the useful one once
hw292, hw293 and hw294 all went away: it answers "what sort of effort does this
need" when the pressing question is "what single event would release it".

Derived by classifying all 76 ❌ rows on their own evidence text, most specific
mechanism first (`scripts/uat-confidence.py --due --env <env>` gives the
schedule; this gives the shape).

| cluster | rows | what holds it shut |
|---|--:|---|
| **H — measured on a destroyed env** | 36 | the verdict is real but its Sovereign is gone; the row owes a re-walk, not a fix |
| **G — destination dead (front door / DNS)** | 21 | the app or console host does not serve; #6186 is the merged fix for the app-host half |
| **E — founder-credential gated (#4277)** | 7 | one console action by the founder; no code path exists for an agent to supply it |
| **C — mutation-gated** | 4 | needs a write walk; a read-only walk structurally cannot answer the clause |
| **D — needs a customer Org through checkout** | 3 | the funnel must be driven past the review step so a second Org with an app exists |
| **F — blocked by design / not exercisable** | 2 | correct behaviour on this shape; candidates for clause adjudication |
| **A — clause retired** | 1 | the replacement clause has never been walked |
| ~~I — unclassified~~ | 0 | resolved on a read — see below |

- **H (36)** — 16 19 25 29 30 31 33 34 35 37 38 39 51 52 55 56 62 64 65 66 87 101 109 162 166 176 206 207 208 218 219 235 239 G1 G3 W5
- **G (21)** — 32 36 60 67 69 70 111 172 188 189 213 223 229 232 234 236 241 G7 G11 W1 W2
- **E (7)** — 95 187 220 221 G8 G9 R13
- **C (4)** — 57 71 197 R19 · **D (3)** — 90 222 R16 · **F (2)** — 227 228 · **A (1)** — 115

**Cluster I is empty on a read.** G2 and 237 both describe a hw293 measurement —
`catalyst-api answers a0abba9`, the region-B `no healthy upstream` signature —
without ever writing the token `hw293`. They are cluster H. That moves the
headline from 57 to **59 of 76**, and it is worth noticing *why* they were
missed: the classifier looked for an environment NAME and the evidence proved an
environment by DESCRIPTION.

### The same gap, in the observation filter

`observable_here()` (added with the attribution fix) admits evidence that names
no environment, on the reasoning that under-recording is recoverable while
inventing a walk is not. **19 rows are unattributed**, so it is worth stating the
exposure exactly rather than leaving it as a principle:

    51 52 55 62 64 65 66 67 69 70 111 188 189 227 234 236 G2  — all ❌
    184  R20                                                   — ✅

**17 of the 19 are ❌, and a failure is never discounted or credited to a machine
— those are harmless. The real exposure is two rows, 184 and R20**, which could
be recorded as a pass on a Sovereign that never measured them. The durable fix is
upstream of the filter: a walk stamp should name its environment, and the three-
cell re-walk rule already says so. Until those two are re-stamped, treat their
green as inherited rather than measured.

**The headline: H + G = 57 of 76, three quarters of the failing set, are released
by the same event — one Sovereign that converges and answers.** Not by more code.
Every fix those rows need is already merged; what is absent is a surface to
measure against. This is why the ❌ count sat flat at 76 while real defects were
being closed, and why walking harder against a dead environment cannot move it.

That also sets the order. Fire a Sovereign first and walk H; cluster G's app-host
half rides in on #6186 and is measured in the same pass. Only E, C, D and A need
anything beyond that, and E needs the founder rather than an agent.

## 1. The ❌ set, partitioned by what actually unblocks them (2026-08-09)

| bucket | rows | what it needs |
|---|--:|---|
| **WALKABLE NOW** | 25 | a walk on THIS env can change the verdict |
| **total** | **25** | |

- **WALKABLE NOW (25)** — 16 60 87 90 95 115 166 213 218 219 220 221 222 223 227 228 234 235 G2 G7 G8 G9 G11 R16 R19

## 2. The D-34, clustered by root cause

Fixing a cluster closes several rows at once. Ranked by rows-per-fix:

| root | rows closed | what it is |
|---|--:|---|
| **#5246** per-region Gateway listeners | 5 — 87 88 90 95 R16 | the per-Org console/app listener is written to one region only |
| **#4325** dead vCluster vocabulary | 2 — 98 102 (+ #5937 treemap done, #5945 filed) | `mgmt/dmz/rtz` survive in code after the charts were deleted |
| **#5640 / #5511 / #5930** per-Org delivery | 2 each — 86 90 · 90 233 · 95 233 | overlapping legs of the same funnel→serve chain |
| **placement epic** | 7 — 96 98 100 102 103 106 115 | one epic, mostly one vocabulary defect + #5614 handover |
| **topology honesty** | 4 — 48 62 63 71 | UI drops honesty signals the backend emits (#4901/#4923) |
| **agentic** | 5 — G8 220 221 222 223 | credential now seeded; #5516 claim + an unproven chat round-trip |
| **MCP per-Org** | 2 — 212 213 | the instance the guard names does not exist |

**Four of those seven clusters are now WRITTEN, and the table above describes what
each cluster IS, not what is left to do in it.** Re-read it with §1: #5246's five
rows (87 88 90 95 R16) landed as `0439fbea8` PR #5957, topology-honesty's 62/63/71
as `cd7b02973` PR #5960, the placement legs 100/103/106 as `4d1da6322` PR #5958,
and MCP-per-Org's 212/213 as `1790e52e9` PR #5963 — every one an ancestor of
`origin/main`, none of them running on hw292. Their remaining cost is a roll, not
an engineer. What is genuinely unwritten in this table is #4325's vocabulary rows
(98, 102), the rest of the placement epic (96), and the agentic cluster's G8.

**#5921** (mail tether) gates three more (84, 20, 23) and is half-landed: PR #5951
makes it visible to the egress proof; pivoting the SMTP path itself is still open.

## 3. Sequencing — why the prov is fired ONCE, and last

Firing a fresh prov converts the DEPLOY-GATED bucket in one move, but
`reset-uat.py` flushes every banked green, because UAT evidence is per-env by law.
So:

```
real work to 100%  =  <non-green>  +  <banked greens re-walked>
                   =  80           +  206                        (at 87c994af2)
```

The shape is what matters and it does not move: the re-walk term is roughly
**three times** the non-green term, so any plan quoting only "N rows left" is
wrong by about a factor of three. Both terms are read from
`scripts/uat-tally.py`, not from this page — the figures above were 78 + 208 a
few hours earlier and will be different again by the time the prov is fired. That
is why the prov is a **once** decision: every premature fire pays the whole
re-walk again. Land D first, then fire.

## WP-0 — #5919: cutover step-06 reverts its own pivot  ⟵ CRITICAL PATH HEAD

**Found 2026-08-09. This moved to the head of the critical path tonight.**

Step-06 live-patches HelmRepository URLs with `kubectl patch`, but those objects are
owned by the `bootstrap-kit` Kustomization. The next Flux reconcile reads
`oci://ghcr.io/openova-io` from Git and reverts all 62. The `ghcr-pull` secret pivot
survives (nothing owns it), so 62 repos end up dialing `ghcr.io` with a secret that
holds only local-registry auth — `failed to configure login options: no auth config`
— wedging 53 HelmReleases in region A.

The 7 repos that survived are exactly the `org-tenants` tree, whose **Git source**
was made cutover-aware by #5527. That is the working precedent to copy.

| # | task | notes |
|---|---|---|
| 0.1 | Emit cutover-aware URLs for the bootstrap-kit tree at the source | Port the `cutover_aware_5527.go` pattern |
| 0.2 | Delete step-06 Phase-1 live patching of Flux-owned objects | Structurally cannot hold; guaranteed regression |
| 0.3 | Extend the fail-loud "zero ghcr-prefixed HRs remain" check from region B to region A | Would have caught this on day one |
| 0.4 | Fix `runDRPreflight` hardcoded `Pass` (#4986) | A preflight that cannot fail hid this class |

**Why this gates the fresh prov:** without it, the new env reaches
`cutoverComplete=true` and then wedges exactly the same way, and the 189-row re-walk
is spent for nothing.

**Also invalidates a standing claim:** `cutoverComplete=true` was reached on hw292
with 62 platform repos still tethered to `ghcr.io`. Step-08's deny-egress hold passed
because nothing re-pulls a chart in 600s — reachability during a window is not
absence of dependency (#5650). Pillar 5's proof is weaker than recorded until 0.3
lands.

## WP-1 — complete the train

Everything merged-but-undeployed that the fresh prov should carry.

| # | task |
|---|---|
| 1.1 | cutover `0.1.176`, catalyst-api `c3f9ab0` — the verified pins |
| 1.2 | #5645 catalyst-api OOM fix (rows 37, 86, 90, 94 — the fan-out truncation of #5894) |
| 1.3 | R17 org-wildcard TLS Secret reap (PR #5917, merged) |
| 1.4 | #5919 from WP-0 |
| 1.5 | Verify every pin is an ancestor of the image: `git merge-base --is-ancestor <image> <fix>` |

## WP-2 — fire the fresh prov  (task #999)

One command, irreversible, pays the 189-row re-walk. Preconditions: WP-0 and WP-1
complete, `reset-uat.py` run first so no evidence from the wiped env survives.

## WP-3 — re-walk the 189

The largest *labour* item, ~4× the non-green gap. Mechanical but not free. Prior
sessions banked ~63 rows/session with parallel walkers; at that rate this is ~3
sessions of walk time. Method notes that matter: the marketplace subdomain field
commits on **blur** not input; synthetic value-setter events do not arm the
instance-create submit button (real interactions do); CodeMirror is virtualised so
line lookup needs PageDown then a click on the resolved `.cm-line`.

## WP-4 — the 43 deploy-gated rows convert

No work — these flip when WP-2 lands on a complete train.

- **13 ❌**: W1, W2, 29, 41, 57, 92, 115, 219, 234 (all one root, #5919) + 37, 86, 90, 94 (#5645)
- **30 ⚠️**: the deploy-gated majority of the partial tier

## WP-5 — the genuinely-open defects (need code or a design call)

| rows | item | what it needs |
|---|---|---|
| 57, G3 | #4986 — **no producer** for `CnpgPair`/`Continuum` CRs | Design call: which component emits them (org-controller / catalyst-api at prov / cutover chain). Every reference in the API is a reader; `grep -rln "Continuum{"` returns zero |
| 37, 38 | #5878 — newapi is the only SSO app with no oidc-gate reference | One `instances:` entry, pending the redirect_uri / double-auth adjudication |
| 225 | code-blocked | The one ⚠️ row that is not deploy-gated |

## WP-6 — adjudications (no environment, no code, runs in parallel NOW)

These move the **denominator**, and nothing gates them. Highest ratio of score
movement to effort in the whole plan.

| rows | item |
|---|---|
| 184, 19 | #5867 |
| 5 | #5847 — `TIER=sme` assertion is obsolete |
| 156, 186 | N/A rows — adjudicate out of the denominator or restate |
| G2, G10, 84, 228 | the ◑ tier — restate as ✅/❌ against a clause that can actually fail |
| G6 | collapses into G1 (green) |

## WP-7 — founder-gated

| item | note |
|---|---|
| #4277 credential | Gate 2 in `PATH-TO-100.md`. Not agent-actionable |

---

## What I would do next, in order

1. **WP-6 now** — parallel, needs nothing, moves the denominator today.
2. **WP-0** — the critical-path head; the fresh prov is worthless without it.
3. **WP-1** train completion + ancestry verification of every pin.
4. **WP-2** fire once.
5. **WP-3** re-walk with parallel walkers; **WP-4** converts for free.
6. **WP-5** in parallel with WP-3.

## Honest risks

- **The re-walk is the schedule.** 189 rows dwarfs the 61-row gap. Firing early is
  the most expensive mistake available.
- **WP-5's DR item is a design call, not a patch.** It has no owner yet, and it is
  the only remaining item with genuinely unknown size.
- **Pillar 5's sovereignty proof is provisional** until WP-0.3 lands — the current
  green was measured by a check that cannot fail on this class.
