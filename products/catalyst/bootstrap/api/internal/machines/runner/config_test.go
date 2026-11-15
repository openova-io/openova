package runner

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

func nodeArgs(task string, extra ...string) []string {
	return append([]string{"--task=" + task, "--mode=node", "--machine=acme/ledger-db-1", "--run=machine-ops/scan-1",
		"--os-family=ubuntu", "--os-version=24.04", "--evidence-endpoint=https://s3.example.org",
		"--evidence-region=us-east-1", "--evidence-bucket=machine-evidence", "--evidence-prefix=acme/ledger-db-1/scan-1/"}, extra...)
}

func TestParseArgsReadsANodeRun(t *testing.T) {
	got, err := ParseArgs(nodeArgs(TaskHardening, "--address=10.0.0.5",
		"--profile-id=xccdf_org.ssgproject.content_profile_cis_level1_server", "--content-release=0.1.82",
		"--disabled-rules="+rulePrefix+"a,"+rulePrefix+"b"))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Task: TaskHardening, Mode: ModeNode,
		Machine: Ref{Namespace: "acme", Name: "ledger-db-1"}, Run: Ref{Namespace: "machine-ops", Name: "scan-1"},
		Address: "10.0.0.5", OSFamily: "ubuntu", OSVersion: "24.04",
		ProfileID: "xccdf_org.ssgproject.content_profile_cis_level1_server", ContentRelease: "0.1.82",
		DisabledRules: []string{rulePrefix + "a", rulePrefix + "b"},
		Evidence: Evidence{Endpoint: "https://s3.example.org", Region: "us-east-1", Bucket: "machine-evidence",
			Prefix: "acme/ledger-db-1/scan-1/"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if _, err := ParseArgs(nodeArgs(TaskInventory)); err != nil {
		t.Fatalf("an inventory run without hardening flags was refused: %v", err)
	}
}

func TestParseArgsRefusesWhatIsNotWellFormed(t *testing.T) {
	replace := func(task, old, new string) []string {
		a := nodeArgs(task)
		for i, s := range a {
			if strings.HasPrefix(s, old+"=") {
				a[i] = old + "=" + new
			}
		}
		return a
	}
	hardening := func(extra ...string) []string {
		return nodeArgs(TaskHardening, append([]string{"--profile-id=xccdf_org.ssgproject.content_profile_p", "--content-release=0.1.82"}, extra...)...)
	}
	cases := map[string]struct {
		args []string
		want string
	}{
		"an unknown task":                 {replace(TaskInventory, "--task", "match"), "--task must be one of"},
		"an unknown mode":                 {replace(TaskInventory, "--mode", "agent"), "--mode must be one of"},
		"a machine without a namespace":   {replace(TaskInventory, "--machine", "ledger-db-1"), "--machine must be"},
		"a run in capitals":               {replace(TaskInventory, "--run", "ops/Scan"), "--run must be"},
		"an address that is no IP":        {nodeArgs(TaskInventory, "--address=db.example.org"), "--address must be"},
		"a family with a space":           {replace(TaskInventory, "--os-family", "red hat"), "--os-family must be"},
		"a version with letters":          {replace(TaskInventory, "--os-version", "jammy"), "--os-version must be"},
		"an endpoint over http":           {replace(TaskInventory, "--evidence-endpoint", "http://s3.example.org"), "must use https"},
		"an endpoint with a path":         {replace(TaskInventory, "--evidence-endpoint", "https://s3.example.org/x"), "nothing after the port"},
		"a bucket in capitals":            {replace(TaskInventory, "--evidence-bucket", "Evidence"), "--evidence-bucket must be"},
		"a prefix of another machine":     {replace(TaskInventory, "--evidence-prefix", "acme/other/scan-1/"), `--evidence-prefix must be "acme/ledger-db-1/scan-1/"`},
		"a prefix with the bucket":        {replace(TaskInventory, "--evidence-prefix", "machine-evidence/acme/ledger-db-1/scan-1/"), "--evidence-prefix must be"},
		"hardening without a profile":     {nodeArgs(TaskHardening, "--content-release=0.1.82"), "--profile-id must be"},
		"a release that is not exact":     {hardening("--content-release=0.1"), "--content-release must be"},
		"a disabled rule that is no rule": {hardening("--disabled-rules=accounts_tmout"), `"accounts_tmout" is not an XCCDF rule identifier`},
		"a profile on an inventory run":   {nodeArgs(TaskInventory, "--profile-id=xccdf_org.ssgproject.content_profile_p"), "belong to the task hardening"},
		"an unknown flag":                 {nodeArgs(TaskInventory, "--reboot=true"), "flag provided but not defined"},
		"a stray argument":                {nodeArgs(TaskInventory, "extra"), `unexpected argument "extra"`},
	}
	for name, c := range cases {
		_, err := ParseArgs(c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}

func TestMainWritesTheDocumentAndTheExitCode(t *testing.T) {
	env := testEnv(t, nil)
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--task=inventory"}, env, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	written, err := os.ReadFile(env.ResultFile)
	must(t, err)
	doc, err := runresult.Parse(written)
	if err != nil || doc.Outcome != runresult.Refused || !strings.HasPrefix(doc.Message, "invalid-arguments: --mode must be one of") {
		t.Fatalf("result file %s: %+v %v", written, doc, err)
	}
	if strings.TrimSpace(stdout.String()) != string(written) || stderr.Len() != 0 {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}

	s3 := newFakeS3(t)
	env = testEnv(t, s3)
	fakeProgram(t, env.Trivy, writeSBOM)
	a := nodeArgs(TaskInventory)
	a[6] = "--evidence-endpoint=" + s3.srv.URL
	stdout.Reset()
	if code := Main(context.Background(), a, env, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d, want 0: %s", code, stdout.String())
	}
	if doc := runresult.OrNoResult(bytes.TrimSpace(stdout.Bytes())); doc.Outcome != runresult.Succeeded || !doc.Completed {
		t.Fatalf("stdout %s", stdout.String())
	}
}
