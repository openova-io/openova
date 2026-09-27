import { useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import type { CapacityRegion, CapacityRegionView, CapacityZone, CapacityZoneView } from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { Badge, Field, FormRow, Modal, Notice } from '../../components/ui'
import { t } from '../../i18n'
import { CLOUD_SOURCE_KINDS, sourceKindLabel } from '../../lib/layers'
import type { Act } from './shared'

export type RegionDialog = { kind: 'region'; region: CapacityRegionView } | { kind: 'zone'; zone: CapacityZoneView; region: string }

/** What the region and zone editors open on: a new one, or an existing one to edit. */
export type RegionEditorState = { kind: 'region'; region: CapacityRegionView | null } | { kind: 'zone'; regionID: string; zone: CapacityZoneView | null }

/**
 * Regions and zones: ONE ROW PER REGION — code, name, cloud source kind, how
 * many zones and pools — opening to its zones. Every add and edit is a
 * dialog opened from the row it belongs to: a zone from its region's row, a
 * region from the list header; nothing is a bare form on the page (#6946).
 */
export function RegionsTab({
  regions,
  canManage,
  onOpen,
  onDelete,
  onAddPool,
}: {
  regions: CapacityRegionView[]
  canManage: boolean
  onOpen: (e: RegionEditorState) => void
  onDelete: (d: RegionDialog) => void
  onAddPool: (zoneID: string) => void
}) {
  // Every region starts OPEN: the tab exists to show where the pools live,
  // and one to three regions do not need a click each to say so.
  const [closed, setClosed] = useState<ReadonlySet<string>>(() => new Set())
  const isOpen = (r: CapacityRegionView) => !closed.has(r.id)
  const toggle = (r: CapacityRegionView) =>
    setClosed((s) => {
      const next = new Set(s)
      if (next.has(r.id)) next.delete(r.id)
      else next.add(r.id)
      return next
    })

  const columns: Column<CapacityRegionView>[] = [
    {
      key: 'code',
      header: t('capacity.regions.regionCode'),
      value: (r) => r.code,
      render: (r) => (
        <button type="button" className="link" aria-expanded={isOpen(r)} aria-label={isOpen(r) ? t('capacity.regions.close', { code: r.code }) : t('capacity.regions.open', { code: r.code })} onClick={(e) => { e.stopPropagation(); toggle(r) }}>
          <span aria-hidden>{isOpen(r) ? '▾' : '▸'}</span> <b>{r.code}</b>
        </button>
      ),
    },
    { key: 'name', header: t('capacity.regions.name'), value: (r) => r.name, render: (r) => r.name || <span className="muted">{t('common.none')}</span> },
    { key: 'kind', header: t('capacity.regions.cloudSourceKind'), value: (r) => r.cloud_source_kind, render: (r) => <Badge status={sourceKindLabel(r.cloud_source_kind)} /> },
    { key: 'zones', header: t('capacity.regions.zones'), value: (r) => r.zones.length, numeric: true },
    { key: 'pools', header: t('capacity.kpi.pools'), value: (r) => r.zones.reduce((n, z) => n + z.pools.length, 0), numeric: true },
    {
      key: 'act',
      header: '',
      value: () => '',
      sortable: false,
      className: 'actions',
      render: (r) =>
        canManage ? (
          <span className="btn-row" onClick={(e) => e.stopPropagation()}>
            <button type="button" className="small" aria-label={t('capacity.regions.addZoneTo', { code: r.code })} onClick={() => onOpen({ kind: 'zone', regionID: r.id, zone: null })}>
              {t('capacity.regions.addZone')}
            </button>
            <button type="button" className="small" aria-label={t('capacity.regions.editRegionNamed', { code: r.code })} onClick={() => onOpen({ kind: 'region', region: r })}>
              {t('capacity.regions.edit')}
            </button>
            <button type="button" className="small danger" aria-label={t('capacity.regions.deleteRegionNamed', { code: r.code })} onClick={() => onDelete({ kind: 'region', region: r })}>
              {t('capacity.pool.delete')}
            </button>
          </span>
        ) : null,
    },
  ]

  /** The zones of an open region: code with the default badge, name, pools, and the row's own actions. */
  const zonesOf = (r: CapacityRegionView) =>
    r.zones.length === 0 ? (
      <span className="muted small">{t('capacity.regions.zonesNone')}</span>
    ) : (
      <div className="table-wrap">
        <table aria-label={t('capacity.regions.zonesOf', { code: r.code })}>
          <thead>
            <tr>
              <th>{t('capacity.regions.zoneCode')}</th>
              <th>{t('capacity.regions.name')}</th>
              <th>{t('capacity.kpi.pools')}</th>
              <th className="actions" />
            </tr>
          </thead>
          <tbody>
            {r.zones.map((z) => (
              <tr key={z.id} data-zone={z.code}>
                <td>
                  <b>{z.code}</b> {z.is_default ? <Badge status={t('capacity.regions.defaultZone')} kind="info" /> : null}
                </td>
                <td>{z.name || <span className="muted">{t('common.none')}</span>}</td>
                <td>{z.pools.length === 0 ? <span className="muted">{t('capacity.zone.poolsNone')}</span> : z.pools.map((p) => p.name).join(', ')}</td>
                <td className="actions">
                  {canManage ? (
                    <span className="btn-row">
                      <button type="button" className="small" aria-label={t('capacity.regions.addPoolIn', { code: z.code })} onClick={() => onAddPool(z.id)}>
                        {t('capacity.pool.add')}
                      </button>
                      <button type="button" className="small" aria-label={t('capacity.regions.editZoneNamed', { code: z.code })} onClick={() => onOpen({ kind: 'zone', regionID: r.id, zone: z })}>
                        {t('capacity.regions.edit')}
                      </button>
                      <button type="button" className="small danger" aria-label={t('capacity.regions.deleteZoneNamed', { code: z.code })} onClick={() => onDelete({ kind: 'zone', zone: z, region: r.code })}>
                        {t('capacity.pool.delete')}
                      </button>
                    </span>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    )

  return (
    <div className="card pad-0">
      <div className="card-head" style={{ padding: '12px 16px 0' }}>
        <h2>{t('capacity.regions.title')}</h2>
        <span className="hint">{t('capacity.regions.sub')}</span>
        {canManage ? (
          <button type="button" className="small primary" onClick={() => onOpen({ kind: 'region', region: null })}>
            {t('capacity.regions.addRegion')}
          </button>
        ) : null}
      </div>
      <DataTable
        label={t('capacity.regions.title')}
        columns={columns}
        rows={regions}
        rowKey={(r) => r.id}
        defaultSort={{ key: 'code', dir: 'asc' }}
        onRowClick={toggle}
        emptyTitle={t('capacity.empty.title')}
        emptyBody={t('capacity.empty.body')}
        expanded={(r) => (isOpen(r) ? <div data-region={r.code}>{zonesOf(r)}</div> : null)}
      />
    </div>
  )
}

/** Add a region, or edit one: code, name and cloud source kind, in a dialog. */
export function RegionEditor({ region, act, onClose, onSaved }: { region: CapacityRegionView | null; act: Act; onClose: () => void; onSaved: () => Promise<void> }) {
  const [code, setCode] = useState(region?.code ?? '')
  const [name, setName] = useState(region?.name ?? '')
  const [kind, setKind] = useState(region?.cloud_source_kind ?? CLOUD_SOURCE_KINDS[0].value)
  const title = region ? t('capacity.regions.editRegionNamed', { code: region.code }) : t('capacity.regions.addRegion')

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const c = code.trim().toLowerCase()
    if (!c) {
      act.setError(t('capacity.regions.regionCodeHelp'))
      return
    }
    const body = { code: c, name: name.trim(), cloud_source_kind: kind }
    void act.run(
      region ? t('capacity.regions.regionSaved', { code: c }) : t('capacity.regions.regionAdded', { code: c }),
      () => (region ? api.put<CapacityRegion>(`/capacity/regions/${region.id}`, body) : api.post<CapacityRegion>('/capacity/regions', body)),
      onSaved,
    )
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
          <button className="primary" form="region-editor" disabled={act.busy}>
            {region ? t('capacity.regions.save') : t('capacity.regions.addRegion')}
          </button>
        </>
      }
    >
      <form id="region-editor" className="stack tight" aria-label={title} onSubmit={submit}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <FormRow>
          <Field label={t('capacity.regions.regionCode')} help={region ? t('capacity.regions.codeChangeHelp') : t('capacity.regions.regionCodeHelp')}>
            <input autoFocus value={code} onChange={(e) => setCode(e.target.value)} placeholder="me-east-215" />
          </Field>
          <Field label={t('capacity.regions.name')}>
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Muscat" />
          </Field>
          <Field label={t('capacity.regions.cloudSourceKind')} help={t('capacity.regions.cloudSourceKindHelp')}>
            <select value={kind} onChange={(e) => setKind(e.target.value)}>
              {CLOUD_SOURCE_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </select>
          </Field>
        </FormRow>
      </form>
    </Modal>
  )
}

/**
 * Add a zone to a region, or edit one. The region is preselected from the
 * row the dialog opened on and stays selectable for a new zone; an existing
 * zone stays in its region. The default flag is LOCKED on the region's
 * default zone — a region always has one — so the way to move it is to tick
 * it on another zone.
 */
export function ZoneEditor({
  regions,
  regionID,
  zone,
  act,
  onClose,
  onSaved,
}: {
  regions: CapacityRegionView[]
  regionID: string
  zone: CapacityZoneView | null
  act: Act
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [region, setRegion] = useState(regionID)
  const [code, setCode] = useState(zone?.code ?? '')
  const [name, setName] = useState(zone?.name ?? '')
  const [makeDefault, setMakeDefault] = useState(zone?.is_default ?? false)
  const chosen = regions.find((r) => r.id === region)
  const isTheDefault = Boolean(zone?.is_default)
  const title = zone ? t('capacity.regions.editZoneNamed', { code: zone.code }) : chosen ? t('capacity.regions.addZoneTo', { code: chosen.code }) : t('capacity.regions.addZone')

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const c = code.trim().toLowerCase()
    if (!c) {
      act.setError(t('capacity.regions.zoneCode'))
      return
    }
    if (zone) {
      // The flag is sent only when it changed: the default zone cannot give
      // it up, and the server would say so.
      const body: Record<string, unknown> = { code: c, name: name.trim() }
      if (makeDefault !== zone.is_default) body.default = makeDefault
      void act.run(t('capacity.regions.zoneSaved', { code: c }), () => api.put<CapacityZone>(`/capacity/zones/${zone.id}`, body), onSaved)
      return
    }
    if (!chosen) return
    void act.run(t('capacity.regions.zoneAdded', { code: c, region: chosen.code }), () => api.post<CapacityZone>(`/capacity/regions/${chosen.id}/zones`, { code: c, name: name.trim(), default: makeDefault }), onSaved)
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
          <button className="primary" form="zone-editor" disabled={act.busy}>
            {zone ? t('capacity.regions.save') : t('capacity.regions.addZone')}
          </button>
        </>
      }
    >
      <form id="zone-editor" className="stack tight" aria-label={title} onSubmit={submit}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <FormRow>
          <Field label={t('common.region')} help={zone ? t('capacity.regions.regionFixed') : undefined}>
            <select aria-label={t('common.region')} value={region} disabled={Boolean(zone)} onChange={(e) => setRegion(e.target.value)}>
              {regions.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name ? `${r.code} · ${r.name}` : r.code}
                </option>
              ))}
            </select>
          </Field>
        </FormRow>
        <FormRow>
          <Field label={t('capacity.regions.zoneCode')} help={chosen ? `${chosen.code}a` : undefined}>
            <input autoFocus value={code} onChange={(e) => setCode(e.target.value)} placeholder={chosen ? `${chosen.code}a` : ''} />
          </Field>
          <Field label={t('capacity.regions.name')}>
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="AZ 1" />
          </Field>
          <label className="check" title={isTheDefault ? t('capacity.regions.defaultLocked') : undefined}>
            <input type="checkbox" checked={makeDefault} disabled={isTheDefault} onChange={(e) => setMakeDefault(e.target.checked)} /> {t('capacity.regions.defaultZone')}
          </label>
        </FormRow>
        {isTheDefault ? <span className="muted small">{t('capacity.regions.defaultLocked')}</span> : null}
      </form>
    </Modal>
  )
}
