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
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
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

// Issue #748 — orgEmail must match the authenticated session. A signed-in
// operator who tries to POST a deployment whose req.OrgEmail belongs to
// some OTHER identity must receive 403, NEVER 201. The session header
// (X-User-Email) stands in for the session JWT in tests; production
// auth.RequireSession middleware populates both the header AND the
// context Claims, and the handler treats the context Claims as canonical
// with the header as a fallback for off-prod / CI shapes.
func TestCreateDeployment_RejectsMismatchedOrgEmail(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", "ghp_TEST_PLACEHOLDER_NOT_REAL")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
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
		"orgEmail":               "other@example.com", // ← differs from session
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTACCESSKEY1234567",
		"objectStorageSecretKey": "TESTSECRETKEY1234567890123456789012345678",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	r.Header.Set("X-User-Email", "me@example.com")
	h.CreateDeployment(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 (mismatched orgEmail), body=%s", w.Code, w.Body.String())
	}
	// PDM must not have been touched — the rejection is BEFORE the
	// reservation step.
	if len(fake.reserves) != 0 {
		t.Errorf("PDM Reserve should not have been called on mismatch: %+v", fake.reserves)
	}
	// No deployment row should have been stored.
	count := 0
	h.deployments.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("deployment row should not have been stored on mismatch (got %d rows)", count)
	}
}

// Issue #748 — happy path. A signed-in operator submitting a deployment
// whose req.OrgEmail matches the session.email proceeds normally (201
// Created). Case-insensitive comparison: the wizard pre-fills from the
// session whoami response which Keycloak emits canonical lowercase, but
// older test fixtures use mixed-case so the comparison must be EqualFold.
func TestCreateDeployment_AcceptsMatchingOrgEmail(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", "ghp_TEST_PLACEHOLDER_NOT_REAL")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
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
		"orgEmail":               "Me@Example.com", // mixed-case match
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTACCESSKEY1234567",
		"objectStorageSecretKey": "TESTSECRETKEY1234567890123456789012345678",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	r.Header.Set("X-User-Email", "me@example.com")
	h.CreateDeployment(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (matching orgEmail), body=%s", w.Code, w.Body.String())
	}
	// PDM Reserve should have been called — happy path proceeded.
	if len(fake.reserves) != 1 {
		t.Errorf("PDM Reserve should have been called once on match (got %d)", len(fake.reserves))
	}
	// Deployment row should have OwnerEmail stamped from the session.
	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	val, ok := h.deployments.Load(resp.ID)
	if !ok {
		t.Fatalf("deployment %s missing from sync.Map", resp.ID)
	}
	dep := val.(*Deployment)
	if dep.OwnerEmail != "me@example.com" {
		t.Errorf("OwnerEmail = %q, want %q (session-derived, not request-derived)", dep.OwnerEmail, "me@example.com")
	}
}

// Issue #748 — list endpoint defaults to filtering by session.email.
// Two deployments owned by different operators are seeded; a session
// for operator A must see ONLY their own row. The other operator's
// row must not leak through.
func TestListDeployments_FiltersByOwnerSession(t *testing.T) {
	h := &Handler{log: slog.Default()}

	// Seed two deployments owned by different operators.
	mine := &Deployment{
		ID:         "mine-1",
		Status:     "ready",
		Request:    provisioner.Request{OrgEmail: "me@example.com", SovereignFQDN: "mine.omani.works"},
		StartedAt:  time.Now(),
		eventsCh:   make(chan provisioner.Event),
		done:       make(chan struct{}),
		OwnerEmail: "me@example.com",
	}
	close(mine.eventsCh)
	close(mine.done)
	h.deployments.Store(mine.ID, mine)

	theirs := &Deployment{
		ID:         "theirs-1",
		Status:     "ready",
		Request:    provisioner.Request{OrgEmail: "other@example.com", SovereignFQDN: "theirs.omani.works"},
		StartedAt:  time.Now(),
		eventsCh:   make(chan provisioner.Event),
		done:       make(chan struct{}),
		OwnerEmail: "other@example.com",
	}
	close(theirs.eventsCh)
	close(theirs.done)
	h.deployments.Store(theirs.ID, theirs)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil)
	r.Header.Set("X-User-Email", "me@example.com")
	h.ListDeployments(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Deployments) != 1 {
		t.Fatalf("got %d deployments, want exactly 1 (only the session-owner's)", len(resp.Deployments))
	}
	if resp.Deployments[0]["id"] != "mine-1" {
		t.Errorf("returned deployment id = %v, want %q", resp.Deployments[0]["id"], "mine-1")
	}
}

// Issue #748 — ?owner=<other-email> while session is me@example.com
// must return an empty list. We chose "200 + empty" rather than 403
// because the response shape itself must NOT differentiate "exists but
// not yours" from "doesn't exist" (mirrors the issue #689 404-not-403
// rule for /deployments/{id}). Returning 403 would let a hostile caller
// probe whether any other tenant has provisioned anything.
func TestListDeployments_OwnerQueryParam(t *testing.T) {
	h := &Handler{log: slog.Default()}

	// Seed a deployment owned by "other@example.com" so the test would
	// FAIL if cross-tenant access were ever allowed.
	theirs := &Deployment{
		ID:         "theirs-1",
		Status:     "ready",
		Request:    provisioner.Request{OrgEmail: "other@example.com", SovereignFQDN: "theirs.omani.works"},
		StartedAt:  time.Now(),
		eventsCh:   make(chan provisioner.Event),
		done:       make(chan struct{}),
		OwnerEmail: "other@example.com",
	}
	close(theirs.eventsCh)
	close(theirs.done)
	h.deployments.Store(theirs.ID, theirs)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments?owner=other@example.com", nil)
	r.Header.Set("X-User-Email", "me@example.com")
	h.ListDeployments(w, r)

	// 200 with empty list — never leak existence via response code.
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (empty list), body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Deployments []map[string]any `json:"deployments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Deployments) != 0 {
		t.Fatalf("got %d deployments, want 0 (cross-tenant ?owner must never leak)", len(resp.Deployments))
	}
}
