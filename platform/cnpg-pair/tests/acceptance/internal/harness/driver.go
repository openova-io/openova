// Driver wrappers around `kubectl` and `psql`. Kept stdlib-only so
// the image stays tiny; the harness's job is orchestration, not
// re-implementing a Postgres driver. The shell-out approach also
// matches the chart-render gate at chart/tests/cnpg-pair-render.sh
// (existing precedent in this directory tree).
//
// Each function has a context-bounded timeout — the harness must
// NEVER hang. Per CLAUDE.md §0 the harness "should fail-safe: if
// region-kill doesn't trigger promotion within 90s (3x the 30s RTO),
// report FAIL with diagnostics, don't hang forever."
package harness

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ConnInfo is the minimum a `psql` invocation needs to reach a CNPG
// cluster from inside the Sovereign management cluster. Populated by
// the operator-supplied flags in cmd/d31-acceptance.
type ConnInfo struct {
	// Host: the Service DNS the harness Pod targets. Canonical pair:
	//   primary    → <cluster>-primary-rw.<ns>.svc.cluster.local
	//   replica    → <cluster>-replica-rw.<ns>.svc.cluster.local
	// Post-promotion the replica's `-rw` Service starts answering as
	// the primary; the harness simply re-points to it.
	Host     string
	Port     int
	Database string
	User     string
	Password string // sourced from a Secret env-mount; never logged.
	SSLMode  string // "require" by default; "disable" for in-cluster.
}

// DSN returns the libpq URI for `psql`. Password is intentionally
// passed via PGPASSWORD env (see Driver.runPsql) NOT embedded in the
// URI — that way it never leaks into process listings.
func (c ConnInfo) DSN() string {
	ssl := c.SSLMode
	if ssl == "" {
		ssl = "require"
	}
	return fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=%s", c.User, c.Host, c.Port, c.Database, ssl)
}

// Runner is the shell-out abstraction. Production code uses execRun;
// tests inject a fake.
type Runner interface {
	Run(ctx context.Context, name string, args []string, env map[string]string, stdin string) (stdout string, stderr string, err error)
}

type execRun struct{}

