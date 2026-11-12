package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// The check registry. A gate check is one entry of docs/ledger/gates.yaml.
// Its code lives in a file check_<name>.go of this package. That file calls
// RegisterCheck in init() and holds the check identifier as a string literal:
// `scripts/acceptance-gate.py validate` looks for both in the file that the
// manifest names as the implementation. A check file changes no other file.

// RunIn says where the suite is running. It selects checks by their runs_in list.
type RunIn string

const (
	RunInLocal RunIn = "local" // pull request, hermetic local provider pack
	RunInCloud RunIn = "cloud" // release train, real cloud
	RunInSelf  RunIn = "self"  // Job inside a Sovereign
)

// ManifestSchema is the schema string of the exported gate manifest.
const ManifestSchema = "openova.io/gates/v1"

// The states of a manifest entry that the suite selects.
const (
	StateExpectedFail = "expected-fail"
	StateRequired     = "required"
)

// ErrManifest wraps every error of LoadManifest and SelectChecks. The binary
// exits 2 on it and writes no record.
var ErrManifest = errors.New("gate manifest")

// ManifestCheck is one entry of gates.yaml as exported to JSON.
type ManifestCheck struct {
	ID          string   `json:"id"`
	State       string   `json:"state"` // not-built | expected-fail | required
	RunsIn      []string `json:"runs_in"`
	Destructive bool     `json:"destructive"`
	Needs       []string `json:"needs"`
}

// Manifest is the exported gates.yaml (scripts/acceptance-gate.py export).
type Manifest struct {
	Schema string          `json:"schema"`
	Checks []ManifestCheck `json:"checks"`
}

// Candidate is what the run certifies.
type Candidate struct {
	ID     string `json:"id"`     // empty when RunIn is local or self
	Commit string `json:"commit"` // 40 hex: HEAD of the checkout at RepoRoot
}

// DeploymentInfo describes the deployment under test. The suite knows its
// identifier only. The package that gives the suite the deployment request
// adds the provider and the requested nodes, with the check that reads them.
type DeploymentInfo struct {
	ID string
}

// CheckContext is what a check may read. A check must not modify it.
type CheckContext struct {
	RunIn     RunIn
	Regions   []kube.Region // every region, primary first
	API       *api.Client   // nil when there is no console session
	Candidate Candidate
	RepoRoot  string // checkout of Candidate.Commit; pins are read from here
	Deploy    *DeploymentInfo
	Caps      map[string]bool // capability flags declared for this run
	Prior     []*Step         // the steps recorded before the checks started; read-only
}

// StepRecorder appends steps to the run record.
type StepRecorder interface {
	// Step runs fn, times it, and appends a Step whose Journey is the check
	// identifier. fn fills ev with observed values only. A nil return is PASS,
	// an error is FAIL with that error text.
	Step(id, name string, fn func(ev Evidence) error)
	// Skip appends a Step with status SKIP and the reason.
	Skip(id, name, reason string)
}

// Check is one row of the gate manifest.
type Check interface {
	// ID returns the manifest identifier, for example "FND-G03".
	ID() string
	// Run performs the check and records at least one step. A non-nil error
	// means the check could not run; it is recorded as FAIL with that error.
	Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error
}

var registry = map[string]Check{}

// RegisterCheck adds c to the registry. It panics on a duplicate identifier.
func RegisterCheck(c Check) {
	id := c.ID()
	if _, twice := registry[id]; twice {
		panic("suite: check " + id + " is registered twice")
	}
	registry[id] = c
}

// RegisteredChecks returns every registered check, sorted by identifier.
func RegisteredChecks() []Check {
	out := make([]Check, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// LoadManifest reads the JSON export of gates.yaml.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%w: %s is not JSON: %v", ErrManifest, path, err)
	}
	if m.Schema != ManifestSchema {
		return nil, fmt.Errorf("%w: %s has schema %q, want %q", ErrManifest, path, m.Schema, ManifestSchema)
	}
	return &m, nil
}

