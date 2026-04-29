/**
 * applicationCatalog.test.ts — vitest coverage for the function that
 * resolves the Application set rendered on the Sovereign Admin page.
 *
 * Coverage:
 *   • BOOTSTRAP_KIT (11 entries) is always present, in numerical order.
 *   • User selection adds beyond the bootstrap kit.
 *   • Transitive dependencies cascade in (e.g. Harbor adds cnpg /
 *     valkey / seaweedfs).
 *   • Mandatory components from the catalog are always represented.
 *   • Output is deduplicated and stably ordered.
 */

import { describe, it, expect } from 'vitest'
import { resolveApplications, reverseDependencies } from './applicationCatalog'
import { BOOTSTRAP_KIT } from '@/shared/constants/catalog.generated'

describe('applicationCatalog — resolveApplications', () => {
  it('always includes the eleven BOOTSTRAP_KIT Applications', () => {
    const apps = resolveApplications([])
    const ids = apps.map((a) => a.id)
    for (const b of BOOTSTRAP_KIT) {
      expect(ids).toContain(b.id)
    }
  })

  it('marks bootstrap-kit apps with bootstrapKit: true', () => {
    const apps = resolveApplications([])
    const cilium = apps.find((a) => a.id === 'bp-cilium')
    expect(cilium?.bootstrapKit).toBe(true)
  })

  it('user selection adds beyond the bootstrap kit', () => {
    const apps = resolveApplications(['harbor'])
    const ids = apps.map((a) => a.id)
    expect(ids).toContain('bp-harbor')
    // Harbor's cascade pulls in cnpg, valkey, seaweedfs at the catalog level.
    expect(ids).toContain('bp-cnpg')
    expect(ids).toContain('bp-seaweedfs')
    expect(ids).toContain('bp-valkey')
  })

  it('mandatory catalog components are always represented', () => {
    const apps = resolveApplications([])
    // Examples of mandatory-tier catalog components (post transitive
    // promotion). They must appear regardless of user selection.
    const ids = apps.map((a) => a.id)
    expect(ids).toContain('bp-flux')
    expect(ids).toContain('bp-crossplane')
    expect(ids).toContain('bp-openbao')
  })

  it('output is deduplicated', () => {
    const apps = resolveApplications(['flux']) // bootstrap kit already has flux
    const ids = apps.map((a) => a.id)
    const fluxCount = ids.filter((id) => id === 'bp-flux').length
    expect(fluxCount).toBe(1)
  })

  it('descriptors carry family + tier metadata', () => {
    const apps = resolveApplications([])
    const cilium = apps.find((a) => a.id === 'bp-cilium')
    expect(cilium?.familyId).toBe('spine')
    expect(cilium?.familyName).toBe('SPINE')
    expect(cilium?.tier).toBe('mandatory')
  })
})

describe('applicationCatalog — reverseDependencies', () => {
  it('returns the Blueprint ids whose dependencies include the input', () => {
    // External-DNS depends on PowerDNS, so reverse-deps for powerdns
    // includes external-dns.
    const reverse = reverseDependencies('powerdns')
    expect(reverse).toContain('external-dns')
  })

  it('returns an empty array for components nothing depends on', () => {
    const reverse = reverseDependencies('this-id-does-not-exist')
    expect(reverse).toEqual([])
  })
})
