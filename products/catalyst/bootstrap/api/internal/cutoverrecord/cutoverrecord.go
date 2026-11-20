// Package cutoverrecord is the cutover's pre-step secondary-kubeconfigs and
// the deployment record it needs: why the secondary kubeconfigs fell short, by
// the state of that record, and the bounded wait for a record that has not
// arrived yet. internal/handler wires it in (runCutover, the import route).
//
// Measured 20 Nov 2026 on a fresh two-region install: the hand-over seal fired
// the cutover at 15:21:53 (ReceiveTofuArchive, handler/handover.go); the
// parent's POST /api/v1/internal/deployments/import stored the record at
// 15:22:09. The pre-step found no record, failed, and the first automatic
// repetition of the reconciler (internal/cutoverreconcile) passed five minutes
// later. That happens on every fresh install: the parent starts both exports as
// independent goroutines at the same instant (fireHandover,
// handler/phase1_watch.go), each under the shared retry policy of
// handler/handover_export_retry.go, so nothing orders their arrival. The seal
// handler cannot know that the import will follow, and the import can as well
// land first, so the order cannot be fixed at the seal. A record that has not
// arrived is therefore a wait (Wait), and every other shortfall stays the
// failed step it was, which the reconciler repeats.
package cutoverrecord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ErrNotArrived marks the one shortfall Wait waits on: no deployment record for
// this Sovereign has been stored yet.
var ErrNotArrived = errors.New("cutover: no deployment record for this Sovereign has arrived — the parent's POST /api/v1/internal/deployments/import has not landed")

// Shortfall is what the pre-step knows when fewer usable secondary kubeconfigs
// were found than the spec expects and no already-materialized Secret satisfies
// the count.
type Shortfall struct {
	Arrived   bool     // a deployment record for this Sovereign is stored
	DepID     string   // its trimmed id; "" is the degraded record of #5488
	Expected  int      // secondary regions the spec expects
	Usable    int      // usable secondary kubeconfigs found
	Paths     int      // candidate kubeconfig paths resolved
	Dir       string   // the kubeconfigs directory
	SecretRef string   // <namespace>/<name> of the cutover-secondary-kubeconfigs Secret
	Ambiguous []string // deployment prefixes on disk when the id is empty
	Missing   []string // sorted "<regionKey> (<path>: <why>)" of the rejected files
	Refusal   string   // why an existing Secret was refused (#6107); "" when none
}

// Err names the shortfall. A record that has not arrived wraps ErrNotArrived; a
// record that HAS arrived keeps the #5488 and #5359 failures, word for word.
func (s Shortfall) Err() error {
	// abort carries the #6107 refusal into the ERROR, not only into a log
	// line. Every message below ends with "no already-materialized Secret
	// satisfies the expected count", which is true and useless when the
	// Secret is right there: an operator reads it, finds the object, sees a
	// parseable credential with a token in it, and has been told nothing
	// about why the platform will not use it.
	abort := func(format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		if s.Refusal != "" {
			msg += " — " + s.Refusal
		}
		return errors.New(msg)
	}
	switch {
	case !s.Arrived:
		// NOT ARRIVED is not DEGRADED: the record that would name the id has
		// not been stored at all.
		detail := "no <id>-<regionKey>.yaml kubeconfig path in " + s.Dir + " can be derived without it"
		switch {
		case len(s.Ambiguous) > 0:
			detail = fmt.Sprintf("%d deployment prefixes exist on disk in %s (%s) and only the record can name this Sovereign's",
				len(s.Ambiguous), s.Dir, strings.Join(s.Ambiguous, ", "))
		case len(s.Missing) > 0:
			detail = "the kubeconfig(s) found without it are not usable: " + strings.Join(s.Missing, ", ")
		}
		err := abort("this Sovereign expects %d secondary region(s), no already-materialized %s Secret satisfies them, and %s — refusing to start a cutover whose region-B pivot legs would silently no-op (#5359)",
			s.Expected, s.SecretRef, detail)
		return fmt.Errorf("%w: %v", ErrNotArrived, err)
	case s.DepID == "" && len(s.Ambiguous) > 0:
		// #5488 task 3 — ambiguous on-disk fallback: never silently
		// pick one of several deployment prefixes.
		return abort(
			"cutover: the resolved deployment record carries an EMPTY deployment id (degraded record after a catalyst-api restart, #5488) and %d distinct deployment prefixes exist on disk in %s (%s) — refusing to guess which one is this Sovereign's; expected %d secondary region(s) and no already-materialized %s Secret satisfies them. Recovery: re-import the deployment record (POST /api/v1/internal/deployments/import from the mothership) so the record can name its own id, then re-fire the cutover (#5359 fail-loud contract preserved)",
			len(s.Ambiguous), s.Dir, strings.Join(s.Ambiguous, ", "), s.Expected, s.SecretRef)
	case s.DepID == "":
		// #5488 task 1 — the empty-ID record is its own diagnosable
		// condition, not "0 readable".
		return abort(
			"cutover: the resolved deployment record carries an EMPTY deployment id (degraded record after a catalyst-api restart, #5488) while its spec expects %d secondary region(s) — the on-disk <depID>-<regionKey>.yaml kubeconfig paths in %s cannot be derived from a blank id, and no already-materialized %s Secret satisfies the expected count. Recovery: re-import the deployment record (POST /api/v1/internal/deployments/import from the mothership) or re-POST the secondary kubeconfig(s) via /sovereign/secondary-kubeconfig, then re-fire the cutover (#5359 fail-loud contract preserved)",
			s.Expected, s.Dir, s.SecretRef)
	case s.Paths == 0:
		// #5488 task 2 — an empty candidate map is "we never looked",
		// not "the files were unreadable". Say so instead of printing
		// an empty missing list.
		return abort(
			"cutover: deployment %s expects %d secondary region(s) but no candidate kubeconfig paths resolved (the in-memory secondary-kubeconfig map is empty — typical after a catalyst-api restart — and no %s-<regionKey>.yaml files were found in %s), and no already-materialized %s Secret satisfies the expected count — refusing to start a cutover whose region-B pivot legs would silently no-op (#5359)",
			s.DepID, s.Expected, s.DepID, s.Dir, s.SecretRef)
	default:
		// Original #5359 contract: candidate paths existed but the
		// files are genuinely missing/unreadable/unusable — name each.
		return abort(
			"cutover: deployment %s expects %d secondary region(s) but only %d kubeconfig(s) are USABLE (rejected: %s) — refusing to start a cutover whose region-B pivot legs would silently no-op (#5359)",
			s.DepID, s.Expected, s.Usable, strings.Join(s.Missing, ", "))
	}
}

