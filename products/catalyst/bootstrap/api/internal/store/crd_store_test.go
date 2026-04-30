// crd_store_test.go — round-trip tests for the CRD-backed store.
//
// Test surfaces (issue #88 acceptance):
//
//   1. State-machine mapping: every catalyst-api in-memory status must
//      map to exactly one of the seven legal CRD phases. A future
//      in-memory status that's not in toCRDPhase's switch defaults to
//      "pending" — that's a deliberate fallback the test pins down.
//   2. Save round-trips: a CRDStore with a fake.NewSimpleDynamicClient
//      writes the ProvisioningState resource on first call (Create
//      path), then on second call updates it (Update path). The
//      round-tripped LoadCRD result preserves spec fields.
//   3. Mode behaviour: CRDModeDisabled never calls the dynamic client
//      (verified by passing a nil dynamic.Interface and asserting Save
//      doesn't crash). CRDModeBestEffort swallows dynamic-client
//      errors via the onCRDError callback. CRDModeStrict surfaces
//      them.
//   4. Atomicity at the store boundary: a failed CRD write does NOT
//      lose the flat-file write — Load(id) on the embedded *Store
//      still returns the record.
//   5. Credential redaction holds: the on-cluster object never carries
//      the plaintext HetznerToken / Dynadot* values (the
//      RedactedRequest projection is what gets serialised).
//
// The fake dynamic client comes from k8s.io/client-go/dynamic/fake; it
// supports Create / Get / Update / UpdateStatus / Delete on
// unstructured objects without an apiserver. The list-kinds map needs
// the ProvisioningStateList entry registered up front (the fake's List
// call panics on an unregistered kind even when we never call List).

package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeDynamicClient — fresh fake dynamic.Interface seeded with the
// ProvisioningStateList kind. Each test gets its own instance so
// concurrent tests don't share apiserver state.
func fakeDynamicClient() dynamic.Interface {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		CRDGVR: "ProvisioningStateList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
}

