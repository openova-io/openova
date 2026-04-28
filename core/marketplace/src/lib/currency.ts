// Billing currency helpers — shared by every OMR display in the marketplace app.
//
// The authoritative money unit across SME services is the baisa (1/1000 OMR).
// The marketplace previously defined THREE near-identical helpers —
// formatPrice (rounded to integer OMR) in PlanStep/AddonsStep/ReviewStep, and
// formatOMR (toFixed(3)) in CheckoutStep (#85). That meant the same plan
// could render as "9 OMR" on one page and "9.000 OMR" on another — worse,
// rounding the plan price to the nearest whole OMR at checkout masked sub-
// OMR increments from future pricing changes.
//
// Every money display in the marketplace SHOULD go through `formatOMR(baisa)`.

/** formatOMR renders a baisa value as "OMR 12.345". See console/lib/currency.ts. */
export function formatOMR(baisa: number | null | undefined, opts: { signed?: boolean } = {}): string {
  const n = typeof baisa === 'number' && isFinite(baisa) ? baisa : 0;
  const omr = n / 1000;
  const formatted = Math.abs(omr).toFixed(3);
  if (opts.signed) {
    if (n > 0) return `+OMR ${formatted}`;
    if (n < 0) return `-OMR ${formatted}`;
  } else if (n < 0) {
    return `-OMR ${formatted}`;
  }
  return `OMR ${formatted}`;
}

/** omrToBaisa converts a whole-OMR integer into baisa. API-boundary only. */
export function omrToBaisa(omr: number | null | undefined): number {
  const n = typeof omr === 'number' && isFinite(omr) ? omr : 0;
  return Math.round(n * 1000);
}

/**
 * formatOMRAmount returns only the numeric portion of formatOMR (no "OMR "
 * prefix), e.g. `12.345` — for the plan-card UI where "OMR" is styled as a
 * separate <span> next to the strong price. Still millibaisa precision.
 */
export function formatOMRAmount(baisa: number | null | undefined): string {
  const n = typeof baisa === 'number' && isFinite(baisa) ? baisa : 0;
  return (Math.abs(n) / 1000).toFixed(3);
}
