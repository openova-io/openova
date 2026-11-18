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

// TestReadDialTable_Repository reads the dial table of the repository: it is
// valid, and its first three rows deny the namespace default the three stores.
func TestReadDialTable_Repository(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "policy-dials.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ReadDialTable(data)
	if err != nil {
		t.Fatalf("the dial table of the repository is refused: %v", err)
	}
	want := []DialRow{
		{Name: "default-to-openbao", From: DialSource{Namespace: "default"}, To: DialTarget{Namespace: "openbao", Service: "openbao", Port: 8200}, Expect: "deny"},
		{Name: "default-to-git", From: DialSource{Namespace: "default"}, To: DialTarget{Namespace: "gitea", Service: "gitea-http", Port: 3000}, Expect: "deny"},
		{Name: "default-to-shared-postgres", From: DialSource{Namespace: "default"}, To: DialTarget{Namespace: "shared-data", Service: "shared-pg-rw", Port: 5432}, Expect: "deny"},
	}
	if len(rows) < len(want) {
		t.Fatalf("the dial table has %d rows, want at least %d", len(rows), len(want))
	}
	for i, w := range want {
		got, _ := json.Marshal(rows[i])
		exp, _ := json.Marshal(w)
		if string(got) != string(exp) {
			t.Errorf("row %d is %s, want %s", i+1, got, exp)
		}
	}
	if rows[0].To.String() != "openbao.openbao.svc.cluster.local:8200" {
		t.Errorf("the first row dials %s", rows[0].To)
	}
}

// TestReadDialTable_Refusals: every broken table is refused with its reason.
func TestReadDialTable_Refusals(t *testing.T) {
	row := func(s string) string {
		return `{"schema": "openova.io/policy-dials/v1", "dials": [` + s + `]}`
	}
	good := `{"name": "a", "from": {"namespace": "default"}, "to": {"namespace": "gitea", "service": "gitea-http", "port": 3000}, "expect": "deny"}`
	if rows, err := ReadDialTable([]byte("# a comment\n  # an indented comment\n" + row(good))); err != nil || len(rows) != 1 {
		t.Fatalf("a valid table with comment lines is refused: %v", err)
	}
	address := `{"name": "a", "from": {"namespace": "x", "labels": {"app.kubernetes.io/name": "web"}}, "to": {"address": "169.254.169.254", "port": 80}, "expect": "deny"}`
	if rows, err := ReadDialTable([]byte(row(address))); err != nil || rows[0].To.Host() != "169.254.169.254" {
		t.Fatalf("a row to an address is refused or dials another host: %v %v", rows, err)
	}
	for name, c := range map[string]struct{ text, want string }{
		"schema":         {`{"schema": "v0", "dials": [` + good + `]}`, `has schema "v0"`},
		"no rows":        {row(""), "has no row"},
		"unknown field":  {row(strings.Replace(good, `"expect"`, `"mode": "x", "expect"`, 1)), `unknown field "mode"`},
		"trailing comma": {row(good + ","), "is not JSON"},
		"two documents":  {row(good) + row(good), "more than one document"},
		"name twice":     {row(good + "," + good), `the name "a" is used twice`},
		"bad name":       {row(strings.Replace(good, `"a"`, `"A_b"`, 1)), "the name is not"},
		"no namespace":   {row(strings.Replace(good, `"namespace": "default"`, `"namespace": ""`, 1)), "from.namespace"},
		"expect":         {row(strings.Replace(good, `"deny"`, `"drop"`, 1)), `expect is "drop"`},
		"port 0":         {row(strings.Replace(good, `3000`, `0`, 1)), "to.port 0"},
		"port too large": {row(strings.Replace(good, `3000`, `70000`, 1)), "to.port 70000"},
		"both targets":   {row(strings.Replace(good, `"port": 3000`, `"address": "10.0.0.1", "port": 3000`, 1)), "names a Service and an address"},
		"no target":      {row(strings.Replace(good, `"namespace": "gitea", "service": "gitea-http", `, ``, 1)), "is not an IP address"},
		"no service":     {row(strings.Replace(good, `"service": "gitea-http", `, ``, 1)), "must both be names"},
		"bad address":    {row(strings.Replace(address, "169.254.169.254", "metadata.local", 1)), `to.address "metadata.local"`},
		"reserved label": {row(strings.Replace(address, `"app.kubernetes.io/name"`, `"`+DialLabel+`"`, 1)), "may not set " + DialLabel},
		"bad label":      {row(strings.Replace(address, `"web"`, `"-web"`, 1)), "is not a label"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadDialTable([]byte(c.text)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want an error that holds %q, got %v", c.want, err)
			}
		})
	}
}

