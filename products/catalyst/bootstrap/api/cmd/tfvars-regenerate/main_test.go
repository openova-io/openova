package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Validate reads the cloud profile of a request's provider; the tests read
// the profiles of this repository.
func init() {
	provisioner.ProvidersDir = filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers")
}

// pinEnv makes the test independent of the machine: every CATALYST_ variable
// is empty except the five that writeTfvars reads.
func pinEnv(t *testing.T) {
	t.Helper()
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
}

// request is a deployment request with every optional field set, the
// credentials among them. Every value is made up. TestRebuildGivesBackTheRequest
// fails when a field of the request is left unset here.
func request(provider string) provisioner.Request {
	addedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	off := false
	r := provisioner.Request{
		OrgName: "Regenerate Org", OrgEmail: "owner@example.com",
		SovereignFQDN: "rg.example.com", SovereignDomainMode: "pool",
		SovereignPoolDomain: "example.com", SovereignSubdomain: "rg",
		SovereignTimeZone: "Asia/Muscat",
		MailRelay:         &provisioner.MailRelay{Host: "smtp.example.org", Port: "2525", From: "noreply@example.org"},
		ParentDomains: []provisioner.ParentDomain{
			{Name: "example.com", Role: provisioner.ParentDomainRolePrimary, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-com", AddedAt: addedAt},
			{Name: "orgs.example.net", Role: provisioner.ParentDomainRoleOrgPool, RegistrarKind: "dynadot", RegistrarCredsRef: "registrar-example-net", AddedAt: addedAt},
		},
		Provider:  provider,
		HAEnabled: true, MarketplaceEnabled: true, EnableSharedPostgres: true, QATestEnabled: true,
		AdminCIDRs:          []string{"192.0.2.0/28"},
		QAFixturesNamespace: "fixtures-custom", QAOrganization: "org-custom",
		SSHPublicKey:  "ssh-ed25519 AAAAregeneratetestkey owner@example.com",
		DynadotAPIKey: "test-dynadot-key", DynadotAPISecret: "test-dynadot-secret",
		GHCRPullToken: "test-ghcr-pull-token", HarborRobotToken: "test-harbor-robot-token",
		PowerDNSAPIKey: "test-powerdns-api-key", PDMBasicAuthUser: "test-pdm-user", PDMBasicAuthPass: "test-pdm-pass",
		DeploymentID: "0123456789abcdef", KubeconfigBearerToken: "test-kubeconfig-bearer-token",
		HandoverJWTPublicKey:   `{"kty":"RSA","n":"dGVzdA","e":"AQAB"}`,
		ObjectStorageAccessKey: "test-object-storage-access-key", ObjectStorageSecretKey: "test-object-storage-secret-key",
		ObjectStorageBucket: "rg-bucket", BcpTopology: "active-active",
		ConsoleIsolationEnabled: &off, FireCutoverOnHandover: &off,
		ClusterMeshName: "mesh-custom", ClusterMeshID: 9, GitopsBranch: "rc/20260102T030405Z-0123abc",
	}
	switch provider {
	case "huawei":
		r.Credentials = map[string]string{"access_key": "test-huawei-access-key", "secret_key": "test-huawei-secret-key", "project_id": "test-huawei-project", "region": "region-1"}
		r.ObjectStorageRegion = "region-1"
		r.StorageClass = "csi-disk-ssd"
		r.Regions = []provisioner.RegionSpec{
			{Provider: "huawei", CloudRegion: "region-1-a", ControlPlaneSize: "m7n.large.8", WorkerSize: "m7n.xlarge.8", WorkerCount: 4, Code: "region-1-a", Role: "primary"},
			{Provider: "huawei", CloudRegion: "region-1-b", ControlPlaneSize: "m7n.large.8", WorkerSize: "m7n.xlarge.8", WorkerCount: 3, Code: "region-1-b", Role: "secondary"},
		}
	default:
		// The cloud profile of hetzner allows one region and a token of 64 characters or more.
		r.BcpTopology = provisioner.BcpTopologySingleRegion
		r.Credentials = map[string]string{"hcloud_token": "test-hetzner-token-" + strings.Repeat("0", 64), "hcloud_project_id": "test-hetzner-project"}
		r.ObjectStorageRegion = "fsn1"
		r.StorageClass = "hcloud-volumes"
		r.Regions = []provisioner.RegionSpec{
			{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cx43", WorkerSize: "cx53", WorkerCount: 3, Code: "fsn1", Role: "primary"},
		}
	}
	return r
}

// deployment writes what the engine leaves for req: the variables file in a
// working directory and the record in a deployments directory. It returns the
// record's path and the working directory.
func deployment(t *testing.T, req provisioner.Request) (string, string) {
	t.Helper()
	if err := req.Validate(); err != nil {
		t.Fatalf("the test request is not valid: %v", err)
	}
	work := t.TempDir()
	if err := provisioner.WriteTfvars(work, req); err != nil {
		t.Fatalf("WriteTfvars: %v", err)
	}
	raw, err := json.Marshal(store.Record{ID: req.DeploymentID, Status: "ready", Request: store.Redact(req)})
	if err != nil {
		t.Fatalf("encode the record: %v", err)
	}
	recordPath := filepath.Join(t.TempDir(), req.DeploymentID+".json")
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatalf("write the record: %v", err)
	}
	return recordPath, work
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// TestRoundTrip: for a deployment whose kept file the engine wrote, the
// command writes the same file again and reports no difference. It fails when
// a field that a record does not hold is missing from type kept or is not
// found as a credential. The probe file leaves nothing behind.
func TestRoundTrip(t *testing.T) {
	pinEnv(t)
	for _, provider := range []string{"huawei", "hetzner"} {
		t.Run(provider, func(t *testing.T) {
			recordPath, work := deployment(t, request(provider))
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			want := readFile(t, filepath.Join(work, tfvarsName))
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-record", recordPath, "-dir", work}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
			}
			if got := readFile(t, filepath.Join(work, tfvarsName)); !bytes.Equal(got, want) {
				t.Errorf("the file written again differs from the kept file; reported: %q", stdout.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("reported differences for an unchanged deployment: %q", stdout.String())
			}
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Errorf("the probe left %d entries in the temporary directory", len(left))
			}
		})
	}
}

// TestCredentialKeys: the probe finds the key of every credential that the
// variables file of each provider holds, and no key of a field that the
// record holds.
func TestCredentialKeys(t *testing.T) {
	pinEnv(t)
	common := []string{"dynadot_key", "dynadot_secret", "powerdns_api_key", "ghcr_pull_token", "harbor_robot_token",
		"pdm_basic_auth_user", "pdm_basic_auth_pass", "kubeconfig_bearer_token", "handover_jwt_public_key",
		"object_storage_access_key", "object_storage_secret_key"}
	for provider, own := range map[string][]string{
		"huawei":  {"huawei_access_key", "huawei_secret_key", "huawei_project_id", "huawei_region"},
		"hetzner": {"hcloud_token", "hcloud_project_id"},
	} {
		t.Run(provider, func(t *testing.T) {
			req := request(provider)
			if err := req.Validate(); err != nil {
				t.Fatalf("the test request is not valid: %v", err)
			}
			keys, err := credentialKeys(req)
			if err != nil {
				t.Fatalf("credentialKeys: %v", err)
			}
			found := map[string]bool{}
			for _, key := range keys {
				found[key] = true
			}
			for _, key := range append(append([]string{}, common...), own...) {
				if !found[key] {
					t.Errorf("no credential is read from %s; found %v", key, keys)
				}
			}
			for _, key := range []string{"org_email", "sovereign_fqdn", "deployment_id", "bcp_topology", "gitops_branch"} {
				if found[key] {
					t.Errorf("%s is read as a credential", key)
				}
			}
		})
	}
}

// TestEveryKeptFieldMatters: for a record of an older engine, blanking any
// one field of the kept file that the command reads (a field of type kept or
// a credential that the file holds) changes the file written again, so none
// of them is dead and TestRoundTrip_RecordOfAnOlderEngine really covers each
// one.
func TestEveryKeptFieldMatters(t *testing.T) {
	pinEnv(t)
	req := request("huawei")
	recordPath, work := deployment(t, req)
	olderRecord(t, recordPath)
	path := filepath.Join(work, tfvarsName)
	original := readFile(t, path)
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		t.Fatalf("decode the kept file: %v", err)
	}
	keys := map[string]any{}
	raw, _ := json.Marshal(kept{})
	_ = json.Unmarshal(raw, &keys)
	_ = req.Validate()
	creds, err := credentialKeys(req)
	if err != nil {
		t.Fatalf("credentialKeys: %v", err)
	}
	for _, key := range creds {
		// A credential that this file leaves empty cannot change the result
		// (the hetzner round trip covers its token), so only the others are tried.
		if s, _ := doc[key].(string); s != "" {
			keys[key] = nil
		}
	}
	for key := range keys {
		t.Run(key, func(t *testing.T) {
			changed := map[string]any{}
			for k, v := range doc {
				changed[k] = v
			}
			if key == "variables" {
				vars := map[string]any{}
				for k, v := range doc["variables"].(map[string]any) {
					vars[k] = v
				}
				vars["HANDOVER_AUTO"] = "true"
				changed[key] = vars
			} else {
				delete(changed, key)
			}
			body, _ := json.Marshal(changed)
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			// The field matters when the engine refuses the request without it (exit
			// code 3) or writes another file.
			var stdout, stderr bytes.Buffer
			code := run([]string{"-record", recordPath, "-dir", work}, &stdout, &stderr)
			if code != 0 && code != 3 {
				t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
			}
			if code == 0 && bytes.Equal(readFile(t, path), original) {
				t.Errorf("without %s in the kept file the command still wrote the original file", key)
			}
		})
	}
}

