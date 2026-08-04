#!/usr/bin/env node
// Build-time catalog generator.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #1 (the waterfall is the contract — every
// deliverable in target-state shape) and #4 (never hardcode), the wizard's
// Components step renders cards driven by the unified Blueprint surface
// declared in `platform/<name>/blueprint.yaml` files. This script walks the
// monorepo, reads every blueprint.yaml, and emits a typed TypeScript file
// (src/shared/constants/catalog.generated.ts) consumed by StepComponents.
//
// Why build-time and not a runtime API:
//   - the Vite SPA is a static asset; no need for the catalyst-api to be up
//     for the wizard to render its component grid
//   - build-time generation guarantees the SPA shipped to a Sovereign reflects
//     the SHA-pinned blueprint set its deployment was built against
//   - matches the unified Blueprint shape from BLUEPRINT-AUTHORING.md §1: one
//     Blueprint = one card in the marketplace (when visibility: listed)
//
// Both `npm run build` and `npm run dev` invoke this via the prebuild/predev
// scripts. If a new platform/<name>/blueprint.yaml lands in the monorepo, a
// rebuild picks it up automatically — no special-cases per category, ever.
//
// Output shape mirrors the marketplace App shape in core/marketplace/ so a
// future merge of this SPA into core/console/ (per docs/PROVISIONING-PLAN.md
// §3 Phase 3) reuses the same card surface across products.

import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs'
import { basename, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))

// REPO_ROOT can be overridden via env (set by the docker build) so the
// script can find platform/, products/, and clusters/_template/bootstrap-kit/
// even when the docker build context is the UI subdirectory rather than the
// repo root. Without an override, fall back to the relative path that works
// for `npm run dev` and `npm run build` invoked from the UI dir.
const REPO_ROOT = process.env.OPENOVA_REPO_ROOT
  ? resolve(process.env.OPENOVA_REPO_ROOT)
  : resolve(__dirname, '../../../../..')
const PLATFORM_DIR = resolve(REPO_ROOT, 'platform')
const PRODUCTS_DIR = resolve(REPO_ROOT, 'products')
const BOOTSTRAP_KIT_DIR = resolve(REPO_ROOT, 'clusters/_template/bootstrap-kit')
const OUT_FILE = resolve(__dirname, '../src/shared/constants/catalog.generated.ts')
// Sister output for the Go catalyst-api side. The Sovereign Console's
// /api/v1/sovereign/apps endpoint embeds this JSON via go:embed so the
// in-cluster apps catalog mirrors the wizard's marketplace 1:1. Per
// docs/INVIOLABLE-PRINCIPLES.md #4, the Blueprint catalog has exactly
// ONE source of truth (blueprint.yaml files). This file is generated
// alongside catalog.generated.ts and committed; CI catches drift.
const OUT_FILE_JSON = resolve(REPO_ROOT, 'products/catalyst/bootstrap/api/internal/catalog/blueprints.json')

// Fail loudly if the source dirs are missing — silently producing an empty
// BOOTSTRAP_KIT[] is what shipped a broken provision page where only the
// 2 supernodes rendered. No bandaid: every Catalyst image MUST be built
// against the real source-of-truth tree.
for (const [name, dir] of [
  ['platform', PLATFORM_DIR],
  ['products', PRODUCTS_DIR],
  ['clusters/_template/bootstrap-kit', BOOTSTRAP_KIT_DIR],
]) {
  if (!existsSync(dir)) {
    console.error(
      `[build-catalog] FATAL: ${name} directory missing at ${dir}. ` +
      `Set OPENOVA_REPO_ROOT to a checkout containing platform/, products/, ` +
      `and clusters/_template/bootstrap-kit/, or run from the UI dir with the ` +
      `repo's relative layout intact.`,
    )
    process.exit(1)
  }
}

/**
 * Tiny YAML reader sufficient for the Blueprint CRD shape declared in
 * docs/BLUEPRINT-AUTHORING.md §3. We deliberately do NOT pull in `js-yaml`
 * because:
 *   - blueprint.yaml is hand-authored, simple, predictable structure
 *   - keeping the dev toolchain dependency-free at this layer matches the
 *     "no bespoke when off-the-shelf is specified" rule from
 *     INVIOLABLE-PRINCIPLES.md #2 — there is no off-the-shelf "tiny YAML
 *     subset reader for the Blueprint CRD shape", so we own this slice.
 *   - if Blueprint shape ever needs full YAML expressiveness, swap in
 *     js-yaml here in one place — every consumer reads from
 *     catalog.generated.ts and is unaffected.
 *
 * Supports the subset we need: top-level keys, nested objects (2-space
 * indent), single-line scalar values, comments after `#`. Returns a
 * plain object.
 */
