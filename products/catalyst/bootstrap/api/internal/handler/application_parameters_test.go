// Tests for #4283 / #4282 Root-B (validation half): the two
// Application-CR producers MUST emit a non-null spec.parameters OBJECT so
// console/funnel-created postgres Applications (shared-pg-d/-e) no longer
// fail the application-controller's configSchema validation with
// "#: expected object, but got null".
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/validate"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// bpPostgresConfigSchema mirrors platform/postgres/blueprint.yaml
// spec.configSchema (v0.2.x). The application-controller validates
// Application.spec.parameters against this exact schema; we round-trip the
// produced parameters through validate.Parameters (the SAME validator the
// controller uses) to prove configSchema-validity.
func bpPostgresConfigSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"enabled": map[string]interface{}{"type": "boolean", "default": true},
			"instance": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":        map[string]interface{}{"type": "string", "default": "postgres"},
					"storageSize": map[string]interface{}{"type": "string", "default": "5Gi"},
					"pgVersion":   map[string]interface{}{"type": "string", "default": "16"},
				},
			},
			"topology": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"mode": map[string]interface{}{
						"type":    "string",
						"enum":    []interface{}{"singleton", "active-hot-standby"},
						"default": "singleton",
					},
					"instances": map[string]interface{}{
						"type": "integer", "minimum": 1, "maximum": 5, "default": 1,
					},
				},
			},
			"databases": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type":     "object",
					"required": []interface{}{"name", "owner"},
					"properties": map[string]interface{}{
						"name":  map[string]interface{}{"type": "string"},
						"owner": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}
}

// ── defaultedParameters unit table ──────────────────────────────────────

func TestDefaultedParameters_PostgresNoValues_SeedsSingletonMode(t *testing.T) {
	got := defaultedParameters("bp-postgres", "singleton", "", "", "", nil)
	if got == nil {
		t.Fatal("defaultedParameters returned nil — must always be a non-null object")
	}
	topo, ok := got["topology"].(map[string]interface{})
	if !ok {
		t.Fatalf("postgres parameters missing topology object: %#v", got)
	}
	if topo["mode"] != "singleton" {
		t.Fatalf("topology.mode = %v, want singleton", topo["mode"])
	}
}

func TestDefaultedParameters_PostgresActiveHotStandby(t *testing.T) {
	for _, topo := range []string{"active-hot-standby", "active-hotstandby", "active-active", "active-passive"} {
		got := defaultedParameters("postgres", topo, "", "", "", nil)
		tm := got["topology"].(map[string]interface{})["mode"]
		if tm != "active-hot-standby" {
			t.Fatalf("topology %q → topology.mode = %v, want active-hot-standby", topo, tm)
		}
	}
}

func TestDefaultedParameters_NonPostgresNoValues_EmptyObjectNotNil(t *testing.T) {
	got := defaultedParameters("bp-grafana", "singleton", "", "", "", nil)
	if got == nil {
		t.Fatal("non-postgres parameters returned nil — must be at least an empty object")
	}
	if len(got) != 0 {
		t.Fatalf("non-postgres parameters should be empty {}, got %#v", got)
	}
}

func TestDefaultedParameters_ExplicitValuesPreservedVerbatim(t *testing.T) {
	explicit := map[string]interface{}{
		"topology":  map[string]interface{}{"mode": "active-hot-standby"},
		"databases": []interface{}{map[string]interface{}{"name": "wp", "owner": "wp"}},
	}
	got := defaultedParameters("bp-postgres", "singleton", "", "", "", explicit)
	if got["topology"].(map[string]interface{})["mode"] != "active-hot-standby" {
		t.Fatalf("explicit topology.mode must be preserved (not overwritten by chosen topology), got %#v", got)
	}
	if _, ok := got["databases"]; !ok {
		t.Fatalf("explicit databases must be preserved, got %#v", got)
	}
}

// ── #4556 Item 2: bp-agenity stamps sovereignFqdn so the openova-MCP URL
//    derives https://console.<sovereign-fqdn>, NOT the mothership
//    console.openova.io. ───────────────────────────────────────────────────

func TestDefaultedParameters_AgenityNoValues_StampsSovereignFqdn(t *testing.T) {
	got := defaultedParameters("bp-agenity", "singleton", "omantel.biz", "nstar2", "", nil)
	if got["sovereignFqdn"] != "omantel.biz" {
		t.Fatalf("agenity parameters must stamp sovereignFqdn=omantel.biz, got %#v", got)
	}
}

