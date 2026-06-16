// Package switchover ships the 7-step Continuum switchover sequencer.
//
// Per docs/EPICS-1-6-unified-design.md §9.3 a switchover from
// `from` (old primary) to `to` (new primary) goes through:
//
//	step-1  validate-lease     — confirm the lease holder is `from` (or that we hold quorum to take it)
//	step-2  cordon-old         — stop the old primary accepting writes (mechanism-specific)
//	step-3  drain-traffic      — flip Cilium HTTPRoute weight=0 toward old primary; sleep 10s
//	step-4  flip-dns           — write the lua-record body via PDM /v1/lua/commit; the new primary's IPs come first
//	step-5  swap-lease         — release the old lease, acquire the new lease against the witness
//	step-6  promote-new        — promote the standby to primary (mechanism-specific)
//	step-7  audit              — emit `continuum-switchover` on NATS catalyst.audit
//
// Steps 2 + 6 are the only MECHANISM-SPECIFIC steps — they dispatch
// through a per-mechanism Promoter (promoter.go). For the default
// `cnpg-pair` mechanism they cordon via a `cnpg.io/cluster.primary`
// annotation + flip `spec.replica.enabled` on the two Cluster CR halves;
// for `raft-transition` (bp-openbao) they run the OpenBao OSS peers.json
// recovery (optional snapshot restore → write a single-voter peers.json on
// the surviving standby → restart its Pod so it self-elects as leader;
// `transition-to-primary` is Enterprise-only and absent in OSS — #3492).
// The other five steps are mechanism-agnostic.
//
// Each step is atomic and registers a rollback hook. On step-N
// failure, steps {N-1 .. 1} are rolled back in reverse order. Steps
// 1, 4, 5 are externally-observable (DNS / witness write); their
// rollbacks are best-effort idempotent (re-issue the prior write).
//
// The sequencer is goroutine-safe but a single Continuum CR's
// per-CR goroutine drives one Sequencer.Execute at a time (CR-level
// state machine prevents concurrent switchover).
package switchover

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// SwitchoverPlan captures all inputs the sequencer needs.
//
// This struct is part of the cross-slice contract: K-Cont-3
// (witness impls) + U-DR-1 (UI) wire against this shape. Field
// renames here ripple through the sequencing code AND the sibling
// slices' status / audit decoders — coordinate through the
// canonical-seam map before changing.
type SwitchoverPlan struct {
	// ContinuumName is `<namespace>/<name>` of the Continuum CR
	// driving the switchover. Stamped on every audit emit + log line.
	ContinuumName string

	// ApplicationName is `<namespace>/<name>` of the Application CR
	// the Continuum governs.
	ApplicationName string

	// FromRegion is the host-cluster name currently primary.
	FromRegion string

	// ToRegion is the host-cluster name being promoted to primary.
	ToRegion string

	// Mechanism selects the state-store-promotion strategy for steps 2 +
	// 6 (the only mechanism-specific steps). Empty defaults to
	// `cnpg-pair` so existing Continuum CRs keep their prior behavior.
	// `raft-transition` drives the bp-openbao promotion instead (#3492).
	// Mirrors the Blueprint topology declaration's
	// switchover.mechanism column (docs/topology-matrix.md).
	Mechanism Mechanism

	// CNPGPair is the pair name (label
	// catalyst.openova.io/cnpg-pair) Continuum operates on. The
	// sequencer's CNPG steps look up the (primary, replica) Cluster
	// CRs by this label. Required only for the cnpg-pair mechanism.
	CNPGPair string

	// CNPGNamespace is the K8s namespace the cluster-pair lives in.
	CNPGNamespace string

	// RaftTransition carries the openbao standby target for the
	// `raft-transition` mechanism (namespace / pod / staged snapshot
	// path). Ignored for cnpg-pair. Filled by the controller from the
	// Continuum CR's spec.switchover.raftTransition block.
	RaftTransition RaftTransitionTarget

	// HTTPRouteName + HTTPRouteNamespace are the Gateway-API
	// HTTPRoute resource the sequencer drains. Empty values make
	// step-3 a no-op (single-region degenerate case).
	HTTPRouteName      string
	HTTPRouteNamespace string

	// DrainSeconds is the wait between issuing the weight=0 PATCH and
	// proceeding to step-4. Default 10s per design doc. Test code
	// passes 0.
	DrainSeconds int

	// PDMZone is the PowerDNS zone Continuum writes lua-records into.
	// Typically `<sovereign-fqdn>` (e.g. `acme.openova.io`).
	PDMZone string

	// SynthParams supplies per-region IP map + healthcheck config.
	// The sequencer mutates only PrimaryRegion (set to ToRegion at
	// step-4). Caller fills the rest from the Continuum CR spec +
	// Application status.
	SynthParams dns.SynthParams

	// Application is the Application Unstructured passed to the lua
	// synthesizer for hostname extraction. Optional when
	// SynthParams.Hostnames is non-empty.
	Application *unstructured.Unstructured

	// Reason is the cause label stamped on the audit event
	// (operator-requested | lag-breach | lease-loss | failback).
	Reason string

	// InitiatedBy is the operator email or "auto-failover".
	InitiatedBy string

	// LeaseTTL is the TTL passed to Witness.Acquire in step-5.
	// Default 30s per design doc.
	LeaseTTL time.Duration

	// #2933 (2026-06-03): explicit step-level timeouts. UAT Wave-2
	// caught that step-4 (PDM commit) and step-7 (NATS audit publish)
	// inherit only the caller's context deadline. If PDM hangs OR NATS
	// is slow, the whole switchover hangs past the ≤60s RTO SLA.
	//
	// PDMTimeout — applied via context.WithTimeout around s.PDMCommit
	// in step-4. Default 5s (PDM commit is single HTTP write).
	// AuditPublishTimeout — applied around s.Audit.Publish in step-7.
	// Default 3s (NATS JetStream ACK).
	PDMTimeout          time.Duration
	AuditPublishTimeout time.Duration
}

