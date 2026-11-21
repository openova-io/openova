package orgtenantrepos

import (
	"strings"
	"testing"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestRegistryPivoted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		status map[string]string
		want   bool
	}{
		{"mothership: nothing", nil, nil, false},
		{"Sovereign identity alone", map[string]string{"SOVEREIGN_FQDN": "hw.test"}, map[string]string{"registriesYamlActive": "v1"}, false},
		{"status v2, no step-07 stamp yet", nil, map[string]string{"registriesYamlActive": "v2"}, true},
		{"status v2 with whitespace", nil, map[string]string{"registriesYamlActive": " v2\n"}, true},
		{"status rollback", nil, map[string]string{"registriesYamlActive": "rollback"}, false},
		{"step results are not the fact", nil, map[string]string{"step.registry-pivot.result": "success", "step.helmrepository-patches.result": "success"}, false},
		{"step-07 pin issuer", map[string]string{"CATALYST_PIN_ISSUER": "https://console.hw.test"}, nil, true},
		{"step-07 hand-over issuer", map[string]string{"CATALYST_HANDOVER_JWT_ISSUER": "https://console.hw.test"}, nil, true},
	} {
		if got := RegistryPivoted(envOf(tc.env), tc.status); got != tc.want {
			t.Errorf("%s: RegistryPivoted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBaseFromStatus(t *testing.T) {
	env := envOf(map[string]string{"SOVEREIGN_FQDN": "hw.test"})
	if got := Base(env, map[string]string{"registriesYamlActive": "v2"}); got != "oci://registry.hw.test/openova-io" {
		t.Errorf("pivoted by status: %q", got)
	}
	if got := Base(env, map[string]string{"registriesYamlActive": "v1"}); got != GHCRBase {
		t.Errorf("not pivoted: %q", got)
	}
	if got := Base(envOf(nil), map[string]string{"registriesYamlActive": "v2"}); got != GHCRBase {
		t.Errorf("pivoted without an fqdn must fail safe to the public catalog: %q", got)
	}
}

func TestNamesPublicCatalog(t *testing.T) {
	if !NamesPublicCatalog([]byte(Template)) {
		t.Error("the template names the public catalog")
	}
	local := Render("oci://registry.hw.test/openova-io", TrustSecretName)
	if NamesPublicCatalog([]byte(local)) {
		t.Errorf("a rendered pivoted file still reads as public:\n%s", local)
	}
	one := strings.Replace(local, "url: oci://registry.hw.test/openova-io", "url: "+GHCRBase, 1)
	if !NamesPublicCatalog([]byte(one)) {
		t.Error("one block on the public catalog must be found")
	}
	if NamesPublicCatalog([]byte("# url: " + GHCRBase + "\n")) {
		t.Error("a comment is not a url")
	}
}
