// Cross-region (two API servers) cnpg-pair switchover — the planned,
// lossless path for a bp-cnpg-pair whose halves live in DIFFERENT
// Kubernetes clusters (a true two-region Sovereign).
//
// THE DEFECT THIS CLOSES (hw307, 2026-11-06)
// ------------------------------------------
// Console "Switch over…" reached step-2 and failed:
//
//	step-2 (cordon-old-primary): find pair: cnpg: pair "cnpg-pair-bp-cnpg-pair"
//	incomplete (primary=true replica=false)
//
// cnpg.Reader.FindPair lists BOTH Cluster CRs through ONE local dynamic
// client. On a two-region Sovereign the `-replica` Cluster CR lives in
// region B's own API server, so the single-API cordon/promote (and the
// health + dry-run checks built on FindPair) could never run there. The
// observation path already learned this in #5311 (observeCNPGStandby);
// the switchover path had not.
//
// HOW THE PLANNED SWITCHOVER RUNS ACROSS TWO API SERVERS
// ------------------------------------------------------
// CloudNativePG >= 1.24 ships a native distributed-topology controlled
// switchover: every Cluster of the topology declares
// `spec.replica.{primary, self, source}`; changing `primary` on the
// current primary DEMOTES it (CNPG records the shutdown checkpoint in
// `status.demotionToken`), and the peer is promoted by setting its own
// `primary` to itself together with `promotionToken: <that token>` —
// CNPG refuses to promote before it has replayed up to the token, so no
// acknowledged commit can be lost (RPO=0 by construction).
//
// The halves are Flux/Helm-owned, so the engine NEVER patches a Cluster
// CR (#5125-D1: drift correction reverts it). It writes the desired role
// into each region's bootstrap-kit Kustomization substitute — the same
// GitOps seam the chart's dr-promoter / dr-failback actors use, with its
// own field manager (cnpg/seam.go). bp-cnpg-pair's opt-in distributed
// mode (cnpgPair.distributed.enabled) renders those substitutes as the
// CNPG distributed-topology fields on both halves.
//
//	step-2 Cordon  (demote)  : FromRegion seam ACTIVE_PRIMARY=<to-half>
//	                           → wait FromRegion Cluster status.demotionToken
//	step-6 Promote           : ToRegion seam ACTIVE_PRIMARY=<to-half>,
//	                           PROMOTION_TOKEN=<demotionToken>
//	                           → wait ToRegion Cluster lastPromotionToken==token
//	                             AND primary by spec AND Ready
//
// Region B is reached through the EXISTING secondary-region credential
// channel (Secret catalyst/cutover-secondary-kubeconfigs, see
// RegionClients). Which half is local is decided by the pair-role
// labels + the canonical `openova.io/region` label — never by name.
//
// SINGLE-API TOPOLOGY IS UNCHANGED
// --------------------------------
// When FindPair resolves BOTH halves locally (the kom4dc 2-VPC mimic)
// cnpgPromoter keeps its historical annotate + replica.enabled behaviour
// byte-for-byte. The cross-region path is entered ONLY when the local API
// holds the pair-role primary half and not the replica half.
package switchover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// RegionClients resolves a dynamic client for a REMOTE region's API
// server. region is a Continuum region label (e.g.
// `hw-me-east-215-b-rtz-prod`). Production wires the resolver over the
// secondary-region kubeconfig Secret (internal/regions); tests inject a
// fake.
type RegionClients interface {
	ClientFor(ctx context.Context, region string) (dynamic.Interface, error)
}

// CrossRegionOptions configures the cross-region cnpg-pair path. The
// zero value is valid: a nil Regions resolver makes a split pair fail
// with a clear "no region client" error (never a silent no-op).
type CrossRegionOptions struct {
	// Regions resolves the peer region's API client. Nil = cross-region
	// switchover unavailable.
	Regions RegionClients
	// Seam is the bootstrap-kit Kustomization the role substitutes live
	// on in EVERY region. Zero = flux-system/bootstrap-kit.
	Seam cnpg.SeamRef
	// Timeout bounds each wait (demotion token, promotion). Default 5m.
	Timeout time.Duration
	// PollInterval between status reads. Default 5s.
	PollInterval time.Duration
	// MaxLagSeconds is the preflight replay-lag ceiling for the standby
	// (measured by the StandbyProbe when the current primary is local).
	// Default 30s.
	MaxLagSeconds int
}

