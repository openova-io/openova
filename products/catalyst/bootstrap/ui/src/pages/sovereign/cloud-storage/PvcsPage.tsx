/**
 * PvcsPage — list view for /cloud/storage/pvcs (P3 of #309). Reads
 * data.storage.pvcs from the shared infrastructure tree.
 */

import { useMemo, useState } from 'react'
import { useCloud } from '../CloudPage'
import {
  CloudListDetailDrawer,
  CloudListHeader,
  CloudListToolbar,
  DetailRow,
  EmptyState,
  FilterPills,
  Pagination,
  SortableTH,
  StatusPill,
} from '../cloud-list/cloudListShared'
import { CLOUD_LIST_CSS } from '../cloud-list/cloudListCss'
import { useCloudListState } from '../cloud-list/useCloudListState'
import type { SortState } from '../cloud-list/sortState'
import type { PVCItem, TopologyStatus } from '@/lib/infrastructure.types'

const STATUSES: readonly TopologyStatus[] = ['healthy', 'degraded', 'failed', 'unknown']
const TEST_ID = 'cloud-pvcs'

function compare(a: PVCItem, b: PVCItem, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  switch (sort.column) {
    case 'name':
      return dir * a.name.localeCompare(b.name)
    case 'namespace':
      return dir * a.namespace.localeCompare(b.namespace)
    case 'capacity':
      return dir * a.capacity.localeCompare(b.capacity)
    case 'storageClass':
      return dir * a.storageClass.localeCompare(b.storageClass)
    case 'status':
      return dir * a.status.localeCompare(b.status)
    default:
      return 0
  }
}

export function PvcsPage() {
  const { deploymentId, data, isLoading } = useCloud()
  const rows = useMemo<readonly PVCItem[]>(() => data?.storage?.pvcs ?? [], [data])

  const [statusFilter, setStatusFilter] = useState<TopologyStatus | ''>('')
  const [classFilter, setClassFilter] = useState<string>('')
  const classOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const r of rows) set.add(r.storageClass)
    return [...set].sort()
  }, [rows])

  const list = useCloudListState<PVCItem>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.name.toLowerCase().includes(s) ||
        row.namespace.toLowerCase().includes(s) ||
        row.id.toLowerCase().includes(s)
      )
    },
    matchExtra: (row) => {
      if (statusFilter && row.status !== statusFilter) return false
      if (classFilter && row.storageClass !== classFilter) return false
      return true
    },
    comparator: compare,
    defaultSort: { column: 'name', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<PVCItem | null>(null)

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="PVCs"
        tagline="Persistent volume claims across all namespaces and clusters."
        count={rows.length}
        deploymentId={deploymentId}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading PVCs…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No PVCs yet."
          body="PVCs appear here once stateful workloads claim storage."
        />
      ) : (
        <>
          <CloudListToolbar
            testId={TEST_ID}
            search={list.search}
            onSearchChange={list.setSearch}
            visibleCount={list.sorted.length}
            totalCount={rows.length}
            filters={
              <>
                <FilterPills
                  label="Status"
                  options={STATUSES}
                  selected={statusFilter}
                  onChange={setStatusFilter}
                  testId={`${TEST_ID}-filter-status`}
                />
                <FilterPills
                  label="Class"
                  options={classOptions}
                  selected={classFilter}
                  onChange={setClassFilter}
                  testId={`${TEST_ID}-filter-class`}
                />
              </>
            }
          />
          <div className="cloud-list-table-scroll">
            <table className="cloud-list-table" data-testid={`${TEST_ID}-table`}>
              <thead>
                <tr>
                  <SortableTH testId={`${TEST_ID}-th-name`} column="name" label="Name" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-namespace`} column="namespace" label="Namespace" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-capacity`} column="capacity" label="Capacity" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-storageClass`} column="storageClass" label="Storage class" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-status`} column="status" label="Status" state={list.sort} onChange={list.setSort} />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No PVCs match the current filters.
                    </td>
                  </tr>
                ) : (
                  list.visible.map((row) => (
                    <tr
                      key={row.id}
                      className="cloud-list-row"
                      data-testid={`${TEST_ID}-row-${row.id}`}
                      onClick={() => setOpenRow(row)}
                    >
                      <td className="cloud-list-cell cloud-list-cell-name">{row.name}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.namespace}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.capacity}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.storageClass}</td>
                      <td className="cloud-list-cell"><StatusPill status={row.status} /></td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
          <Pagination
            testId={TEST_ID}
            page={list.page}
            pageSize={list.pageSize}
            total={list.sorted.length}
            onPageChange={list.setPage}
          />
        </>
      )}

      <CloudListDetailDrawer
        testId={`${TEST_ID}-detail`}
        open={!!openRow}
        onClose={() => setOpenRow(null)}
        title={openRow ? `PVC — ${openRow.name}` : ''}
      >
        {openRow ? (
          <>
            <DetailRow label="Name" value={openRow.name} />
            <DetailRow label="ID" value={openRow.id} mono />
            <DetailRow label="Namespace" value={openRow.namespace} mono />
            <DetailRow label="Capacity" value={openRow.capacity} mono />
            <DetailRow label="Used" value={openRow.used || '—'} mono />
            <DetailRow label="Storage class" value={openRow.storageClass} mono />
            <DetailRow label="Status" value={<StatusPill status={openRow.status} />} />
          </>
        ) : null}
      </CloudListDetailDrawer>
    </div>
  )
}
