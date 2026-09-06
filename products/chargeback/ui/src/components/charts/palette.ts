/**
 * Categorical palette — twelve slots in a FIXED order.
 *
 * Validated, not eyeballed, against the app's white panel surface with the
 * data-viz palette checks (OKLCH lightness band, chroma floor, adjacent-pair
 * colour-vision-deficiency separation — worst ΔE 9.1 — and the normal-vision
 * floor — worst ΔE 19.6). Three slots (aqua, yellow, magenta) sit below 3:1
 * contrast on white, which is why every chart in this library ships a legend
 * and a tooltip: identity is never colour alone.
 *
 * Colour follows the entity, never its rank: colorForKey() assigns by position
 * in the caller's key list, so filtering one series out does not repaint the
 * survivors as long as the caller keeps passing the same key order.
 */
export const PALETTE: readonly string[] = [
  '#2a78d6', // blue
  '#eb6834', // orange
  '#1baf7a', // aqua
  '#eda100', // yellow
  '#e87ba4', // magenta
  '#008300', // green
  '#4a3aa7', // violet
  '#e34948', // red
  '#0891b2', // cyan
  '#a16207', // brown
  '#c026d3', // fuchsia
  '#65a30d', // lime
]

/** The key the API (and topN) use for the folded remainder. */
export const OTHER_KEY = 'other'
/** The neutral every chart draws the folded "Other" group in. */
export const OTHER_COLOR = '#94a3b8'
/** Forecast marks: the projected tail is drawn hatched in this neutral. */
export const FORECAST_COLOR = '#64748b'
/** Surface behind every chart (the card panel); used for gaps and rings. */
export const SURFACE = '#ffffff'

/**
 * colorFor returns the slot for a series index. Past twelve the palette wraps,
 * which is a last resort — callers fold the tail with topN() so a thirteenth
 * series never reaches a chart.
 */
export function colorFor(index: number): string {
  if (!Number.isInteger(index) || index < 0) return OTHER_COLOR
  return PALETTE[index % PALETTE.length]
}

/**
 * colorForKey is stable by key ORDER: the slot is the key's position among
 * the non-"other" keys, so "other" (always the neutral) never shifts the
 * slots of the keys after it.
 */
export function colorForKey(key: string, keys: readonly string[]): string {
  if (key === OTHER_KEY) return OTHER_COLOR
  let i = 0
  for (const k of keys) {
    if (k === OTHER_KEY) continue
    if (k === key) return colorFor(i)
    i++
  }
  return OTHER_COLOR
}
