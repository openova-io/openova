package catalog

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// WithCRVersions returns the function that sets the Version of each Blueprint
// in listed to the spec.version of the Sovereign's Blueprint CR of the same
// name (#7922). The function takes a List result as it comes, (items, err),
// so a caller passes the List call straight in.
//
// The embedded catalogue (blueprints.json, go:embed) is frozen when the
// catalyst-api image is built. The deploy bot's lockstep commits after a
// chart publish regenerate blueprints.json with [skip ci] and never rebuild
// the image, so the image's versions trail the kit the Sovereign follows.
// Measured on candidate rc/20261115T144352Z-67402a1: image 2c6f654 said
// bp-chargeback 0.1.66 while the kit pin, the catalog-seed Blueprint CR and
// the installed HelmRelease on hw307 all said 0.1.67, and
// GET /api/v1/sovereign/apps reported 0.1.66.
//
// The Sovereign's own catalogue is the cluster-scoped Blueprint CRs the
// umbrella chart renders from its catalog seed, at the Git ref the Sovereign
// follows. A Blueprint that no CR names, or a failed list (err != nil, e.g.
// the mothership, which has no Blueprint CRD), keeps the embedded version.
// listed is the caller's own slice (ListedBlueprints returns a fresh copy);
// the embedded catalogue is never changed.
func WithCRVersions(listed []Blueprint) func(crs []unstructured.Unstructured, err error) {
	return func(crs []unstructured.Unstructured, err error) {
		if err != nil {
			return
		}
		byName := make(map[string]string, len(crs))
		for i := range crs {
			v, _, _ := unstructured.NestedString(crs[i].Object, "spec", "version")
			if v = strings.TrimSpace(v); v != "" {
				byName[crs[i].GetName()] = v
			}
		}
		for i := range listed {
			if v, ok := byName[listed[i].ID]; ok {
				listed[i].Version = &v
			}
		}
	}
}
