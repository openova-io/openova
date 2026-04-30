/**
 * VolumesPage — list view for /cloud/storage/volumes (P3 of #309).
 * Reads data.storage.volumes from the shared infrastructure tree.
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
import type { TopologyStatus, VolumeItem } from '@/lib/infrastructure.types'

const STATUSES: readonly TopologyStatus[] = ['healthy', 'degraded', 'failed', 'unknown']
const TEST_ID = 'cloud-volumes'

function compare(a: VolumeItem, b: VolumeItem, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  switch (sort.column) {
    case 'name':
      return dir * a.name.localeCompare(b.name)
    case 'region':
      return dir * a.region.localeCompare(b.region)
    case 'attachedTo':
      return dir * (a.attachedTo ?? '').localeCompare(b.attachedTo ?? '')
    case 'capacity':
      return dir * a.capacity.localeCompare(b.capacity)
    case 'status':
      return dir * a.status.localeCompare(b.status)
    default:
      return 0
  }
}

export function VolumesPage() {
  const { deploymentId, data, isLoading } = useCloud()
  const rows = useMemo<readonly VolumeItem[]>(() => data?.storage?.volumes ?? [], [data])

  const [statusFilter, setStatusFilter] = useState<TopologyStatus | ''>('')
  const [regionFilter, setRegionFilter] = useState<string>('')
  const regionOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const r of rows) set.add(r.region)
    return [...set].sort()
  }, [rows])

  const list = useCloudListState<VolumeItem>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.name.toLowerCase().includes(s) ||
        row.id.toLowerCase().includes(s) ||
        (row.attachedTo ?? '').toLowerCase().includes(s)
      )
    },
    matchExtra: (row) => {
      if (statusFilter && row.status !== statusFilter) return false
      if (regionFilter && row.region !== regionFilter) return false
      return true
    },
    comparator: compare,
    defaultSort: { column: 'name', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<VolumeItem | null>(null)

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="Volumes"
        tagline="Cloud block volumes attached to nodes."
        count={rows.length}
        deploymentId={deploymentId}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading volumes…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No volumes yet."
          body="Volumes appear here as soon as a stateful workload claims one."
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
                  label="Region"
                  options={regionOptions}
                  selected={regionFilter}
                  onChange={setRegionFilter}
                  testId={`${TEST_ID}-filter-region`}
                />
              </>
            }
          />
          <div className="cloud-list-table-scroll">
            <table className="cloud-list-table" data-testid={`${TEST_ID}-table`}>
              <thead>
                <tr>
                  <SortableTH testId={`${TEST_ID}-th-name`} column="name" label="Name" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-region`} column="region" label="Region" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-attachedTo`} column="attachedTo" label="Attachment" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-capacity`} column="capacity" label="Capacity" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-status`} column="status" label="Status" state={list.sort} onChange={list.setSort} />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No volumes match the current filters.
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
                      <td className="cloud-list-cell">{row.region}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.attachedTo || 'detached'}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.capacity}</td>
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
        title={openRow ? `Volume — ${openRow.name}` : ''}
      >
        {openRow ? (
          <>
            <DetailRow label="Name" value={openRow.name} />
            <DetailRow label="ID" value={openRow.id} mono />
            <DetailRow label="Capacity" value={openRow.capacity} mono />
            <DetailRow label="Region" value={openRow.region} />
            <DetailRow label="Attached to" value={openRow.attachedTo || 'detached'} mono />
            <DetailRow label="Status" value={<StatusPill status={openRow.status} />} />
          </>
        ) : null}
      </CloudListDetailDrawer>
    </div>
  )
}
