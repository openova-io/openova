package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// sealFake answers kubectl for SEC-G03 and SEC-G05 the way one healthy
// region does; a test changes one answer to make one fault.
type sealFake struct {
	t                         *testing.T
	probe                     string // what the seal probe prints on a node
	secrets, jobs             string // names of `get secret ... -o name` and `get jobs -o name`
	status, restarted         string // bao status before and after the restart
	genRoot, read, waitErr    error  // errors of bao write, bao read and kubectl wait
	hcl, args, logs           string // audit.hcl, the server args, the container log
	created, deleted, deletes []string
	restartedPod              bool
}

const sealStatusOK = `{"type":"static","initialized":true,"sealed":false,"n":0}`

func newSealFake(t *testing.T) *sealFake {
	return &sealFake{t: t,
		probe:     "seal_file=400:100:1000:32 seal_files=1 seal_dir=700:100:1000 seal_dev=64770 root_dev=64769",
		status:    sealStatusOK,
		restarted: sealStatusOK,
		genRoot:   errors.New("Error writing data to " + secGenerateRoot + ": Error making API request.\n\nCode: 405. Errors:\n\n* unsupported operation"),
		read:      errors.New("Error reading secret/data/x: Error making API request.\n\nCode: 403. Errors:\n\n* permission denied"),
		hcl:       "audit \"file\" \"stdout\" {\n  description = \"Audit to standard output; collected by the log pipeline.\"\n  options {\n    file_path = \"stdout\"\n  }\n}\n",
		args:      "/usr/local/bin/docker-entrypoint.sh bao server -config=/tmp/storageconfig.hcl -config=/openbao/userconfig/openbao-seal-config/",
	}
}

// auditOf is an audit response line of path, as the stdout device writes it.
func auditOf(path, id string) string {
	return fmt.Sprintf(`{"time":"2026-11-20T03:24:25Z","type":"response","auth":{},"request":{"id":%q,"operation":"read","path":%q},"error":"1 error occurred:\n\t* permission denied\n\n"}`, id, path)
}

func (f *sealFake) kubectl(_ kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "get nodes"):
		return []byte("cp-1"), nil
	case strings.HasPrefix(line, "create namespace "):
		f.created = append(f.created, args[2])
	case strings.HasSuffix(line, " create -f -"):
		var o kube.Object
		b, _ := io.ReadAll(stdin)
		if err := json.Unmarshal(b, &o); err != nil {
			f.t.Fatalf("the probe Pod is not JSON: %v", err)
		}
		c := o.Objects("spec", "containers")[0]
		m := c.Objects("volumeMounts")
		if c.Int("securityContext", "runAsUser") != 100 || c.Int("securityContext", "runAsGroup") != 1000 || len(m) != 1 ||
			m[0].Get("readOnly") != true || m[0].Str("mountPath") != "/host/var/lib/catalyst/seal" {
			f.t.Errorf("the seal probe must run as 100:1000 with only the seal directory, read-only: %s", b)
		}
	case strings.Contains(line, " wait --for=condition=Ready pod --all"):
		return nil, nil
	case strings.Contains(line, " sh -s -- /host seal"):
		return []byte(f.probe), nil
	case strings.HasPrefix(line, "delete namespace "):
		f.deleted = append(f.deleted, args[2])
	case strings.Contains(line, " get secret "+strings.Join(secSealSecrets, " ")+" --ignore-not-found -o name"):
		return []byte(f.secrets), nil
	case strings.HasSuffix(line, " get jobs -o name"):
		return []byte(f.jobs), nil
	case strings.HasSuffix(line, " bao status -format=json"):
		s := f.status
		if f.restartedPod {
			s = f.restarted
		}
		if strings.Contains(s, `"sealed":true`) {
			return []byte(s), errors.New("exit status 2")
		}
		return []byte(s), nil
	case strings.HasSuffix(line, " bao write -f "+secGenerateRoot):
		return []byte(`{"started":false}`), f.genRoot
	case strings.HasSuffix(line, " delete pod/openbao-0 --wait=false"):
		f.deletes = append(f.deletes, line)
		f.restartedPod = true
	case strings.Contains(line, " wait --for="):
		if !strings.Contains(line, " pod/openbao-0 --timeout=") {
			f.t.Errorf("a wait without its timeout: %s", line)
		}
		return nil, f.waitErr
	case strings.Contains(line, " get configmap openbao-seal-config "):
		return []byte(f.hcl), nil
	case strings.Contains(line, " get pod openbao-0 -o jsonpath={.spec.containers[?(@.name==\"openbao\")].args[0]}"):
		return []byte(f.args), nil
	case strings.Contains(line, " bao read secret/data/sec-g05-"):
		return nil, f.read
	case strings.HasSuffix(line, " logs openbao-0 -c openbao --since=60s"):
		return []byte(f.logs), nil
	default:
		f.t.Errorf("unexpected kubectl %s", line)
		return nil, errors.New("unexpected call")
	}
	return nil, nil
}

