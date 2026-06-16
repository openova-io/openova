// store.go — flat-file persistence for Jobs + Executions + LogLines.
//
// Three on-disk artefacts per deployment:
//
//   - <dir>/<deploymentId>/index.json       — atomic temp+rename, holds
//     the Job + Execution
//     metadata.
//   - <dir>/<deploymentId>/<execId>.log     — append-only NDJSON, one
//     LogLine per line.
//   - The directory itself is created at 0o700 the first time the
//     store touches a deployment.
//
// Atomicity: every persistIndex call writes to a sibling temp file then
// os.Rename. Concurrent calls are serialised under Store.mu so the
// rename is the linearisation point — a crash mid-write leaves the old
// index intact (or, on first write, a missing file the load path
// treats as "no jobs yet").
//
// NDJSON append: opened O_APPEND on every LogLines write. The store
// holds Store.mu around the open+write+close so concurrent writers
// can't interleave bytes (NDJSON is line-oriented; partial writes
// would corrupt parsing).
package jobs

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultDir — the on-PVC path the chart already mounts at
// /var/lib/catalyst (see products/catalyst/chart/templates/api-deployment.yaml).
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the path is configuration, not
// code; the env var CATALYST_EXECUTIONS_DIR overrides it.
const DefaultDir = "/var/lib/catalyst/executions"

// EnvDir is the env var the catalyst-api main reads to override the
// store directory. Empty / unset falls back to DefaultDir.
const EnvDir = "CATALYST_EXECUTIONS_DIR"

// indexFileName — the per-deployment metadata file.
const indexFileName = "index.json"

// MaxLogPageSize — upper bound the API enforces on the /logs
// pagination `limit` query param. The wire spec in #205 documents the
// same number. Hardcoded here so the store's pagination helper agrees
// with the handler.
const MaxLogPageSize = 5000

// DefaultLogPageSize — default `limit` when the caller omits the query
// param.
const DefaultLogPageSize = 500

// ErrNotFound is returned when the requested Job, Execution, or
// Deployment doesn't exist in the store. Callers map this onto HTTP
// 404; tests assert on errors.Is.
var ErrNotFound = errors.New("jobs: not found")

// Store is the flat-file persistence layer for Jobs + Executions +
// LogLines. Construct via NewStore; Close is a no-op (no FDs are kept
// open between calls).
//
// All writes are serialised under mu — the store is designed for
// dozens of writes/sec from a single helmwatch goroutine, not high-
// concurrency log ingestion. Reads also take mu so a partially-written
// index can never be observed by GET /jobs.
type Store struct {
	dir string

	mu sync.Mutex
}

