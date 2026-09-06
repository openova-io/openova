// Wire types for the chargeback API (spec §1 domain + §4 API, EPIC #6723).
// Field names mirror the Postgres columns lane A serialises; optional
// fields are the ones a list endpoint may summarise rather than embed.

export type Role = 'operator' | 'customer-admin' | 'customer-viewer'

export interface Me {
  email: string
  role: Role
  customer_id?: string | null
  /** PROFILE env: 'sovereign' | 'operator-central' (spec §6). */
  profile?: string | null
}

export type CustomerStatus = 'pending' | 'active' | 'suspended'
export type BillingMode = 'real' | 'chargeback' | 'showback'
export type SourceStatus = 'pending' | 'verified' | 'failed'
export type SourceKind = 'huawei-project' | 'openova-org' | 'k8s-namespace' | 'file'

export interface CostSource {
  id: string
  customer_id?: string
  kind: SourceKind | string
  region: string
  project_id: string
  domain_id?: string | null
  credential_id?: string | null
  status: SourceStatus
  verified_at?: string | null
  last_collected_at?: string | null
  last_error?: string | null
  /** Non-secret key id of the linked credential, for display. */
  access_key?: string | null
  /**
   * #6855 — bills only resources whose name carries this token (e.g. a
   * deployment id); empty bills the whole project.
   */
  scope_token?: string | null
  /** Whether the collector picks this source up (customer active + verified). */
  collecting?: boolean
}

export interface CustomerUser {
  customer_id?: string
  email: string
  role: 'admin' | 'viewer'
}

export interface StatementSummary {
  id?: string
  period_start?: string
  period_end?: string
  status?: string
  total?: number | string
}

export interface Customer {
  id: string
  slug: string
  name: string
  admin_email: string
  kind?: 'external' | 'organization' | string
  org_slug?: string | null
  price_book_id?: string | null
  billing_mode: BillingMode | string
  status: CustomerStatus | string
  start_date?: string | null
  created_at?: string
  updated_at?: string
  /** List endpoints may send a count; the detail endpoint embeds the rows. */
  sources?: number | CostSource[] | null
  source_count?: number | null
  verified_source_count?: number | null
  users?: CustomerUser[] | null
  last_collected_at?: string | null
  last_statement?: StatementSummary | string | null
  /** YYYY-MM-DD period start of the newest statement (list aggregate). */
  last_statement_period?: string | null
  /** Active AND at least one verified source — why nothing flows otherwise. */
  collecting?: boolean
}

export interface UsageRow {
  /**
   * The grouped column. The API returns it as `key` for EVERY grouping — the
   * day for group_by=day, the resource id for group_by=resource, the SKU for
   * group_by=sku. `day` and `resource_id` below are NOT sent by the server
   * (#6866); they are kept only so older callers still compile.
   */
  key?: string
  sku?: string
  resource_id?: string
  resource_kind?: string
  day?: string
  region?: string
  quantity: number | string
  unit?: string
  resource_count?: number
}

export interface InventoryItem {
  source_id?: string
  resource_id: string
  kind: string
  name?: string
  region?: string
  attrs?: Record<string, unknown> | null
  first_seen?: string
  last_seen?: string
  deleted_at?: string | null
}

export interface PriceItem {
  sku: string
  unit: string
  unit_price: number | string
  description?: string
}

export interface PriceBook {
  id: string
  name: string
  currency: string
  annual_divisor: number
  bill_stopped: 'compute' | 'storage-only' | 'none' | string
  effective_from?: string | null
  created_at?: string
  items?: PriceItem[] | null
}

export interface RatedLine {
  sku: string
  unit?: string
  quantity: number | string
  unit_price: number | string
  amount: number | string
  resource_count?: number
  source_id?: string | null
}

export interface Statement {
  id: string
  customer_id: string
  customer_name?: string
  customer_slug?: string
  period_start: string
  period_end: string
  currency: string
  subtotal: number | string
  tax_rate: number | string
  tax: number | string
  total: number | string
  status: 'draft' | 'issued' | string
  issued_at?: string | null
  created_at?: string
  lines?: RatedLine[] | null
  /** #6862 — what discounts took off the list subtotal, frozen at issue time. */
  discount_total?: number | string
  discount_detail?: Array<{ id?: string; name: string; kind: string; value?: number | string; sku?: string; amount: number | string }> | null
}

export interface Invite {
  token?: string
  customer_id?: string
  customer_name: string
  email?: string
  region?: string | null
  project_ids?: string[] | null
  expires_at?: string
}

export interface InviteIssued {
  invite_url: string
  expires_at: string
}

export interface ActivateResult {
  customer?: Customer
  sources?: CostSource[]
  status?: string
}

