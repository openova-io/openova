/**
 * e2e/gate/checks.ts — which gate checks the browser harness runs (FND-161).
 *
 * The harness reads the JSON export of the gate manifest
 * (`python3 scripts/acceptance-gate.py export`). A check is one of its
 * projects when its implementation is a folder under one of the two harness
 * roots, its state is expected-fail or required (a not-built check is never
 * recorded), and it runs in the run's place (`runs_in`). The project is named
 * by the check id and runs every `*.spec.ts` of that folder. A new project is
 * a new folder and a manifest entry; nothing here changes for it.
 *
 * The gate workflow lists the checks with this same selector:
 *
 *   node --experimental-strip-types e2e/gate/checks.ts <manifest.json> <run_in>
 *
 * prints one check id per line (none: no line) and exits 2 when the manifest
 * cannot be read.
 */

import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

/** The folders of the browser harness, as rule 3 of scripts/acceptance-gate.py names them. */
export const BROWSER_ROOTS = ['products/catalyst/bootstrap/ui/e2e/gate/', 'core/marketplace/e2e/gate/']

export interface ManifestCheck {
  id: string
  state: string
  runs_in?: string[]
  implementation?: string
}

export interface BrowserCheck {
  id: string
  /** The folder of specifications, from the repository root, without a trailing slash. */
  folder: string
}

export function readManifest(file: string): ManifestCheck[] {
  const parsed: unknown = JSON.parse(readFileSync(file, 'utf8'))
  const checks = (parsed as { checks?: unknown }).checks
  if (!Array.isArray(checks)) throw new Error(`${file} has no list "checks"`)
  return checks as ManifestCheck[]
}

/** The folder of a check when its implementation is a folder of the harness, else null. */
export function harnessFolder(implementation: string | undefined): string | null {
  if (!implementation) return null
  const folder = implementation.replace(/\/+$/, '')
  if (folder.split('/').includes('..')) return null
  return BROWSER_ROOTS.some((root) => (folder + '/').startsWith(root) && folder + '/' !== root) ? folder : null
}

/** The checks the harness runs in the place runIn, in the manifest's order. */
export function browserChecks(manifest: ManifestCheck[], runIn: string): BrowserCheck[] {
  const out: BrowserCheck[] = []
  for (const c of manifest) {
    const folder = harnessFolder(c.implementation)
    if (folder === null) continue
    if (c.state !== 'expected-fail' && c.state !== 'required') continue
    if (!(c.runs_in ?? []).includes(runIn)) continue
    out.push({ id: c.id, folder })
  }
  return out
}

/**
 * The checks of one run: all of browserChecks, or the ones named (the
 * command line of e2e/gate/run.sh). A name that is not one of them stops the
 * run: a check that cannot run is never silently left out.
 */
export function selectChecks(all: BrowserCheck[], named: string[]): BrowserCheck[] {
  if (named.length === 0) return all
  const byId = new Map(all.map((c) => [c.id, c]))
  return named.map((id) => {
    const c = byId.get(id)
    if (!c) throw new Error(`${id} is not a check the harness runs (a folder of the harness, expected-fail or required, in this place)`)
    return c
  })
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [file, runIn] = process.argv.slice(2)
  if (!file || !runIn) {
    console.error('usage: node --experimental-strip-types e2e/gate/checks.ts <manifest.json> <run_in>')
    process.exit(2)
  }
  try {
    for (const c of browserChecks(readManifest(file), runIn)) console.log(c.id)
  } catch (e) {
    console.error(`checks.ts: ${(e as Error).message}`)
    process.exit(2)
  }
}
