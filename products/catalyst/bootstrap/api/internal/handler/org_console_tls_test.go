// org_console_tls_test.go — #4075. Tests the per-Org console TLS trio:
// deterministic name/host derivation (incl. the skip cases) and the
// idempotent apply of the Certificate + Gateway listener + HTTPRoute against a
// fake dynamic client, generalised across ALL four pool parents.

package handler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// shrinkAdmitBudget shrinks the #5511 read-back poll budget/interval so unit
// tests never sit in the production 60s wait; restored on cleanup.
func shrinkAdmitBudget(t *testing.T) {
	t.Helper()
	prevBudget, prevInterval := orgConsoleListenerAdmitBudget, orgConsoleListenerAdmitPollInterval
	orgConsoleListenerAdmitBudget = 50 * time.Millisecond
	orgConsoleListenerAdmitPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		orgConsoleListenerAdmitBudget, orgConsoleListenerAdmitPollInterval = prevBudget, prevInterval
	})
}

// TestResolveOrgConsoleTLSNames_AllPoolParents proves the naming/host
// derivation is generic across every role=org-pool parent (omani.homes/rest/
// trade/works) — NOT hardcoded to omani.homes — and matches the live demo Org
// shape byte-for-byte (Certificate org-wildcard-tls-demo-omani-homes etc.).
func TestResolveOrgConsoleTLSNames_AllPoolParents(t *testing.T) {
	parents := []string{"omani.homes", "omani.rest", "omani.trade", "omani.works"}
	for _, parent := range parents {
		parent := parent
		t.Run(parent, func(t *testing.T) {
			rec := store.OrganizationProvisionRecord{
				OrganizationID: "t-acme",
				Subdomain:      "acme",
				DomainMode:     store.OrganizationDomainFreeSubdomain,
				ParentDomain:   parent,
				OTECHFQDN:      "omantel.biz",
			}
			n, ok := resolveOrgConsoleTLSNames(rec)
			if !ok {
				t.Fatalf("expected provisioning for pool parent %q", parent)
			}
			dashed := strings.ReplaceAll(parent, ".", "-")
			wantCert := "org-wildcard-tls-acme-" + dashed
			wantRoute := "catalyst-ui-acme-" + dashed
			if n.CertName != wantCert {
				t.Errorf("CertName = %q, want %q", n.CertName, wantCert)
			}
			if n.RouteName != wantRoute {
				t.Errorf("RouteName = %q, want %q", n.RouteName, wantRoute)
			}
			if n.WildcardHost != "*.acme."+parent {
				t.Errorf("WildcardHost = %q, want *.acme.%s", n.WildcardHost, parent)
			}
			if n.ConsoleHost != "console.acme."+parent {
				t.Errorf("ConsoleHost = %q, want console.acme.%s", n.ConsoleHost, parent)
			}
			if n.OrgZone != "acme."+parent {
				t.Errorf("OrgZone = %q, want acme.%s", n.OrgZone, parent)
			}
			if n.HTTPSName != "console-https-acme" || n.HTTPName != "console-http-acme" {
				t.Errorf("listener names = %q/%q, want console-https-acme/console-http-acme", n.HTTPSName, n.HTTPName)
			}
		})
	}
}

// TestResolveOrgConsoleTLSNames_SkipCases proves the trio is NOT provisioned
// for BYO Orgs (own cert + CNAME), blank parent (single-domain back-compat —
// apex wildcard already covers it), and invalid slugs.
func TestResolveOrgConsoleTLSNames_SkipCases(t *testing.T) {
	cases := []struct {
		name string
		rec  store.OrganizationProvisionRecord
	}{
		{"byo-mode", store.OrganizationProvisionRecord{
			Subdomain: "acme", DomainMode: store.OrganizationDomainBYO,
			BYODomain: "acme.example.com", ParentDomain: "omani.homes",
		}},
		{"blank-parent", store.OrganizationProvisionRecord{
			Subdomain: "acme", DomainMode: store.OrganizationDomainFreeSubdomain,
			ParentDomain: "", OTECHFQDN: "omantel.biz",
		}},
		{"invalid-slug-leading-digit", store.OrganizationProvisionRecord{
			Subdomain: "1acme", DomainMode: store.OrganizationDomainFreeSubdomain,
			ParentDomain: "omani.homes",
		}},
		{"invalid-slug-too-short", store.OrganizationProvisionRecord{
			Subdomain: "ab", DomainMode: store.OrganizationDomainFreeSubdomain,
			ParentDomain: "omani.homes",
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := resolveOrgConsoleTLSNames(tc.rec); ok {
				t.Errorf("expected skip for %s, got provisioning", tc.name)
			}
		})
	}
}

