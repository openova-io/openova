package tfvarsgolden

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	// The two adapters, registered under their pack names: the variables
	// that each one writes and the regions it places are part of the files.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
)

// The cloud profiles of the repository.
func init() {
	provisioner.ProvidersDir = filepath.Join("..", "..", "..", "..", "..", "..", "..", "infra", "providers")
}

var updateGolden = flag.Bool("update", false, "rewrite the golden files of TestTfvarsGolden from the code as it stands")

// TestTfvarsGolden pins tofu.auto.tfvars.json, the variables writeTfvars hands
// to an OpenTofu module, for one fixed Hetzner and one fixed Huawei request
// (work package CLD-101). Each request has two regions, parent domains and
// every optional field set. What provisioner.WriteTfvars writes with the two
// real adapters (CLD-104: the engine's variables, the regions each adapter
// places and the variables each adapter returns), with the keys sorted, must
// equal the golden file under ../testdata/. The test lives in a package of its
// own because the adapters import the engine. The golden files are written
// from the code as it stands, never by hand:
//
//	go test -count=1 -run '^TestTfvarsGolden$' ./internal/provisioner/tfvarsgolden/ -args -update
//
// A change that is meant to change the variables runs that command and shows
// the diff of the golden files in its pull request.
func TestTfvarsGolden(t *testing.T) {
	// The golden files must not depend on the machine: every CATALYST_
	// variable is empty except the five that writeTfvars reads.
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CATALYST_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("CATALYST_API_PUBLIC_URL", "https://console.parent.example.org/sovereign")
	t.Setenv("CATALYST_GITOPS_REPO_URL", "https://git.parent.example.org/catalog/openova")
	t.Setenv("CATALYST_GITOPS_BRANCH", "main")
	t.Setenv("CATALYST_EGRESS_CIDRS", "198.51.100.10/32")
	t.Setenv("CATALYST_DEFAULT_ADMIN_CIDRS", "203.0.113.0/24")
	for name, req := range map[string]provisioner.Request{"hetzner": goldenRequestHetzner(), "huawei": goldenRequestHuawei()} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := provisioner.WriteTfvars(dir, req); err != nil {
				t.Fatalf("WriteTfvars: %v", err)
			}
			raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "tofu.auto.tfvars.json")))
			if err != nil {
				t.Fatalf("read the written variables: %v", err)
			}
			got, err := sortedJSON(raw)
			if err != nil {
				t.Fatalf("the written variables are not JSON: %v", err)
			}
			path := filepath.Join("..", "testdata", "tfvars-"+name+".golden.json")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Join("..", "testdata"), 0o750); err != nil {
					t.Fatalf("create testdata: %v", err)
				}
				if err := os.WriteFile(path, got, 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(filepath.Clean(path))
			if err != nil {
				t.Fatalf("read %s: %v (run this test with -args -update to write it)", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("writeTfvars no longer writes %s:\n%s", path, firstDifference(want, got))
			}
		})
	}
}

// sortedJSON encodes a JSON document again with the keys of every object
// sorted, an indent of two spaces, no HTML escaping and a final newline.
func sortedJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// firstDifference names the first line where two documents differ.
func firstDifference(want, got []byte) string {
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d\n golden: %s\n now:    %s", i+1, wl, gl)
		}
	}
	return "the documents differ"
}

