package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/core/services/shared/events"
)

// fakeSandboxClient records every Get / Create the orchestrator makes
// and lets the test stage NotFound or AlreadyExists.
type fakeSandboxClient struct {
	getReturn  *unstructured.Unstructured
	getErr     error
	createErr  error
	getCalls   []string
	createObjs []*unstructured.Unstructured
}

func (f *fakeSandboxClient) Get(_ context.Context, ns, name string) (*unstructured.Unstructured, error) {
	f.getCalls = append(f.getCalls, ns+"/"+name)
	return f.getReturn, f.getErr
}

func (f *fakeSandboxClient) Create(_ context.Context, _ string, obj *unstructured.Unstructured) error {
	f.createObjs = append(f.createObjs, obj)
	return f.createErr
}

// fakeMemberEmailLookup stands in for *store.Store for the email-lookup
// fallback path. nil error + empty result keeps the orchestrator on the
// owner_id fallback (which is the production CreateOrg path today).
type fakeMemberEmailLookup struct {
	email string
	err   error
}

func (f *fakeMemberEmailLookup) GetMemberEmail(_ context.Context, _, _ string) (string, error) {
	return f.email, f.err
}

// notFoundErr returns an apierrors.IsNotFound-compatible error so the
// orchestrator's Get-then-Create dance treats the fake as "no existing
// Sandbox, proceed to Create".
func notFoundErr() error {
	return apierrors.NewNotFound(
		schema.GroupResource{Group: "sandbox.openova.io", Resource: "sandboxes"},
		"sandbox-x",
	)
}

func mkSandboxRequestedEvent(t *testing.T, payload SandboxRequestedPayload) *events.Event {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &events.Event{
		ID:       "evt-sb-001",
		Type:     "tenant.sandbox_requested",
		TenantID: payload.TenantID,
		Data:     body,
	}
}

// TestSandboxHandleHappyPath_CreatesCR — primary contract test.
//
// A valid tenant.sandbox_requested event must:
//   - call Get exactly once (idempotency check) and proceed past NotFound,
//   - call Create exactly once with a Sandbox CR shaped per architecture §7
//     (spec.owner.email + spec.owner.orgRef.slug + spec.quota defaults +
//     spec.agentCatalogue from event.Agents),
//   - return nil so the broker acks.
func TestSandboxHandleHappyPath_CreatesCR(t *testing.T) {
	client := &fakeSandboxClient{getErr: notFoundErr()}
	o := &SandboxOrchestrator{
		Client:    client,
		Namespace: "catalyst-system",
	}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:    "tenant-1",
		OrgSlug:     "acme",
		OwnerID:     "user-uuid-1",
		OwnerEmail:  "emrah@acme.com",
		Agents:      []string{"claude-code", "cursor-agent"},
		Sovereign:   "rzk7.openova.io",
		RequestedAt: "2026-05-18T10:00:00Z",
	})

	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("handleSandboxRequested: %v", err)
	}
	if len(client.getCalls) != 1 {
		t.Fatalf("want exactly 1 Get call, got %v", client.getCalls)
	}
	if len(client.createObjs) != 1 {
		t.Fatalf("want exactly 1 Create call, got %d", len(client.createObjs))
	}
	got := client.createObjs[0]
	if got.GetKind() != "Sandbox" {
		t.Fatalf("kind want Sandbox, got %q", got.GetKind())
	}
	if got.GetAPIVersion() != "sandbox.openova.io/v1" {
		t.Fatalf("apiVersion want sandbox.openova.io/v1, got %q", got.GetAPIVersion())
	}
	if got.GetNamespace() != "catalyst-system" {
		t.Fatalf("ns want catalyst-system, got %q", got.GetNamespace())
	}
	wantName := "sandbox-emrah-at-acme-com"
	if got.GetName() != wantName {
		t.Fatalf("name want %q, got %q", wantName, got.GetName())
	}

	spec, ok := got.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("spec missing or wrong shape: %#v", got.Object["spec"])
	}
	owner, ok := spec["owner"].(map[string]any)
	if !ok {
		t.Fatalf("spec.owner missing: %#v", spec["owner"])
	}
	if owner["email"] != "emrah@acme.com" {
		t.Fatalf("spec.owner.email want emrah@acme.com, got %v", owner["email"])
	}
	orgRef, _ := owner["orgRef"].(map[string]any)
	if orgRef["slug"] != "acme" {
		t.Fatalf("spec.owner.orgRef.slug want acme, got %v", orgRef["slug"])
	}
	quota, ok := spec["quota"].(map[string]any)
	if !ok {
		t.Fatalf("spec.quota missing: %#v", spec["quota"])
	}
	if quota["cpu"] != "4" || quota["memory"] != "8Gi" || quota["storage"] != "50Gi" {
		t.Fatalf("default quota mismatch: %#v", quota)
	}
	if cs, _ := quota["concurrentSessions"].(int64); cs != 3 {
		t.Fatalf("concurrentSessions want 3, got %v (%T)", quota["concurrentSessions"], quota["concurrentSessions"])
	}
	cat, ok := spec["agentCatalogue"].([]any)
	if !ok || len(cat) != 2 || cat[0] != "claude-code" || cat[1] != "cursor-agent" {
		t.Fatalf("agentCatalogue mismatch: %#v", spec["agentCatalogue"])
	}

	if labels := got.GetLabels(); labels["openova.io/organization"] != "acme" {
		t.Fatalf("label openova.io/organization want acme, got %q", labels["openova.io/organization"])
	}
	if ann := got.GetAnnotations(); ann["openova.io/source-event"] != "tenant.sandbox_requested" {
		t.Fatalf("annotation openova.io/source-event missing: %v", ann)
	}
}