export interface ImportError {
  line?: number
  slug?: string
  message: string
}

export interface ImportResult {
  created: number
  updated: number
  errors: Array<ImportError | string>
}

export interface AuditEntry {
  id?: number | string
  customer_id?: string | null
  actor: string
  action: string
  details?: unknown
  at: string
}

export interface RunResult {
  period?: string
  statements?: Statement[]
  created?: number
  customers?: number
}

/** GET /overview — the three blocks spec §4 names; values are rendered as sent. */
export interface Overview {
  customers_by_status?: Record<string, number>
  usage_last_30d?: UsageRow[] | Record<string, unknown> | null
  rated_total_last_period?: { period?: string; currency?: string; total?: number | string } | number | string | null
}

// ---------------------------------------------------------------------------
// Cost analysis (#6867, DESIGN.md §3). Every money value is a JSON number in
// the customer's price-book currency; windows are half-open [from, to) in
// whole UTC days (so the picker's inclusive "to" date is sent as +1 day).
// ---------------------------------------------------------------------------

export type Granularity = 'day' | 'month'
export type GroupBy = 'none' | 'customer' | 'source' | 'kind' | 'sku' | 'region' | 'resource' | 'tier' | 'namespace'
export type Metric = 'cost' | 'usage'
export const GROUP_BY_OPTIONS: ReadonlyArray<{ value: GroupBy; label: string }> = [
  { value: 'kind', label: 'Service' },
  { value: 'customer', label: 'Customer' },
  { value: 'sku', label: 'SKU' },
  { value: 'resource', label: 'Resource' },
  { value: 'region', label: 'Region' },
  { value: 'source', label: 'Cost source' },
  { value: 'tier', label: 'Tier' },
  { value: 'namespace', label: 'Namespace' },
  { value: 'none', label: 'Total only' },
]
export const FILTER_DIMENSIONS: ReadonlyArray<Exclude<GroupBy, 'none'>> = [
  'customer', 'kind', 'sku', 'resource', 'region', 'source', 'tier', 'namespace',
]

export interface CostGroup {
  key: string
  label: string
  total: number
  previous: number
  delta_pct: number | null
  share: number
  resources: number
  values: number[]
}

export interface Forecast {
  month_end: number
  run_rate_daily: number
  trend_daily: number
  method: string
  days_observed: number
  days_in_month: number
  confidence: 'low' | 'medium' | 'high' | string
}

/** GET /cost/explore · GET /customers/{id}/cost/explore */
export interface ExploreResult {
  from: string
  to: string
  granularity: Granularity
  group_by: GroupBy
  metric: Metric
  currency: string
  mixed_currency: boolean
  buckets: string[]
  bucket_has_data: boolean[]
  groups: CostGroup[]
  other: CostGroup | null
  total: { current: number; previous: number; delta_pct: number | null; resources: number }
  totals_by_bucket: number[]
  unpriced: Array<{ sku: string; unit: string; quantity: number; resources: number }>
  forecast: Forecast | null
}

export interface ExploreParams {
  from: string
  to: string
  granularity?: Granularity
  group_by?: GroupBy
  metric?: Metric
  limit?: number
  include?: Partial<Record<Exclude<GroupBy, 'none'>, string[]>>
  exclude?: Partial<Record<Exclude<GroupBy, 'none'>, string[]>>
}

/** Serialises ExploreParams to the query string the API reads. */
export function exploreQuery(p: ExploreParams): string {
  const q = new URLSearchParams({ from: p.from, to: p.to })
  if (p.granularity) q.set('granularity', p.granularity)
  if (p.group_by) q.set('group_by', p.group_by)
  if (p.metric) q.set('metric', p.metric)
  if (p.limit !== undefined) q.set('limit', String(p.limit))
  for (const [dim, vals] of Object.entries(p.include ?? {})) if (vals && vals.length) q.set(dim, vals.join(','))
  for (const [dim, vals] of Object.entries(p.exclude ?? {})) if (vals && vals.length) q.set('exclude_' + dim, vals.join(','))
  return q.toString()
}

export interface DimensionValue {
  key: string
  label: string
}
/** GET /cost/dimensions */
export interface DimensionValues {
  from: string
  to: string
  dimensions: Record<string, DimensionValue[]>
}

export interface SummaryGroup {
  key: string
  label: string
  cost: number
  previous: number
  delta_pct: number | null
  share: number
  resources: number
  /** by_customer rows also carry id + name. */
  id?: string
  name?: string
}

export interface BudgetThreshold {
  pct: number
  crossed: boolean
  alerted_at?: string | null
}