// Defaults applies missing-field defaults. Returns a copy.
func (p SwitchoverPlan) Defaults() SwitchoverPlan {
	out := p
	if out.DrainSeconds == 0 {
		out.DrainSeconds = 10
	}
	if out.LeaseTTL == 0 {
		out.LeaseTTL = 30 * time.Second
	}
	if out.Reason == "" {
		out.Reason = "operator-requested"
	}
	// #2933 (2026-06-03): default step timeouts so a zero-value Plan
	// doesn't hang on PDM/NATS slowness.
	if out.PDMTimeout == 0 {
		out.PDMTimeout = 5 * time.Second
	}
	if out.AuditPublishTimeout == 0 {
		out.AuditPublishTimeout = 3 * time.Second
	}
	return out
}

// Validate checks required fields. Returns nil on success.
func (p SwitchoverPlan) Validate() error {
	if p.ContinuumName == "" {
		return errors.New("plan: ContinuumName is required")
	}
	if p.FromRegion == "" || p.ToRegion == "" {
		return errors.New("plan: FromRegion and ToRegion are required")
	}
	if p.FromRegion == p.ToRegion {
		return errors.New("plan: FromRegion == ToRegion (no-op)")
	}
	// Mechanism-specific required fields. Empty mechanism = cnpg-pair
	// (the historical default).
	mech := p.Mechanism
	if mech == "" {
		mech = DefaultMechanism
	}
	switch mech {
	case MechanismCNPGPair:
		if p.CNPGPair == "" || p.CNPGNamespace == "" {
			return errors.New("plan: CNPGPair + CNPGNamespace are required for cnpg-pair mechanism")
		}
	case MechanismRaftTransition:
		if p.RaftTransition.Namespace == "" {
			return errors.New("plan: RaftTransition.Namespace is required for raft-transition mechanism")
		}
		if p.RaftTransition.Pod == "" && p.RaftTransition.PodSelector == "" {
			return errors.New("plan: RaftTransition needs pod or podSelector for raft-transition mechanism")
		}
	case MechanismStateless:
		// #3375 §5.1 — the stateless (DNS-flip-only) mechanism needs NO
		// state-store target. The agnostic steps drive the whole
		// switchover off FromRegion/ToRegion (already validated above) +
		// the optional HTTPRoute/DNS knobs. Nothing further to require.
	default:
		return fmt.Errorf("plan: unknown switchover mechanism %q", mech)
	}
	return nil
}

