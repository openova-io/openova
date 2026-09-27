import { useMemo, useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import type { CapacityResourceKind, CapacityShape, CapacityShapes, CapacitySKUOptions, CapacityUnshapedSKU } from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { Badge, Field, FormRow, Modal, Notice, Skeleton } from '../../components/ui'
import { t } from '../../i18n'
import { formatAmount, kindOf, parseShapeForm, shapeSummary, sortedResourceKeys } from '../../lib/capacity'
import { toNumber } from '../../lib/num'
import type { Act } from './shared'
import { SkuSelect, skuChosen } from './SkuSelect'

/**
 * SKU SHAPES: how much of each resource ONE unit of a SKU consumes. The SKUs
 * metered with no shape at all come first — nothing knows what they consume,
 * so they count against nothing — then every stored shape, one row per SKU.
 * Under them the RESOURCE KINDS the shapes are measured in: a kind is a key
 * with a label and a unit, and the label and unit are edited here, on the
 * kind's row (#6946) — the key itself is what the shapes and pools are keyed
 * on and is never renamed.
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
  // The shape dialog: a fresh shape picks its SKU, an existing one is edited
  // on the SKU of the row it opened from.
  const [dialog, setDialog] = useState<{ sku: string; fresh: boolean } | null>(null)
  // The kind dialog, opened from the kind's own row.
  const [kind, setKind] = useState<CapacityResourceKind | null>(null)
  const [values, setValues] = useState<Record<string, string>>({})
  const columnKeys = useMemo(() => sortedResourceKeys(kinds.map((k) => k.resource), kinds), [kinds])
  const stored = useMemo(() => new Set((doc?.shapes ?? []).map((s) => s.sku)), [doc])

  const startEdit = (sh: CapacityShape) => {
    const v: Record<string, string> = {}
    for (const key of columnKeys) v[key] = sh.resources[key] !== undefined ? String(toNumber(sh.resources[key])) : ''
    setValues(v)
    setDialog({ sku: sh.sku, fresh: false })
  }
  const startAdd = (sku: string) => {
    setValues({})
    setDialog({ sku, fresh: true })
  }
  const close = () => {
    act.setError('')
    setDialog(null)
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
          <button type="button" className="small" aria-label={t('capacity.shapes.editNamed', { sku: sh.sku })} onClick={() => startEdit(sh)}>
            {t('capacity.shapes.edit')}
          </button>
        ) : null,
    },
  ]
  const kindColumns: Column<CapacityResourceKind>[] = [
    { key: 'resource', header: t('capacity.kinds.col.key'), value: (k) => k.resource, render: (k) => <code>{k.resource}</code> },
    { key: 'label', header: t('capacity.kinds.col.label'), value: (k) => k.label },
    { key: 'unit', header: t('capacity.kinds.col.unit'), value: (k) => k.unit, render: (k) => k.unit || <span className="muted">{t('common.none')}</span> },
    {
      key: 'act',
      header: '',
      value: () => '',
      sortable: false,
      className: 'actions',
      render: (k) =>
        canManage ? (
          <button type="button" className="small" aria-label={t('capacity.kinds.editNamed', { resource: k.resource })} onClick={() => setKind(k)}>
            {t('capacity.kinds.edit')}
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
            <button type="button" className="small primary" onClick={() => startAdd('')}>
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
          />
        )}
      </div>

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 16px 0' }}>
          <h2>{t('capacity.kinds.title')}</h2>
          <span className="hint">{t('capacity.kinds.sub')}</span>
        </div>
        {loading && !doc ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={2} />
          </div>
        ) : (
          <DataTable
            label={t('capacity.kinds.title')}
            columns={kindColumns}
            rows={[...kinds].sort((a, b) => a.position - b.position || a.resource.localeCompare(b.resource))}
            rowKey={(k) => k.resource}
            emptyTitle={t('capacity.kinds.none')}
            emptyBody={t('capacity.kinds.noneBody')}
          />
        )}
      </div>

      {kind ? (
        <KindEditor
          key={kind.resource}
          kind={kind}
          act={act}
          onClose={() => {
            act.setError('')
            setKind(null)
          }}
          onSaved={async () => {
            setKind(null)
            await onSaved()
          }}
        />
      ) : null}

      {dialog ? (
        <Modal
          title={dialog.fresh ? t('capacity.shapes.add') : t('capacity.shapes.editNamed', { sku: dialog.sku })}
          onClose={close}
          wide
          footer={
            <>
              <button type="button" onClick={close} disabled={act.busy}>
                {t('common.cancel')}
              </button>
              <button className="primary" form="shape-editor" disabled={act.busy}>
                {t('capacity.shapes.save')}
              </button>
            </>
          }
        >
          <form
            id="shape-editor"
            className="stack tight"
            aria-label={dialog.fresh ? t('capacity.shapes.add') : t('capacity.shapes.editNamed', { sku: dialog.sku })}
            onSubmit={(e) => {
              e.preventDefault()
              void submit(dialog.sku, () => setDialog(null))
            }}
          >
            {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
            {dialog.fresh ? (
              <FormRow>
                {/* Only SKUs with no STORED shape: one that has a row is edited on its row. */}
                <SkuSelect doc={skus} value={dialog.sku} onChange={(sku) => setDialog({ sku, fresh: true })} only={(o) => !stored.has(o.sku) || o.sku === dialog.sku} label={t('capacity.shapes.col.sku')} id="shape-sku" />
              </FormRow>
            ) : null}
            {grid}
            <span className="muted small">{t('capacity.shapes.removeHint')}</span>
          </form>
        </Modal>
      ) : null}
    </div>
  )
}

/**
 * Edit a resource kind's label and unit, in a dialog opened from its row.
 * The key is shown and not editable: it is what every shape, pool vector and
 * override is keyed on (PUT /capacity/resources/{resource}).
 */
export function KindEditor({ kind, act, onClose, onSaved }: { kind: CapacityResourceKind; act: Act; onClose: () => void; onSaved: () => Promise<void> }) {
  const [label, setLabel] = useState(kind.label)
  const [unit, setUnit] = useState(kind.unit)
  const title = t('capacity.kinds.editNamed', { resource: kind.resource })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const l = label.trim()
    if (!l) {
      act.setError(t('capacity.kinds.labelHelp'))
      return
    }
    void act.run(t('capacity.kinds.saved', { resource: kind.resource }), () => api.put<CapacityResourceKind>(`/capacity/resources/${encodeURIComponent(kind.resource)}`, { label: l, unit: unit.trim() }), onSaved)
  }

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            {t('common.cancel')}
          </button>
          <button className="primary" form="kind-editor" disabled={act.busy}>
            {t('capacity.kinds.save')}
          </button>
        </>
      }
    >
      <form id="kind-editor" className="stack tight" aria-label={title} onSubmit={submit}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <FormRow>
          <Field label={t('capacity.kinds.col.key')} help={t('capacity.kinds.keyFixed')}>
            <input value={kind.resource} readOnly className="mono" />
          </Field>
          <Field label={t('capacity.kinds.col.label')} help={t('capacity.kinds.labelHelp')}>
            <input autoFocus value={label} onChange={(e) => setLabel(e.target.value)} placeholder="GPU cards" />
          </Field>
          <Field label={t('capacity.kinds.col.unit')} help={t('capacity.kinds.unitHelp')}>
            <input value={unit} onChange={(e) => setUnit(e.target.value)} placeholder="cards" />
          </Field>
        </FormRow>
      </form>
    </Modal>
  )
}
