package cutoverreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeOps keeps the status ConfigMap in a map; every engine run that starts
// fails the step again, as egress-block-test did on 19 November 2026.
func fakeOps(status map[string]string, now *time.Time, fires *[]time.Time, started bool) Ops {
	return Ops{
		HandoverSealed: func(context.Context) (bool, error) { return true, nil },
		CutoverSealed:  func(context.Context) (bool, error) { return false, nil },
		Read: func(context.Context) (map[string]string, error) {
			out := map[string]string{}
			for k, v := range status {
				out[k] = v
			}
			return out, nil
		},
		Record: func(_ context.Context, m map[string]string) error {
			for k, v := range m {
				status[k] = v
			}
			return nil
		},
		Fire: func(context.Context) (bool, error) {
			*fires = append(*fires, *now)
			if started && status["failedStep"] != "" {
				status["step."+status["failedStep"]+".finishedAt"] = now.Format(time.RFC3339)
			}
			return started, nil
		},
		Publish: func(string) {},
	}
}

// TestPassBoundsRepetitionsOfFailedStep replays 19 November: the step fails on
// every run. Ticking every 2 minutes for 6 hours, the pass repeats it exactly
// 3 times, each pause longer than the one before and at least 5, 10 and 20
// minutes, then halts with a record that names the step. Without the bound
// the same loop fires on every tick (180 times).
func TestPassBoundsRepetitionsOfFailedStep(t *testing.T) {
	start := time.Date(2026, 11, 19, 20, 11, 0, 0, time.UTC)
	now, fires := start, []time.Time{}
	status := map[string]string{"failedStep": "egress-block-test", "step.egress-block-test.finishedAt": start.Format(time.RFC3339)}
	ops := fakeOps(status, &now, &fires, true)
	var last map[string]string // the console's view: a frame never counts a repetition that has not run
	ops.Publish = func(snap string) {
		last = map[string]string{}
		if err := json.Unmarshal([]byte(snap), &last); err != nil || last[CountKey] != strconv.Itoa(len(fires)) {
			t.Errorf("published %s after %d repetitions, want the repetitions run", snap, len(fires))
		}
	}
	for ; now.Before(start.Add(6 * time.Hour)); now = now.Add(2 * time.Minute) {
		Pass(context.Background(), ops, now, DefaultPolicy, quiet)
	}
	if len(fires) != DefaultPolicy.Limit {
		t.Fatalf("fired %d repetitions in 6h, want exactly %d", len(fires), DefaultPolicy.Limit)
	}
	// The pauses of the policy, written out: each repetition fires on the first 2-minute tick after its pause.
	prevAt, pauses := start, []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute}
	for i, at := range fires {
		if gap := at.Sub(prevAt); gap < pauses[i] || gap >= pauses[i]+2*time.Minute {
			t.Errorf("repetition %d came %s after the failure: want %s, up to one 2-minute tick later", i+1, gap, pauses[i])
		}
		prevAt = at
	}
	if status[HaltedKey] != "true" || status[StepKey] != "egress-block-test" || status[CountKey] != "3" || status[LimitKey] != "3" {
		t.Fatalf("record = %v, want step egress-block-test, count 3, limit 3, halted", status)
	}
	if last[HaltedKey] != "true" {
		t.Fatalf("last published frame = %v, want the halted record", last)
	}
}

// TestPassGivesBackARepetitionThatStartedNothing: a fire that started no
// engine run does not spend the budget.
func TestPassGivesBackARepetitionThatStartedNothing(t *testing.T) {
	now, fires := time.Date(2026, 11, 19, 21, 0, 0, 0, time.UTC), []time.Time{}
	status := map[string]string{"failedStep": "egress-block-test", StepKey: "egress-block-test", CountKey: "1", AtKey: "2026-11-19T20:11:00Z"}
	ops, frames := fakeOps(status, &now, &fires, false), []string{}
	ops.Publish = func(snap string) { frames = append(frames, snap) }
	Pass(context.Background(), ops, now, DefaultPolicy, quiet)
	if len(fires) != 1 || status[CountKey] != "1" || len(frames) != 1 || !strings.Contains(frames[0], `"autoRetryCount":"1"`) {
		t.Fatalf("fires=%d count=%q frames=%q, want 1 fire and the count given back to 1, published once", len(fires), status[CountKey], frames)
	}
}

