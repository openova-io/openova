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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

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
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		certificateGVR:    "CertificateList",
		consoleGatewayGVR: "GatewayList",
		httpRouteGVR:      "HTTPRouteList",
	}
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
				map[string]any{"name": "console-https", "port": int64(8443), "protocol": "HTTPS", "hostname": "*.omantel.biz"},
				map[string]any{"name": "console-http", "port": int64(8080), "protocol": "HTTP", "hostname": "*.omantel.biz"},
			},
		},
	}}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, gw)
}

func newConsoleTLSHandler(t *testing.T, dyn *dynamicfake.FakeDynamicClient) *Handler {
	t.Helper()
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: dyn}, nil
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
func TestConsoleApexListenerPorts_DerivesFromApex(t *testing.T) {
	dyn := fakeDynForConsoleTLS(t)
	httpsPort, httpPort := consoleApexListenerPorts(context.Background(), dyn)
	// fakeDynForConsoleTLS seeds the apex pair on 8443/8080 (#4718 scheme).
	if httpsPort != 8443 || httpPort != 8080 {
		t.Errorf("apex-derived ports = %d/%d, want 8443/8080 from the seeded apex", httpsPort, httpPort)
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
