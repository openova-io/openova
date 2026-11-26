package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
)

// A field error of the port (CLD-103) is a 400 with
// {"error":"invalid-field","field":…,"message":…}. An empty provider is one:
// the create handler never defaults it to a pack, and a credential the cloud
// profile marks required is another.
func TestCreateDeployment_FieldErrorIsInvalidField(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	pdm.ResetManagedDomains()
	h := NewWithPDM(slog.Default(), &fakePDM{})

	for _, tc := range []struct{ provider, field string }{{"", "provider"}, {"hetzner", "credentials.hcloud_token"}} {
		body, _ := json.Marshal(map[string]any{
			"provider":               tc.provider,
			"sovereignFQDN":          "nopro.example.om",
			"sovereignDomainMode":    "byo",
			"sovereignSubdomain":     "nopro",
			"region":                 "fsn1",
			"controlPlaneSize":       "cpx22",
			"orgName":                "Example",
			"bcpTopology":            "single-region",
			"orgEmail":               "ops@example.om",
			"sshPublicKey":           "ssh-ed25519 AAAA test",
			"objectStorageRegion":    "fsn1",
			"objectStorageAccessKey": "TESTAK1234567890",
			"objectStorageSecretKey": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		})
		w := httptest.NewRecorder()
		h.CreateDeployment(w, httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("provider %q: status=%d body=%s; want 400", tc.provider, w.Code, w.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("provider %q: body is not a JSON object: %s", tc.provider, w.Body.String())
		}
		if got["error"] != "invalid-field" || got["field"] != tc.field || got["message"] == nil || got["message"] == "" {
			t.Errorf("provider %q: body = %v, want error invalid-field on %s with a message", tc.provider, got, tc.field)
		}
	}
}

// The wizard (StepReview.tsx) sends no top-level provider: it names the
// provider in each regions[] entry. Regions that agree name the provider, so
// admission reaches the adapter (here it refuses the missing hcloud_token);
// regions that name different providers name none.
func TestCreateDeployment_ProviderFromWizardRegions(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	pdm.ResetManagedDomains()
	h := NewWithPDM(slog.Default(), &fakePDM{})

	hetzner := map[string]any{"provider": "hetzner", "cloudRegion": "fsn1", "controlPlaneSize": "cpx22", "workerSize": "cpx42", "workerCount": 1}
	huawei := map[string]any{"provider": "huawei", "cloudRegion": "me-east-215-a", "controlPlaneSize": "m7n.xlarge.8", "workerSize": "m7n.2xlarge.8", "workerCount": 3}
	for _, tc := range []struct {
		name    string
		regions []map[string]any
		field   string
	}{
		{"regions agree", []map[string]any{hetzner}, "credentials.hcloud_token"},
		{"regions differ", []map[string]any{hetzner, huawei}, "provider"},
	} {
		body, _ := json.Marshal(map[string]any{
			"sovereignFQDN":          "wizard.example.om",
			"sovereignDomainMode":    "byo",
			"sovereignSubdomain":     "wizard",
			"region":                 "fsn1",
			"controlPlaneSize":       "cpx22",
			"regions":                tc.regions,
			"orgName":                "Example",
			"bcpTopology":            "single-region",
			"orgEmail":               "ops@example.om",
			"sshPublicKey":           "ssh-ed25519 AAAA test",
			"objectStorageRegion":    "fsn1",
			"objectStorageAccessKey": "TESTAK1234567890",
			"objectStorageSecretKey": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		})
		w := httptest.NewRecorder()
		h.CreateDeployment(w, httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body)))
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: status=%d body is not a JSON object: %s", tc.name, w.Code, w.Body.String())
		}
		if w.Code != http.StatusBadRequest || got["error"] != "invalid-field" || got["field"] != tc.field {
			t.Errorf("%s: status=%d body=%v, want 400 invalid-field on %s", tc.name, w.Code, got, tc.field)
		}
	}
}

// setHuaweiOperatorConfig sets the operator's server-side configuration of a
// Huawei deployment: its credential files, region as cloud-credential.huawei.region
// ("" = a blank file, which sets nothing).
func setHuaweiOperatorConfig(t *testing.T, region string) {
	t.Helper()
	t.Setenv("CATALYST_EGRESS_CIDRS", "192.0.2.10/32")
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	creds := maps.Clone(huaweiOperator)
	creds["region"] = region
	operatorCredentialFiles(t, "huawei", creds)
	pdm.ResetManagedDomains()
}

