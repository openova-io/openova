// tfvars_adapter_test.go — work package CLD-104: writeTfvars writes the
// contract variables and the engine extras of module contract version 1, and
// every variable that belongs to one cloud comes from that cloud's adapter
// (TfVars). The engine runs against the port double here; the two real
// adapters are tested end to end in ./tfvarsgolden.

package provisioner

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// cloudVariables are the variables the engine wrote for one cloud before
// CLD-104. None of them may come from the engine.
var cloudVariables = []string{"hcloud_token", "hcloud_project_id", "huawei_access_key", "huawei_secret_key", "huawei_project_id", "huawei_region", "obs_bucket_name"}

// An adapter that returns no variable gets none: the engine itself writes no
// variable of one cloud, whatever the request's credentials map holds.
func TestWriteTfvars_WritesNoCloudVariable(t *testing.T) {
	for _, req := range []Request{validBase(), huaweiValidBase()} {
		t.Run(req.Provider, func(t *testing.T) {
			req.SetCredential("region", "eu-test-9")
			got := readTfvarsOf(t, req)
			for _, key := range cloudVariables {
				if v, ok := got[key]; ok {
					t.Errorf("the engine wrote %s = %v", key, v)
				}
			}
		})
	}
}

// The variables the adapter's TfVars returns are written, and TfVars gets
// the spec that the adapter's Normalize left.
func TestWriteTfvars_WritesTheAdapterVariables(t *testing.T) {
	withDouble(t, oneCloudRegion, func(spec providers.ProvisionSpec) (map[string]any, error) {
		return map[string]any{"double_token": spec.Creds.Get("access_key"), "double_region": spec.Regions[0].CloudRegion}, nil
	})
	req := huaweiValidBase()
	req.SetCredential("region", "eu-test-9")
	got := readTfvarsOf(t, req)
	if got["double_token"] != "TESTAK1234567890" || got["double_region"] != "eu-test-9" {
		t.Errorf("adapter variables: double_token %v, double_region %v", got["double_token"], got["double_region"])
	}
}

// A variable of the adapter that the engine writes, or that names a common
// variable or an engine extra of the contract, fails the write with
// providers.ErrTfVarCollision naming the key; nothing is written.
func TestWriteTfvars_AdapterVariableCollision(t *testing.T) {
	for _, key := range []string{"regions", "provider", "k3s_version", "enable_fail2ban", "dynadot_key"} {
		t.Run(key, func(t *testing.T) {
			withDouble(t, nil, func(providers.ProvisionSpec) (map[string]any, error) {
				return map[string]any{"double_token": "t", key: "from-the-adapter"}, nil
			})
			dir := t.TempDir()
			err := writeTfvars(dir, validBase())
			if !errors.Is(err, providers.ErrTfVarCollision) || !strings.Contains(err.Error(), key) {
				t.Fatalf("writeTfvars: got %v, want ErrTfVarCollision on %s", err, key)
			}
			if entries, _ := readDirNames(dir); len(entries) != 0 {
				t.Errorf("a refused write left %v", entries)
			}
		})
	}
}

// An error of TfVars fails the write.
func TestWriteTfvars_AdapterVariablesError(t *testing.T) {
	withDouble(t, nil, func(providers.ProvisionSpec) (map[string]any, error) {
		return nil, errors.New("no variables")
	})
	if err := writeTfvars(t.TempDir(), validBase()); err == nil || !strings.Contains(err.Error(), "no variables") {
		t.Fatalf("writeTfvars: got %v, want the adapter's error", err)
	}
}

// formerCloudParentDomainsYAML is the rendering of parent_domains_yaml that
// the engine's cloud branch wrote over its own value before CLD-104.
func formerCloudParentDomainsYAML(pds []ParentDomain) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i, pd := range pds {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("{name: \"" + pd.Name + "\", role: \"" + pd.Role + "\"}")
	}
	sb.WriteString("]")
	return sb.String()
}

// The override of parent_domains_yaml is deleted because both renderings
// decode, as the module's yamldecode does, to the same list for the parent
// domains of the golden request (CLD-104 step 3).
func TestParentDomainsYAML_SameListAsTheFormerCloudRendering(t *testing.T) {
	pds := []ParentDomain{
		{Name: "example.com", Role: ParentDomainRolePrimary, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-com"},
		{Name: "orgs.example.net", Role: ParentDomainRoleOrgPool, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-net"},
	}
	var engine, former []map[string]string
	if err := yaml.Unmarshal([]byte(parentDomainsYAMLLiteral(pds)), &engine); err != nil {
		t.Fatalf("decode the engine's rendering: %v", err)
	}
	if err := yaml.Unmarshal([]byte(formerCloudParentDomainsYAML(pds)), &former); err != nil {
		t.Fatalf("decode the former rendering: %v", err)
	}
	want := []map[string]string{{"name": "example.com", "role": "primary"}, {"name": "orgs.example.net", "role": "org-pool"}}
	if !reflect.DeepEqual(engine, want) || !reflect.DeepEqual(former, want) {
		t.Fatalf("decoded: engine %v, former %v, want %v", engine, former, want)
	}
}

// readTfvarsOf runs writeTfvars for req and returns the variables written.
func readTfvarsOf(t *testing.T, req Request) map[string]any {
	t.Helper()
	dir := t.TempDir()
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	return readTfvars(t, dir)
}

// readDirNames returns the names of the entries of dir.
func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, err
}
