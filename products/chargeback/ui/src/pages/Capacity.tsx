import { useEffect, useMemo, useState, type FormEvent, type MouseEvent } from 'react'
import { api } from '../api/client'
import type { CapacityOverview, CapacityPoolView, CapacityRegion, CapacityRegionView, CapacitySKUView, CapacityZoneView, SKUFootprint, SKUFootprints } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, EmptyState, Field, KPI, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { can } from '../lib/access'
import { FAMILY_ORDER, asOfLabel, familyDef, footprintSummary, formatAmount, formatExhaustion, formatUtilisation, hasTotal, heatClass, parseFootprintForm, parseTotal, zoneRows } from '../lib/capacity'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Plan → Capacity (DESIGN.md §11, founder requirement 2026-09-11). The
 * operator's picture of the cloud underneath: per region and availability
 * zone, one pool per resource family with the total the operator entered
 * (static-first; a capacity collector fills it later), the consumption the
 * latest complete hour of metering implies through the SKU footprints, what
 * is left, and how long it lasts at the present growth. Per SKU, how many
 * more could still be sold in a zone and which family runs out first.
 *
 * Every figure comes from GET /capacity/overview; the page only colours,
 * formats and edits. Reads need metering.read at the Sovereign; totals,
 * footprints, caps, regions and zones are edited with capacity.manage.
 */

const HEAT_LEGEND: ReadonlyArray<[cls: string, label: string]> = [
  ['ok', 'below 70 %'],
  ['warn', '70 – 85 %'],
  ['critical', '85 % and above'],
  ['unset', 'no total entered'],
]

