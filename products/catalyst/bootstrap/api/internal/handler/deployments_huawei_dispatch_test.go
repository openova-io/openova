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
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
)

// TestCreateDeployment_Huawei_DispatchesToHuaweiAdapter — happy path:
// POST body with provider=huawei + complete AK/SK/project_id triplet
// returns 201, the persisted deployment record carries
// Provider="huawei", and the auto-derived ObjectStorageBucket uses the
// Huawei adapter's BucketNameForDeployment (which today shares the
// same naming contract as the Hetzner adapter — both implement
// providers.ObjectStorageNamer).
func TestCreateDeployment_Huawei_DispatchesToHuaweiAdapter(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"provider":         "huawei",
		"sovereignFQDN":    "hcs.example.om",
		// BYO domain mode — Huawei deployments today provision against
		// operator-owned domains; pool-mode is the Hetzner-managed
		// .omani.works case that doesn't apply to HCS.
		"sovereignDomainMode": "byo",
		"sovereignSubdomain":  "hcs",
		// Huawei credentials — required by Validate() when
		// provider=huawei (Wave 4 wire-schema additions).
		"huaweiAccessKey": "TESTAK1234567890",
		"huaweiSecretKey": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		"huaweiProjectID": "0a1b2c3d4e5f6789abcd",
		"huaweiRegion":    "me-east-215",
		"region":          "me-east-215",
		"orgName":         "Example HCS Customer",
		"orgEmail":        "ops@example.om",
		"sshPublicKey":    "ssh-ed25519 AAAA test",
		// Object Storage credentials — Huawei OBS uses S3-compatible
		// AK/SK; the operator supplies the same AK/SK pair as the IAM
		// triplet today. Wave 5 may split these per the OBS-specific
		// IAM separation.
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
	if dep.Request.HuaweiAccessKey == "" {
		t.Errorf("Request.HuaweiAccessKey lost during decode/persist")
	}
	if dep.Request.HuaweiSecretKey == "" {
		t.Errorf("Request.HuaweiSecretKey lost during decode/persist")
	}
	if dep.Request.HuaweiProjectID == "" {
		t.Errorf("Request.HuaweiProjectID lost during decode/persist")
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
	if dep.Request.HetznerToken != "" {
		t.Errorf("Huawei deployment carries non-empty HetznerToken: %q",
			dep.Request.HetznerToken)
	}
}

// TestCreateDeployment_Huawei_MissingAccessKey_Rejected — defensive
// check: a body with provider=huawei but an empty huaweiAccessKey
// must reject at /api/v1/deployments POST with 400 (not propagate
// through to runProvisioning and fail several minutes later).
func TestCreateDeployment_Huawei_MissingAccessKey_Rejected(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"provider":               "huawei",
		"sovereignFQDN":          "hcs.example.om",
		"sovereignDomainMode":    "byo",
		"sovereignSubdomain":     "hcs",
		"huaweiAccessKey":        "", // intentionally empty
		"huaweiSecretKey":        "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		"huaweiProjectID":        "0a1b2c3d4e5f6789abcd",
		"region":                 "me-east-215",
		"orgName":                "Example",
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
	if !strings.Contains(w.Body.String(), "huaweiAccessKey") {
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
