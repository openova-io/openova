// import.go — handover snapshot export/import support (#3263 #3277).
//
// The chroot's jobs store is seeded only by its OWN bootstrap watch,
// which observes the primary region. The secondary regions' install
// rows ("install-<region>:<chart>") are written exclusively by the
// MOTHERSHIP's secondary helmwatch bridges, so a converged multi-region
// Sovereign's /jobs page shows half the picture on the chroot — the
// Region column/filter (#3278 #3352) never even appears because only
// one region bucket exists locally.
//
// The mother closes the gap at handover by POSTing a bounded snapshot
// of its Job + Execution rows (NO log lines) to the chroot's
// /api/v1/internal/jobs/import endpoint. This file holds the two store
// primitives that wire needs:
//
//   - ListExecutions — mother-side read used to build the snapshot.
//   - ImportSnapshot — chroot-side idempotent upsert with
//     no-terminal-regression merge semantics.
package jobs

import (
	"errors"
	"strings"
)

// ListExecutions returns a copy of every Execution row for the
// deployment, in insertion order. The handover exporter filters this
// down to each Job's LatestExecutionID so the wire payload stays
// bounded (one summary per Job, no log lines).
func (s *Store) ListExecutions(deploymentID string) ([]Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return nil, err
	}
	out := make([]Execution, len(idx.Executions))
	copy(out, idx.Executions)
	return out, nil
}

// ImportSnapshot upserts a batch of Job + Execution rows into the
// deployment's index under ONE lock + ONE atomic persist. Designed for
// the handover jobs import (chroot side); the merge rules make a
// re-POST of the same snapshot a no-op:
//
//   - Jobs the store has never seen are inserted verbatim (IDs are
//     re-stamped from deploymentID + JobName so a malformed payload
//     can't plant a foreign-keyed row).
//   - Jobs that already exist locally (the chroot's own region-a watch
//     rows share IDs with the mother's region-a rows) are merged via
//     mergeJob — monotonic timestamps, DependsOn + Region + latest
//     execution-id preservation.
//   - A TERMINAL local row is NEVER regressed by a non-terminal
//     incoming row: the local row wins unchanged. Terminal→terminal
//     overwrites are allowed (the mother is the source of truth for
//     provisioning history, mirroring HandleDeploymentImport).
//   - Executions are insert-or-fill-forward: unknown IDs append; a
//     known non-terminal row may be completed by a terminal incoming
//     row; a known terminal row is never altered.
//   - Rows present locally but absent from the snapshot are untouched
//     — the import only ever ADDS information.
//
// Derived fields (ChildIDs) and the legacy migration hook
// (LegacyBatchID) are stripped on the way in; persistIndex would strip
// them anyway, but clearing them here keeps the in-memory index
// canonical too.
//
// Returns the number of Job rows and Execution rows accepted (inserted
// or merged — skipped rows don't count).
func (s *Store) ImportSnapshot(deploymentID string, jobsIn []Job, execsIn []Execution) (jobsApplied, execsApplied int, err error) {
	if strings.TrimSpace(deploymentID) == "" {
		return 0, 0, errors.New("jobs: ImportSnapshot: deploymentID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex(deploymentID)
	if err != nil {
		return 0, 0, err
	}

	byID := make(map[string]int, len(idx.Jobs))
	for i := range idx.Jobs {
		byID[idx.Jobs[i].ID] = i
	}
	for _, j := range jobsIn {
		if strings.TrimSpace(j.JobName) == "" {
			continue
		}
		j.DeploymentID = deploymentID
		j.ID = JobID(deploymentID, j.JobName)
		j.ChildIDs = nil
		j.LegacyBatchID = ""
		if j.DependsOn == nil {
			j.DependsOn = []string{}
		}
		if j.Type == "" {
			j.Type = JobTypeInstall
		}
		// Recompute DurationMs the same way mergeJob does so a first
		// INSERT and a re-import MERGE converge on identical rows
		// (idempotence) even when the payload's DurationMs drifted.
		if j.StartedAt != nil && j.FinishedAt != nil {
			j.DurationMs = j.FinishedAt.Sub(*j.StartedAt).Milliseconds()
		}
		if i, ok := byID[j.ID]; ok {
			prev := idx.Jobs[i]
			if IsTerminal(prev.Status) && !IsTerminal(j.Status) {
				// Never regress a terminal row — the chroot's own
				// watch already saw this job finish; a stale mother
				// snapshot must not flip it back to running/pending.
				continue
			}
			idx.Jobs[i] = mergeJob(prev, j)
		} else {
			byID[j.ID] = len(idx.Jobs)
			idx.Jobs = append(idx.Jobs, j)
		}
		jobsApplied++
	}

	execByID := make(map[string]int, len(idx.Executions))
	for i := range idx.Executions {
		execByID[idx.Executions[i].ID] = i
	}
	for _, e := range execsIn {
		if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.JobID) == "" {
			continue
		}
		e.DeploymentID = deploymentID
		if i, ok := execByID[e.ID]; ok {
			prev := idx.Executions[i]
			// Never alter a terminal execution; only fill a local
			// non-terminal row forward to a terminal incoming state.
			if IsTerminal(prev.Status) || !IsTerminal(e.Status) {
				continue
			}
			if e.StartedAt.IsZero() {
				e.StartedAt = prev.StartedAt
			}
			if e.LineCount < prev.LineCount {
				// The chroot may have appended more log lines locally
				// than the mother's summary knows about — keep the
				// higher ceiling so /logs pagination never truncates.
				e.LineCount = prev.LineCount
			}
			idx.Executions[i] = e
		} else {
			execByID[e.ID] = len(idx.Executions)
			idx.Executions = append(idx.Executions, e)
		}
		execsApplied++
	}

	if err := s.persistIndex(idx); err != nil {
		return 0, 0, err
	}
	return jobsApplied, execsApplied, nil
}
