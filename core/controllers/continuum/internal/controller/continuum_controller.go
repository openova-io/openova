// Package controller — continuum-controller reconciler (slice
// K-Cont-2 of EPIC-6 #1101).
//
// This file replaces K-Cont-1's no-op skeleton with the full
// reconcile loop:
//
//   - Per-Continuum-CR goroutine, keyed by NamespacedName, maintained
//     in `r.activeContinuums` (mutex-protected).
//   - Each goroutine: lease maintenance (10s renew, 30s TTL) via
//     witness.Client; CNPG status watch via cnpg.Reader; switchover
//     trigger on (a) replication lag > spec.rto, or (b) explicit
//     spec.switchover.requested=true.
//   - On a Continuum CR deletion the per-CR goroutine is cancelled +
//     the lease released.
//
// Per ADR-0001 §2.7 + INVIOLABLE-PRINCIPLES #3 we use the dynamic
// client + Unstructured for every CR access — no controller-gen, no
// generated typed clients.
//
// Per INVIOLABLE-PRINCIPLES #4 every URL / region / lease TTL is
// runtime-configurable via Continuum CR spec (not env, not
// hardcoded).
package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pdm"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// Continuum CR pin — same constants K-Cont-1 shipped.
const (
	ContinuumGroup    = "dr.openova.io"
	ContinuumVersion  = "v1"
	ContinuumKind     = "Continuum"
	ContinuumListKind = "ContinuumList"
	ContinuumResource = "continuums"
)

// ContinuumGVR is consumed by the dynamic client + by tests.
var ContinuumGVR = schema.GroupVersionResource{
	Group:    ContinuumGroup,
	Version:  ContinuumVersion,
	Resource: ContinuumResource,
}

// continuumGVK — used for the controller-runtime watch.
var continuumGVK = schema.GroupVersionKind{
	Group:   ContinuumGroup,
	Version: ContinuumVersion,
	Kind:    ContinuumKind,
}

// Default lease + watch tuning — overridable per-CR via
// spec.leaseClient.config.{ttl,renew}Seconds.
const (
	DefaultLeaseTTLSeconds   = 30
	DefaultLeaseRenewSeconds = 10
	DefaultLagThresholdSecs  = 60
)

// Phase strings — mirrors the CRD enum.
const (
	PhasePending       = "Pending"
	PhaseHealthy       = "Healthy"
	PhaseDegraded      = "Degraded"
	PhaseSwitchingOver = "SwitchingOver"
	PhaseFailedOver    = "FailedOver"
	PhaseFailed        = "Failed"
)

// ContinuumReconciler is the K-Cont-2 reconciler.
type ContinuumReconciler struct {
	client.Client

	// Scheme — controller-runtime convention; we use unstructured so
	// the scheme is for leader election only.
	Scheme *runtime.Scheme

	// Dyn — dynamic client for CR + CNPG + HTTPRoute access.
	Dyn dynamic.Interface

	// WitnessSelector chooses a per-CR witness Client.
	WitnessSelector witness.Selector

	// HoldingRegion is the host-cluster name THIS controller instance
	// represents. Stamped on Witness.Acquire calls; populated from
	// the controller's CATALYST_REGION env var at startup.
	HoldingRegion string

	// LeaseNamespace is where the `k8s-lease` witness stores its
	// coordination.k8s.io/v1 Lease objects (#3829). Defaults to the
	// controller's own namespace at startup (CATALYST_LEASE_NS env or
	// the in-cluster ServiceAccount namespace). A CR may override per
	// slot via leaseClient.config.namespace. Unused by the other
	// witness kinds.
	LeaseNamespace string

	// PDMClient is the pool-domain-manager HTTP client (shared).
	PDMClient *pdm.Client

	// Audit publisher (NATS JetStream in production, Recorder in
	// tests).
	Audit events.Publisher

	// Drainer — HTTPRoute weight flipper. Shared across CRs.
	Drainer switchover.HTTPRouteDrainer

	// RaftPromoter serves the `raft-transition` switchover mechanism
	// (bp-openbao): the OSS peers.json recovery (optional snapshot restore →
	// single-voter peers.json write via Pod exec → Pod restart) on the
	// surviving standby. Wired in cmd/main.go from a pod-exec + pod-delete
	// backend. Nil = raft-transition CRs fail at step-2 with a clear error
	// (cnpg-pair CRs unaffected). (#3492)
	RaftPromoter switchover.Promoter

	// HealthOpts carries the F-3 post-switchover health-check
	// dependencies (DNS resolver fanout + audit-tail). Fields nil =
	// the corresponding check is recorded as deferred.
	HealthOpts switchover.HealthOptions

	// HealthDelay is how long to wait after a successful switchover
	// before running the F-3 post-switchover health check. Default
	// 30s per design doc §9.5; tests override to 0.
	HealthDelay time.Duration

	// RejoinOptions configures step-8 (#5125 Defect-2 re-clone-on-
	// divergence): max attempts + backoff cooldown. Zero-value is
	// filled by switchover.RejoinOptions.Defaults() (3 attempts, 3m
	// cooldown) on every call.
	RejoinOptions switchover.RejoinOptions

	// activeContinuums tracks per-CR goroutines keyed by
	// NamespacedName.String() (e.g. "demo/cr1").
	activeContinuums   map[string]*continuumGoroutine
	activeContinuumsMu sync.Mutex

	// Now / sleeper for tests. Defaults are time.Now / time.Sleep.
	Now   func() time.Time
	Sleep func(time.Duration)
}

// continuumGoroutine bundles a per-CR background loop's state.
type continuumGoroutine struct {
	cancel  context.CancelFunc
	witness witness.Client
	holder  string

	// lastSpecFingerprint is the hash of the switchover-relevant
	// spec fields the per-CR goroutine last observed. When the next
	// loop iteration sees a different value, we emit
	// `continuum-config-changed` (slice F-1).
	lastSpecFingerprint string

	// rejoinState is step-8's (#5125 Defect-2) cross-tick bookkeeping —
	// attempt count + backoff clock + bounded-fail-loud latch for the
	// current re-clone-on-divergence episode. Session-scoped, same
	// pattern as lastSpecFingerprint above (a controller restart starts
	// a fresh episode rather than persisting across restarts).
	rejoinState switchover.RejoinState

	// lastActingPrimary is the Cluster CR name this goroutine last
	// observed ACTING as the pair's primary (live spec.replica.enabled,
	// not the static pair-role label) AND successfully re-asserted
	// managed-role passwords on (#5224, role_reassert.go). Empty on a
	// fresh goroutine, so the first settled observation always fires one
	// idempotent re-assert — deliberate: the per-CR goroutines restart
	// during the very promote-flap incidents the re-assert heals (hw273
	// 18:33:57Z), and a same-value re-apply has no consumer impact.
	lastActingPrimary string
}

