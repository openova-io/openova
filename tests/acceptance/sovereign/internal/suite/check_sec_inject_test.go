package suite

import (
	"context"
	"encoding/base64"
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

const injectCanary = "CANARY-00112233445566778899aabbccddeeff00112233"

// injectFake answers kubectl for check SEC-G08. An exec into the OpenBao pod
// runs the real script of the check with sh and a fake bao that keeps the
// written value in a file; the token of a service account is h.<name>.s.
type injectFake struct {
	t         *testing.T
	pods      string            // running OpenBao server pods
	bao       map[string]string // FAKE_BAO_* environment of the fake bao
	store     string            // the file of the fake bao's value
	file      string            // what the mounted file holds; "" is the value in OpenBao
	secrets   []obj             // the Secrets of the region
	canI      string            // what kubectl auth can-i prints
	notReady  bool              // the reader Pod never becomes Ready
	saFails   bool              // creating a service account is refused
	created   []string          // created namespaces
	deleted   []string          // deleted namespaces
	audiences []string          // <service account>:<audience> of every token asked for
	spc       []obj             // the SecretProviderClasses created
}

func (f *injectFake) kubectl(_ kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	var body []byte
	if stdin != nil {
		body, _ = io.ReadAll(stdin)
	}
	switch {
	case len(args) == 3 && args[0] == "create" && args[1] == "namespace":
		f.created = append(f.created, args[2])
		return nil, nil
	case len(args) == 5 && args[2] == "create" && args[3] == "serviceaccount":
		if f.saFails {
			return nil, errors.New("serviceaccounts is forbidden")
		}
		return nil, nil
	case line == "-n openbao get pods -l app.kubernetes.io/name=openbao,component=server --field-selector=status.phase=Running -o jsonpath={.items[*].metadata.name}":
		return []byte(f.pods), nil
	case len(args) >= 6 && args[2] == "create" && args[3] == "token" && args[5] == "--duration=10m":
		aud := ""
		if len(args) == 7 {
			aud = strings.TrimPrefix(args[6], "--audience=")
		}
		f.audiences = append(f.audiences, args[4]+":"+aud)
		return []byte("h." + args[4] + ".s\n"), nil
	case strings.HasPrefix(line, "-n openbao exec -i ") && strings.HasSuffix(line, " -c openbao -- sh -s"):
		return f.exec(body)
	case line == "create -f -":
		var list struct{ Items []obj }
		if err := json.Unmarshal(body, &list); err != nil || len(list.Items) != 2 {
			f.t.Fatalf("create of no List of two objects: %s", body)
		}
		f.spc = append(f.spc, list.Items[0])
		return nil, nil
	case len(args) == 6 && args[2] == "wait" && args[4] == "pod/sec-g08-reader":
		if f.notReady {
			return nil, errors.New("timed out waiting for the condition on pods/sec-g08-reader")
		}
		return nil, nil
	case len(args) == 9 && args[2] == "exec" && args[3] == "sec-g08-reader" && args[8] == "/var/run/secrets/openova/canary":
		if f.file != "" {
			return []byte(f.file), nil
		}
		b, _ := os.ReadFile(f.store)
		return b, nil
	case line == "get secrets --all-namespaces -o json":
		return []byte(kubeList(f.secrets...)), nil
	case line == "-n kube-system get serviceaccount secrets-store-csi-driver -o name":
		return []byte("serviceaccount/secrets-store-csi-driver\n"), nil
	case line == "auth can-i create secrets --as=system:serviceaccount:kube-system:secrets-store-csi-driver --all-namespaces":
		if f.canI == "no" {
			return []byte("no\n"), errors.New("exit status 1")
		}
		return []byte(f.canI + "\n"), nil
	case len(args) == 5 && args[0] == "delete" && args[1] == "namespace":
		f.deleted = append(f.deleted, args[2])
		return nil, nil
	}
	f.t.Errorf("unexpected kubectl %s", line)
	return nil, errors.New("unexpected kubectl call")
}

// fakeInjectBao is a bao command: login answers s.<service account> for role
// eso-ns as eso-reader and role workload as anyone; put answers FAKE_BAO_PUT
// and writes the value to FAKE_STORE when that is ok or error (an error after
// the write); get answers FAKE_BAO_OTHER for sec-g08-other and FAKE_BAO_OWN
// for sec-g08-reader; metadata delete removes FAKE_STORE.
const fakeInjectBao = `#!/bin/sh
answer() {
	case "$1" in
	denied) printf 'Error: Error making API request.\n\nCode: 403. Errors:\n\n* permission denied\n' >&2; exit 2 ;;
	ok) exit 0 ;;
	*) printf 'Error: connection refused\n' >&2; exit 2 ;;
	esac
}
case "$1 $2" in
"write -field=token")
	[ "$3" = auth/kubernetes/login ] || { echo "bad login $*" >&2; exit 2; }
	[ "$FAKE_BAO_LOGIN" = ok ] || { echo 'Code: 403. permission denied' >&2; exit 2; }
	sa=$(echo "$5" | sed 's/^jwt=h\.//; s/\.s$//')
	case "$4 $sa" in
	"role=eso-ns eso-reader"|"role=workload sec-g08-reader"|"role=workload sec-g08-other") printf 's.%s' "$sa"; exit 0 ;;
	esac
	echo "bad login $4 for $sa" >&2; exit 2 ;;
"kv put")
	[ "$BAO_TOKEN" = s.eso-reader ] && [ "$3" = -mount=mounted ] && [ "$4" = -cas=0 ] || exit 9
	case "$FAKE_BAO_PUT" in ok|error) printf '%s' "${6#value=}" > "$FAKE_STORE" ;; esac
	answer "$FAKE_BAO_PUT" ;;
"kv get")
	case "$BAO_TOKEN" in
	s.sec-g08-other) answer "$FAKE_BAO_OTHER" ;;
	s.sec-g08-reader) answer "$FAKE_BAO_OWN" ;;
	esac
	exit 9 ;;
"kv metadata")
	[ "$BAO_TOKEN" = s.eso-reader ] && [ "$3" = delete ] || exit 9
	rm -f "$FAKE_STORE"; exit 0 ;;
"token revoke")
	exit 0 ;;
esac
echo "unexpected bao $*" >&2
exit 3
`

func (f *injectFake) exec(script []byte) ([]byte, error) {
	bao := filepath.Join(f.t.TempDir(), "bao")
	if err := os.WriteFile(bao, []byte(fakeInjectBao), 0o600); err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader("bao() { sh \"$FAKE_BAO\" \"$@\"; }\n" + string(script))
	cmd.Env = append(os.Environ(), "FAKE_BAO="+bao, "FAKE_STORE="+f.store)
	for k, v := range f.bao {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if strings.Contains(string(out), injectCanary) || strings.Contains(string(out), "s.sec-g08") || strings.Contains(string(out), "h.") {
		f.t.Errorf("the script printed a value or a token: %s", out)
	}
	return out, err
}

func injectSecret(ns, name, value string) obj {
	return obj{"metadata": obj{"namespace": ns, "name": name}, "data": obj{"k": base64.StdEncoding.EncodeToString([]byte(value))}}
}

// goodInject is a region where the driver mounts and syncs nothing.
func goodInject(t *testing.T) *injectFake {
	return &injectFake{t: t, pods: "openbao-0", store: filepath.Join(t.TempDir(), "value"), canI: "no",
		bao:     map[string]string{"FAKE_BAO_LOGIN": "ok", "FAKE_BAO_PUT": "ok", "FAKE_BAO_OTHER": "denied", "FAKE_BAO_OWN": "ok"},
		secrets: []obj{injectSecret("openbao", "openbao-csi-provider-hmac-key", "hmac"), injectSecret("kube-system", "x", "y")}}
}

func runInject(t *testing.T, f *injectFake) ([]string, int) {
	t.Helper()
	c := secInjectCheck{kubectl: f.kubectl, canary: func() string { return injectCanary }}
	r := runCheck(t, "sec-inject", c, &CheckContext{})
	return failedSteps(r), len(r.run.Steps)
}

func TestSecInjectPasses(t *testing.T) {
	f := goodInject(t)
	failed, n := runInject(t, f)
	if len(failed) != 0 || n != 12 {
		t.Fatalf("%d steps (want 6 per region for two regions), failed: %q", n, failed)
	}
	if len(f.created) != 2 || strings.Join(f.created, " ") != strings.Join(f.deleted, " ") || !strings.HasPrefix(f.created[0], "sec-g08-") {
		t.Errorf("namespaces created %q, deleted %q", f.created, f.deleted)
	}
	keyDeleted(t, f)
	if got := strings.Join(f.audiences, " "); got != "eso-reader: sec-g08-other:openbao sec-g08-reader:openbao eso-reader: "+
		"eso-reader: sec-g08-other:openbao sec-g08-reader:openbao eso-reader:" {
		t.Errorf("tokens asked for %q", got)
	}
	spc := f.spc[0]
	if _, ok := spc["spec"].(map[string]any)["secretObjects"]; ok {
		t.Error("the SecretProviderClass sets secretObjects")
	}
	params, _ := json.Marshal(spc["spec"])
	for _, w := range []string{`"provider":"openbao"`, `"roleName":"workload"`, `"audience":"openbao"`, `"openbaoAddress":"http://openbao.openbao.svc.cluster.local:8200"`,
		`secretPath: mounted/data/sec-g08-`} {
		if !strings.Contains(string(params), w) {
			t.Errorf("the SecretProviderClass lacks %s: %s", w, params)
		}
	}
}

func TestSecInjectFails(t *testing.T) {
	cases := []struct {
		name   string
		change func(f *injectFake)
		want   []string
	}{
		{"Secret sync copied the value", func(f *injectFake) {
			f.secrets = append(f.secrets, injectSecret("sec-g08-x", "sec-g08-reader", injectCanary))
		}, []string{"the canary is in 1 Secret values: sec-g08-x/sec-g08-reader key k"}},
		{"no Secret listed", func(f *injectFake) { f.secrets = nil }, []string{"nothing was measured"}},
		{"the driver may create Secrets", func(f *injectFake) { f.canI = "yes" }, []string{`printed "yes", not no`}},
		{"another service account reads the value", func(f *injectFake) { f.bao["FAKE_BAO_OTHER"] = "ok" },
			[]string{`get of mounted/data/sec-g08-`, `as sec-g08-`, `/sec-g08-other gave "ok", not "denied"`}},
		{"OpenBao refuses everything", func(f *injectFake) { f.bao["FAKE_BAO_OWN"] = "denied" },
			[]string{`/sec-g08-reader gave "denied", not "ok"`}},
		{"OpenBao cannot be read", func(f *injectFake) { f.bao["FAKE_BAO_OTHER"] = "error" }, []string{`gave "error", not "denied"`}},
		{"the mounted file is something else", func(f *injectFake) { f.file = "CANARY-other" }, []string{"12 bytes that are not the canary"}},
		{"the reader Pod never starts", func(f *injectFake) { f.notReady = true }, []string{"the reader Pod is not Ready"}},
		{"the birth is refused", func(f *injectFake) { f.bao["FAKE_BAO_PUT"] = "denied" }, []string{`put of mounted/data/sec-g08-`, `gave "denied", not "ok"`}},
		{"the birth answers an error after the write", func(f *injectFake) { f.bao["FAKE_BAO_PUT"] = "error" },
			[]string{`put of mounted/data/sec-g08-`, `gave "error", not "ok"`}},
		{"a service account cannot be created", func(f *injectFake) { f.saFails = true }, []string{"serviceaccounts is forbidden"}},
		{"the login fails", func(f *injectFake) { f.bao["FAKE_BAO_LOGIN"] = "no" }, []string{"could not log in to OpenBao with role eso-ns"}},
		{"no OpenBao pod", func(f *injectFake) { f.pods = "" }, []string{"no running OpenBao server pod in namespace openbao"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := goodInject(t)
			tc.change(f)
			failed, _ := runInject(t, f)
			if len(failed) == 0 {
				t.Fatal("the check passed")
			}
			all := strings.Join(failed, "\n")
			for _, w := range tc.want {
				if !strings.Contains(all, w) {
					t.Errorf("no failed step says %q; failed: %q", w, failed)
				}
			}
			if len(f.created) != 2 || strings.Join(f.created, " ") != strings.Join(f.deleted, " ") {
				t.Errorf("namespaces created %q, deleted %q", f.created, f.deleted)
			}
			keyDeleted(t, f)
		})
	}
}

// keyDeleted fails when the fake OpenBao still holds the canary's key: a
// put that was sent left the key behind.
func keyDeleted(t *testing.T, f *injectFake) {
	t.Helper()
	if _, err := os.Stat(f.store); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the canary's key was not deleted (stat: %v)", err)
	}
}

func TestSecInjectRegistered(t *testing.T) {
	for _, c := range RegisteredChecks() {
		if c.ID() == secInjectID {
			return
		}
	}
	t.Fatal("SEC-G08 is not registered")
}
