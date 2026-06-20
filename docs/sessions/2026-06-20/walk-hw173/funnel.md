# Walk hw173 — Epic: funnel (UAT rows 72–95, #3376 / #3860)

- Env: hw173 (depID `7bb723da8da06047`), me-east-215-a/b, 5h+ uptime.
- Method: marketplace public API + console handover session + minted SME HS256
  token (issued from in-cluster `org-services/sme-secrets` JWT_SECRET) +
  mothership-kubectl into the live cluster.
- Last validated: hw173, 2026-06-20.

## Headline

Funnel **front-half is LIVE and PASSES** (voucher issue/strength/auto-gen,
redeem-preview valid + junk, brand-leak-free redeem page, plans/apps/addons/bcp/
review/checkout wizard pages all 200, passwordless magic-link send+verify, BCP
topology vocabulary present). **Back-half FAILS**: the `provisioning` service is
in **CrashLoopBackOff** (37 restarts) — FerretDB rejects the
`partialFilterExpression` index option (`NotImplemented`), so the service never
starts. `/api/provisioning/status/*` → **502**. Result: **zero Organizations**
exist (`/api/v1/organizations` → `{"items":[]}`; host `kubectl get
organizations.orgs.openova.io -A` → none; no org vClusters), and no per-Org
console / app FQDN resolves. The signup→org-active→app-serving chain (rows
86–90, 93–95) cannot complete on this env.

This is an **env/infra bug**, NOT one of the 3 peripheral non-ready HRs, so the
back-half rows are honest ❌ (verifiable: provisioning API 502 + no Org CRs),
except the steps that genuinely need a browser-driven multi-step interactive
signup, marked ⚠️ with the API-level evidence gathered.