// Result is what Execute returns to the caller. Tracks which steps
// succeeded, which step failed (if any), and the rollback log.
type Result struct {
	StepsCompleted []int
	FailedAtStep   int // 0 means none failed
	Err            error
	RolledBack     []int
	StartedAt      time.Time
	FinishedAt     time.Time
}

// Sequencer executes a SwitchoverPlan.
type Sequencer struct {
	// CNPG is the cluster-CR reader/writer (primary annotation +
	// replica.enabled flips). Serves the default cnpg-pair promotion
	// mechanism (steps 2 + 6).
	CNPG *cnpg.Reader

	// RaftPromoter serves the `raft-transition` promotion mechanism
	// (bp-openbao). Nil unless the controller wires a pod-exec backend;
	// a raft-transition plan with no RaftPromoter fails at step-2 with a
	// clear "no RaftPromoter wired" error (#3492).
	RaftPromoter Promoter

	// Witness is the lease backend (in-memory for tests, CF-KV /
	// dns-quorum in K-Cont-3).
	Witness witness.Client

	// PDMCommit is the lua-record committer. Wired by the controller
	// as a closure over the per-Continuum-CR PDM client + zone so
	// the sequencer doesn't need the CR's full config.
	PDMCommit func(ctx context.Context, records []dns.Record) error

	// HTTPRoute is the optional HTTPRoute drainer. Nil = step-3 is a
	// no-op (single-region degenerate).
	HTTPRoute HTTPRouteDrainer

	// Audit publisher (NATS JetStream in production; Recorder in
	// tests).
	Audit events.Publisher

	// Now is injectable for deterministic tests; defaults to time.Now.
	Now func() time.Time

	// Sleep is injectable for tests so step-3 doesn't actually sleep
	// in unit tests. Defaults to time.Sleep.
	Sleep func(time.Duration)

	// StepHook (optional) fires before each step starts. Tests use
	// it to inject failures; production wires it to the per-CR
	// status writer ("step 3/7 in flight").
	StepHook func(step int, name string)

	mu sync.Mutex
}

// HTTPRouteDrainer is the dependency for step-3 — drain HTTP traffic
// to the old primary. Real implementation patches the HTTPRoute's
// backendRefs[].weight via the dynamic client. The sequencer accepts
// an interface so tests can inject a fake.
type HTTPRouteDrainer interface {
	// SetWeightZero sets backendRefs weight=0 toward `region`.
	// Returns the prior weights (for rollback).
	SetWeightZero(ctx context.Context, namespace, name, region string) (priorWeights []int, err error)

	// RestoreWeights re-applies the prior weights (rollback path).
	RestoreWeights(ctx context.Context, namespace, name string, weights []int) error
}

// Execute runs the 7-step sequence. On step-N failure, rolls back
// steps 1..N-1 in reverse order (best-effort) and returns a Result
// describing what happened.
//
// Per the brief, the function is the ONLY entry point — callers
// should not invoke individual steps. The state-machine boundary is
// the per-Continuum-CR goroutine that calls Execute exactly once per
// switchover.
func (s *Sequencer) Execute(ctx context.Context, plan SwitchoverPlan) Result {
	s.mu.Lock()
	defer s.mu.Unlock()

	plan = plan.Defaults()
	res := Result{StartedAt: s.now()}
	defer func() {
		res.FinishedAt = s.now()
	}()

	if err := plan.Validate(); err != nil {
		res.Err = err
		res.FailedAtStep = 1
		s.emitError(ctx, plan, 1, err)
		return res
	}

	// rollbacks is a stack of (step, hook) pairs we'll unwind in
	// reverse order on failure.
	rollbacks := []struct {
		step int
		hook func(ctx context.Context) error
	}{}

	for step, fn := range s.steps(plan) {
		stepNum := step + 1
		s.fireHook(stepNum, fn.name)

		rollback, err := fn.run(ctx)
		if err != nil {
			res.Err = fmt.Errorf("step-%d (%s): %w", stepNum, fn.name, err)
			res.FailedAtStep = stepNum
			// Unwind in reverse.
			for i := len(rollbacks) - 1; i >= 0; i-- {
				rb := rollbacks[i]
				if rbErr := rb.hook(ctx); rbErr == nil {
					res.RolledBack = append(res.RolledBack, rb.step)
				}
			}
			s.emitError(ctx, plan, stepNum, res.Err)
			return res
		}

		res.StepsCompleted = append(res.StepsCompleted, stepNum)
		if rollback != nil {
			rollbacks = append(rollbacks, struct {
				step int
				hook func(ctx context.Context) error
			}{stepNum, rollback})
		}
	}
	return res
}

