// Package cutoverreconcile decides one pass of catalyst-api's level-triggered
// cutover reconciler (#4635); internal/handler/cutover_reconciler.go wires the
// engine's operations in. A pass keeps the cutover engine running while the
// handover seal exists and the durable cutover-complete seal does not, and it
// repeats a FAILED step at most Policy.Limit times, repetition n after a pause
// of Pause×2^(n-1) (SOV-242). Then the record is marked halted: the cutover
// stays failed, the console shows the step, and only the sovereign-admin's POST
// /api/v1/sovereign/cutover/start (a retry call) goes on; that route writes
// Cleared(). The record lives in the status ConfigMap, so a Pod restart neither
// resets nor repeats the count.
package cutoverreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"
)

// Status ConfigMap keys of the repetition record.
const (
	StepKey   = "autoRetryStep"   // the failed step the record is about
	CountKey  = "autoRetryCount"  // repetitions fired
	LimitKey  = "autoRetryLimit"  // the most repetitions fired
	AtKey     = "autoRetryAt"     // the last repetition, or when the failure was first seen
	NextAtKey = "autoRetryNextAt" // when the next repetition is due
	HaltedKey = "autoRetryHalted" // "true": the budget is spent
)

// ErrNoChart: Ops.Read on a Sovereign without the cutover chart; the pass does nothing.
var ErrNoChart = errors.New("cutover chart not installed")

// Ops are the engine operations one pass uses.
type Ops struct {
	HandoverSealed func(context.Context) (bool, error)              // the Phase-0 archive is sealed
	CutoverSealed  func(context.Context) (bool, error)              // the durable cutover-complete seal exists
	Read           func(context.Context) (map[string]string, error) // the status ConfigMap
	Record         func(context.Context, map[string]string) error   // merge keys into it
	Fire           func(context.Context) (started bool, err error)  // ensure the engine runs
	Publish        func(snapshot string)                            // send the status ConfigMap as JSON to the console's event stream
}

// Policy bounds the repetitions of a failed step.
type Policy struct {
	Limit int
	Pause time.Duration // the pause before the first repetition
}

// DefaultPolicy: three repetitions, after 5, 10 and 20 minutes.
var DefaultPolicy = Policy{Limit: 3, Pause: 5 * time.Minute}

// PauseBefore is the pause before repetition n (1-based): Pause, 2×Pause, 4×Pause, ...
func (p Policy) PauseBefore(n int) time.Duration {
	return p.Pause << max(n-1, 0)
}

// Action is what a pass does.
type Action string

const (
	None Action = "none" // no failed step: ensure the engine runs
	Wait Action = "wait" // a failed step whose pause has not elapsed
	Fire Action = "fire" // repeat the failed step now
	Halt Action = "halt" // the budget is spent: stay failed
)

// Decision is the outcome of Decide.
type Decision struct {
	Action Action
	Step   string
	Patch  map[string]string // recorded before acting; nil records nothing
	Undo   map[string]string // restores the record when a Fire started nothing
}

// Cleared empties the record: the sovereign-admin's resume opens a fresh budget.
func Cleared() map[string]string {
	return map[string]string{StepKey: "", CountKey: "", LimitKey: "", AtKey: "", NextAtKey: "", HaltedKey: ""}
}