// TestDialOutcome: what busybox nc of alpine 3.20 prints for each outcome.
func TestDialOutcome(t *testing.T) {
	for _, c := range []struct{ rc, out, want string }{
		{"0", "10.43.0.10 (10.43.0.10:53) open sent 0, rcvd 0", DialConnected},
		{"1", "nc: 10.255.255.1 (10.255.255.1:80): Operation timed out sent 0, rcvd 0", DialTimedOut},
		{"1", "nc: 127.0.0.1 (127.0.0.1:1): Connection refused sent 0, rcvd 0", DialRefused},
		{"1", "nc: bad address 'no-such.invalid'", DialUnresolved},
		{"1", "nc: 10.0.0.1 (10.0.0.1:80): No route to host", DialError},
		{"", "", DialError},
	} {
		if got := dialOutcome(c.rc, c.out); got != c.want {
			t.Errorf("dialOutcome(%q, %q) = %s, want %s", c.rc, c.out, got, c.want)
		}
	}
}

// dialFake answers kubectl for check NET-G06. Every exec runs the real dial
// script with sh and a fake nc, which prints what busybox nc prints for the
// outcome that answers names for <host>:<port>.
type dialFake struct {
	t                *testing.T
	answers          map[string]string // <host>:<port> -> outcome
	waitErr          error
	ready            map[string]int    // <namespace>/<service> -> ready endpoints; a Service not listed has 1
	readyErr         error             // the error of get endpointslices
	created, deleted []string          // <namespace>/<name> of the Pods
	labels           map[string]string // <namespace>/<name> -> its labels, sorted, as k=v,k=v
	asked            []string          // <namespace>/<service> of every get endpointslices
}

const fakeNC = `#!/bin/sh
# nc -z -vv -w <seconds> <host> <port>
[ "$1 $2 $3" = "-z -vv -w" ] || { echo "fake nc: unexpected arguments $*" >&2; exit 9; }
want=$(sed -n "s|^$5:$6=||p" "$(dirname "$0")/answers")
case "$want" in
connected) echo "$5 (10.0.0.9:$6) open"; echo "sent 0, rcvd 0"; exit 0 ;;
timed-out) echo "nc: $5 (10.0.0.9:$6): Operation timed out" >&2; echo "sent 0, rcvd 0" >&2; exit 1 ;;
refused) echo "nc: $5 (10.0.0.9:$6): Connection refused" >&2; echo "sent 0, rcvd 0" >&2; exit 1 ;;
*) echo "nc: bad address '$5'" >&2; exit 1 ;;
esac
`

func (f *dialFake) kubectl(_ kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	if args[0] != "-n" || len(args) < 4 {
		f.t.Fatalf("every call names a namespace first: %v", args)
	}
	ns, verb := args[1], args[2]
	switch verb {
	case "create":
		var o kube.Object
		b, _ := io.ReadAll(stdin)
		if err := json.Unmarshal(b, &o); err != nil || o.Namespace() != ns || o.Str("kind") != "Pod" {
			f.t.Fatalf("create -f - got another object than a Pod of %s: %s", ns, b)
		}
		c := o.Objects("spec", "containers")[0]
		if c.Str("image") != secProbeImage || o.Get("spec", "hostNetwork") != nil || o.Get("spec", "automountServiceAccountToken") != false ||
			o.Get("spec", "securityContext", "runAsNonRoot") != true || o.Str("metadata", "labels", DialLabel) == "" {
			f.t.Errorf("the dial Pod runs another image, on the host network, with a token, as root or without its label: %s", b)
		}
		f.created = append(f.created, ns+"/"+o.Str("metadata", "name"))
		if f.labels == nil {
			f.labels = map[string]string{}
		}
		labels := map[string]string{}
		if m, ok := o.Get("metadata", "labels").(map[string]any); ok {
			for k, v := range m {
				labels[k], _ = v.(string)
			}
		}
		f.labels[ns+"/"+o.Str("metadata", "name")] = DialSource{Labels: labels}.key()
	case "get":
		if args[3] != "endpointslices" || args[4] != "-l" || !strings.HasPrefix(args[5], "kubernetes.io/service-name=") || args[7] != "json" {
			f.t.Fatalf("unexpected get %v", args)
		}
		svc := ns + "/" + strings.TrimPrefix(args[5], "kubernetes.io/service-name=")
		f.asked = append(f.asked, svc)
		if f.readyErr != nil {
			return nil, f.readyErr
		}
		n, ok := f.ready[svc]
		if !ok {
			n = 1
		}
		// One endpoint that is not ready, one whose condition is not set (it counts as ready), then true ones.
		eps := []string{`{"conditions": {"ready": false}}`}
		for i := 0; i < n; i++ {
			if i == 0 {
				eps = append(eps, `{"conditions": {}}`)
			} else {
				eps = append(eps, `{"conditions": {"ready": true}}`)
			}
		}
		return []byte(`{"items": [{"endpoints": [` + strings.Join(eps, ", ") + `]}]}`), nil
	case "wait":
		return nil, f.waitErr
	case "exec":
		at := 0
		for args[at] != "--" {
			at++
		}
		dir := f.t.TempDir()
		answers := ""
		for k, v := range f.answers {
			answers += k + "=" + v + "\n"
		}
		hostFile(f.t, dir, "nc", 0o700, []byte(fakeNC))
		hostFile(f.t, dir, "answers", 0o600, []byte(answers))
		if args[at+1] != "sh" {
			f.t.Fatalf("the dial does not run sh: %v", args)
		}
		cmd := exec.Command("sh")
		cmd.Args = append(cmd.Args, args[at+2:]...)
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Stdin = stdin
		return cmd.Output()
	case "delete":
		f.deleted = append(f.deleted, ns+"/"+args[4])
	default:
		f.t.Fatalf("unexpected kubectl call %v", args)
	}
	return nil, nil
}