// fakeDynForConsoleTLS builds a fake dynamic client registering the three GVRs
// the console-TLS trio writes (Certificate, Gateway, HTTPRoute), seeded with
// the apex-only cilium-gateway-console Gateway (so the listener apply patches
// an existing object, mirroring the live Sovereign).
func fakeDynForConsoleTLS(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	return fakeDynForConsoleTLSWithApexPorts(t, 8443, 8080)
}

// fakeDynForConsoleTLSWithApexPorts is fakeDynForConsoleTLS with the apex
// listener pair on caller-chosen ports, so consoleApexListenerPorts tests can
// use NON-fallback ports (a fallback-equal seed cannot distinguish "derived
// from the apex" from "gateway unreadable, fell back" — vacuity).
//
// The Gateway is seeded via an explicit-GVR Create, NOT via the constructor's
// object list: the fake's constructor maps an unstructured seed to a resource
// by GUESSING the plural from the Kind ("Gateway" → "gatewaies" per the
// y→ies pluralizer), so a constructor-seeded Gateway is invisible under the
// real "gateways" resource and every GET on it 404s (#5511 — this masked the
// read-back tests until caught).
func fakeDynForConsoleTLSWithApexPorts(t *testing.T, httpsPort, httpPort int64) *dynamicfake.FakeDynamicClient {
	t.Helper()
	gw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata": map[string]any{
			"name":      consoleGatewayName,
			"namespace": consoleGatewayNamespace,
		},
		"spec": map[string]any{
			"gatewayClassName": "cilium",
			"listeners": []any{
				map[string]any{"name": "console-https", "port": httpsPort, "protocol": "HTTPS", "hostname": "*.omantel.biz"},
				map[string]any{"name": "console-http", "port": httpPort, "protocol": "HTTP", "hostname": "*.omantel.biz"},
			},
		},
	}}
	return fakeDynSeededWithGateway(t, gw)
}

// fakeDynSeededWithGateway registers the console-TLS GVR set on an EMPTY fake
// and then Creates the given Gateway under the explicit consoleGatewayGVR, so
// subsequent GETs on the real "gateways" resource find it (see the pluralizer
// trap documented on fakeDynForConsoleTLSWithApexPorts).
func fakeDynSeededWithGateway(t *testing.T, gw *unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		certificateGVR:    "CertificateList",
		consoleGatewayGVR: "GatewayList",
		httpRouteGVR:      "HTTPRouteList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
	if _, err := dyn.Resource(consoleGatewayGVR).Namespace(consoleGatewayNamespace).
		Create(context.Background(), gw, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed console gateway: %v", err)
	}
	return dyn
}

func newConsoleTLSHandler(t *testing.T, dyn *dynamicfake.FakeDynamicClient) *Handler {
	t.Helper()
	return newConsoleTLSHandlerWithCore(t, dyn, nil)
}

func newConsoleTLSHandlerWithCore(t *testing.T, dyn *dynamicfake.FakeDynamicClient, core *k8sfake.Clientset) *Handler {
	t.Helper()
	shrinkAdmitBudget(t)
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		if core == nil {
			return &sovereignDeps{core: nil, dyn: dyn}, nil
		}
		return &sovereignDeps{core: core, dyn: dyn}, nil
	})
	return h
}

