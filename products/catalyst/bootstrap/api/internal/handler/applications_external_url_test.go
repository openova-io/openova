// applications_external_url_test.go — coverage for lookupExternalURL's
// HTTPRoute match rules (#3150).
//
// lookupExternalURL resolves the operator-visible front-door URL for an
// installed Application by joining its (targetNamespace, releaseName)
// against the HTTPRoute set in the chroot k8sCache. It powers the AppDetail
// "Open" button + "External URL" row: an empty result suppresses both.
//
// The bug (#3150): bp-guacamole's HTTPRoute AND backend Service are both
// named `guacamole-server` (the chart's webapp.name), while the bootstrap-
// kit HelmRelease's releaseName is `guacamole`. The pre-fix match rule only
// compared the route name + backendRef name against releaseName → no match
// → no Open button, even though the live front door
// `https://guacamole.<fqdn>/` was serving. The fix adds a third match rule:
// the route's first hostname's leftmost DNS label equals releaseName (the
// canonical `<release>.<sovereign-fqdn>` front-door host).
package handler

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// newHTTPRoute builds an unstructured HTTPRoute with one hostname and a
// single rule carrying the given backend Service names.
func newHTTPRoute(ns, name, hostname string, backends ...string) *unstructured.Unstructured {
	refs := make([]any, 0, len(backends))
	for _, b := range backends {
		refs = append(refs, map[string]any{"name": b, "port": int64(80)})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"resourceVersion": "1",
		},
		"spec": map[string]any{
			"hostnames": []any{hostname},
			"rules": []any{
				map[string]any{"backendRefs": refs},
			},
		},
	}}
}

// httprouteRegistry registers the `httproute` kind so the k8sCache can
// inform/List it.
func httprouteRegistry(t *testing.T) *k8scache.Registry {
	t.Helper()
	r := k8scache.NewRegistry()
	if err := r.Add(k8scache.Kind{
		Name:       "httproute",
		GVR:        schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
		Namespaced: true,
	}); err != nil {
		t.Fatalf("registry.Add(httproute): %v", err)
	}
	return r
}

// newFactoryWithHTTPRoutes builds an in-memory k8sCache pre-seeded with the
// given HTTPRoutes in a single cluster `alpha`.
func newFactoryWithHTTPRoutes(t *testing.T, routes ...*unstructured.Unstructured) *k8scache.Factory {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRouteList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}, &unstructured.Unstructured{})
	gvrList := map[schema.GroupVersionResource]string{
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
	}
	objs := make([]runtime.Object, 0, len(routes))
	for _, rt := range routes {
		objs = append(objs, rt)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, objs...)
	core := kfake.NewSimpleClientset()
	cfg := k8scache.Config{
		Logger:   quietLog(),
		Registry: httprouteRegistry(t),
		Clusters: []k8scache.ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := k8scache.NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)
	// Wait briefly for the informer to sync the seeded routes.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, _ := f.List("alpha", "httproute", nil)
		if len(items) >= len(routes) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f
}

