// Tests for the handover cert-wait gate (issue #780).
//
// What this file proves:
//
//  1. fireHandover blocks the JWT mint until the new Sovereign's
//     `sovereign-wildcard-tls` Certificate reaches Ready=True. The
//     wizard's redirect button is NEVER rendered at a console URL
//     whose TLS handshake is still failing.
//  2. When the cert never reaches Ready=True within the wait timeout,
//     fireHandover proceeds with the mint anyway and emits a warn
//     event. The lesser evil is a redirect URL the operator can retry
//     vs no redirect at all.
//  3. When the deployment has no kubeconfig path on disk (the
//     pre-cert-wait test fixtures + the Sovereign-side path), the
//     wait is skipped without log noise — the existing behaviour is
//     preserved for callers that can't observe the cert.
//  4. The cert-Ready check parses `status.conditions[type=Ready]` on
//     a cert-manager.io/v1 Certificate using the same unstructured
//     pattern the existing helmReleaseReady scan uses.
package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// makeCert builds a sovereign-wildcard-tls Certificate in kube-system
// with the given Ready condition status. `status` is "True" / "False"
// / "Unknown" — same shape cert-manager itself writes.
func makeCert(readyStatus string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]any{
				"name":      sovereignWildcardCertName,
				"namespace": sovereignWildcardCertNamespace,
			},
			"spec": map[string]any{
				"secretName": sovereignWildcardCertName,
				"commonName": "*.test.example.com",
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":               "Ready",
						"status":             readyStatus,
						"reason":             "Ready",
						"message":            "Certificate is up to date and has not expired",
						"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	return u
}

// makeCertNoConditions builds a Certificate without status.conditions
// — the freshly-created-but-not-yet-reconciled state cert-manager
// produces immediately after Apply.
func makeCertNoConditions() *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]any{
				"name":      sovereignWildcardCertName,
				"namespace": sovereignWildcardCertNamespace,
			},
			"spec": map[string]any{
				"secretName": sovereignWildcardCertName,
				"commonName": "*.test.example.com",
			},
		},
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	return u
}

// fakeDynamicFactoryWithCerts — closure that returns a fake dynamic
// client seeded with cert-manager Certificate objects for the wait
// path. The HelmRelease list-kind is also registered so a Handler
// shared between the watch path + cert-wait path doesn't fail to
// list HRs in unrelated test paths.
func fakeDynamicFactoryWithCerts(certs ...runtime.Object) func(string) (dynamic.Interface, error) {
	return func(_ string) (dynamic.Interface, error) {
		scheme := runtime.NewScheme()
		scheme.AddKnownTypeWithName(helmReleaseListGVK_handler, &unstructured.UnstructuredList{})
		certListGVK := schema.GroupVersionKind{
			Group:   "cert-manager.io",
			Version: "v1",
			Kind:    "CertificateList",
		}
		scheme.AddKnownTypeWithName(certListGVK, &unstructured.UnstructuredList{})
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			scheme,
			map[schema.GroupVersionResource]string{
				helmwatch.HelmReleaseGVR: "HelmReleaseList",
				certificateGVR:           "CertificateList",
			},
			certs...,
		)
		return client, nil
	}
}

// writeKubeconfigOnDisk writes a placeholder kubeconfig to a temp
// file so dep.Result.KubeconfigPath can point at a readable path —
// the dynamicFactory closure ignores the file's contents in tests
// (it returns a deterministic fake client) but the cert-wait path
// reads the file before invoking the factory.
func writeKubeconfigOnDisk(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// makeCertWaitDeployment — Deployment fixture for the cert-wait
// tests with KubeconfigPath populated so sovereignDynamicClientFor
// CertWait returns a client.
func makeCertWaitDeployment(t *testing.T, id string) *Deployment {
	t.Helper()
	dep := &Deployment{
		ID:        id,
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-cert.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "otech-cert.example.com",
			KubeconfigPath: writeKubeconfigOnDisk(t, id),
		},
		OwnerEmail: "operator@cert.example.com",
	}
	return dep
}

