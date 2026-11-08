package regions

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRegistry_FromRegionsJSON_VerbatimIDsFirstIsPrimary(t *testing.T) {
	defer SetEnvSource(env(map[string]string{
		"SOVEREIGN_REGIONS_JSON":   `[{"cloudRegion":"hw-me-east-215-a-rtz-prod","provider":"huawei"},{"cloudRegion":"hw-me-east-215-b-rtz-prod","provider":"huawei"}]`,
		"SOVEREIGN_PRIMARY_REGION": "ignored-when-json-present",
	}))()
	got := Registry()
	if len(got) != 2 || got[0].ID != "hw-me-east-215-a-rtz-prod" || got[1].ID != "hw-me-east-215-b-rtz-prod" {
		t.Fatalf("registry = %+v", got)
	}
	if !got[0].Primary || got[1].Primary {
		t.Fatalf("primary flags wrong: %+v", got)
	}
	if got[0].Provider != "huawei" {
		t.Fatalf("provider = %q", got[0].Provider)
	}
	if _, ok := Lookup("me-east-215-a"); ok {
		t.Fatal("a re-spelled region id must not resolve")
	}
	if _, ok := Lookup("hw-me-east-215-b-rtz-prod"); !ok {
		t.Fatal("canonical id must resolve")
	}
}

func TestRegistry_FallbackPrimaryReplicaThenCSV(t *testing.T) {
	restore := SetEnvSource(env(map[string]string{
		"SOVEREIGN_PRIMARY_REGION": "r-a",
		"SOVEREIGN_REPLICA_REGION": "r-b",
	}))
	if ids := IDs(); len(ids) != 2 || ids[0] != "r-a" || ids[1] != "r-b" {
		t.Fatalf("primary/replica ids = %v", ids)
	}
	restore()
	restore = SetEnvSource(env(map[string]string{"CATALYST_CONFIGURED_REGIONS": " r-x, r-y ,,r-x"}))
	if ids := IDs(); len(ids) != 2 || ids[0] != "r-x" || ids[1] != "r-y" {
		t.Fatalf("csv ids = %v", ids)
	}
	restore()
}

func TestRegistry_EmptyMeansNoRegion(t *testing.T) {
	defer SetEnvSource(env(map[string]string{}))()
	if len(Registry()) != 0 {
		t.Fatal("no source must yield an empty registry")
	}
	if _, ok := Primary(); ok {
		t.Fatal("no primary may be invented")
	}
	if _, ok := Lookup("primary"); ok {
		t.Fatal(`the "primary" sentinel is not a region`)
	}
}

// ADR-0015 D3 — the API and the controllers read SOVEREIGN_REGIONS_JSON with
// ONE parser (core/controllers/pkg/regions.Parse). The sovereign-fqdn
// ConfigMap writes `toJson | quote` of the chart value, which is a JSON
// STRING when the value arrived as a string; the pre-integration API parser
// returned an EMPTY registry for that shape (every create → 422
// region-registry-empty) while the controllers resolved both regions.
func TestRegistry_SameParserAsControllers_QuotedAndStringArrayForms(t *testing.T) {
	cases := map[string]string{
		"quoted-json-string": `"[{\"cloudRegion\":\"hw-me-east-215-a-rtz-prod\",\"provider\":\"huawei\"},{\"cloudRegion\":\"hw-me-east-215-b-rtz-prod\"}]"`,
		"string-array":       `["hw-me-east-215-a-rtz-prod","hw-me-east-215-b-rtz-prod"]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			defer SetEnvSource(env(map[string]string{"SOVEREIGN_REGIONS_JSON": raw}))()
			ids := IDs()
			if len(ids) != 2 || ids[0] != "hw-me-east-215-a-rtz-prod" || ids[1] != "hw-me-east-215-b-rtz-prod" {
				t.Fatalf("ids = %v", ids)
			}
		})
	}
	defer SetEnvSource(env(map[string]string{"SOVEREIGN_REGIONS_JSON": cases["quoted-json-string"]}))()
	if p, _ := Primary(); p.Provider != "huawei" {
		t.Fatalf("provider not read through the quoted form: %+v", p)
	}
}
