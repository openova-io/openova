package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// routeFake is the first region for check NET-G11: a console Gateway with the
// apex listener of the console and the listener pairs of the Organizations
// acme and beta, the host namespace acme, and a gateway controller that judges
// each parent of a route by its Gateway, the host name and the allowedRoutes
// of its listener, as Gateway API defines them.
type routeFake struct {
	t     *testing.T
	admit func(label string) any // the allowedRoutes of the listener pair of label
	fault string
	route map[string]any // the route of the check in the namespace acme; nil when there is none
	asked map[string]bool
	calls []string // the writes, in order
}

// net191Admits is the allowedRoutes that NET-191 writes (core/controllers/pkg/orglistener).
func net191Admits(label string) any {
	return obj{"namespaces": obj{"from": "Selector", "selector": obj{"matchExpressions": []any{
		obj{"key": "kubernetes.io/metadata.name", "operator": "In", "values": []any{label, "catalyst-system"}}}}}}
}

// allAdmits is the allowedRoutes before NET-191, as the listeners of hw307 had it on 27 November.
func allAdmits(string) any { return obj{"namespaces": obj{"from": "All"}} }

func (f *routeFake) gateway() obj {
	listeners := []any{obj{"name": "console-https", "hostname": "console.t1.omani.works", "allowedRoutes": net191Admits("catalyst-system")}}
	zones := [][2]string{{"acme", "acme.omani.homes"}, {"beta", "beta.omani.rest"}}
	if f.fault == "one-org" {
		zones = zones[:1]
	}
	for _, z := range zones {
		for _, p := range []string{"console-https-", "console-http-"} {
			if f.fault == "half-pair" && z[0] == "beta" && p == "console-http-" {
				continue
			}
			listeners = append(listeners, obj{"name": p + z[0], "hostname": "*." + z[1], "allowedRoutes": f.admit(z[0])})
		}
	}
	return obj{"metadata": obj{"name": "cilium-gateway-console", "namespace": "kube-system"}, "spec": obj{"listeners": listeners}}
}

// judge is the condition Accepted of the gateway controller for one parent of
// a route in namespace ns.
func (f *routeFake) judge(ns string, hosts []any, ref map[string]any) obj {
	reason := "NoMatchingParent"
	if ref["group"] != "gateway.networking.k8s.io" || ref["kind"] != "Gateway" || ref["namespace"] != "kube-system" || ref["name"] != "cilium-gateway-console" {
		return obj{"type": "Accepted", "status": "False", "reason": reason}
	}
	for _, l := range kube.Object(f.gateway()).Objects("spec", "listeners") {
		if l.Str("name") != ref["sectionName"] {
			continue
		}
		zone := strings.TrimPrefix(l.Str("hostname"), "*")
		ok, from := false, l.Str("allowedRoutes", "namespaces", "from")
		for _, e := range l.Objects("allowedRoutes", "namespaces", "selector", "matchExpressions") {
			values, _ := e.Get("values").([]any)
			ok = e.Str("key") == "kubernetes.io/metadata.name" && e.Str("operator") == "In" && slices.Contains(values, any(ns))
		}
		switch {
		case !slices.ContainsFunc(hosts, func(h any) bool { return strings.HasSuffix(h.(string), zone) }),
			f.fault == "other-reason" && strings.HasSuffix(l.Str("name"), "-beta"):
			reason = "NoMatchingListenerHostname"
		case from != "All" && (from != "Selector" || !ok):
			reason = routeRefused
		default:
			return obj{"type": "Accepted", "status": "True", "reason": "Accepted"}
		}
	}
	return obj{"type": "Accepted", "status": "False", "reason": reason}
}

// read is the route as the API server returns it, with the status of the gateway controller.
func (f *routeFake) read() []byte {
	spec := f.route["spec"].(map[string]any)
	parents := []any{}
	for _, p := range spec["parentRefs"].([]any) {
		if f.fault != "no-status" {
			parents = append(parents, obj{"parentRef": p, "controllerName": "io.cilium/gateway-controller",
				"conditions": []any{f.judge(kube.Object(f.route).Str("metadata", "namespace"), spec["hostnames"].([]any), p.(map[string]any))}})
		}
	}
	b, _ := json.Marshal(obj{"metadata": f.route["metadata"], "spec": spec, "status": obj{"parents": parents}})
	return b
}

