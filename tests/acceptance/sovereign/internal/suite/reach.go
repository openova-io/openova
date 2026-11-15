package suite

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/retry"
)

// The discovery (journey D0: discover.go and provision.go) reads the Sovereign
// over two kinds of path: the console API and the Kubernetes API of each
// region. Its reads are retried as package retry says: a read that got no
// answer, or an answer that means "try again", is made again, at most 5 times
// within 2 minutes; a real answer is never made again. The journeys and the
// checks are not retried, because a call of theirs may write.
//
// A run whose discovery could not reach the Sovereign measured nothing. Its
// result is NOT-MEASURED when some step of D0 failed only because a region or
// the console gave no answer after the retries, and no step of D0 failed on an
// answer. Such a run is no gate result, pass or fail: the suite exits with code
// 4 and writes no record (Execute skips it, Run.Write refuses it), so it is
// never one of the two runs that the pass rule of scripts/acceptance-gate.py
// pairs: that rule reads only the record files in docs/ledger/acceptance/, and
// its validate-record refuses a result other than PASS or FAIL, so the
// workflow could not commit one either (#7922: gate run 3 on hw307 wrote a FAIL
// record without an artefact list after one broken exec stream, and that record
// kept the next run from forming a pair). A step of D0 that failed on an answer
// is a measurement: the run is FAIL and is recorded, as before.

// NotMeasured is the result of a run whose discovery could not reach the Sovereign.
const NotMeasured = "NOT-MEASURED"

// newRetryPolicy gives the retry policy of one target of the discovery. The
// tests of this package replace it with one that does not wait.
var newRetryPolicy = retry.Default

// reach holds one retry policy per target, so that a region that is down
// shortens the retries of that region only.
type reach struct{ a, b, api *retry.Policy }

// reaching runs fn, a part of the discovery, with the retry policies set on
// both regions and on the console API, and takes them off again after.
func (r *runner) reaching(fn func()) {
	if r.reach == nil {
		r.reach = &reach{a: newRetryPolicy(), b: newRetryPolicy(), api: newRetryPolicy()}
	}
	r.A.Retry, r.B.Retry = r.reach.a, r.reach.b
	if r.api != nil {
		r.api.Retry = r.reach.api
	}
	defer func() {
		r.A.Retry, r.B.Retry = nil, nil
		if r.api != nil {
			r.api.Retry = nil
		}
	}()
	fn()
}

// noteUnreached remembers a step of D0 that failed only because the Sovereign
// could not be reached.
func (r *runner) noteUnreached(journey, id string, err error) {
	if journey == "D0" && retry.OnlyUnreachable(err) {
		r.run.unreached = append(r.run.unreached, id)
	}
}

// unmeasured says whether the discovery failed only because the Sovereign could
// not be reached: a step of D0 failed so, and every step of D0 that did not
// pass failed so.
func (r *Run) unmeasured() bool {
	if len(r.unreached) == 0 {
		return false
	}
	for _, s := range r.Steps {
		if s.Journey == "D0" && s.Status != Pass && !slices.Contains(r.unreached, s.ID) {
			return false
		}
	}
	return true
}

// printNotMeasured writes the summary line of a run that is NOT-MEASURED.
func (r *Run) printNotMeasured(w io.Writer) {
	if r.Result != NotMeasured {
		return
	}
	_, _ = fmt.Fprintf(w, "NOT MEASURED: the discovery could not reach the Sovereign after its retries (step %s). "+
		"This run is no gate result, pass or fail; it writes no record and is not one of the two runs the gate pairs.\n",
		strings.Join(r.unreached, ", "))
}
