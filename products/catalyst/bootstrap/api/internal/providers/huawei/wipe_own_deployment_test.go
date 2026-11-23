package huawei

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// The project of these tests holds, beside the bastion:
//   - A, deployment aaaaaaaa11 of t1.omani.works, the one that is wiped;
//   - B, deployment bbbbbbbb22 of the SAME Sovereign, live;
//   - C, an install of t2.omani.works whose fresh addresses are not bound yet;
//   - an unbound address without a name, reserved by someone else.
//
// Every name is the one infra/providers/huawei/main.tf renders
// (local.name_prefix = catalyst-<fqdn, dots as dashes>-<dep8>), or the
// pre-flight's NAT address name.
const (
	owFQDN, owA, owB = "t1.omani.works", "aaaaaaaa11", "bbbbbbbb22"
	owPA, owPB, owPC = "catalyst-t1-omani-works-aaaaaaaa", "catalyst-t1-omani-works-bbbbbbbb", "catalyst-t2-omani-works-cccccccc"
)

// owRes is one cloud object of the fake; owner is A, B, C, D, bastion or
// none.
type owRes struct {
	kind, id, name, owner, vpc string
	bound, deleted             bool
}

// owCloud is a fake of the HCS API the wipe and the reclaim call. A call
// that changes an object whose owner is not `may` fails the test.
type owCloud struct {
	t   *testing.T
	mu  sync.Mutex
	res []*owRes
	may string
}

func owInventory() []*owRes {
	r := func(kind, id, name, owner, vpc string, bound bool) *owRes {
		return &owRes{kind: kind, id: id, name: name, owner: owner, vpc: vpc, bound: bound}
	}
	out := []*owRes{}
	for _, d := range []struct{ owner, p string }{{"A", owPA}, {"B", owPB}} {
		o, p := d.owner, d.p
		out = append(out,
			r("server", o+"-cp", p+"-me-east-215-a-cp1-0a1b2c", o, o+"-vpc", false),
			r("lb", o+"-lb", p+"-elb-primary", o, "", false),
			r("nat", o+"-nat", p+"-me-east-215-a-nat", o, "", false),
			r("eip", o+"-eip", p+"-me-east-215-a-cp1-bw", o, "", true),
			r("sg", o+"-sg", p+"-me-east-215-a-sg", o, "", false),
			r("vpc", o+"-vpc", p+"-me-east-215-a-vpc", o, "", false),
			r("subnet", o+"-subnet", p+"-me-east-215-a-subnet", o, o+"-vpc", false),
			r("peering", o+"-peer", p+"-peer-1a2b3c4d", o, o+"-vpc", false),
			r("keypair", p+"-key", p+"-key", o, "", false))
	}
	return append(out,
		// The pre-flight's NAT address of A, allocated outside the state.
		r("eip", "A-rotated", "catalyst-aaaaaaaa-nat-preflight-1", "A", "", false),
		// C's addresses: created, DOWN, not bound yet.
		r("eip", "C-nat", owPC+"-me-east-215-a-nat-bw", "C", "", false),
		r("eip", "C-cp", owPC+"-me-east-215-a-cp1-bw", "C", "", false),
		r("eip", "none", "", "none", "", false),
		r("eip", "bastion-eip", "bastion-bw", "bastion", "", true),
		r("server", "bastion", "bastion-openova", "bastion", "bastion-vpc", false),
		r("vpc", "bastion-vpc", "bastion-vpc", "bastion", "", false))
}

func (c *owCloud) find(kind, id string) *owRes {
	for _, x := range c.res {
		if x.kind == kind && (x.id == id || kind == "keypair" && x.name == id) && !x.deleted {
			return x
		}
	}
	return nil
}

func (c *owCloud) alive(kind string) []*owRes {
	var out []*owRes
	for _, x := range c.res {
		if x.kind == kind && !x.deleted {
			out = append(out, x)
		}
	}
	return out
}

// touch deletes an object, and fails the test when it is not `may`'s.
func (c *owCloud) touch(w http.ResponseWriter, kind, id, call string) {
	x := c.find(kind, id)
	if x == nil {
		bdAnswer(w, 404, nil)
		return
	}
	if x.owner != c.may {
		c.t.Errorf("%s: %s %q of %s, in an action for %s", call, kind, x.name, x.owner, c.may)
	}
	x.deleted = true
	bdAnswer(w, 204, nil)
}

