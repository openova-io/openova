// tenant_console_tls_5511_test.go — #5511. The per-Org console listener pair
// must ride the ports the console LoadBalancer actually forwards to, and a
// listener the Gateway silently declines must never read as provisioned.
//
// THE DEFECT
// ----------
// Measured live on hw291: `Gateway/kube-system/cilium-gateway-console` declared
// 8 listeners and published 6 in `.status.listeners`. The two absent ones were
// exactly the per-Org pair `console-https-uatcorp` / `console-http-uatcorp`.
// No error, no condition, no event. Consequences, on independent fresh-TCP
// probes:
//
//	https://agenity.uatcorp.omani.homes/  -> 000  x6 of 6
//	https://console.uatcorp.omani.homes/  -> 000  x8 of 8
//	https://console.hw291.omantel.biz/    -> 200        (same VIP, control)
//
// The control matters: `agenity.uatcorp` was FULLY healthy behind that door —
// HelmRelease Ready=True, `bp-agenity-0` 1/1 Running, HTTPRoute present, DNS
// resolving to the console ELB — and still answered 000. Routing failed
// independently of workload health.
//
// ROOT CAUSE
// ----------
// `ensureConsoleOrgListener` appended the pair on the PRE-#4718 ports
// 31443/31080. The console LoadBalancer forwards public 443/80 to the live apex
// `console-https`/`console-http` listener ports and to nothing else, so a
// per-Org listener on any other port receives no traffic at all. The catalyst-api
// twin (`products/catalyst/bootstrap/api/internal/handler/org_console_tls.go`)
// was fixed for precisely this in #4732 item 2 (commit 8663202bc, verbatim: "the
// console ELB only forwards to the apex ports (8443/8080), so any other pair
// receives no traffic"), and its test bans the 31443/31080 literals outright.
// This twin never received the fix — so whichever of the two doors provisioned a
// given Org silently decided whether that Org's console would work. The two
// doors are supposed to be byte-identical (#4241).
//
// A second, independent bug made it permanent: the append short-circuited on
// listener-NAME presence, so a pair already sitting on the dead ports was never
// corrected. Fixing the constant alone would have repaired new Orgs and left
// every existing customer door at 000 forever.
//
// WHAT IS ASSERTED HERE
// ---------------------
// Part 1 — the ports are derived from the live apex pair, the dead literals can
// never be written again, and an existing wrong-port pair is HEALED in place.
// Part 2 — a listener present in `spec` and absent from a published `status`
// blocks Ready with a message naming the count mismatch. Part 2 is asserted in
// both directions: an EMPTY status is Unverifiable (a requeue, never a
// fabricated red), which is also what keeps the status check from passing
// vacuously on a Gateway that has published nothing at all.
package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

// consoleTLSReconciler builds a Reconciler with the Certificate + Gateway GVKs
// registered (the console-TLS up-path writes both as Unstructured) and seeds the
// console Gateway carrying the listeners passed in.
func consoleTLSReconciler(t *testing.T, org *orgapi.Organization, listeners []any) *Reconciler {
	t.Helper()
	r, _, _ := makeReconciler(t, org)
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, gatewayGVK} {
		r.Scheme().AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		r.Scheme().AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetName(consoleGatewayDefaultName)
	gw.SetNamespace(consoleGatewayDefaultNamespace)
	gw.Object["spec"] = map[string]any{"gatewayClassName": "cilium"}
	if err := unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners"); err != nil {
		t.Fatalf("seed listeners: %v", err)
	}
	if err := r.Create(context.Background(), gw); err != nil {
		t.Fatalf("seed console Gateway: %v", err)
	}
	return r
}

// apexPair is the bootstrap-rendered console listener set: one exact-hostname
// pair per operator endpoint on the ports the console LB forwards to. Shape and
// ports come from platform/sovereign-tls-vars/chart/templates/configmap.yaml
// (consoleGateway.httpsPort=443 / httpPort=80).
func apexPair() []any {
	return []any{
		map[string]any{
			"name": consoleApexListenerHTTPSName, "hostname": "console.hw291.omantel.biz",
			"port": int64(443), "protocol": "HTTPS",
		},
		map[string]any{
			"name": consoleApexListenerHTTPName, "hostname": "console.hw291.omantel.biz",
			"port": int64(80), "protocol": "HTTP",
		},
	}
}

