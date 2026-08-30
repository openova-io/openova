// CSV parsing for the two import surfaces (spec §2 price-book import, §4
// customers import). Pure functions — unit-tested in csv.test.ts.

export interface CsvTable {
  header: string[]
  rows: string[][]
}

/** RFC-4180-ish: quoted fields, doubled quotes, CRLF/LF, trailing newline. */
export function parseCsv(text: string): CsvTable {
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let quoted = false
  const src = text.replace(/^﻿/, '')
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (quoted) {
      if (c === '"') {
        if (src[i + 1] === '"') {
          field += '"'
          i++
        } else {
          quoted = false
        }
      } else {
        field += c
      }
      continue
    }
    if (c === '"') {
      quoted = true
    } else if (c === ',') {
      row.push(field)
      field = ''
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && src[i + 1] === '\n') i++
      row.push(field)
      field = ''
      rows.push(row)
      row = []
    } else {
      field += c
    }
  }
  if (field !== '' || row.length > 0) {
    row.push(field)
    rows.push(row)
  }
  const nonEmpty = rows.filter((r) => r.some((f) => f.trim() !== ''))
  if (nonEmpty.length === 0) return { header: [], rows: [] }
  const header = nonEmpty[0].map((h) => h.trim().toLowerCase())
  return { header, rows: nonEmpty.slice(1).map((r) => r.map((f) => f.trim())) }
}

export interface RowError {
  line: number
  message: string
}

// ── customers import (spec §4: slug,name,admin_email,region,project_ids,
//    price_book,billing_mode,start_date) ─────────────────────────────────

export const CUSTOMER_CSV_COLUMNS = [
  'slug',
  'name',
  'admin_email',
  'region',
  'project_ids',
  'price_book',
  'billing_mode',
  'start_date',
] as const

export interface CustomerImportRow {
  line: number
  slug: string
  name: string
  admin_email: string
  region: string
  project_ids: string[]
  price_book: string
  billing_mode: string
  start_date: string
}

export interface CustomerImportPreview {
  rows: CustomerImportRow[]
  errors: RowError[]
}

const SLUG_RE = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const DATE_RE = /^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$/
const BILLING_MODES = new Set(['real', 'chargeback', 'showback'])

export function parseCustomersCsv(text: string): CustomerImportPreview {
  const table = parseCsv(text)
  const errors: RowError[] = []
  const rows: CustomerImportRow[] = []
  if (table.header.length === 0) {
    return { rows, errors: [{ line: 1, message: 'empty file' }] }
  }
  const idx = (name: string) => table.header.indexOf(name)
  for (const required of ['slug', 'name', 'admin_email']) {
    if (idx(required) < 0) errors.push({ line: 1, message: `missing column: ${required}` })
  }
  if (errors.length) return { rows, errors }

  const col = (r: string[], name: string) => {
    const i = idx(name)
    return i < 0 ? '' : (r[i] ?? '')
  }
  const seen = new Set<string>()
  table.rows.forEach((r, i) => {
    const line = i + 2
    const row: CustomerImportRow = {
      line,
      slug: col(r, 'slug').toLowerCase(),
      name: col(r, 'name'),
      admin_email: col(r, 'admin_email').toLowerCase(),
      region: col(r, 'region'),
      project_ids: col(r, 'project_ids')
        .split(';')
        .map((p) => p.trim())
        .filter(Boolean),
      price_book: col(r, 'price_book'),
      billing_mode: col(r, 'billing_mode').toLowerCase() || 'showback',
      start_date: col(r, 'start_date'),
    }
    const rowErrors: string[] = []
    if (!SLUG_RE.test(row.slug)) rowErrors.push('slug must be lowercase letters, digits and dashes')
    else if (seen.has(row.slug)) rowErrors.push(`duplicate slug ${row.slug}`)
    if (!row.name) rowErrors.push('name is required')
    if (!EMAIL_RE.test(row.admin_email)) rowErrors.push('admin_email is not a valid address')
    if (!BILLING_MODES.has(row.billing_mode)) rowErrors.push(`billing_mode must be one of real, chargeback, showback`)
    if (row.start_date && !DATE_RE.test(row.start_date)) rowErrors.push('start_date must be YYYY-MM-DD')
    if (row.project_ids.length > 0 && !row.region) rowErrors.push('region is required when project_ids is set')
    if (rowErrors.length) {
      errors.push({ line, message: rowErrors.join('; ') })
    } else {
      seen.add(row.slug)
      rows.push(row)
    }
  })
  return { rows, errors }
}

export const CUSTOMER_CSV_SAMPLE =
  CUSTOMER_CSV_COLUMNS.join(',') +
  '\nacme,ACME LLC,billing@acme.example,om-east-1,3b1d0d0e5f2c4a7d8e9f0a1b2c3d4e5f;7c2e1f0a9b8d4c6e5f4a3b2c1d0e9f8a,standard,showback,2026-09-01\n'

// ── price-book import (spec §2: sku,unit,annual_price,description) ───────

export const PRICE_CSV_COLUMNS = ['sku', 'unit', 'annual_price', 'description'] as const

export interface PriceImportRow {
  line: number
  sku: string
  unit: string
  annual_price: number
  description: string
}

export interface PriceImportPreview {
  rows: PriceImportRow[]
  errors: RowError[]
}

export function parsePriceBookCsv(text: string): PriceImportPreview {
  const table = parseCsv(text)
  const errors: RowError[] = []
  const rows: PriceImportRow[] = []
  if (table.header.length === 0) return { rows, errors: [{ line: 1, message: 'empty file' }] }
  for (const required of ['sku', 'unit', 'annual_price']) {
    if (!table.header.includes(required)) errors.push({ line: 1, message: `missing column: ${required}` })
  }
  if (errors.length) return { rows, errors }
  const idx = (n: string) => table.header.indexOf(n)
  const seen = new Set<string>()
  table.rows.forEach((r, i) => {
    const line = i + 2
    const sku = (r[idx('sku')] ?? '').trim()
    const unit = (r[idx('unit')] ?? '').trim()
    const priceRaw = (r[idx('annual_price')] ?? '').trim()
    const description = idx('description') < 0 ? '' : (r[idx('description')] ?? '')
    const price = Number(priceRaw)
    const rowErrors: string[] = []
    if (!sku) rowErrors.push('sku is required')
    else if (seen.has(sku)) rowErrors.push(`duplicate sku ${sku}`)
    if (!unit) rowErrors.push('unit is required')
    if (priceRaw === '' || Number.isNaN(price) || price < 0) rowErrors.push('annual_price must be a non-negative number')
    if (rowErrors.length) {
      errors.push({ line, message: rowErrors.join('; ') })
    } else {
      seen.add(sku)
      rows.push({ line, sku, unit, annual_price: price, description })
    }
  })
  return { rows, errors }
}

export const PRICE_CSV_SAMPLE =
  PRICE_CSV_COLUMNS.join(',') +
  '\necs.s6.large.2,instance-hour,876.000,ECS s6.large.2 (2 vCPU / 4 GiB)\nevs.ssd.gb,gb-hour,1.200,EVS SSD per GiB\neip,hour,43.800,Elastic IP\n'

/** unit_price = annual_price / annual_divisor (spec §2), 8 decimals like price_items.unit_price. */
export function unitPrice(annual: number, divisor: number): number {
  if (!divisor || divisor <= 0) return 0
  return Math.round((annual / divisor) * 1e8) / 1e8
}

export function dataUrl(csv: string): string {
  return 'data:text/csv;charset=utf-8,' + encodeURIComponent(csv)
}
