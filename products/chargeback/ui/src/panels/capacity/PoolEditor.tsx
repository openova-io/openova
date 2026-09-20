import { useState, type FormEvent } from 'react'
import { api } from '../../api/client'
import type { CapacityClassDef, CapacityPoolView, CapacityResourceKind } from '../../api/types'
import { Field, Modal, Notice } from '../../components/ui'
import { t } from '../../i18n'
import { CLASS_ORDER, DEFAULT_POOL_CLASSES, floorEffect, formatAmount, orderedClasses, parsePoolForm, reserveForMachines, type PoolForm } from '../../lib/capacity'
import { toNumber } from '../../lib/num'
import { classText, type Act } from './shared'

const emptyResource = { resource: '', per_machine: '', reserve: '', overcommit_ratio: '1', guaranteed_floor: '0' }

/** The value the resource select holds while the operator names a kind of their own. */
const OWN_KIND = '\u0000own'

/**
 * The pool editor: a machine count, the classes the pool can ENFORCE, and a
 * per-machine vector.
 *
 * THE ONLY WAY A POOL IS CREATED OR CHANGED, from every entry point. When it
 * is creating, it asks which zone; when it is editing, the zone is the pool's
 * own — moving a pool between zones is a different operation from re-sizing
 * one, and PUT /capacity/pools/{id} does not do it.
 *
 * THE CLASSES COME FIRST because they decide what the rest of the form shows.
 * Overcommit and the guaranteed floor are burstable's two numbers — how far
 * burstable is oversold, and what it is kept out of — so their columns exist
 * only while burstable is ticked. A pool that cannot throttle has nothing to
 * overcommit, and a form that showed the inputs anyway would be offering a
 * product nobody can deliver.
 */
