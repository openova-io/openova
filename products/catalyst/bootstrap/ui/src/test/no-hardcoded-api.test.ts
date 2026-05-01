/**
 * Regression guardrail for issue #494 — every fetch / EventSource
 * argument MUST go through `${API_BASE}` (or the `apiUrl()` helper) and
 * MUST NOT be a hardcoded `/api/...` literal.
 *
 * Why this exists
 * ───────────────
 * The UI is served under Vite `base: '/sovereign/'`. When a component
 * issues `fetch('/api/v1/foo')`, the browser sends
 *   https://console.openova.io/api/v1/foo
 * which has NO Traefik route on `console.openova.io` (only `/sovereign/*`
 * is mapped to the catalyst-ui ingress). The cure is `${API_BASE}/v1/foo`
 * which derives the tier prefix from `import.meta.env.BASE_URL` at build
 * time and emits the correct `/sovereign/api/v1/foo`.
 *
 * The bug pattern was already fixed once and reappeared in 2026-04 (the
 * JobDetail page hit it during otech9 bootstrap). This test fails CI if
 * it ever returns.
 *
 * What it scans
 * ─────────────
 * Every file under `src/` whose extension is `.ts` or `.tsx`, except
 * test files and this file itself. For each file it:
 *
 *   1. Strips block comments (slash-star … star-slash) and line
 *      comments (`// ...`).
 *      Comments are allowed to mention `/api/v1/...` in JSDoc.
 *   2. Searches for the antipatterns:
 *        fetch( <whitespace>? <quote> /api/ ...
 *        new EventSource( <whitespace>? <quote> /api/ ...
 *        axios.<method>( <whitespace>? <quote> /api/ ...
 *      where <quote> ∈ {' " `} (the latter for template literals where
 *      the `/api/` would land at position 0 of the string with no
 *      preceding `${API_BASE}`).
 *
 * If any match is found, the test fails with a list of `path:line` plus
 * the offending source line, so the CI log is actionable on its own.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (never compromise) — this is the
 * SAME source of truth used by the linter, not a relaxed substring
 * check. Per #4 (never hardcode) — the test reads the antipattern set
 * from a single regex defined here, no inline literals scattered around
 * the suite.
 */

import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { resolve, join, relative, sep } from 'node:path'

const SRC_ROOT = resolve(__dirname, '..')
const REPO_REL_ROOT = resolve(__dirname, '..', '..')

/**
 * Antipattern detector. Captures:
 *   group 1  the call site (`fetch`, `new EventSource`, or `axios.<m>`)
 *   group 2  the opening quote
 *
 * The lookahead `\\/api\\/` requires the URL to begin with `/api/` AT
 * the very first character of the string literal, which is exactly the
 * shape that bypasses `${API_BASE}`. Template literals that start with
 * `${API_BASE}/v1/...` are fine because the first character of the
 * literal is `$` (interpolation), not `/`.
 */
const ANTIPATTERN =
  /(fetch|new\s+EventSource|axios\.\w+)\s*\(\s*(['"`])\/api\//g

/** Files that are by-design exempt from the rule. */
const EXEMPT_BASENAMES = new Set<string>([
  'no-hardcoded-api.test.ts',
])

function listSourceFiles(root: string): string[] {
  const out: string[] = []
  function walk(dir: string) {
    for (const entry of readdirSync(dir)) {
      const abs = join(dir, entry)
      const st = statSync(abs)
      if (st.isDirectory()) {
        // Skip vendored / generated directories that aren't shipped.
        if (entry === 'node_modules' || entry === 'dist' || entry === '.astro')
          continue
        walk(abs)
        continue
      }
      if (!entry.endsWith('.ts') && !entry.endsWith('.tsx')) continue
      // Test files are allowed to mention the antipattern in fixtures
      // and assertions without tripping the guardrail. This test file
      // itself is the canonical example.
      if (entry.endsWith('.test.ts') || entry.endsWith('.test.tsx')) continue
      if (entry.endsWith('.spec.ts') || entry.endsWith('.spec.tsx')) continue
      if (EXEMPT_BASENAMES.has(entry)) continue
      out.push(abs)
    }
  }
  walk(root)
  return out
}

/**
 * Strip JS/TS comments so a doc comment that mentions `fetch('/api/...')`
 * as documentation doesn't count as a violation. Order matters: kill
 * block comments first so line-comment markers nested inside survive
 * the strip — otherwise we'd risk re-matching `//` from inside a JSDoc.
 */
function stripComments(src: string): string {
  // Block comments — non-greedy.
  let out = src.replace(/\/\*[\s\S]*?\*\//g, (m) =>
    // Preserve newlines so line numbers stay aligned with the original.
    m.replace(/[^\n]/g, ' '),
  )
  // Line comments — anything from `//` to end of line. We don't try to
  // be smart about strings; the antipattern doesn't survive in a real
  // string literal anyway because the regex anchors on `fetch(`.
  out = out.replace(/(^|[^:\\])\/\/[^\n]*/g, (_, prefix) => prefix)
  return out
}

interface Hit {
  file: string
  line: number
  text: string
}

describe('no hardcoded /api/ paths in fetch/EventSource (issue #494)', () => {
  const files = listSourceFiles(SRC_ROOT)

  it('finds at least one source file to scan (sanity)', () => {
    expect(files.length).toBeGreaterThan(50)
  })

  it('every fetch / EventSource / axios call routes through API_BASE', () => {
    const hits: Hit[] = []

    for (const abs of files) {
      const src = readFileSync(abs, 'utf8')
      const cleaned = stripComments(src)
      ANTIPATTERN.lastIndex = 0
      let m: RegExpExecArray | null
      while ((m = ANTIPATTERN.exec(cleaned)) !== null) {
        // Compute the line number from byte offset.
        const upTo = cleaned.slice(0, m.index)
        const line = upTo.split('\n').length
        // Pull the original (un-stripped) line so the error message
        // shows the actual source, not the comment-blanked version.
        const origLine = src.split('\n')[line - 1] ?? ''
        hits.push({
          file: relative(REPO_REL_ROOT, abs).split(sep).join('/'),
          line,
          text: origLine.trim(),
        })
      }
    }

    if (hits.length > 0) {
      const detail = hits
        .map((h) => `  ${h.file}:${h.line}\n    ${h.text}`)
        .join('\n')
      throw new Error(
        `Hardcoded /api/ path(s) found — every fetch/EventSource argument ` +
          `must derive from \`API_BASE\` (or the \`apiUrl()\` helper) in ` +
          `\`@/shared/config/urls\`. See issue #494 for the why.\n\n` +
          detail,
      )
    }
    expect(hits).toHaveLength(0)
  })
})
