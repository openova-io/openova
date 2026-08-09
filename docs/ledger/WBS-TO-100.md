# WBS — the sequenced path to 100% STONE

> Companion to `PATH-TO-100.md`. That file is the per-row **fix map** (what each
> non-green row needs). This file is the **schedule**: what order the work has to
> happen in, what depends on what, and where the critical path actually runs.
> Built 2026-08-09 from live hw292 measurement, not from the prior narrative.

## 0. The arithmetic, stated once

| tier | rows | in denominator? |
|---|---|---|
| ✅ green | 189 | yes |
| ⚠️ partial | 39 | yes |
| ❌ failing | 14 | yes |
| ◑ half (G2, G3, G10, 84, 228) | 5 | yes |
| ☐ unwalked (row 38) | 1 | yes |
| N/A (156, 186) | 2 | yes — should be adjudicated out, see WP-6 |
| ⛔ superseded | 36 | **no** — excluded by definition |
| **total** | **286** | denominator **250** |

**STONE = 189/250 = 75.6%.** Gap to 100% = **61 rows**.

## 1. The fact that dominates the schedule

**The 61-row gap is not the work.** 43 of those 61 are deploy-gated — their fixes
are already merged and simply are not running on hw292. Firing a fresh prov
converts them. But `scripts/reset-uat.py` **flushes all 189 banked green rows**,
because UAT evidence is per-env by law.

So the real shape is:

```
work to reach 100%  =  61 non-green rows  +  189 re-walks
```

Any plan that quotes "61 rows left" and omits the re-walk is wrong by a factor of
four. This is the single most important number in this document, and it is why the
fresh prov is a **once** decision, not a routine one: every premature fire pays the
189-row re-walk again. Fire it when the train is complete, not before.

## 2. Critical path

```
WP-0  #5919 cutover pivot        ──┐
WP-1  complete the train          ─┼──> WP-2 fresh prov ──> WP-3 re-walk 189 ──> 100%
WP-6  adjudications (parallel)   ──┘                    └──> WP-4 converts 43
```

Everything upstream of WP-2 must land **before** the fire. Everything in WP-6 runs
in parallel and needs no environment at all.

---

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