// dialRun runs check NET-G06 on two regions with the dial table of the
// repository, the way the runner does. It fails the test unless the check
// passed (want is empty) or failed with a reason that holds want, and unless
// every Pod it created was deleted.
func dialRun(t *testing.T, f *dialFake, repoRoot, want string) *runner {
	t.Helper()
	cc := &CheckContext{Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}, RepoRoot: repoRoot}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	c := policyDialsCheck{kubectl: f.kubectl}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateExpectedFail)}}, []Check{c}, cc, true)
	if res := r.run.Checks[0]; (want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want) {
		t.Errorf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want)
	}
	if strings.Join(f.created, " ") != strings.Join(f.deleted, " ") {
		t.Errorf("Pods created %v, deleted %v", f.created, f.deleted)
	}
	return r
}

func TestPolicyDialsCheck(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	control := dialControlHost + ":53"
	stores := []string{"openbao.openbao.svc.cluster.local:8200", "gitea-http.gitea.svc.cluster.local:3000", "shared-pg-rw.shared-data.svc.cluster.local:5432"}
	answers := func(outcome string, control2 string) map[string]string {
		a := map[string]string{control: control2}
		for _, s := range stores {
			a[s] = outcome
		}
		return a
	}
	for name, c := range map[string]struct {
		answers map[string]string
		want    string
	}{
		"denied":         {answers("timed-out", "connected"), ""},
		"open":           {answers("connected", "connected"), "want deny; the dial of openbao.openbao.svc.cluster.local:8200 connected"},
		"refused":        {answers("refused", "connected"), "was refused: it reached the destination and no listener answered"},
		"unresolved":     {answers("unknown", "connected"), "the name openbao.openbao.svc.cluster.local does not resolve"},
		"no network":     {answers("timed-out", "timed-out"), "did not connect to " + control + " (timed-out"},
		"one store open": {map[string]string{control: "connected", stores[0]: "timed-out", stores[1]: "timed-out", stores[2]: "connected"}, "want deny; the dial of shared-pg-rw"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &dialFake{t: t, answers: c.answers}
			r := dialRun(t, f, root, c.want)
			if len(r.run.Steps) != 10 || len(f.created) != 2 {
				t.Fatalf("want per region one Pod, one start step, three row steps and one delete step; got %d steps and Pods %v", len(r.run.Steps), f.created)
			}
			row := r.run.Steps[1]
			if row.Evidence["block"] != "POL" || row.Evidence["row"] != "default-to-openbao" || row.Evidence["expect"] != "deny" ||
				row.Evidence["from"] != "default" || row.Evidence["to"] != stores[0] {
				t.Errorf("the step of the first row records %v", row.Evidence)
			}
			if f.created[0] != f.created[1] || !strings.HasPrefix(f.created[0], "default/pol-dial-") {
				t.Errorf("want the Pod in the namespace default of each region, got %v", f.created)
			}
		})
	}
}

func TestPolicyDialsCheck_Faults(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	f := &dialFake{t: t, waitErr: errors.New("timed out waiting for the condition")}
	if r := dialRun(t, f, root, "is not Ready: timed out waiting for the condition"); len(r.run.Steps) != 4 || len(f.deleted) != 2 {
		t.Errorf("Pods that are not Ready: want no row step and both Pods deleted, got %d steps and %v", len(r.run.Steps), f.deleted)
	}
	f = &dialFake{t: t}
	if r := dialRun(t, f, t.TempDir(), "policy-dials.yaml"); len(f.created) != 0 || len(r.run.Steps) != 1 {
		t.Errorf("no dial table: want no Pod and one failed step, got %v and %d steps", f.created, len(r.run.Steps))
	}
	if r := dialRun(t, f, "", "--repo-root"); len(r.run.Steps) != 1 {
		t.Errorf("no checkout: want one failed step, got %d", len(r.run.Steps))
	}
	c, ok := registry[policyDialsID].(policyDialsCheck)
	if !ok || c.kubectl == nil {
		t.Errorf("the registry must hold %s with kube.Region.Run", policyDialsID)
	}
}

