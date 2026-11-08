// application_controller_serving_d7_test.go — ADR-0015 D7: the Application is
// the status. Ready = HR Ready ∧ backing Ready ∧ Serving (functional probe),
// reason/message of the first failing input verbatim, Unknown stays Unknown,
// lastTransitionTime moves only on a real status change.
package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeProber struct {
	code  int
	err   error
	calls []ServingProbe
}

func (f *fakeProber) Probe(_ context.Context, p ServingProbe) (int, error) {
	f.calls = append(f.calls, p)
	return f.code, f.err
}

func d7Cond(t *testing.T, conds []interface{}, typ string) map[string]interface{} {
	t.Helper()
	for _, c := range conds {
		if cm, ok := c.(map[string]interface{}); ok && cm["type"] == typ {
			return cm
		}
	}
	t.Fatalf("condition %q absent from %v", typ, conds)
	return nil
}

func d7AppConds(t *testing.T, app *unstructured.Unstructured) []interface{} {
	t.Helper()
	conds, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	return conds
}

func d7RegionServing(t *testing.T, app *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	regions, _, _ := unstructured.NestedSlice(app.Object, "status", "regions")
	if len(regions) == 0 {
		t.Fatalf("status.regions empty")
	}
	rm := regions[0].(map[string]interface{})
	conds, _ := rm["conditions"].([]interface{})
	return d7Cond(t, conds, ConditionServing)
}

// gatewayRoute builds the live acmewalk307 shape: an HTTPRoute labelled with
// the HR's instance, parented on kube-system/cilium-gateway-console, and that
// gateway's Service (ports 8080/8443).
func gatewayRoute(ns, hr, host string) []*unstructured.Unstructured {
	rt := &unstructured.Unstructured{}
	rt.SetAPIVersion("gateway.networking.k8s.io/v1")
	rt.SetKind("HTTPRoute")
	rt.SetNamespace(ns)
	rt.SetName(hr)
	rt.SetLabels(map[string]string{InstanceLabel: hr})
	rt.Object["spec"] = map[string]interface{}{
		"hostnames": []interface{}{host},
		"parentRefs": []interface{}{map[string]interface{}{
			"group": "gateway.networking.k8s.io", "kind": "Gateway",
			"name": "cilium-gateway-console", "namespace": "kube-system",
		}},
	}
	svc := &unstructured.Unstructured{}
	svc.SetAPIVersion("v1")
	svc.SetKind("Service")
	svc.SetNamespace("kube-system")
	svc.SetName("cilium-gateway-cilium-gateway-console")
	svc.Object["spec"] = map[string]interface{}{
		"ports": []interface{}{
			map[string]interface{}{"name": "port-8080", "port": int64(8080)},
			map[string]interface{}{"name": "port-8443", "port": int64(8443)},
		},
	}
	return []*unstructured.Unstructured{rt, svc}
}

// TestD7_BlockedSyncWordPress_ServingFalse_Degraded — the hw307
// acmewalk307/wordpress-db shape: the primary is Ready=True (CNPG says
// "Cluster in healthy state"), but it declares dataDurability=required with
// one synchronous standby (wordpress-db-replica) that has sat in
// "Setting up primary" (Ready=False) for 58 days, so every commit blocks.
// Before D7 the Application reported Ready=True.
func TestD7_BlockedSyncWordPress_ServingFalse_Degraded(t *testing.T) {
	primary := makeCNPGClusterWithStatus("mgmt", "wordpress-db", "obs-mgmt-a", map[string]interface{}{
		"phase": "Cluster in healthy state", "instances": int64(1), "readyInstances": int64(1),
		"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "reason": "ClusterIsReady", "message": "Cluster is Ready"}},
	})
	primary.Object["spec"] = map[string]interface{}{
		"instances": int64(1),
		"postgresql": map[string]interface{}{"synchronous": map[string]interface{}{
			"dataDurability": "required", "method": "first", "number": int64(1),
			"maxStandbyNamesFromCluster": int64(0),
			"standbyNamesPre":            []interface{}{"wordpress-db-replica"},
		}},
	}
	// The standby half (no instance label — in the D2 layout it belongs to
	// the other region's render, so the backing gate does not see it).
	replica := makeCNPGClusterWithStatus("mgmt", "wordpress-db-replica", "other", map[string]interface{}{
		"phase": "Setting up primary", "instances": int64(1),
		"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "False", "reason": "ClusterIsNotReady", "message": "Cluster Is Not Ready"}},
	})
	replica.SetLabels(nil)
	replica.Object["spec"] = map[string]interface{}{"instances": int64(1), "replica": map[string]interface{}{"enabled": true, "source": "wordpress-db"}}

	r := newAppOverCNPG(t, primary, replica)
	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")

	phase, reason, msg := readPhaseAndReason(t, got)
	if phase != PhaseDegraded {
		t.Fatalf("phase = %q, want Degraded (msg=%q)", phase, msg)
	}
	if st := readyConditionStatus(t, got); st != "False" {
		t.Fatalf("Ready = %q, want False", st)
	}
	if reason != ReasonSynchronousStandbyMissing {
		t.Fatalf("Ready reason = %q, want %q", reason, ReasonSynchronousStandbyMissing)
	}
	for _, want := range []string{"mgmt/wordpress-db", "dataDurability=required", "wordpress-db-replica", "Cluster Is Not Ready", "every commit blocks"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Ready message %q does not carry %q verbatim", msg, want)
		}
	}
	serving := d7Cond(t, d7AppConds(t, got), ConditionServing)
	if serving["status"] != "False" || serving["reason"] != ReasonSynchronousStandbyMissing {
		t.Fatalf("Serving = %v, want False/%s", serving, ReasonSynchronousStandbyMissing)
	}
	if rs := d7RegionServing(t, got); rs["status"] != "False" {
		t.Fatalf("per-region Serving = %v, want False", rs)
	}
}

