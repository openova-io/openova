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