// TestPolicyDialsCheck_AllowRows: an allow row passes only when the dial
// connects; its Pod carries the row's labels; a deny row to an address reads
// no EndpointSlice, and a deny row to a Service without a ready endpoint fails.
func TestPolicyDialsCheck_AllowRows(t *testing.T) {
	root := t.TempDir()
	hostFile(t, root, policyDialsTable, 0o600, []byte(`# two rows
{"schema": "openova.io/policy-dials/v1", "dials": [
  {"name": "web-to-db", "from": {"namespace": "gitea", "labels": {"app": "web"}}, "to": {"namespace": "shared-data", "service": "shared-pg-rw", "port": 5432}, "expect": "allow"},
  {"name": "default-to-metadata", "from": {"namespace": "default"}, "to": {"address": "169.254.169.254", "port": 80}, "expect": "deny"},
  {"name": "default-to-db", "from": {"namespace": "default"}, "to": {"namespace": "shared-data", "service": "shared-pg-rw", "port": 5432}, "expect": "deny"}
]}
`))
	control, db, metadata := dialControlHost+":53", "shared-pg-rw.shared-data.svc.cluster.local:5432", "169.254.169.254:80"
	for name, c := range map[string]struct {
		db    string
		ready int
		want  string
	}{
		"allowed":     {"connected", 1, "want deny; the dial of " + db + " connected"},
		"dropped":     {"timed-out", 1, "want allow; the dial of " + db + " timed-out"},
		"refused":     {"refused", 1, "want allow; the dial of " + db + " was refused"},
		"no endpoint": {"timed-out", 0, "want allow; the dial of " + db + " timed-out"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &dialFake{t: t, answers: map[string]string{control: "connected", db: c.db, metadata: "timed-out"},
				ready: map[string]int{"shared-data/shared-pg-rw": c.ready}}
			r := dialRun(t, f, root, c.want)
			if len(f.created) != 4 || !strings.HasPrefix(f.created[0], "gitea/") || !strings.Contains(f.labels[f.created[0]], ",app=web") || strings.Contains(f.labels[f.created[1]], "app=web") {
				t.Errorf("want the Pod of the first source in gitea with the label app=web and the Pod of default without it: %v %v", f.created, f.labels)
			}
			if strings.Join(f.asked, " ") != "shared-data/shared-pg-rw shared-data/shared-pg-rw" {
				t.Errorf("want one EndpointSlice read per region, for the deny row to the Service only, got %v", f.asked)
			}
			row := r.run.Steps[3]
			if c.ready == 0 && !strings.Contains(row.Error, "the Service shared-data/shared-pg-rw has no ready endpoint") {
				t.Errorf("a deny row to a Service without a ready endpoint must fail, got %q", row.Error)
			}
			if c.ready == 1 && row.Evidence["ready_endpoints"] != 1 {
				t.Errorf("the deny row records %v ready endpoints, want 1", row.Evidence["ready_endpoints"])
			}
		})
	}
	f := &dialFake{t: t, answers: map[string]string{control: "connected", db: "connected", metadata: "timed-out"}, readyErr: errors.New("forbidden")}
	if r := dialRun(t, f, root, "read the EndpointSlices of the Service shared-data/shared-pg-rw: forbidden"); len(r.run.Steps) != 10 {
		t.Errorf("an EndpointSlice read that fails: want the row to fail and every step recorded, got %d steps", len(r.run.Steps))
	}
}

// TestDialer_OnePodPerSource: two rows of one source share a Pod, other
// labels make another, and a Dial from a source without a Pod is an error.
func TestDialer_OnePodPerSource(t *testing.T) {
	f := &dialFake{t: t}
	d := &Dialer{Kubectl: f.kubectl, Region: kube.Region{ID: "region-a"}, Run: "r1"}
	web := DialSource{Namespace: "gitea", Labels: map[string]string{"app": "web"}}
	if err := d.Start(context.Background(), []DialSource{{Namespace: "default"}, web, {Namespace: "default"}, web}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.Pods(), " ") != "default/pol-dial-r1-1 gitea/pol-dial-r1-2" {
		t.Errorf("want one Pod per source, got %v", d.Pods())
	}
	if _, err := d.Dial(context.Background(), DialSource{Namespace: "gitea"}, DialTarget{Address: "10.0.0.1", Port: 80}); err == nil {
		t.Error("a dial from a source without a Pod must fail")
	}
	if err := d.Stop(context.Background()); err != nil || strings.Join(f.deleted, " ") != "default/pol-dial-r1-1 gitea/pol-dial-r1-2" {
		t.Errorf("Stop deleted %v: %v", f.deleted, err)
	}
}