// goldenRequest holds the fields both golden requests set the same way.
// Every value is made up.
func goldenRequest(label, deploymentID string) provisioner.Request {
	addedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return provisioner.Request{
		OrgName: "Golden Org", OrgEmail: "owner@example.com",
		SovereignFQDN: label + ".example.com", SovereignDomainMode: "pool",
		SovereignPoolDomain: "example.com", SovereignSubdomain: label,
		ParentDomains: []provisioner.ParentDomain{
			{Name: "example.com", Role: provisioner.ParentDomainRolePrimary, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-com", AddedAt: addedAt},
			{Name: "orgs.example.net", Role: provisioner.ParentDomainRoleOrgPool, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-net", AddedAt: addedAt},
		},
		HAEnabled: true, MarketplaceEnabled: true, EnableSharedPostgres: true, QATestEnabled: true,
		AdminCIDRs:          []string{"192.0.2.0/28"},
		QAFixturesNamespace: "qa-golden-" + label, QAOrganization: "golden-" + label + "-platform",
		SSHPublicKey:  "ssh-ed25519 AAAAgoldentestkey" + label + " owner@example.com",
		DynadotAPIKey: "test-dynadot-key", DynadotAPISecret: "test-dynadot-secret",
		GHCRPullToken: "test-ghcr-pull-token", HarborRobotToken: "test-harbor-robot-token",
		PowerDNSAPIKey: "test-powerdns-api-key", PDMBasicAuthUser: "test-pdm-user", PDMBasicAuthPass: "test-pdm-pass",
		DeploymentID: deploymentID, KubeconfigBearerToken: "test-kubeconfig-bearer-token",
		HandoverJWTPublicKey:   `{"kty":"RSA","n":"dGVzdA","e":"AQAB"}`,
		ObjectStorageAccessKey: "test-object-storage-access-key", ObjectStorageSecretKey: "test-object-storage-secret-key",
		ObjectStorageBucket: "golden-" + label + "-bucket",
	}
}

// goldenRequestHetzner is the fixed Hetzner request. The singular region and
// size fields carry Regions[0], as Validate leaves them.
func goldenRequestHetzner() provisioner.Request {
	off := false
	r := goldenRequest("hz", "0123456789abcdef")
	r.Provider = "hetzner"
	r.Credentials = map[string]string{"hcloud_token": "test-hetzner-token", "hcloud_project_id": "test-hetzner-project"}
	r.Region, r.ControlPlaneSize, r.WorkerSize, r.WorkerCount = "fsn1", "cpx31", "cpx41", 3
	r.StorageClass, r.BcpTopology, r.ObjectStorageRegion = "golden-volumes", "active-hotstandby", "fsn1"
	r.ConsoleIsolationEnabled, r.FireCutoverOnHandover = &off, &off
	r.ClusterMeshName, r.ClusterMeshID, r.GitopsBranch = "hz-mesh", 7, "rc/20260102T030405Z-0123abc"
	r.Regions = []provisioner.RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx31", WorkerSize: "cpx41", WorkerCount: 3, Code: "fsn1", Zone: "fsn1-dc14", Role: "primary", StorageClass: "golden-volumes", ClusterMeshName: "hz-fsn"},
		{Provider: "hetzner", CloudRegion: "hel1", ControlPlaneSize: "cpx21", WorkerSize: "cpx31", WorkerCount: 2, Code: "hel1", Zone: "hel1-dc2", Role: "secondary", StorageClass: "golden-volumes", ClusterMeshName: "hz-hel"},
	}
	return r
}

// goldenRequestHuawei is the fixed Huawei request. The singular region and
// size fields carry Regions[0], as Validate leaves them.
func goldenRequestHuawei() provisioner.Request {
	on := true
	r := goldenRequest("hw", "fedcba9876543210")
	r.Provider = "huawei"
	r.Credentials = map[string]string{"access_key": "test-huawei-access-key", "secret_key": "test-huawei-secret-key", "project_id": "test-huawei-project", "region": "region-1"}
	r.Region, r.ControlPlaneSize, r.WorkerSize, r.WorkerCount = "region-1-a", "m7n.large.8", "m7n.xlarge.8", 4
	r.StorageClass, r.BcpTopology, r.ObjectStorageRegion = "golden-disks", "active-active", "region-1"
	r.ConsoleIsolationEnabled, r.FireCutoverOnHandover = &on, &on
	r.ClusterMeshName, r.ClusterMeshID, r.GitopsBranch = "hw-mesh", 9, "main"
	r.Regions = []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "region-1-a", ControlPlaneSize: "m7n.large.8", WorkerSize: "m7n.xlarge.8", WorkerCount: 4, Code: "region-1-a", Zone: "region-1-a-az1", Role: "primary", StorageClass: "golden-disks", ClusterMeshName: "hw-a"},
		{Provider: "huawei", CloudRegion: "region-1-b", ControlPlaneSize: "m7n.large.8", WorkerSize: "m7n.xlarge.8", WorkerCount: 3, Code: "region-1-b", Zone: "region-1-b-az1", Role: "secondary", StorageClass: "golden-disks", ClusterMeshName: "hw-b"},
	}
	return r
}

// TestWriteTfvars_Regions_Huawei: with the real adapter, every region of a
// Huawei request gets the one cloud region of the Sovereign (the credential
// "region", or the default region when the request names none), keeps its
// cloudRegion as its code and has no zone; the role, the sizes and the count
// are the request's (CLD-090, CLD-104).
func TestWriteTfvars_Regions_Huawei(t *testing.T) {
	for _, tc := range []struct{ name, region, want string }{
		{"the request names the cloud region", "eu-test-9", "eu-test-9"},
		{"the request names none", "", "me-east-215"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := goldenRequestHuawei()
			delete(req.Credentials, "region")
			if tc.region != "" {
				req.SetCredential("region", tc.region)
			}
			req.Regions = []provisioner.RegionSpec{
				{Provider: "huawei", CloudRegion: "zone-tag-a", ControlPlaneSize: "s7n.large.4", WorkerSize: "m7n.xlarge.8", WorkerCount: 3},
				{Provider: "huawei", CloudRegion: "zone-tag-b", Zone: "not-written", ControlPlaneSize: "s7n.large.4", WorkerSize: "m7n.xlarge.8", WorkerCount: 3},
			}
			dir := t.TempDir()
			if err := provisioner.WriteTfvars(dir, req); err != nil {
				t.Fatalf("WriteTfvars: %v", err)
			}
			raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "tofu.auto.tfvars.json")))
			if err != nil {
				t.Fatal(err)
			}
			var vars struct {
				Regions      []map[string]any `json:"regions"`
				HuaweiRegion string           `json:"huawei_region"`
			}
			if err := json.Unmarshal(raw, &vars); err != nil {
				t.Fatal(err)
			}
			want := []map[string]any{
				{"code": "zone-tag-a", "cloud_region": tc.want, "availability_zone": "", "role": "primary", "control_plane_size": "s7n.large.4", "worker_size": "m7n.xlarge.8", "worker_count": float64(3)},
				{"code": "zone-tag-b", "cloud_region": tc.want, "availability_zone": "", "role": "secondary", "control_plane_size": "s7n.large.4", "worker_size": "m7n.xlarge.8", "worker_count": float64(3)},
			}
			if !reflect.DeepEqual(vars.Regions, want) || vars.HuaweiRegion != tc.want {
				t.Fatalf("regions = %v, huawei_region %q\nwant      %v, %q", vars.Regions, vars.HuaweiRegion, want, tc.want)
			}
		})
	}
}
