import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { EmptyState } from './ui'

/**
 * DataTable — sortable, optionally paged table with a totals row and a
 * client-side CSV download (#6867). Rows are plain objects; columns declare
 * how to read, render and sort them. Sorting is stable; numeric columns
 * sort numerically and render right-aligned.
 */
export interface Column<T> {
  key: string
  header: ReactNode
  /** Value used for sorting and CSV. */
  value: (row: T) => string | number | null | undefined
  /** Rendered cell; defaults to the value. */
  render?: (row: T) => ReactNode
  numeric?: boolean
  width?: number | string
  /** Totals-row cell; omit for none. */
  total?: (rows: T[]) => ReactNode
  sortable?: boolean
  className?: string
}

export interface DataTableProps<T> {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T, i: number) => string
  defaultSort?: { key: string; dir: 'asc' | 'desc' }
  pageSize?: number
  onRowClick?: (row: T) => void
  selectedKey?: string | null
  emptyTitle?: string
  emptyBody?: ReactNode
  /** Adds a "Download CSV" control; the file name without extension. */
  csvName?: string
  footNote?: ReactNode
  dense?: boolean
  /**
   * Controlled sort for server-sorted lists: the header shows `sort`, a
   * click reports the next sort here, and rows are rendered in the order
   * they arrived (the caller owns the ordering).
   */
  sort?: { key: string; dir: 'asc' | 'desc' } | null
  onSortChange?: (sort: { key: string; dir: 'asc' | 'desc' }) => void
  /** Optional detail row rendered under a row (null/undefined = collapsed). */
  expanded?: (row: T) => ReactNode | null | undefined
}

export function toCSV<T>(columns: Column<T>[], rows: T[]): string {
  const esc = (v: unknown) => {
    const s = v === null || v === undefined ? '' : String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const head = columns.map((c) => esc(typeof c.header === 'string' ? c.header : c.key)).join(',')
  const body = rows.map((r) => columns.map((c) => esc(c.value(r))).join(','))
  return [head, ...body].join('\n') + '\n'
}

export function sortRows<T>(rows: T[], col: Column<T> | undefined, dir: 'asc' | 'desc'): T[] {
  if (!col) return rows
  const sign = dir === 'asc' ? 1 : -1
  return rows
    .map((r, i) => ({ r, i }))
    .sort((a, b) => {
      const va = col.value(a.r)
      const vb = col.value(b.r)
      let c = 0
      if (va === vb) c = 0
      else if (va === null || va === undefined) c = 1
      else if (vb === null || vb === undefined) c = -1
      else if (col.numeric || (typeof va === 'number' && typeof vb === 'number')) c = Number(va) - Number(vb)
      else c = String(va).localeCompare(String(vb))
      return c !== 0 ? c * sign : a.i - b.i
    })
    .map((x) => x.r)
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  defaultSort,
  pageSize,
  onRowClick,
  selectedKey,
  emptyTitle,
  emptyBody,
  csvName,
  footNote,
  sort: controlledSort,
  onSortChange,
  expanded,
}: DataTableProps<T>) {
  const [localSort, setSort] = useState(defaultSort ?? null)
  const controlled = Boolean(onSortChange)
  const sort = controlled ? (controlledSort ?? null) : localSort
  const [page, setPage] = useState(0)
  const sorted = useMemo(() => (controlled ? rows : sortRows(rows, columns.find((c) => c.key === sort?.key), sort?.dir ?? 'desc')), [rows, columns, sort, controlled])
  const size = pageSize ?? sorted.length
  const pages = Math.max(1, Math.ceil(sorted.length / Math.max(size, 1)))
  const cur = Math.min(page, pages - 1)
  const visible = pageSize ? sorted.slice(cur * size, cur * size + size) : sorted
  const hasTotals = columns.some((c) => c.total)

  const toggleSort = (c: Column<T>) => {
    if (c.sortable === false) return
    setPage(0)
    const next = (s: { key: string; dir: 'asc' | 'desc' } | null): { key: string; dir: 'asc' | 'desc' } =>
      s?.key === c.key ? { key: c.key, dir: s.dir === 'asc' ? 'desc' : 'asc' } : { key: c.key, dir: c.numeric ? 'desc' : 'asc' }
    if (onSortChange) onSortChange(next(sort))
    else setSort(next)
  }

  const download = () => {
    const blob = new Blob([toCSV(columns, sorted)], { type: 'text/csv;charset=utf-8' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${csvName ?? 'table'}.csv`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  if (rows.length === 0) {
    return (
      <EmptyState title={emptyTitle ?? 'Nothing to show'}>
        {emptyBody}
      </EmptyState>
    )
  }

  return (
    <div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              {columns.map((c) => {
                const sorted = sort?.key === c.key
                return (
                  <th
                    key={c.key}
                    className={`${c.numeric ? 'num' : ''} ${c.sortable === false ? '' : 'sortable'} ${sorted ? 'sorted' : ''} ${c.className ?? ''}`}
                    style={c.width ? { width: c.width } : undefined}
                    onClick={() => toggleSort(c)}
                    aria-sort={sorted ? (sort?.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
                  >
                    {c.header}
                    {c.sortable === false ? null : <span className="sort">{sorted ? (sort?.dir === 'asc' ? '↑' : '↓') : '↕'}</span>}
                  </th>
                )
              })}
            </tr>
          </thead>
          <tbody>
            {visible.map((r, i) => {
              const k = rowKey(r, i)
              const detail = expanded ? expanded(r) : null
              return (
                <Fragment key={k}>
                  <tr
                    className={`${onRowClick ? 'clickable' : ''} ${selectedKey === k ? 'selected' : ''} ${detail ? 'open' : ''}`}
                    onClick={onRowClick ? () => onRowClick(r) : undefined}
                  >
                    {columns.map((c) => (
                      <td key={c.key} className={`${c.numeric ? 'num' : ''} ${c.className ?? ''}`}>
                        {c.render ? c.render(r) : (c.value(r) ?? '—')}
                      </td>
                    ))}
                  </tr>
                  {detail ? (
                    <tr className="expand">
                      <td colSpan={columns.length}>{detail}</td>
                    </tr>
                  ) : null}
                </Fragment>
              )
            })}
          </tbody>
          {hasTotals ? (
            <tfoot>
              <tr>
                {columns.map((c) => (
                  <td key={c.key} className={c.numeric ? 'num' : ''}>
                    {c.total ? c.total(sorted) : ''}
                  </td>
                ))}
              </tr>
            </tfoot>
          ) : null}
        </table>
      </div>
      <div className="table-foot">
        <span>
          {sorted.length.toLocaleString()} row{sorted.length === 1 ? '' : 's'}
          {footNote ? <> · {footNote}</> : null}
        </span>
        <span className="row">
          {pages > 1 ? (
            <span className="pager">
              <button className="small" onClick={() => setPage(Math.max(0, cur - 1))} disabled={cur === 0}>
                ‹
              </button>
              <span>
                {cur + 1} / {pages}
              </span>
              <button className="small" onClick={() => setPage(Math.min(pages - 1, cur + 1))} disabled={cur >= pages - 1}>
                ›
              </button>
            </span>
          ) : null}
          {csvName ? (
            <button className="link small" onClick={download}>
              Download CSV
            </button>
          ) : null}
        </span>
      </div>
    </div>
  )
}