// deadPortPair is the live hw291 shape: this reconciler's own pre-fix append,
// sitting on the dead pre-#4718 ports while the apex pair serves 443/80.
func deadPortPair(hostname string) []any {
	return []any{
		map[string]any{
			"name": "console-https-acme", "hostname": hostname,
			"port": int64(31443), "protocol": "HTTPS",
			"tls": map[string]any{
				"mode":            "Terminate",
				"certificateRefs": []any{map[string]any{"group": "", "kind": "Secret", "name": "org-wildcard-tls-acme-omani-homes"}},
			},
		},
		map[string]any{
			"name": "console-http-acme", "hostname": hostname,
			"port": int64(31080), "protocol": "HTTP",
		},
	}
}

// gatewayListeners reads back one of the console Gateway's listener slices
// ("spec" or "status") as a name→port map, plus the raw count.
func gatewayListeners(t *testing.T, r *Reconciler, where string) (map[string]int64, int) {
	t.Helper()
	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(context.Background(), client.ObjectKey{
		Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName,
	}, &gw); err != nil {
		t.Fatalf("get console Gateway: %v", err)
	}
	raw, _, err := unstructured.NestedSlice(gw.Object, where, "listeners")
	if err != nil {
		t.Fatalf("read %s.listeners: %v", where, err)
	}
	out := map[string]int64{}
	for _, l := range raw {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		n, _ := m["name"].(string)
		p, _ := listenerPort(m)
		out[n] = p
	}
	return out, len(raw)
}

// admitGatewayListeners simulates the Gateway controller's admission step: it
// copies the console Gateway's spec.listeners into status.listeners, DECLINING
// the names passed in `decline` — which is exactly the #5511 shape, a spec
// listener that is silently absent from status with no condition and no event.
// Returns the resulting (specCount, statusCount).
func admitGatewayListeners(t *testing.T, r *Reconciler, decline ...string) (int, int) {
	t.Helper()
	declined := map[string]bool{}
	for _, n := range decline {
		declined[n] = true
	}
	gw := unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := r.Get(context.Background(), client.ObjectKey{
		Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName,
	}, &gw); err != nil {
		t.Fatalf("get console Gateway for admission: %v", err)
	}
	spec, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read spec.listeners: %v", err)
	}
	status := make([]any, 0, len(spec))
	for _, l := range spec {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		n, _ := m["name"].(string)
		if declined[n] {
			continue
		}
		status = append(status, map[string]any{
			"name":           n,
			"attachedRoutes": int64(1),
		})
	}
	if err := unstructured.SetNestedSlice(gw.Object, status, "status", "listeners"); err != nil {
		t.Fatalf("set status.listeners: %v", err)
	}
	if err := r.Update(context.Background(), &gw); err != nil {
		t.Fatalf("publish gateway status: %v", err)
	}
	return len(spec), len(status)
}

// poolOrgFor is the acme Org that engages the tenant-networking up-path.
func poolOrgFor() *orgapi.Organization {
	org := sampleOrg()
	org.Spec.TenantPublic = orgapi.OrganizationTenantPublic{ParentDomain: "omani.homes"}
	return org
}

// ── Part 1 — the ports ───────────────────────────────────────────────────────

