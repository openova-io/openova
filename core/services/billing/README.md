# billing — the marketplace's order / checkout service

`org-billing` is the Organization service behind `POST /billing/checkout`
(and its `/billing/purchase` alias): it prices the cart, redeems a voucher,
settles the order from credit or opens a Stripe Checkout Session, records
the order row and the subscription, and emits `order.placed`. It also owns
vouchers (`/billing/vouchers/*`), the credit balance, Stripe webhooks, the
NewAPI metering ledger and the settlement-launch reconciler (#6242).

One Go module (`go test ./...` from this directory); `handlers/` is the HTTP
surface, `store/` the PostgreSQL store and migrations, `packages/` the BSS
price-book client. Deployed by `products/catalyst/chart/templates/org-services/billing.yaml`
into namespace `org-services`, Service `billing:8085`; the gateway
(`core/services/gateway`) exposes it under `/api/billing/*`.

## Pricing — where the money on an order comes from (#6971)

An order has exactly ONE price source, recorded on the row
(`orders.price_source`) and returned on the response, so the receipt and the
later reconciliation with BSS can be matched line by line.

### The authoritative document

When the checkout body carries a `package_sku` (`plan.m` — the SME storefront
chose a package on the S/M/L/XL comparison table), the order is priced from
the **Catalyst BSS public price book**:

    GET <CHARGEBACK_PUBLIC_URL>/api/v1/public/packages

the same public, sessionless document the storefront rendered the table from
(`core/marketplace/src/lib/packages.ts`). `packages/` fetches it, validates
the contract and caches it for 60 s. Money in the document is a string at the
currency's minor unit (`"9.000"`, OMR, three decimals); it is parsed with
integer arithmetic into baisa — no float ever touches a price, and a string
that is not exactly representable (`"1.5001"`) is refused.

| Line | Priced from |
|---|---|
| plan | the package's `price_month` |
| `addon.*` SKU, cell `optional` on this package | that cell's `price_month` |
| `addon.*` SKU, cell `included` on this package | 0 — the line is kept and marked `redundant` (not an error) |
| `addon.*` SKU, cell `not_offered` / no cell / SKU unknown to the book | **refused, HTTP 422** naming the add-on and the package |
| catalog add-on id (anything not `addon.*`) | `/catalog/addons` `price_omr`, as before |
| BCP topology `active-hot-standby` | billing's own surcharge (`activeHotStandbyPriceOMR`, #5104) |

`price_source` is then `bss:<price_book>@<prices_as_of>`
(`bss:OpenOva plans@2026-09-11`) and `package_sku` is stored on the order.

### The fallback — and what is NOT a fallback

- **No `package_sku` on the request** (the legacy plan deck, older clients):
  the catalog path exactly as before — plan from `/catalog/plans`
  (`price_omr`; an unknown plan is a 400), add-ons from `/catalog/addons`,
  the topology surcharge. `price_source = catalog`. BSS is never consulted.
- **`package_sku` present but the price book cannot be read** — BSS down, a
  non-200, a body that is not the contract, a currency other than OMR, or
  `CHARGEBACK_PUBLIC_URL` set empty: **HTTP 503 `prices unavailable`**. The
  order is never silently priced from the catalog instead; the cached copy is
  not served past its TTL either, so a dead BSS is visible within a minute.

### Answers

| Case | Status | Body |
|---|---|---|
| priced | 200 | checkout response + `price_source`, `package_sku`, `plan_amount_baisa`, `lines[{sku,name,amount_baisa,redundant}]`, `topology_amount_baisa`, `amount_baisa`, `amount_omr` |
| add-on not offered on the package / unknown add-on / unknown package | 422 | `{"error": "...names both...", "addon_sku": "...", "package_sku": "..."}` |
| price book unreadable or BSS pricing off, request carries `package_sku` | 503 | `{"error": "prices unavailable — ..."}` |
| catalog unreachable / unknown plan (catalog path) | 400 | `{"error": "failed to compute order total: ..."}` |
| overage fields break a rule (see "Overage mode" below) | 400 | `{"error": "<sentence>"}` |

Pricing runs BEFORE the voucher is redeemed and before any row is written, so
a 422 or a 503 burns no redemption slot and leaves no order behind.

### `POST /billing/quote`

Prices the same body without creating anything. Public (no JWT; listed in
`main.go` `publicBillingPaths` and the gateway route table) because the
marketplace `/review` page renders its total before the customer signs in.
`/review` and `/checkout` take every figure from this answer
(`core/marketplace/src/lib/quote.ts`) and sum no money themselves — the
table, the review, the checkout and the receipt therefore show one number.

### Amounts on the row

`orders.amount_baisa` is the exact total. `orders.amount_omr` and the credit
ledger (`credit_ledger.amount_omr`, whole OMR per row) predate sub-OMR
prices; a BSS total can be fractional (plan M 9.000 + backup 1.500 =
10.500), so the whole-OMR view is rounded UP (`pricedOrder.WholeOMR`) — a
credit balance that covers the order in whole OMR can never fall short of it
in baisa, and the credit comparison itself is done in baisa. Carrying the
ledger to baisa precision is the open seam; it is not changed here.

### Overage mode — capped or grow

A package is a prepaid minimum commitment. Each order chooses what happens
beyond it (founder model, 2026-10-10):

- **`capped`** (default) — nothing is billed beyond the package.
- **`grow`** — the Organization's quota is raised to a ceiling, and usage above
  the package allowance is billed in arrears at the package's pay-per-use
  overage rates. Nothing extra is charged upfront.

`POST /billing/quote` and `POST /billing/checkout` accept three optional
fields, all only on a package order:

| Field | Shape | Rule |
|---|---|---|
| `overage_mode` | `"capped"` \| `"grow"` | empty = capped; anything else → 400 |
| `grow_ceiling` | `{vcpu, memory_gb, disk_gb, bandwidth_mbps}` numbers | only with grow; a value omitted or 0 is the package's grow ceiling |
| `spend_limit_month` | money string, e.g. `"25.000"` | only with grow; more than zero, at most 3 decimals |

They are checked against the price book document:

- grow, a ceiling or a spend limit on an order without `package_sku` → 400
  "grow mode needs a package";
- grow on a package whose document has no `grow` block → 400 ("the M package
  does not offer grow mode");
- each ceiling value must be at least the package headline (`includes`) and at
  most the package's grow ceiling → 400 naming the dimension, the value and
  the range. A headline the document does not state leaves only the upper
  bound;
- DR `active-hot-standby` on a package whose `dr_topology` cell is
  `grow_only` (S/M/L): allowed in grow mode at 0 upfront (the standby's
  resources are billed as overage), refused with 422 in capped mode. XL
  includes it at 0 as before; a v1 document keeps billing's surcharge.

The quote (and the checkout response) echoes `overage_mode` (always),
`grow_ceiling` (the resolved ceiling, all four values, omitted when capped),
`spend_limit_month` (omitted when unset) and `overage_rates` — the chosen
package's `grow.overage_rates` as `{key, sku, unit, price_month}`, echoed in
both modes when the package offers grow, so the storefront can show what grow
would cost.

The order row persists `overage_mode` (TEXT, default `'capped'` — the backfill
for every older row), `grow_ceiling` (JSONB, NULL when capped) and
`spend_limit_month` (TEXT, NULL when unset). `order.placed` carries the same
three keys. The settlement launch body hands them to core/services/tenant:
`overage_mode` on every package order (so `capped` is explicit), the other two
only in grow mode; the Organization CR receives them as
`spec.commerce.overageMode` / `growCeiling` / `spendLimitMonth`.

### Configuration

| Env | Default | Source |
|---|---|---|
| `CHARGEBACK_PUBLIC_URL` | `http://chargeback.chargeback.svc.cluster.local:8080` — the Sovereign placement of bp-chargeback (bootstrap-kit slot 13f: release `chargeback`, namespace `chargeback`, `service.port` 8080) | chart value `orgServices.billing.chargebackPublicURL` |
| `CATALOG_URL` | `http://catalog.org-services.svc.cluster.local:8082` | `org-services-config` ConfigMap |

The price-book client reads with a 5 s timeout and caches one document for
60 s per pod.
