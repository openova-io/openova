/**
 * parseLBPorts — extracted helper used by both AddLBModal and
 * EditLBModal. Lives outside the FormFields component file so the
 * react-refresh/only-export-components rule stays clean.
 */

/** Parse a CSV port list into the wire-shape [{ port, protocol }]. */
export function parseLBPorts(csv: string): { port: number; protocol: string }[] {
  return csv
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
    .map((p) => ({ port: parseInt(p, 10), protocol: 'tcp' }))
    .filter((p) => Number.isFinite(p.port) && p.port > 0)
}
