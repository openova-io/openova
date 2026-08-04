// provisioning_postconditions_5395_test.go — #5395.
//
// The live failure these tests lock down (hw290): Organization `gamma-corp`
// reported Ready/Active on every status surface while all six of its HTTPRoutes
// sat `Accepted=False reason=NoMatchingListenerHostname` — the
// `*.gamma-corp.omani.homes` listener pair was absent from the shared
// `cilium-gateway-console` Gateway. Separately its boundary namespace carried
// no `plan-quota` ResourceQuota. Two artifacts the org-controller authors were
// missing and NOTHING surfaced an error, because Ready derived from the
// vCluster HR / namespace readback alone.
//
// NEGATIVE PROOF (how to confirm these tests are not theater): delete the
// `postconditions` block from Reconcile (organization_controller.go step 6b) —
// TestReconcile_5395_ConsoleListenerMissing_MustNotReadReady and
// TestReconcile_5395_HardCappedPlanWithoutQuota_MustNotReadReady both fail with
// `phase="Ready"` / `Ready=True`, i.e. they reproduce the exact hw290 shape.
// Reverting just the `vcPhase = "Provisioning"` line leaves the condition red
// but the phase green, which is what the console actually renders — the
// PhaseAlsoHeldBack test isolates that half.
package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// boundaryLimitRange / boundaryResourceQuota build the two boundary objects
// gitops.Render authors into the `<slug>` namespace, named off the SAME
// exported constants the renderer and the verifier use — so a rename cannot
// leave these fixtures silently asserting nothing.
func boundaryLimitRange(slug string) *corev1.LimitRange {
	return &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{
		Namespace: slug, Name: gitops.BoundaryLimitRangeName,
	}}
}

func boundaryResourceQuota(slug string) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{
		Namespace: slug, Name: gitops.BoundaryResourceQuotaName,
	}}
}

// readyVClusterHR builds the Ready vCluster HelmRelease a paid-tier Org needs
// before vclusterReadiness reports ready.
func readyVClusterHR(slug string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
	})
	hr.SetNamespace(slug)
	hr.SetName("vcluster")
	_ = unstructured.SetNestedSlice(hr.Object, []any{
		map[string]any{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	return hr
}

// consoleGateway builds the shared console Gateway carrying the apex listener
// plus whichever per-Org listener names the caller passes — the live shape on
// `kube-system/cilium-gateway-console`.
func consoleGateway(perOrgListenerNames ...string) *unstructured.Unstructured {
	listeners := []any{
		map[string]any{
			"name": "console-https", "hostname": "*.omantel.omani.works",
			"port": int64(31443), "protocol": "HTTPS",
		},
	}
	for _, n := range perOrgListenerNames {
		listeners = append(listeners, map[string]any{
			"name": n, "hostname": "*.acme.omani.homes",
			"port": int64(31443), "protocol": "HTTPS",
		})
	}
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetNamespace(consoleGatewayDefaultNamespace)
	gw.SetName(consoleGatewayDefaultName)
	_ = unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners")
	return gw
}

// poolOrg is a paid-tier Org that engages the tenant-networking up-path — the
// gamma-corp shape: a pool parentDomain, so it MUST get a per-Org console
// listener pair on the shared console Gateway.
func poolOrg() *orgapi.Organization {
	org := sampleOrg()
	org.Spec.TenantPublic.ParentDomain = "omani.homes"
	return org
}

// reconcileToStatus drives Reconcile until it has written a status, and returns
// that pass's result + the post-reconcile CR. An Org that engages the
// tenant-networking up-path spends its FIRST pass adding the finalizer and
// returning `{Requeue: true}` before any status is written, so a single pass
// would assert on an empty status block.
func reconcileToStatus(t *testing.T, r *Reconciler, slug string) (ctrl.Result, orgapi.Organization) {
	t.Helper()
	const maxPasses = 4
	var res ctrl.Result
	var got orgapi.Organization
	for i := 0; i < maxPasses; i++ {
		var err error
		res, err = r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: slug},
		})
		if err != nil {
			t.Fatalf("reconcile pass %d: %v", i+1, err)
		}
		if err := r.Get(context.Background(), client.ObjectKey{Name: slug}, &got); err != nil {
			t.Fatalf("get post-reconcile pass %d: %v", i+1, err)
		}
		if readyCondition(&got) != nil {
			return res, got
		}
	}
	t.Fatalf("no status written after %d reconcile passes", maxPasses)
	return res, got
}

// mustReadyCondition is readyCondition (perorg_repo_hotloop_5305_test.go) with
// a fatal on absence, so each assertion below can read the condition inline.
func mustReadyCondition(t *testing.T, org orgapi.Organization) orgapi.Condition {
	t.Helper()
	c := readyCondition(&org)
	if c == nil {
		t.Fatalf("no Ready condition on status: %+v", org.Status.Conditions)
	}
	return *c
}