// sealRun runs c on two regions the way the runner does and returns the run.
func sealRun(t *testing.T, c Check, f *sealFake, want string) *runner {
	t.Helper()
	cc := &CheckContext{Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c}, cc, true)
	if res := r.run.Checks[0]; (want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want) {
		t.Errorf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want)
	}
	if strings.Join(f.created, " ") != strings.Join(f.deleted, " ") {
		t.Errorf("namespaces created %v, deleted %v", f.created, f.deleted)
	}
	return r
}

func TestSecSealPasses(t *testing.T) {
	f := newSealFake(t)
	r := sealRun(t, secSealCheck{kubectl: f.kubectl}, f, "")
	if len(r.run.Steps) != 14 || len(f.created) != 2 || len(f.deletes) != 2 {
		t.Errorf("want 14 steps, two probe namespaces and one pod delete per region; got %d steps, %v, %v", len(r.run.Steps), f.created, f.deletes)
	}
	ids := map[string]bool{}
	for _, s := range r.run.Steps {
		if ids[s.ID] {
			t.Errorf("step %s is recorded twice", s.ID)
		}
		ids[s.ID] = true
	}
	if ev := r.run.Steps[1].Evidence; ev["own_mount"] != true {
		t.Errorf("the probe evidence must say whether the seal directory is a mount of its own: %v", ev)
	}
}

func TestSecSealFails(t *testing.T) {
	for name, c := range map[string]struct {
		change func(f *sealFake)
		want   string
	}{
		"key file readable": {func(f *sealFake) { f.probe = strings.Replace(f.probe, "400:", "440:", 1) }, "a key file is 440:100:1000:32, want 400:100:1000:32"},
		"key file of root":  {func(f *sealFake) { f.probe = strings.Replace(f.probe, ":100:1000:32", ":0:0:32", 1) }, "a key file is 400:0:0:32"},
		"short key":         {func(f *sealFake) { f.probe = strings.Replace(f.probe, ":32 ", ":31 ", 1) }, "a key file is 400:100:1000:31"},
		"no key file":       {func(f *sealFake) { f.probe = "seal_files=0 seal_dir=700:100:1000" }, "no key file openbao-seal-*.key"},
		"probe broke":       {func(f *sealFake) { f.probe = "" }, "the probe did not finish"},
		"unseal Secret":     {func(f *sealFake) { f.secrets = "secret/" + secSealSecrets[0] + "\n" }, "Secrets exist: secret/openbao-unseal-"},
		"a Job":             {func(f *sealFake) { f.jobs = "job.batch/openbao-init\n" }, "Jobs exist in namespace openbao: job.batch/openbao-init"},
		"shamir":            {func(f *sealFake) { f.status = strings.Replace(sealStatusOK, "static", "shamir", 1) }, `the seal type is "shamir"`},
		"sealed":            {func(f *sealFake) { f.status = strings.Replace(sealStatusOK, `"sealed":false`, `"sealed":true`, 1) }, "OpenBao is sealed"},
		"not initialised": {func(f *sealFake) {
			f.status = strings.Replace(sealStatusOK, `"initialized":true`, `"initialized":false`, 1)
		}, "OpenBao is not initialised"},
		"root attempt": {func(f *sealFake) { f.genRoot = nil }, "a token-free PUT sys/generate-"},
		"no answer":    {func(f *sealFake) { f.genRoot = errors.New("error dialing backend") }, "the attempt got no answer from OpenBao"},
		"slow restart": {func(f *sealFake) { f.waitErr = errors.New("timed out waiting for the condition") }, "timed out waiting for the condition"},
		"sealed after": {func(f *sealFake) { f.restarted = strings.Replace(sealStatusOK, `"sealed":false`, `"sealed":true`, 1) }, "the new pod is Ready but sealed"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSealFake(t)
			c.change(f)
			sealRun(t, secSealCheck{kubectl: f.kubectl}, f, c.want)
		})
	}
}

