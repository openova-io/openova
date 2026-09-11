# chargeback — standalone chargeback service (`bp-chargeback`)

**What it is.** A standalone application that meters cloud usage per customer,
rates it against a price book and produces statements. It owns its customers,
its Postgres database, its API and its UI. It is the implementation of
[ADR-0014](../../docs/adr/0014-chargeback-usage-ledger-and-split-deployment.md)
(EPIC #6723): the usage ledger is the measurement primitive, `Customer` is an
app-internal object, and OpenOva is one adapter among others.

**Role in Catalyst.** Application Blueprint. It runs on a Sovereign as the BSS
chargeback component, as a per-Organization Application, or on bare Kubernetes
as an operator's central instance (profile `operator-central`). It has no hard
dependency on Catalyst CRDs; the cloud collector needs only a read-only IAM
user's AK/SK per customer project.

**Canonical docs.** [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md),
[`docs/DOD.md`](../../docs/DOD.md) §J13 (Billing & quotas),
[`docs/GLOSSARY.md`](../../docs/GLOSSARY.md). Inside this application the buyer
is a *customer*; when a customer is a synced Organization the term Organization
is used in user-facing text.

## OpenOva adapter (lane D — ADR-0014 D2 case 1)

Built into this same binary (`internal/adapter/openova`), running only when
`PROFILE=sovereign` and in-cluster Kubernetes configuration is available
(`ADAPTER_ENABLED` overrides in either direction; nothing runs without a
cluster). The `operator-central` profile never runs any of it — the engine
keeps its D5 invariant of zero Catalyst dependencies.

**Organization → Customer sync.** A dynamic-client list+watch on
`organizations.orgs.openova.io/v1` (absence of the CRD is logged once and
idled on, probing every 5 minutes). Each Organization upserts one customer:
slug = Org slug, `kind=organization`, `org_slug` set, `admin_email` from the
owner roster (`role: owner` preferred, blank-pending when the roster is
empty), `billing_mode` from `spec.billingMode` (real→real,
chargeback→chargeback, showback→showback), `plan_slug` from `spec.planSlug`
(lower-cased; empty → `s`, the org-controller's default; the Sovereign's own
`kind: internal` Organization gets none), status active. GitOps-declared
`spec.costSources[]` (see the Organization CRD,
`products/catalyst/chart/crds/organization.yaml`) become `cost_sources`
rows; a `credentialRef` is resolved read-only from the named Secret in the
Organization's host namespace (= slug) — value either JSON
`{"accessKey","secretKey"}` or `ACCESSKEY:SECRETKEY` — sealed with
`APP_ENCRYPTION_KEY`, linked, and verified through the Huawei verifier.
Deleting an Organization SUSPENDS its customer; nothing is ever deleted —
statements and the usage ledger are billing history.

**Platform collector.** Shared informers on namespaces, pods and PVCs; the
join key is the `openova.io/organization` namespace label (the same key the
sovereign-admin dashboard's `buildPodRows` uses;
`catalyst.openova.io/organization` accepted as the legacy spelling).
Event-driven with an hourly reconciliation pass (ADR-0014 D3a) and a
debounced emit for touched Organizations. Usage windows are sliced by the
same `internal/window` math as the cloud collector — hour-bounded,
idempotent per `(source, resource, sku, window_start)`:

| SKU | Unit | Quantity per hour |
|---|---|---|
| `k8s.vcpu` | vcpu-hour | sum of the pod's container CPU requests (cores) |
| `k8s.mem_gb` | gib-hour | sum of the pod's container memory requests (GiB) |
| `k8s.pvc_gb` | gb-hour | PVC capacity (GB), joined to the Organization by namespace |
| `plan.<slug>` | plan-hour | 1 while the Organization is active on plan `s`/`m`/`l`/`xl` (`flexi` = pay per use, no line); `resource_kind=plan`, labels `{name, plan}` |

Every row above lands on the Organization's `openova-org` **platform source**,
which the Organization sync puts on one of the two platform-scoped books below.
The Sovereign's own footprint (namespaces with no Organization label) lands on
the **internal** `openova-platform` source instead: no customer, never billed,
read only by the allocation report.

One `cost_source` of kind `openova-org` is auto-created per Organization;
records land on it, source kind `openova-org` (the request is the entitlement
the plan quota enforces, so the request is what is billed).

**Two platform billing shapes, never both** (DESIGN.md §2.9 / §2.9a). OrgSync
creates both books when absent, never re-creates or re-prices them, and points
each Organization's source at the one its plan calls for — re-pointing when the
plan changes between flexi and a sized plan, and never overruling a book an
operator assigned. The two books price **disjoint** SKUs, so nothing is billed
twice.

**"OpenOva plans"** — the committed plans (`s`/`m`/`l`/`xl`). OMR, divisor 8760;
annual = monthly × 12, so a plan-hour is monthly / 730:

| SKU | Unit | OMR/month | Annual | Unit price |
|---|---|---|---|---|
| `plan.s` | plan-hour | 5 | 60 | `0.00684932` |
| `plan.m` | plan-hour | 9 | 108 | `0.01232877` |
| `plan.l` | plan-hour | 16 | 192 | `0.02191781` |
| `plan.xl` | plan-hour | 30 | 360 | `0.04109589` |

`k8s.vcpu` / `k8s.mem_gb` / `k8s.pvc_gb` are deliberately unpriced here: under a
plan they are the allocation basis, not the bill.

**"Organization PAYG"** — pay per use, for the uncapped `flexi` plan, which has
no bundle to sell and carries no `plan.<slug>` line at all. Rates are derived
from the plan ladder: per unit of (1 vCPU + 2 GiB) the sized plans cost 2.50 /
2.25 / 2.00 / 1.875 OMR per month, and pay per use — which commits to nothing —
is the entry rung plus 10 %, i.e. **2.75 per unit-month**, split 2.00 per vCPU
and 0.375 per GiB. Storage is set against cost instead, ~31 % above the
0.00022831 OMR per GB-hour the cloud charges for the SSD underneath:

| SKU | Unit | OMR/month | Annual | Unit price |
|---|---|---|---|---|
| `k8s.vcpu` | vcpu-hour | 2.000 per vCPU | 24.000 | `0.00273973` |
| `k8s.mem_gb` | gib-hour | 0.375 per GiB | 4.500 | `0.00051370` |
| `k8s.pvc_gb` | gb-hour | 0.219 per GB | 2.628 | `0.00030000` |

A flexi Organization on 4 vCPU + 8 GiB around the clock pays ≈ 11 OMR/month
against 9 for the committed M plan of the same shape, and near zero while idle.
The full derivation is written onto each book's `description`, where the
operator can read it before changing a rate.

**Billing hook (D6).** Off unless `BILLING_HOOK_URL` is set. After
`POST /statements/{id}/issue` for a customer with `kind=organization` and
`billing_mode=real`, the statement total is posted to
`<BILLING_HOOK_URL>/billing/metering/record`
(`core/services/billing/handlers/metering.go`) as a negative micro-OMR
amount with `metadata.request_id` = the statement id — billing's
`external_ref` — so a re-issue can never debit twice (billing answers
`duplicate: true`). Auth is `Authorization: Bearer $BILLING_HOOK_TOKEN`
(superadmin, per the metering endpoint's contract). A hook failure leaves
the statement issued; issuing is idempotent, so re-POSTing issue repeats the
hook.

**RBAC the chart needs (least privilege).** The adapter is read-only against
the cluster:

| Resource | Verbs | Scope |
|---|---|---|
| `organizations.orgs.openova.io` | get, list, watch | ClusterRole (the CRD is cluster-scoped) |
| `namespaces`, `pods`, `persistentvolumeclaims` | get, list, watch | ClusterRole (Organization namespaces are discovered by label) |
| `secrets` | get | ONLY the credential Secrets `costSources[].credentialRef` names, in Organization host namespaces — grant per-Organization Roles on the named Secrets (RBAC `resourceNames` works for `get`), never a cluster-wide secrets read |

The bp-chargeback chart today ships a zero-RBAC ServiceAccount; the chart
change adding this ClusterRole + the per-Organization Secret Roles (plus the
`ADAPTER_ENABLED` / `BILLING_HOOK_*` env wiring and the image/appVersion
roll) is the follow-up chart release — until it lands, the adapter decides
itself off in-cluster because the API calls are forbidden, and logs why.

## Architecture pointers (one line each; the tables live in ADR-0014)

| Concern | Where it is decided |
|---|---|
| Composability + left-menu mapping (an application plugs into the console) | ADR-0014 D9/D9a + `Blueprint.spec.consoleUI` + Settings → Menu (PR #6724) |
| Entity types per deployment mode | ADR-0014 D2a |
| Billing source-of-truth per customer type (incl. dual-registered) | ADR-0014 D2b |
| Customer intake / import without duplication | ADR-0014 D8a |
| Collection modes (event-driven vs pulled change log vs sampled) | ADR-0014 D3a |
| Profiles `sovereign` / `operator-central` | ADR-0014 D10 |

## Lanes

| Lane | Content | Status |
|---|---|---|
| A (this directory) | Go service: domain + migrations, API, PIN auth, cloud collector (Huawei Cloud Stack Online / Kom4DC), rating, statements, image | shipped |
| B | React + TypeScript UI built into `ui/dist` and embedded by this binary | shipped |
| D | OpenOva adapter (`Customer ← Organization` sync, platform collector, billing hook) | shipped |

## Layout

```
products/chargeback/
├── cmd/chargeback/main.go        entry point: config → migrate → collector → HTTP
├── internal/
│   ├── adapter/openova/          OpenOva adapter (lane D): Organization → Customer sync,
│   │                             platform collector (pods/PVCs → k8s.* usage), billing hook (D6)
│   ├── api/                      /api/v1 handlers, session + PIN auth, authorization, UI serving
│   ├── capacity/                 capacity families + SKU footprints derived from SKU names (DESIGN.md §11); pure
│   ├── collector/huawei/         SDK-HMAC-SHA256 signer, gateway client, ECS/EVS/EIP/ELB/NAT listers,
│   │                             CTS change-log poller, CES sampler, kind → SKU mapping
│   ├── config/                   environment → Config
│   ├── window/                   the shared hour-slice window math both collectors bill by
│   ├── crypto/                   envelope encryption (AES-256-GCM, per-secret DEK wrapped by APP_ENCRYPTION_KEY)
│   ├── mail/                     SMTP sender, or log sender when SMTP_HOST is unset
│   ├── metrics/                  dependency-free Prometheus text registry
│   ├── rating/                   price book CSV import, exact decimal math, statement run
│   ├── store/                    Postgres store: embedded migrations, scoped queries
│   └── testdb/                   integration-test database helper
├── ui/embed.go + ui/dist/        embedded front-end (placeholder until lane B lands)
├── Containerfile                 multi-stage, distroless static, uid 65532, read-only rootfs
└── Makefile                      build / vet / test / integration / image / dev-db
```

## Domain

`customers` · `customer_users` · `cost_sources` · `credentials` (secret
envelope-encrypted, never returned by the API, never logged) ·
`resource_inventory` · `usage_records` (append-only facts, idempotent on
`(source, resource, sku, window_start)`) · `price_books` · `price_items` ·
`statements` · `rated_lines` · `invites` · `audit_log` · `sessions` · `pins`.
Migrations are Go-embedded and applied at startup (`store.Migrate`), tracked in
`schema_migrations`.

Compared with the build spec, `price_items` carries one extra nullable column,
`annual_price`, so that changing a price book's `annual_divisor` recomputes the
derived unit prices instead of silently leaving them stale.

## SKUs (Huawei, allocation-based, hourly)

| SKU | Unit | Quantity per hour |
|---|---|---|
| `ecs.<flavor_name>` | instance-hour | 1 (labels: status, flavor, vcpus, ram_mb) |
| `evs.ssd.gb` / `evs.hdd.gb` | gb-hour | volume size (SSD/GP/ESSD → ssd; SAS/SATA → hdd) |
| `eip` | hour | 1 |
| `eip.bandwidth_mbps` | mbps-hour | reserved bandwidth size (see below) |
| `eip.traffic_gb` | gb | outbound GB in the hour, for a traffic-billed address |
| `elb` | hour | 1 |
| `nat.<spec>` | hour | 1 |
| `ecs.cpu_util` | pct-hour-avg | hourly CES average (informational, never rated) |
| `eip.traffic_gb.observed` | gb-hour-out | outbound GB of a bandwidth-billed address (informational, never rated) |

An address bills **either** its reservation **or** its traffic, never both
(DESIGN.md §8.2). The charge mode is read from the address's bandwidth object:
a traffic-billed address emits no `eip.bandwidth_mbps` at all, and several
addresses sharing one pipe emit it once against the pipe (kind `bandwidth`,
keyed by the bandwidth id) rather than once each. An address whose gateway
reports no charge mode keeps billing its reserved size exactly as before.
`eip.traffic_gb` ships **unpriced** — no traffic price is invented here, so it
appears as unpriced usage until the operator enters the rate their contract
carries.

`unit_price = annual_price / annual_divisor` (default 8760). Stopped ECS
instances are rated per `price_books.bill_stopped`: `compute` (billed like
running), `storage-only` (no instance charge while stopped), `none` (neither the
instance nor the volumes attached to it while stopped).

## Collector

Every `COLLECT_INTERVAL` (15m) per verified source: list ECS, EVS, EIP (with the
VPC bandwidths that carry each address's billing shape, and one resource per
shared pipe), ELB and NAT (paginated, 15s per call), upsert `resource_inventory` (first_seen /
last_seen / deleted_at, status and flavor transitions in `attrs`), then recompute
usage for every touched UTC hour since the previous tick. Each record is one
contiguous single-status interval inside one hour, so a re-run over the same
hour updates the same row. The first pass backfills from the customer's
`start_date` (at most 31 days).

Every `CTS_POLL_INTERVAL` (5m): read `cts` traces (`trace_type=system`) and
apply `create*` / `delete*` / `resize*` / `stop*` / `start*` operations as exact
boundaries and transitions of the affected resource, then recompute the hours
from the event; `raw_ref` carries the trace id.

Hourly: `ces` `cpu_util` per ECS → `ecs.cpu_util`, and `ces` `SYS.VPC`
`up_stream` (outbound traffic, `filter=sum`, dimensioned by `bandwidth_id`) per
Elastic IP or shared pipe → `eip.traffic_gb` when the cloud bills that address
by traffic, `eip.traffic_gb.observed` when it bills the reservation.

Verification (activation): one signed `GET ecs /v1/{pid}/cloudservers/detail?limit=1`.
`2xx` ⇒ verified; `401`/`403` ⇒ failed with the gateway error code
(`APIGW.0301`, `EPS.0003`, …) in `last_error`; `404 APIGW.0101` ⇒ failed, API not
published.

**Per-source isolation.** Every tick walks the collectable sources one by one,
each step recover-guarded: one source's failure (gateway error, store error,
even a panic) marks only that source — `last_error` set, per-source exponential
backoff (≤ 6h) — and the loop continues with the next source. The tick result is
`ok` when every source succeeded and `partial` otherwise
(`chargeback_collect_ticks_total{step,result}`); per-source outcomes are counted
in `chargeback_collect_sources_total{step,result="ok|error|skipped"}`. A
credential that cannot be decrypted with the current `APP_ENCRYPTION_KEY` flips
its source to `failed` immediately and is **not retried every tick** — the
source rejoins collection only after a new credential is entered via
`POST /sources/{id}/credential`.

## API (`/api/v1`, JSON, cookie `cb_session` HttpOnly SameSite=Lax)

**Sign-in.** On a Sovereign the identity arrives from the SSO gate
(oauth2-proxy → Keycloak) on `TRUSTED_FORWARD_AUTH_HEADER` (`X-Forwarded-Email`),
with the user's directory groups on `TRUSTED_FORWARD_GROUPS_HEADER`
(`X-Forwarded-Groups`); standalone, a one-time PIN mailed to the address
(`POST /auth/pin/request` → `POST /auth/pin/verify` → `cb_session` cookie).
Either way the session's bindings are resolved on **every** request.

**Access (DESIGN.md §10).** Two scope kinds — `sovereign` and
`customer:<id>` — ten permissions, six roles that are fixed permission
bundles: `sovereign-admin` (everything), `billing-operator` (rating,
customers, issuing, collecting, capacity, audit — no settings, no access changes),
`finance-viewer` (read + export only), `customer-owner` (own costs and
invoices, top-up, own users / PO reference / tax registration),
**Access (DESIGN.md §10, §11.5).** Three scope kinds — `sovereign`,
`partner:<id>` and `customer:<id>` — eleven permissions, eight roles that are
fixed permission bundles: `sovereign-admin` (everything), `billing-operator`
(rating, customers, partners, issuing, collecting, audit — no settings, no
access changes), `finance-viewer` (read + export only), `partner-owner` (its
customers' costs and statements, its own account and margin, its retail rule
and its users), `partner-viewer` (those reads only), `customer-owner` (own
costs and invoices, top-up, own users / PO reference / tax registration),
`customer-billing` (own costs, top-up), `customer-viewer` (own costs). A
binding comes from `OPERATOR_EMAILS` (implicit `sovereign-admin`), from
`role_bindings` (the access API, the customer's Users tab, the customer's
`admin_email`, the partner's Users tab, the partner's `contact_email`, the
Organization sync), or from a directory group mapped in
`group_role_mappings`. A partner binding expands to the customers assigned to
that partner plus the partner's own party. A Sovereign permission covers every customer; a
customer permission covers that customer only. Other customers' ids answer
`404`; a missing permission answers `403` naming it. `customer_users` remains
as a view for older readers; the legacy `role` key on `/auth/me`
(`operator` / `customer-admin` / `customer-viewer`) still describes the
highest-power binding.

| Area | Endpoints |
|---|---|
| Auth | `POST /auth/pin/request` · `POST /auth/pin/verify` · `POST /auth/logout` · `GET /auth/me` = `GET /me` (email, `role`, `roles[]`, `permissions{scope: [...]}`, `scopes[]`) |
| Access (`settings.manage`) | `GET /access/roles` (any principal) · `GET/POST /access/bindings` · `DELETE /access/bindings/{id}` · `GET/PUT /access/group-mappings` — every change audited `access.binding` / `access.mapping` |
| Customers | `GET/POST /customers` (no price book — see below) · `POST /customers/import` (multipart CSV, raw CSV or JSON array) · `GET/PATCH /customers/{id}` · `POST /customers/{id}/invite` · `GET/POST /customers/{id}/users` · `DELETE /customers/{id}/users/{email}` · `GET /customers/{id}/audit` |
| Invites (public by token) | `GET /invites/{token}` · `POST /invites/{token}/activate` |
| Sources | `GET /sources[?internal=true]` (operator-wide) · `GET/POST /customers/{id}/sources` · `GET/PATCH /customers/{id}/sources/{sid}` · `GET/PATCH /sources/{id}` (region, project_id, scope_token, domain_id, **price_book_id**) · `POST /sources/{id}/credential` (rotate + verify) · `POST /sources/{id}/verify` · `DELETE /sources/{id}` |
| Usage | `GET /customers/{id}/usage?from&to&group_by=sku\|resource\|day` · `GET /customers/{id}/inventory` |
| Price books | `GET/POST /pricebooks` (**`scope`**: cloud \| platform) · `GET /pricebooks/template.csv` · `GET/PUT /pricebooks/{id}` · `GET /pricebooks/{id}/coverage` · `PUT /pricebooks/{id}/items` · `POST /pricebooks/{id}/import` · **`PUT /pricebooks/{id}/public`** `{public}` (`rating.manage`; one public book at a time — a second is `409`, a platform book `400`) |
| Public calculator (**unauthenticated**, rate-limited, DESIGN.md §11) | `GET /public/catalog` (the designated public list book + the catalog plans + the pay-per-use rates + regions + tax rate) · `POST /public/estimates` (`?preview=1` prices without saving) · `GET /public/estimates/{id}` (the shareable link). List prices only — never a negotiated book, a discount or a partner rate; no session is read and no cookie is set |
| Leads | `GET /leads[?limit]` (`customers.manage`) — the estimates a prospect left an address on, newest first |
| Price books | `GET/POST /pricebooks` (**`scope`**: cloud \| platform) · `GET /pricebooks/template.csv` · `GET/PUT /pricebooks/{id}` · `GET /pricebooks/{id}/coverage` · `PUT /pricebooks/{id}/items` · `POST /pricebooks/{id}/import` · items carry the rating shapes of DESIGN.md §15 — `tier_mode` (`graduated` \| `all_units`) + `tiers[{up_to, price}]`, `allowance` and `allowance_rollover` — on `POST /pricebooks/{id}/items` and `PATCH /pricebooks/{id}/items/{sku}`; an out-of-order ladder is refused with the reason |
| Partners (`partners.manage`; a partner owner holds `partner.self.manage` on its own) | `GET/POST /partners` · `GET/PATCH /partners/{id}` · `GET/POST /partners/tiers` · `PUT /partners/tiers/{id}/discounts` · `PUT /partners/{id}/retail-rule` (re-derives; the response lists the below-buy lines) · `GET /partners/{id}/retail-book` · `GET /partners/{id}/customers` · `GET /partners/{id}/statements` · `GET /partners/{id}/margin?period=` · `GET /partners/{id}/account` · `GET/POST /partners/{id}/users` · `DELETE /partners/{id}/users/{email}` · `PATCH /customers/{id} {partner_id}` |
| Statements | `POST /statements/run {period, customer_id?}` · `GET /statements[?period&customer_id]` · `GET /customers/{id}/statements` · `GET /statements/{id}` · `GET /statements/{id}.csv` · `POST /statements/{id}/issue` |
| Contracts (DESIGN.md §15; writes `customers.manage`, reads `metering.read` at the scope, SLA credits `billing.issue`, every write audited `contract.*`) | `GET/POST /contracts[?customer_id&status]` · `GET /contracts/renewals[?on=YYYY-MM-DD]` (the notice window) · `GET/PATCH/DELETE /contracts/{id}` · `PUT /contracts/{id}/items` (committed-use and allowance lines; the list sent is the whole list) · `POST /contracts/{id}/sla-credit {statement_id, pct, measured_availability, reason}` (a real credit note, numbered and posted to the ledger) · `GET /customers/{id}/contracts` |
| Operator | `GET /overview` |
| Capacity (DESIGN.md §11; reads `metering.read` at the Sovereign, writes `capacity.manage`, every write audited `capacity.*`) | `GET /capacity/overview[?region=]` (regions → zones → pools with total / reserved / consumed / available / utilisation / exhaustion, SKU headroom with the binding family, `unmapped_skus`, `unmapped_regions`) · `GET/POST /capacity/regions` · `DELETE /capacity/regions/{id}` · `POST /capacity/regions/{id}/zones` · `DELETE /capacity/zones/{id}` · `GET /capacity/zones/{id}/pools` (+ total history) · `PUT /capacity/pools/{id} {total, note}` · `GET /capacity/footprints` · `PUT /capacity/footprints/{sku} {families}` · `GET/PUT /capacity/caps {zone_id, sku, total}` |
| Ops (root) | `GET /healthz` · `GET /readyz` · `GET /metrics` |

**Capacity** (DESIGN.md §11, founder requirement 2026-09-11). Console menu
group **Plan → Capacity**. A region holds availability zones; a zone holds
one pool per resource family (`vcpu`, `memory_gib`, `block_ssd_gib`,
`block_hdd_gib`, `object_gib`, `eip_addresses`, `bandwidth_mbps`) whose
**total** the sovereign-admin enters — static-first; a capacity collector
fills it later through the same `PUT /capacity/pools/{id}` shape with its own
`source`. **Consumed** is not entered: it is the latest complete hour of the
usage ledger multiplied through the **SKU footprints** (how much of each
family one unit of a SKU consumes — `ecs.m7n.2xlarge.8` is 8 vCPU and 64
GiB, read from the flavour name; `evs.ssd.gb` is 1 GiB of block SSD), zone
by the inventory's `availability_zone`, else the region's default zone
(reported as `zone_unknown`). `available = total − reserved − consumed`,
never below 0 (`clamped`, `overcommit`); `reserved` is 0 until proposals
fill it. Per SKU the page shows **headroom** — the fewest more units any of
its families allows, with the binding family — and per pool **time to
exhaustion** = available ÷ the 7-day run-rate trend of consumed
(`rating.RunRate`, the explorer's own arithmetic). A metered SKU with no
footprint is listed under `unmapped_skus` and counts against no pool; a
metered region the admin has not added is `unmapped_regions`.
**Partners — resellers and agents** (DESIGN.md §11, founder direction
2026-09-11). ONE list price per SKU, two independent discount steps off it,
both decided by the one discount engine and its combination rule: the
customer's discounts give the **customer net** (what the end customer pays),
the partner's **tier** gives the **partner buy** (what the partner pays us),
and **margin = net − buy** is derived per line and never entered — there is
no markup typed per SKU anywhere. `bill_to = partner` (resell) invoices the
partner a **wholesale** statement of its customers' lines at the buy price
and prices its customers from a **derived retail book** materialised from its
retail rule (read-only, re-derived on a list, tier or rule change, warning on
any line below buy); `bill_to = customer` (agent) invoices the end customer
at our books and credits the partner a **commission** statement — `net − buy`
— as a ledger credit on its account. A partner is a PARTY: it owns one
`customers` row (`party_kind = partner`) and so has a balance, invoices,
payments and collections through the existing ledger. A third scope kind,
`partner:<id>`, expands to the partner's customers plus its party; the roles
are `partner-owner` and `partner-viewer`. One `POST /statements/run` writes
the customer statements and the partner statements together.

**Contracts and commercial terms** (DESIGN.md §15, founder direction
2026-09-11). Console menu **Configure → Contracts**. A price-book item can
now carry an **allowance** (N units of the SKU included per billing period;
the excess at the item price, lapsing unless it says `rollover`), **volume
tiers** in the two industry modes — `graduated`, each band at its own price,
and `all_units`, the whole volume at the band the total reaches — and a
contract can carry **committed use** (a quantity for a term at a negotiated
rate; the committed quantity at that rate, the excess at list). A
**contract** holds the term, the auto-renewal and its notice period, a
monthly **minimum commitment** whose shortfall is invoiced as a named
`true-up` line, and its committed-use and allowance lines. **SLA credits**
(`POST /contracts/{id}/sla-credit`) go through the existing credit-note
machinery — same numbering, same allocation, same ledger entry — with the
percentage and the measured availability recorded on the note. **Renewal**
adds a step to the daily collections evaluator (`contracts_renewed` /
`contracts_expired`), not a scheduler of its own. All of it is computed in
`internal/rating` in one stated order — allowance, tiers, commitments,
discounts, true-up, tax — and then handed to the same discount engine and the
same partner waterfall, whose margin-is-buy-times-markup invariant is pinned
on a tiered line.

**Two layers, one book per source** (DESIGN.md §2, founder direction
2026-09-08). Every cost source belongs to the **cloud** layer (a cloud
project: `huawei-project`, `file`) or the **platform** layer (an Organization
on this Sovereign: `openova-org`), and the price book that rates it is
assigned **to the source**, not to the customer — its `scope` must equal the
source's `layer`, or the API answers 400 `price book scope X does not match
source layer Y`. A customer's statement is the sum of its sources, each rated
by its own book, and is issued in one currency (a customer whose sources use
books of different currencies is refused with 400).
`customers.price_book_id` is deprecated: `POST|PATCH /customers` decode the
key and ignore it, and the column is never written again.

A source may be **disabled** — decommissioned: the collector skips it and it
counts as neither verified nor live, but its collected history still rates,
in the explorer and on every statement already issued from it
(`PATCH /sources/{id} {"disabled": true}`, operator-only).

**The Sovereign is not a customer.** Its own platform footprint is metered on
one internal source (`kind=openova-platform`, `internal=true`, no customer),
which every customer-facing query excludes and only `GET /allocation` reads —
as the `platform-overhead` row of that report.

Money and quantities are emitted as exact JSON numbers taken from Postgres
`numeric` columns; the service never does money math in floating point.

A customer created by the operator starts `pending`; the first successful
source verification outside the invite flow (`POST /sources/{id}/verify` or a
credential entry that verifies) activates it, with an audit entry recording
`activated via source verification` — otherwise its verified sources would
never be collected. The customer detail and the source verify/rotate responses
carry `collecting: true|false` (customer active ∧ source verified — the exact
gate the collector applies), so the UI can say why nothing flows.

Customer import CSV columns: `slug,name,admin_email,region,project_ids(;-separated),price_book,billing_mode,start_date`.
`price_book` names the **cloud** book the row's projects are rated by — it is
assigned to each imported source, and a platform book there is rejected for
that row.
Price book CSV columns: `sku,unit,annual_price,description` (template at
`/api/v1/pricebooks/template.csv`; a `unit_price` column may be given instead of
`annual_price`).

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | `postgres://chargeback:chargeback@localhost:5432/chargeback?sslmode=disable` | Postgres DSN |
| `APP_ENCRYPTION_KEY` | required | base64 of 32 random bytes; wraps every stored secret (`make key`) |
| `OPERATOR_EMAILS` | empty | comma-separated operator sign-ins |
| `PUBLIC_URL` | `http://localhost:8080` | used in invite links and to mark the cookie Secure |
| `SMTP_HOST/PORT/USER/PASS/FROM` | unset | PIN + invite mail; unset ⇒ logged at info level (development mode) |
| `HUAWEI_ENDPOINT_TEMPLATE` | `https://%s.%s.kom4dc.nationalcloud.om` | `(service, region)` |
| `HUAWEI_INSECURE_TLS` | `true` | on-prem CA |
| `COLLECT_INTERVAL` / `CTS_POLL_INTERVAL` / `CES_INTERVAL` | `15m` / `5m` / `1h` | collector cadences |
| `COLLECTOR_ENABLED` | `true` | set `false` on API-only replicas |
| `PROFILE` | `sovereign` | `sovereign` or `operator-central`; surfaced in `/auth/me` and `/overview` |
| `ADAPTER_ENABLED` | auto | OpenOva adapter override: unset = on when `PROFILE=sovereign` AND in-cluster Kubernetes configuration exists; `true`/`false` force it |
| `BILLING_HOOK_URL` | unset | billing service base URL for the D6 statement hook; unset ⇒ hook off |
| `BILLING_HOOK_TOKEN` | unset | superadmin bearer token `POST /billing/metering/record` requires |
| `BILLING_HOOK_CALLBACK_SECRET` | unset | shared secret the billing service signs its payment callbacks with (`POST /api/v1/gateways/stripe/callback`, DESIGN.md §9.2); unset ⇒ every callback for the stripe gateway is refused. Chart: `adapter.billingHook.callbackSecret` names the Secret |
| `PLATFORM_API_URL` | unset | the Sovereign's sovereign-admin API, for suspend/resume at the platform (DESIGN.md §9.6: `POST /api/v1/internal/organizations/{slug}/suspend` / `resume`); unset ⇒ the Enforcer is a Nop and suspensions are recorded here only. Chart: `platformApi.url`; the Sovereign slot sets `http://catalyst-api.catalyst-system.svc.cluster.local:8080` |
| `PLATFORM_API_BEARER_FILE` | unset | file holding the bearer for those routes — the projected ServiceAccount token the chart mounts at `/var/run/secrets/platform-api/token` (audience `platformApi.tokenAudience`, empty = the apiserver default); re-read on every call because the kubelet rotates it. Wins over `PLATFORM_API_TOKEN`. Named without TOKEN/KEY/SECRET because the Sovereign's Kyverno `secret-not-in-env` policy flags any such name carrying a literal value; `PLATFORM_API_TOKEN_FILE` (the name through chart 0.1.32) is still read as a deprecated alias for one release, the new name winning when both are set |
| `PLATFORM_API_TOKEN` | unset | literal bearer for those routes when no file is mounted (a local run against a Sovereign) |
| `TRUSTED_FORWARD_AUTH_HEADER` | unset | the request header carrying the identity the Sovereign's SSO gate verified (`X-Forwarded-Email`); unset = the header is ignored entirely. Only safe when the gate owns the public hostname — the chart refuses `forwardAuth.header` together with `httpRoute.enabled` |
| `TRUSTED_FORWARD_GROUPS_HEADER` | `X-Forwarded-Groups` | the header carrying the identity's directory groups (comma-separated), each looked up in `group_role_mappings` (DESIGN.md §10). Honoured only while `TRUSTED_FORWARD_AUTH_HEADER` is set |
| `PUBLIC_CALCULATOR_ORIGINS` | empty | comma-separated origins allowed to call `/api/v1/public/*` cross-origin and to frame `/estimate` (the marketplace, a partner site); empty = same origin only and the page cannot be framed, `*` = any. Every other path keeps `X-Frame-Options: DENY` |
| `PUBLIC_CALCULATOR_RATE_PER_MINUTE` | `60` | per-client-address budget on the public calculator routes (token bucket, one minute's burst); beyond it the route answers `429` with `Retry-After` |
| `LISTEN_ADDR` | `:8080` | |

## Development

```
make test                        # go vet + unit tests (no database needed)
make dev-db                      # local Postgres in a container
make integration                 # unit + integration tests against CHARGEBACK_TEST_DATABASE_URL, one package at a time
make key                         # fresh APP_ENCRYPTION_KEY
APP_ENCRYPTION_KEY=... OPERATOR_EMAILS=you@example.com make run
make image                       # container image (build context = this directory)
```

Integration tests share one database and truncate it between tests, which is
why `make integration` passes `-p 1`.

## Synthetic history (showcase)

`cmd/seed-history` fills the database with a deterministic three-month trading
history — 1 June to 1 September 2026, hourly — for six showcase customers, so
the console can be demonstrated with a past instead of a blank explorer. Three
buy National Cloud resources (`file` sources on the National Cloud list) and
three are Organizations of this Sovereign on catalog plans (`openova-org`
sources on "OpenOva plans"):

| Customer | Layer | Story |
|---|---|---|
| Gulf Retail Group | cloud | steady e-commerce; worker pool 6 → 10 ECS on a weekly rhythm, EIP bandwidth on a daily traffic curve, a 14–16 July promo, EVS 2 → 3.5 TB, a bandwidth anomaly on 22 August, 1,800 OMR budget |
| Muscat Health Systems | cloud | migration: 4 × `m7n.2xlarge.8` replaced over 15–17 July by 8 × `m7n.xlarge.8` (both generations overlap), 6 TB SSD, 10 % off the new compute from 1 August |
| Dhofar Logistics | cloud | bursty batch: 2 base servers plus 6–14 more between 02:00 and 06:00 daily; joined 20 June, left 25 August |
| Nizwa Fintech | platform | growth: plan S → M (1 July) → L (10 August); pods 12 → 40 vCPU-hours; PVC 200 → 900 GB |
| Sohar Ports Analytics | platform | plan M throughout, nightly ETL peaks, suspended 18–24 July (no pods, plan still billed) |
| Salalah Tourism Board | platform | plan XL June–July, downgraded to M in August |

Plus a global 5 % "launch" campaign for June. Every one of the six is
decommissioned before the window closes — usage stops, resources are marked
deleted, the customer goes `suspended` and the reason is written to its audit
trail — so **from 2 September only the real data is there** and they read as
having been moved off or shut down. What carries the series across that join is
the landlord backfill described below.

Everything the API can express goes through the API as the operator, so the
product's own validation, auditing and upsert rules apply. The usage ledger,
the inventory and the backdating of created/issued timestamps have no endpoint
(the collectors write them), so those go through the same `internal/store` the
collectors use — which is why `--dsn` is required.

**Marking and removal.** Customers and sources are named `demo-*`, discounts
and budgets `demo: *`, and every usage record and inventory row carries
`{"synthetic":"true"}`. `--purge` removes exactly those rows — measured against
a control: a real customer named `acmewalk307`, a real discount literally named
`demo` and a real budget named `demonstration cap` all survive it. The source
name is a selector in its own right, not merely a label: the landlord backfill
hangs off a REAL customer, so a purge that reached sources only through the
customer slug would strand three months of synthetic rows on a live ledger.
Price books are never removed: `"National Cloud list 2026"` and `"OpenOva
plans"` are shared with real customers and are never created over, re-priced or
deleted. Nor does a purge reverse `--neutralise-reservations` (the landlord
section below): the real `eip.bandwidth_mbps` rows it removed were never
billable, and they are not put back.

```bash
# Preview the plan and the totals — no service, no database, nothing written.
go run ./cmd/seed-history --dry-run

# Seed hw307. Reach the app and its database without exposing either: the
# DSN is read from the cluster at run time and never written down.
kubectl -n chargeback port-forward svc/chargeback 18080:8080 &
kubectl -n chargeback port-forward svc/chargeback-pg-rw 15432:5432 &
DSN="$(kubectl -n chargeback get secret chargeback-db-dsn \
         -o jsonpath='{.data.DATABASE_URL}' | base64 -d \
       | sed 's#@[^/]*/#@127.0.0.1:15432/#')"

go run ./cmd/seed-history \
  --base-url http://127.0.0.1:18080 \
  --forward-auth-email <an address in OPERATOR_EMAILS> \
  --dsn "$DSN"

# Remove everything it created, and nothing else.
go run ./cmd/seed-history --purge --dsn "$DSN"
```

Flags: `--base-url`, `--forward-auth-email` (header name from
`TRUSTED_FORWARD_AUTH_HEADER`) or `--session-cookie`, `--dsn`, `--from`,
`--to` (exclusive), `--seed` (default 2026 — the same seed always produces the
same bytes), `--dry-run`, `--purge`, `--only <slug>`,
`--landlord <slug>` (default `hw307-omani-works`, empty disables),
`--landlord-until <RFC3339>` (default: discovered from the ledger),
`--neutralise-reservations` (default on; see the landlord section below) and
`--cloud-book <name or id>` (default: resolved, see below).

### Which rate card the showcase is priced from

The command **borrows a rate card, it does not mint one**. Minting was a
defect: on hw307 it created `"National Cloud list 2026"` with nine rates while
the operator's own `"National Cloud 2026 list"` — 134 items — was already
there. One word apart, and not equivalent: `nat.1` was `0.11322489` against the
operator's `0.06037935`, `bill_stopped` `compute` against `none`, and several
rates differed in the last digits because the two cards were derived
independently. The showcase was priced from one card and the real customer
beside it from the other, so the demo was not comparing like with like.

Resolution order, logged with the reason on every run:

1. `--cloud-book <name or id>` — the operator overrides everything.
2. **The card the landlord's own cloud source is billed on.** The principled
   default: the showcase exists to be compared against the Sovereign's real
   usage, so it must carry the same rates.
3. The one cloud-scope book whose name reads as a National Cloud card. Two
   such cards is ambiguous and the run stops rather than guess.
4. A card an earlier `seed-history` run made — reused, never duplicated.
5. Only if none of those resolve: create
   `"Showcase cloud rates (seed-history)"`, a name no operator would mistake
   for their own.

`"OpenOva plans"` is unchanged: the platform book is still the product's own
`EnsurePlanBook`.

**Repairing a database that already has the duplicate.** When a card an earlier
run made is still there and a different one resolves, the run re-points every
showcase cloud source and customer onto the resolved card, **drops the showcase
statements rated on the old one** so they are regenerated, and then removes the
duplicate. The delete is refused — loudly, and the book left in place — if any
source, any customer or any issued statement still uses it, and only a card
this tool is known to have made is ever a candidate. `--purge` is unchanged: it
never removes an operator's book.

Because the showcase moves onto the operator's rates, **its historical figures
shift**, and the run says so in as many words. Measured on the scratch database
that reproduced the hw307 state: three sources re-pointed, eighteen statements
dropped and re-rated, the duplicate removed, and Gulf Retail's June moved from
1,414.14 to 1,376.19 OMR.

Re-running is safe: usage upserts on `(source, resource, sku, window_start)`,
customers and sources are matched by slug and name, and an already-issued
statement is left alone by the product itself. Measured: a second run changed
no row count and no metered quantity.

The generators live in `internal/synth` and are pure — no HTTP, no SQL — so
the patterns, the scaling events, the migration, the spike and the plan
switches are unit-tested directly, including that the 22 August spike is
flagged by the product's own `internal/anomaly` rule at z ≥ 3.

### The landlord backfill — continuity across the join

The six customers above were, at first, the only history there was, and the
overview chart said so in two ways the founder caught immediately: 1 September
was an **empty bucket** (the showcase stops at midnight on the 1st, the real
collection on hw307 begins at 10:21:09 on the 2nd), and the series **jumped**
from about 150 OMR a day of showcase customers straight to about 594 OMR a day
of real usage in an entirely different service mix, as though the platform had
sprung into existence fully formed. (About 494 of those 594 OMR turned out,
on 10 September, to be a reservation the cloud never billed — see *Traffic,
not reservation* below.)

`--landlord <slug>` closes both. It gives the Sovereign's own landlord
customer — a REAL customer, `hw307-omani-works` by default — a synthetic past
that grows into the shape its real usage actually has, and stops one hour
before the first real record:

| SKU | at the start (1 June) | 1 July | 1 August | measured end state |
|---|---|---|---|---|
| `ecs.m7n.2xlarge.8` | 6 | 8 | 10 | 10 × 1 instance-hour |
| `ecs.m7n.xlarge.8` | 2 | 2 | 2 | 2 × 1 instance-hour |
| `eip` | 4 | 5 | 6 | 6 × 1 hour |
| `eip.traffic_gb` | 4 addresses on the daily curve | 5 | 6 | 6 × outbound GB in the hour, ~0.1 GB per address per hour on average (a gauge — same curve, not the same number) |
| `evs.ssd.gb` | 70 volumes, 1,400 GB | — grows — | — grows — | 102 volumes, 2,281 GB |
| `nat.1` | 2 | 2 | 2 | 2 × 1 hour |
| `elb` | 2 | 2 | 2 | 2 × 1 hour |

That end state prices at **102.23 OMR a day** at the rates in `internal/synth`
against **99.69** for a real day of the same shape on the operator's card
without the reservation line (594.09 − 494.40, see below), a seam 2.55 % wide.
`TestLandlordSeamMatchesTheMeasuredRealDay` fails if it ever opens past 3 %.
The whole 2.54 OMR of it is `nat.1`: the walked August book priced it at
0.11322489 an hour and the operator's own card at 0.06037935 — the same gap as
before, 0.43 % of a 594-OMR day and 2.5 % of a 100-OMR one, which is why the
tolerance moved from 2 % to 3 % while the slack in OMR shrank from 9.3 to
0.45. The test also requires the day *minus its NAT line* to sit under the
measured figure, so nothing but `nat.1` can hide inside the tolerance. On a
live Sovereign that gap does not exist at all, because the command prices the
backfill from the operator's card. `eip.traffic_gb` is on both sides of the
seam and priced on neither: it is the one SKU the test allows to be unpriced,
until the operator enters a traffic rate.

**Reservations do not move.** An instance-hour, an address's hourly fee, a NAT
gateway, a load balancer and each volume's size are billed for existing, so
each is constant per resource per hour and steps only when a resource is
added. The only total that drifts is storage, and it drifts because volumes
are created, not because a size wobbles. Jittering any of them would make the
data contradict the billing model it exists to illustrate; the tests pin it.

**Traffic, not reservation (10 September).** Until 0.1.26 the collector could
not read an Elastic IP's charge mode and billed every address its reserved
size, `eip.bandwidth_mbps` × hours — 3 × 300 + 3 × 100 = 1,200 Mbps, about
494 OMR a day — and the backfill mirrored that. Then the collector read each
address's bandwidth object: **all six** of the landlord's billable addresses
are `bandwidth_charge_mode = traffic`, `bandwidth_share_type = PER`. The cloud
bills their **outbound gigabytes** and reserves no pipe at all, so the 494 OMR
band was a charge the cloud never made, drawn through the whole showcase with
a cliff at the hour the new collector stopped writing it. The first hourly
`eip.traffic_gb` samples (10 September, 08:00–10:00Z) read 0.013–0.175 GB per
address per hour, about 0.1 on average, roughly 2.4 GB across all six in three
hours.

So each address now meters `eip.traffic_gb` (unit `gb`) and no reservation.
The hourly volume follows a daily curve in Oman local time — about 0.03 GB
through the night, 0.12 across the working day, a 0.20 peak at 20:00 — at
70 % on Friday and Saturday, jittered ±30 % per (seed, address, hour), with the
three 300-Mbps addresses carrying twice the volume of the 100-Mbps ones and
the six weights averaging to exactly 1. Measured on the generated week of
8 August: 96.5 GB across six addresses against 6 × 0.1 × 168 = 100.8 (4 % off,
pinned at 25 %), ratio 2.00, smallest hour 0.0098 GB — never zero, never
negative. The inventory row says what the collector's does, `bandwidth_mbps`
kept and `bandwidth_charge_mode` / `bandwidth_share_type` added, so a reader
comparing it with the real rows sees the same shape. Convergence for a gauge
means the same *shape* at the cut — six addresses on the same curve — not the
same number; `TestLandlordEndStateMatchesTheMeasuredShape` checks count and
unit exactly and the quantity within the jitter band.

**Neutralising the reservations the cloud never billed.** The real ledger
still held every `eip.bandwidth_mbps` row the old collector wrote for those
addresses. `--neutralise-reservations` (on by default, part of the landlord
step, so `--only landlord` runs it alone) deletes them: for every
**non-synthetic** address of the landlord whose inventory says
`bandwidth_charge_mode = traffic`, its `eip.bandwidth_mbps` usage rows go, the
count and the window removed are logged, and one entry on the landlord's audit
trail (`eip.reservation.neutralise`) records what was removed and why. It
refuses to touch an address on charge mode `bandwidth` (the cloud really does
reserve that pipe) or with no charge mode at all (an older gateway; it keeps
billing exactly as the collector does), and it never reaches the `eip` or
`eip.traffic_gb` rows, another customer's addresses, or a synthetic row. A pass
that finds nothing writes nothing, so a nightly re-run stacks no audit entries.
`TestNeutraliseRemovesOnlyTrafficBilledReservations` walks all of that against
a real schema. The neutralisation is a correction of the real ledger and is
**not reversed by `--purge`**: the rows were never billable, and the audit
entry carries no synthetic mark, so both survive a purge — the same test runs
one to prove it. A statement the operator issued over those hours is a
financial record and stands.

Where it writes, and what it refuses to write:

- **The customer is never created or edited.** It is found by slug and read.
  If there is no such customer the backfill is skipped with a log line, not
  forced. Measured: the customer row is byte-identical after a run.
- **No statements.** Usage and inventory only — the landlord's statements are
  the operator's business, and issuing one over invented data would put a bill
  in front of somebody.
- **Its own source**, `demo-<slug>-history`, carrying the same price book as
  the customer's real cloud source (looked up; the run fails naming what it
  found if there is not exactly one). Same customer means the explorer grouped
  by customer draws ONE continuous series; a separate source means `--purge`
  removes exactly the backfill.
- **Resources are handed over alive.** A machine still metering in the last
  hour did not go away, so it gets no `deleted_at` — unlike every showcase
  resource, which is decommissioned before its window closes.

The cut is discovered, not assumed: the earliest non-synthetic
`usage_records.window_start` for that customer, truncated to the hour.
`--landlord-until <RFC3339>` overrides it; `--landlord ''` disables the
backfill; `--only landlord` runs it alone.

Measured end to end on a scratch Postgres (the `chargeback-e2e` workflow's
shape, with control rows for the real half), under the reservation model the
backfill mirrored at the time: the daily series had **no empty day** across
all 98 days from 1 June to 6 September, and the landlord's last synthetic day
and first full real day were both 596.63 OMR — a 0.00 % seam. A purge then
removed 545,274 synthetic usage rows, 257 inventory rows and 7 sources while
leaving the landlord customer, its real source and all 14,300 real rows in
place. Under the traffic model the row and resource counts are unchanged (each
address still emits two lines an hour, the traffic meter in place of the
reservation); the seam at the new measured day is held by the unit test above
and has not yet been re-measured end to end on hw307.

## Operational notes

- The binary is static, runs as uid 65532 with a read-only root filesystem, and
  writes nothing to disk. State is the Postgres database (CNPG on a Sovereign).
- Rotating `APP_ENCRYPTION_KEY` requires re-wrapping the per-secret DEKs; until
  then sources whose credential cannot be opened report the failure on
  `POST /sources/{id}/verify` and are skipped by the collector.
- Multi-region: the service is stateless; run it against the DR-paired database
  and scale API replicas with `COLLECTOR_ENABLED=false` so only one collector
  runs per database.
- No NodePort anywhere: the chart (follow-up) exposes the service through the
  gateway HTTPRoute like every other Blueprint.
- The chart passes the Sovereign's Kyverno compliance set (`bp-kyverno-policies`)
  by rendering each policy's own accepted shape: `prometheus.io/scrape` pod
  annotations pointing at `GET /metrics` on the `http` port, the
  `instrumentation.opentelemetry.io/inject-go` annotation naming the
  Sovereign's `opentelemetry/default` Instrumentation CR (never paired with
  `otel-go-auto-target-exe`, so nothing is injected), a hostname
  `topologySpreadConstraints` entry (`ScheduleAnyway`), requests + limits on
  the CNPG instance, and no secret-shaped env name carrying a literal value.
  `chart/tests/kyverno-policies.sh` renders the chart and runs the full policy
  set against it with the kyverno CLI on every PR.
