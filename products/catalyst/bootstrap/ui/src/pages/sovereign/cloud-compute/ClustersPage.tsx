/**
 * ClustersPage — list view for /cloud/compute/clusters (P3 of #309).
 *
 * Pattern parallels JobsPage / JobsTable: header + toolbar + sortable
 * table + per-row click → detail drawer. Source data is the shared
 * hierarchical infrastructure tree exposed via useCloud(); we flatten
 * regions[].clusters[] into one row per cluster with parent region
 * carried alongside for the detail drawer.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every
 * provider / region / status string flows from the shared tree —
 * there's no inlined "k3s" or "fsn1".
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
} from '@/lib/infrastructure.types'

interface ClusterRow {
  id: string
  cluster: ClusterSpec
  region: RegionSpec
}

const STATUSES: readonly TopologyStatus[] = ['healthy', 'degraded', 'failed', 'unknown']

const TEST_ID = 'cloud-clusters'

function flattenClusters(
  data: ReturnType<typeof useCloud>['data'],
): ClusterRow[] {
  if (!data) return []
  const rows: ClusterRow[] = []
  for (const region of data.topology.regions ?? []) {
    for (const cluster of region.clusters ?? []) {
      rows.push({ id: cluster.id, cluster, region })
    }
  }
  return rows
}

function compareClusters(a: ClusterRow, b: ClusterRow, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  const ca = a.cluster
  const cb = b.cluster
  switch (sort.column) {
    case 'name':
      return dir * ca.name.localeCompare(cb.name)
    case 'region':
      return dir * a.region.providerRegion.localeCompare(b.region.providerRegion)
    case 'provider':
      return dir * a.region.provider.localeCompare(b.region.provider)
    case 'type':
      return dir * ca.version.localeCompare(cb.version)
    case 'status':
      return dir * ca.status.localeCompare(cb.status)
    case 'nodeCount':
      return dir * (ca.nodeCount - cb.nodeCount)
    case 'vclusterCount':
      return dir * ((ca.vclusters?.length ?? 0) - (cb.vclusters?.length ?? 0))
    default:
      return 0
  }
}

export function ClustersPage() {
  const { deploymentId, data, isLoading } = useCloud()

  const rows = useMemo(() => flattenClusters(data), [data])
  const regionOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const r of rows) set.add(r.region.providerRegion)
    return [...set].sort()
  }, [rows])

  const [statusFilter, setStatusFilter] = useState<TopologyStatus | ''>('')
  const [regionFilter, setRegionFilter] = useState<string>('')

  const list = useCloudListState<ClusterRow>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.cluster.name.toLowerCase().includes(s) ||
        row.cluster.id.toLowerCase().includes(s) ||
        row.region.providerRegion.toLowerCase().includes(s)
      )
    },
    matchExtra: (row) => {
      if (statusFilter && row.cluster.status !== statusFilter) return false
      if (regionFilter && row.region.providerRegion !== regionFilter) return false
      return true
    },
    comparator: compareClusters,
    defaultSort: { column: 'name', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<ClusterRow | null>(null)

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="Clusters"
        tagline="k3s / k8s control planes — one row per cluster across all regions."
        count={rows.length}
        deploymentId={deploymentId}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading clusters…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No clusters yet."
          body="Once the Sovereign control plane comes up, every k3s cluster will appear here."
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
                  <SortableTH testId={`${TEST_ID}-th-provider`} column="provider" label="Provider" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-type`} column="type" label="Type" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-status`} column="status" label="Status" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-nodeCount`} column="nodeCount" label="Nodes" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-vclusterCount`} column="vclusterCount" label="vClusters" state={list.sort} onChange={list.setSort} />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No clusters match the current filters.
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
                      <td className="cloud-list-cell cloud-list-cell-name">{row.cluster.name}</td>
                      <td className="cloud-list-cell">{row.region.providerRegion}</td>
                      <td className="cloud-list-cell">{row.region.provider}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.cluster.version}</td>
                      <td className="cloud-list-cell"><StatusPill status={row.cluster.status} /></td>
                      <td className="cloud-list-cell">{row.cluster.nodeCount}</td>
                      <td className="cloud-list-cell">{row.cluster.vclusters?.length ?? 0}</td>
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
        title={openRow ? `Cluster — ${openRow.cluster.name}` : ''}
      >
        {openRow ? (
          <>
            <DetailRow label="Name" value={openRow.cluster.name} />
            <DetailRow label="ID" value={openRow.cluster.id} mono />
            <DetailRow label="Region" value={openRow.region.providerRegion} />
            <DetailRow label="Provider" value={openRow.region.provider} />
            <DetailRow label="Type" value={openRow.cluster.version} mono />
            <DetailRow label="Status" value={<StatusPill status={openRow.cluster.status} />} />
            <DetailRow label="Worker count" value={openRow.region.workerCount} />
            <DetailRow label="Total nodes" value={openRow.cluster.nodeCount} />
            <DetailRow label="Node pools" value={openRow.cluster.nodePools?.length ?? 0} />
            <DetailRow label="vClusters" value={openRow.cluster.vclusters?.length ?? 0} />
            <DetailRow label="Load balancers" value={openRow.cluster.loadBalancers?.length ?? 0} />
            <DetailRow label="Worker SKU" value={openRow.region.skuWorker} mono />
            <DetailRow label="Control-plane SKU" value={openRow.region.skuCp} mono />
          </>
        ) : null}
      </CloudListDetailDrawer>
    </div>
  )
}
