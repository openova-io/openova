package handler

// ADR-0015 D6 — a Blueprint whose spec.regionLocalVolumes has an entry that
// nothing replicates is REFUSED for multi-region placement at every create
// door (validateInstallRefs), with a 422 that names the volume, before any
// object is written. Singleton placements of the same Blueprint, and
// multi-region placements of a Blueprint whose volumes are all replicated,
// are admitted.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// refTestBlueprintWithVolumes is refTestBlueprint carrying
// spec.regionLocalVolumes as name→replication pairs.
func refTestBlueprintWithVolumes(name string, vols ...[2]string) *unstructured.Unstructured {
	bp := refTestBlueprint(name)
	items := make([]interface{}, 0, len(vols))
	for _, v := range vols {
		items = append(items, map[string]interface{}{"name": v[0], "replication": v[1]})
	}
	_ = unstructured.SetNestedSlice(bp.Object, items, "spec", "regionLocalVolumes")
	return bp
}

func TestCreateInstance_RegionLocalVolumeNotReplicated_MultiRegion_422NamesVolume(t *testing.T) {
	for _, mode := range []string{"active-hot-standby", "active-passive"} {
		t.Run(mode, func(t *testing.T) {
			h, _, dyn := newTestHandlerWithEndpoint(t,
				refTestBlueprintWithVolumes("bp-media", [2]string{"uploads", "s3-replicated"}, [2]string{"wp-content", "none"}))
			h.SetCatalogClient(fakeBlueprintInCatalog("bp-media", nil, true, []string{"singleton", mode}))
			rec := postInstance(t, h, `{"blueprint":"bp-media","org":"acme","environment":"acme-prod","name":"m-1",`+
				`"topology":"`+mode+`","placement":{"regions":["rgn-a","rgn-b"]}}`)
			body := rec.Body.String()
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "region-local-volume-not-replicated") {
				t.Fatalf("status = %d body=%s, want 422 region-local-volume-not-replicated", rec.Code, body)
			}
			if !strings.Contains(body, `wp-content`) || strings.Contains(body, `\"uploads\"`) {
				t.Fatalf("message must name the unreplicated volume wp-content and only it: %s", body)
			}
			if _, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "m-1", metav1.GetOptions{}); err == nil {
				t.Fatal("an Application CR was written despite the refusal")
			}
		})
	}
}

func TestCreateInstance_RegionLocalVolumeNotReplicated_Singleton_Admitted(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t, refTestBlueprintWithVolumes("bp-media", [2]string{"wp-content", "none"}))
	h.SetCatalogClient(fakeBlueprintInCatalog("bp-media", nil, true, []string{"singleton"}))
	rec := postInstance(t, h, `{"blueprint":"bp-media","org":"acme","environment":"acme-prod","name":"m-1","topology":"singleton"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201 — a single region strands nothing", rec.Code, rec.Body.String())
	}
}

func TestCreateInstance_RegionLocalVolumesReplicated_MultiRegion_Admitted(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t,
		refTestBlueprintWithVolumes("bp-media", [2]string{"uploads", "s3-replicated"}, [2]string{"wp-content", "none-needed"}))
	h.SetCatalogClient(fakeBlueprintInCatalog("bp-media", nil, true, []string{"singleton", "active-hot-standby"}))
	rec := postInstance(t, h, `{"blueprint":"bp-media","org":"acme","environment":"acme-prod","name":"m-1",`+
		`"topology":"active-hot-standby","placement":{"regions":["rgn-a","rgn-b"]}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201", rec.Code, rec.Body.String())
	}
}

// The gate lives in validateInstallRefs, so a door that passes no mode but
// names two regions is refused too.
func TestValidateInstallRefs_TwoRegionsNoMode_RegionLocalVolume_422(t *testing.T) {
	h, _, _ := newTestHandlerWithEndpoint(t, refTestBlueprintWithVolumes("bp-media", [2]string{"data", "none"}))
	c, err := h.dynamicClientOrFallback()
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := validateInstallRefs(context.Background(), c, installRefs{
		Org: "acme", Environment: "acme-prod", Blueprint: "bp-media", Regions: []string{"rgn-a", "rgn-b"},
	})
	if rerr == nil || rerr.Status != http.StatusUnprocessableEntity || rerr.Code != "region-local-volume-not-replicated" ||
		!strings.Contains(rerr.Message, `"data"`) {
		t.Fatalf("got %+v, want 422 region-local-volume-not-replicated naming \"data\"", rerr)
	}
	// One region: admitted.
	if _, rerr := validateInstallRefs(context.Background(), c, installRefs{
		Org: "acme", Environment: "acme-prod", Blueprint: "bp-media", Regions: []string{"rgn-a"},
	}); rerr != nil {
		t.Fatalf("single region refused: %+v", rerr)
	}
}
