// Wire types for the chargeback API (spec §1 domain + §4 API, EPIC #6723).
// Field names mirror the Postgres columns lane A serialises; optional
// fields are the ones a list endpoint may summarise rather than embed.

/**
 * The legacy `role` key (DESIGN.md §10.5): the highest-power binding in the
 * old vocabulary — operator / customer-admin / customer-viewer — or one of the
 * new role names when no old name means the same thing. The console decides
 * by `permissions`, never by this.
 */
export type Role = 'operator' | 'customer-admin' | 'customer-viewer' | BindingRole | string

/** The eight roles of the access model (DESIGN.md §10.3, §13.5). */
export type BindingRole =
  | 'sovereign-admin'
  | 'billing-operator'
  | 'finance-viewer'
  | 'partner-owner'
  | 'partner-viewer'
  | 'customer-owner'
  | 'customer-billing'
  | 'customer-viewer'
/** A partner binding expands to its customers plus its own party (DESIGN.md §13.5). */
export type ScopeKind = 'sovereign' | 'partner' | 'customer'
/** The twelve permissions (DESIGN.md §10.2, §11 capacity, §13.5 partners). */
export type Permission =
  | 'metering.read'
  | 'rating.manage'
  | 'customers.manage'
  | 'billing.issue'
  | 'billing.collect'
  | 'account.topup'
  | 'settings.manage'
  | 'audit.read'
  | 'customer.self.manage'
  | 'capacity.manage'
  | 'partners.manage'
  | 'partner.self.manage'

/** One binding as /me reports it: where it came from is `source`. */
export interface SessionBinding {
  role: BindingRole | string
  scope_kind: ScopeKind | string
  customer_id?: string | null
  customer_name?: string | null
  /** The partner a partner-scoped binding is bound to (DESIGN.md §11.5). */
  partner_id?: string | null
  partner_name?: string | null
  /** What a partner binding expands to: its customers plus its own party. */
  customer_ids?: string[]
  /** 'config' (OPERATOR_EMAILS) · 'binding' (role_bindings) · 'group:<name>'. */
  source?: string
}

export interface Me {
  email: string
  role: Role
  customer_id?: string | null
  /** Every binding the principal holds (additive, DESIGN.md §10.5). */
  roles?: SessionBinding[]
  /** Effective permissions per scope key: 'sovereign' or 'customer:<id>'. */
  permissions?: Record<string, Array<Permission | string>>
  /** Scope keys, 'sovereign' first. */
  scopes?: string[]
  /** The primary customer's card, when the principal has one. */
  customer?: { id: string; slug: string; name: string; status: string; billing_mode?: string; payment_method?: string; gateway_name?: string } | null
  /** PROFILE env: 'sovereign' | 'operator-central' (spec §6). */
  profile?: string | null
}

/** A role binding row (GET /access/bindings). */
export interface RoleBinding {
  id?: string
  subject_email: string
  role: BindingRole | string
  scope_kind: ScopeKind | string
  customer_id?: string | null
  customer_name?: string
  partner_id?: string | null
  partner_name?: string
  granted_by?: string
  granted_at?: string | null
  source?: string
}

/** A directory group → role mapping (GET|PUT /access/group-mappings). */
export interface GroupRoleMapping {
  id?: string
  group_name: string
  role: BindingRole | string
  scope_kind?: ScopeKind | string
  customer_id?: string | null
  customer_name?: string
  partner_id?: string | null
  partner_name?: string
  created_at?: string
}

