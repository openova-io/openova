package orgtenantrepos

import (
	"strings"
	"testing"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// usableStatus is the cutover status once step 06 has succeeded.
var usableStatus = map[string]string{StatusKeyRegistries: "v2", StatusKeySourcesPivoted: "success"}

// The Sovereign's own source is usable only once step 06 wrote the credential
// and the trust: the registry pivot of step 03 alone is not enough (on main it
// was, so an Organization written in between got a source that cannot work).
func TestOwnSourceUsable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		status map[string]string
		want   bool
	}{
		{"mothership: nothing", nil, nil, false},
		{"Sovereign identity alone", map[string]string{"SOVEREIGN_FQDN": "hw.test"}, map[string]string{"registriesYamlActive": "v1"}, false},
		{"after step 03, before step 06", nil, map[string]string{"registriesYamlActive": "v2"}, false},
		{"step 06 running", nil, map[string]string{"registriesYamlActive": "v2", StatusKeySourcesPivoted: "running"}, false},
		{"step 06 failed", nil, map[string]string{"registriesYamlActive": "v2", StatusKeySourcesPivoted: "failed"}, false},
		{"step 06 re-run blanked its row", nil, map[string]string{"registriesYamlActive": "v2", StatusKeySourcesPivoted: ""}, false},
		{"after step 06", nil, usableStatus, true},
		{"after step 06, with whitespace", nil, map[string]string{"registriesYamlActive": " v2\n", StatusKeySourcesPivoted: "success\n"}, true},
		{"step 06 result without the registry pivot", nil, map[string]string{"registriesYamlActive": "v1", StatusKeySourcesPivoted: "success"}, false},
		{"status rollback", nil, map[string]string{"registriesYamlActive": "rollback", StatusKeySourcesPivoted: "success"}, false},
		{"step-07 pin issuer", map[string]string{"CATALYST_PIN_ISSUER": "https://console.hw.test"}, nil, true},
		{"step-07 hand-over issuer", map[string]string{"CATALYST_HANDOVER_JWT_ISSUER": "https://console.hw.test"}, nil, true},
	} {
		if got := OwnSourceUsable(envOf(tc.env), tc.status); got != tc.want {
			t.Errorf("%s: OwnSourceUsable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBaseFromStatus(t *testing.T) {
	env := envOf(map[string]string{"SOVEREIGN_FQDN": "hw.test"})
	if got := Base(env, usableStatus); got != "oci://registry.hw.test/openova-io" {
		t.Errorf("usable by status: %q", got)
	}
	if got := Base(env, map[string]string{"registriesYamlActive": "v2"}); got != GHCRBase {
		t.Errorf("pivoted but step 06 not yet: %q", got)
	}
	if got := Base(env, map[string]string{"registriesYamlActive": "v1"}); got != GHCRBase {
		t.Errorf("not pivoted: %q", got)
	}
	if got := Base(envOf(nil), usableStatus); got != GHCRBase {
		t.Errorf("usable without an fqdn must fail safe to the public catalog: %q", got)
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
