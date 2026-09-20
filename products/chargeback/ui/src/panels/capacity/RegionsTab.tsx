import { useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import type { CapacityRegion, CapacityRegionView, CapacityZoneView } from '../../api/types'
import { Badge, EmptyState, Field } from '../../components/ui'
import { t } from '../../i18n'
import type { Act } from './shared'

export type RegionDialog = { kind: 'region'; region: CapacityRegionView } | { kind: 'zone'; zone: CapacityZoneView; region: string }

/** Regions and zones: one table, a zone per line, and the forms to add or delete them. */
export function RegionsTab({
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
  onDelete: (d: RegionDialog) => void
  onAddPool: (zoneID: string) => void
}) {
  const [addingZone, setAddingZone] = useState<string | null>(null)
  return (
    <div className="card">
      <div className="card-head">
        <h2>{t('capacity.regions.title')}</h2>
        <span className="hint">{t('capacity.regions.sub')}</span>
      </div>
      {regions.length === 0 ? <EmptyState title={t('capacity.empty.title')}>{t('capacity.empty.body')}</EmptyState> : null}
      {regions.map((r) => (
        <div key={r.id} style={{ borderBottom: '1px solid var(--line)', padding: '8px 0 12px' }} data-region={r.code}>
          <div className="row between">
            <span>
              <b>{r.code}</b>
              {r.name ? <span className="muted"> · {r.name}</span> : null} <Badge status={r.cloud_source_kind} />
            </span>
            {canManage ? (
              <span className="btn-row">
                <button type="button" className="small" onClick={() => setAddingZone(addingZone === r.id ? null : r.id)}>
                  {t('capacity.regions.addZone')}
                </button>
                <button type="button" className="small danger" aria-label={t('capacity.regions.deleteRegionNamed', { code: r.code })} onClick={() => onDelete({ kind: 'region', region: r })}>
                  {t('capacity.pool.delete')}
                </button>
              </span>
            ) : null}
          </div>
          {r.zones.length === 0 ? (
            <span className="muted small">{t('capacity.regions.zonesNone')}</span>
          ) : (
            <div className="table-wrap" style={{ marginTop: 6 }}>
              <table aria-label={t('capacity.regions.zonesOf', { code: r.code })}>
                <thead>
                  <tr>
                    <th>{t('capacity.regions.zoneCode')}</th>
                    <th>{t('capacity.regions.name')}</th>
                    <th>{t('capacity.kpi.pools')}</th>
                    <th />
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
          )}
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
      {canManage ? <AddRegionForm act={act} onDone={onDone} /> : null}
    </div>
  )
}

export function AddRegionForm({ act, onDone }: { act: Act; onDone: () => Promise<void> }) {
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
