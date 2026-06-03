# UAT — PingCash iOS app (money transfer) — 2026-06-03

> **Standard User-Acceptance-Test walk.** *(This is a **filled sample** of [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) for a **native mobile app** — it shows how a UAT sub-agent walks an iOS app, including phone-only steps: OS permission dialogs, Face ID, camera/QR, push notifications, and offline. The cells below are illustrative.)*
>
> **Golden rule — this document is 100% the end-user's experience.** Every step is a real **tap / type / swipe / scan** on the shipped app. No terminal, no API, no code reading. A non-technical beta tester holding the phone must be able to follow every row verbatim.

---

## Metadata

| Field | Value |
|---|---|
| **Product / release** | PingCash iOS 1.4 |
| **Build under test** | TestFlight build 1.4 (142) — [open in TestFlight](https://testflight.apple.com/join/EXAMPLE142) |
| **Environment** | Devnet wallet backend (test funds) |
| **Surface(s)** | Native **iOS app** — walked on **iPhone 15 / iOS 18.2** |
| **Tester** | `@p0-474-lonely` (UAT executor sub-agent) |
| **Walk date** | 2026-06-03 |
| **Overall verdict** | ⚠️ CONDITIONAL *(1 P2 offline defect — see roll-up)* |

---

## How to read & fill this document

**Result legend:** ✅ PASS *(device screenshot required)* · ❌ FAIL *(file defect, leave issue open)* · ⛔ BLOCKED *(couldn't attempt)* · ⏭️ N/A · ☐ NOT WALKED.

Mobile rules: the walk **starts at install/launch** (no URL bar); name each screen the way a user would, with the dev `testID` in parens for engineers; OS permission dialogs, Face ID, camera, push, and offline are each their **own steps**; evidence is a **device screenshot** committed under [`evidence-mobile/`](evidence-mobile). The executor is read-only on the app and never closes the issue.

---

## Test journeys

### TC-01 — First-run onboarding (install → signed in)

- **Persona:** New user on their personal iPhone who just got the TestFlight invite.
- **Goal (user's words):** *"As a new user, I want to install the app and sign in with my phone number, so that I reach my wallet."*
- **Surface:** iOS app, build 142, iPhone 15 / iOS 18.2.
- **Preconditions:** App installed from [TestFlight](https://testflight.apple.com/join/EXAMPLE142); signed out; Notifications not yet granted; a test phone number that can receive SMS.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Home screen (springboard) | Tap the **PingCash** icon | Splash → app opens on the **Welcome** screen | ✅ | [📷 tc01-1-app-launch](evidence-mobile/tc01-1-app-launch.png) |
| 2 | Welcome | Tap **Get started** (`testID: btn-get-started`) | **Phone number** entry screen with a country picker + number field | ✅ | [📷 tc01-2-get-started](evidence-mobile/tc01-2-get-started.png) |
| 3 | Phone number | Type the test number, tap **Send code** (`testID: btn-send-code`) | iOS shows *"PingCash would like to send you Notifications"* — tap **Allow**; advances to the SMS-code screen | ✅ | [📷 tc01-3-notif-permission](evidence-mobile/tc01-3-notif-permission.png) |
| 4 | Enter code | Type the 6-digit SMS code (`testID: otp-input`) | Auto-submits on the 6th digit; app asks to enable **Face ID** | ✅ | [📷 tc01-4-sms-code](evidence-mobile/tc01-4-sms-code.png) |
| 5 | Face ID prompt | Tap **Enable**, complete the Face ID scan | System Face ID sheet succeeds | ✅ | [📷 tc01-5-faceid](evidence-mobile/tc01-5-faceid.png) |
| 6 | (after Face ID) | — | Lands on the **Home** tab, signed in, balance card visible | ✅ | [📷 tc01-6-home](evidence-mobile/tc01-6-home.png) |

- **Journey verdict:** ☑ **PASS** — Full install→launch→OTP→Face ID→Home worked on first try; notification permission honored.

---

### TC-02 — Send money by scanning a QR code

- **Persona:** The same user paying a friend who showed them a PingCash QR.
- **Goal (user's words):** *"As a user, I want to scan a payee's QR and send them money, confirmed with Face ID."*
- **Surface:** iOS app, signed in from TC-01.
- **Preconditions:** Logged in; test wallet funded; Camera permission not yet granted; a payee QR code on hand.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Home tab | Tap **Send** (`testID: tab-send`) | The **Send** screen with an amount keypad and a **Scan QR** button | ✅ | [📷 tc02-1-send](evidence-mobile/tc02-1-send.png) |
| 2 | Send | Tap **Scan QR** (`testID: btn-scan-qr`) | iOS shows *"PingCash would like to access the Camera"* — tap **Allow**; the live camera scanner opens | ✅ | [📷 tc02-2-scan-qr](evidence-mobile/tc02-2-scan-qr.png) |
| 3 | Scanner | Point at the payee QR; type **5.00** in the amount field; tap **Send** | A **Confirm with Face ID** sheet appears showing payee + 5.00 | ✅ | [📷 tc02-3-confirm-faceid](evidence-mobile/tc02-3-confirm-faceid.png) |
| 4 | Face ID confirm | Complete the Face ID scan | A **Success** receipt screen with amount, payee, and a reference number | ✅ | [📷 tc02-4-receipt](evidence-mobile/tc02-4-receipt.png) |

- **Journey verdict:** ☑ **PASS** — Camera permission, QR decode, amount entry, and Face ID-signed send all worked; receipt showed the correct reference.

---

### TC-03 — Receive a payment push and deep-link to the receipt

- **Persona:** The user gets paid back and taps the notification.
- **Goal (user's words):** *"As a user, when someone pays me I want a notification that opens the receipt when I tap it."*
- **Surface:** iOS app, backgrounded; lock screen.
- **Preconditions:** Logged in (TC-01); notifications granted (TC-01.3); app sent to background; an incoming test payment triggered to this account.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Lock screen (app backgrounded) | Wait for the incoming-payment push | A banner: *"You received OMR 5.00 from Aylin"* | ✅ | [📷 tc03-1-push-banner](evidence-mobile/tc03-1-push-banner.png) |
| 2 | Lock screen | Tap the banner | App foregrounds and **deep-links straight to the receipt** for that payment (not just Home) | ✅ | [📷 tc03-2-deeplink](evidence-mobile/tc03-2-deeplink.png) |

- **Journey verdict:** ☑ **PASS** — Push arrived within ~3s; tapping it deep-linked to the correct receipt.

---

### TC-04 — Stay usable offline (Airplane mode)

- **Persona:** The user opens the app on the metro with no signal.
- **Goal (user's words):** *"As a user with no connection, I want the app to tell me I'm offline and show my last balance, then refresh cleanly when I'm back online."*
- **Surface:** iOS app, signed in.
- **Preconditions:** Logged in; known balance from TC-02; ability to toggle Airplane mode.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Home tab | Turn on **Airplane mode**, pull-to-refresh | An **offline banner** (*"No connection — showing last known balance"*); the app does **not** crash or show a blank screen | ✅ | [📷 tc04-1-airplane](evidence-mobile/tc04-1-airplane.png) |
| 2 | Home tab (offline) | Turn **Airplane mode off**, wait for reconnect | Banner clears and the balance refreshes to the **live** value | ❌ | [📷 tc04-2-reconnect](evidence-mobile/tc04-2-reconnect.png) |

- **Journey verdict:** ☒ **FAIL** — Offline banner is correct (step 1 PASS), but on reconnect the balance **stayed stale** until the app was force-quit and reopened; the offline banner also lingered ~20s after signal returned. Filed **[#3002](https://github.com/ping-cash/ping-cash/issues/3002) (P2)**. Not a go-live blocker (force-quit recovers), but the auto-refresh-on-reconnect promise is broken.

---

## Roll-up

| TC | Surface | Journey | Steps | Walked | ✅ | ❌ | ⛔ | Verdict |
|---|---|---|---|---|---|---|---|---|
| TC-01 | iOS app | First-run onboarding | 6 | 6 | 6 | 0 | 0 | 🟢 PASS |
| TC-02 | iOS app | Send by QR + Face ID | 4 | 4 | 4 | 0 | 0 | 🟢 PASS |
| TC-03 | iOS app | Push → deep-link receipt | 2 | 2 | 2 | 0 | 0 | 🟢 PASS |
| TC-04 | iOS app | Offline resilience | 2 | 2 | 1 | 1 | 0 | 🔴 FAIL |
| | | **Total** | **14** | **14** | **13** | **1** | **0** | |

**Overall verdict:** ⚠️ **CONDITIONAL** — Core money-movement journeys (onboarding, QR send, push receipt) are go-live-ready. One P2 offline-refresh defect ([#3002](https://github.com/ping-cash/ping-cash/issues/3002)); no P0/P1.

---

## Defects found during this walk

| Defect | Step | What the user saw | Severity | Ticket |
|---|---|---|---|---|
| Balance doesn't auto-refresh after reconnect; offline banner lingers | TC-04.2 | Stale balance + leftover offline banner until force-quit | P2 | [#3002](https://github.com/ping-cash/ping-cash/issues/3002) |

---

## Out of scope (handled by the dev team, NOT walked here)

Unit/integration/contract tests, CI pipelines, API/RPC/log verification, signer-library internals, source reading. Capabilities with no on-screen surface have no UAT row.

---

_Filled from [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) v1. Every row is one real tap/scan a person could repeat on the device._
