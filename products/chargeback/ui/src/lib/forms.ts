/**
 * Form validation + wire bodies for the customer pages (#6867). Every
 * message here is what the Field shows inline; every body is exactly what
 * the API decodes, so a renamed key fails a test, not a walk.
 */

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const SLUG = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/
const DAY = /^\d{4}-\d{2}-\d{2}$/

export function isEmail(s: string): boolean {
  return EMAIL.test(s.trim())
}

export function isDay(s: string): boolean {
  if (!DAY.test(s)) return false
  const d = new Date(s + 'T00:00:00Z')
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s
}

/** "Acme Trading LLC" → "acme-trading-llc". */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[^\x20-\x7e]/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
}

/** Current calendar month as YYYY-MM (UTC) — the budget status period. */
export function currentPeriod(now = new Date()): string {
  return `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`
}

// ── New customer ──────────────────────────────────────────────────────────

export interface CustomerForm {
  slug: string
  name: string
  admin_email: string
  billing_mode: string
  price_book_id: string
  start_date: string
  kind: string
  org_slug: string
}

export type Errors<T> = Partial<Record<keyof T, string>>

export function emptyCustomerForm(): CustomerForm {
  return { slug: '', name: '', admin_email: '', billing_mode: 'showback', price_book_id: '', start_date: '', kind: 'external', org_slug: '' }
}

export function validateCustomer(f: CustomerForm): Errors<CustomerForm> {
  const e: Errors<CustomerForm> = {}
  if (!f.name.trim()) e.name = 'Name is required.'
  const slug = f.slug.trim()
  if (!slug) e.slug = 'Slug is required.'
  else if (!SLUG.test(slug)) e.slug = 'Lowercase letters, digits and hyphens only; must start and end with a letter or digit.'
  if (!f.admin_email.trim()) e.admin_email = 'Admin email is required.'
  else if (!isEmail(f.admin_email)) e.admin_email = 'Not a valid email address.'
  if (!['showback', 'chargeback', 'real'].includes(f.billing_mode)) e.billing_mode = 'Choose showback, chargeback or real.'
  if (f.start_date && !isDay(f.start_date)) e.start_date = 'Use YYYY-MM-DD.'
  if (!['external', 'organization'].includes(f.kind)) e.kind = 'Choose external or organization.'
  if (f.kind === 'organization' && f.org_slug.trim() && !SLUG.test(f.org_slug.trim())) e.org_slug = 'Lowercase letters, digits and hyphens only.'
  return e
}

export function customerBody(f: CustomerForm): Record<string, string | null> {
  return {
    slug: f.slug.trim().toLowerCase(),
    name: f.name.trim(),
    admin_email: f.admin_email.trim().toLowerCase(),
    billing_mode: f.billing_mode,
    price_book_id: f.price_book_id || null,
    start_date: f.start_date || null,
    kind: f.kind,
    org_slug: f.kind === 'organization' && f.org_slug.trim() ? f.org_slug.trim().toLowerCase() : null,
  }
}

// ── Customer settings (PATCH) ─────────────────────────────────────────────

export interface SettingsShape {
  name: string
  admin_email: string
  billing_mode: string
  start_date: string
  status: string
  org_slug: string
}

export function validateSettings(f: SettingsShape): Errors<SettingsShape> {
  const e: Errors<SettingsShape> = {}
  if (!f.name.trim()) e.name = 'Name is required.'
  if (!f.admin_email.trim()) e.admin_email = 'Admin email is required.'
  else if (!isEmail(f.admin_email)) e.admin_email = 'Not a valid email address.'
  if (!['showback', 'chargeback', 'real'].includes(f.billing_mode)) e.billing_mode = 'Choose showback, chargeback or real.'
  if (!['pending', 'active', 'suspended'].includes(f.status)) e.status = 'Choose pending, active or suspended.'
  if (f.start_date && !isDay(f.start_date)) e.start_date = 'Use YYYY-MM-DD.'
  if (f.org_slug.trim() && !SLUG.test(f.org_slug.trim())) e.org_slug = 'Lowercase letters, digits and hyphens only.'
  return e
}

// ── Discounts ─────────────────────────────────────────────────────────────

export interface DiscountForm {
  name: string
  kind: 'percent' | 'fixed'
  value: string
  sku: string
  starts_at: string
  ends_at: string
  active: boolean
}

export function emptyDiscountForm(): DiscountForm {
  return { name: '', kind: 'percent', value: '', sku: '', starts_at: '', ends_at: '', active: true }
}

const DECIMAL = /^-?\d+(\.\d+)?$/

