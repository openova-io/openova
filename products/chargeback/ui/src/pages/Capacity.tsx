import { useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { api } from '../api/client'
import type {
  CapacityBasket,
  CapacityOverview,
  CapacityPlacementView,
  CapacityPoolView,
  CapacityRegion,
  CapacityRegionView,
  CapacityResourceKind,
  CapacityResourceView,
  CapacityShape,
  CapacityShapes,
  CapacityZoneView,
} from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, EmptyState, Field, KPI, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { t } from '../i18n'
import { can } from '../lib/access'
import {
  asOfLabel,
  basketQuery,
  classLabel,
  formatAmount,
  formatDays,
  formatRatio,
  formatUtilisation,
  heatClass,
  kindOf,
  parsePoolForm,
  parseShapeForm,
  reserveForMachines,
  shapeSummary,
  sortedResourceKeys,
  sparkPath,
  vectorSummary,
  zoneRows,
  type PoolForm,
} from '../lib/capacity'
import { toNumber } from '../lib/num'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Plan → Capacity (DESIGN.md §11, rewritten on founder direction 2026-09-13).
 *
 * The operator's picture of the hardware underneath. Per zone, a POOL is a
 * named set of identical machines: a machine count and a per-machine vector,
 * with a reserve and an overcommit ratio PER RESOURCE. What is running on it
 * is split by CLASS — guaranteed, burstable, spot — because guaranteed growth
 * is a hardware order and spot growth is a reclaim, and a blended line hides
 * which is which. What can still be sold is ONE basket headroom over a named
 * mix, with the BINDING RESOURCE named: a pool can be RAM-bound with a third
 * of its vCPU stranded, and that is the case procurement needs to see.
 *
 * Every figure comes from GET /capacity/overview; the page colours, formats
 * and edits. Reads need metering.read at the Sovereign; pools, shapes and
 * placements are edited with capacity.manage.
 *
 * HONEST EMPTY STATE, everywhere: a pool nobody has sized reads `unset`,
 * never `ok`; a basket with no sized resource prints its REASON, never a
 * number; a metered SKU no pool takes is listed BY NAME rather than summed
 * away, because usage counted against nothing otherwise reads as spare
 * capacity.
 */

const HEAT_LEGEND: ReadonlyArray<[cls: string, key: 'capacity.legend.ok' | 'capacity.legend.warn' | 'capacity.legend.critical' | 'capacity.legend.unset']> = [
  ['ok', 'capacity.legend.ok'],
  ['warn', 'capacity.legend.warn'],
  ['critical', 'capacity.legend.critical'],
  ['unset', 'capacity.legend.unset'],
]

type Act = ReturnType<typeof useAction>

type Dialog =
  | { kind: 'region'; region: CapacityRegionView }
  | { kind: 'zone'; zone: CapacityZoneView; region: string }
  | { kind: 'pool'; pool: CapacityPoolView }
  | null

export function Capacity() {
  const { me } = useSession()
  const canManage = can(me, 'capacity.manage')
  const overview = useQuery<CapacityOverview>('/capacity/overview')
  const shapes = useQuery<CapacityShapes>('/capacity/shapes')
  const act = useAction()
  const [region, setRegion] = useState<string>('all')
  const [confirm, setConfirm] = useState<Dialog>(null)
  const [editing, setEditing] = useState<{ zoneID: string; pool: CapacityPoolView | null } | null>(null)
  const [shapeSeed, setShapeSeed] = useState<string | null>(null)

  const ov = overview.data
  const kinds = ov?.resource_kinds ?? []
  // The page is organised BY ZONE, not by a flat list of pools: a pool
  // belongs to a zone, and a zone with none still needs somewhere to add one.
  const zones = useMemo(() => zoneRows(ov, region), [ov, region])
  // Every zone the Sovereign has, for the editor's zone picker. The region
  // filter narrows what is SHOWN; it never narrows where a pool may be added.
  const zoneOptions = useMemo(
    () =>
      (ov?.regions ?? []).flatMap((r) =>
        r.zones.map((z) => ({ id: z.id, label: `${r.code} / ${z.code}${z.is_default ? ` (${t('capacity.regions.defaultZone')})` : ''}` })),
      ),
    [ov],
  )
  const summary = ov?.summary
  const asOf = asOfLabel(ov?.as_of)

  const reload = async () => {
    await Promise.all([overview.reload(), shapes.reload()])
  }

  const regionOptions = [
    { value: 'all', label: t('capacity.filter.all', { count: ov?.regions.length ?? 0 }) },
    ...(ov?.regions ?? []).map((r) => ({ value: r.code, label: r.name ? `${r.code} · ${r.name}` : r.code })),
  ]

  return (
    <div className="stack">
      <PageHeader
        title={t('capacity.title')}
        sub={
          <>
            {t('capacity.sub')}
            {asOf ? <> · {t('capacity.asOf', { when: asOf })}</> : null}
          </>
        }
        actions={
          canManage && zoneOptions.length > 0 ? (
            <button className="small primary" onClick={() => setEditing({ zoneID: zones[0]?.zone.id ?? zoneOptions[0].id, pool: null })}>
              {t('capacity.pool.add')}
            </button>
          ) : null
        }
      />
      {overview.error ? <Notice kind="bad">{overview.error}</Notice> : null}
      {shapes.error ? <Notice kind="bad">{shapes.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {!canManage ? <Notice kind="info">{t('capacity.readOnly')}</Notice> : null}

      {/* One editor, opened from the page header, from a zone's own header,
          from a zone with no pools, or from a pool's Edit — always here, at
          the top, with the cursor already in it, so the control that opened
          it is never somewhere the reader has to go looking for. The key
          remounts it when it is pointed at a different pool or zone. */}
      {editing ? (
        <PoolEditor
          key={`${editing.pool?.id ?? 'new'}-${editing.zoneID}`}
          zoneID={editing.zoneID}
          zones={zoneOptions}
          pool={editing.pool}
          kinds={kinds}
          act={act}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await reload()
          }}
        />
      ) : null}

      <div className="kpis">
        <KPI
          label={t('capacity.kpi.pools')}
          value={summary?.pools ?? '…'}
          note={summary ? t('capacity.kpi.poolsNote', { sized: summary.pools_sized, total: summary.pools }) : undefined}
          tone={summary && summary.pools > 0 && summary.pools_sized === 0 ? 'warn' : undefined}
        />
        <KPI
          label={t('capacity.kpi.pressure')}
          value={summary?.pools_past_threshold ?? '…'}
          note={summary ? t('capacity.kpi.pressureNote', { critical: summary.pools_critical }) : undefined}
          tone={summary?.pools_critical ? 'bad' : summary?.pools_past_threshold ? 'warn' : undefined}
        />
        <KPI
          label={t('capacity.kpi.order')}
          value={summary?.pools_to_order ?? '…'}
          note={summary ? (summary.pools_to_order ? t('capacity.kpi.orderNote', { late: summary.pools_order_late }) : t('capacity.kpi.orderNoneNote')) : undefined}
          tone={summary?.pools_order_late ? 'bad' : undefined}
        />
        <KPI
          label={t('capacity.kpi.measured')}
          value={asOf || t('common.none')}
          note={ov ? (ov.as_of ? sourcesNote(ov) : t('capacity.kpi.noUsage')) : undefined}
        />
      </div>

      {overview.loading && !ov ? (
        <div className="card">
          <Skeleton lines={4} />
        </div>
      ) : null}

      {ov && ov.regions.length === 0 ? (
        <div className="card">
          <EmptyState title={t('capacity.empty.title')}>{t('capacity.empty.body')}</EmptyState>
          {canManage ? <AddRegionForm act={act} onDone={reload} /> : null}
        </div>
      ) : null}

      {ov && ov.unmapped_regions.length > 0 ? (
        <Notice kind="warn">
          <div className="stack tight">
            {ov.unmapped_regions.map((u) => (
              <div key={u.region} className="row between">
                <span>
                  {t('capacity.unmapped.region', {
                    region: u.region || '(no region)',
                    skus: u.skus,
                    resources: u.resources,
                    why: u.reason === 'no-zones' ? t('capacity.unmapped.noZones') : t('capacity.unmapped.noRegion'),
                  })}
                </span>
                {canManage && u.region && u.reason === 'no-region' ? (
                  <button
                    className="small"
                    disabled={act.busy}
                    onClick={() => void act.run(t('capacity.regions.regionAdded', { code: u.region }), () => api.post<CapacityRegion>('/capacity/regions', { code: u.region }), reload)}
                  >
                    {t('capacity.unmapped.addRegion', { region: u.region })}
                  </button>
                ) : null}
              </div>
            ))}
          </div>
        </Notice>
      ) : null}

      {ov && ov.regions.length > 0 ? (
        <>
          <div className="toolbar" role="region" aria-label="Capacity region filter">
            <div className="field">
              <label>{t('capacity.filter.region')}</label>
              <Segmented value={region} options={regionOptions} onChange={setRegion} ariaLabel="Region filter" />
            </div>
            <div className="grow" />
            <div className="heat-legend">
              {HEAT_LEGEND.map(([cls, key]) => (
                <span key={cls}>
                  <i className={cls} />
                  {t(key)}
                </span>
              ))}
            </div>
          </div>

          {zones.map(({ region: rc, zone }) => (
            <ZoneSection key={zone.id} regionCode={rc} zone={zone} canManage={canManage} onAddPool={() => setEditing({ zoneID: zone.id, pool: null })}>
              {zone.pools.map((pool) => (
                <PoolCard
                  key={pool.id}
                  regionCode={rc}
                  zone={zone}
                  pool={pool}
                  kinds={kinds}
                  classes={ov.classes}
                  canManage={canManage}
                  act={act}
                  onSaved={reload}
                  onEdit={() => setEditing({ zoneID: zone.id, pool })}
                  onDelete={() => setConfirm({ kind: 'pool', pool })}
                />
              ))}
            </ZoneSection>
          ))}

          {/* Everything a zone could not attribute, by name. */}
          {zones.some(({ zone }) => zone.unplaced_skus.length > 0) ? (
            <div className="card">
              <div className="card-head">
                <h2>{t('capacity.unplaced.title')}</h2>
              </div>
              <div className="stack tight">
                {zones
                  .flatMap(({ zone }) => zone.unplaced_skus.map((u) => ({ zone, u })))
                  .map(({ zone, u }) => (
                    <div key={`${zone.id}-${u.sku}-${u.reason}-${u.resource ?? ''}`} className="row between">
                      <span>
                        <code>{u.sku}</code> <span className="muted">· {zone.code} · {formatAmount(u.units)}</span>
                        <span className="sub">{u.reason === 'resource-unplaced' ? t('capacity.unplaced.resourceUnplaced', { resource: kindOf(u.resource ?? '', kinds).label }) : t('capacity.unplaced.noPlacement')}</span>
                      </span>
                    </div>
                  ))}
              </div>
            </div>
          ) : null}

          <RegionsCard regions={ov.regions} canManage={canManage} act={act} onDone={reload} onDelete={setConfirm} onAddPool={(zoneID) => setEditing({ zoneID, pool: null })} />
        </>
      ) : null}

      {ov && ov.unshaped_skus.length > 0 ? (
        <div className="card">
          <div className="card-head">
            <h2>{t('capacity.unshaped.title')}</h2>
            <span className="hint">{t('capacity.unshaped.sub')}</span>
          </div>
          <DataTable
            label={t('capacity.unshaped.title')}
            columns={[
              { key: 'sku', header: t('capacity.shapes.col.sku'), value: (u) => u.sku, render: (u) => <code>{u.sku}</code> },
              { key: 'qty', header: t('capacity.placements.col.units'), value: (u) => toNumber(u.quantity), numeric: true, render: (u) => `${formatAmount(u.quantity)} ${u.unit}` },
              { key: 'res', header: t('capacity.placements.col.units'), value: (u) => u.resources, numeric: true },
              { key: 'regions', header: t('common.region'), value: (u) => u.regions.join(', ') },
              {
                key: 'act',
                header: '',
                value: () => '',
                sortable: false,
                className: 'actions',
                render: (u) =>
                  canManage ? (
                    <button className="small primary" onClick={() => setShapeSeed(u.sku)}>
                      {t('capacity.unshaped.add')}
                    </button>
                  ) : null,
              },
            ]}
            rows={ov.unshaped_skus}
            rowKey={(u) => u.sku}
            defaultSort={{ key: 'qty', dir: 'desc' }}
          />
        </div>
      ) : null}

      <ShapesCard doc={shapes.data} loading={shapes.loading} canManage={canManage} act={act} onSaved={reload} seedSku={shapeSeed} onSeedConsumed={() => setShapeSeed(null)} />

      {confirm?.kind === 'pool' ? (
        <Confirm
          title={t('capacity.form.deleteTitle', { name: confirm.pool.name })}
          body={t('capacity.form.deleteBody')}
          confirmLabel={t('capacity.pool.delete')}
          danger
          busy={act.busy}
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            const ok = await act.run(t('capacity.form.deleted', { name: confirm.pool.name }), () => api.del(`/capacity/pools/${confirm.pool.id}`), reload)
            if (ok) setConfirm(null)
          }}
        />
      ) : null}
      {confirm?.kind === 'region' ? (
        <Confirm
          title={t('capacity.regions.deleteRegionTitle', { code: confirm.region.code })}
          body={t('capacity.regions.deleteRegionBody', { code: confirm.region.code })}
          confirmLabel={t('capacity.pool.delete')}
          danger
          busy={act.busy}
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            const ok = await act.run(t('capacity.regions.regionDeleted', { code: confirm.region.code }), () => api.del(`/capacity/regions/${confirm.region.id}`), reload)
            if (ok) setConfirm(null)
          }}
        />
      ) : null}
      {confirm?.kind === 'zone' ? (
        <Confirm
          title={t('capacity.regions.deleteZoneTitle', { code: confirm.zone.code })}
          body={`${t('capacity.regions.deleteZoneBody')}${confirm.zone.is_default ? ` ${t('capacity.regions.deleteZoneDefault')}` : ''}`}
          confirmLabel={t('capacity.pool.delete')}
          danger
          busy={act.busy}
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            const ok = await act.run(t('capacity.regions.zoneDeleted', { code: confirm.zone.code }), () => api.del(`/capacity/zones/${confirm.zone.id}`), reload)
            if (ok) setConfirm(null)
          }}
        />
      ) : null}
    </div>
  )
}

