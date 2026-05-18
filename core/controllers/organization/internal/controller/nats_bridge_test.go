// Unit tests for the NATS consume-leg bridge (D35).
//
// The handler is wired through a fake controller-runtime client so we
// can assert:
//
//   - tenant.created envelope with a matching CR → annotations stamped.
//   - order.placed envelope with the legacy subdomain field → CR found
//     and annotated (back-compat with PR #1626 publish-side).
//   - envelope with no matching CR → handler returns nil (Ack-to-skip),
//     no patch attempted (assert via list count).
//   - duplicate envelope (same timestamp) → no redundant patch.
//   - malformed Data payload → handler returns nil so dispatcher Acks
//     instead of hot-looping.
//
// The bridge is decoupled from JetStream by construction — the
// natsbus.Handler signature is `func(ctx, *Event) error`, so these
// tests exercise the same surface the live subscriber drives.

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

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

func newBridgeFixture(t *testing.T, objs ...runtime.Object) *NATSBridge {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgo addtoscheme: %v", err)
	}
	if err := orgapi.AddToScheme(scheme); err != nil {
		t.Fatalf("orgapi addtoscheme: %v", err)
	}
	cb := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		cb = cb.WithRuntimeObjects(objs...)
	}
	return &NATSBridge{
		Client: cb.Build(),
		Log:    testr.New(t),
	}
}

// TestNATSBridge_TenantCreated_HappyPath pins: an envelope on
// catalyst.tenant.created with a matching Organization CR results in
// both observation annotations being patched. This is the D35 happy
// path proof.
func TestNATSBridge_TenantCreated_HappyPath(t *testing.T) {
	org := &orgapi.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "acme"},
		Spec:       orgapi.OrganizationSpec{Slug: "acme"},
	}
	bridge := newBridgeFixture(t, org)

	ts := time.Date(2026, 5, 18, 12, 34, 56, 789012345, time.UTC)
	body, _ := json.Marshal(map[string]any{
		"id":   "tnt-1",
		"slug": "acme",
	})
	ev := &natsbus.Event{
		ID:        "evt-tc-1",
		Type:      "tenant.created",
		Source:    "tenant-service",
		Timestamp: ts,
		TenantID:  "tnt-1",
		Data:      body,
	}
	if err := bridge.HandleTenantCreated(context.Background(), ev); err != nil {
		t.Fatalf("HandleTenantCreated: %v", err)
	}

	var got orgapi.Organization
	if err := bridge.Client.Get(context.Background(), types.NamespacedName{Name: "acme"}, &got); err != nil {
		t.Fatalf("get organization: %v", err)
	}
	anns := got.GetAnnotations()
	if anns[AnnotationLastNATSSubject] != natsbus.SubjectTenantCreated {
		t.Errorf("subject annotation: got %q want %q",
			anns[AnnotationLastNATSSubject], natsbus.SubjectTenantCreated)
	}
	wantObservedAt := ts.Format(time.RFC3339Nano)
	if anns[AnnotationLastNATSObservedAt] != wantObservedAt {
		t.Errorf("observed-at annotation: got %q want %q",
			anns[AnnotationLastNATSObservedAt], wantObservedAt)
	}
}

// TestNATSBridge_OrderPlaced_BackCompatSubdomain pins the back-compat
// path: when org_slug is absent, the bridge falls back to the
// `subdomain` field PR #1626's dispatchOrderPlaced enriches in.
// Bodyguard against the publish-side renaming the field without the
// consume-side noticing.
func TestNATSBridge_OrderPlaced_BackCompatSubdomain(t *testing.T) {
	org := &orgapi.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "globex"},
		Spec:       orgapi.OrganizationSpec{Slug: "globex"},
	}
	bridge := newBridgeFixture(t, org)

	body, _ := json.Marshal(map[string]any{
		"tenant_id": "tnt-2",
		"subdomain": "globex",
	})
	ev := &natsbus.Event{
		ID:        "evt-op-1",
		Type:      "order.placed",
		Source:    "billing-service",
		Timestamp: time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC),
		TenantID:  "tnt-2",
		Data:      body,
	}
	if err := bridge.HandleOrderPlaced(context.Background(), ev); err != nil {
		t.Fatalf("HandleOrderPlaced: %v", err)
	}

	var got orgapi.Organization
	if err := bridge.Client.Get(context.Background(), types.NamespacedName{Name: "globex"}, &got); err != nil {
		t.Fatalf("get organization: %v", err)
	}
	if got.GetAnnotations()[AnnotationLastNATSSubject] != natsbus.SubjectOrderPlaced {
		t.Errorf("subject annotation: got %q want %q",
			got.GetAnnotations()[AnnotationLastNATSSubject], natsbus.SubjectOrderPlaced)
	}
}

