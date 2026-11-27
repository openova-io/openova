package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check AIP-G06, the simulation back end is installed, truthful and fenced:
// Axon of kit slot 82 (chart products/axon/chart).
//
// In the primary region, the first region of the run:
//  1. Deployment axon/axon asks for one replica, its controller has observed
//     that spec, and one replica is ready.
//  2. Service axon/axon is of type ClusterIP and carries the annotation
//     service.cilium.io/global "true": the global Service is what sends the
//     callers of a region without a Pod to the one of the primary region, and
//     a ClusterIP Service has no node port.
//  3. Through kubectl port-forward to port 3001 of Service axon/axon, GET
//     /stats answers 200 with a credential.state that is one of the five
//     states of the credential owner (products/axon/src/credential.ts).
//  4. No Secret of namespace axon has a data key document or .credentials.json:
//     the credential reaches the Pod as a CSI file only.
//  5. A dial Pod of namespace default (the Dialer of check_policy_dials.go)
//     reaches the cluster DNS service and kubernetes.default.svc:443, and its
//     dials of axon.axon.svc:3000 and axon.axon.svc:3001 time out (nc -w 3):
//     the chart's CiliumNetworkPolicy admits the meter alone to the model port
//     and catalyst-api alone to the admin port. The dial of the API server
//     proves that the Pod's own egress works, and each deny dial first needs a
//     ready endpoint of Service axon/axon (its EndpointSlices, read with
//     ReadyEndpoints), so the timeout is the fence. A refused dial fails: it
//     reached the port. The Pod is deleted in a step of its own, also when a
//     step failed.
//
// In every other region: Deployment axon/axon asks for zero replicas, its
// controller has observed that spec, and it has none; Service axon/axon is
// judged as in item 2.
//
// The check records names, data keys, counts and the state; never a value of a
// Secret or of the credential.
const (
	aipG06ID        = "AIP-G06"
	aipG06NS        = "axon"
	aipG06Name      = "axon"
	aipG06ModelPort = 3000
	aipG06AdminPort = 3001
	aipG06DialNS    = "default"
	aipG06GetWait   = 15 * time.Second
	// aipG06Global is the annotation that publishes the Service to the mesh.
	aipG06Global = "service.cilium.io/global"
)

// aipG06States are the states that GET /stats reports for the credential.
var aipG06States = []string{"absent", "valid", "expired", "rejected", "conflict"}

// aipG06Keys are the data keys that would mean the credential rests in a Secret object.
var aipG06Keys = []string{"document", ".credentials.json"}

type aipG06Check struct {
	kubectl kubectlFunc
	// forward starts a port-forward to a Service and returns the local address and its stop.
	forward func(ctx context.Context, region kube.Region, namespace, service string, port int) (string, func(), error)
	// get returns the status code and the body of a GET.
	get func(ctx context.Context, url string) (int, []byte, error)
}

func init() {
	RegisterCheck(aipG06Check{kubectl: kube.Region.Run, forward: portForward, get: aipG06Get})
}

func (aipG06Check) ID() string { return aipG06ID }