// TestEnsureConsoleOrgListener_5511_RidesLiveApexPorts — the appended pair must
// take its ports from the LIVE apex listeners (443/80 here), not from a
// constant. The dead pre-#4718 31443/31080 pair must never appear again: those
// are the ports that produced 000 on every hw291 per-Org probe.
func TestEnsureConsoleOrgListener_5511_RidesLiveApexPorts(t *testing.T) {
	t.Parallel()
	org := poolOrgFor()
	r := consoleTLSReconciler(t, org, apexPair())

	changed, err := r.reconcileTenantConsoleTLS(context.Background(), org)
	if err != nil {
		t.Fatalf("reconcileTenantConsoleTLS: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true (cert + listener pair written)")
	}

	ports, count := gatewayListeners(t, r, "spec")
	// Positive control: the append really happened. Without this the port
	// assertions below would pass on a Gateway with no listeners at all.
	if count != 4 {
		t.Fatalf("spec.listeners: got %d %v, want 4 (apex pair + per-Org pair)", count, ports)
	}

	for name, want := range map[string]int64{
		"console-https-acme": 443,
		"console-http-acme":  80,
	} {
		got, ok := ports[name]
		if !ok {
			t.Fatalf("per-Org listener %q was not appended: %v", name, ports)
		}
		if got != want {
			t.Errorf("listener %q port: got %d, want %d (the live apex port — the console LB forwards public 443/80 there and nowhere else)", name, got, want)
		}
		if got == 31443 || got == 31080 {
			t.Errorf("listener %q is back on the dead pre-#4718 port %d — this is the #5511 defect: the console LB forwards no traffic to it, so the customer door answers 000 while the workload is healthy", name, got)
		}
	}

	// The apex pair must be untouched.
	if ports[consoleApexListenerHTTPSName] != 443 || ports[consoleApexListenerHTTPName] != 80 {
		t.Errorf("apex listeners were modified: %v", ports)
	}
}

// TestEnsureConsoleOrgListener_5511_HealsDeadPortsInPlace — the live hw291
// state: the pair is ALREADY on the Gateway at 31443/31080. A presence-only
// check reads that as success and never repairs it, which is what made the
// defect permanent — fixing the port constant alone would repair new Orgs and
// leave every existing customer door at 000. The pair must be healed in place.
func TestEnsureConsoleOrgListener_5511_HealsDeadPortsInPlace(t *testing.T) {
	t.Parallel()
	org := poolOrgFor()
	seeded := append(apexPair(), deadPortPair("*.acme.omani.homes")...)
	r := consoleTLSReconciler(t, org, seeded)

	before, count := gatewayListeners(t, r, "spec")
	// Positive control on the fixture itself: the defect really is present
	// before we act, so a green below cannot come from a fixture that never
	// carried the wrong ports.
	if count != 4 || before["console-https-acme"] != 31443 || before["console-http-acme"] != 31080 {
		t.Fatalf("fixture must start with the pair on the dead ports: %v (%d listeners)", before, count)
	}

	changed, err := r.ensureConsoleOrgListener(context.Background(), orgConsoleTLSNamesFor("acme", "omani.homes"))
	if err != nil {
		t.Fatalf("ensureConsoleOrgListener: %v", err)
	}
	if !changed {
		t.Fatalf("a pair sitting on the dead 31443/31080 ports MUST be reported as changed — a presence-only no-op here is the bug that made #5511 permanent")
	}

	after, count := gatewayListeners(t, r, "spec")
	if count != 4 {
		t.Errorf("heal must repair in place, not duplicate: got %d listeners %v, want 4", count, after)
	}
	if after["console-https-acme"] != 443 {
		t.Errorf("console-https-acme port: got %d, want 443 (healed onto the live apex port)", after["console-https-acme"])
	}
	if after["console-http-acme"] != 80 {
		t.Errorf("console-http-acme port: got %d, want 80 (healed onto the live apex port)", after["console-http-acme"])
	}

	// Idempotent once healed — otherwise the controller rewrites the Gateway on
	// every pass and hot-loops against the apiserver.
	changed, err = r.ensureConsoleOrgListener(context.Background(), orgConsoleTLSNamesFor("acme", "omani.homes"))
	if err != nil {
		t.Fatalf("ensureConsoleOrgListener pass 2: %v", err)
	}
	if changed {
		t.Errorf("pass 2 must be a no-op once the pair is correct — got changed=true (rewrite-every-reconcile hot-loop)")
	}
}

