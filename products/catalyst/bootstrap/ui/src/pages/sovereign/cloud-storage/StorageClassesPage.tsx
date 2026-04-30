/**
 * StorageClassesPage — placeholder list page for
 * /cloud/storage/storage-classes (P3 of #309). Pending the
 * storage-class informer (#321).
 */

import { CloudListPlaceholder } from '../cloud-list/CloudListPlaceholder'

export function StorageClassesPage() {
  return (
    <CloudListPlaceholder
      testId="cloud-storage-classes"
      title="Storage Classes"
      tagline="Cluster-wide storage classes (local-path, longhorn, csi-cinder, etc.)."
      bodyText="Storage class data is not in the current informer set. The storage-class informer rollout is tracked separately."
      docsHref="https://github.com/openova-io/openova/issues/321"
    />
  )
}
