/**
 * #5555 — every wizard step must offer a Back control.
 *
 * StepDomain rendered <StepShell> with `onNext` but no `onBack`, so step 6 of 8
 * showed a footer containing only a disabled `Continue →`. Six sibling steps all
 * passed `onBack={back}`. Found on a live walk of
 * console.openova.io/sovereign/wizard (2026-08-01).
 *
 * Nothing caught it because `_shared.tsx` types the prop as `onBack?:` —
 * optional — so omitting it is neither a type error nor a lint error. It is
 * invisible until someone reaches that step in a browser and looks for a way
 * back. It landed on the one step where `Continue` is disabled by default
 * (the domain fields start empty), so the primary action was dead while the
 * reverse action was absent.
 *
 * This test reads the step sources directly rather than rendering them: the
 * defect is a missing PROP AT THE CALL SITE, and a render test would need each
 * step's full store/provider scaffolding to reach the same assertion. Source
 * inspection catches it at the exact layer it occurs.
 *
 * If a step should genuinely have no Back control, add it to
 * INTENTIONALLY_NO_BACK with a reason — making the absence deliberate and
 * reviewable instead of an easy omission.
 */

import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const STEPS_DIR = join(__dirname)

/** Steps that deliberately render no Back control. Each needs a stated reason. */
const INTENTIONALLY_NO_BACK: Record<string, string> = {
  // StepSuccess is terminal — the deployment is already firing, so there is
  // nothing to go back to.
  'StepSuccess.tsx': 'terminal step; the deployment has already been submitted',

  // StepOrg is step 1 of 8. Nothing precedes it, so a Back control would have
  // no destination. Confirmed by the live walk: step 1's footer carries only
  // `Continue →`, and the header progress chips are the way out.
  'StepOrg.tsx': 'first step of the flow; no preceding step to return to',

  // StepNSDelegation is a POST-HANDOVER surface, not one of the 8 wizard
  // steps — it takes `onNext` as a prop rather than from useStepNav(), and is
  // referenced only by its own test, never mounted in the main flow. Its
  // navigation is owned by whatever embeds it.
  'StepNSDelegation.tsx':
    'post-handover surface, not part of the 8-step flow; nav owned by its embedder',
}

function stepFiles(): string[] {
  return readdirSync(STEPS_DIR)
    .filter(f => /^Step[A-Z].*\.tsx$/.test(f))
    .filter(f => !f.includes('.test.'))
    .sort()
}

describe('#5555 wizard step nav contract', () => {
  it('finds the wizard step components (guard is not vacuous)', () => {
    const files = stepFiles()
    // If this ever drops to a handful the glob has drifted and every assertion
    // below would pass trivially against an empty set.
    expect(files.length).toBeGreaterThanOrEqual(6)
    expect(files).toContain('StepDomain.tsx')
  })

  it.each(stepFiles())('%s renders a Back control', file => {
    const src = readFileSync(join(STEPS_DIR, file), 'utf8')

    // Only steps that actually mount a StepShell are in scope.
    if (!src.includes('<StepShell')) return

    if (file in INTENTIONALLY_NO_BACK) {
      expect(src).not.toMatch(/onBack=/)
      return
    }

    expect(
      src,
      `${file} mounts <StepShell> without passing onBack. That renders a footer ` +
        `with no ← Back control, which is the #5555 defect. StepShell types onBack ` +
        `as optional, so nothing else will flag this. Pass onBack={back} from ` +
        `useStepNav(), or add the file to INTENTIONALLY_NO_BACK with a reason.`,
    ).toMatch(/onBack=\{/)
  })

  it('StepDomain specifically destructures back from useStepNav (the #5555 regression point)', () => {
    const src = readFileSync(join(STEPS_DIR, 'StepDomain.tsx'), 'utf8')
    expect(src).toMatch(/const\s*\{[^}]*\bback\b[^}]*\}\s*=\s*useStepNav\(\)/)
    expect(src).toMatch(/onBack=\{back\}/)
  })
})