// Decide is the pure decision over the status ConfigMap.
func Decide(status map[string]string, now time.Time, p Policy) Decision {
	failed := status["failedStep"]
	if failed == "" {
		return Decision{Action: None}
	}
	now = now.UTC().Truncate(time.Second)
	stamp := func(t time.Time) string { return t.Format(time.RFC3339) }
	if status[StepKey] != failed {
		// First sight of this step's failure: open its record; the first pause counts from now.
		open := map[string]string{StepKey: failed, CountKey: "0", LimitKey: strconv.Itoa(p.Limit),
			AtKey: stamp(now), NextAtKey: stamp(now.Add(p.PauseBefore(1))), HaltedKey: ""}
		if p.Limit == 0 {
			open[HaltedKey], open[NextAtKey] = "true", ""
			return Decision{Action: Halt, Step: failed, Patch: open}
		}
		return Decision{Action: Wait, Step: failed, Patch: open}
	}
	if status[HaltedKey] == "true" {
		return Decision{Action: Halt, Step: failed}
	}
	count, err := strconv.Atoi(status[CountKey])
	ref, _ := time.Parse(time.RFC3339, status[AtKey])
	if t, ferr := time.Parse(time.RFC3339, status["step."+failed+".finishedAt"]); ferr == nil && t.After(ref) {
		ref = t // the pause counts from the later of the last repetition and the failure
	}
	if err != nil || count < 0 || count >= p.Limit || ref.IsZero() {
		// The budget is spent, or the record cannot be read: fail closed.
		return Decision{Action: Halt, Step: failed, Patch: map[string]string{HaltedKey: "true", NextAtKey: ""}}
	}
	if next := ref.Add(p.PauseBefore(count + 1)); now.Before(next) {
		d := Decision{Action: Wait, Step: failed}
		if status[NextAtKey] != stamp(next) {
			d.Patch = map[string]string{NextAtKey: stamp(next)}
		}
		return d
	}
	return Decision{Action: Fire, Step: failed,
		Patch: map[string]string{CountKey: strconv.Itoa(count + 1), LimitKey: strconv.Itoa(p.Limit), AtKey: stamp(now), NextAtKey: ""},
		Undo:  map[string]string{CountKey: status[CountKey], AtKey: status[AtKey], NextAtKey: status[NextAtKey]}}
}

// Pass runs one reconcile pass: fire iff the handover seal exists, the
// cutover-complete seal does not, and no failed step is waiting or halted.
// Any read error fails closed (logs and returns); the next tick tries again.
func Pass(ctx context.Context, ops Ops, now time.Time, p Policy, log *slog.Logger) {
	// Cause: the Sovereign received the Phase-0 archive (handover sealed).
	sealed, err := ops.HandoverSealed(ctx)
	if err != nil {
		log.Info("[CUTOVER-RECONCILE] handover-seal check failed; will retry next tick", "err", err.Error())
		return
	}
	if !sealed {
		return
	}
	// Effect: the durable, revert-immune cutover-complete fact (#3667) is sealed.
	complete, err := ops.CutoverSealed(ctx)
	if err != nil {
		log.Info("[CUTOVER-RECONCILE] cutover-complete check failed; will retry next tick", "err", err.Error())
		return
	}
	if complete {
		return
	}
	status, err := ops.Read(ctx)
	if err != nil {
		if !errors.Is(err, ErrNoChart) {
			log.Info("[CUTOVER-RECONCILE] status read failed; will retry next tick", "err", err.Error())
		}
		return
	}
	d := Decide(status, now, p)
	if d.Patch != nil {
		if err := ops.Record(ctx, d.Patch); err != nil {
			log.Error("[CUTOVER-RECONCILE] repetition record write failed; nothing fired", "step", d.Step, "err", err)
			return
		}
		if d.Action != Fire { // a counted repetition is not published: the run's seed clears failedStep, its failure publishes the record
			publish(ctx, ops)
		}
	}
	if d.Action == Halt && d.Patch != nil {
		log.Error("[CUTOVER-RECONCILE] failed step repeated up to the limit; the cutover stays failed until the sovereign-admin resumes it (POST /api/v1/sovereign/cutover/start)",
			"step", d.Step, "repetitions", status[CountKey])
	}
	if d.Action == Wait || d.Action == Halt {
		return
	}
	started, err := ops.Fire(ctx)
	if d.Action == Fire && !started {
		// Nothing ran: give the counted repetition back.
		if perr := ops.Record(ctx, d.Undo); perr != nil {
			log.Error("[CUTOVER-RECONCILE] repetition record restore failed; the repetition stays counted", "step", d.Step, "err", perr)
		} else {
			publish(ctx, ops)
		}
	}
	if err != nil {
		log.Error("[CUTOVER-RECONCILE] engine fire failed; will retry next tick", "err", err)
		return
	}
	// Already-running and already-complete are steady-state no-ops, not worth a line.
	if started {
		log.Info("[CUTOVER-RECONCILE] sealed-but-uncut → cutover engine (re)started", "repeatedStep", d.Step, "repetition", d.Patch[CountKey])
	}
}

// publish sends the status ConfigMap, as it is now, to the console's event stream.
func publish(ctx context.Context, ops Ops) {
	if fresh, err := ops.Read(ctx); err == nil {
		snap, _ := json.Marshal(fresh) // a map of strings always marshals
		ops.Publish(string(snap))
	}
}
