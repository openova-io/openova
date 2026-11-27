package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G11, route admission by owner (NET-191). For every Organization
// with a public parent domain, the organization controller and the
// catalyst-api write a listener pair on the console Gateway
// kube-system/cilium-gateway-console: console-https-<label> and
// console-http-<label>, host name *.<label>.<parent>. Both admit HTTPRoutes
// only from the Organization's namespace <label> and from catalyst-system,
// where the console's route of console.<label>.<parent> is written
// (core/controllers/pkg/orglistener). The check proves it with one route of
// an Organization A: the listeners of A admit it and those of a second
// Organization B refuse it.
//
// The regions. Only the first region of the run is asked, the region the API
// runs in (as CLD-G07 and CMP-G02 read it): its console Gateway and the host
// namespace of A. Every other region is asked nothing.
//
// The Organizations. A and B are the first two labels, in sort order, of the
// listener pairs on that Gateway: a listener console-https-<label> whose host
// name is *.<label>.<parent>, and console-http-<label> with the same host
// name. With fewer than two pairs nothing can be measured, and the check
// fails.
//
// The route. The check creates the HTTPRoute net-g11-<tag> in the host
// namespace A, the way the platform makes the routes of an Organization: the
// syncer of the vCluster reflects no HTTPRoute to the host (loft vcluster
// 0.33.4 registers no httproute syncer, #4993), so the apps of an
// Organization are routed by host-native HTTPRoutes in its namespace
// (generateHostNativeAppRoute in core/services/provisioning/gitops). No
// Service of any type is created. Its parentRefs name the four listeners of A
// and B by sectionName, and its host names are net-g11-<tag>.<label>.<parent>
// of A and of B, so every listener matches it by host name and only the
// namespace decides. Its one rule has no backend: the route serves nothing
// even where it is admitted.
//
// The assertions, on that route once the gateway controller has written a
// condition Accepted for each of the four listeners (or the wait ended): each
// listener of A has Accepted=True with the reason Accepted, and each listener
// of B has Accepted=False with the reason NotAllowedByListeners. Any other
// reason fails: such a route was refused for its host name or its section, not
// for its owner. A listener without a condition Accepted fails: nothing was
// measured.
//
// Before it ends, also when a step failed, the check deletes its route from
// the host namespace A and waits until it is gone; it fails naming the route
// that is left.
const (
	routeAdmissionID = "NET-G11"
	routeGateway     = "cilium-gateway-console"
	routeGatewayNS   = "kube-system"
	// routeRefused is the reason of Gateway API for a route whose namespace
	// the allowedRoutes of the listener do not admit.
	routeRefused = "NotAllowedByListeners"
)

// routeWaits are the longest waits of the check and the pause between two reads.
type routeWaits struct{ status, gone, every time.Duration }

type routeAdmissionCheck struct {
	kubectl kubectlFunc
	waits   routeWaits
}

func init() {
	RegisterCheck(routeAdmissionCheck{kubectl: kube.Region.Run,
		waits: routeWaits{status: 3 * time.Minute, gone: 3 * time.Minute, every: 5 * time.Second}})
}

func (routeAdmissionCheck) ID() string { return routeAdmissionID }

// routeOrg is an Organization with a listener pair: its label (and host
// namespace) and its zone <label>.<parent>.
type routeOrg struct{ label, zone string }

func (o routeOrg) listeners() []string {
	return []string{"console-https-" + o.label, "console-http-" + o.label}
}

// routeOrgs returns the Organizations with a listener pair on gw, sorted by label.
func routeOrgs(gw kube.Object) []routeOrg {
	hosts := map[string]string{}
	for _, l := range gw.Objects("spec", "listeners") {
		hosts[l.Str("name")] = l.Str("hostname")
	}
	out := []routeOrg{}
	for name, host := range hosts {
		label, pair := strings.CutPrefix(name, "console-https-")
		zone, wild := strings.CutPrefix(host, "*.")
		if pair && label != "" && wild && strings.HasPrefix(zone, label+".") && hosts["console-http-"+label] == host {
			out = append(out, routeOrg{label: label, zone: zone})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out
}

// admissionRoute is the HTTPRoute name in ns that names every listener of orgs.
func admissionRoute(name, ns string, orgs ...routeOrg) map[string]any {
	parents, hosts := []any{}, []any{}
	for _, o := range orgs {
		hosts = append(hosts, name+"."+o.zone)
		for _, l := range o.listeners() {
			parents = append(parents, map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway",
				"namespace": routeGatewayNS, "name": routeGateway, "sectionName": l})
		}
	}
	return map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     map[string]any{"parentRefs": parents, "hostnames": hosts, "rules": []any{map[string]any{}}}}
}

// routeAccepted returns "<status>/<reason>" of the condition Accepted that the
// route holds for listener, or "" when it holds none.
func routeAccepted(route kube.Object, listener string) string {
	for _, p := range route.Objects("status", "parents") {
		if p.Str("parentRef", "name") != routeGateway || p.Str("parentRef", "namespace") != routeGatewayNS || p.Str("parentRef", "sectionName") != listener {
			continue
		}
		for _, c := range p.Objects("conditions") {
			if c.Str("type") == "Accepted" {
				return c.Str("status") + "/" + c.Str("reason")
			}
		}
	}
	return ""
}

type routeRun struct {
	c      routeAdmissionCheck
	rec    StepRecorder
	region kube.Region
	n      int
	a, b   routeOrg
	name   string // the name of the route of the check in the host namespace of A
}

