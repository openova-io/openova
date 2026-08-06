/**
 * useK8sCacheStream.region-split-5571.test.ts — #5571 cross-region
 * collapse guard.
 *
 * ## What this pins
 *
 * `/k8s/stream` fans out across every region of a Sovereign and tags
 * each SSE frame with the region it came from (`cluster` on the event
 * ENVELOPE — see catalyst-api `internal/handler/k8s.go`, and the
 * server-side fan-out fix in PR #5687). The SPA then folds those
 * frames into one snapshot Map.
 *
 * The fold is where the estate can still be silently truncated: if the
 * snapshot key omits the region, the SAME `{ns}/{name}` in two regions
 * collapses to ONE entry. A 2-region Sovereign runs the same charts in
 * both regions, so that collision is the common case, not an edge —
 * measured live on hw292 (dep 1c56518035a83e03, 2026-08-06):
 *
 *   NetworkPolicies        region-a 38 + region-b 26 = 64 real objects
 *                          -> 21 share {ns}/{name} across regions
 *   CiliumNetworkPolicies  region-a 57 + region-b 42 = 99 real objects
 *                          -> 37 share {ns}/{name} across regions
 *
 * So a region-blind key drops 21 of 64 NetworkPolicies and 37 of 99
 * CiliumNetworkPolicies on the Cloud security-posture pages, and — the
 * sharper failure — a DELETE in one region erases the row for a policy
 * that is still enforcing in the other. On a page whose job is to show
 * network policy, absence reads as "no such policy". That is the
 * #5571 symptom, one layer above the server fix.
 *
 * ## Why the assertions look the way they do
 *
 * Every assertion below scans snapshot VALUES by `{ns}/{name}` and
 * checks the object CONTENT, never the key string. That is deliberate:
 *   - it does not encode this implementation's key format, so the
 *     guard stays valid for any correct fix, and
 *   - a count or a key-presence check would pass on an EMPTY or
 *     WRONG-REGION object; only comparing the two regions' distinct
 *     spec values proves both objects genuinely survived the fold.
 *
 * The fixture uses two regions with DISTINGUISHABLE objects, because a
 * single-region fixture structurally cannot detect a single-region bug.
 *
 * Object names are the real hw292 ones (`gitea/gitea-default-deny` is
 * live in both regions; the two `cnpg-pair` policies are the real
 * region-exclusive pair — `...allow-probe-to-replica` is region-B-only
 * and is the same discriminator the original issue cited on hw291).
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useK8sCacheStream, type K8sObject, type K8sSnapshot } from './useK8sCacheStream'

/* ── hw292's two real cluster-id conventions ─────────────────────── */

/** Chroot primary self-registers under a `sovereign-<fqdn>` alias. */
const REGION_A = 'sovereign-hw292.omani.works'
/** Secondaries keep `<deploymentID>-<region>` (LoadClustersFromDir). */
const REGION_B = '1c56518035a83e03-me-east-215-b-1'

/* ── EventSource shim (jsdom has none) ───────────────────────────── */

interface FakeES {
  url: string
  onopen: ((e: Event) => void) | null
  onmessage: ((e: MessageEvent) => void) | null
  onerror: ((e: Event) => void) | null
}

let activeES: FakeES | null = null

/** Registered via a helper rather than `activeES = this` so the class
 *  does not alias `this` (@typescript-eslint/no-this-alias). */
function register(es: FakeES) {
  activeES = es
}

class FakeEventSource implements FakeES {
  url: string
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  constructor(url: string) {
    this.url = url
    register(this)
  }
  close = () => {}
}

beforeEach(() => {
  activeES = null
  // @ts-expect-error — install fake on the global
  globalThis.EventSource = FakeEventSource
})

afterEach(() => {
  activeES = null
  // @ts-expect-error — clean up
  delete globalThis.EventSource
})

function send(frame: unknown) {
  if (!activeES?.onmessage) throw new Error('no onmessage handler attached')
  activeES.onmessage(new MessageEvent('message', { data: JSON.stringify(frame) }))
}

/**
 * One `networkpolicy` delta, in the exact envelope shape catalyst-api
 * emits (`{cluster, kind, type, object, at}`). `marker` goes into the
 * object body so the two regions' copies are distinguishable by VALUE.
 */
function npFrame(
  cluster: string,
  ns: string,
  name: string,
  marker: string,
  type: 'ADDED' | 'MODIFIED' | 'DELETED' = 'ADDED',
) {
  return {
    cluster,
    kind: 'networkpolicy',
    type,
    object: {
      apiVersion: 'networking.k8s.io/v1',
      kind: 'NetworkPolicy',
      metadata: { namespace: ns, name, uid: `uid-${cluster}-${ns}-${name}` },
      spec: { podSelector: { matchLabels: { 'test.openova.io/region-marker': marker } } },
    },
    at: '2026-08-06T00:00:00Z',
  }
}

/* ── Format-agnostic readers — scan VALUES, never keys ────────────── */

function entriesFor(snapshot: K8sSnapshot, ns: string, name: string): K8sObject[] {
  return [...snapshot.values()].filter(
    (o) => o.metadata?.namespace === ns && o.metadata?.name === name,
  )
}

