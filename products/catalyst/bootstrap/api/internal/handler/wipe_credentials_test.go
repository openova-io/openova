package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// CLD-111 — the wipe and the janitor resolve an existing deployment's
// credentials without naming a provider (deploymentCredentials) and read the
// provider from the record.

func huaweiAdapter(t *testing.T) providers.CloudProvider {
	t.Helper()
	cp, err := providers.Get("huawei")
	if err != nil {
		t.Fatal(err)
	}
	return cp
}

// operatorCredentialFiles points provisioner.CloudCredentialsDir, for the
// test, at a new directory with one file cloud-credential.<provider>.<key>
// per entry of creds (CLD-106).
func operatorCredentialFiles(t *testing.T, provider string, creds map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for key, value := range creds {
		if err := os.WriteFile(filepath.Join(dir, "cloud-credential."+provider+"."+key), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	was := provisioner.CloudCredentialsDir()
	provisioner.SetCloudCredentialsDir(dir)
	t.Cleanup(func() { provisioner.SetCloudCredentialsDir(was) })
}

// huaweiOperator is a complete set of the operator's Huawei credential files.
var huaweiOperator = map[string]string{"access_key": "TESTAK1234567890", "secret_key": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890", "project_id": "0a1b2c3d4e5f6789abcd", "region": "me-east-215"}

// TestDeploymentCredentials_OperatorFilesFallback_5193 — a body-less wipe
// after a Pod roll and a partial destroy (no record creds, no variables
// file) takes the operator's credential files, and no region is defaulted in
// the handler: the adapter owns its default region.
func TestDeploymentCredentials_OperatorFilesFallback_5193(t *testing.T) {
	operatorCredentialFiles(t, "huawei", map[string]string{"access_key": "AK-OP", "secret_key": "SK-OP", "project_id": "PID-OP"})
	got, legacy := deploymentCredentials(huaweiAdapter(t), "huawei", nil, nil, provisioner.Request{}, t.TempDir())
	if got["access_key"] != "AK-OP" || got["secret_key"] != "SK-OP" || got["project_id"] != "PID-OP" {
		t.Fatalf("the operator's files did not fill: %v", got)
	}
	if got["region"] != "" || legacy {
		t.Fatalf("region = %q legacy = %v, want no handler default and no legacy field", got["region"], legacy)
	}
	if fe := missingCredential("huawei", "", got, false); fe != nil {
		t.Fatalf("complete operator creds reported missing %s", fe.Field)
	}
}

// TestDeploymentCredentials_NotFromTheEnvironment_CLD106 — CATALYST_HUAWEI_*
// is no source any more: set, with no credential file, it fills nothing.
func TestDeploymentCredentials_NotFromTheEnvironment_CLD106(t *testing.T) {
	operatorCredentialFiles(t, "huawei", nil)
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "AK-ENV")
	if got, _ := deploymentCredentials(huaweiAdapter(t), "huawei", nil, nil, provisioner.Request{}, t.TempDir()); got["access_key"] != "" {
		t.Fatalf("access_key = %q from the environment; want none without a credential file", got["access_key"])
	}
}

// TestDeploymentCredentials_BodyWinsOverTheOperatorFiles_5193 — a key of the
// body's credentials map wins over the operator's configuration; only the keys
// it leaves out are filled.
func TestDeploymentCredentials_BodyWinsOverTheOperatorFiles_5193(t *testing.T) {
	operatorCredentialFiles(t, "huawei", map[string]string{"access_key": "AK-OP", "secret_key": "SK-OP"})
	got, _ := deploymentCredentials(huaweiAdapter(t), "huawei", map[string]string{"access_key": " AK-BODY "}, nil, provisioner.Request{}, "")
	if got["access_key"] != "AK-BODY" || got["secret_key"] != "SK-OP" {
		t.Fatalf("got %v, want the body's access_key and the operator file's secret_key", got)
	}
}

// TestDeploymentCredentials_VariablesFileAfterPodRoll_5135 — the record's
// secrets are redacted after a Pod roll; the adapter's own variables file on
// the PVC supplies them, region included, and wins over the operator's files.
func TestDeploymentCredentials_VariablesFileAfterPodRoll_5135(t *testing.T) {
	operatorCredentialFiles(t, "huawei", map[string]string{"access_key": "AK-OP-MUST-NOT-WIN"})
	work := t.TempDir()
	tf := `{"huawei_access_key":"AK-FILE","huawei_secret_key":"SK-FILE","huawei_project_id":"PID-FILE","huawei_region":"region-file","obs_bucket_name":"b"}`
	if err := os.WriteFile(filepath.Join(work, "tofu.auto.tfvars.json"), []byte(tf), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := provisioner.Request{Provider: "huawei", Credentials: map[string]string{
		"access_key": provisioner.RedactedMarker, "secret_key": provisioner.RedactedMarker,
		"project_id": provisioner.RedactedMarker, "region": provisioner.RedactedMarker,
	}}
	got, _ := deploymentCredentials(huaweiAdapter(t), "huawei", nil, nil, rec, work)
	want := map[string]string{"access_key": "AK-FILE", "secret_key": "SK-FILE", "project_id": "PID-FILE", "region": "region-file"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["bucket"]; ok || len(got) != len(want) {
		t.Errorf("credentials %v carry a key no credential variable holds", got)
	}
}

// TestDeploymentCredentials_RecordFirst_NoFile — the in-memory record wins
// over the variables file, and a missing or unreadable file yields nothing.
func TestDeploymentCredentials_RecordFirst_NoFile(t *testing.T) {
	operatorCredentialFiles(t, "huawei", nil)
	rec := provisioner.Request{Provider: "huawei", Credentials: map[string]string{"access_key": "AK-REC", "secret_key": "SK-REC", "project_id": "PID-REC"}}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "tofu.auto.tfvars.json"), []byte(`{"huawei_access_key":"AK-FILE"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := deploymentCredentials(huaweiAdapter(t), "huawei", nil, nil, rec, work); got["access_key"] != "AK-REC" {
		t.Fatalf("access_key = %q, want the record's", got["access_key"])
	}
	if got := workdirCredentials(huaweiAdapter(t), []string{"access_key"}, t.TempDir()); len(got) != 0 {
		t.Fatalf("a missing variables file yielded %v", got)
	}
	if err := os.WriteFile(filepath.Join(work, "tofu.auto.tfvars.json"), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := workdirCredentials(huaweiAdapter(t), []string{"access_key"}, work); len(got) != 0 {
		t.Fatalf("an unreadable variables file yielded %v", got)
	}
}

// TestDeploymentCredentials_NoHuaweiCredentialFromALegacyField — #2144:
// the Huawei adapter names no legacy alias, so neither the old typed fields
// nor the hetznerToken overload reach its credentials; a Hetzner body's
// legacy token does, and is reported as legacy.
func TestDeploymentCredentials_NoHuaweiCredentialFromALegacyField(t *testing.T) {
	operatorCredentialFiles(t, "huawei", nil)
	fields := map[string]json.RawMessage{
		"huaweiAccessKey": json.RawMessage(`"AK-TYPED"`),
		"hetznerToken":    json.RawMessage(`"TOKEN"`),
	}
	got, legacy := deploymentCredentials(huaweiAdapter(t), "huawei", nil, fields, provisioner.Request{}, "")
	if got["access_key"] != "" || legacy {
		t.Fatalf("a legacy field reached the Huawei credentials: %v legacy=%v", got, legacy)
	}
	hz, err := providers.Get("hetzner")
	if err != nil {
		t.Fatal(err)
	}
	got, legacy = deploymentCredentials(hz, "hetzner", nil, fields, provisioner.Request{}, "")
	if got["hcloud_token"] != "TOKEN" || !legacy {
		t.Fatalf("hetzner: %v legacy=%v, want hcloud_token from hetznerToken and legacy", got, legacy)
	}
}

// TestMissingCredential — the fields the profile marks required; nothing
// when a legacy field was used or no profile can be read.
func TestMissingCredential(t *testing.T) {
	if fe := missingCredential("huawei", "", map[string]string{"access_key": "a", "project_id": "p"}, false); fe == nil || fe.Field != "secret_key" {
		t.Fatalf("got %v, want secret_key", fe)
	}
	if fe := missingCredential("huawei", "", map[string]string{}, true); fe != nil {
		t.Fatalf("a legacy body was checked for required fields: %v", fe)
	}
	if fe := missingCredential("no-such-provider", "", map[string]string{}, false); fe != nil {
		t.Fatalf("a provider without a profile reported %v", fe)
	}
}

// ── the wipe reads the provider from the record and passes ProtectedIDs ──

const stubCLD111Name = "stub-cld111"

var (
	stubCLD111Mu   sync.Mutex
	stubCLD111Spec *providers.WipeSpec
)

type stubCLD111 struct{}

func (stubCLD111) Name() string { return stubCLD111Name }
func (stubCLD111) Normalize(*providers.ProvisionSpec, *cloudprofile.Profile) error {
	return nil
}
func (stubCLD111) TfVars(providers.ProvisionSpec, *cloudprofile.Profile) (map[string]any, error) {
	return map[string]any{}, nil
}
func (stubCLD111) Wipe(_ context.Context, spec providers.WipeSpec, _ func(string)) (*providers.WipeResult, error) {
	stubCLD111Mu.Lock()
	defer stubCLD111Mu.Unlock()
	stubCLD111Spec = &spec
	return &providers.WipeResult{}, nil
}
func (stubCLD111) ListServers(context.Context, string, string, providers.ProviderCreds) ([]providers.ServerInfo, error) {
	return nil, nil
}
func (stubCLD111) ValidateCreds(context.Context, providers.ProviderCreds) error { return nil }

func init() { providers.RegisterProvider(stubCLD111Name, stubCLD111{}) }

// TestWipeDeployment_ProviderFromTheRecordAndNeighboursProtected — the
// wipe dispatches to the adapter of the record's provider, with the body's
// credentials, and protects every other live deployment's prefix (the
// adapter's post-destroy volume step leaves those alone).
func TestWipeDeployment_ProviderFromTheRecordAndNeighboursProtected(t *testing.T) {
	t.Setenv("CATALYST_TOFU_WORKDIR", t.TempDir())
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := &Deployment{ID: "wipe0001cafe0000", Status: "failed", StartedAt: time.Now().Add(-time.Hour),
		Request: provisioner.Request{SovereignFQDN: "cld111.omani.works", SovereignDomainMode: "pool",
			Regions: []provisioner.RegionSpec{{Provider: stubCLD111Name}}}}
	h.deployments.Store(dep.ID, dep)
	h.deployments.Store("live0001beef0000", &Deployment{ID: "live0001beef0000", Status: "ready"})

	w := callWipeDeployment(h, dep.ID, "", `{"credentials":{"token":"from-the-body"}}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}
	waitForWipeDone(t, dep, 30*time.Second)
	stubCLD111Mu.Lock()
	spec := stubCLD111Spec
	stubCLD111Mu.Unlock()
	if spec == nil {
		t.Fatal("the adapter of the record's provider was not called")
	}
	if spec.Creds.Get("token") != "from-the-body" {
		t.Errorf("creds = %v, want the body's token", spec.Creds.Raw)
	}
	if _, ok := spec.ProtectedIDs["live0001"]; !ok {
		t.Errorf("ProtectedIDs = %v, want the live neighbour live0001", spec.ProtectedIDs)
	}
}

// TestWipeDeployment_ProviderUnknown — a record that names no provider is
// refused, never defaulted to a provider, and keeps its status.
func TestWipeDeployment_ProviderUnknown(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := &Deployment{ID: "noprov01cafe0000", Status: "failed", StartedAt: time.Now().Add(-time.Hour),
		Request: provisioner.Request{SovereignFQDN: "noprov.omani.works"}}
	h.deployments.Store(dep.ID, dep)
	w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-hetzner-token"}`)
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadRequest || body["field"] != "provider" {
		t.Fatalf("status=%d body=%s, want 400 on field provider", w.Code, w.Body.String())
	}
	if dep.Status != "failed" {
		t.Errorf("status = %q, want failed", dep.Status)
	}
}