// SetupWithManager wires the reconciler into the controller-runtime
// Manager.
func (r *ContinuumReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.activeContinuums = map[string]*continuumGoroutine{}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(continuumGVK)
	return ctrl.NewControllerManagedBy(mgr).
		Named("continuum").
		For(u).
		Complete(r)
}

// Reconcile is the per-CR entry point.
func (r *ContinuumReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("name", req.Name, "namespace", req.Namespace)
	key := req.NamespacedName.String()

	cr, err := r.fetchCR(ctx, req.NamespacedName)
	if apierrors.IsNotFound(err) {
		log.Info("continuum deleted; cleaning up per-CR goroutine")
		r.stopGoroutine(key)
		return ctrl.Result{}, nil
	}
	if err != nil {
		log.Error(err, "fetch Continuum CR")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	spec, vErr := parseSpec(cr)
	if vErr != nil {
		log.Error(vErr, "invalid Continuum spec")
		return ctrl.Result{}, r.patchStatus(ctx, cr, statusUpdate{
			Phase:   PhaseFailed,
			Reason:  "Invalid",
			Message: vErr.Error(),
		})
	}

	r.activeContinuumsMu.Lock()
	if r.activeContinuums == nil {
		r.activeContinuums = map[string]*continuumGoroutine{}
	}
	g, ok := r.activeContinuums[key]
	newCR := !ok
	if !ok {
		w, sErr := r.selectWitness(req.NamespacedName, spec)
		if sErr != nil {
			r.activeContinuumsMu.Unlock()
			log.Error(sErr, "witness selector")
			return ctrl.Result{}, r.patchStatus(ctx, cr, statusUpdate{
				Phase:   PhaseFailed,
				Reason:  "WitnessUnavailable",
				Message: sErr.Error(),
			})
		}
		gctx, cancel := context.WithCancel(context.Background())
		g = &continuumGoroutine{
			cancel:              cancel,
			witness:             w,
			holder:              r.HoldingRegion,
			lastSpecFingerprint: switchoverSpecFingerprint(spec),
		}
		r.activeContinuums[key] = g
		go r.runPerCR(gctx, req.NamespacedName, spec, w)
		log.Info("started per-CR goroutine")
	}
	_ = g
	r.activeContinuumsMu.Unlock()

	// F-1: emit `continuum-cr-created` on first observation. Done
	// AFTER mutex release so the publish doesn't hold the lock.
	if newCR {
		_ = r.publishCRCreated(ctx, req.NamespacedName, spec)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// runPerCR is the per-Continuum-CR background loop.
func (r *ContinuumReconciler) runPerCR(ctx context.Context, nn types.NamespacedName, initialSpec ContinuumSpec, w witness.Client) {
	log := ctrl.LoggerFrom(ctx).WithValues("name", nn.Name, "namespace", nn.Namespace)
	renewInterval := time.Duration(initialSpec.RenewSeconds) * time.Second
	if renewInterval == 0 {
		renewInterval = DefaultLeaseRenewSeconds * time.Second
	}
	ttl := time.Duration(initialSpec.TTLSeconds) * time.Second
	if ttl == 0 {
		ttl = DefaultLeaseTTLSeconds * time.Second
	}

	// Resolve the region identity this controller holds the lease as.
	// CATALYST_REGION (r.HoldingRegion) wins when set; otherwise we fall
	// back to the CR's declared primaryRegion. The continuum-controller
	// runs ONLY on the primary-side cluster (the cnpg-pair chart + the
	// bp-continuum HR are side-gated to the primary), so the controller
	// IS the primary region — falling back to spec.primaryRegion makes a
	// healthy 2-region pair self-bootstrap the lease as leaseHolder=
	// <primaryRegion> even when the env var was never wired (#3829).
	holder := r.effectiveHoldingRegion(initialSpec)

	leaseState, err := w.Acquire(ctx, holder, ttl)
	if err == nil {
		_ = r.publishLeaseAcquired(ctx, nn, initialSpec, leaseState)
	} else {
		log.Info("initial lease acquire failed; will retry on renew tick", "holder", holder, "err", err)
	}

	tick := time.NewTicker(renewInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = w.Release(context.Background(), holder)
			log.Info("per-CR goroutine exiting")
			return
		case <-tick.C:
		}

		cr, err := r.fetchCR(ctx, nn)
		if apierrors.IsNotFound(err) {
			log.Info("CR vanished mid-loop; exiting")
			_ = w.Release(context.Background(), holder)
			return
		}
		if err != nil {
			log.Error(err, "fetch CR mid-loop")
			continue
		}
		spec, vErr := parseSpec(cr)
		if vErr != nil {
			log.Error(vErr, "spec invalid mid-loop")
			continue
		}
		// Re-resolve in case the primaryRegion changed (and the env var
		// is unset). Stable when CATALYST_REGION is wired.
		holder = r.effectiveHoldingRegion(spec)

		// F-1: detect spec drift on switchover-relevant fields and
		// emit `continuum-config-changed` (rare; happens when the
		// operator edits the CR mid-lifecycle).
		curFingerprint := switchoverSpecFingerprint(spec)
		r.activeContinuumsMu.Lock()
		prevFingerprint := ""
		if g, ok := r.activeContinuums[nn.String()]; ok {
			prevFingerprint = g.lastSpecFingerprint
			g.lastSpecFingerprint = curFingerprint
		}
		r.activeContinuumsMu.Unlock()
		if prevFingerprint != "" && prevFingerprint != curFingerprint {
			_ = r.publishConfigChanged(ctx, nn, spec, prevFingerprint, curFingerprint)
		}

		newState, rErr := w.Renew(ctx, holder, ttl)
		if errors.Is(rErr, witness.ErrLeaseLost) {
			_ = r.publishLeaseLost(ctx, nn, spec, leaseState)
			if newState, err = w.Acquire(ctx, holder, ttl); err != nil {
				// F-1: distinguish "held by another" (collision) from generic acquire failure.
				if errors.Is(err, witness.ErrLeaseHeldByAnother) {
					_ = r.publishLeaseCollision(ctx, nn, spec, err)
				}
				// #3195 (hw130): quorum-unreachable is NOT a competing
				// holder — log it as the wiring problem it is, so the
				// operator fixes resolvers instead of hunting a phantom
				// primary. Same safety posture (no promote) either way.
				if errors.Is(err, witness.ErrQuorumUnavailable) {
					log.Info("witness read-quorum unavailable — check leaseClient resolvers/wiring", "err", err)
				} else {
					log.Info("lease lost; new holder in charge", "err", err)
				}
				_ = r.patchStatusFromCR(ctx, cr, spec, witness.State{}, cnpg.Status{}, cnpg.Status{}, cnpg.StandbyObservation{}, false, "")
				continue
			}
			_ = r.publishLeaseAcquired(ctx, nn, spec, newState)
		} else if rErr != nil {
			log.Error(rErr, "lease renew (non-lost)")
			continue
		}
		leaseState = newState

		// #4901 — resolve the required synchronous hot-standby's
		// availability alongside lag. standby.Known stays false when the
		// CR names no cnpg pair (dr-spine/raft continuums) or the pair's
		// replica half is unresolvable (provisioning in flight) — the
		// status patch then preserves any prior StandbyAvailable
		// condition instead of false-alarming.
		var primaryStatus, replicaStatus cnpg.Status
		var standby cnpg.StandbyObservation
		if reader := r.cnpgReader(); reader != nil && spec.CNPGNamespace != "" && spec.CNPGPair != "" {
			primary, replica, fErr := reader.FindPair(ctx, spec.CNPGNamespace, spec.CNPGPair)
			if fErr == nil {
				primaryStatus, _, _ = reader.Get(ctx, primary.GetNamespace(), primary.GetName())
				var gErr error
				replicaStatus, _, gErr = reader.Get(ctx, replica.GetNamespace(), replica.GetName())
				if gErr == nil {
					standby = cnpg.StandbyObservation{
						Known:          true,
						Available:      cnpg.StandbyAvailable(replicaStatus),
						ReplicaCluster: replica.GetName(),
					}
					// #5125 Defect-2 — step-8 rejoin-repair check. Reuses
					// the SAME FindPair + Get calls above; no extra CNPG
					// API traffic. Runs every tick regardless of
					// switchover-requested/lag-breach below — its own
					// gate (DetectDivergence) is narrow enough to be a
					// safe no-op outside the region-a-resume-with-
					// divergence scenario (rejoin.go).
					r.checkRejoin(ctx, nn, reader, spec.CNPGNamespace, primary.GetName(), primaryStatus, replica.GetName(), replicaStatus)
					// #5224 — canonical role-password re-assert on
					// acting-primary transition (promote / failback /
					// goroutine restart mid-flap). Touches the acting
					// primary's managed-role passwordSecrets so CNPG
					// re-applies the canonical passwords, healing any
					// out-of-band role clobber (the hw273 harbor 28P01
					// lockout) CNPG itself cannot detect. Same reused
					// pair reads; role_reassert.go.
					r.reassertRolesOnPrimaryTransition(ctx, nn, reader, spec.CNPGNamespace, primary.GetName(), primaryStatus, replica.GetName(), replicaStatus)
				}
			}
		}

		switchoverRequested := isSwitchoverRequested(cr)
		maxLag := cnpg.MaxLagSeconds(primaryStatus, replicaStatus)
		lagBreached := spec.RTOSeconds > 0 && maxLag > spec.RTOSeconds
		toRegion := pickFailoverTarget(spec)

		switch {
		case switchoverRequested:
			_ = r.publishCNPGPromotable(ctx, nn, spec, maxLag)
			r.runSwitchover(ctx, cr, spec, toRegion, "operator-requested", maxLag, w)
			_ = r.clearSwitchoverRequest(ctx, cr)
		case lagBreached:
			_ = r.publishLagBreach(ctx, nn, spec, maxLag)
			r.runSwitchover(ctx, cr, spec, toRegion, "lag-breach", maxLag, w)
		default:
			_ = r.patchStatusFromCR(ctx, cr, spec, leaseState, primaryStatus, replicaStatus, standby, false, "")
			_ = r.publishReconcileSuccess(ctx, nn, spec, maxLag)
		}
	}
}

// runSwitchover instantiates the Sequencer and executes the plan.
func (r *ContinuumReconciler) runSwitchover(
	ctx context.Context,
	cr *unstructured.Unstructured,
	spec ContinuumSpec,
	toRegion, reason string,
	maxLag int,
	w witness.Client,
) {
	log := ctrl.LoggerFrom(ctx)
	if toRegion == "" {
		log.Info("no failover target available; skipping switchover")
		return
	}
	plan := switchover.SwitchoverPlan{
		ContinuumName:      types.NamespacedName{Namespace: cr.GetNamespace(), Name: cr.GetName()}.String(),
		ApplicationName:    spec.ApplicationRef,
		FromRegion:         spec.PrimaryRegion,
		ToRegion:           toRegion,
		Mechanism:          spec.Mechanism,
		CNPGPair:           spec.CNPGPair,
		CNPGNamespace:      spec.CNPGNamespace,
		RaftTransition:     spec.RaftTransition,
		HTTPRouteName:      spec.HTTPRouteName,
		HTTPRouteNamespace: spec.HTTPRouteNamespace,
		PDMZone:            spec.PDMZone,
		SynthParams:        spec.SynthParams,
		Application:        spec.Application,
		Reason:             reason,
		InitiatedBy:        spec.InitiatedBy,
		LeaseTTL:           time.Duration(spec.TTLSeconds) * time.Second,
	}

	_ = r.patchStatus(ctx, cr, statusUpdate{
		Phase:   PhaseSwitchingOver,
		Reason:  "InProgress",
		Message: fmt.Sprintf("step 1/7 starting; from=%s to=%s", plan.FromRegion, plan.ToRegion),
	})

	zone := plan.PDMZone
	seq := &switchover.Sequencer{
		CNPG:         r.cnpgReader(),
		RaftPromoter: r.RaftPromoter,
		Witness:      w,
		PDMCommit: func(ctx context.Context, records []dns.Record) error {
			if r.PDMClient == nil {
				return errors.New("PDMClient not configured")
			}
			z := zone
			if z == "" {
				z = currentZone(records)
			}
			if err := r.PDMClient.CommitLuaRecords(ctx, z, records); err != nil {
				return err
			}
			// Z1 follow-up: surface the just-committed lua-record body
			// on status.lastLuaRecord. Patches AFTER the PDM commit
			// succeeds (not on dry-run nor on PDM error) so observers
			// always see what is actually live in PowerDNS. Rollbacks
			// also flow through PDMCommit, so the field re-tracks to
			// the rolled-back records — keeping the contract "what
			// status says == what PDM has".
			_ = r.patchStatus(ctx, cr, statusUpdate{
				LastLuaRecord:   luaRecordStatusValue(records),
				LastLuaRecordAt: r.now().UTC().Format(time.RFC3339),
			})
			return nil
		},
		HTTPRoute: r.Drainer,
		Audit:     r.Audit,
		Sleep:     r.Sleep,
		Now:       r.Now,
		StepHook: func(step int, name string) {
			_ = r.patchStatus(ctx, cr, statusUpdate{
				Phase:   PhaseSwitchingOver,
				Reason:  "InProgress",
				Message: fmt.Sprintf("step %d/7 — %s", step, name),
			})
		},
	}

	res := seq.Execute(ctx, plan)
	if res.Err != nil {
		_ = r.patchStatus(ctx, cr, statusUpdate{
			Phase:                PhaseDegraded,
			Reason:               "SwitchoverFailed",
			Message:              res.Err.Error(),
			LastSwitchoverResult: "Failed",
		})
		return
	}
	_ = r.patchStatus(ctx, cr, statusUpdate{
		Phase:                PhaseFailedOver,
		Reason:               "Succeeded",
		Message:              fmt.Sprintf("switchover %s → %s completed", plan.FromRegion, plan.ToRegion),
		LastSwitchoverResult: "Success",
		LastSwitchoverFrom:   plan.FromRegion,
		LastSwitchoverTo:     plan.ToRegion,
	})

	// F-3: chain the post-switchover health check after HealthDelay
	// (default 30s; tests override to 0). Runs in a goroutine so the
	// reconcile loop returns promptly. The health result is written
	// back to the CR status condition `LastSwitchoverHealthy`.
	go r.runPostSwitchoverHealth(plan, seq)
}

// runPostSwitchoverHealth waits HealthDelay then executes
// PostSwitchoverHealth + writes the result onto the CR's status
// condition `LastSwitchoverHealthy`. Best-effort: failures to write
// the status are logged but don't retry (next reconcile's
// patchStatusFromCR doesn't clobber the condition because patchStatus
// preserves it on no-op writes).
func (r *ContinuumReconciler) runPostSwitchoverHealth(plan switchover.SwitchoverPlan, seq *switchover.Sequencer) {
	delay := r.HealthDelay
	if delay <= 0 {
		delay = 30 * time.Second
	}
	if delay > 0 {
		r.sleeper(delay)
	}

	// Use a fresh context with an outer timeout so a stuck DNS lookup
	// doesn't pin the goroutine.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report, err := seq.PostSwitchoverHealth(ctx, plan, r.HealthOpts)
	if err != nil {
		// Surface failure as Unknown — health check itself errored.
		_ = r.patchStatusByName(ctx, plan.ContinuumName, statusUpdate{
			LastSwitchoverHealthy:       "Unknown",
			LastSwitchoverHealthyDetail: fmt.Sprintf("PostSwitchoverHealth errored: %v", err),
			Reason:                      "HealthCheckErrored",
		})
		return
	}

	condStatus := "False"
	if report.OverallHealthy {
		condStatus = "True"
	}
	detail := summarizeHealth(report)
	_ = r.patchStatusByName(ctx, plan.ContinuumName, statusUpdate{
		LastSwitchoverHealthy:       condStatus,
		LastSwitchoverHealthyDetail: detail,
		Reason:                      "PostSwitchoverHealth",
	})
}

// checkRejoin runs step-8 (#5125 Defect-2 — re-clone-on-divergence)
// for this reconcile tick. It reuses the primary/replica CNPG status
// the caller (runPerCR) already fetched for the #4901 standby check —
// no extra API traffic.
//
// State is carried in the per-CR goroutine's in-memory RejoinState
// (same session-scoped pattern as lastSpecFingerprint) so attempts +
// backoff survive across ticks within one controller lifetime. The
// outcome is mirrored onto the CR status + an audit event ONLY when
// switchover.CheckRejoin reports a determination (a divergence was
// observed this tick) — a healthy/no-divergence tick is a pure no-op,
// exactly like the #4901 StandbyAvailable tri-state contract.
func (r *ContinuumReconciler) checkRejoin(
	ctx context.Context,
	nn types.NamespacedName,
	reader *cnpg.Reader,
	namespace string,
	primaryName string, primaryStatus cnpg.Status,
	replicaName string, replicaStatus cnpg.Status,
) {
	key := nn.String()
	r.activeContinuumsMu.Lock()
	g, ok := r.activeContinuums[key]
	var prior switchover.RejoinState
	if ok {
		prior = g.rejoinState
	}
	r.activeContinuumsMu.Unlock()
	if !ok {
		// No per-CR goroutine registered (shouldn't happen — checkRejoin
		// is only called FROM that goroutine — but never guess/panic).
		return
	}

	seq := &switchover.Sequencer{CNPG: reader, Now: r.Now}
	next, result := seq.CheckRejoin(ctx, namespace, primaryName, primaryStatus, replicaName, replicaStatus, prior, r.RejoinOptions)

	r.activeContinuumsMu.Lock()
	if g2, ok2 := r.activeContinuums[key]; ok2 {
		g2.rejoinState = next
	}
	r.activeContinuumsMu.Unlock()

	if !result.Diverged {
		return
	}

	cr, err := r.fetchCR(ctx, nn)
	if err == nil {
		_ = r.patchStatus(ctx, cr, statusUpdate{
			Reason:  "RejoinRepair",
			Message: result.Message,
			RejoinRepair: &rejoinRepairUpdate{
				Target:   result.Target,
				Attempts: next.Attempts,
				Bounded:  next.Bounded,
				Message:  result.Message,
			},
		})
	}

	if r.Audit == nil {
		return
	}
	// TypeRejoinRepair for an observed/attempted episode; TypeError once
	// Bounded — the same "operator must act" audit-type the sequencer's
	// step-N failures use (emitError in sequence.go), because a bounded
	// rejoin-repair IS an unresolved failure, not routine progress.
	evType := events.TypeRejoinRepair
	reason := "rejoin-divergence"
	if result.Bounded {
		evType = events.TypeError
		reason = "rejoin-repair-bounded"
	}
	_ = r.Audit.Publish(ctx, events.Event{
		Type:          evType,
		ContinuumName: key,
		Reason:        reason,
		Message:       result.Message,
		InitiatedBy:   "controller",
	})
}

// patchStatusByName re-fetches a Continuum CR by its
// "<namespace>/<name>" identifier and applies a statusUpdate. Used by
// the F-3 background health-check goroutine, which doesn't have the
// CR Unstructured pre-fetched.
func (r *ContinuumReconciler) patchStatusByName(ctx context.Context, identifier string, su statusUpdate) error {
	ns, name, ok := splitNamespacedName(identifier)
	if !ok {
		return fmt.Errorf("invalid identifier %q (expected <ns>/<name>)", identifier)
	}
	if r.Dyn == nil {
		return nil
	}
	cr, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.patchStatus(ctx, cr, su)
}

// splitNamespacedName parses "<ns>/<name>" → (ns, name, ok). Returns
// (_, _, false) when the input doesn't contain exactly one '/'.
func splitNamespacedName(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			if i == 0 || i == len(s)-1 {
				return "", "", false
			}
			// Reject multiple '/'.
			for j := i + 1; j < len(s); j++ {
				if s[j] == '/' {
					return "", "", false
				}
			}
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// summarizeHealth renders a one-line summary suitable for the
// LastSwitchoverHealthy condition's `message` field.
func summarizeHealth(rep switchover.HealthReport) string {
	passed, deferred, failed := 0, 0, 0
	for _, c := range rep.Checks {
		switch {
		case c.Deferred:
			deferred++
		case c.Passed:
			passed++
		default:
			failed++
		}
	}
	verdict := "healthy"
	if !rep.OverallHealthy {
		verdict = "UNHEALTHY"
	}
	return fmt.Sprintf("%s: %d passed, %d failed, %d deferred (new primary=%s)",
		verdict, passed, failed, deferred, rep.NewPrimaryRegion)
}

// sleeper is the injectable wait. Tests override.
func (r *ContinuumReconciler) sleeper(d time.Duration) {
	if r.Sleep != nil {
		r.Sleep(d)
		return
	}
	time.Sleep(d)
}

// luaRecordStatusValue projects a slice of dns.Record into the
// `{records: [{hostname, body, ttl, primaryRegion}]}` map shape
// surfaced on Continuum.status.lastLuaRecord (slice Z1 follow-up).
//
// Field name `body` (not `luaBody`) matches U-DR-1's LuaRecordView
// parser which checks `r['body']` / `rec['body']` to render. nil-safe:
// returns nil when records is empty so the patch is a no-op.
func luaRecordStatusValue(records []dns.Record) map[string]interface{} {
	if len(records) == 0 {
		return nil
	}
	out := make([]interface{}, 0, len(records))
	for _, rec := range records {
		out = append(out, map[string]interface{}{
			"hostname":      rec.Hostname,
			"body":          rec.LuaBody,
			"ttl":           int64(rec.TTL),
			"primaryRegion": rec.PrimaryRegion,
		})
	}
	return map[string]interface{}{"records": out}
}

// currentZone resolves the PowerDNS zone from a record set's first
// hostname (everything after the first dot). Empty input → empty
// output.
func currentZone(records []dns.Record) string {
	if len(records) == 0 {
		return ""
	}
	h := records[0].Hostname
	for i := 0; i < len(h); i++ {
		if h[i] == '.' {
			return h[i+1:]
		}
	}
	return h
}

// stopGoroutine cancels + removes the per-CR goroutine.
func (r *ContinuumReconciler) stopGoroutine(key string) {
	r.activeContinuumsMu.Lock()
	defer r.activeContinuumsMu.Unlock()
	g, ok := r.activeContinuums[key]
	if !ok {
		return
	}
	g.cancel()
	delete(r.activeContinuums, key)
}

// ActiveCount — test helper.
func (r *ContinuumReconciler) ActiveCount() int {
	r.activeContinuumsMu.Lock()
	defer r.activeContinuumsMu.Unlock()
	return len(r.activeContinuums)
}

// selectWitness picks a Client for the CR and stamps a per-CR slot
// key (NamespacedName) into the Selector config so the in-memory stub
// gives this CR an isolated lease.
//
// For the `k8s-lease` witness (#3829) we also stamp the controller's
// dynamic client + the Lease namespace into the config: that witness
// stores the lease in a native coordination.k8s.io/v1 Lease object in
// the control-plane cluster (zero external dependency — the
// air-gappable default), and the witness package cannot construct a
// dynamic client itself without an import cycle, so the controller
// hands its already-built one through the config map. Harmless for the
// other kinds (they ignore unknown cfg keys).
func (r *ContinuumReconciler) selectWitness(nn types.NamespacedName, spec ContinuumSpec) (witness.Client, error) {
	if r.WitnessSelector == nil {
		return nil, errors.New("WitnessSelector not configured")
	}
	cfg := map[string]any{}
	for k, v := range spec.LeaseClientConfig {
		cfg[k] = v
	}
	cfg["slot"] = nn.String()
	if r.Dyn != nil {
		cfg["dyn"] = r.Dyn
	}
	// Default the Lease namespace to the controller's own namespace when
	// the CR didn't pin one via leaseClient.config.namespace.
	if _, set := cfg["namespace"]; !set {
		if ns := r.LeaseNamespace; ns != "" {
			cfg["namespace"] = ns
		}
	}
	return r.WitnessSelector.Select(spec.LeaseClientKind, cfg)
}

// cnpgReader returns a fresh cnpg.Reader bound to r.Dyn.
func (r *ContinuumReconciler) cnpgReader() *cnpg.Reader {
	if r.Dyn == nil {
		return nil
	}
	return cnpg.NewReader(r.Dyn)
}

func (r *ContinuumReconciler) fetchCR(ctx context.Context, nn types.NamespacedName) (*unstructured.Unstructured, error) {
	if r.Dyn == nil {
		return nil, errors.New("Dyn not configured")
	}
	return r.Dyn.Resource(ContinuumGVR).Namespace(nn.Namespace).Get(ctx, nn.Name, metav1.GetOptions{})
}

// pickFailoverTarget — first hotStandbyRegions[] entry not equal to
// the current primary.
func pickFailoverTarget(spec ContinuumSpec) string {
	for _, r := range spec.HotStandbyRegions {
		if r != spec.PrimaryRegion {
			return r
		}
	}
	return ""
}

func isSwitchoverRequested(cr *unstructured.Unstructured) bool {
	v, _, _ := unstructured.NestedBool(cr.Object, "spec", "switchover", "requested")
	return v
}

// effectiveHoldingRegion resolves the region identity this controller
// instance holds the lease as. CATALYST_REGION (r.HoldingRegion) wins
// when set; otherwise we fall back to the CR's declared primaryRegion.
//
// The fallback is correct AND load-bearing (#3829): the
// continuum-controller runs ONLY on the primary-side cluster (the
// cnpg-pair chart + the bp-continuum HelmRelease are side-gated to the
// primary via `cnpg-pair.isPrimarySide`), so the running controller IS
// the primary region. Without this fallback, a Sovereign whose
// deployment never wired CATALYST_REGION (the live omantel.biz case)
// holds the lease as the empty string → IsHeldBy("") is always false →
// the CR is pinned Degraded / LeaseHeld=False forever even though the
// witness acquired the slot cleanly. Falling back to spec.primaryRegion
// makes a healthy 2-region pair self-bootstrap leaseHolder=<region-a>.
func (r *ContinuumReconciler) effectiveHoldingRegion(spec ContinuumSpec) string {
	if r.HoldingRegion != "" {
		return r.HoldingRegion
	}
	return spec.PrimaryRegion
}

func (r *ContinuumReconciler) clearSwitchoverRequest(ctx context.Context, cr *unstructured.Unstructured) error {
	_ = unstructured.SetNestedField(cr.Object, false, "spec", "switchover", "requested")
	if r.Dyn == nil {
		return nil
	}
	_, err := r.Dyn.Resource(ContinuumGVR).Namespace(cr.GetNamespace()).Update(ctx, cr, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// patchStatusFromCR — convenience wrapper for steady-state patches.
func (r *ContinuumReconciler) patchStatusFromCR(
	ctx context.Context,
	cr *unstructured.Unstructured,
	spec ContinuumSpec,
	lease witness.State,
	primaryStatus, replicaStatus cnpg.Status,
	standby cnpg.StandbyObservation,
	switchoverInProgress bool,
	stepLabel string,
) error {
	maxLag := cnpg.MaxLagSeconds(primaryStatus, replicaStatus)
	now := r.now()
	self := r.effectiveHoldingRegion(spec)
	heldByUs := lease.IsHeldBy(self, now)
	phase := PhaseHealthy
	if !heldByUs {
		phase = PhaseDegraded
	}
	// #4901 — a lost REQUIRED synchronous hot-standby degrades the
	// Continuum even while the lease is held correctly: an absent
	// standby also reads lag=0 (nothing is following), so without this
	// branch the CR stays phase=Healthy / replicationLagSeconds=0 for
	// the whole outage while writes stall on SyncRep (proven in the
	// G12 region-kill drill on hw232).
	if standby.Known && !standby.Available && phase == PhaseHealthy {
		phase = PhaseDegraded
	}
	su := statusUpdate{
		Phase:                 phase,
		PrimaryRegion:         spec.PrimaryRegion,
		LeaseHolder:           lease.Holder,
		SelfRegion:            self,
		ReplicationLagSeconds: maxLag,
		SwitchoverInProgress:  switchoverInProgress,
		Step:                  stepLabel,
	}
	if standby.Known {
		avail := standby.Available
		su.StandbyAvailable = &avail
		su.StandbyReplicaCluster = standby.ReplicaCluster
	}
	if !lease.ExpiresAt.IsZero() {
		su.LeaseExpiresAt = lease.ExpiresAt.Format(time.RFC3339)
	}
	return r.patchStatus(ctx, cr, su)
}

// statusUpdate — mutation set for one status patch.
type statusUpdate struct {
	Phase         string
	PrimaryRegion string
	LeaseHolder   string
	// SelfRegion is the region identity THIS controller holds the lease
	// as (CATALYST_REGION, or the CR's primaryRegion when unset — see
	// effectiveHoldingRegion). The LeaseHeld condition is True iff
	// LeaseHolder == SelfRegion. Empty falls back to r.HoldingRegion for
	// back-compat with callers that don't set it (#3829).
	SelfRegion            string
	LeaseExpiresAt        string
	ReplicationLagSeconds int
	SwitchoverInProgress  bool
	Step                  string

	LastSwitchoverResult string
	LastSwitchoverFrom   string
	LastSwitchoverTo     string

	// LastSwitchoverHealthy — set by F-3 post-switchover health
	// check. "True" / "False" / "Unknown" tri-state matches K8s
	// condition status semantics.
	LastSwitchoverHealthy       string
	LastSwitchoverHealthyDetail string

	// LastLuaRecord — slice Z1 follow-up. The rendered lua-record
	// body the reconciler last committed via PDM /v1/lua/commit.
	// Populated by runSwitchover's PDMCommit closure on every
	// successful commit (forward or rollback). Surfaced by U-DR-1's
	// LuaRecordView in the AppDetail Topology DR section so operators
	// can confirm the active probe + IP-set without reading
	// PowerDNS.
	//
	// Encoding: the `{records: [{hostname, luaBody, ttl, primaryRegion}]}`
	// shape produced by dns.MarshalRecords. Stored as a structured
	// map under status.lastLuaRecord so jq / kubectl / the UI can
	// project sub-fields without re-parsing.
	//
	// LastLuaRecordAt is the RFC3339 timestamp of the commit — set
	// by the controller (NOT by the caller) so the wire shape is the
	// observed transition time, not the request time.
	LastLuaRecord   map[string]interface{}
	LastLuaRecordAt string

	// StandbyAvailable — tri-state (#4901). nil = no determination this
	// pass (non-cnpg-pair CRs, or replica half unresolvable): any prior
	// StandbyAvailable condition is preserved and the standbyAvailable /
	// hotStandbyAbsent status fields are left untouched. Non-nil = write
	// status.standbyAvailable + status.hotStandbyAbsent and upsert the
	// StandbyAvailable condition (True/StandbyReachable or
	// False/StandbyUnreachable). Field + condition naming matches the
	// catalyst-api DR-panel projection (#4905) so the stored status and
	// the OBSERVED status agree.
	StandbyAvailable      *bool
	StandbyReplicaCluster string

	// RejoinRepair — #5125 Defect-2 step-8 outcome. nil = this pass made
	// no rejoin-repair determination (no divergence observed) — any
	// prior RejoinRepair condition/status is preserved. Non-nil = write
	// status.rejoinRepair + upsert the RejoinRepair condition, mirroring
	// the StandbyAvailable tri-state pattern above.
	RejoinRepair *rejoinRepairUpdate

	Reason  string
	Message string
}

// rejoinRepairUpdate is the #5125 Defect-2 step-8 status projection.
type rejoinRepairUpdate struct {
	// Target is the Cluster CR name step-8 is (re-)cloning.
	Target string
	// Attempts is the re-clone attempt count issued so far for the
	// current divergence episode.
	Attempts int
	// Bounded — true once MaxAttempts is exhausted without converging;
	// fail-loud, awaiting operator intervention.
	Bounded bool
	// Message is the human-readable detail surfaced on the condition.
	Message string
}

func (r *ContinuumReconciler) patchStatus(ctx context.Context, cr *unstructured.Unstructured, su statusUpdate) error {
	if r.Dyn == nil {
		return nil
	}
	latest, err := r.Dyn.Resource(ContinuumGVR).Namespace(cr.GetNamespace()).Get(ctx, cr.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-fetch for status: %w", err)
	}
	status, _, _ := unstructured.NestedMap(latest.Object, "status")
	if status == nil {
		status = map[string]interface{}{}
	}
	status["observedGeneration"] = latest.GetGeneration()
	if su.Phase != "" {
		status["phase"] = su.Phase
	}
	if su.PrimaryRegion != "" {
		status["primaryRegion"] = su.PrimaryRegion
	}
	if su.LeaseHolder != "" {
		status["leaseHolder"] = su.LeaseHolder
	}
	if su.LeaseExpiresAt != "" {
		status["leaseExpiresAt"] = su.LeaseExpiresAt
	}
	status["replicationLagSeconds"] = int64(su.ReplicationLagSeconds)
	status["switchoverInProgress"] = su.SwitchoverInProgress
	if su.Step != "" {
		status["switchoverStep"] = su.Step
	}
	if su.LastSwitchoverResult != "" {
		ls := map[string]interface{}{
			"result": su.LastSwitchoverResult,
			"from":   su.LastSwitchoverFrom,
			"to":     su.LastSwitchoverTo,
			"at":     r.now().UTC().Format(time.RFC3339),
		}
		status["lastSwitchover"] = ls
	}
	// Z1 follow-up — surface the rendered lua-record body the
	// reconciler last committed via PDM. Only written when the caller
	// supplied a non-nil map so steady-state reconciles preserve the
	// prior value.
	if su.LastLuaRecord != nil {
		status["lastLuaRecord"] = su.LastLuaRecord
		ts := su.LastLuaRecordAt
		if ts == "" {
			ts = r.now().UTC().Format(time.RFC3339)
		}
		status["lastLuaRecordAt"] = ts
	}

	conds := []interface{}{}
	if existing, ok := status["conditions"].([]interface{}); ok {
		for _, c := range existing {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := cm["type"].(string)
			if t == "LeaseHeld" || t == "Ready" {
				continue
			}
			// F-3: drop the prior LastSwitchoverHealthy condition
			// when we're about to write a new one; preserve it
			// otherwise so the operator can see the last result
			// across reconciles.
			if t == "LastSwitchoverHealthy" && su.LastSwitchoverHealthy != "" {
				continue
			}
			// #4901: same preserve-unless-rewriting contract for the
			// StandbyAvailable condition — a pass with no determination
			// (lease-lost patches, switchover step patches) keeps the
			// last observed standby posture visible.
			if t == "StandbyAvailable" && su.StandbyAvailable != nil {
				continue
			}
			// #5125 Defect-2: same preserve-unless-rewriting contract for
			// the RejoinRepair condition.
			if t == "RejoinRepair" && su.RejoinRepair != nil {
				continue
			}
			conds = append(conds, c)
		}
	}
	// LeaseHeld is True iff the lease's holder is THIS controller's
	// region identity. SelfRegion (CATALYST_REGION, or the CR's
	// primaryRegion fallback — #3829) is preferred; fall back to
	// r.HoldingRegion for callers that don't stamp SelfRegion.
	self := su.SelfRegion
	if self == "" {
		self = r.HoldingRegion
	}
	conds = append(conds, map[string]interface{}{
		"type":               "LeaseHeld",
		"status":             boolToCondStatus(su.LeaseHolder == self && su.LeaseHolder != ""),
		"reason":             firstNonEmpty(su.Reason, "Reconciled"),
		"message":            firstNonEmpty(su.Message, ""),
		"lastTransitionTime": r.now().UTC().Format(time.RFC3339),
	})
	conds = append(conds, map[string]interface{}{
		"type":               "Ready",
		"status":             phaseToReady(su.Phase),
		"reason":             firstNonEmpty(su.Reason, "Reconciled"),
		"message":            firstNonEmpty(su.Message, ""),
		"lastTransitionTime": r.now().UTC().Format(time.RFC3339),
	})
	if su.LastSwitchoverHealthy != "" {
		conds = append(conds, map[string]interface{}{
			"type":               "LastSwitchoverHealthy",
			"status":             su.LastSwitchoverHealthy,
			"reason":             firstNonEmpty(su.Reason, "PostSwitchoverHealth"),
			"message":            firstNonEmpty(su.LastSwitchoverHealthyDetail, su.Message),
			"lastTransitionTime": r.now().UTC().Format(time.RFC3339),
		})
	}
	// #4901 — surface the required synchronous hot-standby's
	// availability on the CR the controller owns, mirroring the
	// catalyst-api DR-panel projection's naming (#4905) so the two
	// surfaces agree. Only written when this pass made a determination.
	if su.StandbyAvailable != nil {
		status["standbyAvailable"] = *su.StandbyAvailable
		status["hotStandbyAbsent"] = !*su.StandbyAvailable
		cond := map[string]interface{}{
			"type":               "StandbyAvailable",
			"status":             boolToCondStatus(*su.StandbyAvailable),
			"reason":             "StandbyReachable",
			"message":            fmt.Sprintf("required synchronous hot-standby (cnpg-pair replica cluster %q) is reachable and following the primary", su.StandbyReplicaCluster),
			"lastTransitionTime": r.now().UTC().Format(time.RFC3339),
		}
		if !*su.StandbyAvailable {
			cond["reason"] = "StandbyUnreachable"
			cond["message"] = fmt.Sprintf("required synchronous hot-standby (cnpg-pair replica cluster %q) is unreachable; synchronous replication has no standby to acknowledge commits so writes stall and RPO=0 durability is at risk", su.StandbyReplicaCluster)
		}
		conds = append(conds, cond)
	}
	// #5125 Defect-2 — surface step-8's re-clone-on-divergence outcome.
	// Only written when this pass made a determination (a divergence was
	// observed); a healthy/no-divergence pass leaves any prior condition
	// untouched (preserve-unless-rewriting, same contract as
	// StandbyAvailable above).
	if su.RejoinRepair != nil {
		rr := su.RejoinRepair
		status["rejoinRepair"] = map[string]interface{}{
			"target":   rr.Target,
			"attempts": int64(rr.Attempts),
			"bounded":  rr.Bounded,
			"message":  rr.Message,
			"at":       r.now().UTC().Format(time.RFC3339),
		}
		// Unknown while an attempt is in flight / converging (neither
		// proven-healthy nor proven-failed yet); False only once Bounded
		// (definitively did not converge — operator intervention needed).
		condStatus := "Unknown"
		reason := "RejoinRepairAttempted"
		if rr.Bounded {
			condStatus = "False"
			reason = "RejoinRepairBounded"
		}
		conds = append(conds, map[string]interface{}{
			"type":               "RejoinRepair",
			"status":             condStatus,
			"reason":             reason,
			"message":            rr.Message,
			"lastTransitionTime": r.now().UTC().Format(time.RFC3339),
		})
	}
	status["conditions"] = conds
	latest.Object["status"] = status

	_, err = r.Dyn.Resource(ContinuumGVR).Namespace(latest.GetNamespace()).UpdateStatus(ctx, latest, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *ContinuumReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ContinuumReconciler) publishLeaseAcquired(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec, st witness.State) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeLeaseAcquired,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		ToPrimary:       st.Holder,
		Reason:          "lease-acquire",
		Message:         fmt.Sprintf("acquired lease for region %q (gen %d)", st.Holder, st.Generation),
	})
}

func (r *ContinuumReconciler) publishLeaseLost(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec, prev witness.State) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeLeaseLost,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		FromPrimary:     prev.Holder,
		Reason:          "lease-lost",
		Message:         "lease renew returned ErrLeaseLost; will attempt re-acquire",
	})
}

