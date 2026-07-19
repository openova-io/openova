package huawei

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// memberOp records a member create (POST) or delete for assertions.
type memberOp struct {
	verb      string // "POST" | "DELETE"
	poolID    string
	address   string
	port      int
	id        string // for DELETE
	subnet    string // for POST: the subnet_cidr_id sent ("" == omitted)
	hasSubnet bool   // for POST: whether subnet_cidr_id was present at all
}

// fakeHCSGatewayELB mimics the ELB v3 endpoints ReconcileGatewayELBMembers
// walks: list LBs, get LB (pools + vip subnet + ip_target_enable), get pool
// (name + healthmonitor — with ID-ONLY member stubs, the live HCS shape
// proven on hw275), list pool members (the full member objects), member
// POST/DELETE, LB PUT (ip_target_enable), and healthmonitor PUT. It records
// every member op + LB PUT + monitor PUT port so the test asserts the
// reconcile converged the member SET correctly.
//
// Fixture shape (#4706 contract):
//   - live nodes: 10.42.0.10 / .11 / .12 (passed by the test)
//   - https pool: one member at a STALE 1.16.5-era nodePort (m-https-1,
//     .10:31443 → delete, re-add at :443), one member with an EMPTY address
//     (m-https-empty — the exact hw217 memberless-pool bug shape → delete),
//     one member already correct (m-https-ok, .12:443 → untouched). Node .11
//     and .10 are missing at :443 → added.
//   - http pool: one stale member (.10:31080 → delete); all 3 nodes added at :80.
//   - a console ELB that must never be touched.
func fakeHCSGatewayELB(t *testing.T, ops *[]memberOp, monitorPorts *[]int, lbPuts *[]string) func() {
	t.Helper()
	var mu sync.Mutex

	const lbList = `{"loadbalancers":[
	  {"id":"lb-gw","name":"catalyst-hw9-omani-works-5b413990-elb-primary"},
	  {"id":"lb-console","name":"catalyst-hw9-omani-works-5b413990-elb-console"}
	]}`
	const lbGetGW = `{"loadbalancer":{"vip_subnet_cidr_id":"subnet-vip","ip_target_enable":false,"pools":[{"id":"pool-https"},{"id":"pool-http"}]}}`
	// Pool GET carries ID-ONLY member stubs — the live HCS shape (hw275): the
	// reconciler MUST read the members-list endpoint, not these.
	const poolHTTPS = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-https",
	  "healthmonitor_id":"hm-https",
	  "members":[{"id":"m-https-1"},{"id":"m-https-empty"},{"id":"m-https-ok"}]}}`
	const poolHTTP = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-http",
	  "healthmonitor_id":"hm-http",
	  "members":[{"id":"m-http-1"}]}}`
	const membersHTTPS = `{"members":[
	    {"id":"m-https-1","address":"10.42.0.10","protocol_port":31443,"subnet_cidr_id":"subnet-1"},
	    {"id":"m-https-empty","address":"","protocol_port":443,"subnet_cidr_id":""},
	    {"id":"m-https-ok","address":"10.42.0.12","protocol_port":443,"subnet_cidr_id":"subnet-1"}
	  ]}`
	const membersHTTP = `{"members":[
	    {"id":"m-http-1","address":"10.42.0.10","protocol_port":31080,"subnet_cidr_id":"subnet-1"}
	  ]}`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/elb/loadbalancers"):
			_, _ = w.Write([]byte(lbList))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/loadbalancers/lb-gw"):
			_, _ = w.Write([]byte(lbGetGW))
		case r.Method == http.MethodPut && strings.HasSuffix(p, "/loadbalancers/lb-gw"):
			var body struct {
				LoadBalancer map[string]any `json:"loadbalancer"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			b, _ := json.Marshal(body.LoadBalancer)
			mu.Lock()
			*lbPuts = append(*lbPuts, string(b))
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-https/members"):
			_, _ = w.Write([]byte(membersHTTPS))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-http/members"):
			_, _ = w.Write([]byte(membersHTTP))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-https"):
			_, _ = w.Write([]byte(poolHTTPS))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-http"):
			_, _ = w.Write([]byte(poolHTTP))
		case r.Method == http.MethodDelete && strings.Contains(p, "/members/"):
			mu.Lock()
			parts := strings.Split(strings.TrimRight(p, "/"), "/")
			poolID := parts[len(parts)-3]
			*ops = append(*ops, memberOp{verb: "DELETE", poolID: poolID, id: parts[len(parts)-1]})
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/members"):
			var body struct {
				Member map[string]any `json:"member"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addr, _ := body.Member["address"].(string)
			portF, _ := body.Member["protocol_port"].(float64)
			subnet, hasSubnet := body.Member["subnet_cidr_id"].(string)
			mu.Lock()
			parts := strings.Split(strings.TrimRight(p, "/"), "/")
			poolID := parts[len(parts)-2]
			*ops = append(*ops, memberOp{verb: "POST", poolID: poolID, address: addr, port: int(portF), subnet: subnet, hasSubnet: hasSubnet})
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"member":{"id":"m-new"}}`))
		case r.Method == http.MethodPut && strings.Contains(p, "/healthmonitors/"):
			var body struct {
				HealthMonitor struct {
					MonitorPort int `json:"monitor_port"`
				} `json:"healthmonitor"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			*monitorPorts = append(*monitorPorts, body.HealthMonitor.MonitorPort)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
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

