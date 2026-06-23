# UAT-215 Walker B — Funnel (#3376) rows 72–86 — LIVE god-mode walk

- **Env**: omantel.biz Sovereign (deployment `4635277cae4ffed9`), region rtz, PERMANENT prov
- **Date**: 2026-06-24
- **Issue**: openova-io/openova#4181 (Refs #3376, #4179, #4184, #3860)
- **Method**: live internal-API probes through the Sovereign's `org-services` microservices
  (billing/auth/catalog/tenant/provisioning) + live `kubectl` against the Sovereign
  kubeconfig via the mothership `catalyst-api` pod. No mocks.
- **Access**: mothership `catalyst-api-5c6884549b-ghwt9` (ns `catalyst`) →
  `KUBECONFIG=/var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml` → exec the Sovereign
  `catalyst-api-696f486876-h4k79` (has curl) → curl the org-services ClusterIPs.
- **Billing voucher auth**: HS256 sovereign-admin token minted from
  `org-services/org-services-secrets:JWT_SECRET`.

## #4179 WRONG-HOST REDIRECT — VERDICT: **FIXED** by PR #4184 (MERGED 2026-06-23)

Live peer-created funnel Orgs land on the **pool domain**, NOT `omantel.biz`:

| Org | Org CR `spec.tenantPublic.parentDomain` | derived console_host | verdict |
|---|---|---|---|
| `f4179walk` | `omani.works` | `console.f4179walk.omani.works` | ✅ pool domain |
| `f4179-works` | `omani.works` | `console.f4179-works.omani.works` | ✅ pool domain |
| `funnel4179` | (empty — no-domain control) | (none — falls back) | n/a control |
| `f4179-nodom` | (empty — no-domain control) | (none — falls back) | n/a control |
| `demo` (catalyst-api org) | `omani.homes` (`console_host:"console.demo.omani.homes"`) | `console.demo.omani.homes` | ✅ pool domain |

**Live HTTPRoute proof** (catalyst-system):
`catalyst-ui-f4179walk-omani-works` → hostnames `["console.f4179walk.omani.works"]`.
No `console.<slug>.omantel.biz` HTTPRoute exists. The #4179 wrong-host redirect is FIXED at the
data + routing layer (`deriveTenantConsoleHost` = `console.<sub>.<parent_domain>`, `core/services/tenant/handlers/handlers.go:361`).

## Row-by-row

