// applications_update_4950_owndeps_test.go — reproduction + regression for
// #4950: console Topology→Edit-placement→Apply 400'd
// "placement.mode is required when placement is set" even though the #4840
// targets[]→mode fold was in the deployed image.
//
// Root cause (empirically proven here): the REAL console PUT body
// (PlacementEditor.tsx apply()) carries `placement.ownedDependencies`
// ALONGSIDE `placement.targets[]`. `ownedDependencies` was absent from the Go
// `applicationPlacement` wire struct, so `decodeApplicationUpdateBody`'s
// canonical Path-A decode (which runs `DisallowUnknownFields`) REJECTED the
// body and fell back to the lenient simplified decoder — whose
// `decodePlacementValue` returned only mode+regions and silently dropped
// targets[]. With targets gone, the fold's `len(Targets)>0` guard was false,
// Mode stayed empty, and validateApplicationUpdateRequest 400'd.
//
// Fix: (1) add OwnedDependencies to applicationPlacement so the canonical
// decode accepts the real body; (2) make decodePlacementValue carry targets[]
// through the fallback so the targets are never dropped regardless of path.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// realConsolePlacementPUT is the EXACT body shape the Sovereign console
// PlacementEditor sends on Apply (region × cluster × vCluster × role, with a
// Standby standbyType, PLUS the ownedDependencies cascade list). Roles are
// the LOCKED capitalised vocabulary ("Primary"/"Standby"), matching
// core/controllers/pkg/apis/blueprint/v1alpha1/placement_target.go and the FE
// placement.ts DataRole type.
//
// #5616 — this body was RECORDED from the console before the placement
// editor stopped defaulting fresh targets to the `mgmt` tier. It is kept
// verbatim because it is the wire-compat fixture for #4950 (targets[] +
// ownedDependencies must decode), but `vcluster: "mgmt"` is only a legal
// placement on a Sovereign that actually installs the mgmt vCluster — so
// the tests below declare it installed rather than quietly weakening the
// availability gate.
const realConsolePlacementPUT = `{"placement":{` +
	`"targets":[` +
	`{"region":"me-east-215-a","cluster":"mgmt-A","vcluster":"mgmt","role":"Primary"},` +
	`{"region":"me-east-215-b","cluster":"mgmt-B","vcluster":"mgmt","role":"Standby","standbyType":"Hot"}` +
	`],` +
	`"ownedDependencies":[{"name":"shared-pg-pg","follow":true}]` +
	`}}`

// TestDecodeApplicationUpdateBody_4950_OwnedDependenciesKeepsTargets is the
// decode-level regression: the real console body (targets[] + ownedDependencies)
// must decode with targets[] preserved and mode derived to active-hot-standby —
// NOT dropped-to-empty-mode which 400'd. This guards BOTH fix arms (the struct
// field so Path-A succeeds, and the fallback carrying targets).
func TestDecodeApplicationUpdateBody_4950_OwnedDependenciesKeepsTargets(t *testing.T) {
	// #5616 — the recorded body places on `mgmt`; validate it as a
	// Sovereign that installs that tier would.
	instances.SetAvailableVClusterTiers("mgmt")
	t.Cleanup(func() { instances.SetAvailableVClusterTiers("") })
	body, err := decodeApplicationUpdateBody([]byte(realConsolePlacementPUT))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if body.Placement == nil {
		t.Fatalf("placement dropped entirely")
	}
	if len(body.Placement.Targets) != 2 {
		t.Fatalf("targets[] dropped by decode: got %d, want 2 (the #4950 root cause)", len(body.Placement.Targets))
	}
	// The targets[]→mode fold must have derived the pattern (1 Primary + 1 Hot
	// Standby ⇒ active-hot-standby) and the Primary-first regions.
	if body.Placement.Mode != "active-hot-standby" {
		t.Errorf("mode = %q, want active-hot-standby (fold did not fire)", body.Placement.Mode)
	}
	if !reflect.DeepEqual(body.Placement.Regions, []string{"me-east-215-a", "me-east-215-b"}) {
		t.Errorf("regions = %v, want [me-east-215-a me-east-215-b]", body.Placement.Regions)
	}
	// The whole point: validation must PASS (previously 400'd
	// "placement.mode is required when placement is set").
	if msg, ok := validateApplicationUpdateRequest(body); !ok {
		t.Errorf("validation STILL 400s after fix: %s", msg)
	}
}

// TestHandleApplicationUpdate_4950_ConsolePlacementApply_Returns200 drives the
// FULL PUT handler with the exact console body (via json.RawMessage so the
// unknown-to-old-struct `ownedDependencies` key is sent verbatim, exactly as
// the browser sends it) and asserts a 200 with the CR patched to the derived
// active-hot-standby pattern across both regions — the operator-visible
// Edit-placement→Apply outcome from the hw235 walk (app `shared-pg`).
func TestHandleApplicationUpdate_4950_ConsolePlacementApply_Returns200(t *testing.T) {
	// #5616 — see the fixture note: the recorded body places on `mgmt`.
	instances.SetAvailableVClusterTiers("mgmt")
	t.Cleanup(func() { instances.SetAvailableVClusterTiers("") })
	// Seed the app as a singleton in region-a so the Apply is a non-destructive
	// scale-up (singleton → active-hot-standby, +1 region) that needs no ?force.
	cr := makeAppCR("acme", "shared-pg", "1.2.3", "singleton", []string{"me-east-215-a"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-4950-placement")

	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/shared-pg?namespace=acme",
		json.RawMessage(realConsolePlacementPUT), registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("console placement Apply 400'd (the #4950 defect): got %d want 200; body=%s",
			rec.Code, rec.Body.String())
	}

	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "shared-pg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched CR: %v", err)
	}
	mode, _, _ := unstructured.NestedString(got.Object, "spec", "placement")
	if mode != "active-hot-standby" {
		t.Fatalf("spec.placement = %q, want active-hot-standby (derived from targets[])", mode)
	}
	regionsRaw, _, _ := unstructured.NestedSlice(got.Object, "spec", "regions")
	regions := stringsFromAnySlice(regionsRaw)
	if !reflect.DeepEqual(regions, []string{"me-east-215-a", "me-east-215-b"}) {
		t.Fatalf("spec.regions = %v, want [me-east-215-a me-east-215-b] (Primary-first)", regions)
	}
}