// TestPassCauseAndEffect: no fire before handover, after the cutover-complete
// seal, on a read error or without the cutover chart.
func TestPassCauseAndEffect(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		sealed, complete bool
		sealErr, readErr error
		want             int
	}{
		"pre-handover": {}, "seal read error": {sealed: true, sealErr: boom}, "already complete": {sealed: true, complete: true},
		"no cutover chart": {sealed: true, readErr: ErrNoChart}, "status read error": {sealed: true, readErr: boom},
		"sealed-but-uncut, no failed step": {sealed: true, want: 1},
	} {
		now, fires := time.Now(), []time.Time{}
		ops := fakeOps(map[string]string{}, &now, &fires, true)
		ops.HandoverSealed = func(context.Context) (bool, error) { return tc.sealed, tc.sealErr }
		ops.CutoverSealed = func(context.Context) (bool, error) { return tc.complete, nil }
		if tc.readErr != nil {
			ops.Read = func(context.Context) (map[string]string, error) { return nil, tc.readErr }
		}
		if Pass(context.Background(), ops, now, DefaultPolicy, quiet); len(fires) != tc.want {
			t.Errorf("%s: fires=%d, want %d", name, len(fires), tc.want)
		}
	}
}

// TestDecide covers each branch of the decision.
func TestDecide(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)
	rec := func(count string, kv ...string) map[string]string {
		m := map[string]string{"failedStep": "egress-block-test", StepKey: "egress-block-test", AtKey: "2026-11-19T20:00:00Z", CountKey: count}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	later := "step.egress-block-test.finishedAt"
	for _, tc := range []struct {
		name   string
		status map[string]string
		now    time.Duration
		limit  int
		want   Action
		count  string // the count the patch records; "" = none
	}{
		{"no failed step", map[string]string{}, 0, 3, None, ""},
		{"first sight opens the record", map[string]string{"failedStep": "egress-block-test"}, 0, 3, Wait, "0"},
		{"another step failed: a new record", rec("2", "failedStep", "harbor-prewarm"), time.Hour, 3, Wait, "0"},
		{"first pause not elapsed", rec("0"), 4 * time.Minute, 3, Wait, ""},
		{"first pause elapsed", rec("0"), 5 * time.Minute, 3, Fire, "1"},
		{"second pause counts from the later failure", rec("1", later, "2026-11-19T20:30:00Z"), 39 * time.Minute, 3, Wait, ""},
		{"second pause elapsed", rec("1", later, "2026-11-19T20:30:00Z"), 40 * time.Minute, 3, Fire, "2"},
		{"budget spent", rec("3"), 24 * time.Hour, 3, Halt, ""},
		{"already halted", rec("3", HaltedKey, "true"), 24 * time.Hour, 3, Halt, ""},
		{"unreadable count", rec("x"), 24 * time.Hour, 3, Halt, ""},
		{"no time to count from", rec("1", AtKey, ""), 24 * time.Hour, 3, Halt, ""},
		{"limit 0", map[string]string{"failedStep": "egress-block-test"}, 0, 0, Halt, "0"},
	} {
		d := Decide(tc.status, t0.Add(tc.now), Policy{Limit: tc.limit, Pause: 5 * time.Minute})
		halted := tc.status[HaltedKey] == "true" || d.Patch[HaltedKey] == "true"
		if d.Action != tc.want || d.Patch[CountKey] != tc.count || (tc.want == Halt) != halted {
			t.Errorf("%s: action %s, count %q, halted %v; want %s, %q, %v", tc.name, d.Action, d.Patch[CountKey], halted, tc.want, tc.count, tc.want == Halt)
		}
	}
}