function parseBlueprintYaml(raw) {
  const lines = raw.split('\n')
  const root = {}
  // Stack of {indent, container} pairs. The current line is appended to
  // whichever container has the deepest indent <= current indent.
  const stack = [{ indent: -1, container: root }]

  for (let i = 0; i < lines.length; i++) {
    const rawLine = lines[i]
    if (!rawLine || /^\s*$/.test(rawLine)) continue
    if (/^\s*#/.test(rawLine)) continue

    // Strip trailing comments (but NOT inside a quoted string — Blueprint
    // values are never quoted-with-hash, so a simple split is fine here).
    const noComment = rawLine.replace(/\s+#.*$/, '')
    const indent = noComment.match(/^( *)/)[1].length
    const content = noComment.slice(indent)

    // Pop stack frames whose indent is >= current indent (they've ended).
    //
    // #3370 fix: a YAML block list may sit at the SAME indent as its key
    // (`endpoints:\n- name: wire` — the style platform/valkey/blueprint.yaml
    // uses). The old unconditional `>=` pop dropped the key's own frame on
    // the first `- ` line, so the array-conversion below re-keyed the list
    // onto the GRANDPARENT key — replacing e.g. the whole `spec` object
    // with the endpoints array and silently nulling every spec field of
    // that blueprint in the generated catalog. For list lines, a frame at
    // EQUAL indent that was pushed by a `key:` line is the list's owner —
    // keep it.
    const isListLine = /^-(\s|$)/.test(content)
    while (
      stack.length > 1 &&
      (stack[stack.length - 1].indent > indent ||
        (stack[stack.length - 1].indent === indent &&
          !(isListLine && stack[stack.length - 1].key != null)))
    ) {
      stack.pop()
    }

    const top = stack[stack.length - 1]

    // Match `key: value` or `key:` or `- value` or `- key: value`
    const kvMatch = content.match(/^([A-Za-z0-9_.\-/]+):\s*(.*)$/)
    if (kvMatch) {
      const key = kvMatch[1]
      const valueRaw = kvMatch[2].trim()
      if (valueRaw === '') {
        // Empty value: a nested mapping or list begins on the next line.
        // Default to an object container; if the next non-empty line at a
        // deeper indent starts with '- ', we'll convert to array on demand.
        const child = {}
        if (Array.isArray(top.container)) {
          top.container.push({ [key]: child })
        } else {
          top.container[key] = child
        }
        stack.push({ indent, container: child, key })
        continue
      }
      // Inline scalar value: trim quotes if present
      const value = parseScalar(valueRaw)
      if (Array.isArray(top.container)) {
        top.container.push({ [key]: value })
      } else {
        top.container[key] = value
      }
      continue
    }

    const listMatch = content.match(/^-\s*(.*)$/)
    if (listMatch) {
      // Convert top.container to an array if it isn't already. The parent
      // mapping placed an object here on the empty-value path; replace it
      // with an array on first list item. We track that via the parent key.
      const parent = stack[stack.length - 2]
      const childKey = top.key
      if (parent && childKey != null && !Array.isArray(top.container)) {
        // Replace with a fresh array
        const arr = []
        parent.container[childKey] = arr
        top.container = arr
      }
      const itemRaw = listMatch[1].trim()
      if (itemRaw === '') {
        // Nested mapping list item
        const obj = {}
        top.container.push(obj)
        stack.push({ indent: indent + 2, container: obj })
      } else {
        // Inline scalar list item, OR `- key: value` mapping list item
        const itemKv = itemRaw.match(/^([A-Za-z0-9_.\-/]+):\s*(.*)$/)
        if (itemKv) {
          const obj = {}
          obj[itemKv[1]] = parseScalar(itemKv[2].trim())
          top.container.push(obj)
          stack.push({ indent: indent + 2, container: obj })
        } else {
          top.container.push(parseScalar(itemRaw))
        }
      }
    }
  }
  return root
}

function parseScalar(s) {
  if (s === 'true') return true
  if (s === 'false') return false
  if (s === 'null' || s === '~') return null
  if (/^-?\d+$/.test(s)) return Number(s)
  // Strip quotes
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    return s.slice(1, -1)
  }
  return s
}

/**
 * #3375 — Topology declaration extraction.
 *
 * Every blueprint.yaml carries `spec.topology` (the G117 shape promoted
 * into docs/topology-matrix.md — the agreement is the law). The tiny YAML
 * reader above parses the nested objects, but inline flow arrays
 * (`clusters: [mgmt-A, mgmt-B]`) come through `parseScalar` as the raw
 * bracketed string. `parseInlineArray` normalises those.
 *
 * The console's AppDetail Topology tab READS BACK this declaration for
 * EVERY installed app (the §1 acceptance of #3375): topology class, DR
 * mechanism (replication backend/mode + switchover mechanism + RTO/RPO),
 * and per-cluster placement roles (mgmt-A active / mgmt-B passive, …).
 * Sourced here so the read-back has the SAME source the matrix promotes
 * — never invented, never hardcoded (INVIOLABLE-PRINCIPLES.md #4).
 */
function parseInlineArray(v) {
  if (Array.isArray(v)) return v.filter((x) => x != null && x !== '')
  if (typeof v !== 'string') return []
  const s = v.trim()
  if (s.startsWith('[') && s.endsWith(']')) {
    const inner = s.slice(1, -1).trim()
    if (inner === '') return []
    return inner
      .split(',')
      .map((p) => parseScalar(p.trim()))
      .filter((x) => x != null && x !== '')
  }
  // A lone scalar promotes to a single-element list.
  return [parseScalar(s)]
}

/**
 * extractTopology turns the parsed `spec.topology` object into the typed
 * shape the console reads back. Returns null when the Blueprint declares
 * no topology block (older scaffolds), so the consumer can fall back to a
 * neutral "singleton" render rather than asserting a class it never declared.
 */
function extractTopology(topoRaw) {
  if (!topoRaw || typeof topoRaw !== 'object' || Array.isArray(topoRaw)) return null

  const supported = parseInlineArray(topoRaw.supported).map(String)
  const defaults = topoRaw.defaults && typeof topoRaw.defaults === 'object' ? topoRaw.defaults : {}
  const multiRegion =
    typeof defaults['multi-region'] === 'string' ? defaults['multi-region'] : null
  const singleRegion =
    typeof defaults['single-region'] === 'string' ? defaults['single-region'] : null

  // perTopology: { <variant>: { replication{backend,mode,lagSloSeconds},
  //   switchover{mechanism,rtoSeconds,rpoSeconds}, placement{tier,clusters[],roles{}} } }
  const perTopologyRaw =
    topoRaw.perTopology && typeof topoRaw.perTopology === 'object' && !Array.isArray(topoRaw.perTopology)
      ? topoRaw.perTopology
      : {}
  const perTopology = {}
  for (const [variant, vRaw] of Object.entries(perTopologyRaw)) {
    if (!vRaw || typeof vRaw !== 'object') continue
    const repl = vRaw.replication && typeof vRaw.replication === 'object' ? vRaw.replication : null
    const sw = vRaw.switchover && typeof vRaw.switchover === 'object' ? vRaw.switchover : null
    const pl = vRaw.placement && typeof vRaw.placement === 'object' ? vRaw.placement : null
    const roles =
      pl && pl.roles && typeof pl.roles === 'object' && !Array.isArray(pl.roles)
        ? Object.fromEntries(Object.entries(pl.roles).map(([k, v]) => [k, String(v)]))
        : {}
    perTopology[variant] = {
      replication: repl
        ? {
            backend: typeof repl.backend === 'string' ? repl.backend : null,
            mode: typeof repl.mode === 'string' ? repl.mode : null,
            lagSloSeconds: typeof repl.lagSloSeconds === 'number' ? repl.lagSloSeconds : null,
          }
        : null,
      switchover: sw
        ? {
            mechanism: typeof sw.mechanism === 'string' ? sw.mechanism : null,
            rtoSeconds: typeof sw.rtoSeconds === 'number' ? sw.rtoSeconds : null,
            rpoSeconds: typeof sw.rpoSeconds === 'number' ? sw.rpoSeconds : null,
          }
        : null,
      placement: pl
        ? {
            tier: typeof pl.tier === 'string' ? pl.tier : null,
            clusters: parseInlineArray(pl.clusters).map(String),
            roles,
          }
        : null,
    }
  }

  if (supported.length === 0 && Object.keys(perTopology).length === 0) return null
  return { supported, multiRegion, singleRegion, perTopology }
}

/**
 * #3656 (founder #6) — declared user-UI signal.
 *
 * The console's AppCard must render a confident, disabled "Open…"
 * placeholder for a front-door app while its live externalURL is still
 * resolving — and NOTHING for a headless app — so the Open chip never
 * pops in late (assert-then-retract). To do that without an app-name
 * hardcode, the card needs a STATIC, build-time signal of whether a
 * Blueprint exposes a user UI at all.
 *
 * This mirrors the canonical backend predicate `blueprintHasUserUIEndpoint`
 * (products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go)
 * EXACTLY so the FE placeholder and the BE-projected externalURL agree:
 * an endpoint is a user UI when ANY of
 *   - ssoEnabled === true    (the app fronts an OIDC login the operator uses), OR
 *   - launchDefault === true (the chart-tagged "open me" front door), OR
 *   - name === "ui"          (the conventional UI-endpoint name).
 * API/protocol-only endpoints (an OCI registry, a backend proxy target)
 * set none of these and do NOT qualify. Returns false when the Blueprint
 * declares no endpoints — the same fail-closed default the BE uses.
 */
function blueprintHasUserUIEndpoint(endpointsRaw) {
  if (!Array.isArray(endpointsRaw)) return false
  for (const ep of endpointsRaw) {
    if (!ep || typeof ep !== 'object') continue
    const name = typeof ep.name === 'string' ? ep.name : ''
    if (ep.ssoEnabled === true || ep.launchDefault === true || name.toLowerCase() === 'ui') {
      return true
    }
  }
  return false
}

function listBlueprintFiles(dir) {
  if (!existsSync(dir)) return []
  const out = []
  for (const name of readdirSync(dir, { withFileTypes: true })) {
    if (!name.isDirectory()) continue
    const candidate = resolve(dir, name.name, 'blueprint.yaml')
    if (existsSync(candidate)) out.push(candidate)
  }
  return out
}

function buildCatalog() {
  const platformFiles = listBlueprintFiles(PLATFORM_DIR)
  const productsFiles = listBlueprintFiles(PRODUCTS_DIR)
  const allFiles = [...platformFiles, ...productsFiles]

  const entries = []
  const skipped = []

  for (const file of allFiles) {
    let parsed
    try {
      parsed = parseBlueprintYaml(readFileSync(file, 'utf8'))
    } catch (err) {
      skipped.push({ file, reason: `parse error: ${err.message}` })
      continue
    }
    const meta = parsed?.metadata ?? {}
    const spec = parsed?.spec ?? {}
    const card = spec?.card ?? {}
    const rawName = meta?.name
    const visibility = spec?.visibility ?? 'unlisted'

    // #3375 — canonical id derivation. The contract is `bp-<folder>`. Many
    // bootstrap blueprints (harbor, grafana, loki, tempo, trivy, velero, …)
    // declare `metadata.name: <folder>` WITHOUT the `bp-` prefix — they
    // install fine (the HR name is `bp-<folder>`) but the old hard
    // `startsWith('bp-')` gate dropped them from the catalog ENTIRELY, so
    // their AppDetail page never had a catalog entry and their Topology tab
    // could not read back a declared class. The folder name IS the canonical
    // slug (it matches the bootstrap-kit NN-<name>.yaml derivation), so when
    // metadata.name is present but un-prefixed we derive `bp-<folder>` and
    // keep going. Genuinely-missing / non-string names are still skipped.
    const folderSlug = basename(dirname(file)).replace(/^bp-/, '')
    let name
    if (typeof rawName === 'string' && rawName.startsWith('bp-')) {
      name = rawName
    } else if (typeof rawName === 'string' && rawName.trim() !== '' && rawName.replace(/^bp-/, '') === folderSlug) {
      // Un-prefixed name that matches the folder — derive the canonical id.
      name = `bp-${folderSlug}`
    } else {
      skipped.push({ file, reason: `missing or invalid metadata.name (must be bp-<name> or match folder): ${rawName}` })
      continue
    }

    // Slug = the bp-<name> identifier WITHOUT the bp- prefix; we keep the
    // raw "bp-<name>" id alongside so consumers can write either form.
    const slug = name.replace(/^bp-/, '')

    // #3370 — shareability declaration. `shareable: true` marks a
    // Blueprint whose instances support multi-application reuse; the
    // companion contextSchema declares what a Context is (kind label,
    // where Context entries live in the instance values, what a Context
    // needs and produces). All generic console surfaces (catalog badge,
    // Contexts tab, reuse selector) render from this declaration only.
    const ctxRaw = spec.contextSchema
    const contextSchema =
      ctxRaw && typeof ctxRaw === 'object' && !Array.isArray(ctxRaw)
        ? {
            kind: typeof ctxRaw.kind === 'string' ? ctxRaw.kind : '',
            valuesKey: typeof ctxRaw.valuesKey === 'string' ? ctxRaw.valuesKey : '',
            needs: Array.isArray(ctxRaw.needs) ? ctxRaw.needs.filter(n => typeof n === 'string') : [],
            produces: Array.isArray(ctxRaw.produces) ? ctxRaw.produces.filter(p => typeof p === 'string') : [],
          }
        : null

    // #3648 — producesInstances: the DECLARED operator→instance edge. An
    // operator Blueprint (bp-cnpg) that declares it is the engine class;
    // the platform reads kind + instanceBlueprint instead of inferring the
    // pairing from a "postgres" literal. null when the Blueprint is not an
    // instance-producing operator.
    const prodRaw = spec.producesInstances
    const producesInstances =
      prodRaw &&
      typeof prodRaw === 'object' &&
      !Array.isArray(prodRaw) &&
      typeof prodRaw.kind === 'string' &&
      prodRaw.kind.trim() !== '' &&
      typeof prodRaw.instanceBlueprint === 'string' &&
      prodRaw.instanceBlueprint.trim() !== ''
        ? { kind: prodRaw.kind, instanceBlueprint: prodRaw.instanceBlueprint }
        : null

    entries.push({
      id: name,
      slug,
      title: card.title ?? slug,
      summary: card.summary ?? '',
      icon: card.icon ?? null,
      category: card.category ?? null,
      tagline: card.tagline ?? null,
      tags: Array.isArray(card.tags) ? card.tags : [],
      visibility,
      version: spec.version ?? null,
      section: meta?.labels?.['catalyst.openova.io/section'] ?? null,
      depends: Array.isArray(spec.depends)
        ? spec.depends.map(d => (typeof d === 'string' ? d : d?.blueprint)).filter(Boolean)
        : [],
      shareable: spec.shareable === true,
      contextSchema,
      // #3648 — declared operator→instance edge (null unless this is an
      // instance-producing operator like bp-cnpg).
      producesInstances,
      // #3375 — declared topology + DR contract (read back by the
      // AppDetail Topology tab for EVERY app). null when the Blueprint
      // ships no `spec.topology` block.
      topology: extractTopology(spec.topology),
      // #3656 (founder #6) — declared user-UI signal. Drives the AppCard
      // disabled-"Open…" placeholder while the live externalURL resolves,
      // so a front-door app's Open chip never pops in late and a headless
      // app never flashes a transient chip. The explicit
      // spec.hasUserUIEndpoint declaration wins when the author sets it;
      // otherwise derive it from spec.endpoints[] with the SAME rule the BE
      // uses (blueprintHasUserUIEndpoint: ssoEnabled / launchDefault / name="ui").
      hasUserUIEndpoint:
        typeof spec.hasUserUIEndpoint === 'boolean'
          ? spec.hasUserUIEndpoint
          : blueprintHasUserUIEndpoint(spec.endpoints),
    })
  }

  // Stable order: by id
  entries.sort((a, b) => a.id.localeCompare(b.id))
  return { entries, skipped }
}

/**
 * Walk clusters/_template/bootstrap-kit/ and return the ordered list of
 * Blueprints the cloud-init layer guarantees to install on every Sovereign
 * before any user-selected component lands. The numbered prefix on each
 * filename encodes install order, so we sort by basename and strip the
 * leading "NN-" to derive the bp-<name> id.
 *
 * Skips kustomization.yaml and any non-NN-prefixed file. Returns an array
 * shaped { id, label, file, order } so the provision.html DAG view can wire
 * the always-installed supernodes deterministically.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the bootstrap-kit list is the file
 * tree itself — no separate constant in TypeScript. Add a new
 * NN-<name>.yaml under clusters/_template/bootstrap-kit/ and the wizard's
 * provisioning DAG picks it up on next `npm run build:catalog`.
 */
function listBootstrapKit() {
  if (!existsSync(BOOTSTRAP_KIT_DIR)) return []
  const out = []
  for (const name of readdirSync(BOOTSTRAP_KIT_DIR)) {
    // Ignore non-yaml + the kustomization manifest (it's not a Blueprint).
    if (!name.endsWith('.yaml')) continue
    if (name === 'kustomization.yaml') continue
    const m = name.match(/^(\d+)-(.+)\.yaml$/)
    if (!m) continue
    const order = Number(m[1])
    // Strip the optional `bp-` prefix from the captured filename remainder
    // BEFORE forming the canonical Blueprint id. Some bootstrap-kit files
    // are named with the `bp-` already present (e.g.
    // `13-bp-catalyst-platform.yaml`) for historical reasons — without
    // this strip, the captured slug would be `bp-catalyst-platform` and
    // the emitted id would be `bp-bp-catalyst-platform`, which:
    //   (a) produces doubled `/app/bp-bp-*` hrefs on the AppsPage card
    //       grid (BUG-001 / D19 on t129.omani.works, 2026-05-16)
    //   (b) breaks the FE↔BE join in /api/v1/sovereign/apps where HR
    //       name `bp-<slug>` is matched against blueprint id `bp-<slug>`
    //   (c) prints `bp-` twice in the operator-facing card title fallback
    // Canonical contract: filename `NN[-bp]-<slug>.yaml` → id `bp-<slug>`
    // → slug `<slug>` (no bp- prefix), matching the blueprint.yaml side.
    const slug = m[2].replace(/^bp-/, '')
    out.push({
      id: `bp-${slug}`,
      slug,
      // Display label = the slug (consumers prettify per their own UI rules).
      label: slug,
      file: name,
      order,
    })
  }
  // Stable order = numerical prefix ascending.
  out.sort((a, b) => a.order - b.order)
  return out
}

function emit({ entries, skipped, bootstrapKit }) {
  const banner = `// AUTO-GENERATED — do not edit by hand.
//
// Generated by products/catalyst/bootstrap/ui/scripts/build-catalog.mjs from
// every platform/<name>/blueprint.yaml + products/<name>/blueprint.yaml in
// the monorepo. Re-run \`npm run build\` (or \`npm run dev\`) in this package
// after adding or modifying a blueprint.yaml; the prebuild script wires this.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode") this is the ONLY
// place the Catalyst component catalog lives. Both StepComponents (wizard
// app selection) and StepProvisioning (progress timeline) read from here.
//
// Source-of-truth files: see PLATFORM_BLUEPRINT_FILES in this file.
`

  const tsBody = `${banner}
export type BlueprintVisibility = 'listed' | 'unlisted' | 'private'

export interface BlueprintCardEntry {
  /** Full Blueprint id, e.g. "bp-cilium". */
  id: string
  /** Slug = id without the "bp-" prefix. */
  slug: string
  /** Display title (card.title). Falls back to slug. */
  title: string
  /** One-paragraph summary (card.summary). */
  summary: string
  /** Optional icon path (card.icon). Relative to the Blueprint folder. */
  icon: string | null
  /** Optional marketplace category (card.category). */
  category: string | null
  /** Optional one-line tagline (card.tagline). */
  tagline: string | null
  /** Tags (card.tags). */
  tags: string[]
  /** listed = appears in marketplace, unlisted = mandatory infra, private = org-only. */
  visibility: BlueprintVisibility
  /** Blueprint semver. */
  version: string | null
  /** PTS section label (catalyst.openova.io/section), e.g. "pts-3-3-security-and-policy". */
  section: string | null
  /** Other Blueprint ids this Blueprint depends on. */
  depends: string[]
  /** #3370 — instances support multi-application reuse (Contexts). */
  shareable: boolean
  /** #3370 — the Context declaration (null when not shareable). */
  contextSchema: BlueprintContextSchema | null
  /**
   * #3648 — the DECLARED operator→instance edge. Set on an OPERATOR
   * Blueprint (e.g. bp-cnpg) that produces shareable instances of another
   * (instance) Blueprint; the platform reads kind + instanceBlueprint
   * instead of inferring the pairing from a "postgres" literal. null
   * unless this is an instance-producing operator.
   */
  producesInstances: BlueprintProducesInstances | null
  /**
   * #3375 — declared topology + DR contract, lifted verbatim from the
   * Blueprint's \`spec.topology\` (the same source docs/topology-matrix.md
   * promotes). Read back by the AppDetail Topology tab for every app so
   * the operator sees the app's real declared class + DR mechanism +
   * per-cluster placement — not a blank editor. null when the Blueprint
   * ships no topology block.
   */
  topology: BlueprintTopology | null
  /**
   * #3656 (founder #6) — whether this Blueprint declares a user-facing UI
   * endpoint (ssoEnabled / launchDefault / name="ui" in spec.endpoints[]),
   * mirroring the BE's blueprintHasUserUIEndpoint. The AppCard reads this
   * to show a confident disabled "Open…" placeholder for a front-door app
   * while its live externalURL resolves (no late pop-in), and to suppress
   * any placeholder for a headless app (no transient chip).
   */
  hasUserUIEndpoint: boolean
}

/**
 * #3648 — the operator→instance declaration. \`kind\` is the engine-class
 * id a consumer names in \`backingServices[].type\` (e.g. "postgres");
 * \`instanceBlueprint\` is the instance Blueprint id this operator
 * provisions (e.g. "bp-postgres") — the dependsOn HR prefix.
 */
export interface BlueprintProducesInstances {
  kind: string
  instanceBlueprint: string
}

/**
 * #3370 — what a Context is for a shareable Blueprint. The kind label
 * (db | topic | bucket | keyspace) prefixes every Context in the
 * console; valuesKey names the array in the instance's IaC values where
 * Context entries live; needs/produces describe the provisioning form
 * inputs and the consumer-visible outputs.
 */
export interface BlueprintContextSchema {
  kind: string
  valuesKey: string
  needs: string[]
  produces: string[]
}

/**
 * #3375 — the per-Blueprint topology declaration the console reads back.
 *
 *   supported       — declared topology variants (e.g. [active-hot-standby, singleton])
 *   multiRegion     — defaults.multi-region (the class a 2-region Sovereign picks)
 *   singleRegion    — defaults.single-region (the class a 1-region Sovereign picks)
 *   perTopology     — DR contract per variant (replication + switchover + placement roles)
 */
export interface BlueprintTopology {
  supported: string[]
  multiRegion: string | null
  singleRegion: string | null
  perTopology: Record<string, BlueprintTopologyVariant>
}

export interface BlueprintTopologyVariant {
  replication: {
    backend: string | null
    mode: string | null
    lagSloSeconds: number | null
  } | null
  switchover: {
    mechanism: string | null
    rtoSeconds: number | null
    rpoSeconds: number | null
  } | null
  placement: {
    tier: string | null
    clusters: string[]
    roles: Record<string, string>
  } | null
}

/**
 * Every Blueprint discovered at build time. Order is stable (sorted by id).
 *
 * StepComponents filters this with \`visibility === 'listed'\` to render the
 * marketplace card grid; StepProvisioning uses the same source to label
 * bootstrap-kit phases when the SSE backend emits Flux Kustomization events.
 */
export const ALL_BLUEPRINTS: readonly BlueprintCardEntry[] = ${JSON.stringify(entries, null, 2)} as const

/** Subset of ALL_BLUEPRINTS whose visibility is 'listed' — what shows in the wizard's StepComponents card grid. */
export const LISTED_BLUEPRINTS: readonly BlueprintCardEntry[] = ALL_BLUEPRINTS.filter(b => b.visibility === 'listed')

/** Distinct categories present on listed blueprints (for category chip filter). */
export const LISTED_CATEGORIES: readonly string[] = Array.from(
  new Set(LISTED_BLUEPRINTS.map(b => b.category).filter((c): c is string => !!c))
).sort()

/** Lookup: id → entry. */
export const BLUEPRINT_BY_ID: Readonly<Record<string, BlueprintCardEntry>> = Object.fromEntries(
  ALL_BLUEPRINTS.map(b => [b.id, b])
)

/**
 * #3375 — Lookup: blueprint id → declared topology (null when the
 * Blueprint ships no topology block). The AppDetail Topology tab resolves
 * an app's matrix-declared class + DR mechanism + per-cluster roles from
 * here, keyed on either the \`bp-<name>\` id or the bare slug.
 */
export const TOPOLOGY_BY_ID: Readonly<Record<string, BlueprintTopology>> = Object.fromEntries(
  ALL_BLUEPRINTS.filter(b => b.topology != null).flatMap(b => {
    const t = b.topology as BlueprintTopology
    return [[b.id, t], [b.slug, t]]
  })
)

/** Source files this catalog was built from (for diagnostics / CI logs). */
export const PLATFORM_BLUEPRINT_FILES: readonly string[] = ${JSON.stringify(
    [...new Set(entries.map(e => `platform/${e.slug}/blueprint.yaml`))].sort(),
    null,
    2
  )} as const

/**
 * Bootstrap-kit Blueprints reconciled by Flux on every freshly-provisioned
 * Sovereign cluster, in numerical install order (the NN- prefix on each
 * file under clusters/_template/bootstrap-kit/). The provision page reads
 * this to render the always-installed bubbles in dependency / install
 * order alongside the user's selected components.
 */
export interface BootstrapKitEntry {
  /** Full Blueprint id, e.g. "bp-cilium". */
  id: string
  /** Slug = id without the "bp-" prefix. */
  slug: string
  /** Display label = slug. */
  label: string
  /** Source filename under clusters/_template/bootstrap-kit/. */
  file: string
  /** Numerical prefix on the filename — defines install order. */
  order: number
}

export const BOOTSTRAP_KIT: readonly BootstrapKitEntry[] = ${JSON.stringify(bootstrapKit, null, 2)} as const
`

  mkdirSync(dirname(OUT_FILE), { recursive: true })
  writeFileSync(OUT_FILE, tsBody)

  // Sister JSON output for the Go catalyst-api side. The Sovereign
  // Console's /api/v1/sovereign/apps endpoint embeds this via go:embed.
  //
  // #5520 — `generatedAt` marks when the catalog CONTENT last changed, NOT
  // when someone last ran a build. A wall-clock stamp made this generated
  // file non-deterministic: every `npm run build` rewrote it, so every PR
  // that ran a real build carried a spurious diff. Authors then either
  // committed unrelated churn or reverted it by hand — and because the
  // revert path was the common one, the committed file never converged with
  // the generator (that is how the 508-line drift accumulated).
  //
  // Preserving the prior stamp when nothing else changed makes the field
  // both deterministic AND more truthful: consumers (catalog.GeneratedAt →
  // /sovereign/apps + /org applications) want to know when the catalog they
  // are serving last CHANGED, which a rebuild-of-identical-input does not.
  const payload = { blueprints: entries, bootstrapKit }
  let generatedAt = new Date().toISOString()
  try {
    const prev = JSON.parse(readFileSync(OUT_FILE_JSON, 'utf8'))
    const { generatedAt: prevStamp, ...prevPayload } = prev
    if (prevStamp && JSON.stringify(prevPayload) === JSON.stringify(payload)) {
      generatedAt = prevStamp // content identical — keep the real change time
    }
  } catch {
    // No prior file (or unreadable/corrupt) — this IS a fresh generation.
  }
  const jsonBody = JSON.stringify({ generatedAt, ...payload }, null, 2)
  mkdirSync(dirname(OUT_FILE_JSON), { recursive: true })
  writeFileSync(OUT_FILE_JSON, jsonBody + '\n')

  console.log(`[build-catalog] wrote ${entries.length} blueprints to ${OUT_FILE}`)
  console.log(`[build-catalog] wrote ${entries.length} blueprints to ${OUT_FILE_JSON}`)
  console.log(`[build-catalog]   listed:   ${entries.filter(e => e.visibility === 'listed').length}`)
  console.log(`[build-catalog]   unlisted: ${entries.filter(e => e.visibility === 'unlisted').length}`)
  console.log(`[build-catalog]   private:  ${entries.filter(e => e.visibility === 'private').length}`)
  if (skipped.length > 0) {
    console.warn(`[build-catalog] skipped ${skipped.length} file(s):`)
    for (const s of skipped) console.warn(`  ${s.file}: ${s.reason}`)
  }
}

{
  const built = buildCatalog()
  emit({ ...built, bootstrapKit: listBootstrapKit() })
}