func TestLookupExternalURL_MatchRules(t *testing.T) {
	cases := []struct {
		name            string
		routes          []*unstructured.Unstructured
		targetNamespace string
		releaseName     string
		want            string
	}{
		{
			// #3150 — the regression case. guacamole's route + backend are
			// `guacamole-server`, releaseName is `guacamole`; only the
			// hostname-leftmost-label rule matches.
			name: "guacamole hostname-leftmost-label match",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("catalyst-system", "guacamole-server", "guacamole.hw124.omani.works", "guacamole-server"),
			},
			targetNamespace: "catalyst-system",
			releaseName:     "guacamole",
			want:            "https://guacamole.hw124.omani.works",
		},
		{
			// Canonical case: route name == releaseName (gitea/grafana/...).
			name: "route name match",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("gitea", "gitea", "gitea.hw124.omani.works", "gitea-http"),
			},
			targetNamespace: "gitea",
			releaseName:     "gitea",
			want:            "https://gitea.hw124.omani.works",
		},
		{
			// Backend Service name == releaseName but route name differs.
			name: "backendRef name match",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("apps", "some-route", "myapp-host.hw124.omani.works", "myapp"),
			},
			targetNamespace: "apps",
			releaseName:     "myapp",
			want:            "https://myapp-host.hw124.omani.works",
		},
		{
			// No match — controller component with a route in the ns whose
			// name/backend/subdomain all differ from releaseName.
			name: "no match returns empty",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("catalyst-system", "other-server", "other.hw124.omani.works", "other-server"),
			},
			targetNamespace: "catalyst-system",
			releaseName:     "guacamole",
			want:            "",
		},
		{
			// Namespace filter: a matching subdomain in a DIFFERENT namespace
			// must not be returned.
			name: "namespace filter excludes other-ns route",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("other-ns", "guacamole-server", "guacamole.hw124.omani.works", "guacamole-server"),
			},
			targetNamespace: "catalyst-system",
			releaseName:     "guacamole",
			want:            "",
		},
		{
			// #3931 — post-#3642 the app moved INTO the mgmt vCluster: its HR
			// still declares targetNamespace `gitea`, but the syncer mirrors the
			// HTTPRoute onto the HOST in the `mgmt` sync namespace (keeping the
			// route's plain name `gitea`). The host-side lookup must search the
			// mgmt sync namespace even though it differs from targetNamespace.
			name: "mgmt-vcluster synced route resolves via sync namespace",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("mgmt", "gitea", "gitea.hw171.omantel.biz", "gitea-http"),
			},
			targetNamespace: "gitea",
			releaseName:     "gitea",
			want:            "https://gitea.hw171.omantel.biz",
		},
		{
			// #3931 — the same widening must work via the hostname-leftmost
			// label rule (openbao's route on the host carries host bao.<fqdn>?
			// no — openbao keeps `openbao.<fqdn>`; use a name-divergent case:
			// route mirrored as `guacamole-server` in mgmt, releaseName
			// `guacamole`).
			name: "mgmt-vcluster synced route resolves via hostname label",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("mgmt", "guacamole-server", "guacamole.hw171.omantel.biz", "guacamole-server"),
			},
			targetNamespace: "guacamole",
			releaseName:     "guacamole",
			want:            "https://guacamole.hw171.omantel.biz",
		},
		{
			// #3931 — the widening is bounded to the well-known vCluster sync
			// namespaces (mgmt/dmz/rtz), NOT arbitrary ones. A same-subdomain
			// route in an UNRELATED namespace stays excluded (the #3150
			// namespace-filter protection must survive the #3642 fix).
			name: "unrelated namespace still excluded after mgmt widening",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("totally-other", "gitea", "gitea.hw171.omantel.biz", "gitea-http"),
			},
			targetNamespace: "gitea",
			releaseName:     "gitea",
			want:            "",
		},
		{
			// #5358 — gate-owned front door. bp-guacamole 0.2.30 renders NO
			// HTTPRoute of its own (sso.mode=header); the slot-13c
			// bp-oidc-gate route `oidc-gate-guacamole` in the `oidc-gate`
			// namespace owns guacamole.<fqdn>. The gate-owned rule must
			// resolve it even though the namespace differs from the app's
			// targetNamespace.
			name: "oidc-gate-owned route resolves for gated app",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("oidc-gate", "oidc-gate-guacamole", "guacamole.hw290.omani.works", "oidc-gate-guacamole"),
			},
			targetNamespace: "guacamole",
			releaseName:     "guacamole",
			want:            "https://guacamole.hw290.omani.works",
		},
		{
			// #5358 — the gate-owned rule keys on the oidc-gate-<release>
			// NAME contract, not on hostname: another instance's gate route
			// must NOT resolve for this release.
			name: "other gate instance route stays excluded",
			routes: []*unstructured.Unstructured{
				newHTTPRoute("oidc-gate", "oidc-gate-powerdns-admin", "pdns-admin.hw290.omani.works", "oidc-gate-powerdns-admin"),
			},
			targetNamespace: "guacamole",
			releaseName:     "guacamole",
			want:            "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFactoryWithHTTPRoutes(t, tc.routes...)
			h := &Handler{log: quietLog()}
			h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
			// depID "alpha" is directly registered → resolveChrootClusterID
			// returns it unchanged (no SOVEREIGN_FQDN aliasing needed).
			got := h.lookupExternalURL(context.Background(), "alpha", tc.targetNamespace, tc.releaseName)
			if got != tc.want {
				t.Fatalf("lookupExternalURL(%q, %q) = %q, want %q", tc.targetNamespace, tc.releaseName, got, tc.want)
			}
		})
	}
}

