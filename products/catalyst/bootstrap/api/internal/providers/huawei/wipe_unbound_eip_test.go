// #4636 — regression guard for the UNBOUND / nameless EIP wipe leak.
//
// Found live wiping dep 8fd457a8 / omantel.biz on kom4dc: an UNBOUND, DOWN
// EIP (212.72.24.36) with no catalyst-<stem>- bandwidth name slipped past
// listEIPsByNamePrefix and verifyZeroOrphans → wipe falsely reported
// verifiedZeroOrphans:true → orphan EIP leaked → publicIp quota leak (the
// #4614 reap class). These tests assert the project-wide unbound sweep
// catches such an orphan while NEVER flagging the bastion EIP.

package huawei

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// fakeHCSPublicIPs stands up a httptest server that serves a fixed
// `GET /v1/{proj}/publicips` listing and overrides the package
// `endpointFor` so every Huawei call in the package routes at it. Any
// other path (os-keypairs, ELB list, NAT list, …) returns an empty JSON
// object so the unrelated residual scans in verifyZeroOrphans read empty.
// Returns a restore func.
func fakeHCSPublicIPs(t *testing.T, publicipsBody string) func() {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/publicips"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(publicipsBody))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	host := strings.TrimPrefix(srv.URL, "https://")
	orig := endpointFor
	endpointFor = func(service, region string) string { return "https://" + host }
	return func() {
		endpointFor = orig
		srv.Close()
	}
}

// publicIPsFixture: (a) the bastion EIP — BOUND, literal sacred IP;
// (b) a BOUND catalyst EIP (in-use, name-matched, has a port);
// (c) an UNBOUND nameless orphan — the 212.72.24.36 leak shape (DOWN,
// no port_id, no catalyst- bandwidth name).
const publicIPsFixture = `{"publicips":[
  {"id":"eip-bastion","public_ip_address":"212.72.24.20","status":"ACTIVE","port_id":"port-bastion","bandwidth_name":"bastion-openova-bw"},
  {"id":"eip-catalyst-bound","public_ip_address":"212.72.24.40","status":"ACTIVE","port_id":"port-cp-1","bandwidth_name":"catalyst-t99-omani-works-5b413990-cp-bw"},
  {"id":"eip-orphan","public_ip_address":"212.72.24.36","status":"DOWN","port_id":"","bandwidth_name":""}
]}`

// TestListUnboundOrphanEIPs_OnlyOrphan asserts the project-wide sweep
// returns EXACTLY the unbound nameless orphan: the bastion (bound + sacred
// IP) and the bound catalyst EIP are both excluded.
func TestListUnboundOrphanEIPs_OnlyOrphan(t *testing.T) {
	restore := fakeHCSPublicIPs(t, publicIPsFixture)
	defer restore()

	hw := hwCreds{AccessKey: "ak", SecretKey: "sk", ProjectID: "proj"}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": "me-east-215"}})

	got, err := listUnboundOrphanEIPs(context.Background(), client, hw, "me-east-215")
	if err != nil {
		t.Fatalf("listUnboundOrphanEIPs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d orphan EIP(s) %+v, want exactly 1 (the unbound nameless orphan)", len(got), got)
	}
	if got[0].Address != "212.72.24.36" || got[0].ID != "eip-orphan" {
		t.Fatalf("orphan = %+v, want {ID:eip-orphan Address:212.72.24.36}", got[0])
	}
	for _, e := range got {
		if e.Address == bastionEIPAddress {
			t.Fatalf("bastion EIP %s was flagged as an orphan — HARD bastion-protection violated", e.Address)
		}
	}
}

// TestVerifyZeroOrphans_FlagsUnboundEIP wires the sweep through the real
// verifyZeroOrphans path: the unbound nameless orphan must land in
// ResidualOrphans["floating_ips"] and flip VerifiedZeroOrphans=false,
// while the bastion EIP is NEVER recorded.
func TestVerifyZeroOrphans_FlagsUnboundEIP(t *testing.T) {
	restore := fakeHCSPublicIPs(t, publicIPsFixture)
	defer restore()

	hw := hwCreds{AccessKey: "ak", SecretKey: "sk", ProjectID: "proj"}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": "me-east-215"}})
	out := &providers.WipeResult{}

	verifyZeroOrphans(context.Background(), client, hw, "me-east-215", "t99.omani.works", func(string) {}, out)

	if out.VerifiedZeroOrphans {
		t.Fatalf("VerifiedZeroOrphans=true, but an unbound orphan EIP (212.72.24.36) survived — the wipe-contract gate must FAIL")
	}
	fips := out.ResidualOrphans["floating_ips"]
	found := false
	for _, a := range fips {
		if a == bastionEIPAddress {
			t.Fatalf("bastion EIP %s recorded as a residual orphan — HARD bastion-protection violated", a)
		}
		if a == "212.72.24.36" {
			found = true
		}
	}
	if !found {
		t.Fatalf("residual floating_ips = %v, want it to include the unbound orphan 212.72.24.36", fips)
	}
}

// TestVerifyZeroOrphans_CleanProjectPasses confirms NO false positive: a
// project whose only EIP is the bastion (bound, sacred) reports zero
// orphans — verifyZeroOrphans must NOT flag the bastion.
func TestVerifyZeroOrphans_CleanProjectPasses(t *testing.T) {
	const onlyBastion = `{"publicips":[
      {"id":"eip-bastion","public_ip_address":"212.72.24.20","status":"ACTIVE","port_id":"port-bastion","bandwidth_name":"bastion-openova-bw"}
    ]}`
	restore := fakeHCSPublicIPs(t, onlyBastion)
	defer restore()

	hw := hwCreds{AccessKey: "ak", SecretKey: "sk", ProjectID: "proj"}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": "me-east-215"}})
	out := &providers.WipeResult{}

	verifyZeroOrphans(context.Background(), client, hw, "me-east-215", "t99.omani.works", func(string) {}, out)

	if !out.VerifiedZeroOrphans {
		t.Fatalf("VerifiedZeroOrphans=false on a clean (bastion-only) project; residuals=%v — bastion must never be flagged", out.ResidualOrphans)
	}
}
