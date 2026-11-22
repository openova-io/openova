package provisioner

// A phase of a deployment that runs again (a retry) runs on the code path of
// its first run: the same deployment id, the same working directory, the same
// bounds and the same follow-up phases. This file holds what that needs from
// the provisioner. A request rebuilt from a stored record re-applies the
// variables file it was applied with, and the writers of one record's result
// merge their fields instead of replacing each other.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// stageTfvars writes tofu.auto.tfvars.json into deployDir from req. A request
// rebuilt from a stored record (FromRecord) holds no credential and not every
// field (store.Redact), so a file written from it would differ from the file
// the deployment was applied with. The working directory keeps that file, and
// the run applies it again: it is what the full request writes. Without such a
// file nothing was applied for the deployment, and the record alone cannot
// reproduce its request.
func stageTfvars(deployDir string, req Request) error {
	if !req.FromRecord {
		if err := writeTfvars(deployDir, req); err != nil {
			return fmt.Errorf("write tfvars: %w", err)
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(deployDir, "tofu.auto.tfvars.json")); err != nil {
		return fmt.Errorf("deployment %s: the request was rebuilt from its stored record, which holds no credential and not every field, and the working directory holds no variables file the deployment was applied with (%v); nothing can be applied again from the record alone — start a new deployment", req.DeploymentID, err)
	}
	return nil
}

// MergePhase0 returns the record's result after a Phase-0 run: fresh, the
// result Provision returned, with every field that fresh leaves unset taken
// from prev, the result the record held. A kubeconfig upload stamps
// KubeconfigPath while Phase 0 still runs, and the Phase-1 watch and the
// handover write fields that Provision never writes; Phase 0 must not drop
// them. A nil prev or fresh returns fresh.
func MergePhase0(prev, fresh *Result) *Result {
	if prev == nil || fresh == nil {
		return fresh
	}
	out := *fresh
	o, p := reflect.ValueOf(&out).Elem(), reflect.ValueOf(prev).Elem()
	for i := 0; i < o.NumField(); i++ {
		if o.Field(i).IsZero() {
			o.Field(i).Set(p.Field(i))
		}
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
