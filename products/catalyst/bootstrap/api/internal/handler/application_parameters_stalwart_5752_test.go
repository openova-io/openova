// Tests for #5752 — bp-stalwart-tenant's per-Org install door emitted
// spec.parameters: {} (no domain, no keycloak.realmURL), so the chart's
// certificate.yaml Certificate never rendered, cert-manager never
// materialised the stalwart-tls Secret, and the StatefulSet's non-optional
// tls Secret mount left the Pod stuck ContainerCreating forever. Live
// evidence: hw292 funnel Org `uatco`, 2026-08-06 (kubelet Event
// "MountVolume.SetUp failed for volume \"tls\": secret \"stalwart-tls\" not
// found", x1737 over 2d10h; Application.spec.parameters == {}).
//
// THE GUARD: TestNewApplicationCRFromSeed_StalwartTenantNoValues_ParametersValidateConfigSchema
// is the money test — it round-trips the SAME producer the live install
// door uses (newApplicationCRFromSeed) through validate.Parameters against
// the REAL bp-stalwart-tenant configSchema (required: [keycloak],
// keycloak.required: [realmURL] — mirrored from
// platform/stalwart-tenant/blueprint.yaml). Before the #5752 fix,
// defaultedParameters had no bp-stalwart-tenant case, so the produced
// parameters stayed {} and this assertion FAILED
// ("keycloak: keycloak is required" / similar). After the fix it PASSES.
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/validate"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// bpStalwartTenantConfigSchema mirrors platform/stalwart-tenant/blueprint.yaml
// spec.configSchema — specifically the two `required` clauses that make an
// empty parameters map invalid: the top-level `required: [keycloak]` and
// the nested `keycloak.required: [realmURL]`.
func bpStalwartTenantConfigSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"keycloak"},
		"properties": map[string]interface{}{
			"domain": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"primary": map[string]interface{}{"type": "string"},
					"mode":    map[string]interface{}{"type": "string", "enum": []interface{}{"free-subdomain", "byo"}, "default": "free-subdomain"},
				},
			},
			"keycloak": map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"realmURL"},
				"properties": map[string]interface{}{
					"realmURL":         map[string]interface{}{"type": "string"},
					"clientID":         map[string]interface{}{"type": "string", "default": "stalwart"},
					"clientSecretName": map[string]interface{}{"type": "string", "default": "stalwart-oidc"},
				},
			},
		},
	}
}

// ── defaultedParameters unit table ──────────────────────────────────────

func TestDefaultedParameters_StalwartTenantNoValues_StampsDomainIngressKeycloak(t *testing.T) {
	got := defaultedParameters("bp-stalwart-tenant", "singleton", "hw292.omani.works", "uatco", "console.uatco.omani.homes", nil)

	domain, ok := got["domain"].(map[string]interface{})
	if !ok || domain["primary"] != "uatco.omani.homes" {
		t.Fatalf("domain.primary = %#v, want uatco.omani.homes", got["domain"])
	}
	ingress, ok := got["ingress"].(map[string]interface{})
	if !ok {
		t.Fatalf("ingress block missing: %#v", got)
	}
	webmail, ok := ingress["webmail"].(map[string]interface{})
	if !ok || webmail["host"] != "mail.uatco.omani.homes" {
		t.Fatalf("ingress.webmail.host = %#v, want mail.uatco.omani.homes", ingress["webmail"])
	}
	keycloak, ok := got["keycloak"].(map[string]interface{})
	if !ok || keycloak["realmURL"] != "https://auth.hw292.omani.works/realms/sovereign" {
		t.Fatalf("keycloak.realmURL = %#v, want https://auth.hw292.omani.works/realms/sovereign", got["keycloak"])
	}
}

func TestDefaultedParameters_StalwartTenantExplicitValuesWin(t *testing.T) {
	explicit := map[string]interface{}{
		"domain":   map[string]interface{}{"primary": "pinned.example"},
		"keycloak": map[string]interface{}{"realmURL": "https://auth.pinned.example/realms/org-pinned"},
	}
	got := defaultedParameters("bp-stalwart-tenant", "singleton", "hw292.omani.works", "uatco", "console.uatco.omani.homes", explicit)

	// Checked assertions, matching this file's first test. An unchecked
	// `.(map[string]interface{})` here panics the whole test BINARY the moment
	// the stamper regresses to a no-op, and Go abandons every test that had not
	// yet run — the six siblings below reported nothing at all. A regression
	// must fail this test, not silence the suite.
	domain, ok := got["domain"].(map[string]interface{})
	if !ok {
		t.Fatalf("domain must be a map, got %#v — the stamper dropped it entirely", got["domain"])
	}
	if domain["primary"] != "pinned.example" {
		t.Fatalf("a caller-pinned domain.primary must NOT be overwritten, got %#v", domain["primary"])
	}
	keycloak, ok := got["keycloak"].(map[string]interface{})
	if !ok {
		t.Fatalf("keycloak must be a map, got %#v — the stamper dropped it entirely", got["keycloak"])
	}
	if keycloak["realmURL"] != "https://auth.pinned.example/realms/org-pinned" {
		t.Fatalf("a caller-pinned keycloak.realmURL must NOT be overwritten, got %#v", keycloak["realmURL"])
	}
	// ingress.webmail.host was NOT explicitly pinned, so the stamp still
	// lands alongside the preserved explicit values.
	ingress, ok := got["ingress"].(map[string]interface{})
	if !ok {
		t.Fatalf("ingress must be stamped even when domain/keycloak are pinned, got %#v", got["ingress"])
	}
	webmail, ok := ingress["webmail"].(map[string]interface{})
	if !ok {
		t.Fatalf("ingress.webmail must be a map, got %#v", ingress["webmail"])
	}
	if webmail["host"] != "mail.uatco.omani.homes" {
		t.Fatalf("ingress.webmail.host must still be stamped when not explicitly pinned, got %#v", webmail["host"])
	}
}

