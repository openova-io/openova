// applications_vcluster_resources_test.go — #3931 (#3642 sibling).
//
// Coverage for the AppDetail Resources/Logs tab fix: after #3642 the
// front-door apps (gitea/grafana/harbor/keycloak/openbao/newapi/guacamole)
// moved INTO the per-tier `mgmt` vCluster. The loft-sh syncer mirrors each
// in-vCluster Pod/Service/etc. down to the HOST under sync namespace `mgmt`
// with a MANGLED name (`<name>-x-<vcns>-x-<vcluster>`, truncated +
// hash-suffixed when long) and stamps the authoritative in-vCluster
// identity onto `vcluster.loft.sh/object-*` annotations.
//
// The AppDetail Resources tab queries `GET /k8s/{kind}?namespace=<app
// targetNamespace>` (e.g. `gitea`); pre-#3931 the host namespace filter
// required `metadata.namespace == gitea`, so every mgmt-synced row was
// stripped → the tab came back empty for EVERY mgmt-vCluster app even
// though their pods are Running. objectInAppNamespace widens the match to
// the deterministic sync namespaces (mgmt/dmz/rtz) via the loft
// annotation, and the flatten step surfaces the de-mangled display name.
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mgmtSyncedGiteaPod mirrors the real hw171 host object: a gitea Pod synced
// from the in-vCluster `gitea` namespace into host ns `mgmt` with the loft
// mangled name + authoritative object-* annotations.
func mgmtSyncedGiteaPod() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      "gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster",
			"namespace": "mgmt",
			"annotations": map[string]any{
				"vcluster.loft.sh/object-namespace":      "gitea",
				"vcluster.loft.sh/object-name":           "gitea-75d9f486fb-g8hsr",
				"vcluster.loft.sh/object-host-namespace": "mgmt",
			},
			"labels": map[string]any{
				"app.kubernetes.io/instance": "gitea",
			},
		},
		"status": map[string]any{"phase": "Running"},
	}}
}

func TestVClusterSyncedObjectNamespace(t *testing.T) {
	cases := []struct {
		name string
		obj  *unstructured.Unstructured
		want string
	}{
		{
			name: "mgmt-synced gitea pod resolves to in-vcluster namespace",
			obj:  mgmtSyncedGiteaPod(),
			want: "gitea",
		},
		{
			name: "nil object yields empty",
			obj:  nil,
			want: "",
		},
		{
			// A host-native object in a NON-sync namespace must never be
			// reattributed, even if it somehow carries the annotation.
			name: "object in non-sync namespace is never reattributed",
			obj: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{
					"name":      "some-pod",
					"namespace": "default",
					"annotations": map[string]any{
						"vcluster.loft.sh/object-namespace": "gitea",
					},
				},
			}},
			want: "",
		},
		{
			// A host-native object that genuinely lives in the sync
			// namespace (e.g. the vcluster statefulset itself) carries no
			// object-namespace annotation → no reattribution.
			name: "host-native object in mgmt with no annotation yields empty",
			obj: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{
					"name":      "mgmt-vcluster-0",
					"namespace": "mgmt",
				},
			}},
			want: "",
		},
		{
			// Defensive: the host-namespace cross-check must reject a
			// mismatched/forged object-host-namespace.
			name: "mismatched object-host-namespace rejected",
			obj: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{
					"name":      "x-x-y-x-mgmt-vcluster",
					"namespace": "mgmt",
					"annotations": map[string]any{
						"vcluster.loft.sh/object-namespace":      "gitea",
						"vcluster.loft.sh/object-host-namespace": "dmz",
					},
				},
			}},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vClusterSyncedObjectNamespace(tc.obj); got != tc.want {
				t.Fatalf("vClusterSyncedObjectNamespace() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVClusterSyncedDisplayName(t *testing.T) {
	if got := vClusterSyncedDisplayName(mgmtSyncedGiteaPod()); got != "gitea-75d9f486fb-g8hsr" {
		t.Fatalf("display name de-mangle = %q, want %q", got, "gitea-75d9f486fb-g8hsr")
	}
	if got := vClusterSyncedDisplayName(nil); got != "" {
		t.Fatalf("nil object display name = %q, want empty", got)
	}
	// An object with no loft name annotation has no de-mangled name.
	plain := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "gitea-pg-1", "namespace": "gitea"},
	}}
	if got := vClusterSyncedDisplayName(plain); got != "" {
		t.Fatalf("non-synced object display name = %q, want empty", got)
	}
}

