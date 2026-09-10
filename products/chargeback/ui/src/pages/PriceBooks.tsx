import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import type { Layer, PriceBook, PriceBookCoverage, PriceItem } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge } from '../components/ui'
import { CurrencyRatesCard } from '../components/CurrencyRates'
import { BookSettingsModal, CloneBookModal, DeleteBookConfirm, billStoppedLabel, settingsFrom } from '../components/PriceBookForms'
import { KPI, Notice, PageHeader, Segmented, ShareBar, Skeleton } from '../components/ui'
import { day } from '../lib/format'
import { formatPct } from '../lib/money'
import { BOOK_ROLES, bookRole, booksInScope, layerLabel, scopeCounts, scopeOf } from '../lib/layers'
import { useCustomers } from '../lib/useCustomers'
import { useQuery } from '../lib/useQuery'

/**
 * Price books (DESIGN.md §2.5) — every book with its currency, item count,
 * assigned customers and how much of their last-30-day usage it prices.
 * Coverage comes from GET /pricebooks/{id}/coverage per book. Below the
 * list, the Currencies card holds the reporting currency and the exchange
 * rates books in other currencies are converted with (§3.10).
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
  // DESIGN.md §2: a book prices ONE layer. The filter is how an operator
  // finds the cloud rate cards without the plans book in the way.
  const [scope, setScope] = useState<Layer | 'all'>('all')

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
    () => booksInScope(list, scope).map((b) => ({ ...b, ...(extras[b.id] ?? { coverage: null, coverageError: '', itemCount: Array.isArray(b.items) ? b.items.length : null }) })),
    [list, extras, scope],
  )
  const counts = scopeCounts(list)
  // Assignment lives on the SOURCE (DESIGN.md §2), so the count comes from
  // the coverage document's `sources`; `customers` is its distinct owners.
  const sourcesOf = (r: Row) => r.coverage?.sources ?? []
  const assignedOf = (r: Row) => r.coverage?.customers ?? []
  const withoutBook = customers.filter((c) => (c.cloud_source_count ?? 0) + (c.platform_source_count ?? 0) === 0)
  const coverageKnown = rows.filter((r) => r.coverage)
  const unpriced = coverageKnown.reduce((n, r) => n + (r.coverage?.unpriced_count ?? 0), 0)

  const columns: Column<Row>[] = [
    {
      key: 'name',
      header: 'Name',
      value: (r) => r.name,
      render: (r) => {
        // Two platform books look alike in a list: one prices the committed
        // plan line, the other prices the meters a flexi Organization pays.
        // The role says which, and its help says why the other one does not
        // price the same SKUs.
        const role = bookRole(r)
        return (
          <>
            <Link to={`/pricebooks/${r.id}`}>{r.name}</Link>
            <span className="sub">
              {role ? <span title={BOOK_ROLES[role].help}>{BOOK_ROLES[role].label} · </span> : null}
              created {day(r.created_at)}
            </span>
          </>
        )
      },
    },
    {
      key: 'scope',
      header: 'Scope',
      value: (r) => scopeOf(r),
      render: (r) => <Badge status={layerLabel(scopeOf(r))} kind={scopeOf(r) === 'cloud' ? 'info' : undefined} />,
    },
    { key: 'currency', header: 'Currency', value: (r) => r.currency },
    { key: 'items', header: 'Items', value: (r) => r.itemCount, numeric: true, render: (r) => (r.itemCount === null ? <span className="muted">…</span> : r.itemCount.toLocaleString()) },
    {
      key: 'sources',
      header: 'Sources',
      value: (r) => sourcesOf(r).length,
      numeric: true,
      render: (r) => {
        const srcs = sourcesOf(r)
        const owners = assignedOf(r)
        if (!srcs.length) return <span className="muted">none</span>
        return (
          <span title={srcs.map((s) => `${s.customer_name} · ${s.label}`).join(', ')}>
            {srcs.length}
            <span className="sub">
              {owners.length} customer{owners.length === 1 ? '' : 's'}
            </span>
          </span>
        )
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
      className: 'actions',
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
        sub={`${counts.cloud} cloud · ${counts.platform} platform · a book prices one layer, and is assigned to the sources of that layer · coverage measured on the last 30 days of usage`}
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
        <KPI label="Price books" value={list.length} note={`${counts.cloud} cloud · ${counts.platform} platform`} />
        <KPI
          label="Customers without a source"
          value={withoutBook.length}
          note={withoutBook.length ? 'nothing is collected for them at all' : 'every customer has a cost source'}
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

      <div className="toolbar" role="region" aria-label="Price book scope">
        <div className="field">
          <label>Scope</label>
          <Segmented<Layer | 'all'>
            value={scope}
            options={[
              { value: 'all', label: `All (${counts.total})` },
              { value: 'cloud', label: `Cloud (${counts.cloud})` },
              { value: 'platform', label: `Platform (${counts.platform})` },
            ]}
            onChange={setScope}
            ariaLabel="Price book scope filter"
          />
        </div>
        <span className="muted small">
          A cloud book prices cloud SKUs. The two platform books are the two ways an Organization is billed: <strong>{BOOK_ROLES.plans.label}</strong> prices the flat plan line an S / M / L / XL Organization pays, and{' '}
          <strong>{BOOK_ROLES.payg.label}</strong> prices the k8s meters an uncapped flexi Organization pays instead. Neither prices what the other does, so nothing is billed twice.
        </span>
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

      <CurrencyRatesCard />

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
