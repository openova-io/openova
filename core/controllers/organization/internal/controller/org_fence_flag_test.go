package controller

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

// NET-170 — the namespaces of the flag --org-fence-allow-namespaces
// (Reconciler.OrgFenceAllowNamespaces) reach the org-fence object the
// controller commits, in the shared host-apps tree and in its region-baseline
// copy; without the field the default kube-system,keycloak is committed.
func TestReconcile_OrgFenceAllowNamespacesReachTheCommittedFence(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		allow   []string
		want    []string
		notWant []string
	}{
		{"default", nil, []string{`- "acme"`, `- "kube-system"`, `- "keycloak"`}, []string{`- "vllm"`}},
		{"flag", []string{"kube-system", "keycloak", "vllm"}, []string{`- "acme"`, `- "kube-system"`, `- "keycloak"`, `- "vllm"`}, nil},
	} {
		org := sampleOrg()
		r, gs, _ := makeReconciler(t, org)
		r.OrgFenceAllowNamespaces = c.allow
		if _, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "acme"},
		}); err != nil {
			t.Fatalf("%s: reconcile: %v", c.name, err)
		}
		for _, key := range []string{
			"acme/catalyst-tenant/vcluster/host-apps/org-fence.yaml",
			"acme/catalyst-tenant/vcluster/region-baseline/host-apps/org-fence.yaml",
		} {
			f, ok := gs.files[key]
			if !ok {
				t.Fatalf("%s: the controller did not commit %s", c.name, key)
			}
			_, egress, ok := strings.Cut(string(f.content), "egressDeny:")
			if !ok {
				t.Fatalf("%s: %s has no egressDeny:\n%s", c.name, key, f.content)
			}
			for _, w := range c.want {
				if !strings.Contains(egress, w) {
					t.Errorf("%s: the egress deny of %s does not exempt %s:\n%s", c.name, key, w, egress)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(egress, w) {
					t.Errorf("%s: the egress deny of %s exempts %s:\n%s", c.name, key, w, egress)
				}
			}
		}
	}
}
