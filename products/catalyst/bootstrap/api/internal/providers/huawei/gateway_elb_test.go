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
	verb    string // "POST" | "DELETE"
	poolID  string
	address string
	port    int
	id      string // for DELETE
}

// fakeHCSGatewayELB mimics the ELB v3 endpoints ReconcileGatewayELBMembers
// walks: list LBs, get LB (pools), get pool (members + healthmonitor), member
// POST/DELETE, and healthmonitor PUT. It records every member op + monitor PUT
// port so the test asserts the reconcile touched ONLY the mismatched members.
func fakeHCSGatewayELB(t *testing.T, ops *[]memberOp, monitorPorts *[]int) func() {
	t.Helper()
	var mu sync.Mutex

	// One gateway ELB ("...-elb-primary") + one console ELB ("...-elb-console").
	// The console ELB MUST be ignored (name suffix mismatch).
	const lbList = `{"loadbalancers":[
	  {"id":"lb-gw","name":"catalyst-hw9-omani-works-5b413990-elb-primary"},
	  {"id":"lb-console","name":"catalyst-hw9-omani-works-5b413990-elb-console"}
	]}`
	// The gateway LB has two pools: https + http.
	const lbGetGW = `{"loadbalancer":{"pools":[{"id":"pool-https"},{"id":"pool-http"}]}}`
	// https pool: two members on the placeholder port 31443 (need repoint→30111);
	//             one member ALREADY on 30111 (must be left untouched).
	const poolHTTPS = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-https",
	  "healthmonitor_id":"hm-https",
	  "members":[
	    {"id":"m-https-1","address":"10.42.0.10","protocol_port":31443,"subnet_cidr_id":"subnet-1"},
	    {"id":"m-https-2","address":"10.42.0.11","protocol_port":31443,"subnet_cidr_id":"subnet-1"},
	    {"id":"m-https-ok","address":"10.42.0.12","protocol_port":30111,"subnet_cidr_id":"subnet-1"}
	  ]}}`
	// http pool: one member on placeholder 31080 (need repoint→30222).
	const poolHTTP = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-http",
	  "healthmonitor_id":"hm-http",
	  "members":[
	    {"id":"m-http-1","address":"10.42.0.10","protocol_port":31080,"subnet_cidr_id":"subnet-1"}
	  ]}}`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/elb/loadbalancers"):
			_, _ = w.Write([]byte(lbList))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/loadbalancers/lb-gw"):
			_, _ = w.Write([]byte(lbGetGW))
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
				Member struct {
					Address      string `json:"address"`
					ProtocolPort int    `json:"protocol_port"`
				} `json:"member"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			parts := strings.Split(strings.TrimRight(p, "/"), "/")
			poolID := parts[len(parts)-2]
			*ops = append(*ops, memberOp{verb: "POST", poolID: poolID, address: body.Member.Address, port: body.Member.ProtocolPort})
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

// TestReconcileGatewayELBMembers_RepointsMismatchedOnly is the core contract:
// members on the placeholder port are DELETE+POST-recreated at the live
// nodePort; a member already on the live port is left untouched; the console
// ELB is never touched; and both pools' health monitors are repointed.
func TestReconcileGatewayELBMembers_RepointsMismatchedOnly(t *testing.T) {
	var ops []memberOp
	var monitorPorts []int
	restore := fakeHCSGatewayELB(t, &ops, &monitorPorts)
	defer restore()

	p := New()
	changed, err := p.ReconcileGatewayELBMembers(context.Background(),
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works",
		30111 /*https nodePort*/, 30222 /*http nodePort*/, nil)
	if err != nil {
		t.Fatalf("ReconcileGatewayELBMembers: %v", err)
	}

	// 2 https placeholder members + 1 http placeholder member = 3 repoints.
	// The already-on-30111 https member is NOT touched.
	if changed != 3 {
		t.Fatalf("changed = %d, want 3 (2 https + 1 http mismatched members)", changed)
	}

	// Every DELETE must be paired with a POST at the desired port.
	var posts []memberOp
	var deletes []memberOp
	for _, o := range ops {
		switch o.verb {
		case "POST":
			posts = append(posts, o)
		case "DELETE":
			deletes = append(deletes, o)
		}
	}
	if len(deletes) != 3 {
		t.Fatalf("DELETEs = %d (%v), want 3", len(deletes), deletes)
	}
	if len(posts) != 3 {
		t.Fatalf("POSTs = %d (%v), want 3", len(posts), posts)
	}
	// The already-correct member (id m-https-ok, address .12) must NOT be deleted.
	for _, d := range deletes {
		if d.id == "m-https-ok" {
			t.Fatalf("deleted the already-correct member m-https-ok — must be left untouched")
		}
	}
	// POST ports must be the live nodePorts (443→30111, 80→30222).
	for _, po := range posts {
		if po.poolID == "pool-https" && po.port != 30111 {
			t.Fatalf("https POST for %s got port %d, want 30111", po.address, po.port)
		}
		if po.poolID == "pool-http" && po.port != 30222 {
			t.Fatalf("http POST for %s got port %d, want 30222", po.address, po.port)
		}
	}
	// Both health monitors repointed (30111 for https pool, 30222 for http pool).
	if len(monitorPorts) != 2 {
		t.Fatalf("monitor PUTs = %d (%v), want 2", len(monitorPorts), monitorPorts)
	}
	sawHTTPS, sawHTTP := false, false
	for _, mp := range monitorPorts {
		if mp == 30111 {
			sawHTTPS = true
		}
		if mp == 30222 {
			sawHTTP = true
		}
	}
	if !sawHTTPS || !sawHTTP {
		t.Fatalf("monitor ports = %v, want to include both 30111 and 30222", monitorPorts)
	}
}

// TestReconcileGatewayELBMembers_Idempotent — a second run when all members are
// already on the live port is a no-op (0 changes, 0 DELETE/POST).
func TestReconcileGatewayELBMembers_Idempotent(t *testing.T) {
	// Fake HCS whose members are ALREADY on the live ports.
	var mu sync.Mutex
	var ops []memberOp
	const lbList = `{"loadbalancers":[{"id":"lb-gw","name":"catalyst-hw9-omani-works-5b413990-elb-primary"}]}`
	const lbGet = `{"loadbalancer":{"pools":[{"id":"pool-https"}]}}`
	const poolHTTPS = `{"pool":{"name":"catalyst-hw9-omani-works-5b413990-elb-pool-https",
	  "healthmonitor_id":"hm-https",
	  "members":[{"id":"m1","address":"10.42.0.10","protocol_port":30111,"subnet_cidr_id":"subnet-1"}]}}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/elb/loadbalancers"):
			_, _ = w.Write([]byte(lbList))
		case strings.HasSuffix(r.URL.Path, "/loadbalancers/lb-gw"):
			_, _ = w.Write([]byte(lbGet))
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
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works", 30111, 30222, nil)
	if err != nil {
		t.Fatalf("ReconcileGatewayELBMembers: %v", err)
	}
	if changed != 0 {
		t.Fatalf("changed = %d, want 0 (all members already on the live port)", changed)
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
		"ak", "sk", "proj", "me-east-215", "hw9.omani.works", 30111, 30222, nil)
	if err == nil {
		t.Fatalf("expected an error when no -elb-primary ELB exists, got nil")
	}
	if !strings.Contains(err.Error(), "-elb-primary") {
		t.Fatalf("error %q should mention the missing -elb-primary ELB", err.Error())
	}
}
