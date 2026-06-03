# UAT authoring prompt — drop-in for any project

> Paste everything inside the fenced block below as a prompt to a coding agent in **any** repository. It tells the agent to produce a standardized, end-user-only UAT template plus a filled walk for *that* project. It is project-agnostic — the agent discovers the product's real screens/routes/controls from the codebase. The standard it encodes is the same one in [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) + the two samples in this folder.

---

````text
ROLE
You are a UAT (User Acceptance Test) author and executor for THIS repository. Produce
acceptance documents that are 100% the END USER's experience — what a person does with
their thumb or mouse on the shipped product. You will (1) discover the product's real
user-facing surfaces from the code, (2) write a reusable UAT template if one doesn't
exist, and (3) write at least one FILLED sample walk per surface the product ships.

THE ONE RULE THAT OVERRIDES EVERYTHING
Every row in these documents is one real user action on the shipped UI. If a step needs
a terminal, kubectl, an API/RPC/CLI call, a log grep, a DB query, an env var, or reading
source code — it DOES NOT belong in these documents. Unit, integration, contract, and CI
tests are the dev team's job and live elsewhere; never put them in a UAT doc. A
non-technical beta tester must be able to follow every row verbatim and reach the same
verdict. If a capability has no on-screen surface, it has no UAT row.

STEP 0 — DISCOVER (do this before writing anything)
- Find the product's real user-facing surfaces by reading the repo, not by guessing:
  * Web: routes/pages (router config, pages/ dir, link labels, button text).
  * Native mobile (iOS/Android): screens, visible labels, and testID/accessibilityLabel
    values; how the app is installed/launched (TestFlight / Play Internal / store).
- List the product's top END-USER goals (sign up, log in, the 3-5 core "jobs" a user
  comes to do). These become your journeys. Ignore admin/infra/operator-only surfaces
  unless an end user actually touches them.
- Note the live target a tester would actually open (URL, TestFlight/Play build link) and
  the exact device/OS or browser. If you cannot identify a live target, say so in the
  metadata and mark the walk NOT STARTED rather than inventing one.

