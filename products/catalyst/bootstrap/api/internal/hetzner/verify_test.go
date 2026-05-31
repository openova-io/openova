// verify_test.go — G103 (Refs #2670) Hetzner port unit tests.
//
// Pins the wire shape `VerifyReport.AsMap()` exposes to providers.
// WipeResult.ResidualOrphans so the catalyst-api wipe handler can
// surface the per-kind survivor list verbatim. The HTTP-bearing
// VerifyZeroOrphans path is exercised end-to-end by the existing
// purge_e2e_test.go suite; this file covers the pure data-shape
// branches that don't need a fake Hetzner server.
package hetzner

import (
	"reflect"
	"testing"
)

func TestVerifyReport_AsMap_EmptyReturnsEmpty(t *testing.T) {
	got := VerifyReport{}.AsMap()
	if len(got) != 0 {
		t.Fatalf("AsMap on empty report should be empty, got %d keys: %v", len(got), got)
	}
}

func TestVerifyReport_AsMap_OmitsEmptyKinds(t *testing.T) {
	// Only Servers populated → only "servers" key in the map.
	r := VerifyReport{
		Servers: []string{"catalyst-test-cp-1"},
	}
	got := r.AsMap()
	want := map[string][]string{
		"servers": {"catalyst-test-cp-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AsMap(Servers-only) = %v, want %v", got, want)
	}
}

func TestVerifyReport_AsMap_RoundTripsAllKinds(t *testing.T) {
	r := VerifyReport{
		Servers:       []string{"catalyst-test-cp-1"},
		LoadBalancers: []string{"catalyst-test-lb-1"},
		Networks:      []string{"catalyst-test-net-1"},
		Firewalls:     []string{"catalyst-test-fw-1"},
		SSHKeys:       []string{"catalyst-test-key-1"},
		Volumes:       []string{"catalyst-test-vol-1"},
		PrimaryIPs:    []string{"catalyst-test-pip-1"},
		FloatingIPs:   []string{"catalyst-test-fip-1"},
	}
	got := r.AsMap()
	wantKinds := []string{"servers", "load_balancers", "networks", "firewalls", "ssh_keys", "volumes", "primary_ips", "floating_ips"}
	for _, k := range wantKinds {
		if _, ok := got[k]; !ok {
			t.Errorf("AsMap missing kind %q", k)
		}
	}
	if r.Total() != len(wantKinds) {
		t.Errorf("Total() = %d, want %d", r.Total(), len(wantKinds))
	}
}

func TestVerifyReport_Total_ZeroWhenEmpty(t *testing.T) {
	if (VerifyReport{}).Total() != 0 {
		t.Fatal("empty report should have Total() == 0")
	}
}

func TestVerifyReport_Total_SumsAllKinds(t *testing.T) {
	r := VerifyReport{
		Servers:     []string{"a", "b"},
		Networks:    []string{"x"},
		FloatingIPs: []string{"p", "q", "r"},
	}
	// 2 + 1 + 3 = 6.
	if got := r.Total(); got != 6 {
		t.Fatalf("Total() = %d, want 6", got)
	}
}
