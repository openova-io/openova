/**
 * flowFamilyPalette.ts — family colour taxonomy for the canvas.
 *
 * Replaces the palette section of the deleted flowLayoutV4.ts. The
 * palette is the *only* surface the canvas surface still uses from
 * the legacy V4 layout; everything else (multi-stage column grid,
 * Sugiyama assignment, swimlane band) was replaced by the recursive
 * fold-aware layout in flowLayoutOrganic.ts.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), callers MUST
 * NOT inline a family colour — import the palette here. The hex values
 * mirror the public product taxonomy in
 *   src/pages/wizard/steps/componentGroups.ts
 * and the v4 mockup `marketing/mockups/provision-mockup-v4.png`.
 */

/** Family palette — name + hex colour. */
export interface FlowFamily {
  /** Family id, lowercase, matches ApplicationDescriptor.familyId. */
  id: string
  /** Display label, uppercase ("PILOT", "SPINE", ...). */
  label: string
  /** Hex colour, e.g. "#818CF8". Used for ring + glow. */
  color: string
}

export const DEFAULT_FAMILIES: FlowFamily[] = [
  { id: 'catalyst', label: 'Catalyst', color: '#64748B' },
  { id: 'pilot',    label: 'PILOT',    color: '#818CF8' },
  { id: 'spine',    label: 'SPINE',    color: '#38BDF8' },
  { id: 'surge',    label: 'SURGE',    color: '#2DD4BF' },
  { id: 'silo',     label: 'SILO',     color: '#FB923C' },
  { id: 'guardian', label: 'GUARDIAN', color: '#F472B6' },
  { id: 'insights', label: 'INSIGHTS', color: '#A78BFA' },
  { id: 'fabric',   label: 'FABRIC',   color: '#FBBF24' },
  { id: 'cortex',   label: 'CORTEX',   color: '#F87171' },
  { id: 'relay',    label: 'RELAY',    color: '#34D399' },
  { id: 'platform', label: 'Platform', color: '#94A3B8' },
]
