// cutover_engine_lease.go — #6289: ONE driver for the cutover state machine.
//
// ── The defect ──────────────────────────────────────────────────────────────
//
// The cutover engine ran as a goroutine on context.Background() with no Lease
// and no holder identity. A Deployment rolling update deliberately overlaps
// the outgoing and incoming catalyst-api Pods, and ResumeInterruptedCutover
// runs on the incoming Pod's boot — so for the length of the termination
// grace period there were TWO drivers of the same self-sovereign-cutover-status
// ConfigMap and the same step Jobs. hw296 (2026-08-14, dep e689e3b34a75fdec):
// the new Pod reset the status and re-ran steps 1-2 while the old Pod's
// step-03 Job was still Running. Adoption (findExistingRunningJobForStep)
// kept that run from corrupting, which is evidence the guard helps — not that
// the race is absent. The cutover is the Pillar-5 sovereignty proof; two
// drivers interleaving on it can produce a cutoverComplete claim whose step
// record is not a faithful account of what ran.
//
// ── The fix ─────────────────────────────────────────────────────────────────
//
//  1. A coordination.k8s.io/Lease (`self-sovereign-cutover-engine`, in the
//     cutover namespace) is acquired before any engine entry — /start, the
//     startup resume, the internal trigger, the jobs retry — and held for the
//     life of the run. A Lease held by ANOTHER live holder refuses the entry
//     (409 at the HTTP edge; the resume waits for it to lapse or be released).
//  2. The engine runs on a context the process's shutdown hook cancels
//     (StopCutoverEngine, called from the drain), so the OUTGOING Pod stops
//     stepping the moment the rollout begins and releases the Lease — it does
//     not race the newcomer through its grace period.
//  3. A lost renewal (someone else took the Lease, or the apiserver refused
//     it) cancels the run the same way. An engine stopped this way leaves the
//     durable record as it stands (the in-flight step stays `running`) — it
//     never stamps a failure for an interruption that was not the step's.
//  4. The holder identity is recorded on the status ConfigMap (engineHolder,
//     engineHolderSince) so an interleaving is visible after the fact.
//
// Degraded mode: on a Sovereign whose catalyst-api RBAC predates this change
// the Lease API answers Forbidden. The engine then runs WITHOUT a lease and
// says so loudly, because refusing to run would turn an RBAC lag into a
// cutover that can never start — which is a worse outcome than today's
// behaviour on exactly the Sovereigns that need the cutover most.
package handler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	cutoverEngineLeaseName = "self-sovereign-cutover-engine"
	// cutoverEngineLeaseTTL is how long a holder that stops renewing keeps
	// the Lease. A SIGKILLed Pod (no drain) therefore blocks the successor
	// for at most this long; a drained Pod releases immediately.
	cutoverEngineLeaseTTL = 30 * time.Second
	// envCutoverEngineHolder overrides the holder identity (the Pod name is
	// the natural value; the chart sets it from metadata.name).
	envCutoverEngineHolder = "CATALYST_POD_NAME"
)

// errCutoverLeaseHeld is returned when another live holder owns the Lease.
type errCutoverLeaseHeld struct {
	holder string
	until  time.Time
}

func (e *errCutoverLeaseHeld) Error() string {
	return fmt.Sprintf("cutover engine lease %s is held by %s until %s", cutoverEngineLeaseName, e.holder, e.until.UTC().Format(time.RFC3339))
}

// cutoverEngineHolderIdentity names this process as a Lease holder.
func cutoverEngineHolderIdentity() string {
	if v := os.Getenv(envCutoverEngineHolder); v != "" {
		return v
	}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		return hn
	}
	return fmt.Sprintf("catalyst-api-%d", os.Getpid())
}

// cutoverEngineLease is a held Lease plus the renew loop that keeps it.
type cutoverEngineLease struct {
	deps   *cutoverDeps
	holder string
	// degraded is true when the Lease API was Forbidden and the engine is
	// running unfenced (see the file header).
	degraded bool
	// lost is closed when a renewal discovers the Lease is no longer ours.
	lost     chan struct{}
	lostOnce sync.Once
	stop     chan struct{}
	stopOnce sync.Once
	// done is closed when the renew loop exits; renewing records whether
	// one was ever started, so Release never waits on a loop that does
	// not exist.
	done     chan struct{}
	renewing bool
}

