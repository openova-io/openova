package transition

import (
	"fmt"

	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// A move interrupted by a restarted database Pod still completes.
//
// traffic-off waits for the old primary's CNPG Cluster to publish
// status.demotionToken. When that Cluster's instance restarts while the
// demotion is applied, the instance manager demotes it at start-up without a
// token and the operator, seeing no primary instance, never starts the switch
// that would publish one (see Demotion). The wait would then last for ever.
//
// demotionPending recognises that state from the Cluster's own status — the
// spec names another primary, the instances are reported and none runs a
// primary, no switch is in progress, the Cluster is Ready, no token — and
// once it has held for DemotionLostAfter, it issues the demotion again: the
// old primary's file names the old primary again (CNPG promotes its own
// standby: it holds every write, the new region never accepted one), and when
// its Cluster runs a primary the demotion is committed once more, which CNPG
// carries out on a running primary and so publishes the token.
//
// It is safe to run traffic-off twice: it only ever happens inside
// traffic-off, before data-primary has committed anything to the new region
// (a finished step is never re-run), so the new region is a replica
// throughout; the data-primary step commits the token of the demotion that
// completed. ReissueAttempts bounds it; past the bound nothing more is
// re-issued and the phase fails at its deadline (Policy.PhaseTimeout), with
// the Interrupted condition naming why, and the move releases the reboot
// agent it holds (Demotion.Exhausted, HoldOf).

func (e evaluator) setDemotion(m Member, d Demotion) {
	if e.rec.Demotions == nil {
		e.rec.Demotions = map[string]Demotion{}
	}
	e.rec.Demotions[m.Key()] = d
}

// reissued is the Interrupted condition while a member's demotion is issued
// again.
func (e evaluator) reissued(m Member, from string, d Demotion) *Interruption {
	return &Interruption{Reason: ReasonDemotionInterrupted, Message: fmt.Sprintf(
		"%s: the demotion in %s did not complete — the old primary came back as a standby without publishing a demotion token (its instance restarted while CNPG applied the demotion); the demotion is issued again, attempt %d of %d",
		m.App, from, d.Attempts, ReissueAttempts)}
}

// demotionPending is traffic-off's wait for the demotion token of a stateful
// member: it waits, or it issues the demotion again.
func (e evaluator) demotionPending(m Member, from string, ms MemberStatus, d Demotion) evalResult {
	db := ms.DB
	wait := evalResult{msg: fmt.Sprintf("waiting: CNPG Cluster %s in %s has not published status.demotionToken (phase %q)", db.Cluster, from, db.Phase)}
	lostNow := db.Ready && !db.Primary && !db.Switching && db.RunningPrimary == False
	if !lostNow {
		if d.LostSince != "" || d.Exhausted {
			d.LostSince, d.Exhausted = "", false
			e.setDemotion(m, d)
		}
		return wait
	}
	if d.LostSince == "" {
		d.LostSince = ts(e.v.Now)
		e.setDemotion(m, d)
	}
	if d.Attempts >= ReissueAttempts {
		if !d.Exhausted {
			// Nothing more is re-issued: the move no longer holds the reboot
			// agent (HoldOf).
			d.Exhausted = true
			e.setDemotion(m, d)
		}
		wait.interruption = &Interruption{Reason: ReasonDemotionReissueExhausted, Message: fmt.Sprintf(
			"%s: the demotion in %s did not complete after it was issued %d more times; nothing more is re-issued and the phase fails at its deadline (%s)",
			m.App, from, d.Attempts, e.p.PhaseTimeout)}
		return wait
	}
	since, ok := parseTS(d.LostSince)
	if !ok || e.v.Now.Sub(since) < DemotionLostAfter {
		wait.msg = fmt.Sprintf("waiting: CNPG Cluster %s in %s runs no primary and has published no demotion token since %s; the demotion is issued again if that holds for %s", db.Cluster, from, d.LostSince, DemotionLostAfter)
		wait.interruption = &Interruption{Reason: ReasonDemotionInterrupted, Message: fmt.Sprintf(
			"%s: CNPG Cluster %s in %s runs no primary and has published no demotion token since %s; the demotion is issued again if that holds for %s (attempt %d of %d)",
			m.App, db.Cluster, from, d.LostSince, DemotionLostAfter, d.Attempts+1, ReissueAttempts)}
		return wait
	}
	d.Attempts++
	d.Restoring, d.LostSince = true, ""
	e.setDemotion(m, d)
	r := e.commit(m, from, regionfile.TransitionFields{Primary: from}, fmt.Sprintf("restore the old primary, so that CNPG can demote a running primary (the demotion is issued again, attempt %d of %d)", d.Attempts, ReissueAttempts))
	r.interruption = e.reissued(m, from, d)
	return r
}
