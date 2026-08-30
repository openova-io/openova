import { describe, expect, it } from 'vitest'
import {
  CUSTOMER_CSV_SAMPLE,
  PRICE_CSV_SAMPLE,
  parseCsv,
  parseCustomersCsv,
  parsePriceBookCsv,
  unitPrice,
} from './csv'

describe('parseCsv', () => {
  it('handles quoted fields, doubled quotes, CRLF and a BOM', () => {
    const t = parseCsv('﻿a,b\r\n"x, y","say ""hi"""\r\n')
    expect(t.header).toEqual(['a', 'b'])
    expect(t.rows).toEqual([['x, y', 'say "hi"']])
  })

  it('returns an empty table for blank input', () => {
    expect(parseCsv('\n\n')).toEqual({ header: [], rows: [] })
  })
})

describe('parseCustomersCsv', () => {
  it('previews the shipped sample as one valid row', () => {
    const p = parseCustomersCsv(CUSTOMER_CSV_SAMPLE)
    expect(p.errors).toEqual([])
    expect(p.rows).toHaveLength(1)
    expect(p.rows[0].slug).toBe('acme')
    expect(p.rows[0].project_ids).toEqual([
      '3b1d0d0e5f2c4a7d8e9f0a1b2c3d4e5f',
      '7c2e1f0a9b8d4c6e5f4a3b2c1d0e9f8a',
    ])
    expect(p.rows[0].billing_mode).toBe('showback')
    expect(p.rows[0].line).toBe(2)
  })

  it('reports per-line errors and keeps the valid rows', () => {
    const csv = [
      'slug,name,admin_email,region,project_ids,price_book,billing_mode,start_date',
      'Good Co,Good,ops@good.example,,,,showback,',
      'okay,Okay LLC,nope,om-east-1,abc,,prepaid,2026-13-01',
      'fine,Fine,fin@fine.example,,,,,',
      'fine,Dup,dup@fine.example,,,,showback,',
    ].join('\n')
    const p = parseCustomersCsv(csv)
    expect(p.rows.map((r) => r.slug)).toEqual(['fine'])
    expect(p.rows[0].billing_mode).toBe('showback') // defaulted
    expect(p.errors.map((e) => e.line)).toEqual([2, 3, 5])
    expect(p.errors[0].message).toContain('slug')
    expect(p.errors[1].message).toContain('admin_email')
    expect(p.errors[1].message).toContain('billing_mode')
    expect(p.errors[1].message).toContain('start_date')
    expect(p.errors[2].message).toContain('duplicate slug')
  })

  it('requires the region when project ids are given', () => {
    const p = parseCustomersCsv('slug,name,admin_email,project_ids\nx,X,x@x.example,abc')
    expect(p.rows).toHaveLength(0)
    expect(p.errors[0].message).toContain('region')
  })

  it('fails the header when a required column is missing', () => {
    const p = parseCustomersCsv('slug,name\nx,X')
    expect(p.rows).toHaveLength(0)
    expect(p.errors).toEqual([{ line: 1, message: 'missing column: admin_email' }])
  })
})

describe('parsePriceBookCsv', () => {
  it('previews the shipped sample', () => {
    const p = parsePriceBookCsv(PRICE_CSV_SAMPLE)
    expect(p.errors).toEqual([])
    expect(p.rows.map((r) => r.sku)).toEqual(['ecs.s6.large.2', 'evs.ssd.gb', 'eip'])
    expect(p.rows[0].annual_price).toBe(876)
  })

  it('rejects bad prices and duplicate skus', () => {
    const p = parsePriceBookCsv('sku,unit,annual_price\na,hour,-1\nb,hour,x\nc,hour,10\nc,hour,11')
    expect(p.rows.map((r) => r.sku)).toEqual(['c'])
    expect(p.errors.map((e) => e.line)).toEqual([2, 3, 5])
  })

  it('derives the hourly unit price from the annual list and divisor', () => {
    expect(unitPrice(876, 8760)).toBe(0.1)
    expect(unitPrice(1, 3)).toBe(0.33333333)
    expect(unitPrice(10, 0)).toBe(0)
  })
})
