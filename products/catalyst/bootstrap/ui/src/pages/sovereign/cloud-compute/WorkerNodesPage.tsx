/**
 * WorkerNodesPage — list view for /cloud/compute/worker-nodes (P3 of #309).
 * Flattens regions[].clusters[].nodes[] into one row per node VM.
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
  NodeSpec,
  RegionSpec,
  TopologyStatus,
} from '@/lib/infrastructure.types'

interface NodeRow {
  id: string
  node: NodeSpec
  cluster: ClusterSpec
  region: RegionSpec
}

const STATUSES: readonly TopologyStatus[] = ['healthy', 'degraded', 'failed', 'unknown']
const TEST_ID = 'cloud-worker-nodes'

function flatten(data: ReturnType<typeof useCloud>['data']): NodeRow[] {
  if (!data) return []
  const rows: NodeRow[] = []
  for (const region of data.topology.regions ?? []) {
    for (const cluster of region.clusters ?? []) {
      for (const node of cluster.nodes ?? []) {
        rows.push({ id: node.id, node, cluster, region })
      }
    }
  }
  return rows
}

function compare(a: NodeRow, b: NodeRow, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  switch (sort.column) {
    case 'hostname':
      return dir * a.node.name.localeCompare(b.node.name)
    case 'parentCluster':
      return dir * a.cluster.name.localeCompare(b.cluster.name)
    case 'role':
      return dir * a.node.role.localeCompare(b.node.role)
    case 'kubeletVersion':
      return dir * a.cluster.version.localeCompare(b.cluster.version)
    case 'sku':
      return dir * a.node.sku.localeCompare(b.node.sku)
    case 'status':
      return dir * a.node.status.localeCompare(b.node.status)
    default:
      return 0
  }
}

export function WorkerNodesPage() {
  const { deploymentId, data, isLoading } = useCloud()
  const rows = useMemo(() => flatten(data), [data])

  const [statusFilter, setStatusFilter] = useState<TopologyStatus | ''>('')
  const [roleFilter, setRoleFilter] = useState<string>('')
  const roleOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const r of rows) set.add(r.node.role)
    return [...set].sort()
  }, [rows])

  const list = useCloudListState<NodeRow>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.node.name.toLowerCase().includes(s) ||
        row.node.id.toLowerCase().includes(s) ||
        row.node.ip.toLowerCase().includes(s) ||
        row.cluster.name.toLowerCase().includes(s)
      )
    },
    matchExtra: (row) => {
      if (statusFilter && row.node.status !== statusFilter) return false
      if (roleFilter && row.node.role !== roleFilter) return false
      return true
    },
    comparator: compare,
    defaultSort: { column: 'hostname', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<NodeRow | null>(null)

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="Worker Nodes"
        tagline="Individual VMs / kubelets — one row per node across every cluster."
        count={rows.length}
        deploymentId={deploymentId}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading worker nodes…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No worker nodes yet."
          body="Nodes appear here as soon as their kubelet registers with the cluster."
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
                  label="Role"
                  options={roleOptions}
                  selected={roleFilter}
                  onChange={setRoleFilter}
                  testId={`${TEST_ID}-filter-role`}
                />
              </>
            }
          />
          <div className="cloud-list-table-scroll">
            <table className="cloud-list-table" data-testid={`${TEST_ID}-table`}>
              <thead>
                <tr>
                  <SortableTH testId={`${TEST_ID}-th-hostname`} column="hostname" label="Hostname" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-parentCluster`} column="parentCluster" label="Parent cluster" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-role`} column="role" label="Role" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-kubeletVersion`} column="kubeletVersion" label="Kubelet" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-sku`} column="sku" label="SKU" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-status`} column="status" label="Status" state={list.sort} onChange={list.setSort} />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No worker nodes match the current filters.
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
                      <td className="cloud-list-cell cloud-list-cell-name cloud-list-cell-mono">{row.node.name}</td>
                      <td className="cloud-list-cell">{row.cluster.name}</td>
                      <td className="cloud-list-cell">{row.node.role}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.cluster.version}</td>
                      <td className="cloud-list-cell cloud-list-cell-mono">{row.node.sku}</td>
                      <td className="cloud-list-cell"><StatusPill status={row.node.status} /></td>
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
        title={openRow ? `Worker Node — ${openRow.node.name}` : ''}
      >
        {openRow ? (
          <>
            <DetailRow label="Hostname" value={openRow.node.name} mono />
            <DetailRow label="ID" value={openRow.node.id} mono />
            <DetailRow label="Role" value={openRow.node.role} />
            <DetailRow label="SKU" value={openRow.node.sku} mono />
            <DetailRow label="IP" value={openRow.node.ip} mono />
            <DetailRow label="Status" value={<StatusPill status={openRow.node.status} />} />
            <DetailRow label="Kubelet" value={openRow.cluster.version} mono />
            <DetailRow label="Parent cluster" value={openRow.cluster.name} />
            <DetailRow label="Region" value={openRow.region.providerRegion} />
            <DetailRow label="Provider" value={openRow.region.provider} />
          </>
        ) : null}
      </CloudListDetailDrawer>
    </div>
  )
}
