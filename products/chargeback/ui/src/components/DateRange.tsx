import { Field, Segmented } from './ui'
import { MAX_HOURLY_DAYS, PRESETS, fitGranularity, hourlyAllowed, presetWindow, toExclusive, toInclusive, type Preset, type Window } from '../lib/dates'
import type { Granularity } from '../api/types'

/**
 * Window picker: preset select + custom from/to (inclusive in the UI,
 * exclusive on the wire) + hour/day/month grain. Emits a whole state object
 * so a page can mirror it to the URL. Hourly is offered only for windows of
 * at most MAX_HOURLY_DAYS days (the API's limit); when the window grows past
 * it under an hourly view the grain falls back to daily.
 */
export interface DateRangeState {
  preset: Preset
  window: Window
  granularity: Granularity
}

export function DateRange({
  value,
  onChange,
  showGranularity = true,
}: {
  value: DateRangeState
  onChange: (v: DateRangeState) => void
  showGranularity?: boolean
}) {
  // Every window change passes through here so an hourly grain never outlives
  // a window the API would refuse it for.
  const emit = (next: DateRangeState) => onChange({ ...next, granularity: fitGranularity(next.granularity, next.window) })
  const setPreset = (p: Preset) => {
    if (p === 'custom') {
      onChange({ ...value, preset: 'custom' })
      return
    }
    emit({ ...value, preset: p, window: presetWindow(p) })
  }
  const setFrom = (from: string) => {
    if (!from) return
    emit({ ...value, preset: 'custom', window: { from, to: value.window.to > from ? value.window.to : toExclusive(from) } })
  }
  const setTo = (toInc: string) => {
    if (!toInc) return
    const to = toExclusive(toInc)
    emit({ ...value, preset: 'custom', window: { from: value.window.from < to ? value.window.from : toInc, to } })
  }
  const hourly = hourlyAllowed(value.window)
  return (
    <>
      <Field label="Period">
        <select value={value.preset} onChange={(e) => setPreset(e.target.value as Preset)} aria-label="Period preset">
          {PRESETS.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </select>
      </Field>
      <Field label="From">
        <input type="date" value={value.window.from} max={toInclusive(value.window.to)} onChange={(e) => setFrom(e.target.value)} aria-label="From date" />
      </Field>
      <Field label="To">
        <input type="date" value={toInclusive(value.window.to)} min={value.window.from} onChange={(e) => setTo(e.target.value)} aria-label="To date (inclusive)" />
      </Field>
      {showGranularity ? (
        <Field label="Granularity">
          <Segmented<Granularity>
            value={value.granularity}
            onChange={(granularity) => onChange({ ...value, granularity })}
            options={[
              { value: 'hour', label: 'Hourly', disabled: !hourly, title: hourly ? 'One bucket per hour (UTC)' : `Hourly needs a window of at most ${MAX_HOURLY_DAYS} days` },
              { value: 'day', label: 'Daily' },
              { value: 'month', label: 'Monthly' },
            ]}
            ariaLabel="Granularity"
          />
        </Field>
      ) : null}
    </>
  )
}