func TestDefaultedParameters_AgenityWithExplicitValues_StampsSovereignFqdn(t *testing.T) {
	explicit := map[string]interface{}{
		"agent": map[string]interface{}{"model": "claude-opus-4-8"},
	}
	got := defaultedParameters("agenity", "singleton", "t99.omani.works", "t99org.omani.homes", "", explicit)
	if got["sovereignFqdn"] != "t99.omani.works" {
		t.Fatalf("agenity sovereignFqdn must be stamped even with explicit params, got %#v", got)
	}
	if _, ok := got["agent"]; !ok {
		t.Fatalf("explicit agent params must be preserved alongside the stamped sovereignFqdn, got %#v", got)
	}
}

func TestDefaultedParameters_AgenityExplicitSovereignFqdnWins(t *testing.T) {
	explicit := map[string]interface{}{"sovereignFqdn": "pinned.example"}
	got := defaultedParameters("bp-agenity", "singleton", "omantel.biz", "nstar2", "", explicit)
	if got["sovereignFqdn"] != "pinned.example" {
		t.Fatalf("a caller-pinned sovereignFqdn must NOT be overwritten, got %#v", got)
	}
}

func TestDefaultedParameters_AgenityEmptyFQDN_NoStamp(t *testing.T) {
	// Mothership / Catalyst-Zero: empty FQDN → leave sovereignFqdn unset so
	// the chart's fail-closed default applies (no bogus console host).
	got := defaultedParameters("bp-agenity", "singleton", "", "", "", nil)
	if _, ok := got["sovereignFqdn"]; ok {
		t.Fatalf("empty FQDN must NOT stamp sovereignFqdn, got %#v", got)
	}
}

// ── #4610: bp-agenity stamps the openova-MCP bearer ExternalSecret wiring so
//    the install-door agenity gets OPENOVA_MCP_BEARER from the per-Org OpenBao
//    path (LOCAL-signer-signed bearer the Sovereign's catalyst-api accepts),
//    NOT the chart's `enabled:false` default that delivers nothing → 401. ───

// assertAgenityMCPBearerWiring fails unless params carries the full openovaMCP
// mcpBearer wiring the chart needs to project OPENOVA_MCP_BEARER +
// OPENOVA_MCP_RS256_PUBKEY_PEM from secret/catalyst/agenity/<slug>/mcp-bearer.
func assertAgenityMCPBearerWiring(t *testing.T, params map[string]interface{}, wantSlug string) {
	t.Helper()
	mcp, ok := params["openovaMCP"].(map[string]interface{})
	if !ok {
		t.Fatalf("agenity parameters missing openovaMCP block: %#v", params)
	}
	bs, ok := mcp["bearerSecret"].(map[string]interface{})
	if !ok || bs["name"] != agenityMCPBearerSecretName || bs["key"] != "bearer" {
		t.Fatalf("openovaMCP.bearerSecret must point at %q/bearer, got %#v", agenityMCPBearerSecretName, mcp["bearerSecret"])
	}
	pk, ok := mcp["rs256PubkeySecret"].(map[string]interface{})
	if !ok || pk["name"] != agenityMCPBearerSecretName || pk["key"] != "pubkeyPem" {
		t.Fatalf("openovaMCP.rs256PubkeySecret must point at %q/pubkeyPem (the SEEDED local-signer pubkey, NOT the host catalyst-handover-jwt mothership key), got %#v", agenityMCPBearerSecretName, mcp["rs256PubkeySecret"])
	}
	mb, ok := mcp["mcpBearer"].(map[string]interface{})
	if !ok {
		t.Fatalf("openovaMCP.mcpBearer missing: %#v", mcp)
	}
	es, ok := mb["externalSecret"].(map[string]interface{})
	if !ok {
		t.Fatalf("openovaMCP.mcpBearer.externalSecret missing: %#v", mb)
	}
	if es["enabled"] != true {
		t.Fatalf("openovaMCP.mcpBearer.externalSecret.enabled must be true (the chart default is false → bearer undelivered → 401), got %#v", es["enabled"])
	}
	wantKey := "catalyst/agenity/" + wantSlug + "/mcp-bearer"
	if es["remoteKey"] != wantKey {
		t.Fatalf("openovaMCP.mcpBearer.externalSecret.remoteKey = %v, want %q (must match the producer's mcpBearerSecretPath)", es["remoteKey"], wantKey)
	}
	if es["remoteBearerProperty"] != "bearer" || es["remotePubkeyProperty"] != "pubkeyPem" {
		t.Fatalf("openovaMCP.mcpBearer.externalSecret property names must be bearer/pubkeyPem, got %#v", es)
	}
}

