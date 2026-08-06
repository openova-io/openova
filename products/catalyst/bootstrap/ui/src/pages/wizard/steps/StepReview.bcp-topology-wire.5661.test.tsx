/**
 * StepReview.bcp-topology-wire.5661.test.tsx — the wire contract for the
 * operator's TOPOLOGY choice.
 *
 * ── What this guards (issue #5661) ──────────────────────────────────
 * StepTopology offers a SOLO ("Single region") card. The catalyst-api's
 * #4706 admission floor (handler/deployments.go CreateDeployment) rejects
 * any POST body whose `bcpTopology` is empty AND whose `regions` array
 * has fewer than 2 entries — HTTP 400, BEFORE Request.Validate() ever
 * runs. So a wizard that renders the SOLO card but omits `bcpTopology`
 * from the body can NEVER fire a single-region prov: the operator's
 * choice is structurally unreachable. That was #5661; PR #5662 fixed it
 * with 12 lines in StepReview and NO test.
 *
 * ── Why the assertion is on the WIRE, not on the render ─────────────
 * "The dropdown shows Single region" is not the claim under test — the
 * pre-fix tree rendered that card perfectly and still could not fire.
 * The claim is "a single-region choice produces a body the #4706 floor
 * ACCEPTS". So every assertion below reads the actual `fetch` request
 * body that `postDeployment()` sends, and evaluates it through
 * `admittedByBcpFloor()` — a literal transcription of the Go floor
 * condition at handler/deployments.go:
 *
 *     strings.TrimSpace(req.BcpTopology) == "" && len(req.Regions) < 2
 *         → HTTP 400
 *
 * plus `rejectedByValidate()`, the mirror invariant from
 * provisioner.Validate() that rejects an explicit single-region
 * declaration carrying >=2 regions. A body must clear BOTH to fire.
 *
 * ── Controls (so "accept everything" cannot pass) ───────────────────
 *   • DUAL (2 regions) must still send NO bcpTopology and still be
 *     admitted — the server's multi-region default semantics unchanged.
 *   • A hand-built contradictory body (single-region + 2 regions) must
 *     be REJECTED by the Validate mirror — proving the oracle can say no.
 *   • The floor oracle itself is exercised on a synthetic pre-fix body
 *     (1 region, no bcpTopology) and must report 400 — proving the oracle
 *     can go red at all.
 *
 * Refs #5661 #5662 #4706 #960
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { StepReview } from './StepReview'
import { useWizardStore } from '@/entities/deployment/store'
import { useWizardNav } from '@/shared/lib/wizardNav'
import {
  INITIAL_WIZARD_STATE,
  TOPOLOGY_REGION_COUNT,
  TOPOLOGY_REGION_LABELS,
} from '@/entities/deployment/model'
import type { TopologyTemplate } from '@/entities/deployment/model'

/* ── Test doubles for StepReview's non-wire dependencies ──────────── */

const navigateSpy = vi.fn()

vi.mock('@tanstack/react-router', () => ({
  useRouter: () => ({ navigate: navigateSpy }),
}))

vi.mock('@/shared/lib/useSession', () => ({
  useSession: () => ({
    signedIn: true,
    email: 'operator@example.test',
    sub: 'sub-1',
    tier: 'owner',
    roles: [],
    loading: false,
    refetch: vi.fn(),
    signOut: vi.fn(),
  }),
}))

vi.mock('@/widgets/auth/PinSignInModal', () => ({
  PinSignInModal: () => null,
}))

/* ── The server-side contract, transcribed ────────────────────────── */

interface WireBody {
  bcpTopology?: string
  regions?: unknown[]
}

/**
 * #4706 admission floor, verbatim from
 * products/catalyst/bootstrap/api/internal/handler/deployments.go:
 *
 *   if strings.TrimSpace(req.BcpTopology) == "" && len(req.Regions) < 2 {
 *       http.Error(w, "...", http.StatusBadRequest); return
 *   }
 */
function admittedByBcpFloor(body: WireBody): boolean {
  const declared = (body.bcpTopology ?? '').trim()
  const regionCount = body.regions?.length ?? 0
  return !(declared === '' && regionCount < 2)
}

