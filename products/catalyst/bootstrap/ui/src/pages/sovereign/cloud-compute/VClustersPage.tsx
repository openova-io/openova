/**
 * VClustersPage — list view for /cloud/compute/vclusters (P3 of #309).
 * Flattens regions[].clusters[].vclusters[] into one row per vCluster.
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
  RegionSpec,
  TopologyStatus,
  VClusterSpec,
} from '@/lib/infrastructure.types'

interface VClusterRow {
  id: string
  vcluster: VClusterSpec
  cluster: ClusterSpec
  region: RegionSpec
}

const STATUSES: readonly TopologyStatus[] = ['healthy', 'degraded', 'failed', 'unknown']
const TEST_ID = 'cloud-vclusters'

function flattenVClusters(
  data: ReturnType<typeof useCloud>['data'],
): VClusterRow[] {
  if (!data) return []
  const rows: VClusterRow[] = []
  for (const region of data.topology.regions ?? []) {
    for (const cluster of region.clusters ?? []) {
      for (const vc of cluster.vclusters ?? []) {
        rows.push({ id: vc.id, vcluster: vc, cluster, region })
      }
    }
  }
  return rows
}

function compare(a: VClusterRow, b: VClusterRow, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  switch (sort.column) {
    case 'name':
      return dir * a.vcluster.name.localeCompare(b.vcluster.name)
    case 'parentCluster':
      return dir * a.cluster.name.localeCompare(b.cluster.name)
    case 'region':
      return dir * a.region.providerRegion.localeCompare(b.region.providerRegion)
    case 'isolation':
      return dir * a.vcluster.isolationMode.localeCompare(b.vcluster.isolationMode)
    case 'status':
      return dir * a.vcluster.status.localeCompare(b.vcluster.status)
    default:
      return 0
  }
}

export function VClustersPage() {
  const { deploymentId, data, isLoading } = useCloud()
  const rows = useMemo(() => flattenVClusters(data), [data])

  const [statusFilter, setStatusFilter] = useState<TopologyStatus | ''>('')
  const list = useCloudListState<VClusterRow>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.vcluster.name.toLowerCase().includes(s) ||
        row.vcluster.id.toLowerCase().includes(s) ||
        row.cluster.name.toLowerCase().includes(s)
      )
    },
    matchExtra: (row) => !statusFilter || row.vcluster.status === statusFilter,
    comparator: compare,
    defaultSort: { column: 'name', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<VClusterRow | null>(null)

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="vClusters"
        tagline="Logical isolation slices (DMZ / RTZ / MGMT) inside each physical cluster."
        count={rows.length}
        deploymentId={deploymentId}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading vClusters…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No vClusters yet."
          body="vClusters are provisioned alongside their parent k3s cluster."
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
                  <SortableTH testId={`${TEST_ID}-th-region`} column="region" label="Region" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-isolation`} column="isolation" label="Isolation" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-status`} column="status" label="Status" state={list.sort} onChange={list.setSort} />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No vClusters match the current filters.
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
                      <td className="cloud-list-cell cloud-list-cell-name">{row.vcluster.name}</td>
                      <td className="cloud-list-cell">{row.cluster.name}</td>
                      <td className="cloud-list-cell">{row.region.providerRegion}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.vcluster.isolationMode}</td>
                      <td className="cloud-list-cell"><StatusPill status={row.vcluster.status} /></td>
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
        title={openRow ? `vCluster — ${openRow.vcluster.name}` : ''}
      >
        {openRow ? (
          <>
            <DetailRow label="Name" value={openRow.vcluster.name} />
            <DetailRow label="ID" value={openRow.vcluster.id} mono />
            <DetailRow label="Isolation" value={openRow.vcluster.isolationMode} mono />
            <DetailRow label="Status" value={<StatusPill status={openRow.vcluster.status} />} />
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