func (r *ContinuumReconciler) publishLagBreach(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec, lag int) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeCNPGLagBreach,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		FromPrimary:     spec.PrimaryRegion,
		LagSeconds:      lag,
		Reason:          "lag-breach",
		Message:         fmt.Sprintf("replication lag %ds > RTO %ds", lag, spec.RTOSeconds),
	})
}

func (r *ContinuumReconciler) publishCNPGPromotable(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec, lag int) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeCNPGPromotable,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		FromPrimary:     spec.PrimaryRegion,
		LagSeconds:      lag,
		Reason:          "promotable",
		Message:         fmt.Sprintf("standby promotable; lag=%ds", lag),
	})
}

func (r *ContinuumReconciler) publishReconcileSuccess(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec, lag int) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeReconcileSuccess,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		ToPrimary:       spec.PrimaryRegion,
		LagSeconds:      lag,
		Reason:          "ok",
		Message:         "reconcile loop iteration successful",
	})
}

// publishCRCreated — F-1 audit emit fired ONCE on the first
// observation of a Continuum CR (per-CR goroutine spin-up).
func (r *ContinuumReconciler) publishCRCreated(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeCRCreated,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		ToPrimary:       spec.PrimaryRegion,
		Reason:          "cr-observed",
		Message:         fmt.Sprintf("Continuum CR observed; per-CR goroutine started (primaryRegion=%s, hotStandby=%v)", spec.PrimaryRegion, spec.HotStandbyRegions),
	})
}