func (c *owCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, m := r.URL.Path, r.Method
	seg := strings.Split(strings.Trim(p, "/"), "/")
	last := seg[len(seg)-1]
	call := m + " " + p
	named := func(kind, key string) {
		list := []map[string]any{}
		for _, x := range c.alive(kind) {
			list = append(list, map[string]any{"id": x.id, "name": x.name})
		}
		bdAnswer(w, 200, map[string]any{key: list})
	}
	switch {
	case m == http.MethodPost && last == "action": // the server listing by tag: tags are off on HCS
		bdAnswer(w, 200, map[string]any{"resources": []any{}})
	case m == http.MethodGet && strings.HasSuffix(p, "/cloudservers/detail"):
		list := []map[string]any{}
		for _, x := range c.alive("server") {
			list = append(list, map[string]any{"id": x.id, "name": x.name, "status": "ACTIVE", "flavor": map[string]string{"id": "s7n"}})
		}
		bdAnswer(w, 200, map[string]any{"servers": list})
	case m == http.MethodGet && len(seg) >= 2 && seg[len(seg)-2] == "cloudservers":
		if x := c.find("server", last); x != nil {
			bdAnswer(w, 200, map[string]any{"server": map[string]any{"name": x.name, "metadata": map[string]string{"vpc_id": x.vpc}}})
			return
		}
		bdAnswer(w, 404, nil)
	case m == http.MethodPost && strings.HasSuffix(p, "/cloudservers/delete"):
		var body struct {
			Servers []struct {
				ID string `json:"id"`
			} `json:"servers"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, s := range body.Servers {
			if x := c.find("server", s.ID); x != nil {
				if x.owner != c.may {
					c.t.Errorf("%s: server %q of %s, in an action for %s", call, x.name, x.owner, c.may)
				}
				x.deleted = true
			}
		}
		bdAnswer(w, 200, map[string]any{"job_id": "j"})
	case m == http.MethodGet && strings.HasSuffix(p, "/ports"):
		list := []map[string]any{}
		for _, x := range c.alive("server") {
			list = append(list, map[string]any{"id": "port-" + x.id, "device_owner": "compute:az1", "device_id": x.id})
		}
		bdAnswer(w, 200, map[string]any{"ports": list})
	case m == http.MethodGet && strings.HasSuffix(p, "/elb/loadbalancers"):
		named("lb", "loadbalancers")
	case m == http.MethodGet && strings.Contains(p, "/elb/loadbalancers/") && c.find("lb", last) != nil:
		bdAnswer(w, 200, map[string]any{"loadbalancer": map[string]any{"id": last, "listeners": []any{}, "pools": []any{}}})
	case m == http.MethodDelete && strings.Contains(p, "/elb/loadbalancers/"):
		c.touch(w, "lb", last, call)
	case m == http.MethodGet && strings.HasSuffix(p, "/nat_gateways"):
		named("nat", "nat_gateways")
	case m == http.MethodGet && (last == "snat_rules" || last == "dnat_rules"):
		bdAnswer(w, 200, map[string]any{last: []any{}})
	case m == http.MethodDelete && len(seg) >= 2 && seg[len(seg)-2] == "nat_gateways":
		c.touch(w, "nat", last, call)
	case m == http.MethodGet && strings.HasSuffix(p, "/publicips"):
		list := []map[string]any{}
		for i, x := range c.res {
			if x.kind != "eip" || x.deleted {
				continue
			}
			addr, port, status := fmt.Sprintf("198.51.100.%d", i+1), "", "DOWN"
			if x.owner == "bastion" {
				addr = bastionEIPAddress
			}
			if x.bound {
				port, status = "port-"+x.id, "ACTIVE"
			}
			list = append(list, map[string]any{"id": x.id, "public_ip_address": addr, "status": status, "port_id": port, "bandwidth_name": x.name})
		}
		bdAnswer(w, 200, map[string]any{"publicips": list})
	case m == http.MethodDelete && len(seg) >= 2 && seg[len(seg)-2] == "publicips":
		c.touch(w, "eip", last, call)
	case m == http.MethodGet && strings.HasSuffix(p, "/security-groups"):
		named("sg", "security_groups")
	case m == http.MethodDelete && len(seg) >= 2 && seg[len(seg)-2] == "security-groups":
		c.touch(w, "sg", last, call)
	case m == http.MethodGet && strings.HasSuffix(p, "/vpcs"):
		named("vpc", "vpcs")
	case m == http.MethodDelete && len(seg) >= 2 && seg[len(seg)-2] == "subnets" && strings.Contains(p, "/vpcs/"):
		c.touch(w, "subnet", last, call)
	case m == http.MethodDelete && len(seg) >= 2 && seg[len(seg)-2] == "vpcs":
		c.touch(w, "vpc", last, call)
	case m == http.MethodGet && strings.HasSuffix(p, "/v2.0/vpc/peerings"):
		list := []map[string]any{}
		for _, x := range c.alive("peering") {
			list = append(list, map[string]any{"id": x.id, "name": x.name, "request_vpc_info": map[string]string{"vpc_id": x.vpc},
				"accept_vpc_info": map[string]string{"vpc_id": x.vpc}})
		}
		bdAnswer(w, 200, map[string]any{"peerings": list})
	case m == http.MethodDelete && strings.Contains(p, "/v2.0/vpc/peerings/"):
		c.touch(w, "peering", last, call)
	case m == http.MethodGet && strings.HasSuffix(p, "/subnets"): // the VPC layer's and Neutron's
		list := []map[string]any{}
		for _, x := range c.alive("subnet") {
			if strings.Contains(p, "/v2.0/") {
				continue // the Neutron records went with the VPC layer's here
			}
			list = append(list, map[string]any{"id": x.id, "name": x.name, "vpc_id": x.vpc})
		}
		bdAnswer(w, 200, map[string]any{"subnets": list})
	case m == http.MethodGet && strings.HasSuffix(p, "/floatingips"):
		bdAnswer(w, 200, map[string]any{"floatingips": []any{}})
	case m == http.MethodGet && strings.HasSuffix(p, "/os-keypairs"):
		list := []map[string]any{}
		for _, x := range c.alive("keypair") {
			list = append(list, map[string]any{"keypair": map[string]string{"name": x.name}})
		}
		bdAnswer(w, 200, map[string]any{"keypairs": list})
	case m == http.MethodDelete && len(seg) >= 2 && seg[len(seg)-2] == "os-keypairs":
		c.touch(w, "keypair", last, call)
	default:
		c.t.Errorf("unexpected %s", call)
		bdAnswer(w, 405, nil)
	}
}

// owServe points the API seam at cloud and the object store at an S3 fake
// that holds A's bucket, keeps the released addresses' cooldown store in
// the test's folder.
func owServe(t *testing.T, cloud *owCloud) {
	s3 := &obsFakeS3{bucket: owPA, exists: true}
	api, obs := httptest.NewTLSServer(cloud), httptest.NewTLSServer(s3.handler())
	origAPI, origOBS := endpointFor, obsEndpointFor
	endpointFor = func(string, string) string { return api.URL }
	obsEndpointFor = func(string) string { return strings.TrimPrefix(obs.URL, "https://") }
	t.Setenv(eipBlocklistFileEnv, filepath.Join(t.TempDir(), "nat-eip-state.json"))
	t.Cleanup(func() {
		endpointFor, obsEndpointFor = origAPI, origOBS
		api.Close()
		obs.Close()
	})
}

// owLeft names what of `owner` the cloud still holds, and what of it is gone.
func (c *owCloud) owLeft(owner string) (alive, gone []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range c.res {
		if x.owner != owner {
			continue
		}
		if x.deleted {
			gone = append(gone, x.kind+" "+x.name)
		} else {
			alive = append(alive, x.kind+" "+x.name)
		}
	}
	return alive, gone
}

// Two deployments of one Sovereign, and an install of another whose
// addresses are not bound yet: the wipe of A removes every object of A, its
// pre-flight NAT address included, and touches no object of B, of C, of
// nobody or of the bastion; it verifies zero orphans while B lives.
func TestWipe_TouchesOnlyItsOwnDeployment(t *testing.T) {
	cloud := &owCloud{t: t, res: owInventory(), may: "A"}
	owServe(t, cloud)
	orig := tofuDestroy
	tofuDestroy = func(context.Context, provisioner.Request, chan<- provisioner.Event) error { return nil }
	t.Cleanup(func() { tofuDestroy = orig })

	out, err := New().Wipe(context.Background(), providers.WipeSpec{DeploymentID: owA, SovereignFQDN: owFQDN,
		Creds: providers.ProviderCreds{Raw: map[string]string{"access_key": "ak", "secret_key": "sk", "project_id": "proj"}}}, nil)
	if err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	if alive, _ := cloud.owLeft("A"); len(alive) != 0 {
		t.Errorf("after the wipe of A the cloud still holds %v of A", alive)
	}
	for _, o := range []string{"B", "C", "none", "bastion"} {
		if _, gone := cloud.owLeft(o); len(gone) != 0 {
			t.Errorf("the wipe of A removed %v of %s", gone, o)
		}
	}
	if !out.VerifiedZeroOrphans || len(out.ResidualOrphans) != 0 {
		t.Errorf("VerifiedZeroOrphans %v, residuals %v; want true and none: the objects of B, C and nobody are not A's orphans", out.VerifiedZeroOrphans, out.ResidualOrphans)
	}
}

// A wipe without the deployment's identity is refused before any call: it
// could only select by the Sovereign's name, which every deployment of it
// shares.
func TestWipe_RefusesWithoutTheDeploymentsIdentity(t *testing.T) {
	cloud := &owCloud{t: t, res: owInventory(), may: "nobody"}
	owServe(t, cloud)
	orig := tofuDestroy
	tofuDestroy = func(context.Context, provisioner.Request, chan<- provisioner.Event) error {
		t.Error("the destroy ran for a wipe without the deployment's identity")
		return nil
	}
	t.Cleanup(func() { tofuDestroy = orig })
	_, err := New().Wipe(context.Background(), providers.WipeSpec{DeploymentID: "", SovereignFQDN: owFQDN,
		Creds: providers.ProviderCreds{Raw: map[string]string{"access_key": "ak", "secret_key": "sk", "project_id": "proj"}}}, nil)
	if err == nil {
		t.Fatal("Wipe with no deployment id: want a refusal")
	}
}

// The engine restarted between an install and its reclaim, so it remembers
// no deployment (no protected prefix). The reclaim deletes the VPC of D,
// which holds no server, and touches neither B's servers nor B's VPC nor
// its peering: a VPC that holds a server is not an orphan.
func TestSweepOrphanVPCs_AfterARestartDeletesNothingForeign(t *testing.T) {
	res := owInventory()
	res = append(res, &owRes{kind: "vpc", id: "D-vpc", name: "catalyst-t9-omani-works-dddddddd-me-east-215-a-vpc", owner: "D"},
		&owRes{kind: "subnet", id: "D-subnet", name: "catalyst-t9-omani-works-dddddddd-me-east-215-a-subnet", owner: "D", vpc: "D-vpc"})
	// A is gone already; only B and D carry a catalyst- VPC.
	for _, x := range res {
		if x.owner == "A" {
			x.deleted = true
		}
	}
	cloud := &owCloud{t: t, res: res, may: "D"}
	owServe(t, cloud)
	n, err := New().SweepOrphanVPCs(context.Background(), "ak", "sk", "proj", defaultRegion, map[string]struct{}{}, true, nil)
	if err != nil {
		t.Fatalf("SweepOrphanVPCs: %v", err)
	}
	if alive, _ := cloud.owLeft("D"); n != 1 || len(alive) != 0 {
		t.Errorf("reclaimed %d VPCs, D still holds %v; want 1 and nothing", n, alive)
	}
	for _, o := range []string{"B", "C", "bastion"} {
		if _, gone := cloud.owLeft(o); len(gone) != 0 {
			t.Errorf("the reclaim removed %v of %s", gone, o)
		}
	}
}
