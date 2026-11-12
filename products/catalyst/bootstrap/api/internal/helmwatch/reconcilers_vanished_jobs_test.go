// reconcilers_vanished_jobs_test.go — the producer half of the "Jobs page
// says Running forever" defect (hw307, 2026-11-12).
//
// A WordPress install was Ready in 2m41s. Its admin-user hook Job (Helm
// hook-delete-policy hook-succeeded) and its CloudNativePG initdb Job both
// completed and were DELETED seconds later, so no later LIST ever saw them
// terminate; the Jobs page kept both "Running" 30 minutes on. The outcome of
// such a run survives only in the terminal Job event (kept ~1h) and, for a
// Helm hook, in its delete policy. These tests pin that the producer reports
// both, and that it reports when it could NOT look (so the bridge never reads
// "no event" out of a failed read).
package helmwatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// vanishedFakeClient is reconcilerFakeClient, optionally with the event
// LIST failing the way an RBAC denial does.
func vanishedFakeClient(eventsReadable bool, objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	dyn := reconcilerFakeClient(objs...)
	if !eventsReadable {
		dyn.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(`events.events.k8s.io is forbidden`)
		})
	}
	return dyn
}

func jobEvent(ns, name, jobName, reason, typ, at string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "events.k8s.io/v1",
		"kind":       "Event",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"regarding": map[string]any{
			"apiVersion": "batch/v1", "kind": "Job", "name": jobName, "namespace": ns,
		},
		"reason":                  reason,
		"type":                    typ,
		"note":                    reason,
		"deprecatedLastTimestamp": at,
	}}
}

func runningHookJob(ns, name, policy string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name": name, "namespace": ns,
			"annotations": map[string]any{
				"helm.sh/hook":               "post-install,post-upgrade",
				"helm.sh/hook-delete-policy": policy,
			},
			"labels": map[string]any{"app.kubernetes.io/instance": "wp-rtz-a"},
		},
		"status": map[string]any{"startTime": "2026-11-12T08:10:40Z", "active": int64(1)},
	}}
	return u
}

// The live shape: the initdb + admin-user Jobs are GONE, their Completed
// events remain; a policy-engine warning on the same Job is NOT an outcome.
func TestJobLedger_EventsCarryTheOutcomeOfDeletedJobs(t *testing.T) {
	dyn := vanishedFakeClient(true,
		jobEvent("hw307-omani-works", "e1", "wp-db-6e1e3f9c-1-initdb", "Completed", "Normal", "2026-11-12T08:09:50Z"),
		jobEvent("hw307-omani-works", "e2", "wp-bp-wordpress-tenant-admin-user", "Completed", "Normal", "2026-11-12T08:11:35Z"),
		jobEvent("hw307-omani-works", "e3", "wp-db-6e1e3f9c-1-initdb", "PolicyViolation", "Warning", "2026-11-12T08:10:10Z"),
		jobEvent("acme", "e4", "seed-catalog", "BackoffLimitExceeded", "Warning", "2026-11-12T07:00:00Z"),
		jobEvent("acme", "e5", "seed-catalog", "SuccessfulCreate", "Normal", "2026-11-12T06:59:00Z"),
	)
	_, led, err := ListReconcilerObservationsWithLedger(context.Background(), dyn)
	if err != nil {
		t.Fatal(err)
	}
	if !led.Listed || !led.EventsListed {
		t.Fatalf("both LISTs succeeded, ledger says Listed=%v EventsListed=%v", led.Listed, led.EventsListed)
	}
	if len(led.Present) != 0 {
		t.Fatalf("no Job exists, Present=%v", led.Present)
	}
	want := map[string]string{
		"wp-db-6e1e3f9c-1-initdb":           ObsStatusSucceeded,
		"wp-bp-wordpress-tenant-admin-user": ObsStatusSucceeded,
		"seed-catalog":                      ObsStatusFailed,
	}
	for name, st := range want {
		got, ok := led.Ended[name]
		if !ok || got.Status != st {
			t.Errorf("Ended[%s] = %+v (present=%v), want status %s", name, got, ok, st)
		}
	}
	// The policy warning came AFTER Completed and must not displace it.
	if r := led.Ended["wp-db-6e1e3f9c-1-initdb"].Reason; r != "Completed" {
		t.Errorf("initdb verdict reason = %q, want Completed (a PolicyViolation is not an outcome)", r)
	}
	if at := led.Ended["wp-bp-wordpress-tenant-admin-user"].At; !at.Equal(time.Date(2026, 11, 12, 8, 11, 35, 0, time.UTC)) {
		t.Errorf("admin-user ended at %v, want the event time", at)
	}
	if len(led.Ended) != len(want) {
		t.Errorf("Ended has %d entries, want %d: %v", len(led.Ended), len(want), led.Ended)
	}
}

// A failed event read must be distinguishable from "no events": the bridge
// closes evidence-less runs Failed only when the read itself worked.
func TestJobLedger_EventReadFailureIsNotNoEvents(t *testing.T) {
	dyn := vanishedFakeClient(false, runningHookJob("ns", "j1", "hook-succeeded"))
	_, led, err := ListReconcilerObservationsWithLedger(context.Background(), dyn)
	if err != nil {
		t.Fatal(err)
	}
	if !led.Listed || !led.Present["j1"] {
		t.Fatalf("Job LIST worked: Listed=%v Present=%v", led.Listed, led.Present)
	}
	if led.EventsListed {
		t.Fatal("event LIST was denied but EventsListed=true")
	}
}

// The delete policy is recorded on the run while the Job is visible: only
// hook-succeeded WITHOUT hook-failed means "Helm deletes this on success".
func TestJobLedger_RemovedOnSuccessFromHookDeletePolicy(t *testing.T) {
	cases := map[string]bool{
		"before-hook-creation,hook-succeeded": true,
		"hook-succeeded":                      true,
		"hook-succeeded,hook-failed":          false,
		"before-hook-creation":                false,
		"":                                    false,
	}
	for policy, want := range cases {
		obs, _, err := ListReconcilerObservationsWithLedger(context.Background(),
			vanishedFakeClient(true, runningHookJob("ns", "wp-bp-wordpress-tenant-admin-user", policy)))
		if err != nil {
			t.Fatal(err)
		}
		var got *bool
		for _, o := range obs {
			for _, e := range o.Executions {
				if e.Name == "wp-bp-wordpress-tenant-admin-user" {
					v := e.RemovedOnSuccess
					got = &v
				}
			}
		}
		if got == nil {
			t.Fatalf("policy %q: run not observed as an execution: %+v", policy, obs)
		}
		if *got != want {
			t.Errorf("policy %q: RemovedOnSuccess=%v, want %v", policy, *got, want)
		}
	}
	// A plain Job with the same policy annotation but no helm.sh/hook is not a hook.
	plain := runningHookJob("ns", "plain", "hook-succeeded")
	delete(plain.Object["metadata"].(map[string]any)["annotations"].(map[string]any), "helm.sh/hook")
	if removedOnSuccess(plain) {
		t.Error("a Job without helm.sh/hook must not be treated as Helm-deleted-on-success")
	}
}