| Row | Verdict | Evidence (HTTP/JSON/kubectl) | Note |
|-----|---------|------------------------------|------|
| 72 | ✅ | console `GET /bss/vouchers` → HTTP 200 (no login wall); `GET /api/billing/vouchers/list` (sovereign-admin) → 200 returns issued voucher | BSS Vouchers surface renders, no auth wall |
| 73 | ✅ | `POST /api/billing/vouchers/issue` `{"code":"1234",...}` → HTTP **400** `{"error":"voucher code must be at least 12 characters (or omit it to auto-generate a strong code)"}` | weak code rejected inline, exact wording |
| 74 | ✅ | `POST .../issue` `{"code":"",credit_omr:5000,max_redemptions:1}` → HTTP 200 `{"code":"VCH-5FY2TN5KTYF3","credit_omr":5000,"active":true,"max_redemptions":1,"times_redeemed":0}`; list shows `created_at:2026-06-20T08:30:16Z` | server auto-gen high-entropy code, credit, unredeemed 0/1 |
| 75 | ✅ | `POST /api/billing/vouchers/redeem-preview {"code":"VCH-5FY2TN5KTYF3"}` → HTTP 200 `{credit_omr:5000,active:true,accepting_redemptions:true}`; `GET /redeem/?code=…` page HTTP 200; brand defaults to "OpenOva" (this Sovereign), no openova.io leak | "Voucher valid · 5000 OMR" with this Sovereign's chrome |
| 76 | ✅ | `POST .../redeem-preview {"code":"JUNKCODE99999"}` → HTTP **404** `{"error":"voucher not valid"}`; page `/redeem/?code=JUNK…` → 200 | generic invalid msg, no tombstone/detail leak |
| 77 | ✅ | `GET /redeem/?code=…` HTML grep for `openova.io`/`console.openova.io`/`omantel.openova.io` → **0 hits**; brand table read at runtime from `window.__SME_BRANDS__` (no baked partner literal); `<title>` "Redeem voucher — OpenOva SME" | no mothership host literal in source |
| 78 | ✅ | "Sign up to redeem" target `/plans` → 301 → `/plans/` HTTP 200 | plan-picker grid reachable |
| 79 | ✅ | `GET /api/catalog/plans?product=generic` → 200, tiers **S/M/L/XL/Flexi** w/ price_omr 5/9/16/30/0 + cpu/memory/storage; `/plans/` page 200 | plans grid w/ price/CPU/memory |
| 80 | ✅ | `GET /api/catalog/apps?published=true` → 200 includes `{"slug":"wordpress","deployable":true,...,"tags":[...,"cnpg-pair"]}`; `/apps/` page 200 | WordPress present in THIS Sovereign's catalog, advances to /addons |
| 81 | ✅ | `/addons/` page HTTP 200; `GET /api/catalog/addons` served; `/bcp/` page 200 | add-ons step renders, Continue → /bcp |
| 82 | ✅ | live bundle `/_astro/BCPStep.Dio7CxgA.js` contains `Single-region`, `Active-hot-standby`, "Business continuity"; `/bcp/` page 200 | both BCP radios present (Pillar-2 choice at signup) |
| 83 | ✅ | `/review/` page HTTP 200; review summary component served (plan/app/topology/slug/pool-TLD) | review summary reachable → checkout |
| 84 | ⚠️ | `POST /api/auth/magic-link {email}` → HTTP 200 `{"message":"check your email"}`; `POST /api/auth/verify {email,code:000000}` → HTTP **401** `{"error":"invalid code"}` (path validates) | passwordless email-code path LIVE; the actual emailed-code → "signed in" step needs mailbox + browser (not reachable by API alone) |
| 85 | ⚠️ | `/checkout/` page 200; CheckoutStep bundle/source carries "✓ Credit covers this order — no card charge needed." + "Launch my tenant"; voucher credit applies via `paid_by_credit` in `POST /billing/checkout` | credit-covers copy present; full place-order needs a signed-in customer session (browser) |
| 86 | ❌ | `GET /api/provisioning/status/<id>` → HTTP **502**; pod `org-services/provisioning` **CrashLoopBackOff** 37×, log: `failed to create provision indexes … Index option "partialFilterExpression" is not implemented yet` (FerretDB) | provisioning timeline cannot advance — service down |
| 87 | ❌ | `https://console.acme.omani.homes/` → HTTP **000** (no DNS/connect); zero Orgs provisioned | no per-Org console exists (back-half blocked by row 86) |
| 88 | ❌ | no per-Org console FQDN resolves (000); `/api/v1/organizations` → `{"items":[]}` | no Org → no zero-click owner landing |
| 89 | ❌ | no per-Org console; `kubectl get organizations.orgs.openova.io -A` → No resources found | no Applications view for a non-existent Org |
| 90 | ❌ | `https://wordpress.acme.omani.homes/` → HTTP **000** (no DNS/connect) | purchased WordPress site does not exist (chain blocked at row 86) |
| 91 | ⚠️ | redeem page inline script implements returning-user redirect to Org console (source verified); cannot exercise without a signed-in customer w/ a live workspace — none exists (no Org) | mechanism present in shipped bundle; unverifiable live (no provisioned Org to return to) |
| 92 | ❌ | 8 rapid `POST /api/billing/vouchers/redeem-preview` → all HTTP 200, **no 429** after 5; gateway rate-limit is 120 RPM/IP not a per-customer 5-attempt redeem throttle | the per-customer >5×-in-seconds redeem/checkout rate-limit did not trip at the API level (preview is public + IP-RPM-limited only) |
| 93 | ❌ | depends on a 2nd Org provisioning (omani.rest slug) — provisioning down (row 86); zero Orgs | generality re-walk blocked by the same provisioning crash |
| 94 | ❌ | `https://console.walk-stranger-two.omani.rest/` → HTTP **404** (no Org); no 2nd-Org console | 2nd-Org zero-click landing not reachable |
| 95 | ❌ | no 2nd-Org app FQDN; chain blocked at row 86 | 2nd-Org app does not serve |

## Root cause (back-half)

```
org-services/provisioning  CrashLoopBackOff (37 restarts)
  2026/06/20 08:26:14 INFO  connected to FerretDB db=provisioning
  2026/06/20 08:26:14 ERROR failed to create provision indexes
        error="store: ensure provision indexes:
        (NotImplemented) Index option \"partialFilterExpression\"
        is not implemented yet"
```

The provisioning store calls `EnsureIndexes` at startup with a partial-filter
index that FerretDB (the in-cluster Mongo-wire backend) does not implement →
fatal startup → CrashLoop → `/api/provisioning/*` 502 → the funnel back-half
(Creating Org → manifests → vCluster → WordPress → TLS → Health) never fires.
Front-half (catalog/billing/auth/tenant services) is fully green.

## Tally (24 rows, 72–95)

- ✅ PASS = **12** — rows 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83
- ⚠️ PARTIAL/BLOCKED = **3** — rows 84, 85, 91 (need browser-driven signed-in session)
- ❌ FAIL = **9** — rows 86, 87, 88, 89, 90, 92, 93, 94, 95
- Front-half voucher/redeem/wizard chain: fully LIVE.
- Back-half blocker: `provisioning` CrashLoop (FerretDB `partialFilterExpression`
  unsupported) → zero Organizations → no org-active, no per-Org console/app.
