import { useState } from 'react'
import type { PublicCatalog, PublicCatalogPlan } from '../../api/types'
import { Field, FormRow, Modal } from '../../components/ui'
import { formatMoney } from '../../lib/money'
import { optionalFeatures, planSku } from '../../lib/packages'
import { MAX_HOURS } from '../../pages/Estimate'
import {
  QUANTITY_LABEL,
  USAGES,
  defaultConfig,
  deploymentsOf,
  findService,
  itemLines,
  kindOf,
  lineUsageText,
  sizeOptions,
  storageFor,
  variantsOf,
  type CatalogService,
  type ItemConfig,
} from './model'

/**
 * The configurator (DESIGN.md §12.5): one modal per service, every choice a
 * dropdown or a number, never a SKU. It shows the lines it is about to add
 * — with the SKU as a small hint — and hands the page a configuration; the
 * page prices it through the server.
 */
export function Configurator({
  catalog,
  service,
  initial,
  onClose,
  onSubmit,
}: {
  catalog: PublicCatalog
  service: CatalogService
  /** The item being edited, or null for a new one. */
  initial: ItemConfig | null
  onClose: () => void
  onSubmit: (config: ItemConfig) => void
}) {
  const [draft, setDraft] = useState<ItemConfig>(() => initial ?? defaultConfig(catalog, service))
  const kind = kindOf(service.key)
  const set = (patch: Partial<ItemConfig>) => setDraft((d) => ({ ...d, ...patch }))
  const result = itemLines(catalog, draft)
  const errors = result.errors
  const valid = Object.keys(errors).length === 0 && result.lines.length > 0
  const currency = catalog.currency

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (valid) onSubmit(draft)
  }

  // Changing a first choice re-picks the smallest size under it, so the
  // size dropdown never holds an option that is not in the list.
  const pickVariant = (variant: string) => {
    const first = sizeOptions(service, { variant })[0]
    set({ variant, sku: first?.sku ?? '' })
  }
  const pickDeployment = (deployment: string) => {
    const first = sizeOptions(service, { deployment })[0]
    set({ deployment, sku: first?.sku ?? '' })
  }

  const quantityField = (
    <Field label={QUANTITY_LABEL[kind]} error={errors.quantity}>
      <input type="number" min={1} step={1} inputMode="numeric" value={draft.quantity} onChange={(e) => set({ quantity: e.target.value })} />
    </Field>
  )
  const usageFields = (
    <>
      <Field label="Usage">
        <select value={draft.usage} onChange={(e) => set({ usage: e.target.value as ItemConfig['usage'] })}>
          {USAGES.map((u) => (
            <option key={u.value} value={u.value}>
              {u.label}
            </option>
          ))}
        </select>
      </Field>
      {draft.usage === 'custom' ? (
        <Field label="Hours per month" error={errors.hours} help={`Up to ${MAX_HOURS}`}>
          <input type="number" min={1} max={MAX_HOURS} step="any" inputMode="decimal" value={draft.hours} onChange={(e) => set({ hours: e.target.value })} />
        </Field>
      ) : null}
    </>
  )
  // The size picker: one chip per size, the shape as labelled numbers and
  // the monthly figure under it. A radio group, so the keyboard and a
  // screen reader see one choice among several.
  const sizePicker = (label: string, pick: { variant?: string; deployment?: string }) => {
    const options = sizeOptions(service, pick)
    return (
      <div className="field">
        <label id="estimate-size-label">{label}</label>
        <div className="size-chips" role="radiogroup" aria-labelledby="estimate-size-label">
          {options.map((o) => {
            const on = draft.sku === o.sku
            const { vcpu, memory_gb: mem } = o.entry
            return (
              <button key={o.sku} type="button" role="radio" aria-checked={on} aria-label={o.label} className={`size-chip ${on ? 'on' : ''}`} onClick={() => set({ sku: o.sku })}>
                <span className="shape">
                  {vcpu && mem ? (
                    <>
                      <b>{vcpu}</b> <span className="dim">vCPU</span> · <b>{mem}</b> <span className="dim">GB</span>
                    </>
                  ) : (
                    <b>{o.entry.display_name}</b>
                  )}
                  {o.hint ? <span className="mono dim"> {o.hint}</span> : null}
                </span>
                <span className="price">{formatMoney(o.entry.monthly, currency)} / month</span>
              </button>
            )
          })}
        </div>
        {errors.sku ? <div className="err">{errors.sku}</div> : null}
      </div>
    )
  }

  let body: React.ReactNode
  switch (kind) {
    case 'server': {
      const evs = findService(catalog, 'evs')
      const eip = findService(catalog, 'eip')
      body = (
        <>
          <FormRow>
            <Field label="Family">
              <select value={draft.variant} onChange={(e) => pickVariant(e.target.value)}>
                {variantsOf(service).map((v) => (
                  <option key={v.value} value={v.value}>
                    {v.label}
                  </option>
                ))}
              </select>
            </Field>
            {quantityField}
            {usageFields}
          </FormRow>
          {sizePicker('Size', { variant: draft.variant })}
          {evs ? (
            <FormRow>
              <Field label="Attached disk" help="Disks are kept for the whole month">
                <select value={draft.diskVariant} onChange={(e) => set({ diskVariant: e.target.value })}>
                  <option value="">No disk</option>
                  {variantsOf(evs).map((v) => (
                    <option key={v.value} value={v.value}>
                      {v.label}
                    </option>
                  ))}
                </select>
              </Field>
              {draft.diskVariant ? (
                <Field label="Disk size (GB) per server" error={errors.storageGb}>
                  <input type="number" min={1} step={1} inputMode="numeric" value={draft.storageGb} onChange={(e) => set({ storageGb: e.target.value })} />
                </Field>
              ) : null}
            </FormRow>
          ) : null}
          {eip ? (
            <FormRow>
              <label className="check">
                <input type="checkbox" checked={draft.eip} onChange={(e) => set({ eip: e.target.checked })} /> Add an Elastic IP to each server
              </label>
              {draft.eip && eip.entries.some((e) => e.variant === 'bandwidth') ? (
                <Field label="Bandwidth (Mbps) per address" error={errors.bandwidthMbps}>
                  <input type="number" min={1} step={1} inputMode="numeric" value={draft.bandwidthMbps} onChange={(e) => set({ bandwidthMbps: e.target.value })} />
                </Field>
              ) : null}
            </FormRow>
          ) : null}
        </>
      )
      break
    }
    case 'database': {
      const storage = storageFor(catalog, service.key, draft.deployment)
      body = (
        <>
          <FormRow>
            <Field label="Deployment">
              <select value={draft.deployment} onChange={(e) => pickDeployment(e.target.value)}>
                {deploymentsOf(service).map((d) => (
                  <option key={d.value} value={d.value}>
                    {d.label}
                  </option>
                ))}
              </select>
            </Field>
            {storage ? (
              <Field label="Storage (GB) per database" error={errors.storageGb} help="Storage is kept for the whole month">
                <input type="number" min={1} step={1} inputMode="numeric" value={draft.storageGb} onChange={(e) => set({ storageGb: e.target.value })} />
              </Field>
            ) : null}
            {quantityField}
            {usageFields}
          </FormRow>
          {sizePicker('Size', { deployment: draft.deployment })}
        </>
      )
      break
    }
    case 'storage': {
      const variants = variantsOf(service)
      body = (
        <FormRow>
          {variants.length > 1 || variants[0]?.value ? (
            <Field label="Type">
              <select value={draft.sku} onChange={(e) => set({ sku: e.target.value })}>
                {sizeOptions(service, {}).map((o) => (
                  <option key={o.sku} value={o.sku}>
                    {o.entry.display_name}
                  </option>
                ))}
              </select>
            </Field>
          ) : null}
          <Field label="Size (GB)" error={errors.storageGb}>
            <input type="number" min={1} step={1} inputMode="numeric" value={draft.storageGb} onChange={(e) => set({ storageGb: e.target.value })} />
          </Field>
          {quantityField}
          {usageFields}
        </FormRow>
      )
      break
    }
    case 'eip':
      body = (
        <FormRow>
          {quantityField}
          {service.entries.some((e) => e.variant === 'bandwidth') ? (
            <Field label="Bandwidth (Mbps) per address" error={errors.bandwidthMbps}>
              <input type="number" min={1} step={1} inputMode="numeric" value={draft.bandwidthMbps} onChange={(e) => set({ bandwidthMbps: e.target.value })} />
            </Field>
          ) : null}
          {usageFields}
        </FormRow>
      )
      break
    case 'sized':
      body = (
        <>
          <FormRow>
            {quantityField}
            {usageFields}
          </FormRow>
          {sizePicker(service.key === 'cce' ? 'Cluster size' : 'Size', {})}
        </>
      )
      break
    case 'capacity': {
      const has = (variant: string) => service.entries.some((e) => e.variant === variant)
      body = (
        <>
          <FormRow>
            {has('vcpu') ? (
              <Field label="vCPU" error={errors.vcpu}>
                <input type="number" min={0} step={1} inputMode="numeric" value={draft.vcpu} onChange={(e) => set({ vcpu: e.target.value })} />
              </Field>
            ) : null}
            {has('memory') ? (
              <Field label="Memory (GiB)" error={errors.memory}>
                <input type="number" min={0} step={1} inputMode="numeric" value={draft.memGb} onChange={(e) => set({ memGb: e.target.value })} />
              </Field>
            ) : null}
            {has('storage') ? (
              <Field label="Persistent storage (GB)" error={errors.storage}>
                <input type="number" min={0} step={1} inputMode="numeric" value={draft.pvcGb} onChange={(e) => set({ pvcGb: e.target.value })} />
              </Field>
            ) : null}
          </FormRow>
          <FormRow>
            {quantityField}
            {usageFields}
          </FormRow>
        </>
      )
      break
    }
    case 'plan': {
      // The package's optional features (DESIGN.md §22), ticked to add
      // their add-on lines; what the chosen package includes is not offered.
      const addons = optionalFeatures(catalog.packages, planSku(draft.plan))
      const taken = draft.addons ?? []
      body = (
        <>
          <FormRow>
            <Field label="Plan" error={errors.plan}>
              <select value={draft.plan} onChange={(e) => set({ plan: e.target.value })}>
                {(service.entries as PublicCatalogPlan[]).map((p) => (
                  <option key={p.slug} value={p.slug}>
                    {p.name} — {p.vcpu} vCPU · {p.memory_gib} GiB — {formatMoney(p.monthly, currency)} / month
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Months" error={errors.months} help="1 to 12">
              <input type="number" min={1} max={12} step={1} inputMode="numeric" value={draft.months} onChange={(e) => set({ months: e.target.value })} />
            </Field>
            {quantityField}
          </FormRow>
          {addons.length ? (
            <div className="field" data-testid="plan-addons">
              <label>Add-ons</label>
              <div className="stack tight">
                {addons.map((o) => (
                  <label key={o.key} className="check" style={{ alignItems: 'flex-start' }}>
                    <input type="checkbox" checked={taken.includes(o.key)} onChange={() => set({ addons: taken.includes(o.key) ? taken.filter((k) => k !== o.key) : [...taken, o.key] })} aria-label={`${o.name} add-on`} disabled={!o.price_month} />
                    <span>
                      {o.name} <span className="num muted">{o.price_month ? `+ ${o.price_month} ${currency} / month` : 'ask us'}</span>
                      {o.included_from ? <span className="tiny muted"> · {o.included_from}</span> : null}
                    </span>
                  </label>
                ))}
              </div>
            </div>
          ) : null}
        </>
      )
      break
    }
    default:
      body = (
        <FormRow>
          {service.entries.length > 1 ? (
            <Field label="Option" error={errors.sku}>
              <select value={draft.sku} onChange={(e) => set({ sku: e.target.value })}>
                {sizeOptions(service, {}).map((o) => (
                  <option key={o.sku} value={o.sku}>
                    {('description' in o.entry && o.entry.description) || o.entry.display_name} — {formatMoney(o.entry.monthly, currency)} / month
                  </option>
                ))}
              </select>
            </Field>
          ) : null}
          {quantityField}
          {usageFields}
        </FormRow>
      )
  }

  return (
    <Modal
      title={initial ? `Edit ${service.name}` : service.name}
      wide
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="primary" disabled={!valid} onClick={() => valid && onSubmit(draft)}>
            {initial ? 'Save changes' : 'Add to estimate'}
          </button>
        </>
      }
    >
      {service.blurb ? <p className="muted small" style={{ marginTop: -6 }}>{service.blurb}</p> : null}
      <form id="estimate-configurator" onSubmit={submit} className="stack">
        {body}
        {errors.service ? <div className="err warn small">{errors.service}</div> : null}
        <div className="card flat" style={{ background: 'var(--panel-2)', padding: '8px 12px' }}>
          <div className="tiny muted" style={{ marginBottom: 4 }}>
            This adds {result.lines.length} line{result.lines.length === 1 ? '' : 's'} to the estimate
          </div>
          {result.lines.map((l, i) => (
            <div key={`${l.sku ?? l.plan}-${i}`} className="row between small" data-testid="configurator-line">
              <span>
                {l.label} <span className="mono muted tiny">{l.sku ?? `plan.${l.plan}`}</span>
              </span>
              <span className="muted num">{lineUsageText(l)}</span>
            </div>
          ))}
        </div>
      </form>
    </Modal>
  )
}
