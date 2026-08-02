# EPIC completion matrix — 2026-08-01

One row per labelled EPIC, with the **specific artifact** that justifies its figure.
Where no artifact exists the row says **unverified** rather than carrying a number.

Method, so the numbers are reproducible:

    gh issue list --label epic --state all              -> the 18 EPICs below
    gh issue list --search "<n> in:body,title" --state open|closed
                                                       -> child counts per EPIC

**% = closed children / total referencing issues.** That measures *issue closure*, not
runtime acceptance. The distinction is the whole point of this file and is restated at
the bottom — a 100% row means every child issue is closed, **not** that its UAT rows
are green.

## The 18

| EPIC | state | children (closed/total) | % | artifact justifying the % |
|---|---|---|---|---|
| **#1096** Compliance — Kyverno library + score aggregator | CLOSED | 27/27 | **100%** | `gh issue list --search "1096 in:body,title"` → 0 open. Live: `platform/kyverno-policies/chart/values.yaml` ships the baseline set; `networkpolicyPresent` back at `Audit` (`values.yaml:358`) after the #5505 P0 |
| **#2737** G117 Application Lifecycle Phase 2 | CLOSED | 47/47 | **100%** | largest child set in the repo, 0 open |
| **#1094** Catalyst Phase 0/1 unified roll-out | CLOSED | 25/25 | **100%** | 0 open |
| **#825** Multi-domain Sovereign — N parent domains pooled | CLOSED | 12/12 | **100%** | 0 open; domain canon live in `docs/DOD.md` §Domains-canon |
| **#1099** Cloud Resources — k9s-on-web | CLOSED | 11/11 | **100%** | 0 open |
| **#3988** OpenOva MCP server — RBAC-scoped facade | CLOSED | 10/10 | **100%** | 0 open; `products/openova-mcp/` ships; row 211 ✅ on hw269 (7-tool surface, real 8-app JSON) |
| **#1095** EPIC-0 Foundation contracts | CLOSED | 9/9 | **100%** | 0 open |
| **#3188** Reusable backing-services model | CLOSED | 8/8 | **100%** | 0 open |
| **#1101** Multi-cluster + Continuum DR | CLOSED | 7/7 | **100%** | 0 open; **G12 region-kill 6/6 CLEAN, RPO=0** on hw282 (#5303 proven e2e) |
| **#1097** EPIC-2 Applications — CRDs + controllers | CLOSED | 6/6 | **100%** | 0 open |
| **#1082** Sovereign-first Organization onboarding | CLOSED | 5/5 | **100%** | 0 open |
| **#1098** EPIC-3 RBAC — useraccess-controller + Keycloak | CLOSED | 5/5 | **100%** | 0 open |
| **#1100** EPIC-5 Networking — default-deny + Hubble | CLOSED | 3/3 | **100%** | 0 open |
| **#4010** bp-chepherd as a deployable Application | CLOSED | 2/2 | **100%** | 0 open |
| **#1090** Console routing + auth journey audit | CLOSED | 1/1 | **100%** | 0 open. ⚠️ see caveat below — #5401 is an open SECURITY issue in this surface |
| **#4212** ONE object-model / DR backbone | **OPEN** | 16/16 | **100% of children** | **zero open children.** What remains is the Crossplane-adoption architecture call itself (`status/blocked-ext`). DR half live-Healthy, re-proven by hw291's cutover |
| **#795** SME-tenant turnkey experience | CLOSED | 20/21 | **95.2%** | 1 open child. EPIC closed ahead of it |
| **#3969** Application-centric Placement (`targets[]`) | **OPEN** | 11/16 | **68.8%** | 5 open children — see breakdown |

**Aggregate: 16 of 18 EPICs closed = 88.9%** (`gh issue list --label epic --state all`).

## #3969's five open children — the real frontier

| child | state | artifact |
|---|---|---|
| #5515 derivePattern fails open | **delivered** | `796e587b2` ⊂ image `fb41faf`; 21/21 tests; Go-side analysis in UAT row 60 shows the residual `DerivePattern([])→singleton` is a *pinned contract* the DR fan-out depends on |
| #5482 Overview shows a host-cluster label as PRIMARY REGION | **half** | read side `b41c93b3c`; emit seam localized to `application_controller.go:2593` (declared) vs `placement_projection.go:279` (effective); deferred — DR-path write, unvalidatable without an env |
| #5422 Overview hardcodes a `singleton` fallback | fix open in PR | **PR #5536** |
| #5420 Topology renders declared, not effective | fix open in PR | **PR #5538** |
| #5485 observability defects 4/5/6 | delivered, PR held | `53bdf8052` ancestry into `fb41faf`; **PR #5534** |