// wizardHuaweiBody is the body StepReview.tsx posts for a two-region Huawei
// Sovereign: no top-level provider, the provider in each regions[] entry, the
// singular fields mirrored from region 1, and empty Hetzner and Object
// Storage fields. cp and worker are the flavors of both regions.
func wizardHuaweiBody(cp, worker string) []byte {
	region := func(cloudRegion string) map[string]any {
		return map[string]any{"provider": "huawei", "cloudRegion": cloudRegion, "controlPlaneSize": cp, "workerSize": worker, "workerCount": 3}
	}
	body, _ := json.Marshal(map[string]any{
		"orgName":                "Example HCS Customer",
		"orgEmail":               "ops@example.om",
		"sovereignFQDN":          "hcs.example.om",
		"sovereignDomainMode":    "byo",
		"sovereignPoolDomain":    "",
		"sovereignSubdomain":     "hcs",
		"hetznerToken":           "",
		"hetznerProjectID":       "",
		"region":                 "me-east-215-a",
		"controlPlaneSize":       cp,
		"workerSize":             worker,
		"workerCount":            3,
		"haEnabled":              false,
		"storageClass":           "",
		"regions":                []map[string]any{region("me-east-215-a"), region("me-east-215-b")},
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "",
		"objectStorageAccessKey": "",
		"objectStorageSecretKey": "",
		"marketplaceEnabled":     true,
		"marketplaceBrand":       map[string]string{"name": "", "tagline": "", "primaryColor": ""},
	})
	return body
}

// createAccepted posts body and returns the stored deployment; it fails the
// test unless the answer is 201.
func createAccepted(t *testing.T, h *Handler, body []byte) *Deployment {
	t.Helper()
	w := httptest.NewRecorder()
	h.CreateDeployment(w, httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s; want 201", w.Code, w.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	val, ok := h.deployments.Load(resp.ID)
	if !ok {
		t.Fatalf("deployment %s is not stored", resp.ID)
	}
	return val.(*Deployment)
}

// A request body never fills a server-only field: a credentials map that
// names the operator's access key or region is refused with invalid-field on
// that key, whether or not the operator's region file is set, so a body can
// never choose the region, and with it the endpoints, of the operator's keys.
func TestCreateDeployment_Huawei_BodyCannotFillServerOnlyCredentials(t *testing.T) {
	for _, envRegion := range []string{"me-east-215", ""} {
		for _, key := range []string{"region", "access_key"} {
			setHuaweiOperatorConfig(t, envRegion)
			h := NewWithPDM(slog.Default(), &fakePDM{})
			body, _ := json.Marshal(map[string]any{
				"provider":            "huawei",
				"credentials":         map[string]string{key: "attacker.example/"},
				"sovereignFQDN":       "hcs.example.om",
				"sovereignDomainMode": "byo",
				"sovereignSubdomain":  "hcs",
				"region":              "me-east-215",
				"orgName":             "Example HCS Customer",
				"bcpTopology":         "single-region",
				"orgEmail":            "ops@example.om",
				"sshPublicKey":        "ssh-ed25519 AAAA test",
			})
			w := httptest.NewRecorder()
			h.CreateDeployment(w, httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body)))
			var got map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if w.Code != http.StatusBadRequest || got["error"] != "invalid-field" || got["field"] != "credentials."+key {
				t.Errorf("credentials.%s, operator region %q: status=%d body=%s; want 400 invalid-field on credentials.%s", key, envRegion, w.Code, w.Body.String(), key)
			}
		}
	}
}

// The wizard's two-region Huawei body names the provider only in regions[]:
// it takes the path of a top-level provider, so the operator's credentials
// and region are stamped and the Object Storage keys derived from them.
func TestCreateDeployment_Huawei_WizardRegionsShape(t *testing.T) {
	setHuaweiOperatorConfig(t, "me-east-215")
	h := NewWithPDM(slog.Default(), &fakePDM{})
	dep := createAccepted(t, h, wizardHuaweiBody("m7n.xlarge.8", "m7n.2xlarge.8"))
	req := dep.Request
	if req.Provider != "huawei" || len(req.Regions) != 2 {
		t.Fatalf("Provider=%q regions=%d, want huawei with 2 regions", req.Provider, len(req.Regions))
	}
	if req.Credentials["access_key"] != "TESTAK1234567890" || req.Credentials["region"] != "me-east-215" {
		t.Errorf("operator credentials not stamped: access key %q, region %q", req.Credentials["access_key"], req.Credentials["region"])
	}
	if req.ObjectStorageAccessKey != "TESTAK1234567890" || req.ObjectStorageRegion != "me-east-215" {
		t.Errorf("Object Storage not derived from the operator credentials: access key %q, region %q", req.ObjectStorageAccessKey, req.ObjectStorageRegion)
	}
}

// Empty per-region flavors on the regions[] path are floored by the adapter
// before the per-region required checks, as the create handler floored them
// before CLD-103: control plane m7n.xlarge.8, workers m7n.2xlarge.8.
func TestCreateDeployment_Huawei_RegionsEmptyFlavorsFloored(t *testing.T) {
	setHuaweiOperatorConfig(t, "me-east-215")
	h := NewWithPDM(slog.Default(), &fakePDM{})
	dep := createAccepted(t, h, wizardHuaweiBody("", ""))
	for i, rs := range dep.Request.Regions {
		if rs.ControlPlaneSize != "m7n.xlarge.8" || rs.WorkerSize != "m7n.2xlarge.8" || rs.WorkerCount < 3 {
			t.Errorf("region[%d] = %q/%q x%d, want m7n.xlarge.8/m7n.2xlarge.8 x>=3", i, rs.ControlPlaneSize, rs.WorkerSize, rs.WorkerCount)
		}
	}
}
