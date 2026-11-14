/**
 * BucketsPage — list view for /cloud/storage/buckets (P3 of #309).
 * Reads data.storage.buckets from the shared infrastructure tree.
 */

import { useMemo, useState } from 'react'
import { useCloud } from '../CloudPage'
import {
  CloudListDetailDrawer,
  CloudListHeader,
  CloudListToolbar,
  DetailRow,
  EmptyState,
  Pagination,
  SortableTH,
} from '../cloud-list/cloudListShared'
import { CLOUD_LIST_CSS } from '../cloud-list/cloudListCss'
import { useCloudListState } from '../cloud-list/useCloudListState'
import type { SortState } from '../cloud-list/sortState'
import type { BucketItem } from '@/lib/infrastructure.types'

const TEST_ID = 'cloud-buckets'

function compare(a: BucketItem, b: BucketItem, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  switch (sort.column) {
    case 'name':
      return dir * a.name.localeCompare(b.name)
    case 'endpoint':
      return dir * a.endpoint.localeCompare(b.endpoint)
    case 'capacity':
      return dir * a.capacity.localeCompare(b.capacity)
    case 'used':
      return dir * (a.used ?? '').localeCompare(b.used ?? '')
    case 'retentionDays':
      return dir * (a.retentionDays ?? '').localeCompare(b.retentionDays ?? '')
    default:
      return 0
  }
}

/**
 * The current backend doesn't carry an explicit region per bucket
 * (it's encoded into the endpoint). We surface the endpoint host as
 * the "provider" surface and skip the region column. Status is also
 * not exposed today; treat all buckets as healthy on the table.
 */
export function BucketsPage() {
  const { deploymentId, data, isLoading } = useCloud()
  const rows = useMemo<readonly BucketItem[]>(() => data?.storage?.buckets ?? [], [data])

  const list = useCloudListState<BucketItem>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.name.toLowerCase().includes(s) ||
        row.id.toLowerCase().includes(s) ||
        row.endpoint.toLowerCase().includes(s)
      )
    },
    comparator: compare,
    defaultSort: { column: 'name', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<BucketItem | null>(null)

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="Buckets"
        tagline="S3-compatible buckets — SeaweedFS or provider-native."
        count={rows.length}
        deploymentId={deploymentId}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading buckets…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No buckets yet."
          body="Buckets appear once SeaweedFS or a provider-native bucket is provisioned."
        />
      ) : (
        <>
          <CloudListToolbar
            testId={TEST_ID}
            search={list.search}
            onSearchChange={list.setSearch}
            visibleCount={list.sorted.length}
            totalCount={rows.length}
          />
          <div className="cloud-list-table-scroll">
            <table className="cloud-list-table" data-testid={`${TEST_ID}-table`}>
              <thead>
                <tr>
                  <SortableTH testId={`${TEST_ID}-th-name`} column="name" label="Name" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-endpoint`} column="endpoint" label="Endpoint" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-capacity`} column="capacity" label="Capacity" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-used`} column="used" label="Used" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-retentionDays`} column="retentionDays" label="Retention" state={list.sort} onChange={list.setSort} />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No buckets match the current filters.
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
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.endpoint}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.capacity}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.used || '—'}</td>
                      <td className="cloud-list-cell">{row.retentionDays || 'indefinite'}</td>
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
        title={openRow ? `Bucket — ${openRow.name}` : ''}
      >
        {openRow ? (
          <>
            <DetailRow label="Name" value={openRow.name} />
            <DetailRow label="ID" value={openRow.id} mono />
            <DetailRow label="Endpoint" value={openRow.endpoint} mono />
            <DetailRow label="Capacity" value={openRow.capacity} mono />
            <DetailRow label="Used" value={openRow.used || '—'} mono />
            <DetailRow label="Retention" value={openRow.retentionDays || 'indefinite'} />
          </>
        ) : null}
      </CloudListDetailDrawer>
    </div>
  )
}
