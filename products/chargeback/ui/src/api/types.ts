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
/**
 * `disabled` is a decommissioned source (coordinator direction 2026-09-08):
 * the collector skips it and it counts as neither verified nor live, but its
 * history keeps rating — in the explorer and on every statement already
 * issued from it.
 */
export type SourceStatus = 'pending' | 'verified' | 'failed' | 'disabled'
export type SourceKind = 'huawei-project' | 'openova-org' | 'openova-platform' | 'k8s-namespace' | 'file'
/**
 * The two layers a source belongs to (DESIGN.md §2). A CLOUD source is a
 * cloud project whose resource kinds are cloud SKUs, priced by a cloud price
 * book; a PLATFORM source is an Organization on this Sovereign whose
 * resource kinds are platform SKUs, priced by a platform book. The two never
 * meet in billing: the book is assigned per source and its scope must equal
 * the source's layer.
 */
export type Layer = 'cloud' | 'platform'

export interface CostSource {
  id: string
  customer_id?: string
  /** Joined for the operator-wide directory; absent on a customer's own list. */
  customer_name?: string
  kind: SourceKind | string
  /** Derived from `kind` — cloud (huawei-project, file) or platform. */
  layer: Layer | string
  /** The book that rates THIS source; null = none, so its SKUs rate to 0. */
  price_book_id?: string | null
  price_book_name?: string
  /** The Sovereign's own platform source: no customer, never billed. */
  internal?: boolean
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
  /** @deprecated The book is assigned per SOURCE (DESIGN.md §4.1); this is never written. */
  price_book_id?: string | null
  /**
   * @deprecated DESIGN.md §8 — DERIVED from charging + payment_method on
   * every write and never sent by this UI. Kept on the wire for readers
   * written against it.
   */
  billing_mode: BillingMode | string
  /** DESIGN.md §8 — is anything collected at all? */
  charging?: 'billed' | 'informational' | string
  /** When it is paid; absent when charging is informational. */
  payment_model?: 'prepaid' | 'postpaid' | string | null
  /** How the money moves; absent when charging is informational. */
  payment_method?: 'gateway' | 'transfer' | 'internal' | string | null
  /** Which gateway collects, when payment_method is gateway. */
  gateway_name?: string | null
  /** The customer's standing purchase-order reference, copied onto invoices. */
  po_reference?: string | null
  /** Net terms an invoice falls due in; 0 is due on receipt. */
  payment_terms_days?: number | null
  /**
   * DESIGN.md §8.10 — this customer's account in the operator's own billing
   * system, when that system is the Sovereign's system of record.
   */
  external_account_id?: string | null
  /** DESIGN.md §9.4 — the tax profile; tax_rate is a fraction overriding the Sovereign default (absent = default). */
  tax_registration_number?: string | null
  tax_exempt?: boolean
  tax_exempt_reason?: string | null
  tax_rate?: number | string | null
  /** DESIGN.md §9.5 — apply available credit to every invoice at issue. */
  auto_apply_credit?: boolean
  /** Prepaid wallet: alert below this (absent = off) and suspend at zero. */
  low_balance_threshold?: number | string | null
  suspend_at_zero?: boolean
  /** DESIGN.md §9.7 — set while this product holds the Organization suspended at the platform. */
  platform_suspended_at?: string | null
  suspension_reason?: string | null
  suspension_source?: 'collections' | 'wallet' | 'operator' | 'import' | string | null
  /** The balance the external billing system last reported (external mode). */
  external_balance?: number | string | null
  external_balance_at?: string | null
  /**
   * DESIGN.md §9 — the ledger balance from the customer_balances view,
   * accounting-signed: positive is owed by the customer, negative is credit
   * it holds. Read-only bare numbers; absent on a document written before
   * they existed.
   */
  balance?: number
  available_credit?: number
  status: CustomerStatus | string
  start_date?: string | null
  /** Catalog plan (s, m, l, xl, flexi; '' = none) — an Organization's comes from its CR. */
  plan_slug?: string | null
  created_at?: string
  updated_at?: string
  /** List endpoints may send a count; the detail endpoint embeds the rows. */
  sources?: number | CostSource[] | null
  source_count?: number | null
  verified_source_count?: number | null
  /** Sources per layer — the list's "1 cloud · 1 platform" column. */
  cloud_source_count?: number | null
  platform_source_count?: number | null
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
  /** List (annual) price when the item came from an annual list; unit_price = annual_price ÷ annual_divisor. */
  annual_price?: number | string | null
  description?: string
}

