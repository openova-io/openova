package suite

// Check NET-G11, route admission by owner. In every region of the Sovereign:
//
//  1. No listener of any Gateway admits the HTTPRoutes of every namespace
//     (allowedRoutes.namespaces.from All). A region with no listener fails.
//  2. Every HTTPRoute has a parent status for each of its parentRefs, and each
//     says Accepted True. A region with no HTTPRoute fails.
//  3. ISO-7: an HTTPRoute that a test Organization writes into its own host
//     namespace for console.<sovereign FQDN>, parented on both Gateways of
//     kube-system without a section so that every listener judges it, is
//     accepted by no listener: both Gateways report Accepted False on it.
//
// The two test Organizations of ISO-7 are created and deleted the way check
// NET-G07 does it (isoJourney.createOrgs and cleanup, check_org_isolation.go),
// with the smallest plan; their steps are recorded under this check.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

const (
	routeAdmissionID = "NET-G11"
	routeGatewayNS   = "kube-system"
	routeKind        = "httproutes.gateway.networking.k8s.io"
)

// routeGateways are the two Gateways of a Sovereign: the shared one and the console one.
var routeGateways = []string{"cilium-gateway", "cilium-gateway-console"}

// routeAdmissionCheck waits as NET-G07 does; waits.policy is the longest wait
// for the Gateways to report on an ISO-7 route.
type routeAdmissionCheck struct {
	kubectl kubectlFunc
	waits   isoWaits
}

func init() {
	RegisterCheck(routeAdmissionCheck{kubectl: kube.Region.Run,
		waits: isoWaits{org: 30 * time.Minute, gone: 15 * time.Minute, policy: 3 * time.Minute, every: 10 * time.Second}})
}

func (routeAdmissionCheck) ID() string { return routeAdmissionID }

// routeRecorder records the steps of the journey of NET-G07 under NET-G11.
type routeRecorder struct{ StepRecorder }

func (r routeRecorder) Step(id, name string, fn func(ev Evidence) error) {
	r.StepRecorder.Step(routeAdmissionID+strings.TrimPrefix(id, orgIsolationID), name, fn)
}

func (c routeAdmissionCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	run := strings.ToLower(NewRunID(time.Now())) // 20261119t101500z-abcd
	tag := run[9:15] + run[len(run)-4:]
	j := &isoJourney{c: orgIsolationCheck{kubectl: c.kubectl, waits: c.waits}, cc: cc, rec: routeRecorder{rec}, tag: tag,
		a: &isoOrg{label: "A", slug: "iso-a-" + tag, plan: isoPlanB}, b: &isoOrg{label: "B", slug: "iso-b-" + tag, plan: isoPlanB}}
	for _, region := range cc.Regions {
		j.step("no listener of a Gateway in region "+region.ID+" admits the HTTPRoutes of every namespace", func(ev Evidence) error {
			gateways, err := c.list(ctx, region, "gateways.gateway.networking.k8s.io")
			open, n := openListeners(gateways)
			ev["gateways"], ev["listeners"], ev["open"] = len(gateways), n, open
			return routeVerdict(err, region, n, "Gateway listener", open, "listeners admit the HTTPRoutes of every namespace")
		})
		j.step("every HTTPRoute in region "+region.ID+" is Accepted by each of its parents", func(ev Evidence) error {
			routes, err := c.list(ctx, region, routeKind)
			bad := unacceptedRoutes(routes)
			ev["routes"], ev["not_accepted"] = len(routes), bad
			return routeVerdict(err, region, len(routes), "HTTPRoute", bad, "HTTPRoutes are not Accepted")
		})
	}
	fqdn := sovereignFQDN(cc.Prior)
	switch {
	case cc.API == nil:
		return errors.New("ISO-7 creates its Organizations through the console API, and the run has no console session")
	case fqdn == "":
		return errors.New("the run did not learn the name of the Sovereign: step D0.1 recorded no sovereignFQDN")
	}
	if j.createOrgs(ctx) {
		for _, region := range cc.Regions {
			j.step("ISO-7: an HTTPRoute of Organization A and of B for console."+fqdn+" is accepted by no listener in region "+region.ID, func(ev Evidence) error {
				return c.iso7(ctx, j, region, "console."+fqdn, ev)
			})
		}
	}
	j.cleanup(ctx)
	return nil
}

// routeVerdict fails a step that could not list, that measured nothing (n is
// 0), or that found bad items.
func routeVerdict(err error, region kube.Region, n int, what string, bad []string, why string) error {
	switch {
	case err != nil:
		return err
	case n == 0:
		return fmt.Errorf("region %s has no %s: nothing was measured", region.ID, what)
	case len(bad) > 0:
		return fmt.Errorf("region %s: %d of %d %s: %s", region.ID, len(bad), n, why, strings.Join(bad, "; "))
	}
	return nil
}