func TestSecSealScript(t *testing.T) {
	root := t.TempDir()
	hostFile(t, root, "var/lib/catalyst/seal/openbao-seal-3f9a1c0b7d2e4a61.key", 0o400, make([]byte, 32))
	hostFile(t, root, "var/lib/catalyst/seal/README", 0o644, nil)
	cmd := exec.Command("sh")
	cmd.Args = append(cmd.Args, "-s", "--", root, "seal")
	cmd.Stdin = strings.NewReader(secSealScript)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	v, ev := parseProbe(out), Evidence{}
	owner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if got := v["seal_file"]; len(got) != 1 || got[0] != "400:"+owner+":32" || v.one("seal_files") != "1" || v.one("seal_dir") != "750:"+owner {
		t.Errorf("the script printed %q", out)
	}
	if err := judgeSeal(v, ev); (err == nil) != (owner == "100:1000") {
		t.Errorf("judged %v for owner %s", err, owner)
	}
}

func TestSecAudit(t *testing.T) {
	const run = "20261120T032425Z-ab12"
	path := "secret/data/sec-g05-" + strings.ToLower(run)
	for name, c := range map[string]struct {
		change func(f *sealFake)
		want   string
	}{
		"audited":            {func(f *sealFake) {}, ""},
		"not in the log":     {func(f *sealFake) { f.logs = "2026-11-20T03:24:25Z [INFO] core: vault is unsealed\n" }, "no audit response of " + path},
		"only the request":   {func(f *sealFake) { f.logs = strings.ReplaceAll(f.logs, `"type":"response"`, `"type":"request"`) }, "no audit response of " + path},
		"read answered":      {func(f *sealFake) { f.read = nil }, "a token-free read of " + path + " was answered"},
		"no answer":          {func(f *sealFake) { f.read = errors.New("container not found") }, "got no 403 from OpenBao"},
		"no device declared": {func(f *sealFake) { f.hcl = "" }, "declares no file audit device stdout/"},
		"to a file": {func(f *sealFake) {
			f.hcl = strings.Replace(f.hcl, `file_path = "stdout"`, `file_path = "/openbao/audit.log"`, 1)
		}, "declares no file audit device stdout/"},
		"not loaded": {func(f *sealFake) { f.args = "bao server -config=/tmp/storageconfig.hcl" }, "the server does not load /openbao/userconfig/openbao-seal-config/"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSealFake(t)
			f.logs = "2026-11-20T03:24:24Z [INFO] core: post-unseal setup complete\n" + auditOf("secret/data/other", "x-1") + "\n" + auditOf(path, "f0d33990-4a80") + "\n"
			c.change(f)
			r := sealRun(t, secAuditCheck{kubectl: f.kubectl, run: func() string { return run }}, f, c.want)
			if len(r.run.Steps) != 4 {
				t.Errorf("want 2 steps per region, got %d", len(r.run.Steps))
			}
			if ev := r.run.Steps[1].Evidence; c.want == "" && ev["request_id"] != "f0d33990-4a80" {
				t.Errorf("the request identifier of the audit line is not recorded: %v", ev)
			}
		})
	}
}

func TestSecSealRegistered(t *testing.T) {
	seal, isSeal := registry[secSealID].(secSealCheck)
	audit, isAudit := registry[secAuditID].(secAuditCheck)
	if !isSeal || !isAudit || seal.kubectl == nil || audit.kubectl == nil || audit.run == nil || audit.run() == "" {
		t.Errorf("the registry must hold %s and %s, each with kube.Region.Run", secSealID, secAuditID)
	}
}
