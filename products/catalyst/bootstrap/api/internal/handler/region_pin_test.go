package handler

// A Blueprint that declares spec.regionPinned keeps each Application in its
// region. The install route admits only a singleton in one region; the edit
// route refuses a change of the regions and a move of the Primary (the move
// is a placement edit). Each refusal is 422 placement-immutable and writes
// nothing. Every refusal has a control with the same request on a Blueprint
// that is not pinned.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func pinTestBlueprint(name string, pinned bool) *unstructured.Unstructured {
	bp := refTestBlueprint(name)
	_ = unstructured.SetNestedField(bp.Object, pinned, "spec", "regionPinned")
	return bp
}

func TestCreateInstance_RegionPinned_MultiRegion_422PlacementImmutable(t *testing.T) {
	body := `{"blueprint":"bp-vm-lab","org":"acme","environment":"acme-prod","name":"vm-1",` +
		`"topology":"active-hot-standby","placement":{"regions":["rgn-a","rgn-b"]}}`
	for _, pinned := range []bool{true, false} {
		h, dyn := newTestHandlerWithEndpoint(t, pinTestBlueprint("bp-vm-lab", pinned))
		h.SetCatalogClient(fakeBlueprintInCatalog("bp-vm-lab", nil, true, []string{"singleton", "active-hot-standby"}))
		rec := postInstance(t, h, body)
		_, getErr := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "vm-1", metav1.GetOptions{})
		if !pinned {
			if rec.Code != http.StatusCreated || getErr != nil {
				t.Fatalf("not pinned: status = %d body=%s get=%v, want 201 and the Application", rec.Code, rec.Body.String(), getErr)
			}
			continue
		}
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"placement-immutable"`) {
			t.Fatalf("pinned: status = %d body=%s, want 422 placement-immutable", rec.Code, rec.Body.String())
		}
		if getErr == nil {
			t.Fatal("pinned: an Application was written despite the refusal")
		}
	}
}

func TestValidateInstallRefs_RegionPinned(t *testing.T) {
	h, _ := newTestHandlerWithEndpoint(t, pinTestBlueprint("bp-vm-lab", true))
	c, err := h.dynamicClientOrFallback()
	if err != nil {
		t.Fatal(err)
	}
	refs := installRefs{Org: "acme", Environment: "acme-prod", Blueprint: "bp-vm-lab", Mode: "singleton", Regions: []string{"rgn-a", "rgn-b"}}
	if _, rerr := validateInstallRefs(context.Background(), c, refs); rerr == nil || rerr.Status != 422 || rerr.Code != "placement-immutable" {
		t.Fatalf("singleton in two regions: got %+v, want 422 placement-immutable", rerr)
	}
	refs.Regions = []string{"rgn-b"}
	out, rerr := validateInstallRefs(context.Background(), c, refs)
	if rerr != nil || !reflect.DeepEqual(out.Regions, []string{"rgn-b"}) {
		t.Fatalf("singleton in one region: got %+v regions %v, want admitted in rgn-b", rerr, out.Regions)
	}
}

// putPinned PUTs body to the edit route for an Application of bp-vm-lab in
// regions, placed mode. blueprint is the Blueprint object on the Sovereign, or
// nil for none.
func putPinned(t *testing.T, blueprint *unstructured.Unstructured, mode string, regions []string, query, body string) (*httptest.ResponseRecorder, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	app := makeAppCR("acme", "vm-1", "1.0.0", mode, regions)
	_ = unstructured.SetNestedField(app.Object, "bp-vm-lab", "spec", "blueprintRef", "name")
	seed := []runtime.Object{app}
	if blueprint != nil {
		seed = append(seed, blueprint)
	}
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(seed...)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-region-pin")
	rec := callUserAccess(t, h, http.MethodPut, "/api/v1/sovereigns/"+dep.ID+"/applications/vm-1?namespace=acme"+query,
		json.RawMessage(body), registerApplicationUpdateRoutes)
	return rec, client
}

func storedRegions(t *testing.T, client *dynamicfake.FakeDynamicClient) []string {
	t.Helper()
	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "vm-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Application: %v", err)
	}
	raw, _, _ := unstructured.NestedSlice(got.Object, "spec", "regions")
	return stringsFromAnySlice(raw)
}

func TestHandleApplicationUpdate_RegionPinned_422PlacementImmutable(t *testing.T) {
	const a, b = "me-east-215-a", "me-east-215-b"
	cases := []struct {
		name, mode, query, body, message string
		regions, moved                   []string
	}{
		{"region change", "singleton", "", `{"placement":{"mode":"singleton","regions":["` + b + `"]}}`,
			"change its regions from [" + a + "] to [" + b + "]", []string{a}, []string{b}},
		{"region change with force", "singleton", "&force=true", `{"placement":{"mode":"singleton","regions":["` + b + `"]}}`,
			"change its regions from [" + a + "]", []string{a}, []string{b}},
		{"region change with a target", "singleton", "", `{"placement":{"mode":"singleton","regions":["` + b + `"],` +
			`"targets":[{"region":"` + b + `","cluster":"rtz-b","role":"Primary"}]}}`,
			"change its regions from [" + a + "]", []string{a}, []string{b}},
		{"Primary move", "active-hot-standby", "", `{"placement":{"targets":[{"region":"` + b + `","cluster":"rtz-b","role":"Primary"},` +
			`{"region":"` + a + `","cluster":"rtz-a","role":"Standby","standbyType":"Hot"}]}}`,
			"move its Primary from " + a + " to " + b, []string{a, b}, []string{b, a}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, client := putPinned(t, pinTestBlueprint("bp-vm-lab", true), c.mode, c.regions, c.query, c.body)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"placement-immutable"`) ||
				!strings.Contains(rec.Body.String(), c.message) {
				t.Fatalf("pinned: status = %d body=%s, want 422 placement-immutable saying %q", rec.Code, rec.Body.String(), c.message)
			}
			if got := storedRegions(t, client); !reflect.DeepEqual(got, c.regions) {
				t.Fatalf("pinned: spec.regions = %v after the refusal, want %v unchanged", got, c.regions)
			}
			rec, client = putPinned(t, pinTestBlueprint("bp-vm-lab", false), c.mode, c.regions, c.query, c.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("not pinned: status = %d body=%s, want 200", rec.Code, rec.Body.String())
			}
			if got := storedRegions(t, client); !reflect.DeepEqual(got, c.moved) {
				t.Fatalf("not pinned: spec.regions = %v, want %v", got, c.moved)
			}
		})
	}
}

func TestHandleApplicationUpdate_RegionPinned_SameRegionAdmitted(t *testing.T) {
	rec, client := putPinned(t, pinTestBlueprint("bp-vm-lab", true), "singleton", []string{"me-east-215-a"}, "",
		`{"placement":{"mode":"singleton","regions":["me-east-215-a"]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200: the edit keeps the region", rec.Code, rec.Body.String())
	}
	if got := storedRegions(t, client); !reflect.DeepEqual(got, []string{"me-east-215-a"}) {
		t.Fatalf("spec.regions = %v, want [me-east-215-a]", got)
	}
}

func TestHandleApplicationUpdate_RegionChange_BlueprintMissing_Refused(t *testing.T) {
	rec, client := putPinned(t, nil, "singleton", []string{"me-east-215-a"}, "",
		`{"placement":{"mode":"singleton","regions":["me-east-215-b"]}}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"blueprint-not-found"`) {
		t.Fatalf("status = %d body=%s, want 422 blueprint-not-found", rec.Code, rec.Body.String())
	}
	if got := storedRegions(t, client); !reflect.DeepEqual(got, []string{"me-east-215-a"}) {
		t.Fatalf("spec.regions = %v after the refusal, want [me-east-215-a] unchanged", got)
	}
}
