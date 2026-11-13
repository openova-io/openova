/**
 * NodePoolsPage — list view for /cloud/compute/node-pools (P3 of #309).
 * Flattens regions[].clusters[].nodePools[] into one row per pool.
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
import type {
  ClusterSpec,
  NodePoolSpec,
  RegionSpec,
  TopologyStatus,
} from '@/lib/infrastructure.types'

interface NodePoolRow {
  id: string
  pool: NodePoolSpec
  cluster: ClusterSpec
  region: RegionSpec
}

const STATUSES: readonly TopologyStatus[] = ['healthy', 'degraded', 'failed', 'unknown']
const TEST_ID = 'cloud-node-pools'

function flatten(data: ReturnType<typeof useCloud>['data']): NodePoolRow[] {
  if (!data) return []
  const rows: NodePoolRow[] = []
  for (const region of data.topology.regions ?? []) {
    for (const cluster of region.clusters ?? []) {
      for (const pool of cluster.nodePools ?? []) {
        rows.push({ id: pool.id, pool, cluster, region })
      }
    }
  }
  return rows
}

function compare(a: NodePoolRow, b: NodePoolRow, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  switch (sort.column) {
    case 'name':
      return dir * a.pool.id.localeCompare(b.pool.id)
    case 'parentCluster':
      return dir * a.cluster.name.localeCompare(b.cluster.name)
    case 'sku':
      return dir * a.pool.sku.localeCompare(b.pool.sku)
    case 'replicas':
      return dir * (a.pool.replicas - b.pool.replicas)
    case 'status':
      return dir * a.pool.status.localeCompare(b.pool.status)
    default:
      return 0
  }
}

export function NodePoolsPage() {
  const { deploymentId, data, isLoading } = useCloud()
  const rows = useMemo(() => flatten(data), [data])

  const [statusFilter, setStatusFilter] = useState<TopologyStatus | ''>('')
  const list = useCloudListState<NodePoolRow>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.pool.id.toLowerCase().includes(s) ||
        row.pool.sku.toLowerCase().includes(s) ||
        row.cluster.name.toLowerCase().includes(s)
      )
    },
    matchExtra: (row) => !statusFilter || row.pool.status === statusFilter,
    comparator: compare,
    defaultSort: { column: 'name', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<NodePoolRow | null>(null)

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="Node Pools"
        tagline="Worker pools grouped by SKU + role; one row per pool across all clusters."
        count={rows.length}
        deploymentId={deploymentId}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading node pools…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No node pools yet."
          body="Pools appear once their cluster has scheduled at least one worker."
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
              <FilterPills
                label="Status"
                options={STATUSES}
                selected={statusFilter}
                onChange={setStatusFilter}
                testId={`${TEST_ID}-filter-status`}
              />
            }
          />
          <div className="cloud-list-table-scroll">
            <table className="cloud-list-table" data-testid={`${TEST_ID}-table`}>
              <thead>
                <tr>
                  <SortableTH testId={`${TEST_ID}-th-name`} column="name" label="Name" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-parentCluster`} column="parentCluster" label="Parent cluster" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-sku`} column="sku" label="Machine type" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-replicas`} column="replicas" label="Replicas" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-status`} column="status" label="Status" state={list.sort} onChange={list.setSort} />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No node pools match the current filters.
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
                      <td className="cloud-list-cell cloud-list-cell-name cloud-list-cell-mono">{row.pool.id}</td>
                      <td className="cloud-list-cell">{row.cluster.name}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.pool.sku}</td>
                      <td className="cloud-list-cell">{row.pool.replicas}</td>
                      <td className="cloud-list-cell"><StatusPill status={row.pool.status} /></td>
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
        title={openRow ? `Node Pool — ${openRow.pool.id}` : ''}
      >
        {openRow ? (
          <>
            <DetailRow label="Pool ID" value={openRow.pool.id} mono />
            <DetailRow label="Machine type" value={openRow.pool.sku} mono />
            <DetailRow label="Replicas" value={openRow.pool.replicas} />
            <DetailRow label="Status" value={<StatusPill status={openRow.pool.status} />} />
            <DetailRow label="Parent cluster" value={openRow.cluster.name} />
            <DetailRow label="Cluster ID" value={openRow.cluster.id} mono />
            <DetailRow label="Region" value={openRow.region.providerRegion} />
            <DetailRow label="Provider" value={openRow.region.provider} />
          </>
        ) : null}
      </CloudListDetailDrawer>
    </div>
  )
}
