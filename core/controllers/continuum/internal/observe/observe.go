// Package observe reads the NATIVE status of topology-transition members in
// the region this Continuum runs in, and carries it to the other regions.
//
// Nothing here is derived: every field is copied from the resource that owns
// it — the HelmRelease's own Ready condition and the values Flux applied, the
// CNPG Cluster's own status (demotionToken, lastPromotionToken, phase, Ready
// condition), the Application's own per-region Serving condition (ADR-0015
// D7). The package only READS (get/list); it has no write path.
//
// Regions do not share an API server, so each region's Continuum serves its
// own observations over HTTP (GET /v1/observations) behind a region-addressable
// ClusterMesh Service `continuum-<region-id>` (ClusterIP — never a nodePort),
// the same pattern as gitea-<region-id>. The acting region reads its peers'
// observations through it; a region that does not answer is "unreachable",
// which is the region-loss signal.
package observe

import (
	"context"
	"encoding/json"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
	"github.com/openova-io/openova/core/controllers/pkg/serving"
)

// GVRs read by the observer.
var (
	HelmReleaseGVR = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	CNPGClusterGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	ApplicationGVR = schema.GroupVersionResource{Group: "apps.openova.io", Version: "v1", Resource: "applications"}
)

// Labels the application-controller stamps on every rendered HelmRelease
// (core/controllers/application/internal/render/topology.go).
const (
	LabelApp    = "catalyst.openova.io/app"
	LabelRegion = "catalyst.openova.io/region"
)

// Target is one member to observe.
type Target struct {
	Namespace string `json:"namespace"`
	App       string `json:"app"`
	Stateful  bool   `json:"stateful"`
	// HelmRelease ("<namespace>/<name>") names the member's HelmRelease
	// directly. Set for a PLATFORM Application, whose HelmRelease is the
	// bootstrap-kit slot's (not labelled by the application-controller);
	// empty = find the HelmReleases by the application-controller's labels.
	HelmRelease string `json:"helmRelease,omitempty"`
}

// AnnotationTopologyPrimary is stamped by a contract chart that receives its
// topology through `valuesFrom` (bp-cnpg-pair, bp-postgres) on the CNPG
// Cluster it renders: the `openova.topology.primary` Helm actually APPLIED.
// The slot HelmRelease's spec.values does not carry the contract (it arrives
// from the region file's carrier ConfigMap), so this is how a region reports
// what Flux has applied — read from the rendered object, never inferred.
const AnnotationTopologyPrimary = "openova.io/topology-primary"

// Key matches transition.Member.Key().
func (t Target) Key() string { return t.Namespace + "/" + t.App }

// Observer reads the local region.
type Observer struct {
	Dyn    dynamic.Interface
	Region string
	// Prober runs the functional endpoint probe (pkg/serving); nil = the
	// production HTTP prober.
	Prober serving.Prober
}

func (o *Observer) prober() serving.Prober {
	if o.Prober != nil {
		return o.Prober
	}
	return serving.HTTPProber{}
}

// Observe returns the native status of every target in this region.
func (o *Observer) Observe(ctx context.Context, targets []Target) map[string]transition.MemberStatus {
	out := make(map[string]transition.MemberStatus, len(targets))
	for _, t := range targets {
		out[t.Key()] = o.observeOne(ctx, t)
	}
	return out
}

func readyCondition(obj *unstructured.Unstructured) (bool, string) {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok || m["type"] != "Ready" {
			continue
		}
		msg, _ := m["message"].(string)
		return m["status"] == "True", msg
	}
	return false, "no Ready condition"
}

// releaseName is Flux's default: spec.releaseName, else [targetNamespace-]name.
func releaseName(hr *unstructured.Unstructured) string {
	if n, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName"); n != "" {
		return n
	}
	if tn, _, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace"); tn != "" {
		return tn + "-" + hr.GetName()
	}
	return hr.GetName()
}

func targetNamespace(hr *unstructured.Unstructured) string {
	if tn, _, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace"); tn != "" {
		return tn
	}
	return hr.GetNamespace()
}

// releases finds the member's HelmReleases in this region. An empty result
// carries the reason.
func (o *Observer) releases(ctx context.Context, t Target) ([]unstructured.Unstructured, string) {
	var hrs []unstructured.Unstructured
	if t.HelmRelease != "" {
		ns, name, _ := strings.Cut(t.HelmRelease, "/")
		hr, err := o.Dyn.Resource(HelmReleaseGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Sprintf("HelmRelease %s: %v", t.HelmRelease, err)
		}
		return append(hrs, *hr), ""
	}
	sel := fmt.Sprintf("%s=%s,%s=%s", LabelApp, t.App, LabelRegion, o.Region)
	l, err := o.Dyn.Resource(HelmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, "list HelmReleases: " + err.Error()
	}
	for _, hr := range l.Items {
		if hr.GetNamespace() == t.Namespace || targetNamespace(&hr) == t.Namespace {
			hrs = append(hrs, hr)
		}
	}
	sort.Slice(hrs, func(i, j int) bool { return hrs[i].GetName() < hrs[j].GetName() })
	if len(hrs) == 0 {
		return nil, "no HelmRelease labelled " + sel
	}
	return hrs, ""
}

