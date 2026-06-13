# #3376 FUNNEL — user acceptance walk (web UI)

**The story:** a stranger with a voucher ends **signed into their own org's console with the purchased app running** — entirely in the browser. Two actors: the **operator** (mints the voucher) and the **stranger** (the customer, fresh browser).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Organizations · Billing](https://console.hw133.omani.works/organizations/billing/vouchers) | Operator: **Vouchers** → strong code, 10 OMR, description → **Add/Update** → row shows **active, 0 used** | ☐ | |
| [Organizations · Billing](https://console.hw133.omani.works/organizations/billing/vouchers) | A too-short code is rejected ("must be ≥16 characters") | ☐ | |
| [marketplace · redeem](https://marketplace.hw133.omani.works/redeem?code=DEMO) | Stranger (fresh browser): a green **"Voucher valid — 10 OMR credit"** card renders | ☐ | |
| [marketplace · redeem](https://marketplace.hw133.omani.works/redeem?code=DEMO) | Click **Sign up to redeem** → lands on **Plans** | ☐ | |
| [marketplace · Plans](https://marketplace.hw133.omani.works/plans) | Select a plan → **Continue to Stack** | ☐ | |
| [marketplace · Apps](https://marketplace.hw133.omani.works/apps) | Add **WordPress** (toast "✓ added") → **Continue** | ☐ | |
| [marketplace · Add-ons](https://marketplace.hw133.omani.works/addons) | Subdomain **walmart** + **.omani.homes** → "✓ available" → **Continue** | ☐ | |
| [marketplace · BCP](https://marketplace.hw133.omani.works/bcp) | Choose a topology → **Review Order** | ☐ | |
| [marketplace · Review](https://marketplace.hw133.omani.works/review) | WordPress + plan + URL `walmart.omani.homes` listed → **Proceed to Checkout** | ☐ | |
| [marketplace · Checkout](https://marketplace.hw133.omani.works/checkout) | Type email → **Send sign-in code** → form switches to "code sent to …" | ☐ | |
| [marketplace · Checkout](https://marketplace.hw133.omani.works/checkout) | Enter the 6-digit code from the email → page flips to the signed-in launch panel | ☐ | |
| [marketplace · Checkout](https://marketplace.hw133.omani.works/checkout) | Tenant name set; subdomain **walmart.omani.homes**; voucher pre-filled; summary **"✓ Credit covers this order — Due now OMR 0"** | ☐ | |
| [marketplace · Checkout](https://marketplace.hw133.omani.works/checkout) | ❌ **GAP (hw133)** — voucher credit fails to load ("Couldn't load your voucher credit"); billing service is down → **Launch can't complete** | ☐ | |
| [marketplace · Checkout](https://marketplace.hw133.omani.works/checkout) | Click **Launch my tenant** → provisioning progress (workspace → apps → TLS) | ☐ | |
| [console.walmart.omani.homes](https://console.walmart.omani.homes/) | Browser is redirected here and renders **signed in as the owner** — no login | ☐ | |
| [console.walmart.omani.homes/apps](https://console.walmart.omani.homes/apps) | **WordPress** is listed as installed/running | ☐ | |
| [walmart.omani.homes](https://walmart.omani.homes/) | Click **Open** → the org's **WordPress site loads over HTTPS** — it actually serves | ☐ | |

**The DoD is met only when the last two rows are witnessed.** **Live blocker (hw133):** voucher/billing step fails (SME stack crash-looping); the back half is unproven.