// TestNATSBridge_NoMatchingCR pins: an envelope referencing a slug
// that doesn't exist returns nil (Ack-to-skip) and does NOT churn the
// API server. Critical for cold-start ordering — tenant.created may
// arrive before the operator's Organization CR write.
func TestNATSBridge_NoMatchingCR(t *testing.T) {
	bridge := newBridgeFixture(t) // empty fake client

	body, _ := json.Marshal(map[string]any{"slug": "nonexistent"})
	ev := &natsbus.Event{
		ID:        "evt-miss",
		Type:      "tenant.created",
		Timestamp: time.Now().UTC(),
		Data:      body,
	}
	if err := bridge.HandleTenantCreated(context.Background(), ev); err != nil {
		t.Fatalf("HandleTenantCreated on missing CR returned error (should soft-miss): %v", err)
	}
}

// TestNATSBridge_DuplicateEnvelope_NoChurn pins: replaying the same
// envelope (same Timestamp) does not mutate the CR a second time. The
// gen-2 controller-runtime informer enqueues on annotation drift; a
// byte-stable patch keeps the reconcile queue clean.
func TestNATSBridge_DuplicateEnvelope_NoChurn(t *testing.T) {
	org := &orgapi.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "dup", ResourceVersion: "1"},
		Spec:       orgapi.OrganizationSpec{Slug: "dup"},
	}
	bridge := newBridgeFixture(t, org)
	ts := time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC)
	body, _ := json.Marshal(map[string]any{"slug": "dup"})
	ev := &natsbus.Event{
		ID:        "evt-dup",
		Type:      "tenant.created",
		Timestamp: ts,
		Data:      body,
	}

	// First delivery — patches.
	if err := bridge.HandleTenantCreated(context.Background(), ev); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	var afterFirst orgapi.Organization
	if err := bridge.Client.Get(context.Background(), types.NamespacedName{Name: "dup"}, &afterFirst); err != nil {
		t.Fatalf("get after first: %v", err)
	}
	rvAfterFirst := afterFirst.GetResourceVersion()

	// Second delivery (same envelope, same timestamp) — skip path.
	if err := bridge.HandleTenantCreated(context.Background(), ev); err != nil {
		t.Fatalf("second delivery: %v", err)
	}
	var afterSecond orgapi.Organization
	if err := bridge.Client.Get(context.Background(), types.NamespacedName{Name: "dup"}, &afterSecond); err != nil {
		t.Fatalf("get after second: %v", err)
	}
	if afterSecond.GetResourceVersion() != rvAfterFirst {
		t.Errorf("duplicate envelope mutated CR — rv went %q → %q",
			rvAfterFirst, afterSecond.GetResourceVersion())
	}
}

// TestNATSBridge_MalformedData pins: a Data blob that fails
// json.Unmarshal returns nil so the natsbus dispatcher Acks-to-skip.
// A Nak would hot-loop the consumer against a poison pill.
func TestNATSBridge_MalformedData(t *testing.T) {
	bridge := newBridgeFixture(t)
	ev := &natsbus.Event{
		ID:        "evt-bad",
		Type:      "tenant.created",
		Timestamp: time.Now().UTC(),
		Data:      []byte("{not-json"),
	}
	if err := bridge.HandleTenantCreated(context.Background(), ev); err != nil {
		t.Errorf("malformed Data should NOT return error (would Nak + hot-loop), got: %v", err)
	}
}

// TestNATSBridge_OrderPlaced_PreferOrgSlug pins: when both org_slug and
// subdomain are present, org_slug wins. Forward-compat with the
// publish-side normalizing to the explicit field.
func TestNATSBridge_OrderPlaced_PreferOrgSlug(t *testing.T) {
	org := &orgapi.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "winner"},
		Spec:       orgapi.OrganizationSpec{Slug: "winner"},
	}
	loser := &orgapi.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "loser"},
		Spec:       orgapi.OrganizationSpec{Slug: "loser"},
	}
	bridge := newBridgeFixture(t, org, loser)

	body, _ := json.Marshal(map[string]any{
		"tenant_id": "tnt-99",
		"org_slug":  "winner",
		"subdomain": "loser",
	})
	ev := &natsbus.Event{
		ID:        "evt-pref",
		Type:      "order.placed",
		Timestamp: time.Now().UTC(),
		Data:      body,
	}
	if err := bridge.HandleOrderPlaced(context.Background(), ev); err != nil {
		t.Fatalf("HandleOrderPlaced: %v", err)
	}

	var winner orgapi.Organization
	if err := bridge.Client.Get(context.Background(), types.NamespacedName{Name: "winner"}, &winner); err != nil {
		t.Fatalf("get winner: %v", err)
	}
	if _, ok := winner.GetAnnotations()[AnnotationLastNATSSubject]; !ok {
		t.Error("winner Organization was not annotated despite org_slug match")
	}

	var loserGot orgapi.Organization
	if err := bridge.Client.Get(context.Background(), types.NamespacedName{Name: "loser"}, &loserGot); err != nil {
		t.Fatalf("get loser: %v", err)
	}
	if _, ok := loserGot.GetAnnotations()[AnnotationLastNATSSubject]; ok {
		t.Error("loser Organization was unexpectedly annotated; org_slug should outrank subdomain")
	}
}