func (o CrossRegionOptions) withDefaults() CrossRegionOptions {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.MaxLagSeconds <= 0 {
		o.MaxLagSeconds = 30
	}
	return o
}

// ErrRegionUnreachable wraps every failure to reach the peer region's
// API server (no resolver, no credential, apiserver down) so callers can
// distinguish "region B unreachable" from a topology defect.
var ErrRegionUnreachable = errors.New("standby region unreachable")

// pairHalf is one Cluster CR of a split pair and the API that holds it.
type pairHalf struct {
	name   string
	region string
	dyn    dynamic.Interface
	local  bool
}

func (h pairHalf) where() string {
	if h.local {
		return fmt.Sprintf("%s (region %s, local API)", h.name, h.region)
	}
	return fmt.Sprintf("%s (region %s, remote API)", h.name, h.region)
}

// crossTopology is a cnpg-pair split across two API servers.
type crossTopology struct {
	ns     string
	local  pairHalf // the pair-role primary half (this controller's API)
	remote pairHalf // the pair-role replica half (the peer region's API)
}

// halves returns (from, to) for the plan's direction.
func (t *crossTopology) halves(plan SwitchoverPlan) (from, to pairHalf, err error) {
	switch {
	case t.local.region == plan.FromRegion && t.remote.region == plan.ToRegion:
		return t.local, t.remote, nil
	case t.remote.region == plan.FromRegion && t.local.region == plan.ToRegion:
		return t.remote, t.local, nil
	}
	return pairHalf{}, pairHalf{}, fmt.Errorf("plan regions from=%q to=%q do not match the pair halves %s / %s", plan.FromRegion, plan.ToRegion, t.local.where(), t.remote.where())
}

func (t *crossTopology) status(ctx context.Context, h pairHalf) (cnpg.Status, error) {
	st, _, err := cnpg.NewReader(h.dyn).Get(ctx, t.ns, h.name)
	if err != nil && !h.local {
		return st, fmt.Errorf("%w: %s: %v", ErrRegionUnreachable, h.region, err)
	}
	return st, err
}

// cnpgTopology is the resolved shape of a cnpg-pair: either both halves
// in the local API (single) or split (cross != nil).
type cnpgTopology struct {
	primary, replica *unstructured.Unstructured
	cross            *crossTopology
}

