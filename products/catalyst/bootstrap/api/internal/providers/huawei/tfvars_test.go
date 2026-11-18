package huawei

import (
	"reflect"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// TfVars returns the variables of this cloud (CLD-104): the three IAM values
// of the credentials map, the OBS bucket name and huawei_region, the one cloud
// region, which falls back to the default region when the credentials name
// none. It returns nothing else.
func TestTfVars_TheVariablesOfThisCloud(t *testing.T) {
	p := &Provider{}
	for _, tc := range []struct{ name, region, want string }{
		{"the credentials name the region", "ap-test-1", "ap-test-1"},
		{"the credentials name none", "", defaultRegion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := creds()
			if tc.region != "" {
				c.Raw["region"] = tc.region
			}
			got, err := p.TfVars(providers.ProvisionSpec{Creds: c, ObjectStorage: providers.ObjectStorageSpec{Bucket: "the-bucket"}}, packProfile(t))
			if err != nil {
				t.Fatalf("TfVars: %v", err)
			}
			want := map[string]any{"huawei_access_key": "ak", "huawei_secret_key": "sk", "huawei_project_id": "pid", "huawei_region": tc.want, "obs_bucket_name": "the-bucket"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("TfVars = %v, want %v", got, want)
			}
		})
	}
}

// Normalize places the regions before any check: a spec that fails the
// credential check still has the one cloud region in every entry, its
// cloudRegion as its code and no zone, because the engine writes the module's
// regions from the spec as Normalize leaves it.
func TestNormalize_PlacesRegionsBeforeTheChecks(t *testing.T) {
	spec := providers.ProvisionSpec{
		Creds: providers.ProviderCreds{Raw: map[string]string{"region": "eu-test-9"}},
		Regions: []providers.RegionSpec{
			{CloudRegion: "zone-tag-a", Zone: "not-written", WorkerCount: 3},
			{Code: "kept-code", CloudRegion: "zone-tag-b", WorkerCount: 3},
		},
	}
	if err := (&Provider{}).Normalize(&spec, packProfile(t)); err == nil {
		t.Fatal("Normalize accepted a spec without the IAM credentials")
	}
	for i, want := range []providers.RegionSpec{
		{Code: "zone-tag-a", CloudRegion: "eu-test-9", WorkerCount: 3},
		{Code: "kept-code", CloudRegion: "eu-test-9", WorkerCount: 3},
	} {
		if spec.Regions[i] != want {
			t.Errorf("regions[%d] = %+v, want %+v", i, spec.Regions[i], want)
		}
	}
}
