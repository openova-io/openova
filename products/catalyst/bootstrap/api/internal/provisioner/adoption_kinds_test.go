package provisioner

// adoption_kinds_test.go — GenerateAdoptionClaims reads the resource types
// it adopts from the provider's adapter (providers.AdoptionMapper). The
// adapter packages import this package, so its tests cannot import them;
// they register stand-ins that declare the same kinds as the two adapters.
// The real adapters are exercised through the registry by the adoption
// tests of internal/handler.

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// adoptionStandIn is a registered adapter that maps adoption kinds. Its
// CloudProvider is the port double of port_double_test.go, so each pack name
// is registered once and serves the Validate tests as well.
type adoptionStandIn struct {
	providers.CloudProvider
	kinds map[string]string
}

func (a adoptionStandIn) AdoptionKinds() map[string]string { return a.kinds }

// noAdoptionStandIn is a registered adapter without the capability.
type noAdoptionStandIn struct{ providers.CloudProvider }

func init() {
	providers.RegisterProvider("huawei", adoptionStandIn{CloudProvider: portDouble{name: "huawei"}, kinds: map[string]string{
		"huaweicloud_elb_loadbalancer": "loadbalancer",
		"huaweicloud_compute_instance": "server",
		"huaweicloud_vpc":              "network",
		"huaweicloud_vpc_subnet":       "subnet",
		"huaweicloud_vpc_eip":          "eip",
	}})
	providers.RegisterProvider("hetzner", adoptionStandIn{CloudProvider: portDouble{name: "hetzner"}, kinds: map[string]string{
		"hcloud_load_balancer": "loadbalancer",
		"hcloud_server":        "server",
		"hcloud_network":       "network",
		"hcloud_firewall":      "firewall",
		"hcloud_primary_ip":    "eip",
	}})
	providers.RegisterProvider("plain", noAdoptionStandIn{})
}

// An empty cloud is an error: the provider is never defaulted.
func TestGenerateAdoptionClaims_EmptyCloudIsError(t *testing.T) {
	if _, err := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw173.omani.works", "", "me-east-215"); err == nil {
		t.Fatal("expected an error for an empty cloud, got none")
	}
}

// A cloud with no registered adapter is an error.
func TestGenerateAdoptionClaims_UnregisteredCloudIsError(t *testing.T) {
	_, err := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw173.omani.works", "nosuch", "me-east-215")
	if err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Fatalf("expected an error naming the unregistered cloud, got %v", err)
	}
}

// The adapter decides which resources are adopted: a resource type the
// adapter of the given cloud does not map is skipped.
func TestGenerateAdoptionClaims_KindsComeFromTheAdapter(t *testing.T) {
	out, err := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw173.omani.works", "hetzner", "fsn1")
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if strings.Contains(string(out), "kind: CloudAdoption") {
		t.Errorf("the other adapter's resource types were adopted:\n%s", out)
	}
}

// An adapter without AdoptionMapper adopts nothing; the document is the
// valid header-only file, not an error.
func TestGenerateAdoptionClaims_AdapterWithoutMapperAdoptsNothing(t *testing.T) {
	out, err := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw173.omani.works", "plain", "r1")
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "kind: CloudAdoption") || !strings.Contains(got, "no adoptable resources") {
		t.Errorf("expected a header-only document, got:\n%s", got)
	}
}
