import { useMemo, useState, type FormEvent } from 'react'
import { api, errorText } from '../api/client'
import type { PriceItem, PriceTier } from '../api/types'
import { TIER_MODES, explainItem, hasShape, priceQuantity, tierProblem, trimZeros } from '../lib/contracts'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { Field, Modal, Notice } from './ui'

// The RATING SHAPES on a price-book item (DESIGN.md §15.1-15.2): the volume
// tier ladder with its mode, and the allowance the plan includes — edited
// here, with the item's effective price explained IN WORDS underneath and a
// sample volume priced by the same arithmetic the engine uses.
//
// The explanation is not decoration. A ladder is the one thing in a price
// book that an operator cannot read off the numbers: "0.010 / 0.008 / 0.006"
// says nothing about whether crossing 10,240 reprices the lot. The sentence
// says it, and `lib/contracts` produces it from the same rules the Go engine
// rates by, with both sides asserting the same string.

/** Form state for one item's shapes — strings, as typed. */
export interface TermsDraft {
  mode: 'graduated' | 'all_units'
  tiers: Array<{ up_to: string; price: string }>
  allowance: string
  rollover: boolean
  /** A volume the operator can price to see what the ladder does. */
  sample: string
}

export function termsDraftFrom(it: PriceItem): TermsDraft {
  const tiers = (it.tiers ?? []).map((t) => ({
    up_to: t.up_to === null || t.up_to === undefined || t.up_to === '' ? '' : trimZeros(String(toNumber(t.up_to))),
    price: trimZeros(String(toNumber(t.price))),
  }))
  return {
    mode: it.tier_mode === 'all_units' ? 'all_units' : 'graduated',
    tiers,
    allowance: toNumber(it.allowance) > 0 ? trimZeros(String(toNumber(it.allowance))) : '',
    rollover: Boolean(it.allowance_rollover),
    sample: '',
  }
}

/** The draft as the API's PATCH body — empty bands clear the ladder. */
export function termsBody(d: TermsDraft) {
  const tiers: PriceTier[] = d.tiers
    .filter((t) => t.price !== '')
    .map((t) => ({ up_to: t.up_to === '' ? null : t.up_to, price: t.price }))
  return {
    tier_mode: tiers.length ? d.mode : '',
    tiers,
    allowance: d.allowance === '' ? null : d.allowance,
    allowance_rollover: d.rollover,
  }
}

/** The draft as a PriceItem, for the live explanation and the sample price. */
export function previewItem(it: PriceItem, d: TermsDraft): PriceItem {
  const body = termsBody(d)
  return { ...it, tier_mode: body.tier_mode, tiers: body.tiers, allowance: body.allowance, allowance_rollover: body.allowance_rollover }
}

/**
 * The sentence under a row: what this item charges, in words. Rendered in the
 * items table for every shaped item and live inside the editor.
 */
export function ExplainedPrice({ item, currency }: { item: PriceItem; currency: string }) {
  return (
    <div className="help" data-testid="explained-price">
      {explainItem(item, currency)}
    </div>
  )
}

/** "Tiered · 3 bands" / "50 gb included" — the badge on a shaped row. */
export function shapeSummary(it: PriceItem): string {
  const bits: string[] = []
  const n = (it.tiers ?? []).length
  if (n) bits.push(`${it.tier_mode === 'all_units' ? 'All units' : 'Graduated'} · ${n} band${n === 1 ? '' : 's'}`)
  const allowance = toNumber(it.allowance)
  if (allowance > 0) bits.push(`${trimZeros(String(allowance))} ${it.unit} included${it.allowance_rollover ? ', rolls over' : ''}`)
  return bits.join(' · ')
}

export { hasShape }

/**
 * The editor. The caller performs no request: this one PATCHes the item and
 * reports the saved row back, because the shape and the price live on the
 * same endpoint and splitting them would let one save without the other.
 */
