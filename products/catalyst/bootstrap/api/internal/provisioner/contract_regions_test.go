// contract_regions_test.go — the module input `regions` in the shape of
// module contract version 1 (infra/providers/_contract/contract.json),
// work package CLD-090: what writeTfvars writes for every provider, what a
// kept variables file gains before a destroy, and that the per-region
// read-back is compared by region code.

package provisioner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// contractRegionKeys are the fields of one `regions` entry of contract
// version 1.
var contractRegionKeys = []string{
	"availability_zone", "cloud_region", "code", "control_plane_size", "role", "worker_count", "worker_size",
}

// writtenRegions runs writeTfvars for req and returns `regions` as written.
func writtenRegions(t *testing.T, req Request) []map[string]any {
	t.Helper()
	dir := t.TempDir()
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "tofu.auto.tfvars.json")))
	if err != nil {
		t.Fatal(err)
	}
	var vars struct {
		Regions []map[string]any `json:"regions"`
	}
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatal(err)
	}
	for i, r := range vars.Regions {
		keys := make([]string, 0, len(r))
		for k := range r {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, contractRegionKeys) {
			t.Fatalf("regions[%d] has the fields %v, want exactly %v", i, keys, contractRegionKeys)
		}
	}
	return vars.Regions
}

func TestWriteTfvars_Regions_ContractShape_Hetzner(t *testing.T) {
	req := validBase()
	req.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx32", WorkerSize: "cpx32", WorkerCount: 2, Role: "secondary"},
		{Provider: "hetzner", CloudRegion: "fsn1", Code: "fsn1-dataplane", Zone: "fsn1-dc14", ControlPlaneSize: "cpx32", WorkerSize: "cpx42", WorkerCount: 3, Role: "primary"},
	}
	got := writtenRegions(t, req)
	want := []map[string]any{
		{"code": "fsn1", "cloud_region": "fsn1", "availability_zone": "", "role": "primary", "control_plane_size": "cpx32", "worker_size": "cpx32", "worker_count": float64(2)},
		{"code": "fsn1-dataplane", "cloud_region": "fsn1", "availability_zone": "fsn1-dc14", "role": "secondary", "control_plane_size": "cpx32", "worker_size": "cpx42", "worker_count": float64(3)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regions = %v\nwant      %v", got, want)
	}
}

