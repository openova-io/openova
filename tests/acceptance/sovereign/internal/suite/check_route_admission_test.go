package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// raFake is the cluster of the NET-G11 tests: the lists of every region, what
// each Gateway reports on an ISO-7 route (absent: no status), whether writing
// it fails, and region/namespace/name of each ISO-7 route written and deleted.
type raFake struct {
	gateways, routes string
	verdict          map[string]string
	refuse           bool
	applied, deleted []string
}

const raSelector = `{"namespaces": {"from": "Selector", "selector": {"matchExpressions": [{"key": "kubernetes.io/metadata.name", "operator": "In", "values": ["acme", "catalyst-system"]}]}}}`

func raGateways(orgFrom string) string {
	return `{"items": [{"metadata": {"name": "cilium-gateway", "namespace": "kube-system"}, "spec": {"listeners": [
	  {"name": "https", "allowedRoutes": ` + raSelector + `}, {"name": "http", "allowedRoutes": ` + raSelector + `}]}},
	  {"metadata": {"name": "cilium-gateway-console", "namespace": "kube-system"}, "spec": {"listeners": [{"name": "console-https", "allowedRoutes": ` + raSelector + `},
	  {"name": "console-https-acme", "allowedRoutes": {"namespaces": {"from": "` + orgFrom + `"}}}, {"name": "same"}]}}]}`
}

func raRoute(ns, accepted string) string {
	return `{"metadata": {"name": "app", "namespace": "` + ns + `"}, "spec": {"parentRefs": [{"name": "cilium-gateway-console", "namespace": "kube-system"}]},
	  "status": {"parents": [{"parentRef": {"name": "cilium-gateway-console", "namespace": "kube-system"}, "conditions": [{"type": "Accepted", "status": "` + accepted + `", "reason": "Why"}]}]}}`
}

func raRoutes(extra ...string) string {
	return `{"items": [` + strings.Join(append([]string{raRoute("catalyst-system", "True"), raRoute("acme", "True")}, extra...), ",") + `]}`
}

func (f *raFake) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	switch q := strings.Join(args, " "); {
	case q == "get gateways.gateway.networking.k8s.io -A -o json":
		return []byte(f.gateways), nil
	case q == "get "+routeKind+" -A -o json":
		return []byte(f.routes), nil
	case q == "apply -f -":
		var m kube.Object
		if err := json.NewDecoder(stdin).Decode(&m); err != nil || f.refuse {
			return nil, fmt.Errorf("[%s] kubectl apply: forbidden", r.ID)
		}
		f.applied = append(f.applied, r.ID+"/"+m.Namespace()+"/"+m.Name())
		return nil, nil
	case len(args) == 7 && args[0] == "get" && args[1] == routeKind:
		parents := []string{}
		for _, gw := range routeGateways {
			if s, ok := f.verdict[gw]; ok {
				parents = append(parents, `{"parentRef": {"name": "`+gw+`", "namespace": "kube-system"}, "conditions": [{"type": "Accepted", "status": "`+s+`", "reason": "NotAllowedByListeners"}]}`)
			}
		}
		return []byte(`{"status": {"parents": [` + strings.Join(parents, ",") + `]}}`), nil
	case args[0] == "delete" && args[1] == routeKind:
		f.deleted = append(f.deleted, r.ID+"/"+args[4]+"/"+args[2])
		return nil, nil
	case args[0] == "get" && args[1] == "namespace":
		return nil, nil // the host namespace of a deleted Organization is gone
	default:
		return nil, fmt.Errorf("[%s] unexpected kubectl %s", r.ID, q)
	}
}

// raRun runs check NET-G11 the way the runner does, and fails the test unless
// every Organization and every ISO-7 route that it wrote was deleted.
func raRun(t *testing.T, f *raFake, withAPI bool, fqdn string) (*runner, *isoConsole) {
	t.Helper()
	console := &isoConsole{t: t}
	srv := httptest.NewServer(console)
	defer srv.Close()
	cc := &CheckContext{Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	if withAPI {
		cc.API = api.New(srv.URL, "session")
	}
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: []*Step{{Journey: "D0", ID: "D0.1", Evidence: Evidence{"sovereignFQDN": fqdn}}}}}
	c := routeAdmissionCheck{kubectl: f.kubectl, waits: isoTestWaits()}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c}, cc, true)
	sort.Strings(f.applied)
	sort.Strings(f.deleted)
	if strings.Join(console.created, " ") != strings.Join(console.deleted, " ") || strings.Join(f.applied, " ") != strings.Join(f.deleted, " ") {
		t.Errorf("Organizations created %v, deleted %v; routes written %v, deleted %v", console.created, console.deleted, f.applied, f.deleted)
	}
	return r, console
}

