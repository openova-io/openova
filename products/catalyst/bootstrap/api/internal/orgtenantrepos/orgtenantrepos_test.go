package orgtenantrepos

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// usableStatus is the cutover status once step 06 has succeeded.
var usableStatus = map[string]string{StatusKeyRegistries: "v2", StatusKeySourcesPivoted: "success"}

// stripLookup is a PublicAuthFunc: held says whether the blocks' Secret still
// holds the public catalog's auth (step 06's Phase 3b has not stripped it);
// calls counts the lookups.
func stripLookup(held bool, err error, calls *int) PublicAuthFunc {
	return func(context.Context) (bool, error) { *calls++; return held, err }
}

// The Sovereign's own source is used once step 06 wrote the credential and the
// trust: the registry pivot of step 03 alone is not enough (on main it was, so
// an Organization written in between got a source that cannot work). A step-06
// row that is not "success" does not mean the step has not got there: once its
// Phase 3b stripped the public catalog's auth, the own source is the only one
// that works, also when the step failed after the strip or a re-run blanked the
// row (the public catalog there fails "no auth config for 'ghcr.io'" on every
// shared block).
func TestOwnSourceUsable(t *testing.T) {
	v2 := func(row string) map[string]string {
		return map[string]string{StatusKeyRegistries: "v2", StatusKeySourcesPivoted: row}
	}
	for _, tc := range []struct {
		name      string
		env       map[string]string
		status    map[string]string
		held      bool  // the Secret still holds the public catalog's auth
		lookupErr error // the lookup fails
		lookups   int   // lookups of the strip expected
		want      bool
	}{
		{"mothership: nothing", nil, nil, true, nil, 0, false},
		{"Sovereign identity alone", map[string]string{"SOVEREIGN_FQDN": "hw.test"}, map[string]string{"registriesYamlActive": "v1"}, true, nil, 0, false},
		{"after step 03, before step 06", nil, map[string]string{"registriesYamlActive": "v2"}, true, nil, 1, false},
		{"step 06 running, before its strip", nil, v2("running"), true, nil, 1, false},
		{"step 06 failed before its strip", nil, v2("failed"), true, nil, 1, false},
		{"step 06 failed after its strip (a secondary not reached)", nil, v2("failed"), false, nil, 1, true},
		{"step 06 re-run blanked its row after an earlier strip", nil, v2(""), false, nil, 1, true},
		{"step 06 running again after an earlier strip (engine restart)", nil, v2("running"), false, nil, 1, true},
		{"the strip is unreadable", nil, v2("failed"), true, errors.New("forbidden"), 1, true},
		{"after step 06", nil, usableStatus, true, nil, 0, true},
		{"after step 06, with whitespace", nil, map[string]string{"registriesYamlActive": " v2\n", StatusKeySourcesPivoted: "success\n"}, true, nil, 0, true},
		{"step 06 result without the registry pivot", nil, map[string]string{"registriesYamlActive": "v1", StatusKeySourcesPivoted: "success"}, false, nil, 0, false},
		{"status rollback", nil, map[string]string{"registriesYamlActive": "rollback"}, false, nil, 0, false},
		{"step-07 pin issuer", map[string]string{"CATALYST_PIN_ISSUER": "https://console.hw.test"}, nil, true, nil, 0, true},
		{"step-07 hand-over issuer", map[string]string{"CATALYST_HANDOVER_JWT_ISSUER": "https://console.hw.test"}, v2(""), true, nil, 0, true},
	} {
		calls := 0
		var logged bytes.Buffer
		got := OwnSourceUsable(context.Background(), envOf(tc.env), tc.status, stripLookup(tc.held, tc.lookupErr, &calls), slog.New(slog.NewTextHandler(&logged, nil)))
		if got != tc.want || calls != tc.lookups {
			t.Errorf("%s: OwnSourceUsable = %v after %d lookups of the strip, want %v after %d", tc.name, got, calls, tc.want, tc.lookups)
		}
		if tc.lookupErr != nil && !strings.Contains(logged.String(), tc.lookupErr.Error()) {
			t.Errorf("%s: the lookup error was not logged: %q", tc.name, logged.String())
		}
	}
}

func TestBase(t *testing.T) {
	env := envOf(map[string]string{"SOVEREIGN_FQDN": "hw.test"})
	if got := Base(env, true); got != "oci://registry.hw.test/openova-io" {
		t.Errorf("usable: %q", got)
	}
	if got := Base(env, false); got != GHCRBase {
		t.Errorf("not usable: %q", got)
	}
	if got := Base(envOf(nil), true); got != GHCRBase {
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