func (c routeAdmissionCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	run := strings.ToLower(NewRunID(time.Now())) // 20261119t101500z-abcd
	r := &routeRun{c: c, rec: rec, region: cc.Regions[0], name: "net-g11-" + run[9:15] + run[len(run)-4:]}
	if !r.step("the console Gateway "+routeGatewayNS+"/"+routeGateway+" of region "+r.region.ID+" carries the listener pairs of two Organizations A and B", r.orgs(ctx)) {
		return nil
	}
	if r.step("the HTTPRoute "+r.name+" is created in the host namespace "+r.a.label+" of A in region "+r.region.ID, r.create(ctx)) {
		r.judge(ctx)
	}
	r.step("the HTTPRoute "+r.a.label+"/"+r.name+" is deleted and gone", r.cleanup(ctx))
	return nil
}

func (r *routeRun) step(name string, fn func(ev Evidence) error) bool {
	r.n++
	return checkStep(r.rec, routeAdmissionID, r.n, name, fn)
}

func (r *routeRun) orgs(ctx context.Context) func(ev Evidence) error {
	return func(ev Evidence) error {
		ev["region"] = r.region.ID
		out, err := r.c.kubectl(r.region, ctx, nil, "-n", routeGatewayNS, "get", "gateways.gateway.networking.k8s.io", routeGateway, "-o", "json")
		if err != nil {
			return err
		}
		var gw kube.Object
		if err := json.Unmarshal(out, &gw); err != nil {
			return fmt.Errorf("the Gateway is not JSON: %v", err)
		}
		orgs := routeOrgs(gw)
		labels := []string{}
		for _, o := range orgs {
			labels = append(labels, o.label)
		}
		ev["organizations"] = labels
		if len(orgs) < 2 {
			return fmt.Errorf("the Gateway carries the listener pairs of %d Organizations %v, and the check needs two: nothing was measured", len(orgs), labels)
		}
		r.a, r.b = orgs[0], orgs[1]
		mine := append(r.a.listeners(), r.b.listeners()...)
		admits := map[string]any{}
		for _, l := range gw.Objects("spec", "listeners") {
			if slices.Contains(mine, l.Str("name")) {
				admits[l.Str("name")] = l.Get("allowedRoutes")
			}
		}
		ev["a"], ev["b"], ev["allowed_routes"] = r.a.label, r.b.label, admits
		return nil
	}
}

// route reads the route of the check; nil when there is none.
func (r *routeRun) route(ctx context.Context) (kube.Object, error) {
	out, err := r.c.kubectl(r.region, ctx, nil, "-n", r.a.label, "get", "httproutes.gateway.networking.k8s.io", r.name, "--ignore-not-found", "-o", "json")
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil, err
	}
	var o kube.Object
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("the HTTPRoute %s is not JSON: %v", r.name, err)
	}
	return o, nil
}

func (r *routeRun) create(ctx context.Context) func(ev Evidence) error {
	return func(ev Evidence) error {
		route := admissionRoute(r.name, r.a.label, r.a, r.b)
		ev["route"] = route
		body, err := json.Marshal(route)
		if err != nil {
			return err
		}
		_, err = r.c.kubectl(r.region, ctx, bytes.NewReader(body), "-n", r.a.label, "create", "-f", "-")
		return err
	}
}

// judge waits until the route holds a condition Accepted for every listener
// of A and B, and records one step for A and one for B on what it read.
func (r *routeRun) judge(ctx context.Context) {
	listeners := append(r.a.listeners(), r.b.listeners()...)
	seen := map[string]string{}
	_, waited := poll(ctx, r.c.waits.status, r.c.waits.every, func() (bool, string, error) {
		o, err := r.route(ctx)
		switch {
		case err != nil:
			return false, err.Error(), nil
		case o == nil:
			return false, "no HTTPRoute " + r.name + " in the host namespace " + r.a.label, nil
		}
		all := true
		for _, l := range listeners {
			seen[l] = routeAccepted(o, l)
			all = all && seen[l] != ""
		}
		return all, fmt.Sprint(seen), nil
	})
	for _, side := range []struct {
		org        routeOrg
		what, want string
	}{{r.a, "admit the route of A", "True/Accepted"}, {r.b, "refuse the route of A", "False/" + routeRefused}} {
		r.step("the listeners "+strings.Join(side.org.listeners(), " and ")+" of "+side.org.label+" "+side.what+" (Accepted="+side.want+")", func(ev Evidence) error {
			bad := []string{}
			for _, l := range side.org.listeners() {
				ev[l] = seen[l]
				switch seen[l] {
				case side.want:
				case "":
					bad = append(bad, l+" wrote no condition Accepted: nothing was measured")
				default:
					bad = append(bad, l+" has Accepted="+seen[l])
				}
			}
			if len(bad) > 0 && waited != nil {
				bad = append(bad, waited.Error())
			}
			if len(bad) > 0 {
				return errors.New(strings.Join(bad, "; "))
			}
			return nil
		})
	}
}

func (r *routeRun) cleanup(ctx context.Context) func(ev Evidence) error {
	return func(ev Evidence) error {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.c.waits.gone+time.Minute)
		defer cancel()
		_, derr := r.c.kubectl(r.region, cctx, nil, "-n", r.a.label, "delete", "httproutes.gateway.networking.k8s.io", r.name, "--ignore-not-found", "--wait=false")
		_, err := poll(cctx, r.c.waits.gone, r.c.waits.every, func() (bool, string, error) {
			o, err := r.route(cctx)
			if err != nil {
				return false, err.Error(), nil
			}
			return o == nil, "the HTTPRoute " + r.a.label + "/" + r.name + " is left", nil
		})
		return errors.Join(derr, err)
	}
}