func (execRun) Run(ctx context.Context, name string, args []string, env map[string]string, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		envv := cmd.Environ()
		for k, v := range env {
			envv = append(envv, k+"="+v)
		}
		cmd.Env = envv
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

// DefaultRunner returns the production exec runner.
func DefaultRunner() Runner { return execRun{} }

// Driver bundles kubectl + psql ops. Kept stateless so cmd/main.go
// constructs one and passes it around.
type Driver struct {
	R       Runner
	Kubectl string // "kubectl" by default; tests override.
	Psql    string // "psql" by default.
}

// NewDriver returns a Driver with the production runner and default
// binary names.
func NewDriver() *Driver {
	return &Driver{R: DefaultRunner(), Kubectl: "kubectl", Psql: "psql"}
}

// PsqlScalar runs `psql -A -t -c '<query>'` and returns the trimmed
// scalar result as a string. Use for SELECT count(*), SELECT MAX(id),
// `SELECT currentPrimary FROM ...`, etc.
func (d *Driver) PsqlScalar(ctx context.Context, c ConnInfo, query string) (string, error) {
	args := []string{"-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", query, c.DSN()}
	env := map[string]string{}
	if c.Password != "" {
		env["PGPASSWORD"] = c.Password
	}
	so, se, err := d.R.Run(ctx, d.Psql, args, env, "")
	if err != nil {
		return "", fmt.Errorf("psql failed: %w (stderr=%s)", err, strings.TrimSpace(se))
	}
	return strings.TrimSpace(so), nil
}

// PsqlExec runs `psql` against the given conn with the supplied SQL
// on stdin (multi-statement OK). Discards stdout; surfaces stderr in
// the error.
func (d *Driver) PsqlExec(ctx context.Context, c ConnInfo, sql string) error {
	args := []string{"-v", "ON_ERROR_STOP=1", c.DSN()}
	env := map[string]string{}
	if c.Password != "" {
		env["PGPASSWORD"] = c.Password
	}
	_, se, err := d.R.Run(ctx, d.Psql, args, env, sql)
	if err != nil {
		return fmt.Errorf("psql exec failed: %w (stderr=%s)", err, strings.TrimSpace(se))
	}
	return nil
}

// PsqlReadAllIDs streams `SELECT id FROM <table> ORDER BY id` and
// returns every ID as int64. Used post-promotion to feed the gap
// detector. For a 1M-row dataset the result is ~8MB — fine to hold
// in memory in a single-shot harness.
func (d *Driver) PsqlReadAllIDs(ctx context.Context, c ConnInfo, table string) ([]int64, error) {
	query := fmt.Sprintf("SELECT id FROM %s ORDER BY id", table)
	args := []string{"-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", query, c.DSN()}
	env := map[string]string{}
	if c.Password != "" {
		env["PGPASSWORD"] = c.Password
	}
	so, se, err := d.R.Run(ctx, d.Psql, args, env, "")
	if err != nil {
		return nil, fmt.Errorf("psql read-all failed: %w (stderr=%s)", err, strings.TrimSpace(se))
	}
	var ids []int64
	for _, line := range strings.Split(strings.TrimSpace(so), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad row id %q: %w", line, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ScalePrimaryToZero scales the primary CNPG Cluster CR's instances
// to 0 — the canonical "region-kill proxy" (per DESIGN.md:236-243).
// Option A in the harness brief; safest because it doesn't require
// hcloud creds or NetworkPolicy authority, just RBAC to patch the
// Cluster CR. CNPG operator reconciles instances=0 by terminating
// every Pod in the StatefulSet; the replica Cluster CR (separate
// Cluster CR in the other region) is untouched.
//
// Returns the time of the patch, so the caller can measure RTO from
// kill-time to replica-promoted.
func (d *Driver) ScalePrimaryToZero(ctx context.Context, namespace, clusterName string) (time.Time, error) {
	args := []string{
		"-n", namespace,
		"patch", "cluster.postgresql.cnpg.io", clusterName,
		"--type=merge",
		"-p", `{"spec":{"instances":0}}`,
	}
	killT := time.Now()
	_, se, err := d.R.Run(ctx, d.Kubectl, args, nil, "")
	if err != nil {
		return killT, fmt.Errorf("kubectl patch failed: %w (stderr=%s)", err, strings.TrimSpace(se))
	}
	return killT, nil
}

// PromoteReplica flips the replica Cluster CR's `replica.enabled`
// to false, which CNPG interprets as "promote to standalone primary."
// In the canonical D31 flow this is the manual promotion path the
// Continuum K-Cont-2 sequencer normally automates; the harness
// invokes it directly so the test is self-contained and does NOT
// depend on Continuum being installed.
//
// (See DESIGN.md:244 — "Continuum K-Cont-2 promotes the replica via
// the replica.enabled flip.")
func (d *Driver) PromoteReplica(ctx context.Context, namespace, replicaCluster string) error {
	args := []string{
		"-n", namespace,
		"patch", "cluster.postgresql.cnpg.io", replicaCluster,
		"--type=merge",
		"-p", `{"spec":{"replica":{"enabled":false}}}`,
	}
	_, se, err := d.R.Run(ctx, d.Kubectl, args, nil, "")
	if err != nil {
		return fmt.Errorf("kubectl promote failed: %w (stderr=%s)", err, strings.TrimSpace(se))
	}
	return nil
}

// WaitReplicaPrimary polls the replica Cluster CR's
// `status.currentPrimary` field until it is non-empty (which only
// happens once CNPG has elected a primary on the now-standalone
// cluster). Returns the elapsed time from `killT`. Returns an error
// with diagnostics if the deadline expires.
func (d *Driver) WaitReplicaPrimary(ctx context.Context, namespace, replicaCluster string, killT time.Time, deadline time.Duration, pollEvery time.Duration) (time.Duration, error) {
	end := time.Now().Add(deadline)
	for {
		if time.Now().After(end) {
			// Best-effort grab of the CR status for the diagnostics
			// dump — never let this hang.
			dumpCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			dump, _, _ := d.R.Run(dumpCtx, d.Kubectl, []string{"-n", namespace, "get", "cluster.postgresql.cnpg.io", replicaCluster, "-o", "yaml"}, nil, "")
			cancel()
			return 0, fmt.Errorf("RTO exceeded: replica did not promote within %s. Cluster CR status snapshot:\n%s", deadline, dump)
		}
		args := []string{
			"-n", namespace,
			"get", "cluster.postgresql.cnpg.io", replicaCluster,
			"-o", `jsonpath={.status.currentPrimary}`,
		}
		so, _, err := d.R.Run(ctx, d.Kubectl, args, nil, "")
		if err == nil && strings.TrimSpace(so) != "" {
			return time.Since(killT), nil
		}
		select {
		case <-time.After(pollEvery):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}
