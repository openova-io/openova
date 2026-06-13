# #3376 FUNNEL — user acceptance walk (100% web UI)

**The story:** a stranger with a voucher ends up **signed into their own org's console with the purchased app running** — entirely in the browser, no terminal. Two actors: the **operator** (mints the voucher in the admin console) and the **stranger** (the customer, in a fresh browser).

_Each line = one browser step. `☐`/`✅`/`❌`._

### Operator mints a voucher
- [ ] Operator signs in at `https://console.<sov>/` → **Organizations → Billing** → **Vouchers** section.
- [ ] Type a strong code, **10 OMR** credit, a description → **Add / Update** → the voucher appears in the table as **active, 0 used**.
- [ ] (Try a too-short code → it is rejected with a "must be ≥16 characters" message.)

### Stranger opens the voucher link
- [ ] In a **fresh browser**, open `https://marketplace.<sov>/redeem?code=<CODE>` → a green **"Voucher valid — 10 OMR credit"** card renders.
- [ ] Click **Sign up to redeem** → lands on the **Plans** page.

### Stranger builds their tenant (the 6-step wizard)
- [ ] **Plans** → click **Select** on a plan → **Continue to Stack**.
- [ ] **Apps** → add **WordPress** (toast "✓ WordPress added") → **Continue**.
- [ ] **Add-ons** → type subdomain **walmart** + choose **.omani.homes** → it shows "✓ walmart.omani.homes is available" → **Continue**.
- [ ] **Business continuity** → choose a topology → **Review Order**.
- [ ] **Review** → the WordPress app + plan + URL `walmart.omani.homes` are listed → **Proceed to Checkout**.

### Stranger signs in by email code
- [ ] **Checkout** → type the customer's email → **Send sign-in code** → the form switches to "A 6-digit code was sent to …".
- [ ] Open the email, copy the code, type it → the page flips to the signed-in launch panel (the customer's email shown).

### Voucher covers the cost, then launch
- [ ] In the launch panel → type a tenant name; the subdomain shows **walmart.omani.homes**; the voucher is pre-filled.
- [ ] The order summary shows **"✓ Credit covers this order — Due now OMR 0.000"** and the button reads **Launch my tenant**.
- [ ] ❌ **GAP (live, hw133)** — the voucher credit fails to load ("Couldn't load your voucher credit") because the billing service is down; **Launch** cannot complete. Root cause behind the scenes: the SME services can't reach their message bus. This blocks everything below until fixed.
- [ ] Click **Launch my tenant** → it shows provisioning progress (creating workspace → installing apps → issuing TLS).

### Lands in the OWN org console, signed in
- [ ] The browser is redirected to **`https://console.walmart.omani.homes/`** and renders **signed in as the owner** — no login screen.

### The purchased app is RUNNING (the finish line)
- [ ] In the org console → **Apps** → **WordPress** is listed as installed/running.
- [ ] Click **Open** → a new tab loads the org's **WordPress site over valid HTTPS** — it actually serves.

**The DoD is met only when the last two steps are witnessed.** **Live blocker (hw133):** the voucher/billing step fails because the SME stack is crash-looping; the back half (provision → app running) is unproven.
