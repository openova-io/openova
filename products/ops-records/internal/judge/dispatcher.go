// Package judge asks agents bounded questions and writes their answers: the bounded-judge
// contract of the operations-records service.
//
// A judge is an agent called with one prepared JSON document; it must answer with one JSON
// document that matches a schema. For every question the dispatcher holds the offered
// document, the rule result (what the rules decide without any agent), a validation function
// and a deadline. The rules:
//
//   - Record. Asking writes one row of ops.judgements: the offered document, the rule result,
//     the time asked and the deadline (the judge's deadline, or its urgent deadline for an
//     urgent question). Its outcome is undecided until one of the outcomes below is written,
//     always in the same transaction as the records that outcome changes.
//   - One at a time. A subject (Organization, subject kind, subject identifier) has at most one
//     question in flight; asking again returns ErrBusy.
//   - Lanes. Every judge runs in a lane, and a lane runs at most its number of turns at once.
//     A question waits for a free turn of its lane until its deadline, and no longer.
//   - Turn. A turn takes one turn of the budget of its Organization and judge (agentguard:
//     a refused or uncountable budget means no call), asks catalyst-api for an agent token of
//     the judge's agent, tier and life (the request carries the pod's ServiceAccount token),
//     and calls the agent over A2A with the offered document as the text and the token as
//     bearer. A turn lives at most the token's life from the time it was asked. Its end, by
//     answer, error, cancel or end of life, frees its turn of the lane.
//   - Text. Every string of the offered document passes agentguard.CleanValue, with no link
//     host allowed, before the agent reads it. The row records the document as it was sent.
//   - Outcomes. The rule result is written first unless a valid answer comes before the
//     deadline:
//     applied: a valid answer came before the deadline and was written instead.
//     rejected_invalid: an invalid answer came before the deadline; the rule result was written
//     at once.
//     unavailable: the turn ended without an answer before the deadline (budget, token, call,
//     cancel); the rule result was written at once. Recover also writes it, without the rule
//     result, for a question a stopped dispatcher left undecided.
//     timeout: the deadline came first; the rule result was written at the deadline. The turn
//     goes on.
//     late_applied: after timeout a valid answer came within the turn's life and revised what
//     the rules wrote.
//     late_rejected: after timeout an invalid answer came.
//     A turn that ends without an answer after its deadline leaves the outcome timeout.
//   - Answers. The row records the answer as the agent gave it, with the time it came, for
//     every outcome that has one. Validation decides; an answer that cannot be written is
//     treated as invalid.
//   - Decided. Options.Decided is called with the row after every outcome is written: the
//     deadline's outcome is reported on time.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/agentguard"
	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// Errors of Ask.
var (
	ErrBusy     = errors.New("judge: the subject has a question in flight")
	ErrClosed   = errors.New("judge: the dispatcher is closed")
	ErrQuestion = errors.New("judge: bad question")
)

// writeWait bounds every write of an outcome. Writes do not end with the turn.
const writeWait = 30 * time.Second

// Question is one question for a judge.
type Question struct {
	Judge        string // a key of Policy.Judges
	Organization string // an Organization slug
	SubjectKind  string // for example "signal_batch"
	SubjectID    string
	Urgent       bool // the judge's urgent deadline applies, when it has one
	Offered      any  // the document offered to the judge, as JSON
	RuleResult   any  // the result of the rule function, as JSON

	// Validate checks an answer against what was offered and returns it in the form that
	// Apply and Revise take, or an error.
	Validate func(answer json.RawMessage) (any, error)
	// Rule writes the rule result and returns what it wrote, for Revise.
	Rule func(ctx context.Context, tx store.Tx, now time.Time) (any, error)
	// Apply writes a valid answer that came before the deadline, instead of the rule result.
	Apply func(ctx context.Context, tx store.Tx, answer any, now time.Time) error
	// Revise writes a valid answer that came after the rule result was written.
	Revise func(ctx context.Context, tx store.Tx, written, answer any, now time.Time) error
}