// NewStore returns a Store rooted at dir, creating the directory at
// 0o700 if missing. A failure to create the root directory is fatal —
// production manifests guarantee the PVC exists, and a CI runner
// without write access surfaces an unmistakable error rather than
// silently dropping logs.
func NewStore(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("jobs: store directory is required (set CATALYST_EXECUTIONS_DIR or pass DefaultDir)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("jobs: create store dir %q: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the absolute root path the Store persists to. Used by
// log-paths the handler renders into operator diagnostics.
func (s *Store) Dir() string {
	return s.dir
}

// deploymentDir returns the per-deployment subdirectory, ensuring it
// exists at 0o700. Called from every mutator under s.mu.
func (s *Store) deploymentDir(deploymentID string) (string, error) {
	if strings.TrimSpace(deploymentID) == "" {
		return "", errors.New("jobs: deploymentID is required")
	}
	// Disallow path-traversal — the deploymentId comes from
	// CreateDeployment which uses crypto/rand hex, but defence-in-
	// depth: reject any id that contains a path separator.
	if strings.ContainsAny(deploymentID, "/\\") {
		return "", fmt.Errorf("jobs: invalid deploymentID %q", deploymentID)
	}
	d := filepath.Join(s.dir, deploymentID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", fmt.Errorf("jobs: create deployment dir %q: %w", d, err)
	}
	return d, nil
}

// loadIndex reads <depDir>/index.json. Returns a fresh zero-Index when
// the file is missing — that's a "no jobs yet" deployment, not an
// error. Caller MUST hold s.mu.
func (s *Store) loadIndex(deploymentID string) (*Index, error) {
	depDir, err := s.deploymentDir(deploymentID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(depDir, indexFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Index{
				DeploymentID: deploymentID,
				Jobs:         []Job{},
				Executions:   []Execution{},
			}, nil
		}
		return nil, fmt.Errorf("jobs: read index %q: %w", path, err)
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("jobs: decode index %q: %w", path, err)
	}
	if idx.Jobs == nil {
		idx.Jobs = []Job{}
	}
	if idx.Executions == nil {
		idx.Executions = []Execution{}
	}
	idx.DeploymentID = deploymentID

	// Legacy migration (#351). Pre-refactor indexes persist `batchId`
	// where the new shape uses `parentId`. Hoist non-empty legacy
	// values into ParentID so deriveTreeView sees the relationship
	// without a separate one-shot data migration. The legacy field is
	// then cleared so persistIndex doesn't echo it on disk — next
	// write produces a canonical record.
	for i := range idx.Jobs {
		if idx.Jobs[i].ParentID == "" && idx.Jobs[i].LegacyBatchID != "" {
			idx.Jobs[i].ParentID = JobID(deploymentID, idx.Jobs[i].LegacyBatchID)
		}
		idx.Jobs[i].LegacyBatchID = ""
		// Pre-refactor leaves also have empty Type — every persisted
		// job before #351 was a leaf install. Default to install so
		// the recursive Job model contracts (childIds derivation,
		// status rollup) hold.
		if idx.Jobs[i].Type == "" {
			idx.Jobs[i].Type = JobTypeInstall
		}
	}
	return &idx, nil
}

// persistIndex writes idx to <depDir>/index.json via atomic
// temp+rename. The temp file is written at 0o600 so concurrent readers
// either see the old version or the new one — never a partial write.
// Caller MUST hold s.mu.
func (s *Store) persistIndex(idx *Index) error {
	depDir, err := s.deploymentDir(idx.DeploymentID)
	if err != nil {
		return err
	}
	final := filepath.Join(depDir, indexFileName)

	// Strip derived fields before serialising — ChildIDs is recomputed
	// on read by deriveTreeView. Persisting it would waste disk space
	// and risks the on-disk copy drifting from the live tree if a
	// future writer forgets to recompute.
	persisted := *idx
	persisted.Jobs = make([]Job, len(idx.Jobs))
	copy(persisted.Jobs, idx.Jobs)
	for i := range persisted.Jobs {
		// ChildIDs is derived; LegacyBatchID is the legacy migration
		// hook — both must be cleared so the on-disk record stays
		// canonical (one source of truth: ParentID).
		persisted.Jobs[i].ChildIDs = nil
		persisted.Jobs[i].LegacyBatchID = ""
	}

	raw, err := json.MarshalIndent(&persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("jobs: marshal index: %w", err)
	}

	tmp, err := os.CreateTemp(depDir, ".index-*.json.tmp")
	if err != nil {
		return fmt.Errorf("jobs: create temp index: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("jobs: write temp index %q: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("jobs: fsync temp index %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("jobs: close temp index %q: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("jobs: chmod temp index %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("jobs: rename temp index → %q: %w", final, err)
	}
	cleanup = false
	return nil
}

// UpsertJob inserts or updates the Job with id JobID(deploymentID,
// jobName). The supplied Job's ID + DeploymentID are stamped from the
// arguments — callers don't have to spell them.
//
// The merge keeps StartedAt + FinishedAt monotonic: a re-emission with
// nil StartedAt won't clobber a previously-stamped one. The frontend
// never sees a job "un-start".
func (s *Store) UpsertJob(j Job) error {
	if strings.TrimSpace(j.DeploymentID) == "" {
		return errors.New("jobs: UpsertJob: deploymentID is required")
	}
	if strings.TrimSpace(j.JobName) == "" {
		return errors.New("jobs: UpsertJob: jobName is required")
	}
	j.ID = JobID(j.DeploymentID, j.JobName)
	if j.DependsOn == nil {
		j.DependsOn = []string{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(j.DeploymentID)
	if err != nil {
		return err
	}
	for i := range idx.Jobs {
		if idx.Jobs[i].ID == j.ID {
			merged := mergeJob(idx.Jobs[i], j)
			idx.Jobs[i] = merged
			return s.persistIndex(idx)
		}
	}
	idx.Jobs = append(idx.Jobs, j)
	return s.persistIndex(idx)
}

// mergeJob keeps monotonic timestamps + the latest non-empty
// LatestExecutionID. The status from the new event always wins (the
// helmwatch bridge is the only writer; later state-machine events
// supersede earlier ones).
//
// DependsOn preservation: when the incoming event has empty DependsOn,
// keep the previously-stored list. The Bridge's per-HR state-transition
// path (OnHelmReleaseEvent at helmwatch_bridge.go:502) hardcodes
// `DependsOn: []string{}` because it does not look up sibling
// dependencies on every event — only the seed path
// (SeedJobsFromInformerList) computes the real list from
// HR.spec.dependsOn. Without this preservation, every subsequent state
// transition CLOBBERS the seeded deps and the canvas snapshot's
// inter-HR dep edges disappear after the first state change. Caught on
// prov #73 (8cd1ff1a80430dc5, 2026-05-14): 135/135 install Jobs ended
// up with `dependsOn=[]` despite SeedJobsFromInformerList running with
// proper deps, because every HR Ready=True event after the seed wrote
// empty deps. Founder caught the resulting flat-leaf canvas 4 sessions
// in a row.
func mergeJob(prev, next Job) Job {
	out := next
	if next.StartedAt == nil && prev.StartedAt != nil {
		out.StartedAt = prev.StartedAt
	}
	if next.FinishedAt == nil && prev.FinishedAt != nil {
		out.FinishedAt = prev.FinishedAt
	}
	if next.LatestExecutionID == "" && prev.LatestExecutionID != "" {
		out.LatestExecutionID = prev.LatestExecutionID
	}
	if len(next.DependsOn) == 0 && len(prev.DependsOn) > 0 {
		out.DependsOn = prev.DependsOn
	}
	// Carry forward Region — the seed fan-out stamps it once, but a
	// later OnHelmReleaseEvent transition merge passes a Job with
	// Region="" (the event path doesn't recompute the region). Without
	// this preservation the multi-region Region column would blank out
	// on the first state transition after the seed, regressing the
	// per-region job picture this field exists to surface. Same shape
	// as the DependsOn preservation directly above.
	if next.Region == "" && prev.Region != "" {
		out.Region = prev.Region
	}
	if out.StartedAt != nil && out.FinishedAt != nil {
		out.DurationMs = out.FinishedAt.Sub(*out.StartedAt).Milliseconds()
	}
	return out
}

// StartExecution allocates a new Execution row for the given Job and
// stamps the Job's LatestExecutionID + StartedAt + Status=running. The
// returned Execution.ID is the path-segment component the /logs
// endpoint accepts. Caller is responsible for writing the matching
// Job upsert with appId/parentId metadata BEFORE the first
// StartExecution — the store does not back-fill those fields.
func (s *Store) StartExecution(deploymentID, jobName string, startedAt time.Time) (Execution, error) {
	if strings.TrimSpace(deploymentID) == "" {
		return Execution{}, errors.New("jobs: StartExecution: deploymentID is required")
	}
	if strings.TrimSpace(jobName) == "" {
		return Execution{}, errors.New("jobs: StartExecution: jobName is required")
	}

	jobID := JobID(deploymentID, jobName)
	execID, err := newExecutionID()
	if err != nil {
		return Execution{}, err
	}

	exec := Execution{
		ID:           execID,
		JobID:        jobID,
		DeploymentID: deploymentID,
		Status:       StatusRunning,
		StartedAt:    startedAt.UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return Execution{}, err
	}
	idx.Executions = append(idx.Executions, exec)
	// Stamp the Job's LatestExecutionID + flip Status=running so the
	// table view reflects the in-flight attempt without a separate
	// UpsertJob call from the bridge.
	for i := range idx.Jobs {
		if idx.Jobs[i].ID == jobID {
			started := startedAt.UTC()
			if idx.Jobs[i].StartedAt == nil {
				idx.Jobs[i].StartedAt = &started
			}
			idx.Jobs[i].Status = StatusRunning
			idx.Jobs[i].LatestExecutionID = execID
			break
		}
	}
	if err := s.persistIndex(idx); err != nil {
		return Execution{}, err
	}
	return exec, nil
}

// FinishExecution flips an Execution's Status + FinishedAt + flips the
// parent Job into the corresponding terminal state. status must be
// StatusSucceeded or StatusFailed.
func (s *Store) FinishExecution(deploymentID, execID, status string, finishedAt time.Time) error {
	if !IsTerminal(status) {
		return fmt.Errorf("jobs: FinishExecution: status must be terminal, got %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return err
	}
	finished := finishedAt.UTC()
	var jobID string
	found := false
	for i := range idx.Executions {
		if idx.Executions[i].ID == execID {
			idx.Executions[i].Status = status
			idx.Executions[i].FinishedAt = &finished
			jobID = idx.Executions[i].JobID
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("jobs: FinishExecution: execution %q: %w", execID, ErrNotFound)
	}
	for i := range idx.Jobs {
		if idx.Jobs[i].ID == jobID {
			idx.Jobs[i].Status = status
			idx.Jobs[i].FinishedAt = &finished
			if idx.Jobs[i].StartedAt != nil {
				idx.Jobs[i].DurationMs = finished.Sub(*idx.Jobs[i].StartedAt).Milliseconds()
			}
			break
		}
	}
	return s.persistIndex(idx)
}

// AppendLogLines appends one or more LogLines to the per-execution
// NDJSON file. Stamps LineNumber 1-indexed, monotonic across calls.
// Updates the parent Execution's LineCount under the same lock so
// subsequent /logs?total reflects the new ceiling.
//
// Lines is a slice so a bridge that emits batched events (e.g. one
// state transition + a derived "Helm install in progress" log line)
// can persist them in a single write.
func (s *Store) AppendLogLines(deploymentID, execID string, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return err
	}
	var exec *Execution
	for i := range idx.Executions {
		if idx.Executions[i].ID == execID {
			exec = &idx.Executions[i]
			break
		}
	}
	if exec == nil {
		return fmt.Errorf("jobs: AppendLogLines: execution %q: %w", execID, ErrNotFound)
	}

	depDir, err := s.deploymentDir(deploymentID)
	if err != nil {
		return err
	}
	logPath := filepath.Join(depDir, execID+".log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("jobs: open log %q: %w", logPath, err)
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	startLine := exec.LineCount
	for i := range lines {
		startLine++
		lines[i].LineNumber = startLine
		if lines[i].Timestamp.IsZero() {
			lines[i].Timestamp = time.Now().UTC()
		} else {
			lines[i].Timestamp = lines[i].Timestamp.UTC()
		}
		if lines[i].Level == "" {
			lines[i].Level = LevelInfo
		}
		raw, err := json.Marshal(lines[i])
		if err != nil {
			return fmt.Errorf("jobs: marshal log line: %w", err)
		}
		if _, err := bw.Write(raw); err != nil {
			return fmt.Errorf("jobs: write log %q: %w", logPath, err)
		}
		if err := bw.WriteByte('\n'); err != nil {
			return fmt.Errorf("jobs: write log newline %q: %w", logPath, err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("jobs: flush log %q: %w", logPath, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("jobs: fsync log %q: %w", logPath, err)
	}
	exec.LineCount = startLine
	return s.persistIndex(idx)
}

// ListJobs returns every Job for the deployment, sorted started-at
// DESC with pending Jobs (no StartedAt) bucketed last. Group jobs
// (Type == JobTypeGroup) get their Status / StartedAt / FinishedAt /
// DurationMs derived from descendants at read time; ChildIDs is
// always derived. The handler returns the slice unchanged.
func (s *Store) ListJobs(deploymentID string) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return nil, err
	}
	out := make([]Job, len(idx.Jobs))
	copy(out, idx.Jobs)
	out = deriveTreeView(out)
	sort.SliceStable(out, func(i, j int) bool {
		// Pending (no StartedAt) sort last.
		ai, bi := out[i].StartedAt, out[j].StartedAt
		switch {
		case ai == nil && bi == nil:
			return out[i].JobName < out[j].JobName
		case ai == nil:
			return false
		case bi == nil:
			return true
		}
		// started-at DESC: more-recent first.
		if ai.Equal(*bi) {
			return out[i].JobName < out[j].JobName
		}
		return ai.After(*bi)
	})
	return out, nil
}

// deriveTreeView returns the supplied slice (possibly extended with
// synthesized parent group rows) with ChildIDs populated on every Job
// and Status / StartedAt / FinishedAt / DurationMs rolled up on every
// group Job from its descendants.
//
// Rollup rules (group jobs only — leaf jobs are untouched):
//
//   - Status: failed > running > pending > succeeded. A group with at
//     least one failed descendant is failed; otherwise running if any
//     descendant is running; otherwise pending if any descendant is
//     pending; otherwise succeeded.
//   - StartedAt: earliest non-nil StartedAt across all descendants.
//   - FinishedAt: latest FinishedAt across all descendants — but only
//     populated when every descendant has terminated (succeeded or
//     failed).
//   - DurationMs: FinishedAt - StartedAt when both are non-nil; else 0.
//
// Synthesis of missing parents (#351 legacy migration): every leaf
// whose ParentID points at an id without a corresponding on-disk Job
// row triggers an in-memory synthesized group Job — so old
// deployments (whose pre-refactor index has no parent rows) still
// render the parent relationship in the canvas + table without a
// separate one-shot data migration.
//
// The walk is post-order via index lookup so a 3-level tree
// (root group → mid group → leaf) rolls up correctly without
// recursion.
func deriveTreeView(jobs []Job) []Job {
	if len(jobs) == 0 {
		return jobs
	}

	// Synthesize a group Job for every ParentID that doesn't already
	// have an on-disk row. The synthesized rows append to the slice
	// before the rollup pass so they participate in childIds /
	// status / timing aggregation just like a real on-disk parent.
	have := make(map[string]bool, len(jobs))
	for i := range jobs {
		have[jobs[i].ID] = true
	}
	for i := range jobs {
		pid := jobs[i].ParentID
		if pid == "" || have[pid] {
			continue
		}
		// Derive the slug from the canonical "<deploymentId>:<slug>"
		// id format. Falls back to the full pid when the format
		// doesn't match (defence-in-depth — should never happen for
		// helmwatch-bridge writes).
		slug := pid
		if c := strings.LastIndex(pid, ":"); c >= 0 && c+1 < len(pid) {
			slug = pid[c+1:]
		}
		display := slug
		switch slug {
		case GroupBootstrapKit:
			display = GroupBootstrapKitDisplay
		case GroupDay2Mutations:
			display = GroupDay2MutationsDisplay
		}
		jobs = append(jobs, Job{
			ID:           pid,
			DeploymentID: jobs[i].DeploymentID,
			JobName:      slug,
			DisplayName:  display,
			Type:         JobTypeGroup,
			ParentID:     "",
			DependsOn:    []string{},
			Status:       StatusPending,
		})
		have[pid] = true
	}

	idx := make(map[string]int, len(jobs))
	for i := range jobs {
		idx[jobs[i].ID] = i
		jobs[i].ChildIDs = nil
	}
	// Build adjacency: parent ID → list of child indexes (children of
	// the indexed parent's ID).
	children := make(map[int][]int, len(jobs))
	for i := range jobs {
		pid := jobs[i].ParentID
		if pid == "" {
			continue
		}
		pi, ok := idx[pid]
		if !ok {
			continue
		}
		children[pi] = append(children[pi], i)
		jobs[pi].ChildIDs = append(jobs[pi].ChildIDs, jobs[i].ID)
	}
	// Topological-by-depth iteration: process jobs in order of
	// ascending parent-chain length so descendants are settled
	// before ancestors. Compute depth lazily via a memo.
	depth := make([]int, len(jobs))
	var computeDepth func(int) int
	visiting := make([]bool, len(jobs))
	computeDepth = func(i int) int {
		if depth[i] > 0 {
			return depth[i]
		}
		if visiting[i] {
			// Cycle defence — should never happen since bridges only
			// ever set ParentID to a known group's ID, but never let
			// a malformed index crash the read path.
			return 1
		}
		visiting[i] = true
		pid := jobs[i].ParentID
		if pid == "" {
			depth[i] = 1
			visiting[i] = false
			return 1
		}
		pi, ok := idx[pid]
		if !ok {
			depth[i] = 1
			visiting[i] = false
			return 1
		}
		d := computeDepth(pi) + 1
		depth[i] = d
		visiting[i] = false
		return d
	}
	for i := range jobs {
		computeDepth(i)
	}
	// Process deepest first so a group sees its children's already-
	// rolled-up status when it's its turn.
	order := make([]int, len(jobs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return depth[order[a]] > depth[order[b]]
	})
	for _, i := range order {
		if jobs[i].Type != JobTypeGroup {
			continue
		}
		kids := children[i]
		if len(kids) == 0 {
			// Empty group — show as pending so the operator can tell
			// it's allocated but has no installs underneath.
			jobs[i].Status = StatusPending
			jobs[i].StartedAt = nil
			jobs[i].FinishedAt = nil
			jobs[i].DurationMs = 0
			continue
		}
		var earliest, latest *time.Time
		hasFailed, hasRunning, hasPending := false, false, false
		allTerminal := true
		for _, ki := range kids {
			c := jobs[ki]
			switch c.Status {
			case StatusFailed:
				hasFailed = true
			case StatusRunning:
				hasRunning = true
				allTerminal = false
			case StatusPending, "":
				hasPending = true
				allTerminal = false
			}
			if c.StartedAt != nil {
				if earliest == nil || c.StartedAt.Before(*earliest) {
					t := *c.StartedAt
					earliest = &t
				}
			}
			if c.FinishedAt != nil {
				if latest == nil || c.FinishedAt.After(*latest) {
					t := *c.FinishedAt
					latest = &t
				}
			}
		}
		switch {
		case hasFailed:
			jobs[i].Status = StatusFailed
		case hasRunning:
			jobs[i].Status = StatusRunning
		case hasPending:
			jobs[i].Status = StatusPending
		default:
			jobs[i].Status = StatusSucceeded
		}
		jobs[i].StartedAt = earliest
		if allTerminal {
			jobs[i].FinishedAt = latest
		} else {
			jobs[i].FinishedAt = nil
		}
		if jobs[i].StartedAt != nil && jobs[i].FinishedAt != nil {
			jobs[i].DurationMs = jobs[i].FinishedAt.Sub(*jobs[i].StartedAt).Milliseconds()
		} else {
			jobs[i].DurationMs = 0
		}
	}

	// Cascading-failure propagation (founder fail-fast directive,
	// 2026-05-03): when a leaf job depends on another leaf that has
	// already terminally Failed, the dependent must surface as Failed
	// at read time too — never sit at Pending forever waiting on a
	// dead dependency.
	//
	// Without this pass, otech34's install-external-secrets stayed
	// Pending while install-openbao was Failed, "masking it and
	// waiting unnecessarily" (founder's words).
	//
	// Iterative fixpoint: a single sweep handles direct dependencies;
	// subsequent sweeps propagate transitive failure (failed → blocks
	// dependent → that dependent's dependents). 8 sweeps cover the
	// deepest bootstrap-kit chain (cilium → cert-manager → flux →
	// crossplane → keycloak → gitea → catalyst-platform = 7 hops).
	byJobName := make(map[string]int, len(jobs))
	for i := range jobs {
		if jobs[i].Type == JobTypeGroup {
			continue
		}
		byJobName[jobs[i].JobName] = i
	}
	for sweep := 0; sweep < 8; sweep++ {
		changed := false
		for i := range jobs {
			if jobs[i].Type == JobTypeGroup {
				continue
			}
			if jobs[i].Status == StatusFailed || jobs[i].Status == StatusSucceeded {
				continue
			}
			for _, depName := range jobs[i].DependsOn {
				di, ok := byJobName[depName]
				if !ok {
					continue
				}
				if jobs[di].Status == StatusFailed {
					jobs[i].Status = StatusFailed
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	return jobs
}

// GetJob returns the Job + its Executions list. ErrNotFound if no Job
// with the given id exists for the deployment. The returned Job
// carries derived ChildIDs (and, for group jobs, rolled-up status /
// timing) — the same view ListJobs returns.
func (s *Store) GetJob(deploymentID, jobID string) (Job, []Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return Job{}, nil, err
	}
	// Run the tree-view derivation across the full slice so the
	// returned Job reflects the same derived shape ListJobs emits.
	view := make([]Job, len(idx.Jobs))
	copy(view, idx.Jobs)
	view = deriveTreeView(view)

	// Lookup accepts EITHER the full "<deploymentId>:<jobName>" id OR
	// the bare jobName. The colon in the canonical id is path-safe per
	// RFC 3986 §3.3 but Traefik (or some upstream proxy) was observed
	// returning 404 on URL-encoded colons in the path segment, so the
	// FE may send the bare jobName. (depID, jobName) is unique per
	// deployment, so the bare-name lookup is always unambiguous.
	for i := range view {
		j := view[i]
		if j.ID == jobID || j.JobName == jobID {
			execs := []Execution{}
			for _, e := range idx.Executions {
				if e.JobID == j.ID {
					execs = append(execs, e)
				}
			}
			sort.Slice(execs, func(a, b int) bool {
				return execs[a].StartedAt.After(execs[b].StartedAt)
			})
			return j, execs, nil
		}
	}
	return Job{}, nil, fmt.Errorf("jobs: GetJob %q: %w", jobID, ErrNotFound)
}

// GetExecution returns the Execution metadata + the parent
// deploymentID for resolving the log file path. The /logs endpoint
// uses this so the URL only carries the executionID, not the
// deployment id (matching the spec's
// /api/v1/actions/executions/{execId}/logs shape).
func (s *Store) FindExecution(deploymentID, execID string) (Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return Execution{}, err
	}
	for _, e := range idx.Executions {
		if e.ID == execID {
			return e, nil
		}
	}
	return Execution{}, fmt.Errorf("jobs: FindExecution %q: %w", execID, ErrNotFound)
}

// FindExecutionAcrossDeployments scans every <depId>/index.json under
// the store root for an execution with the given id. Used by the
// /api/v1/actions/executions/{execId}/logs endpoint where the URL
// does not carry the deploymentID — see the contract spec in #205.
//
// Returns the Execution + its DeploymentID. Stops scanning at the
// first match. ErrNotFound when no deployment has it.
func (s *Store) FindExecutionAcrossDeployments(execID string) (Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Execution{}, fmt.Errorf("jobs: FindExecutionAcrossDeployments %q: %w", execID, ErrNotFound)
		}
		return Execution{}, fmt.Errorf("jobs: scan store dir %q: %w", s.dir, err)
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		idx, err := s.loadIndex(ent.Name())
		if err != nil {
			// A single corrupt deployment must not poison the lookup;
			// the API returns 404 only if NO deployment matches.
			continue
		}
		for _, e := range idx.Executions {
			if e.ID == execID {
				return e, nil
			}
		}
	}
	return Execution{}, fmt.Errorf("jobs: FindExecutionAcrossDeployments %q: %w", execID, ErrNotFound)
}

// DeploymentIDs returns the id of every deployment the store has an
// on-disk index for (one subdirectory per deployment). Order is the
// filesystem ReadDir order (lexical on most filesystems). A missing
// store root is not an error — it returns an empty slice (the store was
// constructed but no deployment has been written yet).
//
// Used by activity sources that run on a Sovereign chroot and need to
// resolve which deployment id to project under WITHOUT depending on the
// handler's in-memory deployment map: the chroot's store carries exactly
// the deployment imported at handover (HandleJobsImport writes it under
// the mother's id), so the cutover/activity bridge can find its target
// by scanning for a deployment that already has the imported
// bootstrap-kit group. The caller does the group-presence check via
// ListJobs; this method only enumerates.
func (s *Store) DeploymentIDs() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("jobs: scan store dir %q: %w", s.dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			out = append(out, ent.Name())
		}
	}
	return out, nil
}

// LogPage is the wire-contract response shape for the /logs endpoint.
// Defined in the store package so the handler doesn't have to
// re-declare it.
type LogPage struct {
	Lines             []LogLine `json:"lines"`
	Total             int       `json:"total"`
	ExecutionFinished bool      `json:"executionFinished"`
}

// PageLogs returns a window into the Execution's NDJSON log file.
// fromLine is 1-indexed (matches LogLine.LineNumber); limit is
// clamped to [1, MaxLogPageSize] with DefaultLogPageSize on
// fromLine==0/limit==0.
func (s *Store) PageLogs(deploymentID, execID string, fromLine, limit int) (LogPage, error) {
	if fromLine <= 0 {
		fromLine = 1
	}
	if limit <= 0 {
		limit = DefaultLogPageSize
	}
	if limit > MaxLogPageSize {
		limit = MaxLogPageSize
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return LogPage{}, err
	}
	var exec *Execution
	for i := range idx.Executions {
		if idx.Executions[i].ID == execID {
			exec = &idx.Executions[i]
			break
		}
	}
	if exec == nil {
		return LogPage{}, fmt.Errorf("jobs: PageLogs %q: %w", execID, ErrNotFound)
	}

	depDir, err := s.deploymentDir(deploymentID)
	if err != nil {
		return LogPage{}, err
	}
	logPath := filepath.Join(depDir, execID+".log")
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No log file yet — execution started but no LogLines
			// were appended. That's a valid empty-page response.
			return LogPage{
				Lines:             []LogLine{},
				Total:             exec.LineCount,
				ExecutionFinished: IsTerminal(exec.Status),
			}, nil
		}
		return LogPage{}, fmt.Errorf("jobs: open log %q: %w", logPath, err)
	}
	defer f.Close()

	out := make([]LogLine, 0, limit)
	br := bufio.NewReader(f)
	lineNum := 0
	for {
		raw, err := br.ReadBytes('\n')
		if len(raw) > 0 {
			lineNum++
			if lineNum >= fromLine && len(out) < limit {
				var ll LogLine
				if uerr := json.Unmarshal(stripNewline(raw), &ll); uerr == nil {
					out = append(out, ll)
				}
			}
			if len(out) >= limit {
				// Drain remaining lines just for the count — but we
				// have exec.LineCount on hand; abort scan early.
				break
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return LogPage{}, fmt.Errorf("jobs: read log %q: %w", logPath, err)
		}
	}
	return LogPage{
		Lines:             out,
		Total:             exec.LineCount,
		ExecutionFinished: IsTerminal(exec.Status),
	}, nil
}

func stripNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

// newExecutionID returns a 16-byte hex string. Globally unique within
// a deployment with vanishing collision probability — even at the
// catalyst-api's maximum sustained emit rate (a few hundred per
// minute) this is overkill, but cheap.
func newExecutionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jobs: crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
