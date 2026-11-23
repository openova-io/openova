package provisioner

// A phase of a deployment that runs again (a retry) runs on the code path of
// its first run: the same deployment id, the same working directory, the same
// bounds and the same follow-up phases. This file holds what that needs from
// the provisioner. A request rebuilt from a stored record re-applies the
// variables file it was applied with, and a Phase-0 result keeps the one field
// another writer stamps while Phase 0 runs.

import (
	"fmt"
	"os"
	"path/filepath"
)

// stageTfvars writes tofu.auto.tfvars.json into deployDir from req. A request
// rebuilt from a stored record (FromRecord) holds no credential and not every
// field (store.Redact), so a file written from it would differ from the file
// the deployment was applied with. The working directory keeps that file, and
// the run applies it again: it is what the full request writes. Without such a
// file nothing was applied for the deployment, and the record alone cannot
// reproduce its request. A kept file may be of an older module contract, so it
// is brought to the current one first, as before a destroy (SOV-131, SOV-338,
// CLD-090); held values stay.
func stageTfvars(deployDir string, req Request) error {
	if !req.FromRecord {
		if err := writeTfvars(deployDir, req); err != nil {
			return fmt.Errorf("write tfvars: %w", err)
		}
		return nil
	}
	path := filepath.Join(deployDir, "tofu.auto.tfvars.json")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("deployment %s: the request was rebuilt from its stored record, which holds no credential and not every field, and the working directory holds no variables file the deployment was applied with (%v); nothing can be applied again from the record alone — start a new deployment", req.DeploymentID, err)
	}
	if err := addVariablesIfMissing(path, req); err != nil {
		return fmt.Errorf("add variables to tfvars: %w", err)
	}
	if err := addCloudRegionIfMissing(path, req); err != nil {
		return fmt.Errorf("add cloud_region to tfvars: %w", err)
	}
	return nil
}

// MergePhase0 returns the record's result after a Phase-0 run: fresh, the
// result Provision returned, with the kubeconfig path of prev, the result the
// record held, when fresh has none. The path is the one field another writer
// stamps while Phase 0 runs (PutKubeconfig), and a run that converges existing
// state gets no new upload. Every other field of prev was written by an earlier
// run's Phase-1 watch or handover (verdict, census, degraded flags, handover
// URL and times) and is dropped, as a replaced result dropped it: this run's
// watch and handover write them again. A nil prev returns fresh; a nil fresh
// returns prev.
func MergePhase0(prev, fresh *Result) *Result {
	if prev == nil || fresh == nil {
		if fresh == nil {
			return prev
		}
		return fresh
	}
	out := *fresh
	if out.KubeconfigPath == "" {
		out.KubeconfigPath = prev.KubeconfigPath
	}
	return &out
}

// HasPhase0Outputs reports whether r holds what a Phase 0 that reached its
// outputs writes (every Provision result sets ConsoleURL), not only the
// kubeconfig path that an upload stamps on the minimal result it creates. A
// later run of such a deployment does not own its pool name: an earlier run
// committed it (or, on a partial failure, released it already).
func (r *Result) HasPhase0Outputs() bool { return r != nil && r.ConsoleURL != "" }

// ClearPhase1 drops the verdict of a finished Phase-1 watch (its outcome, its
// finish time and the component states), so that the next watch of the
// deployment judges the run again. A nil r is left alone.
func (r *Result) ClearPhase1() {
	if r != nil {
		r.Phase1Outcome, r.Phase1FinishedAt, r.ComponentStates = "", nil, nil
	}
}
