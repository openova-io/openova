// Package serving is the ONE functional "does it work, here, now" endpoint
// probe (ADR-0015 D7), shared by the application-controller (its own region)
// and the Continuum observer (every region, served to the others).
//
// For every HTTPRoute a HelmRelease installed (label
// app.kubernetes.io/instance=<release> or helm.toolkit.fluxcd.io/name=<release>)
// it GETs the Blueprint's declared health path THROUGH the region's own gateway
// Service (`cilium-gateway-<gateway>` in the gateway's namespace, from the
// route's parentRefs[0]) with Host = the route hostname. Status < 500 =
// serving; 5xx or a transport error = not serving. The probe goes through the
// gateway, not to the pod, because tenant namespaces admit only same-namespace
// + gateway traffic, and the gateway path is the one a User's request takes.
//
// Why it is shared: only the writer region holds the Application, so before
// this package a peer region's leg was "not observable" by construction. After
// a primary move to that region the Application could never become Ready again
// (hw307, 2026-11-12). The Continuum in each region now runs this same probe
// for its own legs and serves the verdict to the others.
package serving

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Condition vocabulary (the application-controller's Serving condition).
const (
	ReasonServing                  = "Serving"
	ReasonEndpointNotServing       = "EndpointNotServing"
	ReasonNoFunctionalProbe        = "NoFunctionalProbeDeclared"
	ReasonServingProbeUnverifiable = "ServingProbeUnverifiable"
)

// InstanceLabel is Helm's release label on every rendered object.
const InstanceLabel = "app.kubernetes.io/instance"

var (
	// HTTPRouteGVR — Gateway API HTTPRoute (the app's declared route).
	HTTPRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	// ServiceGVR — core Service (the gateway's in-cluster address).
	ServiceGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
)

// Verdict is one condition's {status, reason, message}.
type Verdict struct {
	Status  string `json:"status"` // "True" | "False" | "Unknown"
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Probe is one HTTP probe target.
type Probe struct {
	// Addr — host:port to dial (the region gateway Service).
	Addr string
	// Host — the route hostname (Host header + TLS SNI).
	Host string
	// Path — the declared health path.
	Path string
	// TLS — dial with TLS (ServerName=Host).
	TLS bool
}

// Prober performs one HTTP probe and returns the response status code.
// Injectable so tests never open sockets.
type Prober interface {
	Probe(ctx context.Context, p Probe) (int, error)
}

// HTTPProber is the production prober: dial the gateway Service, send
// Host = route hostname, never follow redirects, 5s budget.
type HTTPProber struct{}

// Probe implements Prober.
func (HTTPProber) Probe(ctx context.Context, p Probe) (int, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, p.Addr)
		},
		// We are testing ROUTING (gateway → route → backend), not the
		// certificate chain; cert validity is cert-manager's Ready condition.
		TLSClientConfig:   &tls.Config{ServerName: p.Host, InsecureSkipVerify: true}, //nolint:gosec
		DisableKeepAlives: true,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	scheme := "http"
	if p.TLS {
		scheme = "https"
	}
	path := p.Path
	if path == "" {
		path = "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+p.Host+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// kindAbsent: the resource kind is not installed (no Gateway API here).
func kindAbsent(err error) bool {
	if err == nil {
		return false
	}
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err)
}

// ProbeRoutes probes every HTTPRoute the release installed in namespace.
// No routes = no verdicts (the caller decides what "nothing declared" means).
func ProbeRoutes(ctx context.Context, dyn dynamic.Interface, prober Prober, namespace, release, path string) []Verdict {
	seen := map[string]bool{}
	var routes []unstructured.Unstructured
	for _, sel := range []string{InstanceLabel + "=" + release, "helm.toolkit.fluxcd.io/name=" + release} {
		list, err := dyn.Resource(HTTPRouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			if kindAbsent(err) {
				continue
			}
			return []Verdict{{
				Status:  "Unknown",
				Reason:  ReasonServingProbeUnverifiable,
				Message: fmt.Sprintf("could not list HTTPRoutes in %s for %s: %v", namespace, release, err),
			}}
		}
		for _, it := range list.Items {
			if !seen[it.GetName()] {
				seen[it.GetName()] = true
				routes = append(routes, it)
			}
		}
	}
	out := make([]Verdict, 0, len(routes))
	for i := range routes {
		out = append(out, ProbeRoute(ctx, dyn, prober, &routes[i], path))
	}
	return out
}

// ProbeRoute probes one HTTPRoute through its parent gateway.
func ProbeRoute(ctx context.Context, dyn dynamic.Interface, prober Prober, rt *unstructured.Unstructured, path string) Verdict {
	id := rt.GetNamespace() + "/" + rt.GetName()
	hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
	parents, _, _ := unstructured.NestedSlice(rt.Object, "spec", "parentRefs")
	if len(hosts) == 0 || hosts[0] == "" || len(parents) == 0 {
		return Verdict{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("HTTPRoute %s declares no hostname or no parent Gateway; nothing to probe", id)}
	}
	pm, _ := parents[0].(map[string]interface{})
	gw, _ := pm["name"].(string)
	gwNS, _ := pm["namespace"].(string)
	if gwNS == "" {
		gwNS = rt.GetNamespace()
	}
	if gw == "" {
		return Verdict{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("HTTPRoute %s parentRefs[0] names no Gateway", id)}
	}
	svcName := "cilium-gateway-" + gw
	svc, err := dyn.Resource(ServiceGVR).Namespace(gwNS).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return Verdict{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("gateway Service %s/%s for HTTPRoute %s not readable: %v", gwNS, svcName, id, err)}
	}
	port, useTLS, ok := GatewayProbePort(svc)
	if !ok {
		return Verdict{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("gateway Service %s/%s exposes no 443/8443/80/8080 port", gwNS, svcName)}
	}
	p := Probe{
		Addr: fmt.Sprintf("%s.%s.svc.cluster.local:%d", svcName, gwNS, port),
		Host: hosts[0],
		Path: path,
		TLS:  useTLS,
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	target := fmt.Sprintf("%s://%s%s via gateway %s/%s", scheme, p.Host, p.Path, gwNS, gw)
	code, err := prober.Probe(ctx, p)
	if err != nil {
		return Verdict{Status: "False", Reason: ReasonEndpointNotServing,
			Message: fmt.Sprintf("GET %s failed: %v", target, err)}
	}
	if code >= 500 {
		return Verdict{Status: "False", Reason: ReasonEndpointNotServing,
			Message: fmt.Sprintf("GET %s answered HTTP %d", target, code)}
	}
	return Verdict{Status: "True", Reason: ReasonServing,
		Message: fmt.Sprintf("GET %s answered HTTP %d", target, code)}
}

// GatewayProbePort picks the TLS listener (443/8443 or a port named https*),
// else the plain one (80/8080).
func GatewayProbePort(svc *unstructured.Unstructured) (int64, bool, bool) {
	ports, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	var plain int64
	for _, p := range ports {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		n, _ := pm["port"].(int64)
		name, _ := pm["name"].(string)
		if n == 443 || n == 8443 || strings.HasPrefix(name, "https") {
			return n, true, true
		}
		if plain == 0 && (n == 80 || n == 8080) {
			plain = n
		}
	}
	if plain != 0 {
		return plain, false, true
	}
	return 0, false, false
}

// Worst rolls verdicts up: False > Unknown > True. No verdicts at all =
// NoFunctionalProbeDeclared.
func Worst(vs []Verdict) Verdict {
	if len(vs) == 0 {
		return Verdict{
			Status:  "Unknown",
			Reason:  ReasonNoFunctionalProbe,
			Message: "no functional probe declared: the app installed no HTTPRoute and no database that requires synchronous replication",
		}
	}
	var unknown *Verdict
	var msgs []string
	for i := range vs {
		switch vs[i].Status {
		case "False":
			return vs[i]
		case "Unknown":
			if unknown == nil {
				unknown = &vs[i]
			}
		default:
			msgs = append(msgs, vs[i].Message)
		}
	}
	if unknown != nil {
		return *unknown
	}
	sort.Strings(msgs)
	return Verdict{Status: "True", Reason: ReasonServing, Message: strings.Join(msgs, "; ")}
}
