// applications_newapi_open_launch_5389_test.go — the per-app "Open"
// (launch) control for bp-newapi, driven by the REAL on-disk
// platform/newapi/blueprint.yaml rather than a synthetic fixture.
//
// # What was broken (#5389, UAT row 114, measured live on hw290 2026-07-26)
//
// platform/newapi/blueprint.yaml declared `endpoints: []`. Two independent
// console surfaces read that list, and BOTH went dark:
//
//  1. Handler.userUIGatePasses fails closed on len(Endpoints) == 0, so
//     externalURLIfUserUI suppressed the Application's externalURL. AppDetail
//     renders the hero CTA only under `{appExternalURL ? … : null}` and
//     HandleSovereignApps projects the AppsPage grid button from the same
//     gate — so NEITHER Open button existed for bp-newapi. The live walk
//     recorded a locator timeout on [data-testid=btn-launch-app].
//  2. HandleGetLaunchURL resolves its target via pickEndpoint, which returns
//     nil against an empty list — GET /catalyst/v1/apps/bp-newapi/launch-url
//     answered 404 {"code":"endpoint-not-found","message":"Blueprint newapi
//     has no endpoint \"\""} (reproduced against the live hw290 catalyst-api
//     before the fix).
//
// So there was no button to click, and no URL behind it either.
//
// # Why these tests read the file instead of a fixture
//
// The neighbouring #3224 tests in applications_open_button_gate_test.go all
// build synthetic blueprintMeta values, so they would have passed unchanged
// with bp-newapi's real declaration empty — a fixture can only prove the
// predicate, never the declaration the Sovereign actually ships. These tests
// therefore parse platform/newapi/blueprint.yaml itself: revert that file to
// `endpoints: []` and they fail, naming the exact live symptom. The
// `pre-#5389 shape` sub-tests below assert the failure direction explicitly,
// so a green run means the fix is load-bearing, not that the assertions are
// vacuous.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// repoRootFor5389 walks up from this test file to the monorepo root (the dir
// containing platform/newapi/blueprint.yaml).
func repoRootFor5389(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "platform", "newapi", "blueprint.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("monorepo root (platform/newapi/blueprint.yaml) not found from test cwd — skipping on-disk blueprint test")
	return ""
}

