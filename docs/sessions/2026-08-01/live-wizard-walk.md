# Live Playwright walk — mothership sovereign console wizard (2026-08-01)

**Surface**: `https://console.openova.io/sovereign/` (mothership, READ-ONLY — no mutations performed)
**Method**: Playwright `browser_navigate` → `browser_snapshot` → `browser_take_screenshot` (full page)
**Session**: unauthenticated (no PIN mint; `whoami` returns 401 as expected)

## What actually happened

`GET /sovereign/` **redirects to `/sovereign/wizard`** and the SPA hydrates into a real 8-step
wizard — page title `OpenOva Corporate`, step 1 of 8 rendered, steps 2–8 disabled until step 1
completes. This corrects an earlier assumption in this session that the console served only a
static shell: the shell is 1063 bytes, but it hydrates into a working route.

## Finding 1 — `ORG_DEFAULTS` fabrication is LIVE (validates PR #5554)

Wizard step 1 ("Tell us about your organisation") renders with a **fabricated company already
typed into the identity fields**:

| Field | Live pre-filled value |
|---|---|
| Organisation name | `Acme Financial` |
| Headquarters | `Frankfurt, Germany` |

The page then actively invites the operator to keep them:

> "Fields marked default are pre-filled. Click to focus — all text is selected so you can type a
> replacement immediately."
>
> "All fields are pre-filled — **proceed without changing anything** or override what you need."

This is the defect fixed in **PR #5554** (`products/catalyst/bootstrap/ui/src/entities/deployment/model.ts`),
now confirmed on the production surface rather than inferred from source. The aggravating factor
already documented on that PR — `SmartField`'s `onBlur` handler in `StepOrg.tsx` restoring the
default when an operator clears the field — means clearing these values in the live UI is not an
escape from them.

Evidence: `evidence/uat-wizard-step1-acme-fabrication-2026-08-01.png` (full-page screenshot).

**Status**: fix authored and open as PR #5554; unmerged. `gh pr merge` is unavailable in this
session (permission classifier), so the fix cannot be landed from here.

## Finding 2 — `tenant/discover` 404s on the mothership host

```
[ERROR] 404 @ https://console.openova.io/sovereign/api/v1/tenant/discover?host=console.openova.io
```

The wizard calls tenant-discovery with its own hostname and receives 404. The wizard renders
anyway, so this is not user-blocking on this surface, but the call is unconditional and its
failure is swallowed silently — the operator sees no indication. Not yet ticketed.

## Finding 3 — `whoami` 401 (expected, recorded for completeness)

```
[ERROR] 401 @ https://console.openova.io/sovereign/api/v1/whoami
```

Correct behaviour for an unauthenticated visit; the header offers a `Sign in` button. Recorded so
the 401 in the console log is not later mistaken for a defect.

## Finding 4 — Pillar-2 topology step renders live (step 2 of 8)

Advanced step 1 → step 2 ("Choose your infrastructure topology"). This is the **Pillar 2** surface
per `docs/DOD.md:110` — "Wizard exposes region / topology choice during signup; customer picks N
regions". It renders with five topologies plus an optional add-on:

| Option | Label as rendered |
|---|---|
| Four-region | dedicated dual-CP (MGMT) + dual data plane |
| Two-region | DMZ · RTZ · MGMT per region (3 clusters each) |
| Two-region | DMZ cluster (top) + RTZ·MGMT cluster (bottom, 2 vClusters) — *selected by default, badged `ZONED` / Mid-market* |
| Two-region | DMZ · RTZ · MGMT as vClusters inside each cluster |
| Single region | DMZ · RTZ · MGMT as vClusters |
| Add-on | AIR-GAP (optional, applies to any topology) |

The selected topology renders a live diagram — `Region 1 · Primary` and `Region 2 · DR`, each
showing DMZ / RTZ / MGMT in network order top→bottom, with outer box = physical cluster and inner
box = vCluster — plus four rationale bullets including "MGMT present in both regions — eliminates
single-site management risk".

Evidence: `evidence/uat-wizard-step2-topology-choice-2026-08-01.png`.

## UAT ledger impact — none

**No row was stamped, including for Finding 4.** The reasoning matters, because the near-miss here
is real:

