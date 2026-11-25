// deployments_huawei_dispatch_test.go — Wave 4 (Refs #2140) handler-level
// coverage for the Huawei provider dispatch path.
//
// Background: Wave 3 (PR #2142) shipped the Huawei adapter behind the
// providers.CloudProvider seam, but a POST /api/v1/deployments with
// provider=huawei lacked a wire-facing place to carry AK/SK +
// project_id. Wave 4 adds the Provider field + Huawei credential
// triplet to provisioner.Request; this file asserts the
// CreateDeployment handler accepts the new shape end-to-end:
//
//   1. POST with provider=huawei + AK/SK/project_id returns 201 and
//      persists the deployment with Provider="huawei".
//   2. The auto-derived ObjectStorageBucket lands in the expected
//      `catalyst-<fqdn-dashed>-<id-prefix>` shape (the Huawei adapter
//      implements providers.ObjectStorageNamer with the same naming
//      contract as the Hetzner adapter).
//   3. POST with provider=huawei but missing huaweiAccessKey returns
//      400 with the offending field name in the error message.

package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// TestCreateDeployment_Huawei_DispatchesToHuaweiAdapter — happy path:
// POST body with provider=huawei (NO Huawei creds in body) + the four files
// cloud-credential.huawei.* returns 201, the persisted deployment
// carries Provider="huawei" + Huawei creds stamped from them, and the
// auto-derived ObjectStorageBucket uses the Huawei adapter's
// BucketNameForDeployment.
//
// History: PR #2143 (Wave 4) v1 of this test passed AK/SK in body.
// PR #2144 (Wave 4.5) moved Huawei creds to server-side env-var stamp
// (matching every other operator credential — Dynadot/GHCR/Harbor/
// PowerDNS/PDM). The body now carries deployment intent only; since CLD-106
// the creds are files the Secrets Store CSI driver mounts from OpenBao.
func TestCreateDeployment_Huawei_DispatchesToHuaweiAdapter(t *testing.T) {
	t.Setenv("CATALYST_EGRESS_CIDRS", "192.0.2.10/32")
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	operatorCredentialFiles(t, "huawei", huaweiOperator)
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"provider":      "huawei",
		"sovereignFQDN": "hcs.example.om",
		// BYO domain mode — Huawei deployments today provision against
		// operator-owned domains; pool-mode is the Hetzner-managed
		// .omani.works case that doesn't apply to HCS.
		"sovereignDomainMode": "byo",
		"sovereignSubdomain":  "hcs",
		// NOTE: NO huaweiAccessKey / huaweiSecretKey / huaweiProjectID /
		// huaweiRegion in body. These are json:"-" since PR #2144 —
		// stamped from the operator's files at CreateDeployment time. The wizard
		// frontend should not send them either.
		"region":       "me-east-215",
		"orgName":      "Example HCS Customer",
		"bcpTopology": "single-region", // #4706 — implicit 1-region is rejected
		"orgEmail":     "ops@example.om",
		"sshPublicKey": "ssh-ed25519 AAAA test",
		// Object Storage credentials — Huawei OBS uses S3-compatible
		// AK/SK; today these are still wizard-supplied. Wave 6 may
		// move them to env-var stamping too.
		"objectStorageRegion":    "me-east-215",
		"objectStorageAccessKey": "TESTAK1234567890",
		"objectStorageSecretKey": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	val, ok := h.deployments.Load(resp.ID)
	if !ok {
		t.Fatalf("deployment %s missing from sync.Map", resp.ID)
	}
	dep := val.(*Deployment)

	if dep.Request.Provider != "huawei" {
		t.Errorf("Request.Provider = %q, want %q", dep.Request.Provider, "huawei")
	}
	if dep.Request.Credentials["access_key"] != "TESTAK1234567890" {
		t.Errorf("Request.HuaweiAccessKey = %q, file stamp failed", dep.Request.Credentials["access_key"])
	}
	if dep.Request.Credentials["secret_key"] != huaweiOperator["secret_key"] {
		t.Errorf("Request.HuaweiSecretKey = %q after the file stamp", dep.Request.Credentials["secret_key"])
	}
	if dep.Request.Credentials["project_id"] != huaweiOperator["project_id"] {
		t.Errorf("Request.HuaweiProjectID = %q after the file stamp", dep.Request.Credentials["project_id"])
	}
	if dep.Request.Credentials["region"] != "me-east-215" {
		t.Errorf("Request.HuaweiRegion = %q, want me-east-215", dep.Request.Credentials["region"])
	}

	// Bucket derivation — Huawei adapter implements
	// providers.ObjectStorageNamer with the same naming contract as
	// Hetzner: catalyst-<fqdn-dashed>-<id-first-8>. The handler now
	// dispatches off req.Provider so this lands on the Huawei impl.
	if len(dep.ID) < 8 {
		t.Fatalf("dep.ID = %q, expected >=8 hex chars from newID()", dep.ID)
	}
	want := "catalyst-hcs-example-om-" + dep.ID[:8]
	if dep.Request.ObjectStorageBucket != want {
		t.Errorf("ObjectStorageBucket = %q, want %q",
			dep.Request.ObjectStorageBucket, want)
	}

	// Sanity — Hetzner-specific credentials must NOT be required when
	// provider=huawei. The dep.Request must carry them empty.
	if dep.Request.Credentials["hcloud_token"] != "" {
		t.Errorf("Huawei deployment carries non-empty HetznerToken: %q",
			dep.Request.Credentials["hcloud_token"])
	}
}

