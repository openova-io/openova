// Billing currency helpers — shared by every OMR display in the console app.
//
// The authoritative money unit across SME services is the baisa (1/1000 OMR).
// Previously the console used ad-hoc `toFixed(3)` calls and string concatenation
// in BillingPage.svelte, which drifted from the marketplace's rounded "9 OMR"
// rendering. Now every money display in the console goes through `formatOMR`
// so the same plan reads identically on the marketplace checkout, the billing
// page, and the admin revenue dashboard. See issue #85.

/** formatOMR renders a baisa value as "OMR 12.345". */
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