func TestDefaultedParameters_AgenityNoValues_StampsMCPBearerWiring(t *testing.T) {
	// FQDN-form org ref must collapse to the leading DNS label for the path.
	got := defaultedParameters("bp-agenity", "singleton", "omantel.biz", "nstar2.omani.homes", "", nil)
	assertAgenityMCPBearerWiring(t, got, "nstar2")
}

func TestDefaultedParameters_AgenityEmptyOrgSlug_NoMCPBearerStamp(t *testing.T) {
	// No Org slug ⇒ we cannot scope the per-Org OpenBao path; skip the stamp
	// (a path with an empty/cross-Org slug would defeat seedMCPBearer's
	// own-Org guarantee). sovereignFqdn still stamps independently.
	got := defaultedParameters("bp-agenity", "singleton", "omantel.biz", "", "", nil)
	if _, ok := got["openovaMCP"]; ok {
		t.Fatalf("empty org slug must NOT stamp openovaMCP wiring, got %#v", got)
	}
	if got["sovereignFqdn"] != "omantel.biz" {
		t.Fatalf("sovereignFqdn must still stamp independently of the mcpBearer slug, got %#v", got)
	}
}

func TestDefaultedParameters_NonAgenity_NoMCPBearerStamp(t *testing.T) {
	got := defaultedParameters("bp-postgres", "singleton", "omantel.biz", "nstar2", "", nil)
	if _, ok := got["openovaMCP"]; ok {
		t.Fatalf("non-agenity Blueprint must NOT get openovaMCP wiring, got %#v", got)
	}
}

func TestDefaultedParameters_AgenityExplicitMCPBearerWins(t *testing.T) {
	explicit := map[string]interface{}{
		"openovaMCP": map[string]interface{}{
			"bearerSecret": map[string]interface{}{"name": "pinned-secret", "key": "bearer"},
		},
	}
	got := defaultedParameters("bp-agenity", "singleton", "omantel.biz", "nstar2", "", explicit)
	mcp := got["openovaMCP"].(map[string]interface{})
	bs := mcp["bearerSecret"].(map[string]interface{})
	if bs["name"] != "pinned-secret" {
		t.Fatalf("a caller-pinned openovaMCP.bearerSecret must NOT be overwritten, got %#v", bs)
	}
	// The additive merge still wires the missing keys (rs256PubkeySecret, mcpBearer).
	if _, ok := mcp["mcpBearer"]; !ok {
		t.Fatalf("additive merge must still wire the absent mcpBearer key, got %#v", mcp)
	}
}

func TestNewApplicationUnstructured_Agenity_StampsSovereignFqdn(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-agenity", Version: "0.5.17"},
		Name:            "agenity",
		OrganizationRef: "agnstar.omani.homes",
		EnvironmentRef:  "agnstar-prod",
		Placement:       applicationPlacement{Mode: "singleton", Regions: []string{"primary"}},
	}
	obj := newApplicationUnstructured(req, "omantel.biz", "console.agnstar.omani.homes")
	params, found, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil for agenity install")
	}
	if params["sovereignFqdn"] != "omantel.biz" {
		t.Fatalf("agenity Application CR must carry spec.parameters.sovereignFqdn=omantel.biz (else the openova-MCP URL falls back to the mothership console.openova.io), got %#v", params)
	}
	// #4610 — the install-door CR must ALSO wire the per-Org MCP bearer.
	assertAgenityMCPBearerWiring(t, params, "agnstar")
	// #4624 — the install-door CR must ALSO pin the ORG console host as the
	// MCP tenant host (X-Tenant-Host), while sovereignFqdn (above) keeps the
	// catalyst-api URL on the SOVEREIGN console host.
	mcp := params["openovaMCP"].(map[string]interface{})
	if mcp["tenantHost"] != "console.agnstar.omani.homes" {
		t.Fatalf("openovaMCP.tenantHost = %v, want console.agnstar.omani.homes", mcp["tenantHost"])
	}
}