export function PriceItemTermsModal({
  bookId,
  item,
  currency,
  onClose,
  onSaved,
}: {
  bookId: string
  item: PriceItem
  currency: string
  onClose: () => void
  onSaved: (saved: PriceItem) => void | Promise<void>
}) {
  const [draft, setDraft] = useState<TermsDraft>(() => termsDraftFrom(item))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const set = (patch: Partial<TermsDraft>) => setDraft((d) => ({ ...d, ...patch }))
  const setTier = (i: number, patch: Partial<{ up_to: string; price: string }>) =>
    setDraft((d) => ({ ...d, tiers: d.tiers.map((t, k) => (k === i ? { ...t, ...patch } : t)) }))
  const addTier = () => setDraft((d) => ({ ...d, tiers: [...d.tiers, { up_to: '', price: '' }] }))
  const removeTier = (i: number) => setDraft((d) => ({ ...d, tiers: d.tiers.filter((_, k) => k !== i) }))

  const body = useMemo(() => termsBody(draft), [draft])
  const problem = useMemo(() => tierProblem(body.tiers), [body.tiers])
  const preview = useMemo(() => previewItem(item, draft), [item, draft])
  const sample = useMemo(() => {
    if (draft.sample.trim() === '') return null
    const q = Number(draft.sample)
    if (!Number.isFinite(q)) return null
    return priceQuantity(preview, q)
  }, [draft.sample, preview])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    setBusy(true)
    setError('')
    try {
      const saved = await api.patch<PriceItem>(`/pricebooks/${bookId}/items/${encodeURIComponent(item.sku)}`, body)
      await onSaved(saved)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Pricing shapes for ${item.sku}`}
      wide
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="price-item-terms" disabled={busy || Boolean(problem)}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </>
      }
    >
      <form id="price-item-terms" onSubmit={(e) => void submit(e)} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}

        <div className="card flat stack tight">
          <div className="row between">
            <h3 style={{ margin: 0 }}>Allowance</h3>
            <span className="muted small">what the plan includes each billing period</span>
          </div>
          <div className="grid2">
            <Field label={`Included quantity (${item.unit})`} help="Usage up to this rates to zero; the excess rates at the price below. Leave empty for no allowance.">
              <input type="number" step="any" min={0} value={draft.allowance} onChange={(e) => set({ allowance: e.target.value })} aria-label="Allowance" placeholder="e.g. 50" />
            </Field>
            <Field label="Carry over" help="An allowance is per billing period and lapses. Carried over, it lasts ONE further period and does not compound.">
              <label className="check">
                <input type="checkbox" checked={draft.rollover} onChange={(e) => set({ rollover: e.target.checked })} aria-label="Carry the unused allowance into the next period" /> Carry the unused part into the next period
              </label>
            </Field>
          </div>
        </div>

        <div className="card flat stack tight">
          <div className="row between">
            <h3 style={{ margin: 0 }}>Volume tiers</h3>
            <span className="muted small">bands instead of one unit price</span>
          </div>
          <Field label="Mode" help={TIER_MODES.find((m) => m.value === draft.mode)?.help}>
            <select value={draft.mode} onChange={(e) => set({ mode: e.target.value === 'all_units' ? 'all_units' : 'graduated' })} aria-label="Tier mode" disabled={draft.tiers.length === 0}>
              {TIER_MODES.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </select>
          </Field>
          {draft.tiers.length === 0 ? (
            <p className="muted small" style={{ margin: 0 }}>
              No bands: every unit rates at the item&rsquo;s unit price of {formatMoney(toNumber(item.unit_price), currency, { digits: 8 })}.
            </p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Band</th>
                    <th className="num">Up to ({item.unit})</th>
                    <th className="num">Price ({currency} per {item.unit})</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {draft.tiers.map((t, i) => (
                    <tr key={i}>
                      <td className="nowrap muted small">{i + 1}</td>
                      <td className="num">
                        <input
                          type="number"
                          step="any"
                          min={0}
                          value={t.up_to}
                          onChange={(e) => setTier(i, { up_to: e.target.value })}
                          aria-label={`Band ${i + 1} upper bound`}
                          placeholder={i === draft.tiers.length - 1 ? 'no limit' : ''}
                        />
                      </td>
                      <td className="num">
                        <input type="number" step="any" min={0} value={t.price} onChange={(e) => setTier(i, { price: e.target.value })} aria-label={`Band ${i + 1} price`} />
                      </td>
                      <td className="nowrap">
                        <button type="button" className="link small danger" onClick={() => removeTier(i)}>
                          Remove
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="row">
            <button type="button" onClick={addTier}>
              Add band
            </button>
            <span className="muted small">Leave the last band&rsquo;s upper bound empty: everything above the one before it rates at that price.</span>
          </div>
          {problem ? <Notice kind="bad">{problem}</Notice> : null}
        </div>

        {/* The explained price, live. This is the surface the founder asked
            for: what the item actually charges, in words, under the editor. */}
        <div className="card flat stack tight">
          <div className="row between">
            <h3 style={{ margin: 0 }}>What this item charges</h3>
            <span className="muted small">the same order the bill is rated in</span>
          </div>
          <ExplainedPrice item={preview} currency={currency} />
          <div className="row">
            <Field label={`Price a sample volume (${item.unit})`} help="Rated here exactly as the engine would: the allowance first, then the bands.">
              <input type="number" step="any" min={0} value={draft.sample} onChange={(e) => set({ sample: e.target.value })} aria-label="Sample volume" placeholder="e.g. 51200" />
            </Field>
            <div className="kpi" style={{ minWidth: 180 }}>
              <div className="k">
                <span>Sample cost</span>
              </div>
              <div className="v">{sample === null ? '—' : formatMoney(sample, currency)}</div>
            </div>
          </div>
        </div>
      </form>
    </Modal>
  )
}