// TestProvisionOrgConsoleTLS_AppliesTrio is the binary DoD: a completed
// free-subdomain Org on a pool parent gets a Certificate + Gateway listener
// patch + console HTTPRoute applied to the live cluster.
func TestProvisionOrgConsoleTLS_AppliesTrio(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	h := newConsoleTLSHandler(t, dyn)
	rec := store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.rest",
		OTECHFQDN:      "omantel.biz",
		CompanyName:    "Acme Corp",
		AdminEmail:     "admin@acme.test",
	}
	ctx := context.Background()
	h.provisionOrgConsoleTLS(ctx, rec)

	// 1) Certificate exists in kube-system with the wildcard SAN + DNS-01 issuer.
	cert, err := dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		Get(ctx, "org-wildcard-tls-acme-omani-rest", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Certificate not created: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(cert.Object, "spec")
	if got, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName"); got != "org-wildcard-tls-acme-omani-rest" {
		t.Errorf("cert secretName = %q", got)
	}
	if got, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name"); got != orgConsoleTLSIssuer {
		t.Errorf("cert issuer = %q, want %q", got, orgConsoleTLSIssuer)
	}
	dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	if !sliceContains(dnsNames, "*.acme.omani.rest") || !sliceContains(dnsNames, "acme.omani.rest") {
		t.Errorf("cert dnsNames = %v, want *.acme.omani.rest + acme.omani.rest", dnsNames)
	}
	_ = spec

	// 2) HTTPRoute exists in catalyst-system → cilium-gateway-console, hostname
	//    console.acme.omani.rest, with the catalyst-ui + catalyst-api backends.
	route, err := dyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Get(ctx, "catalyst-ui-acme-omani-rest", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("HTTPRoute not created: %v", err)
	}
	hosts, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if !sliceContains(hosts, "console.acme.omani.rest") {
		t.Errorf("route hostnames = %v, want console.acme.omani.rest", hosts)
	}
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if len(parents) != 1 {
		t.Fatalf("route parentRefs = %d, want 1", len(parents))
	}
	pr, _ := parents[0].(map[string]any)
	if pr["name"] != consoleGatewayName || pr["namespace"] != consoleGatewayNamespace {
		t.Errorf("route parent = %v/%v, want %s/%s", pr["namespace"], pr["name"], consoleGatewayNamespace, consoleGatewayName)
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	if len(rules) != 5 {
		t.Errorf("route rules = %d, want 5 (readyz/healthz, auth/handover, /api/, /catalyst/, /)", len(rules))
	}

	// 3) Gateway listener apply was invoked against cilium-gateway-console.
	//    (The fake's SSA does not merge keyed lists like a real apiserver, so
	//    we assert the apply ACTION was issued rather than re-deriving merge
	//    semantics here; the live merge is verified on the Sovereign.)
	sawGatewayApply := false
	for _, a := range dyn.Actions() {
		if a.GetResource() == consoleGatewayGVR && a.GetVerb() == "patch" {
			if pa, ok := a.(interface{ GetName() string }); ok && pa.GetName() == consoleGatewayName {
				sawGatewayApply = true
			}
		}
	}
	if !sawGatewayApply {
		t.Errorf("expected a server-side-apply patch against Gateway %s; actions=%v", consoleGatewayName, dyn.Actions())
	}
}

// TestProvisionOrgConsoleTLS_Idempotent proves a second pass does not error
// (Create returns AlreadyExists which the helpers treat as success).
func TestProvisionOrgConsoleTLS_Idempotent(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	h := newConsoleTLSHandler(t, dyn)
	rec := store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		OTECHFQDN:      "omantel.biz",
	}
	ctx := context.Background()
	h.provisionOrgConsoleTLS(ctx, rec)
	h.provisionOrgConsoleTLS(ctx, rec) // second pass must not panic / error out

	if _, err := dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		Get(ctx, "org-wildcard-tls-acme-omani-homes", metav1.GetOptions{}); err != nil {
		t.Fatalf("Certificate missing after idempotent re-run: %v", err)
	}
}