Row **82** (`funnel`, #3376) is the Pillar-2 BCP row and asserts the step "shows BOTH Single-region
and Active-hot-standby radios" — **two** options, on the customer-facing **marketplace funnel**.
The step walked above is the **sovereign deployment wizard** with **five** topologies and an
air-gap add-on. Different surface, different option set, different actor (sovereign-admin
provisioning a Sovereign vs. a customer signing up for an Organization). Stamping row 82 from this
walk would be env- and surface-mismatched evidence.

So there is **no Pillar-2 coverage gap** in the ledger — row 82 covers it on the funnel side. What
is genuinely uncovered is the *deployment wizard's* topology step, which has no dedicated row. That
is a ledger-coverage observation, not a defect, and is deliberately not being filed as a TBD (it
does not block the next pillar walk — see `docs/PRINCIPLES.md` L8).

The remaining wizard steps (3–8) were not advanced: step 8 submits and would fire a live
deployment, and firing is founder-gated.

No row was stamped. The ledger carries **no row asserting wizard step-1 pre-fill behaviour**, so
there is nothing here to mark ✅ or ❌ without inventing an assertion. UAT rows 1/2/5 (`console
bare URL → /dashboard signed-in`, `full sidebar renders`) are **Sovereign-scoped** — they assert
the sovereign-admin console of a provisioned Sovereign, not the mothership's deployment wizard —
so this walk does not satisfy them either.

The ~150 Sovereign-scoped rows remain gated on firing hw292.

---

## Finding 5 — the fabricated HQ DRIVES cloud provider + region selection (escalates #5401)

Continued the walk to step 3 of 8 ("Cloud provider per region"). It renders:

> ★ **Pre-selected based on HQ: Frankfurt, Germany**

`Frankfurt, Germany` is the fabricated `ORG_DEFAULTS.headquarters` value. It is not merely
displayed — it is **consumed to choose infrastructure**. Confirmed in source rather than inferred
from the banner text:

- `src/pages/wizard/steps/StepProvider.tsx:366` — `const hint = getHqHint(store.orgHeadquarters)`
- `:370` comment, verbatim — *"On first visit: apply HQ hint, or fall back to first provider +
  first region."*
- `:507-511` — the `★ Pre-selected based on HQ` banner renders **only when `hint` is truthy**, so
  its presence on screen is itself proof the fabricated value was consumed, not just echoed.

### The full consequence chain

```
ORG_DEFAULTS.headquarters = "Frankfurt, Germany"      (fabricated company)
  -> store.orgHeadquarters                             (seeded; restored on blur if cleared)
    -> getHqHint(...)                    StepProvider.tsx:366
      -> provider + cloud-region pre-selection          (first-visit default)
        -> the infrastructure actually provisioned
```

An operator who accepts the defaults — which step 1 explicitly invites ("proceed without changing
anything") — provisions infrastructure **sited on a fictional company's headquarters**.

This materially escalates #5401. The original finding was fabricated text in identity fields, with
the aggravating factor that `SmartField`'s `onBlur` restores the default when the field is cleared
(`StepOrg.tsx:43-85`). The live walk shows the fabrication is load-bearing: it reaches placement.

**The fix in PR #5554 is correspondingly load-bearing.** With `headquarters: ''`, `getHqHint`
returns falsy, the banner does not render, and the wizard falls back to first-provider /
first-region instead of a fabricated geography. Emptying the field is not cosmetic tidying — it
removes a fabricated input from the provisioning path.

Evidence: `evidence/uat-wizard-step3-hq-drives-region-2026-08-01.png`.

### Two incidental confirmations on the same step

- Storage-class field states *"local-path is not permitted"*, consistent with the #3971 CSI policy.
- Cost estimate renders live: **€0.136/hr · €99/mo** across 2 regions (€0.068/hr per region).

### UAT ledger impact — none

Still no row asserts wizard pre-fill or HQ-driven placement. Steps 4-8 were not advanced: step 8
submits and would fire a live deployment, which is founder-gated.

## Finding 6 — step 4 "Platform Components" is internally consistent and repo-backed

Advanced to step 4 of 8. Unlike findings 1 and 5, this step surfaced **no defect** — recording it
because a clean result verified against the repo is evidence too, and a walk that only ever reports
problems is not being run honestly.

Rendered state:

| Element | Value |
|---|---|
| Components tab | 40 shown · **21 selected** |
| Foundation tab | **24** (always-on platform set) |
| Header total | **Selected (45) of 64** |

**Arithmetic checks out in both directions**: 21 + 24 = 45 selected, and 40 + 24 = 64 total. The
two independently-rendered counters agree, which is the kind of cross-field consistency that the
treemap/showback defects (#5485) failed.

**Names are repo-backed, not fabricated.** Spot-checked 14 selected component ids against the
monorepo Blueprint layout — `alloy`, `axon`, `continuum`, `falco`, `grafana`, `loki`, `mimir`,
`opensearch`, `sigstore`, `stalwart`, `strimzi`, `tempo`, `trivy`, `valkey` — **14/14 resolve** to a
real `platform/<x>/` or `products/<x>/` directory. Every card also links to
`/sovereign/marketplace/product/<id>`, i.e. the ids are the marketplace join key, not display
strings.

The step also states its dependency behaviour explicitly ("Harbor pulls in cnpg, seaweedfs,
valkey") and separates opt-in components from the mandatory foundation set.

Note for context, not a contradiction: `trivy` and `syft-grype` appear as selectable/selected here.
That is consistent with the Day-2 scanner work (#3971 lineage) which removed scanners from the
**provisioning flow graph**, not from the installable catalogue.

Evidence: `evidence/uat-wizard-step4-components-2026-08-01.png`.

### Ledger impact — none

No row asserts the wizard's component-selection step. Steps 5-8 not advanced; step 8 submits and
would fire a live deployment.

## Finding 7 — step 5 already uses the CORRECT pattern that step 1 violates (settles #5401)

Advanced to step 5 of 8, "Marketplace mode" — the **Pillar 1** surface. It renders the toggle
(enabled by default), the `marketplace.<your-sovereign-fqdn>` signup story, per-Organization
isolated subdomains, and three optional storefront-branding fields.

The branding fields are the finding. They are **blank**, with grey hints:

| Field | Rendered as |
|---|---|
| Brand name | `placeholder: e.g. Acme Marketplace` — field empty |
| Tagline | `placeholder: e.g. Apps for the regulated cloud` — field empty |
| Primary colour | `placeholder: #38BDF8` — field empty |

and the copy states plainly: *"Optional. All three fields can be left blank — the storefront falls
back to platform defaults, and you can edit them later from the post-launch Settings page without
re-running the wizard."*

**This is the correct pattern, in the same wizard, one step after step 1 gets it wrong.** Confirmed
in source rather than inferred from rendering:

```
StepMarketplace.tsx:237   placeholder="e.g. Acme Marketplace"        <- HINT, field stays empty
StepMarketplace.tsx:251   placeholder="e.g. Apps for the regulated cloud"
StepOrg.tsx:139           <SmartField required label="Organisation name"
                            defaultValue={ORG_DEFAULTS.name} ...>    <- SEEDED VALUE
StepOrg.tsx:140           <SmartField label="Headquarters"
                            defaultValue={ORG_DEFAULTS.headquarters} ...>
```

`placeholder` is a hint the browser discards on submit. `defaultValue` seeds real state that flows
into `store.orgName` / `store.orgHeadquarters`, survives `SmartField`'s onBlur restore, and — per
Finding 5 — reaches `getHqHint()` and selects the cloud region.

### Why this settles the #5401 argument

The objection to PR #5554 would be "the defaults are convenience; removing them hurts UX". Step 5
refutes that from inside the same codebase: it delivers the identical convenience (an example the
operator can copy) using `placeholder`, while keeping the submitted value honest and explicitly
telling the operator blank is fine and editable later.

PR #5554 makes step 1 consistent with step 5. It is not removing a feature — it is aligning one
step with the pattern the wizard already considers correct.

Evidence: `evidence/uat-wizard-step5-marketplace-2026-08-01.png`.

### Ledger impact — none

No row asserts wizard branding-field behaviour. Steps 6-8 not advanced; step 8 submits and would
fire a live deployment.
