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

## 2. Information architecture

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

### 2.1 Overview
KPI strip: month-to-date cost · forecast month end (with method + confidence) ·
last month · MoM Δ% · average daily (30d) · live resources · active customers ·
unpriced SKUs (warning). Daily cost stacked by kind for the last 30 days with the
forecast tail dashed. Cost by customer (donut + ranked table). Cost by service
kind (ranked bars). Budgets strip (actual vs amount, forecast marker). Latest
anomalies. Recent statements. Collector health (sources verified/failed, last
collected).

### 2.2 Cost explorer
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

### 2.3 Resources
Inventory joined with cost in the window: kind, name, region, customer, status
(live / stopped / deleted), first/last seen, cost, cost sparkline. Filters and
free text. Drill-in shows daily cost, SKU lines, attributes, transitions.

### 2.4 Customer detail
Header KPIs (MTD, forecast, last month, open drafts). Tabs: Overview (trend + by
kind + top resources) · Cost explorer (scoped) · Resources · Statements ·
Discounts (CRUD) · Budgets (CRUD) · Sources (CRUD incl. scope token) · Users ·
Settings (edit every field; delete) · Audit.

### 2.5 Price books
List with currency, items, assigned customers, coverage % of SKUs in use.
Detail: settings (edit), searchable inline item table (add / edit / delete / bulk
save), import CSV with preview, export CSV, clone (per-account pricing), delete
(refused while assigned), coverage panel listing SKUs in use that carry no rate.

### 2.6 Discounts
All discounts in one place: scope (customer or all customers), kind, value, SKU
scope, campaign window, active. Create / edit / delete / toggle. Preview panel
shows the effect on the current MTD.

### 2.7 Budgets
Create / edit / delete. Scope (all or one customer), monthly amount, thresholds,
notification emails. Status bars: actual, forecast marker, thresholds crossed.

### 2.8 Allocation
Settings editor: basis weights (vCPU-h, GiB-h, GB-h), overhead policy (keep as a
separate line, or distribute across Organizations), cost pool (the Sovereign
customer's rated cloud cost for the window, or a manual amount). Result table in
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
source, only while the Organization is active and on a plan other than flexi,
starting at the customer's `start_date` or, absent one, the first sync. OrgSync
reads `spec.planSlug` (lower-cased; empty → `s`, the org-controller's default;
the Sovereign's own Organization gets none) into `customers.plan_slug`, and
owns the **"OpenOva plans"** price book: OMR, divisor 8760, `plan.s` 60/yr,
`plan.m` 108, `plan.l` 192, `plan.xl` 360 as annual prices, so
`unit_price = annual / 8760 = monthly / 730` per plan-hour; created once when
absent, assigned to every tenant Organization customer with no book, never
re-created, re-priced or re-assigned over an operator's choice. `k8s.vcpu` /
`k8s.mem_gb` / `k8s.pvc_gb` stay unpriced in that book — they are the allocation
basis above, and flexi's pay-per-use rates are a product decision the founder
has not made (the item descriptions say so; `price_books` has no description
column). `rated_revenue` needs no new arithmetic: it is the Explore total per
Organization customer, and the plan line is part of it. Statements group the
line under "Subscription plan" (`KindLabel("plan")`, `serviceOfSKU("plan.m")`).

### 2.9 Statements
Filters (period, customer, status). Run period. Statement view: waterfall (list
→ discounts → net → tax → total), lines grouped by service kind with per-source
breakdown, printable, CSV. The Issue confirm carries a checked-by-default
"Email the statement to the customer" box (§3.9).

### 2.10 Reports
Scheduled plain-text cost reports, the way a cloud console mails a cost report
on a schedule. KPIs (schedules, sent last 30 days, failures, next due); table
(name, scope, cadence in words, recipients, sections, next run, last sent,
active) with Create / Edit (name, scope, cadence, weekday or day-of-month,
hour UTC, recipients, section checkboxes, active), Delete, Preview (the exact
text the next send mails, in a `<pre>`), Send now, and a deliveries log per
schedule. Customer lens `/my/reports`: a customer-admin manages schedules for
its own customer only; a viewer reads and previews.

## 3. API contracts (all under `/api/v1`, JSON, scope-filtered)

Dates are `YYYY-MM-DD`, windows are half-open `[from, to)`. Money is a decimal
number in the customer's price-book currency. `customer` query values are
customer ids; the customer role is forced to its own id server-side.

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
The overview payload: `currency`, `mtd{cost,from,to,days}`,
`forecast{month_end,run_rate_daily,trend_daily,method,days_observed,days_in_month,confidence,projection[{day,cost}],weekday_factors?}`
(same object as §3.1),
`last_month{period,cost}`, `prev_mtd{cost}` (same day count last month),
`mom_delta_pct`, `avg_daily_30d`, `resources_live`, `unpriced_skus`,
`customers{active,pending,suspended}`, `sources{verified,failed,pending}`,
`last_collected_at`, `daily[{day,cost}]` (30 days), `by_customer[{id,name,slug,cost,share}]`,
`by_kind[{key,label,cost,share}]`, `budgets[…status rows]`, `anomalies[…]`,
`statements{draft,issued,latest[…]}`. `GET /overview` returns the same document.

### 3.3 `GET /cost/export.csv`
Same params as explore; one row per (bucket, group).

### 3.4 `GET /resources` · `GET /customers/{id}/resources`
Params: `from`, `to`, `kind`, `region`, `status=live|stopped|deleted|all`, `q`,
`sort=cost|name|kind|first_seen|last_seen`, `order`, `limit`, `offset`.
Returns `rows[{source_id,resource_id,kind,name,region,customer_id,customer_name,
status,first_seen,last_seen,deleted_at,cost,currency,lines[{sku,unit,quantity,cost}]}]`,
`total`, `sum_cost`. `GET /resources/{source_id}/{resource_id}` adds `daily[]`,
`attrs`, `transitions`, `records_recent[]`.

### 3.5 Budgets
`GET|POST /budgets`, `GET|PUT|DELETE /budgets/{id}`, `GET /budgets/{id}/status?period=YYYY-MM`,
`GET /customers/{id}/budgets`. Budget: `{id,name,customer_id|null,amount,currency,
period:"monthly",thresholds:[50,80,100],notify_emails:[…],active}`. Status:
`{actual,forecast,pct_actual,pct_forecast,status:ok|warning|exceeded,
thresholds:[{pct,crossed,alerted_at}]}`. An hourly evaluator records a crossing
once per threshold per period (`budget_alerts`), writes an audit entry and mails
`notify_emails`.

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
monthly_saving,currency,evidence}` and `total_monthly_saving`. Types:
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
- `PATCH /sources/{id}` — region, project_id, scope_token, domain_id.
- `DELETE /statements/{id}` — drafts only.
- `GET|PUT /allocation/settings` — `{weights{vcpu,mem_gib,pvc_gb},overhead_policy:
  separate|distribute,pool:sovereign-cost|manual,manual_amount,currency,
  sovereign_customer_id}`; `GET /allocation` returns rows with `allocated_cost`,
  `rated_revenue`, `margin`, `margin_pct`, plus `pool` and `totals`.
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
```

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