// TestProvisionOrgConsoleTLS_SkipsBYO proves a BYO Org applies NOTHING (no
// dynamic-client calls beyond the skip log).
func TestProvisionOrgConsoleTLS_SkipsBYO(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	h := newConsoleTLSHandler(t, dyn)
	rec := store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainBYO,
		BYODomain:      "acme.example.com",
		OTECHFQDN:      "omantel.biz",
	}
	ctx := context.Background()
	h.provisionOrgConsoleTLS(ctx, rec)

	if _, err := dyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		Get(ctx, "org-wildcard-tls-acme-example-com", metav1.GetOptions{}); err == nil {
		t.Error("BYO Org should NOT have a pool-parent wildcard Certificate")
	}
}

func sliceContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestConsoleApexListenerPorts_DerivesFromApex locks #4732 item 2: the
// per-Org listener pair must ride the SAME ports as the live apex
// console-https/console-http listeners (the only ports the console ELB
// forwards to), never a hardcoded pair from an older port scheme.
//
// The seeded apex ports are deliberately NON-fallback (9443/9090): a
// fallback-equal seed cannot distinguish "derived from the apex gateway" from
// "gateway unreadable, silently fell back" — which is exactly how this test
// stayed green while the constructor-seeded Gateway was invisible to GETs
// (the gatewaies pluralizer trap, see fakeDynForConsoleTLSWithApexPorts).
func TestConsoleApexListenerPorts_DerivesFromApex(t *testing.T) {
	dyn := fakeDynForConsoleTLSWithApexPorts(t, 9443, 9090)
	httpsPort, httpPort := consoleApexListenerPorts(context.Background(), dyn)
	if httpsPort != 9443 || httpPort != 9090 {
		t.Errorf("apex-derived ports = %d/%d, want 9443/9090 from the seeded apex (fallback leak?)", httpsPort, httpPort)
	}
}

// TestConsoleApexListenerPorts_FallbackWhenGatewayMissing locks the
// degraded path: no gateway readable → the #4718 canonical fallback pair.
func TestConsoleApexListenerPorts_FallbackWhenGatewayMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		certificateGVR:    "CertificateList",
		consoleGatewayGVR: "GatewayList",
		httpRouteGVR:      "HTTPRouteList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
	httpsPort, httpPort := consoleApexListenerPorts(context.Background(), dyn)
	if httpsPort != consoleListenerHTTPSPortFallback || httpPort != consoleListenerHTTPPortFallback {
		t.Errorf("fallback ports = %d/%d, want %d/%d", httpsPort, httpPort,
			consoleListenerHTTPSPortFallback, consoleListenerHTTPPortFallback)
	}
}

// ── #5511 — multi-region fan-out + listener admission read-back ────────────

// TestOrgConsoleTLSSecondaryRegions locks the pure cluster-id → region
// derivation: secondaries split on the deployment boundary (longest
// candidate-primary prefix, so hyphenated region keys survive), and any set
// with no prefix relations yields the safe empty map (host-only behaviour).
func TestOrgConsoleTLSSecondaryRegions(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want map[string]string
	}{
		{"two-secondaries", []string{"dep291", "dep291-hel1-1", "dep291-nbg1-2"},
			map[string]string{"dep291-hel1-1": "hel1-1", "dep291-nbg1-2": "nbg1-2"}},
		{"hyphenated-region-keys", []string{"dep", "dep-ap-southeast-3", "dep-ap-southeast-3-1"},
			map[string]string{"dep-ap-southeast-3": "ap-southeast-3", "dep-ap-southeast-3-1": "ap-southeast-3-1"}},
		{"fqdn-alias-primary", []string{"sovereign-hw291.omantel.biz", "sovereign-hw291.omantel.biz-hel1-1"},
			map[string]string{"sovereign-hw291.omantel.biz-hel1-1": "hel1-1"}},
		{"no-prefix-relation-safe-noop", []string{"sovereign-x.y", "dep123-r1"}, map[string]string{}},
		{"single-cluster", []string{"dep291"}, map[string]string{}},
		{"empty", nil, map[string]string{}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := orgConsoleTLSSecondaryRegions(tc.ids)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for cid, region := range tc.want {
				if got[cid] != region {
					t.Errorf("region for %q = %q, want %q (full: %v)", cid, got[cid], region, got)
				}
			}
		})
	}
}

