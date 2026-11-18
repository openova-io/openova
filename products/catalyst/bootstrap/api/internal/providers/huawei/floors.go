// floors.go — the worker minimum and the flavor floors of a Huawei Sovereign
// (#2467, #2536, #4055). Moved here from the create handler (CLD-103).
//
// The full platform (console, the database pair, the management and DMZ
// vClusters, Keycloak, Harbor, Gitea, OpenBao and the security stack) does
// not fit on 2 vCPU nodes: a Sovereign built on them came up with
// "Insufficient cpu" and the console never scheduled. The floors are profile
// values: spec.compute.minWorkerCount and spec.compute.minimumFlavor.

package huawei

import (
	"sort"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// Flavor roles of the cloud profile.
const (
	roleControlPlane = "control-plane"
	roleWorker       = "worker"
)

// installationFlavors are the flavors of the installation the platform
// provisions on today. The pack profile lists no flavor, because flavors are
// installation values; while a request names no installation document, the
// floors are measured against this list. An effective profile that lists
// flavors replaces it.
var installationFlavors = []cloudprofile.Flavor{
	{Name: "s7n.large.4", VCPU: 2, MemoryGiB: 8, Roles: []string{roleControlPlane, roleWorker}},
	{Name: "m7n.large.8", VCPU: 2, MemoryGiB: 16, Roles: []string{roleControlPlane, roleWorker}},
	{Name: "m7n.xlarge.8", VCPU: 4, MemoryGiB: 32, Roles: []string{roleControlPlane, roleWorker}},
	{Name: "m7n.2xlarge.8", VCPU: 8, MemoryGiB: 64, Roles: []string{roleControlPlane, roleWorker}},
}

// applyFloors raises every region of spec to the floors of profile: the
// worker count to spec.compute.minWorkerCount, and an empty flavor or a
// listed flavor below spec.compute.minimumFlavor to the smallest listed
// flavor of the role that meets the floor. A flavor the list does not name
// is kept as sent.
func applyFloors(spec *providers.ProvisionSpec, profile *cloudprofile.Profile) {
	c := profile.Spec.Compute
	flavors := c.Flavors
	if len(flavors) == 0 {
		flavors = installationFlavors
	}
	var cp, worker cloudprofile.Floor
	if c.MinimumFlavor != nil {
		cp, worker = c.MinimumFlavor.ControlPlane, c.MinimumFlavor.Worker
	}
	for i := range spec.Regions {
		r := &spec.Regions[i]
		if r.WorkerCount < c.MinWorkers() {
			r.WorkerCount = c.MinWorkers()
		}
		r.ControlPlaneSize = floorFlavor(r.ControlPlaneSize, roleControlPlane, cp, flavors)
		r.WorkerSize = floorFlavor(r.WorkerSize, roleWorker, worker, flavors)
	}
}

// floorFlavor returns name, or the smallest flavor of flavors with role that
// meets floor when name is empty or a listed flavor below floor. A zero
// floor, a name the list does not hold, and a floor no listed flavor meets
// all return name.
func floorFlavor(name, role string, floor cloudprofile.Floor, flavors []cloudprofile.Flavor) string {
	if floor.VCPU == 0 && floor.MemoryGiB == 0 {
		return name
	}
	if name != "" {
		f, ok := flavorNamed(name, flavors)
		if !ok || meets(f, floor) {
			return name
		}
	}
	var fit []cloudprofile.Flavor
	for _, f := range flavors {
		if hasRole(f, role) && meets(f, floor) {
			fit = append(fit, f)
		}
	}
	if len(fit) == 0 {
		return name
	}
	sort.Slice(fit, func(a, b int) bool {
		if fit[a].VCPU != fit[b].VCPU {
			return fit[a].VCPU < fit[b].VCPU
		}
		if fit[a].MemoryGiB != fit[b].MemoryGiB {
			return fit[a].MemoryGiB < fit[b].MemoryGiB
		}
		return fit[a].Name < fit[b].Name
	})
	return fit[0].Name
}

func flavorNamed(name string, flavors []cloudprofile.Flavor) (cloudprofile.Flavor, bool) {
	for _, f := range flavors {
		if f.Name == name {
			return f, true
		}
	}
	return cloudprofile.Flavor{}, false
}

func meets(f cloudprofile.Flavor, floor cloudprofile.Floor) bool {
	return f.VCPU >= floor.VCPU && f.MemoryGiB >= floor.MemoryGiB
}

func hasRole(f cloudprofile.Flavor, role string) bool {
	for _, r := range f.Roles {
		if r == role {
			return true
		}
	}
	return false
}
