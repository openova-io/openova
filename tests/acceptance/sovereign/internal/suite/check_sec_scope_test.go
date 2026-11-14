package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// scopeFake answers kubectl for check SEC-G07. A dry run is judged by the
// rule of remote-key-scope-policy.yaml, written out in Go for the fixtures
// (legacy is the legacy map; a nil map is a cluster without the rule). An
// exec runs the real script of the check with sh, with a fake bao
// that answers as the scenario says.
type scopeFake struct {
	t        *testing.T
	legacy   map[string][]string
	denyAll  bool                // the rule refuses every request
	objects  []obj               // ExternalSecrets and PushSecrets of the region
	orgs     string              // the Organization namespaces, as jsonpath prints them
	pods     string              // running OpenBao server pods
	bao      map[string]string   // FAKE_BAO_* environment of the fake bao
	applied  []string            // namespace/name of every dry-run update
	tokenFor []string            // namespace/name of every token asked for
	created  map[string][]string // dry-run creates: namespace -> keys
}

const scopeJWT = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"

func (f *scopeFake) judge(ns, key string) error {
	msg := errors.New(`The externalsecrets "x" is invalid: : ValidatingAdmissionPolicy 'external-secret-remote-key-scope' with binding 'external-secret-remote-key-scope' denied request: remote key must start with ...`)
	if f.denyAll {
		return msg
	}
	if f.legacy == nil || strings.HasPrefix(key, "openova/apps/"+ns+"/") || strings.HasPrefix(key, ns+"/") {
		return nil
	}
	for _, p := range f.legacy[ns] {
		if strings.HasPrefix(key, p) {
			return nil
		}
	}
	return msg
}

