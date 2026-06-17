# UAT Walkthrough — #3376 FUNNEL: stranger-with-voucher → signed-in in their OWN Org console with the purchased app RUNNING

## Status — format: browser-walk (agreed standard), last revamped 2026-06-17 on hw158

> **This runbook was rewritten into the agreed UAT contract after the curl/kubectl violation.**
> The prior version asserted every step with `curl` / `kubectl` command output (`POST /api/billing/...`, `kubectl get organizations...`, `kubectl get hr vcluster...`) — that format is **BANNED**. It has been **replaced in full** by the 100%-browser walk below.
>
> **The contract (mandatory):** **100% browser — no curl, no kubectl, no git, no command output.** Every row = open a clickable URL → click/type → **see a rendered screen** → screenshot. A redirect that ends on a **login / PIN / token screen is a FAIL**; only a rendered, signed-in screen is **✅**. `GAP` = no UI surface exists for that step.
>
> **Format (mandated):** 4-column table — **Tested page · Description · Status · Evidence**.
> - **Tested page** = a **clickable link** to the live page on the current env (`hw158.omani.works`).
> - **Description** = the browser action + the rendered screen the stranger must SEE.
> - **Status** = `☐` (reset — pending this browser walk; nothing is banked from a prior env).
> - **Evidence** = a **screenshot link** under `docs/sessions/2026-06-17/evidence/`.

- **The acceptance (what this walk proves):** a stranger holding a voucher opens the marketplace, **redeems** it, walks the **6-step wizard** (plans → apps → add-ons → **BCP topology pick** → review → checkout), **signs in with an email code** (no password), checks out at **due-zero** (the voucher credit covers the plan), clicks **Launch**, and is **redirected into their OWN new Organization's console — signed in zero-click as the Org owner — with the purchased app RUNNING at its own URL**.
- **Env under test:** the current fresh Sovereign on pool-TLD `omani.works` / `omani.homes` — **`hw158.omani.works`**. Per the founder flush rule *"each new environment flushes all the evidence"*, **every row starts `☐`**; no verdict is carried from a prior env (hw150 is wiped/void).
- **Maps to:** [`../UAT.md`](../UAT.md) **Row 12** (funnel TC-01).
- **Index:** [`README.md`](README.md).

> **Issue #3376** — folds the provisioning-token cutover contract (was #3689), the Org-namespace RBAC create (was #3663), the sovereign-clean marketplace bundle (was #3691), the tofu-archive handover wire, voucher entropy, and the authenticated-redeem rate-limit. This walkthrough covers EVERY one of those as a browser step the stranger (or the operator) actually takes.

---

## Section A — Operator pre-walk: mint the voucher (BSS), entropy enforced

