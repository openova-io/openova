// Raft-transition promotion — the bp-openbao half of #3375 (#3492).
//
// bp-openbao is active-passive with DR mechanism `raft-transition`
// (docs/topology-matrix.md, platform/openbao/blueprint.yaml
// spec.topology.perTopology.active-passive.switchover.mechanism). On a
// region-kill the surviving-region openbao — a Raft standby that has been
// fed periodic snapshots by the bp-openbao snapshot-replication CronJob
// (platform/openbao/chart/templates/snapshot-replication.yaml: the
// secondary fetches `latest.snap` to a restore-staging PVC) — is promoted
// by:
//
//	1. `bao operator raft snapshot restore <staged-snapshot>`  (load the
//	   point-in-time copy of region-A's KV store), then
//	2. `bao operator raft transition-to-primary`               (make this
//	   standby the cluster's write leader).
//
// The bp-openbao chart's snapshot-replication template stages the data and
// states verbatim: "The actual restore + `transition-to-primary` is a
// bp-continuum switchover step (CLASS-B controller follow-on), NOT
// performed here." THIS file is that step.
//
// KV reads continue uninterrupted on the standby throughout — the restore
// is the row's only mutating call and it runs on a Pod that was already
// serving reads as a replica. (The walk runbook's DoD-8 step 8.5 asserts
// the read loop is uninterrupted across the kill.)
//
// Promotion is effected via a Pod exec against the standby's openbao
// container — the same mechanism a sovereign-admin would use by hand
// (`kubectl exec <pod> -- bao operator raft ...`). bp-continuum holds the
// in-cluster ServiceAccount + RBAC (pods/exec) to do this without a human.

package switchover

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PodExecutor runs a command inside a container and returns its combined
// output. The production implementation (RaftExecPromoter's, wired in
// cmd/main.go) uses client-go remotecommand SPDY exec against the
// kube-apiserver; tests inject a fake. Kept minimal so the switchover
// package doesn't grow a hard dependency on the remotecommand SDK — the
// concrete SPDY executor lives in the cmd package alongside the rest of
// the kube wiring.
type PodExecutor interface {
	// Exec runs `command` in (namespace/pod/container) and returns the
	// merged stdout+stderr. A non-nil error means the exec stream itself
	// failed OR the command exited non-zero (the SDK surfaces a non-zero
	// exit as a CodeExitError).
	Exec(ctx context.Context, namespace, pod, container string, command []string) (output string, err error)
}

// RaftTransitionTarget identifies the surviving-region openbao standby Pod
// the promotion execs into, plus the snapshot path the bp-openbao chart
// staged. Populated by the controller from the Continuum CR's
// spec.switchover.raftTransition block (parseSpec), which the bp-openbao
// bootstrap-kit slot fills from the topology declaration.
type RaftTransitionTarget struct {
	// Namespace is the openbao release namespace on the standby cluster
	// (e.g. "openbao").
	Namespace string

	// Pod is the standby Raft member Pod to promote (e.g.
	// "openbao-0"). When empty, PodSelector is used to discover it.
	Pod string

	// PodSelector is a label selector used to find the standby Pod when
	// Pod is empty (e.g. "app.kubernetes.io/name=openbao"). The promoter
	// picks the lexically-first Ready Pod matching it. Either Pod or
	// PodSelector must be set.
	PodSelector string

	// Container is the openbao container name inside the Pod (default
	// "openbao").
	Container string

	// SnapshotPath is the on-Pod path the secondary's snapshot-fetch
	// CronJob staged `latest.snap` to (the chart default is
	// /snapshots/latest.snap on the openbao-snapshot-restore-staging
	// PVC). Empty disables the restore and promotes from whatever state
	// the standby already has (degraded — last-snapshot-on-disk only).
	SnapshotPath string

	// BaoBinary is the openbao CLI name in the container image (default
	// "bao"; some images ship the legacy "vault" name).
	BaoBinary string
}

// withDefaults fills empty fields with the chart-default conventions.
func (t RaftTransitionTarget) withDefaults() RaftTransitionTarget {
	out := t
	if out.Container == "" {
		out.Container = "openbao"
	}
	if out.BaoBinary == "" {
		out.BaoBinary = "bao"
	}
	return out
}

// PodLister discovers the standby Pod when only a label selector is given.
// Implemented by the controller's dynamic client; tests inject a fake.
// Returns Ready Pod names so the promoter never execs into a NotReady
// member.
type PodLister interface {
	// ReadyPods returns the names of Ready Pods in `namespace` matching
	// `selector`, in lexical order (so promotion is deterministic).
	ReadyPods(ctx context.Context, namespace, selector string) ([]string, error)
}