// fakeDynWithGatewayStatus builds a fake dynamic client whose console gateway
// carries an explicit metadata.generation, a status condition with
// observedGeneration, and the given status.listeners names — the read-back
// verification's ground truth surface.
func fakeDynWithGatewayStatus(t *testing.T, generation, observedGeneration int64, statusListenerNames ...string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	statusListeners := make([]any, 0, len(statusListenerNames))
	for _, n := range statusListenerNames {
		statusListeners = append(statusListeners, map[string]any{"name": n, "attachedRoutes": int64(1)})
	}
	gw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata": map[string]any{
			"name":       consoleGatewayName,
			"namespace":  consoleGatewayNamespace,
			"generation": generation,
		},
		"spec": map[string]any{
			"gatewayClassName": "cilium",
			"listeners": []any{
				map[string]any{"name": "console-https", "port": int64(8443), "protocol": "HTTPS", "hostname": "*.omantel.biz"},
				map[string]any{"name": "console-http", "port": int64(8080), "protocol": "HTTP", "hostname": "*.omantel.biz"},
			},
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Accepted", "status": "True", "observedGeneration": observedGeneration},
			},
			"listeners": statusListeners,
		},
	}}
	return fakeDynSeededWithGateway(t, gw)
}

// TestWaitOrgConsoleListenersAdmitted_Success — read-back success path: both
// per-Org listener names PRESENT in status.listeners (among the apex pair)
// returns nil. The trailing vacuity guard proves the same checker FAILS when
// the names are absent — the presence assertion cannot pass on nothing.
func TestWaitOrgConsoleListenersAdmitted_Success(t *testing.T) {
	shrinkAdmitBudget(t)
	names, ok := resolveOrgConsoleTLSNames(store.OrganizationProvisionRecord{
		Subdomain:    "acme",
		DomainMode:   store.OrganizationDomainFreeSubdomain,
		ParentDomain: "omani.homes",
	})
	if !ok {
		t.Fatal("resolveOrgConsoleTLSNames returned !ok")
	}
	admitted := fakeDynWithGatewayStatus(t, 2, 2,
		"console-https", "console-http", names.HTTPSName, names.HTTPName)
	if err := waitOrgConsoleListenersAdmitted(context.Background(), admitted, names,
		time.Now().Add(orgConsoleListenerAdmitBudget)); err != nil {
		t.Fatalf("expected admission success, got: %v", err)
	}
	// Vacuity guard: apex-only status MUST fail the same check.
	apexOnly := fakeDynWithGatewayStatus(t, 2, 1, "console-https", "console-http")
	if err := waitOrgConsoleListenersAdmitted(context.Background(), apexOnly, names,
		time.Now().Add(orgConsoleListenerAdmitBudget)); err == nil {
		t.Fatal("vacuity: checker returned nil on a status WITHOUT the per-Org listeners")
	}
}

// TestWaitOrgConsoleListenersAdmitted_Timeout — read-back timeout path: a
// gateway that never admits the pair (the hw291 defect-A shape: generation=2,
// observedGeneration=1, apex-only status) errors after the bounded budget and
// the error names the gateway, BOTH listener names, and gen vs obsGen so the
// next env walk can diagnose the wedge from the log line alone.
func TestWaitOrgConsoleListenersAdmitted_Timeout(t *testing.T) {
	shrinkAdmitBudget(t)
	names, ok := resolveOrgConsoleTLSNames(store.OrganizationProvisionRecord{
		Subdomain:    "acme",
		DomainMode:   store.OrganizationDomainFreeSubdomain,
		ParentDomain: "omani.homes",
	})
	if !ok {
		t.Fatal("resolveOrgConsoleTLSNames returned !ok")
	}
	wedged := fakeDynWithGatewayStatus(t, 2, 1, "console-https", "console-http")
	err := waitOrgConsoleListenersAdmitted(context.Background(), wedged, names,
		time.Now().Add(orgConsoleListenerAdmitBudget))
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		consoleGatewayName,
		names.HTTPSName,
		names.HTTPName,
		"generation=2",
		"observedGeneration=1",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("timeout error missing %q\nerror: %s", want, msg)
		}
	}
}