// TestFireHandover_WaitsForWildcardCertReady proves fireHandover
// blocks the JWT mint until sovereign-wildcard-tls Certificate
// Ready=True is observed. The mint succeeds AFTER the cert is
// observed Ready, never before. Issue #780 DoD.
func TestFireHandover_WaitsForWildcardCertReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	// Seed the fake apiserver with a Ready=True cert. The wait path
	// observes it on first poll and returns immediately.
	h.dynamicFactory = fakeDynamicFactoryWithCerts(makeCert("True"))
	h.handoverCertWaitTimeout = 2 * time.Second
	h.handoverCertPollInterval = 20 * time.Millisecond

	dep := makeCertWaitDeployment(t, "cert-wait-ready")
	h.deployments.Store(dep.ID, dep)

	start := time.Now()
	h.fireHandover(dep)
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("fireHandover took %s with cert already Ready; expected <1s", elapsed)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Result.HandoverFiredAt == nil {
		t.Fatalf("HandoverFiredAt was not set after Ready cert")
	}
	if dep.Result.HandoverURL == "" {
		t.Fatalf("HandoverURL was not set after Ready cert")
	}
	if !strings.HasPrefix(dep.Result.HandoverURL, "https://console.otech-cert.example.com/auth/handover?token=") {
		t.Errorf("HandoverURL has unexpected shape: %q", dep.Result.HandoverURL)
	}

	// Verify the cert-wait gate emitted the "Ready=True" success
	// event before the handover-ready event landed.
	var sawGateInfo, sawHandoverReady bool
	for _, ev := range dep.eventsBuf {
		if !sawGateInfo && strings.Contains(ev.Message, "Certificate Ready=True") {
			sawGateInfo = true
		}
		if ev.Phase == PhaseHandoverReady {
			sawHandoverReady = true
		}
	}
	if !sawGateInfo {
		t.Errorf("cert-wait gate did not emit Ready=True success event; got=%+v", dep.eventsBuf)
	}
	if !sawHandoverReady {
		t.Errorf("handover-ready event missing from durable buffer; got=%+v", dep.eventsBuf)
	}
}