// TestReconcileGatewayELBMembers_ConvergesSetAtHostPorts is the core #4706
// contract: the member set converges to the live node IPs at the fixed
// gateway host ports (443/80); stale members (wrong port, gone node, EMPTY
// address — the hw217 memberless-pool bug shape) are removed; a member
// already correct is untouched; the console ELB is never touched; both
// pools' health monitors point at the gateway host port. Member state is
// read from the members-list endpoint (the pool GET's inline stubs carry no
// addresses on HCS — hw275).
func TestReconcileGatewayELBMembers_ConvergesSetAtHostPorts(t *testing.T) {
	var ops []memberOp
	var monitorPorts []int
	var lbPuts []string
	restore := fakeHCSGatewayELB(t, &ops, &monitorPorts, &lbPuts)
	defer restore()

	nodes := []string{"10.42.0.10", "10.42.0.11", "10.42.0.12"}
	p := New()
	changed, err := p.ReconcileGatewayELBMembers(context.Background(),
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works",
		nodes, nil, 443, 80, true, nil)
	if err != nil {
		t.Fatalf("ReconcileGatewayELBMembers: %v", err)
	}

	var posts, deletes []memberOp
	for _, o := range ops {
		switch o.verb {
		case "POST":
			posts = append(posts, o)
		case "DELETE":
			deletes = append(deletes, o)
		}
	}

	// https pool: delete m-https-1 (stale port 31443) + m-https-empty (empty
	// address). http pool: delete m-http-1 (stale port 31080). Total 3.
	if len(deletes) != 3 {
		t.Fatalf("DELETEs = %d (%v), want 3 (stale-port ×2 + empty-address ×1)", len(deletes), deletes)
	}
	for _, d := range deletes {
		if d.id == "m-https-ok" {
			t.Fatalf("deleted the already-correct member m-https-ok — must be left untouched")
		}
	}
	// https pool: add .10 + .11 at :443 (.12 already present). http pool: add
	// all 3 at :80. Total 5.
	if len(posts) != 5 {
		t.Fatalf("POSTs = %d (%v), want 5 (2 https + 3 http adds)", len(posts), posts)
	}
	for _, po := range posts {
		if po.address == "" {
			t.Fatalf("POSTed a member with an EMPTY address — the hw217 bug shape must be impossible")
		}
		if po.poolID == "pool-https" && po.port != 443 {
			t.Fatalf("https POST for %s got port %d, want 443", po.address, po.port)
		}
		if po.poolID == "pool-http" && po.port != 80 {
			t.Fatalf("http POST for %s got port %d, want 80", po.address, po.port)
		}
		if po.port >= 30000 {
			t.Fatalf("POSTed member port %d is in the NodePort range — nodePorts are FORBIDDEN (§854)", po.port)
		}
		if !po.hasSubnet || po.subnet == "" {
			t.Fatalf("primary-region POST for %s must carry a subnet_cidr_id, got %+v", po.address, po)
		}
	}
	if changed != len(posts)+len(deletes) {
		t.Fatalf("changed = %d, want %d (adds + removals)", changed, len(posts)+len(deletes))
	}
	// No peer IPs were supplied → the cross-VPC enable PUT must NOT fire.
	if len(lbPuts) != 0 {
		t.Fatalf("LB PUTs = %v, want none (no peer members requested)", lbPuts)
	}
	// Both health monitors point at the gateway host ports.
	if len(monitorPorts) != 2 {
		t.Fatalf("monitor PUTs = %d (%v), want 2", len(monitorPorts), monitorPorts)
	}
	sawHTTPS, sawHTTP := false, false
	for _, mp := range monitorPorts {
		if mp == 443 {
			sawHTTPS = true
		}
		if mp == 80 {
			sawHTTP = true
		}
	}
	if !sawHTTPS || !sawHTTP {
		t.Fatalf("monitor ports = %v, want to include both 443 and 80", monitorPorts)
	}
}

