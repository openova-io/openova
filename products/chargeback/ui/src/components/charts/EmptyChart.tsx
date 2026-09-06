/**
 * EmptyChart is what every chart renders when there is nothing to draw. An
 * axis with no marks reads as "zero spend", which is a different and wrong
 * claim — absence of data is stated in words, never drawn as zero.
 */
export function EmptyChart({
  message = 'No data in this window.',
  hint,
  height,
}: {
  message?: string
  hint?: string
  /** Match the chart's height so a card does not jump when data arrives. */
  height?: number
}) {
  return (
    <div className="chart-empty" role="status" style={height ? { minHeight: height } : undefined}>
      <div>{message}</div>
      {hint ? <div className="hint">{hint}</div> : null}
    </div>
  )
}