// resolveCNPGTopology finds the pair. Both halves local → single-API
// topology (historical path). Only the pair-role primary local → the
// replica half is fetched from the peer region's API (cross-region).
// Anything else returns FindPair's own error unchanged.
func (s *Sequencer) resolveCNPGTopology(ctx context.Context, plan SwitchoverPlan) (cnpgTopology, error) {
	if s.CNPG == nil {
		return cnpgTopology{}, errors.New("cnpg-pair mechanism: CNPG reader is required")
	}
	primary, replica, err := s.CNPG.FindPair(ctx, plan.CNPGNamespace, plan.CNPGPair)
	if err == nil {
		return cnpgTopology{primary: primary, replica: replica}, nil
	}
	if primary == nil || replica != nil {
		return cnpgTopology{}, err
	}
	// The local API holds the pair-role primary but not the replica:
	// the pair is split across API servers (#5311 shape).
	localRegion := primary.GetLabels()[cnpg.RegionLabel]
	if localRegion == "" {
		return cnpgTopology{}, fmt.Errorf("%v; pair is split across API servers but the local half %q carries no %s label to place it", err, primary.GetName(), cnpg.RegionLabel)
	}
	var remoteRegion string
	switch localRegion {
	case plan.FromRegion:
		remoteRegion = plan.ToRegion
	case plan.ToRegion:
		remoteRegion = plan.FromRegion
	default:
		return cnpgTopology{}, fmt.Errorf("%v; local half %q is in region %q, which is neither FromRegion %q nor ToRegion %q", err, primary.GetName(), localRegion, plan.FromRegion, plan.ToRegion)
	}
	remoteName := cnpg.ReplicaNameForPrimary(primary.GetName())
	if remoteName == "" {
		return cnpgTopology{}, fmt.Errorf("%v; cannot derive the replica half's name from %q", err, primary.GetName())
	}
	if s.CrossRegion.Regions == nil {
		return cnpgTopology{}, fmt.Errorf("%w: %s: pair %q is split across API servers (replica half %q is not in the local API) and no region client resolver is wired", ErrRegionUnreachable, remoteRegion, plan.CNPGPair, remoteName)
	}
	dyn, rErr := s.CrossRegion.Regions.ClientFor(ctx, remoteRegion)
	if rErr != nil {
		return cnpgTopology{}, fmt.Errorf("%w: %s: %v", ErrRegionUnreachable, remoteRegion, rErr)
	}
	t := &crossTopology{
		ns:     plan.CNPGNamespace,
		local:  pairHalf{name: primary.GetName(), region: localRegion, dyn: s.CNPG.Dyn, local: true},
		remote: pairHalf{name: remoteName, region: remoteRegion, dyn: dyn},
	}
	if _, err := t.status(ctx, t.remote); err != nil {
		return cnpgTopology{}, fmt.Errorf("get replica half %s/%s: %w", plan.CNPGNamespace, remoteName, err)
	}
	return cnpgTopology{cross: t}, nil
}

// crossPreflight checks a split pair is ready for a planned switchover
// in plan's direction. It reads only. Blockers fail the switchover;
// warnings are informational.
func (s *Sequencer) crossPreflight(ctx context.Context, plan SwitchoverPlan, t *crossTopology) (blockers, warnings []string) {
	opts := s.CrossRegion.withDefaults()
	from, to, err := t.halves(plan)
	if err != nil {
		return []string{err.Error()}, nil
	}
	fromSt, err := t.status(ctx, from)
	if err != nil {
		return []string{fmt.Sprintf("read %s: %v", from.where(), err)}, nil
	}
	toSt, err := t.status(ctx, to)
	if err != nil {
		return []string{fmt.Sprintf("read %s: %v", to.where(), err)}, nil
	}
	if !fromSt.Distributed || !toSt.Distributed {
		blockers = append(blockers, fmt.Sprintf("cross-region switchover needs bp-cnpg-pair distributed topology on BOTH halves (spec.replica.primary set; enable cnpgPair.distributed via SOVEREIGN_CNPG_PAIR_DISTRIBUTED=true on every region): %s distributed=%v, %s distributed=%v", from.name, fromSt.Distributed, to.name, toSt.Distributed))
		return blockers, warnings
	}
	if fromSt.IsReplicaCluster {
		blockers = append(blockers, fmt.Sprintf("%s is not the primary (spec.replica.primary=%q) — nothing to switch over from region %s", from.where(), fromSt.DistributedPrimary, plan.FromRegion))
	}
	if !toSt.IsReplicaCluster {
		blockers = append(blockers, fmt.Sprintf("%s is already primary (spec.replica.primary=%q)", to.where(), toSt.DistributedPrimary))
	}
	if !toSt.Ready {
		blockers = append(blockers, fmt.Sprintf("standby %s is not Ready (phase=%q)", to.where(), toSt.Phase))
	}
	// An unplanned DR episode owns the pair while either DR seam is
	// latched; its switchback is RUNBOOKS §6.1, not a planned move.
	for _, h := range []pairHalf{from, to} {
		sub, sErr := cnpg.ReadSubstitutes(ctx, h.dyn, opts.Seam)
		if sErr != nil {
			if !h.local {
				sErr = fmt.Errorf("%w: %s: %v", ErrRegionUnreachable, h.region, sErr)
			}
			blockers = append(blockers, fmt.Sprintf("read role seam in region %s: %v", h.region, sErr))
			continue
		}
		for _, k := range []string{cnpg.SubstitutePromoted, cnpg.SubstituteDemoted} {
			if strings.EqualFold(sub[k], "true") {
				blockers = append(blockers, fmt.Sprintf("region %s has %s=true — the pair is in an unplanned DR episode; switch back via RUNBOOKS §6.1 first", h.region, k))
			}
		}
	}
	// Standby caught up. The probe reads pg_stat_replication through the
	// LOCAL `<primary>-rw`, so it can measure only when the current
	// primary is the local half.
	switch {
	case !from.local:
		warnings = append(warnings, fmt.Sprintf("standby lag not measurable from this region (current primary %s is remote); CNPG's promotion token still blocks promotion until %s has replayed every acknowledged commit", from.where(), to.name))
	case s.StandbyProbe == nil:
		warnings = append(warnings, "no StandbyProbe wired — standby lag unchecked; CNPG's promotion token still blocks promotion until the standby has replayed every acknowledged commit")
	default:
		posture, pErr := s.StandbyProbe.Observe(ctx, t.ns, from.name, to.name)
		switch {
		case pErr != nil:
			blockers = append(blockers, fmt.Sprintf("cannot verify standby %s is caught up: pg_stat_replication probe on %s failed: %v", to.name, from.name, pErr))
		case !posture.StandbyPresent:
			blockers = append(blockers, fmt.Sprintf("standby %s is not streaming from %s (no pg_stat_replication row)", to.name, from.name))
		case posture.ReplayLagSeconds > opts.MaxLagSeconds:
			blockers = append(blockers, fmt.Sprintf("standby %s replay lag %ds exceeds %ds", to.name, posture.ReplayLagSeconds, opts.MaxLagSeconds))
		}
	}
	return blockers, warnings
}