The operator does this once in the operator console before handing the voucher code to the stranger. All-browser.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158.omani.works/bss/vouchers](https://console.hw158.omani.works/bss/vouchers) | Open the operator console **BSS → Vouchers** page → see the **voucher issuance form** (code, credit OMR, plan tier, description). No login wall (owner is pre-seeded). | ☐ | `docs/sessions/2026-06-17/evidence/3376-a1-bss-vouchers-form.png` |
| [console.hw158.omani.works/bss/vouchers](https://console.hw158.omani.works/bss/vouchers) | In the form, **type a deliberately weak code `1234`** and click **Issue** → the form **rejects it inline**: *"voucher code must be at least 12 characters"* (entropy gate visible in the UI). | ☐ | `docs/sessions/2026-06-17/evidence/3376-a2-voucher-weak-code-rejected.png` |
| [console.hw158.omani.works/bss/vouchers](https://console.hw158.omani.works/bss/vouchers) | **Leave the code field empty** and click **Issue** → a new voucher row appears with a **server-auto-generated high-entropy code** (e.g. `VCH-…`); the row shows the credit (e.g. `5000 OMR`), plan tier, and `unredeemed 0/1`. Copy the code as `<CODE>`. | ☐ | `docs/sessions/2026-06-17/evidence/3376-a3-voucher-issued-autogen.png` |

---

## Section B — THE STRANGER WALK (the acceptance)

The stranger uses a **fresh incognito browser with no session**. Every step is a page they SEE.

### B.1 — Redeem the voucher on the sovereign-clean storefront

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [marketplace.hw158.omani.works/redeem](https://marketplace.hw158.omani.works/redeem/?code=%3CCODE%3E) | As the stranger, open the redeem page with `?code=<CODE>` → see **"Voucher valid · 5000 OMR"** with **THIS Sovereign's** brand chrome (no `openova.io`, no mothership logo). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b1-redeem-voucher-valid.png` |
| [marketplace.hw158.omani.works/redeem](https://marketplace.hw158.omani.works/redeem/?code=JUNK) | Open the redeem page with a **junk code** → see a generic **"voucher not valid"** message (no tombstone / no detail leak). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b2-redeem-junk-code-invalid.png` |
| [marketplace.hw158.omani.works/redeem](https://marketplace.hw158.omani.works/redeem/?code=%3CCODE%3E) | Open **DevTools → View page source / Elements** on the redeem page → confirm the HTML shows **THIS Sovereign's** brand and **no** `console.openova.io`, `omantel.openova.io`, or bare `openova.io` host literal (sovereign-clean storefront). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b3-redeem-html-sovereign-clean.png` |
| [marketplace.hw158.omani.works/redeem](https://marketplace.hw158.omani.works/redeem/?code=%3CCODE%3E) | Click **"Sign up to redeem"** → the browser lands on the **plan picker grid** (`/plans`). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b4-plans-grid.png` |

### B.2 — The 6-step wizard (plans → apps → add-ons → BCP topology → review)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [marketplace.hw158.omani.works/plans](https://marketplace.hw158.omani.works/plans) | On the **plans** grid → see the plan tiers (S / M / L / XL / Flexi) with price/CPU/memory. **Pick plan M** → advances to the app catalog (`/apps`). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b5-plan-picked.png` |
| [marketplace.hw158.omani.works/apps](https://marketplace.hw158.omani.works/apps) | On the **app catalog** grid (served from THIS Sovereign's catalog) → see the apps. **Pick WordPress** → advances to add-ons (`/addons`). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b6-app-wordpress-picked.png` |
| [marketplace.hw158.omani.works/addons](https://marketplace.hw158.omani.works/addons) | On the **add-ons** step → see the optional add-ons; leave defaults → click **Continue** → advances to the BCP topology step (`/bcp`). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b7-addons.png` |
| [marketplace.hw158.omani.works/bcp](https://marketplace.hw158.omani.works/bcp) | On the **BCP topology** step → see **both** **Single-region** and **Active-hot-standby** radio options (the Pillar-2 BCP choice at signup). **Select Active-hot-standby** → click **Continue** → advances to review (`/review`). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b8-bcp-active-hot-standby.png` |
| [marketplace.hw158.omani.works/review](https://marketplace.hw158.omani.works/review) | On the **review summary** → see the chosen **plan (M)**, **app (WordPress)**, **topology (Active-hot-standby)**, the **Org slug** (e.g. `walk-stranger-co`), and the pool-TLD (`omani.homes`). Click **Proceed to checkout** → advances to `/checkout`. | ☐ | `docs/sessions/2026-06-17/evidence/3376-b9-review-summary.png` |

### B.3 — Email-code sign-in + due-zero checkout

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [marketplace.hw158.omani.works/checkout](https://marketplace.hw158.omani.works/checkout) | On **checkout**, enter the stranger's email → click **Send code** → check the inbox → **type the emailed sign-in code** (no password) → the page shows the stranger **signed in** and the Organization confirmed. | ☐ | `docs/sessions/2026-06-17/evidence/3376-b10-email-code-signin.png` |
| [marketplace.hw158.omani.works/checkout](https://marketplace.hw158.omani.works/checkout) | On the checkout summary → the **voucher credit is applied**: order total shows **"Credit covers this order — 0 OMR due"** (5000 credit − 9 OMR plan M = due settled to zero; no Stripe card form). Click **Launch** / **Place order**. | ☐ | `docs/sessions/2026-06-17/evidence/3376-b11-checkout-due-zero.png` |
| [marketplace.hw158.omani.works/checkout](https://marketplace.hw158.omani.works/checkout) | Watch the **provisioning progress timeline** on the page → the steps **advance to Done** (Creating Organization → Committing manifests → Provisioning vCluster → Deploying WordPress → TLS → Health) — the timeline does **not** hang or show a red failed step. | ☐ | `docs/sessions/2026-06-17/evidence/3376-b12-provisioning-timeline-done.png` |

### B.4 — Launch redirect → land signed-in in the OWN Org console with the app RUNNING (the terminal)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.walk-stranger-co.omani.homes](https://console.walk-stranger-co.omani.homes/) | After **Launch**, the marketplace **redirects** the stranger to their **per-Org console** → the browser URL becomes `https://console.<slug>.omani.homes` and the page loads with **publicly-trusted TLS** (no cert error, no connection-refused). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b13-per-org-console-redirect.png` |
| [console.walk-stranger-co.omani.homes/dashboard](https://console.walk-stranger-co.omani.homes/dashboard) | Observe the console landing → the stranger is **signed in zero-click as the Org owner** (their email in the avatar, their Org name in the header) — **no login / PIN form** (the per-Org realm SSO contract). | ☐ | `docs/sessions/2026-06-17/evidence/3376-b14-per-org-console-signedin.png` |
| [console.walk-stranger-co.omani.homes/applications](https://console.walk-stranger-co.omani.homes/applications) | In the Org console, open the **Applications** view → see the purchased **WordPress** app card showing **Running / Healthy** → click **Open**. | ☐ | `docs/sessions/2026-06-17/evidence/3376-b15-app-card-running.png` |
| [wordpress.walk-stranger-co.omani.homes](https://wordpress.walk-stranger-co.omani.homes/) | The purchased **WordPress app SERVES at its own FQDN** → see the live, rendered WordPress site (not a 404 / 502 / cert error). **THIS is the terminal acceptance screenshot — the purchased app running inside the customer's own Org.** | ☐ | `docs/sessions/2026-06-17/evidence/3376-b16-wordpress-running-terminal.png` |

### B.5 — Returning customer is NOT bounced to the mothership

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [marketplace.hw158.omani.works](https://marketplace.hw158.omani.works/) | While still signed in, **re-open the marketplace root** in the same browser → the returning-user redirect sends the customer to **`https://console.<slug>.omani.homes`** (their own Org console), **never** to `console.openova.io` / the mothership. | ☐ | `docs/sessions/2026-06-17/evidence/3376-b17-returning-user-redirect-own-console.png` |

### B.6 — Authenticated-redeem rate-limit (voucher hardening)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [marketplace.hw158.omani.works/checkout](https://marketplace.hw158.omani.works/checkout) | As the signed-in customer, **rapidly re-submit the checkout/redeem several times** (refresh + re-click Place order >5× in a few seconds) → after ~5 attempts the page shows a **rate-limit notice** (*"please wait before retrying"*); a single legitimate redeem is unaffected. | ☐ | `docs/sessions/2026-06-17/evidence/3376-b18-rate-limit-notice.png` |

---

## Section C — GENERALITY PROOF (no per-slug / per-TLD special-casing)

Repeat the WHOLE stranger walk with a **different Org slug AND a different pool-TLD**, proving slug + parent-domain are data, not code. All-browser.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [marketplace.hw158.omani.works/redeem](https://marketplace.hw158.omani.works/redeem/?code=%3CCODE2%3E) | Mint a **second** voucher (Section A) and re-walk B.1→B.4 with a **different slug** (e.g. `walk-stranger-two`) and a **different pool-TLD** (`omani.rest`) → a **second Organization** provisions and the customer lands signed-in with ITS purchased app. | ☐ | `docs/sessions/2026-06-17/evidence/3376-c1-second-org-walk.png` |
| [console.walk-stranger-two.omani.rest/dashboard](https://console.walk-stranger-two.omani.rest/dashboard) | The **second** Org's console lands signed-in on a **different TLD** (`omani.rest`) — identical zero-click contract, no special-casing. | ☐ | `docs/sessions/2026-06-17/evidence/3376-c2-second-org-console-signedin.png` |
| [wordpress.walk-stranger-two.omani.rest](https://wordpress.walk-stranger-two.omani.rest/) | The **second** Org's purchased app serves at **its own** different-TLD FQDN → two different Orgs, two different TLDs, two running apps, **identical mechanism**. | ☐ | `docs/sessions/2026-06-17/evidence/3376-c3-second-org-app-running.png` |

---

## How to read this runbook

- Acceptance = an operator/founder walking the clickable rows above **in one fresh session on the current env** and reaching the **terminal screenshot** (Section B.4, last row: the purchased WordPress app serving inside the customer's own Org console).
- A row flips to **✅** only when its **Evidence** screenshot shows the **rendered, signed-in / serving** screen described.
- A row is **FAIL** if the browser ends on a **login / PIN / token screen**, a **404 / 502 / 503**, a **cert error**, or **connection-refused**.
- **NOTHING is banked**: a chart roll touching the funnel, the wizard, the org-provisioning loop, or the per-Org console flips every affected row back to `☐`. No ✅ may be carried from a past env (founder flush rule). Each ✅ must trace to a same-day screenshot under `docs/sessions/2026-06-17/evidence/` and be linked from [`../UAT.md`](../UAT.md).