// Options are the parts of a Dispatcher that have defaults.
type Options struct {
	Now     func() time.Time                     // nil means time.Now
	After   func(time.Duration) <-chan time.Time // the deadline timer; nil means time.After
	Counter agentguard.Counter                   // the turn counter; nil means a new MemoryCounter
	NewID   func() string                        // judgement identifiers; nil means ids.New
	Decided func(store.Judgement)                // called after each outcome is written; may be nil
	Log     *slog.Logger                         // nil means slog.Default()
}

// Dispatcher asks judges and writes their outcomes. Its methods may be called from several
// goroutines. One dispatcher serves one database.
type Dispatcher struct {
	store  store.Store
	policy Policy
	tokens TokenSource
	agent  Agent
	budget *agentguard.Budget
	opt    Options

	ctx    context.Context
	cancel context.CancelFunc
	lanes  map[string]chan struct{}

	mu     sync.Mutex
	closed bool
	turns  map[string]*turn  // by judgement ID
	busy   map[string]string // subject key -> judgement ID
	wg     sync.WaitGroup
}

type turn struct {
	q      Question
	judge  Judge
	row    store.Judgement
	key    string
	ctx    context.Context
	cancel context.CancelFunc
}

// result is what a call gave: an answer, or an error.
type result struct {
	answer json.RawMessage
	err    error
	at     time.Time
}

// New returns a dispatcher. The policy must be valid.
func New(s store.Store, p Policy, tokens TokenSource, agent Agent, o Options) (*Dispatcher, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if s == nil || tokens == nil || agent == nil {
		return nil, errors.New("judge: New needs a store, a token source and an agent")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.Counter == nil {
		o.Counter = &agentguard.MemoryCounter{}
	}
	if o.NewID == nil {
		o.NewID = ids.New
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	d := &Dispatcher{store: s, policy: p, tokens: tokens, agent: agent, opt: o,
		budget: &agentguard.Budget{Turns: p.TurnBudget.Turns, Window: time.Duration(p.TurnBudget.WindowSeconds) * time.Second,
			Counter: o.Counter, Now: o.Now},
		lanes: map[string]chan struct{}{}, turns: map[string]*turn{}, busy: map[string]string{}}
	for lane, n := range p.Lanes {
		d.lanes[lane] = make(chan struct{}, n)
	}
	d.ctx, d.cancel = context.WithCancel(context.Background())
	return d, nil
}

// Ask writes the judgement row of q, starts its turn and returns the judgement's identifier.
// It does not wait for the turn.
func (d *Dispatcher) Ask(ctx context.Context, q Question) (string, error) {
	j, ok := d.policy.Judges[q.Judge]
	switch {
	case !ok:
		return "", fmt.Errorf("%w: judge %q is not in the policy", ErrQuestion, q.Judge)
	case !organizationPattern.MatchString(q.Organization):
		return "", fmt.Errorf("%w: Organization %q", ErrQuestion, q.Organization)
	case q.SubjectKind == "" || q.SubjectID == "":
		return "", fmt.Errorf("%w: the subject is empty", ErrQuestion)
	case q.Validate == nil || q.Rule == nil || q.Apply == nil || q.Revise == nil:
		return "", fmt.Errorf("%w: Validate, Rule, Apply and Revise are required", ErrQuestion)
	}
	offered, err := cleaned(q.Offered)
	if err != nil {
		return "", fmt.Errorf("%w: offered: %v", ErrQuestion, err)
	}
	rule, err := json.Marshal(q.RuleResult)
	if err != nil {
		return "", fmt.Errorf("%w: rule result: %v", ErrQuestion, err)
	}
	wait := time.Duration(j.DeadlineSeconds) * time.Second
	if q.Urgent && j.UrgentDeadlineSeconds > 0 {
		wait = time.Duration(j.UrgentDeadlineSeconds) * time.Second
	}
	key := q.Organization + "\x00" + q.SubjectKind + "\x00" + q.SubjectID
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return "", ErrClosed
	}
	if _, taken := d.busy[key]; taken {
		d.mu.Unlock()
		return "", ErrBusy
	}
	d.busy[key] = ""
	d.wg.Add(1)
	d.mu.Unlock()

	now := d.opt.Now()
	row := store.Judgement{ID: d.opt.NewID(), Organization: q.Organization, Judge: q.Judge, SubjectKind: q.SubjectKind,
		SubjectID: q.SubjectID, Offered: offered, RuleResult: rule, Outcome: store.OutcomeUndecided,
		AskedAt: now, DeadlineAt: now.Add(wait)}
	if err := d.store.InTx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.InsertJudgement(ctx, row) }); err != nil {
		d.mu.Lock()
		delete(d.busy, key)
		d.mu.Unlock()
		d.wg.Done()
		return "", err
	}
	t := &turn{q: q, judge: j, row: row, key: key}
	t.ctx, t.cancel = context.WithTimeout(d.ctx, time.Duration(j.TokenTTLSeconds)*time.Second)
	d.mu.Lock()
	d.busy[key] = row.ID
	d.turns[row.ID] = t
	d.mu.Unlock()
	go d.run(t, wait)
	return row.ID, nil
}

