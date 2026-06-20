/**
 * CloudListView — list-mode body for the Cloud parent surface.
 *
 * #3970 unifies the list with the graph: the list is now ONE rendering of
 * the SAME dataset (cloud + k8s + reconcilers), driven by the SAME active
 * chip-set (the shared lens state). The legacy per-kind dispatch (a
 * separate `cloud-list/kinds.ts` catalogue that had NO reconciler kinds)
 * is replaced by `CloudUnifiedList`, which renders every resource the
 * graph shows — including HelmReleases / Kustomizations / Certificates /
 * ExternalSecrets / Databases (CNPG) and the Catalyst CRs — filtered by
 * the active lens.
 */

import { CloudUnifiedList } from './CloudUnifiedList'

export function CloudListView() {
  return (
    <div data-testid="cloud-list-view">
      <CloudUnifiedList />
    </div>
  )
}
