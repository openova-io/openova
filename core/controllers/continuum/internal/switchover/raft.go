// Raft-transition promotion — the bp-openbao half of #3375 (#3492).
//
// bp-openbao is active-passive with DR mechanism `raft-transition`
// (docs/topology-matrix.md, platform/openbao/blueprint.yaml
// spec.topology.perTopology.active-passive.switchover.mechanism).
//
// CORRECTION (2026-06-15, #3492): the original implementation execed
// `bao operator raft snapshot restore` + `bao operator raft
// transition-to-primary`. The latter DOES NOT EXIST in OpenBao OSS — OSS
// integrated-raft has only `operator raft promote`/`demote` (voter state,
// and they REQUIRE an active leader, so they are useless when all of a
// region's voters are dead — openbao PR #996). The real OSS cross-region
// design (openbao discussion #1842) is a single STRETCHED raft cluster:
// region-B joins region-A as a NON-VOTER via retry_join (the bp-openbao
// chart's cross-region-raft-config.yaml), so region-B holds region-A's live
// KV. On a region-kill the surviving region-B node is promoted to a writable
// single-node voter via OpenBao OSS's documented quorum-loss RECOVERY:
//
//	1. (OPTIONAL) `bao operator raft snapshot restore <staged>` — only for
//	   the degenerate non-stretched topology where region-B was fed periodic
//	   snapshots instead of being a live non-voter. Skipped when SnapshotPath
//	   is empty (the common case: region-B already holds region-A's live KV).
//	2. Write <RaftDataPath>/raft/peers.json naming ONLY the surviving region-B
//	   node as a voter (non_voter:false). This is OpenBao's documented
//	   peers.json recovery for permanent quorum loss
//	   (openbao.org/docs/concepts/integrated-storage). The node id + cluster
//	   address are read INSIDE the pod (the raft node-id file + POD_IP), so we
//	   never guess them controller-side.
//	3. RESTART the standby Pod. peers.json is read on process START, not live;
//	   on restart raft performs the recovery and the survivor self-elects as
//	   the sole leader (cluster "in an operable state again ... one of the
//	   nodes should claim leadership and become active").
//
// KV reads continue uninterrupted on the standby until the restart — the
// restart is the only interruption, bounded to a single Pod's recreate
// (~seconds), well within the 60s RTO. (DoD-8 step 8.5.)
//
// Promotion is effected via a Pod exec (peers.json write) + a Pod delete
// (restart) against the standby's openbao container/Pod — the same steps a
// sovereign-admin would run by hand. bp-continuum holds the in-cluster
// ServiceAccount + RBAC (pods/exec + pods/delete) to do this without a human.

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

// PodRestarter restarts (deletes; the StatefulSet recreates it) a Pod. The
// peers.json recovery is only applied when the openbao process STARTS, so
// the survivor Pod must be restarted after the peers.json write. The
// production implementation deletes the Pod via the kube client; the
// StatefulSet controller recreates it with the same name + PVC, and on boot
// openbao reads peers.json and the survivor becomes the sole leader.
//
// Kept a separate seam from PodExecutor so a controller missing pods/delete
// RBAC fails with a clear error at promote time rather than silently
// leaving the survivor as a non-voter that can never accept writes.
type PodRestarter interface {
	// RestartPod deletes (namespace/pod); the owning StatefulSet recreates
	// it. Returns an error if the delete itself fails (RBAC / not-found).
	RestartPod(ctx context.Context, namespace, pod string) error
}

