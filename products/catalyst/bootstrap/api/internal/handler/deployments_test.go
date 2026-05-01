// Tests for the catalyst-api → PDM lifecycle: reserve before tofu apply,
// commit on success, release on failure. These cover the deployment-level
// path #163 introduced — the wizard creates a deployment, PDM holds the
// reservation while tofu runs, and PDM owns the eventual DNS write.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
)

func TestCreateDeployment_ManagedPoolReservesViaPDM(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	// Pool-mode deployments require a GHCR pull token (Phase 1 pulls
	// private bp-* OCI artifacts from ghcr.io/openova-io). The chart
	// mounts CATALYST_GHCR_PULL_TOKEN from the catalyst-ghcr-pull-token
	// Secret; tests inject a placeholder so Validate() does not 400.
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", "ghp_TEST_PLACEHOLDER_NOT_REAL")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"sovereignFQDN":          "omantel.omani.works",
		"sovereignDomainMode":    "pool",
		"sovereignPoolDomain":    "omani.works",
		"sovereignSubdomain":     "omantel",
		"hetznerToken":           "tok",
		"hetznerProjectID":       "proj",
		"region":                 "fsn1",
		"orgName":                "Omantel",
		"orgEmail":               "ops@omantel.om",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTACCESSKEY1234567",
		"objectStorageSecretKey": "TESTSECRETKEY1234567890123456789012345678",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)

	// 201 — deployment row created. The runProvisioning goroutine is
	// launched in a background goroutine; in this unit test the goroutine
	// will fail at tofu exec (not installed) but for this test we only
	// care that CreateDeployment reserved before launching it.
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(fake.reserves) != 1 {
		t.Fatalf("expected 1 PDM reserve, got %d", len(fake.reserves))
	}
	if fake.reserves[0].pool != "omani.works" || fake.reserves[0].sub != "omantel" {
		t.Errorf("reserve called with wrong args: %+v", fake.reserves[0])
	}
}

func TestCreateDeployment_PDMConflictBlocksDeployment(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", "ghp_TEST_PLACEHOLDER_NOT_REAL")
	pdm.ResetManagedDomains()

	fake := &fakePDM{
		reserve: func(ctx context.Context, pool, sub, by string) (*pdm.Reservation, error) {
			return nil, pdm.ErrConflict
		},
	}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"sovereignFQDN":          "omantel.omani.works",
		"sovereignDomainMode":    "pool",
		"sovereignPoolDomain":    "omani.works",
		"sovereignSubdomain":     "omantel",
		"hetznerToken":           "tok",
		"hetznerProjectID":       "proj",
		"region":                 "fsn1",
		"orgName":                "Omantel",
		"orgEmail":               "ops@omantel.om",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTACCESSKEY1234567",
		"objectStorageSecretKey": "TESTSECRETKEY1234567890123456789012345678",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (subdomain-conflict), body=%s", w.Code, w.Body.String())
	}
}

func TestCreateDeployment_BYODoesNotReserve(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"sovereignFQDN":          "k8s.acme.io",
		"sovereignDomainMode":    "byo",
		"sovereignPoolDomain":    "acme.io",
		"sovereignSubdomain":     "k8s",
		"hetznerToken":           "tok",
		"hetznerProjectID":       "proj",
		"region":                 "fsn1",
		"orgName":                "Acme",
		"orgEmail":               "ops@acme.io",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTACCESSKEY1234567",
		"objectStorageSecretKey": "TESTSECRETKEY1234567890123456789012345678",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// BYO must NOT consult PDM — the customer owns DNS.
	if len(fake.reserves) != 0 {
		t.Errorf("BYO reserved via PDM unexpectedly: %+v", fake.reserves)
	}
}

// TestCreateDeployment_DerivesObjectStorageBucketFromFQDN verifies the
// per-Sovereign Object Storage bucket name (issue #371) is derived
// deterministically from the FQDN — `catalyst-<fqdn-with-dots-replaced>`.
// The wizard never surfaces this; the handler derives it before
// Validate() runs. We assert the persisted Deployment carries the
// derived value so the OpenTofu module's `aminueza/minio` provider
// finds a non-empty bucket name when writeTfvars renders.
func TestCreateDeployment_DerivesObjectStorageBucketFromFQDN(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	body, _ := json.Marshal(map[string]any{
		"sovereignFQDN":          "k8s.acme.io",
		"sovereignDomainMode":    "byo",
		"sovereignSubdomain":     "k8s",
		"hetznerToken":           "tok",
		"hetznerProjectID":       "proj",
		"region":                 "fsn1",
		"orgName":                "Acme",
		"orgEmail":               "ops@acme.io",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTACCESSKEY1234567",
		"objectStorageSecretKey": "TESTSECRETKEY1234567890123456789012345678",
		// objectStorageBucket intentionally omitted — handler derives.
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
	const want = "catalyst-k8s-acme-io"
	if dep.Request.ObjectStorageBucket != want {
		t.Errorf("ObjectStorageBucket = %q, want %q", dep.Request.ObjectStorageBucket, want)
	}
}
