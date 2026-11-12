package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/wordpress"
)

// runner holds the state threaded through the journeys.
type runner struct {
	cfg Config
	out io.Writer
	api *api.Client
	run *Run

	dep, fqdn string
	A, B      kube.Region // A starts as Primary, B as Standby
	regionIPs map[string][]string
	// sharedIngressIPs — load balancers that front every region (discover).
	sharedIngressIPs []string
	org              OrgItem
	env              EnvInfo

	app app
}

// app is the Application under test and what was written into it.
type app struct {
	Name, Namespace, UID, FQDN string
	Created                    bool
	DBName                     string
	Site                       *wordpress.Site
	Posts                      []writtenPost // every post this run wrote, checked after each move
	MediaURL, MediaSHA         string
	MediaID                    int
}

type writtenPost struct {
	ID     int
	Marker string
	Region string // region that was Primary when it was written
}

// Execute runs discover or the requested journeys and returns the record.
func Execute(ctx context.Context, cfg Config, out io.Writer) (*Run, error) {
	if cfg.Env == "" {
		return nil, errors.New("--env is required (the environment named in the UAT header, e.g. hw307)")
	}
	if cfg.Console == "" {
		cfg.Console = "https://console." + cfg.Env + ".omani.works"
	}
	if cfg.Session == "" {
		return nil, errors.New("CATALYST_SESSION is not set (see README: scripts/acceptance-sovereign.sh mints one when ACCEPT_MINT=1)")
	}
	if cfg.KubeconfigA == "" || cfg.KubeconfigB == "" {
		return nil, errors.New("--kubeconfig-a and --kubeconfig-b are required (region assertions are made per cluster)")
	}
	mode := "run"
	if cfg.ReadOnly {
		mode = "discover"
	}
	// The checks are selected before anything runs: a manifest that cannot be
	// read, or an entry without a registered check, stops the run at once.
	// A discover run certifies nothing and runs no check.
	var manifest *Manifest
	var checks []Check
	if cfg.Manifest != "" && !cfg.ReadOnly {
		var err error
		if manifest, err = LoadManifest(cfg.Manifest); err != nil {
			return nil, err
		}
		if checks, err = SelectChecks(manifest, RunIn(cfg.RunIn)); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	r := &runner{
		cfg: cfg, out: out,
		api: api.New(cfg.Console, cfg.Session),
		run: &Run{Schema: SchemaVersion, RunID: NewRunID(now), Env: cfg.Env, Console: cfg.Console, Mode: mode,
			StartedAt: now, Subject: Evidence{}, SuiteGitSHA: gitSHA(),
			RunIn: cfg.RunIn, Capabilities: splitList(cfg.Capabilities), Checks: []CheckResult{},
			Candidate: Candidate{ID: cfg.CandidateID, Commit: headCommit(cfg.RepoRoot)},
			Producer:  producerFromEnv(os.Getenv)},
		A: kube.Region{Kubeconfig: cfg.KubeconfigA},
		B: kube.Region{Kubeconfig: cfg.KubeconfigB},
	}
	r.api.ReadOnly = cfg.ReadOnly
	fmt.Fprintf(out, "acceptance %s env=%s run=%s console=%s\n", mode, cfg.Env, r.run.RunID, cfg.Console)

	requested := []string{}
	if !cfg.ReadOnly {
		for _, j := range strings.Split(cfg.Journeys, ",") {
			if j = strings.TrimSpace(strings.ToUpper(j)); j != "" {
				requested = append(requested, j)
			}
		}
	}
	want := map[string]bool{}
	for _, j := range requested {
		want[j] = true
	}

	ok := r.discover(ctx)
	switch {
	case cfg.ReadOnly:
		r.probeReadOnly(ctx)
	case !ok:
		for _, j := range requested {
			r.skip(j, "discovery (D0) failed — nothing to drive")
		}
	default:
		r.journeys(ctx, want)
	}
	if manifest != nil {
		cc := &CheckContext{RunIn: RunIn(cfg.RunIn), Regions: []kube.Region{r.A, r.B}, API: r.api,
			Candidate: r.run.Candidate, RepoRoot: cfg.RepoRoot, Deploy: &DeploymentInfo{ID: r.dep}, Caps: map[string]bool{}}
		for _, c := range r.run.Capabilities {
			cc.Caps[c] = true
		}
		r.runChecks(ctx, manifest, checks, cc, ok)
	}

	r.run.FinishedAt = time.Now().UTC()
	r.run.Finalize(requested)
	r.run.PrintTable(out)
	if cfg.Record {
		if cfg.ReadOnly {
			fmt.Fprintln(out, "note: --record ignored in discover mode (a discover run certifies nothing)")
		} else {
			p, err := r.run.Write(cfg.RecordDir)
			if err != nil {
				return r.run, fmt.Errorf("record: %w", err)
			}
			fmt.Fprintf(out, "recorded %s\n", p)
		}
	}
	return r.run, nil
}

func (r *runner) journeys(ctx context.Context, want map[string]bool) {
	if want["J1"] && !r.j1(ctx) {
		for _, j := range []string{"J2", "J3", "J4"} {
			if want[j] {
				r.skip(j, "J1 failed — no working install to drive")
			}
		}
	} else {
		ok2 := !want["J2"] || r.j2(ctx)
		if want["J3"] {
			if !ok2 {
				r.skip("J3", "J2 failed — no verified data to carry across the move")
				if want["J4"] {
					r.skip("J4", "J3 not run")
				}
			} else {
				ok3 := r.move(ctx, "J3", r.B, r.A)
				if want["J4"] {
					if ok3 {
						r.move(ctx, "J4", r.A, r.B)
					} else {
						r.skip("J4", "J3 failed — the app is not in B to move back")
					}
				}
			}
		}
	}
	if want["J5"] {
		failedSoFar := false
		for _, s := range r.run.Steps {
			if s.Status == Fail {
				failedSoFar = true
			}
		}
		switch {
		case r.cfg.KeepOnFail && failedSoFar:
			r.skip("J5", "--keep-on-fail: an earlier step failed; "+r.app.Name+" left in place for debugging")
		case r.app.Name == "" || !r.app.Created:
			r.skip("J5", "no Application was created by this run")
		default:
			r.j5(ctx)
		}
	}
}

// step runs one assertion, records it, and prints it live.
func (r *runner) step(journey, id, name string, fn func(ev Evidence) error) bool {
	s := &Step{Journey: journey, ID: id, Name: name, StartedAt: time.Now().UTC(), Evidence: Evidence{}}
	fmt.Fprintf(r.out, "  %s %-6s … %s\n", journey, id, name)
	err := fn(s.Evidence)
	s.FinishedAt = time.Now().UTC()
	s.DurationMs = s.FinishedAt.Sub(s.StartedAt).Milliseconds()
	if err != nil {
		s.Status, s.Error = Fail, err.Error()
		fmt.Fprintf(r.out, "  %s %-6s FAIL %s\n", journey, id, err)
	} else {
		s.Status = Pass
		fmt.Fprintf(r.out, "  %s %-6s PASS %s\n", journey, id, trunc(evidenceLine(s.Evidence), 200))
	}
	r.run.Steps = append(r.run.Steps, s)
	return err == nil
}

func (r *runner) skip(journey, why string) {
	now := time.Now().UTC()
	r.run.Steps = append(r.run.Steps, &Step{Journey: journey, ID: "-", Name: "journey not run", Status: Skip,
		StartedAt: now, FinishedAt: now, Error: why})
	fmt.Fprintf(r.out, "  %s SKIP %s\n", journey, why)
}

// poll calls f every interval until it returns done, an error, or timeout.
// On timeout the LAST observed detail is returned in the error, so a failure
// says what the system was doing, not just that time ran out.
func poll(ctx context.Context, timeout, interval time.Duration, f func() (bool, string, error)) (string, error) {
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		done, detail, err := f()
		if detail != "" {
			last = detail
		}
		if err != nil {
			return last, err
		}
		if done {
			return last, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("timed out after %s; last observed: %s", timeout, nonEmpty(last, "nothing"))
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func gitSHA() string {
	if s := os.Getenv("GITHUB_SHA"); s != "" {
		return s
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
