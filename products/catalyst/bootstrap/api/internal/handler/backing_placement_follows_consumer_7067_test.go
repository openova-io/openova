package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestCreateInstance_BackingFollowsConsumerRegions_7067 — a backing instance is
// placed like the install that creates it. On hw307 the Sovereign-wide probe
// answered single-region for a two-region install, so the backing bp-postgres
// was written as `singleton` across BOTH regions, failed placement, and blocked
// the consumer that dependsOn it.
func TestCreateInstance_BackingFollowsConsumerRegions_7067(t *testing.T) {
	cases := []struct {
		name, topology, regions string
		wantMode                string
		wantRegions             int
	}{
		{"two-region consumer", "active-hot-standby", `["` + refTestRegionA + `","` + refTestRegionB + `"]`, "active-hot-standby", 2},
		{"one-region consumer", "singleton", `["` + refTestRegionA + `"]`, "singleton", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOVEREIGN_REGIONS", "") // the probe says single-region
			h, dyn := newTestHandlerWithEndpoint(t)
			h.SetCatalogClient(newFakeCatalog(
				shareablePostgresCatalog().byKey["bp-postgres@0.1.6"],
				fakeBlueprintInCatalog("wordpress", []map[string]interface{}{}, true, []string{"singleton", "active-hot-standby"}).byKey["wordpress@1.0.0"],
			))
			r := newTestRouter(h)
			body := []byte(`{"blueprint":"wordpress","org":"acme","name":"wiki","topology":"` + tc.topology + `",` +
				`"placement":{"regions":` + tc.regions + `},"backing":[{"blueprint":"bp-postgres"}]}`)
			req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
			}
			backing, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wiki-postgres", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("backing CR missing: %v", err)
			}
			mode, _, _ := unstructured.NestedString(backing.Object, "spec", "placement", "mode")
			regions, _, _ := unstructured.NestedStringSlice(backing.Object, "spec", "regions")
			if mode != tc.wantMode || len(regions) != tc.wantRegions {
				t.Fatalf("backing placement = %s over %v, want %s over %d region(s)", mode, regions, tc.wantMode, tc.wantRegions)
			}
		})
	}
}