// TestCreateDeployment_Huawei_EnforcesResourceFloor — #4055 regression.
// hw182 shipped with m7n.large.8 (2vCPU/16GB) for BOTH the control plane
// AND all 5 workers and wedged on "0/6 nodes available: Insufficient
// cpu": bp-catalyst-platform's post-install hook timed out, the install
// oscillated, and the console never scheduled. The old guard only
// rewrote the deprecated s7n.large.4/empty, so a 2vCPU m7n.large.8
// (the CP flavor wrongly sent for workers) slipped through un-bumped.
// The handler must now floor undersized Huawei flavors UP:
// control-plane >= m7n.xlarge.8 (4vCPU/32GB), workers >= m7n.2xlarge.8
// (8vCPU/64GB).
func TestCreateDeployment_Huawei_EnforcesResourceFloor(t *testing.T) {
	t.Setenv("CATALYST_EGRESS_CIDRS", "192.0.2.10/32")
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	operatorCredentialFiles(t, "huawei", huaweiOperator)
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	// Reproduce hw182's undersized request verbatim: 2vCPU m7n.large.8
	// for BOTH roles. The handler must floor them up.
	body, _ := json.Marshal(map[string]any{
		"provider":               "huawei",
		"sovereignFQDN":          "hcs.example.om",
		"sovereignDomainMode":    "byo",
		"sovereignSubdomain":     "hcs",
		"region":                 "me-east-215",
		"orgName":                "Example HCS Customer",
		"bcpTopology": "single-region", // #4706 — implicit 1-region is rejected
		"orgEmail":               "ops@example.om",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"controlPlaneSize":       "m7n.large.8",
		"workerSize":             "m7n.large.8",
		"workerCount":            5,
		"objectStorageRegion":    "me-east-215",
		"objectStorageAccessKey": "TESTAK1234567890",
		"objectStorageSecretKey": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	val, ok := h.deployments.Load(resp.ID)
	if !ok {
		t.Fatalf("deployment %s missing from sync.Map", resp.ID)
	}
	dep := val.(*Deployment)

	if dep.Request.ControlPlaneSize != "m7n.xlarge.8" {
		t.Errorf("ControlPlaneSize = %q, want m7n.xlarge.8 (2vCPU m7n.large.8 must floor to 4vCPU)",
			dep.Request.ControlPlaneSize)
	}
	if dep.Request.WorkerSize != "m7n.2xlarge.8" {
		t.Errorf("WorkerSize = %q, want m7n.2xlarge.8 (2vCPU m7n.large.8 must floor to 8vCPU)",
			dep.Request.WorkerSize)
	}
}

// TestCreateDeployment_Huawei_MissingAccessKey_Rejected — defensive
// check: provider=huawei with NO file cloud-credential.huawei.access_key
// (the retired variable CATALYST_HUAWEI_ACCESS_KEY set) must reject at /api/v1/deployments POST with
// 400. The wizard payload no longer carries Huawei creds (json:"-"
// since PR #2144), so the empty-creds case translates to "operator
// did not mount the credential" — fail-fast
// with a clear error rather than crashing 5 min into tofu apply.
func TestCreateDeployment_Huawei_MissingAccessKey_Rejected(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	// The access key file intentionally absent; the other 3 files
	// exist to ensure the missing-AK is the specific cause of 400.
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "TESTAK1234567890")
	operatorCredentialFiles(t, "huawei", map[string]string{"secret_key": huaweiOperator["secret_key"], "project_id": huaweiOperator["project_id"], "region": "me-east-215"})
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"provider":               "huawei",
		"sovereignFQDN":          "hcs.example.om",
		"sovereignDomainMode":    "byo",
		"sovereignSubdomain":     "hcs",
		"region":                 "me-east-215",
		"orgName":                "Example",
		"bcpTopology": "single-region", // #4706 — implicit 1-region is rejected at admission
		"orgEmail":               "ops@example.om",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "me-east-215",
		"objectStorageAccessKey": "TESTAK1234567890",
		"objectStorageSecretKey": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"field":"credentials.access_key"`) {
		t.Errorf("error body must name the offending field; got %s", w.Body.String())
	}
}

// TestCreateDeployment_Huawei_UnknownProviderRejected — defensive
// check: a body with a bogus provider name (typo, future provider
// not yet implemented) rejects with 400 + the supported-providers
// list so the operator can fix the payload.
func TestCreateDeployment_Huawei_UnknownProviderRejected(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"provider":               "alibaba", // not registered
		"sovereignFQDN":          "alicloud.example.om",
		"sovereignDomainMode":    "byo",
		"region":                 "me-east-215",
		"orgName":                "Example",
		"bcpTopology": "single-region", // #4706 — implicit 1-region is rejected at admission
		"orgEmail":               "ops@example.om",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTAK1234567890",
		"objectStorageSecretKey": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unsupported provider") {
		t.Errorf("error body must mention 'unsupported provider'; got %s", w.Body.String())
	}
}

// TestCreateDeployment_Huawei_MisnamedCredentialFile_Refused — CLD-106: a
// file cloud-credential.huawei without a key is refused with 500 and named,
// before anything is provisioned with the other files.
func TestCreateDeployment_Huawei_MisnamedCredentialFile_Refused(t *testing.T) {
	setHuaweiOperatorConfig(t, "me-east-215")
	if err := os.WriteFile(filepath.Join(provisioner.CloudCredentialsDir(), "cloud-credential.huawei"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewWithPDM(slog.Default(), &fakePDM{}).CreateDeployment(w, httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(wizardHuaweiBody("m7n.xlarge.8", "m7n.2xlarge.8"))))
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != http.StatusInternalServerError || got["error"] != "operator-credentials-unreadable" || !strings.Contains(got["message"], "file cloud-credential.huawei is not named") {
		t.Fatalf("status=%d body=%s; want 500 operator-credentials-unreadable naming file cloud-credential.huawei", w.Code, w.Body.String())
	}
}
