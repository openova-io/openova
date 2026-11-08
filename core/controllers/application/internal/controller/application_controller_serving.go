// application_controller_serving.go — ADR-0015 D7: the per-region `Serving`
// condition, from a FUNCTIONAL probe.
//
// HelmRelease Ready says Helm applied the manifests. Backing Ready says the
// operator-managed object (CNPG Cluster) reports itself ready. Neither says the
// thing the User bought WORKS: on hw307 acmewalk307/wordpress-db reported
// Ready=True while every commit blocked, because the primary declares
// `synchronous.dataDurability: required` and its synchronous standby
// (wordpress-db-replica) has sat in "Setting up primary" for 58 days.
//
// Serving answers "does it work, here, now" with two cheap native probes:
//
//  1. ENDPOINT ANSWERS. For every HTTPRoute the app's HelmRelease installed
//     (label app.kubernetes.io/instance=<hr> or helm.toolkit.fluxcd.io/name=<hr>)
//     the controller GETs the Blueprint's declared healthPath (default "/")
//     THROUGH the region's own gateway Service (`cilium-gateway-<gateway>` in
//     the gateway's namespace, taken from the route's parentRefs[0]) with
//     Host = the route hostname. Status < 500 = serving. 5xx or a transport
//     error = not serving. The probe goes through the gateway rather than to
//     the pod because tenant namespaces carry NetworkPolicies that admit only
//     same-namespace + gateway traffic (acmewalk307 `allow-same-org`), so a
//     direct controller→pod GET would be a false negative by construction —
//     and the gateway path is the one a User's request actually takes.
//
//  2. DATABASE ACCEPTS A WRITE. For every backing CNPG Cluster that REQUIRES
//     synchronous replication (spec.postgresql.synchronous.dataDurability=
//     required, or legacy spec.minSyncReplicas>0) the controller counts the
//     synchronous standbys that can acknowledge a commit: the cluster's own
//     ready replicas (readyInstances-1, capped by maxStandbyNamesFromCluster)
//     plus every standbyNamesPre/Post entry that resolves to a same-namespace
//     CNPG Cluster whose own Ready condition is True. Fewer than required =
//     every commit blocks = not serving. A named standby that cannot be found
//     from here is UNKNOWN, never assumed present. No SQL, no credentials: the
//     native objects already carry the answer.
//
// Region scope. This controller instance observes only its own region's
// cluster. A region it cannot observe reports Serving=Unknown with reason
// ServingNotObservable — never True. Aggregate Serving: False if any observed
// region is False, else the primary region's verdict.
//
// An app that declares nothing probe-able (no HTTPRoute, no sync-required
// database) reports Serving=Unknown reason NoFunctionalProbeDeclared. That one
// reason does NOT gate Ready (open item: Blueprints must declare an endpoint or
// a probe-able backing so every app earns Serving) — otherwise every
// probe-less platform app would rotate to Ready=Unknown with nothing anyone
// could do about it from the app side.
package controller

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/core/controllers/internal/clusterregistry"
	"github.com/openova-io/openova/core/controllers/internal/placement"
)

// Serving condition vocabulary.
const (
	ConditionServing = "Serving"

	ReasonServing                      = "Serving"
	ReasonEndpointNotServing           = "EndpointNotServing"
	ReasonSynchronousStandbyMissing    = "SynchronousStandbyNotStreaming"
	ReasonSynchronousStandbyUnobserved = "SynchronousStandbyUnobservable"
	ReasonServingNotObservable         = "ServingNotObservable"
	ReasonNoFunctionalProbe            = "NoFunctionalProbeDeclared"
	ReasonServingProbeUnverifiable     = "ServingProbeUnverifiable"

	// ReasonProgressing — the HelmRelease(s) have not reported Ready yet.
	// Ready is Unknown in this state (ADR-0015 D7); before D7 it was True.
	ReasonProgressing = "Progressing"
)

var (
	// HTTPRouteGVR — Gateway API HTTPRoute (the app's declared route).
	HTTPRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	// ServiceGVR — core Service (the gateway's in-cluster address).
	ServiceGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
)

// condValue is one condition's {status, reason, message}.
type condValue struct {
	Status  string // "True" | "False" | "Unknown"
	Reason  string
	Message string
}

