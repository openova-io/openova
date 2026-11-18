package hetzner

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// packProfile is the Hetzner pack profile of this repository.
func packProfile(t *testing.T) *cloudprofile.Profile {
	t.Helper()
	profiles, err := cloudprofile.Load(filepath.Join("..", "..", "..", "..", "..", "..", "..", "infra", "providers"))
	if err != nil {
		t.Fatalf("load the cloud profiles: %v", err)
	}
	return profiles[Name]
}

// validSpec passes Normalize against the pack profile.
func validSpec() providers.ProvisionSpec {
	return providers.ProvisionSpec{
		Creds: providers.ProviderCreds{Raw: map[string]string{
			"hcloud_token":              "token",
			"object_storage_access_key": "access",
			"object_storage_secret_key": "secret",
			"object_storage_region":     "fsn1",
		}},
		ObjectStorage: providers.ObjectStorageSpec{Region: "fsn1"},
		Regions:       []providers.RegionSpec{{Code: "fsn1", CloudRegion: "fsn1", ControlPlaneSize: "cpx22", WorkerSize: "cpx42", WorkerCount: 1}},
	}
}

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var fe *providers.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("got %v, want a *providers.FieldError", err)
	}
	return fe.Field
}

func TestNormalize_AcceptsAValidSpec(t *testing.T) {
	spec := validSpec()
	if err := New().Normalize(&spec, packProfile(t)); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
}

func TestNormalize_RequiredCredentials(t *testing.T) {
	for _, key := range []string{"hcloud_token", "object_storage_access_key", "object_storage_secret_key", "object_storage_region"} {
		spec := validSpec()
		delete(spec.Creds.Raw, key)
		if got := fieldOf(t, New().Normalize(&spec, packProfile(t))); got != "credentials."+key {
			t.Errorf("without %s: field %q, want credentials.%s", key, got, key)
		}
	}
	spec := validSpec()
	delete(spec.Creds.Raw, "hcloud_project_id") // not required by the profile
	if err := New().Normalize(&spec, packProfile(t)); err != nil {
		t.Errorf("hcloud_project_id is optional in the profile: %v", err)
	}
}

// The pack profile says multiRegion: false.
func TestNormalize_OneRegionOnly(t *testing.T) {
	spec := validSpec()
	spec.Regions = append(spec.Regions, providers.RegionSpec{Code: "hel1", CloudRegion: "hel1", ControlPlaneSize: "cpx22"})
	if got := fieldOf(t, New().Normalize(&spec, packProfile(t))); got != "regions" {
		t.Errorf("two regions: field %q, want regions", got)
	}
}

// The Object Storage regions are the pattern of the profile's credential
// field object_storage_region.
func TestNormalize_ObjectStorageRegion(t *testing.T) {
	for region, ok := range map[string]bool{"fsn1": true, "nbg1": true, "hel1": true, "us-east-1": false, "ash": false} {
		spec := validSpec()
		spec.ObjectStorage.Region = region
		err := New().Normalize(&spec, packProfile(t))
		if ok && err != nil {
			t.Errorf("%s: %v", region, err)
		}
		if !ok && fieldOf(t, err) != "objectStorageRegion" {
			t.Errorf("%s: want a field error on objectStorageRegion", region)
		}
	}
}

// The availability of a server type per location (issue #916, #752).
func TestNormalize_SkuAvailability(t *testing.T) {
	cases := []struct {
		sku, region string
		ok          bool
	}{
		{"cpx9999", "fsn1", true},
		{"cpx22", "ash", true},
		{"cpx32", "fsn1", true},
		{"cpx32", "HEL1", true},
		{"cpx32", "ash", false},
		{"cpx32", "hil", false},
		{"cpx21", "fsn1", false},
		{"cpx31", "ash", false},
		{"cpx32", "", true},
	}
	for _, tc := range cases {
		for _, worker := range []bool{false, true} {
			spec := validSpec()
			if worker {
				spec.Regions[0].WorkerSize, spec.Regions[0].CloudRegion = tc.sku, tc.region
			} else {
				spec.Regions[0].ControlPlaneSize, spec.Regions[0].CloudRegion = tc.sku, tc.region
			}
			err := New().Normalize(&spec, packProfile(t))
			switch {
			case tc.ok && err != nil:
				t.Errorf("%s in %q (worker %v): %v", tc.sku, tc.region, worker, err)
			case !tc.ok && err == nil:
				t.Errorf("%s in %q (worker %v): accepted, want refused", tc.sku, tc.region, worker)
			case !tc.ok && !strings.Contains(err.Error(), "orderable"):
				t.Errorf("%s in %q: %v, want the orderable message", tc.sku, tc.region, err)
			}
		}
	}
	// A worker size is not judged when the region has no workers.
	spec := validSpec()
	spec.Regions[0].WorkerSize, spec.Regions[0].WorkerCount = "cpx21", 0
	if err := New().Normalize(&spec, packProfile(t)); err != nil {
		t.Errorf("cpx21 as the worker size of a region without workers: %v", err)
	}
}

// Decoding a request copies the legacy JSON fields into the credentials map.
func TestLegacyCredentialAliases_Decode(t *testing.T) {
	var r provisioner.Request
	body := `{"provider":"hetzner","hetznerToken":"t-1","hetznerProjectID":"p-1","objectStorageRegion":"fsn1","objectStorageAccessKey":"a-1","objectStorageSecretKey":"s-1"}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]string{"hcloud_token": "t-1", "hcloud_project_id": "p-1", "object_storage_region": "fsn1", "object_storage_access_key": "a-1", "object_storage_secret_key": "s-1"}
	if len(r.Credentials) != len(want) {
		t.Fatalf("credentials = %v, want %v", r.Credentials, want)
	}
	for k, v := range want {
		if r.Credentials[k] != v {
			t.Errorf("credentials[%s] = %q, want %q", k, r.Credentials[k], v)
		}
	}
	if r.Credentials["hcloud_token"] != "t-1" {
		t.Errorf("the legacy field still decodes: HetznerToken = %q", r.Credentials["hcloud_token"])
	}
}

// TfVars returns the variables of this cloud (CLD-104): the API token and the
// project of the credentials map, and nothing else.
func TestTfVars_TokenAndProject(t *testing.T) {
	spec := validSpec()
	spec.Creds.Raw["access_key"] = "not-of-this-cloud"
	spec.Creds.Raw["hcloud_project_id"] = "p-1"
	got, err := (&Provider{}).TfVars(spec, packProfile(t))
	if err != nil {
		t.Fatalf("TfVars: %v", err)
	}
	want := map[string]any{"hcloud_token": spec.Creds.Get("hcloud_token"), "hcloud_project_id": spec.Creds.Get("hcloud_project_id")}
	if len(got) != 2 || got["hcloud_token"] != want["hcloud_token"] || got["hcloud_project_id"] != want["hcloud_project_id"] {
		t.Errorf("TfVars = %v, want %v", got, want)
	}
}
