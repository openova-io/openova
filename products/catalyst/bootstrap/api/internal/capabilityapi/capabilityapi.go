// Package capabilityapi serves the two read routes of the capability registry
// (ADR-0020 S6, work package SVC-280):
//
//	GET /api/v1/catalog/capabilities         every capability of the index
//	GET /api/v1/catalog/capabilities/{name}  one capability; 404 capability-not-found
//
// An item names the capability, its newest contract with status current, the
// effective default implementation and whether an Organization may choose,
// and lists every Blueprint that provides the capability, each with whether
// this Sovereign offers it and whether it can be used here. A caller who is
// not a sovereign-admin sees the offered implementations only.
//
// The capabilities and their contracts come from the index the binary embeds.
// The providers come from the Blueprint objects of the informer cache, the
// objects the install route resolves against, through capability.NewRegistry
// with no catalogue choices (those arrive with the Sovereign's catalogue
// block). The routes answer only on a Sovereign, a catalyst-api whose
// environment has SOVEREIGN_FQDN, and read the cluster it runs in: the only
// cluster of the cache, else the first whose id starts with "sovereign-",
// else the first in sorted order. While that cluster's Blueprint informer has
// not finished its first list they answer 503 cache-not-synced: a partial
// list is never served. Errors are {"error": {"code", "message"}}.
package capabilityapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/lifecycle"
)

// BlueprintKind is the name of the Blueprint kind in the informer cache.
const BlueprintKind = "blueprint"

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
	index       func() (capability.Index, error)
	avail       capability.Availability
	admin       func(*auth.Claims) bool
	onSovereign func() bool
}

// embeddedIndex is the capability index of this binary, internal/catalog/capabilities.json.
var embeddedIndex = sync.OnceValues(func() (capability.Index, error) {
	return capability.LoadIndex(catalog.CapabilitiesJSON())
})

// New returns the routes. cache returns the informer cache of the process, or
// nil while it is not set up; it is called on every request. No cloud-native
// implementation is available until the cloud profile says which composite
// kinds the Sovereign's cloud offers (capability.NoCloudProfile).
func New(cache func() *k8scache.Factory) *Routes {
	return &Routes{
		cache: func() lister {
			if f := cache(); f != nil {
				return f
			}
			return nil
		},
		index: embeddedIndex,
		avail: capability.NoCloudProfile{},
		admin: lifecycle.SovereignAdmin,
		onSovereign: func() bool {
			return strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) != ""
		},
	}
}

// Implementation is one Blueprint that provides the capability.
type Implementation struct {
	Blueprint         string `json:"blueprint"`
	Title             string `json:"title"`
	Implementation    string `json:"implementation"`
	Lifecycle         string `json:"lifecycle"`
	Offered           bool   `json:"offered"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason"`
}

// Item is one capability.
type Item struct {
	Name               string           `json:"name"`
	Title              string           `json:"title"`
	Contract           string           `json:"contract"`
	Default            string           `json:"default"`
	OrganizationChoice string           `json:"organizationChoice"`
	Implementations    []Implementation `json:"implementations"`
}

// ItemList is the body of a 200 answer of the list route.
type ItemList struct {
	Items []Item `json:"items"`
}

// List serves GET /api/v1/catalog/capabilities: one item per capability of
// the index, by name.
func (rt *Routes) List(w http.ResponseWriter, r *http.Request) {
	idx, ok := rt.loadIndex(w)
	if !ok {
		return
	}
	reg, ok := rt.registry(w, idx)
	if !ok {
		return
	}
	admin := rt.admin(auth.ClaimsFromContext(r.Context()))
	out := ItemList{Items: []Item{}}
	for _, name := range idx.Capabilities() {
		it, err := rt.item(idx, reg, name, admin)
		if err != nil {
			refuse(w, http.StatusInternalServerError, "capability-index-invalid", err.Error())
			return
		}
		out.Items = append(out.Items, it)
	}
	write(w, http.StatusOK, out)
}

