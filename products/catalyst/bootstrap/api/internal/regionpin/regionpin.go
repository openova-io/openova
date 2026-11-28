// Package regionpin is the rule of a Blueprint that pins each of its
// Applications to its region (spec.regionPinned, for things a region change
// would delete and re-create, such as a virtual machine). It has three
// readers in catalyst-api, each refusing with 422 placement-immutable:
//
//   - the install route (validateInstallRefs) admits only a singleton in one
//     region: InstallRefusal;
//   - the edit route refuses a change of the Application's regions, and
//   - the Primary move, which is a placement edit, refuses another Primary:
//     EditGate.
//
// Spike INF-210 measured why: a region change, and the first Primary move,
// delete the chart's resources in the old region within a minute and create
// new ones in the new region.
package regionpin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/regionlocal"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// CodePlacementImmutable is the code of every refusal of this package.
const CodePlacementImmutable = "placement-immutable"

// What a placement edit does to an Application's regions (Change).
const (
	ChangeNone    = ""
	ChangePrimary = "primary"
	ChangeRegions = "regions"
)

// blueprints is the Blueprint resource the install route reads too.
var blueprints = schema.GroupVersionResource{Group: "catalyst.openova.io", Version: "v1", Resource: "blueprints"}

// Pinned reports whether a Blueprint object (the map under
// unstructured.Unstructured.Object) declares spec.regionPinned: true. The
// definition types the field boolean with default false.
func Pinned(bpObj map[string]any) bool {
	spec, _ := bpObj["spec"].(map[string]any)
	pinned, _ := spec["regionPinned"].(bool)
	return pinned
}

// InstallRefusal returns the refusal of an install of an Application of a
// pinned Blueprint, or nil. Only a singleton in one region is admitted: any
// other placement (more than one region, or a mode with a standby) lets the
// Application's region change without an edit, by a Primary move or a
// failover. "More than one region" is regionlocal.MultiRegion, the rule of
// ADR-0015 D6, so the two install refusals cannot disagree on it.
func InstallRefusal(blueprint string, bpObj map[string]any, mode string, regions []string) *instances.Refusal {
	if !Pinned(bpObj) || !regionlocal.MultiRegion(mode, len(regions)) {
		return nil
	}
	return &instances.Refusal{Status: http.StatusUnprocessableEntity, Code: CodePlacementImmutable,
		Message: fmt.Sprintf("Blueprint %s pins each Application to its region (spec.regionPinned): install it as a singleton in one region, not mode %q in regions [%s]",
			blueprint, mode, strings.Join(regions, ", "))}
}

// TargetRegions flattens the #3969 targets model into the legacy
// spec.regions list, Primary target(s) first so regions[0] is the primary
// region (placement_projection.go derives status.placement.primaryRegion
// from regions[0] for the single-primary modes). Duplicate regions are
// dropped; relative order is otherwise preserved. The edit route folds
// targets with it, so EditGate compares the very list the edit writes.
func TargetRegions(targets []bpv1.PlacementTarget) []string {
	seen := map[string]bool{}
	var primaries, rest []string
	for _, t := range targets {
		r := strings.TrimSpace(t.Region)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		if t.Role == bpv1.DataRolePrimary {
			primaries = append(primaries, r)
		} else {
			rest = append(rest, r)
		}
	}
	return append(primaries, rest...)
}

// Change reports what a placement edit does to an Application's regions.
// cur is the list the Application has, Primary first; next holds every list
// the edit writes (spec.regions, and the regions of spec.placement.targets,
// Primary first). It returns ChangeRegions when a list in next names another
// set of regions, else ChangePrimary when one names another Primary first,
// else ChangeNone. An empty cur cannot be compared and is ChangeRegions; so is
// an empty list in next, which leaves the choice of regions to the controller.
func Change(cur []string, next [][]string) string {
	if len(cur) == 0 {
		return ChangeRegions
	}
	change := ChangeNone
	for _, n := range next {
		if !sameRegions(cur, n) {
			return ChangeRegions
		}
		if n[0] != cur[0] {
			change = ChangePrimary
		}
	}
	return change
}

// EditRefusal returns the refusal of a placement edit of the Application app
// of a pinned Blueprint, or nil: nil when the Blueprint is not pinned or the
// edit changes nothing (change is Change(cur, next)).
func EditRefusal(blueprint, app string, bpObj map[string]any, change string, cur []string, next [][]string) *instances.Refusal {
	if change == ChangeNone || !Pinned(bpObj) {
		return nil
	}
	var to []string
	moved := ""
	for _, n := range next {
		to = append(to, "["+strings.Join(n, ", ")+"]")
		if len(n) > 0 && len(cur) > 0 && n[0] != cur[0] && moved == "" {
			moved = n[0]
		}
	}
	what := fmt.Sprintf("change its regions from [%s] to %s", strings.Join(cur, ", "), strings.Join(to, " and "))
	if change == ChangePrimary {
		what = fmt.Sprintf("move its Primary from %s to %s", cur[0], moved)
	}
	return &instances.Refusal{Status: http.StatusUnprocessableEntity, Code: CodePlacementImmutable,
		Message: fmt.Sprintf("Application %s of Blueprint %s is pinned to its region (spec.regionPinned): an edit may not %s", app, blueprint, what)}
}

// EditGate is the edit route's reader: the refusal of a placement edit of the
// Application app, whose spec.regions is cur, that writes regions to
// spec.regions and carries targets; or nil. The Blueprint object is read only
// when the edit changes the region list. A Blueprint that cannot be read
// refuses the edit, because whether it is pinned is then unknown: 422
// blueprint-not-found when it does not exist, 503 reference-lookup-failed
// when the apiserver did not answer.
func EditGate(ctx context.Context, client dynamic.Interface, app *unstructured.Unstructured, cur, regions []string, targets []bpv1.PlacementTarget) *instances.Refusal {
	next := [][]string{regions}
	if len(targets) > 0 {
		next = append(next, TargetRegions(targets))
	}
	change := Change(cur, next)
	if change == ChangeNone {
		return nil
	}
	name, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "name")
	bp, err := client.Resource(blueprints).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &instances.Refusal{Status: http.StatusUnprocessableEntity, Code: "blueprint-not-found",
			Message: fmt.Sprintf("Blueprint %q of Application %q does not exist on this Sovereign, so whether the edit may change its regions cannot be read", name, app.GetName())}
	}
	if err != nil {
		return &instances.Refusal{Status: http.StatusServiceUnavailable, Code: "reference-lookup-failed",
			Message: fmt.Sprintf("could not read Blueprint %q: %v", name, err)}
	}
	return EditRefusal(name, app.GetName(), bp.Object, change, cur, next)
}

// sameRegions reports whether two region lists name the same set of regions.
func sameRegions(a, b []string) bool {
	set := func(in []string) []string {
		seen := map[string]bool{}
		out := []string{}
		for _, r := range in {
			if r = strings.TrimSpace(r); r != "" && !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		sort.Strings(out)
		return out
	}
	x, y := set(a), set(b)
	if len(x) != len(y) || len(x) == 0 {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
