package provisioner

import (
	"strings"
	"testing"
)

// hw173StateFixture is a trimmed terraform.tfstate matching the REAL
// hw173 (kom4dc / Huawei) Phase-0 state: the ELB the task brief names
// (d0e827ad-…), a control-plane ECS, the VPC, a subnet, and EIPs. Used to
// assert the generator emits exactly the right Observe-first CloudAdoptions.
const hw173StateFixture = `{
  "version": 4,
  "terraform_version": "1.6.0",
  "resources": [
    {
      "type": "huaweicloud_elb_loadbalancer",
      "name": "primary",
      "instances": [
        {"index_key": null, "attributes": {
          "id": "d0e827ad-2e02-44b3-9b25-1513b7b771f4",
          "name": "catalyst-hw173-omani-works-7bb723da-elb-primary",
          "vip_address": "10.20.1.50"
        }}
      ]
    },
    {
      "type": "huaweicloud_compute_instance",
      "name": "control_plane",
      "instances": [
        {"index_key": 0, "attributes": {
          "id": "b3313987-d9aa-4ade-be60-d044c6cf1b4c",
          "public_ip": "212.72.24.30"
        }},
        {"index_key": 1, "attributes": {
          "id": "f47b474d-f77a-4813-9452-a5fb2007dded",
          "public_ip": "212.72.24.35"
        }}
      ]
    },
    {
      "type": "huaweicloud_vpc",
      "name": "region",
      "instances": [
        {"index_key": "me-east-215-a", "attributes": {
          "id": "d25a49e8-ad3f-41eb-9b43-950a5582bf48"
        }}
      ]
    },
    {
      "type": "huaweicloud_vpc_subnet",
      "name": "region",
      "instances": [
        {"index_key": "me-east-215-a", "attributes": {
          "id": "5ce9b2ce-9099-4a66-a89b-de44efe8f2f7"
        }}
      ]
    },
    {
      "type": "huaweicloud_vpc_eip",
      "name": "elb_primary",
      "instances": [
        {"index_key": null, "attributes": {
          "id": "e350e859-c3bf-42a2-a356-716b6a76e052",
          "publicip": [{"ip_address": "212.72.24.33"}]
        }}
      ]
    },
    {
      "type": "huaweicloud_nat_gateway",
      "name": "nat",
      "instances": [
        {"index_key": "me-east-215-a", "attributes": {"id": "ignored-not-adoptable"}}
      ]
    }
  ]
}`

func TestGenerateAdoptionClaims_hw173_RealELB(t *testing.T) {
	out, err := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw173.omani.works", "huawei", "me-east-215")
	if err != nil {
		t.Fatalf("GenerateAdoptionClaims: %v", err)
	}
	got := string(out)

	// The whole point of #4002: the real ELB id must be carried as the
	// crossplane.io/external-name — that is the adoption binding.
	if !strings.Contains(got, "crossplane.io/external-name: d0e827ad-2e02-44b3-9b25-1513b7b771f4") {
		t.Errorf("expected ELB external-name annotation for the real hw173 ELB id; got:\n%s", got)
	}
	if !strings.Contains(got, "resourceId: d0e827ad-2e02-44b3-9b25-1513b7b771f4") {
		t.Errorf("expected ELB resourceId; got:\n%s", got)
	}
	if !strings.Contains(got, "resourceKind: loadbalancer") {
		t.Errorf("expected a loadbalancer CloudAdoption")
	}
	// Observe-first is the safety invariant — adoption must never
	// re-provision the live platform.
	if !strings.Contains(got, "manage: false") {
		t.Errorf("expected Observe-first manage:false on every claim")
	}
	if strings.Contains(got, "manage: true") {
		t.Errorf("no claim may be full-manage by default")
	}
	// The cloud + ProviderConfig wiring.
	if !strings.Contains(got, "cloud: huawei") {
		t.Errorf("expected cloud: huawei")
	}
	if !strings.Contains(got, "providerConfigRef:\n    name: default") {
		t.Errorf("expected providerConfigRef name default")
	}
	// Both control-plane nodes adopted (server kind, both ids).
	for _, id := range []string{
		"b3313987-d9aa-4ade-be60-d044c6cf1b4c",
		"f47b474d-f77a-4813-9452-a5fb2007dded",
	} {
		if !strings.Contains(got, "resourceId: "+id) {
			t.Errorf("expected control-plane server %s adopted", id)
		}
	}
	// VPC + subnet + EIP adopted.
	for _, id := range []string{
		"d25a49e8-ad3f-41eb-9b43-950a5582bf48", // vpc
		"5ce9b2ce-9099-4a66-a89b-de44efe8f2f7", // subnet
		"e350e859-c3bf-42a2-a356-716b6a76e052", // eip
	} {
		if !strings.Contains(got, "resourceId: "+id) {
			t.Errorf("expected %s adopted", id)
		}
	}
	// Non-adoptable resource (NAT gateway) is skipped.
	if strings.Contains(got, "ignored-not-adoptable") {
		t.Errorf("NAT gateway must NOT be adopted")
	}
	// Sovereign label stamped.
	if !strings.Contains(got, "catalyst.openova.io/sovereign: hw173.omani.works") {
		t.Errorf("expected sovereign label")
	}

	// Expected total adoptable count: 1 ELB + 2 CP + 1 VPC + 1 subnet + 1 EIP = 6.
	if n := strings.Count(got, "kind: CloudAdoption"); n != 6 {
		t.Errorf("expected 6 CloudAdoption docs, got %d:\n%s", n, got)
	}
}

