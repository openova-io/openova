package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
)

// TestBootstrapHRInstances_ProjectsEngineHRs locks the #3188 card fix:
// a bootstrap-kit HelmRelease whose chart is bp-<blueprint> (the
// shared-PG engine shape: HR bp-postgres-shared, chart bp-postgres,
// release in shared-data, NO Application CR) must appear as ONE
// instance row — round-1 walked the card reading "0 instances" while
// the live engine served two consumers. Also: a non-matching chart is
// excluded, and a name already covered by an Application-CR row is not
// duplicated.
func TestBootstrapHRInstances_ProjectsEngineHRs(t *testing.T) {
	mk := func(name, chart, ready string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system", "uid": "uid-" + name},
			"spec": map[string]interface{}{
				"chart": map[string]interface{}{"spec": map[string]interface{}{"chart": chart}},
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": ready},
				},
			},
		}}
	}
	scheme := runtime.NewScheme()
	c := dynfake.NewSimpleDynamicClient(scheme,
		mk("bp-postgres-shared", "bp-postgres", "True"),
		mk("bp-gitea", "bp-gitea", "True"),
		mk("bp-postgres-covered", "bp-postgres", "False"),
	)
	h := &Handler{log: quietLog()}
	rows := h.bootstrapHRInstances(context.Background(), c, "postgres", nil,
		[]applicationSummary{{Name: "postgres-covered"}})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (engine HR only; gitea chart excluded, covered name deduped): %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Name != "postgres-shared" || r.Org != "platform" || r.Status != "Ready" || r.Blueprint != "postgres" {
		t.Fatalf("row = %+v, want name=postgres-shared org=platform status=Ready blueprint=postgres", r)
	}
	if r.ID != "uid-bp-postgres-shared" {
		t.Fatalf("ID = %q, want the HR UID", r.ID)
	}
	_ = metav1.ListOptions{}
}