// acquireCutoverEngineLease takes the Lease for holder, or returns
// *errCutoverLeaseHeld when another holder is live. A Lease whose renewTime
// + duration has passed is taken over. Forbidden (RBAC lag on an older
// Sovereign) yields a DEGRADED lease that fences nothing but still carries
// the holder identity for the status record.
func acquireCutoverEngineLease(ctx context.Context, deps *cutoverDeps, holder string, now time.Time) (*cutoverEngineLease, error) {
	l := &cutoverEngineLease{
		deps:   deps,
		holder: holder,
		lost:   make(chan struct{}),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	ttl := int32(cutoverEngineLeaseTTL.Seconds())
	leases := deps.core.CoordinationV1().Leases(deps.ns)

	cur, err := leases.Get(ctx, cutoverEngineLeaseName, metav1.GetOptions{})
	switch {
	case apierrors.IsForbidden(err):
		l.degraded = true
		close(l.done)
		return l, nil
	case apierrors.IsNotFound(err):
		fresh := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cutoverEngineLeaseName,
				Namespace: deps.ns,
				Labels: map[string]string{
					cutoverStepPartOfLabel: cutoverStepPartOfValue,
				},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &ttl,
				AcquireTime:          &metav1.MicroTime{Time: now},
				RenewTime:            &metav1.MicroTime{Time: now},
			},
		}
		if _, cerr := leases.Create(ctx, fresh, metav1.CreateOptions{}); cerr != nil {
			if apierrors.IsForbidden(cerr) {
				l.degraded = true
				close(l.done)
				return l, nil
			}
			if apierrors.IsAlreadyExists(cerr) {
				// Lost the create race — re-read and fall into the held/expired judgement.
				return acquireCutoverEngineLease(ctx, deps, holder, now)
			}
			return nil, fmt.Errorf("create cutover engine lease: %w", cerr)
		}
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("get cutover engine lease: %w", err)
	}

	curHolder := ""
	if cur.Spec.HolderIdentity != nil {
		curHolder = *cur.Spec.HolderIdentity
	}
	expiry := leaseExpiry(cur)
	if curHolder != "" && curHolder != holder && now.Before(expiry) {
		return nil, &errCutoverLeaseHeld{holder: curHolder, until: expiry}
	}
	// Free, expired, or already ours — take it with optimistic concurrency.
	upd := cur.DeepCopy()
	if curHolder != holder {
		n := int32(0)
		if upd.Spec.LeaseTransitions != nil {
			n = *upd.Spec.LeaseTransitions
		}
		n++
		upd.Spec.LeaseTransitions = &n
		upd.Spec.AcquireTime = &metav1.MicroTime{Time: now}
	}
	upd.Spec.HolderIdentity = &holder
	upd.Spec.LeaseDurationSeconds = &ttl
	upd.Spec.RenewTime = &metav1.MicroTime{Time: now}
	if _, uerr := leases.Update(ctx, upd, metav1.UpdateOptions{}); uerr != nil {
		if apierrors.IsConflict(uerr) {
			// Someone else moved it between our Get and Update — judge again.
			return acquireCutoverEngineLease(ctx, deps, holder, now)
		}
		if apierrors.IsForbidden(uerr) {
			l.degraded = true
			close(l.done)
			return l, nil
		}
		return nil, fmt.Errorf("take cutover engine lease: %w", uerr)
	}
	return l, nil
}

// cutoverLeaseHeldByOther answers the HTTP edge's question without taking
// anything: is the engine Lease currently held by a LIVE holder that is not
// us? nil when free, expired, ours, or unanswerable (the run itself then
// judges with the authoritative acquire).
func cutoverLeaseHeldByOther(ctx context.Context, deps *cutoverDeps, me string) *errCutoverLeaseHeld {
	cur, err := deps.core.CoordinationV1().Leases(deps.ns).Get(ctx, cutoverEngineLeaseName, metav1.GetOptions{})
	if err != nil || cur.Spec.HolderIdentity == nil || *cur.Spec.HolderIdentity == "" || *cur.Spec.HolderIdentity == me {
		return nil
	}
	if exp := leaseExpiry(cur); time.Now().Before(exp) {
		return &errCutoverLeaseHeld{holder: *cur.Spec.HolderIdentity, until: exp}
	}
	return nil
}

// leaseExpiry is renewTime + leaseDurationSeconds (zero time when unset, so
// a malformed Lease reads as expired and is taken over).
func leaseExpiry(l *coordinationv1.Lease) time.Time {
	if l == nil || l.Spec.RenewTime == nil || l.Spec.LeaseDurationSeconds == nil {
		return time.Time{}
	}
	return l.Spec.RenewTime.Time.Add(time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second)
}

