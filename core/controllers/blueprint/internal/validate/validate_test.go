package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// loadBlueprint parses raw YAML into an Unstructured. Empty / whitespace
// docs return nil.
func loadBlueprint(t *testing.T, raw []byte) *unstructured.Unstructured {
	t.Helper()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var obj map[string]interface{}
	if err := yaml.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	if obj == nil {
		return nil
	}
	return &unstructured.Unstructured{Object: obj}
}

// minimalBlueprint returns an Unstructured Blueprint that the validator
// accepts cleanly.
func minimalBlueprint() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName("bp-test")
	u.Object["spec"] = map[string]interface{}{
		"version": "1.0.0",
		"card": map[string]interface{}{
			"title": "Test",
		},
		"placementSchema": map[string]interface{}{
			"modes":   []interface{}{"single-region"},
			"default": "single-region",
		},
	}
	return u
}

func TestValidate_HappyPath(t *testing.T) {
	t.Parallel()
	bp := minimalBlueprint()
	res := Validate(bp, nil)
	if res.HasErrors() {
		t.Fatalf("expected no errors, got %v", res.Errors)
	}
	if len(res.PendingDeps) > 0 {
		t.Errorf("expected no pending deps, got %v", res.PendingDeps)
	}
}

func TestValidate_PlacementModes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		modes     interface{}
		def       string
		wantError bool
	}{
		{"valid single", []interface{}{"single-region"}, "", false},
		{"valid multiple", []interface{}{"single-region", "active-active"}, "", false},
		// Bootstrap-topology tier (docs/SOVEREIGN-MULTI-REGION-DOD.md A4)
		// — used by bp-*-vcluster + bp-vcluster-helmrepo. NOT user-
		// selectable; documents which regions the bootstrap layer
		// auto-installs the chart into. See canonicalPlacementModes in
		// validate.go for the full mode taxonomy.
		{"valid primary-only", []interface{}{"primary-only"}, "", false},
		{"valid secondary-only", []interface{}{"secondary-only"}, "", false},
		{"valid every-region", []interface{}{"every-region"}, "", false},
		{"valid default primary-only", []interface{}{"primary-only"}, "primary-only", false},
		{"invalid mode", []interface{}{"round-robin"}, "", true},
		{"empty array", []interface{}{}, "", true},
		{"null array", nil, "", true},
		{"valid default in modes", []interface{}{"single-region", "active-active"}, "active-active", false},
		{"default not in modes", []interface{}{"single-region"}, "active-active", true},
		{"invalid default value", []interface{}{"single-region"}, "round-robin", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bp := minimalBlueprint()
			ps := bp.Object["spec"].(map[string]interface{})["placementSchema"].(map[string]interface{})
			ps["modes"] = tc.modes
			if tc.def != "" {
				ps["default"] = tc.def
			} else {
				delete(ps, "default")
			}
			res := Validate(bp, nil)
			if tc.wantError && !res.HasErrors() {
				t.Errorf("expected error, got none. result=%+v", res)
			}
			if !tc.wantError && res.HasErrors() {
				t.Errorf("expected no error, got %v", res.Errors)
			}
		})
	}
}

func TestValidate_ManifestSourceKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind      string
		wantError bool
	}{
		{"HelmChart", false},
		{"Kustomize", false},
		{"OAM", false},
		{"helm-chart", true}, // wrong case
		{"Docker", true},
	}
	for _, tc := range cases {
		bp := minimalBlueprint()
		bp.Object["spec"].(map[string]interface{})["manifests"] = map[string]interface{}{
			"source": map[string]interface{}{
				"kind": tc.kind,
				"ref":  "oci://example/test:1.0.0",
			},
		}
		res := Validate(bp, nil)
		if tc.wantError && !res.HasErrors() {
			t.Errorf("kind=%q: expected error, got none", tc.kind)
		}
		if !tc.wantError && res.HasErrors() {
			t.Errorf("kind=%q: expected no error, got %v", tc.kind, res.Errors)
		}
	}

	// Short-form `manifests.chart: ./chart` with no source block must NOT
	// trigger an error. Most existing v1alpha1 blueprints use this form.
	bp := minimalBlueprint()
	bp.Object["spec"].(map[string]interface{})["manifests"] = map[string]interface{}{
		"chart": "./chart",
	}
	if res := Validate(bp, nil); res.HasErrors() {
		t.Errorf("short-form manifests.chart: expected no error, got %v", res.Errors)
	}
}

