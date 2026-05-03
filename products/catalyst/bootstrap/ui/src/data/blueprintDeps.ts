/**
 * blueprintDeps.ts — single-source-of-truth wrapper around the
 * generated `blueprint-deps.generated.json` that scripts/generate-
 * blueprint-deps.sh emits from clusters/_template/bootstrap-kit/*.yaml.
 *
 * The JSON is THE map between component IDs and their install
 * dependencies, derived from Flux HelmRelease `dependsOn`. Any
 * componentGroups.ts component WITHOUT explicit deps inherits its
 * dependencies from this file at module load.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the wizard
 * MUST NOT carry hand-maintained dependency arrays — Flux is canonical.
 *
 * Drift detection: a CI test runs the generator and diffs against the
 * committed JSON; any drift fails the build with a pointer to re-run
 * `scripts/generate-blueprint-deps.sh`.
 */
import depsJson from './blueprint-deps.generated.json'

export type BlueprintDeps = Record<string, string[]>

export const BLUEPRINT_DEPS: BlueprintDeps = depsJson as BlueprintDeps

/**
 * Resolve the Flux-canonical install dependencies for a component id
 * (without the bp- prefix). Returns an empty array when the id is not
 * a tracked Blueprint (matches Flux behaviour for an HR with no
 * `dependsOn` field).
 */
export function depsFor(componentId: string): string[] {
  return BLUEPRINT_DEPS[componentId] ?? []
}