// Lost reports whether a renewal found the Lease no longer ours.
func (l *cutoverEngineLease) Lost() <-chan struct{} { return l.lost }

// StartRenewing keeps the Lease fresh every ttl/3 until Release or until the
// Lease is observed held by someone else (then Lost is closed). Not started
// for a degraded lease.
func (l *cutoverEngineLease) StartRenewing(interval time.Duration) {
	if l.degraded || l.renewing {
		return
	}
	l.renewing = true
	go func() {
		defer close(l.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				if !l.renewOnce() {
					l.lostOnce.Do(func() { close(l.lost) })
					return
				}
			}
		}
	}()
}

// renewOnce re-stamps renewTime if the Lease is still ours. Returns false
// when it is held by another identity (fencing). Transient apiserver errors
// are tolerated — the Lease simply ages toward its TTL.
func (l *cutoverEngineLease) renewOnce() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leases := l.deps.core.CoordinationV1().Leases(l.deps.ns)
	cur, err := leases.Get(ctx, cutoverEngineLeaseName, metav1.GetOptions{})
	if err != nil {
		return !apierrors.IsNotFound(err) // deleted from under us = lost; other errors = keep going
	}
	if cur.Spec.HolderIdentity == nil || *cur.Spec.HolderIdentity != l.holder {
		return false
	}
	upd := cur.DeepCopy()
	upd.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
	if _, uerr := leases.Update(ctx, upd, metav1.UpdateOptions{}); uerr != nil {
		return !apierrors.IsConflict(uerr) || l.stillOurs(ctx)
	}
	return true
}

func (l *cutoverEngineLease) stillOurs(ctx context.Context) bool {
	cur, err := l.deps.core.CoordinationV1().Leases(l.deps.ns).Get(ctx, cutoverEngineLeaseName, metav1.GetOptions{})
	if err != nil {
		return !apierrors.IsNotFound(err)
	}
	return cur.Spec.HolderIdentity != nil && *cur.Spec.HolderIdentity == l.holder
}

// Release stops renewing and hands the Lease back (holder cleared) so the
// successor acquires it immediately instead of waiting out the TTL. A Lease
// that is no longer ours is left alone.
func (l *cutoverEngineLease) Release() {
	l.stopOnce.Do(func() { close(l.stop) })
	if l.renewing {
		<-l.done
	}
	if l.degraded {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leases := l.deps.core.CoordinationV1().Leases(l.deps.ns)
	cur, err := leases.Get(ctx, cutoverEngineLeaseName, metav1.GetOptions{})
	if err != nil || cur.Spec.HolderIdentity == nil || *cur.Spec.HolderIdentity != l.holder {
		return
	}
	upd := cur.DeepCopy()
	empty := ""
	upd.Spec.HolderIdentity = &empty
	_, _ = leases.Update(ctx, upd, metav1.UpdateOptions{})
}

// ── Engine context + shutdown ───────────────────────────────────────────────

// cutoverEngineContext returns the process-wide context every engine run
// derives from. StopCutoverEngine cancels it; the drain hook calls that so
// an outgoing Pod yields instead of racing its successor (#6289).
func (h *Handler) cutoverEngineContext() context.Context {
	h.cutoverEngineMu.Lock()
	defer h.cutoverEngineMu.Unlock()
	if h.cutoverEngineCtx == nil {
		h.cutoverEngineCtx, h.cutoverEngineCancel = context.WithCancel(context.Background())
	}
	return h.cutoverEngineCtx
}

// StopCutoverEngine cancels the running engine (if any) and waits for it to
// yield — it releases the Lease on the way out. Safe to call when no engine
// is running. Bounded by the caller's budget via ctx.
func (h *Handler) StopCutoverEngine(ctx context.Context) {
	h.cutoverEngineMu.Lock()
	cancel := h.cutoverEngineCancel
	h.cutoverEngineMu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Wait for the in-process running flag to drop.
	bus := h.cutoverBusFor()
	for {
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// errCutoverEngineYielded marks a run that stopped because its context was
// cancelled (process shutdown) or its Lease was lost — NOT a step failure.
var errCutoverEngineYielded = errors.New("cutover engine yielded")

// cutoverRunCtx derives the per-run context: the engine context (shutdown),
// cancelled additionally when the Lease is lost.
func cutoverRunCtx(parent context.Context, lease *cutoverEngineLease) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if lease != nil && !lease.degraded {
		go func() {
			select {
			case <-lease.Lost():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}