func (f *routeFake) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	f.asked[r.ID] = true
	call := strings.Join(args, " ")
	name := ""
	if f.route != nil {
		name = kube.Object(f.route).Str("metadata", "name")
	}
	switch {
	case r.ID == "region-a" && call == "-n kube-system get gateways.gateway.networking.k8s.io cilium-gateway-console -o json":
		return json.Marshal(f.gateway())
	case r.ID == "region-a" && call == "-n acme create -f -" && f.route == nil && len(f.calls) == 0:
		f.calls = append(f.calls, call)
		var route map[string]any
		if err := json.NewDecoder(stdin).Decode(&route); err != nil {
			f.t.Fatalf("the route is not JSON: %v", err)
		}
		o := kube.Object(route)
		if o.Str("metadata", "namespace") != "acme" || !strings.HasPrefix(o.Str("metadata", "name"), "net-g11-") {
			f.t.Errorf("the route is %s/%s; want net-g11-<tag> in the host namespace acme", o.Str("metadata", "namespace"), o.Str("metadata", "name"))
		}
		if rules := o.Objects("spec", "rules"); len(rules) != 1 || rules[0].Get("backendRefs") != nil {
			f.t.Errorf("the route has the rules %v; want one rule without a backend", rules)
		}
		if f.fault == "refused" {
			return nil, errors.New(`admission webhook "validate.gateway.networking.k8s.io" denied the request`)
		}
		f.route = route
		return nil, nil
	case r.ID == "region-a" && strings.HasPrefix(call, "-n acme get httproutes.gateway.networking.k8s.io net-g11-") && strings.HasSuffix(call, " --ignore-not-found -o json"):
		if f.route == nil || call != "-n acme get httproutes.gateway.networking.k8s.io "+name+" --ignore-not-found -o json" {
			return nil, nil
		}
		return f.read(), nil
	case r.ID == "region-a" && strings.HasPrefix(call, "-n acme delete httproutes.gateway.networking.k8s.io net-g11-") && strings.HasSuffix(call, " --ignore-not-found --wait=false"):
		f.calls = append(f.calls, call)
		if f.route != nil && call == "-n acme delete httproutes.gateway.networking.k8s.io "+name+" --ignore-not-found --wait=false" && f.fault != "stuck" {
			f.route = nil
		}
		return nil, nil
	}
	f.t.Errorf("an unexpected call in %s: %s", r.ID, call)
	return nil, errors.New("unexpected")
}

// TestRouteAdmissionCheck: with the listeners of NET-191 the route of acme is
// admitted by the listeners of acme and refused by those of beta; with the
// listeners before NET-191, and on every other fault, the check fails. Only
// the first region is asked, and the route of the check is created in the
// host namespace acme and deleted there.
func TestRouteAdmissionCheck(t *testing.T) {
	for _, tc := range []struct {
		name, fault, want string
		admit             func(string) any
	}{
		{"the listeners of NET-191", "", "", net191Admits},
		{"the listeners before NET-191", "", "console-https-beta has Accepted=True/Accepted; console-http-beta has Accepted=True/Accepted", allAdmits},
		{"the listeners admit only the console", "", "console-https-acme has Accepted=False/NotAllowedByListeners", func(string) any { return net191Admits("catalyst-system") }},
		{"refused for another reason", "other-reason", "console-https-beta has Accepted=False/NoMatchingListenerHostname", net191Admits},
		{"no condition written", "no-status", "console-https-acme wrote no condition Accepted: nothing was measured", net191Admits},
		{"the route is not created", "refused", `admission webhook "validate.gateway.networking.k8s.io" denied the request`, net191Admits},
		{"the route is left", "stuck", "the HTTPRoute acme/net-g11-", net191Admits},
		{"one Organization", "one-org", "the listener pairs of 1 Organizations [acme], and the check needs two", net191Admits},
		{"a listener without its twin", "half-pair", "the listener pairs of 1 Organizations [acme], and the check needs two", net191Admits},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &routeFake{t: t, admit: tc.admit, fault: tc.fault, asked: map[string]bool{}}
			c := routeAdmissionCheck{kubectl: f.kubectl, waits: routeWaits{status: 50 * time.Millisecond, gone: 50 * time.Millisecond, every: time.Millisecond}}
			r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
			cc := &CheckContext{Regions: []kube.Region{{ID: "region-a", Kubeconfig: "a"}, {ID: "region-b", Kubeconfig: "b"}}}
			r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateExpectedFail)}}, []Check{c}, cc, true)
			res, failed := r.run.Checks[0], strings.Join(failedSteps(r), "; ")
			if (tc.want == "") != (res.Status == Pass) || !strings.Contains(failed, tc.want) {
				t.Errorf("status %s, failed steps %q; want a failed step that holds %q (empty: PASS)", res.Status, failed, tc.want)
			}
			if f.asked["region-b"] || !f.asked["region-a"] {
				t.Errorf("regions asked %v; want region-a only", f.asked)
			}
			for _, s := range r.run.Steps {
				if tc.fault == "refused" && strings.HasPrefix(s.Name, "the listeners ") {
					t.Errorf("step %s %q after a refused create; want no verdict on a route that does not exist", s.ID, s.Name)
				}
			}
			want := []string{}
			if tc.fault != "one-org" && tc.fault != "half-pair" {
				want = []string{"-n acme create -f -", "-n acme delete httproutes.gateway.networking.k8s.io "}
			}
			ok := len(f.calls) == len(want)
			for i := range want {
				ok = ok && strings.HasPrefix(f.calls[i], want[i])
			}
			if !ok || (f.route != nil) != (tc.fault == "stuck") {
				t.Errorf("writes %q, route left %v; want %q and no route left", f.calls, f.route != nil, want)
			}
		})
	}
}

// TestRouteAdmissionCheck_Registered: the registry holds NET-G11 with the
// kubectl and the waits of a cloud run.
func TestRouteAdmissionCheck_Registered(t *testing.T) {
	c, ok := registry[routeAdmissionID].(routeAdmissionCheck)
	if !ok || routeAdmissionID != "NET-G11" || c.kubectl == nil || c.waits.status != 3*time.Minute || c.waits.gone != 3*time.Minute || c.waits.every != 5*time.Second {
		t.Fatal("the registry must hold NET-G11 with kubectl and the waits of a cloud run")
	}
}
