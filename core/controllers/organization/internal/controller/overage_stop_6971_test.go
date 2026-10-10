// overage_stop_6971_test.go — the BSS spend-limit stop reaches the committed
// quotas (#6971, products/chargeback DESIGN.md §22.11).
//
// BSS stamps spec.overageStoppedUntil (through the sovereign-admin API's
// ServiceAccount route) when a grow Organization's accrued usage above its
// package reaches its monthly spend limit. Until that instant the controller
// renders the Organization capped — the headline on the host plan-quota
// limits and the package disk on the in-vCluster app-storage quota — and it
// requeues AT the instant so grow returns at the period start whether or not
// BSS has cleared the field.
//
// The watch: SetupWithManager registers For(&Organization{}) with no event
// filter, so every update of the CR enqueues a reconcile; a merge patch of
// spec.overageStoppedUntil also bumps metadata.generation, so a
// generation-based filter added later would still see it.
package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

const (
	rqCommitKey      = "acme/catalyst-tenant/vcluster/resourcequota.yaml"
	storageCommitKey = "acme/catalyst-tenant/vcluster/apps/storagequota.yaml"
)

func stopOrg(until string) *orgapi.Organization {
	org := sampleOrg() // plan m
	org.Spec.Commerce = &orgapi.OrganizationCommerce{PackageSKU: "plan.m", OverageMode: "grow", SpendLimitMonth: "3.000"}
	org.Spec.OverageStoppedUntil = until
	org.Spec.OverageStopReason = "spend limit 3.000 OMR reached for 2026-10"
	return org
}

func TestReconcile_SpendStopRendersCappedAndRequeuesAtTheInstant(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	run := func(org *orgapi.Organization) (string, string, time.Duration) {
		r, gs, _ := makeReconciler(t, org,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
			readyVClusterHR("acme"),
			boundaryLimitRange("acme"),
			boundaryResourceQuota("acme"),
		)
		r.Now = func() time.Time { return now }
		res, _ := reconcileToStatus(t, r, "acme")
		rq, ok := gs.files[rqCommitKey]
		if !ok {
			t.Fatalf("no %s committed", rqCommitKey)
		}
		sq, ok := gs.files[storageCommitKey]
		if !ok {
			t.Fatalf("no %s committed", storageCommitKey)
		}
		return string(rq.content), string(sq.content), res.RequeueAfter
	}

	// In force: capped figures (plan M: limits 2 + 1500m + 4550m = 8050m,
	// 4Gi + 1194Mi + 7168Mi = 12458Mi; disk 50Gi), the stop stamped on both
	// quotas, and the requeue lands one second after 2026-11-01T00:00Z —
	// 11 days 12 hours from now.
	rq, sq, after := run(stopOrg("2026-11-01T00:00:00Z"))
	for _, want := range []string{`openova.io/overage-mode: "capped"`, `limits.cpu: "8050m"`, `limits.memory: "12458Mi"`,
		`openova.io/overage-stopped-until: "2026-11-01T00:00:00Z"`} {
		if !strings.Contains(rq, want) {
			t.Errorf("stopped plan-quota lacks %s:\n%s", want, rq)
		}
	}
	if strings.Contains(rq, "grow-ceiling") {
		t.Errorf("stopped plan-quota still carries the grow ceiling:\n%s", rq)
	}
	for _, want := range []string{`requests.storage: "50Gi"`, `openova.io/overage-stopped-until: "2026-11-01T00:00:00Z"`, `openova.io/overage-mode: "capped"`} {
		if !strings.Contains(sq, want) {
			t.Errorf("stopped app-storage quota lacks %s:\n%s", want, sq)
		}
	}
	if want := 11*24*time.Hour + 12*time.Hour + time.Second; after != want {
		t.Errorf("RequeueAfter = %v, want %v (the instant the stop lifts, plus a second)", after, want)
	}

	// Lifted (the instant has passed, BSS has not cleared the field yet):
	// grow again — the XL ceiling 8 + overheads = 14050m, disk 250Gi — and
	// no stop-driven requeue.
	rq, sq, after = run(stopOrg("2026-10-01T00:00:00Z"))
	for _, want := range []string{`openova.io/overage-mode: "grow"`, `limits.cpu: "14050m"`} {
		if !strings.Contains(rq, want) {
			t.Errorf("lifted plan-quota lacks %s:\n%s", want, rq)
		}
	}
	if strings.Contains(rq, "overage-stopped-until") || strings.Contains(sq, "overage-stopped-until") {
		t.Errorf("a lifted stop is still stamped:\n%s\n%s", rq, sq)
	}
	if !strings.Contains(sq, `requests.storage: "250Gi"`) {
		t.Errorf("lifted app-storage quota is not the grow disk ceiling:\n%s", sq)
	}
	if after != 0 {
		t.Errorf("a lifted stop must not requeue a Ready Org, got %v", after)
	}

	// A stop on a CAPPED Organization is nothing to stop: capped as always,
	// unstamped, no requeue.
	capped := stopOrg("2026-11-01T00:00:00Z")
	capped.Spec.Commerce.OverageMode = "capped"
	rq, _, after = run(capped)
	if strings.Contains(rq, "overage-stopped-until") || after != 0 {
		t.Errorf("a stop on a capped Org must change nothing, got requeue %v:\n%s", after, rq)
	}
}

// Flexi retired: the CR's planSlug flexi reconciles as S in grow mode — the S
// requests, the XL grow ceiling on the limits, the XL disk on app-storage.
func TestReconcile_RetiredFlexiReconcilesAsSGrow(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.PlanSlug = "flexi"
	r, gs, _ := makeReconciler(t, org)
	if _, err := r.Reconcile(context.Background(), reqFor("acme")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	rq, ok := gs.files[rqCommitKey]
	if !ok {
		t.Fatal("a retired-flexi Org committed no ResourceQuota — it must carry the S grow quota")
	}
	for _, want := range []string{`openova.io/plan: s`, `openova.io/overage-mode: "grow"`, `openova.io/grow-ceiling: "cpu=8 memory=16Gi"`} {
		if !strings.Contains(string(rq.content), want) {
			t.Errorf("flexi plan-quota lacks %s:\n%s", want, rq.content)
		}
	}
	sq, ok := gs.files[storageCommitKey]
	if !ok || !strings.Contains(string(sq.content), `requests.storage: "250Gi"`) || !strings.Contains(string(sq.content), `openova.io/retired-plan: "flexi"`) {
		t.Errorf("flexi app-storage quota = %q, want 250Gi and the retired-plan stamp", sq.content)
	}
}

func reqFor(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}
}
