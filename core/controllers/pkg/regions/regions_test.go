package regions

import (
	"reflect"
	"testing"
)

// The live hw307 sovereign-fqdn/regionsJson value (2026-11-08).
const hw307 = `[{"cloudRegion":"hw-me-east-215-a-rtz-prod","controlPlaneSize":"m7n.xlarge.8","provider":"huawei","workerCount":5,"workerSize":"m7n.2xlarge.8"},{"cloudRegion":"hw-me-east-215-b-rtz-prod","controlPlaneSize":"m7n.xlarge.8","provider":"huawei","workerCount":5,"workerSize":"m7n.2xlarge.8"}]`

const (
	idA = "hw-me-east-215-a-rtz-prod"
	idB = "hw-me-east-215-b-rtz-prod"
)

func TestParse_ProvisioningShape(t *testing.T) {
	r, err := Parse(hw307)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.IDs(); !reflect.DeepEqual(got, []string{idA, idB}) {
		t.Fatalf("ids = %v", got)
	}
}

func TestParse_JSONStringWrapped(t *testing.T) {
	r, err := Parse(`"[{\"cloudRegion\":\"hw-me-east-215-a-rtz-prod\"}]"`)
	if err != nil || !reflect.DeepEqual(r.IDs(), []string{idA}) {
		t.Fatalf("Parse(wrapped) = %v, %v", r.IDs(), err)
	}
}

func TestParse_StringArrayAndEmpty(t *testing.T) {
	r, err := Parse(`["x-a-rtz-prod","x-a-rtz-prod","y"]`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Len() != 2 {
		t.Fatalf("duplicates must collapse: %v", r.IDs())
	}
	for _, raw := range []string{"", "null", "[]"} {
		r, err := Parse(raw)
		if err != nil || r.Len() != 0 {
			t.Fatalf("Parse(%q) = %v, %v", raw, r.IDs(), err)
		}
	}
	if _, err := Parse("{"); err == nil {
		t.Fatal("malformed JSON must error")
	}
}

func TestFromEnv_FallsBackToOwnRegion(t *testing.T) {
	t.Setenv(EnvRegionsJSON, "")
	t.Setenv(EnvRegionID, idA)
	r, err := FromEnv()
	if err != nil || !reflect.DeepEqual(r.IDs(), []string{idA}) {
		t.Fatalf("FromEnv = %v, %v", r.IDs(), err)
	}
}

func TestCanonical(t *testing.T) {
	r, _ := Parse(hw307)
	ok := map[string]string{
		idA:             idA,
		idB:             idB,
		"rtz-A":         idA,
		"rtz-B":         idB,
		"mgmt-b":        idB,
		"dmz-A":         idA,
		"me-east-215-a": idA,
		"me-east-215-b": idB,
	}
	for in, want := range ok {
		got, err := r.Canonical(in)
		if err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "primary", "A", "B", "meea", "rtz-C", "platform-bootstrap-owned-host", "me-east-215", "rtz", "hz-fsn-rtz-prod"} {
		if got, err := r.Canonical(bad); err == nil {
			t.Errorf("Canonical(%q) = %q, want error", bad, got)
		}
	}
}

func TestCanonical_HetznerBareRegion(t *testing.T) {
	r := New("hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	if got, err := r.Canonical("hel"); err != nil || got != "hz-hel-rtz-prod" {
		t.Fatalf("Canonical(hel) = %q, %v", got, err)
	}
	if _, err := r.Canonical("prod"); err == nil {
		t.Fatal("a bb/env token is not a region")
	}
}

func TestPeers(t *testing.T) {
	if got := Peers(idA, []string{idA, idB}); !reflect.DeepEqual(got, []string{idB}) {
		t.Fatalf("Peers = %v", got)
	}
	if got := Peers(idA, []string{idA}); len(got) != 0 {
		t.Fatalf("singleton peers = %v", got)
	}
}
