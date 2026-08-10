import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, basename } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * orphaned-page-components.test.ts — #5831 (UAT row 192).
 *
 * A page component that NOTHING imports except its own test file renders
 * nowhere, while its test suite reports PASS. That is false coverage in the
 * ledger's own terms: the suite is green on a surface no operator can open.
 *
 * ConvergenceWizard is the case that motivated this. It owns
 * `data-testid="wizard-link-reconciliation"` — the Reconciliation → RECON-lens
 * deep-link UAT row 192 asserts — and `be5477c43` replaced the 5-phase wizard
 * with the treemap Dashboard without removing the component. Verified
 * independently rather than taken from the row: the only non-comment reference
 * anywhere in `src/` is its own test file; `router.tsx` mentions it in a COMMENT
 * only. So the link the row looks for cannot exist on any page, the row sat at
 * partial across two environments, and `ConvergenceWizard.test.tsx` was green
 * throughout.
 *
 * WHAT THIS GUARD DOES *NOT* DO. It does not decide whether that link should be
 * restored, and it must not: that is a product decision (`be5477c43` removed
 * the wizard deliberately) and row 192 flags it as needing adjudication.
 * Deleting the component or re-adding it to the router would both pre-empt that
 * call. What the guard does is make the orphan LOUD instead of silent, so the
 * decision is taken on purpose rather than by neglect.
 *
 * ALLOWLIST, NOT AUTO-PASS. A known orphan must be listed here with a reason.
 * That keeps the check honest in both directions: it cannot go green by
 * accident, and it cannot be silenced without someone writing down why.
 */

// Resolved from the vitest cwd (the ui package root), not from import.meta.url:
// under the jsdom environment that URL's pathname resolves to a bare "/src/pages"
// and the walk ENOENTs. Existence is asserted immediately so a future cwd change
// fails loudly here rather than silently reporting an empty component tree — an
// empty walk would make every page look orphaned, or worse, make the allowlist
// checks vacuous.
const SRC_DIR = join(process.cwd(), 'src')
const PAGES_DIR = join(SRC_DIR, 'pages')
if (!existsSync(PAGES_DIR)) {
  throw new Error(
    `orphan guard: ${PAGES_DIR} does not exist (cwd=${process.cwd()}). ` +
      'Refusing to run — an empty walk would report nonsense in both directions.',
  )
}

/**
 * Components that are KNOWINGLY unreferenced, each with the reason and what
 * resolves it. An entry here is a debt marker, not an exemption — it is meant to
 * be removed, either by wiring the component up or by deleting it.
 */
