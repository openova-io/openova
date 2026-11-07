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
		out.SwitchoverInProgress = phase == "SwitchingOver"
	}
	if out.SwitchoverInProgress {
		out.Step, _, _ = unstructured.NestedString(cr.Object, "status", "message")
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
