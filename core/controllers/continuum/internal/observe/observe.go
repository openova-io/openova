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
}

// Key matches transition.Member.Key().
func (t Target) Key() string { return t.Namespace + "/" + t.App }

// Observer reads the local region.
type Observer struct {
	Dyn    dynamic.Interface
	Region string
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

func (o *Observer) observeOne(ctx context.Context, t Target) transition.MemberStatus {
	ms := transition.MemberStatus{Serving: transition.Unknown}
	sel := fmt.Sprintf("%s=%s,%s=%s", LabelApp, t.App, LabelRegion, o.Region)
	l, err := o.Dyn.Resource(HelmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		ms.ReleaseMessage = "list HelmReleases: " + err.Error()
		return ms
	}
	var hrs []unstructured.Unstructured
	for _, hr := range l.Items {
		if hr.GetNamespace() == t.Namespace || targetNamespace(&hr) == t.Namespace {
			hrs = append(hrs, hr)
		}
	}
	sort.Slice(hrs, func(i, j int) bool { return hrs[i].GetName() < hrs[j].GetName() })
	if len(hrs) == 0 {
		ms.ReleaseMessage = "no HelmRelease labelled " + sel
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

// Handler serves this region's observations. Query: members=<ns>/<app>[:s],…
// (":s" marks a stateful member). Read-only.
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
		stateful := strings.HasSuffix(p, ":s")
		p = strings.TrimSuffix(p, ":s")
		ns, app, ok := strings.Cut(p, "/")
		if !ok || ns == "" || app == "" {
			continue
		}
		out = append(out, Target{Namespace: ns, App: app, Stateful: stateful})
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