const KNOWN_ORPHANS: Record<string, string> = {
  'ConvergenceWizard.tsx':
    'UAT row 192 / #5831 — orphaned by be5477c43 (5-phase wizard replaced by the ' +
    'treemap Dashboard). Still owns wizard-link-reconciliation, the RECON-lens ' +
    'deep-link row 192 asserts. Awaiting the contract decision: restore the link on ' +
    'the Dashboard, or retire the assertion. Do NOT resolve this by deleting the ' +
    'file — the deep-link TARGET still works (/cloud?view=graph&lens=reconciliation ' +
    'opens the RECON lens on arrival), it is only the affordance that has no host.',

  // The Cloud per-kind tree (#3987 / #3981). router.tsx names these paths in a
  // COMMENT at ~line 1944 ("/cloud/architecture, /cloud/compute, etc. resolve")
  // but imports none of the components, and there is no generated route tree —
  // router.tsx is the single route source. So the pages are built and untested
  // against a real route. Recorded per-file rather than as one blanket entry so
  // wiring them up removes them one at a time.
  'CloudComputePage.tsx': '#3987 — Cloud/Compute landing, built (issue #309 P3) but not routed.',
  'CloudNetworkPage.tsx': '#3987 — Cloud/Network landing, built but not routed.',
  'CloudStoragePage.tsx': '#3987 — Cloud/Storage landing, built but not routed.',
  'DnsZonesPage.tsx': '#3987 — Cloud/Network per-kind list, built but not routed.',
  'IngressesPage.tsx': '#3987 — Cloud/Network per-kind list, built but not routed.',
  'ServicesPage.tsx': '#3987 — Cloud/Network per-kind list, built but not routed.',
  'StorageClassesPage.tsx': '#3987 — Cloud/Storage per-kind list, built but not routed.',

  'FlowLogFeed.tsx':
    '#5831 — flow log feed component with no importer. Unclear whether it predates ' +
    'or postdates the FlowPage rework; needs an owner decision, not a silent delete.',

  // Not a page at all — a shared SVG/logo module that happens to live under
  // src/pages/. Its only mention anywhere is inside a COMMENT in
  // wizard/steps/logoTone.ts, which the reference matcher correctly does not
  // count. Listed rather than special-cased: narrowing the guard to "route
  // components only" would need a heuristic that could hide a real orphan.
  'componentLogos.tsx':
    '#5831 — shared logo module under src/pages/ with no code reference; only a ' +
    'prose mention in logoTone.ts. Either it is dead, or its consumer was rewritten.',
}

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      walk(full, out)
    } else if (full.endsWith('.tsx') || full.endsWith('.ts')) {
      out.push(full)
    }
  }
  return out
}

const isTestFile = (p: string) => /\.(test|spec)\.[tj]sx?$/.test(p)

/**
 * Does `src` USE `name` — as opposed to merely mentioning it in prose?
 *
 * The distinction is the whole guard. `router.tsx` mentions ConvergenceWizard in
 * a comment while importing nothing, so "the name appears somewhere in the file"
 * would report it as wired.
 *
 * WHY NOT STRIP COMMENTS FIRST. That was the first cut, and it was wrong in a way
 * worth recording: a hand-rolled `/\*…*\/` stripper ate 67% of router.tsx
 * (107496 chars → 35035) because the non-greedy pair matching slips the moment a
 * string or regex literal contains `*` + `/`. The guard then reported FIFTEEN
 * orphans, fourteen of them false — including VouchersPage, which the hw292 walk
 * had already observed serving 200 at /billing/vouchers, and which router.tsx
 * imports on line 158. Publishing that list would have been a fabricated finding.
 *
 * Matching the SHAPE of a reference needs no stripping and cannot be fooled by
 * prose: an import specifier, a path-suffix import, JSX usage, a call, or a type
 * position. Deliberately broad — a false negative here means an orphan slips
 * through unnoticed, while a false positive means someone chases a component
 * that is perfectly fine.
 */
function componentMatchers(name: string): RegExp[] {
  const n = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return [
    new RegExp(`\\b(?:import|export)\\b[^\\n;]*\\b${n}\\b`),  // import/export specifier
    new RegExp(`from\\s+['"\`][^'"\`]*${n}['"\`]`),                  // path-suffix import
    new RegExp(`<\\s*${n}[\\s/>]`),                                // JSX usage
    new RegExp(`\\b${n}\\s*\\(`),                                // call
    new RegExp(`[:=]\\s*${n}\\b`),                                 // type / assignment position
  ]
}

/**
 * referencesComponent — the SAME five shapes, with the compiled matchers passed
 * in and a literal-substring pre-filter in front.
 *
 * WHY THIS SHAPE. The pair space is |pages| x |sources| — 176 x 373 = ~65,600
 * today — and the previous form rebuilt all five RegExp objects on every single
 * pair (~328,000 constructions) before testing them against whole file texts.
 * That put the walk at ~3.2s locally against vitest's 5s per-test timeout, and
 * it went red on a loaded CI runner: twice on this PR, and identically on main
 * (run 31335661070, 2026-08-09) BEFORE this PR existed. The file's own comment
 * above already names the failure mode — "a guard that goes red because it is
 * slow is indistinguishable, from CI's side, from one that found something" —
 * so this is that same lesson applied one level further in.
 *
 * SEMANTICS ARE UNCHANGED, and the pre-filter is sound rather than a heuristic:
 * all five patterns embed `name` literally, so a file that does not contain the
 * raw substring cannot match any of them. `includes` therefore only skips pairs
 * every regex would have rejected. Nothing is loosened — a component that is
 * genuinely orphaned still reports as one, which the KNOWN_ORPHANS assertions
 * below verify in both directions.
 */