// TestProvisionOrgConsoleTLS_MultiRegionFanOut is the #5511 defect-A lock: on
// a chroot with secondary regions registered in k8sCache, the listener pair +
// console HTTPRoute are applied to EVERY region cluster, the issued cert
// secret is MIRRORED from the host region to each secondary, and the
// cert-manager Certificate CR stays host-only (no duplicate LE issuance).
func TestProvisionOrgConsoleTLS_MultiRegionFanOut(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw291.omantel.biz") // chroot gate for the fan-out

	hostDyn := fakeDynForConsoleTLS(t)
	secDynB := fakeDynForConsoleTLS(t)
	secDynC := fakeDynForConsoleTLS(t)

	issued := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "org-wildcard-tls-acme-omani-homes", Namespace: consoleCertNamespace},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("host-issued-cert"),
			"tls.key": []byte("host-issued-key"),
		},
	}
	hostCore := k8sfake.NewSimpleClientset(issued)
	secCoreB := k8sfake.NewSimpleClientset()
	secCoreC := k8sfake.NewSimpleClientset()

	h := newConsoleTLSHandlerWithCore(t, hostDyn, hostCore)
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clusters: []k8scache.ClusterRef{
			{ID: "dep291", DynamicClient: hostDyn, CoreClient: hostCore},
			{ID: "dep291-hel1-1", DynamicClient: secDynB, CoreClient: secCoreB},
			{ID: "dep291-nbg1-2", DynamicClient: secDynC, CoreClient: secCoreC},
		},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	h.SetK8sCache(f, k8scache.NewSARCache(), "")

	rec := store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		OTECHFQDN:      "hw291.omantel.biz",
	}
	ctx := context.Background()
	h.provisionOrgConsoleTLS(ctx, rec)

	for name, sec := range map[string]*dynamicfake.FakeDynamicClient{"hel1-1": secDynB, "nbg1-2": secDynC} {
		// (a) listener SSA patch reached the secondary's console gateway.
		sawGatewayApply := false
		for _, a := range sec.Actions() {
			if a.GetResource() == consoleGatewayGVR && a.GetVerb() == "patch" {
				if pa, ok := a.(interface{ GetName() string }); ok && pa.GetName() == consoleGatewayName {
					sawGatewayApply = true
				}
			}
		}
		if !sawGatewayApply {
			t.Errorf("[%s] no Gateway listener SSA patch on the secondary region", name)
		}
		// (b) console HTTPRoute created on the secondary region.
		if _, err := sec.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
			Get(ctx, "catalyst-ui-acme-omani-homes", metav1.GetOptions{}); err != nil {
			t.Errorf("[%s] console HTTPRoute not created on the secondary region: %v", name, err)
		}
		// (c) Certificate CR must stay HOST-only — no duplicate LE issuance.
		if _, err := sec.Resource(certificateGVR).Namespace(consoleCertNamespace).
			Get(ctx, "org-wildcard-tls-acme-omani-homes", metav1.GetOptions{}); err == nil {
			t.Errorf("[%s] Certificate CR must NOT be created on a secondary region", name)
		}
	}
	// (d) issued cert secret mirrored into each secondary's kube-system.
	for name, core := range map[string]*k8sfake.Clientset{"hel1-1": secCoreB, "nbg1-2": secCoreC} {
		got, err := core.CoreV1().Secrets(consoleCertNamespace).
			Get(ctx, "org-wildcard-tls-acme-omani-homes", metav1.GetOptions{})
		if err != nil {
			t.Errorf("[%s] cert secret not mirrored to the secondary region: %v", name, err)
			continue
		}
		if string(got.Data["tls.crt"]) != "host-issued-cert" || got.Type != corev1.SecretTypeTLS {
			t.Errorf("[%s] mirrored secret drifted: type=%s data=%v", name, got.Type, got.Data)
		}
	}
	// (e) host region unchanged by the fan-out — trio still applied there.
	if _, err := hostDyn.Resource(certificateGVR).Namespace(consoleCertNamespace).
		Get(ctx, "org-wildcard-tls-acme-omani-homes", metav1.GetOptions{}); err != nil {
		t.Errorf("host Certificate CR missing: %v", err)
	}
	if _, err := hostDyn.Resource(httpRouteGVR).Namespace(catalystConsoleNamespace).
		Get(ctx, "catalyst-ui-acme-omani-homes", metav1.GetOptions{}); err != nil {
		t.Errorf("host console HTTPRoute missing: %v", err)
	}
}