// waitFor polls cond until it reports done, the context ends, or the
// timeout elapses. Bounded by an iteration count too, so a frozen
// injected clock (tests) can never spin forever.
func (s *Sequencer) waitFor(ctx context.Context, what string, cond func() (bool, string, error)) error {
	opts := s.CrossRegion.withDefaults()
	deadline := s.now().Add(opts.Timeout)
	maxIter := int(opts.Timeout/opts.PollInterval) + 1
	last := ""
	for i := 0; ; i++ {
		done, detail, err := cond()
		if err != nil {
			last = err.Error()
		} else {
			last = detail
		}
		if err == nil && done {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w (last: %s)", what, ctx.Err(), last)
		}
		if i >= maxIter || !s.now().Before(deadline) {
			return fmt.Errorf("%s: timed out after %s (last: %s)", what, opts.Timeout, last)
		}
		s.sleep(opts.PollInterval)
	}
}

func (s *Sequencer) patchSeam(ctx context.Context, h pairHalf, kv map[string]*string) error {
	err := cnpg.PatchSubstitutes(ctx, h.dyn, s.CrossRegion.Seam, kv, s.now())
	if err != nil && !h.local {
		return fmt.Errorf("%w: %s: %v", ErrRegionUnreachable, h.region, err)
	}
	return err
}

func (s *Sequencer) readSeam(ctx context.Context, h pairHalf) (map[string]string, error) {
	sub, err := cnpg.ReadSubstitutes(ctx, h.dyn, s.CrossRegion.Seam)
	if err != nil && !h.local {
		return nil, fmt.Errorf("%w: %s: %v", ErrRegionUnreachable, h.region, err)
	}
	return sub, err
}

var seamKeys = []string{cnpg.SubstituteActivePrimary, cnpg.SubstitutePromotionToken}