// SelectChecks returns the registered checks whose manifest entry has state
// expected-fail or required, lists r in runs_in, and is not destructive when
// r is RunInSelf. It returns an error if such an entry has no registered
// check: a missing check is never a pass.
func SelectChecks(m *Manifest, r RunIn) ([]Check, error) {
	out := []Check{}
	for _, e := range m.Checks {
		if e.State != StateExpectedFail && e.State != StateRequired {
			continue
		}
		if !slices.Contains(e.RunsIn, string(r)) || (e.Destructive && r == RunInSelf) {
			continue
		}
		c, ok := registry[e.ID]
		if !ok {
			return nil, fmt.Errorf("%w: no registered check for %s", ErrManifest, e.ID)
		}
		out = append(out, c)
	}
	return out, nil
}

// entry returns the manifest entry of a check.
func (m *Manifest) entry(id string) ManifestCheck {
	for _, e := range m.Checks {
		if e.ID == id {
			return e
		}
	}
	return ManifestCheck{ID: id}
}

// splitList turns "a, b" into [a b]. An empty string gives an empty list.
func splitList(s string) []string {
	out := []string{}
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// headCommit is the commit that the checkout at dir has checked out, or "".
func headCommit(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// producerFromEnv says who produced the record. Inside a GitHub Actions run
// it is the workflow file and the run; anywhere else it is manual, and a
// manual record never counts for a verdict.
func producerFromEnv(getenv func(string) string) Producer {
	if getenv("GITHUB_ACTIONS") != "true" {
		return Producer{Kind: "manual"}
	}
	// GITHUB_WORKFLOW_REF is <owner>/<repo>/.github/workflows/<file>@<ref>.
	file := getenv("GITHUB_WORKFLOW_REF")
	if i := strings.Index(file, "@"); i >= 0 {
		file = file[:i]
	}
	file = file[strings.LastIndex(file, "/")+1:]
	attempt, _ := strconv.Atoi(getenv("GITHUB_RUN_ATTEMPT"))
	return Producer{Kind: "workflow", Workflow: file, RunID: getenv("GITHUB_RUN_ID"), RunAttempt: attempt}
}

// checkRecorder is the StepRecorder of one check.
type checkRecorder struct {
	r  *runner
	id string
}

func (c checkRecorder) Step(id, name string, fn func(ev Evidence) error) {
	c.r.step(c.id, id, name, fn)
}

func (c checkRecorder) Skip(id, name, reason string) {
	now := time.Now().UTC()
	c.r.run.Steps = append(c.r.run.Steps, &Step{Journey: c.id, ID: id, Name: name, Status: Skip,
		StartedAt: now, FinishedAt: now, Error: reason})
	_, _ = fmt.Fprintf(c.r.out, "  %s %-6s SKIP %s\n", c.id, id, reason)
}

// runChecks records one result for every selected check, in manifest order.
// A check whose needs the run did not declare is skipped and not run. When
// discovery failed no check is run: each is recorded SKIP with that reason.
func (r *runner) runChecks(ctx context.Context, m *Manifest, checks []Check, cc *CheckContext, discovered bool) {
	cc.Prior = append([]*Step(nil), r.run.Steps...)
	for _, c := range checks {
		e := m.entry(c.ID())
		res := CheckResult{ID: e.ID, StateAtRun: e.State, Status: Pass}
		rec := checkRecorder{r: r, id: e.ID}
		first := len(r.run.Steps)
		missing := []string{}
		for _, n := range e.Needs {
			if !cc.Caps[n] {
				missing = append(missing, n)
			}
		}
		switch {
		case len(missing) > 0:
			res.unmet = true
			rec.Skip("-", "check not run", "the run does not declare "+strings.Join(missing, ", "))
		case !discovered:
			rec.Skip("-", "check not run", "discovery (D0) failed")
		default:
			if err := c.Run(ctx, cc, rec); err != nil {
				rec.Step("-", "the check could run", func(Evidence) error { return err })
			}
			if len(r.run.Steps) == first {
				rec.Step("-", "the check recorded a step", func(Evidence) error {
					return errors.New("the check recorded no step")
				})
			}
		}
		for _, s := range r.run.Steps[first:] {
			switch {
			case s.Status == Fail && res.Status != Fail:
				res.Status, res.Reason = Fail, s.Error
			case s.Status == Skip && res.Status == Pass:
				res.Status, res.Reason = Skip, s.Error
			}
		}
		r.run.Checks = append(r.run.Checks, res)
	}
}