/**
 * The label a class shows under. It comes from the CATALOGUE, keyed by the
 * class, so a second locale can name the three classes; the server's own
 * label is the fallback for a class this console does not know.
 */
function classText(key: string, classes: CapacityOverview['classes']): string {
  switch (key) {
    case 'guaranteed':
      return t('capacity.class.guaranteed')
    case 'burstable':
      return t('capacity.class.burstable')
    case 'spot':
      return t('capacity.class.spot')
    default:
      return classLabel(key, classes)
  }
}

function sourcesNote(ov: CapacityOverview): string {
  const base = ov.sources === 1 ? t('capacity.kpi.sourcesOne') : t('capacity.kpi.sources', { count: ov.sources })
  return ov.lagging_sources ? `${base} · ${t('capacity.kpi.lagging', { count: ov.lagging_sources })}` : base
}

/**
 * One zone, and its pools.
 *
 * ADDING A POOL LIVES WHERE THE POOLS ARE. It used to live only at the foot
 * of the page, inside the regions-and-zones administration block: the founder,
 * who had commissioned the feature and knew it existed, still asked "where is
 * the add pool". A control nobody can find is a control nobody has. So the
 * zone's own header offers it, a zone with no pools offers it in the space
 * where its pools would be, and the page header offers it too — all three
 * open the SAME editor, and the zones block below still works exactly as it
 * did. One dialog, four ways in, no second way of creating a pool.
 */
