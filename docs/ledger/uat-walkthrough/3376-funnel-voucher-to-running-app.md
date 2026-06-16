# UAT Walkthrough — #3376 FUNNEL: stranger-with-voucher → signed-in in their OWN org console with the purchased app RUNNING

> **Format**: every row is ONE concrete human action. `Go to <URL>` / `do <click/type>` / `you should see <screen>` / `☐`. Routes + labels are cited from real UI/code (file:line in the ticket). Automated probes (`kubectl` / `curl` / `dig`) are the appendix, labelled NOT-acceptance. **Acceptance = the founder walking the clickable rows below and reaching the terminal screenshot (the purchased app serving inside the customer's own Org console).**
>
> **Env under test**: a FRESH **cut-over** Sovereign (cutoverComplete=true), pool-TLD `omantel.biz` (or `omani.homes`), reached zero-touch. Reference env for the live-failure column: **hw150** (`console.hw150.omantel.biz`, `marketplace.hw150.omantel.biz`), 2026-06-16.
>
> This walkthrough covers EVERY scenario folded into #3376: the provisioning-token cutover contract (was #3689), the Org-namespace RBAC create (was #3663), the sovereign-clean marketplace bundle (was #3691), the tofu-archive handover wire, voucher entropy, and the authenticated-redeem rate-limit.

---

## Section A — Pre-walk: prove the env is cut-over and the three blockers are CLEARED (operator, one-time)

These are gate checks the operator runs once on the fresh prov BEFORE the customer walk. They are pass/fail, not the acceptance walk itself.

| # | Go to / run | You should see | ☐ |
|---|---|---|---|
| A1 | Operator console → **Sovereignty** card (`https://console.<fqdn>/sovereignty`) | `cutoverComplete: true`, all 11 steps green, the 600s deny-egress hold PASSED | ☐ |
| A2 | `kubectl -n sme get pods \| grep provisioning` | BOTH `provisioning-*` replicas `1/1 Running` — **no `Init:0/1`** (blocker-a cleared: the init gate is satisfied) | ☐ |
| A3 | `kubectl -n sme get secret provisioning-github-token -o jsonpath='{.metadata.annotations.catalyst\.openova\.io/token-source}'` | `self-sovereign-cutover-step-09` IS present (the mint-job now stamps it even on the no-op path) — OR, if the data-probe fix was taken, A2 already proves it | ☐ |
| A4 | `kubectl auth can-i create namespaces --as=system:serviceaccount:catalyst-system:catalyst-api-cutover-driver` | `yes` (blocker-b cleared: the cutover-driver ClusterRole now grants `namespaces: [create]`) | ☐ |
| A5 | `kubectl -n flux-system get kustomization sme-tenants` | `READY: True` (the customer-onboarding Kustomization reconciles; the cutover proof no longer excludes it as "baseline noise") | ☐ |
| A6 | `curl -sk 'https://marketplace.<fqdn>/redeem/?code=X' \| grep -oE 'openova\.io'` | **empty output** (blocker-c cleared: the served customer bundle has zero mothership-domain literals) | ☐ |

---

## Section B — THE CUSTOMER WALK (the acceptance): stranger → voucher → running app in own Org

### B.1 — Operator mints a voucher (BSS) with entropy enforced

| # | Go to / do | You should see | ☐ |
|---|---|---|---|
| B1 | Go to operator console **BSS → Vouchers** (`https://console.<fqdn>/bss/vouchers` — voucher/billing lives in the operator console BSS menu, NOT a dead `admin.<fqdn>` subdomain) | The voucher issuance form | ☐ |
| B2 | In the voucher form, **type a deliberately weak code** `1234` and submit | Form **rejects** the weak code with a validation message (server-side minimum length+charset on generated/accepted codes) — entropy gate proven | ☐ |
| B3 | Submit again leaving the code field empty so the server **auto-generates** the code | A new voucher row appears with a high-entropy code meeting the minimum (length+charset); copy it as `<CODE>` | ☐ |
| B4 | Note the voucher value (e.g. `10 OMR`), plan tier, and that it is `unredeemed 0/1` | Voucher reflected in the BSS list | ☐ |

### B.2 — Stranger opens the marketplace and redeems (sovereign-clean storefront)

| # | Go to / do | You should see | ☐ |
|---|---|---|---|
| B5 | As the stranger (fresh browser, no session), go to `https://marketplace.<fqdn>/redeem/?code=<CODE>` | Redeem page: "Voucher valid · N OMR". Brand chrome is **THIS Sovereign's** (no `Omantel Cloud` mothership-partner literal unless this IS that partner Sovereign) | ☐ |
| B6 | **View page source / open DevTools → Network → the document HTML** | The HTML contains **no** `console.openova.io`, **no** `omantel.openova.io`, **no** bare `openova.io` (matches A6) | ☐ |
| B7 | Click **"Sign up to redeem"** → lands on `/plans` | Plan picker grid | ☐ |
| B8 | Pick a plan → `/apps` | Catalog app grid (served from THIS Sovereign's catalog) | ☐ |
| B9 | Pick the purchased app (e.g. **WordPress**) → `/addons` → `/bcp` | **BCP topology** step renders both **Single-region** and **Active-hot-standby** options (Pillar 2 choice present) | ☐ |
| B10 | Choose a topology → `/review` | Review summary: plan, app, topology, org slug `<slug>` (e.g. `walkmart`), pool-TLD | ☐ |
| B11 | `/checkout` → PIN-login with the Stalwart PIN, confirm the Organization | Checkout submits; the **provisioning progress timeline advances** (does NOT hang) | ☐ |

### B.3 — The Organization provisions zero-touch (the blocker-a + blocker-b chain now RUNS)

| # | Go to / do | You should see | ☐ |
|---|---|---|---|
| B12 | Watch the `/checkout` progress timeline | Steps advance to **Done** — the `tenant.created` event is consumed by the live provisioning pod (no longer Init:0/1), which POSTs the Organization CR; the cutover-driver creates the Org namespace (no 403) | ☐ |
| B13 | (operator window) `kubectl get organizations.orgs.openova.io -A` | **≥ 1 Organization** with your `<slug>` appears within ~60s of checkout (was `No resources found` on hw150) | ☐ |
| B14 | (operator window) `kubectl get ns \| grep <slug>` and `kubectl -n catalyst-system logs deploy/organization-controller \| tail` | The Org namespace exists; organization-controller logs show it fanned out the per-Org **vCluster + Keycloak group/realm + Gitea org + base RBAC** | ☐ |

### B.4 — The customer lands signed-in in their OWN console with the app RUNNING (the terminal)

| # | Go to / do | You should see | ☐ |
|---|---|---|---|
| B15 | After checkout the marketplace **redirects** the customer to their per-Org console | Browser URL becomes `https://console.<slug>.<pool-tld>` (per-Org, NOT `console.<fqdn>` operator, NOT `console.openova.io/nova` mothership) with publicly-trusted TLS | ☐ |
| B16 | Observe the console landing | Customer is **signed in zero-click as the Org owner** (per-Org realm, the SSO contract) — no login form, no token paste | ☐ |
| B17 | In the Org console, open the purchased app's card and click **Open** | The purchased app (e.g. WordPress) **serves** at its own FQDN inside the Org — **TERMINAL SCREENSHOT = the running app** | ☐ |

### B.5 — Returning customer is NOT bounced to the mothership (blocker-c redirect)

| # | Go to / do | You should see | ☐ |
|---|---|---|---|
| B18 | While still signed in (workspace exists), re-open `https://marketplace.<fqdn>` in the same browser | The returning-user redirect sends the customer to **`https://console.<slug>.<pool-tld>`** (their OWN Sovereign console) | ☐ |
| B19 | Inspect DevTools → Network for the redirect `Location` | The redirect target is the THIS-Sovereign console host, **never** `console.openova.io/nova` (was the hw150 failure: host not in `TENANTS`, `skipConsoleRedirect` unset → bounced to mothership) | ☐ |

### B.6 — Authenticated-redeem rate-limit (voucher hardening)

| # | Go to / do | You should see | ☐ |
|---|---|---|---|
| B20 | As the signed-in customer, **burst** the authenticated redeem/checkout endpoint > N times rapidly (DevTools console or repeated submits) | After N attempts the endpoint returns **HTTP 429** (per-customer rate-limit bucket); a single legitimate redeem is unaffected | ☐ |

---

## Section C — GENERALITY PROOF (no per-slug / per-TLD special-casing)

Repeat the WHOLE customer walk with a **different slug AND a different pool-TLD**, proving slug + parent-domain are data, not code.

| # | Go to / do | You should see | ☐ |
|---|---|---|---|
| C1 | Mint a second voucher; run B5→B17 with slug `<slug2>` (e.g. `acme`) and pool-TLD `omani.rest` (different from B's TLD) | A **second** Organization CR provisions; the customer lands on `https://console.<slug2>.omani.rest` signed-in with ITS purchased app running | ☐ |
| C2 | `git grep` the marketplace bundle + the chart for either slug/TLD literal | **No** `<slug>`/`<slug2>`/`omantel.biz`/`omani.rest` host literal was added — both walks succeeded on data alone | ☐ |
| C3 | Compare B17 and C1 terminal screenshots | Two different Orgs, two different TLDs, two running apps — identical mechanism, zero host-specific code | ☐ |

---

## Appendix — automated probes (NOT acceptance; evidence only)

These ran during the build/verify; they are diagnostic, not the founder walk.

```bash
# blocker-a — provisioning pods Running, token-source annotation present
kubectl -n sme get pods | grep provisioning            # both 1/1 Running, no Init:0/1
kubectl -n sme get secret provisioning-github-token \
  -o jsonpath='{.metadata.annotations.catalyst\.openova\.io/token-source}'   # self-sovereign-cutover-step-09
kubectl -n catalyst logs job/cutover-gitea-token-mint-* | tail # stamps annotation even on no-op path

# blocker-b — Org-namespace create RBAC
kubectl auth can-i create namespaces \
  --as=system:serviceaccount:catalyst-system:catalyst-api-cutover-driver   # yes
kubectl get organizations.orgs.openova.io -A           # >= 1 after a signup walk

# blocker-c — sovereign-clean served marketplace bundle
curl -sk 'https://marketplace.<fqdn>/redeem/?code=X' | grep -oE 'openova\.io' # empty

# cutover proof no longer excludes the customer Kustomization
kubectl -n flux-system get kustomization sme-tenants    # READY True
kubectl -n catalyst logs job/cutover-egress-block-test-* | grep -i sme-tenants # no baseline exclusion

# handover archive wire (the funnel terminal precondition)
kubectl -n catalyst get secret tofu-phase0-archive      # EXISTS on the child after handover re-mint
```

**Live-failure baseline captured on hw150 (2026-06-16), all to be GREEN on the fixed fresh prov:**
- `provisioning-*` both `Init:0/1` CrashLooping (3-4 restarts).
- `kubectl get organizations.orgs.openova.io -A` → `No resources found`.
- `kubectl auth can-i create namespaces --as=...cutover-driver` → `no`.
- `kubectl -n flux-system get kustomization sme-tenants` → `False` (path not found).
- `curl marketplace.hw150.omantel.biz/redeem | grep openova.io` → `4 console.openova.io`, `1 omantel.openova.io`.
- mint-job log: `existing token ... is VALID — re-run is a no-op` (the early `exit 0` that skips the annotation stamp).