func TestNewApplicationCRFromSeed_Agenity_StampsSovereignFqdn(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:           "agenity",
		Namespace:      "agnstar.omani.homes",
		Blueprint:      "bp-agenity",
		Topology:       "singleton",
		SovereignFQDN:  "omantel.biz",
		OrgConsoleHost: "console.agnstar.omani.homes",
	}
	obj := newApplicationCRFromSeed(seed)
	params, found, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil for agenity seed")
	}
	if params["sovereignFqdn"] != "omantel.biz" {
		t.Fatalf("agenity seed CR must carry spec.parameters.sovereignFqdn=omantel.biz, got %#v", params)
	}
	// #4610 — the create-instance seed door must ALSO wire the per-Org MCP bearer.
	assertAgenityMCPBearerWiring(t, params, "agnstar")
	// #4624 — the seed door must ALSO pin the ORG console host as the MCP
	// tenant host.
	mcp := params["openovaMCP"].(map[string]interface{})
	if mcp["tenantHost"] != "console.agnstar.omani.homes" {
		t.Fatalf("openovaMCP.tenantHost = %v, want console.agnstar.omani.homes", mcp["tenantHost"])
	}
}

// ── #4624: bp-agenity stamps openovaMCP.tenantHost = the ORG console host so
//    OPENOVA_MCP_TENANT_HOST (the X-Tenant-Host the MCP forwards on the
//    org-scoped install path) targets the Org, NOT the Sovereign console
//    host. Live-proven on hw220 2026-07-04: with
//    TENANT_HOST=console.hw220.omani.works every create_application returned
//    404 tenant-not-registered (MCP error -32010); setting it to
//    console.nstar.omani.homes made the same call return 201. ──────────────

func TestDefaultedParameters_AgenityStampsOrgConsoleTenantHost(t *testing.T) {
	// The decisive #4624 case: orgSlug=nstar + pool domain omani.homes ⇒ the
	// stamped TENANT_HOST is console.nstar.omani.homes while the catalyst-api
	// URL derivation input (sovereignFqdn) stays the SOVEREIGN fqdn.
	got := defaultedParameters("bp-agenity", "singleton", "hw220.omani.works", "nstar", "console.nstar.omani.homes", nil)
	if got["sovereignFqdn"] != "hw220.omani.works" {
		t.Fatalf("sovereignFqdn must stay the SOVEREIGN fqdn (the catalyst-api URL derives https://console.<sovereignFqdn> from it), got %#v", got)
	}
	mcp, ok := got["openovaMCP"].(map[string]interface{})
	if !ok {
		t.Fatalf("agenity parameters missing openovaMCP block: %#v", got)
	}
	if mcp["tenantHost"] != "console.nstar.omani.homes" {
		t.Fatalf("openovaMCP.tenantHost = %v, want console.nstar.omani.homes (the ORG console host — a Sovereign-host X-Tenant-Host 404s tenant-not-registered)", mcp["tenantHost"])
	}
}

func TestDefaultedParameters_AgenityExplicitTenantHostWins(t *testing.T) {
	explicit := map[string]interface{}{
		"openovaMCP": map[string]interface{}{"tenantHost": "console.pinned.example"},
	}
	got := defaultedParameters("bp-agenity", "singleton", "hw220.omani.works", "nstar", "console.nstar.omani.homes", explicit)
	mcp := got["openovaMCP"].(map[string]interface{})
	if mcp["tenantHost"] != "console.pinned.example" {
		t.Fatalf("a caller-pinned openovaMCP.tenantHost must NOT be overwritten, got %#v", mcp["tenantHost"])
	}
	// The additive sibling stamp (#4610 mcpBearer wiring) must still land.
	assertAgenityMCPBearerWiring(t, got, "nstar")
}

func TestDefaultedParameters_AgenityEmptyOrgConsoleHost_NoTenantHostStamp(t *testing.T) {
	// Mothership / Catalyst-Zero / registry-miss: empty orgConsoleHost ⇒ no
	// tenantHost stamp (fail-closed — the chart keeps its existing
	// behaviour); the sibling stamps are unaffected.
	got := defaultedParameters("bp-agenity", "singleton", "hw220.omani.works", "nstar", "", nil)
	mcp, ok := got["openovaMCP"].(map[string]interface{})
	if !ok {
		t.Fatalf("agenity parameters missing openovaMCP block (the #4610 bearer stamp): %#v", got)
	}
	if _, present := mcp["tenantHost"]; present {
		t.Fatalf("empty orgConsoleHost must NOT stamp openovaMCP.tenantHost, got %#v", mcp["tenantHost"])
	}
}

func TestDefaultedParameters_NonAgenity_NoTenantHostStamp(t *testing.T) {
	got := defaultedParameters("bp-postgres", "singleton", "hw220.omani.works", "nstar", "console.nstar.omani.homes", nil)
	if _, ok := got["openovaMCP"]; ok {
		t.Fatalf("non-agenity Blueprint must NOT get openovaMCP wiring, got %#v", got)
	}
}

