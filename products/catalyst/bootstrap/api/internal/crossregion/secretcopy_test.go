package crossregion

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// #7922: ApplyCopy orphans every write and writes an owned destination
// even when its bytes already match.
func TestApplyCopy_OrphansAndWritesOwnedDestination_7922(t *testing.T) {
	owner := []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "shared-pg", UID: "u1"}}
	desired := &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"ca.crt": []byte("region-a")}}
	cases := []struct {
		name               string
		owners             []metav1.OwnerReference
		matches            bool
		write, changed     bool
		wantData, wantType string
	}{
		{"unowned and matching: nothing to write", nil, true, false, false, "old", ""},
		{"owned and matching: orphaned, no byte changes", owner, true, true, false, "region-a", string(corev1.SecretTypeTLS)},
		{"owned and drifted: orphaned and rewritten", owner, false, true, true, "region-a", string(corev1.SecretTypeTLS)},
		{"unowned and drifted: rewritten", nil, false, true, true, "region-a", string(corev1.SecretTypeTLS)},
	}
	for _, c := range cases {
		existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{OwnerReferences: c.owners}, Data: map[string][]byte{"ca.crt": []byte("old")}}
		write, changed := ApplyCopy(existing, desired, c.matches)
		if write != c.write || changed != c.changed {
			t.Errorf("%s: write=%v changed=%v, want write=%v changed=%v", c.name, write, changed, c.write, c.changed)
		}
		if string(existing.Data["ca.crt"]) != c.wantData || string(existing.Type) != c.wantType {
			t.Errorf("%s: data=%q type=%q, want data=%q type=%q", c.name, existing.Data["ca.crt"], existing.Type, c.wantData, c.wantType)
		}
		if write && len(existing.OwnerReferences) != 0 {
			t.Errorf("%s: a written copy keeps ownerReferences %v; the garbage collector deletes it with its owner", c.name, existing.OwnerReferences)
		}
	}
}
