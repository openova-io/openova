// Package handler — continuum_convergence.go: the DESIRED-STATE view of a
// Continuum CR, shared by every read path that reports "which region is the
// primary".
//
// Model (founder, 2026-11-07): the operator never REQUESTS a switchover. They
// declare the Application's Primary region in the Topology tab's placement
// editor; the Application controller projects that into the Continuum's
// `spec.desiredPrimaryRegion`, and the continuum-controller CONVERGES —
// running the 7-step switchover itself when the desired primary differs from
// the region actually serving writes. The imperative
// `POST .../switchover` route (which patched spec.switchover.requested) is
// gone; nothing in catalyst-api writes spec.switchover anymore.
//
// So every read surface must tell two values apart:
//
//   - observed — `status.primaryRegion`, the region serving writes NOW
//     (written by the continuum-controller). This is the truth.
//   - desired  — `status.desiredPrimaryRegion` (the controller's echo), else
//     `spec.desiredPrimaryRegion` (what the Application controller wrote).
//
// `spec.primaryRegion` is the STATIC home region of the pair's first half — a
// topology fact, not the writer. It is only ever a last-resort fallback for
// "observed" when the controller has not reconciled a status yet.
package handler

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// continuumLastSwitchover — status.lastSwitchover, relayed verbatim.
type continuumLastSwitchover struct {
	At     string `json:"at,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Result string `json:"result,omitempty"`
}

// continuumConvergence — the desired-vs-observed projection. Embedded (JSON
// flattened) into the enriched GET, replication-status and Application detail
// responses so the console reads one shape everywhere.
type continuumConvergence struct {
	// DesiredPrimaryRegion — the region the operator declared Primary in the
	// placement (status echo, else spec). Empty when nothing is declared.
	DesiredPrimaryRegion string `json:"desiredPrimaryRegion,omitempty"`
	// ObservedPrimaryRegion — status.primaryRegion verbatim (empty when the
	// controller has not published it). Kept separate from `currentPrimary`
	// (which may fall back) so a consumer can tell an observation from a guess.
	ObservedPrimaryRegion string `json:"observedPrimaryRegion,omitempty"`
	// Converged — observed == desired. status.converged when the controller
	// publishes it, else the Converged condition, else derived.
	Converged bool `json:"converged"`
	// SwitchoverInProgress — the controller is running the 7-step sequence.
	SwitchoverInProgress bool `json:"switchoverInProgress"`
	// Step — status.message while SwitchoverInProgress ("step N/7 — <name>").
	Step string `json:"step,omitempty"`
	// LastSwitchoverRecord — status.lastSwitchover {at, from, to, result}.
	LastSwitchoverRecord *continuumLastSwitchover `json:"lastSwitchover,omitempty"`
	// ConvergeReason / ConvergeMessage — the Converged condition's reason +
	// message: WHY it is not converged / what blocks it / when it retries.
	ConvergeReason  string `json:"convergeReason,omitempty"`
	ConvergeMessage string `json:"convergeMessage,omitempty"`
	// Transition — the Continuum's current (or most recent) primary move,
	// verbatim from status.transition + status.activity, so the console can
	// show every step while it runs. Nil when the Continuum never moved.
	Transition *drTransition `json:"transition,omitempty"`
}

// drTransition is one primary move as the Continuum controller records it.
type drTransition struct {
	ID        string `json:"id,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	// Outcome — the controller's verdict: Transitioning while it runs, then
	// Converged (or a failure outcome).
	Outcome string `json:"outcome,omitempty"`
	Message string `json:"message,omitempty"`
	// Active — the move is still running.
	Active bool `json:"active"`
	// JobName — the Jobs group that records this move (the projection in
	// application_jobs_projection.go names it the same way), so the console
	// can link to its full log.
	JobName string             `json:"jobName,omitempty"`
	Steps   []drTransitionStep `json:"steps"`
}

