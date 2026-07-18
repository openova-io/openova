/**
 * org.api.test.ts — regression guard for UAT row 214 (issue #5100).
 *
 * PR #5102 purged the two rendered "tenant" strings that leaked out of
 * `createOrgTenant` / `listOrgTenants` error messages (`create org
 * tenant: …` / `list org tenants: …` → `create organization: …` /
 * `list organizations: …`). These messages are rendered verbatim in
 * `CreateTenantPage`'s submit-error panel (`err.message`), so a
 * regression here is a user-visible tenant-string leak, not just an
 * internal rename.
 *
 * This file locks the wording so it can't silently regress. It does
 * NOT touch the wire contract: the request path (`/v1/organizations`),
 * the `X-Tenant-Host` header, or any `tenant*` JSON field name are
 * untouched by design — only the human-facing Error.message text.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createOrgTenant, listOrgTenants } from './org.api'

beforeEach(() => {
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('org.api — create/list organization error copy (row 214)', () => {
  it('createOrgTenant failure message reads "create organization:" with no tenant residue', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response('boom', { status: 500 }),
    )
    await expect(
      createOrgTenant({
        subdomain: 'acme',
        admin_email: 'admin@acme.com',
        company_name: 'Acme Corp',
        domain_mode: 'free-subdomain',
        parent_domain: 'omani.works',
        kind: 'customer',
        billing_mode: 'real',
        isolation: 'vcluster',
      }),
    ).rejects.toThrow(/^create organization: HTTP 500/)
  })

  it('listOrgTenants failure message reads "list organizations:" with no tenant residue', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response('boom', { status: 503 }),
    )
    await expect(listOrgTenants()).rejects.toThrow(/^list organizations: HTTP 503/)
  })

  it('neither error message contains the banned "tenant" term (docs/GLOSSARY.md)', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce(new Response('boom', { status: 500 }))
      .mockResolvedValueOnce(new Response('boom', { status: 500 }))

    const createErr = await createOrgTenant({
      subdomain: 'acme',
      admin_email: 'admin@acme.com',
      company_name: 'Acme Corp',
      domain_mode: 'free-subdomain',
      parent_domain: 'omani.works',
      kind: 'customer',
      billing_mode: 'real',
      isolation: 'vcluster',
    }).catch((e) => e as Error)
    const listErr = await listOrgTenants().catch((e) => e as Error)

    expect((createErr as Error).message.toLowerCase()).not.toContain('tenant')
    expect((listErr as Error).message.toLowerCase()).not.toContain('tenant')
  })
})