// ServingOf runs the functional endpoint probe (pkg/serving — the same one
// the application-controller runs in its own region) for every HTTPRoute the
// member's HelmReleases installed in THIS region. It is how a region that
// does not hold the Application still gets a Serving verdict: the writer
// region's application-controller asks for it over /v1/serving.
func (o *Observer) ServingOf(ctx context.Context, t Target, path string) serving.Verdict {
	hrs, why := o.releases(ctx, t)
	if len(hrs) == 0 {
		return serving.Verdict{Status: "Unknown", Reason: serving.ReasonServingProbeUnverifiable,
			Message: "region " + o.Region + ": " + why}
	}
	var vs []serving.Verdict
	for i := range hrs {
		vs = append(vs, serving.ProbeRoutes(ctx, o.Dyn, o.prober(), targetNamespace(&hrs[i]), hrs[i].GetName(), path)...)
	}
	return serving.Worst(vs)
}

func (o *Observer) observeOne(ctx context.Context, t Target) transition.MemberStatus {
	ms := transition.MemberStatus{Serving: transition.Unknown}
	hrs, why := o.releases(ctx, t)
	if len(hrs) == 0 {
		ms.ReleaseMessage = why
		return ms
	}
	ms.Observed = true
	ms.ReleaseReady = true
	var msgs []string
	for i := range hrs {
		hr := &hrs[i]
		p, _, _ := unstructured.NestedString(hr.Object, "spec", "values", "openova", "topology", "primary")
		if ms.AppliedPrimary == "" {
			ms.AppliedPrimary = p
		} else if p != ms.AppliedPrimary {
			ms.AppliedPrimary = "" // legs disagree: nothing is applied consistently
		}
		ready, msg := readyCondition(hr)
		og, _, _ := unstructured.NestedInt64(hr.Object, "status", "observedGeneration")
		if !ready || og != hr.GetGeneration() {
			ms.ReleaseReady = false
			if og != hr.GetGeneration() {
				msg = fmt.Sprintf("generation %d not yet reconciled (observed %d)", hr.GetGeneration(), og)
			}
			msgs = append(msgs, hr.GetName()+": "+msg)
		}
	}
	ms.ReleaseMessage = strings.Join(msgs, "; ")
	if t.Stateful {
		ms.DB = o.observeDB(ctx, &hrs[0])
		if ms.AppliedPrimary == "" && t.HelmRelease != "" {
			// The contract arrived through valuesFrom: what Helm applied is
			// stamped on the rendered Cluster (AnnotationTopologyPrimary).
			ms.AppliedPrimary = ms.DB.TopologyPrimary
		}
	}
	ms.Serving = o.observeServing(ctx, t)
	return ms
}

func (o *Observer) observeDB(ctx context.Context, hr *unstructured.Unstructured) transition.DBStatus {
	sel := "app.kubernetes.io/instance=" + releaseName(hr)
	l, err := o.Dyn.Resource(CNPGClusterGVR).Namespace(targetNamespace(hr)).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil || len(l.Items) == 0 {
		return transition.DBStatus{}
	}
	items := l.Items
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	c := &items[0]
	for i := range items {
		if self, _, _ := unstructured.NestedString(items[i].Object, "spec", "replica", "self"); self == items[i].GetName() {
			c = &items[i]
			break
		}
	}
	return DBStatusOf(c)
}

// DBStatusOf reads one CNPG Cluster's own status. A Cluster is the primary
// when its distributed-topology stanza names itself primary (or it has no
// replica stanza at all) and it is not an explicit replica.
func DBStatusOf(c *unstructured.Unstructured) transition.DBStatus {
	d := transition.DBStatus{Found: true, Cluster: c.GetName()}
	self, _, _ := unstructured.NestedString(c.Object, "spec", "replica", "self")
	primary, _, _ := unstructured.NestedString(c.Object, "spec", "replica", "primary")
	enabled, hasEnabled, _ := unstructured.NestedBool(c.Object, "spec", "replica", "enabled")
	_, hasReplica, _ := unstructured.NestedMap(c.Object, "spec", "replica")
	switch {
	case hasEnabled && enabled:
		d.Primary = false
	case !hasReplica:
		d.Primary = true
	default:
		d.Primary = self != "" && self == primary
	}
	d.Ready, _ = readyCondition(c)
	d.Phase, _, _ = unstructured.NestedString(c.Object, "status", "phase")
	d.DemotionToken, _, _ = unstructured.NestedString(c.Object, "status", "demotionToken")
	d.LastPromotionToken, _, _ = unstructured.NestedString(c.Object, "status", "lastPromotionToken")
	d.TopologyPrimary = c.GetAnnotations()[AnnotationTopologyPrimary]
	return d
}

