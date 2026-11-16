package huawei

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// SOV-135: this adapter registers the address at which a new Sovereign on
// this cloud reaches the registry of the parent, under the provider name that
// a validated request carries.
func TestOriginRegistryIsRegistered(t *testing.T) {
	const want = "http://212.72.24.20:5000"
	if Name != "huawei" {
		t.Fatalf("Name = %q; a validated request carries the provider name huawei", Name)
	}
	if got := provisioner.OriginRegistryByProvider[Name]; got != want {
		t.Errorf("OriginRegistryByProvider[%q] = %q, want %q", Name, got, want)
	}
}
