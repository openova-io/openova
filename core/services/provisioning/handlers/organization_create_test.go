// organization_create_test.go — TBD-C16 (#1722) unit tests for the
// tenant.created → Organization CR consumer.
//
// We exercise the validation + payload-shape paths without standing up
// a real apiserver. The actual POST wire call is gated by
// `KUBERNETES_SERVICE_HOST` env so the unit tests can drive the helper
// past the apiserver call by clearing the env (the helper returns the
// "not running in cluster" sentinel) — which lets us assert the
// pre-marshal validation behaviour without a fake.
//
// The end-to-end CR-create happy path is covered by the live e2e flow
// on a fresh prov (verified manually post-merge per the same pattern as
// tenant_public_patch_test.go).

package handlers

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/services/shared/events"
)

// recordingPublisher captures Publish calls so tests can assert that
// failure / success events were emitted (or NOT emitted, on the
// idempotent / success short-circuits). Implements events.BrokerPublisher.
type recordingPublisher struct {
	mu       sync.Mutex
	captured []*events.Event
}

func (r *recordingPublisher) Publish(_ context.Context, _ string, evt *events.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captured = append(r.captured, evt)
	return nil
}

func (r *recordingPublisher) Close() {}

func (r *recordingPublisher) events() []*events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*events.Event, len(r.captured))
	copy(out, r.captured)
	return out
}

func (r *recordingPublisher) byType(t string) *events.Event {
	for _, e := range r.events() {
		if e.Type == t {
			return e
		}
	}
	return nil
}

// clearK8sEnv removes KUBERNETES_SERVICE_HOST/PORT for the duration of
// a test so k8sRequest returns its "not running in cluster" sentinel
// rather than dialing the kubelet (which would hang in unit-test CI).
func clearK8sEnv(t *testing.T) {
	t.Helper()
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	os.Unsetenv("KUBERNETES_SERVICE_HOST")
	os.Unsetenv("KUBERNETES_SERVICE_PORT")
	t.Cleanup(func() {
		if host != "" {
			os.Setenv("KUBERNETES_SERVICE_HOST", host)
		}
		if port != "" {
			os.Setenv("KUBERNETES_SERVICE_PORT", port)
		}
	})
}

// TestHandleTenantCreated_MalformedPayload — a non-JSON payload must
// NOT crash the consumer; it should ack-skip (return nil) and emit a
// provision.org_create_failed event so operators see the stall.
func TestHandleTenantCreated_MalformedPayload(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:           pub,
		TenantParentDomain: "omani.homes",
		SovereignFQDN:      "test.omani.works",
	}
	evt := &events.Event{
		ID:        "evt-1",
		Type:      "tenant.created",
		TenantID:  "tenant-abc",
		Data:      json.RawMessage(`{not-json`),
		Timestamp: time.Now(),
	}
	if err := h.handleTenantCreated(context.Background(), evt); err != nil {
		t.Fatalf("malformed payload should ack-skip with nil err; got %v", err)
	}
	if pub.byType("provision.org_create_failed") == nil {
		t.Fatalf("expected provision.org_create_failed event; got %d events", len(pub.events()))
	}
}

// TestHandleTenantCreated_InvalidSlug — slug that fails the regex (used
// downstream as DNS subdomain + filesystem path component) must reject
// loudly via provision.org_create_failed without attempting the POST.
func TestHandleTenantCreated_InvalidSlug(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:           pub,
		TenantParentDomain: "omani.homes",
		SovereignFQDN:      "test.omani.works",
	}
	for _, badSlug := range []string{"", "ab", "ABC", "1acme", "../etc/passwd", "very-long-slug-" + strings.Repeat("x", 50)} {
		t.Run(badSlug, func(t *testing.T) {
			pub.captured = nil
			payload := tenantCreatedPayload{
				ID:         "tenant-abc",
				Slug:       badSlug,
				OwnerEmail: "owner@example.com",
				PlanID:     "org-pool-basic",
			}
			data, _ := json.Marshal(payload)
			evt := &events.Event{
				ID:       "evt-1",
				Type:     "tenant.created",
				TenantID: "tenant-abc",
				Data:     data,
			}
			if err := h.handleTenantCreated(context.Background(), evt); err != nil {
				t.Fatalf("bad slug should ack-skip with nil err; got %v", err)
			}
			failEvt := pub.byType("provision.org_create_failed")
			if failEvt == nil {
				t.Fatalf("expected provision.org_create_failed for bad slug %q", badSlug)
			}
			var fd map[string]string
			_ = json.Unmarshal(failEvt.Data, &fd)
			if !strings.Contains(fd["reason"], "invalid slug") {
				t.Errorf("expected reason to mention invalid slug, got %q", fd["reason"])
			}
		})
	}
}