// TestD7_SyncStandbyStreaming_Serving — control: the same primary with its
// standby Ready reaches Ready (no over-rotation).
func TestD7_SyncStandbyStreaming_Serving(t *testing.T) {
	primary := makeCNPGClusterWithStatus("mgmt", "wordpress-db", "obs-mgmt-a", map[string]interface{}{
		"instances": int64(1), "readyInstances": int64(1),
		"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
	})
	primary.Object["spec"] = map[string]interface{}{"postgresql": map[string]interface{}{"synchronous": map[string]interface{}{
		"dataDurability": "required", "number": int64(1), "standbyNamesPre": []interface{}{"wordpress-db-replica"},
	}}}
	replica := makeCNPGClusterWithStatus("mgmt", "wordpress-db-replica", "other", map[string]interface{}{
		"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
	})
	replica.SetLabels(nil)
	r := newAppOverCNPG(t, primary, replica)
	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")
	if st := readyConditionStatus(t, got); st != "True" {
		_, reason, msg := readPhaseAndReason(t, got)
		t.Fatalf("Ready = %q (%s: %s), want True", st, reason, msg)
	}
	if s := d7Cond(t, d7AppConds(t, got), ConditionServing); s["status"] != "True" {
		t.Fatalf("Serving = %v, want True", s)
	}
}

// TestD7_HTTPProbe_503_ServingFalse and _200_Serving — the endpoint probe
// goes through the region gateway Service with Host = route hostname.
func TestD7_HTTPProbe_503_ServingFalse(t *testing.T) {
	r := newAppOverCNPG(t, gatewayRoute("mgmt", "obs-mgmt-a", "obs.acme.omani.homes")...)
	fp := &fakeProber{code: 503}
	r.Prober = fp
	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase != PhaseDegraded || reason != ReasonEndpointNotServing {
		t.Fatalf("phase/reason = %q/%q, want Degraded/%s (msg=%q)", phase, reason, ReasonEndpointNotServing, msg)
	}
	if !strings.Contains(msg, "HTTP 503") || !strings.Contains(msg, "obs.acme.omani.homes") {
		t.Errorf("message %q must name the host and the status verbatim", msg)
	}
	if len(fp.calls) == 0 {
		t.Fatalf("prober never called")
	}
	c := fp.calls[0]
	if c.Addr != "cilium-gateway-cilium-gateway-console.kube-system.svc.cluster.local:8443" || !c.TLS || c.Host != "obs.acme.omani.homes" || c.Path != "/" {
		t.Fatalf("probe target = %+v, want TLS via the gateway Service :8443 with Host=route host, path /", c)
	}
}

func TestD7_HTTPProbe_TransportError_ServingFalse(t *testing.T) {
	r := newAppOverCNPG(t, gatewayRoute("mgmt", "obs-mgmt-a", "obs.acme.omani.homes")...)
	r.Prober = &fakeProber{err: errors.New("dial tcp: i/o timeout")}
	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")
	if s := d7Cond(t, d7AppConds(t, got), ConditionServing); s["status"] != "False" || !strings.Contains(s["message"].(string), "i/o timeout") {
		t.Fatalf("Serving = %v, want False carrying the transport error", s)
	}
}

func TestD7_HTTPProbe_200_Serving(t *testing.T) {
	r := newAppOverCNPG(t, gatewayRoute("mgmt", "obs-mgmt-a", "obs.acme.omani.homes")...)
	r.Prober = &fakeProber{code: 302}
	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")
	if st := readyConditionStatus(t, got); st != "True" {
		t.Fatalf("Ready = %q, want True (a 302 to the login page is serving)", st)
	}
	if s := d7Cond(t, d7AppConds(t, got), ConditionServing); s["status"] != "True" {
		t.Fatalf("Serving = %v, want True", s)
	}
	if rs := d7RegionServing(t, got); rs["status"] != "True" {
		t.Fatalf("per-region Serving = %v, want True", rs)
	}
}

