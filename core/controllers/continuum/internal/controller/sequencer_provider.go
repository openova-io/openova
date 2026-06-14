// SequencerProvider adapter — slice F-2 + F-3 of EPIC-6 (#1101).
//
// The internal/api Server consumes a SequencerProvider so it can build
// a Sequencer + partial SwitchoverPlan for any (ns, name) pair without
// importing the controller package directly (the api package lives at
// the same depth as the controller, so importing "controller" would
// pull controller-runtime into the api binary surface area).
//
// The reconciler's `SequencerFor` method returns:
//
//   - A Sequencer wired with the controller's shared deps (PDM client,
//     audit publisher, drainer, region) and the per-CR witness from
//     the WitnessSelector.
//   - A SwitchoverPlan with FromRegion + CNPGPair + ... pre-populated
//     from the CR spec. The api.Server fills in ToRegion + Reason
//     from the request body.
//
// Compile-time assertion at the bottom keeps the interface alignment
// honest.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
)

// ErrCRNotFound — returned by SequencerFor when the CR doesn't exist.
// Same sentinel shape the api.Server checks.
var ErrCRNotFound = errors.New("controller: Continuum CR not found")

// SequencerFor implements the api.SequencerProvider contract.
// Returns a Sequencer + a partial SwitchoverPlan with FromRegion +
// CNPGPair etc. pre-populated from the CR spec.
//
// The returned Sequencer's witness is selected per-CR via the
// reconciler's WitnessSelector (the same selection path runPerCR
// uses) — so a dry-run uses the SAME witness backend the actual
// switchover would use.
func (r *ContinuumReconciler) SequencerFor(ns, name string) (*switchover.Sequencer, switchover.SwitchoverPlan, error) {
	if r == nil {
		return nil, switchover.SwitchoverPlan{}, errors.New("controller: nil reconciler")
	}
	if r.Dyn == nil {
		return nil, switchover.SwitchoverPlan{}, errors.New("controller: Dyn not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cr, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, switchover.SwitchoverPlan{}, ErrCRNotFound
	}
	if err != nil {
		return nil, switchover.SwitchoverPlan{}, fmt.Errorf("get CR: %w", err)
	}
	spec, vErr := parseSpec(cr)
	if vErr != nil {
		return nil, switchover.SwitchoverPlan{}, fmt.Errorf("parseSpec: %w", vErr)
	}

	w, sErr := r.selectWitness(types.NamespacedName{Namespace: ns, Name: name}, spec)
	if sErr != nil {
		return nil, switchover.SwitchoverPlan{}, fmt.Errorf("witness selector: %w", sErr)
	}

	zone := spec.PDMZone
	seq := &switchover.Sequencer{
		CNPG:         r.cnpgReader(),
		RaftPromoter: r.RaftPromoter,
		Witness:      w,
		PDMCommit: func(ctx context.Context, records []dns.Record) error {
			if r.PDMClient == nil {
				return errors.New("PDMClient not configured")
			}
			z := zone
			if z == "" {
				z = currentZone(records)
			}
			return r.PDMClient.CommitLuaRecords(ctx, z, records)
		},
		HTTPRoute: r.Drainer,
		Audit:     r.Audit,
		Now:       r.Now,
		Sleep:     r.Sleep,
	}

	plan := switchover.SwitchoverPlan{
		ContinuumName:      types.NamespacedName{Namespace: ns, Name: name}.String(),
		ApplicationName:    spec.ApplicationRef,
		FromRegion:         spec.PrimaryRegion,
		Mechanism:          spec.Mechanism,
		CNPGPair:           spec.CNPGPair,
		CNPGNamespace:      spec.CNPGNamespace,
		RaftTransition:     spec.RaftTransition,
		HTTPRouteName:      spec.HTTPRouteName,
		HTTPRouteNamespace: spec.HTTPRouteNamespace,
		PDMZone:            spec.PDMZone,
		SynthParams:        spec.SynthParams,
		Application:        spec.Application,
		InitiatedBy:        spec.InitiatedBy,
		LeaseTTL:           time.Duration(spec.TTLSeconds) * time.Second,
	}
	return seq, plan, nil
}

// Compile-time hint that the reconciler satisfies the api.SequencerProvider
// shape. We can't import api here (cyclic), but the method signature is
// the contract.
//
// The api package's interface:
//
//	type SequencerProvider interface {
//	    SequencerFor(ns, name string) (*switchover.Sequencer, switchover.SwitchoverPlan, error)
//	}
//
// Any drift here vs api.SequencerProvider will surface at the cmd/main.go
// wiring site (`api.Server{Provider: r}`) as a "does not implement" error.

// silence unused import warnings on builds that strip them
var _ = cnpg.PairLabel