// Busy reports whether the subject has a question in flight.
func (d *Dispatcher) Busy(organization, subjectKind, subjectID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.busy[organization+"\x00"+subjectKind+"\x00"+subjectID]
	return ok
}

// Cancel ends the turn of a judgement. It returns false when the judgement has no turn.
func (d *Dispatcher) Cancel(judgementID string) bool {
	d.mu.Lock()
	t := d.turns[judgementID]
	d.mu.Unlock()
	if t == nil {
		return false
	}
	t.cancel()
	return true
}

// Close refuses new questions, ends every turn and waits until each has written its outcome.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.cancel()
	d.wg.Wait()
}

// Recover writes the outcome unavailable on every judgement that a stopped dispatcher left
// undecided, without its rule result: the turn is lost, and the subject is asked again under a
// new judgement. Run it once, before the first Ask. It writes page after page of at most
// store.MaxLimit rows, each in its own transaction, until a page comes back empty, and returns
// how many rows it wrote.
func Recover(ctx context.Context, s store.Store) (int, error) {
	total := 0
	for {
		n := 0
		err := s.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
			rows, err := tx.ListJudgements(ctx, store.JudgementFilter{Undecided: true, Limit: store.MaxLimit})
			if err != nil {
				return err
			}
			for _, j := range rows {
				j.Outcome = store.OutcomeUnavailable
				if err := tx.UpdateJudgement(ctx, j); err != nil {
					return err
				}
			}
			n = len(rows)
			return nil
		})
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		total += n
	}
}

func (d *Dispatcher) run(t *turn, wait time.Duration) {
	defer d.end(t)
	deadline := d.opt.After(wait)
	lane := d.lanes[t.judge.Lane]
	select {
	case lane <- struct{}{}:
	case <-deadline:
		d.writeRule(t, store.OutcomeTimeout, nil)
		return
	case <-t.ctx.Done():
		d.writeRule(t, store.OutcomeUnavailable, nil)
		return
	}
	defer func() { <-lane }()
	results := make(chan result, 1)
	offered := string(t.row.Offered) // the call never reads t.row: the deadline may rewrite it
	go func() { results <- d.call(t, offered) }()
	select {
	case r := <-results:
		d.early(t, r)
		return
	case <-deadline:
	}
	written, ok := d.writeRule(t, store.OutcomeTimeout, nil)
	r := <-results
	if ok {
		d.late(t, written, r)
	}
}

func (d *Dispatcher) end(t *turn) {
	t.cancel()
	d.mu.Lock()
	delete(d.busy, t.key)
	delete(d.turns, t.row.ID)
	d.mu.Unlock()
	d.wg.Done()
}

