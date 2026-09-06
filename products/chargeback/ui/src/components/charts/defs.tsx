import { useId } from 'react'
import { FORECAST_COLOR, SURFACE } from './palette'

/** A DOM-safe id prefix unique to this chart instance (for <pattern> refs). */
export function useChartId(): string {
  return 'c' + useId().replace(/[^a-zA-Z0-9_-]/g, '')
}

export const hatchForecast = (id: string) => `url(#${id}-fc)`
export const hatchMissing = (id: string) => `url(#${id}-nd)`

/**
 * HatchDefs declares the two hatch patterns a chart may use: forecast (45°
 * neutral lines on the surface) and no-data (a fainter version). Both are
 * pattern fills, never gradients.
 */
export function HatchDefs({ id }: { id: string }) {
  return (
    <defs>
      <pattern id={`${id}-fc`} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
        <rect width="6" height="6" fill={SURFACE} />
        <line x1="0" y1="0" x2="0" y2="6" stroke={FORECAST_COLOR} strokeWidth="1.5" opacity="0.7" />
      </pattern>
      <pattern id={`${id}-nd`} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
        <rect width="6" height="6" fill={SURFACE} />
        <line x1="0" y1="0" x2="0" y2="6" stroke="#cbd5e1" strokeWidth="1" />
      </pattern>
    </defs>
  )
}
