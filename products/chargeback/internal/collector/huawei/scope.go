package huawei

import "strings"

// Resource scoping (#6855).
//
// A huawei-project cost source meters EVERY resource in the project. The
// project holds shared infrastructure and can hold more than one Sovereign, so
// without scoping a customer is billed for resources that are not theirs.
// Measured on hw307: 13 ECS collected for a 12-node Sovereign — the extra was
// `bastion-openova`, protected shared infrastructure, on the customer's bill.
//
// The failure is silent and looks correct: every line is a real resource at a
// real price. With two Sovereigns in one project each bill would simply double.
//
// Name matching alone CANNOT do this, which is why the rule is a graph. Measured
// across the live project:
//
//	ECS   12/13 carry the deployment id   (the bastion does not)
//	ELB    2/2  carry it
//	NAT    2/2  carry it
//	EVS   12/104 carry it — the other 92 are `pvc-<uuid>` from Kubernetes,
//	      attributable only through their attachment to a server
//	EIP    0/7  have a name at all — attributable through `bandwidth_name`
//
// Tag-based scoping is not available: the provisioner defines TMS tags but they
// are disabled (HCS tag API divergence), and every instance returns `tags: []`.

// ScopeMatcher decides whether a resource belongs to one deployment.
//
// The zero value matches EVERYTHING, which preserves the pre-#6855 behaviour
// for a source that has declared no scope. That is deliberate: silently
// dropping resources from a bill because a scope was not configured would be a
// worse failure than the one being fixed.
type ScopeMatcher struct {
	// Token is the deployment discriminator carried in provisioned resource
	// names — in practice the short deployment id. Empty = match everything.
	Token string
}

// Enabled reports whether any filtering happens at all.
func (m ScopeMatcher) Enabled() bool { return strings.TrimSpace(m.Token) != "" }

// direct reports whether a resource names the deployment itself.
func (m ScopeMatcher) direct(r Resource) bool {
	if strings.Contains(r.Name, m.Token) {
		return true
	}
	// EIPs carry no name; their bandwidth is named after what they serve.
	if bw := str(r.Attrs["bandwidth_name"]); bw != "" && strings.Contains(bw, m.Token) {
		return true
	}
	return false
}

// Partition splits resources into those belonging to the deployment and those
// that do not. Excluded resources are RETURNED, never silently dropped: a bill
// that quietly contains someone else's server is bad, and a bill that quietly
// omits the customer's own is equally bad — the caller logs and counts both.
//
// Two passes, because attribution is transitive: a `pvc-<uuid>` volume is only
// attributable through the server it is attached to, so the in-scope server set
// must exist before volumes are judged.
func (m ScopeMatcher) Partition(resources []Resource) (in, out []Resource) {
	if !m.Enabled() {
		return resources, nil
	}
	owned := map[string]bool{}
	for _, r := range resources {
		if m.direct(r) {
			owned[r.ID] = true
		}
	}
	// Pass 2: adopt resources attached to something already in scope.
	for _, r := range resources {
		if owned[r.ID] {
			continue
		}
		if att := str(r.Attrs["attached_to"]); att != "" && owned[att] {
			owned[r.ID] = true
		}
	}
	for _, r := range resources {
		if owned[r.ID] {
			in = append(in, r)
		} else {
			out = append(out, r)
		}
	}
	return in, out
}