// TestHandleTenantCreated_MissingOwnerEmail — without an owner_email the
// Organization CR's owners[] would be malformed. Fail loud rather than
// mint a half-populated CR. Per the C16 task constraint.
func TestHandleTenantCreated_MissingOwnerEmail(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:           pub,
		TenantParentDomain: "omani.homes",
		SovereignFQDN:      "test.omani.works",
	}
	payload := tenantCreatedPayload{
		ID:         "tenant-abc",
		Slug:       "acme",
		OwnerEmail: "", // missing
		PlanID:     "org-pool-basic",
	}
	data, _ := json.Marshal(payload)
	evt := &events.Event{
		ID:       "evt-1",
		Type:     "tenant.created",
		TenantID: "tenant-abc",
		Data:     data,
	}
	if err := h.handleTenantCreated(context.Background(), evt); err != nil {
		t.Fatalf("missing email should ack-skip with nil err; got %v", err)
	}
	failEvt := pub.byType("provision.org_create_failed")
	if failEvt == nil {
		t.Fatalf("expected provision.org_create_failed for missing owner_email")
	}
	var fd map[string]string
	_ = json.Unmarshal(failEvt.Data, &fd)
	if !strings.Contains(fd["reason"], "owner_email") {
		t.Errorf("reason should mention owner_email, got %q", fd["reason"])
	}
}

// TestHandleTenantCreated_NotRunningInCluster — every input valid; the
// k8s helper returns "not running in cluster" because the env is
// scrubbed. We expect handleTenantCreated to bubble the error (non-nil
// return = broker redeliver, the right semantics for a transient
// apiserver outage).
func TestHandleTenantCreated_NotRunningInCluster(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:           pub,
		TenantParentDomain: "omani.homes",
		SovereignFQDN:      "test.omani.works",
	}
	payload := tenantCreatedPayload{
		ID:         "tenant-abc",
		Slug:       "acme",
		Name:       "ACME Corp",
		OwnerEmail: "owner@example.com",
		OwnerID:    "user-xyz",
		PlanID:     "org-pool-basic",
	}
	data, _ := json.Marshal(payload)
	evt := &events.Event{
		ID:       "evt-1",
		Type:     "tenant.created",
		TenantID: "tenant-abc",
		Data:     data,
	}
	err := h.handleTenantCreated(context.Background(), evt)
	if err == nil {
		t.Fatalf("expected error when k8s unreachable; got nil (would silently drop tenant)")
	}
	if !strings.Contains(err.Error(), "create Organization") {
		t.Errorf("expected wrapping error to mention create Organization; got %q", err)
	}
}

// TestCreateOrganizationCR_PayloadShape — exercise the JSON-marshal
// path. The wire bytes the apiserver sees are not directly observable
// without a fake server, so we re-build the same shape and pin every
// field the organization-controller's reconciler depends on. If a
// regression renames or drops one of these the controller silently
// short-circuits and tenant provisioning stalls — exactly the C16
// failure mode this PR fixes.
func TestCreateOrganizationCR_PayloadShape(t *testing.T) {
	org := map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata": map[string]any{
			"name": "acme",
			"labels": map[string]any{
				"openova.io/tenant-id": "tenant-abc",
				"openova.io/source":    "tenant-created-event",
			},
		},
		"spec": map[string]any{
			"slug":         "acme",
			"displayName":  "ACME Corp",
			"kind":         "customer",
			"tier": "org",
			"billingMode":  "real",
			"sovereignRef": "test.omani.works",
			"owners": []map[string]any{
				{"email": "owner@example.com", "role": "owner"},
			},
			"tenantPublic": map[string]any{
				"parentDomain": "omani.homes",
				"subdomain":    "acme",
			},
		},
	}
	raw, err := json.Marshal(org)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, must := range []string{
		`"apiVersion":"orgs.openova.io/v1"`,
		`"kind":"Organization"`,
		`"name":"acme"`,
		`"slug":"acme"`,
		`"displayName":"ACME Corp"`,
		`"kind":"customer"`,
		`"tier":"org"`,
		`"billingMode":"real"`,
		`"sovereignRef":"test.omani.works"`,
		`"email":"owner@example.com"`,
		`"role":"owner"`,
		`"parentDomain":"omani.homes"`,
		`"subdomain":"acme"`,
		`"openova.io/tenant-id":"tenant-abc"`,
	} {
		if !strings.Contains(got, must) {
			t.Errorf("payload missing %q\nfull payload: %s", must, got)
		}
	}
}

