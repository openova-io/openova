/**
 * org.api.test.ts — regression guard for UAT row 214 (issue #5100).
 *
 * `createOrganization` / `listOrganizationRecords` surface their failure
 * as an `Error.message` that is rendered VERBATIM in the create form's
 * submit-error panel. This file locks that copy so a future edit can't
 * silently reintroduce the banned org-rename term (docs/GLOSSARY.md:
 * "tenant" → Organization).
 *
 * Supersedes the guard added on PR #5203 (which targeted the pre-rename
 * `createOrgTenant` / `listOrgTenants` symbols — both renamed by the
 * #5247 code-token purge). It does NOT touch the wire contract: the
 * request path (`/v1/organizations`), the org-scope host header, and
 * the legacy `tenant_*` JSON field names on the wire are intentionally
 * untouched — only the human-facing Error.message text is asserted.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createOrganization, listOrganizationRecords } from './org.api'

const CREATE_REQ = {
  subdomain: 'acme',
  admin_email: 'admin@acme.com',
  company_name: 'Acme Corp',
  domain_mode: 'free-subdomain' as const,
  parent_domain: 'omani.works',
  kind: 'customer' as const,
  billing_mode: 'real' as const,
  isolation: 'vcluster' as const,
}

beforeEach(() => {
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('org.api — create/list organization error copy (row 214)', () => {
  it('createOrganization failure message reads "create organization:" with no banned residue', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response('boom', { status: 500 }),
    )
    await expect(createOrganization(CREATE_REQ)).rejects.toThrow(
      /^create organization: HTTP 500/,
    )
  })

  it('listOrganizationRecords failure message reads "list organizations:" with no banned residue', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response('boom', { status: 503 }),
    )
    await expect(listOrganizationRecords()).rejects.toThrow(
      /^list organizations: HTTP 503/,
    )
  })

  it('neither error message contains the banned org-rename term (docs/GLOSSARY.md)', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce(new Response('boom', { status: 500 }))
      .mockResolvedValueOnce(new Response('boom', { status: 500 }))

    const createErr = (await createOrganization(CREATE_REQ).catch((e) => e)) as Error
    const listErr = (await listOrganizationRecords().catch((e) => e)) as Error

    expect(createErr.message.toLowerCase()).not.toContain('tenant')
    expect(listErr.message.toLowerCase()).not.toContain('tenant')
  })
})