export function validateDiscount(f: DiscountForm): Errors<DiscountForm> {
  const e: Errors<DiscountForm> = {}
  if (!f.name.trim()) e.name = 'Name is required.'
  if (f.kind !== 'percent' && f.kind !== 'fixed') e.kind = 'Choose percent or fixed.'
  const v = f.value.trim()
  if (!v) e.value = 'Value is required.'
  else if (!DECIMAL.test(v)) e.value = 'Enter a plain number, e.g. 10 or 12.5.'
  else if (Number(v) < 0) e.value = 'A discount cannot be negative.'
  else if (f.kind === 'percent' && Number(v) > 100) e.value = 'A percentage cannot exceed 100 — use a fixed amount instead.'
  if (f.starts_at && !isDay(f.starts_at)) e.starts_at = 'Use YYYY-MM-DD.'
  if (f.ends_at && !isDay(f.ends_at)) e.ends_at = 'Use YYYY-MM-DD.'
  if (!e.starts_at && !e.ends_at && f.starts_at && f.ends_at && f.starts_at >= f.ends_at) e.ends_at = 'End must be after start.'
  return e
}

/** The body POST /customers/{id}/discounts and PUT /discounts/{id} decode. */
export function discountBody(f: DiscountForm, customerId: string | null): Record<string, string | boolean | null> {
  return {
    customer_id: customerId,
    name: f.name.trim(),
    kind: f.kind,
    value: f.value.trim(),
    sku: f.sku.trim(),
    starts_at: f.starts_at || '',
    ends_at: f.ends_at || '',
    active: f.active,
  }
}

// ── Budgets ───────────────────────────────────────────────────────────────

export interface BudgetForm {
  name: string
  amount: string
  currency: string
  thresholds: string
  notify_emails: string
  active: boolean
}

export const DEFAULT_THRESHOLDS = [50, 80, 100]

export function emptyBudgetForm(currency: string): BudgetForm {
  return { name: '', amount: '', currency, thresholds: DEFAULT_THRESHOLDS.join(', '), notify_emails: '', active: true }
}

/** "50, 80,100" → [50, 80, 100]; sorted, de-duplicated whole percentages 1..1000. */
export function parseThresholds(s: string): { values: number[]; error?: string } {
  const parts = s
    .split(/[\s,;]+/)
    .map((p) => p.trim())
    .filter(Boolean)
  if (parts.length === 0) return { values: [], error: 'Give at least one threshold, e.g. 50, 80, 100.' }
  const values: number[] = []
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return { values: [], error: `"${p}" is not a whole percentage.` }
    const v = Number(p)
    if (v < 1 || v > 1000) return { values: [], error: `${v} is out of range (1–1000 %).` }
    if (!values.includes(v)) values.push(v)
  }
  values.sort((a, b) => a - b)
  return { values }
}

/** "a@x.com, B@y.com" → ["a@x.com", "b@y.com"]; empty is allowed (no mail). */
export function parseEmails(s: string): { values: string[]; error?: string } {
  const parts = s
    .split(/[\s,;]+/)
    .map((p) => p.trim().toLowerCase())
    .filter(Boolean)
  const values: string[] = []
  for (const p of parts) {
    if (!isEmail(p)) return { values: [], error: `"${p}" is not a valid email address.` }
    if (!values.includes(p)) values.push(p)
  }
  return { values }
}

export function validateBudget(f: BudgetForm): Errors<BudgetForm> {
  const e: Errors<BudgetForm> = {}
  if (!f.name.trim()) e.name = 'Name is required.'
  const a = f.amount.trim()
  if (!a) e.amount = 'Amount is required.'
  else if (!DECIMAL.test(a)) e.amount = 'Enter a plain number, e.g. 3000 or 250.500.'
  else if (Number(a) <= 0) e.amount = 'Amount must be above zero.'
  if (!/^[A-Z]{3}$/.test(f.currency.trim().toUpperCase())) e.currency = 'Three-letter code, e.g. OMR.'
  const t = parseThresholds(f.thresholds)
  if (t.error) e.thresholds = t.error
  const m = parseEmails(f.notify_emails)
  if (m.error) e.notify_emails = m.error
  return e
}

/** The body POST /budgets and PUT /budgets/{id} decode. */
export function budgetBody(f: BudgetForm, customerId: string | null): Record<string, unknown> {
  return {
    name: f.name.trim(),
    customer_id: customerId,
    amount: f.amount.trim(),
    currency: f.currency.trim().toUpperCase(),
    period: 'monthly',
    thresholds: parseThresholds(f.thresholds).values,
    notify_emails: parseEmails(f.notify_emails).values,
    active: f.active,
  }
}

// ── Sources ───────────────────────────────────────────────────────────────

export interface SourceForm {
  kind: string
  region: string
  project_id: string
  domain_id: string
  scope_token: string
}

export function validateSource(f: SourceForm, editable: Array<keyof SourceForm>): Errors<SourceForm> {
  const e: Errors<SourceForm> = {}
  const projectScoped = f.kind === 'huawei-project'
  if (editable.includes('region') && projectScoped && !f.region.trim()) e.region = 'Region is required for a Huawei project.'
  if (editable.includes('project_id') && projectScoped && !f.project_id.trim()) e.project_id = 'Project id is required for a Huawei project.'
  if (editable.includes('scope_token') && /\s/.test(f.scope_token.trim())) e.scope_token = 'A scope token has no spaces — it is matched inside resource names.'
  return e
}

export function hasErrors<T>(e: Errors<T>): boolean {
  return Object.keys(e).length > 0
}