export function PoolEditor({
  zoneID,
  zones,
  pool,
  kinds,
  classes,
  act,
  onClose,
  onSaved,
}: {
  zoneID: string
  zones: Array<{ id: string; label: string }>
  pool: CapacityPoolView | null
  kinds: CapacityResourceKind[]
  classes: CapacityClassDef[]
  act: Act
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [zone, setZone] = useState(zoneID)
  const [form, setForm] = useState<PoolForm>(() => ({
    name: pool?.name ?? '',
    machines: pool ? String(toNumber(pool.machines)) : '',
    classes: pool ? orderedClasses(pool.classes) : [...DEFAULT_POOL_CLASSES],
    lead_time_days: String(pool?.lead_time_days ?? 0),
    note: pool?.note ?? '',
    resources: pool?.resources.length
      ? pool.resources.map((r) => ({
          resource: r.resource,
          per_machine: String(toNumber(r.per_machine)),
          reserve: String(toNumber(r.reserve)),
          overcommit_ratio: String(toNumber(r.overcommit_ratio)),
          guaranteed_floor: String(toNumber(r.guaranteed_floor)),
        }))
      : [{ ...emptyResource }],
  }))
  // Which rows are naming a resource kind of the operator's own.
  const [own, setOwn] = useState<Record<number, boolean>>({})
  const [err, setErr] = useState('')
  const burstable = form.classes.includes('burstable')
  // A class placements still use cannot be withdrawn; say so at the checkbox
  // instead of letting the save fail.
  const inUse = new Set((pool?.placements ?? []).map((p) => p.class))

  const setRes = (i: number, patch: Partial<PoolForm['resources'][number]>) => {
    setForm({ ...form, resources: form.resources.map((r, j) => (i === j ? { ...r, ...patch } : r)) })
  }
  const toggleClass = (c: string) => {
    const has = form.classes.includes(c)
    setForm({ ...form, classes: orderedClasses(has ? form.classes.filter((x) => x !== c) : [...form.classes, c]) })
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
    await act.run(label, () => (pool ? api.put(`/capacity/pools/${pool.id}`, parsed.body) : api.post(`/capacity/zones/${zone}/pools`, parsed.body)), onSaved)
  }

  const taken = (i: number) => new Set(form.resources.filter((_, j) => j !== i).map((r) => r.resource))

  return (
    <Modal title={pool ? t('capacity.pool.editNamed', { name: pool.name }) : t('capacity.pool.add')} onClose={onClose} wide>
      <form onSubmit={submit} className="stack tight" aria-label={pool ? t('capacity.pool.edit') : t('capacity.pool.add')}>
        {err ? <Notice kind="bad">{err}</Notice> : null}
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
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
            <input autoFocus value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="m7n-a" />
          </Field>
          <Field label={t('capacity.form.machines')} help={t('capacity.form.machinesHelp')}>
            <input inputMode="decimal" value={form.machines} onChange={(e) => setForm({ ...form, machines: e.target.value })} placeholder="10" />
          </Field>
          <Field label={t('capacity.form.leadTime')} help={t('capacity.form.leadTimeHelp')}>
            <input inputMode="numeric" value={form.lead_time_days} onChange={(e) => setForm({ ...form, lead_time_days: e.target.value })} placeholder="45" />
          </Field>
          <Field label={t('capacity.form.note')} help={t('capacity.form.noteHelp')}>
            <input value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />
          </Field>
        </div>

        <fieldset className="stack tight" aria-label={t('capacity.classes.title')}>
          <legend>
            <b>{t('capacity.classes.title')}</b> <span className="muted small">{t('capacity.classes.sub')}</span>
          </legend>
          {CLASS_ORDER.map((c) => {
            const def = classes.find((d) => d.class === c)
            const locked = form.classes.includes(c) && inUse.has(c)
            return (
              <label key={c} className="check" title={locked ? t('capacity.classes.inUse', { class: classText(c, classes) }) : undefined}>
                <input type="checkbox" checked={form.classes.includes(c)} disabled={locked} onChange={() => toggleClass(c)} /> <b>{classText(c, classes)}</b>
                <span className="muted small">
                  {' '}
                  — {def?.requires ?? def?.note ?? ''}
                  {locked ? ` · ${t('capacity.classes.inUse', { class: classText(c, classes) })}` : ''}
                </span>
              </label>
            )
          })}
        </fieldset>

        <div className="table-wrap">
          <table aria-label={t('capacity.form.vector')}>
            <thead>
              <tr>
                <th>{t('capacity.form.resource')}</th>
                <th>{t('capacity.form.perMachine')}</th>
                <th>{t('capacity.form.reserve')}</th>
                {burstable ? <th title={t('capacity.form.ratioHelp')}>{t('capacity.form.ratio')}</th> : null}
                {burstable ? <th title={t('capacity.floor.help')}>{t('capacity.floor.title')}</th> : null}
                <th />
              </tr>
            </thead>
            <tbody>
              {form.resources.map((r, i) => {
                const used = taken(i)
                const known = kinds.some((k) => k.resource === r.resource)
                const naming = own[i] || (r.resource !== '' && !known)
                const effect = burstable ? floorEffect(form.machines, r.per_machine, r.reserve, r.overcommit_ratio, r.guaranteed_floor) : null
                return (
                  <tr key={i}>
                    <td>
                      <select
                        aria-label={`${t('capacity.form.resource')} ${i + 1}`}
                        value={naming ? OWN_KIND : r.resource}
                        onChange={(e) => {
                          const v = e.target.value
                          setOwn({ ...own, [i]: v === OWN_KIND })
                          setRes(i, { resource: v === OWN_KIND ? '' : v })
                        }}
                      >
                        <option value="">{t('capacity.form.chooseResource')}</option>
                        {kinds.map((k) => (
                          <option key={k.resource} value={k.resource} disabled={used.has(k.resource)}>
                            {k.label}
                            {k.unit ? ` (${k.unit})` : ''}
                          </option>
                        ))}
                        <option value={OWN_KIND}>{t('capacity.form.ownResource')}</option>
                      </select>
                      {naming ? (
                        <input aria-label={`${t('capacity.form.ownResourceKey')} ${i + 1}`} value={r.resource} onChange={(e) => setRes(i, { resource: e.target.value })} placeholder="gpu_cards" style={{ marginTop: 4 }} />
                      ) : null}
                    </td>
                    <td>
                      <input inputMode="decimal" aria-label={`${t('capacity.form.perMachine')} ${i + 1}`} value={r.per_machine} onChange={(e) => setRes(i, { per_machine: e.target.value })} placeholder="64" />
                    </td>
                    <td>
                      <input inputMode="decimal" aria-label={`${t('capacity.form.reserve')} ${i + 1}`} value={r.reserve} onChange={(e) => setRes(i, { reserve: e.target.value })} placeholder="0" />
                      <button type="button" className="link small" title={t('capacity.form.nPlusOneHelp')} onClick={() => setRes(i, { reserve: reserveForMachines(r.per_machine, 1) })}>
                        {t('capacity.form.nPlusOne')}
                      </button>
                    </td>
                    {burstable ? (
                      <td>
                        <input inputMode="decimal" aria-label={`${t('capacity.form.ratio')} ${i + 1}`} value={r.overcommit_ratio} onChange={(e) => setRes(i, { overcommit_ratio: e.target.value })} placeholder="1" />
                      </td>
                    ) : null}
                    {burstable ? (
                      <td>
                        <input inputMode="decimal" aria-label={`${t('capacity.floor.title')} ${i + 1}`} value={r.guaranteed_floor} onChange={(e) => setRes(i, { guaranteed_floor: e.target.value })} placeholder="0" />
                        {effect ? (
                          <span className="sub" data-floor-effect>
                            {t('capacity.floor.effect', { floor: formatAmount(effect.floor), envelope: formatAmount(effect.envelope) })}
                          </span>
                        ) : null}
                      </td>
                    ) : null}
                    <td className="actions">
                      {form.resources.length > 1 ? (
                        <button type="button" className="link small danger" onClick={() => setForm({ ...form, resources: form.resources.filter((_, j) => j !== i) })}>
                          {t('capacity.form.removeResource')}
                        </button>
                      ) : null}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        <span className="muted small">
          {t('capacity.form.reserveHelp')}
          {burstable ? ` · ${t('capacity.form.ratioHelp')} · ${t('capacity.floor.help')}` : ` · ${t('capacity.classes.noBurstable')}`}
        </span>
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
    </Modal>
  )
}