function markerOf(obj: K8sObject): string {
  const sel = obj.spec?.['podSelector'] as
    | { matchLabels?: Record<string, string> }
    | undefined
  return sel?.matchLabels?.['test.openova.io/region-marker'] ?? ''
}

describe('#5571 — /k8s/stream fold must not collapse regions', () => {
  it('GUARD: the same {ns}/{name} in two regions survives as TWO distinct objects', async () => {
    const { result } = renderHook(() => useK8sCacheStream('1c56518035a83e03'))
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      // Real hw292 collision: gitea/gitea-default-deny exists in BOTH.
      send(npFrame(REGION_A, 'gitea', 'gitea-default-deny', 'from-region-a'))
      send(npFrame(REGION_B, 'gitea', 'gitea-default-deny', 'from-region-b'))
      // Real hw292 region-exclusive policies.
      send(
        npFrame(
          REGION_A,
          'cnpg',
          'cnpg-pair-bp-cnpg-pair-allow-replication-to-primary',
          'from-region-a',
        ),
      )
      send(
        npFrame(
          REGION_B,
          'cnpg',
          'cnpg-pair-bp-cnpg-pair-allow-probe-to-replica',
          'from-region-b',
        ),
      )
    })

    const snap = result.current.snapshot
    const collided = entriesFor(snap, 'gitea', 'gitea-default-deny')

    // Pre-fix this is 1: region-b's frame overwrote region-a's, because
    // the key carried no region.
    expect(
      collided.length,
      '#5571 REGRESSION: gitea/gitea-default-deny exists in BOTH hw292 regions but the ' +
        `snapshot kept ${collided.length} — one region's NetworkPolicy was silently dropped ` +
        'by the fold, so the Cloud page under-reports the estate.',
    ).toBe(2)

    // Assert on the VALUE, not the count: both regions' OWN bodies must
    // be present. A count of 2 could otherwise be two copies of the
    // same region's object.
    expect(collided.map(markerOf).sort()).toEqual(['from-region-a', 'from-region-b'])

    // …and each must be attributed to the region it actually came from.
    expect(
      collided.map((o) => o.clusterId).sort(),
      '#5571: both copies survived but region attribution was lost — the operator still ' +
        'cannot tell which region enforces which policy.',
    ).toEqual([REGION_B, REGION_A].sort())

    // Whole-estate count: 4 real objects across 2 regions (pre-fix: 3).
    expect(snap.size).toBe(4)
  })

  it('GUARD: DELETE in one region must NOT erase the other region\'s policy', async () => {
    const { result } = renderHook(() => useK8sCacheStream('1c56518035a83e03'))
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send(npFrame(REGION_A, 'gitea', 'gitea-default-deny', 'from-region-a'))
      send(npFrame(REGION_B, 'gitea', 'gitea-default-deny', 'from-region-b'))
    })
    expect(entriesFor(result.current.snapshot, 'gitea', 'gitea-default-deny')).toHaveLength(2)

    // region-b's copy is removed; region-a still enforces its own.
    await act(async () => {
      send(npFrame(REGION_B, 'gitea', 'gitea-default-deny', 'from-region-b', 'DELETED'))
    })

    const left = entriesFor(result.current.snapshot, 'gitea', 'gitea-default-deny')
    expect(
      left.length,
      "#5571 REGRESSION: deleting region-b's NetworkPolicy also removed region-a's — the " +
        'Cloud page now shows NO policy for a namespace that is still protected in region-a. ' +
        'On a security-posture surface, that absence reads as "unprotected".',
    ).toBe(1)
    expect(markerOf(left[0]!), 'the surviving object must be REGION-A’s, not a stale region-b body').toBe(
      'from-region-a',
    )
    expect(left[0]!.clusterId).toBe(REGION_A)
  })

  it('CONTROL: a single-region stream still folds normally (passes pre- AND post-fix)', async () => {
    // This control shares the harness, the fixture helpers and the
    // value-scanning assertions with the guards above, but exercises
    // NO cross-region collision — so it passes on both the pre-fix and
    // post-fix trees. If someone silences the guards by disabling this
    // file, stubbing the hook, or neutering the EventSource shim, this
    // control goes red too, which is what stops blanket suppression
    // from looking like a pass.
    const { result } = renderHook(() => useK8sCacheStream('1c56518035a83e03'))
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send(npFrame(REGION_A, 'gitea', 'gitea-default-deny', 'from-region-a'))
      send(npFrame(REGION_A, 'alloy', 'alloy-default-deny', 'from-region-a'))
    })

    const snap = result.current.snapshot
    expect(snap.size).toBe(2)
    expect(entriesFor(snap, 'gitea', 'gitea-default-deny')).toHaveLength(1)
    expect(entriesFor(snap, 'alloy', 'alloy-default-deny')).toHaveLength(1)
    expect(markerOf(entriesFor(snap, 'alloy', 'alloy-default-deny')[0]!)).toBe('from-region-a')
    expect(result.current.status).toBe('open')
  })
})
