/**
 * LoadBalancersPage — list view for /cloud/network/load-balancers
 * (P3 of #309). Flattens regions[].clusters[].loadBalancers[] into one
 * row per LB.
 */

import { useMemo, useState } from 'react'
import { useCloud } from '../CloudPage'
import {
  CloudListDetailDrawer,
  CloudListHeader,
  CloudListToolbar,
  DetailDrawerActions,
  DetailRow,
  EmptyState,
  FilterPills,
  Pagination,
  RowActionsMenu,
  SortableTH,
  StatusPill,
} from '../cloud-list/cloudListShared'
import { CLOUD_LIST_CSS } from '../cloud-list/cloudListCss'
import { useCloudListState } from '../cloud-list/useCloudListState'
import type { SortState } from '../cloud-list/sortState'
import type {
  ClusterSpec,
  LoadBalancerSpec,
  RegionSpec,
  TopologyStatus,
} from '@/lib/infrastructure.types'
import {
  AddLBModal,
  EditLBModal,
  SimpleDeleteConfirm,
} from '@/components/CrudModals'

interface LBRow {
  id: string
  lb: LoadBalancerSpec
  cluster: ClusterSpec
  region: RegionSpec
}

const STATUSES: readonly TopologyStatus[] = ['healthy', 'degraded', 'failed', 'unknown']
const TEST_ID = 'cloud-load-balancers'

function flatten(data: ReturnType<typeof useCloud>['data']): LBRow[] {
  if (!data) return []
  const rows: LBRow[] = []
  for (const region of data.topology.regions ?? []) {
    for (const cluster of region.clusters ?? []) {
      for (const lb of cluster.loadBalancers ?? []) {
        rows.push({ id: lb.id, lb, cluster, region })
      }
    }
  }
  return rows
}

function compare(a: LBRow, b: LBRow, sort: SortState): number {
  const dir = sort.dir === 'asc' ? 1 : -1
  switch (sort.column) {
    case 'name':
      return dir * a.lb.name.localeCompare(b.lb.name)
    case 'region':
      return dir * a.region.providerRegion.localeCompare(b.region.providerRegion)
    case 'listeners':
      return dir * ((a.lb.listeners?.length ?? 0) - (b.lb.listeners?.length ?? 0))
    case 'targets':
      return dir * ((a.lb.targets?.length ?? 0) - (b.lb.targets?.length ?? 0))
    case 'status':
      return dir * a.lb.status.localeCompare(b.lb.status)
    default:
      return 0
  }
}

function formatListeners(lb: LoadBalancerSpec): string {
  if (!lb.listeners?.length) return '—'
  return lb.listeners.map((l) => `${l.protocol}:${l.port}`).join(', ')
}

// formatFrontDoor explains the real ingress datapath (Refs #3998): a real
// cloud LB vs an EIP whose ELB targets node:443/:80 — the cilium-envoy
// hostNetwork host port (§854 / #4765 / #4706), NOT a nodePort. The gateway
// is served DIRECT; there is no nodePort anywhere in the datapath.
export function formatFrontDoor(lb: LoadBalancerSpec): string {
  switch (lb.frontDoorKind) {
    case 'cloud-lb':
      return 'Cloud load balancer'
    case 'gateway-eip':
      return 'EIP → Cilium Gateway (hostPort node:443/:80)'
    default:
      return '—'
  }
}

