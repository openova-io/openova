// applications_open_button_gate_test.go — coverage for the AppDetail
// "Open" button gate (#3224).
//
// Root cause (#3224): lookupExternalURL returns a front-door URL for ANY
// HTTPRoute that matches an installed Application by name / backendRef /
// hostname-leftmost-label. The SPA renders the "Open" button whenever
// resp.ExternalURL is non-empty — so apps whose only endpoint is an
// API/protocol surface (bp-newapi backend, bp-openova-flow-server,
// keycloak's admin-only realm, an OCI registry endpoint, …) got a DEAD
// "Open" button that lands the operator on a bare login form or a 404.
//
// The fix: gate ExternalURL on the SAME blueprint-endpoint signal the
// silent-SSO launch-url endpoint uses (endpoint_handler.go) — an app only
// qualifies for an "Open" button when its Blueprint declares a user-UI
// endpoint: one with ssoEnabled:true OR launchDefault:true OR named "ui".
// API/protocol-only endpoints (ssoEnabled:false, launchDefault:false,
// non-"ui" name) must NOT qualify.
//
// This test exercises the pure predicate blueprintHasUserUIEndpoint so the
// fail->pass cycle is hermetic (no k8sCache / catalog wiring needed).
package handler

import (
	"context"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

func TestBlueprintHasUserUIEndpoint(t *testing.T) {
	cases := []struct {
		name string
		bp   *blueprintMeta
		want bool
	}{
		{
			// bp-newapi: a single backend/protocol endpoint, no SSO UI.
			// This is the regression case — must NOT show an Open button.
			name: "api-only endpoint does not qualify",
			bp: &blueprintMeta{Endpoints: []endpointDecl{
				{Name: "api", Protocol: "https", SSOEnabled: false, LaunchDefault: false},
			}},
			want: false,
		},
		{
			// bp-openova-flow-server: a backend endpoint reached by the
			// console proxy, never opened directly by the operator.
			name: "backend endpoint does not qualify",
			bp: &blueprintMeta{Endpoints: []endpointDecl{
				{Name: "backend", Protocol: "https", SSOEnabled: false, LaunchDefault: false},
			}},
			want: false,
		},
		{
			// OCI registry / protocol surface — TLS but no UI.
			name: "registry protocol endpoint does not qualify",
			bp: &blueprintMeta{Endpoints: []endpointDecl{
				{Name: "registry", Protocol: "https", TLS: boolPtr(true), SSOEnabled: false, LaunchDefault: false},
			}},
			want: false,
		},
		{
			// grafana/harbor/openbao/guacamole/pdns-admin shape: a UI
			// endpoint flagged launchDefault — MUST keep its Open button.
			name: "launchDefault UI endpoint qualifies",
			bp: &blueprintMeta{Endpoints: []endpointDecl{
				{Name: "web", Protocol: "https", LaunchDefault: true, SSOEnabled: true},
			}},
			want: true,
		},
		{
			// SSO-enabled but launchDefault omitted — still a user UI.
			name: "ssoEnabled UI endpoint qualifies",
			bp: &blueprintMeta{Endpoints: []endpointDecl{
				{Name: "console", Protocol: "https", SSOEnabled: true, LaunchDefault: false},
			}},
			want: true,
		},
		{
			// An endpoint literally named "ui" qualifies even without the
			// SSO flags — the conventional UI-endpoint name.
			name: "ui-named endpoint qualifies",
			bp: &blueprintMeta{Endpoints: []endpointDecl{
				{Name: "ui", Protocol: "https", SSOEnabled: false, LaunchDefault: false},
			}},
			want: true,
		},
		{
			// Mixed: keycloak-shaped — an admin/protocol endpoint plus a
			// genuine UI endpoint. The UI endpoint wins → qualifies.
			name: "mixed api + ui endpoints qualifies",
			bp: &blueprintMeta{Endpoints: []endpointDecl{
				{Name: "admin", Protocol: "https", SSOEnabled: false, LaunchDefault: false},
				{Name: "account", Protocol: "https", SSOEnabled: true, LaunchDefault: false},
			}},
			want: true,
		},
		{
			// No endpoints declared at all — cannot prove a UI surface, so
			// the predicate reports false (the caller treats a resolved-but-
			// empty endpoint list as "no UI").
			name: "no endpoints does not qualify",
			bp:   &blueprintMeta{Endpoints: []endpointDecl{}},
			want: false,
		},
		{
			// nil metadata — the blueprint could not be resolved; predicate
			// reports false and the caller decides the fail-open policy.
			name: "nil blueprint does not qualify",
			bp:   nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := blueprintHasUserUIEndpoint(tc.bp)
			if got != tc.want {
				t.Fatalf("blueprintHasUserUIEndpoint(%+v) = %v, want %v", tc.bp, got, tc.want)
			}
		})
	}
}

