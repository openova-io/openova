import { useState } from 'react'
import { FILTER_DIMENSIONS, type DimensionValues, type GroupBy, type StaticGroupBy } from '../api/types'
import { isTagDim, isValidTagKey, tagDim, tagKeyOf } from '../lib/tags'

/**
 * Include / exclude filter chips for the explorer (#6867). The value picker
 * is fed by GET /cost/dimensions so only values that exist in the window are
 * offered; free text is accepted too (paste a resource id). Tag dimensions
 * (`tag:<key>`) are offered for every key the window carries, plus a free
 * key for one it does not.
 */
export type Dim = Exclude<GroupBy, 'none'>
export type StaticDim = Exclude<StaticGroupBy, 'none'>
export type Filters = { include: Partial<Record<Dim, string[]>>; exclude: Partial<Record<Dim, string[]>> }

export const DIM_LABEL: Record<StaticDim, string> = {
  customer: 'Customer',
  kind: 'Service',
  sku: 'SKU',
  resource: 'Resource',
  region: 'Region',
  source: 'Cost source',
  tier: 'Tier',
  namespace: 'Namespace',
  enterprise_project: 'Enterprise project',
}

/** Display name of any dimension: static ones by table, `tag:<key>` as "Tag <key>". */
export function dimLabel(dim: string): string {
  const key = tagKeyOf(dim)
  if (key !== null) return `Tag ${key}`
  return (DIM_LABEL as Record<string, string>)[dim] ?? dim
}

/** The dimensions a filter set names, static ones first in the canonical order, tags after, sorted. */
export function filterDims(f: Filters): Dim[] {
  const present = new Set<string>([...Object.keys(f.include), ...Object.keys(f.exclude)])
  const out: Dim[] = FILTER_DIMENSIONS.filter((d) => present.has(d))
  const tags = [...present].filter((d) => isTagDim(d)).sort()
  return [...out, ...(tags as Dim[])]
}

/** Sentinel option of the dimension picker: "Tag (other key)…" reveals a key input. */
const TAG_OTHER = 'tag:'

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
  const [dim, setDim] = useState<string>('kind')
  const [tagKey, setTagKey] = useState('')
  const [value, setValue] = useState('')
  const dims = FILTER_DIMENSIONS.filter((d) => !hideDims?.includes(d))
  const tagKeys = dimensions?.tag_keys ?? []
  // The dimension the chip will carry: a static one, a known tag, or the typed key.
  const effDim: Dim | null = dim === TAG_OTHER ? (isValidTagKey(tagKey.trim()) ? tagDim(tagKey.trim()) : null) : (dim as Dim)
  const values = effDim ? dimensions?.dimensions[effDim] ?? [] : []
  const label = (d: Dim, k: string) => labelFor?.(d, k) ?? dimensions?.dimensions[d]?.find((v) => v.key === k)?.label ?? k

  const commit = () => {
    const v = value.trim()
    if (!v || !effDim) return
    onChange(addFilter(filters, mode, effDim, v))
    setValue('')
    setAdding(false)
  }

  const chips: Array<{ mode: 'include' | 'exclude'; dim: Dim; value: string }> = []
  for (const m of ['include', 'exclude'] as const) {
    for (const d of filterDims(filters)) if (!hideDims?.includes(d)) for (const v of filters[m][d] ?? []) chips.push({ mode: m, dim: d, value: v })
  }

  return (
    <div className="chips" role="group" aria-label="Filters">
      {chips.map((c) => (
        <span key={`${c.mode}:${c.dim}:${c.value}`} className={`chip ${c.mode}`} title={`${c.mode} ${dimLabel(c.dim)} = ${c.value}`}>
          <span className="dim">{dimLabel(c.dim)}:</span>
          <span className="val">{label(c.dim, c.value)}</span>
          <button type="button" aria-label={`remove filter ${dimLabel(c.dim)} ${c.value}`} onClick={() => onChange(removeFilter(filters, c.mode, c.dim, c.value))}>
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
          <select value={dim} onChange={(e) => setDim(e.target.value)} aria-label="Filter dimension" style={{ width: 'auto' }}>
            {dims.map((d) => (
              <option key={d} value={d}>
                {DIM_LABEL[d]}
              </option>
            ))}
            {tagKeys.map((k) => (
              <option key={tagDim(k)} value={tagDim(k)}>
                Tag {k}
              </option>
            ))}
            <option value={TAG_OTHER}>Tag (other key)…</option>
          </select>
          {dim === TAG_OTHER ? (
            <input
              list="filter-tag-keys"
              value={tagKey}
              onChange={(e) => setTagKey(e.target.value)}
              placeholder="tag key"
              aria-label="Tag key"
              aria-invalid={tagKey.trim() !== '' && !isValidTagKey(tagKey.trim())}
              title="letters, digits, _ . : / @ - (max 128)"
              style={{ width: 140 }}
              autoFocus
            />
          ) : null}
          <datalist id="filter-tag-keys">
            {tagKeys.map((k) => (
              <option key={k} value={k} />
            ))}
          </datalist>
          <input
            list={`dim-values-${effDim ?? 'none'}`}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                commit()
              }
              if (e.key === 'Escape') setAdding(false)
            }}
            placeholder={values.length ? `choose or type (${values.length})` : effDim && isTagDim(effDim) ? 'value, or (untagged)' : 'type a value'}
            aria-label="Filter value"
            style={{ width: 220 }}
            autoFocus={dim !== TAG_OTHER}
          />
          <datalist id={`dim-values-${effDim ?? 'none'}`}>
            {values.map((v) => (
              <option key={v.key} value={v.key}>
                {v.label !== v.key ? v.label : undefined}
              </option>
            ))}
          </datalist>
          <button type="button" className="small primary" onClick={commit} disabled={!effDim || !value.trim()}>
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
