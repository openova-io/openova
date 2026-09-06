import { asList } from '../api/client'
import type { AuditEntry } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Details, Notice, Skeleton } from '../components/ui'
import { when } from '../lib/format'
import { useQuery } from '../lib/useQuery'

/** Every recorded change to this customer, newest first (#6867). */
export function AuditPanel({ customerId }: { customerId: string }) {
  const q = useQuery<unknown>(`/customers/${customerId}/audit`)
  const rows = asList<AuditEntry>(q.data, 'entries', 'audit')
  const columns: Column<AuditEntry>[] = [
    { key: 'at', header: 'At', value: (a) => a.at, render: (a) => <span className="nowrap">{when(a.at)}</span> },
    { key: 'actor', header: 'Actor', value: (a) => a.actor },
    { key: 'action', header: 'Action', value: (a) => a.action, render: (a) => <span className="mono">{a.action}</span> },
    { key: 'details', header: 'Details', value: (a) => (a.details === undefined || a.details === null ? '' : typeof a.details === 'string' ? a.details : JSON.stringify(a.details)), sortable: false, render: (a) => <Details value={a.details} /> },
  ]
  if (q.error) return <Notice kind="bad">{q.error}</Notice>
  if (q.loading && !q.data) return <Skeleton lines={4} />
  return (
    <div className="card pad-0">
      <DataTable columns={columns} rows={rows} rowKey={(a, i) => String(a.id ?? i)} defaultSort={{ key: 'at', dir: 'desc' }} pageSize={50} csvName={`audit-${customerId}`} emptyTitle="No audit entries" emptyBody="Nothing has been changed on this customer since it was created." />
    </div>
  )
}