// stepFunc bundles a step name + run + (optional) rollback hook
// returned by run on success.
type stepFunc struct {
	name string
	// run executes the step. Returns a (rollback, err) tuple. The
	// rollback hook is registered on success and invoked on a LATER
	// step's failure.
	run func(ctx context.Context) (rollback func(ctx context.Context) error, err error)
}

// steps returns the 7 step closures for this plan. Each closure
// captures `s` and `plan` by reference so the rollback hooks see
// the same state.
func (s *Sequencer) steps(plan SwitchoverPlan) []stepFunc {
	return []stepFunc{
		// Step 1 — validate lease holder is current primary, OR we
		// hold quorum to assume it.
		{
			name: "validate-lease",
			run: func(ctx context.Context) (func(ctx context.Context) error, error) {
				if s.Witness == nil {
					return nil, errors.New("Witness client is required")
				}
				st, err := s.Witness.Read(ctx)
				if err != nil {
					return nil, fmt.Errorf("witness read: %w", err)
				}
				// Acceptable starting states:
				//  (a) the lease is currently held by FromRegion (we're flipping it to ToRegion)
				//  (b) the lease is unheld / expired (lease-loss path; ToRegion takes it cold)
				if st.Holder != "" && st.Holder != plan.FromRegion {
					return nil, fmt.Errorf("lease holder %q != plan.FromRegion %q", st.Holder, plan.FromRegion)
				}
				return nil, nil // no rollback needed for a read
			},
		},

		// Step 2 — cordon the old primary's writes. Mechanism-specific:
		// cnpg-pair annotates the primary Cluster CR; raft-transition is
		// a no-op (the old primary is in the killed region, nothing to
		// fence — the survivor keeps serving reads). Dispatched through
		// the per-mechanism Promoter (#3492).
		{
			name: "cordon-old-primary",
			run: func(ctx context.Context) (func(ctx context.Context) error, error) {
				promoter, err := s.promoterFor(plan)
				if err != nil {
					return nil, err
				}
				return promoter.Cordon(ctx, plan)
			},
		},

		// Step 3 — drain HTTP traffic via HTTPRoute weight flip.
		{
			name: "drain-http",
			run: func(ctx context.Context) (func(ctx context.Context) error, error) {
				if s.HTTPRoute == nil || plan.HTTPRouteName == "" {
					// Single-region degenerate / not yet wired —
					// skip with no rollback.
					return nil, nil
				}
				prior, err := s.HTTPRoute.SetWeightZero(ctx, plan.HTTPRouteNamespace, plan.HTTPRouteName, plan.FromRegion)
				if err != nil {
					return nil, err
				}
				if plan.DrainSeconds > 0 {
					s.sleep(time.Duration(plan.DrainSeconds) * time.Second)
				}
				return func(ctx context.Context) error {
					return s.HTTPRoute.RestoreWeights(ctx, plan.HTTPRouteNamespace, plan.HTTPRouteName, prior)
				}, nil
			},
		},

		// Step 4 — flip lua-record probe target via PDM commit.
		{
			name: "flip-dns",
			run: func(ctx context.Context) (func(ctx context.Context) error, error) {
				if s.PDMCommit == nil {
					return nil, errors.New("PDMCommit is required")
				}
				params := plan.SynthParams
				params.PrimaryRegion = plan.ToRegion
				records, err := dns.Synthesize(plan.Application, params)
				if err != nil {
					return nil, fmt.Errorf("synth records: %w", err)
				}
				// #2933 (2026-06-03): bound PDM commit by step-level
				// timeout (default 5s). Without this, PDM unreachable
				// + caller context lacking deadline = hang forever.
				pdmCtx, pdmCancel := context.WithTimeout(ctx, plan.PDMTimeout)
				defer pdmCancel()
				if err := s.PDMCommit(pdmCtx, records); err != nil {
					return nil, fmt.Errorf("pdm commit: %w", err)
				}
				// Rollback hook: re-emit the OLD primary's records.
				oldParams := plan.SynthParams
				oldParams.PrimaryRegion = plan.FromRegion
				oldRecords, oldErr := dns.Synthesize(plan.Application, oldParams)
				return func(ctx context.Context) error {
					if oldErr != nil {
						return oldErr
					}
					return s.PDMCommit(ctx, oldRecords)
				}, nil
			},
		},

		// Step 5 — swap lease: release on old, acquire on new.
		{
			name: "swap-lease",
			run: func(ctx context.Context) (func(ctx context.Context) error, error) {
				// Best-effort release on FromRegion — Acquire on
				// ToRegion is the operative call. If FromRegion no
				// longer holds (lease-loss path) Release is a no-op.
				_ = s.Witness.Release(ctx, plan.FromRegion)
				if _, err := s.Witness.Acquire(ctx, plan.ToRegion, plan.LeaseTTL); err != nil {
					return nil, fmt.Errorf("acquire on ToRegion: %w", err)
				}
				return func(ctx context.Context) error {
					_ = s.Witness.Release(ctx, plan.ToRegion)
					_, _ = s.Witness.Acquire(ctx, plan.FromRegion, plan.LeaseTTL)
					return nil
				}, nil
			},
		},

		// Step 6 — promote the standby to primary. Mechanism-specific:
		// cnpg-pair clears the cordon + flips replica.enabled on both
		// Cluster CR halves; raft-transition runs the OSS peers.json
		// recovery (optional snapshot restore → single-voter peers.json
		// write → Pod restart) on the surviving openbao standby.
		// Dispatched through the per-mechanism Promoter (#3492).
		{
			name: "promote-new-primary",
			run: func(ctx context.Context) (func(ctx context.Context) error, error) {
				promoter, err := s.promoterFor(plan)
				if err != nil {
					return nil, err
				}
				return promoter.Promote(ctx, plan)
			},
		},

		// Step 7 — emit audit event on NATS catalyst.audit.
		{
			name: "audit-emit",
			run: func(ctx context.Context) (func(ctx context.Context) error, error) {
				if s.Audit == nil {
					return nil, errors.New("Audit publisher is required")
				}
				// #2933 (2026-06-03): bound NATS publish by step-level
				// timeout (default 3s). Without this, NATS broker slow
				// to ACK = entire switchover blocks past ≤60s SLA.
				auditCtx, auditCancel := context.WithTimeout(ctx, plan.AuditPublishTimeout)
				defer auditCancel()
				return nil, s.Audit.Publish(auditCtx, events.Event{
					Type:            events.TypeSwitchover,
					ContinuumName:   plan.ContinuumName,
					ApplicationName: plan.ApplicationName,
					FromPrimary:     plan.FromRegion,
					ToPrimary:       plan.ToRegion,
					Step:            "7/7",
					Reason:          plan.Reason,
					InitiatedBy:     plan.InitiatedBy,
					Message:         fmt.Sprintf("switchover %s → %s completed", plan.FromRegion, plan.ToRegion),
				})
			},
		},
	}
}

