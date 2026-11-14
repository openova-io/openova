// Package topologyread serves two read routes of catalyst-api that the
// acceptance suite and the console use to name existing objects
// (ADR-0015 D3 and D4):
//
//	GET /api/v1/sovereigns/{id}/regions       the Sovereign's region registry
//	GET /api/v1/sovereigns/{id}/environments  the Environment objects
//
// Both answer only on a Sovereign: a catalyst-api whose environment has
// SOVEREIGN_FQDN. Elsewhere (the mothership) they answer 404
// not-a-sovereign, because one mothership process holds the clusters of
// many Sovereigns. A Sovereign serves only itself, so the {id} of the
// path is not compared with anything.
//
// The regions are regions.Registry(), the one region list of the API,
// in its order; the install routes accept exactly these ids.
//
// The Environments come from the informer cache, never from the API
// server. They are read from the cluster this catalyst-api runs in, the
// cluster the install routes check an Environment against. That cluster
// is chosen as every other Sovereign view of the API chooses it: the
// cluster whose id is the {id} of the path; else the only cluster of the
// cache; else the first cluster whose id starts with "sovereign-"; else
// the first cluster in sorted order. While that cluster's Environment
// informer has not finished its first list, the route answers 503
// cache-not-synced: a partial list is never served.
package topologyread

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/regions"
)

// EnvironmentKind is the name of the Environment kind in the informer cache.
const EnvironmentKind = "environment"

// lister is the part of the informer cache this package reads.
// *k8scache.Factory has these three methods.
type lister interface {
	Clusters() []string
	Synced() map[string]map[string]bool
	List(clusterID, kindName string, sel labels.Selector) ([]*unstructured.Unstructured, time.Duration, error)
}

// Routes holds the two route handlers.
type Routes struct {
	cache       func() lister
	registry    func() []regions.Region
	onSovereign func() bool
}

// New returns the routes. cache returns the informer cache of the
// process, or nil while it is not set up; it is called on every request.
func New(cache func() *k8scache.Factory) *Routes {
	return &Routes{
		cache: func() lister {
			if f := cache(); f != nil {
				return f
			}
			return nil
		},
		registry: regions.Registry,
		onSovereign: func() bool {
			return strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) != ""
		},
	}
}

// Environment is one item of the Environment list. Regions is the
// object's spec.regions, verbatim.
type Environment struct {
	Name            string `json:"name"`
	OrganizationRef string `json:"organizationRef"`
	EnvType         string `json:"envType"`
	Placement       string `json:"placement"`
	Regions         []any  `json:"regions"`
}

// EnvironmentList is the body of a 200 answer of the Environment list.
type EnvironmentList struct {
	Items []Environment `json:"items"`
	Total int           `json:"total"`
}

// Regions serves GET /api/v1/sovereigns/{id}/regions. The body is
// {"regions": [{"id", "label", "primary", "provider"}, ...]}.
func (rt *Routes) Regions(w http.ResponseWriter, r *http.Request) {
	if !rt.onSovereign() {
		refuse(w, http.StatusNotFound, "not-a-sovereign", "this catalyst-api does not run on a Sovereign")
		return
	}
	reg := rt.registry()
	if reg == nil {
		reg = []regions.Region{}
	}
	write(w, http.StatusOK, map[string]any{"regions": reg})
}

// Environments serves GET /api/v1/sovereigns/{id}/environments. The body
// is an EnvironmentList, sorted by name.
func (rt *Routes) Environments(w http.ResponseWriter, r *http.Request) {
	if !rt.onSovereign() {
		refuse(w, http.StatusNotFound, "not-a-sovereign", "this catalyst-api does not run on a Sovereign")
		return
	}
	c := rt.cache()
	if c == nil {
		refuse(w, http.StatusServiceUnavailable, "cache-unavailable", "the informer cache is not set up")
		return
	}
	cluster, ok := homeCluster(chi.URLParam(r, "id"), c.Clusters())
	if !ok {
		refuse(w, http.StatusServiceUnavailable, "cache-unavailable", "the informer cache holds no cluster")
		return
	}
	objs, _, err := c.List(cluster, EnvironmentKind, labels.Everything())
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "cache-unavailable", err.Error())
		return
	}
	if !c.Synced()[cluster][EnvironmentKind] {
		refuse(w, http.StatusServiceUnavailable, "cache-not-synced",
			"the Environment informer of cluster "+cluster+" has not finished its first list")
		return
	}
	items := make([]Environment, 0, len(objs))
	for _, o := range objs {
		items = append(items, environmentOf(o))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	write(w, http.StatusOK, EnvironmentList{Items: items, Total: len(items)})
}

// homeCluster picks the cluster this catalyst-api runs in, by the rule
// of the package comment. ok is false when the cache holds no cluster.
func homeCluster(pathID string, clusters []string) (string, bool) {
	if len(clusters) == 0 {
		return "", false
	}
	sorted := append([]string(nil), clusters...)
	sort.Strings(sorted)
	for _, c := range sorted {
		if c == pathID {
			return c, true
		}
	}
	if len(sorted) == 1 {
		return sorted[0], true
	}
	for _, c := range sorted {
		if strings.HasPrefix(c, "sovereign-") {
			return c, true
		}
	}
	return sorted[0], true
}

// environmentOf reads one Environment object. An absent field is the
// empty value; spec.regions absent is an empty list.
func environmentOf(o *unstructured.Unstructured) Environment {
	org, _, _ := unstructured.NestedString(o.Object, "spec", "organizationRef")
	envType, _, _ := unstructured.NestedString(o.Object, "spec", "envType")
	placement, _, _ := unstructured.NestedString(o.Object, "spec", "placement")
	regs, _, _ := unstructured.NestedSlice(o.Object, "spec", "regions")
	if regs == nil {
		regs = []any{}
	}
	return Environment{
		Name:            o.GetName(),
		OrganizationRef: org,
		EnvType:         envType,
		Placement:       placement,
		Regions:         regs,
	}
}

// refuse writes {"error": code, "detail": detail}.
func refuse(w http.ResponseWriter, status int, code, detail string) {
	write(w, status, map[string]string{"error": code, "detail": detail})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