func TestValidate_UpgradesSemver(t *testing.T) {
	t.Parallel()

	// All these forms appear in the existing 61-blueprint corpus and
	// must validate cleanly.
	good := []string{"0.x", "1.x", "1.2.x", "^1.4", "1.0.0", "~1.4", ">=1.0.0 <2"}
	for _, fr := range good {
		bp := minimalBlueprint()
		bp.Object["spec"].(map[string]interface{})["upgrades"] = map[string]interface{}{
			"from": []interface{}{fr},
		}
		if res := Validate(bp, nil); res.HasErrors() {
			t.Errorf("upgrades.from=%q: expected no error, got %v", fr, res.Errors)
		}
	}

	bad := []string{"abc", "1.2.3.4", "v1.0.0"}
	for _, fr := range bad {
		bp := minimalBlueprint()
		bp.Object["spec"].(map[string]interface{})["upgrades"] = map[string]interface{}{
			"from": []interface{}{fr},
		}
		if res := Validate(bp, nil); !res.HasErrors() {
			t.Errorf("upgrades.from=%q: expected error, got none", fr)
		}
	}
}

func TestValidate_DependsResolution(t *testing.T) {
	t.Parallel()

	bp := minimalBlueprint()
	bp.Object["spec"].(map[string]interface{})["depends"] = []interface{}{
		map[string]interface{}{
			"blueprint": "bp-postgres",
			"version":   "^1.4",
		},
		map[string]interface{}{
			"blueprint": "bp-keycloak",
			"version":   "^1.0",
		},
	}

	// Empty catalog → all deps Pending.
	res := Validate(bp, map[string]struct{}{})
	if res.HasErrors() {
		t.Errorf("dep-pending must NOT be a hard error, got %v", res.Errors)
	}
	if len(res.PendingDeps) != 2 {
		t.Errorf("expected 2 pending deps, got %v", res.PendingDeps)
	}

	// Catalog with bp-postgres present → only bp-keycloak pends.
	res = Validate(bp, map[string]struct{}{"bp-postgres": {}})
	if len(res.PendingDeps) != 1 || res.PendingDeps[0] != "bp-keycloak" {
		t.Errorf("expected only bp-keycloak pending, got %v", res.PendingDeps)
	}

	// Catalog with both present → 0 pending.
	res = Validate(bp, map[string]struct{}{"bp-postgres": {}, "bp-keycloak": {}})
	if len(res.PendingDeps) != 0 {
		t.Errorf("expected 0 pending deps, got %v", res.PendingDeps)
	}

	// Bare-name vs bp-prefixed: Blueprint depends on `bp-postgres` but
	// catalog only has `postgres` (or vice versa). Must resolve.
	res = Validate(bp, map[string]struct{}{"postgres": {}, "keycloak": {}})
	if len(res.PendingDeps) != 0 {
		t.Errorf("bare-name resolution: expected 0 pending, got %v", res.PendingDeps)
	}
}

func TestValidate_NameWarning(t *testing.T) {
	t.Parallel()

	// bp-<kebab(title)> exact match — no warning.
	bp := minimalBlueprint()
	bp.SetName("bp-test")
	bp.Object["spec"].(map[string]interface{})["card"].(map[string]interface{})["title"] = "Test"
	if res := Validate(bp, nil); len(res.Warnings) > 0 {
		t.Errorf("bp-<kebab(title)> exact: expected no warnings, got %v", res.Warnings)
	}

	// bare kebab matching title — no warning.
	bp = minimalBlueprint()
	bp.SetName("test")
	bp.Object["spec"].(map[string]interface{})["card"].(map[string]interface{})["title"] = "Test"
	if res := Validate(bp, nil); len(res.Warnings) > 0 {
		t.Errorf("bare kebab title: expected no warnings, got %v", res.Warnings)
	}

	// Mismatched name — warning, not error.
	bp = minimalBlueprint()
	bp.SetName("bp-totally-different")
	bp.Object["spec"].(map[string]interface{})["card"].(map[string]interface{})["title"] = "WordPress"
	res := Validate(bp, nil)
	if res.HasErrors() {
		t.Errorf("name mismatch must NOT be an error, got %v", res.Errors)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("name mismatch: expected warning, got none")
	}
}