// The place of each region (code, cloud_region, availability_zone) is the
// one the adapter's Normalize gives it (CLD-104); the sizes and the count are
// the request's. The real adapters are tested end to end in tfvarsgolden.
func TestWriteTfvars_Regions_PlacedByTheAdapter(t *testing.T) {
	withDouble(t, oneCloudRegion, nil)
	req := huaweiValidBase()
	req.SetCredential("region", "eu-test-9")
	req.Regions = []RegionSpec{
		{Provider: "huawei", CloudRegion: "zone-tag-a", ControlPlaneSize: "s7n.large.4", WorkerSize: "m7n.xlarge.8", WorkerCount: 3},
		{Provider: "huawei", CloudRegion: "zone-tag-b", Zone: "not-written", ControlPlaneSize: "s7n.large.4", WorkerSize: "m7n.xlarge.8", WorkerCount: 2},
	}
	got := writtenRegions(t, req)
	want := []map[string]any{
		{"code": "zone-tag-a", "cloud_region": "eu-test-9", "availability_zone": "", "role": "primary", "control_plane_size": "s7n.large.4", "worker_size": "m7n.xlarge.8", "worker_count": float64(3)},
		{"code": "zone-tag-b", "cloud_region": "eu-test-9", "availability_zone": "", "role": "secondary", "control_plane_size": "s7n.large.4", "worker_size": "m7n.xlarge.8", "worker_count": float64(2)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regions = %v\nwant      %v", got, want)
	}
}

func TestAddCloudRegionIfMissing(t *testing.T) {
	read := func(t *testing.T, path string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}
		var vars map[string]any
		if err := json.Unmarshal(raw, &vars); err != nil {
			t.Fatal(err)
		}
		return vars
	}
	cloudRegions := func(vars map[string]any) []any {
		var out []any
		for _, entry := range vars["regions"].([]any) {
			out = append(out, entry.(map[string]any)["cloud_region"])
		}
		return out
	}
	oldShape := `"regions": [{"code": "zone-tag-a", "role": "primary", "control_plane_size": "s", "worker_size": "w", "worker_count": 3}, {"code": "zone-tag-b", "role": "secondary", "control_plane_size": "s", "worker_size": "w", "worker_count": 3}]`

	t.Run("an entry without cloud_region gets the huawei_region of the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tofu.auto.tfvars.json")
		if err := os.WriteFile(path, []byte(`{"huawei_region": "eu-test-9", "obs_bucket_name": "kept", `+oldShape+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := addCloudRegionIfMissing(path, Request{Provider: "huawei", Credentials: map[string]string{"region": "not-read"}}); err != nil {
			t.Fatal(err)
		}
		vars := read(t, path)
		if got := cloudRegions(vars); !reflect.DeepEqual(got, []any{"eu-test-9", "eu-test-9"}) {
			t.Fatalf("cloud_region of the entries = %v, want eu-test-9 twice", got)
		}
		first := vars["regions"].([]any)[0].(map[string]any)
		if first["code"] != "zone-tag-a" || first["role"] != "primary" || first["worker_count"] != float64(3) || vars["obs_bucket_name"] != "kept" {
			t.Fatalf("a value other than cloud_region changed: %v", vars)
		}
	})

	t.Run("a file without huawei_region takes the region the adapter gives the request", func(t *testing.T) {
		withDouble(t, oneCloudRegion, nil)
		path := filepath.Join(t.TempDir(), "tofu.auto.tfvars.json")
		if err := os.WriteFile(path, []byte(`{`+oldShape+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := addCloudRegionIfMissing(path, Request{Provider: "huawei", Credentials: map[string]string{"region": "eu-test-7"}}); err != nil {
			t.Fatal(err)
		}
		if got := cloudRegions(read(t, path)); !reflect.DeepEqual(got, []any{"eu-test-7", "eu-test-7"}) {
			t.Fatalf("cloud_region of the entries = %v, want eu-test-7 twice", got)
		}
	})

	t.Run("a file of the contract shape is not written", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tofu.auto.tfvars.json")
		body := []byte(`{"huawei_region":"eu-test-9","regions":[{"code":"zone-tag-a","cloud_region":"eu-test-1"}]}`)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := addCloudRegionIfMissing(path, Request{}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != string(body) {
			t.Fatalf("the file was rewritten:\n%s", raw)
		}
	})

	t.Run("a file that is not JSON is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tofu.auto.tfvars.json")
		if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := addCloudRegionIfMissing(path, Request{}); err == nil {
			t.Fatal("want an error for a file that is not JSON")
		}
	})
}

// The per-region read-back is compared by region code: an explicit Code is
// the key every module of contract version 1 gives its output
// control_plane_ips_per_region.
func TestPartialRegionDetection_ComparesByRegionCode(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", Code: "fsn1-mgmt"},
		{Provider: "hetzner", CloudRegion: "fsn1", Code: "fsn1-dataplane"},
	}
	perRegion := normalisePerRegionKeys(declared, map[string]string{"fsn1-mgmt": "192.0.2.1", "fsn1-dataplane": "192.0.2.2"})
	materialised, missing := detectPartialRegionMaterialisation(declared, perRegion)
	sort.Strings(materialised)
	if !reflect.DeepEqual(materialised, []string{"fsn1-dataplane", "fsn1-mgmt"}) || len(missing) != 0 {
		t.Fatalf("materialised = %v, missing = %v; want both codes materialised and none missing", materialised, missing)
	}

	_, missing = detectPartialRegionMaterialisation(declared, normalisePerRegionKeys(declared, map[string]string{"fsn1-mgmt": "192.0.2.1"}))
	if !reflect.DeepEqual(missing, []string{"fsn1-dataplane"}) {
		t.Fatalf("missing = %v, want [fsn1-dataplane]", missing)
	}
}
