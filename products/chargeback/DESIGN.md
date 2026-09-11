# Chargeback — cost-analysis design (cloud-provider-grade)

**Status:** target design, 2026-09-07. Supersedes the "lean statement" UI shipped
under EPIC #6723 lanes A/B. The chart install path stays in
[`chart/DESIGN.md`](chart/DESIGN.md); ADR-0014 decisions are unchanged.

## 0. What the founder saw on hw307 (2026-09-07) and why

| Complaint | Measured cause |
|---|---|
| Overview shows `0 0 0 0 0` | Wire mismatch between lanes. The API sends `customers`, `last_period`, `sources`; the page reads `customers_by_status` and `rated_total_last_period`, which never existed. `/api/v1/overview` on hw307 returns active=1, total=2780.011998 — the screen renders none of it. No test crossed the lane boundary. |
| Customer page is a flat statement | The page is tabs over raw tables (SKU/quantity/unit). There is no cost anywhere outside a monthly statement: usage is rated only when a period is run, so no screen can answer "what is this costing me today". |
| No cost analysis, no charts | One SVG bar chart of *quantity per day* exists. There is no cost time series, no grouping, no filtering, no comparison, no forecast, no top-N, no per-resource cost. |
| Almost nothing has CRUD | Missing: customer delete, price-book delete/clone/item edit/delete/export, discount edit/delete/global, source edit, draft statement delete, budgets, saved views, allocation settings. |
| Allocation is hardcoded and not editable | The basis (vCPU-h + GiB-h + GB-h with equal weights) is a constant in `store/allocation.go`; the page shows shares with no currency, no pool, no margin, and nothing to edit. On hw307 100% is platform-overhead (no Organization besides the Sovereign's own yet), so it reads as a meaningless table. |

Data on hw307 at the time: 72,098 usage records, 13 SKUs, 6 resource kinds live
(ecs 12, evs 102, eip 6, elb 2, nat 2, vpc 2) plus 14,097 pod resource ids,
one customer, one price book (134 items), three draft statements. The k8s.*
SKUs are unpriced by design (platform consumption is allocated, not rated).

## 1. Capability matrix — AWS Cost Explorer · GCP Billing · Azure Cost Management vs this app

Legend: ✅ have · ◐ partial · ❌ missing. "Target" = this design.

| Capability | AWS | GCP | Azure | Had | Target |
|---|---|---|---|---|---|
| Cost over time (daily / monthly) | ✅ | ✅ | ✅ | ❌ | ✅ `cost/explore` |
| Group by service · account · region · SKU · resource · tag | ✅ | ✅ | ✅ | ❌ | ✅ kind · customer · source · region · sku · resource · tier · namespace |
| Include / exclude filters per dimension | ✅ | ✅ | ✅ | ❌ | ✅ |
| Chart types: stacked bar · line · area · donut · ranked bars | ✅ | ✅ | ✅ | ◐ (1 bar) | ✅ dependency-free SVG set |
| Table under the chart with totals, share %, Δ vs previous period | ✅ | ✅ | ✅ | ❌ | ✅ |
| Previous-period comparison | ✅ | ✅ | ✅ | ❌ | ✅ always computed |
| Forecast to month end | ✅ | ✅ | ✅ | ❌ | ✅ run-rate → run-rate + trend → weekday-seasonal by history, per-day projection, with confidence |
| Top-N with "Other" bucket | ✅ | ✅ | ✅ | ❌ | ✅ |
| MTD / last month / MoM KPIs | ✅ | ✅ | ✅ | ❌ | ✅ |
| Per-resource cost, ranking, drill-in | ✅ (resource level) | ✅ | ✅ | ❌ | ✅ `resources` + detail |
| Budgets with thresholds + alerts | ✅ | ✅ | ✅ | ❌ | ✅ CRUD + hourly evaluator + mail |
| Anomaly detection with root-cause drivers | ✅ | ✅ (alerts) | ✅ | ❌ | ✅ z-score on daily series, drivers by SKU/resource |
| Rightsizing / idle / unattached recommendations | ✅ Compute Optimizer, Trusted Advisor | ✅ Recommender | ✅ Advisor | ❌ | ✅ stopped ECS · unattached EVS · unbound EIP · low CPU · unpriced SKUs · stale sources |
| Credits / discounts shown separately from list price | ✅ | ✅ | ✅ | ◐ (statement only) | ✅ waterfall list → discount → net → tax |
| Rate card (price list) browse + per-account pricing | ✅ Price List API | ✅ SKU catalog | ✅ | ◐ | ✅ full CRUD, clone per account, coverage of SKUs in use |
| Saved reports / views | ✅ | ✅ | ✅ | ❌ | ✅ |
| CSV export of any view | ✅ CUR | ✅ BigQuery export | ✅ | ◐ (statements) | ✅ explore + resources + price books |
| Invoices / statements with line detail | ✅ Bills | ✅ Invoices | ✅ | ✅ | ✅ redesigned, printable |
| Cost allocation of shared spend to Organizations | ✅ split charges | ❌ | ✅ allocation rules | ◐ | ✅ editable weights, overhead policy, pool, money + margin |
| Multi-Organization scope (operator vs customer) | ✅ | ✅ | ✅ | ✅ | ✅ every endpoint scope-filtered |
| One reporting currency with stored exchange rates | ✅ | ✅ | ✅ | ❌ (`mixed_currency` flag, unconverted sums) | ✅ `currency_rates` + `cost_base`, unconverted listed never summed (§3.10) |

## 2. The ownership model — two layers that never meet in billing

Founder direction, 2026-09-08. Everything this application meters belongs to
exactly one of two layers, and the two are priced independently:

| | **Cloud layer** | **Platform layer** |
|---|---|---|
| A source is | a cloud project — kind `huawei-project` (and `file` for imports) | an Organization on this Sovereign — kind `openova-org` |
| Distinguishers | enterprise project, tag filter, resource scope (`scope_token`) | the Organization slug |
| Its resource kinds are | cloud SKUs (`ecs.*`, `evs.*`, `eip*`, …) | platform SKUs (`plan.<slug>`, and `k8s.vcpu` / `k8s.mem_gb` / `k8s.pvc_gb` only if sold per use) |
| Priced by | a **cloud** price book | a **platform** price book (the "OpenOva plans" book) |

Three rules follow, and they are enforced in the schema, not by convention:

1. **A customer owns one or more sources.** A source belongs to one layer,
   derived from its kind (`cost_sources.layer`, a generated column).
2. **A price book is assigned per SOURCE, not per customer**
   (`cost_sources.price_book_id`), and the book's `scope` must equal the
   source's `layer`. A customer's statement is the sum of its sources, each
   rated by its own book. `customers.price_book_id` survives only as a
   deprecated column (§4.1).
3. **Coverage and "unpriced" are computed per book from the usage of the
   sources assigned to it** — so a platform meter can never appear under a
   cloud book, which is exactly what the hw307 screenshot showed before this
   change.

### 2.0a The Sovereign is not a customer

The Sovereign's own platform footprint — its control plane, gitea, harbor,
keycloak, openbao, shared-pg, every namespace with no Organization label —
is recorded on **one internal source**: kind `openova-platform`,
`internal = true`, `customer_id NULL`. It has no customer row, no plan, and
no price book, and every customer-facing query excludes it
(`CostQuery.IncludeInternal` is the single opt-in, used only by Allocation).

Before this change `internal/adapter/openova/orgsync.go` synced the
Sovereign's own Organization (`spec.kind = internal`) as a customer and the
platform collector attached the cluster-wide overhead to it. On hw307 that
made the landlord "Omantel" a customer holding two Huawei sources **and** an
`openova-org` source whose `k8s.*` rows read "unpriced" under the National
Cloud cloud book — the mixing the founder rejected. OrgSync now skips the
internal Organization entirely, ensures the internal source instead, and
retires the customer an earlier version created (§4.1). The landlord's
Huawei project stays a plain customer with cloud sources and nothing else.

**The Organization's vCluster control plane is not the customer's usage.**
Every Organization's vCluster control plane runs in the Organization's own
host namespace (#6902): the `vcluster-0` StatefulSet pod (`app=vcluster`), its
`data-vcluster-0` backing-store PVC, and the vCluster's own coredns, which the
syncer mirrors down from the virtual `kube-system`
(`vcluster.loft.sh/managed-by` + `vcluster.loft.sh/namespace: kube-system`).
The org-controller sizes the namespace ResourceQuota as plan **plus** that
control plane (520m / 1088Mi requests, 1500m / 1194Mi limits, 5Gi storage) so
it never eats into what the customer bought, and the platform collector draws
the same line: `isVClusterControlPlane` keeps those pods and that PVC off a
customer Organization's `k8s.*` meters (`collector.go`,
`TestVClusterControlPlaneIsNotMetered`). Customer workloads synced from the
vCluster carry the managed-by label too but sit in the customer's own virtual
namespace and are metered as before. The one place the control plane IS
counted is the platform-overhead line above — the Sovereign pays for it, and
that line has to reconcile back to the cloud total.

**The per-Organization platform stack is not the customer's usage either.**
Every Organization is delivered with the same platform HelmReleases in its
host namespace, none of them chosen from the catalog: `bp-keycloak` (the
Organization's own Keycloak plus its bundled PostgreSQL), `bp-newapi` (the
LLM gateway plus its CNPG PostgreSQL), `bp-openclaw` (the workspace
controller) and `bp-agenity` (the agentic dashboard plus the oidc-gate in
front of it). Measured on hw307 (Acme Walk, plan S, 2026-09-10): with the
quota at plan + control plane, `bp-keycloak-0` was refused at admission
(`requested: limits.cpu=1,limits.memory=2Gi`, `used: limits.cpu=3450m`,
`limited: limits.cpu=3500m`), so the purchased WordPress and Stalwart waited
on the keycloak dependency forever. Derived from the sources that size it,
the stack is 3840m / 6064Mi of requests and 4550m / 7168Mi of limits before a
single customer application — larger than the S plan by itself.
The org-controller now sizes the quota as **plan + vCluster control plane +
platform stack** (`manifests.go` `platformStack`; requests 3840m / 6064Mi and
limits 4550m / 7168Mi on S/M/L, 4835m / 8096Mi and 5500m / 9152Mi on XL,
where the 2-CPU / 4Gi LimitRange default sizes agenity's unsized init
container above its app containers), and the collector draws the same line:
`isPlatformStack` keeps those pods and their labelled PVCs off a customer
Organization's `k8s.*` meters, by `app.kubernetes.io/instance` ∈
{bp-keycloak, bp-newapi, bp-openclaw, bp-agenity}, by the chart-fixed
`app.kubernetes.io/name` ∈ {bp-newapi, bp-openclaw, bp-agenity, bp-oidc-gate}
(which also covers the funnel door's `newapi` / `openclaw` / `agenity`
releases synced from the vCluster) and by `cnpg.io/cluster` ending in
`-newapi-pg` (`collector.go`, `TestPlatformStackIsNotMetered`). The
purchased `bp-wordpress-tenant` and `bp-stalwart-tenant`, a customer's own
CNPG cluster, and any customer workload synced from the vCluster stay
metered. On the platform-overhead line the stack IS counted, exactly like the
control plane, so that line still reconciles to the cloud total.

### 2.0b Allocation is a report, not billing

`Allocation` reads the two layers read-only and never writes a bill:

- **pool** = the rated cloud cost of the chosen **landlord customer's** cloud
  sources (`allocation_settings.sovereign_customer_id` keeps its wire name);
- **shares** = each Organization customer's platform-source usage;
- **overhead** = the internal source's usage, as the `platform-overhead` row
  (no customer id; it is labelled `Platform overhead`).

It must keep working after this change, and
`TestIntegrationInternalSourceIsInvisibleToCustomersButCountedByAllocation`
pins both halves: the internal source never reaches an explorer, a summary or
a statement, and it is exactly the overhead row of the report.

### 2.1 Information architecture

Operator (sovereign-admin lens):

```
Analyse    Overview · Cost explorer · Resources · Anomalies · Recommendations
Plan       Capacity                                                  (§11)
Bill       Statements · Budgets · Reports
Configure  Customers · Price books · Discounts · Allocation
```

Customer lens (`/my/…`): Overview · Cost explorer · Resources · Statements · Budgets · Reports · Sources.

Partner lens (`/partner/…`, §13.5): My customers · Cost explorer · Resources · Anomalies · Recommendations · Statements · Account · Margin · Retail prices · Users. The Analyse pages are the operator's own, over the partner's customers.

Every page is a real route (deep-linkable) and every list is sortable, filterable
and exportable. Every number on a screen comes from an endpoint in §3; nothing is
computed client-side except display formatting.

### 2.2 Overview
KPI strip: month-to-date cost · forecast month end (with method + confidence) ·
last month · MoM Δ% · average daily (30d) · live resources · active customers ·
unpriced SKUs (warning). Daily cost stacked by kind for the last 30 days with the
forecast tail dashed. Cost by customer (donut + ranked table). Cost by service
kind (ranked bars). Budgets strip (actual vs amount, forecast marker). Latest
anomalies. Recent statements. Collector health (sources verified/failed, last
collected).

### 2.3 Cost explorer
Controls: date presets (7d · 30d · MTD · last month · 3M · 6M · YTD · custom),
granularity (hourly for windows ≤ 14 days · daily · monthly; hourly falls back
to daily when the window grows), group by, include/exclude filter chips per
dimension, metric (cost | usage), chart type (stacked bar · line · area), top-N,
compare with (previous period · same period last month · same period last year ·
custom from/to), save view, export CSV. Chart + table (group · current · compare
window · Δ% · share · resources) with a totals row; the compare column is headed
by the window it sums. Clicking a group row adds it as a filter and re-groups one
level down (kind → sku → resource); clicking a day bar with nothing to drill zooms
to that day at hourly grain.

### 2.4 Resources
Inventory joined with cost in the window: kind, name, region, customer, status
(live / stopped / deleted), first/last seen, cost, cost sparkline. Filters and
free text. Drill-in shows daily cost, SKU lines, attributes, transitions.

### 2.5 Customer detail
Header KPIs (MTD, forecast, last month, open drafts). Tabs: Overview (trend + by
kind + top resources) · Cost explorer (scoped) · Resources · Statements ·
Discounts (CRUD) · Budgets (CRUD) · Sources (CRUD incl. scope token, the
layer badge and the per-source price book) · Users · Settings (edit every
field except the price book; delete) · Audit.

**Disabling a source.** `cost_sources.status` gains `disabled`: a
decommissioned source. The collector's listing skips it, it counts as
neither a verified source nor live estate, and the Sources tab badges it and
offers Disable / Enable (`PATCH /sources/{id} {"disabled": true|false}`,
operator-only). Its history is billing data and is untouched: it still rates,
still appears in the explorer, and still stands on every statement already
issued from it. Enabling returns it to `verified` when it had been verified
before and to `pending` otherwise; verifying a disabled source is refused
(409) until it is enabled, and neither the Organization sync nor the platform
collector may re-enable one. The internal platform source cannot be disabled.

**Creating a customer lands on the Sources tab with the add-source modal
open** (`/customers/{id}?tab=sources&add=1`): a customer is defined by where
its cost comes from, and the price book is chosen there, one per source. The
customers list shows a **Sources** column counting by layer ("1 cloud ·
1 platform") in place of the old single price-book column.

### 2.6 Price books
List with **scope** (cloud | platform, with a filter above the table),
currency, items, the **sources** assigned to the book, and coverage % of the
SKUs those sources use. New price book asks for the scope; the detail header
shows it.
Detail: settings (edit), searchable inline item table (add / edit / delete / bulk
save), import CSV with preview, export CSV, clone (per-account pricing), delete
(refused while any SOURCE is assigned — the 409 names the sources and their
customers), and the coverage card **"SKUs in use by the sources assigned to
this book"**, which lists those sources.

Under a platform book that prices none of the `k8s.*` meters, those meters
are reported as **not sold per use** — the plan covers them and they are the
allocation basis — never as "unpriced", and the page says so in one line
instead of offering "Add rate". The explorer and the summary carry the same
split: `unpriced` for a genuine gap, `not_sold_per_use` for the basis meters.

Below the list, a **Currencies** card (§3.10): the reporting currency (read-only —
it is `allocation_settings.currency`, changed on the Allocation page) and the
table of exchange rates (code, per base, one unit in the reporting currency,
source, updated) with inline add / edit / delete. The overview and the explorer
show a warning naming every currency in use that has no rate, with the records
and cost left out, and link here.

### 2.7 Discounts
All discounts in one place: scope (customer or all customers), kind, value, SKU
scope, campaign window, active. Create / edit / delete / toggle. Preview panel
shows the effect on the current MTD.

### 2.8 Budgets
Create / edit / delete. Scope (all or one customer), monthly amount **in the
reporting currency** (the form shows it read-only; §3.5), thresholds,
notification emails. Status bars: actual (converted), forecast marker,
thresholds crossed.

### 2.9 Allocation
A **report over the two layers, never billing** (§2.0b). Settings editor:
basis weights (vCPU-h, GiB-h, GB-h), overhead policy (keep as a separate
line, or distribute across Organizations), cost pool (the **landlord
customer's** rated cloud cost for the window — its cloud sources are the pool
— or a manual amount). Result table in
currency: allocated cloud cost, rated revenue, margin, margin %. Chart of the split.

**Plan revenue.** What an Organization actually pays is its catalog plan
(S 5 · M 9 · L 16 · XL 30 OMR/month, Flexi 0 = pay per use — the prices seeded
by `core/services/catalog/handlers/seed.go` `seedPlanRows`, restated in
`internal/store/planbook.go` because the engine imports nothing from Catalyst).
The plans are bundles whose per-resource split is not identifiable (M = 2×S,
L = 4×S, XL = 8×S), so no per-vCPU rate is invented: the platform collector
meters the plan itself as one `plan.<slug>` record per hour slice (unit
`plan-hour`, quantity 1 for a full hour, `resource_kind=plan`, labels
`{name: "<Plan> plan", plan: <slug>}`) on the Organization's `openova-org`
source — the source the plans book is assigned to — only while the
Organization is active and on a plan other than flexi,
starting at the customer's `start_date` or, absent one, the first sync. OrgSync
reads `spec.planSlug` (lower-cased; empty → `s`, the org-controller's default;
the Sovereign's own Organization gets none) into `customers.plan_slug`, and
owns the **"OpenOva plans"** price book: OMR, divisor 8760, `plan.s` 60/yr,
`plan.m` 108, `plan.l` 192, `plan.xl` 360 as annual prices, so
`unit_price = annual / 8760 = monthly / 730` per plan-hour; created once when
absent with `scope = platform`, assigned to every Organization's
`openova-org` SOURCE whose plan calls for it, never re-created, re-priced or
re-assigned over an operator's choice. `k8s.vcpu` /
`k8s.mem_gb` / `k8s.pvc_gb` stay unpriced in that book — under a plan they are
the allocation basis above, not the bill, and a bundle has no identifiable
per-resource split, so any per-vCPU rate under the plans book would be
invented. An Organization on **flexi** is billed the other way round; that is
§2.9a. `rated_revenue` needs no new arithmetic: it is the Explore total per
Organization customer, and the plan line is part of it. Statements group the
line under "Subscription plan" (`KindLabel("plan")`, `serviceOfSKU("plan.m")`).

### 2.9a Pay per use — the two platform billing shapes

Founder direction 2026-09-10 (EPIC #6867): *"How will the payg flexi users get
measured and charged"*. There are **two** platform billing shapes, and an
Organization is on exactly one of them — which is what makes double charging
structurally impossible rather than merely avoided.

| | Committed plan (`s` / `m` / `l` / `xl`) | Pay per use (`flexi`) |
|---|---|---|
| What the Organization buys | a fixed shape, enforced by a ResourceQuota (S 2 vCPU / 4 GiB, M 4/8, L 8/16, XL 16/32, all Guaranteed; the namespace quota is that plan **plus** the vCluster control-plane overhead — 520m / 1088Mi requests, 1500m / 1194Mi limits — **plus** the per-Organization platform-stack overhead — 3840m / 6064Mi requests, 4550m / 7168Mi limits on S/M/L; 4835m / 8096Mi and 5500m / 9152Mi on XL — so neither eats the plan (S renders `requests.cpu: 6360m`, `limits.cpu: 8050m`), §2.0a) | nothing fixed: `planQuotaTable` gives flexi no CPU/memory ceiling and Burstable QoS |
| What the collector emits | one `plan.<slug>` record per hour **plus** the `k8s.*` meters | the `k8s.*` meters only — `billablePlan` returns "" for flexi, so there is no plan line to emit |
| Which book rates its source | **"OpenOva plans"** | **"Organization PAYG"** |
| What that book prices | `plan.s` / `plan.m` / `plan.l` / `plan.xl` — the meters are deliberately unpriced | `k8s.vcpu` / `k8s.mem_gb` / `k8s.pvc_gb` — no plan line is priced |
| What the bill is | the flat monthly plan, whatever it ran | exactly what it ran; zero while idle |

The two books price **disjoint** SKU sets (`TestPlanAndPAYGBooksAreDisjoint`),
and a source carries exactly ONE book, so a sized Organization can never be
charged per vCPU on top of its plan and a flexi Organization can never be
charged a plan line it does not have.
`TestIntegrationSizedAndFlexiAreNeverDoubleCharged` proves both directions in
money over a full 7-day window.

**Before this change a flexi Organization was billed nothing at all**: it got
no plan line by design, and its meters were unpriced under the only book the
sync ever assigned. The "Organization PAYG" book existed on a Sovereign but at
`scope = 'cloud'`, which is the wrong scope for `k8s.*` SKUs and made it
**unassignable** — `SetSourcePriceBook` refuses a book whose scope is not the
source's layer, so nothing could ever be pointed at it.

**Rate derivation** (`internal/store/paygbook.go`, and written onto the book's
own `description` so an operator can read it before changing a rate). The
sized plans are S 5 · M 9 · L 16 · XL 30 OMR/month for 2 / 4 / 8 / 16 vCPU with
2 GiB per vCPU. Per **unit** of (1 vCPU + 2 GiB) per month that is:

| Plan | units | OMR/month | OMR per unit-month |
|---|---|---|---|
| S | 2 | 5 | 2.500 |
| M | 4 | 9 | 2.250 |
| L | 8 | 16 | 2.000 |
| XL | 16 | 30 | 1.875 |

— a volume ladder: the larger the commitment, the cheaper the unit. Pay per
use commits to **nothing** (the Organization can scale to zero and stop paying
that hour), so it must not undercut the cheapest thing an Organization can
commit to, or nobody would ever take a plan. It is therefore the ENTRY
commitment plus 10 %: **2.50 × 1.1 = 2.75 OMR per unit-month**, which sits
above every rung of the ladder. That splits across what a unit is made of:

| SKU | OMR/month | Annual (× 12) | Unit price (÷ 8760) | Why |
|---|---|---|---|---|
| `k8s.vcpu` | 2.000 per vCPU | 24.000 | `0.00273973` per vcpu-hour | the compute share of the 2.75 unit |
| `k8s.mem_gb` | 0.375 per GiB | 4.500 | `0.00051370` per gib-hour | 2.75 − 2.00 = 0.75 over the 2 GiB a unit carries |
| `k8s.pvc_gb` | 0.219 per GB | 2.628 | `0.00030000` per gb-hour (exact) | not from the ladder — no plan bundles storage; set ~31 % above the 0.00022831 OMR per GB-hour the cloud charges for the SSD underneath |

`2.00 + 2 × 0.375 = 2.75` is asserted by `TestPAYGUnitRateSplit`, so the split
and the ladder cannot drift apart. Conversion uses the book's own divisor
(annual ÷ 8760 = monthly ÷ 730), so an operator who changes the divisor
recomputes both platform books alike.

**Sanity check.** A flexi Organization holding 4 vCPU + 8 GiB around the clock
pays 730 × (4 × 0.00273973 + 8 × 0.00051370) ≈ **11.00 OMR/month**, against
**9** for the committed M plan of the same shape — the no-commitment premium —
and near **zero** when it is idle.

**Assignment.** `OrgSync` ensures BOTH books on every Organization sync and
points the Organization's `openova-org` source at the one `BookForPlan` names:
the plans book for `s`/`m`/`l`/`xl` (and for an unknown or absent plan, which
keeps the pre-existing fallback rather than inferring pay-per-use), the
pay-per-use book for `flexi`. A source already on the OTHER managed book is
**re-pointed** — that is how a plan change between flexi and a sized plan
reaches the bill. A source on any other book was put there by an operator (a
negotiated clone) and is never touched.

**Migration.** The last entry of `migrations` — appended at the END, because
migrations are positional and an entry inserted mid-list is silently skipped
on an already-migrated database — adds `price_books.description`, moves
"Organization PAYG" to `scope = platform` (only while nothing cloud-shaped is
assigned to it), backfills both books' descriptions where the operator has
written none, and replaces the shipped placeholder rates (`k8s.vcpu`
0.02589041 per vcpu-hour = 18.90 OMR per vCPU-month, an order of magnitude
out) with the derived ones — but ONLY while the book rates no source at all, so
a book that has ever billed anyone is left exactly as it is. Its SQL is built
from `PAYGBookItems()` rather than restating the numbers, so a migrated
Sovereign and a fresh one cannot end up with two different books.

### 2.10 Statements
A statement is the sum of the customer's sources, each rated by its own book,
and is issued in **one** currency. A run for a customer whose sources are
assigned books of different currencies is **refused with 400** naming both
("… a statement is issued in one currency — assign books of one currency");
in an all-customer run that customer carries the message in its own result
and every other customer is still rated.

Filters (period, customer, status). Run period. Statement view: waterfall (list
→ discounts → net → tax → total), lines grouped by service kind with per-source
breakdown, printable, CSV. The Issue confirm carries a checked-by-default
"Email the statement to the customer" box (§3.9).

### 2.11 Reports
Scheduled plain-text cost reports, the way a cloud console mails a cost report
on a schedule. KPIs (schedules, sent last 30 days, failures, next due); table
(name, scope, cadence in words, recipients, sections, next run, last sent,
active) with Create / Edit (name, scope, cadence, weekday or day-of-month,
hour UTC, recipients, section checkboxes, active), Delete, Preview (the exact
text the next send mails, in a `<pre>`), Send now, and a deliveries log per
schedule. Customer lens `/my/reports`: a customer-admin manages schedules for
its own customer only; a viewer reads and previews.

### 2.11 Discount combination rule
Founder direction 2026-09-08 (EPIC #6867): *"why don't we provide a
stack/aggregation function selection?"* Until then every percent discount was
computed against the untouched base and **summed** — a 10 % campaign for all
customers plus a 20 % SKU discount took 30 % off that SKU — which reads as a
surprise on a bill. The rule is now an operator setting, read at statement run
time and printed on the statement.

**Setting.** `billing_settings` is a single-row table (`id = 1`,
`discount_rule TEXT NOT NULL DEFAULT 'most-specific'`, `updated_at`); the
migration seeds the row and a wiped row reads as the defaults. `GET|PUT
/api/v1/billing-settings` `{discount_rule}`, operator-only, 400 naming the
accepted values on an unknown rule, audited as `billing.settings` with the
previous value. The Discounts page carries a "Combination rule" card at the
top: a segmented control over the four rules, a one-line explanation, a live
example computed client-side by `ui/src/lib/discountRule.ts` (the same
arithmetic as the engine, unit-tested against the engine's fixture), Save.

**The four rules** decide, **per SKU** (a discount applies to a meter; the
lines of one meter share every applicable discount), what happens when more
than one percent discount applies:

| rule | per line | 10 % global + 20 % on A, list 100 (A 50, B 50) |
|---|---|---|
| `most-specific` (default) | the one percent with the narrowest scope wins — a SKU discount beats a whole-bill one; at the same scope the higher percent wins | A 20 % (10) + B 10 % (5) = **15** |
| `highest` | the highest percent wins regardless of scope (scope is the tie-break) | A 20 % (10) + B 10 % (5) = **15**; with a 30 % global instead: 30 on both = **30**, where most-specific gives A 20 % (10) + B 30 % (15) = **25** |
| `stack` | every applicable percent is summed against the untouched base — what every statement did before this section | A 30 % (15) + B 10 % (5) = **20** |
| `compound` | percents multiply: 10 % then 20 % is 1 − 0.9 × 0.8 = 28 % | A 28 % (14) + B 10 % (5) = **19** |

Scope means SKU-scoped vs whole-bill only. The customer dimension does not
enter — a customer's own discount and an all-customer campaign both already
apply to that customer's statement — and a tie at the same scope goes to the
higher percent, so a customer never loses on a tie. **Fixed amounts** come off
what remains after the percentages, in every rule, clamped so the bill never
goes below zero (a 100 credit on the 15-off example above takes the remaining
85). Money stays exact (`big.Rat`); the per-discount breakdown is rounded once.

**Stackable.** A per-discount boolean (`discounts.stackable`, default false;
the "Stackable" checkbox column on the Discounts page saves inline via
`PATCH /discounts/{id} {stackable}`; create/PUT accept it). Under
`most-specific` and `highest` a stackable discount is **added on top of the
winner** instead of competing with it — the campaign on top of the contract:
with the 20 % on A stackable, A takes 10 % + 20 % = 15 and the bill 20. Under
`stack` and `compound` the flag changes nothing. Stackable discounts never
win; if every candidate on a line is stackable they simply add.

**On the statement.** `statements.discount_rule` (wire key `discount_rule`)
records the rule in force when the run wrote the statement, so an issued bill
states which rule produced its numbers; the migration backfills `stack` on
statements that carry a discount breakdown, because summing is what produced
them. Changing the setting never rewrites an issued statement — the next run
states the new rule. Each `discount_detail` entry carries `stackable` when
set, and a discount that matched the bill but lost on every line it matched
under `most-specific` / `highest` is still on the bill with `amount` 0 and
`superseded_by` = the winner's id, so the statement view shows "not applied:
superseded by <name>" rather than a discount that silently vanished. The
statement view names the rule next to the discount block.

**Tests.** `internal/rating/discount_test.go` pins every number in the table
above plus the stackable and fixed-after-percent cases;
`internal/store/billing_settings_integration_test.go` the setting round-trip,
the `stackable` column and the rule recorded on a real run;
`internal/api/billing_settings_test.go` the endpoints' validation, scope and
audit; `ui/src/lib/discountRule.test.ts` the client-side example.

## 3. API contracts (all under `/api/v1`, JSON, scope-filtered)

Dates are `YYYY-MM-DD`, windows are half-open `[from, to)`. Money is a decimal
number in the **reporting currency** (§3.10) on every cost surface — explore,
summary, resources, anomalies, recommendations, budgets, allocation, reports —
and in the customer's price-book currency on statements and price books, which
are never converted. `customer` query values are customer ids, and every one
of them is INTERSECTED with the caller's scope server-side: a customer role
sees its own customer, a partner role its partner's customers (§13.5), the
operator whichever it asked for. Naming a customer outside the scope returns
an empty document, never someone else's rows.

### 3.1 `GET /cost/explore` · `GET /customers/{id}/cost/explore`
Params: `from`, `to`, `granularity=hour|day|month` (`hour` only for windows of at
most 14 days — 336 buckets; every grain is capped at 400 buckets),
`group_by=none|customer|source|kind|sku|region|resource|tier|namespace|enterprise_project|tag:<key>`,
`metric=cost|usage` (usage requires `group_by=sku` or a single `sku` filter),
include filters `customer|kind|sku|region|source|resource|tier|namespace|enterprise_project|tag:<key>=a,b`,
exclude filters `exclude_<dim>=a,b`, `limit` (top-N groups, default 10, 0 = all),
`compare_from`/`compare_to` (`YYYY-MM-DD`, half-open, both or neither — the window
`previous` and `delta_pct` are measured against; it may be any length and may
overlap the window; omitted = the same-length window immediately before `from`).
Buckets are `YYYY-MM-DDTHH` (hour, UTC), `YYYY-MM-DD` (day) or `YYYY-MM` (month).

**Tags and enterprise project** (the AWS cost-allocation-tag / Azure tag
dimension). `tag:<key>` is a dynamic dimension over the resource tags the
collectors store in `labels.tags`: the Huawei ECS/EVS/EIP/ELB/RDS-family
tags (all three wire shapes — `["k=v"]`, `[{key,value}]`, `{k: v}` — folded
into one map, keys case-sensitive, ≤50 tags, keys ≤128 chars), and on the
Sovereign's own cluster the pod/PVC labels `app.kubernetes.io/name|instance|component`
and `openova.io/application`, exposed as `tag:app|instance|component|application`
— which is cost per Application. Records without the key group as
`(untagged)`, and `(untagged)` is a legal filter value. The key must match
`^[A-Za-z0-9_.:/@-]{1,128}$` (400 naming the rule otherwise) and is bound as a
SQL parameter, never interpolated; the colon may be URL-encoded.
`enterprise_project` is a static dimension over `labels.enterprise_project`
(`(none)` when absent). `GET /cost/dimensions` additionally returns `tag_keys`
(the distinct keys in the window, scoped and filtered like the explorer) and,
when `group_by` or a filter names a tag, that tag's values under
`dimensions["tag:<key>"]`.

```json
{
  "from": "2026-09-01", "to": "2026-09-08", "granularity": "day",
  "group_by": "kind", "metric": "cost", "currency": "OMR",
  "buckets": ["2026-09-01", "…"],
  "groups": [{ "key": "ecs", "label": "ECS", "total": 421.1, "previous": 398.0,
               "delta_pct": 5.8, "share": 0.52, "resources": 12, "values": [60.1, "…"] }],
  "other": { "total": 3.2, "values": ["…"] },
  "total": { "current": 810.4, "previous": 790.2, "delta_pct": 2.6, "resources": 126 },
  "totals_by_bucket": [115.7, "…"],
  "unpriced": [{ "sku": "k8s.vcpu", "unit": "vcpu-hour", "quantity": 1118.4, "resources": 14097 }],
  "unconverted": [{ "currency": "USD", "records": 168, "cost": 84.0 }],
  "mixed_currency": true,
  "forecast": { "month_end": 2712.5, "run_rate_daily": 92.1, "trend_daily": 0.8,
                "method": "weekday-seasonal", "days_observed": 21, "days_in_month": 30,
                "confidence": "medium",
                "projection": [{ "day": "2026-09-22", "cost": 98.4 }, "…"],
                "weekday_factors": { "Mon": 1.17, "Tue": 1.17, "Wed": 1.17, "Thu": 1.17,
                                     "Fri": 1.17, "Sat": 0.58, "Sun": 0.58 } },
  "compare": { "from": "2026-08-25", "to": "2026-09-01", "label": "previous period" }
}
```
`forecast` is present only when the window is the current calendar month at day
granularity. `previous` is the same-length window immediately before `from`.

`currency` is always the reporting currency (§3.10); every group, bucket and
total is the exact rational sum of converted record costs rounded once to six
decimals. `unconverted` lists, per price-book currency that has no exchange
rate, the priced records the window holds in it and their cost **in that
currency** — none of it is in any total. `mixed_currency` is true exactly when
`unconverted` is non-empty (it no longer means "several book currencies were
summed": nothing is ever summed across currencies).

The forecast method follows how much of the month is complete
(`internal/rating/forecast.go`; every value is a float estimate, never billed):

| complete days | `method` | each remaining day *d* (k = 1 today, 2 tomorrow, …) |
|---|---|---|
| < 7 | `run-rate-Nd` | mean of the N days |
| 7 – 13 | `run-rate-7d+trend` | max(0, rr7 + slope × (3 + k)) |
| ≥ 14 | `weekday-seasonal` | max(0, (mean₂₈ + slope × ((W−1)/2 + k)) × factor(weekday d)) |

rr7 = mean of the last 7 complete days; mean₂₈ = mean of the last W = min(28, n)
days; slope = least-squares cost change per day (fitted on cost ÷ weekday factor
for the seasonal method, so the weekly shape never reads as a trend); factor(w)
= mean cost on weekday w ÷ overall mean, 1 for a weekday seen fewer than twice.
The slope is applied from the centre of the averaging window because a
window's mean is the fitted line's value at its midpoint. `projection` lists the
exact per-day values summed into `month_end` (today first), so the chart tail
reconciles with the KPI; `weekday_factors` is present only for
`weekday-seasonal`. Confidence: `high` needs ≥ 14 days and a last-week
coefficient of variation < 0.15; `medium` ≥ 7 days; `low` otherwise — and
always `low` when the last week's CV ≥ 0.5.

`compare` is the window every `previous` (and so every `delta_pct`) was summed
over: `label` is `previous period` for the automatic same-length window
immediately before `from`, `custom` when `compare_from`/`compare_to` were given.
The CSV export (`/cost/export.csv`) stays one row per bucket; a custom compare
window is appended to the file name (`cost-<group>-<from>-<to>-vs-<cf>-<ct>.csv`).
Stopped-instance policy of the customer's price book applies exactly as in rating.

### 3.2 `GET /cost/summary` · `GET /customers/{id}/cost/summary`
The overview payload: `currency` (the reporting currency), `mixed_currency`,
`unconverted[{currency,records,cost}]` (the month-to-date list; the 30-day
series' when the month has none), `mtd{cost,from,to,days}`,
`forecast{month_end,run_rate_daily,trend_daily,method,days_observed,days_in_month,confidence,projection[{day,cost}],weekday_factors?}`
(same object as §3.1),
`last_month{period,cost}`, `prev_mtd{cost}` (same day count last month),
`mom_delta_pct`, `avg_daily_30d`, `resources_live`, `unpriced_skus`,
`customers{active,pending,suspended}`, `sources{verified,failed,pending}`,
`last_collected_at`, `daily[{day,cost}]` (30 days), `by_customer[{id,name,slug,cost,share}]`,
`by_kind[{key,label,cost,share}]`, `budgets[…status rows]`, `anomalies[…]`,
`statements{draft,issued,latest[…]}`. `GET /overview` returns the same document.

The global blocks belong to `GET /cost/summary` alone. On
`GET /customers/{id}/cost/summary` — the operator's customer page as much as a
customer principal's own — `sources` counts and `statements` lists only that
customer's, and `customers` is `{}`; the explorer blocks are already
customer-filtered. (The hw307 walk found the operator's customer page carrying
every customer's statements and every source's status.)

### 3.3 `GET /cost/export.csv`
Same params as explore; one row per (bucket, group).

### 3.4 `GET /resources` · `GET /customers/{id}/resources`
Params: `from`, `to`, `kind`, `region`, `status=live|stopped|deleted|all`, `q`,
`sort=cost|name|kind|first_seen|last_seen`, `order`, `limit`, `offset`.
Returns `rows[{source_id,resource_id,kind,name,region,customer_id,customer_name,
status,first_seen,last_seen,deleted_at,cost,currency,unconverted?,lines[{sku,unit,quantity,cost}]}]`,
`total`, `sum_cost`, `currency`, `mixed_currency`. `cost` and `currency` are the
reporting currency; a row whose book currency has no rate carries `cost` 0 and
`unconverted: true`, and `mixed_currency` says whether any row of the filtered
set is like that. `GET /resources/{source_id}/{resource_id}` adds `daily[]`,
`attrs`, `transitions`, `records_recent[]`.

### 3.5 Budgets
`GET|POST /budgets`, `GET|PUT|DELETE /budgets/{id}`, `GET /budgets/{id}/status?period=YYYY-MM`,
`GET /customers/{id}/budgets`. Budget: `{id,name,customer_id|null,amount,currency,
period:"monthly",thresholds:[50,80,100],notify_emails:[…],active}`. Status:
`{actual,forecast,pct_actual,pct_forecast,status:ok|warning|exceeded,
thresholds:[{pct,crossed,alerted_at}]}`. An hourly evaluator records a crossing
once per threshold per period (`budget_alerts`), writes an audit entry and mails
`notify_emails`.

A budget's `amount` is in the **reporting currency** (§3.10): `actual` is the
explorer's converted month total, so the cap must be in the same unit. `currency`
defaults to the reporting currency and any other value is refused with 400
naming it (`currency must be the reporting currency (OMR)…`).

### 3.6 `GET /anomalies` · `GET /customers/{id}/anomalies`
Params `from`, `to`. Daily cost per (customer, kind) is compared with the trailing
14-day mean/σ; a day is flagged when `z ≥ 3`, `actual ≥ 1.3 × mean` and the
absolute impact is at least 1 currency unit. Rows carry `drivers[]` — the SKUs and
resources whose Δ explains the spike.

Shipped shape (`internal/anomaly`, `internal/store/anomalies.go`, `internal/api/anomalies.go`):
the baseline needs ≥ 5 prior days with data (absent days are unknown, not zero);
σ = 0 makes z infinite, so `score` is capped at 99 (JSON cannot carry Inf); `actual`
is the ledger's exact day total, `expected`/`impact`/`score` are statistics. A driver's
`delta` is the day's cost minus the mean daily cost of the 7 calendar days before it,
per SKU and per resource, top 5 by |Δ|, zero deltas dropped. Default window = last
30 days; the summary block is the last 7 days, top 5 by impact.

### 3.7 `GET /recommendations` · `GET /customers/{id}/recommendations`
Rows `{type,severity,customer_id,customer_name,resource_id,resource_name,kind,title,detail,
monthly_saving,currency,evidence}`, `total_monthly_saving`, `currency` (the
reporting currency) and `unconverted[{currency,records,cost}]`. A saving is
computed from the customer's book rates and converted with the book currency's
exchange rate (§3.10); when the book currency has no rate the row keeps its
book currency, carries `evidence.unconverted = true`, is left out of
`total_monthly_saving` and is summed under `unconverted`. Types:
`stopped-instance-billed`, `unattached-volume`, `unbound-eip`, `low-cpu-utilisation`
(7-day mean < 10 % → one flavor step down), `unpriced-sku`, `stale-source`,
`no-price-book`. Savings = rate × 730 h.

Shipped rules (`internal/recommend`, inputs from `internal/store/recommendations.go`):
resource savings reuse the collector's `SKUsFor` (ECS `ecs.<flavor>`, EVS
`evs.<class>.gb × size_gb`, EIP `eip + eip.bandwidth_mbps × bandwidth_mbps`), exact
rational money. `stopped-instance-billed` fires only under `bill_stopped = compute` and
only when the flavor is priced (otherwise nothing is billed). `unbound-eip` uses
`attrs.status = DOWN` as the signal — Huawei reports an EIP bound to no port as DOWN —
and carries it as `evidence.status`. `low-cpu-utilisation` needs ≥ 48 hourly samples;
when the smaller SKU is not on the rate card the saving is half the current rate with
`evidence.estimate = true`. `unpriced-sku` covers customers that HAVE a book (a bookless
customer gets one `no-price-book` row instead) and never the `ecs.cpu_util` metric.
`stale-source` reasons: `failed`, `error`, `never-collected`, `stale` (> 2 h); sources of
non-active customers are dormant, not stale. Rows sort by `monthly_saving` desc, then
severity, type, id; ids are `type:customer:resource` / `type:customer:sku` /
`type:source` / `type:customer`.

### 3.8 CRUD gaps closed
- `DELETE /customers/{id}` — 409 while issued statements exist.
- `POST|PATCH /customers[/{id}]` accept `plan_slug` (`s|m|l|xl|flexi|""`,
  case-folded); every customer read carries it. A PATCH on an Organization
  customer is 400 — its plan is read from the Organization CR (§2.8).
- `DELETE /pricebooks/{id}` (409 while assigned) · `POST /pricebooks/{id}/clone {name}` ·
  `PATCH|DELETE /pricebooks/{id}/items/{sku}` · `GET /pricebooks/{id}/export.csv` ·
  `GET /pricebooks/{id}/coverage`.
- `GET|POST /discounts` (global list; `customer_id` null = all customers) ·
  `GET|PUT|DELETE /discounts/{id}`; existing customer-scoped routes kept.
- **Sources carry the price book (§2).** `GET /sources` is the operator-wide
  directory (every source with `layer`, `price_book_id`, `price_book_name`,
  `internal` and its customer; `?internal=true` adds the Sovereign's own
  internal source). `GET|PATCH /sources/{id}` and
  `GET|PATCH /customers/{id}/sources/{sid}` read and edit one source:
  `region`, `project_id`, `scope_token`, `domain_id`, `price_book_id`.
  A book whose `scope` is not the source's `layer` is **400**
  `price book scope <X> does not match source layer <Y>`, and nothing else in
  the patch is applied; an unknown book id is 400, not 500; `""` clears the
  book. A customer-admin may still change `scope_token` only. The internal
  source is never edited through the API (400).
  `POST /customers/{id}/sources` accepts `price_book_id` alongside the kind,
  and offers **cloud kinds only** (`huawei-project`, `file`) — platform
  sources are created by the Organization sync.
- `POST /pricebooks` accepts `scope` (`cloud` | `platform`, default cloud);
  `GET /pricebooks` and `GET /pricebooks/{id}` return it. `PUT` may change it
  only while no source is assigned (409 otherwise).
  `GET /pricebooks/{id}/coverage` returns `scope`, the assigned `sources`
  (`{source_id, customer_id, customer_name, customer_slug, label, kind,
  layer}`), the distinct `customers`, `skus_in_use` (each with
  `not_sold_per_use`), `coverage_pct`, `unpriced_count` and `not_sold_count`.
  `DELETE /pricebooks/{id}` is 409 while any SOURCE is assigned, with
  `details.sources` and `details.customers`.
- **`POST|PATCH /customers` no longer accept a price book.** The
  `price_book_id` key is still decoded and **ignored** with a log line — never
  an error, so an older client is not broken — and `customers.price_book_id`
  is never written again (§4.1). Every customer read carries
  `cloud_source_count` and `platform_source_count`.
- `POST /statements/run` answers **400** when one customer's sources are
  assigned books of different currencies (§2.10).
- The explorer and summary documents carry `not_sold_per_use[]` beside
  `unpriced` / `unpriced_skus` (§2.6).
- `GET /statements` — the operator list, newest period first. `period=YYYY-MM`
  narrows to one period and `customer_id=<id>` (alias `customer`) to one
  customer; both may be given. An id no customer has answers an empty list,
  not 404 — the filter selected nothing. Customer principals always get their
  own list, whatever they ask for.
- `DELETE /statements/{id}` — drafts only.
- `GET|PUT /allocation/settings` — `{weights{vcpu,mem_gib,pvc_gb},overhead_policy:
  separate|distribute,pool:sovereign-cost|manual,manual_amount,currency,
  sovereign_customer_id}`; `currency` is the **reporting currency** of the whole
  service (§3.10; the field keeps its name and place). `GET /allocation` returns
  rows with `allocated_cost`, `rated_revenue`, `margin`, `margin_pct` (all in the
  reporting currency), plus `pool`, `totals` and `unconverted[…]` — priced
  usage the pool or revenue query could not convert.
- `GET|POST /views`, `DELETE /views/{id}` — saved explorer views per user.

### 3.9 Scheduled reports + statement mail
`GET|POST /reports/schedules`, `GET|PUT|DELETE /reports/schedules/{id}`,
`POST /reports/schedules/{id}/send` → `{sent_to, subject, window_from, window_to, delivery}`,
`GET /reports/schedules/{id}/preview` → `{subject, body, window_from, window_to, recipients}`,
`GET /reports/schedules/{id}/deliveries`, `GET /customers/{id}/reports/schedules`.
Schedule: `{id, name, customer_id|null, cadence: daily|weekly|monthly, day_of_week
(0=Sun..6, weekly), day_of_month (1..28, monthly), hour_utc, recipients[≤20],
sections ⊆ {summary, services, customers, budgets, anomalies, recommendations},
active, last_sent_at, next_at, sent_30d, failed_30d, last_error}`. Reads follow
the session scope; the operator writes any schedule, a customer-admin only its
own customer's (customer_id forced server-side), a viewer none.

The window a send covers is implied by the cadence at send time: daily =
yesterday, weekly = the last 7 complete days, monthly = the previous calendar
month; today is never included. `internal/report` builds the document from the
same store calls the explorer, budgets, anomalies and recommendations endpoints
use (never HTTP) and renders it as ≤ 78-column plain text: window label
("1–7 Sep 2026"), total vs the previous period, month to date + forecast,
top 5 services (cost, share, Δ), top 5 customers (operator scope only), budget
standings, anomaly count + biggest, recommendation count + total saving, unpriced
SKUs, console link. A golden test pins the text. The scheduler polls every
5 minutes (first poll one minute after start); `ClaimReportRun` is a
compare-and-set on `next_at`, so two replicas never mail one due instant twice.
Every attempt is a `report_deliveries` row; a failure records `ok=false, error`
and still advances `next_at` (no retry storm); audit `report.sent` /
`report.failed`.

`POST /statements/{id}/issue` takes an optional `{notify: bool}` (default
true). On the draft → issued transition only — issuing stays idempotent — the
customer's `admin_email` and every `customer_users` admin receive a plain-text
statement summary (period, list → discount → net → tax → total, largest lines,
link `PUBLIC_URL/statements/<id>`); audit `statement.notified {recipients}`.
A re-issue never mails again.

### 3.10 Currency rates — `GET /currencies`, `GET|PUT|DELETE /currencies/{code}`
Operator-only. Price books carry a currency and customers' books may differ;
like a cloud console, every cost surface reports in **one reporting currency**
with stored exchange rates. The reporting currency is
`allocation_settings.currency` (the field keeps its name; PUT
`/allocation/settings` changes it).

`GET /currencies` → `{reporting_currency, rates:[{code, per_base, source,
updated_at}]}`. `PUT /currencies/{code} {per_base}` (optional `source`, default
`manual`) creates or replaces a rate; `code` must be three letters (400
otherwise), `per_base` a number > 0 (400), and the reporting currency itself is
refused with 400 `reporting currency …: its rate is 1 by definition`. `GET
/currencies/{code}` reads one rate (the reporting currency answers `per_base` 1,
`source: reporting`); `DELETE` removes one (404 when absent). Every write is
audited as `currency.rate` `{code, per_base | deleted, previous_per_base?,
source}`.

`per_base` is how many units of `code` **one unit of the reporting currency**
buys: 1 OMR = 2.6 USD → `USD 2.6`. The priced ledger (`store/cost.go`
`costBaseSQL`) carries, next to `cost` in the book currency,

    cost_base = cost / per_base(book currency)      per_base(reporting) = 1

and every reader — explorer, summary, resources, anomalies, budgets (`actual`),
allocation (pool, revenue), reports — sums `cost_base` and reports `currency =
<reporting>`. Recommendation savings are converted the same way on the Go side
(`store.ToBase`). A record whose book currency has no rate has `cost_base NULL`:
it is **left out of every total** and counted, per currency, in the document's
`unconverted[{currency, records, cost}]` list (cost in that currency), with
`mixed_currency = true`. Nothing is ever summed across currencies. Changing the
reporting currency does not rewrite the stored rates — they are relative to the
new one from that moment, and the former reporting currency is unconverted until
a rate for it is entered.

**Statements are not converted.** A statement is issued in the customer's
price-book currency — that is the bill the customer pays — so `statements`
and `rated_lines` keep the book currency and the explorer ↔ statement
reconciliation holds record for record within one book. Price books keep
their own currency too.

## 4. Data model additions (migrations 6+)

```
budgets(id, name, customer_id NULL, amount numeric, currency, period, thresholds int[],
        notify_emails text[], active, created_at, updated_at)
budget_alerts(id, budget_id, period, threshold, actual numeric, at)   UNIQUE(budget_id, period, threshold)
allocation_settings(id=1, weights jsonb, overhead_policy, pool, manual_amount, currency,
        sovereign_customer_id NULL, updated_at)
saved_views(id, owner_email, name, page, params jsonb, created_at)
discounts.customer_id → NULLABLE (global campaigns)
INDEX usage_records (window_start, sku); INDEX usage_records (customer_id, resource_kind, window_start)
report_schedules(id, name, customer_id NULL, cadence, day_of_week NULL, day_of_month NULL, hour_utc,
        recipients text[], sections text[], active, last_sent_at, next_at, created_at, updated_at)
report_deliveries(id, schedule_id, sent_at, window_from date, window_to date, recipients text[],
        subject, ok, error)
currency_rates(code TEXT PK CHECK '^[A-Z]{3}$', per_base NUMERIC(20,10) CHECK (> 0),
        source TEXT DEFAULT 'manual', updated_at)        -- allocation_settings.currency is the reporting currency

-- Two-layer ownership (§2), one migration:
cost_sources.layer      TEXT NOT NULL GENERATED ALWAYS AS
                        (CASE WHEN kind IN ('huawei-project','file') THEN 'cloud' ELSE 'platform' END) STORED
                        CHECK (layer IN ('cloud','platform'))
cost_sources.price_book_id UUID NULL REFERENCES price_books(id)
cost_sources.internal   BOOLEAN NOT NULL DEFAULT false
cost_sources.customer_id → NULLABLE          -- the internal source has no customer
cost_sources.kind       += 'openova-platform'
CHECK ((internal AND customer_id IS NULL AND kind = 'openova-platform')
       OR (NOT internal AND customer_id IS NOT NULL))
UNIQUE INDEX (kind, region, project_id) WHERE customer_id IS NULL   -- one internal source per slug
usage_records.customer_id → NULLABLE          -- the internal source's rows carry none
price_books.scope       TEXT NOT NULL DEFAULT 'cloud' CHECK (scope IN ('cloud','platform'))

-- Capacity (§11), one migration appended last:
capacity_regions(id, code UNIQUE lower-case, name, cloud_source_kind IN ('huawei-project','file'), created_at)
capacity_zones(id, region_id → regions CASCADE, code lower-case, name, is_default, created_at)   UNIQUE(region_id, code)
        UNIQUE INDEX (region_id) WHERE is_default                       -- one default zone per region
capacity_pools(id, zone_id → zones CASCADE, family CHECK IN (the seven families), total NUMERIC(20,6) >= 0,
        reserved NUMERIC(20,6) >= 0, source DEFAULT 'manual', note, updated_by, updated_at)   UNIQUE(zone_id, family)
capacity_pool_history(id, pool_id → pools CASCADE, total, source, note, changed_by, changed_at)
sku_footprints(sku, family CHECK, amount NUMERIC(20,6) > 0, source DEFAULT 'manual', updated_at)   PK(sku, family)
        -- seeded from the National Cloud list (capacity.Seed), source = 'seed'
sku_caps(zone_id → zones CASCADE, sku, total NUMERIC(20,6) >= 0, updated_by, updated_at)   PK(zone_id, sku)
```

### 4.1 Migrating the customer-level price book

One migration (`store.MigrationTwoLayerSources`), schema and data in one
transaction, idempotent, and pinned end to end by
`TestIntegrationTwoLayerMigrationMovesBooksOntoSources`, which stands a
database at the PREVIOUS shape, writes the rows the old model wrote, applies
the migration and asserts every clause:

1. The plans book (`EnsurePlanBook`'s name) becomes `scope = platform`; every
   other book is `cloud`.
2. `customers.price_book_id` is **copied onto each of that customer's sources
   whose layer matches the book's scope** — a platform book therefore never
   lands on a cloud source, which stays bookless rather than mis-rated.
3. Platform sources still without a book get the plans book.
4. The customer row whose Organization is the Sovereign's own (found by the
   `openova-org` source carrying `tier: platform-overhead` usage — the way
   OrgSync identifies it) becomes `kind = 'external'`, `org_slug NULL`,
   `plan_slug ''`; its `openova-org` source becomes `kind='openova-platform'`,
   `internal = true`, `customer_id = NULL`, and its usage rows lose their
   customer. Its cloud sources stay with it: the landlord is a plain customer.
5. `customers.price_book_id` is **kept as a deprecated column**: the API stops
   writing it and the UI stops showing it, and the JSON key stays for
   compatibility. Nothing reads it for rating any more.

At runtime the same conversion happens on the first sync of the internal
Organization (`OrgSync.syncInternalOrganization` →
`RetireOrganizationCustomer`), for a database that had not yet collected
overhead usage when the migration ran.

Cost is computed at query time by joining `usage_records` to the customer's
price book — no rollup table, no second source of truth, and a price change is
visible immediately. Measured shape on hw307 (72 k rows, 42 MB) aggregates in
tens of milliseconds; the two indexes above keep month-scale windows there.

## 5. Charts

A dependency-free SVG chart set in `ui/src/components/charts/` — stacked bars,
line (with dashed forecast segment), area, donut, ranked horizontal bars,
sparkline, waterfall, progress bar — sharing one 12-colour palette, a hover
tooltip, a legend, and a `formatMoney` axis. No chart renders an empty frame:
absence of data is stated in words, never drawn as zero.

## 6. Verification standard

- Go unit tests on every computation (forecast, anomaly scoring, recommendation
  rules, allocation with weights/overhead policies, discount waterfall) with
  discriminating cases (a mutant that swaps two passes must fail).
- Integration tests against Postgres for every new query, including scope leaks
  (customer A cannot read B's cost) and the explore ↔ statement reconciliation:
  explore total for a period == statement subtotal for that period.
- A **wire-contract test**: the Go overview/explore handlers write a golden JSON
  fixture; the UI's parsing tests read that fixture — the class of bug that
  produced the zeros cannot recur silently.
- UI: vitest on data mappers; a rendered walk on hw307 with screenshots for
  every page in §2, recorded in `docs/ledger/UAT.md`.

## 7. Synthetic history for showcases

Everything above describes surfaces that are only convincing against data with
a past. A freshly provisioned Sovereign has none: the explorer draws one
bucket, the anomaly detector has no baseline to judge against, no budget has
ever crossed a threshold and the statements list is empty. `cmd/seed-history`
(EPIC #6867, founder direction 2026-09-08) manufactures that past —
1 June to 1 September 2026 at hourly granularity, for six customers who are
all decommissioned before the window closes, so **the real data from
2 September stands alone** and the showcase customers read as having been
moved off, deleted or decommissioned. §7.1 covers the second half of the job:
giving the Sovereign's own landlord customer a past that converges on its real
present, so the series joins the two instead of jumping between them.

**Where it writes.** Through the product's own surfaces wherever they exist —
`POST /customers`, sources, price books, discounts, budgets,
`POST /statements/run`, `POST /statements/{id}/issue` with `notify:false` — so
every invariant, validation and audit entry is the product's own rather than
this tool's imitation of it. Three things have no endpoint, because the
collectors write them and nothing else does: the usage ledger, the resource
inventory, and the `created_at` / `issued_at` timestamps that make the history
read as history. Those go through `internal/store` — `UpsertUsage`,
`UpsertInventory`, `SetInventoryBounds` — which is the same code path the
Huawei and platform collectors take. That is why the command needs a `--dsn`
as well as a `--base-url`, and it is the same split
`tests/e2e/chargeback/seed.sh` already uses.

**Determinism.** Every quantity is a function of `(seed, customer, resource,
hour)` alone, hashed with FNV-1a into a PCG stream — never of generation order
and never of the window asked for. Two runs with the same seed produce
identical bytes; a run over a narrower window agrees with the wider one on
every shared hour. That is what makes the command idempotent rather than
merely re-runnable: usage upserts on `(source, resource, sku, window_start)`,
so a second pass rewrites the same rows with the same values. Measured on a
full local run: a re-run changed no row count and no metered quantity across
291,343 records.

**Marking.** Customers and sources are named `demo-*`, discounts and budgets
`demo: *`, and every usage record and inventory row carries
`{"synthetic":"true"}` in its labels. `--purge` deletes exactly what those
selectors match — five of them, the fifth being the source NAME, which is the
only one that can reach the landlord backfill of §7.1 (it hangs off a real
customer). Two findings from building it, both now covered by the purge:
`audit_log.customer_id` carries **no foreign key**, so deleting a customer does
not cascade to its audit trail and left 111 orphaned rows behind; and the audit
log is append-only by design, so writing the decommission note unconditionally
stacked a second copy on every re-run. The selectors are pinned by test against
real names taken from live databases — a customer named `acmewalk307`, a
discount named `demo`, a budget named `demonstration cap` — none of which the
purge may touch. Price books are never purged at all: `"National Cloud list
2026"` priced the real August 2026 statement on hw307, and a showcase must
never move a real rate.

**Rates.** The cloud rate card is **resolved, never minted** (§7.2). The eight
hourly rates in `internal/synth` are only the fallback used when a card has to
be created; they reproduce to the last decimal the ones the hw307 book rated
the real August statement with (`docs/sessions/2026-08-31/chargeback-walk/
statement-2026-08.csv`), and a test pins them. The plan SKUs are priced by the
product's own `store.EnsurePlanBook`, so a showcase Organization is billed at
exactly the platform's rate, and the `k8s.*` meters stay unpriced — an
Organization's bill is its plan and nothing else, which §2.8 requires and a
test asserts.

**What the scenario demonstrates.** A worker pool scaling 6 → 10 on a weekly
rhythm; a three-day migration in which two ECS generations overlap; a promo
weekend; a bandwidth anomaly on 22 August that the product's own
`internal/anomaly` rule flags at z = 12.25 through `GET /anomalies`; a 1,800
OMR budget that reaches 50 % in June, 80 % in July and 100 % in August; plan
upgrades and a downgrade, each splitting the switch day into exactly 24
plan-hours; a suspended Organization that pays its plan and meters nothing
else; and eighteen statements issued on the first of the following month.

One deliberate tension is recorded rather than hidden. The founder asked for
each cloud customer to bill 1,500–4,000 OMR a month *and* for the 1,800 OMR
budget to reach 80 % in July and 100 % in August. Those cannot both hold in
June: 1,500 is 83 % of 1,800, so any June inside the band already crosses the
80 % threshold and flattens the escalation the budget exists to show. The
escalation wins; Gulf Retail's June is 1,418 OMR, 5 % under the band, and the
test that checks the band names that month as the exception and why.

### 7.1 The landlord backfill — continuity across the join

Six invented customers and nothing else left two visible defects in the very
chart the showcase exists to fill (founder, 2026-09-10, looking at the hw307
overview): *"step 1st is empty and the actual usage was already there from the
beginning, you failed to show the continuity"*. Both were real.

**The hole.** The showcase window ends at midnight on 1 September; the real
collection on hw307 begins at **2026-09-02 10:21:09Z**, when the Sovereign was
provisioned. Nothing was written for 1 September or the morning of the 2nd, so
the daily series carried an empty bucket in the middle.

**The jump.** The Sovereign's own landlord customer had no past at all, so the
series stepped from ~150 OMR a day of showcase customers to ~594 OMR a day of
real usage in a different service mix, from one bucket to the next. (About
494 of those 594 OMR were, it turned out on 10 September, a reservation the
cloud never billed — see *Traffic, not reservation* below.) Six
customers appearing and vanishing against a platform with no history reads as
"nothing existed, then everything appeared" — the opposite of the story.

The fix is a synthetic past for the landlord that **converges on its real
present**, so the join is invisible rather than merely covered. The end state
(`synth.LandlordEndState`) is the measured shape of that customer's real
cloud-layer usage sampled on 5 September 2026, its Elastic-IP billing shape
re-read on 10 September — 10 `m7n.2xlarge.8` and 2 `m7n.xlarge.8`, six
Elastic IPs billed by outbound traffic (§8.2), 102 volumes totalling 2,281 GB,
two NAT gateways, two load balancers — and the backfill grows into exactly that
across two steps (1 July, 1 August) and a storage ramp, then stops at the hour
boundary before the first real record. Priced on the National Cloud list the
final full day is 102.23 OMR against 99.69 for a real day of that shape on the
operator's card without the reservation line: a seam 2.55 % wide, the whole of
it the known `nat.1` rate gap (2.54 OMR a day), pinned by test at 3 % with a
second check that the day *minus* its NAT line sits under the measured figure.

**Reservations do not move; traffic does.** An instance-hour, an address's
hourly fee, a NAT gateway, a load balancer and each volume's size are billed
for existing, so each is constant per resource per hour and steps only when a
resource is added. Only the storage TOTAL drifts, and only because volumes are
created (a 32-step staircase tracking a straight line from 1,400 to 2,281 GB).
Jittering any of them would make the data contradict the billing model it is
there to explain; the tests in `internal/synth/landlord_test.go` hold the
line.

**Traffic, not reservation (10 September).** The first revision of the
backfill mirrored what the pre-0.1.26 collector had recorded: every address
billing its reserved size, `eip.bandwidth_mbps` × hours — 3 × 300 + 3 × 100 =
1,200 Mbps, about 494 OMR a day, 83 % of the bill. That collector could not
read the charge mode (§8.2). The 0.1.26 one does, and on hw307 **all six** of
the landlord's billable addresses are `bandwidth_charge_mode = traffic`,
`bandwidth_share_type = PER`: the cloud bills their outbound gigabytes and
reserves no pipe. The 494 OMR was therefore a charge the cloud never made, and
the backfill had drawn it as a band through the whole showcase with a cliff at
the hour the new collector stopped writing it. The first hourly `eip.traffic_gb`
samples (10 September, 08:00–10:00Z) read 0.013–0.175 GB per address per hour,
about 0.1 on average, roughly 2.4 GB across the six in three hours.

Each address now meters `eip.traffic_gb` (unit `gb`) and no reservation. The
hourly volume is a gauge on a daily profile in Oman local time — about 0.03 GB
through the night, 0.12 across the working day, a 0.20 peak at 20:00 — at 70 %
on Friday and Saturday, jittered ±30 % per (seed, address, hour), the three
300-Mbps addresses carrying twice the 100-Mbps ones with the six weights
averaging to exactly 1, so the roster's total is the profile times six.
Measured on the generated week of 8 August: 96.5 GB across six addresses
against 6 × 0.1 × 168 = 100.8 (4 % off, pinned at 25 %), a big/small ratio of
2.00, and a smallest hour of 0.0098 GB — the curve's floor is a night hour on a
small address on a weekend at the bottom of the jitter band, never zero. The
inventory row keeps `bandwidth_mbps` and adds `bandwidth_charge_mode` and
`bandwidth_share_type`, so it reads like the collector's. Convergence for a
gauge is the same *shape* at the cut, not the same number: the end-state test
checks the traffic line's count and unit exactly and its quantity within the
jitter band, and every reservation exactly. `eip.traffic_gb` stays unpriced,
as §8.2 requires; it sits on both sides of the seam and adds to neither.

**Neutralising the reservations the cloud never billed.** Correcting the
backfill left the real ledger holding every `eip.bandwidth_mbps` row the old
collector wrote for those addresses. `seed-history --neutralise-reservations`
(on by default, part of the landlord step, so `--only landlord` runs it alone)
removes them: for every non-synthetic address of the landlord whose inventory
says `bandwidth_charge_mode = traffic`, the `eip.bandwidth_mbps` usage rows are
deleted, the count and the window removed are logged, and one entry on the
landlord's audit trail (`eip.reservation.neutralise`) says what went and why.
Every clause of the selection is a refusal: an address on charge mode
`bandwidth` really reserves its pipe and keeps every row; an address with no
charge mode — an older gateway that does not publish the bandwidths API — is
left exactly as it is, for the same reason the collector keeps billing it; the
`eip` fee and the `eip.traffic_gb` meter are the cloud's real charges and stay;
another customer's addresses and any synthetic row are out of reach. A pass
that finds nothing writes nothing, so a nightly re-run stacks no entries. The
neutralisation is a correction of the real ledger and is deliberately **not
reversed by `--purge`**: the rows were never billable, and the audit entry
carries no synthetic mark, so both survive a purge.
`TestNeutraliseRemovesOnlyTrafficBilledReservations` runs the three charge
modes, a synthetic address, a second customer, a second pass and a purge
against the real schema. A statement the operator issued over those hours is a
financial record and stands.

**What it refuses to write.** The landlord is a REAL customer, and three rules
follow, each one a rule about not writing: the customer is found and read but
never created, patched, suspended, audited or backdated (measured: its row is
byte-identical after a run); no statement is ever run or issued for it, because
issuing one would put a bill in front of somebody over invented data; and its
rows go on their OWN source, `demo-<slug>-history`, carrying the same price
book as the customer's real cloud source so both halves of the series are rated
identically. Same customer means the explorer grouped by customer draws one
continuous series; separate source means a purge removes exactly the backfill.
A resource still metering in the last hour is handed over **alive** — no
`deleted_at`, because it did not go away, the real collection took it over.

Hanging the backfill off a real customer also exposed a gap in the purge. Its
source step reached sources only through `customers.slug LIKE 'demo-_%'`, which
by construction can never match the landlord: the labelled usage and inventory
rows would have gone, and the `demo-hw307-omani-works-history` source itself
would have stayed behind forever — an orphan `demo-` source on a live customer,
counted in its source directory and its verified-source count.
`synth.SQLSourcePredicate` (`project_id LIKE 'demo-_%'`, paired with
`NOT internal` so the Sovereign's own platform source stays out of reach) is
the second door, pinned by test against real project ids taken from live
databases.

The cut is **discovered, never assumed**: the earliest non-synthetic
`usage_records.window_start` for that customer, truncated to the hour. A row
written at or after it would double-bill an hour the collector already owns.
`--landlord-until` overrides the discovery for a dry run or a database whose
ledger cannot answer; `--landlord ''` disables the backfill entirely.

Measured end to end against a scratch Postgres with control rows standing in
for the real half, under the reservation model the backfill mirrored at the
time: 98 daily buckets from 1 June to 6 September, **not one of them empty**,
and the landlord's last synthetic day and first full real day both 594.09 OMR
— a 0.00 % seam. A purge then removed 545,274 synthetic usage rows, 257
inventory rows, 7 sources and 6 customers while leaving the landlord, its real
source and all 14,300 real rows untouched. Under the traffic model the row and
resource counts are unchanged — each address still emits two lines an hour,
the meter in place of the reservation — and the seam at the new measured day
is held by the unit test; it has not yet been re-measured end to end on hw307.

### 7.2 Borrow a rate card, never mint one

The same look at hw307 found a second defect, and a worse one, because it moved
money rather than a chart. `seed-history` **created** a cloud book,
`"National Cloud list 2026"`, with the nine rates in `internal/synth`. The
operator's own card was already there, called `"National Cloud 2026 list"`,
with 134 items. One word apart, and not equivalent:

| | seeded card | operator's card |
|---|---|---|
| `nat.1` | 0.11322489 /hour | 0.06037935 /hour |
| `bill_stopped` | `compute` | `none` |
| items | 9 | 134 |

So the three showcase cloud customers were rated from one card and the real
customer beside them in the same console from another. A console whose whole
purpose is comparison was not comparing like with like, and the difference was
nearly double on the biggest of the fixed-fee SKUs.

The rule is now **borrow, never mint**, and the resolution order says what to
borrow (`cmd/seed-history/book.go`): `--cloud-book`, then the card the LANDLORD
is billed on — the principled default, since the whole point is to read the
showcase against the Sovereign's own usage — then the one cloud book whose name
reads as a National Cloud card (two is ambiguous and stops the run rather than
guessing), then a card an earlier run made, and only then a new
`"Showcase cloud rates (seed-history)"`, named so nobody has to guess whose it
is. Every run logs which rule answered and why.

Resolving the right card fixes tomorrow. hw307 also needed yesterday moved, so
a run repairs what an earlier one left: showcase cloud sources and customers
are re-pointed onto the resolved card, the showcase statements rated on the old
card are **dropped and regenerated** — a bill nothing can reproduce is worse
than no bill, and these are synthetic bills for customers that never existed —
and the duplicate is then removed. Three guards stand in front of that delete,
and each one leaves the book in place and says so: a source still assigned, a
customer still referencing it, or an ISSUED statement whose lines came through
a source on it. Only a card this tool is known to have made is ever a
candidate, whatever anything is called; the product's own
`DELETE /pricebooks/{id}` refuses on assigned sources as well, so the last word
belongs to the product rather than to this command. `--purge` is untouched: it
has never removed a price book and still does not.

Because the figures move, the run says so rather than letting a reader assume
the database changed under them. Measured on a scratch database that
reproduced the hw307 state exactly: the landlord's card resolved by rule (b),
three showcase sources re-pointed, eighteen statements dropped and re-rated,
the duplicate removed, and Gulf Retail's June moved 1,414.14 → 1,376.19 OMR.
The landlord backfill's own figures are read back from the explorer scoped to
its source, so the summary reports the product's number at the operator's
rates and never this command's arithmetic at the fallback ones — which is how
the seam above closed from 0.43 % to 0.00 %.

## 8. True metered billing — in-place resizes and the Elastic-IP meter

Two things were being billed on a shape rather than on what happened. Both
are corrected here, and both go through the one piece of window math the
service already had (`internal/window`) rather than a second copy of it.

### 8.1 A pod resized in place was billed at whichever size the emit saw

The platform collector tracked a pod's requested vCPU and memory as two
fields and every informer update overwrote them. There was no transition
boundary, so an hour was billed entirely at whatever value happened to be in
the map when the hour was emitted. With eviction-based autoscaling that is
accidentally correct — the pod is recreated under a new UID, so it is a new
resource with its own life — but Kubernetes in-place vertical scaling keeps
the UID, which is exactly what a Vertical Pod Autoscaler does on a recent
cluster. A pod that doubled at half past the hour billed the whole hour at
one of the two sizes, and which one depended on the emit schedule.

The cloud collector never had this problem: an ECS resize is a
`window.Transition` and `window.HourSlices` splits the hour at it, so a
resize is billed half at the old flavour and half at the new. The fix gives
the tracked pod the same thing. The size is encoded into the transition's
`Flavor` field as a token (`cpu=0.5,mem=1`), which is what lets the platform
collector reuse the cloud collector's math **unchanged** — to that math, a
pod growing from 500m to 1 CPU is an instance changing flavour. `EmitOrg`
then asks for the size in force per slice instead of reading the tracked
fields.

Three properties are pinned by test: a pod whose requests double at 30
minutes past the hour bills 0.25 + 0.5 vCPU-hours, which at 0.030000 per
vCPU-hour is 0.022500 and not the 0.030000 or 0.015000 the old code produced;
a pod recreated by eviction still bills as two lifecycles, one per UID; and a
workload that never resizes emits **byte-identical** records, compared
against the pre-change code path (an empty transition list) over the
serialised batch rather than a spot check. PVCs go through the same
mechanism, since a volume expansion is the same class of change.

A restart of the service loses the in-memory transitions and re-seeds each
tracked resource at its current size, which is the pre-change behaviour for
the hour in progress and no worse than it.

### 8.2 An Elastic IP was billed on a reservation it may never have made

`eip.bandwidth_mbps` — `bandwidth_size` × hours — is correct for a
fixed-bandwidth address, and it is why bandwidth dominates the bill on this
Sovereign. It was applied to every address, because the lister never read the
charge mode. Two shapes were therefore billed wrongly:

- an address the cloud bills **by traffic** reserves no pipe at all, so the
  charge was one the cloud never made;
- several addresses on **one shared pipe** each report the whole pipe's size,
  so the same reservation was billed once per address. Not happening on
  hw307 today — every `bandwidth_name` there is distinct — and nothing would
  have detected it if it started.

The billing shape lives on the bandwidth object, not on the address, so
`ListEIP` now joins `GET vpc /v1/{pid}/bandwidths` to the publicips listing
and stores `bandwidth_id`, `bandwidth_charge_mode` and `bandwidth_share_type`
as attributes. A shared (`WHOLE`) pipe also becomes a resource of its own,
kind `bandwidth`, keyed by the bandwidth id — registered in the lister
registry through the new `also` field so the deletion sweep and the "nothing
listed" check still derive from one list, which is what stops a released pipe
billing forever.

Billing is then either/or, driven off the captured mode:

| shape | hourly address fee | reservation | traffic |
|---|---|---|---|
| bandwidth-billed, dedicated pipe | yes | `eip.bandwidth_mbps` × size | — |
| traffic-billed | yes | — | `eip.traffic_gb` |
| on a shared pipe | yes | on the pipe's own resource, **once** | on the pipe |
| charge mode not reported | yes | `eip.bandwidth_mbps` × size | — |

The last row is deliberate. An older gateway that does not publish the
bandwidths API reports no charge mode, and every address there keeps billing
its reservation exactly as before — under-billing an address because its
shape is unknown would be as wrong as over-billing one. A bandwidths call
that fails for any *other* reason (a rejected credential, a 500) fails the
whole kind instead, because reading that as "this project has no bandwidths"
would mark every shared pipe deleted and silently stop billing it.

**The meter.** `eip.traffic_gb`, unit `gb`, one record per hour, written by
the same `SampleCES` path that already writes `ecs.cpu_util`. It reads CES
`SYS.VPC` / **`up_stream`** — Huawei's *Outbound Traffic* — dimensioned by
`bandwidth_id`, `period=3600`, `filter=sum`. Outbound is the direction the
cloud charges for (inbound is free), so it is the only one read. The
dimension is the bandwidth rather than the address because that is the object
the cloud meters: a dedicated pipe is exactly one address, and a shared pipe
is metered once for all of them — the same "count the pipe once" rule the
reservation follows.

`up_stream` is **not** a cumulative counter. Each raw point is the number of
bytes that left during its own one-minute interval, so the hour is the SUM of
the points inside it and no delta between readings is taken; `filter=sum`
asks for exactly that and the answer is divided by 10⁹ — decimal gigabytes,
because network traffic is sold per 10⁹ bytes and using 2³⁰ would
under-report every hour by about 7 %. A gateway that answers with only an
`average` is reporting the mean of those per-minute totals, and the hour is
that mean × the 60 raw intervals in the period. Both conversions are stated
in the code because reading a mean as a total under-reports by a factor of 60
on the one meter charged per gigabyte.

**The measurement is not always a meter.** The same outbound traffic on a
*reservation*-billed address is something the cloud charges nothing for, so
it is written under a separate SKU, `eip.traffic_gb.observed`, which is a
metric and never rated — the `ecs.cpu_util` precedent. That separation is
what keeps "either the reservation or the traffic, never both" true even
after a rate is added for the meter: a rate on `eip.traffic_gb` can only ever
reach addresses the cloud really bills by traffic. The list of such metrics
now lives once, in `internal/store/metric_skus.go`, rendered both as Go data
and as the SQL tuple every aggregate filters on; it used to be four
hand-written copies of one literal across the cost CTE, the rating aggregate,
the overview aggregate and the boundary-recompute delete, and adding a second
metric to three of those four would have left it billable in the fourth.

**Rates.** `eip.traffic_gb` ships **unpriced**. No National Cloud traffic
price is invented here, and the product already reports unpriced usage
honestly — the SKU will appear in the unpriced-SKU recommendation until the
operator enters the rate their contract actually carries. Until then a
traffic-billed address rates to its hourly address fee alone. That is a
smaller number than the wrong one it used to produce, and it is visible
rather than silent.

### 8.3 The oversized-reservation recommendation

The rule engine could only flag an address bound to nothing. On a Sovereign
where reserved bandwidth is about forty times compute, the larger money is in
pipes that *are* in use and are far wider than anything that crosses them.
`oversized-bandwidth` reports a reservation whose reserved size exceeds the
busiest hour observed over the window by at least a factor of four, and names
the size to drop to.

It sizes against the **peak** hour, not the mean: a pipe has to carry the
busy hour, and a suggestion sitting on the average is an outage waiting for
the next one. The peak hour's gigabytes convert to an average throughput at
1 GB/h = 2.222 Mbps, that is doubled for headroom, and the result is rounded
**up** to a size an operator can actually buy (1, 2, 5, 10, 20, 50, 100, 200,
300, 500, 1000, 2000 Mbps). Two days of hourly samples are required before
any of it is trusted, the same bar the CPU rule uses. A traffic-billed
address is skipped — it already pays only for what it moved — and for a
shared pipe the row is the pipe, since that is where the reservation and the
fix both are. The saving is `rate(eip.bandwidth_mbps) × (reserved −
suggested) × 730 h`, exact rational money rounded once, like every other
saving; with no rate on the card the row still names both sizes and carries
`unpriced: true` rather than inventing a price to make the number look big.

Measured against the shape hw307 actually has — a 300 Mbps pipe whose busiest
hour moved 1.2 GB (2.67 Mbps) — the rule suggests 10 Mbps and, at the
National Cloud list rate of 0.005 per Mbps-hour, a saving of 1,058.500 OMR a
month for that one address.

---

## 8. Post-paid invoicing and the commercial model (founder direction 2026-09-10)

> *"in many cases omantel corporate customers are charged through invoicing and
> they are being paid by the customer post paid approach through raising POs
> etc. so where do these types of customers fall into. And when it comes to the
> SME cloud customers at the end we need to get integrated with the omantel
> payment gateway instead of stripe, where do those customers fall under?"*

They fell nowhere, and the reason is worth stating plainly.

### 8.1 Why the three billing modes were retired

`billing_mode` was one column with three values — `showback`, `chargeback`,
`real` — and it was answering three different questions at once:

- Is anything collected at all? (`showback` said no.)
- When is it paid? (nothing said.)
- How does the money move? (`chargeback` implied internally, `real` implied
  Stripe, and neither said so.)

Three labels over three orthogonal questions cannot cover the cases. A
corporate customer invoiced on thirty-day terms against a purchase order is
*real* money, but it is neither Stripe nor an internal recharge — under the old
model it had to be filed as `real` and then collected by hand outside the
product. An SME that must pay through Omantel's gateway was `real` too, but
`real` meant Stripe by construction, because the one place money moved was a
Stripe-backed hook wired to that value. The labels could not be extended
either: a fourth mode would have been a fourth label over the same three
questions.

So the three questions are now three fields, and the answer to "where do those
customers fall" is a coordinate rather than a label.

### 8.2 The four fields

| Field | Values | Meaning |
|---|---|---|
| `charging` | `billed` · `informational` | Is anything collected? `informational` is what showback meant: statements exist for visibility and nothing is ever collected. |
| `payment_model` | `prepaid` · `postpaid` | `prepaid` settles an invoice from the customer's balance at issue, and service depends on that balance. `postpaid` leaves the invoice due on terms, net of any credit the customer chooses to apply. |
| `payment_method` | `gateway` · `transfer` · `internal` | How the money moves. `gateway` = a pluggable payment gateway collects. `transfer` = bank transfer against the invoice and its purchase order, recorded by the operator. `internal` = a cost-centre recharge; no external money. |
| `gateway_name` | e.g. `stripe` | Which gateway collects. Only when `payment_method = gateway`. |

Plus the terms an invoice needs: `po_reference` (the customer's standing
purchase order) and `payment_terms_days` (net terms, default 30; `0` is due on
receipt).

**Account credit is universal, not a property of `prepaid`.** A postpaid
customer may top up as well, and later choose to pay some invoices out of that
balance. `payment_model` says only what happens *at issue* — whether the
invoice is settled from the balance and service depends on it, or left due on
terms. The top-up flow and the allocation of credit across invoices are a
separate lane; §8.6 describes the shape this one leaves for it.

The last three fields are meaningful **only when `charging = billed`**. That is
not a convention: the store validates the whole combination and the database
carries a `CHECK` that refuses an informational customer with a payment model,
a billed one without a method, a gateway customer with no gateway name, and a
non-gateway customer that carries one. Every refusal names the field.

The founder's two customers now have coordinates:

| Customer | charging | payment_model | payment_method | gateway_name |
|---|---|---|---|---|
| Omantel corporate, invoiced against a PO on terms | `billed` | `postpaid` | `transfer` | — |
| Omantel SME on Omantel's gateway | `billed` | `prepaid` | `gateway` | `omantel` |
| The same SME today, on Stripe | `billed` | `prepaid` | `gateway` | `stripe` |
| An internal department recharged | `billed` | `postpaid` | `internal` | — |
| A customer shown its cost and never billed | `informational` | — | — | — |

### 8.3 The migration, and `billing_mode` as a derived column

The migration maps the retired trio exactly:

| `billing_mode` | `charging` | `payment_model` | `payment_method` | `gateway_name` |
|---|---|---|---|---|
| `showback` | `informational` | NULL | NULL | `''` |
| `chargeback` | `billed` | `postpaid` | `internal` | `''` |
| `real` | `billed` | `prepaid` | `gateway` | `stripe` |

`billing_mode` is **kept as a DEPRECATED column**, derived from the four fields
on every write — `informational` → `showback`, billed + `internal` →
`chargeback`, anything else → `real` — so a reader written against it, and the
wire key it reads, keep working unchanged. It is never written directly by the
API: `POST /customers` and `PATCH /customers/{id}` decode `billing_mode` and
ignore it with a log line, exactly as they already do for the deprecated
`price_book_id`. The UI neither shows it nor sends it.

The same mapping is available in Go as `store.CommercialFromBillingMode`, and
`store.CustomerInput` / `CustomerPatch` route a legacy `BillingMode` through
it. That is how the **CSV importer** (whose documented columns include
`billing_mode`) and the **Organization sync** keep working with no change of
their own: OrgSync still reads `spec.billingMode` from the Organization CR and
still compares against the derived column, so the CR stays authoritative over
the coarse mode while a finer operator choice inside it — Stripe gateway versus
bank transfer — is left alone.

The derivation is deliberately lossy in one direction: a billed customer paying
by **transfer** also derives `real`. That is the case the three labels could not
express, which is the whole point; nothing downstream may use `billing_mode` to
decide whether to take money (§8.7).

### 8.4 A statement becomes an invoice

An issued statement now carries what an invoice must carry:

- **`invoice_number`** — assigned at issue, gapless per calendar year, unique
  across the table, formatted `<prefix>-<year>-<00001>`. The prefix is the
  billing setting `invoice_prefix` (default `INV`); changing it changes the
  *next* number only, because the ones already assigned are on documents the
  customer holds.
- **`po_reference`** — copied from the customer at issue, editable on the draft
  before then (`PATCH /statements/{id}`), frozen afterwards.
- **`payment_terms_days`** — the customer's terms, overridable per statement
  before issue.
- **`due_at`** — the issue date plus the terms, computed at issue.

**Gaplessness is a transaction property, not a counter.** The number is taken
inside the *same* transaction that flips the status, from a one-row-per-year
`invoice_sequences` table via `INSERT … ON CONFLICT DO UPDATE … RETURNING`.
That takes the year row's lock, so a concurrent issue blocks until this
transaction commits — which makes the sequence unique *and* leaves no hole when
a transaction rolls back. Measured: twelve concurrent issues produce 1…12 with
no duplicate and no gap.

### 8.5 The lifecycle

Legal transitions, enforced in the store rather than only in the UI:

| From | May become |
|---|---|
| `draft` | `issued`, `cancelled` |
| `issued` | `sent`, `paid`, `cancelled` |
| `sent` | `paid`, `overdue` |
| `overdue` | `paid` |
| `paid` | — final |
| `cancelled` | — final |

Anything else is refused with `ErrConflict` (HTTP 409) and a message that says
what the statement is now and what it could become instead. A **sent** invoice
is deliberately not cancellable: the customer holds it, and the correction for
that is a credit note, not a status flip.

**Overdue is DERIVED, never stored.** A statement is overdue when its stored
status is `sent`, its `due_at` has passed, and money is still outstanding. It is
computed on every read as `effective_status`, so it is true of the clock rather
than of the last sweep — there is no sweeper, no cron, and no window in which
the ledger is wrong. The stored `status` keeps its own name and meaning, and a
reader written before invoicing falls back to it.

### 8.6 Payments, the balance, and the seam for account credit

A payment is a **row of its own**. It belongs to the CUSTOMER and carries its
own amount, date, method, reference and status; it is *linked* to the invoice
it was recorded against rather than being a column on that invoice:

    payments(id, customer_id, statement_id NULL, amount, paid_at,
             method, reference, status, gateway, recorded_by, recorded_at)

`POST /statements/{id}/payments` creates one such row and links it to that
invoice, which is why `statement_id` is nullable: unallocated credit is a
payment with no invoice yet, and allocating one payment across several invoices
is an allocation table over these same rows. **This lane always sets the link
and builds neither the top-up flow nor allocation** — it leaves the shape so
that a later lane can add both without changing the wire.

- `paid_total` is the sum of the **received** payments linked to the statement
  and `balance` is `total − paid_total`, both computed on read with exact
  rational arithmetic — never float, never stored, so they cannot drift from
  the ledger they are read out of. A `pending` or `failed` payment is recorded
  and settles nothing.
- A **part payment** leaves the status unchanged and carries the balance; the
  payment that brings the balance to zero flips the statement to `paid` and
  stamps `paid_at` with the day that money arrived.
- **Overpayment is refused** (409, naming the outstanding amount). A customer
  who sent too much needs a credit note, not a bigger invoice.
- Both of those are judged **at the currency's minor unit** — three decimals
  for OMR and the other dinars / the rial, two for everything else — because
  money is added at six decimals but moves at the unit. An invoice of
  14.856782 part-paid by 10.000 leaves 4.856782, which no transfer carries:
  the 4.857 the dialog prefills is the settlement (paid, balance 0 — never
  negative), while half a unit or more over (4.858) is the overpayment that
  is refused. The arithmetic underneath stays exact; only the two decisions
  round.
- A **reference is unique per customer**, so a gateway that delivers the same
  confirmation twice books one payment.

### 8.7 The payment-gateway seam

`internal/settle` is the one place that knows how money is collected.

    type Gateway interface {
        RequestSettlement(ctx, Request) (Result, error)
        ConfirmSettlement(ctx, Confirmation) (Payment, error)
    }

**What an implementer must supply — the whole contract:**

1. `RequestSettlement(ctx, Request) (Result, error)`. Called once, on the
   draft → issued edge, for a customer whose `gateway_name` this
   implementation is registered under. `Request` carries the statement and the
   customer; the amount to collect is `Request.Statement.Total` in
   `Request.Statement.Currency`. Answer a `Result` whose `Outcome` is
   `settled` (money moved during the call), `pending` (the gateway will confirm
   later — set `PayURL` when the payer completes it on a hosted page), or
   `not-applicable` (nothing this gateway collects). It **must be idempotent on
   `Request.Statement.ID`**: issuing is idempotent, so a re-issue repeats the
   call and must not take money twice.
2. `ConfirmSettlement(ctx, Confirmation) (Payment, error)`. Called when the
   gateway — or the operator, for a transfer — says money arrived. Validate and
   normalise it into the `Payment` to record: exact amount, the day it arrived,
   the method, the reference that proves it, and its status.
   `settle.Normalise` does the standard checks. It records nothing itself: the
   caller books the payment through the store, which owns the lifecycle,
   refuses overpayment and carries a part-paid balance.
3. One line of wiring in `cmd/chargeback`:
   `settlement.Register("omantel", omantel.New(cfg))` — and the customers that
   gateway serves get `charging=billed`, `payment_method=gateway`,
   `gateway_name=omantel`. Nothing else in the product changes.

A gateway never touches the database, never decides whether a customer is
billable, and never writes a statement's status. Money in, normalised facts
out.

**Routing.** The registry resolves per customer: `charging != billed` → nothing
collects (not an error — an informational customer settles nowhere by design);
`payment_method` of `transfer` or `internal` → the built-in `settle.Manual`
gateway, which collects nothing and reports what to expect next; `gateway` →
the implementation registered under `gateway_name`, or an audited error when
this deployment has none, never a silent success.

**Stripe today.** The existing ADR-0014 D6 billing hook
(`internal/adapter/openova.BillingHook`) is registered under the name `stripe`
and is unchanged in behaviour: the same metering post to the platform billing
service, the same idempotency on the statement id, the same `duplicate=true`
handling. What changed is only *who calls it*. It now fires on exactly
**`kind = organization` AND `charging = billed` AND `payment_method = gateway`
AND `gateway_name = stripe`** — the four-field replacement for the old
`billing_mode = real`, and strictly narrower than it was, because a billed
customer paying by **transfer** also derives `real` and must never be debited.

The `kind = organization` guard is **load-bearing and stays**: the metering
payload's `customer_id` is the Organization slug, which is the only identifier
the platform billing service knows. An external customer has no billing account
there, so posting for one would be a debit against nothing.

**What is deliberately not modelled.** Omantel's gateway API, endpoints and
credentials are not specified in this repository, and inventing their shape
would be a guess dressed as an integration. What is specified is the seam it
plugs into and the two methods it must answer.

### 8.8 API

Operator-only, every transition audited:

| Route | Does |
|---|---|
| `PATCH /statements/{id}` | `{po_reference, payment_terms_days}` on a DRAFT; frozen once issued (409). |
| `POST /statements/{id}/send` | `issued → sent`. `{"notify": true}` also emails the invoice; the default is **false**, because an operator marking what they already sent should not put a second copy in the customer's inbox. |
| `POST /statements/{id}/payments` | `{amount, paid_at, reference}`. Goes through the customer's gateway `ConfirmSettlement` first, then the store creates the payment row and links it to this invoice. Part payment carries the balance; overpayment is 409. |
| `POST /statements/{id}/cancel` | `{reason}`; `draft` / `issued` → `cancelled`. |
| `GET /statements/{id}/payments` | The payment history, inside the session's scope — a customer may read what it has paid. |

The statement document carries `invoice_number`, `po_reference`,
`payment_terms_days`, `due_at`, `sent_at`, `paid_at`, `cancelled_at`,
`cancel_reason`, `paid_total`, `balance`, `effective_status` and `payments`.
Every one is additive and absent from a statement that never had it, so a
reader written against the pre-invoicing document keeps working. `status` keeps
its name and its meaning.

Customer create and patch accept `charging`, `payment_model`, `payment_method`,
`gateway_name`, `po_reference` and `payment_terms_days`; the customer document
carries them alongside the derived `billing_mode`.

`PUT /billing-settings` gains `invoice_prefix` — absent leaves the stored prefix
alone, so a client that only knows about the discount rule cannot reset it.

### 8.9 UI

- **Customer settings** ask the three questions as three labelled controls with
  one line under each saying what it means, and reveal the terms
  (purchase-order reference, payment terms) for a customer paid by transfer or
  internal recharge, or the gateway picker for one paid through a gateway.
  Switching charging off hides and clears what then has no meaning.
- **The customers directory** shows the position compactly — "prepaid · Stripe",
  "postpaid · transfer", "internal recharge", "informational" — with the terms
  underneath where they apply. The mode word is gone.
- **The statements list** filters over the whole lifecycle including the derived
  `overdue`, and shows the invoice number, the due date with "due in 12 days" /
  "9 days overdue", and the balance. Two KPIs answer the two questions an
  operator has: how much is overdue, and how much is outstanding.
- **The statement view** shows the invoice block (number, purchase order, terms,
  due date, paid of total), the payment history — a pending payment is listed
  and marked as settling nothing — and the actions the lifecycle allows from
  where it is: Mark sent, Record payment, Cancel invoice.

### 8.10 WHO invoices — the internal and external systems of record

Omantel already runs invoicing, payment and collections. So does any operator
of that size. This product must not become a second system of record beside
theirs: two systems numbering invoices for the same customer, two ledgers of
what was paid, and a reconciliation problem nobody asked for.

What this product is unambiguously good at is **mediation and rating**:
collecting usage, pricing it, and producing a rated bill. Invoicing, payment,
collections and the customer account belong to whichever system is the system
of record for the Sovereign.

That is one Sovereign-level setting, `billing_settings.commercial_provider`:

| | `internal` (default) | `external` |
|---|---|---|
| Numbers the invoice | this product, gapless per year (§8.4) | the operator's billing system; **we never number one** |
| Sends it to the customer | `POST /statements/{id}/send` | theirs |
| Records payments | our ledger (§8.6) | theirs, reported to us |
| Cancels it | `POST /statements/{id}/cancel` | theirs |
| We hold | `invoice_number` | `external_invoice_ref` |

`internal` is the default, so an upgraded Sovereign behaves exactly as it did
and everything in §8.1–§8.9 applies unchanged.

Both sit behind one interface, `commercial.InvoiceProvider` — Issue, Send,
RecordPayment, Cancel — so the API handlers do not branch on the setting and
neither does the UI.

**In external mode**, `POST /statements/{id}/issue` flips the statement to
issued and, in the SAME transaction, writes the rated bill to an **outbox**.
It assigns no invoice number and calls nothing. `send`, `payments` and
`cancel` answer **409, "owned by the external billing system"**, and the
customer's commercial fields and terms are read-only in the API and the UI
with a one-line note saying so. The one commercial field that stays writable
is `external_account_id`: it is how a rated bill is attributed over there, and
only we know which of our customers is which.

#### The outbox, and why the export is not synchronous

Rating and issuing must never wait on, or fail because of, a system in someone
else's estate. So issuing writes two rows in one transaction — the statement,
and a `commercial_outbox` entry carrying the document, `attempts`,
`next_attempt_at`, `delivered_at` and `last_error` — and returns. A delivery
loop drains it afterwards:

- **At-least-once**, keyed on `idempotency_key` (the statement id, unique per
  document type). The same document may reach the far end more than once, and
  the Exporter's contract is to make that one bill — by writing to a name
  derived from the key, or by sending the key as the receiver's idempotency
  header.
- **Exponential backoff**, one minute doubling to an hour, so a billing system
  down for an afternoon costs a delay and nothing else.
- A non-empty `externalRef` from the Exporter is stored on the statement as
  `external_invoice_ref`; the inbound webhook may also name it later, for a
  billing system that answers asynchronously.
- `GET /commercial/outbox` lists what is queued and why anything is stuck
  (`last_error`, `attempts`); `POST /commercial/outbox/{id}/retry` makes one
  row due now and pushes it immediately. A delivered row is refused rather
  than sent twice.

An export that cannot be delivered right now is therefore a row an operator
can see — never a bill that was silently not raised.

#### What an Exporter implementer must supply

    type Exporter interface {
        Deliver(ctx context.Context, doc InvoiceDocument) (externalRef string, err error)
    }

One method. Take the document, get it to the billing system, answer with the
reference that system will know it by (or `""` when it answers later), and
return an error if you could not — the row is retried and nothing is lost. The
transport is entirely the implementer's: a file the operator's own job
collects, an HTTP POST to a TMF678 endpoint, a message on a queue.

**`csvfile` is the one that ships.** It writes one CSV per bill into
`COMMERCIAL_EXPORT_DIR`, named by the reference derived from the idempotency
key — `2026-05-omantel-corp-1a2b3c4d.csv` — so a redelivery overwrites its own
file rather than duplicating the bill. Getting the file to the billing system
(SFTP, a mounted share, a pickup job) is the operator's concern and
deliberately not this product's.

#### The document, and the TM Forum shapes

The exported document is aimed at the TM Forum Open APIs, and the field names
follow **TMF678 (Customer Bill)** where a field exists there:

| TMF | Used for |
|---|---|
| **TMF678** Customer Bill | the bill itself: `id`, `billNo` (empty — theirs to assign), `billDate`, `billingPeriod`, `paymentDueDate`, `state`, `taxExcludedAmount`, `taxIncludedAmount`, `taxAmount`, `amountDue`, `remainingAmount` |
| **TMF635** Usage Management | the rated detail inside it: `ratedProductUsage[]` with `productRef`, `usageQuantity`, `unitOfMeasure`, `ratingUnitPrice`, `taxExcludedRatingAmount` |
| **TMF666** Account | `billingAccount.id` — the customer's `external_account_id` |
| **TMF676** Payment | what comes back on the import: amount, date, reference |

Anything with no TMF equivalent is namespaced `@openova…` — the idempotency
key, the purchase-order reference, the payment terms, the resource count, the
source reference — so a strict consumer can drop those without losing a
required field. Every money value is an exact decimal, never a float and never
a formatted number.

#### The import

    POST /commercial/import/invoice-status
    X-Signature: sha256=<hex HMAC-SHA256 of the raw body, COMMERCIAL_IMPORT_SECRET>

    { "external_ref": "2026-05-omantel-corp-1a2b3c4d",
      "state": "paid", "paid_amount": 1000.100000,
      "paid_at": "2026-06-19", "reference": "BANK-88213" }

In external mode this is the ONLY thing that moves a statement after issue.
The signature is verified over the raw bytes BEFORE the body is decoded, in
constant time; no secret configured means 503, never an unauthenticated write
into a billing ledger. In internal mode the import is refused with 409 — we
are the system of record there, and a second writer on the same ledger is
exactly what this design avoids.

`state` takes the billing system's own vocabulary (`sent`, `validated`,
`settled`, `partiallyPaid`, `void`, …) and maps onto `sent`, `paid` or
`cancelled`. `overdue` maps to `sent` deliberately: overdue is derived here
from the due date and the outstanding balance (§8.5), and storing it would put
two sources of truth on one fact. `paid_amount` is CUMULATIVE, and the
difference against what we already hold is booked as a payment — so the
payment ledger stays the single place a balance comes from, and a repeated
import books nothing twice.

## 9. Account, credit notes, collections and tax — the console

The account surfaces follow the §8.9 pattern: every figure the server sends is
shown with the word that says what it means, and every write is an explicit
action with its consequence spelled out.

- **The customers directory** gains a `Balance` column read from the customer
  document's accounting-signed `balance`: owed in red with "owes" under it,
  credit in green with "in credit", "settled" at zero, and a dash — never 0 —
  when the document did not carry one.
- **Customer → Account** (first tab after Overview) shows Balance, Credit
  available, Owed and Overdue from `GET /customers/{id}/account`, then the
  ledger newest first (date, type, reference linking to the invoice, debit,
  credit, running balance — `aria-label="Account ledger"`), with where each
  payment went under its line. `Top up` records a transfer or internal recharge
  as credit on account (`POST /customers/{id}/payments`, no allocations); a
  gateway customer also gets `Checkout with <gateway>` (`POST
  …/payment-intents`, purpose checkout) and a Checkouts table of its intents;
  `Apply credit` is a confirm that applies the available credit to the open
  invoices oldest-first — explicit, never implicit. Credit notes and platform
  suspensions (with the platform's refusal when there was one) are listed
  below; each absence is one sentence.
- **Customer → Settings** adds the account-credit switches (auto-apply credit;
  suspend at zero for a prepaid wallet) and the tax block (exempt with a
  required reason, a rate override typed as a percentage and sent as a
  fraction, the registration number) on the same only-what-changed PATCH.
- **The statement view** of an issued invoice shows the tax line from
  `tax_snapshot` (the rate and both registrations, or the exemption), a
  `Credited` line, the credit notes it carries, the allocations applied from
  the account, and a `Credit note` action whose dialog refuses more than the
  total less the notes already issued — the server's rule, seen before the
  round trip.
- **Bill → Collections** is the aging report per customer (`aria-label="Aging"`:
  the five buckets, total owed, overdue, oldest due, credit available, a
  suspended badge), a strip of Total owed / Overdue / Customers overdue /
  Suspended, `Run collections now` behind a confirm that reports the pass, and
  per-row `Suspend` / `Resume` with a reason. A row expands to the customer's
  open invoices.

  **The enforcement path (§9.6, EPIC #6867).** A suspension — a collections
  escalation, a prepaid balance at zero, an operator's `Suspend`, or the
  external billing system's command (§9.1) — is recorded here first and then
  executed at the platform, in that order, by `collections.Enforcer`
  (`internal/collections/enforce.go`). The platform half runs through
  `internal/platform`: chargeback POSTs the Sovereign's sovereign-admin API
  at `PLATFORM_API_URL` — in-cluster,
  `http://catalyst-api.catalyst-system.svc.cluster.local:8080` — on
  `/api/v1/internal/organizations/{slug}/suspend` (with the reason) or
  `/resume`, presenting the projected ServiceAccount token the chart mounts
  at `/var/run/secrets/platform-api/token` (`platformApi.url` /
  `platformApi.tokenAudience`; the file is re-read on every call because the
  kubelet rotates it hourly). Those routes live outside the operator session
  gate and authenticate exactly as the cutover trigger does: a TokenReview on
  the bearer, then an allow-list that admits
  `system:serviceaccount:chargeback:chargeback`. The API merge-patches
  `spec.suspended` and `spec.suspendReason` onto the Organization CR and
  records the ServiceAccount username in the
  `orgs.openova.io/suspend-actor` annotation; the org-controller honours the
  flag by parking the per-Org Flux Kustomizations and surfacing a `Suspended`
  condition, so nothing new reconciles for that Organization until the flag
  is cleared. The chain is chargeback → internal route → CR → org-controller
  and nothing on the platform infers a suspension from a payment state; only
  that stamp sets it. Without `PLATFORM_API_URL` the Enforcer is a `Nop`: the
  customer still flips here and the audit entry says the platform was not
  called. The platform's refusal, when there is one, is kept verbatim on the
  suspension record and shown on the customer's Account tab.
- **Configure → Billing** edits the invoice and credit-note prefixes, the
  Sovereign's tax rate (as a percentage), registration number, legal name and
  address, and the collections schedule — reminder days as a comma list read
  back in words, escalation days and action — through one `PUT
  /billing-settings` that carries the saved discount rule plus only what
  changed.

## 10. Access model — who signs in, and what a role lets them do (founder requirement 2026-09-10)

The founder's question was exact: *how will customers log in, and how is
their role-based access defined — scopes and roles*. Before this section the
answer was three hard-coded roles (`operator`, `customer-admin`,
`customer-viewer`), a `customer_users` table binding an email to one customer,
and `requireOperator` in front of every write. That could not say "this
person runs billing but may not change settings", "this auditor reads
everything and changes nothing", or "this customer's finance contact may top
up the account but not manage its users". It now can, with two scope kinds,
ten permissions and six roles — and nothing else: there is no per-user
permission and no custom role.

### 10.1 Scopes

| Scope key | Meaning |
|---|---|
| `sovereign` | The whole Sovereign: every customer, every setting. |
| `partner:<id>` | The customers assigned to one partner, plus its own party (§13.5). |
| `customer:<id>` | One customer. |

A permission held at the Sovereign scope holds on every customer; a
permission held on a customer holds there and nowhere else — never at the
Sovereign, never on another customer; a permission held on a partner holds on
the customers that partner expands to. Every handler asks the one question
`access.Has(bindings, permission, customerID)` (`internal/access`), with
`customerID == ""` meaning the Sovereign scope. The routes that read ACROSS
customers ask `requireCrossCustomer` instead, which is that same question at
the Sovereign plus `access.HasAnyPartner` — see §13.5.

### 10.2 Permissions

| Permission | Grants |
|---|---|
| `metering.read` | Every read surface: usage, cost, resources, statements, the account, budgets, reports, sources, price books, settings documents. At the Sovereign scope it spans all customers. |
| `rating.manage` | Price books and their items, discounts and campaigns, currency rates. |
| `customers.manage` | Create, edit, invite, import and delete customers; their sources (region, project, price book, disable), budgets and report schedules — for ANY customer. Implies `customer.self.manage` on every customer. |
| `billing.issue` | Run, issue, edit (PO / terms of a draft), send, cancel and delete statements; issue credit notes; retry the commercial outbox. |
| `billing.collect` | Record, allocate and refund payments on the customer's behalf, apply credit, run collections, suspend and resume at the platform. |
| `account.topup` | Ask the gateway to collect a top-up for the customer's OWN account (a checkout). Nothing is booked until the gateway confirms — this is the one money write a customer may make. |
| `settings.manage` | Billing settings, allocation settings, and access itself: role bindings and directory group mappings. |
| `audit.read` | Audit trails. A Sovereign permission: a customer does not read its own trail. |
| `customer.self.manage` | The customer-scoped subset of `customers.manage` an owner holds on its own customer: its users, its sources' credentials and scope token, its PO reference and tax registration number. |
| `capacity.manage` | Capacity (§11): regions, zones, pool totals, SKU footprints and caps. A Sovereign permission; capacity reads ride on `metering.read` at the Sovereign, so a customer never sees capacity at all. |

### 10.3 Roles — fixed bundles

| Role | Scope kind | Permissions |
|---|---|---|
| `sovereign-admin` | sovereign | all ten |
| `billing-operator` | sovereign | `metering.read`, `rating.manage`, `customers.manage`, `billing.issue`, `billing.collect`, `audit.read`, `capacity.manage` |
| `finance-viewer` | sovereign | `metering.read`, `audit.read` — read and export only |
| `customer-owner` | customer | `metering.read`, `account.topup`, `customer.self.manage` |
| `customer-billing` | customer | `metering.read`, `account.topup` |
| `customer-viewer` | customer | `metering.read` |

The discriminating decisions: a `billing-operator` runs everything about
billing but cannot change a setting or grant a role; a `finance-viewer` opens
every page and every CSV and changes nothing; a `customer-owner` manages its
own users, PO reference and tax registration and may top up, but cannot
record a payment, apply credit, issue, or suspend — those stay
`billing.collect` / `billing.issue`, never customer-side; a `customer-billing`
tops up but manages nobody; a `customer-viewer` reads.

`access.Matrix` (`internal/access/access.go`) is the whole policy;
`TestMatrixEveryRoleEveryPermission` pins every cell, at both scope kinds.

### 10.4 Where a binding comes from

A **binding** is (subject, role, scope). A session holds the UNION of:

1. **Configuration** — every address in `OPERATOR_EMAILS` holds an implicit
   `sovereign-admin` binding (source `config`). Not a row, not revocable
   through the API; it is the bootstrap identity of a Sovereign.
2. **Explicit bindings** — `role_bindings(id, subject_email, role, scope_kind,
   customer_id, granted_by, granted_at)`, unique per (email, role, scope,
   customer). Granted through `POST /api/v1/access/bindings`, through the
   customer's Users tab (`POST /customers/{id}/users`), by the customer's
   `admin_email` (the `customers_owner_binding` trigger keeps every
   `admin_email` a `customer-owner` of its customer), and by the Organization
   sync (§10.6). `customer_users` — the pre-binding table — is now a VIEW over
   the customer-scoped bindings with its old columns (`role` reads `admin`
   for an owner and `viewer` for everything else), so an older reader is
   unchanged.
3. **Directory groups** — `group_role_mappings(group_name, role, scope_kind,
   customer_id)`. When the SSO gate forwards the identity
   (`TRUSTED_FORWARD_AUTH_HEADER`, `X-Forwarded-Email`) it also forwards the
   user's groups (`TRUSTED_FORWARD_GROUPS_HEADER`, default
   `X-Forwarded-Groups`, comma-separated); each named group adds the roles
   mapped to it, with source `group:<name>`. The groups header is trusted
   under exactly the same conditions as the identity header and is inert
   without it — a deployment that has not opted into the gate cannot be
   handed a role by naming a group.

Bindings are resolved on EVERY request — for a cookie session and for a
gate identity alike — never read back from what was stored at sign-in. A
revoked binding takes effect at the principal's next request; a granted one
needs no re-login; a principal left with no binding is unauthenticated (401),
so the API never invents access. The PIN flow mails a code only to an address
that resolves to at least one binding (groups cannot be known there).

### 10.5 What the session carries — `GET /api/v1/me`

```json
{
  "email": "owner@acme.example",
  "role": "customer-admin",
  "customer_id": "…",
  "roles": [{ "role": "customer-owner", "scope_kind": "customer", "customer_id": "…", "customer_name": "Acme", "source": "binding" }],
  "permissions": { "customer:…": ["account.topup", "customer.self.manage", "metering.read"] },
  "scopes": ["customer:…"],
  "customer": { "id": "…", "slug": "acme", "name": "Acme", "status": "active", "billing_mode": "chargeback", "payment_method": "gateway", "gateway_name": "stripe" },
  "expires_at": "…", "profile": "sovereign", "version": "…"
}
```

`role` and `customer_id` are the legacy pair and describe the highest-power
binding in the old vocabulary (`sovereign-admin` → `operator`,
`customer-owner` → `customer-admin`, `customer-viewer` unchanged; the three
new roles are reported as themselves because no old name means the same
thing), so `seed-history` and any reader written against the old document
keep working. `roles`, `permissions` and `scopes` are additive. The console
hides and shows by `permissions` alone. `GET /api/v1/auth/me` is the same
document.

### 10.6 The Organization sync

When an Organization CR is created or updated, `OrgSync.SyncOrganization`
upserts a `customer-owner` binding for the Organization's owner (the first
`spec.owners[]` entry with `role: owner`, else the first entry — the same
field it reads for `admin_email`) on the Organization's customer, granted by
`org-sync`. The sync only ever ADDS: a previous owner, and anyone the
operator granted, keeps access until it is revoked through the Users tab or
the access API. An Organization with no owner grants nothing; the
Sovereign's own Organization is not a customer and grants nothing.

### 10.7 The API

| Route | Permission |
|---|---|
| `GET /api/v1/access/roles` | any signed-in principal (the policy as a document, for labels) |
| `GET /api/v1/access/bindings[?email&customer_id]` | `settings.manage` — explicit rows plus the implicit `OPERATOR_EMAILS` ones, marked |
| `POST /api/v1/access/bindings {subject_email, role, customer_id?}` | `settings.manage` — 201, or 200 when already granted; audited `access.binding` op=grant |
| `DELETE /api/v1/access/bindings/{id}` | `settings.manage` — audited op=revoke; 409 on the last `sovereign-admin` when `OPERATOR_EMAILS` is empty |
| `GET /api/v1/access/group-mappings` | `settings.manage` |
| `PUT /api/v1/access/group-mappings {mappings:[{group_name, role, customer_id?}]}` | `settings.manage` — replaces the set, all or nothing; audited `access.mapping` |
| `GET /api/v1/customers/{id}/users` | `metering.read` on the customer |
| `POST /api/v1/customers/{id}/users {email, role}` | `customer.self.manage` on the customer (the owner) or `customers.manage` (the operator); `role` is a customer role or the legacy `admin` / `viewer`; the email ends up with exactly one customer role there |
| `DELETE /api/v1/customers/{id}/users/{email}` | as above; removes every customer-scoped binding of that email on the customer |

The role fixes the scope kind: a customer role without `customer_id`, or a
Sovereign role with one, is 400.

### 10.8 Handler → permission

Reads follow the session scope as before (`store.Scope`): a Sovereign
binding reads every row, a customer binding its customer's. A customer-scoped
route asked for a customer the caller holds no binding on answers 404, so
ids are not confirmed; a caller on the scope without the permission answers
403 naming the permission. `requirePermission(perm, customerID)` /
`requireSovereign(perm)` / `requireAnyPermission(customerID, perms…)` in
`internal/api/server.go` are the only gates.

| Handlers | Permission (scope) |
|---|---|
| `overview`, `explore`, `exploreCSV`, `costDimensions`, `summary`, `listResources`, `resourcesCSV`, `anomalies`, `recommendations`, `listAllSources`, `allocation`, `getAllocationSettings`, `getBillingSettings`, `listCurrencies`, `getCurrency`, `listAllDiscounts`, `getDiscount`, `priceBookCoverage`, `listOutbox` | `metering.read` (sovereign) |
| `customerUsage`, `customerInventory`, `customerExplore`, `customerExploreCSV`, `customerCostDimensions`, `customerSummary`, `customerResources`, `customerResourcesCSV`, `customerAnomalies`, `customerRecommendations`, `customerBudgets`, `customerReportSchedules`, `listSources`, `listDiscounts`, `listCustomerStatements`, `getCustomer`, `listUsers`, `getAccount`, `listCustomerPayments`, `listPaymentIntents`, `listCustomerCreditNotes`, `listSuspensions` | `metering.read` (customer) |
| `listCustomers`, `listAllStatements`, `getStatement`, `listPriceBooks`, `getPriceBook`, `exportPriceBook`, `listBudgets`, `getBudget`, `budgetStatus`, `getSource`, `getResource`, `getPayment`, `getCreditNote`, `listStatementCreditNotes`, `listStatementPayments`, `aging`, `listReportSchedules`, `getReportSchedule`, `previewReport`, `listReportDeliveries`, `listViews`, `createView`, `deleteView`, `me`, `listRoles` | signed in; rows filtered by the session scope |
| `createCustomer`, `patchCustomer` (all fields), `deleteCustomer`, `inviteCustomer`, `importCustomers`, `createBudget`, `updateBudget`, `deleteBudget`, `purgeExcluded` | `customers.manage` |
| `patchCustomer` (`po_reference`, `tax_registration_number` only), `addUser`, `deleteUser`, `createSource`, `rotateCredential`, `verifySource`, `deleteSource`, `patchSource` (`scope_token` only; the other fields need `customers.manage`) | `customer.self.manage` (customer) — or `customers.manage` |
| `createReportSchedule`, `updateReportSchedule`, `deleteReportSchedule`, `sendReportNow` | `customers.manage`, or `customer.self.manage` on the session's own customer (`customer_id` forced) |
| `createPriceBook`, `updatePriceBook`, `putPriceItems`, `addPriceItem`, `patchPriceItem`, `deletePriceItem`, `importPriceBook`, `clonePriceBook`, `deletePriceBook`, `createDiscount`, `createGlobalDiscount`, `updateDiscount`, `setDiscountActive`, `deleteDiscount`, `putCurrency`, `deleteCurrency` | `rating.manage` |
| `runStatements`, `issueStatement`, `patchStatement`, `sendStatement`, `cancelStatement`, `deleteStatement`, `createCreditNote`, `retryOutbox` | `billing.issue` |
| `recordPayment`, `recordStatementPayment`, `allocatePayment`, `refundPayment`, `applyCredit`, `runCollections`, `suspendCustomer`, `resumeCustomer` | `billing.collect` |
| `createPaymentIntent` | `account.topup` (customer) or `billing.collect` |
| `putBillingSettings`, `putAllocationSettings`, `listBindings`, `createBinding`, `deleteBinding`, `listGroupMappings`, `putGroupMappings` | `settings.manage` |
| `customerAudit` | `audit.read` (customer route; a customer principal is 403) |
| `capacityOverview`, `listCapacityRegions`, `listCapacityPools`, `listFootprints`, `listCaps` | `metering.read` (sovereign) — a customer principal is 403, never a filtered view |
| `createCapacityRegion`, `deleteCapacityRegion`, `createCapacityZone`, `deleteCapacityZone`, `putCapacityPool`, `putFootprint`, `putCap` | `capacity.manage` |
| `importInvoiceStatus`, `importPaymentStatus`, `importAccountBalance`, `importEnforcement`, `gatewayCallback`, `getInvite`, `activateInvite`, `pinRequest`, `pinVerify`, `logout` | not session-gated (HMAC, gateway signature, invite token, public) |

Every change to who holds what is audited: `access.binding` (op grant /
revoke, with the subject, role, scope and customer) and `access.mapping` (op
replace, with the resulting set); the Users tab writes both its
`customer.user.add` / `customer.user.remove` entry and the `access.binding`
one.

### 10.9 The console

`/me.permissions` is the only thing the console consults. The Sovereign lens
(any Sovereign binding) shows Analyse · Bill · Configure; **Configure →
Access** (bindings with grant and revoke, the implicit `OPERATOR_EMAILS`
rows read-only, the directory group mappings with save) is listed only with
`settings.manage`, and every other control is rendered only with its
permission — Issue / Send / Cancel / Credit note with `billing.issue`, Record
payment / Apply credit / Suspend / Resume / Run collections with
`billing.collect`, price-book and discount edits with `rating.manage`,
customer create / edit / invite / delete with `customers.manage`. The
customer lens shows Analyse (overview, explorer, resources), Bill
(statements, budgets, reports), **Account** (the ledger; `Top up` — a
checkout through the gateway — with `account.topup`) and Configure (sources,
discounts, and **Users** with `customer.self.manage`: add a user as owner /
billing / viewer, remove). A customer never sees the operator's Bill /
Configure groups.

### 10.10 Tests

`internal/access/access_test.go` pins the matrix (every role × permission ×
scope kind) and one discriminating case per role.
`internal/api/authz_roles_test.go` proves each refusal against a nil store
(the decision does not depend on data). `internal/api/access_integration_test.go`
proves the grants against Postgres: the implicit `OPERATOR_EMAILS` binding and
the `/me` shape; a directory group mapped to `finance-viewer` reads and gets
403 on every write, and is unknown without the group; a `customer-owner` tops
up its own account, manages its users and PO reference, is 404 on another
customer and 403 on the operator's money writes; a `customer-billing` tops up
and manages nobody; a `customer-viewer` is 403 on top-up; a revoked binding
ends a live cookie session at its next request; the bindings API and the
last-`sovereign-admin` guard. `internal/store/access_migration_integration_test.go`
stands a database before the migration, writes `customer_users` rows and
proves the backfill, the view, the widened sessions CHECK and the unique
indexes. `internal/adapter/openova/orgsync_access_test.go` proves the sync
grants the owner binding once and never revokes.

## 11. Capacity — regions, zones, pools and SKU footprints (founder requirement 2026-09-11)

The founder's requirement, verbatim: *"capacity management for the underlying
regions — overall capacity information of underlying AZs and regions as well
for each SKU; initially static, the admin defines the capacity; later from
integrations"*. The module answers three questions for a sovereign-admin: how
much of each kind of capacity does each availability zone hold; how much of it
is in use right now; and how many more of a given SKU could still be sold in
that zone before something runs out — and when, at the present rate, it will.

### 11.1 The model

```
capacity_regions  ─┬─ capacity_zones (one is_default per region) ─┬─ capacity_pools, one per family
                   │                                              ├─ sku_caps (optional direct ceiling per SKU)
                   │                                              └─ (consumption lands here, see 11.2)
                   └─ code = usage_records.region, e.g. me-east-215
sku_footprints    how much of each family ONE unit of a SKU consumes
capacity_pool_history   every total ever entered, by whom, with the note
```

**Families** (`internal/capacity.Families`) are the seven pooled kinds a zone
is measured in: `vcpu`, `memory_gib`, `block_ssd_gib`, `block_hdd_gib`,
`object_gib`, `eip_addresses`, `bandwidth_mbps`. The list is the CHECK
constraint on `capacity_pools.family` and `sku_footprints.family`, generated
from the Go list so the two cannot drift.

**Static first.** A pool's `total` is what the sovereign-admin types, with a
note, under `capacity.manage`; `source` reads `manual`. Every change writes
`capacity_pool_history` and an audit entry `capacity.pool` with the previous
and new total. A capacity collector — the integration the requirement defers —
plugs into exactly this shape later: it writes the same pools with its own
`source`, and nothing downstream changes. Until it exists a pool without a
total reads `status: unset`, never `ok`, so an empty page is honest about
what has not been entered. **Reserved** is carried at 0, column and wire key
present, for proposals and plans to fill.

**Footprints.** `sku_footprints(sku, family, amount)` says how much of each
family one unit of the SKU consumes: `ecs.m7n.2xlarge.8` → `vcpu 8,
memory_gib 64`; `evs.ssd.gb` → `block_ssd_gib 1`; `eip` → `eip_addresses 1`;
`eip.bandwidth_mbps` → `bandwidth_mbps 1`. The migration seeds the SKUs of the
National Cloud list price book whose footprint the name states —
`capacity.Seed()`, pinned equal to `synth.NationalCloudRates` — six of its
nine SKUs (`elb`, `nat.1`, `vpc` have no per-unit footprint in any family and
are reported as such). At read time a metered SKU with no row takes what its
name implies (`capacity.Derive`, source `derived`): an ECS flavour
`<family>.<size>.<ratio>` is `size` vCPU (small/medium 1, large 2, xlarge 4,
Nxlarge 4N) and vCPU × ratio GiB — the convention the ECS lister's
`vcpus`/`ram_mb` attributes and the list-price descriptions both follow.
Platform meters (`k8s.*`, `plan.*`) derive nothing: they run on the cloud's
instances, which the `ecs.*` SKUs already count, and deriving them too would
consume the same vCPU twice. A SKU whose storage class is not in its name
(`rds.storage.ha.gb`, `cbr.gb`, `ims.gb`) derives nothing and is listed as
unmapped until the operator writes its footprint. A stored row always wins
over derivation.

### 11.2 Derivations — consumed, available, headroom, exhaustion

Nothing about consumption is entered. It is the usage ledger this product
already keeps (§2), read one way:

- **Current hour.** For every cloud-layer source that is not disabled, its
  latest metered hour before the current one (`window_start < date_trunc(hour,
  now)`). Per source rather than one global hour, so a collector that lags a
  few hours still contributes its last fact instead of reading as zero;
  `as_of` is the newest of those hours, `lagging_sources` counts sources more
  than six hours behind it. Sampled measurements (`ecs.cpu_util`,
  `eip.traffic_gb.observed`) are excluded exactly as rating excludes them.
- **Region** is `usage_records.region`, matched to `capacity_regions.code`.
  Usage in a region the admin has not added — or has added without a zone —
  is `unmapped_regions[]` with the reason.
- **Zone** is the inventory row's `availability_zone` (or `az`) attribute
  when present, matched to `capacity_zones.code` within the region; otherwise
  the region's **default zone**, and that share is reported on the pool as
  `zone_unknown`. (The Huawei ECS lister does not yet record the zone; when it
  does, attribution sharpens with no change here.)
- **Consumed** per (zone, family) = Σ over SKUs of quantity × footprint, in
  exact rationals; a SKU with no footprint contributes to no pool and is
  listed once under `unmapped_skus[]` with its quantity, resources and regions.
- **Available** = total − reserved − consumed, never below 0: when the
  arithmetic goes negative the pool reads `available 0`, `clamped true`,
  `overcommit` = the shortfall. **Utilisation** = (consumed + reserved) ÷
  total, null without a total. **Status** is `unset` (no total), `ok`, `warn`
  (≥ 70 %) or `critical` (≥ 85 %); the thresholds ride on the document.
- **Headroom per SKU, per zone** = min over the SKU's families of
  ⌊available ÷ footprint⌋, over the families that have a total — a family the
  admin has not sized carries no information and is skipped; when none has a
  total the headroom is null. The family that produced the minimum is the
  `binding_family`. A direct `sku_caps` row (units of the SKU) bounds it
  further: ⌊cap − consumed units⌋, binding as `cap` when it is the lower one.
- **Time to exhaustion per pool** = available ÷ growth per day, where growth
  is the least-squares trend over the last seven complete days of consumed
  (each day's value: the sources' last metered hour of that day) — the
  explorer's own run-rate arithmetic, `rating.RunRate`, the trend
  `ForecastMonth` projects with. The store cannot import `rating` (which
  imports `store`), so the API supplies the function
  (`api.capacityGrowth`) and `TestCapacityGrowthIsTheRunRateTrend` pins it to
  `RunRate`; there is no second run rate. Null when consumption is not
  growing, when the history is shorter than three days, or when the pool has
  no total. The daily series rides on the pool as `series[]`.

Every quantity on the wire is an exact Postgres numeric rendered as a JSON
number; only the ratios (utilisation, growth, exhaustion) are floats, because
they are estimates and have no exact form.

### 11.3 API (`/api/v1`, DESIGN.md §10.8 for the gates)

| Method and path | Body / answer |
|---|---|
| `GET /capacity/overview[?region=<code>]` | `{as_of, sources, lagging_sources, thresholds{warn_pct, critical_pct}, families[], regions[{id, code, name, cloud_source_kind, zones[{id, code, name, is_default, pools[{…pool, label, unit, consumed, available, utilisation_pct, status, clamped, overcommit, zone_unknown, growth_per_day, exhaustion_days, history_days, series[]}], skus[{sku, footprint, footprint_source, consumed_units, resources, headroom_units, binding_family, cap}]}]}], unmapped_skus[], unmapped_regions[], summary{regions, zones, pools, pools_with_total, pools_warn, pools_critical, pools_below_threshold, skus, unmapped_skus}}` |
| `GET /capacity/regions` | `{regions[{…, zones[]}]}` |
| `POST /capacity/regions` | `{code, name, cloud_source_kind?}` → 201 the region; 409 on a duplicate code |
| `DELETE /capacity/regions/{id}` | cascades zones, pools, history, caps |
| `POST /capacity/regions/{id}/zones` | `{code, name, default?}` → 201 the zone with its seven pools at 0; the first zone is the default |
| `DELETE /capacity/zones/{id}` | the oldest remaining zone becomes default |
| `GET /capacity/zones/{id}/pools` | `{zone, pools[], history{pool_id: [changes]}, families[]}` |
| `PUT /capacity/pools/{id}` | `{total, note}` → the pool; audited `capacity.pool` with `from` / `to` |
| `GET /capacity/footprints` | `{footprints[{sku, families{family: amount}, source, updated_at}], families[], unseeded_skus[]}` |
| `PUT /capacity/footprints/{sku}` | `{families: {family: amount}}` — PUT semantics: absent or 0 removes a family, `{}` removes the footprint; 400 names an unknown family |
| `GET /capacity/caps` · `PUT /capacity/caps` | `{zone_id, sku, total}`; `total: null` removes the cap |

Reads need `metering.read` at the Sovereign — a customer principal is 403,
not a filtered view: capacity is the operator's picture of the cloud, never a
customer's bill. Writes need `capacity.manage` (`sovereign-admin`,
`billing-operator`); every write is audited as `capacity.region` /
`capacity.zone` / `capacity.pool` / `capacity.footprint` / `capacity.cap`.

### 11.4 The console — Plan → Capacity

A new menu group **Plan** holds **Capacity**. The page: a KPI strip (regions,
zones, pools past the 70 % line, SKUs without a footprint, as-of hour); a
heatmap table per zone × family — utilisation coloured at 70 / 85 %, the
available amount and the time to exhaustion in each cell, an inline editor for
the total with its note where the principal holds `capacity.manage`; a SKU
headroom table per zone with the binding family; the footprints editor; and an
"unmapped SKUs" notice with a one-click footprint form. The empty state
explains the static-first model: add the region and its zones, enter totals,
and a capacity collector fills them later. `ui/src/lib/capacity.ts` carries
the threshold colouring and the headroom arithmetic the page renders with,
pinned by vitest against the same figures the Go tests derive.

### 11.5 Tests

`internal/capacity/capacity_test.go` pins the flavour convention, `Derive`
(with the platform-meter and storage-class controls) and the seed against the
National Cloud list. `internal/store/capacity_integration_test.go` derives
consumption from seeded records: known-zone and unknown-zone instances, a
volume growing 10 GB a day (82.0 days to exhaustion against 1000 GiB), a
bandwidth reservation past its total (clamped, overcommit, critical), an
address with no total (unset), a SKU without a footprint (unmapped, counts
against no pool), a metric sample and a platform meter (neither counts), an
unconfigured region, headroom with the binding family and the cap, region
filtering, history, and the default-zone hand-over on delete.
`internal/api/capacity_integration_test.go` proves the permissions (viewer
reads, 403 naming `capacity.manage` on writes; customer 403 naming
`metering.read` at the Sovereign), the overview's keys, and the audit rows
of every write; `internal/api/authz_roles_test.go` proves the refusals
against a nil store.
---

## 12. Public cost calculator (founder requirement 2026-09-11)

> *"we'll provide a cost calculator publicly."*

A prospect who has never signed in can price a month of the Sovereign's
services, share the result as a link, and ask us to mail it to them. It is the
shape of the AWS and Azure pricing calculators, with one rule the cloud
calculators do not have to state: **the public surface shows list prices and
nothing else**.

### 12.1 What is public, and what never is

Public — the whole of it:

| Published | Where it comes from |
|---|---|
| One **cloud list book** | the book flagged `price_books.public`, designated in `billing_settings.public_price_book_id` |
| The **catalog plans** (S / M / L / XL, monthly, vCPU, memory) | the `OpenOva plans` book (§2.8) |
| The **pay-per-use rates** (`k8s.vcpu` / `k8s.mem_gb` / `k8s.pvc_gb`) | the `Organization PAYG` book (§2.9a) |
| The Sovereign **default tax rate** | `billing_settings.tax_rate`, shown as its own line |
| The **regions** | `capacity_regions` when that table exists, else the regions the sources and usage carry |

Never, by construction rather than by filtering:

- **A negotiated book.** The catalog is assembled from exactly three books by
  id and by name. A clone made for one customer carries neither the flag nor
  the designation, and `SetPriceBookPublic` refuses a second public book
  (`409`, naming the one in force) and refuses a platform book (`400`).
- **A discount, a campaign or a partner tier.** The estimate path never reads
  `discounts` at all: `rating.PriceEstimate` calls `Rate` and `Totals`, not
  `ApplyDiscounts`. A 20 % campaign that moves an invoice moves no estimate —
  pinned by the control in `public_calculator_integration_test.go`.
- **Any customer data.** The public routes read three books, the settings row,
  the region list and the caller's own submission. Nothing else is reachable
  from them.
- **A principal.** The session middleware skips `/api/v1/public/`; a cookie
  sent there is ignored, never looked up, and no route under it sets one.

Both books the Organization sync owns are published only when they exist on
this Sovereign **and are priced in the public book's currency** — an estimate
is issued in one currency and nothing here converts money, the same rule §2.9
gives a statement.

### 12.2 One pricing function, two callers

The calculator has no arithmetic of its own. `rating.PriceEstimate` turns each
requested line into the `store.RatableUsage` aggregate a month of hourly
records would leave in the ledger — quantity × hours × months, at the six
decimals `usage_records` carries — and hands the batch to **`rating.Rate`**,
the function the statement run calls, with the book's stopped-instance policy;
the totals come from **`rating.Totals`**, taxed exactly as an invoice is taxed.

The proof is a measurement, not an assertion.
`TestIntegrationPublicCalculator` seeds 730 hourly records of one instance and
of 100 GB of SSD, runs the statement, then prices an estimate of the same
shape, and compares the **text** of every decimal on the wire (the bodies are
re-read with `json.Number`, so a float64 that happens to print the same cannot
pass):

```
invoice line ecs.s6.large.2  730.000000 × 0.10000000 = 73.000000
estimate line ecs.s6.large.2 730.000000 × 0.10000000 = 73.000000
invoice  subtotal 83.000270  tax 4.150014  total 87.150284
estimate subtotal 83.000270  tax 4.150014  total 87.150284
```

`rating.TestPriceEstimateEqualsRate` pins the same equality at the unit level
on a deliberately awkward case — an 8-decimal rate and 100.5 hours — where a
second rounding rule would show.

**Monthly, yearly and the term.** `subtotal` / `tax` / `total` cover the whole
term of every line, so a plan taken for three months counts three times.
`monthly` is the recurring month — each line divided by its months, then taxed
the way `Totals` taxes the term, so an estimate whose lines are all one month
has `monthly == total` to the digit. `yearly` is 12 × `monthly`.

### 12.3 API

Unauthenticated, rate-limited, under `/api/v1/public/`:

| Route | What it answers |
|---|---|
| `GET /public/catalog` | the priced SKUs of the public book (sku, service, unit, unit price, the monthly price of one unit), the plans, the pay-per-use rates, the regions, the currency, the tax rate, the book's name and `updated_at`, and the list-price notice. `404` with *"the public price list is not published yet"* when nothing is designated |
| `POST /public/estimates` | prices `{currency?, region?, lines:[{sku, quantity, hours_per_month?} \| {plan, months?}], contact_email?}` and saves it → `{id, lines, subtotal, tax, total, monthly, yearly, currency, price_book, valid_until, share_url, lead, created_at}`. `?preview=1` prices without saving (the cart's live total) |
| `GET /public/estimates/{id}` | the saved estimate — the shareable link. The address a prospect left is **never** in this document |

Defaults and limits: `hours_per_month` 730 (`rating.HoursPerMonth` = 8760 / 12,
the figure the platform books derive a monthly price from), `quantity` 1,
`months` 1. At most 200 lines; quantity in (0, 1e9]; hours in (0, 744];
months in [1, 12]; an unknown SKU is `400` naming it and the line; a plan and
an SKU on one line is `400`; `flexi` is `400` pointing at the pay-per-use
meters, because it has no plan line to sell (§2.9a). An estimate quotes its
prices for **30 days** (`valid_until`).

Rate limit: a token bucket per client address — `PUBLIC_CALCULATOR_RATE_PER_MINUTE`
(default 60) tokens a minute, one minute's burst — answering `429` with
`Retry-After` in whole seconds. Behind the Sovereign's gateway the address is
the **last** `X-Forwarded-For` hop, the one the gateway appended, which a
caller cannot forge by prepending its own; reached directly, it is the peer.
An estimate records `client_hash`, a digest of that address, never the address.

CORS: `PUBLIC_CALCULATOR_ORIGINS` (comma-separated; empty = same origin only,
`*` = any) is the allow-list for cross-origin calls and preflights.

Operator side, on the authenticated API:

| Route | Permission |
|---|---|
| `PUT /pricebooks/{id}/public` `{public}` | `rating.manage` — audited `pricebook.public`; `409` on a second public book, `400` on a platform book |
| `GET /leads[?limit]` | `customers.manage` — the estimates a prospect left an address on, newest first, with the address and the shareable link |

### 12.4 Data model

`estimatesMigrationSQL`, appended last (migrations are positional) and located
by content in `store.MigrationEstimates`:

- `price_books.public BOOLEAN NOT NULL DEFAULT false`, with a **partial unique
  index** on `(public) WHERE public` — the backstop behind the store's check,
  so a second public book cannot be written even directly.
- `price_books.updated_at`, moved by a trigger on the book **and on its items**:
  "prices as of" must change when a rate changes, not only when the header does.
- `billing_settings.public_price_book_id` — the designation, `ON DELETE SET NULL`.
- `estimates(id, price_book_id, price_book_name, price_book_updated_at,
  currency, region, payload JSONB, subtotal, tax_rate, tax, total, monthly,
  yearly, contact_email, lead, client_hash, created_at, valid_until)`, with a
  partial index on the leads. The book's **name** is copied onto the row so a
  shared estimate still says what it was priced from after the book is deleted.

An estimate belongs to nobody: no customer, no session, no cookie. `lead` is
simply "an address was left", and the proposals module reads those rows later.

### 12.5 The page

`/estimate` in the same React app, outside the console shell and outside
sign-in: a cart (search the catalog by service or SKU, quantity, hours; a plan
picker), a region selector, a live total from `?preview=1`, tax on its own
line, **Share estimate** (saves it and shows the link), **Send me this
estimate** (an address, which makes it a lead), and a footer naming the price
book and its date beside the list-price notice.

`/estimate?embed=1` drops the page header and posts its height to the parent
on every change (`{type: 'openova-estimate-height', height}`), for the
marketplace or a partner site to frame. Framing is allowed only for the
configured origins: the page answers `Content-Security-Policy: frame-ancestors
'self' <origins>` and every other path, the console included, keeps
`X-Frame-Options: DENY`.

Configure → **Price books** carries the `Public` toggle per cloud book — a
platform book reads "with the list" instead, because the plans and
pay-per-use cards are published alongside the cloud book and never on their
own — beside a **Public catalog preview** link to `/estimate`. Configure →
**Leads** is the read-only list (received, address, per month, per year, what
they priced, region, link) under `customers.manage`.

The page is mounted ABOVE the session provider in `src/App.tsx`: `/estimate`
and `/estimate/{id}` render outside the console tree, so a visitor's browser
makes no `/auth/me` call at all and the sidebar cannot appear. The cart module
(`src/pages/Estimate.tsx`) holds the cart STATE and the request it becomes —
never money arithmetic: a total computed in the browser would be the second
pricing path §12.2 forbids, so every figure on the page comes from
`?preview=1`.

### 12.6 Tests

`internal/rating/estimate_test.go` — the estimate line equals the rated usage
line for the same SKU, quantity and hours (8-decimal rate, fractional hours),
the totals equal `Totals`, plan months, `monthly == total` for a one-month
estimate, `yearly == 12 × monthly`, and every refusal names its line.
`internal/store/estimates_integration_test.go` — the flag and the designation
move together, only one public book (store check **and** unique index), never
a platform book, the resolver's two paths, `updated_at` following header and
items, the estimate round-trip with its 30-day validity, the lead flag, and
the region fallback with and without `capacity_regions`.
`internal/api/public_calculator_test.go` — the token bucket's budget, refill
and `Retry-After`; the trusted forwarded hop; every line-validation message;
the framing and origin rules. `internal/api/public_calculator_integration_test.go`
— the catalog carries only the public book (the negotiated clone's rate is not
in the body), estimate math equals invoice math to the digit, the discount
control, plans and pay-per-use lines, the refusals, the 30-day validity, the
shareable link, leads and their permission, the `429`, CORS and preflight, and
that no public route ever reads or sets the session cookie.
`ui/src/pages/Estimate.test.ts` — the cart model: a second add bumps the
quantity instead of duplicating a row, a plan line carries months and never
hours (and an SKU line the reverse, which is what the API refuses), every
row-level refusal is worded as the server words it, a half-typed row is left
out of the request while the rest still prices, and the embed predicate and
height message. `ui/src/pages/EstimatePublic.render.test.tsx` — the page
renders the list prices, the plans, the pay-per-use rates, the tax line and
the price-book footer with no console shell and no sign-out, drops its header
under `?embed=1`, and shows a shared estimate read-only.
## 13. Partners — resellers and agents (founder direction 2026-09-11)

A Sovereign does not only sell direct. It sells **through** partners: a
reseller that buys capacity at a wholesale price and bills its own customers,
and an agent that introduces customers we bill ourselves and earns a
commission. Both were expressible only as "another customer with a discount",
which cannot say what a partner actually is — a party that owes us money for
somebody else's usage, or that we owe money to.

### 13.1 The waterfall — one list price, two independent steps

The whole model is three lines, and everything below follows from them:

    list  −  customer discounts  →  CUSTOMER NET   what the end customer pays
    list  −  partner tier        →  PARTNER BUY    what the partner pays us
                                    MARGIN = customer net − partner buy

**There is one list price per SKU** — the Sovereign's list book, assigned to
the source as always (§2). The two reductions off it are **independent**: a
customer's negotiated discount does not change what the partner pays us, and
a partner's tier does not change what the customer is billed. **Margin is
derived per line and never entered.** There is no markup typed per SKU
anywhere in the product, because a typed markup is a second price that drifts
from the first the moment either side moves.

Both steps run through **the same discount engine** — `rating.ApplyDiscounts`
and its per-meter sibling `rating.DiscountBySKU`, under the operator's
combination rule (§2.11). A tier discount IS a discount: a row of the
`discounts` table with `tier_id` set, `customer_id` NULL and `kind =
percent`. Most-specific, highest, stack and compound decide two tier
percentages exactly as they decide two customer percentages, and the table in
§2.11 is the table for a tier. There is no second pricing path to keep in
step with the first.

Worked, to the decimal, and pinned by
`TestIntegrationPartnerWaterfallResellAndAgent`:

| | figure |
|---|---|
| list (1 unit at 100) | **100.000000** |
| customer discount 10 % | −10.000000 |
| **customer net** | **90.000000** |
| tier discount 30 % off list | −30.000000 |
| **partner buy** | **70.000000** |
| **margin** (net − buy) | **20.000000** |

Tax is charged on what the customer pays, as everywhere else: 5 % of 90 is
4.500000 and the invoice total is 94.500000.

### 13.2 The two billing models

`partners.bill_to` selects one, and it is a value in the rating run
(`billingModel`), not a conditional sprinkled through it. Both are
first-class; neither is a special case of the other.

**`partner` — RESELL.** The partner is invoiced a **wholesale statement**: every
one of its customers' lines at list, less the tier, grouped by end customer.
Its customers are rated at the partner's **derived retail book** (§13.3) for
informational showback — they are the partner's customers to bill. The
wholesale statement is a real invoice on the partner's account: issued,
numbered, due on terms, collected like any other.

**`customer` — AGENT.** We invoice the end customer ourselves, at **our own
books** — nothing about the bill changes because an agent introduced it. The
partner is credited a **commission statement**: one line per end customer,
`customer net − partner buy`, or `commission_pct × customer net` when the
partner has no tier. Issuing it posts a ledger **credit** of kind
`commission` on the partner's account: money we owe, so the balance reads in
credit. Nothing is collected on it.

**One run produces both.** `POST /statements/run` rates every customer as
before — a partner's customer additionally carrying its per-line waterfall —
and then writes each affected partner's own statement from the lines the
customer pass just froze. A partner's statement is therefore always
reconcilable to its customers' statements, line for line, and a customer
whose period is already issued still contributes to it.

### 13.3 A partner is a PARTY — it reuses the customer ledger

A partner owes money, pays it, gets credit notes, is chased by collections
and can be suspended. All of that already exists for a customer, and
duplicating it for a partner would mean two ledgers that must agree.

So each partner owns one `customers` row flagged `party_kind = 'partner'`
(`partners.party_customer_id`), created in the same transaction as the
partner and billed `postpaid` by `transfer`. The partner's balance, invoices,
payments, allocations, credit notes and aging are the ones that row already
has — **zero new ledger code**. `GET /partners/{id}/account` is the customer
account handler pointed at the party.

A party row is not a customer: `ListCustomers` and the status counts exclude
it, the rating run never rates it as one, and it can never be assigned to a
partner itself. `customers.partner_id` assigns an END customer to a partner —
one partner per customer, nullable, and clearing it makes the customer direct
again.

### 13.4 The derived retail book

Under resell the partner sets its own prices, and it must be able to do that
without hand-maintaining a rate card that has to track ours. So it states a
**rule**, and the book is **materialised from it**:

    partner_retail_rules(partner_id, base ∈ {list, buy}, markup_pct, overrides[])

Per SKU: `buy = list − tier` (through the combination engine),
`base = list | buy`, `retail = base × (1 + markup)`. The markup is the most
specific override that matches — `sku`, then `service` (the SKU's first
segment: `ecs`, `evs`, `plan`, `k8s`), then the rule's default — the same
"most specific wins" the discount engine uses.

The result is a **real `price_books` row** owned by the partner
(`partner_id`, `derived_from_rule`, `derived_from_book_id`), one per list
book its customers are priced by, so every reader that knows a price book —
rating, coverage, export — reads it unchanged. It is **read-only in the
editor**: an item write answers 409 naming what to change instead. It is
re-derived on a **list change, a tier change, a rule change, a billing-model
change and a customer assignment**, and it is removed when the partner
becomes an agent or loses its rule.

A retail price **below the partner's own buy price** is reported, in the API
response and on the console, as a `below_buy` line — never refused. Selling
at a loss is the partner's decision to make; hiding it from them is not ours.

    base = buy (70) + 5 %  → 73.50000000, no warning
    base = list (100) + 5 % → 105.00000000
    base = buy (70) − 10 % → 63.00000000, below the buy price of 70 → warned

### 13.5 The partner scope

A third scope kind joins `sovereign` and `customer:<id>`: **`partner:<id>`**.
A binding there **expands**, with the session and never stored, to the
customers assigned to that partner plus the partner's own party — so a
partner principal reads its customers' explorer, resources, statements and
accounts through the surfaces that already exist. A customer assigned to or
taken from a partner takes effect at the principal's next request.

**One predicate.** Every scoped read resolves its customers through
`store.Scope.Confine(customerID)` and filters on `customer_id = ANY(<that
set>)`. A customer principal is the one-element case of what a partner does
with several; `nil` is the operator's unfiltered read and can mean nothing
else. A customer the caller named is INTERSECTED with the scope, never
replaced by it — so a partner opening one of its own customers sees that
customer alone, and one naming a customer outside its scope is told not
found rather than quietly handed its own rows.

**The cost surfaces reach a partner.** `GET /cost/explore`,
`/cost/summary`, `/cost/export.csv`, `/cost/dimensions`, `/resources`,
`/resources.csv`, `/anomalies`, `/recommendations` and `/overview` are
cross-customer routes, guarded by `requireCrossCustomer`: a Sovereign
principal passes, and so does a partner principal holding `metering.read` at
its partner scope — the store confines every query underneath to that
partner's customers. So a reseller groups, filters, compares, forecasts and
exports ACROSS its customers exactly as the operator does, with `group_by=customer`
listing its own and no others; `/views` (saved explorer states) belongs to the
signed-in email and needs no scope at all. A customer principal is still
refused these routes (403): its lens is `/customers/{id}/…`.

What a partner does NOT get from `/cost/summary` is the Sovereign's own
counts — how many customers this Sovereign has (`customers`) and how its
sources are doing (`sources`) are the operator's picture, so both come back
empty; the cost blocks, the statements and the live-resource count are its
customers'.

Two roles, bound at that scope:

| Role | Permissions |
|---|---|
| `partner-owner` | `metering.read`, `account.topup`, `partner.self.manage` — reads its customers and its own account, edits its retail rule, manages its own users, tops up its account |
| `partner-viewer` | `metering.read` |

and two permissions join the nine:

| Permission | Grants |
|---|---|
| `partners.manage` | Create and edit partners, tiers and their discounts; assign a customer to a partner; set any partner's retail rule. Sovereign-only (`sovereign-admin`, `billing-operator`). Implies `partner.self.manage` on every partner. |
| `partner.self.manage` | The partner-scoped subset an owner holds on its OWN partner: its retail rule and its users. |

**What a partner never sees**: another partner, another partner's customers
or margin (a filter naming one returns an empty document, a path naming one a
404), the provider's list books (403 — it reads its own retail book instead),
the tier catalogue, the Sovereign-wide customer and source counts, and the
provider's own configuration pages. **What a customer never sees**: the partner
directory (403), any partner-scoped route (404), and the buy price or margin
on its own bill — `buy_total`, `margin_total` and the per-line `buy_amount`,
`list_amount` and `list_unit_price` are stripped for any principal that is
neither Sovereign nor bound to that partner. The partner it buys through
stays named on its own customer record: that is its own commercial
relationship, not a secret.

### 13.6 The API

| Route | Permission |
|---|---|
| `GET /api/v1/partners` | `metering.read` (sovereign) — a partner principal gets its own only; a customer principal 403 |
| `POST /api/v1/partners` | `partners.manage` — creates the partner AND its party, and grants the contact `partner-owner` |
| `GET /api/v1/partners/{id}` | `metering.read` (partner) |
| `PATCH /api/v1/partners/{id}` | `partners.manage` — a tier or model change re-derives the retail books |
| `GET /api/v1/partners/tiers` | `metering.read` (sovereign) |
| `POST /api/v1/partners/tiers` | `partners.manage` |
| `PUT /api/v1/partners/tiers/{id}/discounts` | `partners.manage` — replaces the set; re-derives every partner on the tier |
| `PUT /api/v1/partners/{id}/retail-rule` | `partner.self.manage` (partner) — re-derives; the response carries the books and the below-buy lines |
| `GET /api/v1/partners/{id}/retail-book` | `metering.read` (partner) |
| `GET /api/v1/partners/{id}/customers` | `metering.read` (partner) |
| `GET /api/v1/partners/{id}/statements` | `metering.read` (partner) — its wholesale / commission statements |
| `GET /api/v1/partners/{id}/margin?period=` | `metering.read` (partner) — per customer per service: net, buy, margin, margin % |
| `GET /api/v1/partners/{id}/account` | `metering.read` (partner) — the party's ledger |
| `GET /api/v1/partners/{id}/users` | `metering.read` (partner) |
| `POST /api/v1/partners/{id}/users` · `DELETE …/users/{email}` | `partner.self.manage` (partner) |
| `PATCH /api/v1/customers/{id}` (`partner_id`) | `partners.manage` — never `customers.manage` alone, and never the customer itself |
| `GET /api/v1/cost/explore` · `cost/summary` · `cost/export.csv` · `cost/dimensions` · `resources` · `resources.csv` · `anomalies` · `recommendations` · `overview` | `metering.read` at the Sovereign OR at a partner — the cross-customer read (§13.5). A partner's answer covers its customers and nothing else; a customer principal is 403 |

Every write is audited: `partner.create`, `partner.update`, `partner.tier`,
`partner.retail_rule`, and `access.binding` for the partner-scoped grants.

Statement documents gain `partner_id`, `party_kind`, `statement_kind`
(`customer` | `wholesale` | `commission`), `buy_total` and `margin_total`;
rated lines gain `end_customer_id`, `list_unit_price`, `list_amount`,
`buy_amount` and `net_amount`. Every key is additive — a reader written
before partners existed is unchanged, and a direct customer's statement
carries none of them.

### 13.7 The console

**Configure → Partners** lists every partner with its tier, billing model,
customer count and account balance; opens one for its customers, its
statements, its margin, its retail rule (with the derived book preview and
the below-buy warnings) and its users; and creates and edits both partners
and tiers, tier discounts included. The customer page gains a **Partner**
block: which partner this customer buys through, and the assignment.

The **partner lens** (`/partner/…`) is the third lens beside the Sovereign's
and the customer's. When the caller's bindings are partner-scoped the menu
shows **Analyse** — My customers, and then the operator's own **Cost
explorer**, **Resources**, **Anomalies** and **Recommendations** over the
partner's customers — **Bill** (its customers' statements and its own
wholesale or commission statements), **Account** (its party), **Margin** and
**Users**, and nothing else. Those are the same components the operator
opens, on the same Sovereign-wide documents: what a lens carries is
`crossCustomer` — this lens spans more than one customer, so the `customer`
dimension is offered and the Customer column drawn — separately from
`operator`, which alone opens the provider's price books and a customer's
configuration. Every link a partner follows stays inside `/partner`. The
statement view shows the partner block — buy total, margin, margin % — to
Sovereign roles and partner roles only.

### 13.8 Tests

`internal/rating/partners_test.go` pins the engine: `DiscountBySKU` allocates
exactly the total `ApplyDiscounts` reports, per meter, under all four
combination rules; a tier obeys that same table (most-specific 20, highest
30, stack 50, compound 44 on one 100 line); the derived book's 73.5 / 105 /
63-with-a-warning; the override precedence; and that a split of a figure
across lines sums back to it exactly.
`internal/store/partners_integration_test.go` walks the waterfall against
Postgres: 100 → net 90 / buy 70 / margin 20 on the customer statement and its
line, the resell wholesale statement summing at 70 with the lines grouped by
end customer, the agent commission of 20 posted as a ledger credit that takes
the partner's balance to −20, the margin report, the derived book re-derived
on a list change (200 − 30 % + 5 % = 147) and removed when the partner
becomes an agent, the scope expansion, and that a tier discount never reaches
a customer's list or bill.
`internal/api/partners_integration_test.go` proves the routes: the documents
the console reads, the retail-rule response with its below-buy lines, the 409
on editing a derived book, a partner owner reading its own customer (200) and
another partner's (404), 403 on the provider books and the tier catalogue,
403 on every Sovereign write, a partner-viewer refused its own retail rule, a
revoked partner user out at the next request, and a customer principal
refused the partner directory with the buy price and margin stripped from its
own statement.

The cost surfaces have their own pair, on a deliberately uneven ledger — One's
customers cost 10 and 20, Two's 40, a direct customer 80, so 30 is reachable
only by seeing exactly One's two.
`internal/store/partner_cost_integration_test.go` pins the store: the partner
scope totals its own two customers and lists only them under
`group_by=customer`; a filter naming another partner's customer returns
nothing and a path naming it returns not found; naming one of its OWN returns
that one alone; the operator still sees all five and a customer principal
still only itself; and resources, the daily series and the recommendation
inputs are confined by the same predicate.
`internal/api/partner_explore_integration_test.go` walks it through HTTP: a
partner session 200 on `/cost/explore` with only its customers in the
group-by and its exact total, an empty document for a foreign `customer`
filter, 404 on a foreign path, a CSV export carrying its customers and no
others, a summary with its costs but none of the Sovereign's counts, its two
resources, a saved view of its own — with the operator's own explorer and
counts unchanged and a customer principal still 403.
`ui/src/pages/PartnerAnalyse.render.test.tsx` renders the partner lens's
explorer and resource list (the Customer group-by offered, its customers on
the page, every link inside `/partner`, no price-book link);
`ui/src/layout/Shell.test.ts` and `ui/src/lib/scope.test.ts` pin the menu and
the lens's paths.
---

## 14. Documents — the invoice the customer actually receives (EPIC #6867)

Everything above settles what a customer owes. This settles what they are
*handed*: a **PDF**. An invoice that exists only as JSON and a CSV is not an
invoice anyone can file, forward to their accountant, or attach to a payment
run, and a quote that cannot be sent as a document is not a quote.

**A separate service, not a package.** `products/chargeback/docrender` is its
own Go module, its own image and an optional sub-chart of `bp-chargeback`
(`docrender.enabled`, off by default). The split is deliberate on three
counts. Document layout is a different problem from rating — fonts, page
breaks, column metrics — and mixing it into the billing binary would put a
typesetting dependency behind every statement run. A renderer is a **pure
function** of the document it is given, so it can be stateless, hold no
credentials, reach nothing, and be restarted or scaled without a thought. And
the four document kinds are wanted by more than the invoice route: a quote
today, an e-invoice attachment's human-readable rendition next.

**No headless browser.** The PDF is drawn directly from the document JSON
with `github.com/go-pdf/fpdf`, a pure-Go library with real font metrics, so
column widths, wrapped cells and page breaks are measured rather than
guessed, the output carries a genuine text layer, and the runtime image is a
single static binary on distroless — no Chromium, no font directory, no
`/tmp` spool. An HTML rendition of the same document is served from
`html/template` files for the console to show inline.

**Money crosses the wire as decimal STRINGS.** `internal/docs` passes the
ledger's own digits through untouched — it never parses an amount, so it
cannot round or re-scale one — and the renderer formats them at the
**currency's minor unit**: three decimals for OMR and the other dinars, two
for the rest, rounded once, half away from zero. A value that has been
through a float is refused with 400 rather than silently reinterpreted, on
the same reasoning as §8.6: the arithmetic stays exact and only the final
rendering rounds.

**Which document it is follows the statement.** One that has been issued
carries an invoice number and renders as a *Tax Invoice*; a draft, or one the
operator's external billing system numbers (§8.10), has no number of ours to
print and renders as a *Statement* of the period. The seller block comes from
the statement's own **tax snapshot** (§9.4) when it has one — an operator who
renames the Sovereign must not silently rewrite a document the customer is
already holding — and only a draft falls back to the live billing settings.

**The route.** `GET /api/v1/statements/{id}.pdf`, alongside the existing
`.csv`. It is the same handler and the same scope-filtered store read as
fetching the statement, so the permission to download the document is exactly
the permission to read the statement it is made of — a customer downloads its
own invoices and gets 404 on anyone else's. With no renderer configured the
route answers **503 "document renderer not configured"** and every other
surface is untouched: an operator reading that response is told which of "not
turned on" and "broken" they are looking at.

**In-cluster only.** The renderer has no external door at all — ClusterIP,
no HTTPRoute, no Ingress, and the chart refuses any other Service type. A
default-deny NetworkPolicy admits the chargeback pods on the service port and
nothing else, and allows egress to kube-dns and nothing else; it makes no
outbound call, so there is nothing further to allow. An optional
`X-Render-Token` shared secret sits on top of that as defence in depth, never
as the perimeter. `chart/tests/kyverno-policies.sh` applies the platform's
own admission baseline at its Enforce actions and requires zero failures.

**The contract between the two modules is a committed document.** They are
separate Go modules and cannot import each other, so
`docrender/testdata/bss-invoice.json` is written by the BSS mapper's test and
rendered by the renderer's — a field renamed on either side fails one of the
two suites, rather than surfacing as a 400 on a customer's download months
later.

## 15. Contracts and commercial terms (founder direction 2026-09-11)

Two things the price books and discounts shipped so far cannot express, and
which every cloud provider sells.

A price book could only say *one rate per SKU*. It could not say "the plan
includes 50 GB of traffic", it could not say "the first 10 TiB at 0.010 and
the rest at 0.006", and it could not say "you committed to ten machines for a
year, so those ten are cheaper". Discounts could not fill the gap: a discount
takes money off a bill, and none of those three is a reduction of money — two
of them change the *quantity* being priced or the *rate per unit*, and all
three belong to an agreement with a start, an end and a signature.

So §15 adds **three rating shapes** on a price-book item and **the contract**
they hang on. The vocabulary is the industry's throughout — AWS free tiers,
tiered S3 pricing and Savings Plans; Azure included quantities, graduated
meters and reservations; Stripe's `graduated` and `volume` tier modes;
Zuora's tiered and volume pricing, minimum commitments and true-ups. Nothing
below is invented, and nothing below is a second pricing path.

### 15.1 Allowances — a quantity the plan includes

An item may carry an **allowance**: N units of that SKU included in every
billing period. Usage up to the allowance rates to **zero**; the excess rates
at the item's price. Plan M including 50 GB of `eip.traffic_gb` and 100 GB of
`object_gib` is two items with two allowances, not a discount and a note.

The allowance is **per billing period and lapses**. It does not carry over
unless the item (or the contract line) says `rollover`, and where it does the
carry-over is **one period deep and does not compound**: what last period did
not use is available this period, and then it is gone. An unbounded bank
would have to be materialised somewhere, and a bank nobody can see on a bill
is worse than no bank at all.

An allowance may come from the **plan** (the price-book item) or from the
**contract** (a `contract_items` row of kind `allowance`). They **add up**: a
negotiated 100 GB on top of a plan's 50 GB is 150 GB. A contract allowance
that silently replaced the plan's would make a customer worse off for having
negotiated.

### 15.2 Volume tiers — bands instead of one rate

An item may carry **tiers** instead of one unit price:

    [{up_to: 10240, price: 0.010}, {up_to: 102400, price: 0.008}, {up_to: null, price: 0.006}]

in **two modes**, and the mode is explicit on the item because the two are
different prices for the same volume:

| mode | what it means | 51,200 units of the ladder above |
|---|---|---|
| `graduated` (the default) | each band rates at **its own** price | 10,240 × 0.010 + 40,960 × 0.008 = **430.080000** |
| `all_units` | the **whole** volume rates at the price of the band the total reaches | 51,200 × 0.008 = **409.600000** |

Both are standard — `graduated` is Stripe's graduated tiers and AWS's tiered
storage; `all_units` is Stripe's volume tiers and Zuora's volume pricing. An
engine that picked one silently would over- or under-bill by 20.48 on that
one line with nothing on the bill to show which it had chosen, so the mode is
a column and the difference is pinned by
`TestGraduatedAndAllUnitsDifferOnTheSameVolume`.

The ladder is **validated, not repaired**: bands must ascend, the unbounded
band must be last, and no band may be priced below zero. Two bands claiming
the same volume have no single answer, and guessing one would be a pricing
bug nobody could see. A ladder that simply stops is completed by carrying its
last price upward — volume above the last bound must not rate at nothing.

### 15.3 Committed use — a quantity bought ahead at a rate

A customer commits to a quantity of a SKU for a term at a discounted rate:
ten `ecs.m7n.2xlarge.8` for twelve months at 30 % off. The **committed
quantity rates at the committed price and the excess at list**, and the
commitment is **consumed per period** — each period offers the whole
committed quantity again.

The commitment covers the **head** of the volume, not the tail: a commitment
is for baseline usage, the part you always have, so it displaces the dearest
band of a ladder rather than the cheapest. Under- and over-consumption are
both pinned by `TestCommitmentUnderAndOver`:

| the month | arithmetic | total |
|---|---|---|
| 5,000 h against 7,440 committed at 0.35 | 5,000 × 0.35 | **1,750.000000** |
| 10,000 h against the same commitment | 7,440 × 0.35 + 2,560 × 0.50 | **3,884.000000** |
| the same 10,000 h at list | 10,000 × 0.50 | 5,000.000000 |

A committed-use line carries either a **committed price** or a **discount
percentage** off list; a commitment at list is refused, because it is not a
commitment. Unused commitment is **not** invoiced by itself — the instrument
for "you will spend at least X" is the minimum commitment of §15.4, which is
explicit on the bill rather than hidden inside a rate.

### 15.4 The contract, the minimum commitment and the true-up

    contracts(id, customer_id, name, starts_on, ends_on, term_months, auto_renew,
              renewal_notice_days, minimum_commitment, currency, status, signed_at,
              po_reference, notes, renewed_at, renewal_count)
    contract_items(id, contract_id, kind, sku, unit, quantity,
                   committed_price, discount_pct, rollover, notes)

`status` is `draft | active | expired | cancelled`, and only an **active**
contract covering the first day of a period rates that period. A term of N
months from `starts_on` ends the day **before** the anniversary — twelve
months from 2026-01-01 ends 2026-12-31 — so a term never overlaps its own
renewal. Where two active contracts cover the same day, the one that
**started last** is in force: that is the renegotiation.

**The minimum commitment is a monthly floor.** If the period's rated total,
**net of discounts**, falls below it, the statement carries a line:

    true-up   1 period   <shortfall>

named on the invoice and explained, and never a silent adjustment of the
totals. It brings the net subtotal to exactly the minimum, and tax is charged
on that, because a true-up is a charge for the service like any other. The
comparison is against the **net** deliberately: a discount agreed in the same
contract must not be usable to slide under the minimum the contract set.

A statement records the `contract_id` it was rated under, so an issued bill
says which agreement produced its numbers.

### 15.5 SLA credits

`POST /contracts/{id}/sla-credit` issues a credit note against a **named
statement** for an availability breach, with the percentage owed and the
availability actually measured recorded on it. The amount is that percentage
of the statement's total, which is what an availability SLA credits.

It uses **the existing credit-note machinery and adds nothing parallel**: the
same gapless `CN-<year>-<seq>` numbering, the same allocation against the
invoice with the remainder becoming credit on the account, the same
`credit_note` entry on the ledger. The only additions are three columns on
`credit_notes` — `contract_id`, `sla_pct`, `measured_availability` — so the
document says what it answers. A parallel "service credit" table would have
been a second ledger to reconcile.

### 15.6 Renewal

A contract with `auto_renew` renews for another term at its end date: the
same agreement, its window moved forward by `term_months`, `renewal_count`
incremented and `renewed_at` stamped. One without it becomes `expired`.

`renewal_notice_days` before the end date the contract appears in the
**renewals-due list** — `GET /contracts/renewals` and the Contracts page —
which is what an operator acts on. There is **no mailing and no scheduler of
its own**: the decision between renewing and expiring is a step added to the
existing daily collections evaluator (`internal/collections`), which already
walks every open invoice once a day, and it reports `contracts_renewed` and
`contracts_expired` beside its reminders. The step is idempotent — a second
pass on the same day changes nothing — so a restart cannot advance a term
twice.

### 15.7 The order of operations

One waterfall, one engine. Allowances, tiers, commitments and the true-up are
computed inside `internal/rating` and then handed to the **same**
`ApplyDiscounts` / `DiscountBySKU` the rest of the product uses, and to the
**same** partner waterfall (§13):

| | step | where |
|---|---|---|
| 1 | **allowance** — usage up to the included quantity rates to zero | `rating.ApplyTerms` |
| 2 | **tiers** — the remainder priced by the item's bands, or its flat unit price | `rating.ApplyTerms` |
| 3 | **commitment** — the committed head of the volume repriced at the committed rate | `rating.ApplyTerms` |
| 4 | **discounts** — customer discounts, and a partner tier, under the combination rule (§2.11) | `rating.ApplyDiscounts` |
| 5 | **true-up** — the shortfall against the monthly minimum | `rating.TrueUp` |
| 6 | **tax** — on the net subtotal, the true-up included | `rating.TotalsWithDiscount` |

The order is **not a detail**, and `TestOrderOfOperations` pins it by
computing the plausible alternative beside it:

- *allowance before tiers.* With bands of 1.00 up to 100 units and 0.10
  above, an allowance of 100 against 200 units of usage costs **100.000000**
  under this order — the included quantity resets the ladder. Read the other
  way, where the allowance is free but still holds its place on the ladder,
  the same month costs **10.000000**. A factor of ten between two defensible
  readings; ours is the one the console states.
- *commitment on the head.* The same ladder, 200 units, 100 committed at
  0.50: **60.000000** here, against **150.000000** if the commitment took the
  tail.
- *discounts before the true-up.* A 1,000 minimum against 1,100 of lines and
  a 200 discount gives a **100.000000** true-up and a subtotal of exactly
  1,000. Computed the other way round the period would clear the minimum on
  its gross, and the discount would then take the bill to 900 — under the
  minimum the same contract agreed.

Money is exact throughout (`store.Decimal`, `math/big.Rat`), rounded once at
the edge, as everywhere else in this product.

**The shapes are per SKU per billing period, not per source.** An allowance
of 50 GB is 50 GB of the customer's month however many projects reported it,
so a SKU's whole quantity is rated at once and the amount is allocated back
across its lines in proportion to their quantity, by largest remainder, so
the lines of a SKU sum exactly to the SKU's figure. A line's `unit_price`
becomes the rate it actually paid, so quantity × unit price = amount still
reads across on the invoice. A SKU whose item carries no shape comes back
**byte for byte** — which is every item of every book written before §15.

**The partner waterfall is unchanged and must stay so.** Buy and net are both
derived from the same rated lines, so a tiered or allowance-bearing line
flows through the tier discount exactly as a flat one does. One thing did
have to change: the customer-facing price under `resell` is now applied as a
**ratio** of the book's list rate rather than by re-multiplying quantity ×
retail rate. For a line the price book alone rated the two are identical; for
a line a tier or an allowance reshaped, re-multiplying would throw the shape
away and bill the end customer for volume its plan included.
`TestPartnerMarginIsBuyTimesMarkupOnATieredLine` pins the invariant: on a
51,200-unit graduated line of 430.080000, with a 20 % tier and a retail rule
of buy + 25 %, buy is **344.064000** and margin is **86.016000** — buy ×
markup, exactly, and the customer's line still carries its tier rather than
the 640.000000 a flat rate would have produced.

### 15.8 Permissions

| surface | permission | scope |
|---|---|---|
| create / edit / delete a contract, replace its items | `customers.manage` | Sovereign |
| read contracts, the renewals-due list | `metering.read` | at the scope — a customer principal reads **its own** contract read-only, a partner principal **its customers'**, a Sovereign principal every one |
| issue an SLA credit | `billing.issue` | Sovereign |
| edit tiers and allowances on a price-book item | `rating.manage` | Sovereign |

A contract outside the caller's scope reads as **404**, not 403, so ids of
other customers' contracts are not confirmed — the same rule as everywhere
else in §10. Every write is audited.

### 15.9 The console

**Configure → Contracts** lists every contract with its customer, term,
monthly minimum, status and renewal date, with the **renewals due** inside
their notice window called out at the top of the page. A contract opens to
its committed-use and allowance lines, edited in place.

The **customer page gains a Contract tab**, so the agreement is where the
customer is rather than only in a directory.

A **statement** shows the true-up line named and explained, and any SLA
credit note beside the reason it was issued for.

The **price-book item editor** gains the tier ladder (mode plus bands) and the
allowance, with **the item's effective price explained in words underneath** —
"Each band rates at its own price: up to 10240 at 0.01 OMR per gb-month,
10240-102400 at 0.008 OMR per gb-month, above 102400 at 0.006 OMR per
gb-month." That sentence is produced by `rating.ExplainItem`, the same
package that does the arithmetic, so the words and the price cannot drift.

## 16. Customer self-service — what a paying customer does without the operator (EPIC #6867)

Everything above is what the operator can do. This is what the **customer**
can do alone, and it is three things: take the invoice away, keep a payment
method on file, and say an invoice is wrong. Each of the three was, until
now, an email to the operator — which is to say, a person's afternoon.

None of the three is a new mechanism. The invoice is the existing document
route (§14); the payment method rides the existing gateway registry
(`internal/settle`); the dispute's credit note is the existing credit note
(§9.3). Anything else would have been a second way to do something this
product already does, and a second way to do money is a way to disagree with
yourself.

**Who may act.** A customer principal acts on its OWN customer, and every
handler proves it the same way: the target is read through the session's
scope before anything else happens, so another customer's id answers **404**
— not a filtered list, and not a 403 that would confirm the id exists. A
partner acts on its customers by the same route, because the scope expands to
them (§13). Reading is `metering.read`; the two customer-side writes —
saving a method and raising a dispute — are **`account.topup`**, the
permission an owner and a billing user hold and a viewer does not, which is
the same permission that lets a customer ask the gateway for a top-up.
Resolving a dispute is the operator's **`billing.collect`**.

### 16.1 Download the invoice

`GET /api/v1/statements/{id}.pdf` (§14) is put in the customer's hands: a
**Download** on the statement view and on every row of its invoice list. The
route is the same handler and the same scope-filtered store read as fetching
the statement, so the permission to download the document is exactly the
permission to read what it is made of.

The response carries `Content-Disposition: attachment; filename="<invoice
number>.pdf"`, so the file lands in the customer's downloads folder already
named after the invoice — filable, forwardable, attachable to a payment run
without being renamed first. The filename is built by `docs.Filename`, which
keeps letters, digits, dash, underscore and dot and maps everything else to a
dash: an invoice number can therefore neither escape its directory nor end
the header and start another one. Both are asserted, on real values, in
`internal/docs`.

### 16.2 A saved payment method

For a customer whose payment method is a **gateway**, `POST
/api/v1/customers/{id}/payment-methods` opens a setup through the gateway
seam. `settle.Gateway` gains the pair `SetupMethod` / `ConfirmMethod`,
added exactly as `VerifyCallback` was: on the interface, with
`ErrMethodSetupNotSupported` as the honest answer from a gateway that has no
such facility — the built-in manual gateway, because a bank transfer and an
internal recharge have no instrument to keep, and a hook that predates the
seam. The Stripe-backed billing hook implements it against the billing
service's own portal session, which is the existing surface on which that
Organization's payer adds, replaces or removes a card.

**The card is never entered here, and never held here.** The payer enters it
on the gateway's page. What this product stores is the display record and
only the display record:

| Stored | Never stored |
|---|---|
| the gateway's name | the card number |
| the gateway's **token id** for the instrument | the security code |
| brand (`visa`), last four digits, expiry month and year | the full expiry-plus-number pair |
| the customer's own label, who added it, when | any gateway secret or API key |

Two of those rules are enforced rather than described. `store.PaymentMethod`
carries the token as `json:"-"`, so it reaches no wire at all: that is how
"an operator may see that a method exists but not its token" holds, by the
type rather than by a redaction each handler has to remember. And the
`last4` column carries `CHECK (last4 ~ '^[0-9]{0,4}$')`, so a gateway
implementation that mistakenly answered with a whole card number could not
record it — the INSERT fails. The audit entries (`payment_method.setup`,
`payment_method.confirm`, `payment_method.remove`) carry the method id, the
gateway and the display triple, and never the token.

`GET /customers/{id}/payment-methods` lists what is on file; `DELETE
/customers/{id}/payment-methods/{mid}` (or `DELETE /payment-methods/{id}`)
takes one off it. Removal keeps the row — who removed what, and when, is part
of the account's history — and **erases the token in the same statement**, so
a removed method cannot be charged even by a bug.

### 16.3 Dispute an invoice

`POST /api/v1/statements/{id}/disputes {reason, lines?}` opens a dispute by
the customer on its own invoice. The statement gains `disputed_at` and
`dispute_reason`, and the dispute itself is a row with a lifecycle — reason,
amount, who raised it and when, and the outcome.

**What is disputed.** With no lines named, it is the invoice's **outstanding
balance** — what is actually being chased. With lines named, it is the share
of the invoice **total** those rated lines represent, computed as an exact
rational over their amounts and rendered at the schema's six decimals, so the
figure agreed here is the figure a credit note later carries. Either way it
is capped at what the invoice can still be credited (total less credit notes
already issued), because an upheld dispute must always be creditable. A draft
is refused — there is no invoice yet — and so is a cancelled invoice, which
nobody collects on. One open dispute per invoice, enforced by a partial
unique index.

**What a dispute does, and does not do.** The disputed amount **stays on the
balance**: a dispute is not a credit and does not move money. What stops is
**collections chasing**, and both readers of the flag say so:

- `GET /collections/aging` still lists the invoice — the operator must see it
  — and marks it `disputed` with its reason. Its amount stays in `total` and
  in the aging buckets and leaves `overdue`; the report gains `disputed` and
  `disputed_invoices` at both the report and the customer-row level, so the
  gap between the buckets and the overdue figure is named rather than
  mysterious. A disputed invoice also stops driving `oldest_days`.
- The daily evaluator (`internal/collections`) passes over it: no reminder
  stage, no escalation, no suspension flowing from it. Its report carries
  `disputed`, the count of invoices it passed over, which is what tells an
  operator why a reminder did not go out.

**Resolving it.** `POST /api/v1/disputes/{id}/resolve {outcome, note}`,
`billing.collect`:

- **upheld** — a credit note is issued for the disputed amount through
  `createCreditNoteTx`, the same function `POST /statements/{id}/credit-notes`
  calls: the same numbering, the same allocation against the invoice, the same
  ledger entry as any other credit note. The balance moves by exactly the
  disputed amount. It runs **inside the transaction that records the
  outcome**, so a dispute can never end up credited but still open, nor
  resolved without the credit the customer was promised — a retry after a
  failure finds the dispute still open and no note issued. In external
  commercial mode the invoice is the billing system's and so is any correction
  to it, so this is refused with 409 — the same refusal §9.3 makes.
- **rejected** — the flag is cleared, nothing is credited, and collections
  resume from the age the invoice then is.

Either way `disputed_at` and `dispute_reason` are cleared in the same
transaction that records the outcome, so the one flag the aging report and
the evaluator read can never be left set behind a resolved dispute. Every
step is audited: `dispute.open`, `dispute.resolve`, and the credit note's own
`statement.credit_note` carrying the dispute id.

### 16.4 The console

The customer lens (`/my/...`) gains **Payment methods** beside Account, and
its statements page is the **Invoices** page: a Download on every row, a
banner naming any invoice under dispute, and a `disputed` marker beside the
status. The payment-methods card shows brand, last four and expiry, offers
`Add a payment method` (which sends the payer to the gateway's own page) and
`Remove`, and says in as many words that the card is held by the payment
provider. A viewer sees what is on file and is offered nothing to change.

On the statement view: **Download PDF** for anyone who may read the invoice,
**Dispute** for a customer that may raise one, and — for an operator with
`billing.collect` while a dispute is open — a **Resolve** control offering
the two outcomes with the consequence of each spelled out. A disputed invoice
carries a banner with the reason, the amount and the sentence that matters:
it stays on the balance and is not chased.

---

## 18. Finance handover — the journal, the reconciliation and the close (EPIC #6867)

An operator's finance department will not accept a figure it cannot
reconcile or close. Three things answer that, and all three are standard in
every billing system:

1. a **journal** — every financial event of a period as double-entry lines
   against the operator's own account codes, balanced before it is written;
2. a **reconciliation** — the gateway's settlement against what this product
   recorded, in four buckets, correcting nothing;
3. a **period close** — the month stops moving, and what may no longer be
   done to it is enforced rather than agreed.

Nothing here rates, prices or collects. It reads what already happened — the
append-only account ledger of §9, the invoices behind it, the allocations
that applied money to them — and renders it in the vocabulary a general
ledger speaks.

The code: `internal/store/ledgerexport.go` (the schema and the event reads),
`internal/finance` (the pure emitter, the balance assertion and the
settlement matcher — no SQL, no HTTP, no clock) and `internal/api/finance.go`
(the routes).

### 18.1 Which month an event belongs to

Revenue is recognised in the month it was EARNED and cash in the month it
MOVED — the ordinary accrual split. So:

- a row tied to a statement — the invoice, a credit note, a write-off —
  takes the statement's **billing period**: an invoice for August usage
  issued on 2 September is August revenue;
- everything else — a payment, a top-up, an application of account credit, a
  refund, a settlement fee — takes the day the money moved.

Each event's own debit and credit stay together either way, so every period
balances on its own. The expression is `ledgerPeriodExpr`, one CASE in one
query, and it is the only place the rule lives.

### 18.2 The journal

**The account map.** Account codes are DATA, never strings in the emitter:
`account_mappings(key, account_code, description)`, seeded with defaults on
migration and edited with `settings.manage`. The keys are fixed — this
product books to them and to nothing else — and the codes are the operator's:

| Key | Default | What posts to it |
|---|---|---|
| `receivable` | 1100 | what customers owe |
| `cash` | 1000 | money that moved, for a transfer or an internal recharge |
| `gateway_clearing` | 1010 | money a gateway holds between collecting and settling |
| `customer_advances` | 2100 | money held on account: a top-up, and a payment beyond what it settled |
| `tax_payable` | 2200 | tax an invoice owes, one line per rule the invoice froze |
| `revenue` | 4000 | the catch-all revenue account |
| `revenue.<service>` | — | revenue of one service, added by the operator per service |
| `discounts` | 4800 | what discounts took off the list price, as contra revenue |
| `credit_notes` | 4900 | the reduction of an invoice through a credit note |
| `write_offs` | 6100 | a receivable given up on |
| `gateway_fees` | 6200 | what a gateway kept, as reconciliation discovered it |
| `commission` | 6300 | what a partner is owed on a commission statement |

`<service>` is the SKU's first segment — `ecs`, `evs`, `k8s`, `plan` — the
same split every other surface of this product groups by. A service with no
account of its own falls back to `revenue`.

**The event-to-journal table.** One row per event kind; both sides of every
one are emitted together.

| Event | Debit | Credit |
|---|---|---|
| Invoice issued | `receivable` (total), `discounts` (discount total) | `revenue.<service>` (list amount, per service), `tax_payable` (per tax rule) |
| Payment received | `cash` or `gateway_clearing` | `receivable` (what it settled this period), `customer_advances` (the remainder) |
| Top-up | `cash` or `gateway_clearing` | `customer_advances` |
| Account credit applied | `customer_advances` | `receivable` |
| Credit note | `credit_notes` (total) | `receivable` (applied), `customer_advances` (unapplied) |
| Write-off | `write_offs` (total) | `receivable` (applied), `customer_advances` (unapplied) |
| Refund | `receivable`, or `customer_advances` for a refunded top-up | `cash` or `gateway_clearing` |
| Settlement fee | `gateway_fees` | `gateway_clearing` |

Three points that are decisions rather than detail:

- **The discount is a contra line, never apportioned.** An invoice books its
  LIST revenue per service and the whole discount as one debit, because
  `total = list − discount + tax` exactly; spreading the discount across
  services would introduce a rounding remainder into a document whose whole
  purpose is that it balances.
- **A payment splits.** What it settled in its own period credits the
  receivable; what it did not is account credit, and an allocation made in a
  LATER month is that month's `advance_applied` event. That is what keeps
  each period local to itself — and a closed one stable.
- **Multi-rate tax comes from the invoice's own frozen snapshot.** The
  emitter reads `statements.tax_snapshot` for a `rules` (or `lines`) array
  and emits one `tax_payable` line per rule; a snapshot with no such array is
  one rule at the snapshot's rate. Nothing here recomputes tax. When the
  rules do not sum to the tax the invoice froze, the remainder lands on the
  last rule, so the journal balances against the INVOICE rather than against
  the snapshot.

**Traceability.** Every line carries `source_kind` and `source_id` — the
statement, payment, credit note or reconciliation run it came from — plus
the invoice or credit-note number. Any figure in the export is followed back
to the object that produced it in one step.

**The balance rule.** Total debits equal total credits, in every currency and
overall, asserted in code before anything is written. A batch that does not
balance is an **error naming the difference** (`finance.ErrUnbalanced`,
which reads as `store.ErrInvalid` so the API answers 422) and it is never
exported, never stored, and never closes a period. The refusal also names
every account key the map had no code for, because that is almost always the
cause: a key with no code posts nothing, and the batch is short by exactly
that amount.

**Getting it out.** `GET /api/v1/finance/journal?period=YYYY-MM[&format=csv]`
— JSON with the lines, the totals and the balance check, or the CSV a finance
system imports (a fixed header, exact decimal strings, nothing formatted for
a human; the same batch always renders the same bytes).
`POST /api/v1/finance/journal/export {period}` queues it as a **TMF-shaped
document on the commercial outbox** — the same at-least-once lane the bills
leave by (§8.10), so an operator receives the month's journal through the
transport it already configured. The document type is `journal` and the
idempotency key is the period and its close state, so re-exporting an
unchanged period delivers one document.

### 18.3 Gateway settlement reconciliation

A gateway takes money and, days later, pays a batch into the bank with its
own list of what is in it. Finance will not sign off a cash figure until
that list and the ledger agree line for line.

`POST /api/v1/finance/reconciliation[?gateway=&from=&to=]` takes the
settlement either way:

- a **file** — multipart `file`, or a raw `text/csv` body. The header is read
  in any column order and under the aliases real gateway exports use
  (`gateway_reference` / `reference` / `transaction_id`, `amount` /
  `settled_amount`, `currency`, `settled_date` / `value_date`, `fee` /
  `commission`). A row that cannot be read is an error naming the file line:
  a settlement file is a cash document, and half of one reconciled silently
  is worse than none;
- a **fetch** — through `settle.Gateway.Settlements(ctx, from, to)`, added to
  the gateway seam exactly as `VerifyCallback` and `SetupMethod` were, with
  `ErrSettlementsNotSupported` as the default. The built-in manual gateway
  answers that (a transfer settles at the bank), and the operator uploads the
  bank's statement instead.

Each settlement line is matched to a payment by **gateway reference**,
trimmed and case-folded and compared for nothing cleverer than equality. The
report is four buckets:

| Bucket | What it means |
|---|---|
| `matched` | the reference is on both sides for the same amount |
| `amount-mismatch` | the reference is on both sides for different amounts — BOTH figures are reported, and neither is believed |
| `missing-in-ledger` | the gateway settled something no payment records |
| `missing-in-settlement` | a payment is recorded the gateway has not settled |

A fifth, `duplicate`, catches a settlement file that names one gateway
reference twice: the later line is **reported** rather than matched against
the same payment again — which is how a reconciliation quietly doubles a
cash figure — and it raises neither the matched count, the settled total nor
the fee total.

**Fees** ride on the line that matched and become their own pair of journal
lines (`gateway_fees` / `gateway_clearing`) dated on the settlement day.
That is the only way a gateway's fee ever enters the books: discovered by
reconciliation, never guessed.

**Nothing is auto-corrected.** No payment is created, amended, reallocated or
re-statused by a run. `GET /api/v1/finance/reconciliation/{id}` returns the
stored run with its rows so the operator can act on it, and
`GET /api/v1/finance/reconciliations` lists the runs.

### 18.4 Period close

`POST /api/v1/finance/periods/{period}/close` (`settings.manage`):

1. **It refuses while anything in the period is still moving** — any
   statement in it that is still a draft, and any open dispute on an invoice
   in it — answering 409 and NAMING each one. A draft is a bill nobody has
   decided on and an open dispute is a figure the customer says is wrong;
   closing over either freezes a number that is still changing.
   `GET /api/v1/finance/periods/{period}` reports the same list, plus the
   balance check as a figure, BEFORE the operator presses the button.
2. **It asserts the balance** and refuses (422) if the journal does not
   balance. The balance assertion is a hard refusal, not a warning.
3. **It stamps the period closed and freezes the journal it closed on**, in
   one transaction (`finance_periods` + `finance_journal_lines`).

After that, `GET /finance/journal` for that period is served from the frozen
lines, not re-derived. That is what makes a closed period's export stable by
construction: the same period exports byte-identically however many later
events land, and a test proves it against an unrelated later-period invoice,
payment and top-up.

**What a closed period forbids.** No statement in it may be:

- **issued** — `POST /statements/{id}/issue`
- **re-run** — `POST /statements/run {period}`, the rating run
- **cancelled** — `POST /statements/{id}/cancel`
- **credited** — `POST /statements/{id}/credit-notes` and
  `POST /contracts/{id}/sla-credit`

Each answers 409 with a message saying the period is closed, **who closed it
and when**, and that reopening with a reason is the way through. The check is
one helper (`refuseClosedPeriod`) called from the existing handlers rather
than a rule buried in the store: the store records what happened, and "this
month is shut" is a policy the operator sets and lifts.

`POST /api/v1/finance/periods/{period}/reopen {reason}` (`settings.manage`)
lifts it. The reason is required, who reopened it and why are recorded on the
row, and the whole thing is audited as `finance.period.reopen` carrying the
reason and who had closed it. The frozen journal is KEPT: the reopened period
derives live again, and the lines the close asserted stay readable as what
the books said at the time.

### 18.5 Permissions

| Surface | Needs |
|---|---|
| Read the journal, the periods, the account map, the runs; export; reconcile | `audit.read` AND `metering.read`, both at the **Sovereign** |
| Close, reopen, edit the account map | `settings.manage` at the Sovereign |

Two permissions rather than one for the read, because this surface is the
whole ledger of every customer in one document: a principal that may read one
customer's costs has not thereby been given the books. In the role matrix
that makes it the **finance-viewer** bundle and everything above it; a
billing-operator reads and reconciles but closes nothing.

**A customer and a partner reach none of it.** Every route is gated at the
Sovereign scope, so a customer-scoped or partner-scoped binding is refused
(403) on every one of them — read, write and upload alike — and the tests
walk each route as a customer owner, a partner owner and an anonymous caller.

### 18.6 The console

A **Finance** group in the Sovereign lens:

- **Journal** — a period picker, the strip (total debits, total credits, the
  balance check as a FIGURE with its difference, the accounts posted to), the
  by-account summary a ledger is posted from, the lines with their account
  codes and what each traces back to, and a CSV download.
- **Reconciliation** — upload a settlement file or fetch from the gateway,
  the four buckets with counts and amounts, a bucket filter and the
  row-level view showing both figures on a mismatch, and the list of runs.
  The page says in as many words that it reports and the operator decides.
- **Period close** — the status of each month, what blocks a close named row
  by row, Close (disabled while anything blocks it, with the reason on the
  control) and Reopen with its required reason.
- **Account mapping** — the key-to-code table with what each key is for, the
  per-service revenue accounts and how to add one, and a banner naming any
  key with no code and what that costs.

### 18.7 Tests

Pure (`internal/finance`): each event type against the MAPPED accounts, an
unmapped service falling back to `revenue`, multi-rate tax emitting one
payable line per rule, a commission statement booking to `commission`, a
deliberately unbalanced mapping refused naming the difference AND the missing
account, per-currency balance, and two renders of one batch being the same
bytes. Reconciliation: the four buckets on a seeded file, a duplicate
reference reported rather than double-matched (and not counted in the matched,
settled or fee totals), fees on the matched lines only, the alias-tolerant
parser and the rubbish it refuses, and a run leaving its input payments
untouched.

Integration (`internal/api`, against Postgres): the journal balancing and
every line tracing back, the account map driving the codes, a multi-rate tax
snapshot on a real statement, the close refused by name over a draft and over
an open dispute, issue / re-run / cancel / credit all refused after the close
with who closed it, reopen audited with who and why and the same writes
allowed again afterwards, **a closed period exporting byte-identically before
and after an unrelated later-period event**, the four buckets end to end with
the fees journaled and the payments unchanged, the journal reaching the
commercial outbox once per period, and the permission matrix per route.
