// #144 — Smoke: marketplace card grid renders unified bp-<x> Blueprints.
//
// IMPORTANT scope note (per principle #1, never speculate):
//
// "Marketplace card grid" in the Catalyst architecture (Pass 103/104+
// unification) is the Catalyst wizard's StepComponents grid. Every
// Application is a `bp-<name>` Blueprint with a unified `card` shape; that
// grid is rendered by:
//
//   products/catalyst/bootstrap/ui/src/pages/wizard/steps/StepComponents.tsx
//
// from the auto-generated catalog at:
//
//   products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts
//
// The separate SME marketplace at core/marketplace renders SaaS *Apps*
// (WordPress, Ghost, Nextcloud, …), NOT bp-<x> Blueprints — that's a
// different product surface and is intentionally out of scope for this
// ticket.
//
// What this test asserts:
//
//   1. The catalog data layer enumerates Blueprints in the
//      `bp-<name>` shape. Today (Pass 105) every published blueprint.yaml
//      has visibility:unlisted (mandatory infra), so LISTED_BLUEPRINTS is
//      empty. Asserting "≥11 listed cards" against the live UI would fail
//      against the current truth. Instead we assert the underlying
//      ALL_BLUEPRINTS list contains the expected mandatory infra Blueprints
//      with `bp-` prefix and a non-empty version. When `visibility: listed`
//      is added to ≥1 Blueprint, this test will be augmented to also assert
//      the rendered card grid.
//
//   2. If the wizard UI is reachable AND any Blueprint is `listed`, we
//      assert the StepComponents grid renders cards for those Blueprints.
//      Otherwise we assert the documented EmptyState ("No marketplace
//      Blueprints published yet" copy from StepComponents.tsx).
//
// This honesty about current vs. target state is deliberate. A test that
// fakes "11 cards" by patching the catalog is a deferred regression — it
// would silently pass when the marketplace is empty, masking a real
// production gap.

import { test, expect } from '@playwright/test'
import { reachable } from './_helpers'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const CATALOG_PATH = resolve(
  __dirname,
  '../../../../products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts',
)

const BASE_URL = process.env.BASE_URL || 'http://localhost:4321'
const COMPONENTS_URL = `${BASE_URL}/sovereign/wizard`

// Mandatory-infra Blueprints we expect to find in the catalog (Pass 105
// snapshot). Source: every platform/<name>/blueprint.yaml in the public repo.
const KNOWN_BP_IDS = [
  'bp-cert-manager',
  'bp-cilium',
  'bp-crossplane',
  'bp-flux',
  'bp-gitea',
  'bp-keycloak',
  'bp-nats-jetstream',
  'bp-openbao',
  'bp-sealed-secrets',
  'bp-spire',
] as const

/**
 * Cheap parse of catalog.generated.ts — pull the {id, version, visibility}
 * triples out of ALL_BLUEPRINTS without spinning up a TypeScript pipeline.
 */
function parseCatalog(): Array<{ id: string; version: string | null; visibility: string }> {
  const src = readFileSync(CATALOG_PATH, 'utf8')
  // Slice out the ALL_BLUEPRINTS literal and split on "{" so each fragment
  // contains exactly one entry's id/version/visibility, then extract from
  // each fragment independently. This avoids the greedy-cross-entry trap
  // a single multi-group regex falls into when entries appear in any order.
  // Find the literal array assignment: `ALL_BLUEPRINTS: readonly ...[] = [`.
  // The first `[` after the identifier is the type annotation `[]`; the array
  // literal is the next `[` after that.
  const startIdx = src.indexOf('ALL_BLUEPRINTS')
  if (startIdx < 0) return []
  const eqIdx = src.indexOf('=', startIdx)
  if (eqIdx < 0) return []
  const arrStart = src.indexOf('[', eqIdx)
  if (arrStart < 0) return []
  let depth = 0
  let arrEnd = -1
  for (let i = arrStart; i < src.length; i++) {
    const c = src[i]
    if (c === '[') depth++
    else if (c === ']') {
      depth--
      if (depth === 0) { arrEnd = i; break }
    }
  }
  if (arrEnd < 0) return []
  const block = src.slice(arrStart, arrEnd + 1)

  const out: Array<{ id: string; version: string | null; visibility: string }> = []
  // Each entry is a `{ ... }` literal — split by `},` boundaries.
  const fragments = block.split(/\},\s*\{/)
  for (const f of fragments) {
    const id = /"id":\s*"([^"]+)"/.exec(f)?.[1]
    if (!id) continue
    const versionMatch = /"version":\s*(?:"([^"]*)"|null)/.exec(f)
    const visibility = /"visibility":\s*"([^"]+)"/.exec(f)?.[1]
    if (!visibility) continue
    out.push({ id, version: versionMatch?.[1] ?? null, visibility })
  }
  return out
}