func (f *scopeFake) kubectl(_ kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	var body []byte
	if stdin != nil {
		body, _ = io.ReadAll(stdin)
	}
	switch {
	case line == "create --dry-run=server -f -":
		var o kube.Object
		if err := json.Unmarshal(body, &o); err != nil {
			f.t.Fatalf("dry run of no JSON: %s", body)
		}
		key := o.Objects("spec", "data")[0].Str("remoteRef", "key")
		if f.created == nil {
			f.created = map[string][]string{}
		}
		f.created[o.Namespace()] = append(f.created[o.Namespace()], key)
		if o.Str("spec", "secretStoreRef", "name") != "vault-region1" || o.Name() != "sec-g07-probe" {
			f.t.Errorf("the probe names another store or object: %s", body)
		}
		return nil, f.judge(o.Namespace(), key)
	case line == "get namespaces -l openova.io/organization -o jsonpath={.items[*].metadata.name}":
		return []byte(f.orgs), nil
	case line == "get externalsecrets.external-secrets.io,pushsecrets.external-secrets.io --all-namespaces -o json":
		return []byte(kubeList(f.objects...)), nil
	case line == "apply --server-side --dry-run=server --force-conflicts --field-manager=sec-g07 -f -":
		var o kube.Object
		if err := json.Unmarshal(body, &o); err != nil {
			f.t.Fatalf("dry-run update of no JSON: %s", body)
		}
		f.applied = append(f.applied, o.Namespace()+"/"+o.Name())
		for _, d := range o.Objects("spec", "data") {
			key := d.Str("remoteRef", "key")
			if key == "" {
				key = d.Str("match", "remoteRef", "remoteKey")
			}
			if err := f.judge(o.Namespace(), key); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case line == "get clustersecretstore.external-secrets.io vault-region1 -o json":
		return []byte(`{"spec": {"provider": {"vault": {"path": "secret", "auth": {"kubernetes": {"mountPath": "kubernetes",
			"role": "external-secrets", "serviceAccountRef": {"name": "external-secrets", "namespace": "external-secrets-system"}}}}}}}`), nil
	case line == "-n openbao get pods -l app.kubernetes.io/name=openbao,component=server --field-selector=status.phase=Running -o jsonpath={.items[*].metadata.name}":
		return []byte(f.pods), nil
	case strings.HasPrefix(line, "-n ") && strings.Contains(line, " create token "):
		f.tokenFor = append(f.tokenFor, args[1]+"/"+args[4])
		return []byte(scopeJWT + "\n"), nil
	case strings.HasPrefix(line, "-n openbao exec -i ") && strings.HasSuffix(line, " -c openbao -- sh -s"):
		return f.exec(body)
	}
	f.t.Errorf("unexpected kubectl %s", line)
	return nil, errors.New("unexpected kubectl call")
}

// fakeBao is a bao command that answers from FAKE_BAO_LOGIN, FAKE_BAO_ARCHIVE
// and FAKE_BAO_SSO, and checks the token and the jwt it is given.
const fakeBao = `#!/bin/sh
answer() {
	case "$1" in
	denied) printf 'Error reading %s: Error making API request.\n\nCode: 403. Errors:\n\n* permission denied\n' "$2" >&2; exit 2 ;;
	absent) printf 'No value found at %s\n' "$2" >&2; exit 2 ;;
	read) printf 'Key Value\nvalue SECRET-VALUE\n'; exit 0 ;;
	*) printf 'Error reading %s: connection refused\n' "$2" >&2; exit 2 ;;
	esac
}
case "$1 $2" in
"write -field=token")
	[ "$3" = auth/kubernetes/login ] && [ "$4" = role=external-secrets ] && [ "$5" = "jwt=$FAKE_JWT" ] || { echo "bad login $*" >&2; exit 2; }
	[ "$FAKE_BAO_LOGIN" = ok ] || { echo 'Code: 403. permission denied' >&2; exit 2; }
	printf 's.token'; exit 0 ;;
"read secret/data/catalyst/tofu-phase0-archive")
	[ "$BAO_TOKEN" = s.token ] || exit 9
	answer "$FAKE_BAO_ARCHIVE" "$2" ;;
"read secret/data/sso/sec-g07-probe")
	[ "$BAO_TOKEN" = s.token ] || exit 9
	answer "$FAKE_BAO_SSO" "$2" ;;
"token revoke")
	exit 0 ;;
esac
echo "unexpected bao $*" >&2
exit 3
`

func (f *scopeFake) exec(script []byte) ([]byte, error) {
	bao := filepath.Join(f.t.TempDir(), "bao")
	if err := os.WriteFile(bao, []byte(fakeBao), 0o600); err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	// The shell function bao stands in for the command bao of the pod.
	cmd.Stdin = strings.NewReader("bao() { sh \"$FAKE_BAO\" \"$@\"; }\n" + string(script))
	cmd.Env = append(os.Environ(), "FAKE_BAO="+bao, "FAKE_JWT="+scopeJWT)
	for k, v := range f.bao {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if strings.Contains(string(out), "SECRET-VALUE") || strings.Contains(string(out), scopeJWT) || strings.Contains(string(out), "s.token") {
		f.t.Errorf("the script printed a value or a token: %s", out)
	}
	return out, err
}

func externalSecret(ns, name string, keys ...string) obj {
	data := []obj{}
	for _, k := range keys {
		data = append(data, obj{"secretKey": "k", "remoteRef": obj{"key": k}})
	}
	return obj{"apiVersion": "external-secrets.io/v1beta1", "kind": "ExternalSecret", "metadata": kubeMeta(ns, name),
		"spec": obj{"secretStoreRef": obj{"kind": "ClusterSecretStore", "name": "vault-region1"}, "data": data}}
}

func pushSecret(ns, name, key string) obj {
	return obj{"apiVersion": "external-secrets.io/v1alpha1", "kind": "PushSecret", "metadata": kubeMeta(ns, name),
		"spec": obj{"data": []obj{{"match": obj{"secretKey": "k", "remoteRef": obj{"remoteKey": key}}}}}}
}

// goodScope is a region where the rule and the policy are in place.
func goodScope(t *testing.T) *scopeFake {
	return &scopeFake{t: t,
		legacy: map[string][]string{"gitea": {"sso/"}, "newapi": {"catalyst/", "sso/"}},
		objects: []obj{
			externalSecret("gitea", "gitea-oauth-source-credentials", "sso/gitea"),
			externalSecret("newapi", "catalyst-newapi-admin-token", "catalyst/newapi/admin-token"),
			pushSecret("newapi", "catalyst-newapi-admin-token-push", "catalyst/newapi/admin-token"),
			externalSecret("wp", "db", "openova/apps/wp/db/db-owner"),
			externalSecret("acme", "agenity-anthropic", "catalyst/anthropic/token"),
		},
		orgs: "acme other", pods: "openbao-0 openbao-1",
		bao: map[string]string{"FAKE_BAO_LOGIN": "ok", "FAKE_BAO_ARCHIVE": "denied", "FAKE_BAO_SSO": "absent"},
	}
}

func runScope(t *testing.T, f *scopeFake) []string {
	t.Helper()
	r := runCheck(t, "sec-scope", secScopeCheck{kubectl: f.kubectl}, &CheckContext{})
	if n := len(r.run.Steps); n != 10 {
		t.Fatalf("%d steps, want 5 per region for two regions", n)
	}
	return failedSteps(r)
}

func TestSecScopePasses(t *testing.T) {
	f := goodScope(t)
	if failed := runScope(t, f); len(failed) != 0 {
		t.Fatalf("a region with the rule and the policy failed: %q", failed)
	}
	if got := strings.Join(f.applied, " "); got != "gitea/gitea-oauth-source-credentials newapi/catalyst-newapi-admin-token newapi/catalyst-newapi-admin-token-push wp/db "+
		"gitea/gitea-oauth-source-credentials newapi/catalyst-newapi-admin-token newapi/catalyst-newapi-admin-token-push wp/db" {
		t.Errorf("replayed %q: the Organization namespace acme must be left out", got)
	}
	if got := strings.Join(f.tokenFor, " "); got != "external-secrets-system/external-secrets external-secrets-system/external-secrets" {
		t.Errorf("tokens asked for %q", got)
	}
	if got := strings.Join(f.created["default"], " ") + " | " + strings.Join(f.created["gitea"], " "); got !=
		"sso/sec-g07-probe sso/sec-g07-probe | catalyst/tofu-phase0-archive sso/sec-g07-probe catalyst/tofu-phase0-archive sso/sec-g07-probe" {
		t.Errorf("dry runs %q", got)
	}
}

func TestSecScopeFails(t *testing.T) {
	cases := []struct {
		name   string
		change func(f *scopeFake)
		want   []string
	}{
		{"no rule", func(f *scopeFake) { f.legacy = nil },
			[]string{"admitted an ExternalSecret in namespace default with remote key sso/sec-g07-probe",
				"admitted an ExternalSecret in namespace gitea with remote key catalyst/tofu-phase0-archive"}},
		{"the rule refuses everything", func(f *scopeFake) { f.denyAll = true },
			[]string{"external-secret-remote-key-scope' with binding", "would be refused on their next update"}},
		{"an existing object outside the map", func(f *scopeFake) {
			f.objects = append(f.objects, externalSecret("harbor", "harbor-sso-oidc-credentials", "sso/harbor"))
		}, []string{"1 of 5 objects would be refused on their next update: ExternalSecret harbor/harbor-sso-oidc-credentials"}},
		{"only Organization objects", func(f *scopeFake) {
			f.objects = []obj{externalSecret("acme", "agenity-anthropic", "catalyst/anthropic/token")}
		}, []string{"nothing was measured"}},
		{"the archive is readable", func(f *scopeFake) { f.bao["FAKE_BAO_ARCHIVE"] = "read" }, []string{`tofu-phase0-archive gave "read", not a 403`}},
		{"the archive is absent but not denied", func(f *scopeFake) { f.bao["FAKE_BAO_ARCHIVE"] = "absent" }, []string{`gave "absent", not a 403`}},
		{"sso is denied", func(f *scopeFake) { f.bao["FAKE_BAO_SSO"] = "denied" }, []string{`sso/sec-g07-probe gave "denied"`}},
		{"OpenBao cannot be read", func(f *scopeFake) { f.bao["FAKE_BAO_SSO"] = "error" }, []string{`sso/sec-g07-probe gave "error"`}},
		{"the login fails", func(f *scopeFake) { f.bao["FAKE_BAO_LOGIN"] = "no" }, []string{"could not log in to OpenBao with role external-secrets on mount kubernetes"}},
		{"no OpenBao pod", func(f *scopeFake) { f.pods = "" }, []string{"no running OpenBao server pod in namespace openbao"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := goodScope(t)
			tc.change(f)
			failed := runScope(t, f)
			if len(failed) == 0 {
				t.Fatal("the check passed")
			}
			all := strings.Join(failed, "\n")
			for _, w := range tc.want {
				if !strings.Contains(all, w) {
					t.Errorf("no failed step says %q; failed: %q", w, failed)
				}
			}
		})
	}
}

func TestSecScopeRegistered(t *testing.T) {
	for _, c := range RegisteredChecks() {
		if c.ID() == secScopeID {
			return
		}
	}
	t.Fatal("SEC-G07 is not registered")
}