// catalogBPWithEndpoints builds a CatalogBlueprint whose
// spec.endpoints[] carry the given endpoint declarations, so the gate
// helper (externalURLIfUserUI → resolveBlueprintMeta) sees the same
// blueprintMeta shape the live catalog returns.
func catalogBPWithEndpoints(name string, eps []map[string]interface{}) *CatalogBlueprint {
	rawEps := make([]interface{}, 0, len(eps))
	for _, e := range eps {
		rawEps = append(rawEps, e)
	}
	return &CatalogBlueprint{
		Name:    name,
		Version: "1.0.0",
		Raw: map[string]interface{}{
			"spec": map[string]interface{}{
				"endpoints": rawEps,
			},
		},
	}
}

// TestExternalURLIfUserUI_GatesOnBlueprint exercises the full Open-button
// gate: a live HTTPRoute resolves a front-door URL, but the Blueprint's
// endpoint shape decides whether the URL survives (UI) or is suppressed
// (API/protocol only). This is the #3224 acceptance: given an api/protocol-
// only Blueprint the ExternalURL must be EMPTY; given a launchDefault/
// ssoEnabled UI Blueprint it must be POPULATED.
func TestExternalURLIfUserUI_GatesOnBlueprint(t *testing.T) {
	// A live route exists for both apps (front door is serving) — the gate
	// must decide purely on the Blueprint UI signal.
	route := newHTTPRoute("catalyst-system", "newapi", "newapi.hw124.omani.works", "newapi")
	uiRoute := newHTTPRoute("catalyst-system", "grafana", "grafana.hw124.omani.works", "grafana")

	// The gate strips the `bp-` prefix before resolving (mirroring
	// extractBlueprintFromApp), so the catalog resolves the stripped name —
	// fixtures are keyed on `newapi` / `grafana`.
	apiOnly := catalogBPWithEndpoints("newapi", []map[string]interface{}{
		{"name": "api", "protocol": "https", "ssoEnabled": false, "launchDefault": false},
	})
	uiBP := catalogBPWithEndpoints("grafana", []map[string]interface{}{
		{"name": "web", "protocol": "https", "ssoEnabled": true, "launchDefault": true},
	})

	t.Run("api-only blueprint suppresses URL", func(t *testing.T) {
		f := newFactoryWithHTTPRoutes(t, route)
		h := &Handler{log: quietLog()}
		h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
		h.SetCatalogClient(newFakeCatalog(apiOnly))
		got := h.externalURLIfUserUI(context.Background(), "alpha", "bp-newapi", "catalyst-system", "newapi")
		if got != "" {
			t.Fatalf("api-only blueprint: got %q, want empty (no Open button)", got)
		}
	})

	t.Run("ui blueprint preserves URL", func(t *testing.T) {
		f := newFactoryWithHTTPRoutes(t, uiRoute)
		h := &Handler{log: quietLog()}
		h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
		h.SetCatalogClient(newFakeCatalog(uiBP))
		got := h.externalURLIfUserUI(context.Background(), "alpha", "bp-grafana", "catalyst-system", "grafana")
		want := "https://grafana.hw124.omani.works"
		if got != want {
			t.Fatalf("ui blueprint: got %q, want %q (Open button must render)", got, want)
		}
	})

	t.Run("catalog unwired fails open", func(t *testing.T) {
		// No SetCatalogClient → resolveBlueprintMeta soft-fails with an
		// empty (non-nil) meta. The gate must NOT suppress a real UI when
		// blueprint metadata is merely unavailable (chroot / CI).
		f := newFactoryWithHTTPRoutes(t, uiRoute)
		h := &Handler{log: quietLog()}
		h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
		got := h.externalURLIfUserUI(context.Background(), "alpha", "bp-grafana", "catalyst-system", "grafana")
		want := "https://grafana.hw124.omani.works"
		if got != want {
			t.Fatalf("catalog-unwired: got %q, want %q (fail open)", got, want)
		}
	})
}
