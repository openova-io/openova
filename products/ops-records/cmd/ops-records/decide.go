package main

import (
	"context"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/api"
	"github.com/openova-io/openova/products/ops-records/internal/correlate"
	"github.com/openova-io/openova/products/ops-records/internal/judge"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// decider decides the batches of pending signals: through the correlator judge when the policy
// has one (judge.CorrelationTicker; the dispatcher writes the rule result when the agent is
// invalid, unavailable or late), else by the rules alone (correlate.Engine).
//
// It reads the store only when it is woken or when the next batch is due: after each decision
// round it computes when the earliest pending batch that has no question in flight is due and
// waits for that time, for wake, or for the end of ctx. With no pending signal it waits for
// wake alone. Every change that can make a batch pending or free a busy one calls wake: an
// action of a person and every outcome of a judge. A batch that is still due after a round
// (its decision failed) is tried again after the urgent wait, never at once.
type decider struct {
	store   store.Store
	batches correlate.Policy
	tick    func(ctx context.Context) error
	busy    func(organization, subjectKind, subjectID string) bool // nil for the rules alone
	close   func()
	after   func(time.Duration) <-chan time.Time
	now     func() time.Time
	logf    func(format string, args ...any)
	woken   chan struct{}
}

// newDecider builds the decider of cfg's policy. Its changes go to hub.
func newDecider(s store.Store, cfg serveConfig, hub *api.Hub, logf func(string, ...any)) (*decider, error) {
	d := &decider{store: s, batches: cfg.policy.Batches(), close: func() {}, after: time.After, now: time.Now, logf: logf,
		woken: make(chan struct{}, 1)}
	if _, ok := cfg.policy.Judges[judge.Correlator]; !ok {
		engine := &correlate.Engine{Store: s, Policy: d.batches}
		d.tick = func(ctx context.Context) error {
			results, err := engine.Tick(ctx)
			for _, b := range results {
				for _, g := range b.Groups {
					if g.IncidentID != "" {
						hub.Publish(api.Change{Organization: b.Organization, IncidentID: g.IncidentID, Kind: "rules"})
					}
				}
			}
			return err
		}
		return d, nil
	}
	disp, err := judge.New(s, cfg.policy, &judge.Tokens{BaseURL: cfg.catalystAPI}, &judge.A2A{}, judge.Options{
		Decided: func(j store.Judgement) {
			hub.Publish(api.Change{Organization: j.Organization, JudgementID: j.ID, Kind: "judgement"})
			d.wake()
		}})
	if err != nil {
		return nil, err
	}
	ticker := &judge.CorrelationTicker{Store: s, Dispatcher: disp, Batches: d.batches}
	d.tick = func(ctx context.Context) error {
		_, err := ticker.Tick(ctx)
		return err
	}
	d.busy, d.close = disp.Busy, disp.Close
	return d, nil
}

// wake makes the decider look at the pending signals now. It never waits.
func (d *decider) wake() {
	select {
	case d.woken <- struct{}{}:
	default:
	}
}

// run decides until ctx ends.
func (d *decider) run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := d.tick(ctx); err != nil && ctx.Err() == nil {
			d.logf("decide: %v", err)
		}
		var due <-chan time.Time
		next, found, err := d.next(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			d.logf("decide: the next batch: %v", err)
			due = d.after(d.batches.UrgentBatch)
		case found:
			wait := next.Sub(d.now())
			if wait <= 0 { // still due after the round: its decision failed
				wait = d.batches.UrgentBatch
			}
			due = d.after(wait)
		}
		select {
		case <-ctx.Done():
		case <-d.woken:
		case <-due:
		}
	}
}

// next returns when the earliest pending batch without a question in flight is due, and
// false when there is none. A batch's last decision is the answer time of its newest
// judgement, else its ask time, as judge.CorrelationTicker counts it.
func (d *decider) next(ctx context.Context) (time.Time, bool, error) {
	batches, _, err := correlate.Load(ctx, d.store, "")
	if err != nil {
		return time.Time{}, false, err
	}
	var first time.Time
	found := false
	for _, b := range batches {
		var last time.Time
		if d.busy != nil {
			subject := judge.BatchSubject(b)
			if d.busy(b.Organization, judge.SubjectBatch, subject) {
				continue
			}
			rows, err := d.store.ListJudgements(ctx, store.JudgementFilter{Organization: b.Organization,
				Judge: judge.Correlator, SubjectKind: judge.SubjectBatch, SubjectID: subject, Limit: 1})
			if err != nil {
				return time.Time{}, false, err
			}
			if len(rows) == 1 {
				last = rows[0].AskedAt
				if rows[0].AnsweredAt != nil {
					last = *rows[0].AnsweredAt
				}
			}
		}
		if due := correlate.DueAt(b, last, d.batches); !found || due.Before(first) {
			first, found = due, true
		}
	}
	return first, found, nil
}