// TestReconcile_5395_ConsoleListenerMissing_MustNotReadReady is the hw290
// gamma-corp reproduction. Everything vclusterReadiness looks at is green (ns
// Active + vCluster HR Ready) and the boundary tree landed — but the shared
// console Gateway carries the apex listener and ANOTHER Org's pair, never this
// Org's, because every append this Org attempts loses to a writer conflict.
// Every HTTPRoute on `*.acme.omani.homes` is therefore
// NoMatchingListenerHostname and the customer's console/mail/apps are
// unreachable. The Org MUST NOT read Ready, and MUST requeue.
func TestReconcile_5395_ConsoleListenerMissing_MustNotReadReady(t *testing.T) {
	t.Parallel()
	org := poolOrg()

	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		readyVClusterHR("acme"),
		boundaryLimitRange("acme"),
		boundaryResourceQuota("acme"),
		// A sibling Org's listeners are present; this Org's are not — exactly
		// the hw290 shape where six Orgs had theirs and gamma-corp did not.
		consoleGateway("console-https-otherorg", "console-http-otherorg"),
	)
	// Every append this Org attempts fails, so the listener never lands. Reads
	// pass through, so the verifier sees the Gateway exactly as an operator
	// would: apex + the sibling Org, no acme pair.
	r.Client = gatewayUpdateFails{Client: r.Client}

	res, got := reconcileToStatus(t, r, "acme")

	cond := mustReadyCondition(t, got)
	if cond.Status != "False" || cond.Reason != "ProvisioningIncomplete" {
		t.Errorf("an Org whose console listener pair is absent must NOT read Ready=True — got %+v", cond)
	}
	if !strings.Contains(cond.Message, "console Gateway listeners") {
		t.Errorf("Ready message must name the missing listener pair so the gap is diagnosable, got %q", cond.Message)
	}
	if !strings.Contains(cond.Message, "console-https-acme") {
		t.Errorf("Ready message must name the exact listener the up-path writes, got %q", cond.Message)
	}
	// The console derives the Org's Active badge from status.vcluster.phase
	// FIRST (org_list_from_cr.go orgStateFromCR) — a red condition under a
	// green phase would still render Active.
	if got.Status.VCluster.Phase == "Ready" {
		t.Errorf("status.vcluster.phase must not read Ready while the console edge is missing — the console maps phase=Ready to Active regardless of the condition")
	}
	if res.RequeueAfter == 0 {
		t.Errorf("a missing postcondition must keep the requeue alive so the listener is re-appended, got %v", res)
	}
}

// TestReconcile_5395_SilentListenerSkip_MustNotReadReady is the nastier half of
// the same hole, and the one nothing could have caught before this change:
// `ensureConsoleOrgListener` returns `(false, nil)` — INDISTINGUISHABLE FROM
// SUCCESS — when the console Gateway is not present (the bootstrap window
// before sovereign-tls applies it). So `reconcileConsoleServing` reports
// degraded=false, the Org goes Ready=True, Reconcile returns `ctrl.Result{}`
// with NO requeue, and `SetupWithManager` registers no Gateway watch — the Org
// never looks again. Silent, permanent, and green.
//
// Post-fix the Org must read ProvisioningIncomplete AND keep requeueing, which
// is what eventually re-runs the append once the Gateway appears.
func TestReconcile_5395_SilentListenerSkip_MustNotReadReady(t *testing.T) {
	t.Parallel()
	org := poolOrg()

	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		readyVClusterHR("acme"),
		boundaryLimitRange("acme"),
		boundaryResourceQuota("acme"),
		// No console Gateway at all — ensureConsoleOrgListener's NotFound
		// branch returns (false, nil) and reports no degradation whatsoever.
	)

	res, got := reconcileToStatus(t, r, "acme")

	cond := mustReadyCondition(t, got)
	if cond.Status != "False" || cond.Reason != "ProvisioningIncomplete" {
		t.Errorf("a silently-skipped listener append must NOT leave the Org reading Ready=True — got %+v", cond)
	}
	if got.Status.VCluster.Phase == "Ready" {
		t.Errorf("status.vcluster.phase must not read Ready while the console edge was never written")
	}
	if res.RequeueAfter == 0 {
		t.Errorf("the silent-skip path reports no degradation, so the postcondition is the ONLY thing that can keep the retry alive — got %v", res)
	}
}