func TestDefaultedParameters_StalwartTenantEmptyOrgConsoleHost_NoDomainStamp(t *testing.T) {
	// Mothership / Catalyst-Zero / registry-miss: empty orgConsoleHost ⇒ no
	// domain/ingress stamp (fail-closed, chart's existing behaviour holds).
	// keycloak.realmURL still stamps since sovereignFQDN is independently
	// non-empty.
	got := defaultedParameters("bp-stalwart-tenant", "singleton", "hw292.omani.works", "uatco", "", nil)
	if _, ok := got["domain"]; ok {
		t.Fatalf("empty orgConsoleHost must NOT stamp domain, got %#v", got["domain"])
	}
	if _, ok := got["ingress"]; ok {
		t.Fatalf("empty orgConsoleHost must NOT stamp ingress, got %#v", got["ingress"])
	}
	keycloak, ok := got["keycloak"].(map[string]interface{})
	if !ok || keycloak["realmURL"] != "https://auth.hw292.omani.works/realms/sovereign" {
		t.Fatalf("keycloak.realmURL must still stamp off sovereignFQDN alone, got %#v", got["keycloak"])
	}
}

func TestDefaultedParameters_StalwartTenantEmptyFQDN_NoKeycloakStamp(t *testing.T) {
	// Mothership / Catalyst-Zero: empty sovereignFQDN ⇒ no keycloak stamp.
	got := defaultedParameters("bp-stalwart-tenant", "singleton", "", "uatco", "console.uatco.omani.homes", nil)
	if _, ok := got["keycloak"]; ok {
		t.Fatalf("empty sovereignFQDN must NOT stamp keycloak.realmURL, got %#v", got["keycloak"])
	}
	// domain/ingress still stamp since orgConsoleHost is independently non-empty.
	domain, ok := got["domain"].(map[string]interface{})
	if !ok || domain["primary"] != "uatco.omani.homes" {
		t.Fatalf("domain.primary must still stamp off orgConsoleHost alone, got %#v", got["domain"])
	}
}

func TestDefaultedParameters_NonStalwartTenant_NoStamp(t *testing.T) {
	got := defaultedParameters("bp-agenity", "singleton", "hw292.omani.works", "uatco", "console.uatco.omani.homes", nil)
	if _, ok := got["keycloak"]; ok {
		t.Fatalf("non-stalwart-tenant Blueprint must NOT get keycloak.realmURL stamp, got %#v", got["keycloak"])
	}
	if domain, ok := got["domain"]; ok {
		t.Fatalf("non-stalwart-tenant Blueprint must NOT get domain stamp, got %#v", domain)
	}
}

// ── The money test: proves the LIVE install door's output now satisfies the
//    Blueprint's own configSchema, using the SAME producer + SAME validator
//    the application-controller runs. This is the #5752 red→green guard. ──

func TestNewApplicationCRFromSeed_StalwartTenantNoValues_ParametersValidateConfigSchema(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:           "uatco-mail",
		Namespace:      "uatco",
		Blueprint:      "bp-stalwart-tenant",
		Topology:       "singleton",
		SovereignFQDN:  "hw292.omani.works",
		OrgConsoleHost: "console.uatco.omani.homes",
		// Values intentionally empty — this is the exact live shape:
		// a funnel install through POST /catalyst/v1/apps/instances
		// supplies no explicit values.
	}
	obj := newApplicationCRFromSeed(seed)

	params, found, err := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if err != nil {
		t.Fatalf("read spec.parameters: %v", err)
	}
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil — must be a non-null object")
	}

	rep, vErr := validate.Parameters(bpStalwartTenantConfigSchema(), params)
	if vErr != nil {
		t.Fatalf("validate.Parameters internal error: %v", vErr)
	}
	if !rep.Valid {
		t.Fatalf("produced spec.parameters does NOT satisfy bp-stalwart-tenant configSchema (this is the live #5752 bug — an empty parameters map fails 'required: keycloak'): %v\nparams=%#v", rep.Errors, params)
	}

	domain, _ := params["domain"].(map[string]interface{})
	if domain == nil || domain["primary"] != "uatco.omani.homes" {
		t.Fatalf("spec.parameters.domain.primary = %#v, want uatco.omani.homes (required for templates/certificate.yaml to render the stalwart-tls Certificate)", params["domain"])
	}
}

func TestNewApplicationUnstructured_StalwartTenant_StampsDomainAndKeycloak(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef: applicationBlueprintRef{Name: "bp-stalwart-tenant", Version: "0.1.15"},
	}
	obj := newApplicationUnstructured(req, "hw292.omani.works", "console.uatco.omani.homes")
	params, found, err := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if err != nil {
		t.Fatalf("read spec.parameters: %v", err)
	}
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil")
	}
	rep, vErr := validate.Parameters(bpStalwartTenantConfigSchema(), params)
	if vErr != nil {
		t.Fatalf("validate.Parameters internal error: %v", vErr)
	}
	if !rep.Valid {
		t.Fatalf("installApplicationCore door output does NOT satisfy configSchema: %v\nparams=%#v", rep.Errors, params)
	}
}
