# #4196 — Native Billing menu + voucher creation — live walk on omantel.biz

**Status — last validated: omantel.biz (2026-06-24)**
**Result: 5/5 DoD PASS (live browser walk, sovereign-admin session)**

PR #4198 (merged `31bd8ca`) ships ONE first-class native **Billing** menu — a sibling
of Dashboard/Apps/Catalog/…/Organizations in the SovereignSidebar — with native
Vouchers · Orders · Revenue sections hitting the catalyst-api `/api/v1/org/billing/*`
bridge. No iframe, no showback wall on Vouchers, no orphan `/…/vouchers/issue` route.

## Roll

- catalyst-ui image rolled live: `ghcr.io/openova-io/openova/catalyst-ui:31bd8ca`
  (was `a64d18e`), via `bp-catalyst-platform` chart `1.4.799 → 1.4.803` on the
  Sovereign Flux HelmRelease (bootstrap-kit slot-13 pin bumped on main by the
  auto-bumper, GitRepository synced to `610f47456`, Kustomization suspend/resume
  to abandon a stale reconcile, HR Helm-upgrade to 1.4.803).
- Served bundle `/assets/index-t8mSPHIf.js` contains `/billing/vouchers` +
  `/billing/orders` routes (the OLD bundle did not).
- Deployment `catalyst-ui` in `catalyst-system` = 1/1 ready on `31bd8ca`.

## Session

Sovereign-admin browser session minted via the Sovereign's OWN handover signer
(`/var/lib/catalyst/handover-jwt-private.pem` inside the Sovereign catalyst-api pod —
NOT the mothership key, NOT the founder mailbox). RS256 handover token
`role=sovereign-admin`, throwaway email `billing-walk@omantel.biz`, driven through
`https://console.omantel.biz/auth/handover?token=…` → `catalyst_session` cookie →
landed `/dashboard`.

## DoD evidence

| # | DoD | Result | Evidence |
|---|-----|--------|----------|
| 1 | Sidebar shows top-level **Billing** item (sovereign-admin) | ✅ | `evidence/dod1-sidebar-billing-item.png` — `Billing` → `/billing`, sibling of Dashboard/Cloud/Apps/Catalog/Sandbox/Jobs/Compliance/Users/Organizations/Settings |
| 2 | Billing → Vouchers renders NATIVE live list (no iframe, no showback wall) | ✅ | `evidence/dod2-vouchers-native-list.png` — native React `<table>` (0 iframes), 5 live vouchers; network proof `GET /api/v1/org/billing/vouchers/list → 200` |
| 3 | Issue a voucher through the UI → appears in list AND redeemable | ✅ | `dod3a-issue-voucher-form-filled.png` (form: code `BILLINGWALK4196UAT`, 50 OMR, description) · `dod3b-new-voucher-in-list.png` (`POST /…/vouchers/issue → 200`, new row Active 50 OMR) · `dod3c-redeem-voucher-valid.png` (`marketplace.omantel.biz/redeem/?code=BILLINGWALK4196UAT` → **"Voucher valid · 50 OMR credit"**) |
| 4 | Revoke a voucher through the UI → leaves the active list | ✅ | `dod4a-revoked-left-active-list.png` (`DELETE /…/vouchers/revoke/BILLINGWALK4196UAT → 200`, row gone from default list) · `dod4b-revoked-filter-empty.png` |
| 5 | Orders + Revenue render native (no iframe) | ✅ | `dod5a-orders-native.png` (`/billing/orders`, `iframeCount=0`) · `dod5b-revenue-native.png` (`/billing/revenue`, `iframeCount=0`); BillingModeGate showback notice retained on these real-payment surfaces per spec |

Bonus: legacy orphan `/organizations/billing/vouchers/issue` now 302→`/billing/vouchers`
(no 404; the old iframe/showback-walled surface is deleted).

## Network ledger (live, catalyst-ui 31bd8ca)

- `GET  /api/v1/org/billing/vouchers/list                     → 200`
- `POST /api/v1/org/billing/vouchers/issue                    → 200`
- `DELETE /api/v1/org/billing/vouchers/revoke/BILLINGWALK4196UAT → 200`