// call takes a turn of the budget, gets a token and asks the agent with the offered text. It
// runs beside the turn's goroutine and reads only the parts of t that never change.
func (d *Dispatcher) call(t *turn, offered string) result {
	fail := func(err error) result { return result{err: err, at: d.opt.Now()} }
	if err := d.budget.Take(t.ctx, t.q.Organization+"/"+t.q.Judge); err != nil {
		return fail(err)
	}
	token, err := d.tokens.Token(t.ctx, TokenRequest{Organization: t.q.Organization, Agent: t.judge.Agent,
		Tier: t.judge.TokenTier, TTLSeconds: t.judge.TokenTTLSeconds})
	if err != nil {
		return fail(err)
	}
	answer, err := d.agent.Ask(t.ctx, d.policy.AgentRuntime.URL(t.q.Organization, t.judge.Agent), token, offered)
	if err != nil {
		return fail(err)
	}
	if !json.Valid(answer) {
		return fail(fmt.Errorf("%w: the answer is not JSON", ErrNoAnswer))
	}
	return result{answer: answer, at: d.opt.Now()}
}

// early writes the outcome of a turn that ended before the deadline.
func (d *Dispatcher) early(t *turn, r result) {
	if r.err != nil {
		d.opt.Log.Info("judge: no answer before the deadline", "judgement", t.row.ID, "judge", t.q.Judge, "err", r.err)
		d.writeRule(t, store.OutcomeUnavailable, nil)
		return
	}
	answer, err := t.q.Validate(r.answer)
	if err == nil {
		err = d.write(t, store.OutcomeApplied, &r, func(ctx context.Context, tx store.Tx, now time.Time) error {
			return t.q.Apply(ctx, tx, answer, now)
		})
		if err == nil {
			return
		}
	}
	d.opt.Log.Info("judge: answer rejected", "judgement", t.row.ID, "judge", t.q.Judge, "err", err)
	d.writeRule(t, store.OutcomeRejectedInvalid, &r)
}

// late writes the outcome of an answer that came after the rule result was written.
func (d *Dispatcher) late(t *turn, written any, r result) {
	if r.err != nil {
		d.opt.Log.Info("judge: no answer after the deadline", "judgement", t.row.ID, "judge", t.q.Judge, "err", r.err)
		return
	}
	answer, err := t.q.Validate(r.answer)
	if err == nil {
		err = d.write(t, store.OutcomeLateApplied, &r, func(ctx context.Context, tx store.Tx, now time.Time) error {
			return t.q.Revise(ctx, tx, written, answer, now)
		})
		if err == nil {
			return
		}
	}
	d.opt.Log.Info("judge: late answer rejected", "judgement", t.row.ID, "judge", t.q.Judge, "err", err)
	_ = d.write(t, store.OutcomeLateRejected, &r, nil)
}

// writeRule writes the rule result with an outcome and returns what the rules wrote.
func (d *Dispatcher) writeRule(t *turn, outcome string, r *result) (any, bool) {
	var written any
	err := d.write(t, outcome, r, func(ctx context.Context, tx store.Tx, now time.Time) error {
		w, err := t.q.Rule(ctx, tx, now)
		written = w
		return err
	})
	return written, err == nil
}

// write runs fn and records the outcome, with the answer of r when r has one, in one
// transaction, then reports the row.
func (d *Dispatcher) write(t *turn, outcome string, r *result, fn func(ctx context.Context, tx store.Tx, now time.Time) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(d.ctx), writeWait)
	defer cancel()
	row := t.row
	row.Outcome = outcome
	if r != nil && r.answer != nil {
		at := r.at
		if at.Before(row.AskedAt) {
			at = row.AskedAt
		}
		row.Answer, row.AnsweredAt = r.answer, &at
	}
	now := d.opt.Now()
	err := d.store.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if fn != nil {
			if err := fn(ctx, tx, now); err != nil {
				return err
			}
		}
		return tx.UpdateJudgement(ctx, row)
	})
	if err != nil {
		d.opt.Log.Error("judge: the outcome cannot be written", "judgement", t.row.ID, "outcome", outcome, "err", err)
		return err
	}
	t.row = row // only the goroutine of run reads and writes t.row; call gets the offered text
	if d.opt.Decided != nil {
		d.opt.Decided(row)
	}
	return nil
}

// cleaned returns the offered document with every string passed through agentguard.
func cleaned(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(agentguard.CleanValue(generic, nil))
}
