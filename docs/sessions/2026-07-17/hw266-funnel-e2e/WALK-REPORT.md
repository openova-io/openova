# hw266 — Funnel E2E validate-only walk (#5104 + #5123)

**Env**: hw266, dep `b85cb3b3a565893a`, FQDN `hw266.omani.works` (cutoverComplete=true today;
region-A app plane healthy; region-B/cnpg-pair DR separately broken per #5178 — NOT touched).
**Date**: 2026-07-17. **Walk type**: real customer onboarding funnel (Playwright browser) +
kubectl / HTTP evidence. **Org created**: slug `store266`, name "Store 266", owner
`owner@store266.omani.homes`, `store266.omani.homes`, **S plan (host tier)**, single-region BCP,
1 app (WordPress). Voucher `HW266FUNNELE2E` (50 OMR) minted via the owner console **BSS →
Billing → Vouchers** and redeemed at checkout (credit covered the order, Due now OMR 0.000).

## Result

| Row | Verdict | Evidence |
|---|---|---|
| **#5104** — funnel-purchased app actually DEPLOYS | **PASS** | `kubectl -n store266 get deploy,pods`: `wordpress` 1/1 Running + `mysql` 1/1 Running (raw Deployments — NO Application CR, NO HelmRelease → pure funnel Deployment-shaped purchase). Delivery unit `flux-system/catalyst-tenant-store266-apps` **Ready=True** (Applied revision `main@9c5d980d`) — the cart-install kustomization committed + reconciled, **not** wedged on `namespaces "store266" not found` (the #5104 bug is fixed for the host-tier facet: CNP correctly excluded from the apps tree). Live: `https://wordpress.store266.omani.homes/` → **HTTP 302 → /wp-admin/install.php** (fresh WP installer serving). See `5104-kubectl-evidence.txt`, screenshots 01–07. |
| **#5123** — per-Org console lists the funnel purchase | **PASS** | `GET https://console.store266.omani.homes/api/v1/org/applications` (org member session `catalyst_session` + `X-Tenant-Host`) → **HTTP 200** with the purchased WordPress projected: `{id:wordpress, slug:wordpress, status:"installed", environment:"dev", externalURL:"https://wordpress.store266.omani.homes", instance:true, blueprint:"bp-wordpress"}`. Per-Org **Apps grid renders the WordPress card as INSTALLED** with an Open (silent-SSO) button. This is the #5170 funnel-purchase projection pass joining on the `catalyst.openova.io/component=per-org-app-route` HTTPRoute. See `5123-org-applications-response.json`, screenshot 07. |

The purchase's per-app HTTPRoute `store266/app-wordpress` (host `wordpress.store266.omani.homes`,
labels `app=wordpress`, `catalyst.openova.io/component=per-org-app-route`,
`openova.io/tenant=store266`) is the exact artifact #5123's projection keys on — confirmed present.

## Render-surface regression sanity check (owner console, no deep-walk)

| Surface | Result |
|---|---|
| Dashboard | Renders (treemap). Sovereign status badge "Degraded/Converging" — expected (region-B DR #5178). |
| Organizations directory | Renders. New **Store 266** customer Org appears: kind=customer, tier=org, billing=real, isolation=namespace, **Active**. Showback per-app table renders. |
| Cloud view | Renders (topology graph: Cloud 1 · Region 2 · Cluster 2 · NodePool 4 · WorkerNode 8 · LB 2 · 18 nodes / 10 edges). |
| Jobs | Renders (populated cron/task/reconciler job table). |
| Topology | = Cloud graph, renders. |

**No NEW regression found.** Only pre-existing, documented noise observed:
- **#5123 Facet B (known)**: on the per-Org member console every page load fires owner-scoped
  `GET /api/v1/deployments/{id}` → **403** and `GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries`
  → **403** (console errors in the browser log). Same ticket, non-blocking — Apps grid renders fine regardless.
- **Minor cosmetic**: the per-Org Apps page search placeholder reads "Search your **0** apps…" while
  1 instance card (WordPress INSTALLED) renders and the API returns 1 app — a stale/decoupled counter,
  not a functional defect.
- `generatedAt` in the org/applications response is the embedded catalog build time
  (`2026-06-25T...`), unrelated to the live funnel-app projection (which is derived from live HTTPRoutes).

## Method notes (for reproducibility)
- Owner console session via mothership single-use handover (`GET /sovereign/api/v1/deployments/{id}` → `.handoverURL`).
- Voucher minted through the BSS Vouchers UI (code must be ≥12 chars).
- Checkout PIN read from Valkey (`kubectl -n valkey exec valkey-primary-0 -c valkey -- valkey-cli GET magic:<email>`) — mail also delivered (send-pin HTTP 200); zero-click landed the customer on the per-Org console.
