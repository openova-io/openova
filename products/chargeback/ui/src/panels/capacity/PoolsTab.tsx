import { useState } from 'react'
import type { CapacityClassDef, CapacityPoolView, CapacityResourceKind, CapacitySKUOptions, CapacityZoneView } from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { Badge, EmptyState } from '../../components/ui'
import { t } from '../../i18n'
import { bindingOf, formatAmount, formatDays, formatUtilisation, heatClass, kindOf, vectorSummary } from '../../lib/capacity'
import { toNumber } from '../../lib/num'
import { PoolDetail } from './PoolDetail'
import { ClassBadges, type Act } from './shared'

export interface PoolRow {
  region: string
  zone: CapacityZoneView
  pool: CapacityPoolView
}

/**
 * THE POOLS, ONE LINE EACH. Three pools are three lines, not three screens:
 * machines, the classes the pool enforces, what binds first, how full it is
 * and when to order. Everything else about a pool is behind opening its row.
 */
export function PoolsTab({
  rows,
  hasZones,
  kinds,
  classes,
  skus,
  canManage,
  act,
  onSaved,
  onAdd,
  onEdit,
  onDelete,
  onPlacements,
}: {
  rows: PoolRow[]
  hasZones: boolean
  kinds: CapacityResourceKind[]
  classes: CapacityClassDef[]
  skus: CapacitySKUOptions | null
  canManage: boolean
  act: Act
  onSaved: () => Promise<void>
  onAdd: () => void
  onEdit: (row: PoolRow) => void
  onDelete: (pool: CapacityPoolView) => void
  onPlacements: (pool: CapacityPoolView) => void
}) {
  const [open, setOpen] = useState<string | null>(null)

  const columns: Column<PoolRow>[] = [
    {
      key: 'name',
      header: t('capacity.pools.col.pool'),
      value: (r) => r.pool.name,
      render: (r) => (
        <>
          <button type="button" className="link" aria-expanded={open === r.pool.id} aria-label={open === r.pool.id ? t('capacity.pools.close', { name: r.pool.name }) : t('capacity.pools.open', { name: r.pool.name })} onClick={(e) => { e.stopPropagation(); setOpen(open === r.pool.id ? null : r.pool.id) }}>
            <span aria-hidden>{open === r.pool.id ? '▾' : '▸'}</span> <b>{r.pool.name}</b>
          </button>
          {r.pool.note ? <span className="sub">{r.pool.note}</span> : null}
        </>
      ),
    },
    {
      key: 'zone',
      header: t('capacity.form.zone'),
      value: (r) => `${r.region} / ${r.zone.code}`,
      render: (r) => (
        <>
          {r.region} / {r.zone.code}
          {r.pool.zone_unknown ? <span className="sub" title={t('capacity.pool.zoneUnknown')}>{t('capacity.pools.zoneUnknown')}</span> : null}
        </>
      ),
    },
    {
      key: 'machines',
      header: t('capacity.form.machines'),
      value: (r) => toNumber(r.pool.machines),
      numeric: true,
      render: (r) => (
        <>
          {formatAmount(r.pool.machines, 0)}
          <span className="sub">
            {vectorSummary(r.pool.resources, kinds)} {t('capacity.pool.perMachine')}
          </span>
        </>
      ),
    },
    { key: 'classes', header: t('capacity.pools.col.classes'), value: (r) => r.pool.classes.join(' '), sortable: false, render: (r) => <ClassBadges list={r.pool.classes} classes={classes} /> },
    {
      key: 'binds',
      header: t('capacity.pool.binds'),
      value: (r) => r.pool.binding_resource,
      render: (r) =>
        r.pool.binding_resource ? (
          <Badge status={kindOf(r.pool.binding_resource, kinds).label} kind={r.pool.status === 'critical' ? 'bad' : r.pool.status === 'warn' ? 'warn' : undefined} />
        ) : (
          <span className="muted">{t('capacity.pool.bindsNone')}</span>
        ),
    },
    {
      key: 'used',
      header: t('capacity.pools.col.used'),
      value: (r) => r.pool.utilisation_pct ?? -1,
      numeric: true,
      render: (r) => {
        const b = bindingOf(r.pool)
        return (
          <span className={heatClass(r.pool.status)} data-status={r.pool.status} style={{ padding: '0 6px', display: 'inline-block' }}>
            <b>{formatUtilisation(r.pool.utilisation_pct)}</b>
            {b && b.sized ? (
              <span className="sub">
                {formatAmount(b.sold_nominal)} / {formatAmount(b.sellable)} {b.unit}
              </span>
            ) : (
              <span className="sub">{t('capacity.legend.unset')}</span>
            )}
          </span>
        )
      },
    },
    {
      key: 'order',
      header: t('capacity.trend.orderBy'),
      value: (r) => r.pool.order_by_days ?? Number.MAX_SAFE_INTEGER,
      render: (r) =>
        r.pool.order_by_date === null ? (
          <span className="muted">{t('capacity.pool.orderNone')}</span>
        ) : r.pool.late ? (
          <b className="bad">{t('capacity.pool.orderLate', { when: formatDays(r.pool.order_by_days) })}</b>
        ) : (
          <b>{r.pool.order_by_date}</b>
        ),
    },
    {
      key: 'act',
      header: '',
      value: () => '',
      sortable: false,
      className: 'actions',
      render: (r) =>
        canManage ? (
          <span className="btn-row" onClick={(e) => e.stopPropagation()}>
            <button type="button" className="small" aria-label={t('capacity.pool.editNamed', { name: r.pool.name })} onClick={() => onEdit(r)}>
              {t('capacity.pool.edit')}
            </button>
            <button type="button" className="small danger" aria-label={t('capacity.pool.deleteNamed', { name: r.pool.name })} onClick={() => onDelete(r.pool)}>
              {t('capacity.pool.delete')}
            </button>
          </span>
        ) : null,
    },
  ]

  if (rows.length === 0) {
    return (
      <div className="card">
        <EmptyState title={t('capacity.pools.none')}>
          {hasZones ? t('capacity.empty.noPoolsBody') : t('capacity.pools.noZones')}
          {canManage && hasZones ? (
            <div style={{ marginTop: 10 }}>
              <button type="button" className="small primary" onClick={onAdd}>
                {t('capacity.pool.add')}
              </button>
            </div>
          ) : null}
        </EmptyState>
      </div>
    )
  }

  return (
    <div className="card pad-0">
      <DataTable
        label={t('capacity.tab.pools')}
        columns={columns}
        rows={rows}
        rowKey={(r) => r.pool.id}
        defaultSort={{ key: 'used', dir: 'desc' }}
        onRowClick={(r) => setOpen(open === r.pool.id ? null : r.pool.id)}
        selectedKey={open}
        csvName="capacity-pools"
        footNote={t('capacity.pools.foot')}
        expanded={(r) =>
          open === r.pool.id ? <PoolDetail pool={r.pool} kinds={kinds} classes={classes} skus={skus} canManage={canManage} act={act} onSaved={onSaved} onPlacements={() => onPlacements(r.pool)} /> : null
        }
      />
    </div>
  )
}
