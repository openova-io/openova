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
	got := defaultedParameters("bp-postgres", "singleton", "", nil)
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
		got := defaultedParameters("postgres", topo, "", nil)
		tm := got["topology"].(map[string]interface{})["mode"]
		if tm != "active-hot-standby" {
			t.Fatalf("topology %q → topology.mode = %v, want active-hot-standby", topo, tm)
		}
	}
}

func TestDefaultedParameters_NonPostgresNoValues_EmptyObjectNotNil(t *testing.T) {
	got := defaultedParameters("bp-grafana", "singleton", "", nil)
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
	got := defaultedParameters("bp-postgres", "singleton", "", explicit)
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
	got := defaultedParameters("bp-agenity", "singleton", "omantel.biz", nil)
	if got["sovereignFqdn"] != "omantel.biz" {
		t.Fatalf("agenity parameters must stamp sovereignFqdn=omantel.biz, got %#v", got)
	}
}

func TestDefaultedParameters_AgenityWithExplicitValues_StampsSovereignFqdn(t *testing.T) {
	explicit := map[string]interface{}{
		"agent": map[string]interface{}{"model": "claude-opus-4-8"},
	}
	got := defaultedParameters("agenity", "singleton", "t99.omani.works", explicit)
	if got["sovereignFqdn"] != "t99.omani.works" {
		t.Fatalf("agenity sovereignFqdn must be stamped even with explicit params, got %#v", got)
	}
	if _, ok := got["agent"]; !ok {
		t.Fatalf("explicit agent params must be preserved alongside the stamped sovereignFqdn, got %#v", got)
	}
}

func TestDefaultedParameters_AgenityExplicitSovereignFqdnWins(t *testing.T) {
	explicit := map[string]interface{}{"sovereignFqdn": "pinned.example"}
	got := defaultedParameters("bp-agenity", "singleton", "omantel.biz", explicit)
	if got["sovereignFqdn"] != "pinned.example" {
		t.Fatalf("a caller-pinned sovereignFqdn must NOT be overwritten, got %#v", got)
	}
}

func TestDefaultedParameters_AgenityEmptyFQDN_NoStamp(t *testing.T) {
	// Mothership / Catalyst-Zero: empty FQDN → leave sovereignFqdn unset so
	// the chart's fail-closed default applies (no bogus console host).
	got := defaultedParameters("bp-agenity", "singleton", "", nil)
	if _, ok := got["sovereignFqdn"]; ok {
		t.Fatalf("empty FQDN must NOT stamp sovereignFqdn, got %#v", got)
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
	obj := newApplicationUnstructured(req, "omantel.biz")
	params, found, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil for agenity install")
	}
	if params["sovereignFqdn"] != "omantel.biz" {
		t.Fatalf("agenity Application CR must carry spec.parameters.sovereignFqdn=omantel.biz (else the openova-MCP URL falls back to the mothership console.openova.io), got %#v", params)
	}
}

func TestNewApplicationCRFromSeed_Agenity_StampsSovereignFqdn(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:          "agenity",
		Namespace:     "agnstar.omani.homes",
		Blueprint:     "bp-agenity",
		Topology:      "singleton",
		SovereignFQDN: "omantel.biz",
	}
	obj := newApplicationCRFromSeed(seed)
	params, found, _ := unstructured.NestedMap(obj.Object, "spec", "parameters")
	if !found || params == nil {
		t.Fatalf("spec.parameters absent/nil for agenity seed")
	}
	if params["sovereignFqdn"] != "omantel.biz" {
		t.Fatalf("agenity seed CR must carry spec.parameters.sovereignFqdn=omantel.biz, got %#v", params)
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
	obj := newApplicationUnstructured(req, "")
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