// ServingProbe is one HTTP probe target.
type ServingProbe struct {
	// Addr — host:port to dial (the region gateway Service).
	Addr string
	// Host — the route hostname (Host header + TLS SNI).
	Host string
	// Path — the declared health path.
	Path string
	// TLS — dial with TLS (ServerName=Host).
	TLS bool
}

// ServingProber performs one HTTP probe and returns the response status code.
// Injectable so tests never open sockets.
type ServingProber interface {
	Probe(ctx context.Context, p ServingProbe) (int, error)
}

// httpServingProber is the production prober: dial the gateway Service, send
// Host = route hostname, never follow redirects, 5s budget.
type httpServingProber struct{}

func (httpServingProber) Probe(ctx context.Context, p ServingProbe) (int, error) {
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

func (r *Reconciler) prober() ServingProber {
	if r.Prober != nil {
		return r.Prober
	}
	return httpServingProber{}
}

// servingRef is one HelmRelease whose installed objects the Serving probe
// examines: its name (= Helm release = instance label), the namespace its
// objects live in, and the canonical cluster ID it was fanned out to ("" for
// the single-HR host path).
type servingRef struct{ hr, namespace, cluster string }

// servingResult is the per-region + aggregate Serving verdict.
type servingResult struct {
	aggregate condValue
	byRegion  map[string]condValue
}

// observeServing runs the functional probes for the local region and returns
// per-region + aggregate verdicts. Read-only (lists + gets + HTTP GETs).
func (r *Reconciler) observeServing(
	ctx context.Context,
	app *unstructured.Unstructured,
	bp *unstructured.Unstructured,
	plan placement.Plan,
	perClusterStatus []map[string]interface{},
) servingResult {
	local := r.localRegion()
	refs := make([]servingRef, 0, len(perClusterStatus))
	for _, pcs := range perClusterStatus {
		hr, _ := pcs["hr"].(string)
		if hr == "" {
			continue
		}
		ns, _ := pcs["namespace"].(string)
		if ns == "" {
			ns = app.GetNamespace()
		}
		cl, _ := pcs["cluster"].(string)
		refs = append(refs, servingRef{hr: hr, namespace: ns, cluster: cl})
	}
	if len(refs) == 0 {
		refs = append(refs, servingRef{hr: app.GetName(), namespace: app.GetNamespace()})
	}

	// Probe every ref that lives in THIS region; a ref fanned out to another
	// region's cluster is not observable from here.
	var probed []condValue
	for _, ref := range refs {
		if ref.cluster != "" {
			if id, err := clusterregistry.Parse(ref.cluster); err == nil && id.Region != local {
				continue
			}
		}
		probed = append(probed, r.probeHTTPRoutes(ctx, bp, ref)...)
		probed = append(probed, r.probeSyncStandbys(ctx, ref)...)
	}
	localVerdict := worstServing(probed)

	localName := r.localPlanRegion(plan)
	res := servingResult{byRegion: map[string]condValue{}}
	for _, rp := range plan.Regions {
		if rp.Name == localName {
			res.byRegion[rp.Name] = localVerdict
			continue
		}
		res.byRegion[rp.Name] = condValue{
			Status: "Unknown",
			Reason: ReasonServingNotObservable,
			Message: fmt.Sprintf("region %s is not observable from the application-controller in region %s",
				rp.Name, local),
		}
	}

	// Aggregate: any observed False wins; else the primary region's verdict.
	if localVerdict.Status == "False" {
		res.aggregate = localVerdict
		return res
	}
	primary := plan.PrimaryRegion
	if primary == "" && len(plan.Regions) > 0 {
		primary = plan.Regions[0].Name
	}
	if v, ok := res.byRegion[primary]; ok {
		res.aggregate = v
	} else {
		res.aggregate = localVerdict
	}
	return res
}

// localPlanRegion picks which plan region this controller instance runs in.
// Cfg.LocalRegionName (CATALYST_REGION, e.g. "me-east-215-a") matched as a
// substring of the plan region name wins; otherwise a single-region plan's
// sole region is local; otherwise the region letter (A→index 0, B→index 1)
// indexes the plan, which puts the declared Primary at A — the convention the
// placement resolver and the cluster-ID registry share.
func (r *Reconciler) localPlanRegion(plan placement.Plan) string {
	if len(plan.Regions) == 0 {
		return ""
	}
	if n := strings.ToLower(strings.TrimSpace(r.Cfg.LocalRegionName)); n != "" {
		for _, rp := range plan.Regions {
			if strings.Contains(strings.ToLower(rp.Name), n) {
				return rp.Name
			}
		}
		return ""
	}
	if len(plan.Regions) == 1 {
		return plan.Regions[0].Name
	}
	idx := 0
	if r.localRegion() == clusterregistry.Region("B") {
		idx = 1
	}
	if idx < len(plan.Regions) {
		return plan.Regions[idx].Name
	}
	return ""
}

// worstServing rolls probe verdicts up: False > Unknown(observability) > True.
// No probes at all = NoFunctionalProbeDeclared.
func worstServing(vs []condValue) condValue {
	if len(vs) == 0 {
		return condValue{
			Status:  "Unknown",
			Reason:  ReasonNoFunctionalProbe,
			Message: "no functional probe declared: the app installed no HTTPRoute and no database that requires synchronous replication",
		}
	}
	var unknown *condValue
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
	return condValue{Status: "True", Reason: ReasonServing, Message: strings.Join(msgs, "; ")}
}

// blueprintHealthPath returns the declared health path: the launchDefault
// endpoint's healthPath, else the first non-empty one, else "/".
func blueprintHealthPath(bp *unstructured.Unstructured) string {
	if bp == nil {
		return "/"
	}
	eps, _, _ := unstructured.NestedSlice(bp.Object, "spec", "endpoints")
	first := ""
	for _, e := range eps {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		hp, _ := em["healthPath"].(string)
		if hp == "" {
			continue
		}
		if ld, _ := em["launchDefault"].(bool); ld {
			return hp
		}
		if first == "" {
			first = hp
		}
	}
	if first != "" {
		return first
	}
	return "/"
}

// probeHTTPRoutes probes every HTTPRoute the HR installed.
func (r *Reconciler) probeHTTPRoutes(ctx context.Context, bp *unstructured.Unstructured, ref servingRef) []condValue {
	seen := map[string]bool{}
	var routes []unstructured.Unstructured
	for _, sel := range []string{InstanceLabel + "=" + ref.hr, "helm.toolkit.fluxcd.io/name=" + ref.hr} {
		list, err := r.Dynamic.Resource(HTTPRouteGVR).Namespace(ref.namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			if backingKindAbsent(err) {
				continue
			}
			return []condValue{{
				Status:  "Unknown",
				Reason:  ReasonServingProbeUnverifiable,
				Message: fmt.Sprintf("could not list HTTPRoutes in %s for %s: %v", ref.namespace, ref.hr, err),
			}}
		}
		for _, it := range list.Items {
			if !seen[it.GetName()] {
				seen[it.GetName()] = true
				routes = append(routes, it)
			}
		}
	}
	path := blueprintHealthPath(bp)
	out := make([]condValue, 0, len(routes))
	for i := range routes {
		out = append(out, r.probeOneRoute(ctx, &routes[i], path))
	}
	return out
}

func (r *Reconciler) probeOneRoute(ctx context.Context, rt *unstructured.Unstructured, path string) condValue {
	id := rt.GetNamespace() + "/" + rt.GetName()
	hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
	parents, _, _ := unstructured.NestedSlice(rt.Object, "spec", "parentRefs")
	if len(hosts) == 0 || hosts[0] == "" || len(parents) == 0 {
		return condValue{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("HTTPRoute %s declares no hostname or no parent Gateway; nothing to probe", id)}
	}
	pm, _ := parents[0].(map[string]interface{})
	gw, _ := pm["name"].(string)
	gwNS, _ := pm["namespace"].(string)
	if gwNS == "" {
		gwNS = rt.GetNamespace()
	}
	if gw == "" {
		return condValue{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("HTTPRoute %s parentRefs[0] names no Gateway", id)}
	}
	svcName := "cilium-gateway-" + gw
	svc, err := r.Dynamic.Resource(ServiceGVR).Namespace(gwNS).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return condValue{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("gateway Service %s/%s for HTTPRoute %s not readable: %v", gwNS, svcName, id, err)}
	}
	port, useTLS, ok := gatewayProbePort(svc)
	if !ok {
		return condValue{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("gateway Service %s/%s exposes no 443/8443/80/8080 port", gwNS, svcName)}
	}
	p := ServingProbe{
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
	code, err := r.prober().Probe(ctx, p)
	if err != nil {
		return condValue{Status: "False", Reason: ReasonEndpointNotServing,
			Message: fmt.Sprintf("GET %s failed: %v", target, err)}
	}
	if code >= 500 {
		return condValue{Status: "False", Reason: ReasonEndpointNotServing,
			Message: fmt.Sprintf("GET %s answered HTTP %d", target, code)}
	}
	return condValue{Status: "True", Reason: ReasonServing,
		Message: fmt.Sprintf("GET %s answered HTTP %d", target, code)}
}

// gatewayProbePort picks the TLS listener (443/8443 or a port named https*),
// else the plain one (80/8080).
func gatewayProbePort(svc *unstructured.Unstructured) (int64, bool, bool) {
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

// probeSyncStandbys checks every backing CNPG Cluster the HR installed that
// requires synchronous replication.
func (r *Reconciler) probeSyncStandbys(ctx context.Context, ref servingRef) []condValue {
	list, err := r.Dynamic.Resource(CNPGClusterGVR).Namespace(ref.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: InstanceLabel + "=" + ref.hr,
	})
	if err != nil {
		if backingKindAbsent(err) {
			return nil
		}
		return []condValue{{Status: "Unknown", Reason: ReasonServingProbeUnverifiable,
			Message: fmt.Sprintf("could not list CNPG Clusters in %s for %s: %v", ref.namespace, ref.hr, err)}}
	}
	var out []condValue
	for i := range list.Items {
		if v, ok := r.syncStandbyVerdict(ctx, &list.Items[i]); ok {
			out = append(out, v)
		}
	}
	return out
}

// syncStandbyVerdict returns (verdict, true) for a Cluster that requires
// synchronous replication, (_, false) for one that does not (no claim).
func (r *Reconciler) syncStandbyVerdict(ctx context.Context, c *unstructured.Unstructured) (condValue, bool) {
	id := c.GetNamespace() + "/" + c.GetName()
	// A replica cluster (spec.replica.enabled) is not the writer.
	if en, _, _ := unstructured.NestedBool(c.Object, "spec", "replica", "enabled"); en {
		return condValue{}, false
	}
	required := int64(0)
	durability, _, _ := unstructured.NestedString(c.Object, "spec", "postgresql", "synchronous", "dataDurability")
	if sync, ok, _ := unstructured.NestedMap(c.Object, "spec", "postgresql", "synchronous"); ok && sync != nil {
		// CNPG default dataDurability is "required" when synchronous is set.
		if durability == "" || durability == "required" {
			if n, ok := sync["number"].(int64); ok {
				required = n
			} else {
				required = 1
			}
		}
	}
	legacy := ""
	if required == 0 {
		if m, ok, _ := unstructured.NestedInt64(c.Object, "spec", "minSyncReplicas"); ok && m > 0 {
			required = m
			legacy = " (spec.minSyncReplicas)"
		}
	}
	if required == 0 {
		return condValue{}, false
	}
	if durability == "" {
		durability = "required"
	}

	available := int64(0)
	// In-cluster replicas.
	instances, _, _ := unstructured.NestedInt64(c.Object, "spec", "instances")
	readyInst, hasReady, _ := unstructured.NestedInt64(c.Object, "status", "readyInstances")
	if hasReady && readyInst > 1 && instances > 1 {
		inCluster := readyInst - 1
		if max, ok, _ := unstructured.NestedInt64(c.Object, "spec", "postgresql", "synchronous", "maxStandbyNamesFromCluster"); ok && max < inCluster {
			inCluster = max
		}
		if inCluster > 0 {
			available += inCluster
		}
	}
	// Named external standbys.
	var named []string
	for _, k := range []string{"standbyNamesPre", "standbyNamesPost"} {
		if ss, ok, _ := unstructured.NestedStringSlice(c.Object, "spec", "postgresql", "synchronous", k); ok {
			named = append(named, ss...)
		}
	}
	var problems []string
	unobserved := ""
	for _, n := range named {
		sb, err := r.Dynamic.Resource(CNPGClusterGVR).Namespace(c.GetNamespace()).Get(ctx, n, metav1.GetOptions{})
		if err != nil {
			if unobserved == "" {
				unobserved = n
			}
			continue
		}
		st, rsn, msg := readReadyCondition(sb)
		if st == "True" {
			available++
			continue
		}
		if st == "" {
			st = "absent"
		}
		detail := fmt.Sprintf("standby %s is not ready (Ready=%s", n, st)
		if msg != "" {
			detail += ": " + msg
		} else if rsn != "" {
			detail += ": " + rsn
		}
		problems = append(problems, detail+")")
	}
	if available >= required {
		return condValue{Status: "True", Reason: ReasonServing,
			Message: fmt.Sprintf("primary %s has %d of %d required synchronous standby(s)", id, available, required)}, true
	}
	head := fmt.Sprintf("primary %s requires %d synchronous standby(s) (dataDurability=%s)%s", id, required, durability, legacy)
	if len(problems) > 0 {
		return condValue{Status: "False", Reason: ReasonSynchronousStandbyMissing,
			Message: head + "; " + strings.Join(problems, "; ") + " — every commit blocks"}, true
	}
	if unobserved != "" {
		return condValue{Status: "Unknown", Reason: ReasonSynchronousStandbyUnobserved,
			Message: fmt.Sprintf("%s; standby %s is not observable from this region", head, unobserved)}, true
	}
	return condValue{Status: "False", Reason: ReasonSynchronousStandbyMissing,
		Message: fmt.Sprintf("%s; %d available — every commit blocks", head, available)}, true
}

// withRegionServing stamps each status.regions[] row's `conditions` with its
// region's Serving verdict (lastTransitionTime is reconciled against the prior
// object by preserveRegionTransitions in updateStatus).
func withRegionServing(regions []map[string]interface{}, res servingResult) []map[string]interface{} {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, rs := range regions {
		name, _ := rs["name"].(string)
		v, ok := res.byRegion[name]
		if !ok {
			continue
		}
		rs["conditions"] = []interface{}{map[string]interface{}{
			"type":               ConditionServing,
			"status":             v.Status,
			"reason":             v.Reason,
			"message":            v.Message,
			"lastTransitionTime": now,
		}}
	}
	return regions
}

// preserveConditionTransitions keeps a condition's prior lastTransitionTime
// when its status did not change (ADR-0015 D7: `since` must mean since).
func preserveConditionTransitions(prior interface{}, next []interface{}) []interface{} {
	type seen struct{ status, ltt string }
	prev := map[string]seen{}
	if ps, ok := prior.([]interface{}); ok {
		for _, c := range ps {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := cm["type"].(string)
			st, _ := cm["status"].(string)
			ltt, _ := cm["lastTransitionTime"].(string)
			prev[t] = seen{st, ltt}
		}
	}
	for _, c := range next {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		st, _ := cm["status"].(string)
		if p, ok := prev[t]; ok && p.status == st && p.ltt != "" {
			cm["lastTransitionTime"] = p.ltt
		}
	}
	return next
}

// preserveRegionTransitions keeps each region row's lastTransitionTime unless
// its ready/replicas changed, and each per-region condition's
// lastTransitionTime unless its status changed. Region rows are matched by
// name. A brand-new region row gets `now`.
func preserveRegionTransitions(prior interface{}, next []interface{}, now string) []interface{} {
	prev := map[string]map[string]interface{}{}
	if ps, ok := prior.([]interface{}); ok {
		for _, r := range ps {
			if rm, ok := r.(map[string]interface{}); ok {
				if n, _ := rm["name"].(string); n != "" {
					prev[n] = rm
				}
			}
		}
	}
	for _, r := range next {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		n, _ := rm["name"].(string)
		old, had := prev[n]
		if !had {
			if _, set := rm["lastTransitionTime"]; !set {
				rm["lastTransitionTime"] = now
			}
			continue
		}
		if fmt.Sprint(old["ready"]) == fmt.Sprint(rm["ready"]) &&
			fmt.Sprint(old["replicas"]) == fmt.Sprint(rm["replicas"]) {
			if ltt, _ := old["lastTransitionTime"].(string); ltt != "" {
				rm["lastTransitionTime"] = ltt
			}
		}
		if nc, ok := rm["conditions"].([]interface{}); ok {
			rm["conditions"] = preserveConditionTransitions(old["conditions"], nc)
		}
	}
	return next
}
