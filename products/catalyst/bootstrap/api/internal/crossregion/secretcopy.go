package crossregion

import corev1 "k8s.io/api/core/v1"

// ApplyCopy decides how the parent's cross-region Secret copy treats a
// destination Secret that already exists, and prepares it for the Update.
//
// matches says whether existing already carries desired's content (the
// handler's secretContentMatches: Type and effective Data). It returns
// write=false when nothing is to be written, and otherwise overwrites
// Data, StringData, Type, Labels and Annotations of existing with desired's
// and clears its ownerReferences; changed says whether the bytes change, so
// a caller restarts consumers (#4878) only on a real change.
//
// The copy is always orphaned (#7922). A destination Secret that an object
// of the destination cluster minted stays owned by that object when only
// its bytes are overwritten, and is garbage-collected with it. That is the
// shape of a follower region's shared-pg before the database-pair flip:
// a placeholder singleton Cluster named `<instance>` (bp-postgres
// preFlipSecondary, #4460) for which CNPG mints `<instance>-ca` and
// `<instance>-replication` with an ownerReference to it, the very names the
// parent copies. The flip's Helm upgrade deletes the placeholder and the
// copies went with it (hw307, 15 Nov 2026: copied 16:49:53, placeholder
// `shared-pg` deleted 16:50:02.83, `missing specified client CA secret
// shared-pg-ca` 16:50:03.22, healed by the next steady-state pass at
// 17:01). So an owned destination is written even when its bytes match;
// that write changes no byte and reports changed=false.
func ApplyCopy(existing, desired *corev1.Secret, matches bool) (write, changed bool) {
	if matches && len(existing.OwnerReferences) == 0 {
		return false, false
	}
	existing.Data = desired.Data
	existing.StringData = desired.StringData
	existing.Type = desired.Type
	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.OwnerReferences = nil
	return true, !matches
}
