import { useMemo, useRef, useState } from 'react'
import { api, errorText } from '../api/client'
import type { ReconciliationLine, ReconciliationRun } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Empty, KPI, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { bucketLabel, BUCKETS, bucketSummaries, lineDifference, needsAttention } from '../lib/finance'
import { day, when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { useQuery } from '../lib/useQuery'

/**
 * Finance → Reconciliation (DESIGN.md §18.3): a gateway's settlement file
 * against what this product recorded, in four buckets with a row-level view.
 *
 * Nothing on this page corrects anything. It reports, and a human decides —
 * which is why every action here is an upload or a fetch, and never a fix.
 */
export function FinanceReconciliation() {
  const runs = useQuery<{ runs: ReconciliationRun[] }>('/finance/reconciliations')
  const [openID, setOpenID] = useState<string | null>(null)
  // The run on show is the one clicked, else the newest — an operator lands
  // on the last reconciliation rather than on an empty page.
  const activeID = openID ?? runs.data?.runs?.[0]?.id ?? null
  const detail = useQuery<{ run: ReconciliationRun }>(activeID ? `/finance/reconciliation/${activeID}` : null)
  const [gateway, setGateway] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [ok, setOk] = useState('')
  const [filter, setFilter] = useState<string>('all')
  const fileRef = useRef<HTMLInputElement>(null)

  const run = detail.data?.run ?? runs.data?.runs?.[0] ?? null
  const summaries = bucketSummaries(run)
  const currency = run?.currency || ''
  const money = (v: number | string | null | undefined, cur = currency) => formatMoney(toNumber(v), cur)
  // The rows of whichever run is open: the one clicked, else the newest.
  const lines = useMemo(() => (run?.lines ?? []).filter((l) => filter === 'all' || l.bucket === filter), [run, filter])

  const query = () => {
    const parts: string[] = []
    if (gateway.trim()) parts.push(`gateway=${encodeURIComponent(gateway.trim())}`)
    if (from.trim()) parts.push(`from=${from.trim()}`)
    if (to.trim()) parts.push(`to=${to.trim()}`)
    return parts.length ? `?${parts.join('&')}` : ''
  }

  const finish = async (res: ReconciliationRun) => {
    setOk(`reconciled ${res.matched + res.amount_mismatched + res.missing_in_ledger + res.duplicates} settlement line${res.matched + res.amount_mismatched + res.missing_in_ledger + res.duplicates === 1 ? '' : 's'}`)
    setOpenID(res.id)
    await runs.reload()
  }

  const upload = async (file: File) => {
    setBusy(true)
    setError('')
    setOk('')
    try {
      const form = new FormData()
      form.append('file', file)
      const res = await api.upload<{ run: ReconciliationRun }>(`/finance/reconciliation${query()}`, form)
      await finish(res.run)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  const fetchFromGateway = async () => {
    setBusy(true)
    setError('')
    setOk('')
    try {
      const res = await api.post<{ run: ReconciliationRun }>(`/finance/reconciliation${query()}`)
      await finish(res.run)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  const columns: Column<ReconciliationLine>[] = [
    { key: 'bucket', header: 'Bucket', value: (l) => l.bucket, render: (l) => <Badge status={bucketLabel(l.bucket)} kind={BUCKETS.find((b) => b.value === l.bucket)?.tone ?? 'info'} /> },
    { key: 'reference', header: 'Gateway reference', value: (l) => l.gateway_reference ?? '', render: (l) => <span className="mono">{l.gateway_reference || '—'}</span> },
    { key: 'settled_date', header: 'Settled', value: (l) => l.settled_date ?? '', render: (l) => (l.settled_date ? <span className="mono">{l.settled_date}</span> : <span className="muted">—</span>) },
    { key: 'settled', header: 'Gateway says', numeric: true, value: (l) => toNumber(l.settled_amount), render: (l) => (l.settled_amount === null || l.settled_amount === undefined ? <span className="muted">—</span> : money(l.settled_amount, l.currency)) },
    { key: 'ledger', header: 'We recorded', numeric: true, value: (l) => toNumber(l.ledger_amount), render: (l) => (l.ledger_amount === null || l.ledger_amount === undefined ? <span className="muted">—</span> : money(l.ledger_amount, l.currency)) },
    {
      key: 'difference',
      header: 'Difference',
      numeric: true,
      value: (l) => lineDifference(l) ?? 0,
      render: (l) => {
        const d = lineDifference(l)
        if (d === null) return <span className="muted">—</span>
        return d === 0 ? <span className="ok">{money(0, l.currency)}</span> : <span className="bad">{money(d, l.currency)}</span>
      },
    },
    { key: 'fee', header: 'Fee', numeric: true, value: (l) => toNumber(l.fee), render: (l) => (toNumber(l.fee) ? money(l.fee, l.currency) : <span className="muted">—</span>), total: (rs) => money(rs.reduce((n, l) => n + toNumber(l.fee), 0)) },
    { key: 'customer', header: 'Customer', value: (l) => l.customer_name ?? '', render: (l) => (l.customer_name ? <>{l.customer_name}</> : <span className="muted">—</span>) },
    { key: 'detail', header: 'What it says', value: (l) => l.detail ?? '', render: (l) => <span className="sub">{l.detail || ''}</span> },
  ]

  const runColumns: Column<ReconciliationRun>[] = [
    { key: 'ran_at', header: 'Run', value: (r) => r.ran_at, render: (r) => <>{when(r.ran_at)}<span className="sub">{r.ran_by}</span></> },
    { key: 'gateway', header: 'Gateway', value: (r) => r.gateway ?? '', render: (r) => <span className="mono">{r.gateway || '—'}</span> },
    { key: 'source', header: 'From', value: (r) => r.source, render: (r) => <>{r.source === 'gateway' ? 'the gateway feed' : r.file_name || 'an uploaded file'}</> },
    { key: 'window', header: 'Window', value: (r) => r.from ?? '', render: (r) => (r.from ? <span className="mono">{day(r.from)} → {day(r.to)}</span> : <span className="muted">—</span>) },
    { key: 'matched', header: 'Matched', numeric: true, value: (r) => r.matched },
    { key: 'attention', header: 'Needs attention', numeric: true, value: (r) => needsAttention(r), render: (r) => (needsAttention(r) ? <span className="bad">{needsAttention(r)}</span> : <span className="ok">none</span>) },
    { key: 'fees', header: 'Fees', numeric: true, value: (r) => toNumber(r.fee_total), render: (r) => formatMoney(toNumber(r.fee_total), r.currency || '') },
  ]

  return (
    <div className="stack">
      <PageHeader title="Reconciliation" sub="a gateway's settlement against what this product recorded — it reports, and you decide" />

      {error ? <Notice kind="bad">{error}</Notice> : null}
      {ok ? <Notice kind="ok">{ok}</Notice> : null}

      <section className="card">
        <h2>Run a reconciliation</h2>
        <p className="muted">Upload the gateway's settlement file, or fetch it when that gateway publishes a feed. Nothing is corrected either way.</p>
        <div className="row">
          <label className="inline">
            <span className="muted">Gateway</span> <input value={gateway} onChange={(e) => setGateway(e.target.value)} placeholder="omantel" aria-label="Gateway" />
          </label>
          <label className="inline">
            <span className="muted">From</span> <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="From" />
          </label>
          <label className="inline">
            <span className="muted">To</span> <input type="date" value={to} onChange={(e) => setTo(e.target.value)} aria-label="To" />
          </label>
        </div>
        <div className="btn-row">
          <input
            ref={fileRef}
            type="file"
            accept=".csv,text/csv"
            aria-label="Settlement file"
            disabled={busy}
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) void upload(f)
            }}
          />
          <button className="primary" disabled={busy} onClick={() => void fetchFromGateway()}>
            Fetch from the gateway
          </button>
        </div>
        <p className="help">The file needs a gateway reference and an amount; a currency, a settled date and a fee are read when they are there.</p>
      </section>

      {run ? (
        <>
          <div className="kpis">
            {summaries.map((b) => (
              <KPI key={b.value} label={b.label} value={b.count} note={b.amount ? money(b.amount) : '—'} tone={b.count ? b.tone === 'info' ? undefined : b.tone : undefined} hint={b.hint} />
            ))}
          </div>
          <section className="card">
            <h2>
              Lines <span className="muted">{run.gateway ? `· ${run.gateway}` : ''}</span>
            </h2>
            <p className="muted">
              Settled {money(run.settled_total)} against {money(run.ledger_total)} recorded, with {money(run.fee_total)} of fees — each fee is its own pair of journal lines.
            </p>
            <Segmented
              ariaLabel="Bucket"
              value={filter}
              onChange={setFilter}
              options={[{ value: 'all', label: 'All' }, ...BUCKETS.map((b) => ({ value: b.value as string, label: b.label }))]}
            />
            {detail.loading && !detail.data ? (
              <Skeleton lines={4} />
            ) : (
              <DataTable label="Reconciliation lines" columns={columns} rows={lines} rowKey={(l, i) => String(l.id ?? i)} defaultSort={{ key: 'bucket', dir: 'asc' }} emptyTitle="Nothing in this bucket" emptyBody={<>Pick another bucket, or run a reconciliation.</>} csvName={`reconciliation-${run.id}`} />
            )}
          </section>
        </>
      ) : runs.loading ? (
        <Skeleton lines={5} />
      ) : null}

      <section className="card">
        <h2>Runs</h2>
        {runs.error ? <Notice kind="bad">{runs.error}</Notice> : null}
        {runs.data?.runs?.length ? (
          <DataTable
            label="Reconciliation runs"
            columns={runColumns}
            rows={runs.data.runs}
            rowKey={(r) => r.id}
            defaultSort={{ key: 'ran_at', dir: 'desc' }}
            selectedKey={activeID}
            onRowClick={(r) => setOpenID(r.id)}
            emptyTitle="No reconciliation has been run"
          />
        ) : (
          <Empty>No reconciliation has been run yet. Upload a settlement file above.</Empty>
        )}
      </section>
    </div>
  )
}
