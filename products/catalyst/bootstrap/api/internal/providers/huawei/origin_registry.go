package huawei

import "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"

// projectRegistry is the address at which a new Sovereign on this cloud
// reaches the registry of the parent. Its nodes reach this address without
// leaving the cloud. It answers for the same project paths as the registry
// of the parent. Work package SOV-940 deletes it.
const projectRegistry = "http://212.72.24.20:5000"

// SOV-135: the variables document of a new Sovereign on this cloud names
// projectRegistry as the origin address of its proxy names.
func init() {
	provisioner.OriginRegistryByProvider[Name] = projectRegistry
}
