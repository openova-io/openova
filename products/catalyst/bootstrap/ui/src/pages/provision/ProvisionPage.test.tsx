/**
 * ProvisionPage.test.tsx — smoke test for the re-export contract.
 *
 * The DAG-era ProvisionPage tests were moved to
 * `src/pages/sovereign/AppsPage.test.tsx` along with the rendering work
 * itself. The file at `pages/provision/ProvisionPage.tsx` now exists
 * only as a re-export of AppsPage (the wizard's StepReview redirects
 * to `/sovereign/provision/$deploymentId`, and that route module
 * imports `ProvisionPage` from this file). This test asserts the
 * re-export wiring is correct so the route never resolves to undefined.
 */

import { describe, it, expect } from 'vitest'
import { ProvisionPage } from './ProvisionPage'
import { AppsPage } from '@/pages/sovereign/AppsPage'

describe('ProvisionPage re-export', () => {
  it('exports the AppsPage component (legacy DAG view abandoned)', () => {
    expect(ProvisionPage).toBe(AppsPage)
  })
})