export interface PriceBook {
  id: string
  name: string
  /**
   * Which layer of source this book may price (DESIGN.md §2): a cloud book
   * prices cloud SKUs, a platform book platform SKUs (plan.<slug>, and the
   * k8s.* meters only when they are sold per use).
   */
  scope: Layer | string
  currency: string
  annual_divisor: number
  bill_stopped: 'compute' | 'storage-only' | 'none' | string
  effective_from?: string | null
  /**
   * The operator-editable note on the book itself: what it is for and, on
   * the two books the Organization sync owns, where every rate in it came
   * from. Absent on a document that predates the field.
   */
  description?: string
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

/**
 * One payment the customer made (DESIGN.md §8). It belongs to the CUSTOMER
 * and is linked to the invoice it was recorded against, so a later lane can
 * allocate one payment across invoices and hold unallocated credit without
 * changing this shape.
 */
export interface StatementPayment {
  id: number
  customer_id?: string
  /** The invoice it was recorded against; absent for unallocated credit. */
  statement_id?: string
  amount: number | string
  paid_at: string
  /** How the money arrived. */
  method?: 'gateway' | 'transfer' | 'internal' | string
  reference?: string
  /** Only a received payment counts towards the balance. */
  status?: 'received' | 'pending' | 'failed' | string
  /** "manual" when the operator recorded a transfer, else the gateway. */
  gateway?: string
  recorded_by?: string
  recorded_at?: string
  /**
   * DESIGN.md §9.2 — allocation. On a customer's payment list `allocated` is
   * what reached invoices and `unallocated` the credit left on account; on
   * an invoice document `allocated` is what reached THAT invoice.
   */
  allocated?: number | string
  unallocated?: number | string
  allocations?: Allocation[] | null
  /** checkout (the customer paid now) or collection (an unpaid invoice was pursued). */
  purpose?: 'checkout' | 'collection' | string
  intent_id?: string
  refunded_at?: string | null
  refund_reason?: string
  note?: string
}

/** A payment as the account lists it — the same object as StatementPayment. */
export type Payment = StatementPayment

/** One application of a payment or a credit note to an invoice. */
export interface Allocation {
  id: number
  statement_id: string
  invoice_number?: string
  payment_id?: number
  credit_note_id?: string
  amount: number | string
  allocated_at: string
  allocated_by?: string
}

/** DESIGN.md §9.3 — the only way an issued invoice is reduced. */
export interface CreditNote {
  id: string
  customer_id: string
  statement_id: string
  invoice_number?: string
  number: string
  kind: 'partial' | 'full' | 'write_off' | string
  reason?: string
  currency: string
  subtotal: number | string
  tax_rate: number | string
  tax: number | string
  total: number | string
  lines?: Array<{ sku?: string; description?: string; quantity?: number | string; unit?: string; unit_price?: number | string; amount: number | string }> | null
  /** What reduced the invoice, and what became credit on the account. */
  applied: number | string
  unapplied: number | string
  issued_at: string
  issued_by?: string
}

/** DESIGN.md §9.4 — what an issued invoice carries about tax, frozen at issue. */
export interface TaxSnapshot {
  rate: number | string
  exempt: boolean
  exempt_reason?: string
  customer_name: string
  customer_tax_registration_number?: string
  seller_legal_name?: string
  seller_tax_registration_number?: string
  seller_address?: string
}

/** One row of the account ledger; amount is signed (positive = owed). */
export interface AccountEntry {
  id: number
  customer_id: string
  kind: 'invoice' | 'payment' | 'credit_note' | 'refund' | 'write_off' | 'top_up' | string
  amount: number | string
  currency: string
  statement_id?: string
  invoice_number?: string
  payment_id?: number
  credit_note_id?: string
  credit_note_number?: string
  reference?: string
  note?: string
  /** The running balance after this entry. */
  balance: number | string
  entered_at: string
  entered_by?: string
}

/** GET /customers/{id}/account */
export interface AccountDocument {
  customer_id: string
  currency: string
  /** The ledger sum: positive owed, negative in credit. */
  balance: number | string
  available_credit: number | string
  outstanding: number | string
  overdue: number | string
  open_invoices: number
  account_owner: 'internal' | 'external' | string
  external_balance?: number | string | null
  external_balance_at?: string | null
  entries: AccountEntry[]
  payments: Payment[]
  credit_notes: CreditNote[]
  suspension?: { suspended_at: string; source: string; reason?: string } | null
  payment_model?: string
  auto_apply_credit?: boolean
  suspend_at_zero?: boolean
  low_balance_threshold?: number | string | null
}

/** DESIGN.md §9.2 — one request to the gateway seam and its outcome. */
export interface PaymentIntent {
  id: string
  customer_id: string
  purpose: 'checkout' | 'collection' | string
  statement_id?: string
  amount: number | string
  currency: string
  gateway?: string
  status: 'requested' | 'pending' | 'settled' | 'failed' | 'refused' | 'awaiting-transfer' | string
  reference?: string
  pay_url?: string
  detail?: string
  payment_id?: number
  created_at: string
  updated_at: string
}

/** DESIGN.md §9.7 — one executed suspend / resume with the platform's answer. */
export interface Suspension {
  id: number
  customer_id: string
  action: 'suspend' | 'resume' | string
  source: string
  reason?: string
  ok: boolean
  error?: string
  actor?: string
  at: string
}

export type AgingBucket = 'current' | '1-30' | '31-60' | '61-90' | 'over-90'

export interface AgingRow {
  customer_id: string
  customer_name: string
  customer_slug: string
  currency: string
  buckets: Partial<Record<AgingBucket, number | string>>
  total: number | string
  overdue: number | string
  oldest_days: number
  invoices: number
  suspended: boolean
  suspended_at?: string | null
  suspension_source?: string
  suspension_reason?: string
  available_credit: number | string
}

/** GET /collections/aging */
export interface AgingReport {
  as_of: string
  buckets: string[]
  rows: AgingRow[]
  totals: Partial<Record<AgingBucket, number | string>>
  total: number | string
  overdue: number | string
  invoices: Array<{ statement_id: string; invoice_number?: string; customer_id: string; customer_name: string; currency: string; due_at: string; days_past_due: number; bucket: AgingBucket | string; outstanding: number | string; status: string }>
  collections_owner: 'internal' | 'external' | string
}

/** POST /collections/run */
export interface CollectionsRun {
  invoices: number
  reminders: number
  escalations: number
  suspended: number
  resumed: number
  mails: number
  errors: number
  skipped: boolean
  skip_reason?: string
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
  status: 'draft' | 'issued' | 'sent' | 'paid' | 'cancelled' | string
  /**
   * DESIGN.md §8 — `status`, except that a sent invoice past its due date
   * with money outstanding reads "overdue". Absent on a document written
   * before invoicing existed, so readers fall back to `status`.
   */
  effective_status?: 'draft' | 'issued' | 'sent' | 'paid' | 'overdue' | 'cancelled' | string
  /** Assigned at issue: gapless per calendar year, unique. */
  invoice_number?: string | null
  /**
   * DESIGN.md §8.10 — what the operator's billing system knows this invoice
   * by, when that system is the Sovereign's system of record. Present
   * INSTEAD of invoice_number: we never number an invoice for them.
   */
  external_invoice_ref?: string | null
  /** The purchase order this invoice quotes. */
  po_reference?: string | null
  /** Net terms the due date was computed from. */
  payment_terms_days?: number | null
  due_at?: string | null
  sent_at?: string | null
  paid_at?: string | null
  cancelled_at?: string | null
  cancel_reason?: string | null
  /** Sum of the recorded payments, and total − paid − credited. Computed on read. */
  paid_total?: number | string
  balance?: number | string
  /** DESIGN.md §9.3 — what credit notes took off this invoice. */
  credited_total?: number | string
  tax_snapshot?: TaxSnapshot | null
  credit_notes?: CreditNote[] | null
  payments?: StatementPayment[] | null
  issued_at?: string | null
  created_at?: string
  lines?: RatedLine[] | null
  /** #6862 — what discounts took off the list subtotal, frozen at issue time. */
  discount_total?: number | string
  discount_detail?: Array<{
    id?: string
    discount_id?: string
    name: string
    kind: string
    value?: number | string
    sku?: string
    amount: number | string
    /** DESIGN.md §2.11 — added on top of the winner. */
    stackable?: boolean
    /** DESIGN.md §2.11 — matched but lost to this discount id; amount is 0. */
    superseded_by?: string
  }> | null
  /** DESIGN.md §2.11 — the combination rule the run applied; absent on statements rated before it existed. */
  discount_rule?: DiscountRule | string | null
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
// the REPORTING currency (§3.10: allocation settings → currency; price-book
// currencies are converted at the stored rates, and usage in a currency
// without a rate is listed under `unconverted`, never summed); windows are
// half-open [from, to) in whole UTC days (so the picker's inclusive "to" date
// is sent as +1 day). Buckets are `YYYY-MM-DDTHH` (hour, UTC), `YYYY-MM-DD`
// (day) or `YYYY-MM`.
// ---------------------------------------------------------------------------

/** One stored exchange rate: how many units of `code` one reporting unit buys. */
export interface CurrencyRate {
  code: string
  per_base: number
  /** "manual", or the feed a future importer names; "reporting" on GET of the reporting currency itself. */
  source: string
  updated_at?: string
}

/** GET /currencies */
export interface CurrencyRates {
  reporting_currency: string
  rates: CurrencyRate[]
}

/** Priced usage in a book currency that has no rate — left out of every total. */
export interface UnconvertedCurrency {
  currency: string
  records: number
  /** In `currency`, not the reporting currency. */
  cost: number
}

export type Granularity = 'hour' | 'day' | 'month'
/** The fixed dimensions the server lists in CostDimensions(). */
export type StaticGroupBy = 'none' | 'customer' | 'source' | 'kind' | 'sku' | 'region' | 'resource' | 'tier' | 'namespace' | 'enterprise_project'
/** A resource-tag dimension, `tag:<key>` — dynamic, one per tag key present (lib/tags.ts). */
export type TagDimension = `tag:${string}`
export type GroupBy = StaticGroupBy | TagDimension
export type Metric = 'cost' | 'usage'
export const GROUP_BY_OPTIONS: ReadonlyArray<{ value: StaticGroupBy; label: string }> = [
  { value: 'kind', label: 'Service' },
  { value: 'customer', label: 'Customer' },
  { value: 'sku', label: 'SKU' },
  { value: 'resource', label: 'Resource' },
  { value: 'region', label: 'Region' },
  { value: 'source', label: 'Cost source' },
  { value: 'tier', label: 'Tier' },
  { value: 'namespace', label: 'Namespace' },
  { value: 'enterprise_project', label: 'Enterprise project' },
  { value: 'none', label: 'Total only' },
]
export const FILTER_DIMENSIONS: ReadonlyArray<Exclude<StaticGroupBy, 'none'>> = [
  'customer', 'kind', 'sku', 'resource', 'region', 'source', 'tier', 'namespace', 'enterprise_project',
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

/** One projected (or observed) day of the month-end forecast. */
export interface ForecastDay {
  day: string
  cost: number
}

export interface Forecast {
  month_end: number
  run_rate_daily: number
  trend_daily: number
  /** run-rate-Nd (< 7 days) · run-rate-7d+trend (7–13) · weekday-seasonal (≥ 14) */
  method: string
  days_observed: number
  days_in_month: number
  confidence: 'low' | 'medium' | 'high' | string
  /** One entry per remaining day, today first; sums to month_end − observed. Absent from older APIs. */
  projection?: ForecastDay[]
  /** Mon…Sun cost relative to the overall mean; only for weekday-seasonal. */
  weekday_factors?: Record<string, number>
}

/**
 * The half-open window every `previous` in the document was summed over.
 * label is "previous period" (the automatic same-length window before `from`)
 * or "custom" (the caller's compare_from/compare_to).
 */
export interface CompareWindow {
  from: string
  to: string
  label: 'previous period' | 'custom' | string
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
  /**
   * Platform meters (k8s.*) on a platform source whose book prices none of
   * them: the allocation basis, deliberately not sold per use — never a gap
   * in the book, so the page says so instead of asking for a rate.
   */
  not_sold_per_use?: Array<{ sku: string; unit: string; quantity: number; resources: number }>
  forecast: Forecast | null
  compare: CompareWindow
  /** Usage no total includes because its book currency has no rate; mixed_currency is true exactly when non-empty. Absent from older APIs. */
  unconverted?: UnconvertedCurrency[]
}

export interface ExploreParams {
  from: string
  to: string
  /** `hour` is accepted for windows of at most 14 days. */
  granularity?: Granularity
  group_by?: GroupBy
  metric?: Metric
  limit?: number
  include?: Partial<Record<Exclude<GroupBy, 'none'>, string[]>>
  exclude?: Partial<Record<Exclude<GroupBy, 'none'>, string[]>>
  /** Custom compare window, half-open; both or neither. Omitted = previous period of equal length. */
  compare_from?: string
  compare_to?: string
}

/** Serialises ExploreParams to the query string the API reads. */
export function exploreQuery(p: ExploreParams): string {
  const q = new URLSearchParams({ from: p.from, to: p.to })
  if (p.granularity) q.set('granularity', p.granularity)
  if (p.group_by) q.set('group_by', p.group_by)
  if (p.metric) q.set('metric', p.metric)
  if (p.limit !== undefined) q.set('limit', String(p.limit))
  if (p.compare_from && p.compare_to) {
    q.set('compare_from', p.compare_from)
    q.set('compare_to', p.compare_to)
  }
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
  /** Static dimensions always; `tag:<key>` when the query grouped or filtered by that tag. */
  dimensions: Record<string, DimensionValue[]>
  /** Distinct tag keys on the records in the window (scoped) — the "group by tag" picker. */
  tag_keys?: string[]
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
  /** Month-to-date usage left out for want of a rate (the 30-day series when the month has none). Absent from older APIs. */
  unconverted?: UnconvertedCurrency[]
  mtd: { cost: number; from: string; to: string; days: number; resources: number }
  forecast: Forecast | null
  last_month: { period: string; cost: number }
  prev_mtd: { cost: number; days: number }
  mom_delta_pct: number | null
  avg_daily_30d: number
  last_30d: { cost: number; days_with_data: number }
  resources_live: number
  unpriced_skus: Array<{ sku: string; unit: string; quantity: number; resources: number }>
  /** Platform meters a platform book deliberately does not price (§2.5). */
  not_sold_per_use?: Array<{ sku: string; unit: string; quantity: number; resources: number }>
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
  /** Some of this resource's priced usage has no exchange rate, so `cost` understates it. */
  unconverted?: boolean
}

export interface ResourceList {
  rows: ResourceRow[]
  total: number
  sum_cost: number
  limit: number
  offset: number
  currency: string
  /** True when a row in the filtered set is unconverted. */
  mixed_currency?: boolean
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
  /** The REPORTING currency of every cost screen (§3.10), not only of the pool. */
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
  /** Pool or revenue usage in a book currency with no rate — money the split could not see. */
  unconverted?: UnconvertedCurrency[]
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
  /** DESIGN.md §2.11 — adds on top of the winner under most-specific / highest. */
  stackable?: boolean
  created_at?: string
}

/** DESIGN.md §2.11 — how several percent discounts on one line combine. */
export type DiscountRule = 'most-specific' | 'highest' | 'stack' | 'compound'

export interface BillingSettings {
  discount_rule: DiscountRule | string
  /** DESIGN.md §8 — what invoice numbers start with. */
  invoice_prefix?: string
  /** DESIGN.md §8.10 — who invoices, and the external-ingest variant (§9.1). */
  commercial_provider?: 'internal' | 'external' | string
  external_ingest?: 'rated_bill' | 'summary_charge' | string
  /** DESIGN.md §9.4 — the Sovereign's tax identity and default rate. */
  tax_rate?: number | string
  tax_registration_number?: string
  legal_name?: string
  address?: string
  credit_note_prefix?: string
  /** DESIGN.md §9.6 — the collections schedule; negative days are before the due date. */
  reminder_days?: number[]
  escalation_days?: number
  escalation_action?: 'notify' | 'suspend' | string
  updated_at?: string
}

/** One source assigned to a price book (DESIGN.md §2.5 coverage). */
export interface CoverageSource {
  source_id: string
  customer_id: string
  customer_name: string
  customer_slug: string
  label: string
  kind: string
  layer: Layer | string
}

export interface PriceBookCoverage {
  scope?: Layer | string
  /** The SOURCES assigned to this book — what its coverage is measured over. */
  sources?: CoverageSource[]
  customers: Array<{ id: string; name: string; slug: string }>
  skus_in_use: Array<{ sku: string; unit: string; quantity_30d: number; resources: number; priced: boolean; not_sold_per_use?: boolean; unit_price: number | null }>
  coverage_pct: number
  unpriced_count: number
  /** Platform meters this platform book deliberately does not price. */
  not_sold_count?: number
}

// ---------------------------------------------------------------------------
// Scheduled cost reports (#6867 follow-up). A schedule mails a plain-text
// report on a cadence; every attempt is a delivery row.
// ---------------------------------------------------------------------------

export type ReportCadence = 'daily' | 'weekly' | 'monthly'
export type ReportSection = 'summary' | 'services' | 'customers' | 'budgets' | 'anomalies' | 'recommendations'

/** Every section the server knows, in the order it renders them. */
export const REPORT_SECTIONS: ReadonlyArray<{ value: ReportSection; label: string; hint: string }> = [
  { value: 'summary', label: 'Summary', hint: 'total vs previous period, month to date, forecast, unpriced usage' },
  { value: 'services', label: 'Top services', hint: 'the five biggest service kinds' },
  { value: 'customers', label: 'Top customers', hint: 'the five biggest customers (operator reports only)' },
  { value: 'budgets', label: 'Budgets', hint: 'every active budget with its standing' },
  { value: 'anomalies', label: 'Anomalies', hint: 'flagged days in the window and the biggest' },
  { value: 'recommendations', label: 'Recommendations', hint: 'count, total saving and the top three' },
]

export interface ReportSchedule {
  id: string
  name: string
  customer_id: string | null
  customer_name?: string | null
  cadence: ReportCadence | string
  /** 0 = Sunday … 6 = Saturday; set for weekly schedules. */
  day_of_week: number | null
  /** 1..28; set for monthly schedules. */
  day_of_month: number | null
  hour_utc: number
  recipients: string[]
  sections: string[]
  active: boolean
  last_sent_at: string | null
  next_at: string
  created_at?: string
  updated_at?: string
  /** Delivery attempts in the last 30 days, how many failed, newest failure. */
  sent_30d: number
  failed_30d: number
  last_error: string | null
}

export interface ReportDelivery {
  id: number
  schedule_id: string
  sent_at: string
  window_from: string
  /** Half-open end: the day after the last reported day. */
  window_to: string
  recipients: string[]
  subject: string
  ok: boolean
  error: string | null
}

/** GET /reports/schedules/{id}/preview */
export interface ReportPreview {
  subject: string
  body: string
  window_from: string
  window_to: string
  recipients: string[]
}

/** POST /reports/schedules/{id}/send */
export interface ReportSendResult {
  sent_to: string[]
  subject: string
  window_from: string
  window_to: string
  delivery?: ReportDelivery
  error?: string
}
