import { useEffect, useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import type { CapacityClassDef, CapacityResourceKind, CapacitySKUOptions, CapacityZoneView } from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { Field, Notice } from '../../components/ui'
import { t } from '../../i18n'
import { formatAmount, kindOf, orderedClasses, shapeSummary, type PlacementRow } from '../../lib/capacity'
import { toNumber } from '../../lib/num'
import type { PoolRow } from './PoolsTab'
import { ClassBadge, classText, type Act } from './shared'
import { SkuSelect, skuChosen } from './SkuSelect'

/** What the place form opens holding: a SKU from the unplaced list, a pool from a pool's row. */
export interface PlaceSeed {
  sku?: string
  poolID?: string
}

/**
 * EVERY PLACEMENT IN ONE TABLE: which SKU (or family) sells out of which
 * pool, at which class, and what is running through it now. One SKU may
 * appear on the same pool up to three times — once per class the pool
 * enforces — and the form above only ever offers those classes.
 */
export function PlacementsTab({
  rows,
  pools,
  zones,
  kinds,
  classes,
  skus,
  canManage,
  act,
  onSaved,
  seed,
  onSeedConsumed,
}: {
  rows: PlacementRow[]
  pools: PoolRow[]
  zones: Array<{ region: string; zone: CapacityZoneView }>
  kinds: CapacityResourceKind[]
  classes: CapacityClassDef[]
  skus: CapacitySKUOptions | null
  canManage: boolean
  act: Act
  onSaved: () => Promise<void>
  seed: PlaceSeed | null
  onSeedConsumed: () => void
}) {
  const [sku, setSku] = useState('')
  const [poolID, setPoolID] = useState('')
  const [cls, setCls] = useState('')
  useEffect(() => {
    if (!seed) return
    if (seed.sku !== undefined) setSku(seed.sku)
    if (seed.poolID !== undefined) setPoolID(seed.poolID)
    onSeedConsumed()
  }, [seed, onSeedConsumed])

  const pool = pools.find((p) => p.pool.id === poolID)?.pool
  const offered = orderedClasses(pool?.classes ?? [])
  // The class follows the pool: it is kept while the pool still enforces it,
  // and falls to the pool's first class otherwise.
  const chosenClass = offered.includes(cls) ? cls : (offered[0] ?? '')
  const already = pool && skuChosen(sku) ? new Set(rows.filter((r) => r.pool.id === pool.id && r.placement.sku.toLowerCase() === sku.toLowerCase()).map((r) => r.placement.class)) : new Set<string>()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!skuChosen(sku)) {
      act.setError(t('capacity.place.chooseSku'))
      return
    }
    if (!pool) {
      act.setError(t('capacity.place.choosePool'))
      return
    }
    void act.run(t('capacity.place.saved', { sku, pool: pool.name, class: classText(chosenClass, classes) }), () => api.put('/capacity/placements', { pool_id: pool.id, sku, class: chosenClass }), onSaved)
  }

  const columns: Column<PlacementRow>[] = [
    {
      key: 'sku',
      header: t('capacity.placements.col.sku'),
      value: (r) => r.placement.sku,
      render: (r) => (
        <>
          <code>{r.placement.sku}</code>
          {r.placement.family ? (
            <span className="sub">{r.placement.matched_skus.length ? t('capacity.placements.familyTook', { skus: r.placement.matched_skus.join(', ') }) : t('capacity.placements.familyIdle')}</span>
          ) : null}
        </>
      ),
    },
    { key: 'pool', header: t('capacity.pools.col.pool'), value: (r) => r.pool.name, render: (r) => <b>{r.pool.name}</b> },
    { key: 'zone', header: t('capacity.form.zone'), value: (r) => `${r.region} / ${r.zone}` },
    { key: 'class', header: t('capacity.placements.col.class'), value: (r) => r.placement.class, render: (r) => <ClassBadge cls={r.placement.class} classes={classes} /> },
    {
      key: 'shape',
      header: t('capacity.placements.col.shape'),
      value: (r) => shapeSummary(r.placement.shape, kinds),
      render: (r) =>
        r.placement.family ? (
          <span className="muted">{t('capacity.placements.familyShape')}</span>
        ) : (
          <>
            {shapeSummary(r.placement.shape, kinds)}
            <span className="sub">{r.placement.shape_source === 'derived' ? t('capacity.shapes.derived') : r.placement.shape_source}</span>
          </>
        ),
    },
    {
      key: 'units',
      header: t('capacity.placements.col.units'),
      value: (r) => toNumber(r.placement.units),
      numeric: true,
      render: (r) =>
        toNumber(r.placement.units) ? (
          <>
            {formatAmount(r.placement.units)}
            <span className="sub">{t('capacity.placements.resources', { count: r.placement.resources })}</span>
          </>
        ) : (
          <span className="muted">0</span>
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
          <button
            type="button"
            className="link small danger"
            disabled={act.busy}
            aria-label={t('capacity.placements.removeNamed', { sku: r.placement.sku, class: classText(r.placement.class, classes), pool: r.pool.name })}
            onClick={() =>
              void act.run(
                t('capacity.place.removed', { sku: r.placement.sku, pool: r.pool.name, class: classText(r.placement.class, classes) }),
                () => api.del(`/capacity/placements?pool_id=${encodeURIComponent(r.pool.id)}&sku=${encodeURIComponent(r.placement.sku)}&class=${encodeURIComponent(r.placement.class)}`),
                onSaved,
              )
            }
          >
            {t('capacity.placements.remove')}
          </button>
        ) : null,
    },
  ]

  const unplaced = zones.flatMap(({ zone }) => zone.unplaced_skus.map((u) => ({ zone, u })))
  const mismatches = zones.flatMap(({ zone }) => (zone.class_mismatches ?? []).map((m) => ({ zone, m })))

  return (
    <div className="stack">
      {canManage ? (
        <div className="card">
          <div className="card-head">
            <h2>{t('capacity.place.heading')}</h2>
            <span className="hint">{t('capacity.placements.sub')}</span>
          </div>
          {pools.length === 0 ? (
            <Notice kind="info">{t('capacity.place.noPools')}</Notice>
          ) : (
            <form className="inline" aria-label={t('capacity.place.heading')} onSubmit={submit}>
              <SkuSelect doc={skus} value={sku} onChange={setSku} allowFamily needsShape id="place-sku" />
              <Field label={t('capacity.pools.col.pool')}>
                <select aria-label={t('capacity.pools.col.pool')} value={poolID} onChange={(e) => setPoolID(e.target.value)}>
                  <option value="">{t('capacity.place.choosePool')}</option>
                  {pools.map((p) => (
                    <option key={p.pool.id} value={p.pool.id}>
                      {p.pool.name} — {p.region} / {p.zone.code}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label={t('capacity.place.class')} help={pool ? t('capacity.place.classHelp', { pool: pool.name }) : t('capacity.place.classPickPool')}>
                <select aria-label={t('capacity.place.class')} value={chosenClass} disabled={!pool} onChange={(e) => setCls(e.target.value)}>
                  {!pool ? <option value="">{t('common.none')}</option> : null}
                  {offered.map((c) => (
                    <option key={c} value={c} disabled={already.has(c)}>
                      {classText(c, classes)}
                      {already.has(c) ? ` — ${t('capacity.place.alreadyPlaced')}` : ''}
                    </option>
                  ))}
                </select>
              </Field>
              <button type="submit" className="small primary" disabled={act.busy || already.has(chosenClass)}>
                {t('capacity.place.save')}
              </button>
            </form>
          )}
          {pool && !pool.classes.includes('burstable') ? <span className="muted small" data-no-burstable>{t('capacity.place.noBurstable', { pool: pool.name })}</span> : null}
        </div>
      ) : null}

      <div className="card pad-0">
        <DataTable
          label={t('capacity.tab.placements')}
          columns={columns}
          rows={rows}
          rowKey={(r) => r.key}
          defaultSort={{ key: 'sku', dir: 'asc' }}
          pageSize={25}
          csvName="capacity-placements"
          emptyTitle={t('capacity.placements.noneAnywhere')}
          emptyBody={t('capacity.placements.noneBody')}
        />
      </div>

      {unplaced.length > 0 ? (
        <div className="card">
          <div className="card-head">
            <h2>{t('capacity.unplaced.title')}</h2>
          </div>
          <div className="stack tight">
            {unplaced.map(({ zone, u }) => (
              <div key={`${zone.id}-${u.sku}-${u.reason}-${u.resource ?? ''}`} className="row between" data-unplaced={u.sku}>
                <span>
                  <code>{u.sku}</code>{' '}
                  <span className="muted">
                    · {zone.code} · {formatAmount(u.units)}
                  </span>
                  <span className="sub">{u.reason === 'resource-unplaced' ? t('capacity.unplaced.resourceUnplaced', { resource: kindOf(u.resource ?? '', kinds).label }) : t('capacity.unplaced.noPlacement')}</span>
                </span>
                {canManage && u.reason !== 'resource-unplaced' ? (
                  <button type="button" className="small" onClick={() => { setSku(u.sku); document.getElementById('place-sku')?.focus() }}>
                    {t('capacity.unplaced.place')}
                  </button>
                ) : null}
              </div>
            ))}
          </div>
        </div>
      ) : null}

      {mismatches.length > 0 ? (
        <div className="card">
          <div className="card-head">
            <h2>{t('capacity.mismatch.title')}</h2>
            <span className="hint">{t('capacity.mismatch.sub')}</span>
          </div>
          <div className="stack tight">
            {mismatches.map(({ zone, m }) => (
              <div key={`${zone.id}-${m.sku}-${m.asked}`} data-mismatch={m.sku}>
                <code>{m.sku}</code> <span className="muted">· {zone.code} · {formatAmount(m.units)}</span>
                <span className="sub">{t('capacity.mismatch.row', { asked: classText(m.asked, classes), counted: classText(m.counted_as, classes) })}</span>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  )
}
