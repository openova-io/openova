package render

import (
	"testing"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/endpoints"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Every fan-out leg carries the Application identity and its resolved public
// endpoints (Refs #7067: the fan-out path rendered no endpoints, so the chart
// rendered no HTTPRoute).
func TestFanoutHRs_EndpointsAndIdentityOnEveryLeg(t *testing.T) {
	v := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		AppName: "blog", AppNamespace: "acme", Topology: bpv1alpha1.BcpActiveHotStandby, Variant: &v,
		Chart: "bp-wordpress-tenant", RegionFor: testRegionFor,
		Endpoints: []endpoints.Endpoint{{Name: "ui", Hostname: "blog.acme.omani.rest", Port: 443, TLS: true, LaunchDefault: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hrs) != 2 {
		t.Fatalf("legs = %d, want 2", len(hrs))
	}
	for _, hr := range hrs {
		eps, _, _ := unstructured.NestedSlice(hr.Object, "spec", "values", "openova", "endpoints")
		if len(eps) != 1 || eps[0].(map[string]interface{})["hostname"] != "blog.acme.omani.rest" {
			t.Errorf("%s openova.endpoints = %#v", hr.GetName(), eps)
		}
		if n, _, _ := unstructured.NestedString(hr.Object, "spec", "values", "openova", "application", "name"); n != "blog" {
			t.Errorf("%s openova.application.name = %q", hr.GetName(), n)
		}
	}
}
