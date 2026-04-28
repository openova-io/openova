# Catalyst Franchise Model

**Status:** Authoritative. **Updated:** 2026-04-28.

This document describes the **franchise model** — how a Sovereign owner (the franchisee) acquires customers via voucher codes, redeems them into Catalyst Organizations, and earns revenue alongside OpenOva.

The voucher mechanism is **already implemented** in the existing admin app (per `docs/PROVISIONING-PLAN.md` agreement: "Existing admin voucher implementation is the source of truth"). This document documents what's there, not what's planned.

---

## The chain of responsibility

```
OpenOva (publisher) ── publishes ──▶ Catalyst (the platform)
                                     │
                                     ├── deployed as ──▶ Catalyst-Zero (Contabo)
                                     │                     │
                                     │                     ├── provisions ──▶ omantel.omani.works (a franchised Sovereign on Hetzner)
                                     │                     │                    │
                                     │                     │                    ├── omantel-admin issues a voucher (PromoCode)
                                     │                     │                    │
                                     │                     │                    ▼
                                     │                     │              kestrel-rx (a tenant Organization)
                                     │                     │                redeems voucher → BHD 100 credit applied
                                     │                     │                creates Organization, installs Apps
                                     │
                                     └── deployed as ──▶ acme-bank Sovereign (corporate self-host on AWS)
                                                           │
                                                           ├── bank's IT issues vouchers to internal teams
                                                           ▼
                                                       core-banking, digital-channels, fraud-scoring (Catalyst Organizations)
                                                       redeem vouchers → internal credit applied
```

**Every franchised Sovereign runs the same admin app** as Catalyst-Zero. The same `PromoCode` CRUD endpoints exist on every Sovereign. There is no shape difference between OpenOva-run and franchisee-run — only the entity issuing the vouchers differs.

---

## Voucher = `PromoCode`

The canonical voucher CRD (already implemented at `core/admin/src/lib/api.ts`) has these fields:

| Field | Type | Purpose |
|---|---|---|
| `code` | string | User-facing redemption code (e.g. `OMANTEL-LAUNCH-100`) |
| `credit_omr` | number | Credit applied to the redeemer's billing account in Omani Rial. Localizable per Sovereign currency. |
| `description` | string | Free-text reason / campaign name |
| `active` | boolean | Issuer can deactivate without deletion |
| `max_redemptions` | number | 0 = unlimited; otherwise hard cap |

API endpoints:

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /v1/admin/promos` | `org-admin` or `sovereign-admin` | List vouchers in this Sovereign |
| `POST /v1/admin/promos` | `org-admin` or `sovereign-admin` | Issue a new voucher |
| `PUT /v1/admin/promos/:code` | issuer | Update / deactivate |
| `DELETE /v1/admin/promos/:code` | issuer | Remove |
| `POST /v1/redeem` | unauthenticated (rate-limited) | Public redemption: validates code + creates Catalyst Organization for the redeemer |

These endpoints are served by the **existing** `core/admin` UI + `core/services/billing` backend. No new CRDs.

---

## Redemption flow (end-to-end)

1. **Voucher issuance.** A `sovereign-admin` for `omantel.omani.works` (e.g. an Omantel Cloud Operator) opens `console.omantel.omani.works/admin` → Billing → Promo Codes → New. Picks code (e.g. `OMANTEL-MUSCAT-200`), credit (e.g. 200 OMR), max redemptions (e.g. 50). Saves.

2. **Voucher distribution.** Omantel markets the code to SME owners via mobile-bill inserts, partner channels, conferences. The redemption URL is `https://omantel.omani.works/redeem`.

3. **Customer signs up.** A pharmacy owner (Ahmed) visits the redemption URL, enters the code, and is taken through Keycloak signup. After authentication:
   - Catalyst's `provisioning` service auto-creates a Catalyst Organization for Ahmed (e.g. `kestrel-pharmacy`).
   - The voucher's `credit_omr` is added to the Organization's billing balance.
   - Ahmed lands in the marketplace and can install Apps using the credit.

4. **App installs draw down credit.** Each App install consumes a billable amount per the App's tier (per the existing billing-tier surface). When credit is exhausted, Stripe is invoked for additional charges.

5. **Revenue split.** OpenOva and the franchisee share the revenue per their license agreement. Mechanically: every charge generated on a franchised Sovereign carries a metadata field `sovereign=<fqdn>`; OpenOva's billing rollup queries Stripe for those charges, computes the franchisee's share, and pays out monthly.

The split percentage is **NOT a per-Sovereign config field** — it's negotiated bilaterally between OpenOva and each franchisee, and lives in OpenOva's accounting system, not in the Catalyst code.

---

## What franchisees CAN do

- Issue their own vouchers (any code, any credit amount, any cap)
- Curate `catalog-sovereign` Gitea Org with their own private Blueprints (e.g. omantel adds `bp-wordpress`, `bp-jitsi`, `bp-cal-com` for their SME tenants — neither in the public catalog nor accessible to other Sovereigns)
- Set their own marketplace branding (logo, colors, hostname, Keycloak themes)
- Choose their billing tier for their tenants (the per-tier OMR/USD pricing they pass through)

## What franchisees CANNOT do

- Install non-cosigned Blueprints (Kyverno admission policy enforced on every Sovereign denies unsigned manifests)
- Modify Catalyst's own CRDs (the bp-catalyst-platform umbrella locks the Catalyst CRD group at install time)
- Bypass the Sovereign-wide `EnvironmentPolicy` (defined by sovereign-admin in the Sovereign's `system` Gitea Org)
- See data inside their tenants' Organizations (vcluster + Keycloak realm + OpenBao path isolation prevent cross-tenant access)

---

## Cross-Sovereign tenancy

A customer can have Organizations on **multiple Sovereigns**. For example:
- Their primary Organization on `omantel.omani.works` (acquired via the omantel voucher)
- A second Organization on `acme-bank` Sovereign (a subsidiary of theirs in another country)

Each Organization is independent (separate Gitea Org, separate vcluster, separate billing balance). The customer's Keycloak identity may federate across, but Apps and Environments do not.

---

## Migration between Sovereigns

Per `SOVEREIGN-PROVISIONING.md` §10, an Organization can be exported from one Sovereign and imported into another. The voucher trail and redemption history is part of the export bundle (since both are stored in the same per-Sovereign billing database).

When a franchisee winds down (rare but supported), all their tenants can migrate to OpenOva-run Sovereigns or another franchisee with no loss of state. The franchise contract specifies the SLA for assisted migration.

---

## Deferred to follow-up

- Voucher CRD lifting from billing-DB row to first-class Catalyst CRD (so vouchers appear in `kubectl get vouchers` and are audit-loggable via JetStream events). Currently they live in the `core/services/billing` Postgres database, accessed via the admin UI's REST endpoints.
- Cross-Sovereign voucher (e.g. an OpenOva-issued voucher redeemable on any franchised Sovereign). Today vouchers are scoped to the issuing Sovereign.
- Discount-tier vouchers (e.g. "20% off all installs for 3 months") — current implementation supports flat credit only.

---

*Part of [OpenOva](https://openova.io)*
