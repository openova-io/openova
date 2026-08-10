# PATH TO 100% — **hw292 live, cc=true** (delivery-state re-measured 2026-08-08)

> **Source of truth:** [`UAT.md`](UAT.md). **Current env = hw292** (`hw292.omani.works`, dep `1c56518035a83e03`, 2-region Huawei me-east-215-a/-b-1) — **fired 2026-08-03T04:04Z, converged, `cutoverComplete=true` 08:12:30Z**, and G12 region-kill re-proven **6/6 zero-touch** on 2026-08-04 (promotion T0+136s, failback with a clean re-clone, no split-brain — `docs/sessions/2026-08-04/hw292-g12-region-kill/`). hw291 was wiped 2026-07-31 08:54Z after banking cc=true.
> This file maps every non-green row to its gate + owner. A row stays non-green until the hw292 walk verifies it — **merge ≠ green** (founder rule). Nothing below is a walk stamp; this is the *fix map*, and it says exactly which fixes are already inside the image hw292 will boot.

---

## The remaining ❌ set is a DEPLOY, not a fix backlog (re-measured 2026-08-08)

This is the single most decision-relevant fact in this file, and it was not
visible until every failing row was classified at once rather than chased one at
a time.

`scripts/classify-uat-delivery-state.py --image fad88bd` — the catalyst-api
actually running on hw292, **built 2026-08-02** — resolves each row's cited PRs
to their merge commits and asks whether each is an ancestor of that artifact.
Across all three non-green tiers:

| tier | DEPLOY-GATED | CODE-BLOCKED | UNKNOWN | total |
|---|---|---|---|---|
| ❌ | **13** | **0** | 0 | 13 |
| ⚠️ | **28** | 3 | 7 | 38 |
| ☐ | **20** | 0 | 6 | 26 |
| **total** | **61** | **3** | **13** | **77** |

**Sixty-one non-green rows are waiting on a roll**, and for those rows writing
further fixes moves nothing.

> ⚠️ **THE SENTENCE THAT USED TO FOLLOW HERE IS NOW FALSE, and it was the most
> decision-relevant line in the file.** It read: *"Every ❌ row is deploy-gated;
> there is no failing row left that needs code."* That was true of the rows
> classified on 2026-08-08 **morning**, and it stopped being true the same day.
> A live walk of hw292 that afternoon surfaced **five defects on the running
> environment that no roll will fix**, because the code to fix them does not
> exist yet. Anyone reading the old sentence would conclude "just deploy" and be
> wrong.

## The ❌ set, classified against the RUNNING artifact (2026-08-08 late)

`scripts/classify-uat-delivery-state.py --image fad88bd` over all 15 ❌ rows:

| verdict | count | rows |
|---|---:|---|
| **DEPLOY-GATED** | **12** | R17, W1, W2, 37, 57, 86, 90, 92, 94, 115, 219, 234 |
| **CODE-BLOCKED** | **0** | — |
| UNKNOWN → resolved below | 3 | 29, 41, 95 |

**No failing row needs code written for it.** Twelve carry fix PRs already merged
and are waiting on the roll.

### The three UNKNOWNs, resolved from first-hand walks (not left ambiguous)

The classifier reports UNKNOWN when a row cites an ISSUE rather than the PR that
closed it. All three were walked live on 2026-08-08, so they are resolved here
rather than deferred:

