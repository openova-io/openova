# #3376 FUNNEL — user acceptance walk (web UI)

**The story:** a stranger with a voucher ends **signed into their own org's console with the purchased app running** — entirely in the browser. Two actors: the **operator** (mints the voucher) and the **stranger** (the customer, fresh browser).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| `console.<sov>/` → Organizations → Billing | Operator: **Vouchers** → strong code, 10 OMR, description → **Add/Update** → row shows **active, 0 used** | ☐ | |
| same | A too-short code is rejected ("must be ≥16 characters") | ☐ | |
| `marketplace.<sov>/redeem?code=<CODE>` | Stranger (fresh browser): a green **"Voucher valid — 10 OMR credit"** card renders | ☐ | |
| same | Click **Sign up to redeem** → lands on **Plans** | ☐ | |
| `marketplace.<sov>/plans` | Select a plan → **Continue to Stack** | ☐ | |
| `/apps` | Add **WordPress** (toast "✓ added") → **Continue** | ☐ | |
| `/addons` | Subdomain **walmart** + **.omani.homes** → "✓ available" → **Continue** | ☐ | |
| `/bcp` | Choose a topology → **Review Order** | ☐ | |
| `/review` | WordPress + plan + URL `walmart.omani.homes` listed → **Proceed to Checkout** | ☐ | |
| `/checkout` | Type email → **Send sign-in code** → form switches to "code sent to …" | ☐ | |
| `/checkout` | Enter the 6-digit code from the email → page flips to the signed-in launch panel | ☐ | |
| `/checkout` launch | Tenant name set; subdomain **walmart.omani.homes**; voucher pre-filled; summary **"✓ Credit covers this order — Due now OMR 0"** | ☐ | |
| `/checkout` launch | ❌ **GAP (hw133)** — voucher credit fails to load ("Couldn't load your voucher credit"); the billing service is down → **Launch can't complete** | ☐ | |
| `/checkout` launch | Click **Launch my tenant** → provisioning progress (workspace → apps → TLS) | ☐ | |
| `console.walmart.omani.homes/` | Browser is redirected here and renders **signed in as the owner** — no login | ☐ | |
| `console.walmart.omani.homes/apps` | **WordPress** is listed as installed/running | ☐ | |
| WordPress app URL | Click **Open** → the org's **WordPress site loads over HTTPS** — it actually serves | ☐ | |

**The DoD is met only when the last two rows are witnessed.** **Live blocker (hw133):** voucher/billing step fails (SME stack crash-looping); the back half is unproven.