// TestSandboxHandle_IdempotentOnAlreadyExists — Get returns an existing
// CR ⇒ Create must NOT be called, return nil so the broker acks.
func TestSandboxHandle_IdempotentOnExisting(t *testing.T) {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "sandbox.openova.io", Version: "v1", Kind: "Sandbox",
	})
	existing.SetName("sandbox-emrah-at-acme-com")
	existing.SetNamespace("catalyst-system")
	existing.SetCreationTimestamp(metav1.Now())

	client := &fakeSandboxClient{getReturn: existing}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:   "tenant-1",
		OrgSlug:    "acme",
		OwnerID:    "user-uuid-1",
		OwnerEmail: "emrah@acme.com",
	})

	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("handleSandboxRequested: %v", err)
	}
	if len(client.createObjs) != 0 {
		t.Fatalf("Create must not be called when Sandbox already exists, got %d calls", len(client.createObjs))
	}
}

// TestSandboxHandle_AlreadyExistsRace — Get returns NotFound but Create
// races against another writer and returns AlreadyExists. Must be
// swallowed (return nil) so the broker acks and we don't re-loop.
func TestSandboxHandle_AlreadyExistsRace(t *testing.T) {
	client := &fakeSandboxClient{
		getErr: notFoundErr(),
		createErr: apierrors.NewAlreadyExists(
			schema.GroupResource{Group: "sandbox.openova.io", Resource: "sandboxes"},
			"sandbox-emrah-at-acme-com",
		),
	}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:   "tenant-1",
		OrgSlug:    "acme",
		OwnerID:    "user-uuid-1",
		OwnerEmail: "emrah@acme.com",
	})

	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("AlreadyExists race must be swallowed, got: %v", err)
	}
}

// TestSandboxHandle_FallsBackToOwnerIDWhenEmailMissing — the event
// shape from the current CreateOrg path does not include owner_email.
// The orchestrator must:
//   - call Members.GetMemberEmail (when wired) — fake returns "" here,
//   - fall through to using OwnerID as both name and spec.owner.email,
//   - still build a valid CR.
func TestSandboxHandle_FallsBackToOwnerIDWhenEmailMissing(t *testing.T) {
	client := &fakeSandboxClient{getErr: notFoundErr()}
	lookup := &fakeMemberEmailLookup{} // returns ""
	o := &SandboxOrchestrator{Client: client, Members: lookup}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID: "tenant-1",
		OrgSlug:  "acme",
		OwnerID:  "user-uuid-7",
	})
	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("handleSandboxRequested: %v", err)
	}
	if len(client.createObjs) != 1 {
		t.Fatalf("want one Create, got %d", len(client.createObjs))
	}
	got := client.createObjs[0]
	wantName := "sandbox-user-uuid-7"
	if got.GetName() != wantName {
		t.Fatalf("name want %q, got %q", wantName, got.GetName())
	}
	spec := got.Object["spec"].(map[string]any)
	owner := spec["owner"].(map[string]any)
	if owner["email"] != "user-uuid-7" {
		t.Fatalf("owner.email fallback should equal owner_id, got %v", owner["email"])
	}
}

// TestSandboxHandle_MissingOwnerSkips — no owner anywhere ⇒ ack-skip.
func TestSandboxHandle_MissingOwnerSkips(t *testing.T) {
	client := &fakeSandboxClient{getErr: notFoundErr()}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID: "tenant-1",
		OrgSlug:  "acme",
	})
	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("missing owner must ack-skip, got: %v", err)
	}
	if len(client.createObjs) != 0 {
		t.Fatalf("no Create should fire when owner is unknown, got %d", len(client.createObjs))
	}
}

