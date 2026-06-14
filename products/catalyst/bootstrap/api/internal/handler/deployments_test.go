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

// CATALYST_HARBOR_ROBOT_TOKEN — issue #557 tightened
// provisioner.Validate to reject any deployment without the env-stamped
// Harbor proxy-cache robot token (Inviolable Principle #11). The
// CreateDeployment handler reads this env at request time. Tests that
// exercise the success path must set it; the negative-path tests
// (RejectsMismatchedOrgEmail / AcceptsMatchingOrgEmail at L200/L251
// already do).
func TestCreateDeployment_ManagedPoolReservesViaPDM(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	// Pool-mode deployments require a GHCR pull token (Phase 1 pulls
	// private bp-* OCI artifacts from ghcr.io/openova-io). The chart
	// mounts CATALYST_GHCR_PULL_TOKEN from the catalyst-ghcr-pull-token
	// Secret; tests inject a placeholder so Validate() does not 400.
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
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
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
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
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
// per-Sovereign Object Storage bucket name (issue #371, Fix #111) is
// derived deterministically from the (FQDN, deployment-id) pair —
// `catalyst-<fqdn-with-dots-replaced>-<8-hex>`. The wizard never
// surfaces this; the handler derives it before Validate() runs. We
// assert the persisted Deployment carries the derived value so the
// OpenTofu module's `aminueza/minio` provider finds a non-empty
// bucket name when writeTfvars renders, AND that the suffix matches
// the deployment ID's first 8 chars (proves Fix #111 collision-
// avoidance is in effect).
func TestCreateDeployment_DerivesObjectStorageBucketFromFQDN(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
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
	// Fix #111: bucket name is `catalyst-<fqdn-with-dashes>-<id-first-8>`.
	// The deployment ID is generated inside CreateDeployment via newID()
	// (16-hex random); we know what `dep.ID` is, so we can rebuild the
	// expected bucket name and assert exact equality.
	if len(dep.ID) < 8 {
		t.Fatalf("dep.ID = %q, expected >=8 hex chars from newID()", dep.ID)
	}
	want := "catalyst-k8s-acme-io-" + dep.ID[:8]
	if dep.Request.ObjectStorageBucket != want {
		t.Errorf("ObjectStorageBucket = %q, want %q (deployment-id-suffix shape per Fix #111)", dep.Request.ObjectStorageBucket, want)
	}
}

// TBD-V4 (issue #1968, 2026-05-19): a deployment POST that OMITS the
// `marketplaceEnabled` field MUST land with Request.MarketplaceEnabled=true.
// CreateDeployment pre-initialises the Request before json.Decode (the
// canonical Go pattern for default-true bool fields without a struct shape
// change), so the encoding/json package's "absent key leaves field
// untouched" semantics fall through to the pre-init value. The matching
// chart-side slot fallback `${MARKETPLACE_ENABLED:-true}` shipped in PR
// #1967; this handler-side flip closes the trace-end-to-end gap so a
// franchised Sovereign provisions marketplace-enabled out of the box.
// Test asserts both the canonical "field omitted" case (defaults true)
// AND the "wizard explicitly disables" case (false survives the decode).
func TestCreateDeployment_MarketplaceEnabledDefaultsTrue(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", "ghp_TEST_PLACEHOLDER_NOT_REAL")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")

	cases := []struct {
		name           string
		marketplaceKey bool // include the key in the body
		marketplaceVal bool // value when included
		want           bool // expected dep.Request.MarketplaceEnabled
	}{
		{name: "omitted-defaults-true", marketplaceKey: false, want: true},
		{name: "explicit-true-passes-through", marketplaceKey: true, marketplaceVal: true, want: true},
		{name: "explicit-false-wizard-opt-out-survives", marketplaceKey: true, marketplaceVal: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pdm.ResetManagedDomains()
			fake := &fakePDM{}
			h := NewWithPDM(slog.Default(), fake)

			body := map[string]any{
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
			}
			if tc.marketplaceKey {
				body["marketplaceEnabled"] = tc.marketplaceVal
			}
			raw, _ := json.Marshal(body)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(raw))
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
			if dep.Request.MarketplaceEnabled != tc.want {
				t.Fatalf("MarketplaceEnabled = %v, want %v (TBD-V4 default-flip semantics)", dep.Request.MarketplaceEnabled, tc.want)
			}
		})
	}
}

// Refs #3370 — a deployment POST that OMITS `enableSharedPostgres` MUST
// land with Request.EnableSharedPostgres=true, the founder North-Star-2
// target state. CreateDeployment pre-initialises the Request before
// json.Decode (the same default-true bool idiom as MarketplaceEnabled), so
// the "absent key leaves field untouched" semantics fall through to the
// pre-init value. This is the only path that fires the bp-postgres chart's
// shared-pg engines + their self-registered Application CRs on a fresh
// prov; without the default ON, `/api/v1/sovereign/apps` projected ZERO
// instance cards + ZERO Contexts (the #3370 surface was invisible on
// hw138). Asserts the canonical "field omitted → true" case AND the
// "explicit false → byte-identical dedicated-cluster path survives the
// decode" case.
func TestCreateDeployment_EnableSharedPostgresDefaultsTrue(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", "ghp_TEST_PLACEHOLDER_NOT_REAL")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")

	cases := []struct {
		name      string
		sharedKey bool // include enableSharedPostgres in the body
		sharedVal bool // value when included
		want      bool // expected dep.Request.EnableSharedPostgres
	}{
		{name: "omitted-defaults-true", sharedKey: false, want: true},
		{name: "explicit-true-passes-through", sharedKey: true, sharedVal: true, want: true},
		{name: "explicit-false-dedicated-cluster-survives", sharedKey: true, sharedVal: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pdm.ResetManagedDomains()
			fake := &fakePDM{}
			h := NewWithPDM(slog.Default(), fake)

			body := map[string]any{
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
			}
			if tc.sharedKey {
				body["enableSharedPostgres"] = tc.sharedVal
			}
			raw, _ := json.Marshal(body)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(raw))
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
			if dep.Request.EnableSharedPostgres != tc.want {
				t.Fatalf("EnableSharedPostgres = %v, want %v (#3370 default-flip semantics)", dep.Request.EnableSharedPostgres, tc.want)
			}
		})
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