// newapiBlueprintEndpoints parses spec.endpoints[] out of the shipped
// platform/newapi/blueprint.yaml and returns it in the shape the catalog
// client hands to resolveBlueprintMeta.
func newapiBlueprintEndpoints(t *testing.T) []map[string]interface{} {
	t.Helper()
	root := repoRootFor5389(t)
	raw, err := os.ReadFile(filepath.Join(root, "platform", "newapi", "blueprint.yaml"))
	if err != nil {
		t.Fatalf("read platform/newapi/blueprint.yaml: %v", err)
	}
	var doc struct {
		Spec struct {
			Endpoints []map[string]interface{} `yaml:"endpoints"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse platform/newapi/blueprint.yaml: %v", err)
	}
	return doc.Spec.Endpoints
}

// TestNewAPIBlueprint_DeclaresLaunchableUIEndpoint — the declaration itself.
// bp-newapi's chart publishes the standalone host newapi.<fqdn>
// (chart/templates/httproute.yaml) and routes an Exact `/` match to the
// sandbox-bridge sidecar's zero-click SSO landing page, so the Blueprint MUST
// advertise that front door or the console cannot offer it.
func TestNewAPIBlueprint_DeclaresLaunchableUIEndpoint(t *testing.T) {
	eps := newapiBlueprintEndpoints(t)
	if len(eps) == 0 {
		t.Fatal("platform/newapi/blueprint.yaml declares NO endpoints — this is the #5389 regression: " +
			"userUIGatePasses fails closed on an empty list, so the AppDetail hero Open CTA and the " +
			"AppsPage grid button are both suppressed, and GET /catalyst/v1/apps/bp-newapi/launch-url " +
			"404s endpoint-not-found. NewAPI serves an SSO-gated admin console on newapi.<fqdn>; declare it.")
	}

	meta := blueprintMetaFromEndpoints(t, eps)
	if !blueprintHasUserUIEndpoint(meta) {
		t.Fatalf("bp-newapi endpoints %+v carry no user-UI endpoint (need ssoEnabled || launchDefault || name==\"ui\") — "+
			"the Open button gate would suppress the control", meta.Endpoints)
	}

	ep := pickEndpoint(meta, "")
	if ep == nil {
		t.Fatal("pickEndpoint(bp-newapi, \"\") = nil — the launch-url handler would answer 404 endpoint-not-found")
	}
	if !ep.SSOEnabled {
		t.Fatalf("bp-newapi launch endpoint %q must be ssoEnabled: NewAPI's admin console is Keycloak-gated "+
			"(chart/templates/sso-app-registration.yaml registers the KC redirect URI)", ep.Name)
	}
	if got, want := strings.TrimSpace(ep.HostnameTemplate), "newapi.{{.SovereignFQDN}}"; got != want {
		t.Fatalf("bp-newapi hostnameTemplate = %q, want %q (chart/templates/httproute.yaml publishes "+
			"`newapi.<sovereignFQDN>`; any other host has no listener)", got, want)
	}
	// The bare root IS NewAPI's OIDC-init route: the chart's Exact-`/` match
	// hands it to the bridge sidecar, whose sso_init.go 404s every other path.
	if got := strings.TrimSpace(ep.SSOInitPath); got != "/" {
		t.Fatalf("bp-newapi ssoInitPath = %q, want \"/\" — NewAPI has no app-local OIDC-init route; the "+
			"platform supplies one as the Exact-`/` bridge landing page (internal/handler/sso_init.go)", got)
	}
}

// TestGetLaunchURL_NewAPI_FromShippedBlueprint — the control's target.
// Both directions: the shipped declaration must mint the newapi front door,
// and the pre-#5389 `endpoints: []` shape must reproduce the live 404.
func TestGetLaunchURL_NewAPI_FromShippedBlueprint(t *testing.T) {
	t.Run("shipped blueprint mints the front door", func(t *testing.T) {
		h, _, _ := newTestHandlerWithEndpoint(t)
		h.SetCatalogClient(fakeBlueprintInCatalog("newapi", newapiBlueprintEndpoints(t), false, []string{"singleton"}))

		rec := httptest.NewRecorder()
		// The console addresses bootstrap-kit apps by blueprint name (#3150 —
		// launchKey = componentId when no Application CR uid exists).
		newTestRouter(h).ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/bp-newapi/launch-url", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("launch-url for bp-newapi = %d (body=%s), want 200", rec.Code, rec.Body.String())
		}
		var resp launchURLResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode launch-url response: %v", err)
		}
		// SovereignFQDN is t01.omani.works in newTestHandlerWithEndpoint.
		if want := "https://newapi.t01.omani.works/"; resp.URL != want {
			t.Fatalf("launch-url = %q, want %q — the Open button opens this exact URL", resp.URL, want)
		}
		if resp.Endpoint != "ui" {
			t.Fatalf("launch-url endpoint = %q, want \"ui\"", resp.Endpoint)
		}
	})

	t.Run("pre-#5389 empty endpoints reproduce the live 404", func(t *testing.T) {
		h, _, _ := newTestHandlerWithEndpoint(t)
		h.SetCatalogClient(fakeBlueprintInCatalog("newapi", []map[string]interface{}{}, false, []string{"singleton"}))

		rec := httptest.NewRecorder()
		newTestRouter(h).ServeHTTP(rec, httptest.NewRequest("GET", "/catalyst/v1/apps/bp-newapi/launch-url", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("empty endpoints[]: launch-url = %d, want 404 — this sub-test guards that the "+
				"assertion above is load-bearing (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "endpoint-not-found") {
			t.Fatalf("empty endpoints[]: body = %s, want the live hw290 code endpoint-not-found", rec.Body.String())
		}
	})
}

// TestOpenButtonGate_NewAPI_FromShippedBlueprint — the control's existence.
// externalURLIfUserUI is what AppDetail's `{appExternalURL ? … : null}` and
// the AppsPage grid both render from; with the shipped declaration it must
// survive, and with the pre-#5389 empty list it must be suppressed (the
// live "no btn-launch-app" symptom).
func TestOpenButtonGate_NewAPI_FromShippedBlueprint(t *testing.T) {
	route := newHTTPRoute("newapi", "newapi-bp-newapi-public", "newapi.hw290.omani.works", "newapi")

	t.Run("shipped blueprint renders the Open button", func(t *testing.T) {
		f := newFactoryWithHTTPRoutes(t, route)
		h := &Handler{log: quietLog()}
		h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
		h.SetCatalogClient(fakeBlueprintInCatalog("newapi", newapiBlueprintEndpoints(t), false, []string{"singleton"}))

		if !h.userUIGatePasses(context.Background(), "bp-newapi") {
			t.Fatal("bp-newapi must pass the user-UI gate — otherwise AppDetail renders no hero CTA and " +
				"AppsPage renders no grid button (UAT row 114: btn-launch-app locator timeout)")
		}
		got := h.externalURLIfUserUI(context.Background(), "alpha", "bp-newapi", "newapi", "newapi")
		if want := "https://newapi.hw290.omani.works"; got != want {
			t.Fatalf("externalURL = %q, want %q — this is the value AppDetail gates the Open button on", got, want)
		}
	})

	t.Run("pre-#5389 empty endpoints suppress the Open button", func(t *testing.T) {
		f := newFactoryWithHTTPRoutes(t, route)
		h := &Handler{log: quietLog()}
		h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
		h.SetCatalogClient(fakeBlueprintInCatalog("newapi", []map[string]interface{}{}, false, []string{"singleton"}))

		if h.userUIGatePasses(context.Background(), "bp-newapi") {
			t.Fatal("empty endpoints[] must fail the gate — guards that the assertion above is load-bearing")
		}
		if got := h.externalURLIfUserUI(context.Background(), "alpha", "bp-newapi", "newapi", "newapi"); got != "" {
			t.Fatalf("empty endpoints[]: externalURL = %q, want empty (the live no-Open-button symptom)", got)
		}
	})
}

// blueprintMetaFromEndpoints decodes raw endpoint maps into blueprintMeta via
// the SAME json round-trip resolveBlueprintMeta uses, so the test sees exactly
// what the handler sees (field tags, defaulting, unknown-key tolerance).
func blueprintMetaFromEndpoints(t *testing.T, eps []map[string]interface{}) *blueprintMeta {
	t.Helper()
	jsonBytes, err := json.Marshal(map[string]interface{}{"endpoints": eps})
	if err != nil {
		t.Fatalf("marshal endpoints: %v", err)
	}
	out := &blueprintMeta{}
	if err := json.Unmarshal(jsonBytes, out); err != nil {
		t.Fatalf("unmarshal into blueprintMeta: %v", err)
	}
	return out
}
