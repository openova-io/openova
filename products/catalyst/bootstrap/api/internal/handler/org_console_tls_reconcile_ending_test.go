package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// TestReconcileOrgConsoleTLSFromOrgCRs_NeverReAddsAnEndingOrganization: the
// 5-minute pass re-ensured the console listeners, certificate and route of
// every Organization CR, including one marked for deletion, so it re-added
// what the organization-controller's removal had just taken away (on the
// two-region environment, 22 November 2026, it re-ensured both cancelled
// Organizations every 5 minutes). An Organization that is ending gets nothing
// in either region; a live one with a similar name keeps its surface.
func TestReconcileOrgConsoleTLSFromOrgCRs_NeverReAddsAnEndingOrganization(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")

	ending := funnelOrgCR("g01-cel26uza", "omani.rest")
	now := metav1.Now()
	ending.SetDeletionTimestamp(&now)
	ending.SetFinalizers([]string{"orgs.openova.io/tenant-networking"})
	live := funnelOrgCR("g01-cel26uz", "omani.rest")

	hostDyn := fakeDynForOrgConsoleReconcile(t, ending, live)
	secDyn := fakeDynForOrgConsoleReconcile(t)
	hostCore := k8sfake.NewSimpleClientset(
		issuedOrgWildcardSecret("org-wildcard-tls-g01-cel26uza-omani-rest"),
		issuedOrgWildcardSecret("org-wildcard-tls-g01-cel26uz-omani-rest"),
	)
	secCore := k8sfake.NewSimpleClientset()

	h := newReconcileHandler(t, hostDyn, secDyn, hostCore, secCore)
	h.reconcileOrgConsoleTLSOnce(context.Background())

	for region, dyn := range map[string]*dynamicfake.FakeDynamicClient{"region-a-host": hostDyn, "region-b-secondary": secDyn} {
		if listener, route := consoleSurfacePresent(t, dyn, "console.g01-cel26uza.omani.rest"); listener || route != "" {
			t.Errorf("[%s] the pass re-added the console surface of an Organization marked for deletion (listener=%v route=%q)", region, listener, route)
		}
		if listener, route := consoleSurfacePresent(t, dyn, "console.g01-cel26uz.omani.rest"); !listener || route == "" {
			t.Errorf("[%s] the live Organization with a similar name lost its console surface (listener=%v route=%q)", region, listener, route)
		}
	}
}
