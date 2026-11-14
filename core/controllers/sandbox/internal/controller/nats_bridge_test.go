// Unit tests for the sandbox-controller NATS consume-leg bridge (D35).
//
// Mirrors organization/internal/controller/nats_bridge_test.go for
// `catalyst.tenant.sandbox_requested`. The bridge surface is the same
// signature the live JetStream subscriber drives, so these tests
// exercise the same code path the runtime uses.

package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

func newSandboxBridgeFixture(t *testing.T, ns string, objs ...runtime.Object) *NATSBridge {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgo addtoscheme: %v", err)
	}
	if err := sandboxapi.AddToScheme(scheme); err != nil {
		t.Fatalf("sandboxapi addtoscheme: %v", err)
	}
	cb := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		cb = cb.WithRuntimeObjects(objs...)
	}
	return &NATSBridge{
		Client:    cb.Build(),
		Log:       testr.New(t),
		Namespace: ns,
	}
}

// TestNATSBridge_SandboxRequested_HappyPath pins the D35 sandbox round-trip:
// an envelope with owner_email matching a real Sandbox CR results in
// both observation annotations being patched.
func TestNATSBridge_SandboxRequested_HappyPath(t *testing.T) {
	const ns = "catalyst-system"
	// tenant-service derives sandbox name as "sandbox-" + sanitised
	// email → ceo@acme.com → "sandbox-ceo-at-acme-com".
	sb := &sandboxapi.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      "sandbox-ceo-at-acme-com",
		},
	}
	bridge := newSandboxBridgeFixture(t, ns, sb)

	ts := time.Date(2026, 5, 18, 15, 0, 0, 123456789, time.UTC)
	body, _ := json.Marshal(map[string]any{
		"tenant_id":   "tnt-9",
		"org_slug":    "acme",
		"owner_id":    "u-1",
		"owner_email": "ceo@acme.com",
	})
	ev := &natsbus.Event{
		ID:        "evt-sb-1",
		Type:      "tenant.sandbox_requested",
		Source:    "tenant-service",
		Timestamp: ts,
		TenantID:  "tnt-9",
		Data:      body,
	}
	if err := bridge.HandleSandboxRequested(context.Background(), ev); err != nil {
		t.Fatalf("HandleSandboxRequested: %v", err)
	}

	var got sandboxapi.Sandbox
	if err := bridge.Client.Get(context.Background(),
		types.NamespacedName{Namespace: ns, Name: "sandbox-ceo-at-acme-com"}, &got); err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	anns := got.GetAnnotations()
	if anns[AnnotationLastNATSSubject] != natsbus.SubjectTenantSandboxRequested {
		t.Errorf("subject annotation: got %q want %q",
			anns[AnnotationLastNATSSubject], natsbus.SubjectTenantSandboxRequested)
	}
	wantObservedAt := ts.Format(time.RFC3339Nano)
	if anns[AnnotationLastNATSObservedAt] != wantObservedAt {
		t.Errorf("observed-at annotation: got %q want %q",
			anns[AnnotationLastNATSObservedAt], wantObservedAt)
	}
}

// TestNATSBridge_SandboxRequested_OwnerIDFallback pins: when owner_email
// is absent, the bridge falls back to owner_id for the CR name
// derivation. Mirrors tenant-service's sandboxCRName fallback rule
// (PR #1633) — both sides must stay in lockstep.
func TestNATSBridge_SandboxRequested_OwnerIDFallback(t *testing.T) {
	const ns = "catalyst-system"
	sb := &sandboxapi.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      "sandbox-u-1",
		},
	}
	bridge := newSandboxBridgeFixture(t, ns, sb)

	body, _ := json.Marshal(map[string]any{
		"tenant_id": "tnt-no-email",
		"owner_id":  "u-1",
	})
	ev := &natsbus.Event{
		ID:        "evt-sb-no-email",
		Type:      "tenant.sandbox_requested",
		Timestamp: time.Now().UTC(),
		Data:      body,
	}
	if err := bridge.HandleSandboxRequested(context.Background(), ev); err != nil {
		t.Fatalf("HandleSandboxRequested: %v", err)
	}

	var got sandboxapi.Sandbox
	if err := bridge.Client.Get(context.Background(),
		types.NamespacedName{Namespace: ns, Name: "sandbox-u-1"}, &got); err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if _, ok := got.GetAnnotations()[AnnotationLastNATSSubject]; !ok {
		t.Error("owner_id fallback failed — CR not annotated")
	}
}

// TestNATSBridge_SandboxRequested_NoMatchingCR pins: cold-start ordering
// (broker delivered before tenant-service finished creating the CR)
// is a soft miss — return nil so the dispatcher Acks, do not Nak.
func TestNATSBridge_SandboxRequested_NoMatchingCR(t *testing.T) {
	bridge := newSandboxBridgeFixture(t, "catalyst-system")
	body, _ := json.Marshal(map[string]any{
		"owner_email": "ghost@nowhere.io",
	})
	ev := &natsbus.Event{
		ID:        "evt-miss-sb",
		Type:      "tenant.sandbox_requested",
		Timestamp: time.Now().UTC(),
		Data:      body,
	}
	if err := bridge.HandleSandboxRequested(context.Background(), ev); err != nil {
		t.Fatalf("HandleSandboxRequested on missing CR returned error (should soft-miss): %v", err)
	}
}

// TestNATSBridge_SandboxRequested_MalformedData pins poison-pill
// behaviour: malformed JSON returns nil so the dispatcher Acks-to-skip.
func TestNATSBridge_SandboxRequested_MalformedData(t *testing.T) {
	bridge := newSandboxBridgeFixture(t, "catalyst-system")
	ev := &natsbus.Event{
		ID:        "evt-bad-sb",
		Type:      "tenant.sandbox_requested",
		Timestamp: time.Now().UTC(),
		Data:      []byte("not-json{"),
	}
	if err := bridge.HandleSandboxRequested(context.Background(), ev); err != nil {
		t.Errorf("malformed Data should not Nak, got: %v", err)
	}
}

// TestSandboxCRName_MatchesTenantServiceConvention pins the name
// derivation rules verbatim against the publish-side convention. If
// tenant-service changes its naming rule, this test goes red so the
// bridge stays in lockstep.
func TestSandboxCRName_MatchesTenantServiceConvention(t *testing.T) {
	cases := []struct {
		email, ownerID, want string
	}{
		{"ceo@acme.com", "u-1", "sandbox-ceo-at-acme-com"},
		{"", "u-99", "sandbox-u-99"},
		{"Mixed.Case+User@Globex.io", "", "sandbox-mixed-case-plus-user-at-globex-io"},
		{"", "", "sandbox-user"},
		{"a@b", "", "sandbox-a-at-b"},
	}
	for _, c := range cases {
		t.Run(c.email+"|"+c.ownerID, func(t *testing.T) {
			got := sandboxCRNameFromEvent(c.email, c.ownerID)
			if got != c.want {
				t.Errorf("sandboxCRNameFromEvent(%q,%q) = %q, want %q",
					c.email, c.ownerID, got, c.want)
			}
		})
	}
}
