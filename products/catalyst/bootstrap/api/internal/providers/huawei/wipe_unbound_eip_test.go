// #4636, revised 22 November 2026 — the wipe's leftover check counts the
// addresses of the wiped deployment, the pre-flight's NAT addresses included,
// and never an address that carries no identity or another's.
//
// Found live wiping dep 8fd457a8 / omantel.biz on kom4dc: an UNBOUND, DOWN
// EIP (212.72.24.36) with no catalyst-<stem>- bandwidth name slipped past
// listEIPsByNamePrefix and verifyZeroOrphans. The project-wide unbound
// sweep that answered it also released other installs' fresh addresses and
// is withdrawn (wipe_unbound_eip.go); the addresses the engine itself
// allocates outside the module, the pre-flight's, carry the deployment's
// identity and are counted.

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
// (b) a BOUND EIP of the deployment (5b413990 of t99.omani.works);
// (c) the deployment's unbound NAT address the pre-flight allocated;
// (d) an UNBOUND nameless address — the 212.72.24.36 shape;
// (e) an unbound address of another install, not bound yet.
const publicIPsFixture = `{"publicips":[
  {"id":"eip-bastion","public_ip_address":"212.72.24.20","status":"ACTIVE","port_id":"port-bastion","bandwidth_name":"bastion-openova-bw"},
  {"id":"eip-catalyst-bound","public_ip_address":"212.72.24.40","status":"ACTIVE","port_id":"port-cp-1","bandwidth_name":"catalyst-t99-omani-works-5b413990-cp-bw"},
  {"id":"eip-rotated","public_ip_address":"212.72.24.41","status":"DOWN","port_id":"","bandwidth_name":"catalyst-5b413990-nat-preflight-1"},
  {"id":"eip-orphan","public_ip_address":"212.72.24.36","status":"DOWN","port_id":"","bandwidth_name":""},
  {"id":"eip-other","public_ip_address":"212.72.24.42","status":"DOWN","port_id":"","bandwidth_name":"catalyst-t98-omani-works-0c0c0c0c-nat-bw"}
]}`

// t99Scope is the selector of deployment 5b413990 of t99.omani.works.
func t99Scope(t *testing.T) deploymentScope {
	s, err := newDeploymentScope("t99.omani.works", "5b413990aa")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestVerifyZeroOrphans_CountsOnlyTheDeploymentsAddresses: the two
// addresses of the deployment are its residuals; the nameless address, the
// other install's and the bastion's are not.
func TestVerifyZeroOrphans_CountsOnlyTheDeploymentsAddresses(t *testing.T) {
	restore := fakeHCSPublicIPs(t, publicIPsFixture)
	defer restore()

	hw := hwCreds{AccessKey: "ak", SecretKey: "sk", ProjectID: "proj"}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": "me-east-215"}})
	out := &providers.WipeResult{}

	verifyZeroOrphans(context.Background(), client, hw, "me-east-215", t99Scope(t), func(string) {}, out)

	got := strings.Join(out.ResidualOrphans["floating_ips"], ",")
	if out.VerifiedZeroOrphans || got != "212.72.24.40,212.72.24.41" {
		t.Fatalf("VerifiedZeroOrphans %v, residual floating_ips %q; want false and the deployment's two addresses 212.72.24.40,212.72.24.41", out.VerifiedZeroOrphans, got)
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

	verifyZeroOrphans(context.Background(), client, hw, "me-east-215", t99Scope(t), func(string) {}, out)

	if !out.VerifiedZeroOrphans {
		t.Fatalf("VerifiedZeroOrphans=false on a clean (bastion-only) project; residuals=%v — bastion must never be flagged", out.ResidualOrphans)
	}
}
