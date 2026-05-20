package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRunner is the unit-test stand-in for execRun. Each test wires
// up a per-call response map so we can exercise the Driver's command
// shape + result parsing WITHOUT a live cluster.
type fakeRunner struct {
	calls []fakeCall
	// resp maps "<binary> <space-joined-args>" → response.
	resp map[string]fakeResp
}

type fakeCall struct {
	name string
	args []string
	env  map[string]string
}

type fakeResp struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, env map[string]string, _ string) (string, string, error) {
	f.calls = append(f.calls, fakeCall{name, args, env})
	key := name + " " + strings.Join(args, " ")
	if r, ok := f.resp[key]; ok {
		return r.stdout, r.stderr, r.err
	}
	// Default: silent success with empty stdout.
	return "", "", nil
}

func newFake(responses map[string]fakeResp) (*fakeRunner, *Driver) {
	fr := &fakeRunner{resp: responses}
	return fr, &Driver{R: fr, Kubectl: "kubectl", Psql: "psql"}
}

func TestConnInfo_DSN_DefaultsSSLRequire(t *testing.T) {
	c := ConnInfo{Host: "primary-rw.ns.svc", Port: 5432, Database: "app", User: "app"}
	want := "postgres://app@primary-rw.ns.svc:5432/app?sslmode=require"
	if got := c.DSN(); got != want {
		t.Fatalf("DSN mismatch:\nwant %s\ngot  %s", want, got)
	}
}

func TestConnInfo_DSN_HonorsExplicitSSLMode(t *testing.T) {
	c := ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u", SSLMode: "disable"}
	if !strings.Contains(c.DSN(), "sslmode=disable") {
		t.Fatalf("expected sslmode=disable, got %s", c.DSN())
	}
}

func TestPsqlScalar_ReturnsTrimmed(t *testing.T) {
	c := ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u", Password: "p"}
	dsn := c.DSN()
	key := "psql -A -t -v ON_ERROR_STOP=1 -c SELECT count(*) FROM t " + dsn
	_, d := newFake(map[string]fakeResp{
		key: {stdout: "  1000000  \n"},
	})
	got, err := d.PsqlScalar(context.Background(), c, "SELECT count(*) FROM t")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "1000000" {
		t.Fatalf("expected trimmed scalar, got %q", got)
	}
}

func TestPsqlScalar_PasswordInEnvNotInArgs(t *testing.T) {
	// Pillar 3 doc warns "Password ... is intentionally passed via
	// PGPASSWORD env ... NOT embedded in the URI". Lock that in.
	c := ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u", Password: "s3cret"}
	fr, d := newFake(nil)
	_, _ = d.PsqlScalar(context.Background(), c, "SELECT 1")
	if len(fr.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fr.calls))
	}
	call := fr.calls[0]
	if call.env["PGPASSWORD"] != "s3cret" {
		t.Fatalf("expected PGPASSWORD env, got env=%v", call.env)
	}
	for _, a := range call.args {
		if strings.Contains(a, "s3cret") {
			t.Fatalf("password leaked into args: %v", call.args)
		}
	}
}

func TestPsqlReadAllIDs_ParsesLines(t *testing.T) {
	c := ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u"}
	key := "psql -A -t -v ON_ERROR_STOP=1 -c SELECT id FROM regression_d31_counter ORDER BY id " + c.DSN()
	_, d := newFake(map[string]fakeResp{
		key: {stdout: "1\n2\n3\n4\n5\n"},
	})
	ids, err := d.PsqlReadAllIDs(context.Background(), c, "regression_d31_counter")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 5 || ids[0] != 1 || ids[4] != 5 {
		t.Fatalf("unexpected ids %v", ids)
	}
}

func TestPsqlReadAllIDs_RejectsNonNumeric(t *testing.T) {
	c := ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u"}
	key := "psql -A -t -v ON_ERROR_STOP=1 -c SELECT id FROM t ORDER BY id " + c.DSN()
	_, d := newFake(map[string]fakeResp{
		key: {stdout: "1\nnotanumber\n3\n"},
	})
	_, err := d.PsqlReadAllIDs(context.Background(), c, "t")
	if err == nil || !strings.Contains(err.Error(), "bad row id") {
		t.Fatalf("expected bad-row-id error, got %v", err)
	}
}