// publishConfigChanged — F-1 audit emit fired when the per-CR goroutine
// observes a switchover-relevant spec change.
func (r *ContinuumReconciler) publishConfigChanged(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec, prev, cur string) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeConfigChanged,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		ToPrimary:       spec.PrimaryRegion,
		Reason:          "config-drift",
		Message:         fmt.Sprintf("switchover-relevant spec changed: prev=%s cur=%s (primaryRegion=%s, hotStandby=%v)", prev, cur, spec.PrimaryRegion, spec.HotStandbyRegions),
	})
}

// publishLeaseCollision — F-1 audit emit fired when an Acquire on the
// witness returns ErrLeaseHeldByAnother during the renew loop. Rare:
// signals the lease-loss-then-rapid-acquire happened across two
// regions (split-brain warning material).
func (r *ContinuumReconciler) publishLeaseCollision(ctx context.Context, nn types.NamespacedName, spec ContinuumSpec, cause error) error {
	if r.Audit == nil {
		return nil
	}
	return r.Audit.Publish(ctx, events.Event{
		Type:            events.TypeLeaseCollision,
		ContinuumName:   nn.String(),
		ApplicationName: spec.ApplicationRef,
		FromPrimary:     spec.PrimaryRegion,
		Reason:          "lease-collision",
		Message:         fmt.Sprintf("Acquire returned ErrLeaseHeldByAnother (split-brain warning): %v", cause),
	})
}