// RaftExecPromoter implements Promoter for the raft-transition mechanism.
// It runs the openbao snapshot-restore + transition-to-primary via Pod
// exec on the surviving-region standby. Wired by the controller (the
// Exec/Lister backends are the real kube clients); the per-CR target comes
// from the SwitchoverPlan.
type RaftExecPromoter struct {
	// Exec runs commands in the standby openbao container.
	Exec PodExecutor

	// Lister resolves a PodSelector to a concrete Ready Pod. Optional
	// when every plan supplies an explicit Pod.
	Lister PodLister
}

// resolveTarget reads the raft-transition target off the plan and fills in
// the standby Pod (via the Lister when only a selector was given).
func (p *RaftExecPromoter) resolveTarget(ctx context.Context, plan SwitchoverPlan) (RaftTransitionTarget, error) {
	t := plan.RaftTransition.withDefaults()
	if t.Namespace == "" {
		return t, errors.New("raft-transition: spec.switchover.raftTransition.namespace is required")
	}
	if t.Pod == "" {
		if t.PodSelector == "" {
			return t, errors.New("raft-transition: one of raftTransition.pod or raftTransition.podSelector is required")
		}
		if p.Lister == nil {
			return t, errors.New("raft-transition: podSelector set but no PodLister wired")
		}
		pods, err := p.Lister.ReadyPods(ctx, t.Namespace, t.PodSelector)
		if err != nil {
			return t, fmt.Errorf("raft-transition: list standby pods (%s/%s): %w", t.Namespace, t.PodSelector, err)
		}
		if len(pods) == 0 {
			return t, fmt.Errorf("raft-transition: no Ready openbao pod matches %q in %s", t.PodSelector, t.Namespace)
		}
		t.Pod = pods[0]
	}
	return t, nil
}

// Cordon — step-2 for raft-transition. Unlike cnpg-pair there is NOTHING
// to cordon on the standby: the OLD primary lives in the killed region and
// is unreachable (a region-kill is the trigger), so there is no write path
// to fence here. The standby keeps serving KV READS uninterrupted (the row
// invariant) and accepts no writes until promoted in step-6. Cordon is
// therefore a deliberate no-op with no rollback — but it still validates
// the target resolves, so a misconfigured CR fails early (at step-2)
// rather than after traffic + lease have already moved.
func (p *RaftExecPromoter) Cordon(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p == nil || p.Exec == nil {
		return nil, errors.New("RaftExecPromoter: nil executor")
	}
	if _, err := p.resolveTarget(ctx, plan); err != nil {
		return nil, err
	}
	// No write to fence on the survivor; promotion is the operative step.
	return nil, nil
}

// Promote — step-6 for raft-transition. Restores the staged snapshot (when
// configured) then runs `bao operator raft transition-to-primary` on the
// standby, making it the write leader. The rollback hook is a no-op: a
// region-kill promotion is one-directional — the killed region's data is
// gone, so "un-promoting" would lose the writes the survivor accepted
// after promotion. Re-pairing the recovered region is the documented Day-2
// rejoin (DoD-9), not a switchover rollback.
func (p *RaftExecPromoter) Promote(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p == nil || p.Exec == nil {
		return nil, errors.New("RaftExecPromoter: nil executor")
	}
	t, err := p.resolveTarget(ctx, plan)
	if err != nil {
		return nil, err
	}

	// 1. Restore the staged snapshot (point-in-time copy of the killed
	//    region's KV). Skipped when no SnapshotPath is configured — then
	//    the standby promotes from its own on-disk Raft state.
	if t.SnapshotPath != "" {
		restoreCmd := []string{
			t.BaoBinary, "operator", "raft", "snapshot", "restore", t.SnapshotPath,
		}
		if out, err := p.Exec.Exec(ctx, t.Namespace, t.Pod, t.Container, restoreCmd); err != nil {
			return nil, fmt.Errorf("raft snapshot restore on %s/%s: %w (output: %s)", t.Namespace, t.Pod, err, strings.TrimSpace(out))
		}
	}

	// 2. Transition this standby to the Raft primary (write leader).
	transitionCmd := []string{
		t.BaoBinary, "operator", "raft", "transition-to-primary",
	}
	if out, err := p.Exec.Exec(ctx, t.Namespace, t.Pod, t.Container, transitionCmd); err != nil {
		// Some openbao/vault builds name the verb differently across
		// versions; surface the raw output so the operator sees exactly
		// what the binary rejected rather than a bare exit code.
		return nil, fmt.Errorf("raft transition-to-primary on %s/%s: %w (output: %s)", t.Namespace, t.Pod, err, strings.TrimSpace(out))
	}

	// One-directional: no rollback (see doc comment).
	return nil, nil
}
