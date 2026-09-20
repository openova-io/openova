import { useCallback, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { CapacityOverview, CapacityPoolView, CapacityRegion, CapacityShapes, CapacitySKUOptions } from '../api/types'
import { useSession } from '../auth/session'
import { Confirm, EmptyState, KPI, Notice, PageHeader, Segmented, Skeleton, Tabs } from '../components/ui'
import { t } from '../i18n'
import { can } from '../lib/access'
import { asOfLabel, placementRows, poolRows, zoneRows } from '../lib/capacity'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'
import { PlacementsTab, type PlaceSeed } from '../panels/capacity/PlacementsTab'
import { PoolEditor } from '../panels/capacity/PoolEditor'
import { PoolsTab } from '../panels/capacity/PoolsTab'
import { AddRegionForm, RegionsTab, type RegionDialog } from '../panels/capacity/RegionsTab'
import { ShapesTab } from '../panels/capacity/ShapesTab'

/**
 * Plan → Capacity (DESIGN.md §11).
 *
 * The operator's picture of the hardware underneath, in FOUR TABS, because
 * four questions were competing for one scroll and the reader was lost in it
 * (founder direction 2026-09-20):
 *
 *   Pools             how much have I got, what binds first, when do I order —
 *                     ONE LINE per pool; opening a line shows why it binds, how
 *                     many more fit, and what is running on it by class.
 *   Placements        which SKU sells out of which pool, at which class — one
 *                     table; the same SKU may sit on a pool once per class the
 *                     pool enforces.
 *   Shapes            what one unit of a SKU consumes.
 *   Regions & zones   where the pools live.
 *
 * What is above the tabs is what is true whichever tab is open: the KPIs, the
 * region filter, and the things that need attention — each naming the tab
 * that fixes it.
 *
 * NOTHING IS TYPED THAT CAN BE CHOSEN. A SKU, a family, a resource kind, a
 * pool and a class are all picked from what the product already knows.
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

const TAB_KEYS = ['pools', 'placements', 'shapes', 'regions'] as const
type TabKey = (typeof TAB_KEYS)[number]

type Dialog = { kind: 'pool'; pool: CapacityPoolView } | RegionDialog | null

export function Capacity() {
  const { me } = useSession()
  const canManage = can(me, 'capacity.manage')
  const overview = useQuery<CapacityOverview>('/capacity/overview')
  const shapes = useQuery<CapacityShapes>('/capacity/shapes')
  const skus = useQuery<CapacitySKUOptions>('/capacity/skus')
  const act = useAction()
  const [params, setParams] = useSearchParams()
  const asked = params.get('tab') ?? ''
  const tab: TabKey = (TAB_KEYS as ReadonlyArray<string>).includes(asked) ? (asked as TabKey) : 'pools'
  const [region, setRegion] = useState<string>('all')
  const [confirm, setConfirm] = useState<Dialog>(null)
  const [editing, setEditing] = useState<{ zoneID: string; pool: CapacityPoolView | null } | null>(null)
  const [placeSeed, setPlaceSeed] = useState<PlaceSeed | null>(null)
  const clearSeed = useCallback(() => setPlaceSeed(null), [])

  const ov = overview.data
  const kinds = ov?.resource_kinds ?? []
  const classes = ov?.classes ?? []
  const zones = useMemo(() => zoneRows(ov, region), [ov, region])
  const pools = useMemo(() => poolRows(ov, region), [ov, region])
  const allPools = useMemo(() => poolRows(ov, 'all'), [ov])
  const placements = useMemo(() => placementRows(ov, region), [ov, region])
  // Every zone the Sovereign has, for the editor's zone picker. The region
  // filter narrows what is SHOWN; it never narrows where a pool may be added.
  const zoneOptions = useMemo(
    () => (ov?.regions ?? []).flatMap((r) => r.zones.map((z) => ({ id: z.id, label: `${r.code} / ${z.code}${z.is_default ? ` (${t('capacity.regions.defaultZone')})` : ''}` }))),
    [ov],
  )
  const summary = ov?.summary
  const asOf = asOfLabel(ov?.as_of)

  const reload = async () => {
    await Promise.all([overview.reload(), shapes.reload(), skus.reload()])
  }
  const go = (next: TabKey) => setParams({ tab: next })

  const regionOptions = [
    { value: 'all', label: t('capacity.filter.all', { count: ov?.regions.length ?? 0 }) },
    ...(ov?.regions ?? []).map((r) => ({ value: r.code, label: r.name ? `${r.code} · ${r.name}` : r.code })),
  ]
  const tabs = [
    { key: 'pools', label: t('capacity.tab.pools') },
    { key: 'placements', label: t('capacity.tab.placements') },
    { key: 'shapes', label: t('capacity.tab.shapes') },
    { key: 'regions', label: t('capacity.tab.regions') },
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
            <button type="button" className="small primary" onClick={() => setEditing({ zoneID: zones[0]?.zone.id ?? zoneOptions[0].id, pool: null })}>
              {t('capacity.pool.add')}
            </button>
          ) : null
        }
      />
      {overview.error ? <Notice kind="bad">{overview.error}</Notice> : null}
      {shapes.error ? <Notice kind="bad">{shapes.error}</Notice> : null}
      {skus.error ? <Notice kind="bad">{skus.error}</Notice> : null}
      {/* While the editor is open it shows the error itself, next to the
          fields it is about. */}
      {act.error && !editing ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {!canManage ? <Notice kind="info">{t('capacity.readOnly')}</Notice> : null}

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
        <KPI label={t('capacity.kpi.measured')} value={asOf || t('common.none')} note={ov ? (ov.as_of ? sourcesNote(ov) : t('capacity.kpi.noUsage')) : undefined} />
      </div>

      {overview.loading && !ov ? (
        <div className="card">
          <Skeleton lines={4} />
        </div>
      ) : null}

      {/* What needs attention, whichever tab is open — each line names the
          tab that fixes it and takes the reader there. */}
      {ov && summary && (summary.unplaced_skus > 0 || summary.unshaped_skus > 0 || summary.class_mismatches > 0 || summary.spot_to_reclaim > 0 || ov.unmapped_regions.length > 0) ? (
        <Notice kind="warn">
          <div className="stack tight" data-attention>
            {summary.spot_to_reclaim > 0 ? <Attention text={t('capacity.attention.reclaim', { count: summary.spot_to_reclaim })} to={t('capacity.tab.pools')} onGo={() => go('pools')} /> : null}
            {summary.unplaced_skus > 0 ? <Attention text={t('capacity.attention.unplaced', { count: summary.unplaced_skus })} to={t('capacity.tab.placements')} onGo={() => go('placements')} /> : null}
            {summary.class_mismatches > 0 ? <Attention text={t('capacity.attention.mismatch', { count: summary.class_mismatches })} to={t('capacity.tab.placements')} onGo={() => go('placements')} /> : null}
            {summary.unshaped_skus > 0 ? <Attention text={t('capacity.attention.unshaped', { count: summary.unshaped_skus })} to={t('capacity.tab.shapes')} onGo={() => go('shapes')} /> : null}
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
                    type="button"
                    className="small"
                    disabled={act.busy}
                    onClick={() => void act.run(t('capacity.regions.regionAdded', { code: u.region }), () => api.post<CapacityRegion>('/capacity/regions', { code: u.region }), async () => { await reload(); go('regions') })}
                  >
                    {t('capacity.unmapped.addRegion', { region: u.region })}
                  </button>
                ) : (
                  <button type="button" className="small" onClick={() => go('regions')}>
                    {t('capacity.attention.open', { tab: t('capacity.tab.regions') })}
                  </button>
                )}
              </div>
            ))}
          </div>
        </Notice>
      ) : null}

      {ov && ov.regions.length === 0 ? (
        <div className="card">
          <EmptyState title={t('capacity.empty.title')}>{t('capacity.empty.body')}</EmptyState>
          {canManage ? <AddRegionForm act={act} onDone={reload} /> : null}
        </div>
      ) : null}

      {ov && ov.regions.length > 0 ? (
        <>
          <Tabs base="/capacity" tabs={tabs} current={tab} counts={{ pools: summary?.pools, placements: summary?.placements, shapes: summary?.shapes, regions: summary?.regions }} />

          {tab === 'pools' || tab === 'placements' ? (
            <div className="toolbar" role="region" aria-label="Capacity region filter">
              <div className="field">
                <label>{t('capacity.filter.region')}</label>
                <Segmented value={region} options={regionOptions} onChange={setRegion} ariaLabel="Region filter" />
              </div>
              <div className="grow" />
              {tab === 'pools' ? (
                <div className="heat-legend">
                  {HEAT_LEGEND.map(([cls, key]) => (
                    <span key={cls}>
                      <i className={cls} />
                      {t(key)}
                    </span>
                  ))}
                </div>
              ) : null}
            </div>
          ) : null}

          {tab === 'pools' ? (
            <PoolsTab
              rows={pools}
              hasZones={zoneOptions.length > 0}
              kinds={kinds}
              classes={classes}
              skus={skus.data}
              canManage={canManage}
              act={act}
              onSaved={reload}
              onAdd={() => setEditing({ zoneID: zones[0]?.zone.id ?? zoneOptions[0]?.id ?? '', pool: null })}
              onEdit={(row) => setEditing({ zoneID: row.zone.id, pool: row.pool })}
              onDelete={(pool) => setConfirm({ kind: 'pool', pool })}
              onPlacements={(pool) => {
                setPlaceSeed({ poolID: pool.id })
                go('placements')
              }}
            />
          ) : null}
          {tab === 'placements' ? (
            <PlacementsTab rows={placements} pools={allPools} zones={zones} kinds={kinds} classes={classes} skus={skus.data} canManage={canManage} act={act} onSaved={reload} seed={placeSeed} onSeedConsumed={clearSeed} />
          ) : null}
          {tab === 'shapes' ? <ShapesTab doc={shapes.data} loading={shapes.loading} unshaped={ov.unshaped_skus} skus={skus.data} canManage={canManage} act={act} onSaved={reload} /> : null}
          {tab === 'regions' ? (
            <RegionsTab
              regions={ov.regions}
              canManage={canManage}
              act={act}
              onDone={reload}
              onDelete={setConfirm}
              onAddPool={(zoneID) => setEditing({ zoneID, pool: null })}
            />
          ) : null}
        </>
      ) : null}

      {/* One editor, opened from the page header, from the Pools tab, from a
          pool's Edit and from a zone's row — the same dialog every time. The
          key remounts it when it is pointed at a different pool or zone. */}
      {editing ? (
        <PoolEditor
          key={`${editing.pool?.id ?? 'new'}-${editing.zoneID}`}
          zoneID={editing.zoneID}
          zones={zoneOptions}
          pool={editing.pool}
          kinds={kinds}
          classes={classes}
          act={act}
          onClose={() => {
            act.setError('')
            setEditing(null)
          }}
          onSaved={async () => {
            setEditing(null)
            await reload()
            go('pools')
          }}
        />
      ) : null}

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

/** One line of what needs attention, with the button that goes to the tab that fixes it. */
function Attention({ text, to, onGo }: { text: string; to: string; onGo: () => void }) {
  return (
    <div className="row between">
      <span>{text}</span>
      <button type="button" className="small" onClick={onGo}>
        {t('capacity.attention.open', { tab: to })}
      </button>
    </div>
  )
}

function sourcesNote(ov: CapacityOverview): string {
  const base = ov.sources === 1 ? t('capacity.kpi.sourcesOne') : t('capacity.kpi.sources', { count: ov.sources })
  return ov.lagging_sources ? `${base} · ${t('capacity.kpi.lagging', { count: ov.lagging_sources })}` : base
}
