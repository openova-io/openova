// Promotion-mechanism seam — slice for #3492 (the openbao-raft promotion
// half of #3375, "TOPOLOGY/DR ... continuum executes it").
//
// The 7-step Sequencer (sequence.go) is mechanism-agnostic for five of
// its steps — validate-lease (1), drain-http (3), flip-dns (4),
// swap-lease (5), audit (7). Only the two STATE-STORE-PROMOTION steps
// differ per blueprint:
//
//	step-2  cordon old primary's writes
//	step-6  promote the standby to primary (and demote the old primary)
//
// For the cnpg-pair mechanism (bp-cnpg-pair, the working reference) those
// steps are: annotate `cnpg.io/cluster.primary` on the primary Cluster CR
// (cordon) and flip `spec.replica.enabled` on the two cluster-pair halves
// (promote). For the raft-transition mechanism (bp-openbao, per
// docs/topology-matrix.md) the promotion is instead the OpenBao OSS
// peers.json recovery — an optional `bao operator raft snapshot restore`
// followed by a single-voter peers.json write + Pod restart on the
// surviving-region standby pod (`transition-to-primary` is Enterprise-only,
// absent in OSS) — with NO CNPG cluster-pair involved at all.
//
// The Promoter interface is the seam. The sequencer selects the active
// Promoter by `SwitchoverPlan.Mechanism`; cnpgPromoter is the default so
// the cnpg-pair path is byte-identical to before this seam existed.

package switchover

