package switchover

import (
	"context"
	"errors"
	"testing"

	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
)

// hw307 2026-11-07: step-7 audit-emit failed AFTER step-6 promoted region
// B; the unwind re-pointed region A as primary and forked the timeline.
// A failure after promotion must not unwind anything.
func TestPostPromotionFailure_DoesNotUnwind(t *testing.T) {
	t.Parallel()
	seq, w, _, _, _ := makeStatelessSequencer(t)
	seq.Audit = nil // step-7 fails: "Audit publisher is required"

	res := seq.Execute(context.Background(), statelessPlan("cnpg-pair"))
	if res.Err != nil {
		t.Fatalf("a failure after promotion must not fail the switchover: %v", res.Err)
	}
	if len(res.RolledBack) != 0 {
		t.Fatalf("rolled back steps %v after promotion — that forks the timeline", res.RolledBack)
	}
	if len(res.PostCommitErrs) != 1 {
		t.Fatalf("want the audit failure surfaced as one post-commit error, got %v", res.PostCommitErrs)
	}
	st, _ := w.Read(context.Background())
	if st.Holder != "hel" {
		t.Fatalf("lease holder = %q want hel — the swap must stand", st.Holder)
	}
}

// Before promotion, a failure still unwinds (no regression): step-4
// flip-dns fails, so steps 1-3 roll back and the lease never moves.
func TestPrePromotionFailure_StillUnwinds(t *testing.T) {
	t.Parallel()
	seq, w, _, _, _ := makeStatelessSequencer(t)
	seq.PDMCommit = func(context.Context, []dns.Record) error { return errors.New("pdm down") }
	res := seq.Execute(context.Background(), statelessPlan("sso-bridge"))
	if res.Err == nil || res.FailedAtStep != 4 {
		t.Fatalf("want failure at step 4, got step %d err %v", res.FailedAtStep, res.Err)
	}
	if len(res.PostCommitErrs) != 0 {
		t.Fatalf("no promotion happened, so nothing is post-commit: %v", res.PostCommitErrs)
	}
	st, _ := w.Read(context.Background())
	if st.Holder == "hel" {
		t.Fatal("lease moved to hel despite a pre-promotion failure")
	}
}