func TestTitleToKebab(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"WordPress":             "wordpress",
		"WordPress Tenant":      "wordpress-tenant",
		"Hetzner CSI":           "hetzner-csi",
		"kube-prometheus-stack": "kube-prometheus-stack",
		"NATS / JetStream":      "nats-jetstream",
		"":                      "",
		"a_b_c":                 "a-b-c",
		"  spaces   ":           "spaces",
	}
	for in, want := range cases {
		got := titleToKebab(in)
		if got != want {
			t.Errorf("titleToKebab(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidate_ExistingBlueprintCorpus loads every real
// `platform/*/blueprint.yaml` from the openova repo and runs the
// business-logic validator. Per slice C3 brief: the controller must
// not regress validation of the existing 61-blueprint corpus.
//
// We tolerate Warnings (the soft name-vs-title mismatch) and
// PendingDeps (the corpus references each other; we don't pre-load the
// full set into the catalog for this run). Errors fail the test.
//
// Path resolution: from this test file at
// `core/controllers/blueprint/internal/validate/validate_test.go`,
// the blueprint corpus lives 5 levels up at `platform/*/blueprint.yaml`.
func TestValidate_ExistingBlueprintCorpus(t *testing.T) {
	t.Parallel()
	repoRoot := findRepoRoot(t)
	pattern := filepath.Join(repoRoot, "platform", "*", "blueprint.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Skipf("no blueprint.yaml files under %s; skipping corpus check", pattern)
	}

	// Pre-build a synthetic catalog from the corpus filenames so
	// inter-corpus depends[] entries resolve. Each
	// `platform/<name>/blueprint.yaml` adds both `<name>` and
	// `bp-<name>` to the catalog (the depends[].blueprint string in
	// the corpus uses both forms).
	catalog := make(map[string]struct{}, 2*len(matches))
	for _, m := range matches {
		dir := filepath.Base(filepath.Dir(m))
		catalog[dir] = struct{}{}
		catalog["bp-"+dir] = struct{}{}
	}
	// Add a couple of well-known ones that some blueprints reference
	// but that don't have their own folder yet (e.g. bp-postgres,
	// bp-reflector). The validator tolerates pending deps; these
	// entries just keep noise down.
	catalog["bp-postgres"] = struct{}{}
	catalog["bp-reflector"] = struct{}{}

	type failure struct {
		path   string
		errors []string
	}
	var failures []failure
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			t.Errorf("read %s: %v", m, err)
			continue
		}
		bp := loadBlueprint(t, raw)
		if bp == nil {
			t.Errorf("%s: parsed to nil", m)
			continue
		}
		res := Validate(bp, catalog)
		if res.HasErrors() {
			failures = append(failures, failure{path: m, errors: res.Errors})
		}
	}

	t.Logf("validated %d blueprints; %d failed business-logic checks", len(matches), len(failures))
	if len(failures) > 0 {
		for _, f := range failures {
			t.Errorf("FAIL %s:\n  - %s", f.path, strings.Join(f.errors, "\n  - "))
		}
	}
}

// findRepoRoot walks upward from the test's CWD until it finds a
// directory containing both `platform/` and `products/` (the
// monorepo's two top-level Blueprint trees). When run via `go test`
// the CWD is the package dir.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 10; i++ {
		platformDir := filepath.Join(dir, "platform")
		productsDir := filepath.Join(dir, "products")
		if isDir(platformDir) && isDir(productsDir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skipf("could not find repo root from %s; skipping corpus check", wd)
	return ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// --------------------------------------------------------------------
// G117.1 AC2 (#2740) — JSON Schema topology gate.
// These four tests cover the new `Validate*Topology` flow that loads
// `platform/_schemas/blueprint-topology.json` (vendored into
// `schemas/blueprint-topology.json`) and surfaces Findings with
// Code=`topology-schema-violation`.
// --------------------------------------------------------------------

// blueprintWithTopology returns a minimal Blueprint augmented with a
// full, well-formed G117 spec.topology + spec.endpoints + spec.sso +
// spec.multiInstance block. Tests mutate sub-trees on the returned
// object to surface specific schema violations.
func blueprintWithTopology() *unstructured.Unstructured {
	bp := minimalBlueprint()
	spec := bp.Object["spec"].(map[string]interface{})
	spec["topology"] = map[string]interface{}{
		"supported": []interface{}{"active-hot-standby", "singleton"},
		"defaults": map[string]interface{}{
			"multi-region":  "active-hot-standby",
			"single-region": "singleton",
		},
		"perTopology": map[string]interface{}{
			"singleton": map[string]interface{}{
				"placement": map[string]interface{}{
					"tier":     "dmz",
					"clusters": []interface{}{"dmz-A"},
				},
			},
			"active-hot-standby": map[string]interface{}{
				"replication": map[string]interface{}{
					"backend": "cnpg-pair",
					"mode":    "sync",
				},
				"switchover": map[string]interface{}{
					"mechanism": "bp-continuum",
				},
				"placement": map[string]interface{}{
					"tier":     "dmz",
					"clusters": []interface{}{"dmz-A", "dmz-B"},
				},
			},
		},
	}
	spec["endpoints"] = []interface{}{
		map[string]interface{}{
			"name":             "console",
			"hostnameTemplate": "{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}",
			"port":             int64(443),
			"protocol":         "https",
			"tls":              true,
			"visibility":       "public",
			"ssoEnabled":       true,
		},
	}
	spec["sso"] = map[string]interface{}{
		"realm":          "sovereign",
		"protocolMapper": "oidc",
		"silentLogin":    true,
	}
	spec["multiInstance"] = map[string]interface{}{
		"enabled":   true,
		"maxPerOrg": int64(5),
	}
	return bp
}

// findingsByPathContaining returns the subset of res.Findings whose
// Path or Message contains the substring.
func findingsByPathContaining(res Result, substr string) []Finding {
	var out []Finding
	for _, f := range res.Findings {
		if strings.Contains(f.Path, substr) || strings.Contains(f.Message, substr) {
			out = append(out, f)
		}
	}
	return out
}

// TestValidate_Topology_HappyPath — a fully-formed Blueprint with
// topology + endpoints + sso + multiInstance must produce zero
// topology-schema-violation Findings.
func TestValidate_Topology_HappyPath(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology()
	res := Validate(bp, nil)
	if res.HasErrors() {
		t.Fatalf("happy path must not produce hard errors, got %v", res.Errors)
	}
	for _, f := range res.Findings {
		if f.Code == TopologySchemaViolationCode {
			t.Errorf("happy path produced topology-schema-violation finding: path=%s message=%s", f.Path, f.Message)
		}
	}
}

// TestValidate_Topology_MissingRequired — spec.topology.defaults omits
// the required `single-region` key. Must produce at least one Finding
// pointing at the missing field.
func TestValidate_Topology_MissingRequired(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology()
	topology := bp.Object["spec"].(map[string]interface{})["topology"].(map[string]interface{})
	defaults := topology["defaults"].(map[string]interface{})
	delete(defaults, "single-region")
	res := Validate(bp, nil)
	hits := findingsByPathContaining(res, "single-region")
	if len(hits) == 0 {
		// fall back to scanning all findings for the `defaults` path
		// — different jsonschema versions surface the missing-required
		// error at the parent path vs the field path.
		for _, f := range res.Findings {
			if strings.Contains(f.Path, "/defaults") && f.Code == TopologySchemaViolationCode {
				hits = append(hits, f)
			}
		}
	}
	if len(hits) == 0 {
		t.Fatalf("expected ≥1 topology-schema-violation finding for missing defaults.single-region, got findings=%+v", res.Findings)
	}
}

// TestValidate_Topology_VariantMismatch — spec.topology.perTopology
// defines a variant that is NOT a member of spec.topology.supported[].
// Must surface a topology-schema-violation finding via the cross-field
// check (the embedded JSON Schema can't express this — perTopology
// keys are constrained to the 4 canonical variants but not
// cross-checked against the supported[] subset).
func TestValidate_Topology_VariantMismatch(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology()
	topology := bp.Object["spec"].(map[string]interface{})["topology"].(map[string]interface{})
	// supported[] declares only [singleton] (drop active-hot-standby)
	// while perTopology continues to define an `active-hot-standby`
	// variant — the cross-field check must flag the dangling variant.
	topology["supported"] = []interface{}{"singleton"}
	// Also flip the default to match the new supported set so this
	// test isolates the variant-mismatch finding.
	topology["defaults"] = map[string]interface{}{
		"multi-region":  "singleton",
		"single-region": "singleton",
	}
	res := Validate(bp, nil)
	hits := findingsByPathContaining(res, "active-hot-standby")
	if len(hits) == 0 {
		t.Fatalf("expected ≥1 topology-schema-violation finding for perTopology variant not in supported[], got findings=%+v", res.Findings)
	}
	var found bool
	for _, h := range hits {
		if h.Code == TopologySchemaViolationCode && strings.Contains(h.Message, "active-hot-standby") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one finding to name the dangling variant `active-hot-standby`, got %+v", hits)
	}
}

// #3375 §5.3 — a consumer blueprint declaring replication.backend:
// cnpg-pair but NOT binding a backingServices[] and NOT being a
// data-instance provider is DECORATIVE (the grafana hole). Must surface
// a `decorative-replication-backend` finding (advisory, not a hard
// error).
func TestValidate_DecorativeReplicationBackend_Flagged(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology() // declares cnpg-pair, no backing, not a provider
	res := Validate(bp, nil)
	if res.HasErrors() {
		t.Fatalf("decorative backend must be advisory, not a hard error; got %v", res.Errors)
	}
	hits := findingsByPathContaining(res, "replication/backend")
	if len(hits) == 0 {
		t.Fatalf("expected ≥1 decorative-replication-backend finding, got findings=%+v", res.Findings)
	}
	var found bool
	for _, h := range hits {
		if h.Code == DecorativeReplicationBackendCode && strings.Contains(h.Message, "cnpg-pair") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a %q finding naming cnpg-pair, got %+v", DecorativeReplicationBackendCode, hits)
	}
}

// #3375 §5.3 — a blueprint that BINDS a real backing (backingServices[])
// has a concrete pair behind its cnpg-pair declaration; no decorative
// finding.
func TestValidate_ReplicationBackend_BoundByBackingService_NotFlagged(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology()
	spec := bp.Object["spec"].(map[string]interface{})
	spec["backingServices"] = []interface{}{
		map[string]interface{}{"type": "postgres", "mode": "shared", "instanceRef": "shared-pg-b"},
	}
	res := Validate(bp, nil)
	for _, f := range res.Findings {
		if f.Code == DecorativeReplicationBackendCode {
			t.Errorf("a blueprint that binds a backingServices[] must NOT be flagged decorative; got %+v", f)
		}
	}
}

// #3375 §5.3 — a data-instance PROVIDER (shareable:true) legitimately
// declares cnpg-pair for its OWN pair; no decorative finding. Generality:
// the exemption keys on the declared `shareable` signal, never on a name.
func TestValidate_ReplicationBackend_ProviderExempt(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology()
	spec := bp.Object["spec"].(map[string]interface{})
	spec["shareable"] = true
	res := Validate(bp, nil)
	for _, f := range res.Findings {
		if f.Code == DecorativeReplicationBackendCode {
			t.Errorf("a data-instance provider (shareable:true) must NOT be flagged decorative; got %+v", f)
		}
	}
}

// #3375 §5.3 — a `none` / stateless backend never triggers the
// decorative finding (no cross-region state to materialise).
func TestValidate_ReplicationBackend_StatelessNotFlagged(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology()
	topology := bp.Object["spec"].(map[string]interface{})["topology"].(map[string]interface{})
	ahs := topology["perTopology"].(map[string]interface{})["active-hot-standby"].(map[string]interface{})
	ahs["replication"].(map[string]interface{})["backend"] = "none"
	res := Validate(bp, nil)
	for _, f := range res.Findings {
		if f.Code == DecorativeReplicationBackendCode {
			t.Errorf("a `none` backend must NOT be flagged decorative; got %+v", f)
		}
	}
}

// TestValidate_Topology_InvalidHostname — an endpoint's
// hostnameTemplate is "https://" (URL-like, not a hostname template).
// Must surface a topology-schema-violation finding via the
// hostnameTemplate plausibility check.
func TestValidate_Topology_InvalidHostname(t *testing.T) {
	t.Parallel()
	bp := blueprintWithTopology()
	endpoints := bp.Object["spec"].(map[string]interface{})["endpoints"].([]interface{})
	endpoints[0].(map[string]interface{})["hostnameTemplate"] = "https://"
	res := Validate(bp, nil)
	hits := findingsByPathContaining(res, "hostnameTemplate")
	if len(hits) == 0 {
		t.Fatalf("expected ≥1 topology-schema-violation finding for URL-like hostnameTemplate, got findings=%+v", res.Findings)
	}
	for _, h := range hits {
		if h.Code != TopologySchemaViolationCode {
			t.Errorf("expected Code=%q, got %q", TopologySchemaViolationCode, h.Code)
		}
	}
}