// Get serves GET /api/v1/catalog/capabilities/{name}: the item of one
// capability, or 404 capability-not-found when the index has no contract
// file of that name.
func (rt *Routes) Get(w http.ResponseWriter, r *http.Request) {
	idx, ok := rt.loadIndex(w)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	known := false
	for _, c := range idx.Capabilities() {
		known = known || c == name
	}
	if !known {
		refuse(w, http.StatusNotFound, "capability-not-found", fmt.Sprintf("no capability %q in the capability index", name))
		return
	}
	reg, ok := rt.registry(w, idx)
	if !ok {
		return
	}
	it, err := rt.item(idx, reg, name, rt.admin(auth.ClaimsFromContext(r.Context())))
	if err != nil {
		refuse(w, http.StatusInternalServerError, "capability-index-invalid", err.Error())
		return
	}
	write(w, http.StatusOK, it)
}

// loadIndex returns the capability index, or writes the refusal.
func (rt *Routes) loadIndex(w http.ResponseWriter) (capability.Index, bool) {
	if !rt.onSovereign() {
		refuse(w, http.StatusNotFound, "not-a-sovereign", "this catalyst-api does not run on a Sovereign")
		return nil, false
	}
	idx, err := rt.index()
	if err != nil {
		refuse(w, http.StatusInternalServerError, "capability-index-invalid", err.Error())
		return nil, false
	}
	return idx, true
}

// registry builds the registry from the Blueprint objects of the home
// cluster, or writes the refusal.
func (rt *Routes) registry(w http.ResponseWriter, idx capability.Index) (capability.Registry, bool) {
	c := rt.cache()
	if c == nil {
		refuse(w, http.StatusServiceUnavailable, "cache-unavailable", "the informer cache is not set up")
		return nil, false
	}
	cluster, ok := homeCluster(c.Clusters())
	if !ok {
		refuse(w, http.StatusServiceUnavailable, "cache-unavailable", "the informer cache holds no cluster")
		return nil, false
	}
	if !c.Synced()[cluster][BlueprintKind] {
		refuse(w, http.StatusServiceUnavailable, "cache-not-synced",
			"the Blueprint informer of cluster "+cluster+" has not finished its first list")
		return nil, false
	}
	bps, _, err := c.List(cluster, BlueprintKind, labels.Everything())
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "cache-unavailable", err.Error())
		return nil, false
	}
	return capability.NewRegistry(idx, bps, nil, rt.avail), true
}

// item is the answer for one capability. Without admin, only the offered
// implementations are listed.
func (rt *Routes) item(idx capability.Index, reg capability.Registry, name string, admin bool) (Item, error) {
	current, ok := idx.Current(name)
	if !ok {
		return Item{}, fmt.Errorf("capability %s has no contract with status current", name)
	}
	offer := reg.Offer(name)
	offered := map[string]bool{}
	for _, im := range offer.Offered {
		offered[im.Blueprint] = true
	}
	it := Item{Name: name, Title: idx.Title(name), Contract: current.Version, Default: offer.Default,
		OrganizationChoice: offer.OrganizationChoice, Implementations: []Implementation{}}
	for _, im := range reg.Implementations(name) {
		if !admin && !offered[im.Blueprint] {
			continue
		}
		available, reason := rt.available(name, im)
		it.Implementations = append(it.Implementations, Implementation{
			Blueprint: im.Blueprint, Title: im.Title, Implementation: im.Implementation, Lifecycle: im.Lifecycle,
			Offered: offered[im.Blueprint], Available: available, UnavailableReason: reason,
		})
	}
	return it, nil
}

// available follows the registry's rule: an in-cluster implementation can
// always be used, a cloud-native one when the Availability says so.
func (rt *Routes) available(name string, im capability.Implementation) (bool, string) {
	switch im.Implementation {
	case "in-cluster":
		return true, ""
	case "cloud-native":
		return rt.avail.Available(name)
	}
	return false, fmt.Sprintf("unknown implementation kind %q", im.Implementation)
}

// homeCluster picks the cluster this catalyst-api runs in, by the rule of the
// package comment. ok is false when the cache holds no cluster.
func homeCluster(clusters []string) (string, bool) {
	if len(clusters) == 0 {
		return "", false
	}
	sorted := append([]string(nil), clusters...)
	sort.Strings(sorted)
	for _, c := range sorted {
		if strings.HasPrefix(c, "sovereign-") {
			return c, true
		}
	}
	return sorted[0], true
}

// refuse writes {"error": {"code": code, "message": message}}.
func refuse(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
