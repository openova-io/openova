package handler

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeTFState mirrors a minimal Huawei tofu state carrying one ELB + one
// EIP, so the generator yields two CloudAdoption docs the decoder must
// round-trip into namespaced CloudAdoption objects with the real
// external-name id. This is the #4212 write-seam producer→decoder contract.
const fakeHuaweiTFState = `{
  "resources": [
    {
      "type": "huaweicloud_elb_loadbalancer",
      "name": "primary",
      "instances": [
        {"index_key": null, "attributes": {"id": "d0e827ad-2e02-44b3-9b25-1513b7b771f4"}}
      ]
    },
    {
      "type": "huaweicloud_vpc_eip",
      "name": "elb_primary",
      "instances": [
        {"index_key": null, "attributes": {"id": "eip-abc-123", "publicip": [{"ip_address": "212.72.24.33"}]}}
      ]
    },
    {
      "type": "huaweicloud_obs_bucket",
      "name": "ignored",
      "instances": [
        {"index_key": null, "attributes": {"id": "bucket-not-adopted"}}
      ]
    }
  ]
}`

func TestDecodeCloudAdoptionDocs_RoundTripFromGenerator(t *testing.T) {
	yamlBytes, err := provisioner.GenerateAdoptionClaims(
		[]byte(fakeHuaweiTFState), "t99.omani.works", "huawei", "me-east-215")
	if err != nil {
		t.Fatalf("GenerateAdoptionClaims: %v", err)
	}

	objs, err := decodeCloudAdoptionDocs(yamlBytes)
	if err != nil {
		t.Fatalf("decodeCloudAdoptionDocs: %v", err)
	}

	// Two adoptable resources (ELB + EIP); the OBS bucket is intentionally
	// skipped by the generator's type allow-list.
	if len(objs) != 2 {
		t.Fatalf("expected 2 CloudAdoption objects, got %d", len(objs))
	}

	foundELBID := false
	for _, o := range objs {
		if o.GetKind() != "CloudAdoption" {
			t.Errorf("kind = %q, want CloudAdoption", o.GetKind())
		}
		if o.GetNamespace() != adoptionNamespace {
			t.Errorf("namespace = %q, want %q", o.GetNamespace(), adoptionNamespace)
		}
		ann := o.GetAnnotations()
		if ann["crossplane.io/external-name"] == "" {
			t.Errorf("%s: missing crossplane.io/external-name annotation", o.GetName())
		}
		if ann["crossplane.io/external-name"] == "d0e827ad-2e02-44b3-9b25-1513b7b771f4" {
			foundELBID = true
		}
		// Observe-first guard: manage must be false on every generated claim.
		manage, found, _ := unstructuredNestedBool(o.Object, "spec", "parameters", "manage")
		if !found {
			t.Errorf("%s: spec.parameters.manage missing", o.GetName())
		}
		if manage {
			t.Errorf("%s: manage=true — adoption MUST be Observe-first", o.GetName())
		}
	}
	if !foundELBID {
		t.Errorf("the real ELB id was not carried onto a CloudAdoption external-name")
	}
}

func TestDecodeCloudAdoptionDocs_EmptyStateNoObjects(t *testing.T) {
	yamlBytes, err := provisioner.GenerateAdoptionClaims(
		[]byte(`{"resources":[]}`), "t99.omani.works", "huawei", "me-east-215")
	if err != nil {
		t.Fatalf("GenerateAdoptionClaims: %v", err)
	}
	objs, err := decodeCloudAdoptionDocs(yamlBytes)
	if err != nil {
		t.Fatalf("decodeCloudAdoptionDocs: %v", err)
	}
	if len(objs) != 0 {
		t.Fatalf("expected 0 objects for empty state, got %d", len(objs))
	}
}

func TestIsNoMatchOrCRDMissing(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"no matches for kind", "no matches for kind \"CloudAdoption\" in version \"compose.openova.io/v1alpha1\"", true},
		{"server doesn't have type", "the server doesn't have a resource type \"cloudadoptions\"", true},
		{"could not find resource", "could not find the requested resource", true},
		{"unrelated", "connection refused", false},
		{"validation", "admission webhook denied the request", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isNoMatchOrCRDMissing(errString(tc.msg))
			if got != tc.want {
				t.Errorf("isNoMatchOrCRDMissing(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// unstructuredNestedBool reads a nested bool from an unstructured map.
func unstructuredNestedBool(obj map[string]any, fields ...string) (bool, bool, error) {
	cur := any(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return false, false, nil
		}
		cur, ok = m[f]
		if !ok {
			return false, false, nil
		}
	}
	b, ok := cur.(bool)
	if !ok {
		return false, false, nil
	}
	return b, true, nil
}

// guard: ensure the strings import is used even if the file evolves.
var _ = strings.TrimSpace
