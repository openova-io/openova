# UAT Walkthrough — #3376 FUNNEL: stranger-with-voucher → signed-in in their OWN org console with the purchased app RUNNING

## Status — last validated: hw158 (2026-06-17) — RE-WALKED step-by-step via curl/kubectl (corrects a prior headline-only pass)

- **Verdict: PARTIAL — funnel front-half PROVEN live, terminal (running app in own Org console) FAILED.** Honest tally across the 29 rows (A1–A6, B1–B20, C1–C3): **11 PASS / 11 FAIL / 5 VISUAL-PENDING / 2 PARTIAL**. The 11 PASS are the front-half (voucher preview, entropy gate, due-zero checkout, rate-limit, sovereign-clean bundle, real Org-CR mint); the 11 FAIL are the terminal half (no namespace, no running app, no per-Org console/realm) + the cutover precondition rows this fresh prov never ran.
- **What is PROVEN LIVE (curl/kubectl on hw158, `ab2135d4cf2d01e4`, alive ~4h):** voucher `VCH-56T66GFC2BNC` redeem-preview -> **HTTP 200** `credit_omr:5000, accepting_redemptions:true`; junk code -> **404 "voucher not valid"**; the **entropy gate** (`1234`->400 "at least 12 characters", `AAAAAAAAAAAA`->400 "too predictable / 6 distinct", empty->auto-gen `VCH-NFF2FEW4VEAX`); **checkout due-zero** with the voucher -> **200 `paid_by_credit:true, credit_balance 5000->4991`** (plan M 9 OMR fully covered by credit); the **rate-limit** (attempts 1-4 -> 200, 5+ -> **429**); the marketplace bundle is **sovereign-clean** (served `/redeem/` HTML has **0** `openova.io`/`console.openova.io`/`omantel.openova.io` literals); the **Organization CR `walk-stranger-co`** is real (minted from the real redeem; `tenant.created` -> org-controller fanned out Keycloak group `e5c61e02...` + Gitea org + per-Org vCluster Flux loop).
- **What FAILED LIVE (the actual acceptance — purchased app RUNNING in the customer's own Org console):** the Org **never reached a running app**. `kubectl get ns` shows **NO `walk-stranger` namespace**; **NO WordPress pod cluster-wide**; **NO per-Org console HTTPRoute**; `https://console.walk-stranger-co.omani.homes` -> **connection refused (HTTP 000)**. TWO distinct blockers strand it: **(1)** SME-funnel provisioning step 2 "Committing manifests to Git" -> **failed** on the **Gitea `sme-tenants` ref-lock race** (`cannot lock ref 'refs/heads/sme-tenants'`); **(2)** even though the org-controller separately populated the per-Org `catalyst-tenant` repo (vcluster.yaml/namespace.yaml/kustomization.yaml present), its **`GitRepository catalyst-tenant-walk-stranger-co` is READY:False — "authentication required"** cloning the per-Org Gitea repo (no repo-level secretRef) -> Kustomization "Source artifact not found" -> **no vCluster HR ever applied**. So B13–B19 + C1–C3 are **FAIL/blocked**.
- **Maps to:** [`../UAT.md`](../UAT.md) **Row 12** (funnel TC-01).
- **Evidence:** the front-half is curl/kubectl-proven inline below. Prior screenshots `../../sessions/2026-06-17/evidence/01-redeem-voucher-valid.png` ... `05-post-launch-redirect.png` + `04-org-cr-kubectl-proof.txt` corroborate the wizard render + Org-mint; `05-post-launch-redirect.png` itself shows the terminal-FAIL (`ERR_CERT_COMMON_NAME_INVALID` — no per-tenant cert because no vCluster).
- **Blocking follow-ups (NOT "non-blocking" — these block the acceptance):** (a) the per-Org `GitRepository` has no auth -> Flux can't pull the populated catalyst-tenant repo -> vCluster never starts; (b) the `sme-tenants` ref-lock race mis-marks the funnel "failed"; (c) org-controller build has per-Org realm + base-RBAC auto-wiring **disabled** (`RealmDisabled`/`BootstrapDisabled` conditions); (d) residual `Omantel Cloud` brand literal in the served redeem HTML on a non-Omantel Sovereign. Section A1/A3 (cutover proof) + Section C (generality 2nd slug/TLD) were not walkable on this env (no Sovereign/continuum CRs; per-Org provisioning blocked).
- **Index:** [`README.md`](README.md). Prior-env (hw150) evidence is void.

> **Format**: every row is ONE concrete human action. `Go to <URL>` / `do <click/type>` / `you should see <screen>` / `[ ]`. Routes + labels are cited from real UI/code (file:line in the ticket). Automated probes (`kubectl` / `curl` / `dig`) are the appendix, labelled NOT-acceptance. **Acceptance = the founder walking the clickable rows below and reaching the terminal screenshot (the purchased app serving inside the customer's own Org console).**
>
> **Env under test**: a FRESH Sovereign reached zero-touch, pool-TLD `omani.works`/`omani.homes` (current = `hw158.omani.works`). The live-failure column below names the **historical hw150 baseline** (`console.hw150.omantel.biz`, `marketplace.hw150.omantel.biz`, 2026-06-16, now wiped/void) — kept as the before-state record this ticket closed, NOT a current claim.
>
> This walkthrough covers EVERY scenario folded into #3376: the provisioning-token cutover contract (was #3689), the Org-namespace RBAC create (was #3663), the sovereign-clean marketplace bundle (was #3691), the tofu-archive handover wire, voucher entropy, and the authenticated-redeem rate-limit.

---

## Section A — Pre-walk: prove the env is cut-over and the three blockers are CLEARED (operator, one-time)

These are gate checks the operator runs once on the fresh prov BEFORE the customer walk. They are pass/fail, not the acceptance walk itself.

| # | Go to / run | You should see | Verdict (hw158) |
|---|---|---|---|
| A1 | Operator console -> **Sovereignty** card (`https://console.<fqdn>/sovereignty`) | `cutoverComplete: true`, all 11 steps green, the 600s deny-egress hold PASSED | **FAIL — NOT proven on this env.** `kubectl --kubeconfig /tmp/hw158-kc.yaml get continuum -A` -> `No resources found`; no `sovereigns.catalyst.openova.io` CR instances. hw158 is a fresh prov that did NOT run the cutover engine, so the Sovereignty card has nothing green to show. (Cutover is the #3630/#3639/#3640 track, not provable here.) |
| A2 | `kubectl -n sme get pods \| grep provisioning` | BOTH `provisioning-*` replicas `1/1 Running` — no `Init:0/1` (blocker-a cleared) | **PARTIAL-PASS.** `...-n sme get pods \| grep provisioning` -> `provisioning-7c9795d7dd-mnz6b   1/1   Running   0   164m`. Pod is **Running, NOT Init:0/1** (blocker-a init-gate IS satisfied) — but there is **ONE** replica, not "BOTH" (deploy runs replicas=1; the "both" wording is stale). |
| A3 | `kubectl -n sme get secret provisioning-github-token -o jsonpath='{.../token-source}'` | `self-sovereign-cutover-step-09` IS present (mint-job stamps it even on the no-op path) | **FAIL — annotation ABSENT.** `...-o jsonpath='{.metadata.annotations}'` -> `catalyst.openova.io/mirrored-from: catalyst-system/catalyst-gitea-token` + `catalyst.openova.io/note: manual-mirror-uat-hw158-funnel-unblock` — the secret was **hand-mirrored to unblock this UAT**, NOT stamped by `self-sovereign-cutover-step-09` (consistent with A1: cutover never ran). The token works (A2 pod Running) but the stamp contract is unproven. |
| A4 | `kubectl auth can-i create namespaces --as=...:catalyst-api-cutover-driver` | `yes` (blocker-b cleared: cutover-driver ClusterRole grants `namespaces: [create]`) | **PASS.** `kubectl --kubeconfig /tmp/hw158-kc.yaml auth can-i create namespaces --as=system:serviceaccount:catalyst-system:catalyst-api-cutover-driver` -> **`yes`** (control: `...:catalyst-api` -> `no`, so the grant is specific to the cutover-driver SA, not blanket). |
| A5 | `kubectl -n flux-system get kustomization sme-tenants` | `READY: True` | **FAIL.** `...-n flux-system get kustomization sme-tenants` -> **`READY: False` — "Source artifact not found, retrying in 30s"**. The customer-onboarding Kustomization does NOT reconcile (upstream of the funnel commit failing the `sme-tenants` ref-lock — see B12). |
| A6 | `curl -sk 'https://marketplace.<fqdn>/redeem/?code=X' \| grep -oE 'openova\.io'` | empty output (blocker-c cleared) | **PASS.** `curl -sSk -L 'https://marketplace.hw158.omani.works/redeem/?code=X' \| grep -oE 'openova\.io' \| sort \| uniq -c` -> **empty** (0 matches). Served bundle has zero mothership-domain literals. **Note:** the HTML carries one `Omantel Cloud` brand string (1 match) — a residual partner literal on a non-Omantel Sovereign, worth a follow-up, but the `openova.io` assertion A6 makes is clean. |

---

## Section B — THE CUSTOMER WALK (the acceptance): stranger -> voucher -> running app in own Org

### B.1 — Operator mints a voucher (BSS) with entropy enforced

| # | Go to / do | You should see | Verdict (hw158) |
|---|---|---|---|
| B1 | Go to operator console **BSS -> Vouchers** (`https://console.<fqdn>/bss/vouchers`) | The voucher issuance form | **VISUAL-PENDING (rendered page; API proven).** The BSS Vouchers page is a console SPA route — not curl-assertable. The API it drives IS proven: `POST /api/billing/vouchers/issue` is auth-gated (no token -> **401 "invalid or missing token"**) and accepts/lists vouchers (B3 below mints one live). |
| B2 | In the voucher form, **type a deliberately weak code** `1234` and submit | Form **rejects** the weak code with a validation message — entropy gate proven | **PASS — entropy gate PROVEN LIVE.** `POST /api/billing/vouchers/issue` (Bearer SME-JWT) `-d '{"code":"1234","credit_omr":10}'` -> **HTTP 400** `{"error":"voucher code must be at least 12 characters (or omit it to auto-generate a strong code)"}`. Second weak code `AAAAAAAAAAAA` (12 chars, low distinct) -> **HTTP 400** `{"error":"voucher code is too predictable (needs at least 6 distinct characters) ..."}`. Source: `core/services/billing/handlers/voucher_code.go:70` (`minVoucherCodeLen=12`, `minVoucherCodeDistinctChars=6`). |
| B3 | Submit again leaving the code field empty so the server **auto-generates** the code | A new voucher row with a high-entropy code; copy it as `<CODE>` | **PASS — auto-gen PROVEN LIVE.** `POST /api/billing/vouchers/issue` `-d '{"credit_omr":10,"description":"UAT entropy autogen probe"}'` -> **HTTP 200** `{"code":"VCH-NFF2FEW4VEAX","credit_omr":10,...}` — server generated a 12-char base32 (Crockford, no 0/1/O/I/U) code, ~60 bits entropy (crypto/rand). |
| B4 | Note the voucher value (e.g. `10 OMR`), plan tier, and that it is `unredeemed 0/1` | Voucher reflected in the BSS list | **PASS.** The issued voucher returns `credit_omr:10, times_redeemed:0, max_redemptions:0`. The actually-walked voucher `VCH-56T66GFC2BNC` returns `credit_omr:5000, active:true, accepting_redemptions:true` via redeem-preview (B5). |

### B.2 — Stranger opens the marketplace and redeems (sovereign-clean storefront)

| # | Go to / do | You should see | Verdict (hw158) |
|---|---|---|---|
| B5 | As the stranger (fresh browser, no session), go to `https://marketplace.<fqdn>/redeem/?code=<CODE>` | Redeem page: "Voucher valid . N OMR". Brand chrome is **THIS Sovereign's** | **PASS — redeem-preview PROVEN LIVE.** `POST /api/billing/vouchers/redeem-preview -d '{"code":"VCH-56T66GFC2BNC"}'` (public, no auth) -> **HTTP 200** `{"code":"VCH-56T66GFC2BNC","credit_omr":5000,"description":"UAT funnel walk hw158","active":true,"accepting_redemptions":true}` -> page shows "Voucher valid . 5000 OMR". Junk code -> **404** `{"error":"voucher not valid"}` (generic, no tombstone leak). The `/redeem/` page itself renders (HTML 23 KB, served 200). NOTE: one `Omantel Cloud` brand literal present (see A6/B6). |
| B6 | **View page source / DevTools -> the document HTML** | The HTML contains **no** `console.openova.io`, **no** `omantel.openova.io`, **no** bare `openova.io` (matches A6) | **PASS.** `curl .../redeem/?code=VCH-... \| grep -oE 'console\.openova\.io'` -> 0; `omantel\.openova\.io` -> 0; `openova\.io` -> 0. Confirms the served bundle is sovereign-clean of mothership domains (matches A6). The inline brand table is `window.__SME_BRANDS__` (data, not baked literal) per the `#3376 (Refs #3691)` comment in the served HTML. |
| B7 | Click **"Sign up to redeem"** -> lands on `/plans` | Plan picker grid | **VISUAL-PENDING; API proven.** `GET /api/catalog/plans` (public) -> **HTTP 200**, 9 plans (S/M/L/XL/Flexi/Sandbox x3) with price_omr/cpu/memory. The `/plans` grid renders from this; SPA route not curl-assertable. |
| B8 | Pick a plan -> `/apps` | Catalog app grid (served from THIS Sovereign's catalog) | **VISUAL-PENDING; API proven.** `GET /api/catalog/apps` (public) -> **HTTP 200**, **32 apps** from this Sovereign's catalog (incl. the WordPress app id `b0b8c1ff-...` used downstream). |
| B9 | Pick the purchased app (e.g. **WordPress**) -> `/addons` -> `/bcp` | **BCP topology** step renders both **Single-region** and **Active-hot-standby** options | **VISUAL-PENDING.** BCP is a client wizard step (no dedicated public API). Prior evidence `02-bcp-active-hot-standby.png` shows the **Active-hot-standby** option selectable (Pillar-2 choice present). curl cannot assert the rendered radio; the captured screenshot does. |
| B10 | Choose a topology -> `/review` | Review summary: plan, app, topology, org slug `<slug>`, pool-TLD | **VISUAL-PENDING; slug API proven.** `GET /api/tenant/check-slug/walk-stranger-co` -> **HTTP 200** `{"available":false}` (the slug is taken — it was minted); a fresh slug -> `{"available":true}`. The review screen renders these; SPA not curl-assertable. The real walk used slug `walk-stranger-co` on pool-TLD `omani.homes`. |
| B11 | `/checkout` -> PIN-login with the Stalwart PIN, confirm the Organization | Checkout submits; the **provisioning progress timeline advances** (does NOT hang) | **PASS — checkout PROVEN LIVE (due-zero credit settle).** `POST /api/billing/checkout` (Bearer SME-JWT) `-d '{"plan_id":"5e84ff22..."(M,9 OMR),"tenant_id":"8a4c40ad...","promo_code":"VCH-56T66GFC2BNC","apps":["b0b8c1ff..."]}'` -> **HTTP 200** `{"order_id":"4d3a4f29-...","paid_by_credit":true,"credit_balance":4991}` — 5000 credit minus 9 OMR plan = **due settled to 0** ("Credit covers this order"), order placed, no Stripe needed. Timeline advances (does not hang). |

### B.3 — The Organization provisions zero-touch (the blocker-a + blocker-b chain now RUNS)

| # | Go to / do | You should see | Verdict (hw158) |
|---|---|---|---|
| B12 | Watch the `/checkout` progress timeline | Steps advance to **Done** — `tenant.created` consumed by the provisioning pod, which POSTs the Organization CR; the cutover-driver creates the Org namespace (no 403) | **FAIL — timeline FAILS at step 2.** `GET /api/provisioning/tenant/8a4c40ad-...` -> **HTTP 200**, `status:"failed"`. Steps: 1 "Creating tenant" **completed**; 2 "Committing manifests to Git" **failed** -> `git commit failed ... cannot lock ref 'refs/heads/sme-tenants': is at 97576e3c... but expected 3fd04dbe... ! [remote rejected] ... (failed to update ref)`; steps 3-6 (Provisioning vCluster / Deploying WordPress / TLS / health) all **pending** — never ran. progress=16. The **Gitea `sme-tenants` ref-lock race** strands provisioning. |
| B13 | (operator window) `kubectl get organizations.orgs.openova.io -A` | **>= 1 Organization** with your `<slug>` within ~60s of checkout | **PASS — Org CR REAL.** `kubectl --kubeconfig /tmp/hw158-kc.yaml get organizations.orgs.openova.io -A` -> `walk-stranger-co  walk-stranger-co  customer  sme  real  hw158.omani.works  146m`. Full CR: `spec.owners[0].email=walkstranger@omani.homes`, `kind:customer`, `tier:sme`, `sovereignRef:hw158.omani.works`, label `openova.io/source=tenant-created-event`, `tenant-id=8a4c40ad-...`. Minted from the real redeem (corroborated by `04-org-cr-kubectl-proof.txt`). |
| B14 | (operator window) `kubectl get ns \| grep <slug>` and `...logs deploy/organization-controller` | The Org namespace exists; org-controller logs show fan-out of per-Org **vCluster + Keycloak group/realm + Gitea org + base RBAC** | **FAIL — fan-out STARTED but Org namespace does NOT exist + vCluster never came up.** org-controller logs DO show fan-out: `reconcile ok ... keycloak_group:e5c61e02-250f-4f61-841a-6ad73b5c7bcc, gitea_org:walk-stranger-co, vcluster:walk-stranger-co` + `reconciled per-Org vCluster Flux loop ... git_repository:flux-system/catalyst-tenant-walk-stranger-co, kustomization:...-vcluster`. The per-Org `catalyst-tenant` Gitea repo IS populated (vcluster.yaml/namespace.yaml/kustomization.yaml present, HTTP 200). BUT every reconcile logs `vcluster_phase:Pending, vcluster_ready:false` and **`kubectl get ns \| grep walk-stranger` -> NOTHING**. CR conditions: `Ready=False (VClusterProvisioning)`, `PerOrgRealmProvisioned=False (RealmDisabled — per-Org realm auto-wiring disabled in this controller deployment)`, `IacRepoBootstrapped=False (BootstrapDisabled)`. So Keycloak group + Gitea org fanned out, but **per-Org realm + base RBAC are DISABLED in this controller build** and **no namespace** exists. |

### B.4 — The customer lands signed-in in their OWN console with the app RUNNING (the terminal)

| # | Go to / do | You should see | Verdict (hw158) |
|---|---|---|---|
| B15 | After checkout the marketplace **redirects** the customer to their per-Org console | Browser URL becomes `https://console.<slug>.<pool-tld>` with publicly-trusted TLS | **FAIL — per-Org console host does NOT exist.** `kubectl get httproute -A \| grep walk-stranger` -> **nothing** (no per-Org console route). `curl --max-time 12 https://console.walk-stranger-co.omani.homes` -> **HTTP 000, "Connection refused"** (DNS resolved but no listener). Prior `05-post-launch-redirect.png` shows the redirect landing on a host with `ERR_CERT_COMMON_NAME_INVALID` (no cert, no vCluster behind it). |
| B16 | Observe the console landing | Customer is **signed in zero-click as the Org owner** (per-Org realm, the SSO contract) | **FAIL — blocked — no per-Org console + no per-Org realm.** B15 shows no host to land on; CR condition `PerOrgRealmProvisioned=False (RealmDisabled)` means the per-Org Keycloak realm was never created (the org-controller build has realm auto-wiring disabled). Zero-click SSO into the Org console is unreachable on this env. |
| B17 | In the Org console, open the purchased app's card and click **Open** | The purchased app (e.g. WordPress) **serves** at its own FQDN — **TERMINAL SCREENSHOT = the running app** | **FAIL — THE ACCEPTANCE FAILS — no running app.** `kubectl get pods -A \| grep -i wordpress` -> **NO WordPress pod cluster-wide**; `kubectl get ns \| grep walk-stranger` -> **no namespace**; no per-Org vCluster HR applied. Root cause: per-Org `GitRepository catalyst-tenant-walk-stranger-co` is **READY:False — "authentication required"** cloning the Gitea repo (no repo-level secretRef) -> Kustomization `catalyst-tenant-walk-stranger-co-vcluster` "Source artifact not found" -> vCluster never starts -> WordPress never deploys. The terminal screenshot (running app in own Org console) **cannot be produced** on hw158. |

### B.5 — Returning customer is NOT bounced to the mothership (blocker-c redirect)

| # | Go to / do | You should see | Verdict (hw158) |
|---|---|---|---|
| B18 | While still signed in (workspace exists), re-open `https://marketplace.<fqdn>` in the same browser | The returning-user redirect sends the customer to **`https://console.<slug>.<pool-tld>`** | **FAIL — not walkable + redirect machinery is demo-keyed.** No signed-in workspace exists (B15-B17 failed -> no Org console host), so the returning-user path can't be exercised. Inspecting the served redirect script: it references `console.demo.omani.homes` + `skipConsoleRedirect` + `window.__SME_TENANT__` — the redirect logic is PRESENT but the only `TENANTS`/brand entry in the served bundle is keyed to `demo`, not the customer slug `walk-stranger-co`, so a returning `walk-stranger-co` user would have no per-Org target to bounce to. |
| B19 | Inspect DevTools -> Network for the redirect `Location` | The redirect target is the THIS-Sovereign console host, **never** `console.openova.io/nova` | **PARTIAL — no mothership bounce (good), but no per-Org target either.** The served HTML contains **0** `console.openova.io/nova` literals (the hw150 failure mode — bounce to mothership — is NOT present). But because there is no per-Org console host (B15) and the redirect table is demo-keyed (B18), the positive assertion "redirects to `console.walk-stranger-co.<tld>`" cannot be confirmed. |

### B.6 — Authenticated-redeem rate-limit (voucher hardening)

| # | Go to / do | You should see | Verdict (hw158) |
|---|---|---|---|
| B20 | As the signed-in customer, **burst** the authenticated redeem/checkout endpoint > N times rapidly | After N attempts the endpoint returns **HTTP 429** (per-customer rate-limit bucket); a single legitimate redeem is unaffected | **PASS — rate-limit PROVEN LIVE.** Bursting `POST /api/billing/checkout` (same Bearer SME-JWT) 8x rapidly -> attempts **1-4 = HTTP 200**, attempts **5,6,7,8 = HTTP 429** (`{"error":"checkout rate-limit exceeded — please wait before retrying"}`). Per-customer bucket = 5 req/60s (`globalCheckoutRateLimiter`, `core/services/billing/handlers/handlers.go:192`, #2941). A single legitimate redeem (B11) was unaffected. |

---

## Section C — GENERALITY PROOF (no per-slug / per-TLD special-casing)

Repeat the WHOLE customer walk with a **different slug AND a different pool-TLD**, proving slug + parent-domain are data, not code.

| # | Go to / do | You should see | Verdict (hw158) |
|---|---|---|---|
| C1 | Mint a second voucher; run B5->B17 with slug `<slug2>` and pool-TLD `omani.rest` | A **second** Organization CR provisions; the customer lands signed-in with ITS purchased app running | **FAIL — NOT walked — blocked by the B13–B17 terminal failure.** Since the FIRST walk's app never runs (per-Org GitRepository auth failure + sme-tenants ref-lock), a second slug/TLD walk would hit the same wall. No second Org CR was minted on hw158. |
| C2 | `git grep` the marketplace bundle + the chart for either slug/TLD literal | **No** `<slug>`/`<slug2>`/`omantel.biz`/`omani.rest` host literal was added | **PASS (repo-level, env-independent).** The served redeem bundle reads brands from `window.__SME_BRANDS__` (data) and the slug from `/api/tenant/check-slug/<slug>` — confirmed in the served HTML and `core/services/gateway/main.go` (slug is a path param, public). No per-slug host literal is baked in; `walk-stranger-co` exists purely as Org-CR/Gitea data. (The full two-walk generality proof still requires C1 to pass.) |
| C3 | Compare B17 and C1 terminal screenshots | Two different Orgs, two different TLDs, two running apps — identical mechanism | **FAIL — N/A — neither terminal exists.** B17 produced no running app and C1 was not walked, so there are zero terminal screenshots to compare. |

---

## Appendix — automated probes (NOT acceptance; evidence only)

These ran during the build/verify; they are diagnostic, not the founder walk.

```bash
# blocker-a — provisioning pods Running, token-source annotation present
kubectl -n sme get pods | grep provisioning            # 1/1 Running (replicas=1), no Init:0/1
kubectl -n sme get secret provisioning-github-token \
  -o jsonpath='{.metadata.annotations}'                # hw158: manual-mirror note, NOT cutover-step-09 stamp

# blocker-b — Org-namespace create RBAC
kubectl auth can-i create namespaces \
  --as=system:serviceaccount:catalyst-system:catalyst-api-cutover-driver   # yes (control SA catalyst-api -> no)
kubectl get organizations.orgs.openova.io -A           # walk-stranger-co (real, from the redeem)

# blocker-c — sovereign-clean served marketplace bundle
curl -sk 'https://marketplace.hw158.omani.works/redeem/?code=X' | grep -oE 'openova\.io' # empty (clean)

# the funnel terminal blockers (hw158, 2026-06-17)
curl -sk https://marketplace.hw158.omani.works/api/provisioning/tenant/8a4c40ad-78a8-4a64-964d-a6f6398fbb41
   # status:"failed" at step "Committing manifests to Git": cannot lock ref refs/heads/sme-tenants (ref-lock race)
kubectl -n flux-system get gitrepository catalyst-tenant-walk-stranger-co
   # READY:False — "authentication required" cloning the per-Org Gitea repo (no repo-level secretRef)
kubectl -n flux-system get kustomization sme-tenants                       # READY:False — Source artifact not found
kubectl get ns | grep walk-stranger                    # NOTHING — no Org namespace
kubectl get pods -A | grep -i wordpress                # NOTHING — no running app
curl -sk --max-time 12 https://console.walk-stranger-co.omani.homes        # HTTP 000 Connection refused (no per-Org console)

# the front-half PROVEN live (curl)
curl -sk -XPOST https://marketplace.hw158.omani.works/api/billing/vouchers/redeem-preview \
  -d '{"code":"VCH-56T66GFC2BNC"}'                      # 200 credit_omr:5000 accepting_redemptions:true
curl -sk -XPOST https://marketplace.hw158.omani.works/api/billing/vouchers/issue \
  -H 'Authorization: Bearer <SME-JWT>' -d '{"code":"1234","credit_omr":10}'  # 400 "at least 12 characters"
curl -sk -XPOST https://marketplace.hw158.omani.works/api/billing/checkout \
  -H 'Authorization: Bearer <SME-JWT>' \
  -d '{"plan_id":"5e84ff22...","tenant_id":"8a4c40ad...","promo_code":"VCH-56T66GFC2BNC","apps":["b0b8c1ff..."]}'
   # 200 paid_by_credit:true credit_balance:4991  (5000-9 OMR plan M = due 0); 5th rapid call -> 429
```

**Live-failure baseline captured on hw150 (2026-06-16), now void:**
- `provisioning-*` both `Init:0/1` CrashLooping (3-4 restarts). — hw158: 1/1 Running (init-gate cleared).
- `kubectl get organizations.orgs.openova.io -A` -> `No resources found`. — hw158: walk-stranger-co exists (real).
- `kubectl auth can-i create namespaces --as=...cutover-driver` -> `no`. — hw158: yes.
- `kubectl -n flux-system get kustomization sme-tenants` -> `False` (path not found). — hw158: still False (ref-lock).
- `curl marketplace.hw150.omantel.biz/redeem | grep openova.io` -> `4 console.openova.io`, `1 omantel.openova.io`. — hw158: 0 (clean).
- mint-job log: `existing token ... is VALID — re-run is a no-op`. — hw158: token hand-mirrored (cutover never ran).