import (
	"context"
	"errors"
	"fmt"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// Mechanism is the canonical switchover-mechanism discriminator, mirroring
// the Blueprint topology declaration's `switchover.mechanism` field
// (core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go
// SwitchoverSpec.Mechanism) and the `docs/topology-matrix.md` row column.
//
// Only the mechanisms bp-continuum can itself EXECUTE are enumerated here.
// Other rows (`manual`, `bp-velero-restore`, s3/mirrormaker CLASS-B) are
// not driven by this engine and never reach the Sequencer.
type Mechanism string

const (
	// MechanismCNPGPair — the bp-cnpg-pair active-hot-standby /
	// active-passive promotion: cordon annotation + `replica.enabled`
	// flip on the two Cluster CR halves. The default and the long-running
	// reference (hw128 region-kill PASS).
	MechanismCNPGPair Mechanism = "cnpg-pair"

	// MechanismRaftTransition — the bp-openbao active-passive promotion:
	// OpenBao OSS peers.json recovery on the surviving-region standby pod
	// (optional snapshot restore → single-voter peers.json write → Pod
	// restart). KV reads continue uninterrupted on the replica until the
	// restart (the row invariant; the restart is a single Pod recreate).
	MechanismRaftTransition Mechanism = "raft-transition"
)

// DefaultMechanism is applied when SwitchoverPlan.Mechanism is empty. The
// cnpg-pair path is the historical default — an unset mechanism on an
// existing Continuum CR keeps its prior behavior exactly.
const DefaultMechanism = MechanismCNPGPair

// IsValid reports whether m is a mechanism this engine can execute.
func (m Mechanism) IsValid() bool {
	switch m {
	case MechanismCNPGPair, MechanismRaftTransition:
		return true
	}
	return false
}

// Promoter abstracts the two state-store-promotion steps of the
// switchover sequence (the only mechanism-specific steps). One Promoter
// implementation exists per Mechanism. Both methods return an optional
// rollback hook (registered on success, invoked when a LATER step fails),
// mirroring the stepFunc contract in sequence.go.
//
// Cordon is step-2 (stop accepting writes on the old primary). Promote is
// step-6 (the standby becomes primary; the old primary becomes the
// follower). The two are split — rather than one Promote call — so the
// sequence's drain-http + flip-dns + swap-lease steps run BETWEEN cordon
// and promote exactly as the cnpg-pair reference does (cordon early so no
// writes land on the doomed primary while DNS still points at it; promote
// late once traffic + lease have moved).
type Promoter interface {
	// Cordon stops the old primary from accepting writes. Returns a
	// rollback hook that un-cordons it (best-effort).
	Cordon(ctx context.Context, plan SwitchoverPlan) (rollback func(ctx context.Context) error, err error)

	// Promote makes the standby (plan.ToRegion) the new primary and
	// demotes the old primary (plan.FromRegion) to follower. Returns a
	// rollback hook that reverses the promotion (best-effort).
	Promote(ctx context.Context, plan SwitchoverPlan) (rollback func(ctx context.Context) error, err error)
}

// promoterFor returns the Promoter for the plan's mechanism. cnpg-pair is
// served by the sequencer's own cnpg.Reader (the default, wired on the
// Sequencer struct); raft-transition is served by s.RaftPromoter, which
// the controller wires from a pod-exec backend. A raft-transition plan
// with no RaftPromoter wired is a configuration error surfaced at step-2.
func (s *Sequencer) promoterFor(plan SwitchoverPlan) (Promoter, error) {
	m := plan.Mechanism
	if m == "" {
		m = DefaultMechanism
	}
	switch m {
	case MechanismCNPGPair:
		if s.CNPG == nil {
			return nil, errors.New("cnpg-pair mechanism: CNPG reader is required")
		}
		return &cnpgPromoter{r: s.CNPG}, nil
	case MechanismRaftTransition:
		if s.RaftPromoter == nil {
			return nil, errors.New("raft-transition mechanism: no RaftPromoter wired (controller must set Sequencer.RaftPromoter)")
		}
		return s.RaftPromoter, nil
	default:
		return nil, fmt.Errorf("unknown switchover mechanism %q (want one of cnpg-pair, raft-transition)", m)
	}
}

// cnpgPromoter is the default Promoter — it reproduces, byte-for-byte, the
// cnpg-pair cordon (step-2) and replica.enabled flip (step-6) the
// Sequencer performed inline before the Promoter seam was extracted. It
// wraps the same *cnpg.Reader the Sequencer already holds.
type cnpgPromoter struct {
	r *cnpg.Reader
}

// Cordon — step-2 for cnpg-pair: annotate the primary Cluster CR with
// "switchover-pending" so the CNPG operator stops accepting writes. Same
// behavior as the pre-seam inline step.
func (p *cnpgPromoter) Cordon(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p == nil || p.r == nil {
		return nil, errors.New("cnpgPromoter: nil reader")
	}
	primary, _, err := p.r.FindPair(ctx, plan.CNPGNamespace, plan.CNPGPair)
	if err != nil {
		return nil, fmt.Errorf("find pair: %w", err)
	}
	if err := p.r.SetPrimaryAnnotation(ctx, plan.CNPGNamespace, primary.GetName(), "switchover-pending"); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		return p.r.ClearPrimaryAnnotation(ctx, plan.CNPGNamespace, primary.GetName())
	}, nil
}

// Promote — step-6 for cnpg-pair: clear the cordon on the old primary,
// then flip spec.replica.enabled so the old primary follows and the new
// primary leads. Same behavior as the pre-seam inline step.
func (p *cnpgPromoter) Promote(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p == nil || p.r == nil {
		return nil, errors.New("cnpgPromoter: nil reader")
	}
	primary, replica, err := p.r.FindPair(ctx, plan.CNPGNamespace, plan.CNPGPair)
	if err != nil {
		return nil, fmt.Errorf("find pair (uncordon): %w", err)
	}
	if err := p.r.ClearPrimaryAnnotation(ctx, plan.CNPGNamespace, primary.GetName()); err != nil {
		return nil, err
	}
	if err := p.r.SetReplicaEnabled(ctx, plan.CNPGNamespace, primary.GetName(), true); err != nil {
		return nil, err
	}
	if err := p.r.SetReplicaEnabled(ctx, plan.CNPGNamespace, replica.GetName(), false); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		_ = p.r.SetReplicaEnabled(ctx, plan.CNPGNamespace, replica.GetName(), true)
		_ = p.r.SetReplicaEnabled(ctx, plan.CNPGNamespace, primary.GetName(), false)
		return nil
	}, nil
}
