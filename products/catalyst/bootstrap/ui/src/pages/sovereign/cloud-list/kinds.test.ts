/**
 * kinds.test.ts — regression lock for the #3978 restore of the rich
 * per-kind Cloud list. #3970 (commit 6c1144288) flattened /cloud?view=list
 * into a single NAME·KIND·FAMILY·STATUS table (CloudUnifiedList), dropping
 * every kind's own attribute columns + the drill-in. These tests assert:
 *
 *   1. The per-kind catalogue (KINDS) is the source of truth again — every
 *      reconciler kind the founder named is present with its OWN page.
 *   2. KIND_TO_REGISTRY maps each reconciler chip to the exact k8scache
 *      registry Name the backend emits (so chip counts + the K8sListPage
 *      kind-filter resolve to live data).
 *   3. The reconcile-status helpers map onto the Reconciliation vocabulary
 *      (Reconciled / Reconciling / Degraded) — NEVER Success/Failed.
 */

import { describe, expect, it } from 'vitest'
import { KINDS, KIND_IDS, KIND_TO_REGISTRY, isValidKind } from './kinds'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  colReconcileStatus,
  colCnpgStatus,
  colCatalystCrStatus,
} from './K8sListPage'

/** The reconciler chip ids the founder required (#3978). */
const RECONCILER_CHIP_IDS = [
  'helmreleases',
  'kustomizations',
  'gitrepositories',
  'certificates',
  'externalsecrets',
  'cnpg-clusters',
  'cnpg-databases',
  'applications',
  'environments',
  'organizations',
  'continuums',
  'useraccesses',
] as const

/** chip id → the exact k8scache registry Name (api/internal/k8scache/kinds.go). */
const EXPECTED_REGISTRY_NAME: Record<string, string> = {
  helmreleases: 'helmrelease',
  kustomizations: 'kustomization',
  gitrepositories: 'gitrepository',
  certificates: 'certificate',
  externalsecrets: 'externalsecret',
  'cnpg-clusters': 'cnpgcluster',
  'cnpg-databases': 'cnpgdatabase',
  applications: 'application',
  environments: 'environment',
  organizations: 'organization',
  continuums: 'continuum',
  useraccesses: 'useraccess',
}

describe('cloud-list kinds catalogue (#3978 restore)', () => {
  it('registers every reconciler kind with its own page Component', () => {
    for (const id of RECONCILER_CHIP_IDS) {
      expect(isValidKind(id), `${id} should be a valid kind`).toBe(true)
      const entry = KINDS.find((k) => k.id === id)
      expect(entry, `${id} missing from KINDS`).toBeTruthy()
      // Each kind has its OWN page component — NOT a shared flat table.
      expect(typeof entry!.Component).toBe('function')
      expect(entry!.category).toBe('reconciler')
    }
  })

  it('maps each reconciler chip to the exact k8scache registry Name', () => {
    for (const id of RECONCILER_CHIP_IDS) {
      expect(
        KIND_TO_REGISTRY[id],
        `${id} must map to registry name ${EXPECTED_REGISTRY_NAME[id]}`,
      ).toBe(EXPECTED_REGISTRY_NAME[id])
    }
  })

  it('keeps the original rich kinds (clusters/pods/helmreleases) — not a flat table', () => {
    // A sampling of the pre-#3970 kinds that must still exist.
    for (const id of ['clusters', 'vclusters', 'pods', 'services', 'pvcs']) {
      expect(KIND_IDS).toContain(id)
    }
    // The flat unified list is gone: there is no single 'all' / 'unified' kind.
    expect(KIND_IDS).not.toContain('all')
    expect(KIND_IDS).not.toContain('unified')
  })
})

describe('reconcile-status helpers use the Reconciliation vocabulary', () => {
  const withReadyCondition = (status: string, reason = ''): K8sObject => ({
    apiVersion: 'helm.toolkit.fluxcd.io/v2',
    kind: 'HelmRelease',
    metadata: { name: 'x', namespace: 'flux-system' },
    status: { conditions: [{ type: 'Ready', status, reason }] },
  })

  it('colReconcileStatus: Ready=True → Reconciled', () => {
    expect(colReconcileStatus().extract(withReadyCondition('True'))).toBe(
      'Reconciled',
    )
  })

  it('colReconcileStatus: Ready=False + stalled → Degraded', () => {
    expect(
      colReconcileStatus().extract(
        withReadyCondition('False', 'RetriesExhausted'),
      ),
    ).toBe('Degraded')
  })

  it('colReconcileStatus: Ready=False (retrying) → Reconciling, never Failed', () => {
    const v = colReconcileStatus().extract(
      withReadyCondition('False', 'UpgradeFailed'),
    )
    expect(v).toBe('Reconciling')
    expect(v).not.toBe('Failed')
  })

  it('colReconcileStatus: no condition yet → Reconciling', () => {
    const empty: K8sObject = { metadata: { name: 'x' }, status: {} }
    expect(colReconcileStatus().extract(empty)).toBe('Reconciling')
  })

  it('colCnpgStatus: healthy phase → Reconciled', () => {
    const o: K8sObject = {
      metadata: { name: 'db' },
      status: { phase: 'Cluster in healthy state' },
    }
    expect(colCnpgStatus().extract(o)).toBe('Reconciled')
  })

  it('colCnpgStatus: failure phase → Degraded', () => {
    const o: K8sObject = {
      metadata: { name: 'db' },
      status: { phase: 'Failing over' },
    }
    expect(colCnpgStatus().extract(o)).toBe('Degraded')
  })

  it('colCatalystCrStatus: phase Ready → Reconciled', () => {
    const o: K8sObject = {
      metadata: { name: 'app' },
      status: { phase: 'Ready' },
    }
    expect(colCatalystCrStatus().extract(o)).toBe('Reconciled')
  })
})