// demote moves `from` out of the primary role via its region's seam and
// waits for CNPG to publish a FRESH demotion token. Returns the token.
// On failure it restores the seam it wrote (best-effort).
func (s *Sequencer) demote(ctx context.Context, t *crossTopology, from, to pairHalf) (string, map[string]*string, error) {
	sub, err := s.readSeam(ctx, from)
	if err != nil {
		return "", nil, fmt.Errorf("read role seam of %s: %w", from.where(), err)
	}
	prev := cnpg.Snapshot(sub, seamKeys...)
	before, err := t.status(ctx, from)
	if err != nil {
		return "", nil, err
	}
	if err := s.patchSeam(ctx, from, map[string]*string{cnpg.SubstituteActivePrimary: cnpg.StrPtr(to.name)}); err != nil {
		return "", nil, fmt.Errorf("demote %s: %w", from.where(), err)
	}
	var token string
	wErr := s.waitFor(ctx, fmt.Sprintf("demotion of %s", from.where()), func() (bool, string, error) {
		st, err := t.status(ctx, from)
		if err != nil {
			return false, "", err
		}
		if !st.IsReplicaCluster {
			return false, fmt.Sprintf("spec.replica.primary=%q not yet rendered", st.DistributedPrimary), nil
		}
		if st.DemotionToken == "" || st.DemotionToken == before.DemotionToken {
			return false, "status.demotionToken not yet published", nil
		}
		token = st.DemotionToken
		return true, "", nil
	})
	if wErr != nil {
		_ = s.patchSeam(ctx, from, prev)
		return "", nil, wErr
	}
	return token, prev, nil
}

// promote makes `to` the primary via its region's seam, presenting the
// peer's demotion token, and waits for CNPG to consume it. On failure it
// restores the seam it wrote (best-effort).
func (s *Sequencer) promote(ctx context.Context, t *crossTopology, to pairHalf, token string) (map[string]*string, error) {
	sub, err := s.readSeam(ctx, to)
	if err != nil {
		return nil, fmt.Errorf("read role seam of %s: %w", to.where(), err)
	}
	prev := cnpg.Snapshot(sub, seamKeys...)
	if err := s.patchSeam(ctx, to, map[string]*string{
		cnpg.SubstituteActivePrimary:  cnpg.StrPtr(to.name),
		cnpg.SubstitutePromotionToken: cnpg.StrPtr(token),
	}); err != nil {
		return nil, fmt.Errorf("promote %s: %w", to.where(), err)
	}
	wErr := s.waitFor(ctx, fmt.Sprintf("promotion of %s", to.where()), func() (bool, string, error) {
		st, err := t.status(ctx, to)
		if err != nil {
			return false, "", err
		}
		switch {
		case st.IsReplicaCluster:
			return false, fmt.Sprintf("spec.replica.primary=%q not yet rendered", st.DistributedPrimary), nil
		case st.LastPromotionToken != token:
			return false, "promotion token not yet consumed (standby still replaying up to it)", nil
		case !st.Ready:
			return false, fmt.Sprintf("promoted but not Ready (phase=%q)", st.Phase), nil
		}
		return true, "", nil
	})
	if wErr != nil {
		// Undo our own write and wait for the half to read as a replica
		// again by spec, so the step-2 rollback (re-instate the old
		// primary) does not see a half-rendered promotion and refuse.
		if rErr := s.patchSeam(ctx, to, prev); rErr != nil {
			return nil, fmt.Errorf("%v; restoring the seam of %s ALSO failed: %v", wErr, to.where(), rErr)
		}
		if bErr := s.waitFor(ctx, fmt.Sprintf("revert of %s to replica", to.where()), func() (bool, string, error) {
			st, err := t.status(ctx, to)
			if err != nil {
				return false, "", err
			}
			return st.IsReplicaCluster, fmt.Sprintf("spec.replica.primary=%q", st.DistributedPrimary), nil
		}); bErr != nil {
			return nil, fmt.Errorf("%v; %v", wErr, bErr)
		}
		return nil, wErr
	}
	return prev, nil
}

