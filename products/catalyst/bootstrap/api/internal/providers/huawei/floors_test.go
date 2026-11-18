package huawei

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// packProfile is the Huawei pack profile of this repository: no flavor, the
// floors 4 vCPU / 32 GiB and 8 vCPU / 64 GiB, at least 3 workers.
func packProfile(t *testing.T) *cloudprofile.Profile {
	t.Helper()
	profiles, err := cloudprofile.Load(filepath.Join("..", "..", "..", "..", "..", "..", "..", "infra", "providers"))
	if err != nil {
		t.Fatalf("load the cloud profiles: %v", err)
	}
	return profiles[Name]
}

func creds() providers.ProviderCreds {
	return providers.ProviderCreds{Raw: map[string]string{"access_key": "ak", "secret_key": "sk", "project_id": "pid"}}
}

// The rewrites of the create handler before CLD-103, now read from the
// profile: an empty or undersized flavor goes up to the floor flavor, a
// flavor at or above the floor and a flavor of no list stay.
func TestNormalize_FlavorFloors_PackProfile(t *testing.T) {
	cases := []struct{ cp, worker, wantCP, wantWorker string }{
		{"", "", "m7n.xlarge.8", "m7n.2xlarge.8"},
		{"s7n.large.4", "s7n.large.4", "m7n.xlarge.8", "m7n.2xlarge.8"},
		{"m7n.large.8", "m7n.large.8", "m7n.xlarge.8", "m7n.2xlarge.8"},
		{"m7n.xlarge.8", "m7n.xlarge.8", "m7n.xlarge.8", "m7n.2xlarge.8"},
		{"m7n.2xlarge.8", "m7n.2xlarge.8", "m7n.2xlarge.8", "m7n.2xlarge.8"},
		{"c7n.xlarge.2", "c7n.2xlarge.2", "c7n.xlarge.2", "c7n.2xlarge.2"},
	}
	for _, tc := range cases {
		spec := providers.ProvisionSpec{Creds: creds(), Regions: []providers.RegionSpec{{CloudRegion: "r-a", ControlPlaneSize: tc.cp, WorkerSize: tc.worker, WorkerCount: 3}}}
		if err := New().Normalize(&spec, packProfile(t)); err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		if r := spec.Regions[0]; r.ControlPlaneSize != tc.wantCP || r.WorkerSize != tc.wantWorker {
			t.Errorf("(%q, %q) became (%q, %q), want (%q, %q)", tc.cp, tc.worker, r.ControlPlaneSize, r.WorkerSize, tc.wantCP, tc.wantWorker)
		}
	}
}

func TestNormalize_WorkerMinimum(t *testing.T) {
	for in, want := range map[int]int{0: 3, 1: 3, 3: 3, 5: 5} {
		spec := providers.ProvisionSpec{Creds: creds(), Regions: []providers.RegionSpec{
			{CloudRegion: "r-a", ControlPlaneSize: "m7n.xlarge.8", WorkerSize: "m7n.2xlarge.8", WorkerCount: in},
			{CloudRegion: "r-b", ControlPlaneSize: "m7n.xlarge.8", WorkerSize: "m7n.2xlarge.8", WorkerCount: in},
		}}
		if err := New().Normalize(&spec, packProfile(t)); err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		for i, r := range spec.Regions {
			if r.WorkerCount != want {
				t.Errorf("region %d: workerCount %d became %d, want %d", i, in, r.WorkerCount, want)
			}
		}
	}
}

// An effective profile that lists flavors replaces the built-in list: the
// floor flavor is the smallest listed one that meets the floor.
func TestNormalize_FlavorFloors_ListedFlavors(t *testing.T) {
	p := *packProfile(t)
	p.Spec.Compute.Flavors = []cloudprofile.Flavor{
		{Name: "small", VCPU: 2, MemoryGiB: 8, Roles: []string{roleControlPlane, roleWorker}},
		{Name: "big", VCPU: 16, MemoryGiB: 128, Roles: []string{roleControlPlane, roleWorker}},
		{Name: "cp-only", VCPU: 4, MemoryGiB: 32, Roles: []string{roleControlPlane}},
	}
	spec := providers.ProvisionSpec{Creds: creds(), Regions: []providers.RegionSpec{{CloudRegion: "r-a", ControlPlaneSize: "small", WorkerSize: "m7n.large.8", WorkerCount: 3}}}
	if err := New().Normalize(&spec, &p); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if r := spec.Regions[0]; r.ControlPlaneSize != "cp-only" || r.WorkerSize != "m7n.large.8" {
		t.Errorf("got (%q, %q), want (cp-only, m7n.large.8): a flavor of no list is kept", r.ControlPlaneSize, r.WorkerSize)
	}
	spec.Regions[0].WorkerSize = ""
	if err := New().Normalize(&spec, &p); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if spec.Regions[0].WorkerSize != "big" {
		t.Errorf("empty worker flavor became %q, want big", spec.Regions[0].WorkerSize)
	}
}

// The Sovereign has one cloud region: entries whose cloudRegion differ keep
// it as their code and pass the multiCloudRegion: false check.
func TestNormalize_OneCloudRegion(t *testing.T) {
	c := creds()
	c.Raw["region"] = "cloud-1"
	spec := providers.ProvisionSpec{Creds: c, Regions: []providers.RegionSpec{
		{CloudRegion: "cloud-1-a", ControlPlaneSize: "m7n.xlarge.8", WorkerSize: "m7n.2xlarge.8", WorkerCount: 3},
		{Code: "b", CloudRegion: "cloud-1-b", ControlPlaneSize: "m7n.xlarge.8", WorkerSize: "m7n.2xlarge.8", WorkerCount: 3},
	}}
	if err := New().Normalize(&spec, packProfile(t)); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if r := spec.Regions; r[0].Code != "cloud-1-a" || r[1].Code != "b" || r[0].CloudRegion != "cloud-1" || r[1].CloudRegion != "cloud-1" {
		t.Errorf("regions = %+v, want codes cloud-1-a and b in cloud region cloud-1", r)
	}
}

func TestNormalize_RequiredCredentials(t *testing.T) {
	for _, key := range []string{"access_key", "secret_key", "project_id"} {
		c := creds()
		delete(c.Raw, key)
		spec := providers.ProvisionSpec{Creds: c, Regions: []providers.RegionSpec{{CloudRegion: "r-a", ControlPlaneSize: "m7n.xlarge.8", WorkerCount: 3}}}
		var fe *providers.FieldError
		if err := New().Normalize(&spec, packProfile(t)); !errors.As(err, &fe) || fe.Field != "credentials."+key {
			t.Errorf("without %s: got %v, want a field error on credentials.%s", key, err, key)
		}
	}
}
