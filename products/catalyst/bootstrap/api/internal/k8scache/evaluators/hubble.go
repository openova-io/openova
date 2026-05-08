// hubble.go — Hubble flow-observed evaluator.
//
// EPIC-1 (#1096) §4.3 row "Hubble flows observed (last 5m)".
//
// **Status — DEFERRED HUBBLE CLIENT**:
//
// The brief permits this evaluator to defer when the Hubble Observer
// gRPC client (`github.com/cilium/cilium/api/v1/observer`) is not in
// the catalyst-api dependency graph. As of this slice (W2) the
// catalyst-api has NOT pulled `github.com/cilium/cilium` — adding it
// would import the full Cilium operator graph (~30 transitive Go
// modules; ~80MB on `go mod download`). The Coordinator's standing
// rule on dep weight is to land the synthetic-row plumbing first +
// wire the Hubble client in a follow-up slice once the score
// aggregator has a real consumer.
//
// Behaviour TODAY:
//
//   - When Config.HubbleEnabled == false (the default), the evaluator
//     emits result=skip for every Pod. Score aggregator drops skip
//     rows from the denominator → no false negatives on the score.
//   - When Config.HubbleEnabled == true (after the follow-up wires
//     the client), the evaluator queries the Observer API for the
//     last Config.HubbleLookbackWindow (default 5min) of flows
//     touching the Pod's IP. Pass when ≥1 flow seen, fail otherwise.
//
// Wiring of the actual Hubble client lives in the follow-up issue;
// this file documents the contract so the consumer (slice S1) can
// plan around the skip-then-pass transition without re-tooling.
package evaluators

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// HubbleEvaluator implements `policy=hubble-flows-seen`.
type HubbleEvaluator struct {
	Enabled bool

	// Lookback — passed to the Observer API client when Enabled.
	// Stored so a future implementation reads it; not used today.
	LookbackSeconds int64

	// Probe — pluggable client used in the Enabled branch.
	// Production wiring (follow-up slice) sets this to a thin
	// wrapper around the cilium/cilium observer gRPC client. Tests
	// inject a fake to exercise pass/fail without the Hubble dep.
	Probe HubbleProbe
}

// HubbleProbe is the pluggable interface the evaluator calls when
// Enabled. The implementation queries the Hubble Observer API for
// flows touching the given Pod within the given lookback window.
type HubbleProbe interface {
	// FlowsSeen returns true if Hubble has observed at least one flow
	// to/from the Pod's IP/UID in the last `lookbackSeconds` seconds.
	// Errors fail open (pass) — the evaluator does not punish a Pod
	// for an unreachable Hubble Observer; ops can detect Hubble
	// outages via the cluster-level alarm.
	FlowsSeen(ctx context.Context, pod *unstructured.Unstructured, lookbackSeconds int64) (bool, error)
}

// NewHubbleEvaluator builds a HubbleEvaluator from cfg.
func NewHubbleEvaluator(cfg Config) *HubbleEvaluator {
	return &HubbleEvaluator{
		Enabled:         cfg.HubbleEnabled,
		LookbackSeconds: int64(cfg.HubbleLookbackWindow.Seconds()),
	}
}

func (HubbleEvaluator) Name() string { return "hubble-flows-seen" }

func (h *HubbleEvaluator) Evaluate(ctx context.Context, _ Snapshot, target *unstructured.Unstructured) []SyntheticReport {
	if !isPod(target) {
		return nil
	}
	if !h.Enabled || h.Probe == nil {
		return []SyntheticReport{{
			Policy:    h.Name(),
			Rule:      h.Name(),
			Result:    ResultSkip,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "Hubble UI not enabled on this Sovereign — evaluator skipped",
			Properties: map[string]string{
				"reason": "hubble-disabled",
			},
		}}
	}

	seen, err := h.Probe.FlowsSeen(ctx, target, h.LookbackSeconds)
	if err != nil {
		// Fail open — surface as warn so the score isn't depressed
		// by a transient Hubble Observer outage.
		return []SyntheticReport{{
			Policy:    h.Name(),
			Rule:      h.Name(),
			Result:    ResultWarn,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "Hubble Observer unreachable — flow check inconclusive",
			Properties: map[string]string{
				"err": err.Error(),
			},
		}}
	}
	if seen {
		return []SyntheticReport{{
			Policy:    h.Name(),
			Rule:      h.Name(),
			Result:    ResultPass,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "Hubble observed flows to/from Pod within lookback window",
		}}
	}
	return []SyntheticReport{{
		Policy:    h.Name(),
		Rule:      h.Name(),
		Result:    ResultFail,
		Resource:  resourceFor(target),
		Namespace: target.GetNamespace(),
		Message:   "Hubble observed no flows to/from Pod within lookback window",
	}}
}