// TestRecordCredentialsMapIsNotRead: the record keeps the keys of the
// credentials map with redaction markers for values. A credential that the
// kept file does not hold is missing from the request, never the marker: the
// engine refuses the request without it.
func TestRecordCredentialsMapIsNotRead(t *testing.T) {
	pinEnv(t)
	recordPath, work := deployment(t, request("hetzner"))
	var rec store.Record
	if err := json.Unmarshal(readFile(t, recordPath), &rec); err != nil {
		t.Fatalf("decode the record: %v", err)
	}
	marker := rec.Request.Credentials["object_storage_access_key"]
	if marker == "" || marker == request("hetzner").ObjectStorageAccessKey {
		t.Fatalf("the record does not hold the redacted credentials map this test is about: %q", marker)
	}
	path := filepath.Join(work, tfvarsName)
	var doc map[string]any
	if err := json.Unmarshal(readFile(t, path), &doc); err != nil {
		t.Fatalf("decode the kept file: %v", err)
	}
	delete(doc, "object_storage_access_key")
	body, _ := json.Marshal(doc)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-record", recordPath, "-dir", work}, &stdout, &stderr); code != 3 {
		t.Errorf("exit code %d, want 3 (the engine refuses a request without the key); stderr: %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(readFile(t, path), &got); err != nil {
		t.Fatalf("decode the file written again: %v", err)
	}
	for key, value := range got {
		if value == marker {
			t.Errorf("the file written again carries the record's redaction marker under %s", key)
		}
	}
}