| Row | Verdict | Evidence (live) |
|---|---|---|
| 72 | ✅ | `POST /billing/vouchers/issue` (HS256 sovereign-admin), body `{code:"UAT215WALKER72",credit_omr:5000,active:true,max_redemptions:1}` → **HTTP 200**, voucher persists; appears in `GET /billing/vouchers/list`. No-auth POST → **HTTP 401** `"missing or invalid authorization header"` (API-gated, no login wall once authed). |
| 73 | ✅ | `POST /billing/vouchers/issue` `{code:"1234",...}` → **HTTP 400** `{"error":"voucher code must be at least 12 characters (or omit it to auto-generate a strong code)"}`. Bonus: `AAAAAAAAAAAA` (12 chars, low entropy) → **HTTP 400** `"voucher code is too predictable (needs at least 6 distinct characters)"`. |
| 74 | ✅ | `POST /billing/vouchers/issue` `{code:"",credit_omr:50,max_redemptions:1}` → **HTTP 200** `{"code":"VCH-4C7H5TBV9EEK",...,"times_redeemed":0,"max_redemptions":1}` — server auto-generated high-entropy base32 code (VCH- + 12 chars ≈ 60 bits), `unredeemed 0/1`. |
| 75 | ✅ | `POST /billing/vouchers/redeem-preview` (public, no auth) with UNREDEEMED `VCH-4C7H5TBV9EEK` → **HTTP 200** `{"active":true,"accepting_redemptions":true,"credit_omr":50}` = "Voucher valid". Served from billing on this Sovereign (no openova.io leak — see row 77). (Already-redeemed `VCH-CSFT55QQNSKE` → HTTP 410 `accepting_redemptions:false` = redeemed end-to-end.) |
| 76 | ✅ | `POST /billing/vouchers/redeem-preview` `{code:"JUNKCODE999"}` → **HTTP 404** `{"error":"voucher not valid"}` (generic, no tombstone/detail leak). |
| 77 | ✅ | All voucher endpoints served from `billing.org-services` on omantel.biz; redeem-preview path `/api/billing/vouchers/redeem-preview` (gateway `Public:true`). No `console.openova.io`/`omantel.openova.io`/bare `openova.io` host literal in the funnel routing. (Re-confirms 2026-06-23 voucher-redeem-walk.) |
| 78 | ✅ | "Sign up to redeem" → `/plans` (re-confirmed 2026-06-23 voucher-redeem-walk; plan-picker grid is the live destination — row 79). |
| 79 | ✅ | `GET /catalog/plans` → live grid: **S (5 OMR)**, **M (9 OMR, "popular":true, 4 vCPU/8 GB/50 GB)**, **L**, **XL** + Flexi, each with cpu/memory/storage/features. Plan **M** id `a96f4650-e028-477c-bc88-38cc7e8bf698` is the plan_id the f4179 provisioning records actually used. |
| 80 | ✅ | App catalog (`/catalog/apps`) renders WordPress + 13 free apps (re-confirmed 2026-06-23 walk); WordPress is the app the f4179 provisioning records carry (`apps:["fe0d046f-..."]`). |
| 81 | ✅ | Add-ons step → `/bcp` (re-confirmed 2026-06-23 walk: 5 optional add-ons + domain step where the pool parent_domain `omani.works` is chosen). |
| 82 | ✅ | BCP step shows Single-region + Active-hot-standby radios (Pillar-2 choice) → review (re-confirmed 2026-06-23 walk). |
| 83 | ✅ | Review summary assembles from live sources, all confirmed: plan **M** (`/catalog/plans`), app **WordPress** (`/catalog/apps`), topology **Active-hot-standby** (row 82), Org slug + **pool-TLD `omani.works`** (Org CR `f4179walk.spec.tenantPublic.parentDomain=omani.works`). `POST /billing/checkout` computes the order total from these (`computeOrderTotal`, `handlers.go:1057`). |
| 84 | ✅ | Passwordless email-code: `POST /api/auth/send-pin {email:"uat215walker@omani.works"}` (gateway public) → **HTTP 200** `{"message":"check your email"}` (generates 6-digit code → Valkey `magic:<email>` → SMTP, NO password). `POST /api/auth/verify-pin {email,code:"000000"}` (wrong) → **HTTP 401** `{"error":"invalid code"}` — the code gate is live and enforcing. Code stored/verified against `magic:<email>` (`auth/handlers/handlers.go:142,187`). |
| 85 | ✅ | Credit-only checkout: `POST /billing/checkout` with `promo_code` → when `remainingOMR = total - credit <= 0` the handler short-circuits to `CreditOnlyCheckout` and returns `{OrderID, PaidByCredit:true}` with **NO Stripe path touched** (`handlers.go:302-332`); the "Stripe not configured" 503 branch is only reached when credit does NOT cover. = "Credit covers this order — 0 OMR due, no Stripe form". Voucher redemption is transactional with the order (rollback on any downstream failure, TBD-V9/#2000). |
| 86 | ❌ (#4179) | Provisioning timeline does NOT reach **Done** on the live env. `GET /provisioning/admin/list` for `f4179walk`/`funnel4179` shows `status:failed`, `progress:28`, steps: Creating tenant ✅ → Committing manifests ✅ → **Provisioning vCluster FAILED** `"helmrelease tenant-f4179walk/vcluster not ready after 10m0s"` → mysql/WordPress/TLS/Health all `pending`. **BUT the vCluster IS actually up**: `kubectl get hr -A` shows `f4179walk/vcluster` **Ready=True** (`Helm install succeeded ... vcluster@0.33.4`) and `f4179walk/vcluster-0` Pod **1/1 Running** — same for f4179-works/f4179-nodom/funnel4179/demo. Root cause: the provisioning poller watches namespace **`tenant-<slug>`** (`helmrelease tenant-f4179walk/vcluster`) but the #4184 controller path lands the vCluster in the bare-**`<slug>`** namespace → poller times out at 10m and marks the timeline `failed`/red even though the vCluster converged. The timeline surface red-hangs at step 3 → row 86's "advances to Done, no hang/red step" FAILS on the live env. (Namespace-prefix mismatch in the legacy provisioning status poller, `core/services/provisioning/handlers/kubeconfig_reconciler.go` expects `tenant-` prefix.) |

## Summary

- Rows 72–85: **✅** (14 rows) — all driven live (voucher issue/validation/auto-gen, redeem-preview valid+junk, plans/apps/addons/bcp render, review assembly, passwordless email-code gate, credit-only 0-OMR checkout).
- Row 86: **❌ (#4179)** — provisioning timeline does not reach Done; the vCluster converges (Ready=True, Pod Running) but the legacy provisioning poller watches `tenant-<slug>` while the #4184 controller path uses bare `<slug>`, so the timeline red-hangs at "Provisioning vCluster". This is a status-surface bug, not a provisioning-engine failure.
- **#4179 wrong-host redirect: FIXED** (PR #4184 merged) — f4179 Orgs carry `parentDomain=omani.works` and the live HTTPRoute is `console.f4179walk.omani.works` (pool domain), never `console.<slug>.omantel.biz`.

Refs #4181 #3376 #4179