// emitError fires an audit emit on a step failure. Best-effort —
// non-fatal if the audit publisher is itself broken.
func (s *Sequencer) emitError(ctx context.Context, plan SwitchoverPlan, step int, err error) {
	if s.Audit == nil {
		return
	}
	_ = s.Audit.Publish(ctx, events.Event{
		Type:            events.TypeError,
		ContinuumName:   plan.ContinuumName,
		ApplicationName: plan.ApplicationName,
		FromPrimary:     plan.FromRegion,
		ToPrimary:       plan.ToRegion,
		Step:            fmt.Sprintf("%d/7", step),
		Reason:          "step-failed",
		Message:         err.Error(),
		InitiatedBy:     plan.InitiatedBy,
	})
}

func (s *Sequencer) fireHook(step int, name string) {
	if s.StepHook != nil {
		s.StepHook(step, name)
	}
}

func (s *Sequencer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sequencer) sleep(d time.Duration) {
	if s.Sleep != nil {
		s.Sleep(d)
		return
	}
	time.Sleep(d)
}

// FailbackOptions controls RequestFailback's approval-wait behavior.
type FailbackOptions struct {
	// RequireApproval gates the failback on a `spec.failback.approved`
	// flip. The Sequencer doesn't itself read the CR — the per-CR
	// goroutine polls and signals via ApprovalCh.
	RequireApproval bool

	// ApprovalCh is closed (or sent on) when approval lands. The
	// Sequencer blocks on a receive when RequireApproval=true.
	ApprovalCh <-chan struct{}

	// ApprovalTimeout caps the wait. Zero = no timeout (block
	// forever, until ctx cancels).
	ApprovalTimeout time.Duration
}

