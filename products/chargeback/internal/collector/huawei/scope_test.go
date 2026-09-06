package huawei

import "testing"

func res(id, kind, name string, attrs map[string]any) Resource {
	if attrs == nil {
		attrs = map[string]any{}
	}
	return Resource{ID: id, Kind: kind, Name: name, Attrs: attrs}
}

// The defect: shared infrastructure in the same project lands on the
// customer's bill. Measured on hw307 — bastion-openova billed to Omantel.
func TestScopeExcludesSharedInfrastructure(t *testing.T) {
	m := ScopeMatcher{Token: "9a1f230f"}
	in, out := m.Partition([]Resource{
		res("s1", KindECS, "catalyst-hw307-omani-works-9a1f230f-me-east-215-a-cp1", nil),
		res("b1", KindECS, "bastion-openova", nil),
	})
	if len(in) != 1 || in[0].ID != "s1" {
		t.Fatalf("in-scope = %+v, want only the Sovereign node", in)
	}
	if len(out) != 1 || out[0].Name != "bastion-openova" {
		t.Fatalf("excluded = %+v, want the bastion", out)
	}
}

// A Kubernetes PV is named `pvc-<uuid>` and carries no deployment token. It is
// attributable ONLY through the server it is attached to — 92 of 104 volumes
// on hw307 are this shape, so dropping them would under-bill enormously.
func TestScopeAdoptsVolumesViaTheirServer(t *testing.T) {
	m := ScopeMatcher{Token: "9a1f230f"}
	in, out := m.Partition([]Resource{
		res("s1", KindECS, "catalyst-hw307-9a1f230f-w1", nil),
		res("v1", KindEVS, "pvc-94872fd3-23a7-4504", map[string]any{"attached_to": "s1"}),
	})
	if len(in) != 2 {
		t.Fatalf("in-scope = %d, want 2 — the PV was not adopted via its server", len(in))
	}
	if len(out) != 0 {
		t.Fatalf("excluded = %+v, want none", out)
	}
}

// A volume attached to a FOREIGN server must stay out, or the bastion's disks
// ride in on the customer's bill.
func TestScopeDoesNotAdoptVolumesOfForeignServers(t *testing.T) {
	m := ScopeMatcher{Token: "9a1f230f"}
	in, out := m.Partition([]Resource{
		res("b1", KindECS, "bastion-openova", nil),
		res("v9", KindEVS, "pvc-deadbeef", map[string]any{"attached_to": "b1"}),
	})
	if len(in) != 0 {
		t.Fatalf("in-scope = %+v, want none", in)
	}
	if len(out) != 2 {
		t.Fatalf("excluded = %d, want both the foreign server and its volume", len(out))
	}
}

// EIPs carry NO name on this cloud; their bandwidth is named after what they
// serve. 0 of 7 would match on name alone.
func TestScopeAttributesEIPsByBandwidthName(t *testing.T) {
	m := ScopeMatcher{Token: "9a1f230f"}
	in, out := m.Partition([]Resource{
		res("e1", KindEIP, "", map[string]any{"bandwidth_name": "catalyst-hw307-omani-works-9a1f230f-me-east-215-a-nat-bw"}),
		res("e2", KindEIP, "", map[string]any{"bandwidth_name": "bastion-openova-bw"}),
	})
	if len(in) != 1 || in[0].ID != "e1" {
		t.Fatalf("in-scope = %+v, want only the Sovereign EIP", in)
	}
	if len(out) != 1 || out[0].ID != "e2" {
		t.Fatalf("excluded = %+v, want the bastion EIP", out)
	}
}

// A source with no scope configured must bill exactly as before. Silently
// dropping resources because scope was unset would be worse than the defect.
func TestUnscopedSourceMatchesEverything(t *testing.T) {
	m := ScopeMatcher{}
	in, out := m.Partition([]Resource{
		res("s1", KindECS, "anything", nil),
		res("b1", KindECS, "bastion-openova", nil),
	})
	if len(in) != 2 || len(out) != 0 {
		t.Fatalf("unscoped: in=%d out=%d, want everything in scope", len(in), len(out))
	}
}