// RaftTransitionTarget identifies the surviving-region openbao standby Pod
// the promotion drives, plus the raft data dir (where peers.json is written)
// and the optional staged-snapshot path. Populated by the controller from
// the Continuum CR's spec.switchover.raftTransition block (parseSpec), which
// the bp-openbao bootstrap-kit slot fills from the topology declaration.
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

	// RaftDataPath is the openbao raft store directory inside the container
	// (upstream chart default "/openbao/data"; the storage "raft" { path }
	// stanza). The peers.json recovery file is written to
	// <RaftDataPath>/raft/peers.json. Defaulted to /openbao/data.
	RaftDataPath string

	// SnapshotPath is an OPTIONAL last-resort staged snapshot to restore
	// BEFORE the peers.json recovery — only for the degenerate non-stretched
	// topology where region-B was fed periodic snapshots instead of being a
	// live retry_join non-voter. Empty (the common case) skips the restore:
	// the stretched-raft replica already holds region-A's live KV.
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
	if out.RaftDataPath == "" {
		out.RaftDataPath = "/openbao/data"
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
// It promotes the surviving-region standby to a writable single-node leader
// via OpenBao OSS's peers.json recovery (optional snapshot restore → write
// peers.json → restart Pod) — NOT the Enterprise-only transition-to-primary.
// Wired by the controller (the Exec/Lister/Restarter backends are the real
// kube clients); the per-CR target comes from the SwitchoverPlan.
type RaftExecPromoter struct {
	// Exec runs commands in the standby openbao container (snapshot restore +
	// the peers.json write).
	Exec PodExecutor

	// Restarter restarts the standby Pod so the openbao process re-reads
	// peers.json on boot. Required: a nil Restarter fails the promote with a
	// clear error rather than leaving the survivor as an un-promotable
	// non-voter.
	Restarter PodRestarter

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
// the target resolves AND that a Restarter is wired (peers.json recovery
// needs it), so a misconfigured CR fails early (at step-2) rather than after
// traffic + lease have already moved.
func (p *RaftExecPromoter) Cordon(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p == nil || p.Exec == nil {
		return nil, errors.New("RaftExecPromoter: nil executor")
	}
	if p.Restarter == nil {
		return nil, errors.New("RaftExecPromoter: nil Restarter (peers.json recovery requires a Pod restart)")
	}
	if _, err := p.resolveTarget(ctx, plan); err != nil {
		return nil, err
	}
	// No write to fence on the survivor; promotion is the operative step.
	return nil, nil
}

// peersJSONScript returns the shell that writes the single-voter peers.json
// recovery file inside the standby pod. It derives the surviving node's raft
// id + cluster address LOCALLY (no controller-side guessing):
//
//   - node id: the upstream chart sets storage "raft" { node_id } from the
//     BAO_RAFT_NODE_ID env (the Pod's HOSTNAME); raft also persists it to
//     <dataPath>/raft/node-id. Prefer the persisted file, fall back to
//     $HOSTNAME.
//   - address: the cluster address the node advertises is
//     "<POD_IP>:8201" (the upstream BAO_CLUSTER_ADDR is
//     "https://$(POD_IP):8201"; peers.json wants host:port). POD_IP is in
//     the pod env.
//
// The file names ONLY this node, as a VOTER (non_voter:false), so on restart
// raft recovers to a single-node cluster that elects this node leader.
func peersJSONScript(dataPath string) string {
	raftDir := strings.TrimRight(dataPath, "/") + "/raft"
	// Note: single-quoted heredoc-free construction; ${} are expanded by the
	// pod's /bin/sh at exec time, not by Go.
	return strings.Join([]string{
		"set -eu",
		fmt.Sprintf("RAFT_DIR=%q", raftDir),
		// Resolve node id: persisted node-id file wins; else HOSTNAME.
		`if [ -f "$RAFT_DIR/node-id" ]; then NODE_ID="$(cat "$RAFT_DIR/node-id")"; else NODE_ID="${HOSTNAME:-$(hostname)}"; fi`,
		// Resolve advertised cluster address (POD_IP:8201).
		`POD_IP="${POD_IP:-$(hostname -i 2>/dev/null | awk '{print $1}')}"`,
		`if [ -z "${POD_IP:-}" ]; then echo "FATAL: cannot determine POD_IP for peers.json" >&2; exit 1; fi`,
		`ADDR="${POD_IP}:8201"`,
		`mkdir -p "$RAFT_DIR"`,
		// Write the single-voter recovery file. non_voter:false promotes this
		// surviving node from its retry_join non-voter status to a voter.
		`printf '[{"id":"%s","address":"%s","non_voter":false}]\n' "$NODE_ID" "$ADDR" > "$RAFT_DIR/peers.json"`,
		`echo "wrote $RAFT_DIR/peers.json id=$NODE_ID address=$ADDR (single-voter recovery)"`,
	}, "\n")
}

// Promote — step-6 for raft-transition. OpenBao OSS peers.json recovery:
//
//	(1) optional snapshot restore (only when SnapshotPath set — the
//	    degenerate non-stretched topology),
//	(2) write a single-voter <RaftDataPath>/raft/peers.json on the survivor,
//	(3) restart the survivor Pod so openbao re-reads peers.json on boot and
//	    self-elects as the sole leader.
//
// The rollback hook is a no-op: a region-kill promotion is one-directional —
// the killed region's data is gone, so "un-promoting" would lose the writes
// the survivor accepted after promotion. Re-pairing the recovered region is
// the documented Day-2 rejoin (DoD-9), not a switchover rollback.
func (p *RaftExecPromoter) Promote(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p == nil || p.Exec == nil {
		return nil, errors.New("RaftExecPromoter: nil executor")
	}
	if p.Restarter == nil {
		return nil, errors.New("RaftExecPromoter: nil Restarter (peers.json recovery requires a Pod restart)")
	}
	t, err := p.resolveTarget(ctx, plan)
	if err != nil {
		return nil, err
	}

	// 1. OPTIONAL: restore the staged snapshot (point-in-time copy of the
	//    killed region's KV). Skipped when no SnapshotPath is configured —
	//    the common stretched-raft case where the survivor already holds
	//    region-A's live KV. `snapshot restore` IS a real OSS command.
	if t.SnapshotPath != "" {
		restoreCmd := []string{
			t.BaoBinary, "operator", "raft", "snapshot", "restore", t.SnapshotPath,
		}
		if out, err := p.Exec.Exec(ctx, t.Namespace, t.Pod, t.Container, restoreCmd); err != nil {
			return nil, fmt.Errorf("raft snapshot restore on %s/%s: %w (output: %s)", t.Namespace, t.Pod, err, strings.TrimSpace(out))
		}
	}

	// 2. Write the single-voter peers.json recovery file on the survivor.
	//    This promotes the surviving retry_join non-voter to a standalone
	//    voter on the NEXT process start (step 3). OSS-correct — replaces the
	//    Enterprise-only `transition-to-primary` the OSS binary rejects.
	peersCmd := []string{"/bin/sh", "-c", peersJSONScript(t.RaftDataPath)}
	if out, err := p.Exec.Exec(ctx, t.Namespace, t.Pod, t.Container, peersCmd); err != nil {
		return nil, fmt.Errorf("write peers.json recovery on %s/%s: %w (output: %s)", t.Namespace, t.Pod, err, strings.TrimSpace(out))
	}

	// 3. Restart the survivor Pod so openbao re-reads peers.json on boot and
	//    self-elects as the sole leader. peers.json is consumed at process
	//    START, not live — without the restart the survivor stays a
	//    non-voter and never accepts writes.
	if err := p.Restarter.RestartPod(ctx, t.Namespace, t.Pod); err != nil {
		return nil, fmt.Errorf("restart standby pod %s/%s for peers.json recovery: %w", t.Namespace, t.Pod, err)
	}

	// One-directional: no rollback (see doc comment).
	return nil, nil
}