// crossCordon — step-2 on a split pair: preflight, then DEMOTE the old
// primary through its region's seam and wait for the demotion token.
// The rollback re-instates the old primary's previous seam, but REFUSES
// when the peer is already primary by spec (re-promoting the old half
// then would be a split-brain).
func (s *Sequencer) crossCordon(ctx context.Context, plan SwitchoverPlan, t *crossTopology) (func(ctx context.Context) error, error) {
	if err := s.adoptDREpisode(ctx, plan, t); err != nil {
		return nil, err
	}
	if blockers, _ := s.crossPreflight(ctx, plan, t); len(blockers) > 0 {
		return nil, fmt.Errorf("cross-region preflight: %s", strings.Join(blockers, "; "))
	}
	from, to, _ := t.halves(plan)
	_, prev, err := s.demote(ctx, t, from, to)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		st, err := t.status(ctx, to)
		if err != nil {
			return fmt.Errorf("rollback demote of %s: %w", from.name, err)
		}
		if !st.IsReplicaCluster {
			return fmt.Errorf("rollback demote of %s REFUSED: %s is primary by spec — re-promoting %s would split-brain", from.name, to.where(), from.name)
		}
		return s.patchSeam(ctx, from, prev)
	}, nil
}

// adoptDREpisode hands a pair that an unplanned DR episode left behind
// (dr-promoter latched SOVEREIGN_CNPG_PAIR_PROMOTED on the survivor,
// dr-failback latched SOVEREIGN_CNPG_PAIR_DEMOTED and re-cloned the other
// half onto it) to the planned-switchover engine, WITHOUT changing a role:
// both seams get ACTIVE_PRIMARY = the current writer's half and the DR key
// cleared, in ONE patch per region, so no half ever renders without a role.
// The chart then renders the same roles in the distributed shape and the
// DR actors stand down (planned role). Only a clean episode is adopted:
// the from-half is the writer, the to-half a Ready replica. Anything else
// is left for the preflight to refuse (hw307 2026-11-07: without this the
// console could never switch a failed-over pair back).
func (s *Sequencer) adoptDREpisode(ctx context.Context, plan SwitchoverPlan, t *crossTopology) error {
	from, to, err := t.halves(plan)
	if err != nil {
		return nil // preflight reports it
	}
	// The DR seams always designate the pair-role REPLICA half as the
	// writer (PROMOTED on it, DEMOTED on the primary half). Adopt only a
	// switch that starts FROM that writer; anything else (e.g. a latched
	// promotion not yet rendered) is a live DR decision — never cancel it.
	if from.name != t.remote.name {
		return nil
	}
	latched := false
	for _, h := range []pairHalf{from, to} {
		sub, sErr := s.readSeam(ctx, h)
		if sErr != nil {
			return nil // preflight reports it
		}
		for _, k := range []string{cnpg.SubstitutePromoted, cnpg.SubstituteDemoted} {
			if strings.EqualFold(sub[k], "true") {
				latched = true
			}
		}
	}
	if !latched {
		return nil
	}
	fromSt, err := t.status(ctx, from)
	if err != nil {
		return nil
	}
	toSt, err := t.status(ctx, to)
	if err != nil {
		return nil
	}
	if fromSt.IsReplicaCluster || !toSt.IsReplicaCluster || !toSt.Ready {
		return nil // not a clean failed-over geometry — preflight refuses it
	}
	writer := cnpg.StrPtr(from.name)
	for _, h := range []pairHalf{to, from} { // follower first: it keeps following the writer
		if err := s.patchSeam(ctx, h, map[string]*string{
			cnpg.SubstituteActivePrimary: writer,
			cnpg.SubstitutePromoted:      nil,
			cnpg.SubstituteDemoted:       nil,
		}); err != nil {
			return fmt.Errorf("adopt DR episode into the planned topology (%s): %w", h.where(), err)
		}
	}
	return s.waitFor(ctx, "adoption of the failed-over pair into the distributed topology", func() (bool, string, error) {
		f, err := t.status(ctx, from)
		if err != nil {
			return false, "", err
		}
		r, err := t.status(ctx, to)
		if err != nil {
			return false, "", err
		}
		switch {
		case !f.Distributed || f.IsReplicaCluster:
			return false, fmt.Sprintf("%s not yet rendered as distributed primary", from.where()), nil
		case !r.Distributed || !r.IsReplicaCluster:
			return false, fmt.Sprintf("%s not yet rendered as distributed replica", to.where()), nil
		case !r.Ready:
			return false, fmt.Sprintf("%s not Ready (phase=%q)", to.where(), r.Phase), nil
		}
		return true, "", nil
	})
}

