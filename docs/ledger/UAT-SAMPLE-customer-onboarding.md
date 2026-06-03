# UAT — OpenOva Catalyst (customer onboarding) — 2026-06-03

> **Standard User-Acceptance-Test walk.** *(This is a **filled sample** of [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) — it shows how a UAT sub-agent executes and records. The cells below are illustrative.)*
>
> **Golden rule — this document is 100% the end-user's experience.** Every step is something a person does with their thumb or mouse on the shipped UI. No terminal, no `kubectl`, no API calls, no code reading. A non-technical beta tester must be able to follow every row verbatim.

---

## Metadata

| Field | Value |
|---|---|
| **Product / release** | OpenOva Catalyst — Sovereign `hw89` |
| **Build under test** | console image `bootstrap-ui@6e7c50f4` (catalog [#2982](https://github.com/openova-io/openova/issues/2982)-fixed) |
| **Environment** | `https://marketplace.hw89.omani.works` → `https://console.hw89.omani.works` |
| **Surface(s)** | Responsive web (walked on iPhone 15 Safari, 393×852) |
| **Tester** | `@p0-474-lonely` (UAT executor sub-agent) |
| **Walk date** | 2026-06-03 |
| **Overall verdict** | ⚠️ CONDITIONAL *(1 P2 defect, no go-live blocker — see roll-up)* |

---

## How to read & fill this document

**Result legend:** ✅ PASS *(evidence required)* · ❌ FAIL *(file defect, leave issue open)* · ⛔ BLOCKED *(couldn't attempt)* · ⏭️ N/A · ☐ NOT WALKED.

Rules: no ✅ without a screenshot; walk in order; executor is read-only on the product; report the screen you saw, not the screen you expected; the executor never closes the issue.

---

## Test journeys

### TC-01 — Redeem a voucher and create my account

- **Persona:** First-time customer who was emailed a voucher code, on their phone.
- **Goal (user's words):** *"As a new customer, I want to redeem the code I was sent and get into my dashboard, so that I can start using my plan."*
- **Surface:** Responsive web (mobile Safari).
- **Preconditions:** A valid, unredeemed voucher code (`OMANI-WELCOME-7H3K9P2QXR`) sent to `aylin@omani.homes`; no existing account on that email; phone with email access for the sign-in code.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | `marketplace.hw89.omani.works/redeem/?code=OMANI-WELCOME-7H3K9P2QXR` | Open the redeem link from the email | A voucher preview card: plan name + **OMR credit**, and a **Redeem** button | ✅ | [📷 tc01-1-redeem-preview](evidence/tc01-1-redeem-preview.png) |
| 2 | Redeem preview | Tap **Redeem** | Form advances to **Create account** with email pre-filled as `aylin@omani.homes` | ✅ | [📷 tc01-2-signup](evidence/tc01-2-signup.png) |
| 3 | Create account | Tap **Create account** | A "Check your email for a 6-digit code" screen with a PIN input | ✅ | [📷 tc01-3-pin-prompt](evidence/tc01-3-pin-prompt.png) |
| 4 | PIN input | Type the 6-digit code from the email | On the 6th digit it auto-submits and lands on **/dashboard** — no "could not provision user record" error | ✅ | [📷 tc01-4-dashboard](evidence/tc01-4-dashboard.png) |

- **Journey verdict:** ☑ **PASS** — Full redeem→signup→dashboard chain worked on mobile in one sitting; code arrived in ~8s.

---

### TC-02 — Find and launch my first app

- **Persona:** The same customer, now logged in, exploring what they can run.
- **Goal (user's words):** *"As a customer, I want to open an app from my catalog and have it just work without logging in again."*
- **Surface:** Responsive web (mobile Safari).
- **Preconditions:** Logged in from TC-01; at least one app (Grafana) installed in the Organization.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | `/dashboard` | Tap **Apps** in the bottom nav | A grid of installed app cards; **Grafana** is one of them | ✅ | [📷 tc02-1-apps-grid](evidence/tc02-1-apps-grid.png) |
| 2 | `/apps` | Tap the **Grafana** card | The app detail screen **/app/grafana** with a **Launch →** button | ✅ | [📷 tc02-2-app-detail](evidence/tc02-2-app-detail.png) |
| 3 | `/app/grafana` | Tap **Launch →** | A new tab opens Grafana **already signed in** — no Keycloak username/password form appears | ❌ | [📷 tc02-3-kc-login-shown](evidence/tc02-3-kc-login-shown.png) |

- **Journey verdict:** ☒ **FAIL** — On mobile Safari the launch opened the **Keycloak login form** instead of landing signed-in. Desktop works; mobile Safari blocks the silent-SSO `prompt=none` redirect (third-party-cookie / ITP). Filed **[#3001](https://github.com/openova-io/openova/issues/3001) (P2)**. Not a go-live blocker (user can log in once), but breaks the "just works" promise on iOS.

---

### TC-03 — Get help from the in-product Sandbox assistant

- **Persona:** The customer wants the AI assistant to answer a question about their own org.
- **Goal (user's words):** *"As a customer, I want to ask the built-in assistant about my apps and get a real answer."*
- **Surface:** Responsive web (mobile Safari).
- **Preconditions:** Logged in; TC-02 attempted.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | `/dashboard` | Tap **Sandbox** in the nav | A grid of assistant cards (claude-code, qwen-code, …) | ✅ | [📷 tc03-1-sandbox-grid](evidence/tc03-1-sandbox-grid.png) |
| 2 | `/sandbox` | Tap **claude-code** | A session opens at **/sandbox/`<id>`** with an interactive terminal pane | ⛔ | — |
| 3 | `/sandbox/<id>` | Ask *"list my organization's applications"* | The assistant returns your org's apps (not a permission error) | ⛔ | — |

- **Journey verdict:** ☒ **BLOCKED** — Step 2 never produced a session: tapping **claude-code** spun on a loading state for >60s on mobile, no terminal attached, no error toast. Could not reach step 3, so it is marked ⛔ (not ❌ — the feature may work; the session just never started in this walk). Captured loading state at [📷 tc03-2-spinner](evidence/tc03-2-spinner.png). Retry on desktop + re-walk; if it reproduces, open a defect.

---

## Roll-up

| TC | Journey | Steps | Walked | ✅ | ❌ | ⛔ | Verdict |
|---|---|---|---|---|---|---|---|
| TC-01 | Redeem voucher → account | 4 | 4 | 4 | 0 | 0 | 🟢 PASS |
| TC-02 | Find & launch first app | 3 | 3 | 2 | 1 | 0 | 🔴 FAIL |
| TC-03 | Sandbox assistant | 3 | 1 | 1 | 0 | 2 | ⛔ BLOCKED |
| | **Total** | **10** | **8** | **7** | **1** | **2** | |

**Overall verdict:** ⚠️ **CONDITIONAL** — Core onboarding (TC-01) is solid and go-live-ready. TC-02 has a P2 mobile-only SSO regression ([#3001](https://github.com/openova-io/openova/issues/3001)); TC-03 is blocked pending a re-walk. No P0/P1, so onboarding can ship while [#3001](https://github.com/openova-io/openova/issues/3001) and the Sandbox block are worked.

---

## Defects found during this walk

| Defect | Step | What the user saw | Severity | Ticket |
|---|---|---|---|---|
| Silent-SSO falls back to login form on mobile Safari | TC-02.3 | Keycloak username/password form instead of a signed-in Grafana | P2 | [#3001](https://github.com/openova-io/openova/issues/3001) |

---

## Out of scope (handled by the dev team, NOT walked here)

Unit/integration/contract tests, CI pipelines, API/`kubectl`/SQL/log verification, source reading. Capabilities with no on-screen surface have no UAT row.

---

_Filled from [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) v1. Every row is one thumb/mouse action a real customer could repeat._