/**
 * The mirror invariant from provisioner.Validate(): an EXPLICIT
 * single-region declaration carrying >=2 regions is contradictory and is
 * rejected. Returns a reason string when the body would be rejected.
 */
function rejectedByValidate(body: WireBody): string | null {
  const declared = (body.bcpTopology ?? '').trim().toLowerCase()
  const regionCount = body.regions?.length ?? 0
  if (declared !== '' && !['single-region', 'active-hotstandby', 'active-active'].includes(declared)) {
    return `bcpTopology ${declared} is not one of the three valid values`
  }
  if (declared === 'single-region' && regionCount >= 2) {
    return `bcpTopology=single-region contradicts len(regions)=${regionCount}`
  }
  if ((declared === 'active-hotstandby' || declared === 'active-active') && regionCount < 2) {
    return `bcpTopology=${declared} requires len(regions)>=2 (got ${regionCount})`
  }
  return null
}

/* ── Harness ──────────────────────────────────────────────────────── */

const FQDN_POOL = 'omani-works'
const FQDN_SUB = 'sovtest'

/** Seed the wizard store as if the operator completed every prior step. */
function seedStore(topology: TopologyTemplate, airgap = false) {
  const regionCount = TOPOLOGY_REGION_LABELS[topology].length + (airgap ? 1 : 0)
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    orgName: 'Acme Sovereign',
    orgEmail: 'operator@example.test',
    topology,
    airgap,
    sovereignDomainMode: 'pool',
    sovereignPoolDomain: FQDN_POOL,
    sovereignSubdomain: FQDN_SUB,
    regionProviders: Array.from({ length: regionCount }, () => 'hetzner'),
    regionCloudRegions: Array.from({ length: regionCount }, () => 'fsn1'),
    regionControlPlaneSizes: Array.from({ length: regionCount }, () => 'cpx41'),
    regionWorkerSizes: Array.from({ length: regionCount }, () => 'cpx41'),
    regionWorkerCounts: Array.from({ length: regionCount }, () => 2),
    hetznerToken: 'x'.repeat(64),
    hetznerProjectId: 'proj_abc',
    sshPublicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBdkRf2yAJ7E7g1zFJKj7xZl9Q3WkF0K3ZQp5Y7qXmHZ op@example.test',
  })
}

/**
 * Render StepReview, fire the Launch action, and return the parsed POST
 * body that reached `fetch`. StepShell publishes the step's onNext into
 * the wizardNav store (the persistent WizardLayout footer is what renders
 * the Launch button), so invoking `useWizardNav.getState().onNext()` runs
 * the SAME `onLaunch` → `postDeployment` path a real click runs.
 */
async function launchAndCaptureBody(): Promise<WireBody> {
  const fetchSpy = vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({ id: 'dep-test-0001' }),
  })
  vi.stubGlobal('fetch', fetchSpy)

  render(<StepReview />)

  await waitFor(() => {
    expect(useWizardNav.getState().nav.onNext).toBeTypeOf('function')
  })
  await (useWizardNav.getState().nav.onNext!() as unknown as Promise<void>)

  await waitFor(() => expect(fetchSpy).toHaveBeenCalledTimes(1))

  const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit]
  expect(url).toContain('/v1/deployments')
  expect(init.method).toBe('POST')
  return JSON.parse(init.body as string) as WireBody
}