// TestReportsWhatTheRecordChanges: a record that differs from the kept file
// gives a file with the record's value and names the key, not its value.
func TestReportsWhatTheRecordChanges(t *testing.T) {
	pinEnv(t)
	req := request("huawei")
	_, work := deployment(t, req)
	req.OrgEmail = "other@example.com"
	recordPath, _ := deployment(t, req)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-record", recordPath, "-dir", work}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "changed org_email\n") || strings.Contains(got, "other@example.com") {
		t.Errorf("want the line \"changed org_email\" and no value, got %q", got)
	}
	if !strings.Contains(string(readFile(t, filepath.Join(work, tfvarsName))), "other@example.com") {
		t.Errorf("the file written again does not carry the record's value")
	}
}

// TestRefusals: what the command cannot read is exit code 2, what the engine
// refuses is exit code 3, and neither is ever a written file.
func TestRefusals(t *testing.T) {
	pinEnv(t)
	recordPath, work := deployment(t, request("huawei"))
	empty := t.TempDir()
	badRecord := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badRecord, []byte(`{"id": "", "request": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := request("huawei")
	refused.Regions[0].CloudRegion = ""
	refusedRecord := filepath.Join(t.TempDir(), "refused.json")
	raw, _ := json.Marshal(store.Record{ID: "fedcba9876543210", Request: store.Redact(refused)})
	if err := os.WriteFile(refusedRecord, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args []string
		code int
	}{
		{"no arguments", nil, 2},
		{"no directory", []string{"-record", recordPath}, 2},
		{"a stray argument", []string{"-record", recordPath, "-dir", work, "x"}, 2},
		{"no record file", []string{"-record", filepath.Join(empty, "none.json"), "-dir", work}, 2},
		{"a record without an id", []string{"-record", badRecord, "-dir", work}, 2},
		{"no kept file", []string{"-record", recordPath, "-dir", empty}, 2},
		{"a request the engine refuses", []string{"-record", refusedRecord, "-dir", work}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(c.args, &stdout, &stderr); code != c.code {
				t.Errorf("exit code %d, want %d; stderr: %s", code, c.code, stderr.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(empty, tfvarsName)); err == nil {
		t.Errorf("a refused run wrote a variables file")
	}
}

// TestRoundTrip_RecordWithoutCredentialsMap: a record written before the
// request had a credentials map names no credential key. The keys come from
// the cloud profile and the operator's own keys, so the command still writes
// the same file again (CLD-104).
func TestRoundTrip_RecordWithoutCredentialsMap(t *testing.T) {
	pinEnv(t)
	for _, provider := range []string{"huawei", "hetzner"} {
		t.Run(provider, func(t *testing.T) {
			recordPath, work := deployment(t, request(provider))
			var rec map[string]any
			if err := json.Unmarshal(readFile(t, recordPath), &rec); err != nil {
				t.Fatalf("decode the record: %v", err)
			}
			delete(rec["request"].(map[string]any), "credentials")
			raw, _ := json.Marshal(rec)
			if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
				t.Fatalf("write the record: %v", err)
			}
			want := readFile(t, filepath.Join(work, tfvarsName))
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-record", recordPath, "-dir", work}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
			}
			if got := readFile(t, filepath.Join(work, tfvarsName)); !bytes.Equal(got, want) {
				t.Errorf("the file written again differs from the kept file; reported: %q", stdout.String())
			}
		})
	}
}
