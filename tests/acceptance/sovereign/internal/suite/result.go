package suite

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Status of one step.
const (
	Pass = "PASS"
	Fail = "FAIL"
	Skip = "SKIP"
)

// Evidence is the per-step bag of measured values. Every value in it is a
// thing the suite OBSERVED (an HTTP code, a resolved IP, a row count, a
// sha256) — never the thing it asked for.
type Evidence map[string]any

// Step is one asserted action.
type Step struct {
	Journey    string    `json:"journey"`
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMs int64     `json:"duration_ms"`
	Evidence   Evidence  `json:"evidence,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Producer says who wrote the record. Only a workflow record counts for a verdict.
type Producer struct {
	Kind       string `json:"kind"` // workflow | manual
	Workflow   string `json:"workflow,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	RunAttempt int    `json:"run_attempt,omitempty"`
}

// CheckResult is the result of one selected gate check.
type CheckResult struct {
	ID         string `json:"id"`
	StateAtRun string `json:"state_at_run"` // expected-fail | required
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	unmet      bool   // SKIP because the run did not declare the needs of the check
}

// ChartArtifact is one HelmRelease of one region: the chart version that is
// deployed and the version that the candidate pins ("" when it pins none).
type ChartArtifact struct {
	Region    string `json:"region"`
	Namespace string `json:"namespace"`
	Release   string `json:"release"`
	Chart     string `json:"chart"`
	Version   string `json:"version"`
	Pinned    string `json:"pinned"`

	// Check SVC-G06 fills these two: the Blueprint name of the chart and the
	// result of the function test of the newest revision (pass, fail or none).
	// They are left out when that check did not run or the test had not
	// finished.
	Blueprint    string `json:"blueprint,omitempty"`
	FunctionTest string `json:"functionTest,omitempty"`
}

// ImageArtifact is one image reference of one region with the digest that
// runs, and the number of containers that run it.
type ImageArtifact struct {
	Region     string `json:"region"`
	Ref        string `json:"ref"`
	Digest     string `json:"digest"`
	Containers int    `json:"containers"`
}

// Artifacts is what ran: every HelmRelease and every image of the platform
// namespaces, per region. Check FND-G01 observes it.
type Artifacts struct {
	Charts []ChartArtifact `json:"charts"`
	Images []ImageArtifact `json:"images"`
}

// ArtifactsKey is the evidence key under which a check hands a *Artifacts to
// the record. Finalize moves the value to the field Artifacts of the record.
const ArtifactsKey = "artifacts"

// ProofsKey is the evidence key under which check SVC-G06 hands the
// function-test results of one region to the record, as a
// map[string]ChartProof keyed by "<region>/<namespace>/<release>". Finalize
// copies each result onto the chart of the same release in the artifacts.
const ProofsKey = "proofs"

// ChartProof is the Blueprint name of one HelmRelease and the result of the
// function test of its newest revision: pass, fail or none.
type ChartProof struct {
	Blueprint    string `json:"blueprint"`
	FunctionTest string `json:"functionTest"`
}

// Provision is the part of the record about the installation under test. A run
// in the cloud always writes the Installation (provision.go); a run that
// follows a wipe writes the PreviousWipe. A nil part is left out of the record.
type Provision struct {
	*Installation
	*PreviousWipe
}

// Installation is what step D0.11 measured (provision.go).
type Installation struct {
	Provider  string            `json:"provider"`
	CreatedAt time.Time         `json:"created_at"`
	ReadyAt   time.Time         `json:"ready_at"`
	Regions   []ProvisionRegion `json:"regions"`
	Fresh     bool              `json:"fresh"`
}

// ProvisionRegion is one region of the installation.
type ProvisionRegion struct {
	ID             string `json:"id"`
	NodesRequested int    `json:"nodes_requested"`
	NodesReady     int    `json:"nodes_ready"`
}

// PreviousWipe is the wipe that this fire follows: the deployment that was
// wiped and how many of its resources the cloud account still listed after it.
type PreviousWipe struct {
	PreviousDeploymentID string `json:"previous_deployment_id"`
	PreviousWipeOrphans  int    `json:"previous_wipe_orphans"`
}

