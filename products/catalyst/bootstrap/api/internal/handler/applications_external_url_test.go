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