beforeEach(() => {
  navigateSpy.mockReset()
  useWizardNav.setState({ nav: {} })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

/* ── The oracle can go red (vacuity check) ────────────────────────── */

describe('#5661 — the #4706 floor oracle is not vacuous', () => {
  it('rejects the PRE-FIX body shape: 1 region and no bcpTopology', () => {
    const preFix: WireBody = { regions: [{ provider: 'hetzner' }] }
    expect(admittedByBcpFloor(preFix)).toBe(false)
  })

  it('rejects a contradictory body: single-region declared with 2 regions', () => {
    const contradictory: WireBody = {
      bcpTopology: 'single-region',
      regions: [{ provider: 'hetzner' }, { provider: 'hetzner' }],
    }
    expect(admittedByBcpFloor(contradictory)).toBe(true)
    expect(rejectedByValidate(contradictory)).toMatch(/contradicts len\(regions\)=2/)
  })

  it('rejects an unknown topology token', () => {
    expect(rejectedByValidate({ bcpTopology: 'solo', regions: [{}] })).toMatch(/not one of/)
  })
})

/* ── The claim under test ─────────────────────────────────────────── */

describe('#5661 — the wizard can fire a single-region prov', () => {
  it('SOLO sends bcpTopology="single-region" on the wire with exactly 1 region', async () => {
    seedStore('solo')
    const body = await launchAndCaptureBody()

    expect(body.regions).toHaveLength(1)

    // The floor assertion comes FIRST so the RED message on a regressed
    // tree names the actual operator-visible failure, not a field diff.
    expect(
      admittedByBcpFloor(body),
      `the wizard's single-region body would be REJECTED by the #4706 admission floor ` +
        `with HTTP 400 (bcpTopology=${JSON.stringify(body.bcpTopology)}, ` +
        `regions=${body.regions?.length}) — the SOLO topology card cannot fire a prov`,
    ).toBe(true)

    // The VALUE, not the presence of the card.
    expect(body.bcpTopology).toBe('single-region')
    expect(rejectedByValidate(body)).toBeNull()
  })

  it('renders the declared topology in the Review card (body-mirror contract)', async () => {
    seedStore('solo')
    render(<StepReview />)
    const topologySection = await screen.findByTestId('review-section-topology')
    expect(topologySection.textContent).toContain('BCP topology')
    expect(topologySection.textContent).toContain('single-region')
  })

  it('the SOLO body would have been REJECTED without the declaration', async () => {
    seedStore('solo')
    const body = await launchAndCaptureBody()

    // Strip the field the fix threads: the same body is a 400. This is
    // what makes the assertion above load-bearing rather than decorative.
    const stripped: WireBody = { ...body, bcpTopology: undefined }
    expect(admittedByBcpFloor(stripped)).toBe(false)
  })
})

/* ── Controls: multi-region must be unchanged ─────────────────────── */

describe('#5661 controls — multi-region semantics are byte-identical', () => {
  it.each<[TopologyTemplate, number]>([
    ['dual', 2],
    ['zoned', 2],
    ['compact', 2],
    ['citadel', 4],
  ])('%s sends NO bcpTopology and %i regions, and is admitted', async (topology, want) => {
    expect(TOPOLOGY_REGION_COUNT[topology]).toBe(want)
    seedStore(topology)
    const body = await launchAndCaptureBody()

    expect(body.regions).toHaveLength(want)
    // undefined is dropped by JSON.stringify — the key must be ABSENT,
    // not present-and-empty (present-and-empty would still trip the
    // floor's TrimSpace()=="" branch on a hypothetical 1-region body,
    // and would change the server's derive-vs-explicit path).
    expect('bcpTopology' in body).toBe(false)

    expect(admittedByBcpFloor(body)).toBe(true)
    expect(rejectedByValidate(body)).toBeNull()
  })
})

/* ── The AIR-GAP interaction (documented, deliberately unfixed) ────── */

describe('#5661 — AIR-GAP defeats the SOLO declaration', () => {
  it('SOLO + AIR-GAP emits 2 regions and NO declaration — server derives active-hotstandby', async () => {
    seedStore('solo', true)
    const body = await launchAndCaptureBody()

    // The air-gap add-on appends a region row, so regionsPayload.length
    // is 2 and StepReview's `< 2` derivation leaves bcpTopology unset.
    // The body is still ADMITTED (2 regions clears the floor), but the
    // effective topology the server derives is "active-hotstandby" — a
    // synchronous zero-tx-loss standby — for an operator who picked the
    // "Single region" card plus a pull-only forensic region.
    //
    // Asserted as the CURRENT behaviour, not endorsed: whether an
    // air-gap region should count as a BCP standby is a product
    // decision, tracked on #5661. If that decision lands, this
    // expectation is the one to flip.
    expect(body.regions).toHaveLength(2)
    expect('bcpTopology' in body).toBe(false)
    expect(admittedByBcpFloor(body)).toBe(true)
  })
})