// Run is the JSON record written to docs/ledger/acceptance/<env>-<runid>.json.
// The schema is version 2. It is read by scripts/acceptance-gate.py (the
// table RECORD) and by scripts/acceptance_record.py: change all three together.
type Run struct {
	Schema       string            `json:"schema"`
	RunID        string            `json:"run_id"`
	Env          string            `json:"env"`
	Console      string            `json:"console"`
	DeploymentID string            `json:"deployment_id"`
	Mode         string            `json:"mode"`   // run | discover
	RunIn        string            `json:"run_in"` // local | cloud
	Capabilities []string          `json:"capabilities"`
	SuiteGitSHA  string            `json:"suite_git_sha,omitempty"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
	Result       string            `json:"result"`
	Candidate    Candidate         `json:"candidate"`
	Producer     Producer          `json:"producer"`
	Provision    *Provision        `json:"provision,omitempty"`
	Artifacts    *Artifacts        `json:"artifacts,omitempty"`
	Checks       []CheckResult     `json:"checks"`
	Journeys     map[string]string `json:"journeys"`
	Subject      Evidence          `json:"subject"` // what was tested: org/env/regions/app/blueprint/delivered images
	Steps        []*Step           `json:"steps"`
}

// SchemaVersion of the record.
const SchemaVersion = "openova.io/acceptance-sovereign/v2"

// NewRunID is <UTC timestamp>-<4 random hex>, sortable and unique.
func NewRunID(now time.Time) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// Finalize computes per-journey and overall results. A journey PASSes only if
// it has at least one step and every step PASSed — a journey that was
// skipped entirely is SKIP, and any SKIP of a REQUESTED journey makes the run
// FAIL: "not measured" never reads as green.
func (r *Run) Finalize(requested []string) {
	// The artifacts are observed by a check, which hands them over in the
	// evidence of one step. They belong to the record, not to the step.
	for _, s := range r.Steps {
		if a, ok := s.Evidence[ArtifactsKey].(*Artifacts); ok {
			r.Artifacts = a
			delete(s.Evidence, ArtifactsKey)
		}
	}
	// The function-test results stay in the evidence of their step; each one
	// is also written onto the chart of the same release.
	for _, s := range r.Steps {
		proofs, ok := s.Evidence[ProofsKey].(map[string]ChartProof)
		if !ok || r.Artifacts == nil {
			continue
		}
		for i := range r.Artifacts.Charts {
			c := &r.Artifacts.Charts[i]
			if p, found := proofs[c.Region+"/"+c.Namespace+"/"+c.Release]; found {
				c.Blueprint, c.FunctionTest = p.Blueprint, p.FunctionTest
			}
		}
	}
	r.Journeys = map[string]string{}
	for _, j := range requested {
		r.Journeys[j] = Skip
	}
	seen := map[string]bool{}
	for _, s := range r.Steps {
		if s.Journey == "D0" {
			continue
		}
		cur, ok := r.Journeys[s.Journey]
		if !ok {
			continue
		}
		if !seen[s.Journey] {
			seen[s.Journey] = true
			cur = Pass
		}
		switch {
		case s.Status == Fail:
			cur = Fail
		case s.Status == Skip && cur != Fail:
			cur = Skip
		}
		r.Journeys[s.Journey] = cur
	}
	r.Result = Pass
	for _, s := range r.Steps {
		if s.Journey == "D0" && s.Status != Pass {
			r.Result = Fail
		}
	}
	for _, j := range requested {
		if r.Journeys[j] != Pass {
			r.Result = Fail
		}
	}
	// A required check must pass. A skip passes only when the run did not
	// declare what the check needs.
	for _, c := range r.Checks {
		if c.StateAtRun == StateRequired && c.Status != Pass && !c.unmet {
			r.Result = Fail
		}
	}
	if r.Mode == "discover" {
		// discover asserts only reachability of the contract; it never
		// certifies function and its record is refused by the UAT gate.
		r.Journeys = map[string]string{}
	}
}

// PrintTable writes the human step table.
func (r *Run) PrintTable(w io.Writer) {
	fmt.Fprintf(w, "\nACCEPTANCE %s  env=%s  run=%s  result=%s\n", r.Mode, r.Env, r.RunID, r.Result)
	fmt.Fprintf(w, "%-4s %-6s %-6s %8s  %-58s %s\n", "J", "STEP", "RESULT", "SECONDS", "ASSERTION", "EVIDENCE / ERROR")
	fmt.Fprintln(w, strings.Repeat("-", 140))
	for _, s := range r.Steps {
		detail := s.Error
		if detail == "" {
			detail = evidenceLine(s.Evidence)
		}
		fmt.Fprintf(w, "%-4s %-6s %-6s %8.1f  %-58s %s\n", s.Journey, s.ID, s.Status,
			float64(s.DurationMs)/1000, trunc(s.Name, 58), trunc(detail, 200))
	}
	fmt.Fprintln(w, strings.Repeat("-", 140))
	keys := make([]string, 0, len(r.Journeys))
	for k := range r.Journeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %s %s\n", k, r.Journeys[k])
	}
	for _, c := range r.Checks {
		_, _ = fmt.Fprintf(w, "  %s %s (%s) %s\n", c.ID, c.Status, c.StateAtRun, c.Reason)
	}
	fmt.Fprintf(w, "RESULT %s\n", r.Result)
}

func evidenceLine(e Evidence) string {
	if len(e) == 0 {
		return ""
	}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		b, _ := json.Marshal(e[k])
		parts = append(parts, k+"="+string(b))
	}
	return strings.Join(parts, " ")
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// Write stores the record at <dir>/<env>-<runid>.json and returns the path.
func (r *Run) Write(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("%s-%s.json", r.Env, r.RunID))
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return p, os.WriteFile(p, append(b, '\n'), 0o644)
}