| row | verdict | evidence |
|---|---|---|
| **29** | ~~needs code~~ → **fix written and merged (#5909), awaiting walk** *(2026-08-08)* | Silent re-auth *works* — proven by dropping `catalyst_session` while keeping the realm session and landing on `/dashboard` with fresh tokens. The real defect is narrower: when `catalyst:authed=1` is stale relative to the cookie, the stale-marker fast path 401s and PIN-walls **instead of falling back to the silent leg that demonstrably works**. Tracked #5887. A genuine TTL expiry produces exactly that state. **Root cause established 2026-08-08:** `router.tsx:287` (`if (hasCatalystSession()) return`) short-circuits BEFORE line 290's `probeWhoamiAndCacheMarker`, so #5460's 401-revocation is unreachable from the one gate that needs it — the stale marker is self-perpetuating at the only site that could clear it, and the #3374 Layer-B silent-SSO leg is skipped with it. Fixed in **#5909** (merged): the marker now carries a written-at stamp and ages out after 5 min; missing/non-numeric/future stamps all read stale. Fail-safe — worst case is one extra `/whoami`, and the `null` branch already fails open. Source-correct, not walked. |
| **41** | ~~needs code~~ → **DEPLOY-GATED** *(corrected 2026-08-08)* | The finding stands: the sovereign realm holds **two** users — the owner and `emrha.baysal@…`, a two-letter transposition, persisted from a typo at the login form. Only a COUNT finds it (`/emrah/` matches both). **But the fix already exists.** `6663dc441` (2026-08-06) — *"defer pin/issue realm write to pin/verify — no unauthenticated principal creation"* (#5720) — moved the realm write out of `HandlePinIssue` into `HandlePinVerify`, gated on a correct PIN, and ships with the guard `auth_pin_issue_no_unverified_realm_write_5720_test.go`. `auth.go:489-499` cites **this exact row and this exact address** as the finding that prompted it. The image `fad88bd` was built **2026-08-02**, four days earlier, so the fix is simply not in it. Tracked #5883 → resolves on the roll. |
| **95** | ~~needs investigation~~ → **mechanism named, fix merged, awaiting walk** *(2026-08-08, #5910/#5911)* | The purchased app never became an Application. Checkout recorded `Apps (1)`, the Org converged (`state=done`, all six steps, vcluster created), the applications list holds 14 apps and none belongs to the new Org — control: the first Org's apps *are* in that same list. **Traced to the write seam:** `resolveAppSlugs` (`handlers.go:363`) substitutes the raw UUID on a catalog miss, so `len(out) == len(in)` and **the cart count survives by construction** — which is why every count-based check read green. `consumer.go:589` then passes it to `GeneratePerOrgAppsTree` *as a slug*, `GetAppSpec` returns a bare `AppSpec{}`, and the generator emits a Deployment with a **null `image` and `containerPort: 0`**. That is invalid to the apiserver, and per the #4389 note in `helmrelease_apps.go` a rejected manifest fails the **whole** `vcluster/apps` Kustomization — so the blast radius is that app **plus every co-installed app in the same apply**. Fix reports the unresolvable case (the only point where the miss is still observable); pass-through behaviour deliberately unchanged. |

### Where the score actually stands, and what the ⛔ tier is — 2026-08-08

```
total rows        286
  ⛔ excluded       36     STONE denominator excludes these by definition
  N/A excluded       2
STONE denominator  248
STONE green        188  =>  75.8%
raw                188/286 =  65.7%

non-green INSIDE STONE:  ❌14  ⚠️39  ☐2  ◑5  =  60
```

**The ⛔ tier is correctly parked, not neglected.** Classifying all 36 by their
recorded blocker:

| count | theme |
|---:|---|
| **23** | SUPERSEDED / obsolete assertion |
| 7 | other |
| 4 | cutover-dependent |
| 1 | env/scope-blocked (G5) |
| 1 | needs-mutation (G8) |

Since ⛔ is excluded from the STONE denominator, **walking these rows cannot move
the score** — and 23 of them are superseded assertions, which is precisely what ⛔
is for. Re-walking the tier would be motion without progress.

### What the 60 non-green STONE rows actually need

Every one has now been walked or classified against the running artifact:

- **14 ❌** — all walked 2026-08-08. 4 are #5894, 9 are inside the 51-HR cascade,
  **1 (R17) is a live defect and its fix shipped this session (PR #5917)**.
- **39 ⚠️** — 30 deploy-gated on the same cascade, 1 code-blocked (225,
  adjudicated), 7 UNKNOWN all resolved or attributed, 1 walked today.
- **2 ☐ / 5 ◑** — all seven stamped 2026-08-08 with named blockers; none is
  "untriaged".

So the honest statement of remaining work is not "60 rows to fix". It is: **one
credential (#5759), one OOM fix awaiting delivery (#5645), a handful of owner
adjudications, and R17 — which is already fixed.**

### THE NUMBER THAT REFRAMES EVERYTHING — 51 HelmReleases, one credential

Measured read-only on hw292, 2026-08-08, counting every HR whose **Ready**
condition (by type, not index) reads `DependencyNotReady`:

```
51  HelmReleases blocked
     14  flux-system/bp-cert-manager
     11  flux-system/bp-cilium
      7  flux-system/bp-cnpg
      6  flux-system/bp-flux
      2  flux-system/bp-keycloak
```

Five dependency names read like five problems. They are **one**:

```
bp-cilium  Ready=False :: SourceNotReady
  HelmChart 'flux-system/flux-system-bp-cilium' is not ready:
  failed to configure login options:
  no auth config for 'ghcr.io' in the docker-registry Secret 'ghcr-pull'
```

That is **#5759** — the half-reverted cutover left Harbor-only `ghcr-pull` auth
against `ghcr.io` chart URLs, so source-controller cannot pull, the HelmChart never
becomes ready, and everything downstream **fails closed**. The other four
"dependencies" are the next layers of the same cascade.

**This is one layer EARLIER than #5640 records.** #5640 says post-cutover Sovereigns
cannot receive newly published *images*. hw292 cannot resolve the *charts* either —
its GitOps loop is closed at the source. Rows previously attributed to "waiting on a
roll" are in fact waiting on a credential.

**All 14 ❌ rows are now walked (2026-08-08) and every one is accounted for:**

| rows | attribution |
|---|---|
| 37, 86, 90, 94 | #5894 fan-out truncation (catalyst-api OOM, #5645 merged/undeployed) |
| W1, W2, 29, 41, 57, 92, 115, 219, 234 | inside the 51-HR cascade — each verified against its own delivering HelmRelease |
| R17 | **the one live defect.** Fix shipped this session (PR #5917, mutation-tested) |

37 additionally collapses into **#5878**: newapi is the only SSO app with zero
oidc-gate references, so rows 37 and 38 are one defect seen from two directions
(first hit vs re-entry), not two.

**51 is not a backlog of 51 fixes. It is one credential and a fan-out.**

### The ⚠️ tier, classified 2026-08-08 — 30 of 38 are the SAME gate

The 38 unwalked ⚠️ rows were classified against the running artifact. They are not a
second backlog:

| verdict | count | note |
|---|---:|---|
| DEPLOY-GATED | **30** | same #5640 gate as the ❌ set |
| CODE-BLOCKED | 1 | 225 — adjudicated, `bp-newapi` is `allowedPlacements: [host, mgmt]`, a per-Org newapi is not a supported configuration |
| UNKNOWN | 7 | all resolved or attributed below |

**The seven UNKNOWNs, closed out:**

- **95** — mechanism named + fix merged (#5910/#5911).
- **184** — no assertion was ever authored (literal `—`); owner adjudication, tracked **#5867**.
- **5** — asserts `TIER=sme`, and `organization.yaml:120` declares `enum: [org, corporate]`, so the CRD **422-rejects** it. `sme` is also a banned term (GLOSSARY → `Organization`). The assertion is obsolete, not the console. Tracked **#5847**.
- **19** — backing data measured: **17 Applications vs 75 HelmReleases**. A 4.4× gap means counting cards settles it; no data-model reasoning needed.
- **33** — `bao.hw292.omani.works` returns **302** (SSO redirect, not the forbidden token prompt). Rendered-state half needs Playwright.
- **G7** — door B **proven by real creation** (`walk-stranger-two`, `isolation=vcluster`, `state=done`, vcluster StatefulSet 1/1). Door A is exactly **#5857**'s subject and is linked there.
- **165, 177** — both require a **genuinely failed** `cutover-step-*` row to exercise the per-row Re-run. A healthy Sovereign cannot produce one, so these are not walkable on a converged env by construction.

**Net:** across ❌ and ⚠️ together, **51 of 52 non-green rows outside ⛔** trace to the
#5640 delivery gate, an owner adjudication, or an env-lifecycle precondition. The lone
exception is **R17** — a live defect, fix sites named, and its HTTPRoute half already
has a PR in flight at **#5848** (whose scope misses the second orphan, flagged there).

### Every ❌ row walked live on hw292 — 2026-08-08 close-out

All fourteen were probed against the running env, each with a control. The result
is that they resolve to **three** causes, not fourteen:

| cause | rows | evidence |
|---|---|---|
| **#5894 fan-out truncation** (region B has zero per-Org listeners; the catalyst-api OOM cuts `orgConsoleTLSTargets` short) | 37, 86, 90, 94 | 10 fresh connections per host: per-Org hosts exactly **50%** reset, sovereign host **0/10**. Purchased apps return **302** (serving) on every request that completes — the funnel is not broken, half the requests never arrive |
| **deploy-gated, fix merged after the image** | R17\*, W1, W2, 29, 41, 57, 92, 115, 219, 234 | ancestry-checked individually; e.g. row 234's Kyverno §854 denial is fixed in `bp-stalwart-tenant` **0.1.14** (`935d23842`) while hw292 runs **0.1.13** — one version short |
| **empty DR CR layer** (#4986 shape) | 57, G3 | `continuums.dr.openova.io` and `cnpgpairs.dr.openova.io` both **installed with zero CRs** while shared-pg/-b/-c are all 3/3 healthy |

\* **R17 is the exception and the one genuinely-live defect found:** Org delete
leaves two orphans in PLATFORM namespaces, which a namespace-scoped cascade cannot
reach — `catalyst-system httproute/catalyst-ui-<slug>-omani-homes` and
`kube-system secret/org-wildcard-tls-<slug>-omani-homes`, both 4d21h after the Org
was deleted. A future Org re-using that subdomain would collide. Fix sites named.

**The leak is compounding while this sits.** catalyst-api on hw292 measured twice
in one session: `restarts 125 → 132`, working set `1900Mi → 3714Mi` of a 4Gi limit.
Seven more OOMKills in ~5 hours, currently at 93% and climbing. That single
uncorrected leak is the proximate cause of four ❌ rows, and its fix (#5645) has
been merged and published since 2026-08-04 without ever executing.

### So the actual shape of the remaining work

- **13 rows** (the 12 + **41**) — one roll, no code.
- **1 row** (29) — root cause established *and* fix merged (#5909); awaiting walk.
- **1 row** (95) — mechanism named *and* fix merged (#5910/#5911); awaiting walk.
- Everything else non-green is ⚠️/⛔, not ❌.

> **As of 2026-08-08 evening, every ❌ row has a named mechanism and either a merged
> fix or a deploy gate. None is unexplained.** All 15 now resolve to one of three
> states: waiting on the roll (13), or fix-written-awaiting-walk (29, 95). The one
> item still genuinely needing code is **#5878 / row 38** — newapi is the only SSO
> app not fronted by oidc-gate (measured: every other slot carries an oidc-gate
> reference, newapi carries zero), so its `/login?expired=true` is NewAPI's own
> application session, which a realm session does not satisfy. That row is ☐, not ❌.
>
> **Nothing in this list is closeable from source.** Every "fix merged" above is
> source-correct and unwalked, and delivery for all of it is gated behind **#5640**.
> The next state change for this file comes from a fresh prov, not another PR.

> **Row 41 moved buckets on 2026-08-08, and the reason generalises.** It was filed
> "needs code" because the *symptom* was verified first-hand and the *source* never
> was. One `git log --diff-filter=A` on the guard file found `6663dc441` — a fix that
> names this row and this address in its own comment, merged four days before the
> walk that "discovered" the problem. **Ten issues have now resolved this way in one
> session** (#5750, #5752, #5767, #5817, #5823, #5825, #5833, #5835, #5720, and
> #5894's #5511/#5645 chain). At that rate the prior should be inverted: on a
> deploy-gated env, assume a fix exists and prove it does not, rather than the
> reverse. The check costs one command and has been right ten times out of ten.
>
> Method caution from the same hour: `git log --grep="(#5720)"` matched the WRONG
> commit — the parentheses are regex groups, so it silently returned an unrelated
> recent commit and produced a confident, wrong ancestry verdict. Anchor on something
> unforgeable instead — the commit that *added the guard file*
> (`--diff-filter=A -- <path>`) — then compare. Verify the subject before the value.

### Why this section supersedes a prior claim in this file

An earlier revision asserted every ❌ row was deploy-gated and **no failing row
needed code**. That was true of the morning's classification and stopped being
true the same afternoon — five live defects surfaced with no PR to cite, so they
classify as UNKNOWN or deploy-gated by construction. The classifier answers
*"is the fix I know about deployed?"*, never *"is there a fix at all?"*. This
table separates the two by pairing every UNKNOWN with a live walk.

## Live defects found by walking, NOT deploy-gated (2026-08-08 afternoon)

These are new code, not pending deployments. Each was found on hw292 as it runs
today, and each is recorded with the measurement that produced it.

> **One of them was not, and the correction is the point.** #5894 sat in this table
> — and, before that, parked as *needs-design* on the issue — on the strength of a
> real measurement and a **wrong causal story**: I had concluded the org-controller
> holds a single `client.Client` and structurally cannot reach region B. It does not
> build that surface at all; catalyst-api does, and #5511 gave it a per-region seam
> back on 2026-07-30, *inside* the running image. Following it to ground turned a
> "needs a topology decision" into "a merged fix (#5645) that has never executed",
> which is a **completely different kind of work** — nobody has to design anything.
> A correct measurement with an unverified mechanism attached is still a wrong row,
> and it points the next reader at the wrong file. The generalisable check: before
> filing a defect as new code, name the component that actually writes the surface
> and `git log --grep` it — a fix may already exist and be sitting one deploy away.

| issue | what breaks | how it was found |
|---|---|---|
| ~~**#5894**~~ **moved out of this table — it IS deploy-gated (2026-08-08)** | per-Org consoles reset the TLS handshake **~50%** of the time, on both Orgs and both pool TLDs | 12 probes per host across 6 hosts: 4 sovereign-domain hosts **0/48 resets**, 2 pool-TLD hosts **exactly 50%** — isolates it to the pool-TLD cert/listener, one region serving it and one not. **Then followed to ground:** region B *has* `cilium-gateway-console` but **zero per-Org listeners**, while region A has both (`uatco`, `walk-stranger-two`) — so #5511's fan-out reached the Gateway and stopped. Neither guard in `orgConsoleTLSTargets` closes on identity (`SOVEREIGN_FQDN=hw292.omani.works`, both regions configured, `isChroot()` true). The truncation is a **catalyst-api OOMKill**: `restarts=125, exit=137, limits=4Gi, requests=96Mi, in-flight 1900Mi`. `orgConsoleTLSTargets` returns the host region as `targets[0]` and appends secondaries after, so a kill during the long per-region admission poll (`org_console_tls.go:266,437`) completes region A and dies before region B — exactly the observed asymmetry. `api-deployment.yaml:1814-1846` already recorded this from **#5642** four days earlier: the leak is `Factory.AddCluster` rebuilding all 42 informers on byte-identical kubeconfigs, ~62 Mi/min, **fixed in #5645 (MERGED) but never executed** because no published image reaches a `cutoverComplete=true` Sovereign — **#5640** |
| **#5895** | first visit to a per-Org console renders a **blank page**; a reload recovers | cold browser stops at the `prompt=none` silent-auth URL with `body_len=0`, unchanged after 25s |
| **#5882** | OpenBao SSO session lands with no token prompt but its policy **cannot list mounts** — Secrets pane renders `403 permission denied` | authenticated shell renders, content pane errors |
| **#5883** | a **mistyped email** at the PIN form persists a Keycloak realm user (`emrha.` alongside `emrah.`) | counting the Users list — a presence check matches both strings and reports green |
| **#5878** | newapi is the only SSO app that does not accept the realm session — bare URL 302s to `/login?expired=true` | 5 apps land signed-in off one session; newapi expires it |
| **row 95** | a purchased app never becomes an Application — checkout recorded `Apps (1)`, the Org converged, no Application exists | control: the first Org's apps **are** in the same list, so the absence is real |

**Why none of these were visible to the classifier.** It resolves a row's *cited
PRs* against the running image. A defect with **no PR yet** has nothing to cite,
so it classifies as deploy-gated-or-unknown and disappears into the "waiting on a
roll" bucket. The classifier answers "is the fix I know about deployed?" — it
cannot answer "is there a fix at all?". That gap is what walking found.

**Method note worth carrying, since it produced four of the six.** Every one of
these needed either **repeated measurement** (a single probe reports the flapping
console as working) or a **control** (the first Org, the sovereign-domain hosts,
the other Org's apps). A negative with no control is not a finding — today it was
wrong eight times out of eight before a control was added.

### The numbers this section carried until 2026-08-08 were wrong, and understated

The previous text read *"DEPLOY-GATED 12 / CODE-BLOCKED 1 (row 20) … 8 more in
⚠️ … so 20 rows across both tiers are waiting on a roll."* The true figures are
13 / 0 and 61. Three defects in the measuring tool produced that, each fixed and
each the same underlying error — **stating a conclusion the evidence does not
support**:

| defect | effect on this file's numbers |
|---|---|
| #5851 — a row citing the **issue** rather than the PR that closed it printed `CODE-BLOCKED / "no PR cited"` | Row 20 was the "lone exception". Its fix merged as **PR #5688 on 2026-08-05**, three days after the artifact was built. It was deploy-gated all along. |
| #5855 — rows were split on **escaped** pipes, truncating Evidence | Every row carrying a `\|` was classified on a **prefix of its own record**. Row 219 was judged on 2 of its 9 cited PRs. This is what understated ⚠️ as 8 rather than 28. |
| #5859 — `deploy` (109 commits) and `ci` (5) were **unrecognised prefixes**, 28.5% of the last 400 on main | Citations fell into a bucket printed *"cited but unmerged"*, asserting a PR exists that does not. Rows 5/6/10/11 read CODE-BLOCKED purely from this. |

Root cause of the third is structural and worth carrying: **squash-merge replaces
the commit subject with the PR title**, so every commit in a multi-change PR
inherits that PR's overall type. Per-commit subject classification therefore
reflects the PR, not the change.

`CODE-BLOCKED` now requires **positive** evidence — a fix present in the artifact
that still fails. Absence of a citation reports `UNKNOWN`, because git alone
cannot separate "unmerged PR" from "tracking issue" from "fix squashed under a
non-fix title".

### ZERO rows require new code — the three CODE-BLOCKED entries are not engineering

The classifier flags 192, 225 and 231 as CODE-BLOCKED, which is literally true —
their cited fixes are inside the running artifact and the rows still fail. But
**CODE-BLOCKED names a measurement, not a remedy**, and reading each row shows
none of the three is waiting on engineering:

| row | classifier says | what it actually needs |
|---|---|---|
| **192** | fix in artifact, still fails | a **contract decision**. The deep-link half PASSES; the link half is absent because `ConvergenceWizard.tsx` was orphaned from the router by `be5477c43`. Drop the wizard-link clause, or delete the dead component — both change what the row tests. Guarded + named by #5831 rather than left silent. |
| **225** | fix in artifact, still fails | a **precondition**. The #4278 mechanism is proven green (SA exists, hook Completes 1/1 in 5s, HR Ready on bp-newapi@1.4.146, CNPG 2/2). hw292 simply has no per-Org bp-newapi — uatco bought wordpress + openclaw + stalwart-mail + agenity. Needs an Org that BUYS newapi: a funnel mutation. |
| **231** | fix in artifact, still fails | **nothing — its reason is stale.** Walk column `repo-render-2026-08-02`; evidence reads "none exists (hw291 wiped, hw292 **unfired**; catalyst ns 0 pods)". hw292 fired 2026-08-03T04:04Z and hit cc=true at 08:12:30Z, ~14h later. Rows 49/50 prove bp-postgres instances exist there with `singleton` as the pre-selected default. Walkable now. |

**So the honest statement is stronger than "61 deploy-gated, 3 code-blocked":
no row in this 286-row ledger requires code to be written.** Every non-green row
is waiting on a roll, a walk, a funnel mutation, or an owner's adjudication.

Row 5 is a case the delivery axis cannot express at all: it reads DEPLOY-GATED
because one clause cites a merged-but-undelivered fix, while a **different**
clause (`TIER=sme`) asserts a value the Organization CRD 422-rejects and never can
satisfy. A row can be simultaneously deploy-gated and assertion-defective; this
tool reports only the delivery dimension. See #5847.

**Correcting this file's own text from earlier the same day.** The paragraph here
previously read "Three ⚠️ rows (192, 225, 231) carry positive code-blocked
evidence" and stopped. That was accurate about the measurement and misleading
about the work — exactly the failure mode the three classifier defects above
produced, repeated one level up: taking a tool's label as a conclusion instead of
reading what the rows say.

**What this changes.** The next action for the non-green set is delivering the
train — not another fix wave. Four separate investigations in one session (R17's
cross-region reaper, W1/W2's wizard defaults, rows 35/115's bp-guacamole
`crossRegion`, row 92's 429 notice) each independently terminated in "the fix is
merged and the running artifact predates it". That is the same finding four
times, which the classifier now surfaces in one command.

**Three traps the script encodes**, all of which produced wrong answers by hand
before being caught:

0. **Do not read the running artifact off `GET /api/v1/version`'s `buildTime`.**
   That field silently falls back to PROCESS START time when the build-time
   ldflag and `CATALYST_BUILD_TIME` are both unset, keeping the same key name —
   so a pod restart reads as a fresh build and flips every DEPLOY-GATED row to a
   false CODE-BLOCKED. `buildTimeSource` (#5821) now names the branch.

1. A cited PR is often a `docs(uat)` **walk record**, not a fix — #5615 and
   #5617 are the walks that recorded rows 219/234 failing. Counting those as
   fixes turns a code-blocked row into a false deploy-gate, so the commit
   *subject* is classified, not merely its presence.
2. The row's verdict is the **6th pipe-delimited cell**, not "does the line
   contain ❌". Several ✅ rows cite ❌ in their evidence prose; matching the
   whole line over-counted failures (W3 is the example — status ✅, prose
   contains ❌).

The script **refuses to guess the running artifact** and exits non-zero without
`--image`/`UAT_IMAGE`, because a wrong artifact inverts every verdict — the same
wrong-subject failure mode as
`feedback_never_declare_an_env_dead_without_probing_its_own_record_fqdn`.

---

## Every non-green row now states its next action (2026-08-08)

With the classifier trustworthy, the 13 UNKNOWN rows were read individually
rather than left as "the tool cannot tell". **None is code-blocked.** What the
whole remaining set actually needs:

| what it needs | rows | count |
|---|---|---|
| **the roll** (fix merged, artifact predates it) | ❌ 13 · ⚠️ 30 · ☐ 20 | **63** |
| **a walk** — precondition now satisfied | 6, 10, 11 | 3 |
| **a funnel mutation** — 2nd voucher + 2nd Org | 93, 94, 95 | 3 |
| **a funnel mutation** — an Org that BUYS newapi | 225 | 1 |
| **one click** (a write; scheduling, not permission) | 177 | 1 |
| **three login attempts** | 235 | 1 |
| **fault injection** — needs a genuinely failed cutover step | 165 | 1 |
| **an owner adjudication** | 5, 19, 184, 192 | 4 |

The adjudications, stated so they can be answered without re-deriving them:

- **row 5** — demands `TIER=sme`, which the CRD 422-rejects (`enum: [org,
  corporate]`), *and* `ISOLATION=vcluster` without the #4292 plan qualifier its
  siblings 10/11 received. Two clauses, neither satisfiable by a correct
  platform. #5847.
- **row 19** (#5867) — the grid renders 46 blueprint SLOTS; the row asserts one card per
  **Application**. The feed and the CRs now agree exactly (14 = 14), so this is a
  model disagreement by design: re-key the grid, or amend the clause.
- **row 184** (#5867) — has **no assertion at all**; the cell records that one was never
  authored. Row 186, the other assertion-less row, is already N/A. This one sits
  in the denominator as non-green. Changing it alters the denominator, so it is
  not taken unilaterally.
- **row 192** — `ConvergenceWizard.tsx` owns the asserted testid and was orphaned
  from the router by `be5477c43`. Drop the clause or delete the component. The
  orphan is now loud rather than silent (#5831).

**Nothing in this table is waiting on engineering.** The single largest lever
remains the roll: 63 rows move on it, and none of the other categories exceeds
four.

Re-measured after each stamp rather than carried forward — R22 moved UNKNOWN →
DEPLOY-GATED once it cited its own fix PR (#5813), which is why this figure is 63
and not the 62 an earlier draft of this section carried.

---

## Where the ledger actually stands

The ledger was reset for this env — `scripts/reset-uat.py hw292` flushed 135 hw291 evidence cells to ☐/⏳ on 2026-07-31, per the founder's each-new-env-flushes-all-evidence law — and has been re-walked upward from there ever since. It is **no longer** near-zero, so the "a raw tally reads near-zero by design" note that stood here from 2026-07-31 has been retired rather than left to mislead.

**Live tally, `scripts/uat-tally.py` on this commit (2026-08-06): 156 ✅ / 286 data rows = 54.5%** — ✅ 156 · ⚠️ 47 · ◑ 5 · ❌ 19 · ☐ 12 · ⛔ 45 · N/A 2. Read the verdict from the status column only; the tally script does that and a whole-line glyph search does not (it over-reported by 23 rows when last measured, always optimistically).

The **denominator honesty** matters as much as the numerator: 45 rows are ⛔ — assertions superseded by a merged, founder-approved design decision — and they can never go green as written. The largest single block is the 11-row placement family (rows 98–108), voided by #4325's deliberate de-vcluster of the mgmt/rtz/dmz planes. Those are not product faults and must not be counted as gaps; equally, they are not passes. Until the founder rules on rewrite-vs-exclude (see "Not achievable as written" below), 54.5% is the honest raw figure and 156/241 = 64.7% is the honest figure with the ⛔ set excluded from the denominator. Quote whichever, but say which.

The last **walked** tally, on hw291 before the wipe, was **135 ✅ / 281 = 48.0% raw** (`dc18c6d9a`). The last walked north-star remains hw288 at **214 ✅ / 281 = 82.0%** (2026-07-26). Durable structural state — the number that survives a flush — stands at the last evidence-backed value; see the completion matrix, and change it only on walk evidence.

---

## The delivery gate is the whole story this cycle

Every fix listed below is **merged and inside the image hw292 provisions with**. That claim is not an assumption — it is checked mechanically. The catalyst-api / catalyst-ui image pin on `main` is commit `fb41faf`, and a fix is delivered iff:

    git merge-base --is-ancestor <fix-sha> fb41faf

| issue | fix | delivered on hw292 | verification done pre-walk |
|---|---|---|---|
| #5515 derivePattern fails open into `singleton` | PR #5519 → `796e587b2` | ✅ ancestor | `placement.ts:107-108` returns `PATTERN_NOT_REPORTED` when `primaries === 0`, making the `singleton` return unreachable without a Primary; `placement.test.ts` 21/21 incl. a dedicated #5515 block pinning **both** directions |
| #5489 four surfaces report a vCluster that does not exist | PRs #5526 `8523ecf33`, #5490 `54614d3b3`, #5524 `908f76b0e` | ✅ all three ancestors | root fix at `organization_controller.go:1110` — `vclusterStatusFor` keys off the **same** `gitops.BoundaryIsVcluster` predicate that decides authorship, so status cannot disagree with reality; 10 tests green across handler + UI + controller |
| #5477 `replicationLagSeconds: 0` published as a measurement that was never taken | PR #5478 → `c9b2f9f4c` | ✅ — pinned as the controller image itself, `products/continuum/chart/values.yaml:41 tag: "c9b2f9f"` | code present; live re-check of the four Continuum CRs rides the walk |
| #5485 defects 1–3 (reconciler log prefix, showback Job rows, treemap ReplicaSet names) | `a854cb7ae`, `6844b38f6` | ✅ ancestors | defect 1 unit-proven; defects 2+3 **live-verified on hw291 before the wipe** (authed owner API: one platform row, zero itemized Job rows, zero hash names, cost conserves exactly) |
| #5531 gitea OAuth state lost on pod roll | PR #5533 → `fb41faf30` | ✅ (it *is* the tip) | `[session] PROVIDER = db`; bp-gitea **1.2.49 published + signed** after the publish blocker below was fixed |

**Publish blocker found and fixed this cycle:** bp-gitea 1.2.49 silently failed to publish because `httproute-render.sh` piped `echo` into `grep -q` under `pipefail` — `grep -q` closes the pipe on first match, `echo` dies with SIGPIPE 141, and the pipeline reported FAIL **on a passing value**. Herestrings removed the pipe (`bf59fb5f0`); blueprint-release green, 1.2.49 on ghcr. Any chart whose tests use that idiom is exposed to the same false failure.

**Formerly held pending hw292 readiness** (a deploybot roll mid-prov abandons the provisioning — prov-preflight gate 5). hw292 reported ready and the freeze thawed; **both merged**:

| PR | issue | state |
|---|---|---|
| #5534 | #5485 defects 4–6 (suspended HR shows healthy, `?status=` ignored, `/fleet/applications` duplicates) | **MERGED** |
| #5535 | #5488 cutover pre-flight aborts on a recoverable condition | **MERGED** |

Both are in `main` and inside published images — but read Gate 5 below before treating that as delivered to hw292, which it is not.

---

## Gate 1 — Pillar 5 (cutover): mechanism proven, hardening delivered

hw291 reached **`cutoverComplete=true` 2026-07-30T11:03:43Z**, all 11 steps, 600s deny-egress hold green in both regions. G11/166 banked. The gate is no longer "does it work" but "does it survive the awkward cases":

- **#5488** — after a catalyst-api restart (`strategy: Recreate`), the secondary-kubeconfigs pre-flight aborted with `expects 1 secondary region(s) but only 0 kubeconfig(s) are readable (missing/unreadable: )` while the Secret was already correct. Root cause runs deeper than the issue described: `chrootEnsureDeployment` minted a record with an **empty `ID`** under map key `""` with no guard, reachable from ~40 endpoints via unchecked URL params, and `chrootServesDeployment` discriminates on FQDN alone — so `sync.Map.Range` nondeterministically served the poisoned record. Fixed at mint, at resolution, and by accepting an already-materialized Secret. The #5359 fail-loud contract is **unweakened**: genuinely-missing data still aborts before step 1. PR #5535, held.
- **#5391 / #5393** — a per-Org customer app's HelmRelease can still gate the platform's cutover, and plan-s quota misses `bp-keycloak` by 50m CPU / 64Mi (one oidc-gate sidecar). **Founder decision, not an engineering call.**

## Gate 2 — founder-gated credential (#4277)

Rows 219, 220, 222 and G8/G9 need the Anthropic credential; `seedAnthropicToken` loud-skips without it. **No engineering work produces a live credential** — that remains the one genuinely external dependency in the ledger.

**Corrected 2026-08-10 (#5956, PR #5968) — "no engineering work clears these" was true of the credential and false of everything around it.** Read literally, that sentence sent every reader past a real defect for weeks. The credential was seeded and then *revoked upstream*, and four surfaces reported green over it: the ExternalSecret (`SecretSynced`), the #4111 pre-flight (which logged verbatim `claude-code OAuth token valid (~7h remaining).` from `expiresAt` alone, at boot only), `/api/v1/runtime/claude-status` (`logged_in: true`), and — worst, because it is ours and it is the only *periodic* one — the #4877 seed reconciler, whose `openbaoPathHasProperty()` check a revoked token satisfies perfectly, every 10 minutes, forever. All four asked about **delivery**; the row asks about **validity**. That gap was entirely ours to close and needed no credential to close it: `anthropic_credential_health.go` now classifies validity from an authenticated API probe, and `healthy` is reachable only via a live 2xx/429.

**G9 additionally has a cause that has nothing to do with the credential**, and folding it under this gate hid it: the chat surface is unreachable because CNP `uatco/allow-gateway-and-apiserver` grants no `:53` egress, so the gate's Keycloak token exchange cannot resolve. That fix is **already merged** (`7148b21da`, Refs #5617) and merely undeployed — a deploy, not engineering work, and certainly not a founder credential. Before writing "nothing clears this row", check whether the row has a *second* gate.

## Gate 3 — filed defects with named owners

| rows | defect | state |
|---|---|---|
| 110, 112, 114, 115 | #5389 per-app Open/launch does not land in the app | **root-caused 2026-08-06** (see below); fix in flight. Rows 110/112/114 have since walked ✅ on hw292 — those are *platform* apps on `<app>.<SovereignFQDN>`, which is the one shape that resolves. Row 115 is still ❌ but for an unrelated pair of gates (PIN wall #5642 + zero seeded guacamole connections, #5598), not for the hostname defect |
| 35, R9 | #5358 guacamole blank page after the SSO round-trip completes | **root cause CONFIRMED 2026-08-06 on hw292, read-only kubectl both regions** (#5750): `bp-guacamole` HR pinned `0.2.33` in BOTH regions (`main` is `0.2.37`) — 3 versions behind the merged 0.2.36 crossRegion-singleton fix. Live pods show both regions independently running a full webapp+guacd+CNPG stack, and live logs show region-A and region-B each independently authenticating the SAME principal 9s apart — the exact #5358 per-Pod `HashTokenSessionMap` split. Chart fix is correct + complete on `main` (`crossregion-render.sh` 5/5 PASS); hw292 is stuck pre-fix by the SAME `mirrorResync.enabled: false` severance + #5640 version-skew catch-22 as the 5b image-leg case above (cutover chart frozen at `0.1.159`, the CATALOG leg needed to unfreeze it ships at `0.1.170`). New diagnostic `scripts/check-guacamole-crossregion-drift.sh` deterministically classifies this shape (live-verified: KNOWN-DRIFT) so a future walker doesn't need hours of archaeology to reach the same conclusion. Row 35 stays ❌ — no live delivery landed this pass; re-verifies once hw292 (or #5640) closes the gap |
| G12 | #5388 region-kill failback left a data split-brain | first fix merged (cnpg-pair 0.2.23 surfaces peer-probe starvation); re-proof rides hw292 |
| R17 | #5364 org-delete leaves a half-teardown | filed, reopened on runtime evidence |
| 212, 213 | per-Org MCP — #5516 proved Org-scoped reads never worked on any env; fix PR #5522 routes to the own-org seam | merged, delivered on hw292 |
| — | #5385 deployment health aggregate reads stale-degraded (trust kubectl, not the badge) | filed, affects walk trust |

### #5389 root cause — the endpoint hostname vocabulary cannot express a per-Org app host

Found 2026-08-06, measured live on hw292 (dep `1c56518035a83e03`, `cutoverComplete=true`). The earlier row-114 fix (`bp-newapi` declaring `endpoints: []`) was correct but addressed only a *missing declaration*. The residual defect is deeper: for a per-Org app the declared hostname **evaluates to a host that does not exist**, so the launch button is not mis-styled — it points nowhere.

**Per-Org apps are served on the tenant POOL domain.** Every per-Org hostname actually served on hw292:

```
agenity.uatco.omani.homes
console.uatco.omani.homes
console.r17probe.omani.homes
wordpress.uatco.omani.homes
```

Across all 21 live `HTTPRoute`s in both regions, **zero** hostnames match `<app>.<org>.hw292.omani.works`. Platform apps sit on `<app>.hw292.omani.works` (`gitea.`, `grafana.`, `registry.`, `auth.`, `bao.`, `newapi.`, …) and resolve fine — which is why rows 110/112/114 pass and the per-Org rows do not.

**But every per-Org blueprint declares the SovereignFQDN shape.** 30 `hostnameTemplate` declarations across 26 blueprints use `<app>.{{.OrgSlug}}.{{.SovereignFQDN}}` — including the two apps that are actually live per-Org on hw292:

- `products/agenity/blueprint.yaml:175` → `agenity.{{.OrgSlug}}.{{.SovereignFQDN}}` → evaluates to `agenity.uatco.hw292.omani.works`; **served host is `agenity.uatco.omani.homes`**
- `platform/wordpress-tenant/blueprint.yaml:278` → `{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}` → evaluates to `wordpress.uatco.hw292.omani.works`; **served host is `wordpress.uatco.omani.homes`**

plus `neo4j` (x2), `llm-gateway`, `librechat`, `opensearch` (x2), `stalwart-tenant` (x3), `valkey`, `milvus`, `matrix`, `temporal`, `langfuse`, `livekit`, `openmeter`, `flink`, `stunner`, `litmus`, `clickhouse`, `ferretdb`, `iceberg`, `kserve`, `strimzi`, `vllm`, `bge`, `anthropic-adapter`.

**No blueprint can currently be written correctly, because the vocabulary has no token for the pool domain.** `evaluateHostnameTemplate` (`products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go:1889`) substitutes exactly three tokens — `{SovereignFQDN}`, `{OrgSlug}`, `{AppName}` (plus their `{{.X}}` aliases) — and both call sites pass the Sovereign FQDN and never the Org's pool domain:

- `:713` — `evaluateHostnameTemplate(ep.HostnameTemplate, h.endpointSovereignFQDN(), org, appName)` (silent-SSO launch URL)
- `:1816` — same three arguments, over `bp.Endpoints` (resolved-endpoint list)

So the per-Org host is not merely mis-templated in 26 files; it is **inexpressible**. Fixing the blueprints alone cannot work — the resolver needs an Org-pool-domain token and the callers need to supply it.

**It also fails open, which is why this reached a walk instead of a 500.** `strings.NewReplacer` leaves an unrecognised token untouched, the result is then `strings.ToLower`-ed, and `buildLaunchURL` emits it as a URL regardless. An unresolvable template therefore produces a confident-looking dead link rather than an error — the same failure-open shape called out in the [render-guard](reference) and fail-open-CI lessons.

**Separately, `bp-openclaw` reproduces the row-114 dark-button defect exactly.** `platform/openclaw/blueprint.yaml:25` is `visibility: listed`, the chart ships `platform/openclaw/chart/templates/httproute.yaml`, and the blueprint declares **no `endpoints` key at all** (zero occurrences) — a listed, route-bearing app with nothing for the console to render a launch control from.

Full write-up on **#5389**; fix in flight. Do not stamp the per-Org launch rows green on a link that renders — check that the host it points at is one the Sovereign actually serves.

## Gate 4 — the ⚠️ partials

Still the largest actionable group and still the highest-leverage triage target. Two were resolved by adjudication rather than code this cycle, which is the pattern to repeat where the assertion — not the platform — is what is stale:

- **row 22** (showback platform-overhead roll-up) → ✅. Tenant-ns Job pods routing to `__platform__` is the *deliberate, test-encoded* contract of #5493 (`org_consumption_test.go:186`, `:35`, `:63`) and matches the row's own wording ("holding all control-plane/Job workloads"). The predecessor-env symptom — a *named* tenant app inside the platform app list — is structurally unrenderable after the one-shot fold.
- **row 55** (topology single value) stays ⚠️ honestly: all three topology clauses pass, and the residual is a **region** field, not a topology value — host-native singletons carry `primaryRegion: platform-bootstrap-owned-host` because the data layer emits the bootstrap host label as a region. The read-side fix (`b41c93b3c`) is delivered; the data-layer emit is the open half.

## Not achievable as written (⛔)

Placement rows **98–108** are superseded by the #4325 de-vcluster reclassification (founder verdict); row 109 is also ⛔ but for an unrelated reason (the KC account-console REST 401 is an accepted consequence of passwordless-PIN, #688 — its #3642 link was a mis-link and is corrected in the ledger). Plus R1/M1/G5 (janitor), R19 (sandbox — concept removed 2026-06-30), R20 (delivery), 94/95 (funnel). For the ledger to reach 100% these need either rewriting to match the shipped design or formal exclusion from the denominator. **Founder call.**

The 98–108 supersession is **re-anchored to live state 2026-08-06**, not resting on the merged PR alone — this family was re-flipped ❌ once before by a walker who re-asserted the dead expectation (corrected in `d8761bf2b`), so every one of the 11 rows now carries a dated hw292 live re-confirmation and `env=hw292` in place of its wiped-env stamp. Measured on dep `1c56518035a83e03`: across 53 namespaces the only vcluster-related one is `vcluster-system` (the controller ns, itself holding zero workloads), no `mgmt` / `rtz` / `dmz` namespace exists, and all 7 named platform apps run in host namespaces with live pods (`gitea`, `grafana`, `guacamole`, `harbor`, `keycloak`, `newapi`, `openbao`). Row 107 in particular asserts the exact **opposite** of the correct post-#4325 design — "none of the 7 named apps appear under `host`" — when host is precisely where they now correctly belong. **This is verdict classification against a merged decision plus live state; no walk is claimed by any of these 11 rows.**

## §854 disposition — closed, and re-proven this cycle

`scripts/check-no-nodeports.sh` on `main`, 2026-07-31: **zero NodePorts across sources + rendered charts**, every chart rendered. The guard is itself vacuity-tested (#5518, merged): it self-checks that its pattern matches 4/4 banned forms, rejects 2/2 allowed forms, and scans ≥200 files before trusting a pass — so a green result can no longer mean "the guard matched nothing". A raw `grep NodePort` across the repo returns hundreds of hits; every one inspected is the ban's own prose — comments explaining cilium's internal BPF-masquerade requirement, the documented removal of the #4691 fallback, and the guard workflow itself. **Raw grep is not the audit; the render-and-adjudicate guard is.**

**Correction, 2026-08-04 — the enforcement half of that claim held in only ONE region.** This file previously stated flatly that Kyverno `forbid-nodeport-service` "runs Enforce with a fail-closed webhook, made tamper-evident by #5386". Measured live on hw292:

| region | HR `values.compliancePolicies` | ClusterPolicies at Enforce | `forbid-nodeport-service` |
|---|---|---|---|
| region-a | `{"bootstrapMode": false}` | 9 of 25 | **Enforce** |
| region-b | `{}` — never patched | 1 of 25 | **Audit only** |

So on a 2-region Sovereign the NodePort ban was *advisory* in half the fleet: region-b would record a NodePort Service, not block it. The §854 literal scan still passes in both regions (0 NodePorts; 176 svc region-a / 159 region-b **as counted on 2026-08-04** — region-a has since grown to 192, see the 2026-08-06 re-measurement below), so nothing was violating it — the gap is that region-b would not have *stopped* one. Root cause is **#5591**: the Wave 5.90 phase-2b `bootstrapMode` flip reached only the primary region. The source fix is merged (`bb8ceec71` #5592, compile-repaired `ef2d59767` #5619), unit-tested (`TestPolicyEnforceFlip_FlipsEveryRegion_5591`), and delivered in published images — but hw292's phase-2b ran *before* delivery, so its region-b remains unflipped until remediated.

The lesson generalizes past this instance and matches the [per-region split class](reference): a security posture verified in one region is not a fleet property. Assert it per-region or do not assert it.

**Update, 2026-08-06 — the gap is now CAUGHT BY A GUARD, not merely described here.** The correction above stated the split but left nothing enforcing it, so the same drift would have gone unnoticed on the next env. `scripts/check-live-nodeports.sh` gained a **Phase 2 enforcement-posture check** (PR **#5696**, merged `712860075`): it reads `forbid-nodeport-service`'s `spec.validationFailureAction` in the cluster it is pointed at and **fails closed** — `Enforce` → pass, `Audit` → FAIL, policy absent → FAIL, any unrecognised action → FAIL. Its verdict classifier is vacuity-self-tested in-script (`Enforce`→PASS, `Audit`→not-PASS, `""`→ABSENT, `weird`→not-PASS), so a degenerate always-pass cannot survive its own file. Phase 1 (the literal scan) passing no longer implies compliance; the script says so in its own failure text.

Re-measured live on hw292 (dep `1c56518035a83e03`) 2026-08-06, and the guard demonstrably discriminates **in both directions on the live fleet** — which is what makes this a proof rather than an assertion:

| region | Services scanned | live NodePorts | `forbid-nodeport-service` | `check-live-nodeports.sh` exit |
|---|---|---|---|---|
| region-a (`me-east-215-a`) | 192 | 0 | **Enforce** | **0 — pass** |
| region-b (`me-east-215-b-1`) | 159 | 0 | **Audit** | **1 — FAIL** |

Both regions are still literally clean (0 NodePorts), and that is exactly the point: the only thing separating them is the *posture*, and only Phase 2 can see it. The source-side every-region flip (#5592/#5619) remains correct and merged — hw292 simply provisioned before delivery, so its region-b stays unflipped until remediated (item 3 below). Fleet enforcement is proven by running the guard **once per region kubeconfig**; running it once against the primary is the exact mistake that hid this for days.

---

## Gate 5 — the delivery chain itself (found 2026-08-04, the largest single cause of stalled progress)

Steps 1–3 of this plan were never the constraint. **The chain from `main` to a running binary was broken in two independent places, and both looked like "the fix didn't work".**

**5a. `build-ui` was failing, so nothing published at all.** Across the last ten `catalyst-build` runs on `main`, six had `build-ui=failure` with `deploy=skipped` — meaning six merges published *no image*. The cause was a genuine race in `ExecPanel.test.tsx` (#5633): the component renders byte-identical markup for `fallback-loading` and `fallback-ready` and dials its socket in a passive effect, so under CPU-starved CI the scheduler yields between commit and effect flush and the assertion runs before the socket exists. Reproduced 8/8 under load, 0/12 after the fix. **Fixed and merged (#5651); two consecutive greens since, each with `deploy=success`.** #5626 and #5633 closed on that evidence.

Behind it sat the reason nobody noticed for two months: the vitest step ran `npm test || echo WARN` from `f61a52ab6` until #5553, so the gate **exited 0 unconditionally**. When it finally told the truth, four source defects surfaced at once. A fail-open gate is worse than no gate — it manufactures the appearance of coverage.

**5b. Even a published image cannot reach a cutover Sovereign.** hw292's local Harbor holds exactly `["fad88bd"]` for catalyst-api — the step-03 prewarm tag, digest byte-identical to the running pod. **18 catalyst-api tags have published to ghcr since; zero arrived.** Three independent severances: `proxy-ghcr` has `registry_id = None` (a push-populated project, not a pull-through cache), `mirrorResync.enabled: false` is the deliberate Principle-14 git severance, and #5644's Day-2 IMAGE leg ships in cutover chart `0.1.161`/`0.1.162` while hw292 runs `0.1.159` — *the remedy cannot pass through the gap it remedies*. Note also that the shipped default `mode: detect` writes the missing set to a ConfigMap and copies nothing; only `mode: warm` delivers, and it needs an operator credential. Tracked as **#5640**.

**Correction, 2026-08-06 (re-measured live on hw292, not read off this doc).** All three severances still hold, and all three are *correct by design* — `proxy-ghcr` carrying no `registry_id` is #4975 deliberately replacing the proxy-cache with a push-mode mirror so step-08's deny-egress hold works at all. The above is also incomplete in a way that matters: it implies the Day-2 reconciler would deliver once it arrives. **It would not.** Its chart and image legs enumerate their work from the **local Gitea mirror**, and post-cutover that mirror is frozen (`mirrorResync.enabled: false`; nothing re-runs step-01). So both legs are structurally a no-op on a steady-state cut-over Sovereign — they can only ever deliver artifacts for pins that *already exist* in the frozen mirror, and then correctly report `ALL_PINS_PRESENT` forever, because every frozen pin genuinely is present. Live: local Gitea `main@7bcd1d59` pinning bp-self-sovereign-cutover `0.1.159` while upstream `main` had reached `0.1.169` — ten chart versions, zero arrived, nothing red. The same freeze is why #5650's `charts.loft.sh` fix (0.1.168) has not landed either. **The git pin is the head of the chain**, and it is what PR #5703 adds: a CATALOG leg inside the same one-shot, scope-bounded, audited operator window, running *first* in each cycle so one request moves the whole chain. The bootstrapping trap is real and is not engineered around — chart 0.1.170 cannot deliver itself to a Sovereign cut over before it, so the first delivery per Sovereign is `scripts/sovereign-daytwo-bootstrap.sh` (re-stamps the cutover's own step-01 + step-03 from PodSpec ConfigMaps already on the cluster), once, after which delivery is in-band.

The practical consequence: **#5642**'s catalyst-api leak fix (`Factory.AddCluster` rebuilt all 42 informers on byte-identical kubeconfigs) is correct, merged, published — and has never run. The live pod still leaks **62.1 Mi/min**, monotonic, 21 restarts at a ~59-minute cadence. Raising the memory limit remains the wrong fix: at that rate an 8Gi ceiling buys three hours instead of one and hides the defect.

## Gate 6 — sovereignty proves the wrong property (#5650)

Step-08's deny-egress hold is genuinely deny-*all* (`defaultDenyEgress: true` since #3678) — an earlier claim in this session that it blocked a named list was wrong and was corrected on the issue. But the hold is **time-boxed and then torn down**, so it proves nothing external was *reachable* for 600s. It cannot prove the Sovereign has no external *dependency*. Live: `vcluster-system/loft` → `https://charts.loft.sh`, interval **15m**, referenced by a live HelmRelease, artifact re-fetched from the public internet ~10h *after* `cutoverComplete=true`. A 15-minute interval straddles a 10-minute window entirely.

Fixed in cutover chart **0.1.162** (PR #5652, merged, **published to ghcr and verified**): a pre-hold lint asserting every non-suspended `HelmRepository`/`GitRepository`/`OCIRepository` resolves to a Sovereign-local host, `allowHosts` empty by default so it fails closed. Its test drives the function extracted from the *render* in both directions and caught a fail-open bug in the first draft, where an uncreatable scratch file made the lint return PASS having examined nothing.

## What actually moves the number next

1. **Walk hw292.** It is live, cc=true, and G12-proven; every "delivered" row above becomes a green stamp only by walking it. This is the only thing that legitimately moves the durable number.
2. **Close the delivery chain (#5640)** — without it, every future fix repeats 5b: correct in source, invisible in production.

> **hw292 cannot be the env that closes it — measured 2026-08-08.** Its
> `bp-self-sovereign-cutover` HR has read `False — dependency 'flux-system/bp-gitea'
> is not ready` for **5d4h**, and it sits at cutover chart `0.1.159`. The Day-2
> reconciler that delivers images ships `enabled: true` only from **`0.1.170`**, and
> the refuse-by-default hardening at **`0.1.176`** — so the delivery mechanism is not
> present, and the chart carrying it would have to arrive through the same severed
> Gitea/Harbor path. The only in-cluster remedy is restoring the stripped `ghcr-pull`
> credential, i.e. **re-tethering — precisely what cutover exists to prevent.** This
> is not a repairable env; it is the catch-22 in its terminal form.
>
> **Therefore the 12 deploy-gated rows need one fresh prov, not twelve fixes.** Their
> code is merged. Pre-flight step 1 is done and the train is **verified ready**:
>
> | slot | version | carries |
> |---|---|---|
> | `bp-self-sovereign-cutover` | `0.1.176` | Day-2 reconciler + refuse-by-default |
> | `bp-catalyst-platform` | `1.4.1330` | — |
> | `catalyst-api` image | `c3f9ab0` (built 2026-08-08) | **#5645** — `merge-base --is-ancestor e3342f977 c3f9ab0` ✅ |
>
> That last row is the one that matters: the image a fresh prov would run **contains
> the OOM fix**, so the fan-out truncation behind #5894 and the 12 rows does not
> reproduce. Firing on this train is worth the walk; firing on anything older is not.
3. **Remediate #5591 on region-b** (one HR patch) so the NodePort ban is enforcing fleet-wide rather than in half of it.
4. Merge #5534 + #5535, now unblocked — the env is ready and the merge freeze is thawed.
