import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import type { PriceBook, PriceBookCoverage, PriceItem } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { BookSettingsModal, CloneBookModal, DeleteBookConfirm, billStoppedLabel, settingsFrom } from '../components/PriceBookForms'
import { KPI, Notice, PageHeader, ShareBar, Skeleton } from '../components/ui'
import { day } from '../lib/format'
import { formatPct } from '../lib/money'
import { useCustomers } from '../lib/useCustomers'
import { useQuery } from '../lib/useQuery'

/**
 * Price books (DESIGN.md §2.5) — every book with its currency, item count,
 * assigned customers and how much of their last-30-day usage it prices.
 * Coverage comes from GET /pricebooks/{id}/coverage per book.
 */

interface Extra {
  coverage: PriceBookCoverage | null
  coverageError: string
  itemCount: number | null
}
type Row = PriceBook & Extra

type Dialog = { kind: 'new' } | { kind: 'clone'; book: PriceBook } | { kind: 'delete'; book: PriceBook } | null

export function PriceBooks() {
  const nav = useNavigate()
  const books = useQuery<unknown>('/pricebooks')
  const { customers } = useCustomers()
  const list = useMemo(() => asList<PriceBook>(books.data, 'pricebooks', 'price_books'), [books.data])
  const [extras, setExtras] = useState<Record<string, Extra>>({})
  const [dialog, setDialog] = useState<Dialog>(null)
  const [flash, setFlash] = useState('')

  // Coverage + item count per book. The list endpoint may omit items; the
  // detail call fills the count in only when it does.
  useEffect(() => {
    let cancelled = false
    if (list.length === 0) {
      setExtras({})
      return
    }
    void Promise.all(
      list.map(async (b) => {
        const [cov, itemCount] = await Promise.all([
          api
            .get<PriceBookCoverage>(`/pricebooks/${b.id}/coverage`)
            .then((c) => ({ c, e: '' }))
            .catch((e: unknown) => ({ c: null, e: errorText(e) })),
          Array.isArray(b.items)
            ? Promise.resolve<number | null>(b.items.length)
            : api
                .get<PriceBook>(`/pricebooks/${b.id}`)
                .then((d) => asList<PriceItem>(d.items ?? [], 'items').length)
                .catch(() => null),
        ])
        return [b.id, { coverage: cov.c, coverageError: cov.e, itemCount }] as const
      }),
    ).then((pairs) => {
      if (!cancelled) setExtras(Object.fromEntries(pairs))
    })
    return () => {
      cancelled = true
    }
  }, [list])

  const rows: Row[] = useMemo(
    () => list.map((b) => ({ ...b, ...(extras[b.id] ?? { coverage: null, coverageError: '', itemCount: Array.isArray(b.items) ? b.items.length : null }) })),
    [list, extras],
  )
  const assignedOf = (r: Row) => r.coverage?.customers ?? customers.filter((c) => c.price_book_id === r.id).map((c) => ({ id: c.id, name: c.name, slug: c.slug }))
  const withoutBook = customers.filter((c) => !c.price_book_id)
  const coverageKnown = rows.filter((r) => r.coverage)
  const unpriced = coverageKnown.reduce((n, r) => n + (r.coverage?.unpriced_count ?? 0), 0)

  const columns: Column<Row>[] = [
    {
      key: 'name',
      header: 'Name',
      value: (r) => r.name,
      render: (r) => (
        <>
          <Link to={`/pricebooks/${r.id}`}>{r.name}</Link>
          <span className="sub">created {day(r.created_at)}</span>
        </>
      ),
    },
    { key: 'currency', header: 'Currency', value: (r) => r.currency },
    { key: 'items', header: 'Items', value: (r) => r.itemCount, numeric: true, render: (r) => (r.itemCount === null ? <span className="muted">…</span> : r.itemCount.toLocaleString()) },
    {
      key: 'customers',
      header: 'Customers',
      value: (r) => assignedOf(r).length,
      numeric: true,
      render: (r) => {
        const a = assignedOf(r)
        return a.length ? <span title={a.map((c) => c.name).join(', ')}>{a.length}</span> : <span className="muted">none</span>
      },
    },
    {
      key: 'coverage',
      header: 'Coverage',
      value: (r) => r.coverage?.coverage_pct ?? null,
      numeric: true,
      render: (r) =>
        r.coverage ? (
          <>
            <ShareBar share={r.coverage.coverage_pct / 100} /> {formatPct(r.coverage.coverage_pct, { digits: 0 })}
            {r.coverage.unpriced_count ? <span className="sub warn">{r.coverage.unpriced_count} unpriced</span> : r.coverage.skus_in_use.length === 0 ? <span className="sub">no usage</span> : null}
          </>
        ) : r.coverageError ? (
          <span className="muted" title={r.coverageError}>
            —
          </span>
        ) : (
          <span className="muted">…</span>
        ),
    },
    { key: 'stopped', header: 'Stopped compute', value: (r) => billStoppedLabel(r.bill_stopped) },
    {
      key: 'effective',
      header: 'Effective from',
      value: (r) => r.effective_from ?? '',
      render: (r) => (r.effective_from ? day(r.effective_from) : <span className="muted">always</span>),
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      render: (r) => (
        <span className="btn-row">
          <button className="small" onClick={() => nav(`/pricebooks/${r.id}`)}>
            Open
          </button>
          <button className="small" onClick={() => setDialog({ kind: 'clone', book: r })}>
            Clone
          </button>
          <button className="small danger" onClick={() => setDialog({ kind: 'delete', book: r })}>
            Delete
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Price books"
        sub={`${list.length} book${list.length === 1 ? '' : 's'} · ${customers.length} customer${customers.length === 1 ? '' : 's'} · coverage measured on the last 30 days of usage`}
        actions={
          <>
            <a href={`${API_BASE}/pricebooks/template.csv`}>
              <button>Template CSV</button>
            </a>
            <button className="primary" onClick={() => setDialog({ kind: 'new' })}>
              New price book
            </button>
          </>
        }
      />
      {books.error ? <Notice kind="bad">{books.error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="kpis">
        <KPI label="Price books" value={list.length} note={`${rows.filter((r) => assignedOf(r).length > 0).length} in use by a customer`} />
        <KPI
          label="Customers without a book"
          value={withoutBook.length}
          note={withoutBook.length ? 'their usage rates as 0 until a book is assigned' : 'every customer is priced'}
          tone={withoutBook.length ? 'warn' : undefined}
          hint={withoutBook.map((c) => c.name).join(', ')}
        />
        <KPI
          label="Unpriced SKUs"
          value={coverageKnown.length === rows.length ? unpriced : '…'}
          note={unpriced ? 'in use in the last 30 days without a rate — cost shows as 0' : 'every SKU in use carries a rate'}
          tone={unpriced ? 'warn' : undefined}
        />
      </div>

      <div className="card pad-0">
        {books.loading && !books.data ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={4} />
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => r.id}
            defaultSort={{ key: 'name', dir: 'asc' }}
            emptyTitle="No price books"
            emptyBody={
              <>
                Nothing can be rated until a book exists: usage with no book prices at 0. Create one, then import the SKU list as CSV — the{' '}
                <a href={`${API_BASE}/pricebooks/template.csv`}>template</a> has the columns.
              </>
            }
          />
        )}
      </div>

      {dialog?.kind === 'new' ? (
        <BookSettingsModal
          title="New price book"
          initial={settingsFrom(null)}
          submitLabel="Create"
          onClose={() => setDialog(null)}
          onSubmit={async (body) => {
            const b = await api.post<PriceBook>('/pricebooks', body)
            setDialog(null)
            nav(`/pricebooks/${b.id}`)
          }}
        />
      ) : null}
      {dialog?.kind === 'clone' ? (
        <CloneBookModal
          book={dialog.book}
          onClose={() => setDialog(null)}
          onCloned={(copy) => {
            setDialog(null)
            if (copy) nav(`/pricebooks/${copy.id}`)
            else {
              setFlash(`${dialog.book.name} cloned`)
              void books.reload()
            }
          }}
        />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <DeleteBookConfirm
          book={dialog.book}
          assigned={assignedOf({ ...dialog.book, coverage: extras[dialog.book.id]?.coverage ?? null, coverageError: '', itemCount: null }).length}
          onClose={() => setDialog(null)}
          onDeleted={() => {
            setDialog(null)
            setFlash(`${dialog.book.name} deleted`)
            void books.reload()
          }}
        />
      ) : null}
    </div>
  )
}
