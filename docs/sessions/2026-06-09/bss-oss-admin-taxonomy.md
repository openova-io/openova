# Requirement — BSS/OSS operator-admin split + BSS Plans management

**Status:** captured (requirement) — 2026-06-09
**Origin:** founder direction during hw124 walk — *"This must be a menu in BSS where user could manage the plans. Whatever was there in the past SME admin panel they need to be there in the BSS and OSS. Currently we do not have the OSS, but you can consider OSS as the technical components of admin SME panel to be moved under OSS."*

---

## Problem

1. **T-shirt sizes (marketplace plans) are not operator-editable.** They are seeded in Go (`core/services/catalog/handlers/seed.go`) and only changeable by editing code + redeploying. The admin CRUD endpoints already exist but have **no UI**:
   - `POST   /catalog/admin/plans`
   - `PUT    /catalog/admin/plans/{id}`
   - `DELETE /catalog/admin/plans/{id}`
2. **The operator-admin surfaces are not split along the BSS/OSS line.** Business surfaces (BSS) exist; the technical/operational surfaces (would-be OSS) are scattered under `/admin/*` and assorted top-level nav items. There is **no OSS section** today.

## Target taxonomy

The operator console's admin surfaces divide into two families:

### BSS — Business Support Systems (`/bss`, exists today)
The commercial / customer-facing back-office. **Today:** Billing, Orders, Revenue, Vouchers, Tenants.
**Add:**
- **Plans** (`/bss/plans`) — manage the marketplace t-shirt sizes (S/M/L/XL/Flexi + product-scoped tiers like Sandbox). Table + create/edit/delete wired to the existing `/catalog/admin/plans` endpoints. Removes the "edit Go + redeploy" requirement.
- (future) Add-ons, Bundles management (same `/catalog/admin/*` pattern).

### OSS — Operations Support Systems (`/oss`, NEW)
The technical / operational back-office — the "technical components of the admin SME panel." Re-home the existing `/admin/*` (and scattered top-level) surfaces under one OSS section that mirrors the BSS shell (`BssSectionShell` → `OssSectionShell`, `BssLandingPage` → `OssLandingPage`):

| OSS sub-section | Existing source (to re-home) |
|---|---|
| RBAC — access matrix, roles, groups, members, multi-grant, audit | `pages/admin/rbac/*` |
| User access — list, edit | `pages/admin/user-access/*` |
| Compliance / SRE — policy drilldown, runtime alerts, SecLead + SRE dashboards | `pages/admin/compliance/*`, top-level `/sre/compliance` |
| Parent domains | `pages/admin/parent-domains/*`, top-level `/parent-domains` |
| Blueprint curation / publishing | `pages/admin/blueprints/{CuratePage,PublishPage}` |

## Scope / phasing

- **Phase A (near-term, concrete):** BSS **Plans** page — table + CRUD modals against the existing admin endpoints. This is the piece the founder asked for directly; the backend is already built. *No new backend.*
- **Phase B:** Introduce the **OSS** top-level section (landing + section shell + sidebar nav at the BSS sibling order) and re-home the `/admin/*` surfaces under `/oss/*` with redirects from the old paths.
- **Phase C:** Round out BSS (Add-ons, Bundles management) on the same admin-endpoint pattern.

## Non-goals / guardrails

- No new user surface beyond UI (CLAUDE.md §What's-user-facing): this is console UI work over **existing** APIs.
- Plans CRUD must preserve `ProductSlug` scoping (#3156) — the BSS Plans editor must expose `productSlug` so product-scoped tiers (Sandbox) stay out of the generic Org-provisioning picker.
- OSS re-homing is a **move + redirect**, not a rewrite — the `/admin/*` pages keep working behind redirects until cutover.

## References

- #3156 — Sandbox plans leaked into the generic picker (the bug that surfaced "where do I edit plans?")
- Existing BSS shell: `products/catalyst/bootstrap/ui/src/pages/sovereign/bss/{BssLandingPage,BssSectionShell}.tsx`
- Existing admin surfaces: `products/catalyst/bootstrap/ui/src/pages/admin/{rbac,user-access,compliance,parent-domains,blueprints}/`
- Plan CRUD endpoints: `core/services/catalog/handlers/routes.go:33-35`