test.describe('#144 unified bp-<x> Blueprint catalog smoke', () => {
  test('catalog.generated.ts contains mandatory infra Blueprints with bp- prefix and version', () => {
    const entries = parseCatalog()
    expect(entries.length, 'at least one Blueprint discovered').toBeGreaterThan(0)

    const byId = new Map(entries.map(e => [e.id, e]))
    for (const id of KNOWN_BP_IDS) {
      const e = byId.get(id)
      expect(e, `expected Blueprint "${id}" in catalog.generated.ts`).toBeDefined()
      expect(e!.id.startsWith('bp-'), `${id} starts with "bp-"`).toBeTruthy()
      expect(e!.version, `${id} has a non-null semver`).toBeTruthy()
      // Mandatory infra is unlisted; once authoring lands a `listed` one,
      // this test will keep working — we only assert the prefix+version
      // shape here.
    }
  })

  test('Blueprint count is at least the known mandatory-infra set', () => {
    const entries = parseCatalog()
    expect(entries.length).toBeGreaterThanOrEqual(KNOWN_BP_IDS.length)
  })

  test('rendered StepComponents grid matches catalog visibility', async ({ page }) => {
    const ok = await reachable(COMPONENTS_URL)
    test.skip(!ok, `Catalyst UI not reachable at ${COMPONENTS_URL} — run \`npm run dev\` in products/catalyst/bootstrap/ui or set BASE_URL`)

    const entries = parseCatalog()
    const listed = entries.filter(e => e.visibility === 'listed')

    await page.goto(COMPONENTS_URL)

    // Walk forward through the wizard until StepComponents is reachable, OR
    // bail if the run-time wizard model has changed. We try up to 6 Continue
    // clicks (StepOrg → StepTopology → StepProvider → StepCredentials →
    // StepComponents). If the title `Applications` shows up, we're there.
    let onStepComponents = false
    for (let i = 0; i < 6; i++) {
      const title = await page.locator('h2.corp-step-title').first().textContent({ timeout: 5_000 }).catch(() => null)
      if (title && /Applications/i.test(title)) { onStepComponents = true; break }
      const next = page.getByRole('button', { name: /Continue|Next/i }).first()
      if (!(await next.isVisible().catch(() => false))) break
      // Continue may be disabled if step has unfilled required fields. In
      // that case we skip the rest of the assertion — UI navigation is
      // covered by #142.
      if (await next.isDisabled().catch(() => true)) break
      await next.click()
    }

    test.skip(!onStepComponents, 'Could not reach StepComponents from the initial step (likely required-field gating); covered by #142.')

    if (listed.length === 0) {
      // Empty-state copy verbatim from StepComponents.tsx EmptyState() —
      // proves the unified card grid surface is wired even with no listed
      // Blueprints today.
      await expect(page.getByText(/No marketplace Blueprints published yet/i)).toBeVisible({ timeout: 10_000 })
    } else {
      // ≥1 listed Blueprint exists — assert the rendered grid shows them.
      for (const e of listed) {
        await expect(page.getByText(new RegExp(e.id.replace(/^bp-/, ''), 'i')).first()).toBeVisible({ timeout: 10_000 })
      }
    }
  })
})