export interface BudgetStatus {
  id: string
  name: string
  customer_id: string | null
  customer_name?: string | null
  amount: number
  currency: string
  period?: string
  actual: number
  forecast: number | null
  pct_actual: number
  pct_forecast: number | null
  status: 'ok' | 'warning' | 'exceeded' | string
  thresholds: BudgetThreshold[]
}

export interface Budget {
  id: string
  name: string
  customer_id: string | null
  customer_name?: string | null
  amount: number
  currency: string
  period: 'monthly' | string
  thresholds: number[]
  notify_emails: string[]
  active: boolean
  created_at?: string
  updated_at?: string
}

export interface AnomalyDriver {
  kind: 'sku' | 'resource' | string
  key: string
  label: string
  delta: number
}

export interface Anomaly {
  day: string
  customer_id: string
  customer_name: string
  dimension: string
  key: string
  label: string
  expected: number
  actual: number
  impact: number
  score: number
  drivers: AnomalyDriver[]
}

/** GET /cost/summary · GET /overview · GET /customers/{id}/cost/summary */
export interface Summary {
  profile?: string
  now: string
  currency: string
  mixed_currency: boolean
  mtd: { cost: number; from: string; to: string; days: number; resources: number }
  forecast: Forecast | null
  last_month: { period: string; cost: number }
  prev_mtd: { cost: number; days: number }
  mom_delta_pct: number | null
  avg_daily_30d: number
  last_30d: { cost: number; days_with_data: number }
  resources_live: number
  unpriced_skus: Array<{ sku: string; unit: string; quantity: number; resources: number }>
  customers: Record<string, number>
  sources: Record<string, number>
  last_collected_at: string | null
  daily: Array<{ day: string; cost: number; has_data: boolean }>
  by_customer: SummaryGroup[]
  by_kind: SummaryGroup[]
  budgets: BudgetStatus[]
  anomalies: Anomaly[]
  statements: { draft: number; issued: number; latest: Statement[] }
}

export interface Recommendation {
  id: string
  type: string
  severity: 'high' | 'medium' | 'low' | string
  customer_id: string
  customer_name: string
  resource_id?: string | null
  resource_name?: string | null
  kind?: string | null
  title: string
  detail: string
  monthly_saving: number
  currency: string
  evidence?: Record<string, unknown> | null
}

export interface ResourceLine {
  sku: string
  unit: string
  quantity: number
  cost: number
}

export interface ResourceRow {
  source_id: string
  resource_id: string
  kind: string
  name: string
  region: string
  customer_id: string
  customer_name: string
  status: 'live' | 'stopped' | 'deleted' | string
  first_seen: string | null
  last_seen: string | null
  deleted_at: string | null
  cost: number
  currency: string
  lines: ResourceLine[]
  attrs?: Record<string, unknown> | null
}

export interface ResourceList {
  rows: ResourceRow[]
  total: number
  sum_cost: number
  limit: number
  offset: number
  currency: string
}

export interface ResourceDetail extends ResourceRow {
  daily: Array<{ day: string; cost: number; has_data: boolean }>
  transitions?: Array<Record<string, unknown>>
  records_recent?: Array<Record<string, unknown>>
}

export interface AllocationSettings {
  weights: { vcpu: number; mem_gib: number; pvc_gb: number }
  overhead_policy: 'separate' | 'distribute' | string
  pool: 'sovereign-cost' | 'manual' | string
  manual_amount: number
  currency: string
  sovereign_customer_id: string | null
  updated_at?: string
}

export interface AllocationRow {
  customer_id: string
  customer_slug: string
  customer_name: string
  tier: 'organization' | 'platform-overhead' | string
  vcpu_hours: number
  mem_gib_hours: number
  pvc_gb_hours: number
  weight: number
  share: number
  allocated_cost: number
  rated_revenue: number
  margin: number
  margin_pct: number | null
}

export interface AllocationResult {
  from: string
  to: string
  settings: AllocationSettings
  pool: { source: string; amount: number; currency: string; customer_id: string | null; customer_name?: string | null }
  rows: AllocationRow[]
  share_total: number
  totals: { allocated: number; revenue: number; margin: number }
}

export interface SavedView {
  id: string
  name: string
  page: string
  params: ExploreParams & Record<string, unknown>
  owner_email?: string
  created_at?: string
}

export interface Discount {
  id: string
  customer_id: string | null
  customer_name?: string | null
  name: string
  kind: 'percent' | 'fixed' | string
  value: number
  sku: string
  starts_at: string | null
  ends_at: string | null
  active: boolean
  created_at?: string
}

export interface PriceBookCoverage {
  customers: Array<{ id: string; name: string; slug: string }>
  skus_in_use: Array<{ sku: string; unit: string; quantity_30d: number; resources: number; priced: boolean; unit_price: number | null }>
  coverage_pct: number
  unpriced_count: number
}