func TestRouteAdmissionCheck(t *testing.T) {
	if _, ok := registry[routeAdmissionID]; !ok || routeAdmissionID != "NET-G11" {
		t.Fatalf("check %s is not registered", routeAdmissionID)
	}
	no := map[string]string{"cilium-gateway": "False", "cilium-gateway-console": "False"}
	for name, c := range map[string]struct {
		f        raFake
		api      bool
		fqdn     string
		want     string // "" = PASS, else a part of the reason of the FAIL
		routes   int    // ISO-7 routes written
		children int    // test Organizations created
	}{
		"every listener selects, the ISO-7 route is refused": {raFake{gateways: raGateways("Selector"), routes: raRoutes(), verdict: no}, true, "t1.omani.works", "", 4, 2},
		"a listener admits every namespace":                  {raFake{gateways: raGateways("All"), routes: raRoutes(), verdict: no}, true, "t1.omani.works", "region region-a: 1 of 5 listeners admit the HTTPRoutes of every namespace: kube-system/cilium-gateway-console/console-https-acme", 4, 2},
		"a region lists no Gateway":                          {raFake{gateways: `{"items": []}`, routes: raRoutes(), verdict: no}, true, "t1.omani.works", "region region-a has no Gateway listener: nothing was measured", 4, 2},
		"a route is refused":                                 {raFake{gateways: raGateways("Selector"), routes: raRoutes(raRoute("shop", "False")), verdict: no}, true, "t1.omani.works", "1 of 3 HTTPRoutes are not Accepted: shop/app on kube-system/cilium-gateway-console: Accepted False Why", 4, 2},
		"a route has no status":                              {raFake{gateways: raGateways("Selector"), routes: raRoutes(`{"metadata": {"name": "app", "namespace": "shop"}, "spec": {"parentRefs": [{"name": "x"}]}}`), verdict: no}, true, "t1.omani.works", "shop/app: 1 parentRefs, 0 with a status", 4, 2},
		"a region lists no HTTPRoute":                        {raFake{gateways: raGateways("Selector"), routes: `{"items": []}`, verdict: no}, true, "t1.omani.works", "region region-a has no HTTPRoute: nothing was measured", 4, 2},
		"the console Gateway accepts the ISO-7 route":        {raFake{gateways: raGateways("Selector"), routes: raRoutes(), verdict: map[string]string{"cilium-gateway": "False", "cilium-gateway-console": "True"}}, true, "t1.omani.works", "is Accepted by the Gateway kube-system/cilium-gateway-console", 4, 2},
		"a Gateway never reports on the ISO-7 route":         {raFake{gateways: raGateways("Selector"), routes: raRoutes(), verdict: map[string]string{"cilium-gateway": "False"}}, true, "t1.omani.works", "no Accepted condition yet: iso-a-", 4, 2},
		"the ISO-7 route cannot be written":                  {raFake{gateways: raGateways("Selector"), routes: raRoutes(), verdict: no, refuse: true}, true, "t1.omani.works", "kubectl apply: forbidden", 0, 2},
		"the run has no console session":                     {raFake{gateways: raGateways("Selector"), routes: raRoutes(), verdict: no}, false, "t1.omani.works", "the run has no console session", 0, 0},
		"the run did not learn the name of the Sovereign":    {raFake{gateways: raGateways("Selector"), routes: raRoutes(), verdict: no}, true, "", "step D0.1 recorded no sovereignFQDN", 0, 0},
	} {
		r, console := raRun(t, &c.f, c.api, c.fqdn)
		if got := r.run.Checks[0]; (c.want == "") != (got.Status == Pass) || !strings.Contains(got.Reason, c.want) {
			t.Errorf("%s: status %s, reason %q; want a reason that holds %q (empty: PASS)", name, got.Status, got.Reason, c.want)
		}
		if len(c.f.applied) != c.routes || len(console.created) != c.children {
			t.Errorf("%s: routes written %v, Organizations created %v; want %d and %d", name, c.f.applied, console.created, c.routes, c.children)
		}
	}
}

// The steps of a passing run: listeners and routes per region, the steps of
// the Organizations of NET-G07 renumbered under NET-G11, and one ISO-7 step per
// region that writes the route of A and of B into their own host namespaces.
func TestRouteAdmissionCheck_Steps(t *testing.T) {
	f := &raFake{gateways: raGateways("Selector"), routes: raRoutes(), verdict: map[string]string{"cilium-gateway": "False", "cilium-gateway-console": "False"}}
	r, console := raRun(t, f, true, "t1.omani.works")
	names := []string{}
	for i, s := range r.run.Steps[1:] {
		if s.ID != fmt.Sprintf("NET-G11.%d", i+1) || s.Journey != routeAdmissionID {
			t.Errorf("step %d is %s of %s, want NET-G11.%d of NET-G11", i+1, s.ID, s.Journey, i+1)
		}
		names = append(names, s.Name)
	}
	want := "no listener of a Gateway in region region-a admits the HTTPRoutes of every namespace|every HTTPRoute in region region-a is Accepted by each of its parents|" +
		"no listener of a Gateway in region region-b admits the HTTPRoutes of every namespace|every HTTPRoute in region region-b is Accepted by each of its parents|" +
		"ISO: Organization A (" + console.created[0] + ") is created through the console API (POST /api/v1/organizations)|" +
		"ISO: Organization B (" + console.created[1] + ") is created through the console API (POST /api/v1/organizations)|ISO: Organizations A and B reach the state done (GET /api/v1/organizations/{id})|" +
		"ISO-7: an HTTPRoute of Organization A and of B for console.t1.omani.works is accepted by no listener in region region-a|" +
		"ISO-7: an HTTPRoute of Organization A and of B for console.t1.omani.works is accepted by no listener in region region-b"
	if got := strings.Join(names, "|"); !strings.HasPrefix(got, want) {
		t.Errorf("steps %s\nwant first %s", got, want)
	}
	if got, want := strings.Join(f.applied, " "), fmt.Sprintf("region-a/iso-a-%[1]s/iso7-%[1]s region-a/iso-b-%[1]s/iso7-%[1]s region-b/iso-a-%[1]s/iso7-%[1]s region-b/iso-b-%[1]s/iso7-%[1]s", console.created[0][6:]); got != want {
		t.Errorf("ISO-7 routes written %s, want %s: one per test Organization in its own host namespace in each region", got, want)
	}
	if b, _ := json.Marshal(iso7Route("n", "ns", "console.t1.omani.works")["spec"]); string(b) != `{"hostnames":["console.t1.omani.works"],"parentRefs":`+
		`[{"name":"cilium-gateway","namespace":"kube-system"},{"name":"cilium-gateway-console","namespace":"kube-system"}],"rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/"}}]}]}` {
		t.Errorf("the ISO-7 route is %s", b)
	}
}