// observeServing reads the Application's own Serving condition for this
// region (ADR-0015 D7). Unknown when the Application is not in this region's
// API or reports no condition for it.
func (o *Observer) observeServing(ctx context.Context, t Target) transition.Tri {
	app, err := o.Dyn.Resource(ApplicationGVR).Namespace(t.Namespace).Get(ctx, t.App, metav1.GetOptions{})
	if err != nil {
		return transition.Unknown
	}
	regions, _, _ := unstructured.NestedSlice(app.Object, "status", "regions")
	for _, r := range regions {
		rm, ok := r.(map[string]interface{})
		if !ok || rm["name"] != o.Region {
			continue
		}
		conds, _ := rm["conditions"].([]interface{})
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok || cm["type"] != "Serving" {
				continue
			}
			switch cm["status"] {
			case "True":
				return transition.True
			case "False":
				return transition.False
			}
		}
	}
	return transition.Unknown
}

// Response is the GET /v1/observations body.
type Response struct {
	Region     string                             `json:"region"`
	ObservedAt string                             `json:"observedAt"`
	Members    map[string]transition.MemberStatus `json:"members"`
}

// Handler serves this region's observations. Query:
// members=<ns>/<app>[:s][@<hr-ns>/<hr-name>],… (":s" marks a stateful member,
// "@…" names its HelmRelease). Read-only.
func Handler(o *Observer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		targets := ParseTargets(r.URL.Query().Get("members"))
		resp := Response{Region: o.Region, ObservedAt: time.Now().UTC().Format(time.RFC3339), Members: o.Observe(r.Context(), targets)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// EncodeTargets is the members= query value for targets.
func EncodeTargets(ts []Target) string {
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		s := t.Key()
		if t.Stateful {
			s += ":s"
		}
		if t.HelmRelease != "" {
			s += "@" + t.HelmRelease
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

// ParseTargets decodes EncodeTargets.
func ParseTargets(q string) []Target {
	var out []Target
	for _, p := range strings.Split(q, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p, hr, _ := strings.Cut(p, "@")
		stateful := strings.HasSuffix(p, ":s")
		p = strings.TrimSuffix(p, ":s")
		ns, app, ok := strings.Cut(p, "/")
		if !ok || ns == "" || app == "" {
			continue
		}
		out = append(out, Target{Namespace: ns, App: app, Stateful: stateful, HelmRelease: hr})
	}
	return out
}

// Peers fetches another region's observations.
type Peers struct {
	HTTP *http.Client
	// URLFor returns the observation base URL of a region
	// (http://continuum-<region>.<ns>.svc.cluster.local:<port>).
	URLFor func(region string) string
}

// Fetch reads region's observations of targets.
func (p Peers) Fetch(ctx context.Context, region string, targets []Target) (map[string]transition.MemberStatus, error) {
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	u := strings.TrimRight(p.URLFor(region), "/") + "/v1/observations?members=" + url.QueryEscape(EncodeTargets(targets))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("observations from %s: HTTP %d", region, resp.StatusCode)
	}
	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("observations from %s: %w", region, err)
	}
	if body.Region != region {
		return nil, fmt.Errorf("observations: asked %s, answered by %s", region, body.Region)
	}
	return body.Members, nil
}

// ServingResponse is the GET /v1/serving body.
type ServingResponse struct {
	Region string `json:"region"`
	serving.Verdict
}

// ServingHandler serves this region's functional Serving verdict for one
// member: GET /v1/serving?namespace=<ns>&app=<app>[&path=<healthPath>]
// [&helmRelease=<ns>/<name>]. Read-only (lists, gets, one HTTP GET per route
// through this region's own gateway).
func ServingHandler(o *Observer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		t := Target{Namespace: q.Get("namespace"), App: q.Get("app"), HelmRelease: q.Get("helmRelease")}
		if t.Namespace == "" || t.App == "" {
			http.Error(w, "namespace and app are required", http.StatusBadRequest)
			return
		}
		path := q.Get("path")
		if path == "" {
			path = "/"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ServingResponse{Region: o.Region, Verdict: o.ServingOf(r.Context(), t, path)})
	})
}

// ClusterHandler serves this region's view of ONE CNPG Cluster by name:
// GET /v1/clusters?namespace=<ns>&name=<cluster>. It lets a peer region's
// readiness probe confirm a cross-region synchronous standby it cannot see
// through its own API server (ADR-0015 D1 removed cross-region kubeconfigs;
// D7 Serving needs the standby's state — live hw307 2026-11-11, Refs #7067).
// Read-only. found=false when the Cluster does not exist here.
func ClusterHandler(o *Observer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		ns, name := r.URL.Query().Get("namespace"), r.URL.Query().Get("name")
		if ns == "" || name == "" {
			http.Error(w, "namespace and name are required", http.StatusBadRequest)
			return
		}
		out := transition.DBStatus{Cluster: name}
		c, err := o.Dyn.Resource(CNPGClusterGVR).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
		if err == nil {
			out = DBStatusOf(c)
		} else if !apierrors.IsNotFound(err) {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Region string `json:"region"`
			transition.DBStatus
		}{o.Region, out})
	})
}
