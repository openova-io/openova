// Sandbox orchestrator — integration tests (Wave 15).
//
// Where unit tests in sandbox_consumer_test.go drive the dispatcher
// through a hand-rolled fakeSandboxClient at the
// handleSandboxRequested() level, this file exercises the FULL
// event-flow seam end-to-end:
//
//   tenant.sandbox_requested envelope
//     → events.BrokerSubscriber (in-process fake)
//     → SandboxOrchestrator.Start (Subscribe + dispatch)
//     → SandboxClient (recording fake)
//     → Sandbox CR shape verified per architecture.md §7
//
// The fake BrokerSubscriber substitutes for events.MultiSubscriber's
// NATS + Kafka legs without standing up real brokers. Asserting on
// the CR a real sandbox-controller would Watch proves the
// orchestrator wires every field (agents, plan, quota, labels,
// annotations, sovereign) through correctly — the regression we
// want to catch when nobody has a Sovereign available to run the
// loop against.
//
// The SandboxClient interface seam (defined in sandbox_consumer.go)
// keeps production main.go free of test fakes: production wraps
// dynamic.Interface, tests inject a recording fake; both satisfy
// the same Get/Create contract. The dynamic-client integration is
// already covered by core/controllers/sandbox/internal/controller's
// envtest suite, so duplicating that here would be redundant.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/core/services/shared/events"
)

// ── recordingSandboxClient — fake apiserver that records every Get
// + Create and serves Get from a tiny in-memory index keyed by
// (namespace, name). This is the seam where the sandbox-controller
// would otherwise Watch; tests use the readback to assert on the
// CR shape exactly as the controller's informer would observe it.
//
// Distinct from the simpler fakeSandboxClient in sandbox_consumer_test.go:
// that fake stages a single canned response; THIS one is a tiny
// in-memory apiserver so a Create is observable on the next Get
// (proving idempotency under redelivery, where the second Get
// finds the CR the first delivery materialised).
type recordingSandboxClient struct {
	mu      sync.Mutex
	store   map[string]*unstructured.Unstructured // key = "ns/name"
	creates int
	gets    int
}

func newRecordingSandboxClient() *recordingSandboxClient {
	return &recordingSandboxClient{store: map[string]*unstructured.Unstructured{}}
}

func (c *recordingSandboxClient) Get(_ context.Context, ns, name string) (*unstructured.Unstructured, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if obj, ok := c.store[ns+"/"+name]; ok {
		return obj, nil
	}
	return nil, apierrors.NewNotFound(
		schema.GroupResource{Group: SandboxGVR.Group, Resource: SandboxGVR.Resource},
		name,
	)
}

func (c *recordingSandboxClient) Create(_ context.Context, ns string, obj *unstructured.Unstructured) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := ns + "/" + obj.GetName()
	if _, exists := c.store[key]; exists {
		return apierrors.NewAlreadyExists(
			schema.GroupResource{Group: SandboxGVR.Group, Resource: SandboxGVR.Resource},
			obj.GetName(),
		)
	}
	// Deep-copy so the caller's mutations after Create don't show up
	// on the readback — mirrors apiserver storage semantics.
	c.store[key] = obj.DeepCopy()
	c.creates++
	return nil
}

func (c *recordingSandboxClient) sandboxAt(ns, name string) *unstructured.Unstructured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store[ns+"/"+name]
}

func (c *recordingSandboxClient) sandboxCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.store)
}

// ── fakeBrokerSubscriber — in-process events.BrokerSubscriber.
//
// Subscribe captures the orchestrator's handler closure; Emit
// invokes it synchronously so each test drives deterministic
// delivery. Mimics MultiSubscriber's contract: handler returning
// non-nil → nak (broker would redeliver); nil → ack.

type fakeBrokerSubscriber struct {
	handler func(*events.Event) error
}

func (f *fakeBrokerSubscriber) Subscribe(_ context.Context, handler func(*events.Event) error) error {
	f.handler = handler
	// MultiSubscriber blocks until ctx cancellation; here we return
	// immediately after capturing the handler so the test driver can
	// call Emit. The orchestrator's Start exits cleanly because we
	// return nil from Subscribe.
	return nil
}

func (f *fakeBrokerSubscriber) Close() {}

func (f *fakeBrokerSubscriber) Emit(evt *events.Event) error {
	if f.handler == nil {
		return errors.New("fakeBrokerSubscriber: no handler registered — Start() must run before Emit()")
	}
	return f.handler(evt)
}

// ── Helpers ─────────────────────────────────────────────────────────