// TestSandboxHandle_MissingOrgSlugSkips — no slug ⇒ ack-skip.
func TestSandboxHandle_MissingOrgSlugSkips(t *testing.T) {
	client := &fakeSandboxClient{getErr: notFoundErr()}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:   "tenant-1",
		OwnerID:    "user-uuid-1",
		OwnerEmail: "emrah@acme.com",
	})
	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("missing org_slug must ack-skip, got: %v", err)
	}
	if len(client.createObjs) != 0 {
		t.Fatalf("no Create should fire when org_slug is missing, got %d", len(client.createObjs))
	}
}

// TestSandboxHandle_MalformedPayloadAcks — poison pill ⇒ ack-skip, no
// Create. Matches the members_consumer convention.
func TestSandboxHandle_MalformedPayloadAcks(t *testing.T) {
	client := &fakeSandboxClient{getErr: notFoundErr()}
	o := &SandboxOrchestrator{Client: client}
	evt := &events.Event{
		ID:       "evt-bad",
		Type:     "tenant.sandbox_requested",
		TenantID: "tenant-1",
		Data:     json.RawMessage(`"not an object"`),
	}
	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("malformed payload must ack-skip, got: %v", err)
	}
	if len(client.createObjs) != 0 {
		t.Fatalf("no Create on poison pill, got %d", len(client.createObjs))
	}
}

// TestSandboxHandle_CreateErrorPropagates — a real Create error
// (anything other than AlreadyExists) must surface so the broker
// redelivers.
func TestSandboxHandle_CreateErrorPropagates(t *testing.T) {
	client := &fakeSandboxClient{
		getErr:    notFoundErr(),
		createErr: fmt.Errorf("apiserver: timeout"),
	}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:   "tenant-1",
		OrgSlug:    "acme",
		OwnerID:    "user-uuid-1",
		OwnerEmail: "emrah@acme.com",
	})
	if err := o.handleSandboxRequested(context.Background(), evt); err == nil {
		t.Fatal("expected Create error to propagate, got nil")
	}
}

// TestSandboxHandle_GetErrorPropagates — a real Get error (anything
// other than NotFound) must surface so the broker redelivers.
func TestSandboxHandle_GetErrorPropagates(t *testing.T) {
	client := &fakeSandboxClient{getErr: fmt.Errorf("apiserver: connection refused")}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:   "tenant-1",
		OrgSlug:    "acme",
		OwnerID:    "user-uuid-1",
		OwnerEmail: "emrah@acme.com",
	})
	if err := o.handleSandboxRequested(context.Background(), evt); err == nil {
		t.Fatal("expected Get error to propagate, got nil")
	}
	if len(client.createObjs) != 0 {
		t.Fatalf("no Create should fire when Get failed, got %d", len(client.createObjs))
	}
}

// TestSandboxCRName_LongEmailTruncates — name must stay within RFC 1123
// (63 chars) and not end on a hyphen.
func TestSandboxCRName_LongEmailTruncates(t *testing.T) {
	long := "supercalifragilisticexpialidocious-very-long-name@verylongtenant.openova.io"
	got := sandboxCRName(long, "")
	if len(got) > 63 {
		t.Fatalf("name length %d > 63: %q", len(got), got)
	}
	if got[len(got)-1] == '-' {
		t.Fatalf("name ends on hyphen: %q", got)
	}
	if got == "sandbox-" || got == "sandbox-user" {
		t.Fatalf("name truncation lost all entropy: %q", got)
	}
}

// TestSandboxHandle_PlanIDStampsAnnotationAndQuota — when the event
// carries a sandbox-{free,pro,ent} plan_id the orchestrator MUST:
//   - stamp openova.io/plan-id annotation on the CR (so the controller
//     can re-read the plan when the customer upgrades),
//   - stamp spec.planId on the CR (mirrored field for controller cache),
//   - derive spec.quota from the plan's hard-coded quota tier (not the
//     orchestrator's DefaultQuota).
//
// Catches the PR #1633 regression where Sandbox apps were minted but
// every CR got the Wave 1 baseline quota regardless of the customer's
// paid tier — Pro/Ent customers silently got Free resources.
func TestSandboxHandle_PlanIDStampsAnnotationAndQuota(t *testing.T) {
	cases := []struct {
		planID  string
		wantCPU string
		wantMem string
		wantStr string
		wantCS  int64
	}{
		{"sandbox-free", "500m", "1Gi", "5Gi", 1},
		{"sandbox-pro", "2", "4Gi", "50Gi", 3},
		{"sandbox-ent", "8", "16Gi", "500Gi", 0},
	}
	for _, tc := range cases {
		t.Run(tc.planID, func(t *testing.T) {
			client := &fakeSandboxClient{getErr: notFoundErr()}
			o := &SandboxOrchestrator{Client: client}
			evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
				TenantID:   "tenant-1",
				OrgSlug:    "acme",
				OwnerID:    "u-1",
				OwnerEmail: "ops@acme.com",
				PlanID:     tc.planID,
			})
			if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
				t.Fatalf("handleSandboxRequested: %v", err)
			}
			if len(client.createObjs) != 1 {
				t.Fatalf("want 1 Create, got %d", len(client.createObjs))
			}
			got := client.createObjs[0]
			if ann := got.GetAnnotations(); ann["openova.io/plan-id"] != tc.planID {
				t.Errorf("annotation openova.io/plan-id = %q, want %q", ann["openova.io/plan-id"], tc.planID)
			}
			spec, _ := got.Object["spec"].(map[string]any)
			if spec["planId"] != tc.planID {
				t.Errorf("spec.planId = %v, want %q", spec["planId"], tc.planID)
			}
			quota, _ := spec["quota"].(map[string]any)
			if quota["cpu"] != tc.wantCPU {
				t.Errorf("quota.cpu = %v, want %q", quota["cpu"], tc.wantCPU)
			}
			if quota["memory"] != tc.wantMem {
				t.Errorf("quota.memory = %v, want %q", quota["memory"], tc.wantMem)
			}
			if quota["storage"] != tc.wantStr {
				t.Errorf("quota.storage = %v, want %q", quota["storage"], tc.wantStr)
			}
			if cs, _ := quota["concurrentSessions"].(int64); cs != tc.wantCS {
				t.Errorf("quota.concurrentSessions = %v (%T), want %d", quota["concurrentSessions"], quota["concurrentSessions"], tc.wantCS)
			}
		})
	}
}