// TestReconcile_5395_ListenerAppendedThenVerified — the converse: once the
// per-Org listener pair IS on the Gateway (the up-path's own append lands in
// this same pass), the Org reaches Ready=True and stops requeueing. Without
// this, the test above could be satisfied by a check that never goes green.
func TestReconcile_5395_ListenerAppendedThenVerified(t *testing.T) {
	t.Parallel()
	org := poolOrg()

	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		readyVClusterHR("acme"),
		boundaryLimitRange("acme"),
		boundaryResourceQuota("acme"),
		consoleGateway(),
	)

	// Drive reconcile until reconcileConsoleServing's append has actually landed
	// in spec (the first pass only adds the finalizer and requeues).
	const wantSpecListeners = 3 // apex + the per-Org pair
	appended := false
	for i := 0; i < 4 && !appended; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "acme"},
		}); err != nil {
			t.Fatalf("reconcile pass %d: %v", i+1, err)
		}
		if _, n := gatewayListeners(t, r, "spec"); n == wantSpecListeners {
			appended = true
		}
	}
	if !appended {
		t.Fatalf("the up-path never appended the per-Org listener pair to spec")
	}

	// #5511: a live Gateway publishes `status.listeners`, and being in spec is
	// not being served. Simulate the Gateway controller admitting every listener
	// the append just wrote — without this the Org sits (correctly) UNVERIFIABLE
	// on an unpublished status and never reaches its steady state.
	specCount, statusCount := admitGatewayListeners(t, r)
	if specCount != wantSpecListeners || statusCount != specCount {
		t.Fatalf("admission fixture: spec=%d status=%d, want %d/%d", specCount, statusCount, wantSpecListeners, wantSpecListeners)
	}

	res, got := reconcileToStatus(t, r, "acme")

	cond := mustReadyCondition(t, got)
	if cond.Status != "True" || cond.Reason != "Reconciled" {
		t.Errorf("with the listener pair present the Org must reach Ready=True, got %+v", cond)
	}
	if got.Status.VCluster.Phase != "Ready" {
		t.Errorf("status.vcluster.phase: got %q want Ready", got.Status.VCluster.Phase)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a fully provisioned Org must stop requeueing, got %v", res)
	}

	// And the listeners really are on the Gateway — proving the green came from
	// delivered artifacts, not from a check that silently passes.
	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(context.Background(), client.ObjectKey{
		Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName,
	}, &gw); err != nil {
		t.Fatalf("get console gateway: %v", err)
	}
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	names := map[string]bool{}
	for _, l := range listeners {
		if m, ok := l.(map[string]any); ok {
			n, _ := m["name"].(string)
			names[n] = true
		}
	}
	if !names["console-https-acme"] || !names["console-http-acme"] {
		t.Errorf("expected the per-Org listener pair on the Gateway, got %v", names)
	}
	if !names["console-https"] {
		t.Errorf("the apex listener must be preserved (regression guard), got %v", names)
	}
}

// TestReconcile_5395_HardCappedPlanWithoutQuota_MustNotReadReady — symptom B.
// A hard-capped plan (M) whose boundary namespace carries no `plan-quota`
// ResourceQuota is running WITHOUT the cap the customer paid for. It must not
// read Ready.
func TestReconcile_5395_HardCappedPlanWithoutQuota_MustNotReadReady(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // PlanSlug "m" — hard-capped

	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		readyVClusterHR("acme"),
		boundaryLimitRange("acme"),
		// No ResourceQuota.
	)

	res, got := reconcileToStatus(t, r, "acme")

	cond := mustReadyCondition(t, got)
	if cond.Status != "False" || cond.Reason != "ProvisioningIncomplete" {
		t.Errorf("a hard-capped Org with no ResourceQuota must NOT read Ready=True — got %+v", cond)
	}
	if !strings.Contains(cond.Message, gitops.BoundaryResourceQuotaName) {
		t.Errorf("Ready message must name the missing ResourceQuota, got %q", cond.Message)
	}
	if got.Status.VCluster.Phase == "Ready" {
		t.Errorf("status.vcluster.phase must not read Ready while the plan cap is absent")
	}
	if res.RequeueAfter == 0 {
		t.Errorf("a missing quota must keep the requeue alive, got %v", res)
	}
}

