// #7922 — the version a catalog row of GET /api/v1/sovereign/apps reports.
//
// The embedded catalogue (internal/catalog/blueprints.json, go:embed) is
// frozen when the catalyst-api image is built. The deploy bot's lockstep
// commits after a chart publish (blueprint.yaml + catalog-seed + bootstrap-kit
// pin + the regenerated blueprints.json) carry [skip ci] and never rebuild the
// image, so the image's versions trail the kit the Sovereign follows. Measured
// on candidate rc/20261115T144352Z-67402a1: image 2c6f654 said bp-chargeback
// 0.1.66 while the kit pin, the catalog-seed Blueprint CR and the installed
// HelmRelease on hw307 all said 0.1.67.
//
// The Sovereign's own catalog is the cluster-scoped Blueprint CRs the umbrella
// chart renders from templates/catalog-seed — the umbrella version the
// bootstrap kit pins, at the Git ref the Sovereign follows, and the objects the
// install path's in-cluster catalog fallback reads. A row reports that CR's
// spec.version; the image's value stays only as the answer when no CR names
// the Blueprint (CI, a catalog that has not converged, a Blueprint with no
// seed entry).
package handler

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
)

// blueprintCRVersions maps Blueprint CR name → non-empty spec.version.
func blueprintCRVersions(items []unstructured.Unstructured) map[string]string {
	out := make(map[string]string, len(items))
	for i := range items {
		v, _, _ := unstructured.NestedString(items[i].Object, "spec", "version")
		if v = strings.TrimSpace(v); v != "" {
			out[items[i].GetName()] = v
		}
	}
	return out
}

// catalogRowVersion is the version a catalog row reports: the Sovereign's
// Blueprint CR when it names one, else the version embedded in the image.
func catalogRowVersion(b catalog.Blueprint, crVersions map[string]string) string {
	if v, ok := crVersions[b.ID]; ok {
		return v
	}
	return deref(b.Version)
}