func (c aipG06Check) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	step := func(name string, fn func(ev Evidence) error) bool {
		n++
		return checkStep(rec, aipG06ID, n, name, fn)
	}
	service := func(region kube.Region) {
		step("the Service axon/axon of region "+region.ID+" is of type ClusterIP and published to the mesh ("+aipG06Global+" \"true\")", func(ev Evidence) error {
			ev["region"] = region.ID
			s, err := c.object(ctx, region, "service")
			if err != nil {
				return err
			}
			return aipG06Service(s, ev)
		})
	}
	primary := cc.Regions[0]
	step("the Deployment axon/axon of the primary region "+primary.ID+" asks for one replica and has one ready", func(ev Evidence) error {
		ev["region"] = primary.ID
		d, err := c.object(ctx, primary, "deployment")
		if err != nil {
			return err
		}
		return aipG06Replicas(d, 1, ev)
	})
	service(primary)
	step("GET /stats on port 3001 of the Service axon/axon of region "+primary.ID+" reports one of the five credential states", func(ev Evidence) error {
		ev["region"] = primary.ID
		addr, stop, err := c.forward(ctx, primary, aipG06NS, aipG06Name, aipG06AdminPort)
		if err != nil {
			return err
		}
		defer stop()
		code, body, err := c.get(ctx, "http://"+addr+"/stats")
		ev["status"] = code
		if err != nil {
			return err
		}
		state, err := aipG06State(code, body)
		ev["credential_state"] = state
		return err
	})
	step("no Secret of the namespace axon of region "+primary.ID+" has a data key "+strings.Join(aipG06Keys, " or "), func(ev Evidence) error {
		ev["region"] = primary.ID
		out, err := c.kubectl(primary, ctx, nil, "-n", aipG06NS, "get", "secret", "-o", "json")
		if err != nil {
			return err
		}
		var l kube.List
		if err := json.Unmarshal(out, &l); err != nil {
			return fmt.Errorf("[%s] the Secrets of %s are not JSON: %v", primary.ID, aipG06NS, err)
		}
		names := []string{}
		for _, s := range l.Items {
			names = append(names, s.Name())
		}
		held := aipG06Holders(l.Items)
		ev["secrets"], ev["holding"] = names, held
		if len(held) > 0 {
			return fmt.Errorf("the credential rests in a Secret object: %s", strings.Join(held, ", "))
		}
		return nil
	})
	c.fence(ctx, primary, step)
	for _, region := range cc.Regions[1:] {
		step("the Deployment axon/axon of region "+region.ID+" asks for zero replicas and has none", func(ev Evidence) error {
			ev["region"] = region.ID
			d, err := c.object(ctx, region, "deployment")
			if err != nil {
				return err
			}
			return aipG06Replicas(d, 0, ev)
		})
		service(region)
	}
	return nil
}

// fence records the steps of item 5 in region: start the dial Pod, dial the API
// server (allow), dial the model port and the admin port of Axon (deny each,
// once the Service has a ready endpoint), delete the Pod.
func (c aipG06Check) fence(ctx context.Context, region kube.Region, step func(string, func(Evidence) error) bool) {
	d := &Dialer{Kubectl: c.kubectl, Region: region, Run: strings.ToLower(NewRunID(time.Now()))}
	from := DialSource{Namespace: aipG06DialNS}
	api := DialTarget{Namespace: "default", Service: "kubernetes", Port: 443}
	model := DialTarget{Namespace: aipG06NS, Service: aipG06Name, Port: aipG06ModelPort}
	admin := DialTarget{Namespace: aipG06NS, Service: aipG06Name, Port: aipG06AdminPort}
	started := step("a dial Pod is Ready in the namespace "+aipG06DialNS+" of region "+region.ID, func(ev Evidence) error {
		ev["region"] = region.ID
		err := d.Start(ctx, []DialSource{from})
		ev["pods"] = d.Pods()
		return err
	})
	if started {
		for _, row := range []DialRow{
			{Name: "api-server", From: from, To: api, Expect: "allow"},
			{Name: "axon-model-port", From: from, To: model, Expect: "deny"},
			{Name: "axon-admin-port", From: from, To: admin, Expect: "deny"},
		} {
			what := "a Pod of the namespace " + aipG06DialNS + " reaches " + row.To.String() + ", so a timeout below is the fence and not the Pod"
			if row.Expect == "deny" {
				what = "a Pod of the namespace " + aipG06DialNS + " cannot open " + row.To.String() + " within " + dialSeconds + " seconds"
			}
			step(what+" (region "+region.ID+")", func(ev Evidence) error {
				ev["region"], ev["to"], ev["expect"] = region.ID, row.To.String(), row.Expect
				if row.Expect == "deny" {
					ready, err := d.ReadyEndpoints(ctx, row.To)
					if err != nil {
						return err
					}
					ev["ready_endpoints"] = ready
					if ready == 0 {
						return fmt.Errorf("the Service %s/%s has no ready endpoint: nothing listens, so a timeout would prove nothing", row.To.Namespace, row.To.Service)
					}
				}
				res, err := d.Dial(ctx, from, row.To)
				if err != nil {
					return err
				}
				ev["control"], ev["observed"], ev["output"] = res.Control, res.Outcome, res.Output
				return judgeDial(row, res)
			})
		}
	}
	if len(d.Pods()) > 0 {
		step("the dial Pod of region "+region.ID+" is deleted", func(ev Evidence) error {
			ev["region"], ev["pods"] = region.ID, d.Pods()
			return d.Stop(ctx)
		})
	}
}

