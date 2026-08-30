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
  users?: CustomerUser[] | null
  last_collected_at?: string | null
  last_statement?: StatementSummary | string | null
}

export interface UsageRow {
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