// TestProvisionOrgConsoleTLS_FanOutSkippedOffChroot locks the mothership
// guard: without SOVEREIGN_FQDN the k8sCache fan-out must NOT run (the
// mothership cache holds ALIEN deployments' clusters — #3987), so a
// registered non-chroot cluster receives nothing.
func TestProvisionOrgConsoleTLS_FanOutSkippedOffChroot(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "") // mothership: not a chroot
	hostDyn := fakeDynForConsoleTLS(t)
	alienDyn := fakeDynForConsoleTLS(t)
	h := newConsoleTLSHandler(t, hostDyn)
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clusters: []k8scache.ClusterRef{
			{ID: "dep291", DynamicClient: hostDyn, CoreClient: k8sfake.NewSimpleClientset()},
			{ID: "dep291-hel1-1", DynamicClient: alienDyn, CoreClient: k8sfake.NewSimpleClientset()},
		},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	alienDyn.ClearActions() // drop the builder's own gateway-seed Create

	h.provisionOrgConsoleTLS(context.Background(), store.OrganizationProvisionRecord{
		OrganizationID: "t-acme",
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
	})
	for _, a := range alienDyn.Actions() {
		if a.GetVerb() == "patch" || a.GetVerb() == "create" {
			t.Fatalf("mothership fan-out guard breached: %s on %v", a.GetVerb(), a.GetResource())
		}
	}
}

// TestEnsureOrgConsoleListener_RidesApexPorts is the end-to-end lock for
// #4732 item 2 through the SSA apply body: the per-Org listener pair in the
// patch must carry the apex ports.
func TestEnsureOrgConsoleListener_RidesApexPorts(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	names, ok := resolveOrgConsoleTLSNames(store.OrganizationProvisionRecord{
		Subdomain:    "nstar",
		DomainMode:   store.OrganizationDomainFreeSubdomain,
		ParentDomain: "omani.homes",
	})
	if !ok {
		t.Fatal("resolveOrgConsoleTLSNames returned !ok")
	}
	// The fake dynamic client does not implement server-side-apply merge
	// (same limitation TestProvisionOrgConsoleTLS_AppliesTrio documents) —
	// a returned error is tolerated; the assertion target is the recorded
	// patch BODY.
	_ = ensureOrgConsoleListener(context.Background(), dyn, names, store.OrganizationProvisionRecord{})
	var patched []byte
	for _, a := range dyn.Actions() {
		if a.GetResource() == consoleGatewayGVR && a.GetVerb() == "patch" {
			if pa, ok := a.(interface{ GetPatch() []byte }); ok {
				patched = pa.GetPatch()
			}
		}
	}
	if len(patched) == 0 {
		t.Fatal("no SSA patch recorded against the console gateway")
	}
	body := string(patched)
	if !strings.Contains(body, `"port":8443`) || !strings.Contains(body, `"port":8080`) {
		t.Errorf("per-Org listeners not on the apex 8443/8080 ports\npatch: %s", body)
	}
	if strings.Contains(body, `"port":31443`) || strings.Contains(body, `"port":31080`) {
		t.Errorf("per-Org listeners still carry the dead pre-#4718 31443/31080 ports\npatch: %s", body)
	}
}
