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

	raw, err := json.MarshalIndent(idx, "", "  ")
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
	if out.StartedAt != nil && out.FinishedAt != nil {
		out.DurationMs = out.FinishedAt.Sub(*out.StartedAt).Milliseconds()
	}
	return out
}

// StartExecution allocates a new Execution row for the given Job and
// stamps the Job's LatestExecutionID + StartedAt + Status=running. The
// returned Execution.ID is the path-segment component the /logs
// endpoint accepts. Caller is responsible for writing the matching
// Job upsert with appId/batchId metadata BEFORE the first
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
// DESC with pending Jobs (no StartedAt) bucketed last. The handler
// returns the slice unchanged.
func (s *Store) ListJobs(deploymentID string) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return nil, err
	}
	out := make([]Job, len(idx.Jobs))
	copy(out, idx.Jobs)
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

// GetJob returns the Job + its Executions list. ErrNotFound if no Job
// with the given id exists for the deployment.
func (s *Store) GetJob(deploymentID, jobID string) (Job, []Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return Job{}, nil, err
	}
	for i := range idx.Jobs {
		if idx.Jobs[i].ID == jobID {
			execs := []Execution{}
			for _, e := range idx.Executions {
				if e.JobID == jobID {
					execs = append(execs, e)
				}
			}
			sort.Slice(execs, func(a, b int) bool {
				return execs[a].StartedAt.After(execs[b].StartedAt)
			})
			return idx.Jobs[i], execs, nil
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

// SummarizeBatches groups Jobs by BatchID and returns a per-batch
// progress row. Empty deployment → empty slice (not nil) so the JSON
// shape matches the spec's `{batches: []}` exactly.
func (s *Store) SummarizeBatches(deploymentID string) ([]BatchSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return nil, err
	}
	byBatch := map[string]*BatchSummary{}
	order := []string{}
	for _, j := range idx.Jobs {
		bid := j.BatchID
		if bid == "" {
			bid = "(unbatched)"
		}
		bs, ok := byBatch[bid]
		if !ok {
			bs = &BatchSummary{BatchID: bid}
			byBatch[bid] = bs
			order = append(order, bid)
		}
		bs.Total++
		switch j.Status {
		case StatusSucceeded:
			bs.Succeeded++
			bs.Finished++
		case StatusFailed:
			bs.Failed++
			bs.Finished++
		case StatusRunning:
			bs.Running++
		case StatusPending, "":
			bs.Pending++
		}
	}
	out := make([]BatchSummary, 0, len(order))
	for _, bid := range order {
		out = append(out, *byBatch[bid])
	}
	return out, nil
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