// drTransitionStep is one status.activity entry, verbatim.
type drTransitionStep struct {
	Phase      string `json:"phase"`
	Member     string `json:"member,omitempty"`
	State      string `json:"state"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Message    string `json:"message,omitempty"`
}

// continuumPhaseMoving reports whether a Continuum phase means a primary move
// is running. "SwitchingOver" is the pre-ADR-0015 name; the transition
// engine reports "Transitioning". Reading only the old name made the console
// say no switchover was in progress during one (hw307, 2026-11-12).
func continuumPhaseMoving(phase string) bool {
	return phase == "SwitchingOver" || phase == "Transitioning"
}

// continuumTransitionFromCR lifts status.transition + status.activity into
// the wire shape. Returns nil when the Continuum has no transition on record.
func continuumTransitionFromCR(cr *unstructured.Unstructured, moving bool) *drTransition {
	entries := activityEntriesOf(cr)
	rec, hasRec, _ := unstructured.NestedMap(cr.Object, "status", "transition")
	if len(entries) == 0 && !hasRec {
		return nil
	}
	t := &drTransition{Active: moving, Steps: make([]drTransitionStep, 0, len(entries))}
	if hasRec {
		t.ID, _ = rec["id"].(string)
		t.From, _ = rec["from"].(string)
		t.To, _ = rec["to"].(string)
		t.StartedAt, _ = rec["startedAt"].(string)
		t.Outcome, _ = rec["outcome"].(string)
		t.Message, _ = rec["message"].(string)
	}
	for _, e := range entries {
		t.Steps = append(t.Steps, drTransitionStep{
			Phase: e.Phase, Member: e.Member, State: e.State,
			StartedAt: e.StartedAt, FinishedAt: e.FinishedAt, Message: e.Message,
		})
		if !moving && (e.State == "running" || e.State == "pending") && t.Outcome == "" {
			t.Active = true
		}
	}
	if len(entries) > 0 {
		t.JobName = jobsTopologyTransitionGroup(cr, entries)
	}
	return t
}

// continuumObservedPrimary — status.primaryRegion, the region serving writes.
// Empty when the controller has not published it.
func continuumObservedPrimary(cr *unstructured.Unstructured) string {
	if cr == nil {
		return ""
	}
	s, _, _ := unstructured.NestedString(cr.Object, "status", "primaryRegion")
	return strings.TrimSpace(s)
}

// continuumDesiredPrimary — status.desiredPrimaryRegion, else
// spec.desiredPrimaryRegion.
func continuumDesiredPrimary(cr *unstructured.Unstructured) string {
	if cr == nil {
		return ""
	}
	if s, _, _ := unstructured.NestedString(cr.Object, "status", "desiredPrimaryRegion"); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	s, _, _ := unstructured.NestedString(cr.Object, "spec", "desiredPrimaryRegion")
	return strings.TrimSpace(s)
}

// continuumCondition returns the first status.conditions[] entry of `typ`.
func continuumCondition(cr *unstructured.Unstructured, typ string) (map[string]interface{}, bool) {
	conds, ok, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	if !ok {
		return nil, false
	}
	for _, c := range conds {
		cm, isMap := c.(map[string]interface{})
		if !isMap {
			continue
		}
		if t, _ := cm["type"].(string); t == typ {
			return cm, true
		}
	}
	return nil, false
}

// continuumConvergenceFromCR builds the desired-state projection off a CR.
// `fallbackObserved` is what the caller already resolved as the current
// primary (used only to DERIVE convergence when the controller published
// neither status.converged nor a Converged condition).
func continuumConvergenceFromCR(cr *unstructured.Unstructured, fallbackObserved string) continuumConvergence {
	out := continuumConvergence{}
	if cr == nil {
		out.Converged = true
		return out
	}
	out.DesiredPrimaryRegion = continuumDesiredPrimary(cr)
	out.ObservedPrimaryRegion = continuumObservedPrimary(cr)

	phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	if b, ok, _ := unstructured.NestedBool(cr.Object, "status", "switchoverInProgress"); ok {
		out.SwitchoverInProgress = b
	} else {
		out.SwitchoverInProgress = continuumPhaseMoving(phase)
	}
	out.Transition = continuumTransitionFromCR(cr, out.SwitchoverInProgress)
	if out.SwitchoverInProgress {
		out.Step, _, _ = unstructured.NestedString(cr.Object, "status", "message")
		if out.Step == "" && out.Transition != nil {
			out.Step = runningStepLine(out.Transition.Steps)
		}
	}

	cond, hasCond := continuumCondition(cr, "Converged")
	if hasCond {
		out.ConvergeReason, _ = cond["reason"].(string)
		out.ConvergeMessage, _ = cond["message"].(string)
	}

	switch b, ok, _ := unstructured.NestedBool(cr.Object, "status", "converged"); {
	case ok:
		out.Converged = b
	case hasCond:
		s, _ := cond["status"].(string)
		out.Converged = strings.EqualFold(s, "True")
	default:
		observed := out.ObservedPrimaryRegion
		if observed == "" {
			observed = fallbackObserved
		}
		// Nothing declared ⇒ nothing to converge to. Declared but not
		// observed ⇒ NOT converged (absence of a reading is not agreement).
		out.Converged = out.DesiredPrimaryRegion == "" ||
			(observed != "" && observed == out.DesiredPrimaryRegion)
	}
	if out.SwitchoverInProgress {
		out.Converged = false
	}

	if ls, ok, _ := unstructured.NestedMap(cr.Object, "status", "lastSwitchover"); ok {
		rec := &continuumLastSwitchover{}
		rec.At, _ = ls["at"].(string)
		rec.From, _ = ls["from"].(string)
		rec.To, _ = ls["to"].(string)
		rec.Result, _ = ls["result"].(string)
		if rec.At != "" || rec.From != "" || rec.To != "" || rec.Result != "" {
			out.LastSwitchoverRecord = rec
		}
	}
	return out
}

// applicationDRConvergence — the `dr` block on GET /applications/{name}.
type applicationDRConvergence struct {
	Continuum      string `json:"continuum"`
	CurrentPrimary string `json:"currentPrimary,omitempty"`
	continuumConvergence
}

func applicationDRConvergenceFromCR(cr *unstructured.Unstructured) applicationDRConvergence {
	cur := continuumActingPrimary(cr)
	return applicationDRConvergence{
		Continuum:            cr.GetName(),
		CurrentPrimary:       cur,
		continuumConvergence: continuumConvergenceFromCR(cr, cur),
	}
}

// continuumForApplication fetches the Application's Continuum CR by the
// controller's naming convention (`dr-<app>`, in the Application's
// namespace). A direct GET — never a cluster-wide list — so an app with no
// Continuum costs one NotFound. Returns nil on any miss.
func (h *Handler) continuumForApplication(
	ctx context.Context,
	client dynamic.Interface,
	app *unstructured.Unstructured,
) *unstructured.Unstructured {
	if client == nil || app == nil || app.GetName() == "" || app.GetNamespace() == "" {
		return nil
	}
	cr, err := client.Resource(ContinuumGVR()).Namespace(app.GetNamespace()).
		Get(ctx, "dr-"+app.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return cr
}

// runningStepLine renders "step N/M — <phase>: <message>" for the step the
// transition is on (the first one not yet succeeded).
func runningStepLine(steps []drTransitionStep) string {
	for i, st := range steps {
		if st.State == "succeeded" {
			continue
		}
		line := "step " + itoaInt(i+1) + "/" + itoaInt(len(steps)) + " — " + st.Phase
		if st.Message != "" {
			line += ": " + st.Message
		}
		return line
	}
	return ""
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
