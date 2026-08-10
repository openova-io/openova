/**
 * organizations.api.plan-and-parent-identity.test.ts — UAT rows 7 and 25.
 *
 * ROW 7 — the purchased PLAN was absent end to end. The Organization CR has
 * carried spec.planSlug since #4292 (it is the field the org-controller sizes
 * the ResourceQuota from, and the field the isolation class is DERIVED from),
 * but nothing surfaced it: the API response struct never declared it, OrgRow
 * had no member for it, and the detail page rendered no Plan. The wire key is
 * `plan_slug`; OrgRecord's legacy `plan` member was declared "null until BE
 * wires it" and no producer ever wired it — so reading `plan` alone would keep
 * returning null even after the API started emitting the field.
 *
 * ROW 25 — the parent row's identity was UNSTABLE between renders, resolving
 * to `/organizations/sovereign` on one and `/organizations/<fqdn>` on another.
 *
 * The mechanism is one line: the parent row's slug was `fqdn || 'sovereign'`,
 * and the fqdn comes from getSovereignSelf(), which collapses EVERY failure —
 * network throw, non-2xx (a single 503 is enough), unparseable body — to null.
 * That slug is simultaneously
 *
 *   - the directory's link target (OrganizationsDirectoryPage passes
 *     params={{ org: org.slug }}), and
 *   - the detail page's resolution key (rows.find(o => o.slug === org)).
 *
 * So the identity of a row was a function of whether one optional enrichment
 * call happened to succeed on that particular render. Whenever the render that
 * built the LINK disagreed with the render that RESOLVED it, the detail page
 * fell through to org-detail-not-found — which is also the false
 * "not found" a walker sees when a single 503 lands mid-walk.
 *
 * The fix makes the parent's identity a CONSTANT. The FQDN is display data and
 * still renders as the name and console host when known; it is no longer the
 * key anything joins on. The tests below therefore assert that the slug is
 * IDENTICAL across the resolved and unresolved cases — asserting either
 * literal on its own would pass on the broken code half the time, which is
 * exactly how this survived.
 */

import { describe, expect, it } from 'vitest'
import { parentRowFromSelf, subOrgRowFromRecord } from './organizations.api'
import type { OrgRecord } from './bss.api'

const record: OrgRecord = {
  id: 'tnt-1',
  orgName: 'UAT Co',
  consoleHost: 'console.uatco.omani.homes',
  subdomain: 'uatco',
  parentDomain: 'omani.homes',
  plan: null,
  planSlug: 'm',
  kind: 'customer',
  tier: 'org',
  billingMode: 'real',
  isolation: 'vcluster',
  status: 'active',
  region: 'me-east-215-a',
  ownerEmail: 'owner@uatco.example',
  createdAt: '2026-08-10T00:00:00Z',
  updatedAt: '2026-08-10T00:00:00Z',
  lastError: '',
}

describe('row 7 — the purchased plan reaches the directory row', () => {
  it('carries the plan the Organization actually bought', () => {
    expect(subOrgRowFromRecord(record).plan).toBe('m')
  })

  // Discriminating case: a DIFFERENT plan. A mapping that hardcoded any single
  // literal, or that fell back to a constant default, passes the test above
  // and fails this one.
  it('reports the actual plan, not a fixed default', () => {
    expect(subOrgRowFromRecord({ ...record, planSlug: 'xl' }).plan).toBe('xl')
    expect(subOrgRowFromRecord({ ...record, planSlug: 's' }).plan).toBe('s')
  })

  // Fail-closed: a legacy row with no plan reads as empty, never as an
  // invented tier. Row 7 asserts the CANONICAL value; a fabricated "s" on a
  // record that never declared one would be wrong data, not a safe default.
  it('leaves the plan empty when the record declares none', () => {
    expect(subOrgRowFromRecord({ ...record, planSlug: '' }).plan).toBe('')
  })

  // The sovereign-root row is not a purchased Organization — it has no plan,
  // and must not borrow one.
  it('the parent row declares no plan', () => {
    expect(
      parentRowFromSelf({ deploymentId: 'dep-1', sovereignFQDN: 'hw293.omantel.biz' }).plan,
    ).toBe('')
  })

  // CONTROL — the eight fields row 7 already reported correctly must survive.
  it('does not disturb the fields that already worked', () => {
    const row = subOrgRowFromRecord(record)
    expect(row.slug).toBe('uatco')
    expect(row.kind).toBe('customer')
    expect(row.tier).toBe('org')
    expect(row.billingMode).toBe('real')
    expect(row.isolation).toBe('vcluster')
    expect(row.ownerEmail).toBe('owner@uatco.example')
    expect(row.consoleHost).toBe('console.uatco.omani.homes')
    expect(row.status).toBe('active')
  })
})

describe('row 25 — the parent row has ONE stable identity', () => {
  // THE LOAD-BEARING ASSERTION. Not "the slug equals X" — that was true half
  // the time before the fix. The defect is that the two renders DISAGREE.
  it('resolves to the same slug whether or not self-discovery succeeded', () => {
    const resolved = parentRowFromSelf({
      deploymentId: 'dep-1',
      sovereignFQDN: 'hw293.omantel.biz',
    })
    const unresolved = parentRowFromSelf(null)

    expect(resolved.slug).toBe(unresolved.slug)
  })

  // The link target and the resolution key are the same string, so a row built
  // on one render is findable by a page built on another. This is the actual
  // user-visible contract: the parent row's link must not 404.
  it('a link built from a resolved render is findable in an unresolved one', () => {
    const linkTarget = parentRowFromSelf({
      deploymentId: 'dep-1',
      sovereignFQDN: 'hw293.omantel.biz',
    }).slug

    // The detail page resolves with exactly this predicate.
    const rowsOnALaterRender = [parentRowFromSelf(null), subOrgRowFromRecord(record)]
    expect(rowsOnALaterRender.find((o) => o.slug === linkTarget)).toBeDefined()
  })

  it('and the reverse direction — a link built while self was down still resolves', () => {
    const linkTarget = parentRowFromSelf(null).slug
    const rowsOnALaterRender = [
      parentRowFromSelf({ deploymentId: 'dep-1', sovereignFQDN: 'hw293.omantel.biz' }),
      subOrgRowFromRecord(record),
    ]
    expect(rowsOnALaterRender.find((o) => o.slug === linkTarget)).toBeDefined()
  })

  // The slug must not be a value that can collide with a real Organization's
  // slug, and must be route-safe. A dotted FQDN in a path segment is neither.
  it('the parent slug is a route-safe constant, not an FQDN', () => {
    const slug = parentRowFromSelf({
      deploymentId: 'dep-1',
      sovereignFQDN: 'hw293.omantel.biz',
    }).slug
    expect(slug).not.toContain('.')
  })

  // CONTROL — the FQDN is still surfaced. The fix moves it off the join key;
  // it must not throw the information away, or the operator loses the name of
  // the Sovereign they are looking at.
  it('still shows the FQDN as the display name and console host', () => {
    const row = parentRowFromSelf({
      deploymentId: 'dep-1',
      sovereignFQDN: 'hw293.omantel.biz',
    })
    expect(row.displayName).toBe('hw293.omantel.biz')
    expect(row.consoleHost).toBe('console.hw293.omantel.biz')
    expect(row.isParent).toBe(true)
  })

  // CONTROL — sub-org rows are untouched and still key on their own slug.
  it('sub-org rows keep keying on their own slug', () => {
    expect(subOrgRowFromRecord(record).slug).toBe('uatco')
    expect(subOrgRowFromRecord(record).isParent).toBe(false)
  })
})
