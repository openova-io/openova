/**
 * CloudKindChips — the /cloud (Resources) toolbar chip strip (issue #366
 * item 1). This is now a THIN wrapper over the shared `KindChipStrip`
 * component: it passes the Cloud `KINDS` catalogue plus the Cloud-specific
 * `testidPrefix` / `storageKey`, and keeps its exact public API
 * (`activeKind` / `counts` / `onChange`) and every established testid
 * (`cloud-kind-chips`, `cloud-kind-chip-<id>`, `cloud-kind-chip-more…`) so
 * /cloud's behaviour and tests are unchanged.
 *
 * The single chip implementation (chip button + `+ More` overflow popover
 * + the curate-visible-chips affordances + CSS) lives in
 * `../shared/KindChipStrip.tsx` — shared with the /jobs surface. Per
 * docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the label/icon
 * catalogue still flows through `KINDS` in kinds.ts.
 */

import { KindChipStrip } from '../shared/KindChipStrip'
import { KINDS, type CloudListKind } from './kinds'

interface CloudKindChipsProps {
  /** Currently active kind. */
  activeKind: CloudListKind
  /** Per-kind count map. `null` means data unavailable (renders "—"). */
  counts: Record<CloudListKind, number | null>
  /** Switch to a different kind. */
  onChange: (next: CloudListKind) => void
}

export function CloudKindChips({ activeKind, counts, onChange }: CloudKindChipsProps) {
  return (
    <KindChipStrip<CloudListKind>
      catalogue={KINDS}
      activeKind={activeKind}
      counts={counts}
      onChange={onChange}
      testidPrefix="cloud-kind"
      storageKey="sov-cloud-hidden-kinds"
    />
  )
}