// object reads the object of kind named axon in the namespace axon of region.
func (c aipG06Check) object(ctx context.Context, region kube.Region, kind string) (kube.Object, error) {
	out, err := c.kubectl(region, ctx, nil, "-n", aipG06NS, "get", kind, aipG06Name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var o kube.Object
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("[%s] the %s %s/%s is not JSON: %v", region.ID, kind, aipG06NS, aipG06Name, err)
	}
	return o, nil
}

// aipG06Replicas judges a Deployment that must run want replicas: spec.replicas
// is want, status.observedGeneration equals metadata.generation (the status
// describes this spec), and status.replicas and status.readyReplicas are want.
// The API leaves a status count out when it is 0, so an absent count reads as
// 0 only once the status is shown current.
func aipG06Replicas(d kube.Object, want int, ev Evidence) error {
	spec, hasSpec := d.Get("spec", "replicas").(float64)
	gen, hasGen := d.Get("metadata", "generation").(float64)
	seen, hasSeen := d.Get("status", "observedGeneration").(float64)
	count := func(key string) float64 {
		v, _ := d.Get("status", key).(float64)
		return v
	}
	running, ready := count("replicas"), count("readyReplicas")
	ev["spec_replicas"], ev["replicas"], ev["ready_replicas"] = d.Get("spec", "replicas"), running, ready
	ev["generation"], ev["observed_generation"] = d.Get("metadata", "generation"), d.Get("status", "observedGeneration")
	switch {
	case !hasSpec:
		return errors.New("the Deployment axon/axon states no spec.replicas")
	case !hasGen || !hasSeen || seen != gen:
		return fmt.Errorf("the status of the Deployment axon/axon is not current: generation %v, observedGeneration %v", d.Get("metadata", "generation"), d.Get("status", "observedGeneration"))
	case spec != float64(want):
		return fmt.Errorf("the Deployment axon/axon asks for %v replicas, want %d", spec, want)
	case running != float64(want) || ready != float64(want):
		return fmt.Errorf("the Deployment axon/axon has %v replicas, %v ready, want %d", running, ready, want)
	}
	return nil
}

// aipG06Service judges the Service axon/axon: spec.type is ClusterIP (no node
// port, no load balancer) and the annotation aipG06Global is "true".
func aipG06Service(s kube.Object, ev Evidence) error {
	typ, global := s.Str("spec", "type"), s.Str("metadata", "annotations", aipG06Global)
	ev["type"], ev["global"] = typ, global
	switch {
	case typ != "ClusterIP":
		return fmt.Errorf("the Service axon/axon is of type %q, want ClusterIP", typ)
	case global != "true":
		return fmt.Errorf("the Service axon/axon has %s %q, want \"true\": a region without a Pod would not reach the primary Axon", aipG06Global, global)
	}
	return nil
}

// aipG06State returns the credential state of a /stats answer, and an error
// unless the answer is 200 and the state one of aipG06States.
func aipG06State(code int, body []byte) (string, error) {
	if code != http.StatusOK {
		return "", fmt.Errorf("GET /stats answered %d, want 200", code)
	}
	var stats struct {
		Credential *struct {
			State *string `json:"state"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(body, &stats); err != nil {
		return "", fmt.Errorf("the answer of GET /stats is not JSON: %v", err)
	}
	if stats.Credential == nil || stats.Credential.State == nil {
		return "", errors.New("the answer of GET /stats has no credential.state")
	}
	state := *stats.Credential.State
	if !slices.Contains(aipG06States, state) {
		return state, fmt.Errorf("GET /stats reports the credential state %q, want one of %s", state, strings.Join(aipG06States, ", "))
	}
	return state, nil
}

// aipG06Holders names, as <Secret>/<key> in order, every data key of the
// Secrets that is one of aipG06Keys.
func aipG06Holders(secrets []kube.Object) []string {
	out := []string{}
	for _, s := range secrets {
		data, _ := s.Get("data").(map[string]any)
		for k := range data {
			if slices.Contains(aipG06Keys, k) {
				out = append(out, s.Name()+"/"+k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// aipG06Get is a GET of url with a bound of aipG06GetWait; the body is read up to 1 MiB.
func aipG06Get(ctx context.Context, url string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, aipG06GetWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	res, err := http.DefaultClient.Do(req) //nolint:gosec // G107: the local address of the check's own port-forward
	if err != nil {
		return 0, nil, fmt.Errorf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, body, err
}