// #5270 seam contract: on a kom4dc 2-VPC mimic the claim's region IS the
// pseudo-region (me-east-215-a / -b) — that value is the Kubernetes-side
// placement/label/region-affinity identity and the seeder must pass it
// through VERBATIM. The mimic->real mapping (me-east-215-a -> me-east-215)
// happens ONLY where the API endpoint host is composed: the cloudadoption
// module's hw_api_region local (platform/crossplane-claims chart 1.3.4,
// guarded by scripts/check-adoption-endpoint-region.sh). Stripping the
// suffix HERE would erase the claim's placement identity — this test pins
// the seam so #5270 is never "re-fixed" on the wrong side.
func TestGenerateAdoptionClaims_MimicRegionKeptOnClaim(t *testing.T) {
	out, err := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw278.omani.works", "huawei", "me-east-215-a")
	if err != nil {
		t.Fatalf("GenerateAdoptionClaims: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "region: me-east-215-a") {
		t.Errorf("claim must keep the mimic pseudo-region verbatim (placement/labels contract); got:\n%s", got)
	}
	// Every claim carries the mimic region — none may be silently mapped
	// to the real region by the seeder.
	if strings.Contains(got, "region: me-east-215\n") {
		t.Errorf("seeder must NOT strip the mimic -a suffix — endpoint mapping lives in the cloudadoption module, not the claim")
	}
}

func TestGenerateAdoptionClaims_Deterministic(t *testing.T) {
	a, _ := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw173.omani.works", "huawei", "me-east-215")
	b, _ := GenerateAdoptionClaims([]byte(hw173StateFixture), "hw173.omani.works", "huawei", "me-east-215")
	if string(a) != string(b) {
		t.Errorf("generator output must be deterministic across runs (stable GitOps diffs)")
	}
}

func TestGenerateAdoptionClaims_EmptyStateIsValidNotError(t *testing.T) {
	out, err := GenerateAdoptionClaims([]byte(`{"resources":[]}`), "hw173.omani.works", "huawei", "me-east-215")
	if err != nil {
		t.Fatalf("empty state must not error: %v", err)
	}
	if strings.Contains(string(out), "kind: CloudAdoption") {
		t.Errorf("empty state must emit no CloudAdoption objects")
	}
	if !strings.Contains(string(out), "no adoptable resources") {
		t.Errorf("empty state should note no adoptable resources")
	}
}

func TestGenerateAdoptionClaims_HetznerKinds(t *testing.T) {
	hz := `{"resources":[
      {"type":"hcloud_load_balancer","name":"main","instances":[{"index_key":null,"attributes":{"id":"99887","ipv4":"5.6.7.8"}}]},
      {"type":"hcloud_server","name":"control_plane","instances":[{"index_key":0,"attributes":{"id":"12345","ipv4_address":"1.2.3.4"}}]},
      {"type":"hcloud_network","name":"main","instances":[{"index_key":null,"attributes":{"id":"4242"}}]}
    ]}`
	out, err := GenerateAdoptionClaims([]byte(hz), "hz1.omani.works", "hetzner", "fsn1")
	if err != nil {
		t.Fatalf("hetzner gen: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "cloud: hetzner") {
		t.Errorf("expected cloud: hetzner")
	}
	for _, id := range []string{"99887", "12345", "4242"} {
		if !strings.Contains(got, "crossplane.io/external-name: "+id) {
			t.Errorf("expected hetzner resource %s adopted by external-name", id)
		}
	}
}

func TestGenerateAdoptionClaims_BadStateErrors(t *testing.T) {
	if _, err := GenerateAdoptionClaims([]byte(`{not json`), "x", "huawei", ""); err == nil {
		t.Errorf("expected error on unparseable state")
	}
}