// TestReconcile_5395_FlexiWithoutQuotaIsNotAGap — the disambiguation that makes
// "no ResourceQuota" a DECIDED outcome. Flexi is the on-demand soft-cap plan;
// gitops.Render deliberately emits no ResourceQuota for it, so a quota-less
// Flexi namespace is correct and the Org must stay green. The LimitRange —
// which Render emits for EVERY plan — is what still has to be there.
func TestReconcile_5395_FlexiWithoutQuotaIsNotAGap(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.PlanSlug = "flexi"

	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		readyVClusterHR("acme"),
		boundaryLimitRange("acme"),
		// No ResourceQuota — correct for Flexi.
	)

	res, got := reconcileToStatus(t, r, "acme")

	cond := mustReadyCondition(t, got)
	if cond.Status != "True" {
		t.Errorf("a Flexi Org renders no ResourceQuota by design — it must still read Ready, got %+v", cond)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a fully provisioned Flexi Org must stop requeueing, got %v", res)
	}

	// Same Org, LimitRange also gone → now it IS a delivery gap, because the
	// LimitRange has no per-plan gate. This asymmetry is the whole diagnostic:
	// quota absent alone == Flexi; both absent == the boundary tree never landed.
	org2 := sampleOrg()
	org2.Spec.PlanSlug = "flexi"
	r2, _, _ := makeReconciler(t, org2,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		readyVClusterHR("acme"),
	)
	_, got2 := reconcileToStatus(t, r2, "acme")
	cond2 := mustReadyCondition(t, got2)
	if cond2.Status != "False" || cond2.Reason != "ProvisioningIncomplete" {
		t.Errorf("a Flexi Org missing the always-rendered LimitRange IS a delivery gap and must not read Ready, got %+v", cond2)
	}
	if !strings.Contains(cond2.Message, gitops.BoundaryLimitRangeName) {
		t.Errorf("Ready message must name the missing LimitRange, got %q", cond2.Message)
	}
	if strings.Contains(cond2.Message, gitops.BoundaryResourceQuotaName) {
		t.Errorf("a Flexi Org must NEVER be reported as missing a ResourceQuota it is not supposed to have, got %q", cond2.Message)
	}
}

// TestVerifyProvisioned_UnreadableProbeIsNotEvidenceOfAbsence — a read error
// that is NOT NotFound (RBAC 403 mid-rollout, apiserver blip, absent CRD) must
// land in Unverifiable, never in Missing. Otherwise one botched RBAC rollout
// would red-flag every Organization on the Sovereign simultaneously.
func TestVerifyProvisioned_UnreadableProbeIsNotEvidenceOfAbsence(t *testing.T) {
	t.Parallel()
	org := poolOrg()
	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		boundaryLimitRange("acme"),
		boundaryResourceQuota("acme"),
		consoleGateway("console-https-acme", "console-http-acme"),
	)
	// Wrap the client so every Gateway read fails with a non-NotFound error.
	r.Client = gatewayReadFails{Client: r.Client}

	out := r.verifyProvisioned(context.Background(), org)
	if len(out.Missing) != 0 {
		t.Errorf("a failed (non-NotFound) probe must NOT be reported as a missing artifact, got %v", out.Missing)
	}
	if len(out.Unverifiable) != 1 {
		t.Fatalf("expected exactly 1 unverifiable probe, got %v", out.Unverifiable)
	}
	if !out.complete() {
		t.Errorf("an unverifiable probe must not fail the postcondition set (it is not evidence of absence)")
	}
}

// TestVerifyProvisioned_NoParentDomainSkipsConsoleCheck — an Org with no pool
// parentDomain is reached via the Sovereign-wide `*.<sovFQDN>` wildcard and
// authors no per-Org listener, so demanding one would be a false red.
func TestVerifyProvisioned_NoParentDomainSkipsConsoleCheck(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // no tenantPublic.parentDomain
	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		boundaryLimitRange("acme"),
		boundaryResourceQuota("acme"),
		// No console Gateway at all.
	)

	out := r.verifyProvisioned(context.Background(), org)
	if !out.complete() {
		t.Errorf("an Org with no pool parentDomain must not be held back on a console listener it never authors, got %v", out.Missing)
	}
}

// gatewayReadFails makes every Gateway Get return a non-NotFound error while
// passing every other read through untouched.
type gatewayReadFails struct{ client.Client }

func (g gatewayReadFails) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == gatewayGVK {
		return errGatewayUnreadable
	}
	return g.Client.Get(ctx, key, obj, opts...)
}

// gatewayUpdateFails makes every Gateway Update fail (the shared-Gateway writer
// conflict a per-Org listener append loses) while leaving reads — and every
// other object's writes — untouched.
type gatewayUpdateFails struct{ client.Client }

func (g gatewayUpdateFails) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == gatewayGVK {
		return errGatewayConflict
	}
	return g.Client.Update(ctx, obj, opts...)
}

var (
	errGatewayUnreadable = &probeError{"gateways.gateway.networking.k8s.io is forbidden: RBAC not yet rolled out"}
	errGatewayConflict   = &probeError{"Operation cannot be fulfilled on gateways.gateway.networking.k8s.io: the object has been modified"}
)

type probeError struct{ msg string }

func (e *probeError) Error() string { return e.msg }