function ZoneSection({
  regionCode,
  zone,
  canManage,
  onAddPool,
  children,
}: {
  regionCode: string
  zone: CapacityZoneView
  canManage: boolean
  onAddPool: () => void
  children: ReactNode
}) {
  const count = zone.pools.length
  return (
    <>
      <div className="toolbar zone-head" role="region" aria-label={`Zone ${zone.code}`}>
        <span>
          <b>
            {regionCode} / {zone.code}
          </b>
          {zone.is_default ? (
            <>
              {' '}
              <Badge status="default" kind="info" />
            </>
          ) : null}
          <span className="sub">{count === 0 ? t('capacity.zone.poolsNone') : count === 1 ? t('capacity.zone.poolsOne') : t('capacity.zone.pools', { count })}</span>
        </span>
        <div className="grow" />
        {canManage ? (
          <button className="small primary" onClick={onAddPool}>
            {t('capacity.pool.add')}
          </button>
        ) : null}
      </div>
      {count === 0 ? (
        <div className="card">
          <EmptyState title={t('capacity.empty.noPools')}>
            {t('capacity.empty.noPoolsBody')}
            {canManage ? (
              <div style={{ marginTop: 10 }}>
                <button className="small primary" onClick={onAddPool}>
                  {t('capacity.pool.add')}
                </button>
              </div>
            ) : null}
          </EmptyState>
        </div>
      ) : (
        children
      )}
    </>
  )
}