// TestEnsureConsoleOrgListener_5511_ApexAbsentUsesCanonicalFallback — when the
// apex pair cannot be read (the fresh-install race), the append must fall back
// to the #4718 canonical 8443/8080 scheme, matching the catalyst-api twin's
// fallbacks byte-for-byte. It must NOT resurrect the dead pre-#4718 pair.
func TestEnsureConsoleOrgListener_5511_ApexAbsentUsesCanonicalFallback(t *testing.T) {
	t.Parallel()
	org := poolOrgFor()
	// A Gateway carrying an unrelated listener only — no apex console pair.
	r := consoleTLSReconciler(t, org, []any{
		map[string]any{
			"name": "marketplace-https", "hostname": "marketplace.hw291.omantel.biz",
			"port": int64(443), "protocol": "HTTPS",
		},
	})

	if _, err := r.ensureConsoleOrgListener(context.Background(), orgConsoleTLSNamesFor("acme", "omani.homes")); err != nil {
		t.Fatalf("ensureConsoleOrgListener: %v", err)
	}

	ports, count := gatewayListeners(t, r, "spec")
	if count != 3 {
		t.Fatalf("spec.listeners: got %d %v, want 3", count, ports)
	}
	if ports["console-https-acme"] != consoleListenerHTTPSPortFallback {
		t.Errorf("console-https-acme port: got %d, want the #4718 canonical fallback %d", ports["console-https-acme"], consoleListenerHTTPSPortFallback)
	}
	if ports["console-http-acme"] != consoleListenerHTTPPortFallback {
		t.Errorf("console-http-acme port: got %d, want the #4718 canonical fallback %d", ports["console-http-acme"], consoleListenerHTTPPortFallback)
	}
}

// ── Part 2 — a dropped listener can never be silent ──────────────────────────

// verifyPoolOrg drives verifyProvisioned against a fully-provisioned pool Org
// whose console Gateway carries `listeners` in spec.
func verifyPoolOrg(t *testing.T, listeners []any) (*Reconciler, *orgapi.Organization) {
	t.Helper()
	org := poolOrgFor()
	r, _, _ := makeReconciler(t, org,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		readyVClusterHR("acme"),
		boundaryLimitRange("acme"),
		boundaryResourceQuota("acme"),
	)
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, gatewayGVK} {
		r.Scheme().AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		r.Scheme().AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetName(consoleGatewayDefaultName)
	gw.SetNamespace(consoleGatewayDefaultNamespace)
	gw.Object["spec"] = map[string]any{"gatewayClassName": "cilium"}
	if err := unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners"); err != nil {
		t.Fatalf("seed listeners: %v", err)
	}
	if err := r.Create(context.Background(), gw); err != nil {
		t.Fatalf("seed console Gateway: %v", err)
	}
	return r, org
}

// healthyPairListeners is the post-fix steady state: apex pair on 443/80 plus
// the per-Org pair on the SAME ports.
func healthyPairListeners() []any {
	return append(apexPair(),
		map[string]any{
			"name": "console-https-acme", "hostname": "*.acme.omani.homes",
			"port": int64(443), "protocol": "HTTPS",
		},
		map[string]any{
			"name": "console-http-acme", "hostname": "*.acme.omani.homes",
			"port": int64(80), "protocol": "HTTP",
		},
	)
}

// TestVerifyProvisioned_5511_SilentlyDroppedListenerBlocksReady — THE #5511
// assertion. The pair is in spec on the right ports; the Gateway published a
// status that silently omits it. That is indistinguishable, from the operator's
// side, from a listener that was never configured — so it must block Ready and
// name the count mismatch.
func TestVerifyProvisioned_5511_SilentlyDroppedListenerBlocksReady(t *testing.T) {
	t.Parallel()
	r, org := verifyPoolOrg(t, healthyPairListeners())

	specCount, statusCount := admitGatewayListeners(t, r, "console-https-acme", "console-http-acme")
	// Positive control: status really was published and really is non-empty.
	// Without this the "our listener is missing from status" verdict could be
	// satisfied by a Gateway that published nothing, which is a different
	// (and non-decidable) condition.
	if statusCount == 0 {
		t.Fatalf("fixture must publish a NON-EMPTY status.listeners, else the check below is vacuous")
	}
	if specCount != 4 || statusCount != 2 {
		t.Fatalf("fixture counts: spec=%d status=%d, want 4/2 (the hw291 8-vs-6 shape)", specCount, statusCount)
	}

	out := r.verifyProvisioned(context.Background(), org)
	if out.complete() {
		t.Fatalf("a listener present in spec and silently ABSENT from a published status must NOT read as provisioned — got Missing=%v Unverifiable=%v", out.Missing, out.Unverifiable)
	}
	msg := out.message()
	for _, want := range []string{"console-https-acme", "console-http-acme", "silently dropped"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the Ready message must name the drop — %q absent from: %s", want, msg)
		}
	}
	if !strings.Contains(msg, "4 listeners into spec") || !strings.Contains(msg, "only 2 in status") {
		t.Errorf("the Ready message must carry the COUNT mismatch (spec 4 vs status 2) — got: %s", msg)
	}
}

