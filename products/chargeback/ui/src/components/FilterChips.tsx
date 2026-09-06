import { useState } from 'react'
import { FILTER_DIMENSIONS, type DimensionValues, type GroupBy } from '../api/types'

/**
 * Include / exclude filter chips for the explorer (#6867). The value picker
 * is fed by GET /cost/dimensions so only values that exist in the window are
 * offered; free text is accepted too (paste a resource id).
 */
export type Dim = Exclude<GroupBy, 'none'>
export type Filters = { include: Partial<Record<Dim, string[]>>; exclude: Partial<Record<Dim, string[]>> }

export const DIM_LABEL: Record<Dim, string> = {
  customer: 'Customer',
  kind: 'Service',
  sku: 'SKU',
  resource: 'Resource',
  region: 'Region',
  source: 'Cost source',
  tier: 'Tier',
  namespace: 'Namespace',
}

export function emptyFilters(): Filters {
  return { include: {}, exclude: {} }
}

export function filterCount(f: Filters): number {
  return Object.values(f.include).reduce((n, v) => n + (v?.length ?? 0), 0) + Object.values(f.exclude).reduce((n, v) => n + (v?.length ?? 0), 0)
}

export function addFilter(f: Filters, mode: 'include' | 'exclude', dim: Dim, value: string): Filters {
  const cur = f[mode][dim] ?? []
  if (cur.includes(value)) return f
  return { ...f, [mode]: { ...f[mode], [dim]: [...cur, value] } }
}

export function removeFilter(f: Filters, mode: 'include' | 'exclude', dim: Dim, value: string): Filters {
  const next = (f[mode][dim] ?? []).filter((v) => v !== value)
  const m = { ...f[mode] }
  if (next.length) m[dim] = next
  else delete m[dim]
  return { ...f, [mode]: m }
}

export function FilterChips({
  filters,
  onChange,
  dimensions,
  labelFor,
  hideDims,
}: {
  filters: Filters
  onChange: (f: Filters) => void
  dimensions?: DimensionValues | null
  labelFor?: (dim: Dim, key: string) => string
  /** Dimensions not offered (e.g. `customer` on the customer lens). */
  hideDims?: Dim[]
}) {
  const [adding, setAdding] = useState(false)
  const [mode, setMode] = useState<'include' | 'exclude'>('include')
  const [dim, setDim] = useState<Dim>('kind')
  const [value, setValue] = useState('')
  const dims = FILTER_DIMENSIONS.filter((d) => !hideDims?.includes(d))
  const values = dimensions?.dimensions[dim] ?? []
  const label = (d: Dim, k: string) => labelFor?.(d, k) ?? dimensions?.dimensions[d]?.find((v) => v.key === k)?.label ?? k

  const commit = () => {
    const v = value.trim()
    if (!v) return
    onChange(addFilter(filters, mode, dim, v))
    setValue('')
    setAdding(false)
  }

  const chips: Array<{ mode: 'include' | 'exclude'; dim: Dim; value: string }> = []
  for (const m of ['include', 'exclude'] as const) {
    for (const d of dims) for (const v of filters[m][d] ?? []) chips.push({ mode: m, dim: d, value: v })
  }

  return (
    <div className="chips" role="group" aria-label="Filters">
      {chips.map((c) => (
        <span key={`${c.mode}:${c.dim}:${c.value}`} className={`chip ${c.mode}`} title={`${c.mode} ${DIM_LABEL[c.dim]} = ${c.value}`}>
          <span className="dim">{DIM_LABEL[c.dim]}:</span>
          <span className="val">{label(c.dim, c.value)}</span>
          <button type="button" aria-label={`remove filter ${DIM_LABEL[c.dim]} ${c.value}`} onClick={() => onChange(removeFilter(filters, c.mode, c.dim, c.value))}>
            ×
          </button>
        </span>
      ))}
      {adding ? (
        <span className="row" style={{ gap: 6 }}>
          <select value={mode} onChange={(e) => setMode(e.target.value as 'include' | 'exclude')} aria-label="Filter mode" style={{ width: 'auto' }}>
            <option value="include">is</option>
            <option value="exclude">is not</option>
          </select>
          <select value={dim} onChange={(e) => setDim(e.target.value as Dim)} aria-label="Filter dimension" style={{ width: 'auto' }}>
            {dims.map((d) => (
              <option key={d} value={d}>
                {DIM_LABEL[d]}
              </option>
            ))}
          </select>
          <input
            list={`dim-values-${dim}`}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                commit()
              }
              if (e.key === 'Escape') setAdding(false)
            }}
            placeholder={values.length ? `choose or type (${values.length})` : 'type a value'}
            aria-label="Filter value"
            style={{ width: 220 }}
            autoFocus
          />
          <datalist id={`dim-values-${dim}`}>
            {values.map((v) => (
              <option key={v.key} value={v.key}>
                {v.label !== v.key ? v.label : undefined}
              </option>
            ))}
          </datalist>
          <button type="button" className="small primary" onClick={commit}>
            Add
          </button>
          <button type="button" className="small" onClick={() => setAdding(false)}>
            Cancel
          </button>
        </span>
      ) : (
        <button type="button" className="chip-add" onClick={() => setAdding(true)}>
          + Add filter
        </button>
      )}
      {chips.length ? (
        <button type="button" className="link small" onClick={() => onChange(emptyFilters())}>
          Clear all
        </button>
      ) : null}
    </div>
  )
}
