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
Bill       Statements · Budgets · Reports
Configure  Customers · Price books · Discounts · Allocation
```

Customer lens (`/my/…`): Overview · Cost explorer · Resources · Statements · Budgets · Reports · Sources.

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
| What the Organization buys | a fixed shape, enforced by a ResourceQuota (S 2 vCPU / 4 GiB, M 4/8, L 8/16, XL 16/32, all Guaranteed) | nothing fixed: `planQuotaTable` gives flexi no CPU/memory ceiling and Burstable QoS |
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
are never converted. `customer` query values are customer ids; the customer
role is forced to its own id server-side.

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
real usage in a different service mix, from one bucket to the next. Six
customers appearing and vanishing against a platform with no history reads as
"nothing existed, then everything appeared" — the opposite of the story.

The fix is a synthetic past for the landlord that **converges on its real
present**, so the join is invisible rather than merely covered. The end state
(`synth.LandlordEndState`) is the measured shape of that customer's real
cloud-layer usage sampled on 5 September 2026 — 10 `m7n.2xlarge.8` and 2
`m7n.xlarge.8`, six EIPs reserving 1,200 Mbps, 102 volumes totalling 2,281 GB,
two NAT gateways, two load balancers — and the backfill grows into exactly that
across two steps (1 July, 1 August) and a storage ramp, then stops at the hour
boundary before the first real record. Priced on the National Cloud list the
final full day is 596.63 OMR against a measured 594.10: a seam 0.43 % wide,
pinned by test at 2 %.

**Reservation, not traffic.** 494 of those 594 OMR are EIP bandwidth, and the
reason is that Huawei bills a pipe's *provisioned* size whether or not traffic
flows through it. So bandwidth is constant per EIP per hour and steps only when
an EIP is added — as are the EIP itself, the NAT gateways, the load balancers,
the instance-hours and each volume's size. Only the storage TOTAL drifts, and
only because volumes are created (a 32-step staircase tracking a straight line
from 1,400 to 2,281 GB). Jittering any of them would make the data contradict
the billing model it is there to explain; two tests in
`internal/synth/landlord_test.go` — one on reserved bandwidth, one on the
volume roster — hold the line.

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
for the real half: 98 daily buckets from 1 June to 6 September, **not one of
them empty**, and the landlord's last synthetic day and first full real day
both 594.09 OMR — a 0.00 % seam. A purge then removed 545,274 synthetic usage
rows, 257 inventory rows, 7 sources and 6 customers while leaving the landlord,
its real source and all 14,300 real rows untouched.

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