export function LoadBalancersPage() {
  const { deploymentId, data, isLoading } = useCloud()
  const rows = useMemo(() => flatten(data), [data])

  const [statusFilter, setStatusFilter] = useState<TopologyStatus | ''>('')
  const list = useCloudListState<LBRow>({
    rows,
    matchSearch: (row, q) => {
      if (!q.trim()) return true
      const s = q.toLowerCase()
      return (
        row.lb.name.toLowerCase().includes(s) ||
        row.lb.id.toLowerCase().includes(s) ||
        row.lb.publicIP.toLowerCase().includes(s)
      )
    },
    matchExtra: (row) => !statusFilter || row.lb.status === statusFilter,
    comparator: compare,
    defaultSort: { column: 'name', dir: 'asc' },
  })

  const [openRow, setOpenRow] = useState<LBRow | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [editRow, setEditRow] = useState<LBRow | null>(null)
  const [deleteRow, setDeleteRow] = useState<LBRow | null>(null)

  const regionIds = useMemo(
    () => (data?.topology.regions ?? []).map((r) => r.id),
    [data],
  )

  return (
    <div data-testid={`${TEST_ID}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={TEST_ID}
        title="Load Balancers"
        tagline="Cloud-provisioned LBs fronting clusters; one row per LB across all regions."
        count={rows.length}
        deploymentId={deploymentId}
        newLabel="+ New load balancer"
        onNew={regionIds.length > 0 ? () => setAddOpen(true) : undefined}
      />

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]">
          Loading load balancers…
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          testId={`${TEST_ID}-empty`}
          title="No load balancers yet."
          body="Once Service-type=LoadBalancer is provisioned, balancers appear here."
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
                  <SortableTH testId={`${TEST_ID}-th-region`} column="region" label="Region" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-listeners`} column="listeners" label="Listeners" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-targets`} column="targets" label="Targets" state={list.sort} onChange={list.setSort} />
                  <SortableTH testId={`${TEST_ID}-th-status`} column="status" label="Status" state={list.sort} onChange={list.setSort} />
                  <th className="cloud-list-th" aria-label="Row actions" />
                </tr>
              </thead>
              <tbody>
                {list.visible.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="cloud-list-empty-row" data-testid={`${TEST_ID}-table-empty`}>
                      No load balancers match the current filters.
                    </td>
                  </tr>
                ) : (
                  list.visible.map((row) => {
                    const healthy = row.lb.targets?.filter((t) => t.status === 'healthy').length ?? 0
                    const total = row.lb.targets?.length ?? 0
                    return (
                      <tr
                        key={row.id}
                        className="cloud-list-row"
                        data-testid={`${TEST_ID}-row-${row.id}`}
                        onClick={() => setOpenRow(row)}
                      >
                        <td className="cloud-list-cell cloud-list-cell-name">{row.lb.name}</td>
                        <td className="cloud-list-cell">{row.region.providerRegion}</td>
                        <td className="cloud-list-cell cloud-list-cell-mono">{formatListeners(row.lb)}</td>
                        <td className="cloud-list-cell">{`${healthy}/${total}`}</td>
                        <td className="cloud-list-cell"><StatusPill status={row.lb.status} /></td>
                        <td className="cloud-list-cell">
                          <RowActionsMenu
                            testId={`${TEST_ID}-row-${row.id}`}
                            onEdit={() => setEditRow(row)}
                            onDelete={() => setDeleteRow(row)}
                          />
                        </td>
                      </tr>
                    )
                  })
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
        title={openRow ? `Load Balancer — ${openRow.lb.name}` : ''}
      >
        {openRow ? (
          <>
            <DetailDrawerActions
              testId={`${TEST_ID}-detail`}
              onEdit={() => {
                setEditRow(openRow)
                setOpenRow(null)
              }}
              onDelete={() => {
                setDeleteRow(openRow)
                setOpenRow(null)
              }}
            />
            <DetailRow label="Name" value={openRow.lb.name} />
            <DetailRow label="ID" value={openRow.lb.id} mono />
            <DetailRow label="Front door" value={formatFrontDoor(openRow.lb)} />
            <DetailRow label="Public IP" value={openRow.lb.publicIP} mono />
            <DetailRow label="Listeners" value={formatListeners(openRow.lb)} mono />
            <DetailRow
              label="Targets"
              value={`${openRow.lb.targets?.filter((t) => t.status === 'healthy').length ?? 0}/${openRow.lb.targets?.length ?? 0} healthy`}
            />
            <DetailRow label="Status" value={<StatusPill status={openRow.lb.status} />} />
            <DetailRow label="Region" value={openRow.region.providerRegion} />
            <DetailRow label="Provider" value={openRow.region.provider} />
            <DetailRow label="Parent cluster" value={openRow.cluster.name} />
          </>
        ) : null}
      </CloudListDetailDrawer>

      {regionIds.length > 0 && (
        <AddLBModal
          open={addOpen}
          deploymentId={deploymentId}
          regionId={regionIds[0]!}
          regionIdChoices={regionIds}
          onClose={() => setAddOpen(false)}
        />
      )}
      {editRow && (
        <EditLBModal
          open={!!editRow}
          deploymentId={deploymentId}
          lb={editRow.lb}
          onClose={() => setEditRow(null)}
        />
      )}
      {deleteRow && (
        <SimpleDeleteConfirm
          open={!!deleteRow}
          deploymentId={deploymentId}
          resource="loadbalancers"
          resourceId={deleteRow.lb.id}
          resourceLabel={deleteRow.lb.name}
          resourceKind="Load balancer"
          onClose={() => setDeleteRow(null)}
        />
      )}
    </div>
  )
}