export function Capacity() {
  const { me } = useSession()
  const canManage = can(me, 'capacity.manage')
  const overview = useQuery<CapacityOverview>('/capacity/overview')
  const footprints = useQuery<SKUFootprints>('/capacity/footprints')
  const act = useAction()
  const [region, setRegion] = useState<string>('all')
  const [skuZone, setSkuZone] = useState<string>('')
  const [confirm, setConfirm] = useState<{ kind: 'region'; region: CapacityRegionView } | { kind: 'zone'; zone: CapacityZoneView; region: string } | null>(null)
  const [fpSeed, setFpSeed] = useState<string | null>(null)

  const ov = overview.data
  const families = ov?.families?.length ? ov.families : FAMILY_ORDER.map((f) => familyDef(f))
  const rows = useMemo(() => zoneRows(ov, region), [ov, region])
  const allZones = useMemo(() => zoneRows(ov), [ov])
  const selectedZone = allZones.find((r) => r.zone.id === skuZone) ?? rows[0] ?? allZones[0] ?? null
  const summary = ov?.summary
  const asOf = asOfLabel(ov?.as_of)

  const reload = async () => {
    await Promise.all([overview.reload(), footprints.reload()])
  }

  const regionOptions = [{ value: 'all', label: `All (${ov?.regions.length ?? 0})` }, ...(ov?.regions ?? []).map((r) => ({ value: r.code, label: r.name ? `${r.code} · ${r.name}` : r.code }))]

  return (
    <div className="stack">
      <PageHeader
        title="Capacity"
        sub={
          <>
            Totals per availability zone and resource family, entered here until a capacity collector fills them · consumption derived from the latest complete hour of metering through the SKU footprints
            {asOf ? <> · as of {asOf}</> : null}
          </>
        }
      />
      {overview.error ? <Notice kind="bad">{overview.error}</Notice> : null}
      {footprints.error ? <Notice kind="bad">{footprints.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {!canManage ? (
        <Notice kind="info">
          Read-only: totals, footprints, caps, regions and zones are edited with <code>capacity.manage</code> (sovereign-admin, billing-operator).
        </Notice>
      ) : null}

      <div className="kpis">
        <KPI label="Regions" value={summary?.regions ?? '…'} note={summary ? `${summary.zones} zone${summary.zones === 1 ? '' : 's'}` : undefined} />
        <KPI
          label="Pools past 70 %"
          value={summary?.pools_below_threshold ?? '…'}
          note={summary ? `${summary.pools_critical} critical · ${summary.pools_with_total} of ${summary.pools} pools sized` : undefined}
          tone={summary?.pools_critical ? 'bad' : summary?.pools_below_threshold ? 'warn' : undefined}
        />
        <KPI
          label="SKUs without a footprint"
          value={summary?.unmapped_skus ?? '…'}
          note={summary?.unmapped_skus ? 'metered, but counted against no pool' : `${summary?.skus ?? 0} SKUs carry a footprint`}
          tone={summary?.unmapped_skus ? 'warn' : undefined}
        />
        <KPI
          label="Measured at"
          value={asOf || '—'}
          note={ov ? (ov.as_of ? `${ov.sources} cloud source${ov.sources === 1 ? '' : 's'}${ov.lagging_sources ? ` · ${ov.lagging_sources} lagging` : ''}` : 'no cloud usage metered yet') : undefined}
        />
      </div>

      {overview.loading && !ov ? (
        <div className="card">
          <Skeleton lines={4} />
        </div>
      ) : null}

      {ov && ov.regions.length === 0 ? (
        <div className="card">
          <EmptyState title="No regions yet">
            Capacity is static-first: add the cloud region as the ledger names it (the <code>region</code> of your cloud sources, e.g. <code>me-east-215</code>), its availability zones, then enter each zone's totals per family. Consumption is
            never typed — the latest metered hour is read through the SKU footprints — and a capacity collector will fill the totals from the cloud later, through the same pools.
          </EmptyState>
          {canManage ? <AddRegionForm act={act} onDone={reload} /> : null}
        </div>
      ) : null}

      {ov && ov.unmapped_regions.length > 0 ? (
        <Notice kind="warn">
          <div className="stack tight">
            {ov.unmapped_regions.map((u) => (
              <div key={u.region} className="row between">
                <span>
                  Metered usage in <code>{u.region || '(no region)'}</code> — {u.skus} SKU{u.skus === 1 ? '' : 's'}, {u.resources} resource{u.resources === 1 ? '' : 's'} — {u.reason === 'no-zones' ? 'has a region here but no zone to land in.' : 'has no region here, so it counts against nothing.'}
                </span>
                {canManage && u.region && u.reason === 'no-region' ? (
                  <button className="small" disabled={act.busy} onClick={() => void act.run(`Region ${u.region} added — now add its zones`, () => api.post<CapacityRegion>('/capacity/regions', { code: u.region }), reload)}>
                    Add region {u.region}
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
              <label>Region</label>
              <Segmented value={region} options={regionOptions} onChange={setRegion} ariaLabel="Region filter" />
            </div>
            <div className="grow" />
            <div className="heat-legend">
              {HEAT_LEGEND.map(([cls, label]) => (
                <span key={cls}>
                  <i className={cls} />
                  {label}
                </span>
              ))}
              <span className="muted-2">· click a cell to set its total</span>
            </div>
          </div>

          <div className="card pad-0">
            <div className="table-wrap">
              <table className="heatmap" aria-label="Capacity by zone and family">
                <thead>
                  <tr>
                    <th>Zone</th>
                    {families.map((f) => (
                      <th key={f.family} title={`${f.label} in ${f.unit}`}>
                        {f.label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map(({ region: rc, zone }) => (
                    <tr key={zone.id}>
                      <td>
                        <b>{zone.code}</b>
                        {zone.is_default ? (
                          <>
                            {' '}
                            <Badge status="default" kind="info" />
                          </>
                        ) : null}
                        <span className="sub">
                          {rc}
                          {zone.name ? ` · ${zone.name}` : ''}
                        </span>
                      </td>
                      {families.map((f) => {
                        const pool = zone.pools.find((p) => p.family === f.family)
                        return pool ? <HeatCell key={f.family} pool={pool} canManage={canManage} act={act} onSaved={reload} /> : <td key={f.family} className="heat unset">—</td>
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="table-foot">
              <span>
                {rows.length} zone{rows.length === 1 ? '' : 's'} · available = total − reserved − consumed, never below 0 · reserved is 0 until proposals fill it · "runs out" is available ÷ the 7-day growth of consumed
              </span>
            </div>
          </div>

          <div className="grid side">
            <div className="card pad-0">
              <div className="card-head" style={{ padding: '12px 16px 0' }}>
                <h2>SKU headroom</h2>
                <span className="hint">
                  how many more units each SKU fits in{' '}
                  <select aria-label="Headroom zone" value={selectedZone?.zone.id ?? ''} onChange={(e) => setSkuZone(e.target.value)} style={{ width: 'auto', display: 'inline-block', padding: '2px 6px' }}>
                    {allZones.map((r) => (
                      <option key={r.zone.id} value={r.zone.id}>
                        {r.region} / {r.zone.code}
                      </option>
                    ))}
                  </select>
                </span>
              </div>
              {selectedZone ? <SKUTable zone={selectedZone.zone} families={ov.families} canManage={canManage} act={act} onSaved={reload} /> : <EmptyState title="No zone" />}
            </div>
            <RegionsCard regions={ov.regions} canManage={canManage} act={act} onDone={reload} onDelete={setConfirm} />
          </div>
        </>
      ) : null}

      {ov && ov.unmapped_skus.length > 0 ? (
        <div className="card">
          <div className="card-head">
            <h2>Unmapped SKUs</h2>
            <span className="hint">metered in the latest hour, but no footprint says what they consume — their usage counts against no pool</span>
          </div>
          <DataTable
            label="Unmapped SKUs"
            columns={[
              { key: 'sku', header: 'SKU', value: (u) => u.sku, render: (u) => <code>{u.sku}</code> },
              { key: 'qty', header: 'Quantity / h', value: (u) => Number(u.quantity), numeric: true, render: (u) => `${formatAmount(u.quantity)} ${u.unit}` },
              { key: 'res', header: 'Resources', value: (u) => u.resources, numeric: true },
              { key: 'regions', header: 'Regions', value: (u) => u.regions.join(', ') },
              {
                key: 'act',
                header: '',
                value: () => '',
                sortable: false,
                className: 'actions',
                render: (u) =>
                  canManage ? (
                    <button className="small primary" onClick={() => setFpSeed(u.sku)}>
                      Add footprint
                    </button>
                  ) : null,
              },
            ]}
            rows={ov.unmapped_skus}
            rowKey={(u) => u.sku}
            defaultSort={{ key: 'qty', dir: 'desc' }}
          />
        </div>
      ) : null}

      <FootprintsCard doc={footprints.data} loading={footprints.loading} canManage={canManage} act={act} onSaved={reload} seedSku={fpSeed} onSeedConsumed={() => setFpSeed(null)} />

      {confirm?.kind === 'region' ? (
        <Confirm
          title={`Delete region ${confirm.region.code}?`}
          body={`Its ${confirm.region.zones.length} zone${confirm.region.zones.length === 1 ? '' : 's'}, every pool total with its history and every SKU cap go with it. Metered usage in ${confirm.region.code} will read as unmapped.`}
          confirmLabel="Delete region"
          danger
          busy={act.busy}
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            const ok = await act.run(`Region ${confirm.region.code} deleted`, () => api.del(`/capacity/regions/${confirm.region.id}`), reload)
            if (ok) setConfirm(null)
          }}
        />
      ) : null}
      {confirm?.kind === 'zone' ? (
        <Confirm
          title={`Delete zone ${confirm.zone.code}?`}
          body={`Its pools, their total history and its SKU caps go with it.${confirm.zone.is_default ? ' It is the default zone: the oldest remaining zone takes over unknown-zone usage.' : ''}`}
          confirmLabel="Delete zone"
          danger
          busy={act.busy}
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            const ok = await act.run(`Zone ${confirm.zone.code} deleted`, () => api.del(`/capacity/zones/${confirm.zone.id}`), reload)
            if (ok) setConfirm(null)
          }}
        />
      ) : null}
    </div>
  )
}

type Act = ReturnType<typeof useAction>

/** One heatmap cell: utilisation, what is left, when it runs out; click to set the total. */
function HeatCell({ pool, canManage, act, onSaved }: { pool: CapacityPoolView; canManage: boolean; act: Act; onSaved: () => Promise<void> }) {
  const [editing, setEditing] = useState(false)
  const [total, setTotal] = useState(String(pool.total))
  const [note, setNote] = useState(pool.note)
  const [err, setErr] = useState('')
  const sized = hasTotal(pool)
  const unknown = Number(pool.zone_unknown) > 0
  const title = [
    `${pool.label} · total ${formatAmount(pool.total)} ${pool.unit} · reserved ${formatAmount(pool.reserved)} · consumed ${formatAmount(pool.consumed)} · available ${formatAmount(pool.available)}`,
    pool.clamped ? `over-committed by ${formatAmount(pool.overcommit)} ${pool.unit}` : '',
    unknown ? `${formatAmount(pool.zone_unknown)} ${pool.unit} attributed here because the resource's zone is unknown` : '',
    pool.growth_per_day !== null ? `growth ${formatAmount(pool.growth_per_day, 3)} ${pool.unit}/day over ${pool.history_days} complete day${pool.history_days === 1 ? '' : 's'}` : 'growth unknown: fewer than 3 complete days of history',
    pool.note ? `note: ${pool.note}` : '',
    pool.updated_by ? `set by ${pool.updated_by} (${pool.source})` : '',
  ]
    .filter(Boolean)
    .join('\n')

  const start = () => {
    if (!canManage || editing) return
    setTotal(String(pool.total))
    setNote(pool.note)
    setErr('')
    setEditing(true)
  }
  const save = async (e: FormEvent) => {
    e.preventDefault()
    const p = parseTotal(total)
    if (p.error) {
      setErr(p.error)
      return
    }
    const ok = await act.run(`${pool.label} total set to ${formatAmount(p.total)} ${pool.unit}`, () => api.put(`/capacity/pools/${pool.id}`, { total: p.total, note }), onSaved)
    if (ok) setEditing(false)
  }
  const stop = (e: MouseEvent) => e.stopPropagation()

  return (
    <td className={`${heatClass(pool.status)} ${canManage ? 'editable' : ''}`} onClick={start} title={editing ? undefined : title} data-status={pool.status}>
      {editing ? (
        <form className="edit" onSubmit={save} onClick={stop} aria-label={`Set ${pool.label} total`}>
          <input aria-label="Total" value={total} onChange={(e) => setTotal(e.target.value)} placeholder={`total in ${pool.unit}`} autoFocus />
          <input aria-label="Note" value={note} onChange={(e) => setNote(e.target.value)} placeholder="note (where the number comes from)" />
          {err ? <span className="err small">{err}</span> : null}
          <span className="btn-row">
            <button type="button" className="small" onClick={() => setEditing(false)} disabled={act.busy}>
              Cancel
            </button>
            <button type="submit" className="small primary" disabled={act.busy}>
              Save
            </button>
          </span>
        </form>
      ) : (
        <>
          <div className="pct">
            <span>{formatUtilisation(pool.utilisation_pct)}</span>
            {pool.clamped ? <Badge status="over" kind="bad" /> : null}
          </div>
          {sized ? (
            <div className="line">
              <b>{formatAmount(pool.available)}</b> {pool.unit} left of {formatAmount(pool.total)}
            </div>
          ) : (
            <div className="line">
              no total · <b>{formatAmount(pool.consumed)}</b> {pool.unit} in use
            </div>
          )}
          <div className="line">{sized ? (pool.exhaustion_days !== null ? `runs out in ${formatExhaustion(pool.exhaustion_days)}` : pool.growth_per_day !== null ? 'not growing' : 'growth unknown') : canManage ? 'click to set' : ''}</div>
          {unknown ? <div className="line">{formatAmount(pool.zone_unknown)} zone unknown</div> : null}
        </>
      )}
    </td>
  )
}

/** The SKU headroom of one zone, with the cap editor. */
function SKUTable({ zone, families, canManage, act, onSaved }: { zone: CapacityZoneView; families: CapacityOverview['families']; canManage: boolean; act: Act; onSaved: () => Promise<void> }) {
  const [capSku, setCapSku] = useState('')
  const [capTotal, setCapTotal] = useState('')
  const bindingLabel = (s: CapacitySKUView) => (s.binding_family === 'cap' ? 'cap' : s.binding_family ? familyDef(s.binding_family, families).label : '')
  const columns: Column<CapacitySKUView>[] = [
    { key: 'sku', header: 'SKU', value: (s) => s.sku, render: (s) => <code>{s.sku}</code> },
    {
      key: 'fp',
      header: 'Footprint / unit',
      value: (s) => footprintSummary(s.footprint, families),
      render: (s) => (
        <>
          {footprintSummary(s.footprint, families)}
          <span className="sub">{s.footprint_source === 'derived' ? 'derived from the name' : s.footprint_source}</span>
        </>
      ),
    },
    { key: 'used', header: 'In use', value: (s) => Number(s.consumed_units), numeric: true, render: (s) => (Number(s.consumed_units) ? `${formatAmount(s.consumed_units)} · ${s.resources} res.` : <span className="muted">0</span>) },
    {
      key: 'headroom',
      header: 'Headroom',
      value: (s) => (s.headroom_units === null ? null : Number(s.headroom_units)),
      numeric: true,
      render: (s) =>
        s.headroom_units === null ? (
          <span className="muted" title="no family of this footprint has a total in this zone yet">
            —
          </span>
        ) : (
          <b className={Number(s.headroom_units) === 0 ? 'bad' : ''}>{formatAmount(s.headroom_units, 0)}</b>
        ),
    },
    { key: 'binding', header: 'Binds first', value: (s) => bindingLabel(s), render: (s) => (s.binding_family ? <Badge status={bindingLabel(s)} kind={s.binding_family === 'cap' ? 'info' : undefined} /> : <span className="muted">—</span>) },
    {
      key: 'cap',
      header: 'Cap',
      value: (s) => (s.cap === null ? null : Number(s.cap)),
      numeric: true,
      render: (s) =>
        s.cap === null ? (
          <span className="muted">—</span>
        ) : (
          <>
            {formatAmount(s.cap, 0)}
            {canManage ? (
              <>
                {' '}
                <button className="link small danger" onClick={() => void act.run(`Cap on ${s.sku} removed`, () => api.put('/capacity/caps', { zone_id: zone.id, sku: s.sku, total: null }), onSaved)} disabled={act.busy}>
                  remove
                </button>
              </>
            ) : null}
          </>
        ),
    },
  ]
  return (
    <>
      <DataTable
        label={`SKU headroom in ${zone.code}`}
        columns={columns}
        rows={zone.skus}
        rowKey={(s) => s.sku}
        defaultSort={{ key: 'headroom', dir: 'asc' }}
        pageSize={15}
        csvName={`capacity-headroom-${zone.code}`}
        emptyTitle="No SKU carries a footprint"
        emptyBody="Footprints say how much of each family one unit of a SKU consumes; the National Cloud list SKUs are seeded, others are read from their name or entered below."
      />
      {canManage ? (
        <form
          className="inline"
          style={{ padding: '8px 16px 14px' }}
          aria-label="Cap a SKU"
          onSubmit={(e) => {
            e.preventDefault()
            const p = parseTotal(capTotal)
            if (!capSku || p.error) {
              act.setError(!capSku ? 'choose a SKU to cap' : p.error)
              return
            }
            void act.run(`${capSku} capped at ${formatAmount(p.total, 0)} in ${zone.code}`, () => api.put('/capacity/caps', { zone_id: zone.id, sku: capSku, total: p.total }), async () => {
              setCapSku('')
              setCapTotal('')
              await onSaved()
            })
          }}
        >
          <Field label="Cap a SKU in this zone" help="a direct ceiling in units of the SKU, on top of what the pools allow">
            <select value={capSku} onChange={(e) => setCapSku(e.target.value)}>
              <option value="">choose a SKU…</option>
              {zone.skus.map((s) => (
                <option key={s.sku} value={s.sku}>
                  {s.sku}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Total units">
            <input value={capTotal} onChange={(e) => setCapTotal(e.target.value)} placeholder="e.g. 500" />
          </Field>
          <button type="submit" className="small" disabled={act.busy}>
            Set cap
          </button>
        </form>
      ) : null}
    </>
  )
}

/** Regions and zones: the list, and the forms to add or delete them. */
function RegionsCard({ regions, canManage, act, onDone, onDelete }: { regions: CapacityRegionView[]; canManage: boolean; act: Act; onDone: () => Promise<void>; onDelete: (c: { kind: 'region'; region: CapacityRegionView } | { kind: 'zone'; zone: CapacityZoneView; region: string }) => void }) {
  const [addingZone, setAddingZone] = useState<string | null>(null)
  return (
    <div className="card">
      <div className="card-head">
        <h2>Regions and zones</h2>
        <span className="hint">a region is the code the ledger carries; usage whose zone is unknown lands in the default zone</span>
      </div>
      <div className="stack tight">
        {regions.map((r) => (
          <div key={r.id} style={{ borderBottom: '1px solid var(--line)', paddingBottom: 8 }}>
            <div className="row between">
              <span>
                <b>{r.code}</b>
                {r.name ? <span className="muted"> · {r.name}</span> : null} <Badge status={r.cloud_source_kind} />
                <span className="sub">
                  {r.zones.length} zone{r.zones.length === 1 ? '' : 's'}
                  {r.zones.length ? `: ${r.zones.map((z) => z.code + (z.is_default ? ' (default)' : '')).join(', ')}` : ' — add one, or its usage stays unmapped'}
                </span>
              </span>
              {canManage ? (
                <span className="btn-row">
                  <button className="small" onClick={() => setAddingZone(addingZone === r.id ? null : r.id)}>
                    Add zone
                  </button>
                  <button className="small danger" onClick={() => onDelete({ kind: 'region', region: r })}>
                    Delete
                  </button>
                </span>
              ) : null}
            </div>
            {canManage && r.zones.length ? (
              <div className="row" style={{ marginTop: 4 }}>
                {r.zones.map((z) => (
                  <button key={z.id} className="link small danger" onClick={() => onDelete({ kind: 'zone', zone: z, region: r.code })}>
                    delete {z.code}
                  </button>
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
      aria-label="Add a region"
      onSubmit={(e) => {
        e.preventDefault()
        if (!code.trim()) {
          act.setError('the region code is required — the region as your cloud sources name it')
          return
        }
        void act.run(`Region ${code.trim().toLowerCase()} added — now add its zones`, () => api.post<CapacityRegion>('/capacity/regions', { code: code.trim(), name: name.trim() }), async () => {
          setCode('')
          setName('')
          await onDone()
        })
      }}
    >
      <Field label="Region code" help="as the ledger names it, e.g. me-east-215">
        <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="me-east-215" />
      </Field>
      <Field label="Name">
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Muscat" />
      </Field>
      <button type="submit" className="small primary" disabled={act.busy}>
        Add region
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
      aria-label={`Add a zone to ${region.code}`}
      onSubmit={(e) => {
        e.preventDefault()
        if (!code.trim()) {
          act.setError('the zone code is required — the availability zone as the cloud names it')
          return
        }
        void act.run(`Zone ${code.trim().toLowerCase()} added to ${region.code} with its seven pools at 0`, () => api.post(`/capacity/regions/${region.id}/zones`, { code: code.trim(), name: name.trim(), default: makeDefault }), onDone)
      }}
    >
      <Field label="Zone code" help={`e.g. ${region.code}a`}>
        <input value={code} onChange={(e) => setCode(e.target.value)} placeholder={`${region.code}a`} />
      </Field>
      <Field label="Name">
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="AZ 1" />
      </Field>
      <label className="check" style={{ marginBottom: 10 }}>
        <input type="checkbox" checked={makeDefault} onChange={(e) => setMakeDefault(e.target.checked)} /> default zone
      </label>
      <button type="submit" className="small primary" disabled={act.busy}>
        Add zone
      </button>
    </form>
  )
}

/** The footprints editor: every stored footprint, one row per SKU, with inline edit and an add form. */
function FootprintsCard({ doc, loading, canManage, act, onSaved, seedSku, onSeedConsumed }: { doc: SKUFootprints | null; loading: boolean; canManage: boolean; act: Act; onSaved: () => Promise<void>; seedSku: string | null; onSeedConsumed: () => void }) {
  const families = doc?.families?.length ? doc.families : FAMILY_ORDER.map((f) => familyDef(f))
  const [editing, setEditing] = useState<string | null>(null)
  const [values, setValues] = useState<Record<string, string>>({})
  const [newSku, setNewSku] = useState('')
  const [adding, setAdding] = useState(false)
  // A one-click "Add footprint" on an unmapped SKU opens the add form with
  // the SKU filled in.
  useEffect(() => {
    if (seedSku === null) return
    setAdding(true)
    setNewSku(seedSku)
    setValues({})
    onSeedConsumed()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seedSku])
  const startEdit = (fp: SKUFootprint) => {
    const v: Record<string, string> = {}
    for (const f of families) v[f.family] = fp.families[f.family] !== undefined ? formatAmount(fp.families[f.family], 6).replace(/,/g, '') : ''
    setValues(v)
    setEditing(fp.sku)
  }
  const submit = async (sku: string, done: () => void) => {
    const parsed = parseFootprintForm(values, families)
    if (parsed.error) {
      act.setError(parsed.error)
      return
    }
    if (!sku.trim()) {
      act.setError('the SKU is required')
      return
    }
    const n = Object.keys(parsed.families).length
    const ok = await act.run(n ? `Footprint of ${sku.trim()} saved (${footprintSummary(parsed.families, families)})` : `Footprint of ${sku.trim()} removed`, () => api.put(`/capacity/footprints/${encodeURIComponent(sku.trim())}`, { families: parsed.families }), onSaved)
    if (ok) done()
  }
  const grid = (
    <div className="fp-grid">
      {families.map((f) => (
        <Field key={f.family} label={`${f.label} (${f.unit})`}>
          <input aria-label={`${f.label} per unit`} value={values[f.family] ?? ''} onChange={(e) => setValues({ ...values, [f.family]: e.target.value })} placeholder="0" />
        </Field>
      ))}
    </div>
  )
  const columns: Column<SKUFootprint>[] = [
    { key: 'sku', header: 'SKU', value: (fp) => fp.sku, render: (fp) => <code>{fp.sku}</code> },
    ...families.map<Column<SKUFootprint>>((f) => ({
      key: f.family,
      header: (
        <span title={`${f.label} in ${f.unit}`}>
          {f.label}
        </span>
      ),
      value: (fp) => (fp.families[f.family] === undefined ? null : Number(fp.families[f.family])),
      numeric: true,
      render: (fp) => (fp.families[f.family] === undefined ? <span className="muted-2">·</span> : formatAmount(fp.families[f.family])),
    })),
    { key: 'source', header: 'Source', value: (fp) => fp.source, render: (fp) => <Badge status={fp.source} kind={fp.source === 'seed' ? 'info' : undefined} /> },
    {
      key: 'act',
      header: '',
      value: () => '',
      sortable: false,
      className: 'actions',
      render: (fp) =>
        canManage ? (
          <button className="small" onClick={() => startEdit(fp)}>
            Edit
          </button>
        ) : null,
    },
  ]
  return (
    <div className="card pad-0">
      <div className="card-head" style={{ padding: '12px 16px 0' }}>
        <h2>SKU footprints</h2>
        <span className="hint">
          how much of each family one unit of a SKU consumes · the National Cloud list SKUs are seeded, an ECS flavour is read from its name
          {doc?.unseeded_skus?.length ? <> · no per-unit footprint on the list: {doc.unseeded_skus.join(', ')}</> : null}
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
            Add footprint
          </button>
        ) : null}
      </div>
      {loading && !doc ? (
        <div style={{ padding: 16 }}>
          <Skeleton lines={3} />
        </div>
      ) : (
        <DataTable
          label="SKU footprints"
          columns={columns}
          rows={doc?.footprints ?? []}
          rowKey={(fp) => fp.sku}
          defaultSort={{ key: 'sku', dir: 'asc' }}
          pageSize={20}
          csvName="capacity-footprints"
          emptyTitle="No footprints"
          emptyBody="Without footprints nothing counts against the pools."
          expanded={(fp) =>
            editing === fp.sku ? (
              <form
                aria-label={`Edit footprint ${fp.sku}`}
                onSubmit={(e) => {
                  e.preventDefault()
                  void submit(fp.sku, () => setEditing(null))
                }}
              >
                {grid}
                <div className="row end" style={{ marginTop: 8 }}>
                  <span className="muted small">a blank or 0 removes the family; all blank removes the footprint</span>
                  <button type="button" className="small" onClick={() => setEditing(null)} disabled={act.busy}>
                    Cancel
                  </button>
                  <button type="submit" className="small primary" disabled={act.busy}>
                    Save
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
          aria-label="Add a footprint"
          onSubmit={(e) => {
            e.preventDefault()
            void submit(newSku, () => {
              setAdding(false)
              setNewSku('')
              setValues({})
            })
          }}
        >
          <Field label="SKU" help="as the ledger names it, e.g. nat.1 or rds.storage.ha.gb">
            <input value={newSku} onChange={(e) => setNewSku(e.target.value)} placeholder="nat.1" />
          </Field>
          {grid}
          <div className="row end" style={{ marginTop: 8 }}>
            <button type="button" className="small" onClick={() => setAdding(false)} disabled={act.busy}>
              Cancel
            </button>
            <button type="submit" className="small primary" disabled={act.busy}>
              Save footprint
            </button>
          </div>
        </form>
      ) : null}
    </div>
  )
}