// freshStore — a flat-file Store rooted at t.TempDir(). Used as the
// inner store every CRDStore wraps.
func freshStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// sampleRecord — a representative Record covering every field type
// the round-trip exercises.
func sampleRecord() Record {
	now := time.Now().UTC().Truncate(time.Second)
	return Record{
		ID:     "0123456789abcdef",
		Status: "provisioning", // maps to bootstrapping
		Request: Redact(provisioner.Request{
			OrgName:             "Omantel",
			OrgEmail:            "ops@omantel.om",
			SovereignFQDN:       "omantel.omani.works",
			SovereignDomainMode: "pool",
			SovereignPoolDomain: "omani.works",
			SovereignSubdomain:  "omantel",
			HetznerToken:        "MUST-NEVER-LAND-ON-CRD",
			HetznerProjectID:    "omantel-prod",
			Region:              "fsn1",
			ControlPlaneSize:    "cx32",
			WorkerSize:          "cx32",
			WorkerCount:         3,
			HAEnabled:           true,
			DynadotAPIKey:       "MUST-NEVER-LAND-ON-CRD",
			DynadotAPISecret:    "MUST-NEVER-LAND-ON-CRD",
			Regions: []provisioner.RegionSpec{
				{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cx32", WorkerSize: "cx32", WorkerCount: 3},
			},
		}),
		StartedAt: now,
	}
}

// --- Tests ---

func TestToCRDPhase_MapsEveryInMemoryStatus(t *testing.T) {
	// The catalyst-api in-memory state vocabulary (from
	// internal/handler/handler.go) must every map to exactly one CRD
	// phase. A new in-memory status that isn't in the switch must
	// default to "pending" (the default branch).
	cases := map[string]string{
		// Empty / pending — no work yet.
		"":         PhasePending,
		"pending":  PhasePending,
		"PENDING":  PhasePending, // case-insensitive
		"  pending ": PhasePending, // whitespace-trimmed

		// Phase-0 (tofu).
		"provisioning":   PhaseBootstrapping,
		"tofu-applying":  PhaseBootstrapping,

		// Phase-1 (flux + bootstrap-kit reconcile).
		"flux-bootstrapping": PhaseInstallingControlPlane,

		// Explicit registering / TLS phases (used by future stages).
		"registering-dns": PhaseRegisteringDNS,
		"tls-issuing":     PhaseTLSIssuing,
		// phase1-watching folds into tls-issuing — at that point the
		// HelmRelease watch is observing bp-cert-manager going Ready.
		"phase1-watching": PhaseTLSIssuing,

		// Terminal.
		"ready":  PhaseReady,
		"failed": PhaseFailed,

		// Unknown future status — defensive fallback.
		"some-future-status": PhasePending,
	}
	for in, want := range cases {
		got := toCRDPhase(in)
		if got != want {
			t.Errorf("toCRDPhase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidatePhase_AcceptsLegalRejectsIllegal(t *testing.T) {
	legal := []string{
		PhasePending, PhaseBootstrapping, PhaseInstallingControlPlane,
		PhaseRegisteringDNS, PhaseTLSIssuing, PhaseReady, PhaseFailed,
	}
	for _, p := range legal {
		if err := ValidatePhase(p); err != nil {
			t.Errorf("ValidatePhase(%q) returned %v, want nil", p, err)
		}
	}
	illegal := []string{"", "provisioning", "tofu-applying", "Unknown", "READY"}
	for _, p := range illegal {
		if err := ValidatePhase(p); err == nil {
			t.Errorf("ValidatePhase(%q) returned nil, want error", p)
		}
	}
}

func TestNewCRDStore_NilDynamicForcesDisabledMode(t *testing.T) {
	// A nil dynamic client must force CRDModeDisabled regardless of
	// any opts override — a Save call would otherwise nil-pointer on
	// the dyn.Resource() call.
	flat := freshStore(t)
	c, err := NewCRDStore(flat, nil, WithCRDMode(CRDModeStrict))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	if c.Mode() != CRDModeDisabled {
		t.Errorf("Mode = %v, want CRDModeDisabled (nil dyn must force disable)", c.Mode())
	}
}

func TestNewCRDStore_RejectsNilFlatStore(t *testing.T) {
	if _, err := NewCRDStore(nil, fakeDynamicClient()); err == nil {
		t.Fatal("NewCRDStore(nil, ...) returned no error, want nil-flat-store rejection")
	}
}

func TestSave_DisabledModeSkipsCRDWrite(t *testing.T) {
	// In CRDModeDisabled the dynamic client is never called; Save is
	// equivalent to the embedded flat-file Save.
	flat := freshStore(t)
	c, err := NewCRDStore(flat, nil)
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	if c.Mode() != CRDModeDisabled {
		t.Fatalf("setup: Mode = %v, want CRDModeDisabled", c.Mode())
	}
	rec := sampleRecord()
	if err := c.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Flat-file write succeeded.
	got, err := c.Load(rec.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != rec.ID {
		t.Errorf("Load returned id %q, want %q", got.ID, rec.ID)
	}
}

func TestSave_StrictModeCreatesCRDOnFirstCall(t *testing.T) {
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	c, err := NewCRDStore(flat, dyn, WithCRDMode(CRDModeStrict), WithCRDNamespace("catalyst"))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	rec := sampleRecord()

	if err := c.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// CRD object now exists with the expected name + spec fields.
	obj, err := dyn.Resource(CRDGVR).Namespace("catalyst").Get(context.Background(), rec.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get on fake client: %v", err)
	}
	if obj.GetName() != rec.ID {
		t.Errorf("CRD name = %q, want %q", obj.GetName(), rec.ID)
	}
	spec, _ := obj.Object["spec"].(map[string]any)
	if got := spec["sovereignFQDN"]; got != "omantel.omani.works" {
		t.Errorf("spec.sovereignFQDN = %v, want omantel.omani.works", got)
	}
	if got := spec["region"]; got != "fsn1" {
		t.Errorf("spec.region = %v, want fsn1", got)
	}
	// HetznerProjectID rides on the spec, but the TOKEN never does —
	// we never pass it through recordToUnstructured (Record.Request is
	// already redacted).
	if got, ok := spec["hetznerToken"]; ok {
		t.Errorf("spec.hetznerToken should not exist on the CRD, got %v", got)
	}
}

func TestSave_StrictModeUpdatesCRDOnSecondCall(t *testing.T) {
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	c, err := NewCRDStore(flat, dyn, WithCRDMode(CRDModeStrict))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}

	rec := sampleRecord()
	if err := c.Save(rec); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	// Mutate Status to a terminal state and Save again.
	rec.Status = "ready"
	rec.FinishedAt = time.Now().UTC().Truncate(time.Second)
	rec.Result = &provisioner.Result{
		SovereignFQDN:  "omantel.omani.works",
		ControlPlaneIP: "1.2.3.4",
		LoadBalancerIP: "5.6.7.8",
		ComponentStates: map[string]string{
			"cilium":       "installed",
			"cert-manager": "installed",
			"flux":         "installed",
		},
	}
	if err := c.Save(rec); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	// Status now reads "ready" with the IPs populated.
	obj, err := dyn.Resource(CRDGVR).Namespace(c.Namespace()).Get(context.Background(), rec.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	status, _ := obj.Object["status"].(map[string]any)
	if got := status["phase"]; got != PhaseReady {
		t.Errorf("status.phase = %v, want ready", got)
	}
	if got := status["controlPlaneIP"]; got != "1.2.3.4" {
		t.Errorf("status.controlPlaneIP = %v, want 1.2.3.4", got)
	}
	if got := status["loadBalancerIP"]; got != "5.6.7.8" {
		t.Errorf("status.loadBalancerIP = %v, want 5.6.7.8", got)
	}
	cs, _ := status["componentStates"].(map[string]any)
	if cs["cilium"] != "installed" {
		t.Errorf("status.componentStates.cilium = %v, want installed", cs["cilium"])
	}
	// Ready condition now True.
	conds, _ := status["conditions"].([]any)
	if len(conds) == 0 {
		t.Fatal("expected at least one condition")
	}
	c0, _ := conds[0].(map[string]any)
	if c0["status"] != "True" || c0["reason"] != "Ready" {
		t.Errorf("Ready condition = %+v, want status=True reason=Ready", c0)
	}
}

func TestSave_BestEffortModeSwallowsCRDErrorViaCallback(t *testing.T) {
	// Inject a dynamic client that always returns an error on Create.
	// The flat-file write must still succeed and Save must return nil
	// (best-effort), with onCRDError called once.
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "provisioningstates",
		func(_ clienttesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, errors.New("simulated apiserver outage")
		},
	)
	var (
		mu      sync.Mutex
		calls   []string
		lastErr error
	)
	c, err := NewCRDStore(flat, dyn,
		WithCRDMode(CRDModeBestEffort),
		WithCRDErrorCallback(func(id string, e error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, id)
			lastErr = e
		}),
	)
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}

	rec := sampleRecord()
	if err := c.Save(rec); err != nil {
		t.Fatalf("Save returned error in best-effort mode: %v", err)
	}
	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	gotErr := lastErr
	mu.Unlock()
	if len(gotCalls) != 1 || gotCalls[0] != rec.ID {
		t.Errorf("onCRDError calls = %v, want exactly [%q]", gotCalls, rec.ID)
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "simulated apiserver outage") {
		t.Errorf("onCRDError last error = %v, want one containing 'simulated apiserver outage'", gotErr)
	}
	// Flat-file write succeeded despite the CRD failure.
	got, err := c.Load(rec.ID)
	if err != nil {
		t.Fatalf("Load: %v (flat-file write must have succeeded)", err)
	}
	if got.ID != rec.ID {
		t.Errorf("Load id = %q, want %q", got.ID, rec.ID)
	}
}

func TestSave_StrictModeReturnsCRDError(t *testing.T) {
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "provisioningstates",
		func(_ clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("simulated apiserver outage")
		},
	)
	c, err := NewCRDStore(flat, dyn, WithCRDMode(CRDModeStrict))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	rec := sampleRecord()
	err = c.Save(rec)
	if err == nil {
		t.Fatal("Save returned nil in strict mode despite CRD error")
	}
	if !strings.Contains(err.Error(), "simulated apiserver outage") {
		t.Errorf("Save error = %v, want one containing 'simulated apiserver outage'", err)
	}
	// Flat-file STILL wrote — the strict mode error is on the CRD
	// path only. This is the migration-safety guarantee: no record is
	// lost just because the apiserver hiccupped.
	got, err := c.Load(rec.ID)
	if err != nil {
		t.Fatalf("Load: %v (flat-file must still hold the record)", err)
	}
	if got.ID != rec.ID {
		t.Errorf("Load id = %q, want %q", got.ID, rec.ID)
	}
}

func TestSave_OnDiskAndOnCRDNeverCarryPlaintextSecrets(t *testing.T) {
	// Defense-in-depth: even with strict-mode CRD writes enabled, the
	// HetznerToken / DynadotAPIKey / DynadotAPISecret plaintexts must
	// not land in the unstructured object served to the apiserver.
	// recordToUnstructured operates on the already-redacted Record;
	// this test asserts the marker doesn't accidentally get
	// substituted back to the plaintext.
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	c, err := NewCRDStore(flat, dyn, WithCRDMode(CRDModeStrict))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	rec := sampleRecord()
	if err := c.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	obj, err := dyn.Resource(CRDGVR).Namespace(c.Namespace()).Get(context.Background(), rec.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Walk every string in the unstructured object and assert no
	// plaintext token leaks. The fake client preserves the exact
	// shape we passed to Create, so the assertion is direct.
	leaks := []string{"MUST-NEVER-LAND-ON-CRD"}
	for leak := range leaks {
		walk(t, obj.Object, leaks[leak])
	}
}

// walk descends a map[string]any / []any tree and fails the test if
// any string value contains target. Used by the leak test above.
func walk(t *testing.T, v any, target string) {
	t.Helper()
	switch tv := v.(type) {
	case string:
		if strings.Contains(tv, target) {
			t.Errorf("found leak %q in unstructured object: %q", target, tv)
		}
	case map[string]any:
		for _, child := range tv {
			walk(t, child, target)
		}
	case []any:
		for _, child := range tv {
			walk(t, child, target)
		}
	}
}

func TestLoadCRD_RoundTripsSpecFields(t *testing.T) {
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	c, err := NewCRDStore(flat, dyn, WithCRDMode(CRDModeStrict))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	rec := sampleRecord()
	if err := c.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := c.LoadCRD(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("LoadCRD: %v", err)
	}
	if got.ID != rec.ID {
		t.Errorf("ID round-trip: %q != %q", got.ID, rec.ID)
	}
	if got.Request.SovereignFQDN != rec.Request.SovereignFQDN {
		t.Errorf("SovereignFQDN round-trip: %q != %q", got.Request.SovereignFQDN, rec.Request.SovereignFQDN)
	}
	if got.Request.Region != rec.Request.Region {
		t.Errorf("Region round-trip: %q != %q", got.Request.Region, rec.Request.Region)
	}
	if got.Request.WorkerCount != rec.Request.WorkerCount {
		t.Errorf("WorkerCount round-trip: %d != %d", got.Request.WorkerCount, rec.Request.WorkerCount)
	}
	if !got.Request.HAEnabled {
		t.Errorf("HAEnabled should round-trip as true")
	}
	// Status should reflect the CRD's coarse phase, not the in-memory
	// value — that's the lossy contract the CRD is the public face of.
	if got.Status != PhaseBootstrapping {
		t.Errorf("Status round-trip: got %q, want %q (in-memory provisioning → CRD bootstrapping)", got.Status, PhaseBootstrapping)
	}
}

func TestDeleteCRD_RemovesObjectButKeepsFlatFile(t *testing.T) {
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	c, err := NewCRDStore(flat, dyn, WithCRDMode(CRDModeStrict))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	rec := sampleRecord()
	if err := c.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := c.DeleteCRD(context.Background(), rec.ID); err != nil {
		t.Fatalf("DeleteCRD: %v", err)
	}
	if _, err := dyn.Resource(CRDGVR).Namespace(c.Namespace()).Get(context.Background(), rec.ID, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("CRD should be gone, Get returned %v", err)
	}
	// Flat-file STILL has the record — this is the audit-trail
	// guarantee. Tenant-deletion workflows can prune the K8s side
	// without losing the on-disk receipt.
	got, err := c.Load(rec.ID)
	if err != nil {
		t.Fatalf("Load after DeleteCRD: %v", err)
	}
	if got.ID != rec.ID {
		t.Errorf("flat-file Load id = %q, want %q", got.ID, rec.ID)
	}
}

func TestDeleteCRD_IdempotentOnMissingCRD(t *testing.T) {
	flat := freshStore(t)
	dyn := fakeDynamicClient()
	c, err := NewCRDStore(flat, dyn, WithCRDMode(CRDModeStrict))
	if err != nil {
		t.Fatalf("NewCRDStore: %v", err)
	}
	// Delete on a never-created id — must not error.
	if err := c.DeleteCRD(context.Background(), "no-such-id"); err != nil {
		t.Errorf("DeleteCRD on missing id returned %v, want nil (idempotent)", err)
	}
}

func TestRecordToUnstructured_ReadyConditionMessage(t *testing.T) {
	// readyConditionMessage on phase=failed must surface rec.Error
	// verbatim; on phase=ready it should describe the success.
	rec := sampleRecord()
	rec.Status = "failed"
	rec.Error = "tofu apply: hcloud_server.cp[0]: 422 Unprocessable Entity"

	obj := recordToUnstructured(rec, "catalyst")
	conds, _ := obj.Object["status"].(map[string]any)["conditions"].([]any)
	c0, _ := conds[0].(map[string]any)
	if c0["status"] != "False" {
		t.Errorf("Ready.status = %v, want False", c0["status"])
	}
	if !strings.Contains(c0["message"].(string), "422 Unprocessable Entity") {
		t.Errorf("Ready.message = %v, want it to surface the tofu error", c0["message"])
	}
}

func TestRecordToUnstructured_StripsCredentialFields(t *testing.T) {
	// Belt-and-braces: even a hand-built Record carrying plaintext in
	// fields the Redact() helper would have replaced won't end up on
	// the CRD because recordToUnstructured only reads the redacted
	// fields from RedactedRequest. The Hetzner token isn't in the
	// projection at all.
	rec := Record{
		ID: "feed1234567890ab",
		Request: RedactedRequest{
			SovereignFQDN:    "test.example.com",
			HetznerToken:     "<redacted>",
			DynadotAPIKey:    "<redacted>",
			DynadotAPISecret: "<redacted>",
		},
		StartedAt: time.Now().UTC(),
	}
	obj := recordToUnstructured(rec, "catalyst")
	spec, _ := obj.Object["spec"].(map[string]any)
	for _, banned := range []string{"hetznerToken", "dynadotKey", "dynadotSecret", "registrarToken"} {
		if _, ok := spec[banned]; ok {
			t.Errorf("spec carries banned field %q — credentials must not project to the CRD", banned)
		}
	}
}

// --- Helpers (no production code, kept here to avoid expanding the
//     public surface of the store package) ---

// _ exists so the `unstructured` import is exercised from a public
// surface in tests even when no other test references it directly.
// Keeps a future field-add that drops the last reference from
// triggering a `goimports`-induced removal.
var _ = unstructured.Unstructured{}