func (c routeAdmissionCheck) list(ctx context.Context, region kube.Region, resource string) ([]kube.Object, error) {
	var l kube.List
	out, err := c.kubectl(region, ctx, nil, "get", resource, "-A", "-o", "json")
	if err == nil {
		err = json.Unmarshal(out, &l)
	}
	return l.Items, err
}

// openListeners names each listener that admits the HTTPRoutes of every
// namespace as <namespace>/<gateway>/<listener>, and counts all listeners.
func openListeners(gateways []kube.Object) ([]string, int) {
	open, n := []string{}, 0
	for _, g := range gateways {
		for _, l := range g.Objects("spec", "listeners") {
			n++
			if l.Str("allowedRoutes", "namespaces", "from") == "All" {
				open = append(open, g.Namespace()+"/"+g.Name()+"/"+l.Str("name"))
			}
		}
	}
	sort.Strings(open)
	return open, n
}

// unacceptedRoutes names each HTTPRoute that is not Accepted by every one of its parents, with the reason.
func unacceptedRoutes(routes []kube.Object) []string {
	bad := []string{}
	for _, r := range routes {
		id := r.Namespace() + "/" + r.Name()
		refs, parents := r.Objects("spec", "parentRefs"), r.Objects("status", "parents")
		if len(refs) == 0 || len(parents) < len(refs) {
			bad = append(bad, fmt.Sprintf("%s: %d parentRefs, %d with a status", id, len(refs), len(parents)))
			continue
		}
		for _, p := range parents {
			if status, reason := parentAccepted(p); status != "True" {
				bad = append(bad, fmt.Sprintf("%s on %s/%s: Accepted %s %s", id, p.Str("parentRef", "namespace"), p.Str("parentRef", "name"), nonEmpty(status, "absent"), reason))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// parentAccepted is the status and the reason of the condition Accepted of one parent status.
func parentAccepted(p kube.Object) (string, string) {
	for _, c := range p.Objects("conditions") {
		if c.Str("type") == "Accepted" {
			return c.Str("status"), c.Str("reason")
		}
	}
	return "", ""
}

// iso7Route is the route of ISO-7. It has no backend: it would answer 500 if a listener took it.
func iso7Route(name, namespace, host string) map[string]any {
	parents := []any{map[string]any{"name": routeGateways[0], "namespace": routeGatewayNS}, map[string]any{"name": routeGateways[1], "namespace": routeGatewayNS}}
	return map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{"parentRefs": parents, "hostnames": []any{host},
			"rules": []any{map[string]any{"matches": []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}}}}}}}
}

// iso7 writes the ISO-7 route into the host namespaces of A and B in region,
// waits until both Gateways report on each, and deletes the routes again.
func (c routeAdmissionCheck) iso7(ctx context.Context, j *isoJourney, region kube.Region, host string, ev Evidence) error {
	name, written, verdicts := "iso7-"+j.tag, []string{}, map[string]string{}
	defer func() {
		for _, ns := range written {
			_, _ = c.kubectl(region, context.WithoutCancel(ctx), nil, "delete", routeKind, name, "-n", ns, "--ignore-not-found", "--wait=false")
		}
	}()
	for _, o := range j.orgs() {
		body, _ := json.Marshal(iso7Route(name, o.namespace, host))
		if _, err := c.kubectl(region, ctx, bytes.NewReader(body), "apply", "-f", "-"); err != nil {
			return fmt.Errorf("the HTTPRoute %s/%s of Organization %s cannot be written: %w", o.namespace, name, o.label, err)
		}
		written = append(written, o.namespace)
	}
	ev["route"], ev["host"], ev["namespaces"], ev["accepted"] = name, host, written, verdicts
	_, err := poll(ctx, j.c.waits.policy, j.c.waits.every, func() (bool, string, error) {
		pending := []string{}
		for _, ns := range written {
			out, err := c.kubectl(region, ctx, nil, "get", routeKind, name, "-n", ns, "-o", "json")
			var r kube.Object
			if err == nil {
				err = json.Unmarshal(out, &r)
			}
			if err != nil {
				return false, err.Error(), nil
			}
			for _, gw := range routeGateways {
				status, reason := "", ""
				for _, p := range r.Objects("status", "parents") {
					if p.Str("parentRef", "name") == gw && p.Str("parentRef", "namespace") == routeGatewayNS {
						status, reason = parentAccepted(p)
					}
				}
				verdicts[ns+" on "+gw] = strings.TrimSpace(status + " " + reason)
				if status == "True" {
					return false, "", fmt.Errorf("the HTTPRoute %s/%s for %s is Accepted by the Gateway %s/%s", ns, name, host, routeGatewayNS, gw)
				} else if status != "False" {
					pending = append(pending, ns+" on "+gw)
				}
			}
		}
		return len(pending) == 0, "no Accepted condition yet: " + strings.Join(pending, ", "), nil
	})
	return err
}
