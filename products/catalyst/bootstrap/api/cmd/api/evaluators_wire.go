// evaluators_wire.go — Wave 5.65b (#2337 / #1096): wires the custom-
// evaluator Engine to the running k8scache.Factory.
//
// Per the design in `internal/k8scache/evaluators/evaluators.go`:
//
//   - Engine subscribes to Factory SSE for trigger-based re-evaluations
//     (Pod ADD/MODIFY → re-run all evaluators on the new state).
//   - Engine ticks on a fixed cadence (Config.TickInterval) to re-sweep
//     every cluster, catching drift that doesn't surface as an event.
//   - Each evaluator emits zero or more SyntheticReport rows; the
//     Publisher fanout pushes them onto the same SSE stream the UI's
//     useComplianceStream hook reads. The handler/compliance.go
//     aggregator joins these synthetic rows with Kyverno-emitted
//     PolicyReports + EnvironmentPolicy weights to produce per-resource
//     + per-Application + per-Organization scores.
//
// Adapter layout:
//
//   - resolveSnapshot — per-clusterID closure over Factory.List
//   - subscribe       — converts Factory.Subscribe's <-chan k8scache.Event
//                       into <-chan evaluators.Event (Engine's wire type)
//   - publisher       — bridges evaluators.SyntheticReport → Factory.Publish
//                       (wraps each as a Catalyst-canonical Event)
//
// Failure modes: any wire-up error is non-fatal — the caller logs +
// continues with custom evaluators disabled, but Kyverno-emitted
// PolicyReports still flow through the existing aggregator.
package main

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache/evaluators"
)

// wireEvaluatorEngine builds + starts the evaluators.Engine over the
// running k8scache.Factory. Returns nil on success. On error, caller
// logs and continues without custom evaluators.
func wireEvaluatorEngine(ctx context.Context, log *slog.Logger, factory *k8scache.Factory) error {
	if factory == nil {
		return fmt.Errorf("nil factory")
	}

	cfg := evaluators.Config{Logger: log}

	evals := []evaluators.Evaluator{
		evaluators.NewHPAEvaluator(cfg),
		evaluators.NewOTelEvaluator(cfg),
		evaluators.NewHubbleEvaluator(cfg),
		evaluators.NewHarborEvaluator(cfg),
		evaluators.NewFluxEvaluator(cfg),
	}

	// resolveSnapshot: Engine asks for cluster X's Snapshot. Adapter
	// wraps Factory.List with a fixed clusterID. Returns the list of
	// kinds the snapshot supports (used by Engine to skip evaluators
	// whose required kinds aren't watched).
	resolveSnapshot := func(clusterID string) (evaluators.Snapshot, []string, error) {
		snap := &factorySnapshot{factory: factory, clusterID: clusterID}
		// Engine doesn't filter on this list today; pass empty to opt in
		// to all evaluators. Future: query Factory.Registry().Kinds().
		return snap, nil, nil
	}

	// subscribe: Engine asks for an event channel; adapter wires
	// Factory.Subscribe + converts each k8scache.Event to evaluators.Event.
	subscribe := func(kinds map[string]struct{}) (<-chan evaluators.Event, func()) {
		// Factory.Subscribe takes a `user` for SAR-based RBAC filtering;
		// the evaluator engine runs server-side so we use a sentinel
		// internal user that bypasses tenant filtering.
		src, cancel := factory.Subscribe("system:catalyst-evaluator", kinds)
		out := make(chan evaluators.Event, cap(src))
		go func() {
			defer close(out)
			for ev := range src {
				if ev.Object == nil {
					continue
				}
				out <- evaluators.Event{
					Cluster: ev.Cluster,
					Kind:    ev.Kind,
					Object:  ev.Object,
				}
			}
		}()
		return out, cancel
	}

	pub := factoryPublisher{factory: factory, log: log}

	engine, err := evaluators.NewEngine(cfg, evals, pub, resolveSnapshot, subscribe)
	if err != nil {
		return fmt.Errorf("NewEngine: %w", err)
	}

	go func() {
		if err := engine.Run(ctx); err != nil && ctx.Err() == nil {
			log.Warn("evaluators: engine.Run returned error", "err", err)
		}
	}()

	return nil
}

// factorySnapshot adapts k8scache.Factory.List to the evaluators.Snapshot
// read interface for a single clusterID.
type factorySnapshot struct {
	factory   *k8scache.Factory
	clusterID string
}

func (s *factorySnapshot) List(kindName string, sel labels.Selector) ([]*unstructured.Unstructured, error) {
	objs, _, err := s.factory.List(s.clusterID, kindName, sel)
	return objs, err
}

// factoryPublisher bridges evaluators.SyntheticReport to the Factory's
// SSE fanout (Factory.Publish takes a k8scache.Event). The synthetic
// report's PolicyReport-like shape is wrapped as an Event with
// Kind="policyreport" so the existing handler/compliance.go aggregator
// treats it identically to a Kyverno-emitted PolicyReport CR.
type factoryPublisher struct {
	factory *k8scache.Factory
	log     *slog.Logger
}

func (p factoryPublisher) Publish(clusterID string, report evaluators.SyntheticReport) {
	// SyntheticReport carries the policy verdict + target ref; wrap as
	// a synthetic PolicyReport-shaped unstructured so the existing
	// handler/compliance.go aggregator treats it identically to a
	// Kyverno-emitted PolicyReport CR (Kind="policyreport" + the
	// per-policy result in spec.results[]).
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("wgpolicyk8s.io/v1alpha2")
	obj.SetKind("PolicyReport")
	obj.SetNamespace(report.Namespace)
	// Synthetic PolicyReport name = "<policy>-<targetUID>" so per-
	// workload reports don't collide; matches the Kyverno convention.
	name := fmt.Sprintf("%s-%s", report.Policy, report.Resource.UID)
	obj.SetName(name)
	obj.Object["spec"] = map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{
				"policy":  report.Policy,
				"rule":    report.Rule,
				"result":  string(report.Result),
				"message": report.Message,
			},
		},
	}
	p.factory.Publish(k8scache.Event{
		Cluster: clusterID,
		Kind:    "policyreport",
		Type:    k8scache.EventModified,
		Object:  obj,
	})
}