// crossPromote — step-6 on a split pair: promote the standby with the
// old primary's demotion token. Returns once the new primary consumed
// the token and is Ready.
//
// "Old primary streams from the new one" is verified by the
// post-switchover health check (healthReplicas), NOT here: failing
// step-6 after the new primary is writable would run the sequence's
// rollback, i.e. drag DNS + lease back onto a half that is now a
// read-only replica. The rollback hook below is a full reverse move
// (demote the new primary, promote the old one with the new token).
func (s *Sequencer) crossPromote(ctx context.Context, plan SwitchoverPlan, t *crossTopology) (func(ctx context.Context) error, error) {
	from, to, err := t.halves(plan)
	if err != nil {
		return nil, err
	}
	fromSt, err := t.status(ctx, from)
	if err != nil {
		return nil, err
	}
	if !fromSt.IsReplicaCluster || fromSt.DemotionToken == "" {
		return nil, fmt.Errorf("%s is not demoted (replica=%v, status.demotionToken=%q) — step-2 did not complete", from.where(), fromSt.IsReplicaCluster, fromSt.DemotionToken)
	}
	prevTo, err := s.promote(ctx, t, to, fromSt.DemotionToken)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		// Reverse move, lossless the same way: demote the new primary,
		// then promote the old one with the fresh token.
		token, _, err := s.demote(ctx, t, to, from)
		if err != nil {
			return fmt.Errorf("rollback promote: %w", err)
		}
		if _, err := s.promote(ctx, t, from, token); err != nil {
			return fmt.Errorf("rollback promote: %w", err)
		}
		// Leave the new half's seam as it was before this switchover
		// (its desired role is replica again either way).
		_ = s.patchSeam(ctx, to, prevTo)
		return nil
	}, nil
}

// crossHealthReplicas — the post-switchover replicas check on a split
// pair: the ToRegion half is primary by spec and Ready, the FromRegion
// half follows (replica by spec) and is Ready.
func (s *Sequencer) crossHealthReplicas(ctx context.Context, plan SwitchoverPlan, t *crossTopology) HealthCheck {
	c := HealthCheck{Name: CheckReplicasHealthy}
	from, to, err := t.halves(plan)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	toSt, err := t.status(ctx, to)
	if err != nil {
		c.Detail = fmt.Sprintf("read new primary: %v", err)
		return c
	}
	fromSt, err := t.status(ctx, from)
	if err != nil {
		c.Detail = fmt.Sprintf("read new replica: %v", err)
		return c
	}
	switch {
	case toSt.IsReplicaCluster:
		c.Detail = fmt.Sprintf("new-primary %s is still a replica by spec", to.where())
	case !toSt.Ready:
		c.Detail = fmt.Sprintf("new-primary %s is not Ready (phase=%q)", to.where(), toSt.Phase)
	case !fromSt.IsReplicaCluster:
		c.Detail = fmt.Sprintf("old primary %s is still primary by spec", from.where())
	case !fromSt.Ready:
		c.Detail = fmt.Sprintf("new-replica %s is not Ready — not yet following %s (phase=%q)", from.where(), to.name, fromSt.Phase)
	default:
		c.Passed = true
		c.Detail = fmt.Sprintf("cross-region: new-primary=%s Ready, new-replica=%s Ready following it", to.where(), from.where())
	}
	return c
}