func TestPsqlScalar_SurfacesPsqlError(t *testing.T) {
	c := ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u"}
	key := "psql -A -t -v ON_ERROR_STOP=1 -c SELECT 1/0 " + c.DSN()
	_, d := newFake(map[string]fakeResp{
		key: {stderr: "ERROR: division by zero", err: errors.New("exit status 1")},
	})
	_, err := d.PsqlScalar(context.Background(), c, "SELECT 1/0")
	if err == nil || !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("expected division-by-zero in error, got %v", err)
	}
}

func TestScalePrimaryToZero_PatchShape(t *testing.T) {
	fr, d := newFake(nil)
	_, err := d.ScalePrimaryToZero(context.Background(), "tenant-1", "wp-db-primary")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fr.calls))
	}
	args := strings.Join(fr.calls[0].args, " ")
	// Lock in: namespace, CR kind, name, merge patch with instances:0.
	if !strings.Contains(args, "-n tenant-1") {
		t.Fatalf("namespace flag missing: %s", args)
	}
	if !strings.Contains(args, "cluster.postgresql.cnpg.io") {
		t.Fatalf("CR kind missing: %s", args)
	}
	if !strings.Contains(args, `"instances":0`) {
		t.Fatalf(`expected '"instances":0' in patch: %s`, args)
	}
}

func TestPromoteReplica_FlipsReplicaEnabled(t *testing.T) {
	fr, d := newFake(nil)
	if err := d.PromoteReplica(context.Background(), "tenant-1", "wp-db-replica"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	args := strings.Join(fr.calls[0].args, " ")
	if !strings.Contains(args, `"enabled":false`) {
		t.Fatalf("expected replica.enabled:false patch, got: %s", args)
	}
}

func TestWaitReplicaPrimary_ReturnsWhenStatusPopulates(t *testing.T) {
	c := ConnInfo{}
	_ = c
	// Build a runner that returns empty stdout on first 2 polls and
	// then a non-empty currentPrimary. Verifies the poll loop honors
	// the populated status and returns elapsed-since-killT.
	calls := 0
	d := &Driver{
		R: runnerFunc(func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (string, string, error) {
			calls++
			if calls < 3 {
				return "", "", nil
			}
			return "wp-db-replica-1", "", nil
		}),
		Kubectl: "kubectl",
	}
	killT := time.Now()
	got, err := d.WaitReplicaPrimary(context.Background(), "ns", "wp-db-replica", killT, 5*time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("expected promotion, got err %v", err)
	}
	if got <= 0 {
		t.Fatalf("expected positive elapsed, got %v", got)
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 poll attempts, got %d", calls)
	}
}

func TestWaitReplicaPrimary_FailsFastOnRTOExceeded(t *testing.T) {
	// Per CLAUDE.md "if region-kill doesn't trigger promotion within
	// 90s (3x the 30s RTO), report FAIL with diagnostics, don't hang
	// forever." Smoke-test that the deadline path produces a useful
	// error including a status dump.
	d := &Driver{
		R: runnerFunc(func(_ context.Context, _ string, args []string, _ map[string]string, _ string) (string, string, error) {
			if len(args) > 0 && args[len(args)-1] == `jsonpath={.status.currentPrimary}` {
				return "", "", nil
			}
			// the diagnostics dump request — return a marker the
			// assert below grep's for.
			return "phase: ReplicaCluster (no promotion)", "", nil
		}),
		Kubectl: "kubectl",
	}
	_, err := d.WaitReplicaPrimary(context.Background(), "ns", "r", time.Now(), 50*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected RTO exceeded error")
	}
	if !strings.Contains(err.Error(), "RTO exceeded") {
		t.Fatalf("expected RTO-exceeded error, got %v", err)
	}
	if !strings.Contains(err.Error(), "no promotion") {
		t.Fatalf("expected status dump in error, got %v", err)
	}
}

// runnerFunc is a tiny adaptor letting tests pass a plain function
// where a Runner is expected.
type runnerFunc func(ctx context.Context, name string, args []string, env map[string]string, stdin string) (string, string, error)

func (r runnerFunc) Run(ctx context.Context, name string, args []string, env map[string]string, stdin string) (string, string, error) {
	return r(ctx, name, args, env, stdin)
}