// TestReconcileGatewayELBMembers_CrossRegionMembers (#5244) — peer-region
// node IPs become cross-VPC IP-type members: the LB's ip_target_enable is
// flipped on first (it was false), and every peer member create OMITS
// subnet_cidr_id. Existing correct members (primary and peer) are untouched.
func TestReconcileGatewayELBMembers_CrossRegionMembers(t *testing.T) {
	var mu sync.Mutex
	var ops []memberOp
	var lbPuts []string
	const lbList = `{"loadbalancers":[{"id":"lb-gw","name":"catalyst-hw9-omani-works-5b413990-elb-primary"}]}`
	const lbGet = `{"loadbalancer":{"vip_subnet_cidr_id":"subnet-vip","ip_target_enable":false,"pools":[{"id":"pool-https"}]}}`
	const poolHTTPS = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-https",
	  "healthmonitor_id":"","members":[{"id":"m1"}]}}`
	// Primary member .10 already correct; peer members 10.43.0.20/.21 missing.
	const membersHTTPS = `{"members":[{"id":"m1","address":"10.42.0.10","protocol_port":443,"subnet_cidr_id":"subnet-1"}]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/elb/loadbalancers"):
			_, _ = w.Write([]byte(lbList))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/loadbalancers/lb-gw"):
			_, _ = w.Write([]byte(lbGet))
		case r.Method == http.MethodPut && strings.HasSuffix(p, "/loadbalancers/lb-gw"):
			var body struct {
				LoadBalancer map[string]any `json:"loadbalancer"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			b, _ := json.Marshal(body.LoadBalancer)
			mu.Lock()
			lbPuts = append(lbPuts, string(b))
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-https/members"):
			_, _ = w.Write([]byte(membersHTTPS))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-https"):
			_, _ = w.Write([]byte(poolHTTPS))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/members"):
			var body struct {
				Member map[string]any `json:"member"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addr, _ := body.Member["address"].(string)
			portF, _ := body.Member["protocol_port"].(float64)
			subnet, hasSubnet := body.Member["subnet_cidr_id"].(string)
			mu.Lock()
			ops = append(ops, memberOp{verb: "POST", address: addr, port: int(portF), subnet: subnet, hasSubnet: hasSubnet})
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"member":{"id":"m-new"}}`))
		case r.Method == http.MethodDelete && strings.Contains(p, "/members/"):
			mu.Lock()
			ops = append(ops, memberOp{verb: "DELETE"})
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	orig := endpointFor
	endpointFor = func(service, region string) string { return "https://" + host }
	defer func() { endpointFor = orig }()

	p := New()
	changed, err := p.ReconcileGatewayELBMembers(context.Background(),
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works",
		[]string{"10.42.0.10"}, []string{"10.43.0.20", "10.43.0.21"}, 443, 80, true, nil)
	if err != nil {
		t.Fatalf("ReconcileGatewayELBMembers: %v", err)
	}

	// ip_target_enable must be flipped on exactly once, before member adds.
	if len(lbPuts) != 1 || !strings.Contains(lbPuts[0], `"ip_target_enable":true`) {
		t.Fatalf("LB PUTs = %v, want exactly one enabling ip_target_enable", lbPuts)
	}
	var posts, deletes []memberOp
	for _, o := range ops {
		if o.verb == "POST" {
			posts = append(posts, o)
		} else {
			deletes = append(deletes, o)
		}
	}
	if len(deletes) != 0 {
		t.Fatalf("DELETEs = %d, want 0 (nothing stale)", len(deletes))
	}
	if len(posts) != 2 {
		t.Fatalf("POSTs = %d (%v), want 2 (both peer nodes)", len(posts), posts)
	}
	for _, po := range posts {
		if !strings.HasPrefix(po.address, "10.43.0.") {
			t.Fatalf("unexpected POST address %q — the primary member was already present", po.address)
		}
		if po.port != 443 {
			t.Fatalf("peer POST for %s got port %d, want 443 (the durable host port — NEVER a nodePort, §854)", po.address, po.port)
		}
		if po.hasSubnet {
			t.Fatalf("peer-region POST for %s must OMIT subnet_cidr_id (cross-VPC IP-type member), got subnet %q", po.address, po.subnet)
		}
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2", changed)
	}
}

// TestReconcileGatewayELBMembers_AddsOnlyWhenEnumerationIncomplete (#5244) —
// with sweepStale=false (a secondary region could not be enumerated —
// possibly mid region outage) the reconcile must NOT delete any member, even
// ones outside the supplied node set: the unlistable region's members may be
// the only ones carrying the EIP. Missing members are still added.
func TestReconcileGatewayELBMembers_AddsOnlyWhenEnumerationIncomplete(t *testing.T) {
	var mu sync.Mutex
	var ops []memberOp
	const lbList = `{"loadbalancers":[{"id":"lb-gw","name":"catalyst-hw9-omani-works-5b413990-elb-primary"}]}`
	// ip_target_enable already true (peer members were added by a prior cycle).
	const lbGet = `{"loadbalancer":{"vip_subnet_cidr_id":"subnet-vip","ip_target_enable":true,"pools":[{"id":"pool-https"}]}}`
	const poolHTTPS = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-https",
	  "healthmonitor_id":"","members":[{"id":"m-peer"}]}}`
	// A peer-region member from the unlistable region — must survive.
	const membersHTTPS = `{"members":[{"id":"m-peer","address":"10.43.0.20","protocol_port":443,"subnet_cidr_id":""}]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/elb/loadbalancers"):
			_, _ = w.Write([]byte(lbList))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/loadbalancers/lb-gw"):
			_, _ = w.Write([]byte(lbGet))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-https/members"):
			_, _ = w.Write([]byte(membersHTTPS))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pools/pool-https"):
			_, _ = w.Write([]byte(poolHTTPS))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/members"):
			var body struct {
				Member map[string]any `json:"member"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addr, _ := body.Member["address"].(string)
			mu.Lock()
			ops = append(ops, memberOp{verb: "POST", address: addr})
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"member":{"id":"m-new"}}`))
		case r.Method == http.MethodDelete && strings.Contains(p, "/members/"):
			mu.Lock()
			ops = append(ops, memberOp{verb: "DELETE"})
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	orig := endpointFor
	endpointFor = func(service, region string) string { return "https://" + host }
	defer func() { endpointFor = orig }()

	p := New()
	// Only the primary region was enumerable — sweepStale=false, no peer IPs.
	changed, err := p.ReconcileGatewayELBMembers(context.Background(),
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works",
		[]string{"10.42.0.10"}, nil, 443, 80, false, nil)
	if err != nil {
		t.Fatalf("ReconcileGatewayELBMembers: %v", err)
	}
	for _, o := range ops {
		if o.verb == "DELETE" {
			t.Fatalf("issued a DELETE with sweepStale=false — the unlistable region's member 10.43.0.20 would have been drained mid-outage")
		}
	}
	var posts []memberOp
	for _, o := range ops {
		if o.verb == "POST" {
			posts = append(posts, o)
		}
	}
	if len(posts) != 1 || posts[0].address != "10.42.0.10" {
		t.Fatalf("POSTs = %v, want exactly the missing primary member 10.42.0.10", posts)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1 (one add, zero removals)", changed)
	}
}

// TestReconcileGatewayELBMembers_Idempotent — a re-run when the member set
// already equals the live nodes at the host port is a no-op (0 changes,
// 0 DELETE/POST).
func TestReconcileGatewayELBMembers_Idempotent(t *testing.T) {
	var mu sync.Mutex
	var ops []memberOp
	const lbList = `{"loadbalancers":[{"id":"lb-gw","name":"catalyst-hw9-omani-works-5b413990-elb-primary"}]}`
	const lbGet = `{"loadbalancer":{"vip_subnet_cidr_id":"subnet-vip","ip_target_enable":false,"pools":[{"id":"pool-https"}]}}`
	const poolHTTPS = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-https",
	  "healthmonitor_id":"hm-https","members":[{"id":"m1"}]}}`
	const membersHTTPS = `{"members":[{"id":"m1","address":"10.42.0.10","protocol_port":443,"subnet_cidr_id":"subnet-1"}]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/elb/loadbalancers"):
			_, _ = w.Write([]byte(lbList))
		case strings.HasSuffix(r.URL.Path, "/loadbalancers/lb-gw"):
			_, _ = w.Write([]byte(lbGet))
		case strings.HasSuffix(r.URL.Path, "/pools/pool-https/members"):
			_, _ = w.Write([]byte(membersHTTPS))
		case strings.HasSuffix(r.URL.Path, "/pools/pool-https"):
			_, _ = w.Write([]byte(poolHTTPS))
		case r.Method == http.MethodDelete || r.Method == http.MethodPost:
			mu.Lock()
			ops = append(ops, memberOp{verb: r.Method})
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	orig := endpointFor
	endpointFor = func(service, region string) string { return "https://" + host }
	defer func() { endpointFor = orig }()

	p := New()
	changed, err := p.ReconcileGatewayELBMembers(context.Background(),
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works",
		[]string{"10.42.0.10"}, nil, 443, 80, true, nil)
	if err != nil {
		t.Fatalf("ReconcileGatewayELBMembers: %v", err)
	}
	if changed != 0 {
		t.Fatalf("changed = %d, want 0 (member set already converged)", changed)
	}
	for _, o := range ops {
		if o.verb == http.MethodDelete || o.verb == http.MethodPost {
			t.Fatalf("idempotent re-run issued a %s — must be a no-op", o.verb)
		}
	}
}

// TestReconcileGatewayELBMembers_NoGatewayELB — when no "-elb-primary" ELB
// exists, the reconcile returns an error (caller logs + retries).
func TestReconcileGatewayELBMembers_NoGatewayELB(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only a console ELB — no gateway ELB.
		_, _ = w.Write([]byte(`{"loadbalancers":[{"id":"lb-console","name":"catalyst-hw9-omani-works-5b413990-elb-console"}]}`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	orig := endpointFor
	endpointFor = func(service, region string) string { return "https://" + host }
	defer func() { endpointFor = orig }()

	p := New()
	_, err := p.ReconcileGatewayELBMembers(context.Background(),
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works",
		[]string{"10.42.0.10"}, nil, 443, 80, true, nil)
	if err == nil {
		t.Fatalf("expected an error when no -elb-primary ELB exists, got nil")
	}
	if !strings.Contains(err.Error(), "-elb-primary") {
		t.Fatalf("error %q should mention the missing -elb-primary ELB", err.Error())
	}
}

// TestReconcileGatewayELBMembers_RefusesEmptyNodeSet — an empty node union
// (no primary AND no peer IPs) must error out, never empty the pools (an
// empty pool = console 000, the exact hw217 failure).
func TestReconcileGatewayELBMembers_RefusesEmptyNodeSet(t *testing.T) {
	p := New()
	_, err := p.ReconcileGatewayELBMembers(context.Background(),
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works",
		nil, nil, 443, 80, true, nil)
	if err == nil {
		t.Fatalf("expected an error on an empty node set, got nil")
	}
	if !strings.Contains(err.Error(), "no node IPs") {
		t.Fatalf("error %q should say no node IPs were supplied", err.Error())
	}
}
