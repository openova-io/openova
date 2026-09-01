// reconcilers_suspended_test.go — ListReconcilerObservations must carry
// spec.suspend as ReconcilerObservation.Suspended for the reconciler kinds
// that HAVE a suspend field (Flux Kustomization, batch CronJob), so the jobs
// bridge can render a parked reconciler as Dormant on the dashboard treemap
// (the "suspension wins" precedence) instead of Pending / its last run.
package helmwatch

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestListReconcilerObservations_CarriesSuspendedFlag(t *testing.T) {
	suspendedK := makeKustomization("flux-system", "apps", metav1.ConditionTrue, "ReconciliationSucceeded")
	if err := unstructured.SetNestedField(suspendedK.Object, true, "spec", "suspend"); err != nil {
		t.Fatalf("set suspend on Kustomization: %v", err)
	}
	activeK := makeKustomization("flux-system", "infra", metav1.ConditionTrue, "ReconciliationSucceeded")

	suspendedCron := makeCronJob("openova-system", "openbao-snapshot-save")
	if err := unstructured.SetNestedField(suspendedCron.Object, true, "spec", "suspend"); err != nil {
		t.Fatalf("set suspend on CronJob: %v", err)
	}
	activeCron := makeCronJob("openova-system", "cert-rotate")

	dyn := reconcilerFakeClient(suspendedK, activeK, suspendedCron, activeCron)

	obs, err := ListReconcilerObservations(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}

	if k := findReconcile(obs, "apps"); k == nil {
		t.Fatal("no reconcile observation for the suspended Kustomization")
	} else if !k.Suspended {
		t.Errorf("suspended Kustomization must carry Suspended=true")
	}
	if k := findReconcile(obs, "infra"); k == nil {
		t.Fatal("no reconcile observation for the active Kustomization")
	} else if k.Suspended {
		t.Errorf("active Kustomization must carry Suspended=false")
	}

	if c := findCron(obs, "openbao-snapshot-save"); c == nil {
		t.Fatal("no cron observation for the suspended CronJob")
	} else if !c.Suspended {
		t.Errorf("suspended CronJob must carry Suspended=true")
	}
	if c := findCron(obs, "cert-rotate"); c == nil {
		t.Fatal("no cron observation for the active CronJob")
	} else if c.Suspended {
		t.Errorf("active CronJob must carry Suspended=false")
	}
}