Two of the five (#5422, #5420) are the same declared-vs-effective seam that #5515 and
#5482 already fixed on their own surfaces.

## Caveats that the percentages do NOT capture

**1. Issue closure ≠ UAT green.** Every 100% above means child issues are closed. The
acceptance ledger is separately in its **reset state** (`429a39f76`) pending the hw292
walk. Last walked figures: **135/281 on hw291**, north-star **214/281 on hw288**.

**2. A closed EPIC can still have an open security issue in its surface.** #1090
(console routing + auth audit) reads 1/1 = 100%, while **#5401 is OPEN**: per-Org
console `/settings` renders the Sovereign operator panel — platform API tokens,
Hetzner/S3 keys, Decommission — to an org-admin. Closure of the audit EPIC did not
close what the audit found.

**3. The tracker under-reports delivery.** Verified today on four issues: #5489 is
**5 of 6** tasks delivered with every checkbox unticked; #5505 (P0) is fully delivered
at both layers; #5477 delivered; #5274 and #5305 both closed on real fix commits with
their regression tests re-run green. Read the children and the commits, not the
checkboxes.

**4. Durable pillar completion is a different denominator** and stands at **88**,
unchanged since the hw291 walk — it moves only on walk evidence. See
[`../sessions/2026-08-01/completion-matrix.md`](../sessions/2026-08-01/completion-matrix.md)
for the per-pillar view and the surface map proving no Sovereign is currently
reachable.

## What would move these numbers

#3969 is the only EPIC whose percentage can move on code alone — landing PRs #5536 and
#5538 takes it from 11/16 to 13/16 (**81.3%**). Everything else needs the hw292 fire
and the walk that follows it.

---

# Session addendum — 2026-08-01 close (SHA-backed)

The matrix above was written mid-session. Ten artifacts landed after it. This addendum ties each to
its EPIC with the exact commit, so no percentage rests on an unnamed claim.

## What moved, and what it does to the numbers

| EPIC | artifact landed this session | SHA | effect on % |
|---|---|---|---|
| **#3969** Placement | `derivePattern` fixed to read LIVE regions, not declared specs | `509222c6e` | #5515 was already counted delivered — this closes the *emit* half that row 60 flagged |
| **#3969** | #5515 pinned against a REAL mothership record (2 declared / 1 live) | `ebf5b6feb` | evidence upgrade only |
| **#3969** | #5482 primaryRegion divergence pinned by executable test | `daa1b1ad4` | **no % change** — pins the defect, ships no fix |
| **#1090** Console/auth | #5401 escalated: fabricated HQ *drives* cloud-region selection | `bf4213cf6` | strengthens the open security caveat; EPIC stays 100%-closed-with-open-finding |
| **#1096** Compliance | §854 live NodePort audit — 13 findings, 1 ours | `9a0d290f5` | **no % change** — reports; `powerdns-anycast` still carries 2 ports |
| **#1096** | cluster-side §854 guard + powerdns `nodePort: 0` | `19b3a0933`, `9c4c1d840` | closes the render half of #5348 |
| **catalog** | R21 full-population audit → **6 inert Blueprints** | `c74396bd9` → **#5559** | new open issue; catalog-seed contract is 73/79 clean |
| **infra** | mothership `catalyst` at zero pods, root-caused | `075fca9a2`, `da4871c7a` → **#5558** | new open issue; blocks every console-scoped row |
| **CI** | vitest gate could never fail a build | `8371c9918` | closes a fail-open gate |
| **wizard** | W1–W5 rows added (2 pass / 3 fail) | `ce1f05efe` | ledger coverage, denominator 281 → 286 |

## Honest effect: the headline numbers did NOT move

- **EPIC closure stays 16/18 = 88.9%.** Nothing here closed an EPIC. #3969 is still 11/16 (68.8%);
  landing PRs #5536 + #5538 would take it to 13/16 (81.3%), and both need a merge.
- **Durable pillar completion stays 88.** It moves only on walk evidence against a Sovereign, and
  no Sovereign exists.
- **UAT green stays 5/286 (1.7%).** Three rows were walked this cycle (R21, R18, R9) and all three
  resolved to ⚠️ or ❌, not ✅ — each proves the layer its assertion lives at (registry state,
  decision logic, wiring contract) but none can prove runtime.

**That is the correct outcome, not an underperformance.** Every artifact above is either a defect
found, a guard added, or a claim corrected. None of them is a runtime acceptance, because runtime
acceptance requires an environment that does not currently exist.

## What the session actually changed about the numbers' MEANING

Three corrections to how these percentages should be read:

1. **The tracker under-reports delivery** (`81fdf741f`) — verified on six issues. #5489 reads 0/6
   with five tasks shipped; #5485 sits at `status/uat` with three defects merged; #5305/#5274 are
   closed on real fix commits. Read commits, not checkboxes.
2. **Flux health status is wrong in both directions on the same cluster** (#5558) —
   `catalyst-platform` reports `Healthy=True` on zero pods and a 503 console, while `apps` reports
   `HealthCheckFailed` on four objects that are all `Ready=True`, from a status frozen since
   2026-06-04 by a suspension. Anything gating on Flux status as ground truth is being misled twice.
3. **A repo-clean guard is not a cluster-clean guard** (`9a0d290f5`) — `powerdns-anycast`'s template
   is correct and the live Service still carries two nodePorts, because
   `allocateLoadBalancerNodePorts: false` does not deallocate.

## Unverified — stated rather than hidden

- The 59 remaining EPIC child-issue counts were not re-derived this session; the 16/18 figure is
  carried forward from `291bb3f69`.
- #5559's six inert Blueprints are triaged into three groups but none is fixed.
- #5558's catalyst outage is root-caused but unresolved — the fix belongs in the `openova-private`
  rendering, which is not reachable from this repo.

---

# 2026-08-02 — re-derived live, and one row above is CORRECTED

The 16/18 figure was flagged "carried forward" in the caveat above. It has now been **re-derived
live** rather than trusted, and the re-derivation exposed an error in this file.

Commands, so every number below is reproducible:

```
gh issue list --label epic --state all --limit 40                  -> 18 EPICs, 16 CLOSED
gh issue list --search "3969 in:body,title" --state all --limit 60 -> 16 children, 11 CLOSED
gh issue list --search "4212 in:body,title" --state all --limit 40 -> 16 children, 16 CLOSED
scripts/uat-tally.py                                               -> 8/286 GREEN
```

**Aggregate confirmed: 16 / 18 = 88.9 %.** Not carried forward — queried 2026-08-02.

## 🛑 Correction: #5515 is NOT delivered

The `#3969's five open children` table above records #5515 as **delivered**, citing `796e587b2`.
That is wrong, and the way it went wrong is the defect class this session has been chasing all day.

`796e587b2` is the merge commit of **PR #5519**, titled *"fix(console): never claim DR health
without live backing — phantom standby + fail-open pattern"*. It carries `Refs #5514 #5515`. It is a
**console-side** fix that mentions #5515; it does not touch `derivePattern`. Checked at function
level on `origin/main` rather than by matching an issue number in a commit message:

```
git show origin/main:products/catalyst/bootstrap/api/internal/infrastructure/topology_loader.go \
  | grep -A3 'func derivePattern'
->  func derivePattern(in LoaderInput) string {
        switch {
        case len(in.Regions) > 1:          # the DECLARED spec, not live regions
```

and the in-flight guard — the non-obvious half of the fix — is absent: `grep -cE 'live == 0|in-flight'`
returns **0** on main.

So **#5515's defect is live on main.** The real fix (`509222c6e`) plus its tests (`02108f5e6`,
`ebf5b6feb`, one built from mothership record `2c2d746b578c636b` with 2 declared / 1 live regions)
sit on an unmerged branch.

**#5482** needs the same correction of framing: PR **#5483 MERGED** 2026-07-29 fixed the *read* side
(`read primaryRegion from status.placement`), but the flat `status.primaryRegion` still goes stale on
failover — pinned by executable test `daa1b1ad4`. One leg landed; the divergence did not close.

### The counting rule this establishes

> **A merged PR is not a closed child, and a `Refs #N` is not a fix for #N.**

Counting merged PRs as done would report #3969 at **13/16 (81.3 %)** instead of its true
**11/16 (68.8 %)** — a 12.5-point overstatement produced entirely by trusting merge state. Every
open child below was therefore verified at file level, not by commit message.

| child | artifact | dated | verified state |
|---|---|---|---|
| **#5420** Topology renders declared, not effective | **PR #5538 OPEN**, `692c131dc` | 2026-07-31 | authored, unmerged |
| **#5422** Overview hardcodes `singleton` fallback | **PR #5536 OPEN** | 2026-07-31 | authored, unmerged |
| **#5515** derivePattern fails open | `509222c6e` + `02108f5e6`, `ebf5b6feb` | 2026-08-01 | **unmerged — defect live on main** (proof above) |
| **#5482** flat `primaryRegion` goes stale | `daa1b1ad4` | 2026-08-01 | divergence pinned; merged #5483 closed only the read leg |
| **#4212** (cross-listed) | — | — | 16/16 children CLOSED, zero open |

## Artifacts landed 2026-08-02, each tied to its EPIC

| EPIC | artifact | SHA / ref | how it was verified |
|---|---|---|---|
| **#1096** Compliance | §854 solver carve-out age-bounded — a weeks-old solver no longer reads as "transient" | `b7c43fab9`, `01422f144` | mutation test: reverting the age check makes the self-test exit 2 |
| **#1096** | NodePort guard made able to pass CI at all — it assumed vendored chart deps the repo gitignores | `db5dfef3a` | clean detached worktree + empty helm config: 71 rendered, 56 deps fetched |
| **#1096** | Guard no longer aborts mid-sweep; it reported only its first casualty at the same rc=1 a full run gives | `12c0e0f7c` | probe chart placed FIRST alphabetically so truncation cannot hide behind ordering — 72 charts scanned after it |
| **#1096** | CI helm 3.16.0 → 3.16.3 (two dependency-resolution regressions) | `9b3c1e260`, `f7c93d438` | **PR #5544 CI GREEN, failing=0** — the one artifact today that CI itself certifies |
| **#1096** | sealed-secrets dependency repository answers 404 — allowlisted with the reason, not silently repointed | `6e11a8ab9` → **#5563** | `curl https://bitnami-labs.github.io/sealed-secrets/index.yaml -> 404` |
| **#4706** §854 | **UAT row 240 walked** → ⛔, and its malformed line repaired (10 pipes → 8) | `3ea68a83d` | in-browser HTTP **503** + committed screenshot |
| **#4411** delivery | **UAT row 231 walked** → ⚠️, render half PROVEN | `0f75d9f18` | `helm template --api-versions postgresql.cnpg.io/v1` renders the probe netpol; the control render without it emits **zero** |
| **#4706** §854 | **UAT row 242 walked** → ⛔ at the API the row names | `0f75d9f18` | `/sovereign/api/v1/deployments -> 503`; edge UP, backend absent |
| **#5348** | bp-powerdns catalog-seed pin synced 1.2.22 → 1.2.23 (four-site lockstep) | `671cf3b23` | `tests/e2e/bootstrap-kit` PASS with `-count=1` (cache defeated) |

## The three denominators — today's queried values

| measure | value | source, run 2026-08-02 |
|---|---|---|
| EPIC closure | **16 / 18 = 88.9 %** | `gh issue list --label epic --state all` |
| UAT acceptance ledger | **8 / 286 = 2.8 % GREEN** | `scripts/uat-tally.py` after the row-242 stamp |
| Durable pillar completion | **88** | moves only on walk evidence against a converged Sovereign |

## What did NOT move, stated plainly

**No headline percentage changed today.** EPIC closure stays 16/18 because no child closed. The
ledger stays 8/286 GREEN because the three rows walked resolved to one **partial** and two
**blocked** — not passes.

That is the correct result. Every artifact above is a defect found, a guard repaired, or a claim
corrected; none is a runtime acceptance, and runtime acceptance needs a converged Sovereign that does
not exist (hw291 wiped, hw292 unfired, `catalyst` namespace at zero pods with the console 503 —
confirmed in-browser today at 02:48Z).

The number positioned to move first is **#3969**, one merge away from 81.3 % and two from 93.8 %.
What stands between it and that figure is merge permission, not engineering — with the caveat this
section establishes: **#5515 must be counted by what is on `main`, not by a merged PR that named it.**
