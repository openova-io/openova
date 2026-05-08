/**
 * ComplianceTreemapNode — shape consumed by the ComplianceTreemap
 * component (slice U1+U2, #1096). Mirrors recharts'
 * `TreemapDataType` constraint via the `[k: string]: unknown` index
 * signature and exposes our typed metadata fields on top.
 *
 * Lives in its own file so both the component (.tsx) and helper
 * (.ts) can import without violating react-refresh's
 * "components-only export" rule on the .tsx side.
 */

import type { Score } from '@/pages/admin/compliance/compliance.api'

export interface ComplianceTreemapNode {
  // Recharts uses an index signature on its TreemapDataType — we
  // satisfy the constraint with `[k: string]: unknown` so our typed
  // fields and the recharts walker coexist.
  [k: string]: unknown
  name: string
  /** Set on parent nodes only — children list. */
  children?: ComplianceTreemapNode[]
  /** Set on leaf nodes (Application-level) — drives color. */
  total?: number | null
  /** Set on every node — drives area. */
  size: number
  /** The raw Score struct (leaves only). Drives tooltip + onLeafClick. */
  score?: Score
}
