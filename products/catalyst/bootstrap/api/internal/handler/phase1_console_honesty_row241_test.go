package handler

import (
	"errors"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// UAT row 241 restated as a test.
//
// The clause: a deployment record's console health MATCHES the live front
// door — `consoleDegraded` false/absent exactly while https://console.<fqdn>/
// answers, `consoleDegraded: true` with a non-empty detail whenever it does
// not — and it FAILS IN EITHER DIRECTION. The probe SURFACES, it never gates
// (#5253).
//
// Before this test the second direction was structurally unreachable rather
// than merely buggy. markPhase1Done ran the probe only under
// `outcome == OutcomeReady`, so a record latched `failed` never probed at
// all; ConsoleDegraded stayed zero-valued and `omitempty` dropped both fields
// from the JSON. The record then read "no console problem" about a console
// nobody had looked at. Measured on hw293, dep a0077ba47e3720e5: the record
// sat `status: failed` with consoleDegraded absent while the door answered
// 200 from the public internet — record and door disagreeing, which is
// exactly what the clause forbids.
//
// The table drives EVERY terminal outcome constant against BOTH probe
// results, which is the "either direction" clause made mechanical: 2N cells,
// none of which may be silent.
func TestConsoleHonesty_Row241_EveryOutcomeSurfacesTheDoor(t *testing.T) {
	outcomes := []string{
		helmwatch.OutcomeReady,
		helmwatch.OutcomeFailed,
		helmwatch.OutcomeTimeout,
		helmwatch.OutcomeFluxNotReconciling,
		helmwatch.OutcomeKubeconfigMissing,
		helmwatch.OutcomeWatcherStartFailed,
		helmwatch.OutcomeFluxCRDsAbsent,
	}
	doorShut := errors.New("console front door did not answer: dial tcp: connect: connection refused")

	mkDep := func() *Deployment {
		return &Deployment{
			ID:        "row241",
			Status:    "phase1-watching",
			StartedAt: time.Now(),
			eventsCh:  make(chan provisioner.Event, 256),
			done:      make(chan struct{}),
			Request: provisioner.Request{
				SovereignFQDN: "hw999.omani.works",
				OrgEmail:      "sovereign-admin@test.example.com",
				Regions:       []provisioner.RegionSpec{{Provider: "huawei"}},
			},
			Result:     &provisioner.Result{},
			OwnerEmail: "sovereign-admin@test.example.com",
		}
	}
	finalStates := map[string]string{"cilium": helmwatch.StateInstalled}

	for _, outcome := range outcomes {
		for _, shut := range []bool{false, true} {
			name := outcome + "/door-open"
			if shut {
				name = outcome + "/door-shut"
			}
			t.Run(name, func(t *testing.T) {
				h := NewWithPDM(silentLogger(), &fakePDM{})
				h.suppressPostHandoverHooks = true
				h.SetHandoverSigner(loadTestSigner(t))
				probed := false
				h.consoleProbe = func(string) error {
					probed = true
					if shut {
						return doorShut
					}
					return nil
				}

				dep := mkDep()
				h.markPhase1Done(dep, finalStates, outcome)

				if !probed {
					t.Fatalf("outcome %q never probed the front door — the record cannot "+
						"honestly report console health it did not measure", outcome)
				}
				if dep.Result.ConsoleDegraded != shut {
					t.Fatalf("outcome %q: ConsoleDegraded = %v, want %v (door shut = %v)",
						outcome, dep.Result.ConsoleDegraded, shut, shut)
				}
				if shut && dep.Result.ConsoleDegradedDetail == "" {
					t.Fatalf("outcome %q: a degraded console must carry a non-empty detail; "+
						"the clause names it explicitly", outcome)
				}
				if !shut && dep.Result.ConsoleDegradedDetail != "" {
					t.Fatalf("outcome %q: an answering console must not carry a stale detail, got %q",
						outcome, dep.Result.ConsoleDegradedDetail)
				}
			})
		}
	}
}

// CONTROL for the table above, sharing its suspect property.
//
// The table asserts the probe now runs everywhere. The obvious way to break
// the platform while making that table pass is to let the probe result leak
// into the lifecycle — which is the ORIGINAL #4706 defect inverted, and the
// reason #5253 made the flag a surface. So: hold the outcome fixed and vary
// ONLY the probe result. Status, the error text and the handover fire must be
// byte-identical across the pair. A probe that gated anything would show up
// here as a diff, and this control is what makes the table's "probe runs on
// every outcome" safe rather than merely true.
func TestConsoleHonesty_Row241_ProbeSurfacesButNeverGates(t *testing.T) {
	mkDep := func() *Deployment {
		return &Deployment{
			ID:        "row241-control",
			Status:    "phase1-watching",
			StartedAt: time.Now(),
			eventsCh:  make(chan provisioner.Event, 256),
			done:      make(chan struct{}),
			Request: provisioner.Request{
				SovereignFQDN: "hw999.omani.works",
				OrgEmail:      "sovereign-admin@test.example.com",
				Regions:       []provisioner.RegionSpec{{Provider: "huawei"}},
			},
			Result:     &provisioner.Result{},
			OwnerEmail: "sovereign-admin@test.example.com",
		}
	}
	finalStates := map[string]string{"cilium": helmwatch.StateInstalled}

	run := func(t *testing.T, outcome string, probeErr error) (status, errText string, fired bool) {
		t.Helper()
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.suppressPostHandoverHooks = true
		h.SetHandoverSigner(loadTestSigner(t))
		h.consoleProbe = func(string) error { return probeErr }
		dep := mkDep()
		h.markPhase1Done(dep, finalStates, outcome)
		return dep.Status, dep.Error, dep.Result.HandoverFiredAt != nil
	}

	for _, outcome := range []string{
		helmwatch.OutcomeReady,
		helmwatch.OutcomeFailed,
		helmwatch.OutcomeTimeout,
		helmwatch.OutcomeFluxNotReconciling,
	} {
		t.Run(outcome, func(t *testing.T) {
			okStatus, okErr, okFired := run(t, outcome, nil)
			shutStatus, shutErr, shutFired := run(t, outcome, errors.New("door shut"))

			if okStatus != shutStatus {
				t.Fatalf("outcome %q: the console probe changed Status (%q with the door open, "+
					"%q with it shut) — the flag is a surface and must never gate (#5253)",
					outcome, okStatus, shutStatus)
			}
			if okErr != shutErr {
				t.Fatalf("outcome %q: the console probe changed dep.Error (%q vs %q); "+
					"/deployments/{id} renders a non-empty Error as a hard failure card",
					outcome, okErr, shutErr)
			}
			if okFired != shutFired {
				t.Fatalf("outcome %q: the console probe changed whether the producer chain fired "+
					"(%v vs %v) — that is the hw276 latch this contract exists to prevent",
					outcome, okFired, shutFired)
			}
		})
	}
}