/** GET /access/roles — the policy as a document. */
export interface RoleDoc {
  role: BindingRole | string
  scope_kind: ScopeKind | string
  permissions: string[]
  description: string
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
  /** Legacy vocabulary: admin = customer-owner, viewer = every other customer role. */
  role: 'admin' | 'viewer' | string
  /** The role actually bound (DESIGN.md §10): customer-owner | customer-billing | customer-viewer. */
  binding_role?: BindingRole | string
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
  /**
   * DESIGN.md §17 — what a tax RULE needs on top of the §9.4 profile.
   * `tax_country` is ISO 3166-1 alpha-2 ("" = treated as domestic);
   * `tax_business` marks a REGISTERED BUSINESS buyer, which is what makes
   * reverse charge apply and is not derivable from a registration number.
   * The three certificate fields are what make `tax_exempt` auditable — an
   * expired certificate falls back to the standard rate.
   *
   * `tax_exemption_expires_on` arrives as a TIMESTAMP on the customer
   * document and travels back as YYYY-MM-DD; read it through `dayOf`.
   */
  tax_country?: string | null
  tax_region?: string | null
  tax_business?: boolean
  tax_exemption_number?: string | null
  tax_exemption_expires_on?: string | null
  tax_exemption_scan_ref?: string | null
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
  /**
   * DESIGN.md §11.3 — what this row IS: a customer, or the account (party) of
   * a partner. A party row is never in the customer directory.
   */
  party_kind?: 'customer' | 'partner' | string
  /** The partner this customer buys through (null = direct). */
  partner_id?: string | null
  partner_name?: string
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

/**
 * DESIGN.md §15.2 — one band of a volume-tiered item: everything up to
 * `up_to` rates at `price`. `up_to` null is the last, unbounded band.
 */
export interface PriceTier {
  up_to?: number | string | null
  price: number | string
}

/** The two industry tier modes; '' (or absent) is an item with no bands. */
export type TierMode = '' | 'graduated' | 'all_units'

export interface PriceItem {
  sku: string
  unit: string
  unit_price: number | string
  /** List (annual) price when the item came from an annual list; unit_price = annual_price ÷ annual_divisor. */
  annual_price?: number | string | null
  description?: string
  /**
   * The RATING SHAPES (DESIGN.md §15.1-15.2). All additive: an item with
   * none of them rates at unit_price, which is every item of every book
   * written before §15.
   *
   * `graduated` rates each band at its own price; `all_units` rates the
   * whole billable quantity at the band the total reaches.
   */
  tier_mode?: TierMode | string
  tiers?: PriceTier[] | null
  /** Units of this SKU the plan includes per billing period. */
  allowance?: number | string | null
  /** Carry an unused allowance into the next period (one period, no compounding). */
  allowance_rollover?: boolean
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
  /** DESIGN.md §11 — the ONE cloud book the public calculator prices from. */
  public?: boolean
  /** Moves with the header AND the items: "prices as of" on the public catalog. */
  updated_at?: string
  items?: PriceItem[] | null
  /**
   * DESIGN.md §11.4 — a partner's DERIVED retail book: materialised from the
   * list book it names by the partner's retail rule, and read-only here.
   */
  partner_id?: string | null
  derived_from_rule?: boolean
  derived_from_book_id?: string | null
}

export interface RatedLine {
  sku: string
  unit?: string
  quantity: number | string
  unit_price: number | string
  amount: number | string
  resource_count?: number
  source_id?: string | null
  /**
   * The partner waterfall per line (DESIGN.md §11.1). On a customer's
   * statement: the Sovereign's list figures beside the amount the customer
   * is billed, what the partner pays for the line (buy) and what the
   * customer pays after its own discounts (net). On a wholesale or
   * commission statement, `end_customer_*` names the customer it belongs to.
   * All absent for a direct customer, and stripped for a principal that may
   * not see the buy price.
   */
  end_customer_id?: string | null
  end_customer_name?: string
  list_unit_price?: number | string | null
  list_amount?: number | string | null
  buy_amount?: number | string | null
  net_amount?: number | string | null
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
  /**
   * DESIGN.md §15.5 — an SLA credit is a credit note that RECORDS what it
   * answers: the contract, the percentage owed and the availability actually
   * measured. Absent on every other credit note.
   */
  contract_id?: string | null
  contract_name?: string
  sla_pct?: number | string | null
  measured_availability?: number | string | null
  /** What reduced the invoice, and what became credit on the account. */
  applied: number | string
  unapplied: number | string
  issued_at: string
  issued_by?: string
}

/**
 * DESIGN.md §17 — what a tax rule DOES. The rate alone cannot say it:
 * zero-rated and exempt are both 0 % and are different lines on a tax
 * return, and reverse charge is 0 % to the issuer and taxable to the buyer.
 */
export type TaxKind = 'standard' | 'zero_rated' | 'exempt' | 'reverse_charge' | 'out_of_state'

/**
 * DESIGN.md §17 — one rate, for one country (optionally one region), for one
 * category of supply (empty = every category), valid over a date range.
 * `rate` is a FRACTION: "0.0500" is 5 %. `effective_to` is EXCLUSIVE and
 * empty is open-ended.
 */
export interface TaxRule {
  id: string
  name: string
  country: string
  region?: string
  category?: string
  rate: number | string
  kind: TaxKind | string
  /** The sentence the invoice must carry when this rule applies. */
  note?: string
  effective_from: string
  effective_to?: string
  created_at?: string
  updated_at?: string
}

/** GET /tax/rules. `kinds` is the server's own list, in display order. */
export interface TaxRulesDoc {
  rules?: TaxRule[]
  kinds?: string[]
}

/** DESIGN.md §17 — a SKU, or a family of SKUs, placed in a tax category. */
export interface TaxCategoryRule {
  /** An exact SKU (k8s.vcpu) or a prefix ending in '*' (evs.*). */
  sku: string
  category: string
  note?: string
  updated_at?: string
}

/**
 * DESIGN.md §17 — ONE RULE's contribution to a statement: the taxable base
 * after discounts and the tax on it. A statement carries one per rule that
 * applied, which is the tax summary block an invoice with several rates has
 * to show. Frozen with the statement and never recomputed.
 */
export interface TaxLine {
  rule_id?: string
  rule_name?: string
  kind: TaxKind | string
  category?: string
  rate: number | string
  base: number | string
  tax: number | string
  note?: string
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
  /**
   * DESIGN.md §17 — what the RULES decided, frozen with the rest. `audit`
   * records every determination that was NOT the plain reading of the rule
   * table: an expired exemption certificate, a reverse-charge finding, a
   * per-customer rate override.
   */
  lines?: TaxLine[]
  audit?: string[]
  customer_country?: string
  seller_country?: string
  customer_exemption_number?: string
  customer_exemption_expires_on?: string
}

/** DESIGN.md §17 — where a statement's e-invoice has got to. */
export type EInvoiceStateName = 'built' | 'signed' | 'archived' | 'submitted' | 'not_submitted'

/**
 * DESIGN.md §17 — the e-invoicing state of one statement. The signature and
 * the hash are what make the archived copy verifiable; the signing KEY is
 * never on the wire and is never asked for.
 */
export interface EInvoiceState {
  profile: string
  invoice_number?: string
  state: EInvoiceStateName | string
  hash?: string
  signature?: string
  signature_algorithm?: string
  key_id?: string
  qr_payload?: string
  /** Why it stopped at archived — shown in full, never truncated. */
  submit_reason?: string
  submit_reference?: string
  built_at: string
  submitted_at?: string
}

/** GET /statements/{id}/einvoice — the state plus the structured document. */
export interface EInvoiceDoc {
  statement_id: string
  state: EInvoiceState
  document?: unknown
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
  /**
   * DESIGN.md §17 — the per-rule tax summary, FROZEN at issue: one row per
   * rule that applied. Absent on a statement rated before the rules existed,
   * whose only reading is the single `tax_rate` above.
   */
  tax_lines?: TaxLine[] | null
  /** DESIGN.md §17 — where this statement's e-invoice has got to; absent when no profile is configured. */
  einvoice?: EInvoiceState | null
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
  /**
   * DESIGN.md §16 — the customer disputes this invoice, and why. The money
   * stays owed and on the balance; what stops is collections chasing, until
   * an operator resolves the dispute.
   */
  disputed_at?: string | null
  dispute_reason?: string | null
  /**
   * The partner keys (DESIGN.md §11): the partner of this statement's
   * customer, or the partner whose party this statement bills. `buy_total`
   * is what the partner pays us and `margin_total` the customer net less
   * that — shown to Sovereign roles and to that partner's roles only, and
   * absent from the document for anyone else.
   */
  partner_id?: string | null
  partner_name?: string
  party_kind?: 'customer' | 'partner' | string
  statement_kind?: StatementKind | string
  buy_total?: number | string | null
  margin_total?: number | string | null
}

/** What a statement IS (DESIGN.md §11.2). */
export type StatementKind = 'customer' | 'wholesale' | 'commission'

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
  /**
   * DESIGN.md §17 — the country the Sovereign is REGISTERED in (ISO 3166-1
   * alpha-2). "" = not configured, and no cross-border determination is
   * made: without it reverse charge cannot be decided at all.
   */
  tax_country?: string
  credit_note_prefix?: string
  /** DESIGN.md §9.6 — the collections schedule; negative days are before the due date. */
  reminder_days?: number[]
  escalation_days?: number
  escalation_action?: 'notify' | 'suspend' | string
  /** DESIGN.md §11 — the designated public price book (null = nothing published). */
  public_price_book_id?: string | null
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

// ---------------------------------------------------------------------------
// Capacity (DESIGN.md §11, founder requirement 2026-09-11). Static-first: a
// region holds zones, a zone one pool per family whose TOTAL the operator
// enters; CONSUMED is derived from the latest complete hour of metering
// through the SKU footprints; RESERVED is 0 until proposals fill it. Every
// quantity is an exact JSON number; utilisation, growth and exhaustion are
// floats (estimates).
// ---------------------------------------------------------------------------

/** The seven pooled resource families (`internal/capacity.Families`). */
export type CapacityFamily = 'vcpu' | 'memory_gib' | 'block_ssd_gib' | 'block_hdd_gib' | 'object_gib' | 'eip_addresses' | 'bandwidth_mbps'

export interface CapacityFamilyDef {
  family: CapacityFamily | string
  label: string
  /** What a total and a footprint amount count in (vCPU, GiB, addresses, Mbps). */
  unit: string
}

/** unset = no total entered yet · ok · warn (≥ 70 %) · critical (≥ 85 %). */
export type CapacityPoolStatus = 'unset' | 'ok' | 'warn' | 'critical'

export interface CapacityRegion {
  id: string
  code: string
  name: string
  /** Which cloud collector will fill this region's totals later. */
  cloud_source_kind: string
  created_at?: string
  zones: CapacityZone[]
}

export interface CapacityZone {
  id: string
  region_id: string
  region_code?: string
  code: string
  name: string
  /** The default zone receives usage whose zone the inventory does not carry. */
  is_default: boolean
  created_at?: string
  pools?: CapacityPool[]
}

export interface CapacityPool {
  id: string
  zone_id: string
  family: CapacityFamily | string
  total: number | string
  reserved: number | string
  /** manual (the console) or a collector's name. */
  source: string
  note: string
  updated_by: string
  updated_at: string
}

/** One entry of a pool's total history (GET /capacity/zones/{id}/pools). */
export interface CapacityPoolChange {
  id: number
  pool_id: string
  total: number | string
  source: string
  note: string
  changed_by: string
  changed_at: string
}

export interface CapacityDayPoint {
  day: string
  consumed: number | string
}

/** A pool with the derived figures (GET /capacity/overview). */
export interface CapacityPoolView extends CapacityPool {
  label: string
  unit: string
  consumed: number | string
  /** total − reserved − consumed, never below 0. */
  available: number | string
  utilisation_pct: number | null
  status: CapacityPoolStatus | string
  /** True when the arithmetic went negative; overcommit is the shortfall. */
  clamped: boolean
  overcommit: number | string
  /** The part of consumed attributed here because the resource's zone is unknown. */
  zone_unknown: number | string
  /** Units per day, the 7-day run-rate trend; null with too little history. */
  growth_per_day: number | null
  /** available ÷ growth; null when not growing or no total. */
  exhaustion_days: number | null
  history_days: number
  series: CapacityDayPoint[]
}

/** One SKU's headroom in one zone. */
export interface CapacitySKUView {
  sku: string
  footprint: Record<string, number | string>
  /** seed · manual · derived (from the SKU name, no stored row). */
  footprint_source: string
  consumed_units: number | string
  resources: number
  /** null when no family in the footprint has a total yet. */
  headroom_units: number | string | null
  /** The family that limits headroom, or "cap". */
  binding_family: string
  cap: number | string | null
}

export interface CapacityZoneView {
  id: string
  code: string
  name: string
  is_default: boolean
  pools: CapacityPoolView[]
  skus: CapacitySKUView[]
}

export interface CapacityRegionView {
  id: string
  code: string
  name: string
  cloud_source_kind: string
  zones: CapacityZoneView[]
}

/** A metered SKU with no footprint — counts against no pool. */
export interface CapacityUnmappedSKU {
  sku: string
  unit: string
  quantity: number | string
  resources: number
  regions: string[]
}

/** Metered usage in a region not configured here (or configured without zones). */
export interface CapacityUnmappedRegion {
  region: string
  reason: 'no-region' | 'no-zones' | string
  skus: number
  quantity: number | string
  resources: number
}

export interface CapacityThresholds {
  warn_pct: number
  critical_pct: number
}

export interface CapacitySummary {
  regions: number
  zones: number
  pools: number
  pools_with_total: number
  pools_warn: number
  pools_critical: number
  /** warn + critical: pools past the 70 % line. */
  pools_below_threshold: number
  skus: number
  unmapped_skus: number
}

/** GET /capacity/overview */
export interface CapacityOverview {
  /** The latest complete hour consumption was measured in; null with no cloud usage. */
  as_of: string | null
  sources: number
  lagging_sources: number
  thresholds: CapacityThresholds
  families: CapacityFamilyDef[]
  regions: CapacityRegionView[]
  unmapped_skus: CapacityUnmappedSKU[]
  unmapped_regions: CapacityUnmappedRegion[]
  summary: CapacitySummary
}

export interface SKUFootprint {
  sku: string
  families: Record<string, number | string>
  source: string
  updated_at?: string
}

/** GET /capacity/footprints */
export interface SKUFootprints {
  footprints: SKUFootprint[]
  families: CapacityFamilyDef[]
  /** List-price SKUs with no per-unit footprint in any family (elb, nat.<spec>, vpc). */
  unseeded_skus: string[]
}

export interface SKUCap {
  zone_id: string
  zone_code?: string
  region_code?: string
  sku: string
  total: number | string
  updated_by?: string
  updated_at?: string
}

// ── Public cost calculator (DESIGN.md §12) ──────────────────────────────
// The unauthenticated surface: list prices only. A negotiated book, a
// discount and a partner rate never appear in any document below.

/** One priced SKU of the public list book. `monthly` is one unit for 730 h. */
export interface PublicCatalogSKU {
  sku: string
  service: string
  unit: string
  unit_price: number | string
  monthly: number | string
  description?: string
}

/** One sized catalog plan (S / M / L / XL) as the plans book prices it. */
export interface PublicCatalogPlan {
  slug: string
  name: string
  sku: string
  unit: string
  unit_price: number | string
  monthly: number | string
  vcpu: number
  memory_gib: number
}

/** One pay-per-use platform meter. */
export interface PublicCatalogRate {
  sku: string
  unit: string
  unit_price: number | string
  monthly: number | string
  description?: string
}

/** The book an estimate was priced from, and its date. */
export interface EstimateBook {
  id: string
  name: string
  updated_at: string
}

/** GET /public/catalog */
export interface PublicCatalog {
  price_book: EstimateBook
  currency: string
  tax_rate: number | string
  regions: string[]
  skus: PublicCatalogSKU[]
  plans: PublicCatalogPlan[]
  payg: PublicCatalogRate[]
  hours_per_month: number
  list_prices: boolean
  notice: string
  generated_at: string
}

/** One priced line of an estimate. `plan` is set on a plan line. */
export interface EstimateLine {
  sku: string
  plan?: string
  description?: string
  unit: string
  quantity: number | string
  hours: number | string
  months: number
  rated_quantity: number | string
  unit_price: number | string
  amount: number | string
}

/**
 * POST /public/estimates · GET /public/estimates/{id} · a row of GET /leads.
 * `contact_email` is present only on the operator's Leads list.
 */
export interface Estimate {
  id: string
  lines: EstimateLine[]
  currency: string
  region?: string
  subtotal: number | string
  tax_rate: number | string
  tax: number | string
  total: number | string
  monthly: number | string
  yearly: number | string
  price_book: EstimateBook
  list_prices: boolean
  lead: boolean
  contact_email?: string
  created_at: string
  valid_until: string
  share_url?: string
}

// ---------------------------------------------------------------------------
// Partners — resellers and agents (DESIGN.md §13)
// ---------------------------------------------------------------------------

/** How a partner is billed: resell (the partner) or agent (its customer). */
export type BillTo = 'partner' | 'customer'

/** GET /partners · GET /partners/{id} */
export interface Partner {
  id: string
  slug: string
  name: string
  /** The tier whose discounts set the buy price; null = none. */
  tier_id?: string | null
  tier_name?: string
  bill_to: BillTo | string
  /** The agent commission as a percent of the customer net (used with no tier). */
  commission_pct?: number | string | null
  status: string
  contact_email?: string
  /** The partner's own account row — its balance, invoices and collections. */
  party_customer_id?: string
  customer_count?: number
  /** The party's ledger figures: positive is owed by the partner. */
  balance?: number | string
  available_credit?: number | string
  has_retail_rule?: boolean
  created_at?: string
  updated_at?: string
}

/** GET /partners/tiers — a tier and the discounts that make it. */
export interface PartnerTier {
  id: string
  name: string
  description?: string
  partners?: number
  discounts?: Discount[]
  created_at?: string
}

/** A markup narrowed to one service (a SKU's first segment) or one SKU. */
export interface RetailOverride {
  scope: 'service' | 'sku' | string
  key: string
  markup_pct: number | string
}

/** The rule a resell partner's retail book is derived from. */
export interface RetailRule {
  partner_id?: string
  base: 'list' | 'buy' | string
  markup_pct: number | string
  overrides?: RetailOverride[]
  updated_at?: string
}

/** A derived retail price below the partner's own buy price — warned, never refused. */
export interface BelowBuyLine {
  sku: string
  unit?: string
  list_unit_price: number | string
  buy_unit_price: number | string
  retail_unit_price: number | string
}

/** One materialised retail book with what it derives from. */
export interface DerivedBook {
  book: PriceBook
  list_book_id: string
  list_book_name: string
  below_buy?: BelowBuyLine[]
}

/** PUT /partners/{id}/retail-rule · GET /partners/{id}/retail-book */
export interface RetailDocument {
  partner_id: string
  bill_to: BillTo | string
  retail_rule?: RetailRule | null
  books: DerivedBook[]
  below_buy: BelowBuyLine[]
  /** Why there is no retail book, when there is none. */
  note?: string
}

/** One (end customer, service) line of the margin report. */
export interface MarginRow {
  customer_id: string
  customer_name: string
  service: string
  customer_net: number | string
  partner_buy: number | string
  margin: number | string
  margin_pct?: number | null
}

/** GET /partners/{id}/margin?period= */
export interface MarginReport {
  partner_id: string
  period: string
  currency: string
  rows: MarginRow[]
  totals: MarginRow
}

// ---------------------------------------------------------------------------
// Customer self-service (DESIGN.md §16)
// ---------------------------------------------------------------------------

/**
 * A payment method the GATEWAY holds. This is the display record and only
 * the display record: there is no card number here and no gateway token —
 * the server's own type has no JSON field for one, so the console could not
 * render a token even by mistake.
 */
export interface PaymentMethod {
  id: string
  customer_id: string
  gateway?: string
  status: 'pending' | 'active' | 'removed' | string
  /** While pending: the gateway's own page the card is entered on. */
  setup_url?: string
  brand?: string
  last4?: string
  exp_month?: number
  exp_year?: number
  label?: string
  /** Whether the gateway holds an instrument for it — never the token itself. */
  saved: boolean
  created_at?: string
  confirmed_at?: string | null
  removed_at?: string | null
}

/** One customer's objection to one invoice (DESIGN.md §16). */
export interface Dispute {
  id: string
  statement_id: string
  customer_id: string
  invoice_number?: string
  reason: string
  /** The rated-line ids the dispute names; absent when it is the whole invoice. */
  lines?: string[] | null
  amount: number | string
  currency?: string
  status: 'open' | 'upheld' | 'rejected' | string
  opened_by?: string
  opened_at: string
  resolved_by?: string
  resolved_at?: string | null
  note?: string
  credit_note_id?: string
}

/**
 * DESIGN.md §15.3 — one line of a contract: a committed-use line, or an
 * allowance that belongs to the contract rather than to the plan.
 */
export interface ContractItem {
  id?: string
  contract_id?: string
  kind: 'commitment' | 'allowance' | string
  sku: string
  unit?: string
  /** Committed quantity per billing period, or allowance units per period. */
  quantity: number | string
  /** A commitment's negotiated unit price… */
  committed_price?: number | string | null
  /** …or the percentage off list it stands for. One of the two is required. */
  discount_pct?: number | string | null
  /** Allowance only: carry the unused part into the next period. */
  rollover?: boolean
  notes?: string
  created_at?: string
}

/** DESIGN.md §15.4 — the agreement a customer's commercial terms hang on. */
export interface Contract {
  id: string
  customer_id: string
  customer_name?: string
  customer_slug?: string
  name: string
  starts_on: string
  ends_on: string
  term_months: number
  auto_renew: boolean
  /** Days before the end date the contract joins the renewals-due list. */
  renewal_notice_days: number
  /** The MONTHLY floor; a period below it carries a true-up line. */
  minimum_commitment?: number | string | null
  currency: string
  status: 'draft' | 'active' | 'expired' | 'cancelled' | string
  signed_at?: string | null
  po_reference?: string
  notes?: string
  renewed_at?: string | null
  renewal_count?: number
  created_at?: string
  updated_at?: string
  items?: ContractItem[] | null
  /** Derived, never stored: ends_on + 1 day, and ends_on − notice days. */
  renewal_date?: string
  notice_from?: string
}
