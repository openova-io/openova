import { useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import type {
  CapacityBasket,
  CapacityClassDef,
  CapacityPoolRunning,
  CapacityPoolView,
  CapacityResourceKind,
  CapacityResourceView,
  CapacityRunningResource,
  CapacitySKUOptions,
} from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { Field, Notice, Skeleton } from '../../components/ui'
import { t } from '../../i18n'
import { basketQuery, formatAmount, formatDays, formatRatio, heatClass, kindOf, orderedClasses, poolFingerprint, shapeSummary, sparkPath } from '../../lib/capacity'
import { toNumber } from '../../lib/num'
import { useQuery } from '../../lib/useQuery'
import { ClassBadge, classText, type Act } from './shared'
import { SkuSelect, skuChosen } from './SkuSelect'

/**
 * Everything about ONE pool, shown when its row in the Pools table is opened:
 * why it binds (the per-resource table), how many more fit (the basket), and
 * what is running on it resource by resource — with the class each resource
 * counts at, and the spot to give back when there is more of it than room.
 */
export function PoolDetail({
  pool,
  kinds,
  classes,
  skus,
  canManage,
  act,
  onSaved,
  onPlacements,
}: {
  pool: CapacityPoolView
  kinds: CapacityResourceKind[]
  classes: CapacityClassDef[]
  skus: CapacitySKUOptions | null
  canManage: boolean
  act: Act
  onSaved: () => Promise<void>
  onPlacements: () => void
}) {
  const burstable = pool.classes.includes('burstable')
  return (
    <div className="stack" data-pool-detail={pool.name}>
      <div className="table-wrap">
        <table className="heatmap" aria-label={t('capacity.detail.resources', { pool: pool.name })}>
          <thead>
            <tr>
              <th>{t('capacity.form.resource')}</th>
              <th>{t('capacity.res.usable')}</th>
              {burstable ? <th title={t('capacity.form.ratioHelp')}>{t('capacity.res.ratio')}</th> : null}
              {burstable ? <th title={t('capacity.floor.help')}>{t('capacity.floor.title')}</th> : null}
              <th>{t('capacity.res.sellable')}</th>
              <th>{t('capacity.class.split')}</th>
              <th>{t('capacity.res.left')}</th>
              <th title={t('capacity.trend.sub')}>{t('capacity.trend.title')}</th>
              <th>{t('capacity.trend.orderBy')}</th>
            </tr>
          </thead>
          <tbody>
            {pool.resources_view.map((r) => (
              <ResourceRow key={r.resource} pool={pool} r={r} classes={classes} burstable={burstable} />
            ))}
          </tbody>
        </table>
      </div>
      <div className="table-foot">
        <span>{burstable ? t('capacity.res.formula') : t('capacity.res.formulaPlain')}</span>
      </div>

      <BasketPanel pool={pool} kinds={kinds} classes={classes} skus={skus} />

      <div className="row between" style={{ padding: '0 16px' }}>
        <span>
          <b>{t('capacity.detail.placements', { count: pool.placements.length })}</b>
          <span className="sub">{pool.placements.length ? pool.placements.map((p) => `${p.sku} (${classText(p.class, classes)})`).join(' · ') : t('capacity.placements.none')}</span>
        </span>
        <button type="button" className="small" onClick={onPlacements}>
          {t('capacity.detail.managePlacements')}
        </button>
      </div>

      <RunningPanel pool={pool} kinds={kinds} classes={classes} canManage={canManage} act={act} onSaved={onSaved} />
    </div>
  )
}

/** One resource of a pool, with its class split, its trend and its order-by. */
function ResourceRow({ pool, r, classes, burstable }: { pool: CapacityPoolView; r: CapacityResourceView; classes: CapacityClassDef[]; burstable: boolean }) {
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
      {burstable ? <td>{formatRatio(r.overcommit_ratio)}</td> : null}
      {burstable ? (
        <td data-floor={String(toNumber(r.guaranteed_floor))}>
          {toNumber(r.guaranteed_floor) > 0 ? (
            <>
              <b>{formatAmount(r.guaranteed_floor)}</b> {r.unit}
              <span className="sub">{t('capacity.floor.free', { amount: formatAmount(r.floor_free) })}</span>
              <span className="sub">{t('capacity.floor.envelope', { room: formatAmount(r.burstable_room), envelope: formatAmount(r.burstable_envelope) })}</span>
            </>
          ) : (
            <span className="muted" title={t('capacity.floor.noneNote')}>
              {t('capacity.floor.none')}
            </span>
          )}
        </td>
      ) : null}
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
            {r.stranded ? <span className="sub bad">{t('capacity.res.stranded', { amount: formatAmount(r.physical_free), unit: r.unit, binding: kindOf(pool.binding_resource).label })}</span> : null}
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

interface MixLine {
  sku: string
  units: string
  cls: string
}

/**
 * How many more of a mix fit. The mix is BUILT, line by line, from the SKU
 * list and the classes this pool enforces — never typed as text.
 */
function BasketPanel({ pool, kinds, classes, skus }: { pool: CapacityPoolView; kinds: CapacityResourceKind[]; classes: CapacityClassDef[]; skus: CapacitySKUOptions | null }) {
  const poolClasses = orderedClasses(pool.classes)
  const [lines, setLines] = useState<MixLine[]>([])
  const [draft, setDraft] = useState<MixLine>({ sku: '', units: '1', cls: '' })
  const [err, setErr] = useState('')
  const applied = basketQuery(lines.map((l) => ({ sku: l.sku, units: l.units, class: l.cls })))
  // Re-asked whenever the pool changes under it (poolFingerprint), not only
  // when the mix does: the same mix fits a different number of times on a
  // resized pool.
  const named = useQuery<{ basket: CapacityBasket }>(applied ? `/capacity/pools/${pool.id}/headroom?basket=${encodeURIComponent(applied)}` : null, [applied, poolFingerprint(pool)])
  const basket = applied && named.data ? named.data.basket : pool.basket
  const bindingLabel = basket.binding_resource ? kindOf(basket.binding_resource, kinds).label : ''

  const add = (e: FormEvent) => {
    e.preventDefault()
    if (!skuChosen(draft.sku)) {
      setErr(t('capacity.place.chooseSku'))
      return
    }
    if (!(toNumber(draft.units) > 0)) {
      setErr(t('capacity.basket.unitsInvalid'))
      return
    }
    setErr('')
    setLines([...lines, draft])
    setDraft({ sku: '', units: '1', cls: '' })
  }

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
      {named.error ? <Notice kind="bad">{named.error}</Notice> : null}
      {basket.unshaped_skus.length > 0 ? <Notice kind="warn">{t('capacity.basket.unshaped', { skus: basket.unshaped_skus.join(', ') })}</Notice> : null}
      <div className="chips" aria-label={t('capacity.basket.mix')}>
        <span className="muted small">{lines.length ? t('capacity.basket.yourMix') : t('capacity.basket.current')}:</span>
        {(basket.items ?? []).length === 0 ? <span className="muted small">{t('common.none')}</span> : null}
        {lines.length
          ? lines.map((l, i) => (
              <span className="chip" key={`${l.sku}-${i}`}>
                <span className="val">
                  {formatAmount(l.units)} × {l.sku}
                </span>
                <span className="dim">{l.cls ? classText(l.cls, classes) : t('capacity.basket.defaultClass')}</span>
                <button type="button" aria-label={t('capacity.basket.removeLine', { sku: l.sku })} onClick={() => setLines(lines.filter((_, j) => j !== i))}>
                  ×
                </button>
              </span>
            ))
          : (basket.items ?? []).map((it) => (
              <span className="chip" key={`${it.sku}-${it.class}`}>
                <span className="val">
                  {formatAmount(it.units)} × {it.sku}
                </span>
                <span className="dim">{classText(it.class, classes)}</span>
              </span>
            ))}
        {lines.length ? (
          <button type="button" className="chip-add" onClick={() => setLines([])}>
            {t('capacity.basket.reset')}
          </button>
        ) : null}
      </div>
      <form className="inline" aria-label={t('capacity.basket.editOn', { pool: pool.name })} onSubmit={add}>
        <SkuSelect doc={skus} value={draft.sku} onChange={(sku) => setDraft({ ...draft, sku })} needsShape id={`mix-sku-${pool.id}`} />
        <Field label={t('capacity.basket.units')}>
          <input inputMode="decimal" aria-label={t('capacity.basket.units')} value={draft.units} onChange={(e) => setDraft({ ...draft, units: e.target.value })} style={{ width: 80 }} />
        </Field>
        <Field label={t('capacity.place.class')}>
          <select aria-label={t('capacity.basket.lineClass')} value={draft.cls} onChange={(e) => setDraft({ ...draft, cls: e.target.value })}>
            <option value="">{t('capacity.basket.defaultClass')}</option>
            {poolClasses.map((c) => (
              <option key={c} value={c}>
                {classText(c, classes)}
              </option>
            ))}
          </select>
        </Field>
        <button type="submit" className="small">
          {t('capacity.basket.addLine')}
        </button>
      </form>
      {err ? <Notice kind="bad">{err}</Notice> : null}
      {basket.resources.length > 0 ? (
        <div className="table-wrap">
          <table aria-label={t('capacity.basket.costs', { pool: pool.name })}>
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

/**
 * What is running on the pool, resource by resource, and the class each one
 * counts at. One SKU may be placed at several classes, so the RESOURCE says
 * which it was sold at: an operator's choice here, else its lifecycle tag,
 * else the most conservative class its SKU is placed at.
 */
function RunningPanel({ pool, kinds, classes, canManage, act, onSaved }: { pool: CapacityPoolView; kinds: CapacityResourceKind[]; classes: CapacityClassDef[]; canManage: boolean; act: Act; onSaved: () => Promise<void> }) {
  // Keyed on the pool's fingerprint, never its id alone: a resize, a new
  // placement or a resource changing class all change what is running here
  // and which spot has to be given back.
  const running = useQuery<CapacityPoolRunning>(`/capacity/pools/${pool.id}/resources`, [poolFingerprint(pool)])
  const doc = running.data
  const setClass = (r: CapacityRunningResource, cls: string) => {
    const label = cls ? t('capacity.running.classSet', { resource: r.name || r.resource_id, class: classText(cls, classes) }) : t('capacity.running.classCleared', { resource: r.name || r.resource_id })
    void act.run(label, () => api.put('/capacity/resource-classes', { source_id: r.source_id, resource_id: r.resource_id, class: cls || null }), async () => {
      await Promise.all([running.reload(), onSaved()])
    })
  }
  const columns: Column<CapacityRunningResource>[] = [
    {
      key: 'name',
      header: t('capacity.running.col.resource'),
      value: (r) => r.name || r.resource_id,
      render: (r) => (
        <>
          {r.name || r.resource_id}
          <span className="sub">
            <code>{r.resource_id}</code>
          </span>
        </>
      ),
    },
    {
      key: 'sku',
      header: t('capacity.placements.col.sku'),
      value: (r) => r.sku,
      render: (r) => (
        <>
          <code>{r.sku}</code>
          {r.via !== r.sku ? <span className="sub">{t('capacity.running.via', { family: r.via })}</span> : null}
        </>
      ),
    },
    { key: 'units', header: t('capacity.placements.col.units'), value: (r) => toNumber(r.units), numeric: true, render: (r) => formatAmount(r.units) },
    { key: 'consumes', header: t('capacity.running.col.consumes'), value: (r) => shapeSummary(r.consumes, kinds), render: (r) => shapeSummary(r.consumes, kinds) },
    {
      key: 'class',
      header: t('capacity.placements.col.class'),
      value: (r) => r.class,
      render: (r) => (
        <span data-class={r.class} data-class-source={r.class_source}>
          <ClassBadge cls={r.class} classes={classes} />
          <span className="sub">
            {r.class_source === 'override' ? t('capacity.running.sourceOverride') : r.class_source === 'tag' ? t('capacity.running.sourceTag') : t('capacity.running.sourceDefault')}
            {r.asked ? ` · ${t('capacity.running.asked', { class: classText(r.asked, classes) })}` : ''}
          </span>
        </span>
      ),
    },
    {
      key: 'set',
      header: t('capacity.running.col.soldAs'),
      value: (r) => r.override_class,
      sortable: false,
      render: (r) =>
        canManage ? (
          <select style={{ minWidth: 130 }} aria-label={t('capacity.running.setClass', { resource: r.name || r.resource_id })} value={r.override_class} disabled={act.busy || r.placed_classes.length < 2} title={r.placed_classes.length < 2 ? t('capacity.running.oneClass') : undefined} onChange={(e) => setClass(r, e.target.value)}>
            <option value="">{t('capacity.running.auto')}</option>
            {r.placed_classes.map((c) => (
              <option key={c} value={c}>
                {classText(c, classes)}
              </option>
            ))}
          </select>
        ) : (
          <span className="muted">{r.override_class ? classText(r.override_class, classes) : t('capacity.running.auto')}</span>
        ),
    },
    {
      key: 'reclaim',
      header: t('capacity.reclaim.col'),
      value: (r) => (r.reclaim ? 1 : 0),
      render: (r) => (r.reclaim ? <b className="bad">{t('capacity.reclaim.mark')}</b> : r.shared_with.length ? <span className="muted small">{t('capacity.running.shared', { pools: r.shared_with.join(', ') })}</span> : null),
    },
  ]
  return (
    <div className="stack tight" style={{ borderTop: '1px solid var(--line)', paddingTop: 10 }} data-running={pool.name}>
      <div className="row between" style={{ padding: '0 16px' }}>
        <span>
          <b>{t('capacity.running.title')}</b>
          <span className="sub">{t('capacity.running.sub')}</span>
        </span>
      </div>
      {running.error ? <Notice kind="bad">{running.error}</Notice> : null}
      {(doc?.reclaim ?? []).map((rc) => (
        <Notice kind="warn" key={rc.resource}>
          <span data-reclaim={rc.resource}>
            {t('capacity.reclaim.needed', { amount: formatAmount(rc.needed), unit: rc.unit, label: rc.label })}{' '}
            {rc.short ? t('capacity.reclaim.short', { covered: formatAmount(rc.covered), unit: rc.unit }) : t('capacity.reclaim.covered', { count: rc.resources, covered: formatAmount(rc.covered), unit: rc.unit })} {t('capacity.reclaim.who')}
          </span>
        </Notice>
      ))}
      {running.loading && !doc ? (
        <div style={{ padding: 16 }}>
          <Skeleton lines={2} />
        </div>
      ) : (
        <DataTable
          label={`${t('capacity.running.title')} — ${pool.name}`}
          columns={columns}
          rows={doc?.resources ?? []}
          rowKey={(r) => `${r.source_id}|${r.resource_id}|${r.sku}`}
          defaultSort={{ key: 'reclaim', dir: 'desc' }}
          pageSize={10}
          dense
          emptyTitle={t('capacity.running.none')}
          emptyBody={t('capacity.running.noneBody')}
        />
      )}
    </div>
  )
}