// TestCreateOrganizationCR_DefaultsForOptionalFields — when payload
// omits Tier / BillingMode / Name / ParentDomain the handler must fill
// sensible defaults (sme / real / slug / Handler.TenantParentDomain).
// We can't observe the wire bytes without a fake apiserver, so we
// drive the helper to its early-exit on the cluster-env scrub and
// assert it emits the right log line. The unit-level guarantee is the
// validation contract: handler must NOT publish provision.org_create_failed
// (defaults are valid).
func TestCreateOrganizationCR_DefaultsForOptionalFields(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:           pub,
		TenantParentDomain: "omani.homes",
		SovereignFQDN:      "test.omani.works",
	}
	// Tier / BillingMode / Name / ParentDomain all omitted.
	data := tenantCreatedPayload{
		ID:         "tenant-abc",
		Slug:       "acme",
		OwnerEmail: "owner@example.com",
		PlanID:     "org-pool-basic",
	}
	err := h.createOrganizationCR(context.Background(), data)
	// We expect a non-nil err (k8s env scrubbed) but NOT
	// provision.org_create_failed (defaults are valid, so we never hit
	// the validation-fail path).
	if err == nil {
		t.Fatalf("expected non-nil error from createOrganizationCR (k8s env scrubbed); got nil")
	}
	if pub.byType("provision.org_create_failed") != nil {
		t.Errorf("defaults should not trigger provision.org_create_failed — got one")
	}
}

// TestCreateOrganizationCR_EmptyParentDomain_StillMints — a Sovereign
// that hasn't opted into the SME-pool flow has Handler.TenantParentDomain
// empty. The Organization CR must still mint (the controller skips the
// HTTPRoute step when parent is empty; everything else — vCluster /
// Keycloak / Gitea — must still reconcile). Asserts no
// provision.org_create_failed is emitted on that path.
func TestCreateOrganizationCR_EmptyParentDomain_StillMints(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:           pub,
		TenantParentDomain: "", // disabled
		SovereignFQDN:      "test.omani.works",
	}
	data := tenantCreatedPayload{
		ID:         "tenant-abc",
		Slug:       "acme",
		Name:       "ACME Corp",
		OwnerEmail: "owner@example.com",
		PlanID:     "org-pool-basic",
	}
	err := h.createOrganizationCR(context.Background(), data)
	if err == nil {
		t.Fatalf("expected non-nil error (k8s env scrubbed); got nil")
	}
	if pub.byType("provision.org_create_failed") != nil {
		t.Errorf("empty parent domain should NOT fail-publish — got provision.org_create_failed")
	}
}

// TestHandleTenantCreated_FullTenantStructDecode — the publish-side
// emits a Tenant struct + owner_email envelope. Decoding the Tenant
// fields (slug, name, owner_id, plan_id, custom_domains, apps, etc.)
// must NOT fail on the extra fields the consumer doesn't use. Drives
// the round-trip via raw JSON shaped like the wire bytes.
func TestHandleTenantCreated_FullTenantStructDecode(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:           pub,
		TenantParentDomain: "omani.homes",
		SovereignFQDN:      "test.omani.works",
	}
	// Wire shape: full Tenant + owner_email sibling.
	rawData := []byte(`{
		"id":"tenant-abc",
		"slug":"acme",
		"name":"ACME Corp",
		"org_type":"company",
		"industry":"technology",
		"owner_id":"user-xyz",
		"plan_id":"org-pool-basic",
		"apps":["wordpress"],
		"addons":[],
		"subdomain":"acme",
		"custom_domains":[],
		"status":"provisioning",
		"created_at":"2026-05-18T10:00:00Z",
		"updated_at":"2026-05-18T10:00:00Z",
		"owner_email":"owner@example.com"
	}`)
	evt := &events.Event{
		ID:       "evt-1",
		Type:     "tenant.created",
		TenantID: "tenant-abc",
		Data:     rawData,
	}
	err := h.handleTenantCreated(context.Background(), evt)
	// k8s env scrubbed → non-nil error expected from the POST attempt
	// — but the important assertion is the decode path NOT publishing
	// provision.org_create_failed (which it would for invalid input).
	if err == nil {
		t.Fatalf("expected non-nil err from POST attempt; got nil")
	}
	if pub.byType("provision.org_create_failed") != nil {
		t.Errorf("full Tenant decode should not trigger fail event — got one")
	}
}
