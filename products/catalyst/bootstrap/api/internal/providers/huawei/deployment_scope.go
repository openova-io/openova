// deployment_scope.go — the one selector of every destructive call of the
// wipe and of the project-wide reclaim of this adapter.
//
// A deployment's resources carry its identity in their names: the module
// infra/providers/huawei names each one with local.name_prefix,
// catalyst-<fqdn, dots as dashes>-<first 8 characters of the deployment id>,
// alone (the bucket) or followed by a dash. A destructive call deletes a
// resource only when its name, read back from the cloud, carries that
// identity. The Sovereign's name alone (catalyst-<fqdn>-) never selects: two
// deployments of one FQDN share it, and a wipe of one tore down the other
// (the claims of 22 November 2026). A resource that carries another
// deployment's identity or none never matches; the bastion (its address and
// any name that holds "bastion") never matches.
package huawei

import (
	"errors"
	"strings"
)

// deploymentScope selects the resources of one deployment. The zero value
// selects nothing: a caller that acts for no deployment (the project-wide
// reclaim) owns no resource.
type deploymentScope struct {
	prefix string // local.name_prefix of the deployment
	// node is local.node_name_prefix, the prefix of the deployment's ECS
	// names: the same identity with the FQDN part cut (nodeStem), because a
	// node's name is its hostname. It equals prefix for a short FQDN.
	node string
	// rotated is the name prefix of the NAT addresses the pre-flight
	// allocated outside the state before #8611
	// (catalyst-<first 8 characters of the deployment id>-nat-preflight-<n>);
	// the addresses it chooses since then are named with prefix.
	rotated string
}

// errNoDeploymentIdentity is the refusal of a destructive call that was not
// given the identity of the deployment it acts for.
var errNoDeploymentIdentity = errors.New("the Sovereign FQDN and a deployment id of at least 8 characters are required")

// newDeploymentScope is the selector of the deployment (fqdn, deploymentID).
// It refuses without an FQDN or a deployment id of at least 8 characters,
// because without them it could only select by a name that other
// deployments share.
func newDeploymentScope(fqdn, deploymentID string) (deploymentScope, error) {
	fqdn, dep := strings.TrimSpace(fqdn), strings.TrimSpace(deploymentID)
	if fqdn == "" || len(dep) < 8 {
		return deploymentScope{}, errNoDeploymentIdentity
	}
	stem := strings.ReplaceAll(fqdn, ".", "-")
	return deploymentScope{prefix: "catalyst-" + stem + "-" + dep[:8],
		node:    "catalyst-" + nodeStem(stem) + "-" + dep[:8],
		rotated: "catalyst-" + dep[:8] + "-nat-preflight-"}, nil
}

// nodeNameStemLen is how many characters of the FQDN stem (the FQDN with
// dashes for dots) an ECS node name carries: local.node_name_prefix in
// infra/providers/huawei/main.tf cuts it there, because a node name is the
// node's hostname, at most 63 characters. A stem of up to 20 characters is
// whole, so those names are what they always were.
const nodeNameStemLen = 20

// nodeStem is the FQDN stem as an ECS node name carries it.
func nodeStem(stem string) string {
	if len(stem) > nodeNameStemLen {
		return stem[:nodeNameStemLen]
	}
	return stem
}

// ownsServer reports whether an ECS name carries the deployment's identity:
// the module's node-name prefix, or the full prefix that names every other
// resource (a server created before the node names were cut).
func (s deploymentScope) ownsServer(name string) bool {
	if s.owns(name) {
		return true
	}
	if s.node == "" || strings.Contains(strings.ToLower(name), "bastion") {
		return false
	}
	return name == s.node || strings.HasPrefix(name, s.node+"-")
}

// owns reports whether a resource name carries the deployment's identity.
func (s deploymentScope) owns(name string) bool {
	if s.prefix == "" || strings.Contains(strings.ToLower(name), "bastion") {
		return false
	}
	return name == s.prefix || strings.HasPrefix(name, s.prefix+"-") || strings.HasPrefix(name, s.rotated)
}

// ownsAddress reports whether an EIP carries the deployment's identity in its
// bandwidth name. An address without a name, with another deployment's name,
// or the bastion's, is never the deployment's.
func (s deploymentScope) ownsAddress(e rawPublicIP) bool {
	return !isProtectedBastionEIP(e) && s.owns(e.BandwidthName)
}