// TestSandboxHandle_PlanIDEmptyKeepsDefaultQuota — when the event has
// no plan_id (legacy publisher or pre-PR-#1633 emit) the CR MUST stick
// to the orchestrator's DefaultQuota and MUST NOT carry the plan-id
// annotation or spec.planId. Guards back-compat with already-deployed
// publishers.
func TestSandboxHandle_PlanIDEmptyKeepsDefaultQuota(t *testing.T) {
	client := &fakeSandboxClient{getErr: notFoundErr()}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:   "tenant-1",
		OrgSlug:    "acme",
		OwnerID:    "u-1",
		OwnerEmail: "ops@acme.com",
	})
	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("handleSandboxRequested: %v", err)
	}
	got := client.createObjs[0]
	if ann := got.GetAnnotations(); ann["openova.io/plan-id"] != "" {
		t.Errorf("openova.io/plan-id present without plan_id: %q", ann["openova.io/plan-id"])
	}
	spec, _ := got.Object["spec"].(map[string]any)
	if _, ok := spec["planId"]; ok {
		t.Errorf("spec.planId present without plan_id: %v", spec["planId"])
	}
	quota, _ := spec["quota"].(map[string]any)
	if quota["cpu"] != "4" || quota["memory"] != "8Gi" {
		t.Errorf("default quota changed: cpu=%v memory=%v", quota["cpu"], quota["memory"])
	}
}

// TestSandboxHandle_PlanIDUnknownFallsBackToDefault — a bogus plan_id
// (typo, removed plan, plan from a different product) must not crash
// the orchestrator. It falls through to DefaultQuota but still stamps
// the annotation so the controller can flag the mismatch in its
// status.
func TestSandboxHandle_PlanIDUnknownFallsBackToDefault(t *testing.T) {
	client := &fakeSandboxClient{getErr: notFoundErr()}
	o := &SandboxOrchestrator{Client: client}
	evt := mkSandboxRequestedEvent(t, SandboxRequestedPayload{
		TenantID:   "tenant-1",
		OrgSlug:    "acme",
		OwnerID:    "u-1",
		OwnerEmail: "ops@acme.com",
		PlanID:     "sandbox-mythical",
	})
	if err := o.handleSandboxRequested(context.Background(), evt); err != nil {
		t.Fatalf("handleSandboxRequested: %v", err)
	}
	got := client.createObjs[0]
	if ann := got.GetAnnotations(); ann["openova.io/plan-id"] != "sandbox-mythical" {
		t.Errorf("annotation should still record requested plan: %q", ann["openova.io/plan-id"])
	}
	spec, _ := got.Object["spec"].(map[string]any)
	quota, _ := spec["quota"].(map[string]any)
	if quota["cpu"] != "4" || quota["memory"] != "8Gi" {
		t.Errorf("unknown plan should fall back to default quota: cpu=%v memory=%v", quota["cpu"], quota["memory"])
	}
}

// TestSandboxCRName_PathologicalEmptyEmail — empty email + empty owner_id
// (caller's bug) collapses to the stable "sandbox-user" literal so the
// dynamic-client Create does not panic on an empty name.
func TestSandboxCRName_PathologicalEmpty(t *testing.T) {
	if got := sandboxCRName("", ""); got != "sandbox-user" {
		t.Fatalf("want sandbox-user, got %q", got)
	}
}
