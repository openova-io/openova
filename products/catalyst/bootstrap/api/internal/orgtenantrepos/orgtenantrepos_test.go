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

// testHost is the registry host of SOVEREIGN_FQDN hw.test, the auths key step
// 06's Phase 0 adds.
const testHost = "registry.hw.test"

// evidence is what step 06's first phases left in the cluster, as the two
// lookups see it: the registry's credential in ghcr-pull (Phase 0) and the
// trust Secret (Phase 0.5).
type evidence struct {
	cred     bool
	credErr  error
	trust    string
	trustErr error
}

// counted returns the two lookups of e; creds and trusts count the calls, and a
// credential looked up for another host than testHost fails the test.
func (e evidence) counted(t *testing.T, creds, trusts *int) (PullAuthFunc, TrustFunc) {
	t.Helper()
	pull := func(_ context.Context, host string) (bool, error) {
		*creds++
		if host != testHost {
			t.Errorf("the credential was looked up for %q, want %q", host, testHost)
		}
		return e.cred, e.credErr
	}
	trust := func(context.Context) (string, error) {
		*trusts++
		if e.trustErr != nil {
			return TrustSecretName, e.trustErr
		}
		return e.trust, nil
	}
	return pull, trust
}

// The Sovereign's own source is used once it can be used: the registry pivot of
// step 03, the registry's credential in ghcr-pull (step 06 Phase 0) and the
// trust Secret (Phase 0.5). Both phases run before Phase 2b rewrites the file
// to the Sovereign's registry and before the read-back ahead of the Phase 3b
// strip, so an Organization written while step 06 runs, after Phase 0.5 and
// with the ghcr.io auth still held, gets the own source (on the previous head
// it got the public catalog, undid Phase 2b in Git and failed the read-back).
// The step-06 row is not read, and a read error is no evidence.
func TestOwnSourceUsable(t *testing.T) {
	fqdn := map[string]string{"SOVEREIGN_FQDN": "hw.test"}
	v2 := func(row string) map[string]string {
		return map[string]string{StatusKeyRegistries: "v2", "step.helmrepository-patches.result": row}
	}
	both := evidence{cred: true, trust: TrustSecretName}
	for _, tc := range []struct {
		name           string
		env            map[string]string
		status         map[string]string
		ev             evidence
		creds, trusts  int // lookups expected
		want, wantLogs bool
	}{
		{"mothership: nothing", nil, nil, both, 0, 0, false, false},
		{"Sovereign identity alone", fqdn, map[string]string{StatusKeyRegistries: "v1"}, both, 0, 0, false, false},
		{"after step 03, before Phase 0 (no credential yet)", fqdn, v2("running"), evidence{trust: TrustSecretName}, 1, 0, false, false},
		{"after Phase 0, before Phase 0.5 (no trust yet)", fqdn, v2("running"), evidence{cred: true}, 1, 1, false, false},
		{"after Phase 0.5 while step 06 runs, before or after Phase 2b", fqdn, v2("running"), both, 1, 1, true, false},
		{"step 06 failed after Phase 0.5", fqdn, v2("failed"), both, 1, 1, true, false},
		{"a re-run blanked step 06's row after an earlier Phase 0.5", fqdn, v2(""), both, 1, 1, true, false},
		{"after step 06", fqdn, v2("success"), both, 1, 1, true, false},
		{"after step 06, with whitespace", fqdn, map[string]string{StatusKeyRegistries: " v2\n"}, both, 1, 1, true, false},
		{"ghcr-pull unreadable", fqdn, v2("running"), evidence{credErr: errors.New("forbidden"), trust: TrustSecretName}, 1, 0, false, true},
		{"the trust Secret unreadable", fqdn, v2("running"), evidence{cred: true, trustErr: errors.New("the apiserver is unreachable")}, 1, 1, false, true},
		{"the evidence without the registry pivot", fqdn, map[string]string{StatusKeyRegistries: "v1", "step.helmrepository-patches.result": "success"}, both, 0, 0, false, false},
		{"status rollback", fqdn, map[string]string{StatusKeyRegistries: "rollback"}, both, 0, 0, false, false},
		{"pivoted without an fqdn", nil, v2("running"), both, 0, 0, false, false},
		{"step-07 pin issuer", map[string]string{"CATALYST_PIN_ISSUER": "https://console.hw.test"}, nil, evidence{}, 0, 0, true, false},
		{"step-07 hand-over issuer", map[string]string{"CATALYST_HANDOVER_JWT_ISSUER": "https://console.hw.test"}, v2(""), evidence{}, 0, 0, true, false},
	} {
		creds, trusts := 0, 0
		pull, trust := tc.ev.counted(t, &creds, &trusts)
		var logged bytes.Buffer
		got := OwnSourceUsable(context.Background(), envOf(tc.env), tc.status, pull, trust, slog.New(slog.NewTextHandler(&logged, nil)))
		if got != tc.want || creds != tc.creds || trusts != tc.trusts {
			t.Errorf("%s: OwnSourceUsable = %v after %d credential and %d trust lookups, want %v after %d and %d", tc.name, got, creds, trusts, tc.want, tc.creds, tc.trusts)
		}
		if logs := strings.Contains(logged.String(), "keep the public catalog"); logs != tc.wantLogs {
			t.Errorf("%s: warning logged %v, want %v: %q", tc.name, logs, tc.wantLogs, logged.String())
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
