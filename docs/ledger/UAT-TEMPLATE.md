# UAT — `<PRODUCT>` — `<YYYY-MM-DD>`

> **Standard User-Acceptance-Test template.** Copy this file to `docs/ledger/UAT-<date>.md` (or a per-release name), fill the metadata block, and walk every step. One file = one walk = one verdict.
>
> **Golden rule — this document is 100% the end-user's experience.** Every step is something a person does with their **thumb or mouse** on the shipped **mobile app or web UI**. If a step needs a terminal, `kubectl`, an API call, a log grep, a DB query, or reading source code — **it does not belong in this file.** Unit, integration, contract, and CI checks are the dev team's job and live elsewhere (see *Out of scope*). A non-technical beta tester must be able to follow every row verbatim and reach the same verdict.

---

## Metadata (fill before walking)

| Field | Value |
|---|---|
| **Product / release** | `<e.g. PingCash 1.4 / OpenOva Catalyst hw89>` |
| **Build under test** | `<git SHA, image tag, or store build number — what a user actually runs>` |
| **Environment** | `<the live URL or store/TestFlight link the tester opens>` |
| **Surface(s)** | `<iOS app · Android app · responsive web · desktop web>` |
| **Tester** | `<agent @-handle or human name — who actually walked it>` |
| **Walk date** | `<YYYY-MM-DD>` |
| **Overall verdict** | ⬜ NOT STARTED · 🟡 IN PROGRESS · 🟢 PASS · 🔴 FAIL · ⚠️ CONDITIONAL *(fill the roll-up below first)* |

---

## How to read & fill this document

**Result legend** (put exactly one in every step's Result cell):

| Symbol | Meaning | Rule |
|---|---|---|
| ✅ | **PASS** | You saw the *Expected result* with your own eyes. **Requires evidence** (screenshot) in the same row. |
| ❌ | **FAIL** | You did the action and the screen was *wrong* or *errored*. File a defect, link it, leave the issue **open**. |
| ⛔ | **BLOCKED** | You could not even attempt the step (prior step failed, login down, no test data). **Not** a pass and **not** a fail — note what blocked you. |
| ⏭️ | **N/A** | Step legitimately doesn't apply to this build/surface. Say why in notes. |
| ☐ | **NOT WALKED** | Untouched. The starting state of every cell. |

**Rules for the agent (or human) executing this walk:**
1. **No ✅ without evidence.** A pass cell with an empty Evidence cell is invalid — treat it as ☐. Drop the screenshot path/link in the Evidence column.
2. **Walk top-to-bottom, in order.** Journeys assume the prior step happened. If a step blocks, mark downstream steps ⛔, don't skip-and-pass.
3. **Never edit product code to make your own walk pass.** The executor is read-only on the product. Fix-authoring is a separate role on a separate issue.
4. **Report what you saw, not what should happen.** If the screen differs from *Expected result* even slightly, it's ❌ or ⚠️ — describe the actual screen in notes.
5. **PR-merge ≠ accepted.** A merged fix only flips a row back to ☐ NOT WALKED. Acceptance is *this walk*. Issues close only after the verdict lands as a comment on the issue — **the executor never closes the issue**.
6. **No confabulation.** If you didn't open the screen, the row stays ☐. "Looks right from the code" is a banned justification.

---

## Test journeys

> Each journey is **one real thing a user is trying to accomplish**, written as a story they could narrate. Add as many `TC-NN` blocks as you have journeys. Delete the example fields you don't need, but keep the shape.

### TC-01 — `<Journey title — what the user is trying to do>`

- **Persona:** `<who — e.g. "First-time customer on their phone", "Returning admin on desktop">`
- **Goal (user's words):** *"As a `<persona>`, I want to `<goal>` so that `<benefit>`."*
- **Surface:** `<mobile app / web — the specific one>`
- **Preconditions (in plain language):** `<what the user needs before starting — e.g. "a valid invite code", "a funded test wallet", "no existing account on this email". State it as setup a tester can actually arrange, not infra.>`

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | `<URL or screen name the user lands on>` | `<the single tap/type/swipe — name the real button/field label>` | `<the exact on-screen outcome — a screen, a toast, a value>` | ☐ | |
| 2 | `<...>` | `<...>` | `<...>` | ☐ | |
| 3 | `<...>` | `<...>` | `<...>` | ☐ | |

- **Journey verdict:** ☐ PASS · ☐ FAIL · ☐ BLOCKED — **Notes:** `<what actually happened end-to-end; defect link if not PASS>`

---

### TC-02 — `<next journey>`

- **Persona:** `<...>`
- **Goal (user's words):** *"As a `<...>`, I want to `<...>`."*
- **Surface:** `<...>`
- **Preconditions:** `<...>`

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | `<...>` | `<...>` | `<...>` | ☐ | |
| 2 | `<...>` | `<...>` | `<...>` | ☐ | |

- **Journey verdict:** ☐ PASS · ☐ FAIL · ☐ BLOCKED — **Notes:** `<...>`

---

## Roll-up (fill as you finish each journey)

| TC | Journey | Steps | Walked | ✅ | ❌ | ⛔ | Verdict |
|---|---|---|---|---|---|---|---|
| TC-01 | `<title>` | | | | | | ☐ |
| TC-02 | `<title>` | | | | | | ☐ |
| | **Total** | | | | | | |

**Overall verdict:** `<🟢 PASS only if every journey is PASS · ⚠️ CONDITIONAL if non-blocking ❌ remain · 🔴 FAIL if any go-live journey fails>`

---

## Defects found during this walk

> Only bugs **a user would hit on screen**. Each must trace to a step above.

| Defect | Step | What the user saw | Severity | Ticket |
|---|---|---|---|---|
| `<one line>` | TC-01.3 | `<the wrong screen / error>` | P0 / P1 / P2 | `#<n>` |

---

## Out of scope (handled by the dev team, NOT walked here)

These are **not** acceptance steps and must never appear as rows above. Listed once, for transparency only:

- Unit tests, integration tests, contract tests, CI pipelines.
- API/CLI/`kubectl`/SQL/log-grep verification with no on-screen surface.
- Source-code reading, file:line citations, internal endpoint calls.

If a capability has *no* user-facing surface, it has *no* UAT row — it is verified by the dev team's automated suite, separately, and is none of this document's business.

---

_Template v1 — `docs/ledger/UAT-TEMPLATE.md`. Every row is one thumb/mouse action a real user could repeat. Copy → fill metadata → walk → record verdict on the issue._