// RequestFailback handles the operator-approval gate for a failback
// switchover.
//
// Behavior:
//   - if opts.RequireApproval == false → emits TypeFailbackPending then
//     proceeds straight to Execute(ctx, plan)
//   - if opts.RequireApproval == true → emits TypeFailbackPending then
//     blocks on opts.ApprovalCh; on signal, executes; emits
//     TypeFailbackCompleted on success
//
// On a Result.Err, the failback-completed audit is NOT emitted (the
// step-N error event from Execute already surfaced).
func (s *Sequencer) RequestFailback(ctx context.Context, plan SwitchoverPlan, opts FailbackOptions) Result {
	plan = plan.Defaults()
	if plan.Reason == "" || plan.Reason == "operator-requested" {
		plan.Reason = "failback"
	}
	// Emit pending event first.
	if s.Audit != nil {
		_ = s.Audit.Publish(ctx, events.Event{
			Type:            events.TypeFailbackPending,
			ContinuumName:   plan.ContinuumName,
			ApplicationName: plan.ApplicationName,
			FromPrimary:     plan.FromRegion,
			ToPrimary:       plan.ToRegion,
			Reason:          "approval-pending",
			InitiatedBy:     plan.InitiatedBy,
			Message:         "failback awaiting operator approval",
		})
	}

	if opts.RequireApproval {
		if opts.ApprovalCh == nil {
			return Result{Err: errors.New("RequestFailback: RequireApproval=true but ApprovalCh is nil")}
		}
		select {
		case <-opts.ApprovalCh:
			// approved
		case <-ctx.Done():
			return Result{Err: ctx.Err()}
		case <-timeoutCh(opts.ApprovalTimeout):
			return Result{Err: errors.New("RequestFailback: approval timeout")}
		}
	}

	res := s.Execute(ctx, plan)
	if res.Err == nil && s.Audit != nil {
		_ = s.Audit.Publish(ctx, events.Event{
			Type:            events.TypeFailbackCompleted,
			ContinuumName:   plan.ContinuumName,
			ApplicationName: plan.ApplicationName,
			FromPrimary:     plan.FromRegion,
			ToPrimary:       plan.ToRegion,
			Reason:          "approval-granted",
			InitiatedBy:     plan.InitiatedBy,
			Message:         "failback completed",
		})
	}
	return res
}

// timeoutCh returns a channel that fires after d, OR a nil channel
// (which never fires) when d is zero.
func timeoutCh(d time.Duration) <-chan time.Time {
	if d <= 0 {
		return nil
	}
	return time.After(d)
}
