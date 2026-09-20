import type { CapacitySKUOption, CapacitySKUOptions } from '../../api/types'
import { Field, Segmented } from '../../components/ui'
import { t } from '../../i18n'
import { isFamily } from '../../lib/capacity'

/**
 * The ONE way a SKU is chosen anywhere on Capacity.
 *
 * It is a select, never a text box: every SKU this product can know about is
 * already known — a price book prices it, the ledger meters it, or a shape
 * describes it — and a typed SKU is a typo waiting to count against nothing.
 *
 * A placement may also name a FAMILY ("ecs.m7n.*"), which a list of SKUs
 * cannot express; the toggle switches the select to the families the known
 * SKUs form, so that is chosen from a list too.
 */
export function SkuSelect({
  doc,
  value,
  onChange,
  allowFamily,
  needsShape,
  only,
  label,
  id,
}: {
  doc: CapacitySKUOptions | null
  value: string
  onChange: (sku: string) => void
  /** Offer the One SKU / A family toggle (placements only). */
  allowFamily?: boolean
  /** Disable SKUs with no shape, saying why (they cannot be placed or put in a mix). */
  needsShape?: boolean
  /** Narrow the list (the Shapes tab offers only SKUs with no stored shape). */
  only?: (o: CapacitySKUOption) => boolean
  label?: string
  id?: string
}) {
  const family = isFamily(value)
  const mode: 'sku' | 'family' = allowFamily && (family || value === FAMILY_PENDING) ? 'family' : 'sku'
  const skus = (doc?.skus ?? []).filter((o) => (only ? only(o) : true))
  const families = doc?.families ?? []
  const name = label ?? t('capacity.place.sku')

  return (
    <>
      {allowFamily ? (
        <Field label={t('capacity.sku.mode')}>
          <Segmented
            value={mode}
            ariaLabel={t('capacity.sku.mode')}
            options={[
              { value: 'sku', label: t('capacity.sku.modeOne') },
              { value: 'family', label: t('capacity.sku.modeFamily'), disabled: families.length === 0, title: families.length === 0 ? t('capacity.sku.noFamilies') : t('capacity.sku.modeFamilyHelp') },
            ]}
            onChange={(m) => onChange(m === 'family' ? FAMILY_PENDING : '')}
          />
        </Field>
      ) : null}
      <Field label={mode === 'family' ? t('capacity.sku.family') : name} help={mode === 'family' ? t('capacity.sku.familyHelp') : undefined}>
        {mode === 'family' ? (
          <select id={id} aria-label={t('capacity.sku.family')} value={family ? value : ''} onChange={(e) => onChange(e.target.value || FAMILY_PENDING)}>
            <option value="">{t('capacity.sku.chooseFamily')}</option>
            {families.map((f) => (
              <option key={f.pattern} value={f.pattern}>
                {f.pattern} — {t('capacity.sku.familyCount', { count: f.skus })}
              </option>
            ))}
          </select>
        ) : (
          <select id={id} aria-label={name} value={value} onChange={(e) => onChange(e.target.value)} disabled={!doc}>
            <option value="">{doc ? t('capacity.place.chooseSku') : t('capacity.sku.loading')}</option>
            {/* A price book carries well over a hundred SKUs. What is running
                NOW comes first, then what could be placed, then what cannot
                be yet — so the SKU an operator is looking for is near the top
                rather than somewhere in an alphabet. */}
            {groupSkus(skus).map((g) => (
              <optgroup key={g.key} label={g.label}>
                {g.skus.map((o) => (
                  <option key={o.sku} value={o.sku} disabled={needsShape && !o.has_shape}>
                    {skuOptionLabel(o, needsShape)}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        )}
      </Field>
    </>
  )
}

/**
 * The value the family select holds before a family is picked. It is not a
 * SKU anything could be called, so a form can refuse to submit it.
 */
export const FAMILY_PENDING = '\u0000family'

/** Whether a SkuSelect value is something that can be sent. */
export function skuChosen(value: string): boolean {
  return value.trim() !== '' && value !== FAMILY_PENDING
}

/** The SKUs in three groups: metered now, shaped, and not shaped yet. Empty groups are dropped. */
export function groupSkus(skus: CapacitySKUOption[]): Array<{ key: string; label: string; skus: CapacitySKUOption[] }> {
  return [
    { key: 'metered', label: t('capacity.sku.groupMetered'), skus: skus.filter((o) => o.metered) },
    { key: 'shaped', label: t('capacity.sku.groupShaped'), skus: skus.filter((o) => !o.metered && o.has_shape) },
    { key: 'unshaped', label: t('capacity.sku.groupUnshaped'), skus: skus.filter((o) => !o.metered && !o.has_shape) },
  ].filter((g) => g.skus.length > 0)
}

function skuOptionLabel(o: CapacitySKUOption, needsShape?: boolean): string {
  const notes: string[] = []
  if (o.description) notes.push(o.description)
  if (!o.in_price_book) notes.push(t('capacity.sku.notPriced'))
  if (needsShape && !o.has_shape) notes.push(t('capacity.sku.noShape'))
  return notes.length ? `${o.sku} — ${notes.join(' · ')}` : o.sku
}