func mkRequestedEnvelope(t *testing.T, payload SandboxRequestedPayload) *events.Event {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &events.Event{
		ID:        "evt-integ-001",
		Type:      "tenant.sandbox_requested",
		Source:    "tenant-service",
		Timestamp: time.Now().UTC(),
		TenantID:  payload.TenantID,
		Data:      body,
	}
}

// ── Integration tests ───────────────────────────────────────────────

// TestSandboxIntegration_FullEventFlow_MaterialisesCR walks the
// canonical event flow end-to-end:
//
//  1. operator submits a marketplace cart with the "sandbox" product
//  2. tenant.CreateOrg publishes tenant.sandbox_requested
//  3. SandboxOrchestrator.Start consumes via the subscriber
//  4. a Sandbox CR lands in catalyst-system, fully shaped per
//     architecture.md §7
//
// Asserts on the recordingSandboxClient's readback (what the
// sandbox-controller's informer would see) prove the orchestrator
// wires agents, plan, quota, labels, AND annotations through.
func TestSandboxIntegration_FullEventFlow_MaterialisesCR(t *testing.T) {
	client := newRecordingSandboxClient()
	sub := &fakeBrokerSubscriber{}
	o := &SandboxOrchestrator{
		Client:    client,
		Namespace: "catalyst-system",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := o.Start(ctx, sub); err != nil {
		t.Fatalf("Start: %v", err)
	}

	evt := mkRequestedEnvelope(t, SandboxRequestedPayload{
		TenantID:    "tenant-omega",
		OrgSlug:     "acme",
		OwnerID:     "user-uuid-1",
		OwnerEmail:  "emrah@acme.com",
		Agents:      []string{"claude-code", "cursor-agent"},
		Sovereign:   "rzk7.openova.io",
		PlanID:      "sandbox-pro",
		RequestedAt: "2026-05-18T10:00:00Z",
	})

	if err := sub.Emit(evt); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if client.creates != 1 {
		t.Fatalf("Create call count = %d, want 1", client.creates)
	}

	got := client.sandboxAt("catalyst-system", "sandbox-emrah-at-acme-com")
	if got == nil {
		t.Fatalf("Sandbox CR not found in fake apiserver after Emit")
	}

	if got.GetAPIVersion() != "sandbox.openova.io/v1" {
		t.Errorf("apiVersion = %q, want sandbox.openova.io/v1", got.GetAPIVersion())
	}
	if got.GetKind() != "Sandbox" {
		t.Errorf("kind = %q, want Sandbox", got.GetKind())
	}

	// Labels — used by the controller's per-Org selector AND by
	// operators eyeballing kubectl get sandbox -l openova.io/organization=acme.
	labels := got.GetLabels()
	if labels["openova.io/organization"] != "acme" {
		t.Errorf("label openova.io/organization = %q, want acme", labels["openova.io/organization"])
	}
	if labels["openova.io/owner-id"] != "user-uuid-1" {
		t.Errorf("label openova.io/owner-id = %q, want user-uuid-1", labels["openova.io/owner-id"])
	}
	if labels["openova.io/managed-by"] != "tenant-service" {
		t.Errorf("label openova.io/managed-by = %q, want tenant-service", labels["openova.io/managed-by"])
	}
	if labels["openova.io/sovereign"] != "rzk7.openova.io" {
		t.Errorf("label openova.io/sovereign = %q, want rzk7.openova.io", labels["openova.io/sovereign"])
	}

	// Annotations — source-of-truth pointer back to the envelope.
	ann := got.GetAnnotations()
	if ann["openova.io/source-event"] != "tenant.sandbox_requested" {
		t.Errorf("annotation openova.io/source-event = %q", ann["openova.io/source-event"])
	}
	if ann["openova.io/source-tenant-id"] != "tenant-omega" {
		t.Errorf("annotation openova.io/source-tenant-id = %q", ann["openova.io/source-tenant-id"])
	}
	if ann["openova.io/plan-id"] != "sandbox-pro" {
		t.Errorf("annotation openova.io/plan-id = %q, want sandbox-pro", ann["openova.io/plan-id"])
	}

	// Spec — the contract the controller reconciles against.
	spec, ok := got.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("spec missing or wrong shape: %#v", got.Object["spec"])
	}
	if spec["planId"] != "sandbox-pro" {
		t.Errorf("spec.planId = %v, want sandbox-pro", spec["planId"])
	}

	owner, _ := spec["owner"].(map[string]any)
	if owner["email"] != "emrah@acme.com" {
		t.Errorf("spec.owner.email = %v, want emrah@acme.com", owner["email"])
	}
	orgRef, _ := owner["orgRef"].(map[string]any)
	if orgRef["slug"] != "acme" {
		t.Errorf("spec.owner.orgRef.slug = %v, want acme", orgRef["slug"])
	}

	// Quota — sandbox-pro tier per quotaForPlan.
	quota, _ := spec["quota"].(map[string]any)
	if quota["cpu"] != "2" || quota["memory"] != "4Gi" || quota["storage"] != "50Gi" {
		t.Errorf("spec.quota = %#v, want pro tier (2 / 4Gi / 50Gi)", quota)
	}
	cs, _ := quota["concurrentSessions"].(int64)
	if cs != 3 {
		t.Errorf("spec.quota.concurrentSessions = %v, want 3", cs)
	}

	// Agent catalogue — mirrors the cart's picker.
	agents, _ := spec["agentCatalogue"].([]any)
	if len(agents) != 2 || agents[0] != "claude-code" || agents[1] != "cursor-agent" {
		t.Errorf("spec.agentCatalogue = %#v, want [claude-code, cursor-agent]", agents)
	}
}

