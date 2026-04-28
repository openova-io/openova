// Billing currency helpers — shared by every OMR display in the admin app.
//
// The authoritative money unit across SME services is the baisa (1/1000 OMR).
// Revenue and order amounts arrive from the billing API as baisa and render
// through this single helper so the admin dashboard never disagrees with the
// customer-facing console or marketplace views. See issue #85.

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