// Deadline is the ONE statement of how long the pre-step waits for the record:
// the parent's own retry budget for the import (importBudget, which is
// handler.handoverExportBudget: CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS, default
// 20m), counted from the cutover's FIRST start (the status ConfigMap's
// cutoverStartedAt; now when it is empty or unreadable, i.e. on the first run).
// The parent starts the import no later than the seal that fires that first
// start and gives it up once the budget is spent, so past this instant the
// record will not come. A later attempt (an automatic repetition, the
// sovereign-admin's resume) is past it and fails at once, as before the wait.
func Deadline(cutoverStartedAt string, now time.Time, importBudget time.Duration) time.Time {
	first := now
	if t, err := time.Parse(time.RFC3339, cutoverStartedAt); err == nil {
		first = t
	}
	return first.Add(importBudget)
}

// Signal is the import route's coalescing "a record was stored" signal. The
// zero value is ready to use.
type Signal struct {
	once sync.Once
	ch   chan struct{}
}

func (s *Signal) c() chan struct{} {
	s.once.Do(func() { s.ch = make(chan struct{}, 1) })
	return s.ch
}

// Announce raises the signal without blocking. With no waiter the buffered slot
// stays full, which costs a later waiter one extra check.
func (s *Signal) Announce() {
	select {
	case s.c() <- struct{}{}:
	default:
	}
}

// C is the channel a waiter receives the signal on.
func (s *Signal) C() <-chan struct{} { return s.c() }

// Wait is the pre-step's bounded wait for the deployment record.
type Wait struct {
	StartedAt    string           // the status ConfigMap's cutoverStartedAt ("" on a first run)
	ImportBudget time.Duration    // the parent's retry budget for the import
	Arrived      <-chan struct{}  // Signal.C of the import route
	Log          *slog.Logger     // the wait's start is logged here
	Waiting      func(msg string) // called once, when the wait begins
}

// Run calls try, and while try reports only ErrNotArrived it waits for the
// import route's signal and calls it again. Any other result of try returns at
// once. At the Deadline it fails naming the import; it never waits past it, and
// it returns when ctx ends.
func (w Wait) Run(ctx context.Context, try func() (int, error)) (int, error) {
	deadline := Deadline(w.StartedAt, time.Now(), w.ImportBudget)
	waiting := false
	for {
		n, err := try()
		if !errors.Is(err, ErrNotArrived) {
			return n, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("%w; waited until %s, the parent's retry budget for that import (%s, CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS) counted from the cutover's first start, so it will not arrive any more. Recovery: have the parent send the record again (POST /api/v1/internal/deployments/import), then resume the cutover (POST /api/v1/sovereign/cutover/start)",
				err, deadline.UTC().Format(time.RFC3339), w.ImportBudget)
		}
		if !waiting {
			waiting = true
			msg := fmt.Sprintf("waiting for the deployment record until %s before materializing the secondary-region kubeconfigs: %v", deadline.UTC().Format(time.RFC3339), err)
			if w.Log != nil {
				w.Log.Info("cutover: " + msg)
			}
			if w.Waiting != nil {
				w.Waiting(msg)
			}
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, fmt.Errorf("cutover: stopped while waiting for the deployment record (POST /api/v1/internal/deployments/import): %w", ctx.Err())
		case <-w.Arrived:
		case <-timer.C:
		}
		timer.Stop()
	}
}