// TestSandboxIntegration_RedeliveryIsIdempotent — NATS JetStream's
// at-least-once contract means the broker WILL redeliver after a
// transient handler failure or a Sovereign-side restart that
// hasn't fully ack'd. The orchestrator must absorb redeliveries
// without ever overwriting a controller-tweaked CR.
//
// Drives: Emit the same envelope twice through the subscriber.
// First call goes Get(NotFound) → Create; second call goes
// Get(found) → no-op. Only one CR ends up in the fake apiserver
// and Create was only invoked once.
func TestSandboxIntegration_RedeliveryIsIdempotent(t *testing.T) {
	client := newRecordingSandboxClient()
	sub := &fakeBrokerSubscriber{}
	o := &SandboxOrchestrator{Client: client, Namespace: "catalyst-system"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := o.Start(ctx, sub); err != nil {
		t.Fatalf("Start: %v", err)
	}

	evt := mkRequestedEnvelope(t, SandboxRequestedPayload{
		TenantID:   "tenant-omega",
		OrgSlug:    "acme",
		OwnerID:    "user-uuid-1",
		OwnerEmail: "emrah@acme.com",
		Agents:     []string{"claude-code"},
		PlanID:     "sandbox-free",
	})

	// First delivery — creates.
	if err := sub.Emit(evt); err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	// Second delivery — must be a no-op.
	if err := sub.Emit(evt); err != nil {
		t.Fatalf("second Emit (redelivery): %v", err)
	}

	if client.creates != 1 {
		t.Fatalf("Create count = %d after redelivery, want 1", client.creates)
	}
	if client.sandboxCount() != 1 {
		t.Fatalf("Sandbox count = %d, want exactly 1", client.sandboxCount())
	}
	// Both deliveries must have gone through Get first (idempotency
	// check). Two Gets prove the orchestrator ALWAYS checks for an
	// existing CR before issuing Create.
	if client.gets != 2 {
		t.Fatalf("Get count = %d, want 2 (one per delivery)", client.gets)
	}
}

// TestSandboxIntegration_PlanTiersFanOutCorrectly — drives 3 envelopes
// (free / pro / ent) through the orchestrator and asserts the
// readback carries the per-tier quota verbatim. This is the
// regression that catches "every Sandbox got Free quota regardless
// of paid tier" — the bug PR #1633 originally shipped with before
// quotaForPlan landed.
func TestSandboxIntegration_PlanTiersFanOutCorrectly(t *testing.T) {
	type planExpect struct {
		planID  string
		ownerID string
		ownerEm string
		crName  string
		wantCPU string
		wantMem string
		wantStr string
		wantCS  int64
	}
	cases := []planExpect{
		{"sandbox-free", "u-free", "free@acme.com", "sandbox-free-at-acme-com", "500m", "1Gi", "5Gi", 1},
		{"sandbox-pro", "u-pro", "pro@acme.com", "sandbox-pro-at-acme-com", "2", "4Gi", "50Gi", 3},
		{"sandbox-ent", "u-ent", "ent@acme.com", "sandbox-ent-at-acme-com", "8", "16Gi", "500Gi", 0},
	}
	for _, tc := range cases {
		t.Run(tc.planID, func(t *testing.T) {
			client := newRecordingSandboxClient()
			sub := &fakeBrokerSubscriber{}
			o := &SandboxOrchestrator{Client: client, Namespace: "catalyst-system"}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := o.Start(ctx, sub); err != nil {
				t.Fatalf("Start: %v", err)
			}

			evt := mkRequestedEnvelope(t, SandboxRequestedPayload{
				TenantID:   "tenant-omega",
				OrgSlug:    "acme",
				OwnerID:    tc.ownerID,
				OwnerEmail: tc.ownerEm,
				Agents:     []string{"claude-code"},
				PlanID:     tc.planID,
			})
			if err := sub.Emit(evt); err != nil {
				t.Fatalf("Emit: %v", err)
			}

			got := client.sandboxAt("catalyst-system", tc.crName)
			if got == nil {
				t.Fatalf("%s: Sandbox CR %q not created", tc.planID, tc.crName)
			}
			spec, _ := got.Object["spec"].(map[string]any)
			quota, _ := spec["quota"].(map[string]any)
			if quota["cpu"] != tc.wantCPU {
				t.Errorf("%s: quota.cpu = %v, want %q", tc.planID, quota["cpu"], tc.wantCPU)
			}
			if quota["memory"] != tc.wantMem {
				t.Errorf("%s: quota.memory = %v, want %q", tc.planID, quota["memory"], tc.wantMem)
			}
			if quota["storage"] != tc.wantStr {
				t.Errorf("%s: quota.storage = %v, want %q", tc.planID, quota["storage"], tc.wantStr)
			}
			if cs, _ := quota["concurrentSessions"].(int64); cs != tc.wantCS {
				t.Errorf("%s: quota.concurrentSessions = %v, want %d", tc.planID, cs, tc.wantCS)
			}
			if ann := got.GetAnnotations(); ann["openova.io/plan-id"] != tc.planID {
				t.Errorf("%s: annotation openova.io/plan-id = %q", tc.planID, ann["openova.io/plan-id"])
			}
		})
	}
}

// TestSandboxIntegration_NonSandboxEventsIgnored — the subscriber may
// be configured to multiplex multiple event types onto the same
// subject (the bridge_subscriber filters at the NATS level, but
// EventTypes is operator-tweakable). The handler MUST ignore any
// envelope whose Type is not tenant.sandbox_requested without
// touching the apiserver.
func TestSandboxIntegration_NonSandboxEventsIgnored(t *testing.T) {
	client := newRecordingSandboxClient()
	sub := &fakeBrokerSubscriber{}
	o := &SandboxOrchestrator{Client: client, Namespace: "catalyst-system"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := o.Start(ctx, sub); err != nil {
		t.Fatalf("Start: %v", err)
	}

	other := &events.Event{
		ID:       "evt-other",
		Type:     "tenant.org_created",
		TenantID: "tenant-omega",
		Data:     json.RawMessage(`{"org_slug":"acme"}`),
	}
	if err := sub.Emit(other); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if client.creates != 0 {
		t.Fatalf("non-sandbox event triggered Create %d times, want 0", client.creates)
	}
	if client.gets != 0 {
		t.Fatalf("non-sandbox event triggered Get %d times, want 0", client.gets)
	}
}

// TestSandboxIntegration_AgentsEmptyStringsFilteredAtCRBoundary —
// the cart's picker MAY emit a trailing empty string when the
// operator deletes the last entry but the FE doesn't trim. The
// orchestrator must strip empties so the CR's agentCatalogue
// stays validate-able against the CRD's `items.enum` matcher
// (an empty string would always 422).
func TestSandboxIntegration_AgentsEmptyStringsFilteredAtCRBoundary(t *testing.T) {
	client := newRecordingSandboxClient()
	sub := &fakeBrokerSubscriber{}
	o := &SandboxOrchestrator{Client: client, Namespace: "catalyst-system"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := o.Start(ctx, sub); err != nil {
		t.Fatalf("Start: %v", err)
	}

	evt := mkRequestedEnvelope(t, SandboxRequestedPayload{
		TenantID:   "tenant-omega",
		OrgSlug:    "acme",
		OwnerID:    "u-1",
		OwnerEmail: "a@acme.com",
		Agents:     []string{"claude-code", "", "  ", "cursor-agent"},
	})
	if err := sub.Emit(evt); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := client.sandboxAt("catalyst-system", "sandbox-a-at-acme-com")
	if got == nil {
		t.Fatalf("Sandbox not materialised")
	}
	spec, _ := got.Object["spec"].(map[string]any)
	agents, _ := spec["agentCatalogue"].([]any)
	if len(agents) != 2 || agents[0] != "claude-code" || agents[1] != "cursor-agent" {
		t.Errorf("agentCatalogue = %#v, want only [claude-code, cursor-agent] (empties dropped)", agents)
	}
}
