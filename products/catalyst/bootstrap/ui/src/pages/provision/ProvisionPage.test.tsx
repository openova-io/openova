/**
 * ProvisionPage.test.tsx — smoke test for the re-export contract.
 *
 * The DAG-era ProvisionPage tests were moved to
 * `src/pages/sovereign/AdminPage.test.tsx` along with the rendering work
 * itself. The file at `pages/provision/ProvisionPage.tsx` now exists
 * only as a re-export of AdminPage (the wizard's StepReview redirects
 * to `/sovereign/provision/$deploymentId`, and that route module
 * imports `ProvisionPage` from this file). This test asserts the
 * re-export wiring is correct so the route never resolves to undefined.
 */

import { describe, it, expect } from 'vitest'
import { ProvisionPage } from './ProvisionPage'
import { AdminPage } from '@/pages/sovereign/AdminPage'

describe('ProvisionPage re-export', () => {
  it('exports the AdminPage component (legacy DAG view abandoned)', () => {
    expect(ProvisionPage).toBe(AdminPage)
  })
})