function referencesComponent(src: string, name: string, matchers: RegExp[]): boolean {
  if (!src.includes(name)) return false
  return matchers.some((re) => re.test(src))
}

describe('#5831 — page components must be reachable from something other than their own test', () => {
  const pageFiles = walk(PAGES_DIR).filter((f) => !isTestFile(f))
  const allSource = walk(SRC_DIR).filter((f) => !isTestFile(f))

  // Read every source file ONCE. The first cut re-read all ~250 files for each
  // of the ~49 pages and timed out at 5s — a guard that goes red because it is
  // slow is indistinguishable, from CI's side, from one that found something.
  const sources: Array<{ file: string; text: string }> = allSource.map((file) => ({
    file,
    text: readFileSync(file, 'utf8'),
  }))

  // Compile each component's five matchers ONCE per name, not once per
  // (page, source) pair — see referencesComponent for why that mattered.
  const referencedElsewhere = (page: string, name: string): string[] => {
    const matchers = componentMatchers(name)
    return sources
      .filter((s) => s.file !== page && referencesComponent(s.text, name, matchers))
      .map((s) => s.file)
  }

  // Vacuity control FIRST. Every assertion below is "X is referenced by some
  // file in this set". If the walk returned nothing, every component would look
  // orphaned (loud, so survivable) — but more dangerously, a walk that returned
  // only the pages themselves would make self-reference look like coverage.
  it('the file walk found a real component tree', () => {
    expect(pageFiles.length, 'no page components found — the walk is broken').toBeGreaterThan(20)
    expect(
      allSource.length,
      'the source walk found no more files than the pages themselves — nothing could ever be shown as referenced',
    ).toBeGreaterThan(pageFiles.length * 2)
  })

  it('no page component is referenced only by its own test', () => {
    const orphans: string[] = []

    for (const page of pageFiles) {
      const name = basename(page).replace(/\.tsx?$/, '')
      if (referencedElsewhere(page, name).length === 0) orphans.push(basename(page))
    }

    const unexpected = orphans.filter((o) => !(o in KNOWN_ORPHANS))
    expect(
      unexpected,
      `page component(s) reachable from nothing but their own test:\n  ${unexpected.join('\n  ')}\n\n` +
        'Their test suites report PASS on a surface no operator can open — false coverage. ' +
        'Wire the component up, delete it, or add it to KNOWN_ORPHANS with the reason and ' +
        'what resolves it (#5831, UAT row 192).',
    ).toEqual([])
  })

  it('every KNOWN_ORPHANS entry is still an orphan', () => {
    // The allowlist must not outlive the problem. Once a component is wired up,
    // its entry has to go — otherwise the list slowly becomes a place where
    // reasons go to be forgotten, and the next real orphan hides among them.
    for (const [file, reason] of Object.entries(KNOWN_ORPHANS)) {
      const page = pageFiles.find((p) => basename(p) === file)
      expect(page, `KNOWN_ORPHANS names ${file}, which no longer exists — drop the entry`).toBeTruthy()

      const name = file.replace(/\.tsx?$/, '')
      expect(
        referencedElsewhere(page!, name).map((f) => relative(SRC_DIR, f)),
        `${file} is now referenced — remove it from KNOWN_ORPHANS. Its recorded reason was: ${reason}`,
      ).toEqual([])
    }
  })
})