// TestObjectInAppNamespace is the core of the #3931 fix: the namespace
// filter the AppDetail Resources/Logs tabs depend on must match BOTH the
// pre-#3642 same-namespace case AND the post-#3642 mgmt-synced case,
// scoped strictly to the sync namespaces.
func TestObjectInAppNamespace(t *testing.T) {
	// In-vCluster CNPG pod that genuinely lives in host ns `gitea`
	// (database pods are NOT synced through the vCluster — they run on the
	// host under the app's targetNamespace). Pre-#3642 invariant path.
	giteaPGHost := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "gitea-pg-1", "namespace": "gitea"},
	}}
	// A grafana pod synced into mgmt — belongs to the grafana app, NOT gitea.
	grafanaSynced := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":      "grafana-54cbc744cf-96nhh-x-grafana-x-mgmt-vcluster",
			"namespace": "mgmt",
			"annotations": map[string]any{
				"vcluster.loft.sh/object-namespace":      "grafana",
				"vcluster.loft.sh/object-host-namespace": "mgmt",
			},
		},
	}}

	cases := []struct {
		name     string
		obj      *unstructured.Unstructured
		targetNS string
		want     bool
	}{
		{"mgmt-synced gitea pod matches gitea app", mgmtSyncedGiteaPod(), "gitea", true},
		{"host-native gitea-pg matches gitea app (pre-#3642 path)", giteaPGHost, "gitea", true},
		{"mgmt-synced grafana pod does NOT match gitea app", grafanaSynced, "gitea", false},
		{"mgmt-synced gitea pod does NOT match grafana app", mgmtSyncedGiteaPod(), "grafana", false},
		{"empty targetNS matches nothing", mgmtSyncedGiteaPod(), "", false},
		{"nil object matches nothing", nil, "gitea", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := objectInAppNamespace(tc.obj, tc.targetNS); got != tc.want {
				t.Fatalf("objectInAppNamespace(_, %q) = %v, want %v", tc.targetNS, got, tc.want)
			}
		})
	}
}

// TestFlattenK8sListItems_VClusterDisplayName verifies the list-row
// projection: a mgmt-synced row keeps its HOST metadata coordinates (so
// the Logs/tree drill-down resolves against the host apiserver) AND
// surfaces the de-mangled in-vCluster identity for rendering.
func TestFlattenK8sListItems_VClusterDisplayName(t *testing.T) {
	items := []*unstructured.Unstructured{mgmtSyncedGiteaPod()}
	out := flattenK8sListItems("pod", items)
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d", len(out))
	}
	row := out[0].Object

	// Host coordinates preserved for drill-down.
	gotName, _, _ := unstructured.NestedString(row, "metadata", "name")
	if gotName != "gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster" {
		t.Fatalf("metadata.name = %q, want host mangled name", gotName)
	}
	gotNS, _, _ := unstructured.NestedString(row, "metadata", "namespace")
	if gotNS != "mgmt" {
		t.Fatalf("metadata.namespace = %q, want host ns mgmt", gotNS)
	}

	// De-mangled identity surfaced for the UI.
	if dn, _ := row["displayName"].(string); dn != "gitea-75d9f486fb-g8hsr" {
		t.Fatalf("row displayName = %q, want de-mangled in-vcluster name", dn)
	}
	if vcNS, _ := row["vclusterNamespace"].(string); vcNS != "gitea" {
		t.Fatalf("row vclusterNamespace = %q, want gitea", vcNS)
	}

	// A non-synced host row must NOT carry the display fields.
	plain := []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "gitea-pg-1", "namespace": "gitea"},
		"status":   map[string]any{"phase": "Running"},
	}}}
	pout := flattenK8sListItems("pod", plain)[0].Object
	if _, ok := pout["displayName"]; ok {
		t.Fatalf("non-synced row must not carry displayName")
	}
	if _, ok := pout["vclusterNamespace"]; ok {
		t.Fatalf("non-synced row must not carry vclusterNamespace")
	}
}