// TestD7_NoProbeDeclared_DoesNotGateReady — an app with no route and no
// sync-required database publishes Serving=Unknown/NoFunctionalProbeDeclared
// and still reaches Ready (documented open item).
func TestD7_NoProbeDeclared_DoesNotGateReady(t *testing.T) {
	r := newAppOverCNPG(t)
	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")
	if st := readyConditionStatus(t, got); st != "True" {
		t.Fatalf("Ready = %q, want True", st)
	}
	s := d7Cond(t, d7AppConds(t, got), ConditionServing)
	if s["status"] != "Unknown" || s["reason"] != ReasonNoFunctionalProbe {
		t.Fatalf("Serving = %v, want Unknown/%s", s, ReasonNoFunctionalProbe)
	}
}

// TestD7_HRNotReady_ReadyUnknownNotTrue — Provisioning used to publish
// Ready=True ("we did our part"). Not-yet-known is Unknown.
func TestD7_HRNotReady_ReadyUnknownNotTrue(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology("bp-postgres", "0.2.6", "mgmt", []string{"mgmt-A"})
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6", "single-region", []string{"hetzner-fsn-rtz-prod"}, nil)
	hr := readyHR("obs-mgmt-a", "mgmt", "Unknown")
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, makeEnv("acme-prod", "acme", "prod"), makeOrg("acme"), bp, hr)
	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")
	_, reason, _ := readPhaseAndReason(t, got)
	if st := readyConditionStatus(t, got); st != "Unknown" || reason != ReasonProgressing {
		t.Fatalf("Ready = %q/%q, want Unknown/%s", st, reason, ReasonProgressing)
	}
}

// TestD7_EnvironmentMissing_VerbatimMessage — the live sovereign/test shape:
// Ready=False EnvironmentMissing 'Environment "sovereign-prod" not found'.
func TestD7_EnvironmentMissing_VerbatimMessage(t *testing.T) {
	app := makeApp("sovereign", "test", "sovereign-prod", "bp-wordpress", "1.0.0", "single-region", []string{"hetzner-fsn-rtz-prod"}, nil)
	r := newReconciler(t, newFakeGitea(), app)
	reconcileFromCluster(t, r, "sovereign", "test")
	got := readApp(t, r, "sovereign", "test")
	ready := d7Cond(t, d7AppConds(t, got), "Ready")
	if ready["status"] != "False" || ready["reason"] != ReasonEnvironmentMissing || ready["message"] != `Environment "sovereign-prod" not found` {
		t.Fatalf("Ready = %v, want False/EnvironmentMissing/`Environment \"sovereign-prod\" not found`", ready)
	}
}

// TestD7_LastTransitionTime_OnlyOnStatusChange — a second pass with the same
// verdict keeps every lastTransitionTime (Ready, Serving, per-region Serving,
// region row); a status change restamps only what changed.
func TestD7_LastTransitionTime_OnlyOnStatusChange(t *testing.T) {
	r := newAppOverCNPG(t, gatewayRoute("mgmt", "obs-mgmt-a", "obs.acme.omani.homes")...)
	fp := &fakeProber{code: 200}
	r.Prober = fp
	reconcileFromCluster(t, r, "acme", "obs")

	// Age every timestamp so a restamp is detectable at 1s resolution.
	const old = "2020-01-01T00:00:00Z"
	ctx := context.Background()
	cur := readApp(t, r, "acme", "obs")
	conds := d7AppConds(t, cur) // NestedSlice deep-copies: write it back
	for _, c := range conds {
		c.(map[string]interface{})["lastTransitionTime"] = old
	}
	_ = unstructured.SetNestedSlice(cur.Object, conds, "status", "conditions")
	regions, _, _ := unstructured.NestedSlice(cur.Object, "status", "regions")
	for _, rg := range regions {
		rm := rg.(map[string]interface{})
		rm["lastTransitionTime"] = old
		for _, c := range rm["conditions"].([]interface{}) {
			c.(map[string]interface{})["lastTransitionTime"] = old
		}
	}
	_ = unstructured.SetNestedSlice(cur.Object, regions, "status", "regions")
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("age status: %v", err)
	}

	reconcileFromCluster(t, r, "acme", "obs")
	got := readApp(t, r, "acme", "obs")
	for _, typ := range []string{"Ready", ConditionServing} {
		if ltt := d7Cond(t, d7AppConds(t, got), typ)["lastTransitionTime"]; ltt != old {
			t.Errorf("%s lastTransitionTime = %v, want unchanged %s", typ, ltt, old)
		}
	}
	if ltt := d7RegionServing(t, got)["lastTransitionTime"]; ltt != old {
		t.Errorf("per-region Serving lastTransitionTime = %v, want unchanged", ltt)
	}
	regions, _, _ = unstructured.NestedSlice(got.Object, "status", "regions")
	if ltt := regions[0].(map[string]interface{})["lastTransitionTime"]; ltt != old {
		t.Errorf("region row lastTransitionTime = %v, want unchanged", ltt)
	}

	// Status change: the endpoint starts failing.
	fp.code = 503
	reconcileFromCluster(t, r, "acme", "obs")
	got = readApp(t, r, "acme", "obs")
	for _, typ := range []string{"Ready", ConditionServing} {
		if ltt := d7Cond(t, d7AppConds(t, got), typ)["lastTransitionTime"]; ltt == old {
			t.Errorf("%s lastTransitionTime not restamped on a True→False change", typ)
		}
	}
}