// TestVerifyProvisioned_5511_AdmittedPairGoesGreen — the converse, and the
// count-based assertion in its positive direction: with every spec listener
// published in status the Org is fully provisioned. Without this the test above
// could be satisfied by a check that never goes green.
func TestVerifyProvisioned_5511_AdmittedPairGoesGreen(t *testing.T) {
	t.Parallel()
	r, org := verifyPoolOrg(t, healthyPairListeners())

	specCount, statusCount := admitGatewayListeners(t, r) // decline nothing
	if specCount != statusCount {
		t.Fatalf("fixture must admit every listener: spec=%d status=%d", specCount, statusCount)
	}
	if statusCount == 0 {
		t.Fatalf("fixture must publish a non-empty status.listeners")
	}

	// The count assertion the defect violated: every declared listener admitted.
	_, liveSpec := gatewayListeners(t, r, "spec")
	_, liveStatus := gatewayListeners(t, r, "status")
	if liveStatus != liveSpec {
		t.Errorf("len(status.listeners)=%d != len(spec.listeners)=%d", liveStatus, liveSpec)
	}

	out := r.verifyProvisioned(context.Background(), org)
	if !out.complete() {
		t.Errorf("an Org whose listener pair is in spec AND admitted into status must read fully provisioned — got Missing=%v", out.Missing)
	}
}

// TestVerifyProvisioned_5511_EmptyStatusIsUnverifiableNotMissing — the
// evidence-of-absence guard. A Gateway that has published NO listener status
// yet (the bootstrap window) cannot be judged: reporting that as Missing would
// red-flag every pool Org on the Sovereign the moment the Gateway is created.
// It must requeue instead. This is also what stops the status check from
// passing vacuously — an empty status is explicitly undecided, never a pass.
func TestVerifyProvisioned_5511_EmptyStatusIsUnverifiableNotMissing(t *testing.T) {
	t.Parallel()
	r, org := verifyPoolOrg(t, healthyPairListeners()) // no admission simulated

	_, statusCount := gatewayListeners(t, r, "status")
	if statusCount != 0 {
		t.Fatalf("fixture must have an EMPTY status.listeners, got %d", statusCount)
	}

	out := r.verifyProvisioned(context.Background(), org)
	if len(out.Missing) != 0 {
		t.Errorf("an unpublished status.listeners is not evidence of absence — must not fabricate a red: %v", out.Missing)
	}
	if len(out.Unverifiable) == 0 {
		t.Errorf("an unpublished status.listeners must be reported UNVERIFIABLE so the Org requeues rather than silently passing")
	}
}

// TestVerifyProvisioned_5511_PortDriftBlocksReady — admission alone is not
// enough. A pair admitted into status but bound to the dead pre-#4718 ports
// still receives no traffic, so the port drift itself must block Ready. This
// fires even though `status.listeners` is complete.
func TestVerifyProvisioned_5511_PortDriftBlocksReady(t *testing.T) {
	t.Parallel()
	r, org := verifyPoolOrg(t, append(apexPair(), deadPortPair("*.acme.omani.homes")...))

	specCount, statusCount := admitGatewayListeners(t, r) // every listener admitted
	if specCount != statusCount || statusCount == 0 {
		t.Fatalf("fixture must admit every listener: spec=%d status=%d", specCount, statusCount)
	}

	out := r.verifyProvisioned(context.Background(), org)
	if out.complete() {
		t.Fatalf("a pair on the dead 31443/31080 ports receives no traffic and must NOT read as provisioned, even fully admitted — got Missing=%v", out.Missing)
	}
	msg := out.message()
	if !strings.Contains(msg, "wrong port") || !strings.Contains(msg, "31443") {
		t.Errorf("the Ready message must name the port drift — got: %s", msg)
	}
}
