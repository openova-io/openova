/**
 * YamlEditor.reseed5124.test.tsx — #5124 regression.
 *
 * The Edit-IaC editor's committed seed (`seedYaml` = the GET /iac blueprint.yaml,
 * the source of truth) is fetched ASYNC by the opener (CatalogDetail's Edit-IaC
 * button `getCatalogBlueprintIaC(...).then(setCommittedIac)`), so it arrives
 * AFTER this editor has already mounted on the obj-derived seed (the possibly-
 * stale store CatalogItem.raw). `useState(initial)` captured only that first
 * seed; without a re-sync the editor keeps SHOWING and COMMITTING the stale seed
 * even once the committed file loads — the silent card-field loss #5124 describes
 * (found on hw256 / re-observed on hw270 for bp-alloy whose GET /iac lagged).
 *
 * The editor must ADOPT the arriving committed seed while the user hasn't edited.
 * Behavioural proof (robust to the lazy CodeMirror internals): once adopted,
 * yaml === initial === committed ⇒ not dirty ⇒ the Commit button disables.
 * Without the re-sync, yaml stays the stale obj seed, is "dirty" versus the
 * committed initial, and the Commit button stays ENABLED — the exact state that
 * let a Commit overwrite the committed file from the stale seed.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

import { YamlEditor } from './YamlEditor'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

afterEach(cleanup)

// The stale store raw — bp-alloy's pre-edit thin shape: no spec.version, carries
// a multiInstance marker the committed file no longer has.
const staleRaw = {
  apiVersion: 'catalog.openova.io/v1',
  kind: 'Blueprint',
  metadata: { name: 'bp-alloy' },
  spec: { multiInstance: { enabled: true } },
} as unknown as K8sObject

// The committed blueprint.yaml (source of truth) that arrives async.
const committed =
  'apiVersion: catalyst.openova.io/v1\n' +
  'kind: Blueprint\n' +
  'metadata:\n  name: bp-alloy\n' +
  'spec:\n  version: 1.0.2\n  card:\n    summary: RECONCILE-PROOF-5124\n'

function editor(seedYaml?: string) {
  return (
    <YamlEditor
      deploymentId="d1"
      kind="Blueprint"
      ns={undefined}
      name="bp-alloy"
      obj={staleRaw}
      seedYaml={seedYaml}
      onCommit={async () => 'committed'}
      commitLabel="Commit IaC"
    />
  )
}

describe('#5124 YamlEditor adopts an async-arriving committed seed', () => {
  it('re-syncs to a seedYaml that arrives AFTER mount, so a Commit never reverts the committed file', async () => {
    const { rerender } = render(editor(undefined))
    const commit = () => screen.getByTestId('yaml-editor-apply') as HTMLButtonElement

    // Mounted on the obj-derived (stale) seed: it differs from the committed file,
    // so before the seed arrives the editor is legitimately "dirty" (enabled).
    // The committed file then arrives async (the opener's GET /iac resolves):
    rerender(editor(committed))

    // With the #5124 re-sync the editor ADOPTS `committed` ⇒ yaml === initial ⇒
    // not dirty ⇒ Commit disabled. Without it, yaml stays stale and Commit stays
    // enabled on a phantom diff that would overwrite the committed file.
    await waitFor(() => expect(commit().disabled).toBe(true))
  })

  it('adopts a later seed arrival too (A → B) while the user has not edited', async () => {
    const { rerender } = render(editor('marker-ONE\n'))
    const commit = () => screen.getByTestId('yaml-editor-apply') as HTMLButtonElement
    // Seeded with ONE ⇒ not dirty ⇒ disabled.
    await waitFor(() => expect(commit().disabled).toBe(true))
    // A newer committed seed arrives; the editor re-adopts it, staying in sync
    // (not dirty), never stranded on the first seed.
    rerender(editor('marker-TWO\n'))
    await waitFor(() => expect(commit().disabled).toBe(true))
  })
})