// orgConsoleHostFromRegistrations — the tenant-registry resolver behind the
// #4624 stamp. Must resolve every Org-ref dialect the two install doors
// carry (real namespace / bare slug / dotted Org zone), ignore non-org rows,
// and fail closed on a miss.
func TestOrgConsoleHostFromRegistrations(t *testing.T) {
	regs := []store.TenantRegistration{
		// The Sovereign's own console row (tenant_kind=otech) must NEVER win.
		{Host: "console.hw220.omani.works", TenantID: "self", TenantKind: store.TenantKindOTECH},
		// Funnel/reconcile-shaped org row: OrganizationNamespace = slug.
		{Host: "console.nstar.omani.homes", TenantID: "t-nstar", TenantKind: store.TenantKindOrg, OrganizationNamespace: "nstar"},
		// BSS-shaped org row: OrganizationNamespace = org-<uuid>.
		{Host: "console.acme.omani.rest", TenantID: "t-acme", TenantKind: store.TenantKindOrg, OrganizationNamespace: "org-t-acme"},
	}
	cases := []struct {
		ref, want string
	}{
		{"nstar", "console.nstar.omani.homes"},             // bare slug → ns match
		{"nstar.omani.homes", "console.nstar.omani.homes"}, // dotted Org zone → slug-of-host match
		{"org-t-acme", "console.acme.omani.rest"},          // forced real namespace (org door) → ns match
		{"acme.omani.rest", "console.acme.omani.rest"},     // dotted zone for the BSS-shaped row
		{"hw220.omani.works", ""},                          // the Sovereign itself is NOT an Org — fail closed
		{"unknown", ""},                                    // registry miss — fail closed
		{"", ""},                                           // empty ref — fail closed
	}
	for _, c := range cases {
		if got := orgConsoleHostFromRegistrations(regs, c.ref); got != c.want {
			t.Errorf("orgConsoleHostFromRegistrations(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

// ── configSchema round-trip: producers emit validating parameters ───────

// The decisive regression test: the create-instance seed path
// (newApplicationCRFromSeed — the path wireBackingServices uses to build
// shared-pg-d/-e) with NO Values must emit a spec.parameters that ROUND-
// TRIPS the real bp-postgres configSchema. Pre-fix it emitted no
// parameters key at all → "#: expected object, but got null".
func TestNewApplicationCRFromSeed_PostgresNoValues_ParametersValidateConfigSchema(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "shared-pg-d",
		Namespace: "omantel-biz",
		Blueprint: "bp-postgres",
		Topology:  "singleton",
		// Values intentionally empty — the backing-service auto-create path.
	}
	obj := newApplicationCRFromSeed(seed)

	params, found, err := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if err != nil {
		t.Fatalf("read spec.parameters: %v", err)
	}
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil — must be a non-null object (this is the #4283 bug)")
	}
	if params["topology"].(map[string]interface{})["mode"] != "singleton" {
		t.Fatalf("spec.parameters.topology.mode = %v, want singleton", params["topology"])
	}

	rep, vErr := validate.Parameters(bpPostgresConfigSchema(), params)
	if vErr != nil {
		t.Fatalf("validate.Parameters internal error: %v", vErr)
	}
	if !rep.Valid {
		t.Fatalf("produced spec.parameters does NOT satisfy bp-postgres configSchema: %v", rep.Errors)
	}
}

func TestNewApplicationCRFromSeed_PostgresActiveHotStandby_ValidatesConfigSchema(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "shared-pg-e",
		Namespace: "omantel-biz",
		Blueprint: "bp-postgres",
		Topology:  "active-hot-standby",
	}
	obj := newApplicationCRFromSeed(seed)
	params, found, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if !found || params == nil {
		t.Fatal("spec.parameters absent/nil for active-hot-standby seed")
	}
	if params["topology"].(map[string]interface{})["mode"] != "active-hot-standby" {
		t.Fatalf("topology.mode = %v, want active-hot-standby", params["topology"])
	}
	rep, vErr := validate.Parameters(bpPostgresConfigSchema(), params)
	if vErr != nil {
		t.Fatalf("validate.Parameters internal error: %v", vErr)
	}
	if !rep.Valid {
		t.Fatalf("active-hot-standby parameters fail configSchema: %v", rep.Errors)
	}
}

// The install handler path (newApplicationUnstructured) with NO parameters
// must likewise emit a non-null, configSchema-valid spec.parameters for a
// postgres Application.
func TestNewApplicationUnstructured_PostgresNoParams_ValidatesConfigSchema(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-postgres", Version: "0.2.5"},
		Name:            "shared-pg-d",
		OrganizationRef: "omantel-biz",
		EnvironmentRef:  "omantel-biz-prod",
		Placement:       applicationPlacement{Mode: "singleton", Regions: []string{"primary"}},
	}
	obj := newApplicationUnstructured(req, "", "")
	params, found, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil — newApplicationUnstructured must always stamp a non-null object")
	}
	rep, vErr := validate.Parameters(bpPostgresConfigSchema(), params)
	if vErr != nil {
		t.Fatalf("validate.Parameters internal error: %v", vErr)
	}
	if !rep.Valid {
		t.Fatalf("produced spec.parameters fail bp-postgres configSchema: %v", rep.Errors)
	}
}