// switchoverSpecFingerprint returns a content hash of the
// switchover-relevant spec fields. Used by the per-CR goroutine to
// detect spec drift (F-1's continuum-config-changed audit emit). NOT
// the same as switchover.planFingerprint — that one's input is a
// SwitchoverPlan, this one's input is a ContinuumSpec.
func switchoverSpecFingerprint(spec ContinuumSpec) string {
	return fmt.Sprintf("primary=%s|hot=%v|kind=%s|ttl=%d|renew=%d|rto=%d|auto=%t|mech=%s|pair=%s/%s|raft=%s/%s/%s|hr=%s/%s|zone=%s",
		spec.PrimaryRegion,
		spec.HotStandbyRegions,
		spec.LeaseClientKind,
		spec.TTLSeconds,
		spec.RenewSeconds,
		spec.RTOSeconds,
		spec.AutoFailover,
		spec.Mechanism,
		spec.CNPGNamespace, spec.CNPGPair,
		spec.RaftTransition.Namespace, spec.RaftTransition.Pod, spec.RaftTransition.PodSelector,
		spec.HTTPRouteNamespace, spec.HTTPRouteName,
		spec.PDMZone,
	)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func boolToCondStatus(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func phaseToReady(phase string) string {
	switch phase {
	case PhaseHealthy, PhaseFailedOver:
		return "True"
	case PhasePending, PhaseSwitchingOver:
		return "Unknown"
	default:
		return "False"
	}
}