/** One pool: the vector, the class split per resource, the walls, the basket. */
function PoolCard({
  regionCode,
  zone,
  pool,
  kinds,
  classes,
  canManage,
  act,
  onSaved,
  onEdit,
  onDelete,
}: {
  regionCode: string
  zone: CapacityZoneView
  pool: CapacityPoolView
  kinds: CapacityResourceKind[]
  classes: CapacityOverview['classes']
  canManage: boolean
  act: Act
  onSaved: () => Promise<void>
  onEdit: () => void
  onDelete: () => void
}) {
  const machines = toNumber(pool.machines)
  const binding = pool.binding_resource ? kindOf(pool.binding_resource, kinds).label : ''
  return (
    <div className="card" data-status={pool.status}>
      <div className="card-head">
        <h2>
          <span className={heatClass(pool.status)} style={{ padding: '0 6px', marginRight: 6 }} data-status={pool.status}>
            {formatUtilisation(pool.utilisation_pct)}
          </span>
          {pool.name}
        </h2>
        <span className="hint">
          {regionCode} / {zone.code}
          {zone.is_default ? (
            <>
              {' '}
              <Badge status="default" kind="info" />
            </>
          ) : null}{' '}
          · {machines === 1 ? t('capacity.pool.machinesOne') : t('capacity.pool.machines', { count: machines })} · {vectorSummary(pool.resources, kinds)} {t('capacity.pool.perMachine')}
          {pool.lead_time_days ? <> · {t('capacity.pool.leadTime', { days: pool.lead_time_days })}</> : null}
          {pool.note ? <> · {pool.note}</> : null}
        </span>
        {canManage ? (
          <span className="btn-row">
            <button className="small" onClick={onEdit}>
              {t('capacity.pool.edit')}
            </button>
            <button className="small danger" onClick={onDelete}>
              {t('capacity.pool.delete')}
            </button>
          </span>
        ) : null}
      </div>

      <div className="row between" style={{ padding: '0 16px 8px' }}>
        <span>
          <b>{t('capacity.pool.binds')}:</b> {binding ? <Badge status={binding} kind={pool.status === 'critical' ? 'bad' : pool.status === 'warn' ? 'warn' : undefined} /> : <span className="muted">{t('capacity.pool.bindsNone')}</span>}
          {pool.zone_unknown ? <span className="sub">{t('capacity.pool.zoneUnknown')}</span> : null}
        </span>
        <span>
          {pool.order_by_date === null ? (
            <span className="muted">{t('capacity.pool.orderNone')}</span>
          ) : pool.late ? (
            <b className="bad">{t('capacity.pool.orderLate', { when: formatDays(pool.order_by_days) })}</b>
          ) : (
            <b>{t('capacity.pool.orderBy', { date: pool.order_by_date })}</b>
          )}
        </span>
      </div>

      <div className="table-wrap">
        <table className="heatmap" aria-label={`Capacity of ${pool.name}`}>
          <thead>
            <tr>
              <th>{t('capacity.form.resource')}</th>
              <th>{t('capacity.res.usable')}</th>
              <th>{t('capacity.res.ratio')}</th>
              <th>{t('capacity.res.sellable')}</th>
              <th>{t('capacity.class.split')}</th>
              <th>{t('capacity.res.left')}</th>
              <th title={t('capacity.trend.sub')}>{t('capacity.trend.title')}</th>
              <th>{t('capacity.trend.orderBy')}</th>
            </tr>
          </thead>
          <tbody>
            {pool.resources_view.map((r) => (
              <ResourceRow key={r.resource} pool={pool} r={r} classes={classes} />
            ))}
          </tbody>
        </table>
      </div>
      <div className="table-foot">
        <span>{t('capacity.res.formula')}</span>
      </div>

      <BasketPanel pool={pool} kinds={kinds} />
      <PlacementsPanel pool={pool} kinds={kinds} classes={classes} canManage={canManage} act={act} onSaved={onSaved} />
    </div>
  )
}

/** One resource of a pool, with its class split, its trend and its order-by. */
function ResourceRow({ pool, r, classes }: { pool: CapacityPoolView; r: CapacityResourceView; classes: CapacityOverview['classes'] }) {
  const guaranteed = r.series.find((s) => s.class === 'guaranteed')
  const spark = guaranteed ? sparkPath(guaranteed.days.map((d) => toNumber(d.consumed))) : ''
  return (
    <tr data-resource={r.resource} data-status={r.status}>
      <td>
        <b>{r.label}</b>
        <span className="sub">
          {r.resource === pool.binding_resource ? `${t('capacity.pool.binds')} · ` : ''}
          {formatAmount(r.per_machine)} {r.unit} {t('capacity.pool.perMachine')}
          {toNumber(r.reserve) > 0 ? ` · ${t('capacity.res.reserve')} ${formatAmount(r.reserve)}` : ''}
        </span>
      </td>
      <td className={heatClass(r.status)} data-status={r.status}>
        {r.sized ? (
          <>
            <b>{formatAmount(r.usable)}</b> {r.unit}
            <span className="sub">
              {t('capacity.res.raw')} {formatAmount(r.raw)}
            </span>
          </>
        ) : (
          <span className="muted">{t('capacity.res.unsized')}</span>
        )}
      </td>
      <td>{formatRatio(r.overcommit_ratio)}</td>
      <td>
        {r.sized ? (
          <>
            {formatAmount(r.sellable)}
            <span className="sub">
              {t('capacity.res.sold')} {formatAmount(r.sold_nominal)}
              {r.overcommitted ? ` · ${t('capacity.res.over', { amount: formatAmount(r.over), unit: r.unit })}` : ''}
            </span>
          </>
        ) : (
          <span className="muted">{t('common.none')}</span>
        )}
      </td>
      <td>
        <span className="stack tight">
          {classes.map((c) => {
            const v = c.class === 'guaranteed' ? r.guaranteed : c.class === 'burstable' ? r.burstable : r.spot
            if (toNumber(v) <= 0) return null
            return (
              <span key={c.class} title={c.note}>
                {classText(c.class, classes)} {formatAmount(v)}
              </span>
            )
          })}
          {toNumber(r.spot_reclaim) > 0 ? (
            <span className="bad" title={t('capacity.class.spotReclaimNote')}>
              {t('capacity.class.spotReclaim', { amount: formatAmount(r.spot_reclaim), unit: r.unit })}
            </span>
          ) : null}
        </span>
      </td>
      <td>
        {r.sized ? (
          <>
            <b className={toNumber(r.remaining) === 0 ? 'bad' : ''}>{formatAmount(r.remaining)}</b> {r.unit}
            {r.stranded ? (
              <span className="sub bad">{t('capacity.res.stranded', { amount: formatAmount(r.physical_free), unit: r.unit, binding: kindOf(pool.binding_resource).label })}</span>
            ) : null}
          </>
        ) : (
          <span className="muted">{t('common.none')}</span>
        )}
      </td>
      <td>
        {spark ? (
          <svg viewBox="0 0 120 28" width={120} height={28} aria-hidden className="spark">
            <path d={spark} fill="none" stroke="currentColor" strokeWidth="1.5" />
          </svg>
        ) : null}
        {r.series.length === 0 ? (
          <span className="sub">{t('capacity.trend.unknown')}</span>
        ) : (
          r.series.map((s) => (
            <span className="sub" key={s.class}>
              {classText(s.class, classes)}{' '}
              {s.growth_per_day === null || s.growth_per_day === undefined
                ? t('capacity.trend.unknown')
                : s.growth_per_day === 0
                  ? t('capacity.trend.flat')
                  : t('capacity.trend.growth', { amount: formatAmount(s.growth_per_day, 2), unit: r.unit })}
            </span>
          ))
        )}
      </td>
      <td>
        {r.order_by_days === null ? (
          <span className="muted">{t('capacity.trend.noWall')}</span>
        ) : (
          <>
            <b className={r.late ? 'bad' : ''}>{r.order_by_date}</b>
            <span className="sub" title={t('capacity.trend.orderByNote')}>
              {formatDays(r.order_by_days)}
            </span>
            <span className="sub" title={r.order_by_wall === 'hard' ? t('capacity.trend.hardWallNote') : t('capacity.trend.softWallNote')}>
              {r.order_by_wall === 'hard' ? t('capacity.trend.hardWall') : t('capacity.trend.softWall')} {r.order_by_wall === 'hard' ? r.hard_wall_date : r.soft_wall_date}
            </span>
          </>
        )}
      </td>
    </tr>
  )
}

