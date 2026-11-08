/**
 * objectStatus — the ONE status contract (ADR-0015 D7, Refs #7067).
 *
 * catalyst-api's `statusOf(object)` reads the object's own conditions
 * (Application CR / HelmRelease / Continuum — namespace-exact, every region
 * where applicable) and ships the result verbatim. The console renders it
 * verbatim. There is NO derivation in the browser: no phase→status mapping,
 * no compState precedence, no "absent means pending/healthy" defaulting.
 *
 * `state` is one of three values. When the API sent no objectStatus the
 * console renders `Unknown` — never `Pending`, never `Healthy`.
 */
export type ObjectState = 'Ready' | 'NotReady' | 'Unknown'

export interface RegionObjectStatus {
  region: string
  state: ObjectState | string
  reason?: string
  message?: string
  since?: string
}

export interface ObjectStatus {
  state: ObjectState | string
  reason?: string
  message?: string
  since?: string
  kind?: string
  namespace?: string
  name?: string
  perRegion?: RegionObjectStatus[]
}

/** The state to render: verbatim when present, `Unknown` when absent. */
export function objectStateOf(s: { state?: string } | null | undefined): string {
  const v = s?.state?.trim()
  return v ? v : 'Unknown'
}

/** Per-region entry for `region`, or undefined (rendered as Unknown). */
export function regionStatusOf(
  s: ObjectStatus | null | undefined,
  region: string,
): RegionObjectStatus | undefined {
  if (!s?.perRegion || !region) return undefined
  return s.perRegion.find((r) => r.region === region)
}
