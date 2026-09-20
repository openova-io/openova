import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import type { CapacityShape, CapacityShapes, CapacitySKUOptions, CapacityUnshapedSKU } from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { Badge, Field, Skeleton } from '../../components/ui'
import { t } from '../../i18n'
import { formatAmount, kindOf, parseShapeForm, shapeSummary, sortedResourceKeys } from '../../lib/capacity'
import { toNumber } from '../../lib/num'
import type { Act } from './shared'
import { SkuSelect, skuChosen } from './SkuSelect'

/**
 * SKU SHAPES: how much of each resource ONE unit of a SKU consumes. The SKUs
 * metered with no shape at all come first — nothing knows what they consume,
 * so they count against nothing — then every stored shape, one row per SKU.
 */
export function ShapesTab({
  doc,
  loading,
  unshaped,
  skus,
  canManage,
  act,
  onSaved,
}: {
  doc: CapacityShapes | null
  loading: boolean
  unshaped: CapacityUnshapedSKU[]
  skus: CapacitySKUOptions | null
  canManage: boolean
  act: Act
  onSaved: () => Promise<void>
}) {
  const kinds = doc?.resource_kinds ?? []
  const [editing, setEditing] = useState<string | null>(null)
  const [values, setValues] = useState<Record<string, string>>({})
  const [newSku, setNewSku] = useState('')
  const [adding, setAdding] = useState(false)
  const columnKeys = useMemo(() => sortedResourceKeys(kinds.map((k) => k.resource), kinds), [kinds])
  const stored = useMemo(() => new Set((doc?.shapes ?? []).map((s) => s.sku)), [doc])
  useEffect(() => {
    if (!adding) setNewSku('')
  }, [adding])

  const startEdit = (sh: CapacityShape) => {
    const v: Record<string, string> = {}
    for (const key of columnKeys) v[key] = sh.resources[key] !== undefined ? String(toNumber(sh.resources[key])) : ''
    setValues(v)
    setEditing(sh.sku)
  }
  const startAdd = (sku: string) => {
    setEditing(null)
    setValues({})
    setAdding(true)
    setNewSku(sku)
  }
  const submit = async (sku: string, done: () => void) => {
    const parsed = parseShapeForm(values)
    if (parsed.error) {
      act.setError(parsed.error)
      return
    }
    if (!skuChosen(sku)) {
      act.setError(t('capacity.place.chooseSku'))
      return
    }
    const n = Object.keys(parsed.resources).length
    const label = n ? t('capacity.shapes.saved', { sku }) : t('capacity.shapes.removed', { sku })
    const ok = await act.run(label, () => api.put(`/capacity/shapes/${encodeURIComponent(sku)}`, { resources: parsed.resources }), onSaved)
    if (ok) done()
  }
  const grid = (
    <div className="fp-grid">
      {columnKeys.map((key) => {
        const k = kindOf(key, kinds)
        return (
          <Field key={key} label={`${k.label}${k.unit ? ` (${k.unit})` : ''}`}>
            <input inputMode="decimal" aria-label={t('capacity.shapes.perUnitOf', { resource: k.label })} value={values[key] ?? ''} onChange={(e) => setValues({ ...values, [key]: e.target.value })} placeholder="0" />
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
          <button type="button" className="small" aria-label={t('capacity.shapes.editNamed', { sku: sh.sku })} onClick={() => (editing === sh.sku ? setEditing(null) : startEdit(sh))}>
            {t('capacity.shapes.edit')}
          </button>
        ) : null,
    },
  ]
  return (
    <div className="stack">
      {unshaped.length > 0 ? (
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
              { key: 'res', header: t('capacity.unshaped.col.resources'), value: (u) => u.resources, numeric: true },
              { key: 'regions', header: t('common.region'), value: (u) => u.regions.join(', ') },
              {
                key: 'act',
                header: '',
                value: () => '',
                sortable: false,
                className: 'actions',
                render: (u) =>
                  canManage ? (
                    <button type="button" className="small primary" aria-label={t('capacity.unshaped.addNamed', { sku: u.sku })} onClick={() => startAdd(u.sku)}>
                      {t('capacity.unshaped.add')}
                    </button>
                  ) : null,
              },
            ]}
            rows={unshaped}
            rowKey={(u) => u.sku}
            defaultSort={{ key: 'qty', dir: 'desc' }}
          />
        </div>
      ) : null}

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 16px 0' }}>
          <h2>{t('capacity.shapes.title')}</h2>
          <span className="hint">
            {t('capacity.shapes.sub')}
            {doc?.unseeded_skus?.length ? <> · {t('capacity.shapes.unseeded', { skus: doc.unseeded_skus.join(', ') })}</> : null}
          </span>
          {canManage ? (
            <button type="button" className="small primary" onClick={() => (adding ? setAdding(false) : startAdd(''))}>
              {t('capacity.shapes.add')}
            </button>
          ) : null}
        </div>
        {adding ? (
          <form
            style={{ padding: '12px 16px 16px', borderBottom: '1px solid var(--line)' }}
            aria-label={t('capacity.shapes.add')}
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              void submit(newSku, () => {
                setAdding(false)
                setValues({})
              })
            }}
          >
            <div className="inline">
              {/* Only SKUs with no STORED shape: one that has a row is edited on its row. */}
              <SkuSelect doc={skus} value={newSku} onChange={setNewSku} only={(o) => !stored.has(o.sku) || o.sku === newSku} label={t('capacity.shapes.col.sku')} id="shape-sku" />
            </div>
            {grid}
            <div className="row end" style={{ marginTop: 8 }}>
              <button type="button" className="small" onClick={() => setAdding(false)} disabled={act.busy}>
                {t('common.cancel')}
              </button>
              <button type="submit" className="small primary" disabled={act.busy}>
                {t('capacity.shapes.save')}
              </button>
            </div>
          </form>
        ) : null}
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
                  aria-label={t('capacity.shapes.editNamed', { sku: sh.sku })}
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
                      {t('capacity.shapes.save')}
                    </button>
                  </div>
                </form>
              ) : null
            }
          />
        )}
      </div>
    </div>
  )
}
