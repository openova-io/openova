/**
 * Per-component logo tile tone — explicit metadata so each brand mark
 * renders against a backplate that mirrors how the canonical SME
 * marketplace (https://marketplace.openova.io/apps/) ships its app logos.
 *
 * Why: the marketplace serves PNG avatars with the brand-correct surface
 * BAKED INTO THE IMAGE — Cal.com ships a dark-grey PNG, Mautic ships a
 * dark-blue PNG, full-colour brand marks ship a transparent PNG that
 * blends with the card. The tile chrome is therefore zero (transparent
 * background, no padding, no border) — see `core/marketplace/.../AppsStep.svelte`
 * `.app-logo` rule. The wizard cannot do that because component logos
 * are vendored as raw upstream SVGs (no baked backplate); a single
 * universal "near-white" tile drops white-on-transparent marks
 * (Temporal, LiveKit, Mimir, Tempo, Velero, OpenBao, Mautic-style)
 * into the white pill — the exact contrast bug the user surfaced in
 * commit 691467b4.
 *
 * Mirroring the marketplace's *spirit* therefore means: pick a tile
 * surface PER ASSET, not globally. We classify each logo into one of:
 *
 *   - `light` — glyph is predominantly white / near-white. The tile
 *               must be DARK in BOTH wizard themes so the mark reads.
 *               This matches how the marketplace ships e.g. WordPress
 *               (white W on a dark navy PNG backplate).
 *
 *   - `color` — glyph is full-colour or dark-on-transparent and reads
 *               cleanly on a NEUTRAL light surface. The tile is a
 *               very light slate in BOTH wizard themes — same surface
 *               the marketplace uses for full-colour PNGs (Cal.com,
 *               ERPNext, Strapi, Mautic-orange variant). Dark glyphs
 *               (cert-manager, strimzi, ferretdb) read at 8:1+ on
 *               this surface; full-colour glyphs (cilium, grafana,
 *               keycloak, crossplane) keep their brand fidelity.
 *
 * Both tones are theme-INDEPENDENT — exactly like marketplace PNGs,
 * which look the same regardless of card theme. The card around the
 * tile flips with the theme; the tile itself is the asset's natural
 * surface.
 *
 * No third tone is needed. Validated empirically by rendering every
 * vendored logo against #ffffff / #f1f5f9 / #64748b / #0f172a /
 * transparent (see logo-backplate-test.png in the issue thread):
 *   - All `color` assets read on slate-100.
 *   - All `light` assets disappear on slate-100; read on slate-900.
 *   - No asset needs a mid-grey or transparent tile.
 *
 * If a new component-id is added without an entry here, the helper
 * below falls back to the `color` tone — the safe default since
 * almost all vendored brand marks ship dark-on-transparent or
 * full-colour, and a light slate-100 surface is the wider visibility
 * envelope. Add explicit `light` tone entries for white-glyph assets.
 */

export type LogoTone = 'light' | 'color'

/**
 * Tile surface palette — keyed by tone, identical across both wizard
 * themes (see file header). The text colour applies to the
 * letter-mark fallback so `IconFallback` / `LetterFallback` /
 * `.mp-product-icon` / `.mp-related-icon` share the tile contract.
 *
 * Border is a hairline that visually anchors the tile against the
 * card without competing with the glyph.
 */
export interface LogoToneStyle {
  background: string
  border: string
  /** Foreground for the letter-mark fallback. */
  text: string
}

export const LOGO_TONE_STYLES: Record<LogoTone, LogoToneStyle> = {
  // White / near-white glyphs — dark backplate (slate-900) so the mark
  // reads against the tile. Same in both wizard themes — the asset's
  // natural surface, not the card's.
  light: {
    background: '#0f172a',
    border: 'rgba(255,255,255,0.08)',
    text: '#f8fafc',
  },
  // Colour or dark-on-transparent glyphs — neutral light surface
  // (slate-100). Mirrors the marketplace's full-colour PNG cards.
  color: {
    background: '#f1f5f9',
    border: 'rgba(15,23,42,0.08)',
    text: '#0f172a',
  },
}

/**
 * Explicit per-component tone classification. Every vendored asset
 * under `public/component-logos/` is listed; new entries should be
 * added when a new component ships a brand mark. The classification
 * was determined empirically by rendering each asset on candidate
 * surfaces (see logo-backplate-test.png).
 *
 * Components without a vendored logo (logoUrl: null in componentGroups.ts)
 * still benefit from the tone — the letter-mark fallback uses
 * the same tile surface for visual consistency. They default to
 * `color` (light surface, dark letter) since that's the wider
 * visibility envelope — see header.
 */
const TONE_BY_ID: Record<string, LogoTone> = {
  // ── light-glyph assets (white / near-white on transparent) ────────
  // Pure white SVG marks → require dark backplate.
  temporal: 'light',
  livekit: 'light',
  // Predominantly white text-on-transparent or thin white linework
  // PNGs (each verified visually against the test panel).
  velero: 'light',
  vllm: 'light',
  vcluster: 'light',
  mimir: 'light',
  tempo: 'light',
  openmeter: 'light',
  netbird: 'light',
  neo4j: 'light',
  harbor: 'light',
  openbao: 'light',
  debezium: 'light',
  loki: 'light',
  ntfy: 'light',
  langfuse: 'light',
  superset: 'light',

  // ── color-glyph assets (default — listed for explicitness) ────────
  // Full-colour SVGs or dark-on-transparent — read on slate-100.
  alloy: 'color',
  'cert-manager': 'color',
  cilium: 'color',
  clickhouse: 'color',
  cnpg: 'color',
  coraza: 'color',
  crossplane: 'color',
  envoy: 'color',
  'external-dns': 'color',
  'external-secrets': 'color',
  falco: 'color',
  ferretdb: 'color',
  flink: 'color',
  flux: 'color',
  frpc: 'color',
  gitea: 'color',
  grafana: 'color',
  iceberg: 'color',
  keda: 'color',
  keycloak: 'color',
  knative: 'color',
  kserve: 'color',
  kyverno: 'color',
  librechat: 'color',
  litmus: 'color',
  matrix: 'color',
  milvus: 'color',
  opensearch: 'color',
  opentelemetry: 'color',
  opentofu: 'color',
  reloader: 'color',
  seaweedfs: 'color',
  sigstore: 'color',
  stalwart: 'color',
  strimzi: 'color',
  strongswan: 'color',
  stunner: 'color',
  'syft-grype': 'color',
  trivy: 'color',
  valkey: 'color',
  vpa: 'color',
}

/**
 * Resolve the tone for a component id, defaulting to `color`
 * (light surface) for any component-id not explicitly classified.
 * `color` is the wider-visibility envelope: dark letter-mark
 * fallbacks read on the light tile, and most vendored brand marks
 * ship dark-on-transparent or full-colour.
 */
export function getLogoTone(componentId: string | undefined): LogoTone {
  if (!componentId) return 'color'
  return TONE_BY_ID[componentId] ?? 'color'
}

/**
 * Convenience accessor — returns the resolved tile surface for a
 * component id directly. Used by inline-style tile renderers
 * (StepComponents, StepReview).
 */
export function getLogoToneStyle(componentId: string | undefined): LogoToneStyle {
  return LOGO_TONE_STYLES[getLogoTone(componentId)]
}
