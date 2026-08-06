/**
 * StorageClassesPage — /cloud/storage/storage-classes.
 *
 * #5611: was a CloudListPlaceholder ("Storage class data is not in the
 * current informer set. The storage-class informer rollout is tracked
 * separately."). The `storageclass` GVR is now registered in the
 * catalyst-api k8scache (api/internal/k8scache/kinds.go), so this route
 * renders the SAME live list the /cloud?view=list&kind=storage-classes
 * chip navigates to — one component, one source, so the two routes can
 * never disagree about how many storage classes exist.
 */

export { StorageClassesListPage as StorageClassesPage } from '../cloud-list/kindsPages'
