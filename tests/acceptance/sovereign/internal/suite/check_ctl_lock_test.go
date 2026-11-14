package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

const lockApps = "applications.apps.openova.io"

// lockCluster is the kubectl of check CTL-G04 in the tests. It records every call.
type lockCluster struct {
	notServedIn string // the region that does not serve the kind
	apps        string // the live objects of the kind
	create      error  // the answer to the create of the copy
	canI        string // the answer of kubectl auth can-i
	calls       []string
	copies      []string
}

func (f *lockCluster) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	f.calls = append(f.calls, r.ID+": "+strings.Join(args, " "))
	switch {
	case args[0] == "get" && args[1] == lockApps && r.ID == f.notServedIn:
		return nil, errors.New("error: " + notServed + ` "applications"`)
	case args[0] == "get" && args[1] == lockApps:
		return []byte(f.apps), nil
	case args[0] == "get" && args[1] == "deployment":
		return []byte(`{"spec": {"template": {"spec": {"serviceAccountName": "catalyst-api-cutover-driver"}}}}`), nil
	case args[0] == "create" && args[1] == "-f":
		b, _ := io.ReadAll(stdin)
		f.copies = append(f.copies, string(b))
		return nil, f.create
	case args[0] == "auth" && f.canI == "no":
		return []byte("no\n"), errors.New("exit status 1")
	case args[0] == "auth":
		return []byte(f.canI + "\n"), nil
	}
	return nil, nil // create namespace, delete
}

// lockReport is a report of one stage, applications.
func lockReport(action, managers string) stubAnswer {
	return stubAnswer{out: `{"policy": "catalyst-intent-lock", "stages": [{"stage": "applications", "action": "` + action +
		`", "resources": ["` + lockApps + `"], "objects": 1, "nonFluxSpecManagers": ` + managers + `, "auditCount": null}]}`}
}

// lockStub writes a Python script in place of the report script: it answers a
// for the kubeconfig "a" and b for "b".
func lockStub(t *testing.T, root string, a, b stubAnswer) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(ctlLockScript))
	text := fmt.Sprintf("import sys\nout, code = {'a': (%q, %d), 'b': (%q, %d)}[sys.argv[-1]]\nsys.stdout.write(out)\nsys.exit(code)\n",
		a.out, a.code, b.out, b.code)
	if err := errors.Join(os.MkdirAll(filepath.Dir(path), 0o750), os.WriteFile(path, []byte(text), 0o600)); err != nil {
		t.Fatal(err)
	}
}

const lockSource = `{"items": [{"apiVersion": "apps.openova.io/v1", "kind": "Application",
  "metadata": {"name": "site", "namespace": "acme", "uid": "u-1"}, "spec": {"blueprint": "bp-wordpress"}, "status": {"phase": "Ready"}}]}`

var errLockRefused = errors.New(`applications.apps.openova.io "ctl-lock-x" is forbidden: ValidatingAdmissionPolicy 'catalyst-intent-lock' ` +
	`with binding 'catalyst-intent-lock-applications' denied request: desired state of Application is owned by Git`)

func TestCtlLockCheck(t *testing.T) {
	if _, ok := registry[ctlLockID]; !ok || ctlLockID != "CTL-G04" {
		t.Fatalf("check %s is not registered", ctlLockID)
	}
	held, refused := lockReport("deny", "{}"), errLockRefused
	for name, c := range map[string]struct {
		b       stubAnswer // the report of region b; region a reports held, or b too when the name starts with "every"
		f       lockCluster
		creates int    // the creates of a copy that the check must make
		want    string // "" = PASS, else a part of the reason of the FAIL
	}{
		"the lock holds":                        {held, lockCluster{create: refused, canI: "no"}, 2, ""},
		"region b does not serve the kind":      {held, lockCluster{notServedIn: "region-b", create: refused, canI: "no"}, 1, ""},
		"every stage audits":                    {lockReport("audit", "{}"), lockCluster{}, 0, "no stage is at deny in any region"},
		"another field manager writes spec":     {lockReport("deny", `{"catalyst-api": 2}`), lockCluster{}, 0, "region region-b, stage applications: managers other than Flux own spec map[catalyst-api:2], audit count null"},
		"the report of region b is not read":    {stubAnswer{"", 2}, lockCluster{}, 0, "for region region-b: exit status 2"},
		"the report of region b is not JSON":    {stubAnswer{"<html>", 0}, lockCluster{}, 0, "for region region-b printed no report"},
		"the lock is not installed in region b": {stubAnswer{`{"stages": []}`, 0}, lockCluster{}, 0, "region region-b has no binding of the policy catalyst-intent-lock"},
		"the copy is created":                   {held, lockCluster{canI: "no"}, 2, lockApps + " in region region-a: the copy was created; the lock did not refuse it"},
		"the create fails for another reason":   {held, lockCluster{create: errors.New("spec.blueprint: Required value"), canI: "no"}, 2, "the create failed, but not by the lock: spec.blueprint"},
		"no object to copy":                     {held, lockCluster{apps: `{"items": []}`, canI: "no"}, 0, "has an object of it to copy"},
		"the API account may write":             {held, lockCluster{create: refused, canI: "yes"}, 2, "system:serviceaccount:catalyst-system:catalyst-api-cutover-driver may create " + lockApps},
	} {
		root, a := t.TempDir(), held
		if strings.HasPrefix(name, "every") {
			a = c.b
		}
		lockStub(t, root, a, c.b)
		if c.f.apps == "" {
			c.f.apps = lockSource
		}
		r := runCheck(t, "none", ctlLockCheck{kubectl: c.f.kubectl}, &CheckContext{RepoRoot: root})
		got, calls := r.run.Checks[0], strings.Join(c.f.calls, "\n")
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		case len(c.f.copies) != c.creates:
			t.Errorf("%s: want %d creates of a copy, got %s", name, c.creates, calls)
		case c.creates > 0 && strings.Count(calls, ": delete namespace ctl-lock-") != c.creates:
			t.Errorf("%s: the namespace of the copy is not deleted in each region: %s", name, calls)
		case name == "the copy is created" && !strings.Contains(calls, "region-a: delete "+lockApps+" ctl-lock-"):
			t.Errorf("%s: the copy that was let through is not deleted: %s", name, calls)
		case name == "the lock holds" && (!strings.Contains(c.f.copies[0], `"spec":{"blueprint":"bp-wordpress"}`) ||
			!strings.Contains(c.f.copies[0], `"metadata":{"name":"ctl-lock-`) || strings.Contains(c.f.copies[0], "status") ||
			strings.Contains(c.f.copies[0], "acme")):
			// The copy keeps apiVersion, kind and spec, and gets a name and a namespace of the check's own.
			t.Errorf("%s: the copy is not a copy in a namespace of the check's own: %s", name, c.f.copies[0])
		}
	}
}

// The check runs the report script of this repository; its self-test must pass.
func TestCtlLockScriptOfThisRepository(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	cmd := exec.Command("python3")
	cmd.Args = append(cmd.Args, filepath.Join(root, ctlLockScript), "--self-test")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(lastLine(string(out)), "SELF-TEST OK") {
		t.Errorf("%s --self-test: %v\n%s", ctlLockScript, err, out)
	}
}