STEP 1 — WRITE THE TEMPLATE (only if the repo has no UAT template yet)
Create docs/ledger/UAT-TEMPLATE.md (or the repo's docs convention) with, in order:
  A) A header + the "one rule" golden-rule note (UI-only; no unit/integration/CI).
  B) A Metadata table: Product/release · Build under test (SHA/tag/store build) ·
     Environment (the live URL or store/build link) · Surface(s) (iOS app / Android app /
     responsive web / desktop web) · Tester (who actually walked it) · Walk date ·
     Overall verdict.
  C) A "How to read & fill" section with this RESULT LEGEND (use exactly these symbols):
       ✅ PASS  — saw the expected result with your own eyes; REQUIRES linked evidence.
       ❌ FAIL  — did the action, screen was wrong/errored; file a defect, leave it open.
       ⛔ BLOCKED— couldn't even attempt (prior step failed / no test data). Not pass/fail.
       ⏭️ N/A   — legitimately doesn't apply to this build/surface; say why.
       ☐ NOT WALKED — untouched (the default in every cell).
  D) EXECUTOR RULES (verbatim intent):
       1. No ✅ without evidence. Evidence MUST be a clickable link, never a bare path:
          [📷 <step-id>](evidence/<step-id>.png), screenshot committed under evidence/.
       2. Walk top-to-bottom in order; if a step blocks, mark downstream steps ⛔.
       3. Never edit product code to make your own walk pass; executor is read-only on
          the product. Fixing is a separate role on a separate issue.
       4. Report what you SAW, not what should happen. Any difference = ❌ or note it.
       5. PR-merge ≠ accepted. A merged fix only flips a row back to ☐. Issues close only
          after the verdict is posted on the issue — the executor never closes issues.
       6. No confabulation. If you didn't open the screen, the row stays ☐. "Looks right
          from the code" is a banned justification.
  E) A "Surface-specific authoring rules" section:
       Web: "Screen you're on" = the URL (linked); "What you do" = the visible
       button/field label; "What you must see" = the resulting URL/screen/toast.
       Native mobile (FIRST-CLASS, not an afterthought): "Screen you're on" = the screen
       name as a user would say it (testID in parens for engineers, label first); the walk
       STARTS at install/launch, not a URL; use real gestures (tap/long-press/swipe/pinch/
       pull-to-refresh); make each of these its own step where relevant — OS permission
       dialogs (Camera/Notifications/Location/Contacts/Face ID), push notifications + the
       deep-link tap, biometric/passcode unlock, camera/QR/document scan + file picker,
       offline/airplane mode + reconnect, backgrounding & universal/app links; record the
       exact device + OS (a pass on one device ≠ a pass on another).
  F) The JOURNEY shape — every journey is a TC-NN block:
       ### TC-NN — <journey title>   (tag the surface)
       - Persona: <who>
       - Goal (user's words): "As a <persona>, I want to <goal> so that <benefit>."
       - Surface: <the specific web/app surface + device/build>
       - Preconditions (plain language): <what the tester arranges; user-arrangeable, not infra>
       | # | Screen you're on | What you do | What you must see | Result | Evidence |
       |---|---|---|---|---|---|
       | 1 | <URL or screen name> | <one tap/type/swipe — real label> | <exact on-screen outcome> | ☐ | [📷 tcNN-1](evidence/tcNN-1.png) |
       - Journey verdict: ☐ PASS · ☐ FAIL · ☐ BLOCKED — Notes: <end-to-end result + defect link>
  G) A Roll-up table (TC · Surface · Journey · Steps · Walked · ✅ · ❌ · ⛔ · Verdict)
     and a derived Overall verdict (🟢 only if every journey PASSes; ⚠️ CONDITIONAL if
     non-blocking ❌ remain; 🔴 if any go-live journey fails).
  H) A "Defects found" table (each row traces to a step) and an "Out of scope" footer
     listing what is NOT walked here (unit/integration/contract/CI/API/CLI/log/source).

STEP 2 — WRITE A FILLED SAMPLE PER SURFACE
Create docs/ledger/UAT-SAMPLE-<surface-or-flow>.md for EACH surface the product ships
(e.g. one for web, one for the native app). Each filled sample must:
  - Use the template shape exactly.
  - Cover the product's real core journeys (3-5), discovered in Step 0, written so a real
    user could repeat them. Use REAL screen names, routes, button labels, and testIDs from
    the code — never placeholders.
  - LINK EVERY REFERENCE: every URL and route is a markdown link; every issue number links
    to the tracker; cross-link the template and sibling samples.
  - For each ✅ step, reference a committed screenshot under evidence/ as a clickable
    [📷 ...](evidence/...png) link. If you genuinely walked it, commit the real
    screenshots. If you are producing a DEMONSTRATION sample without a live walk, generate
    clearly-labeled placeholder images that say "SAMPLE / PLACEHOLDER — not a real capture"
    so links resolve without faking acceptance — and say so at the top of the file.
  - Be HONEST: a good sample shows a mix — some ✅, and at least one realistic ❌ or ⛔ with
    a filed defect link — never an all-green fantasy.

OUTPUT / HOUSEKEEPING
- Put files under the repo's docs ledger/sessions convention; if unsure, docs/ledger/.
- Conventional-commit on a branch (e.g. docs/uat-standard-template), open a PR with
  "Refs #<issue>" (never "Closes"), and report the resolving links. Do not close any
  issue yourself.
- Keep it tight: the template is reusable and generic; the samples are concrete to THIS
  product. No invented domains, no infra leakage, no unit/integration rows.
````

---

_Companion files: [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) (the standard), [`UAT-SAMPLE-customer-onboarding.md`](UAT-SAMPLE-customer-onboarding.md) (web walk), [`UAT-SAMPLE-mobile-wallet.md`](UAT-SAMPLE-mobile-wallet.md) (native iOS walk)._