// TestRouteNamespaceMatchesApp — #3931. Pure-function coverage for the
// namespace-widening guard that fixes the #3642 mgmt-vCluster regression
// without re-opening the #3150 cross-namespace false-positive.
func TestRouteNamespaceMatchesApp(t *testing.T) {
	cases := []struct {
		name     string
		routeNS  string
		targetNS string
		want     bool
	}{
		{"exact match", "gitea", "gitea", true},
		{"empty target accepts any (CI / unset)", "anything", "", true},
		{"mgmt sync namespace accepted for in-vcluster targetNS", "mgmt", "gitea", true},
		{"dmz sync namespace accepted", "dmz", "someapp", true},
		{"rtz sync namespace accepted", "rtz", "someapp", true},
		{"arbitrary unrelated namespace rejected", "totally-other", "gitea", false},
		{"host app in its own namespace still matches", "catalyst-system", "catalyst-system", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeNamespaceMatchesApp(tc.routeNS, tc.targetNS); got != tc.want {
				t.Fatalf("routeNamespaceMatchesApp(%q, %q) = %v, want %v", tc.routeNS, tc.targetNS, got, tc.want)
			}
		})
	}
}

// TestPlacementFromHR — #3931. The HR-synthesised (bootstrap-kit) placement
// projection: front-door mgmt-vCluster apps with no Application CR must still
// project a canonical placement class so the AppDetail Topology read-back is
// honest (was empty → dishonest "no value projected").
func TestPlacementFromHR(t *testing.T) {
	mkHR := func(mutate func(obj map[string]any)) *unstructured.Unstructured {
		obj := map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]any{
				"name":      "bp-gitea",
				"namespace": "mgmt",
				"labels": map[string]any{
					"catalyst.openova.io/vcluster": "mgmt",
				},
			},
			"spec": map[string]any{},
		}
		if mutate != nil {
			mutate(obj)
		}
		return &unstructured.Unstructured{Object: obj}
	}

	cases := []struct {
		name string
		hr   *unstructured.Unstructured
		want string
	}{
		{
			// The default front-door mgmt-vCluster app (gitea/openbao/grafana):
			// no explicit placement in values → canonical single-region class.
			name: "bootstrap mgmt-vcluster app defaults to singleton",
			hr:   mkHR(nil),
			want: "singleton",
		},
		{
			name: "explicit spec.values.placement string wins",
			hr: mkHR(func(o map[string]any) {
				o["spec"].(map[string]any)["values"] = map[string]any{"placement": "active-hot-standby"}
			}),
			want: "active-hot-standby",
		},
		{
			name: "legacy spec.values.topology.mode object form",
			hr: mkHR(func(o map[string]any) {
				o["spec"].(map[string]any)["values"] = map[string]any{
					"topology": map[string]any{"mode": "active-passive"},
				}
			}),
			want: "active-passive",
		},
		{
			name: "catalyst.openova.io/topology label fallback",
			hr: mkHR(func(o map[string]any) {
				o["metadata"].(map[string]any)["labels"].(map[string]any)["catalyst.openova.io/topology"] = "active-active"
			}),
			want: "active-active",
		},
		{
			// An unknown / legacy spelling normalises to a canonical class
			// (never leaks a raw dialect to the FE).
			name: "unknown placement normalises to singleton",
			hr: mkHR(func(o map[string]any) {
				o["spec"].(map[string]any)["values"] = map[string]any{"placement": "gibberish"}
			}),
			want: "singleton",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := placementFromHR(tc.hr); got != tc.want {
				t.Fatalf("placementFromHR = %q, want %q", got, tc.want)
			}
		})
	}
}