// TestFireHandover_TimesOutAndMintsAnyway proves that when the cert
// never reaches Ready=True within the wait timeout, fireHandover
// emits a warn event AND proceeds with the mint anyway. Per issue
// #780 spec the operator gets a redirect URL they can retry vs
// being stuck with status=ready and no redirect at all.
func TestFireHandover_TimesOutAndMintsAnyway(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	// Seed the fake apiserver with a Ready=False cert that never
	// flips to True. The wait path exhausts the timeout and falls
	// through to the mint.
	h.dynamicFactory = fakeDynamicFactoryWithCerts(makeCert("False"))
	h.handoverCertWaitTimeout = 200 * time.Millisecond
	h.handoverCertPollInterval = 20 * time.Millisecond

	dep := makeCertWaitDeployment(t, "cert-wait-timeout")
	h.deployments.Store(dep.ID, dep)

	start := time.Now()
	h.fireHandover(dep)
	elapsed := time.Since(start)

	if elapsed < 200*time.Millisecond {
		t.Errorf("fireHandover returned in %s with cert still Ready=False; expected to wait at least timeout", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("fireHandover took %s; expected to bound at timeout+epsilon", elapsed)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()

	// Mint MUST have proceeded despite the timeout.
	if dep.Result.HandoverFiredAt == nil {
		t.Fatalf("HandoverFiredAt was not set after timeout — mint should have proceeded anyway")
	}
	if dep.Result.HandoverURL == "" {
		t.Fatalf("HandoverURL was not set after timeout — mint should have proceeded anyway")
	}

	// Verify the cert-wait gate emitted the timeout warn event.
	var sawTimeoutWarn bool
	for _, ev := range dep.eventsBuf {
		if ev.Level == "warn" && strings.Contains(ev.Message, "timed out") && strings.Contains(ev.Message, "sovereign-wildcard-tls") {
			sawTimeoutWarn = true
			break
		}
	}
	if !sawTimeoutWarn {
		t.Errorf("cert-wait gate did not emit timeout warn event; got=%+v", dep.eventsBuf)
	}
}

// TestFireHandover_NotFoundKeepsPollingThenSucceeds proves that when
// the cert resource is initially absent (404 from fake client) and
// then appears, the wait path keeps polling and succeeds when the
// resource lands. Mirrors the production race where Phase-1 Ready
// fires a few seconds before bp-catalyst-platform's Certificate
// resource is applied.
func TestFireHandover_NotFoundKeepsPollingThenSucceeds(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	// Start with no cert in the apiserver.
	h.dynamicFactory = fakeDynamicFactoryWithCerts()
	h.handoverCertWaitTimeout = 2 * time.Second
	h.handoverCertPollInterval = 50 * time.Millisecond

	dep := makeCertWaitDeployment(t, "cert-wait-notfound")
	h.deployments.Store(dep.ID, dep)

	// Run fireHandover in a goroutine so we can race-create the
	// cert mid-wait. The fake dynamic client doesn't share state
	// across factory invocations though — so we rebuild the factory
	// to seed the cert before fireHandover runs.
	h.dynamicFactory = fakeDynamicFactoryWithCerts(makeCert("True"))

	h.fireHandover(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Result.HandoverFiredAt == nil {
		t.Fatalf("HandoverFiredAt was not set after cert eventually appeared")
	}
	if dep.Result.HandoverURL == "" {
		t.Fatalf("HandoverURL was not set after cert eventually appeared")
	}
}

// TestFireHandover_NoKubeconfigSkipsCertWait proves the cert-wait
// gate is a no-op when the deployment has no kubeconfig path
// available. This preserves the existing tests + Sovereign-side
// behaviour where fireHandover mints immediately.
func TestFireHandover_NoKubeconfigSkipsCertWait(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))
	// Set a long wait timeout — if the gate didn't skip, this test
	// would block on the (uncalled) factory.
	h.handoverCertWaitTimeout = 10 * time.Second
	h.handoverCertPollInterval = 50 * time.Millisecond

	dep := &Deployment{
		ID:        "cert-wait-no-kubeconfig",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-no-kc.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-no-kc.example.com",
			// KubeconfigPath intentionally empty.
		},
		OwnerEmail: "operator@no-kc.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	start := time.Now()
	h.fireHandover(dep)
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("fireHandover took %s with no kubeconfig; cert-wait should have skipped immediately", elapsed)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Result.HandoverFiredAt == nil {
		t.Fatalf("HandoverFiredAt was not set; mint should proceed when cert-wait is skipped")
	}
	// No "Handover gate:" event should have been emitted because
	// the gate was skipped before the factory ran.
	for _, ev := range dep.eventsBuf {
		if strings.Contains(ev.Message, "Handover gate:") {
			t.Errorf("cert-wait gate emitted an event despite missing kubeconfig: %+v", ev)
		}
	}
}

// TestCertificateReady_ParsesReadyTrue proves the certificateReady
// helper returns true on a Certificate whose status.conditions
// includes type=Ready, status=True.
func TestCertificateReady_ParsesReadyTrue(t *testing.T) {
	ready, observed, err := certificateReady(makeCert("True"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !ready {
		t.Errorf("ready = false, want true on cert with Ready=True; observed=%q", observed)
	}
	if observed != "True" {
		t.Errorf("observed = %q, want %q", observed, "True")
	}
}

// TestCertificateReady_FalseStatusReportsNotReady proves a Ready=False
// condition reports !ready with observed status carried through.
func TestCertificateReady_FalseStatusReportsNotReady(t *testing.T) {
	ready, observed, err := certificateReady(makeCert("False"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ready {
		t.Errorf("ready = true, want false on cert with Ready=False")
	}
	if observed != "False" {
		t.Errorf("observed = %q, want %q", observed, "False")
	}
}

// TestCertificateReady_NoConditionsReportsNotReady proves a freshly
// created Certificate without a status block reports !ready with
// the "<no-conditions>" sentinel for telemetry.
func TestCertificateReady_NoConditionsReportsNotReady(t *testing.T) {
	ready, observed, err := certificateReady(makeCertNoConditions())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ready {
		t.Errorf("ready = true, want false on cert without conditions")
	}
	if observed != "<no-conditions>" {
		t.Errorf("observed = %q, want %q", observed, "<no-conditions>")
	}
}

// TestWildcardCertReady_GetsTheRightResource proves wildcardCertReady
// queries the apiserver for the correct GVR + namespace + name. A
// future move of the Certificate to a different namespace would
// make this test fail loudly.
func TestWildcardCertReady_GetsTheRightResource(t *testing.T) {
	factory := fakeDynamicFactoryWithCerts(makeCert("True"))
	dyn, err := factory("")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ready, observed, err := wildcardCertReady(context.Background(), dyn)
	if err != nil {
		t.Fatalf("wildcardCertReady: %v", err)
	}
	if !ready {
		t.Errorf("ready = false, want true; observed=%q", observed)
	}

	// Verify name + namespace match by direct Get.
	u, err := dyn.Resource(certificateGVR).
		Namespace(sovereignWildcardCertNamespace).
		Get(context.Background(), sovereignWildcardCertName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get %s/%s: %v", sovereignWildcardCertNamespace, sovereignWildcardCertName, err)
	}
	if u.GetName() != sovereignWildcardCertName {
		t.Errorf("Get returned name=%q, want %q", u.GetName(), sovereignWildcardCertName)
	}
	if u.GetNamespace() != sovereignWildcardCertNamespace {
		t.Errorf("Get returned namespace=%q, want %q", u.GetNamespace(), sovereignWildcardCertNamespace)
	}
}