/** How many more of a mix fit, and the mix itself. */
function BasketPanel({ pool, kinds }: { pool: CapacityPoolView; kinds: CapacityResourceKind[] }) {
  const [mix, setMix] = useState<string>('')
  const [applied, setApplied] = useState<string>('')
  const named = useQuery<{ basket: CapacityBasket }>(applied ? `/capacity/pools/${pool.id}/headroom?basket=${encodeURIComponent(applied)}` : null, [applied])
  const basket = applied && named.data ? named.data.basket : pool.basket
  const bindingLabel = basket.binding_resource ? kindOf(basket.binding_resource, kinds).label : ''

  return (
    <div className="stack tight" style={{ padding: '10px 16px 14px', borderTop: '1px solid var(--line)' }}>
      <div className="row between">
        <span>
          <b>{t('capacity.basket.title')}</b>
          <span className="sub">{t('capacity.basket.sub')}</span>
        </span>
        <span>
          {basket.units === null ? (
            <span className="muted" data-basket="unmeasured">
              <b>{t('capacity.basket.none')}</b> — {basket.reason}
            </span>
          ) : (
            <b data-basket="units">
              {t('capacity.basket.answer', { count: basket.units })}
              {bindingLabel ? <span className="sub">{t('capacity.basket.binds', { resource: bindingLabel })}</span> : null}
            </b>
          )}
        </span>
      </div>
      {basket.unshaped_skus.length > 0 ? <Notice kind="warn">{t('capacity.basket.unshaped', { skus: basket.unshaped_skus.join(', ') })}</Notice> : null}
      <div className="row between">
        <span className="muted small">
          {applied ? applied : `${t('capacity.basket.current')}: ${(basket.items ?? []).map((i) => `${formatAmount(i.units)} × ${i.sku}`).join(' · ') || t('common.none')}`}
        </span>
      </div>
      <form
        className="inline"
        aria-label={`Change the mix on ${pool.name}`}
        onSubmit={(e: FormEvent) => {
          e.preventDefault()
          setApplied(mix.trim())
        }}
      >
        <Field label={t('capacity.basket.edit')} help={t('capacity.basket.help')}>
          <input value={mix} onChange={(e) => setMix(e.target.value)} placeholder={basketQuery((pool.basket.items ?? []).map((i) => ({ sku: i.sku, units: i.units })))} />
        </Field>
        <button type="submit" className="small">
          {t('capacity.basket.apply')}
        </button>
        {applied ? (
          <button
            type="button"
            className="small"
            onClick={() => {
              setApplied('')
              setMix('')
            }}
          >
            {t('capacity.basket.reset')}
          </button>
        ) : null}
      </form>
      {basket.resources.length > 0 ? (
        <div className="table-wrap">
          <table aria-label={`What one mix costs ${pool.name}`}>
            <thead>
              <tr>
                <th>{t('capacity.form.resource')}</th>
                <th>{t('capacity.basket.perBasket')}</th>
                <th>{t('capacity.res.left')}</th>
                <th>{t('capacity.basket.title')}</th>
              </tr>
            </thead>
            <tbody>
              {(basket.resources ?? []).map((br) => (
                <tr key={br.resource}>
                  <td>{br.label}</td>
                  <td>
                    {formatAmount(br.per_basket)} {br.unit}
                  </td>
                  <td>
                    {formatAmount(br.remaining)} {br.unit}
                  </td>
                  <td>{br.units === null ? <span className="muted">{t('capacity.res.unsized')}</span> : formatAmount(br.units, 0)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  )
}

/** What sells out of a pool, and the form that places one more. */
function PlacementsPanel({
  pool,
  kinds,
  classes,
  canManage,
  act,
  onSaved,
}: {
  pool: CapacityPoolView
  kinds: CapacityResourceKind[]
  classes: CapacityOverview['classes']
  canManage: boolean
  act: Act
  onSaved: () => Promise<void>
}) {
  const [sku, setSku] = useState('')
  const [cls, setCls] = useState<string>('guaranteed')
  const columns: Column<CapacityPlacementView>[] = [
    { key: 'sku', header: t('capacity.placements.col.sku'), value: (p) => p.sku, render: (p) => <code>{p.sku}</code> },
    { key: 'class', header: t('capacity.placements.col.class'), value: (p) => p.class, render: (p) => <Badge status={classText(p.class, classes)} kind={p.class === 'guaranteed' ? 'ok' : p.class === 'spot' ? 'info' : 'warn'} /> },
    {
      key: 'shape',
      header: t('capacity.placements.col.shape'),
      value: (p) => shapeSummary(p.shape, kinds),
      render: (p) => (
        <>
          {shapeSummary(p.shape, kinds)}
          <span className="sub">{p.shape_source === 'derived' ? t('capacity.shapes.derived') : p.shape_source}</span>
        </>
      ),
    },
    { key: 'units', header: t('capacity.placements.col.units'), value: (p) => toNumber(p.units), numeric: true, render: (p) => (toNumber(p.units) ? formatAmount(p.units) : <span className="muted">0</span>) },
    {
      key: 'act',
      header: '',
      value: () => '',
      sortable: false,
      className: 'actions',
      render: (p) =>
        canManage ? (
          <button
            className="link small danger"
            disabled={act.busy}
            onClick={() => void act.run(t('capacity.place.removed', { sku: p.sku, pool: pool.name }), () => api.put('/capacity/placements', { pool_id: pool.id, sku: p.sku, class: null }), onSaved)}
          >
            {t('capacity.placements.remove')}
          </button>
        ) : null,
    },
  ]
  return (
    <>
      <div className="card-head" style={{ padding: '12px 16px 0', borderTop: '1px solid var(--line)' }}>
        <h2>{t('capacity.placements.title')}</h2>
        <span className="hint">{t('capacity.placements.sub')}</span>
      </div>
      <DataTable
        label={`${t('capacity.placements.title')} — ${pool.name}`}
        columns={columns}
        rows={pool.placements}
        rowKey={(p) => p.sku}
        defaultSort={{ key: 'units', dir: 'desc' }}
        pageSize={10}
        emptyTitle={t('capacity.placements.none')}
        emptyBody={t('capacity.placements.noneBody')}
      />
      {canManage ? (
        <form
          className="inline"
          style={{ padding: '8px 16px 14px' }}
          aria-label={t('capacity.place.title', { pool: pool.name })}
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            if (!sku.trim()) {
              act.setError(t('capacity.place.chooseSku'))
              return
            }
            void act.run(t('capacity.place.saved', { sku: sku.trim(), pool: pool.name, class: classText(cls, classes) }), () => api.put('/capacity/placements', { pool_id: pool.id, sku: sku.trim(), class: cls }), async () => {
              setSku('')
              await onSaved()
            })
          }}
        >
          <Field label={t('capacity.place.sku')}>
            <input value={sku} onChange={(e) => setSku(e.target.value)} placeholder="ecs.m7n.2xlarge.8" />
          </Field>
          <Field label={t('capacity.place.class')}>
            <select value={cls} onChange={(e) => setCls(e.target.value)}>
              {classes.map((c) => (
                <option key={c.class} value={c.class} title={c.note}>
                  {c.label}
                </option>
              ))}
            </select>
          </Field>
          <button type="submit" className="small primary" disabled={act.busy}>
            {t('capacity.place.save')}
          </button>
        </form>
      ) : null}
    </>
  )
}

const emptyResource = { resource: '', per_machine: '', reserve: '', overcommit_ratio: '1' }

/**
 * The pool editor: a machine count and a per-machine vector.
 *
 * THE ONLY WAY A POOL IS CREATED OR CHANGED, from every entry point. When it
 * is creating, it asks which zone — the page header's button cannot know, and
 * making the reader go and find the right zone's button first is the defect
 * this is fixing. When it is editing, the zone is the pool's own and is shown
 * rather than offered: moving a pool between zones is a different operation
 * from re-sizing one, and PUT /capacity/pools/{id} does not do it.
 */
function PoolEditor({
  zoneID,
  zones,
  pool,
  kinds,
  act,
  onClose,
  onSaved,
}: {
  zoneID: string
  zones: Array<{ id: string; label: string }>
  pool: CapacityPoolView | null
  kinds: CapacityResourceKind[]
  act: Act
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [zone, setZone] = useState(zoneID)
  const [form, setForm] = useState<PoolForm>(() => ({
    name: pool?.name ?? '',
    machines: pool ? String(toNumber(pool.machines)) : '',
    lead_time_days: String(pool?.lead_time_days ?? 0),
    note: pool?.note ?? '',
    resources: pool?.resources.length
      ? pool.resources.map((r) => ({ resource: r.resource, per_machine: String(toNumber(r.per_machine)), reserve: String(toNumber(r.reserve)), overcommit_ratio: String(toNumber(r.overcommit_ratio)) }))
      : [{ ...emptyResource }],
  }))
  const [err, setErr] = useState('')

  const setRes = (i: number, patch: Partial<PoolForm['resources'][number]>) => {
    const next = form.resources.map((r, j) => (i === j ? { ...r, ...patch } : r))
    setForm({ ...form, resources: next })
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const parsed = parsePoolForm(form)
    if (!parsed.body) {
      setErr(parsed.error)
      return
    }
    setErr('')
    const label = pool ? t('capacity.form.saved', { name: parsed.body.name }) : t('capacity.form.created', { name: parsed.body.name })
    const ok = await act.run(label, () => (pool ? api.put(`/capacity/pools/${pool.id}`, parsed.body) : api.post(`/capacity/zones/${zone}/pools`, parsed.body)), onSaved)
    if (!ok) setErr('')
  }

  return (
    <div className="card" role="dialog" aria-label={pool ? t('capacity.pool.edit') : t('capacity.pool.add')}>
      <div className="card-head">
        <h2>{pool ? t('capacity.pool.edit') : t('capacity.pool.add')}</h2>
      </div>
      <form onSubmit={submit} className="stack tight" style={{ padding: '0 16px 16px' }}>
        {err ? <Notice kind="bad">{err}</Notice> : null}
        <div className="inline">
          {pool ? null : (
            <Field label={t('capacity.form.zone')} help={t('capacity.form.zoneHelp')}>
              <select value={zone} onChange={(e) => setZone(e.target.value)}>
                {zones.map((z) => (
                  <option key={z.id} value={z.id}>
                    {z.label}
                  </option>
                ))}
              </select>
            </Field>
          )}
          <Field label={t('capacity.form.poolName')} help={t('capacity.form.poolNameHelp')}>
            {/* The editor is at the top of the page; the focus is what tells
                a reader who clicked "Add a pool" further down that it opened. */}
            <input autoFocus value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="m7n-a" />
          </Field>
          <Field label={t('capacity.form.machines')} help={t('capacity.form.machinesHelp')}>
            <input value={form.machines} onChange={(e) => setForm({ ...form, machines: e.target.value })} placeholder="10" />
          </Field>
          <Field label={t('capacity.form.leadTime')} help={t('capacity.form.leadTimeHelp')}>
            <input value={form.lead_time_days} onChange={(e) => setForm({ ...form, lead_time_days: e.target.value })} placeholder="45" />
          </Field>
          <Field label={t('capacity.form.note')} help={t('capacity.form.noteHelp')}>
            <input value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />
          </Field>
        </div>
        <div className="table-wrap">
          <table aria-label={t('capacity.form.addResource')}>
            <thead>
              <tr>
                <th>{t('capacity.form.resource')}</th>
                <th>{t('capacity.form.perMachine')}</th>
                <th>{t('capacity.form.reserve')}</th>
                <th>{t('capacity.form.ratio')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {form.resources.map((r, i) => (
                <tr key={i}>
                  <td>
                    <input aria-label={`${t('capacity.form.resource')} ${i + 1}`} list="capacity-resource-kinds" value={r.resource} onChange={(e) => setRes(i, { resource: e.target.value })} placeholder="vcpu" />
                  </td>
                  <td>
                    <input aria-label={`${t('capacity.form.perMachine')} ${i + 1}`} value={r.per_machine} onChange={(e) => setRes(i, { per_machine: e.target.value })} placeholder="64" />
                  </td>
                  <td>
                    <input aria-label={`${t('capacity.form.reserve')} ${i + 1}`} value={r.reserve} onChange={(e) => setRes(i, { reserve: e.target.value })} placeholder="0" />
                    <button type="button" className="link small" title={t('capacity.form.nPlusOneHelp')} onClick={() => setRes(i, { reserve: reserveForMachines(r.per_machine, 1) })}>
                      {t('capacity.form.nPlusOne')}
                    </button>
                  </td>
                  <td>
                    <input aria-label={`${t('capacity.form.ratio')} ${i + 1}`} value={r.overcommit_ratio} onChange={(e) => setRes(i, { overcommit_ratio: e.target.value })} placeholder="1" />
                  </td>
                  <td className="actions">
                    {form.resources.length > 1 ? (
                      <button type="button" className="link small danger" onClick={() => setForm({ ...form, resources: form.resources.filter((_, j) => j !== i) })}>
                        {t('capacity.form.removeResource')}
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <datalist id="capacity-resource-kinds">
          {kinds.map((k) => (
            <option key={k.resource} value={k.resource}>
              {k.label}
            </option>
          ))}
        </datalist>
        <span className="muted small">{t('capacity.form.ratioHelp')} · {t('capacity.form.reserveHelp')}</span>
        <div className="row end">
          <button type="button" className="small" onClick={() => setForm({ ...form, resources: [...form.resources, { ...emptyResource }] })}>
            {t('capacity.form.addResource')}
          </button>
          <button type="button" className="small" onClick={onClose} disabled={act.busy}>
            {t('common.cancel')}
          </button>
          <button type="submit" className="small primary" disabled={act.busy}>
            {t('capacity.form.save')}
          </button>
        </div>
      </form>
    </div>
  )
}

/** Regions and zones: the list, and the forms to add or delete them. */
function RegionsCard({
  regions,
  canManage,
  act,
  onDone,
  onDelete,
  onAddPool,
}: {
  regions: CapacityRegionView[]
  canManage: boolean
  act: Act
  onDone: () => Promise<void>
  onDelete: (c: Dialog) => void
  onAddPool: (zoneID: string) => void
}) {
  const [addingZone, setAddingZone] = useState<string | null>(null)
  return (
    <div className="card">
      <div className="card-head">
        <h2>{t('capacity.regions.title')}</h2>
        <span className="hint">{t('capacity.regions.sub')}</span>
      </div>
      <div className="stack tight">
        {regions.map((r) => (
          <div key={r.id} style={{ borderBottom: '1px solid var(--line)', paddingBottom: 8 }}>
            <div className="row between">
              <span>
                <b>{r.code}</b>
                {r.name ? <span className="muted"> · {r.name}</span> : null} <Badge status={r.cloud_source_kind} />
                <span className="sub">{r.zones.length ? r.zones.map((z) => z.code + (z.is_default ? ` (${t('capacity.regions.defaultZone')})` : '')).join(', ') : t('capacity.regions.zonesNone')}</span>
              </span>
              {canManage ? (
                <span className="btn-row">
                  <button className="small" onClick={() => setAddingZone(addingZone === r.id ? null : r.id)}>
                    {t('capacity.regions.addZone')}
                  </button>
                  <button className="small danger" onClick={() => onDelete({ kind: 'region', region: r })}>
                    {t('capacity.pool.delete')}
                  </button>
                </span>
              ) : null}
            </div>
            {canManage && r.zones.length ? (
              <div className="row" style={{ marginTop: 4 }}>
                {r.zones.map((z) => (
                  <span key={z.id}>
                    <button className="link small" onClick={() => onAddPool(z.id)}>
                      {t('capacity.pool.add')} — {z.code}
                    </button>
                    <button className="link small danger" onClick={() => onDelete({ kind: 'zone', zone: z, region: r.code })}>
                      {t('capacity.pool.delete').toLowerCase()} {z.code}
                    </button>
                  </span>
                ))}
              </div>
            ) : null}
            {addingZone === r.id ? (
              <AddZoneForm
                region={r}
                act={act}
                onDone={async () => {
                  setAddingZone(null)
                  await onDone()
                }}
              />
            ) : null}
          </div>
        ))}
      </div>
      {canManage ? <AddRegionForm act={act} onDone={onDone} /> : null}
    </div>
  )
}

function AddRegionForm({ act, onDone }: { act: Act; onDone: () => Promise<void> }) {
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  return (
    <form
      className="inline"
      style={{ marginTop: 10 }}
      aria-label={t('capacity.regions.addRegion')}
      onSubmit={(e: FormEvent) => {
        e.preventDefault()
        if (!code.trim()) {
          act.setError(t('capacity.regions.regionCodeHelp'))
          return
        }
        void act.run(t('capacity.regions.regionAdded', { code: code.trim().toLowerCase() }), () => api.post<CapacityRegion>('/capacity/regions', { code: code.trim(), name: name.trim() }), async () => {
          setCode('')
          setName('')
          await onDone()
        })
      }}
    >
      <Field label={t('capacity.regions.regionCode')} help={t('capacity.regions.regionCodeHelp')}>
        <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="me-east-215" />
      </Field>
      <Field label={t('capacity.regions.name')}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Muscat" />
      </Field>
      <button type="submit" className="small primary" disabled={act.busy}>
        {t('capacity.regions.addRegion')}
      </button>
    </form>
  )
}

function AddZoneForm({ region, act, onDone }: { region: CapacityRegionView; act: Act; onDone: () => Promise<void> }) {
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [makeDefault, setMakeDefault] = useState(false)
  return (
    <form
      className="inline"
      style={{ marginTop: 8 }}
      aria-label={`${t('capacity.regions.addZone')} ${region.code}`}
      onSubmit={(e: FormEvent) => {
        e.preventDefault()
        if (!code.trim()) {
          act.setError(t('capacity.regions.zoneCode'))
          return
        }
        void act.run(t('capacity.regions.zoneAdded', { code: code.trim().toLowerCase(), region: region.code }), () => api.post(`/capacity/regions/${region.id}/zones`, { code: code.trim(), name: name.trim(), default: makeDefault }), onDone)
      }}
    >
      <Field label={t('capacity.regions.zoneCode')} help={`${region.code}a`}>
        <input value={code} onChange={(e) => setCode(e.target.value)} placeholder={`${region.code}a`} />
      </Field>
      <Field label={t('capacity.regions.name')}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="AZ 1" />
      </Field>
      <label className="check" style={{ marginBottom: 10 }}>
        <input type="checkbox" checked={makeDefault} onChange={(e) => setMakeDefault(e.target.checked)} /> {t('capacity.regions.defaultZone')}
      </label>
      <button type="submit" className="small primary" disabled={act.busy}>
        {t('capacity.regions.addZone')}
      </button>
    </form>
  )
}

/** The shapes editor: every stored shape, one row per SKU, with inline edit. */
function ShapesCard({
  doc,
  loading,
  canManage,
  act,
  onSaved,
  seedSku,
  onSeedConsumed,
}: {
  doc: CapacityShapes | null
  loading: boolean
  canManage: boolean
  act: Act
  onSaved: () => Promise<void>
  seedSku: string | null
  onSeedConsumed: () => void
}) {
  const kinds = doc?.resource_kinds ?? []
  const [editing, setEditing] = useState<string | null>(null)
  const [values, setValues] = useState<Record<string, string>>({})
  const [newSku, setNewSku] = useState('')
  const [adding, setAdding] = useState(false)
  // A one-click "Add a shape" on an unshaped SKU opens the form filled in.
  if (seedSku !== null && !adding) {
    setAdding(true)
    setNewSku(seedSku)
    setValues({})
    onSeedConsumed()
  }
  const columnKeys = useMemo(() => sortedResourceKeys(kinds.map((k) => k.resource), kinds), [kinds])
  const startEdit = (sh: CapacityShape) => {
    const v: Record<string, string> = {}
    for (const key of columnKeys) v[key] = sh.resources[key] !== undefined ? String(toNumber(sh.resources[key])) : ''
    setValues(v)
    setEditing(sh.sku)
  }
  const submit = async (sku: string, done: () => void) => {
    const parsed = parseShapeForm(values)
    if (parsed.error) {
      act.setError(parsed.error)
      return
    }
    if (!sku.trim()) {
      act.setError(t('capacity.shapes.col.sku'))
      return
    }
    const n = Object.keys(parsed.resources).length
    const label = n ? t('capacity.shapes.saved', { sku: sku.trim() }) : t('capacity.shapes.removed', { sku: sku.trim() })
    const ok = await act.run(label, () => api.put(`/capacity/shapes/${encodeURIComponent(sku.trim())}`, { resources: parsed.resources }), onSaved)
    if (ok) done()
  }
  const grid = (
    <div className="fp-grid">
      {columnKeys.map((key) => {
        const k = kindOf(key, kinds)
        return (
          <Field key={key} label={`${k.label}${k.unit ? ` (${k.unit})` : ''}`}>
            <input aria-label={`${k.label} per unit`} value={values[key] ?? ''} onChange={(e) => setValues({ ...values, [key]: e.target.value })} placeholder="0" />
          </Field>
        )
      })}
    </div>
  )
  const columns: Column<CapacityShape>[] = [
    { key: 'sku', header: t('capacity.shapes.col.sku'), value: (sh) => sh.sku, render: (sh) => <code>{sh.sku}</code> },
    { key: 'shape', header: t('capacity.shapes.col.shape'), value: (sh) => shapeSummary(sh.resources, kinds), render: (sh) => shapeSummary(sh.resources, kinds) },
    { key: 'source', header: t('capacity.shapes.col.source'), value: (sh) => sh.source, render: (sh) => <Badge status={sh.source} kind={sh.source === 'seed' ? 'info' : undefined} /> },
    {
      key: 'act',
      header: '',
      value: () => '',
      sortable: false,
      className: 'actions',
      render: (sh) =>
        canManage ? (
          <button className="small" onClick={() => startEdit(sh)}>
            {t('capacity.shapes.edit')}
          </button>
        ) : null,
    },
  ]
  return (
    <div className="card pad-0">
      <div className="card-head" style={{ padding: '12px 16px 0' }}>
        <h2>{t('capacity.shapes.title')}</h2>
        <span className="hint">
          {t('capacity.shapes.sub')}
          {doc?.unseeded_skus?.length ? <> · {t('capacity.shapes.unseeded', { skus: doc.unseeded_skus.join(', ') })}</> : null}
        </span>
        {canManage ? (
          <button
            className="small primary"
            onClick={() => {
              setAdding(!adding)
              setNewSku('')
              setValues({})
            }}
          >
            {t('capacity.shapes.add')}
          </button>
        ) : null}
      </div>
      {loading && !doc ? (
        <div style={{ padding: 16 }}>
          <Skeleton lines={3} />
        </div>
      ) : (
        <DataTable
          label={t('capacity.shapes.title')}
          columns={columns}
          rows={doc?.shapes ?? []}
          rowKey={(sh) => sh.sku}
          defaultSort={{ key: 'sku', dir: 'asc' }}
          pageSize={20}
          csvName="capacity-shapes"
          emptyTitle={t('capacity.shapes.none')}
          emptyBody={t('capacity.shapes.noneBody')}
          expanded={(sh) =>
            editing === sh.sku ? (
              <form
                aria-label={`${t('capacity.shapes.edit')} ${sh.sku}`}
                onSubmit={(e: FormEvent) => {
                  e.preventDefault()
                  void submit(sh.sku, () => setEditing(null))
                }}
              >
                {grid}
                <div className="row end" style={{ marginTop: 8 }}>
                  <span className="muted small">{t('capacity.shapes.removeHint')}</span>
                  <button type="button" className="small" onClick={() => setEditing(null)} disabled={act.busy}>
                    {t('common.cancel')}
                  </button>
                  <button type="submit" className="small primary" disabled={act.busy}>
                    {t('capacity.form.save')}
                  </button>
                </div>
              </form>
            ) : null
          }
        />
      )}
      {adding ? (
        <form
          style={{ padding: '12px 16px 16px', borderTop: '1px solid var(--line)' }}
          aria-label={t('capacity.shapes.add')}
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            void submit(newSku, () => {
              setAdding(false)
              setNewSku('')
              setValues({})
            })
          }}
        >
          <Field label={t('capacity.shapes.col.sku')}>
            <input value={newSku} onChange={(e) => setNewSku(e.target.value)} placeholder="nat.1" />
          </Field>
          {grid}
          <div className="row end" style={{ marginTop: 8 }}>
            <button type="button" className="small" onClick={() => setAdding(false)} disabled={act.busy}>
              {t('common.cancel')}
            </button>
            <button type="submit" className="small primary" disabled={act.busy}>
              {t('capacity.form.save')}
            </button>
          </div>
        </form>
      ) : null}
    </div>
  )
}
