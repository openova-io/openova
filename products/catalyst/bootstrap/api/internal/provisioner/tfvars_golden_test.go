package provisioner

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files of TestTfvarsGolden from the code as it stands")

// TestTfvarsGolden pins tofu.auto.tfvars.json, the variables writeTfvars hands
// to an OpenTofu module, for one fixed Hetzner and one fixed Huawei request
// (work package CLD-101). Each request has two regions, parent domains and
// every optional field set. What writeTfvars writes, with the keys sorted,
// must equal the golden file under testdata/. The golden files are written
// from the code as it stands, never by hand:
//
//	go test -count=1 -run '^TestTfvarsGolden$' ./internal/provisioner/ -args -update
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
	for name, req := range map[string]Request{"hetzner": goldenRequestHetzner(), "huawei": goldenRequestHuawei()} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := writeTfvars(dir, req); err != nil {
				t.Fatalf("writeTfvars: %v", err)
			}
			raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "tofu.auto.tfvars.json")))
			if err != nil {
				t.Fatalf("read the written variables: %v", err)
			}
			got, err := sortedJSON(raw)
			if err != nil {
				t.Fatalf("the written variables are not JSON: %v", err)
			}
			path := filepath.Join("testdata", "tfvars-"+name+".golden.json")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o750); err != nil {
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
func goldenRequest(label, deploymentID string) Request {
	addedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return Request{
		OrgName: "Golden Org", OrgEmail: "owner@example.com",
		SovereignFQDN: label + ".example.com", SovereignDomainMode: "pool",
		SovereignPoolDomain: "example.com", SovereignSubdomain: label,
		ParentDomains: []ParentDomain{
			{Name: "example.com", Role: ParentDomainRolePrimary, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-com", AddedAt: addedAt},
			{Name: "orgs.example.net", Role: ParentDomainRoleOrgPool, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-net", AddedAt: addedAt},
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
func goldenRequestHetzner() Request {
	off := false
	r := goldenRequest("hz", "0123456789abcdef")
	r.Provider, r.HetznerToken, r.HetznerProjectID = "hetzner", "test-hetzner-token", "test-hetzner-project"
	r.Region, r.ControlPlaneSize, r.WorkerSize, r.WorkerCount = "fsn1", "cpx31", "cpx41", 3
	r.StorageClass, r.BcpTopology, r.ObjectStorageRegion = "golden-volumes", "active-hotstandby", "fsn1"
	r.ConsoleIsolationEnabled, r.FireCutoverOnHandover = &off, &off
	r.ClusterMeshName, r.ClusterMeshID, r.GitopsBranch = "hz-mesh", 7, "rc/20260102T030405Z-0123abc"
	r.Regions = []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx31", WorkerSize: "cpx41", WorkerCount: 3, Code: "fsn1", Zone: "fsn1-dc14", Role: "primary", StorageClass: "golden-volumes", ClusterMeshName: "hz-fsn"},
		{Provider: "hetzner", CloudRegion: "hel1", ControlPlaneSize: "cpx21", WorkerSize: "cpx31", WorkerCount: 2, Code: "hel1", Zone: "hel1-dc2", Role: "secondary", StorageClass: "golden-volumes", ClusterMeshName: "hz-hel"},
	}
	return r
}

// goldenRequestHuawei is the fixed Huawei request. The singular region and
// size fields carry Regions[0], as Validate leaves them.
func goldenRequestHuawei() Request {
	on := true
	r := goldenRequest("hw", "fedcba9876543210")
	r.Provider, r.HuaweiAccessKey, r.HuaweiSecretKey = "huawei", "test-huawei-access-key", "test-huawei-secret-key"
	r.HuaweiProjectID, r.HuaweiRegion = "test-huawei-project", "region-1"
	r.Region, r.ControlPlaneSize, r.WorkerSize, r.WorkerCount = "region-1-a", "m7n.large.8", "m7n.xlarge.8", 4
	r.StorageClass, r.BcpTopology, r.ObjectStorageRegion = "golden-disks", "active-active", "region-1"
	r.ConsoleIsolationEnabled, r.FireCutoverOnHandover = &on, &on
	r.ClusterMeshName, r.ClusterMeshID, r.GitopsBranch = "hw-mesh", 9, "main"
	r.Regions = []RegionSpec{
		{Provider: "huawei", CloudRegion: "region-1-a", ControlPlaneSize: "m7n.large.8", WorkerSize: "m7n.xlarge.8", WorkerCount: 4, Code: "region-1-a", Zone: "region-1-a-az1", Role: "primary", StorageClass: "golden-disks", ClusterMeshName: "hw-a"},
		{Provider: "huawei", CloudRegion: "region-1-b", ControlPlaneSize: "m7n.large.8", WorkerSize: "m7n.xlarge.8", WorkerCount: 3, Code: "region-1-b", Zone: "region-1-b-az1", Role: "secondary", StorageClass: "golden-disks", ClusterMeshName: "hw-b"},
	}
	return r
}