// Guard the exact pre-fix failure mode: a totally-absent parameters block
// (nil) is what the validator rejected as "expected object, but got null"
// only when the CR carried an explicit null; the producers now never emit
// that. This asserts the validator's contract so the producers' guarantee
// is meaningful — an explicit JSON null is rejected, a {} object is not.
func TestConfigSchema_RejectsExplicitNull_AcceptsEmptyObject(t *testing.T) {
	schema := bpPostgresConfigSchema()

	repNull, _ := validate.Parameters(schema, nil)
	// validate.Parameters folds a Go nil to {} (the absent-key case), which
	// is valid — so the bug was an EXPLICIT JSON null surviving the Git
	// round-trip, which the producers now prevent by always stamping {}.
	if !repNull.Valid {
		t.Fatalf("nil parameters (absent key) should validate as empty object, got errors: %v", repNull.Errors)
	}

	repEmpty, _ := validate.Parameters(schema, map[string]interface{}{})
	if !repEmpty.Valid {
		t.Fatalf("empty object {} must validate against bp-postgres configSchema, got: %v", repEmpty.Errors)
	}
}

// TestDefaultedParameters_AgenityStampsGateHost locks #4739 W-3: the MCP
// install path must stamp httpRoute.hostnames = [agenity.<slug>.<pool>] from
// the Org console host, so the chart's gateHostname resolves to the RESOLVABLE
// Org host instead of falling back to agenity.<sovereignFqdn> (no DNS).
func TestDefaultedParameters_AgenityStampsGateHost(t *testing.T) {
	got := defaultedParameters("bp-agenity", "singleton", "hw220.omani.works", "nstar", "console.nstar.omani.homes", nil)
	hr, ok := got["httpRoute"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected httpRoute block, got %#v", got["httpRoute"])
	}
	hosts, ok := hr["hostnames"].([]interface{})
	if !ok || len(hosts) != 1 || hosts[0] != "agenity.nstar.omani.homes" {
		t.Fatalf("gate host = %#v, want [agenity.nstar.omani.homes] (Org host, not agenity.hw220.omani.works)", hr["hostnames"])
	}
}

// TestDefaultedParameters_AgenityGateHostEmptyOrgConsole_NoStamp: mothership /
// no-registry-row case → leave the chart's fail-closed sovereignFqdn default.
func TestDefaultedParameters_AgenityGateHostEmptyOrgConsole_NoStamp(t *testing.T) {
	got := defaultedParameters("bp-agenity", "singleton", "hw220.omani.works", "nstar", "", nil)
	if _, present := got["httpRoute"]; present {
		t.Fatalf("no gate host must be stamped when orgConsoleHost is empty, got %#v", got["httpRoute"])
	}
}

// TestDefaultedParameters_AgenityExplicitGateHostWins: an explicit
// httpRoute.hostnames the caller supplied is never clobbered.
func TestDefaultedParameters_AgenityExplicitGateHostWins(t *testing.T) {
	explicit := map[string]interface{}{
		"httpRoute": map[string]interface{}{"hostnames": []interface{}{"agenity.custom.example"}},
	}
	got := defaultedParameters("bp-agenity", "singleton", "hw220.omani.works", "nstar", "console.nstar.omani.homes", explicit)
	hr := got["httpRoute"].(map[string]interface{})
	hosts := hr["hostnames"].([]interface{})
	if hosts[0] != "agenity.custom.example" {
		t.Fatalf("explicit gate host must win, got %#v", hosts)
	}
}
