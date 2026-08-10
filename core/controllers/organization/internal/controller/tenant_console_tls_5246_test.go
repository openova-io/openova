// tenant_console_tls_5246_test.go — #5246. The per-Org console listener pair
// must exist in EVERY region the console ELB pool forwards to, and an Org whose
// peer region never received it must not read as provisioned.
//
// THE DEFECT
// ----------
// The organization-controller Deployment exists in the region-A cluster only —
// measured on hw292 dep 1c56518035a83e03, `catalyst-system` carries the four
// Catalyst controllers in region A and nothing in region B. Every write in
// tenant_console_tls.go went through the manager's client, which is bound to
// the local apiserver, so `console-https-<slug>` / `console-http-<slug>` could
// only ever land in region A. Not "usually" — structurally.
//
// The console EIP does not share that limitation: #5246 made the ELB backend
// pool span every region's nodes so the EIP survives a region-kill, so customer
// TLS arrives at whichever region the pool picks. Measured live on hw292 with
// fresh-TCP sampling:
//
//	console.uatco.omani.homes   -> 200 on 5 of 10   (per-Org host)
//	console.hw292.omani.works   -> 200 on 10 of 10  (apex control, same VIP,
//	                                                 same ELB, same port)
//
// and, read straight off the two Gateways on 2026-08-10:
//
//	region A kube-system/cilium-gateway-console: …, console-https-uatco,
//	                                             console-http-uatco
//	region B kube-system/cilium-gateway-console: …, console-https-r17probe,
//	                                             console-http-r17probe
//
// Region B carried the pair for `r17probe`, an Org DELETED days earlier, and
// nothing for the live Org — one defect in both directions: the up-path never
// reached region B, and the teardown never reached it either.
//
// WHAT IS ASSERTED HERE
// ---------------------
// Every assertion is on the VALUE in the peer region — the listener's hostname,
// port and certificateRef — not on a key existing. Each carries a control that
// answers the other way in the same fixture, so none of them can pass vacuously:
//
//  1. The pair lands in the secondary region, on THAT region's apex ports
//     (a sibling Org's name is absent from the same Gateway — the control
//     proving the presence assertion discriminates), and the issued TLS Secret
//     is mirrored so the listener has material to terminate on.
//  2. verifyProvisioned holds the Org back when the secondary region lacks the
//     pair, naming the region; the same fixture with the pair present in both
//     regions reports complete.
//  3. A region the ClusterMesh witness proves exists but for which no
//     kubeconfig is wired is reported as a MISSING artifact — the control being
//     the single-region Sovereign (no mesh Secret), which stays clean.
//  4. Teardown strips the pair in every region, leaving every other listener
//     in both regions byte-for-byte intact.
package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

const (
	// secondaryRegionKey mirrors the live hw292 bridge-Secret data key
	// `me-east-215-b.yaml`.
	secondaryRegionKey = "me-east-215-b"
	// meshRemoteCluster mirrors the live hw292 region-A ClusterMesh config
	// Secret, whose only non-certificate key names the remote cluster.
	meshRemoteCluster = "hw292-me-east-b"
)

// regionGatewayListeners returns an apex console listener pair on the given
// ports. Each region publishes its own apex pair, and the per-Org listeners
// must ride the ports of the region they are written into (#5511) — so the two
// regions in these tests deliberately run DIFFERENT apex ports.
func regionGatewayListeners(httpsPort, httpPort int64) []any {
	return []any{
		map[string]any{
			"name": consoleApexListenerHTTPSName, "hostname": "console.hw292.omani.works",
			"port": httpsPort, "protocol": "HTTPS",
		},
		map[string]any{
			"name": consoleApexListenerHTTPName, "hostname": "console.hw292.omani.works",
			"port": httpPort, "protocol": "HTTP",
		},
	}
}

// newRegionScheme registers everything a region cluster's fake client must be
// able to serve: core types (the mirrored TLS Secret) plus the Certificate and
// Gateway GVKs the console-TLS path writes as Unstructured.
func newRegionScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	for _, gvk := range []schema.GroupVersionKind{certificateGVK, gatewayGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

// newRegionCluster builds a peer region's fake cluster carrying a console
// Gateway with the given listeners, and a published status.listeners that
// mirrors spec (the Gateway controller having admitted everything). Extra
// spec-only listeners can be injected by the caller afterwards.
func newRegionCluster(t *testing.T, listeners []any) client.Client {
	t.Helper()
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	gw.SetName(consoleGatewayDefaultName)
	gw.SetNamespace(consoleGatewayDefaultNamespace)
	gw.Object["spec"] = map[string]any{"gatewayClassName": "cilium"}
	if err := unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners"); err != nil {
		t.Fatalf("seed region listeners: %v", err)
	}
	if err := unstructured.SetNestedSlice(gw.Object, statusFor(listeners), "status", "listeners"); err != nil {
		t.Fatalf("seed region status listeners: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(newRegionScheme(t)).WithObjects(gw).Build()
}

// statusFor renders the status.listeners projection of a spec listener slice —
// the Gateway controller having ADMITTED every listener it was given.
func statusFor(listeners []any) []any {
	out := make([]any, 0, len(listeners))
	for _, l := range listeners {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, map[string]any{"name": m["name"]})
	}
	return out
}

// multiRegionOrg is the live hw292 shape: a free-subdomain pool Org whose
// console host is `console.uatco.omani.homes`.
func multiRegionOrg() *orgapi.Organization {
	org := sampleOrg()
	org.Name = "uatco"
	org.Spec.Slug = "uatco"
	org.Spec.TenantPublic.Subdomain = "uatco"
	org.Spec.TenantPublic.ParentDomain = "omani.homes"
	return org
}

// multiRegionFixture wires a 2-region Sovereign: a host cluster carrying the
// console Gateway, the ClusterMesh witness, the #5359 bridge Secret and the
// ISSUED per-Org TLS Secret; plus one peer-region cluster reached through the
// RegionClientBuilder seam.
//
// withMesh=false drops the ClusterMesh Secret — the single-region control.
// withBridge=false drops the kubeconfig Secret — the "mesh says B exists but
// nothing is wired to reach it" case.
type multiRegionFixture struct {
	r      *Reconciler
	org    *orgapi.Organization
	names  orgConsoleTLSNames
	region client.Client
}

func newMultiRegionFixture(t *testing.T, withMesh, withBridge bool, regionListeners []any) multiRegionFixture {
	t.Helper()
	org := multiRegionOrg()
	names, ok := orgConsoleTLSNamesForOrg(org)
	if !ok {
		t.Fatalf("fixture Org does not engage the console-TLS up-path")
	}

	// Host region: apex pair on 8443/8080 (the live hw292 ports).
	r := consoleTLSReconciler(t, org, regionGatewayListeners(8443, 8080))
	seedHostGatewayStatus(t, r)

	// The plan-boundary artifacts verifyProvisioned also checks (#5395) —
	// seeded so the region assertions below are the ONLY thing that can move
	// the verdict.
	lr := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: gitops.BoundaryLimitRangeName, Namespace: org.Spec.Slug}}
	if err := r.Create(context.Background(), lr); err != nil {
		t.Fatalf("seed boundary LimitRange: %v", err)
	}
	if gitops.PlanRendersResourceQuota(org.Spec.PlanSlug) {
		rq := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: gitops.BoundaryResourceQuotaName, Namespace: org.Spec.Slug}}
		if err := r.Create(context.Background(), rq); err != nil {
			t.Fatalf("seed boundary ResourceQuota: %v", err)
		}
	}

	// The issued per-Org TLS Secret in the host region — what gets mirrored.
	issued := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: names.CertName, Namespace: consoleTLSDefaultCertNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("CERT-BYTES"), "tls.key": []byte("KEY-BYTES")},
	}
	if err := r.Create(context.Background(), issued); err != nil {
		t.Fatalf("seed issued TLS Secret: %v", err)
	}

	if withMesh {
		mesh := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: consoleClusterMeshSecretDefaultName, Namespace: consoleClusterMeshSecretDefaultNamespace},
			Data: map[string][]byte{
				meshRemoteCluster:             []byte("endpoints: [https://10.218.1.5:2379]"),
				meshRemoteCluster + "-ca.crt": []byte("CA"),
				meshRemoteCluster + ".crt":    []byte("CRT"),
				meshRemoteCluster + ".key":    []byte("KEY"),
			},
		}
		if err := r.Create(context.Background(), mesh); err != nil {
			t.Fatalf("seed ClusterMesh witness: %v", err)
		}
	}

	// Peer region: apex pair on 9443/9080 — deliberately DIFFERENT from the
	// host so the per-Org port must be derived from the region being written.
	peer := newRegionCluster(t, regionListeners)

	if withBridge {
		bridge := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      consoleSecondaryKubeconfigSecretDefaultName,
				Namespace: consoleSecondaryKubeconfigSecretDefaultNamespace,
			},
			Data: map[string][]byte{secondaryRegionKey + ".yaml": []byte("kubeconfig-for-" + secondaryRegionKey)},
		}
		if err := r.Create(context.Background(), bridge); err != nil {
			t.Fatalf("seed secondary-region kubeconfig bridge: %v", err)
		}
	}

	r.RegionClientBuilder = func(kubeconfig []byte) (client.Client, error) {
		if string(kubeconfig) != "kubeconfig-for-"+secondaryRegionKey {
			t.Fatalf("region client built from unexpected kubeconfig %q", string(kubeconfig))
		}
		return peer, nil
	}

	return multiRegionFixture{r: r, org: org, names: names, region: peer}
}

// seedHostGatewayStatus publishes status.listeners on the host Gateway so the
// #5511 admission check is decidable (an empty status is Unverifiable, which
// would otherwise mask the region assertions under test).
func seedHostGatewayStatus(t *testing.T, r *Reconciler) {
	t.Helper()
	syncGatewayStatus(t, r.Client)
}

// syncGatewayStatus republishes a cluster's status.listeners from its current
// spec.listeners — the Gateway controller having admitted everything.
func syncGatewayStatus(t *testing.T, c client.Client) {
	t.Helper()
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	key := client.ObjectKey{Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName}
	if err := c.Get(context.Background(), key, gw); err != nil {
		t.Fatalf("read Gateway for status sync: %v", err)
	}
	spec, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read spec.listeners for status sync: %v", err)
	}
	if err := unstructured.SetNestedSlice(gw.Object, statusFor(spec), "status", "listeners"); err != nil {
		t.Fatalf("set status.listeners: %v", err)
	}
	if err := c.Update(context.Background(), gw); err != nil {
		t.Fatalf("update Gateway status: %v", err)
	}
}

// listenerIn returns the named listener from a cluster's console Gateway spec,
// or nil when absent.
func listenerIn(t *testing.T, c client.Client, name string) map[string]any {
	t.Helper()
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName,
	}, gw); err != nil {
		t.Fatalf("read console Gateway: %v", err)
	}
	listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read spec.listeners: %v", err)
	}
	for _, l := range listeners {
		m, ok := l.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := m["name"].(string); n == name {
			return m
		}
	}
	return nil
}

// listenerNamesIn returns every listener name on a cluster's console Gateway.
func listenerNamesIn(t *testing.T, c client.Client) []string {
	t.Helper()
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK)
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: consoleGatewayDefaultNamespace, Name: consoleGatewayDefaultName,
	}, gw); err != nil {
		t.Fatalf("read console Gateway: %v", err)
	}
	listeners, _, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil {
		t.Fatalf("read spec.listeners: %v", err)
	}
	out := make([]string, 0, len(listeners))
	for _, l := range listeners {
		if m, ok := l.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// certRefName digs the terminating Secret name out of an HTTPS listener.
func certRefName(t *testing.T, l map[string]any) string {
	t.Helper()
	refs, found, err := unstructured.NestedSlice(l, "tls", "certificateRefs")
	if err != nil || !found || len(refs) == 0 {
		t.Fatalf("listener %v carries no tls.certificateRefs (found=%v err=%v)", l["name"], found, err)
	}
	m, ok := refs[0].(map[string]any)
	if !ok {
		t.Fatalf("certificateRefs[0] is not a map: %T", refs[0])
	}
	name, _ := m["name"].(string)
	return name
}

// ── 1. the pair lands in the secondary region ────────────────────────────────

func TestConsoleOrgListener_LandsInEverySecondaryRegion_5246(t *testing.T) {
	// The peer region starts with its apex pair ONLY — the live hw292 region-B
	// shape for a live Org.
	f := newMultiRegionFixture(t, true, true, regionGatewayListeners(9443, 9080))

	if _, err := f.r.reconcileTenantConsoleTLS(context.Background(), f.org); err != nil {
		t.Fatalf("reconcileTenantConsoleTLS: %v", err)
	}

	// The defect, asserted on the VALUE in the peer region.
	https := listenerIn(t, f.region, f.names.HTTPSName)
	if https == nil {
		t.Fatalf("secondary region %s carries NO %s listener — customer TLS arriving there resets (#5246). listeners=%v",
			secondaryRegionKey, f.names.HTTPSName, listenerNamesIn(t, f.region))
	}
	if got, _ := https["hostname"].(string); got != f.names.WildcardHost {
		t.Errorf("secondary region %s listener hostname = %q, want %q", secondaryRegionKey, got, f.names.WildcardHost)
	}
	// The port must come from the region being written, not from the host's
	// apex pair — the two regions run different apex ports in this fixture.
	if got, _ := listenerPort(https); got != 9443 {
		t.Errorf("secondary region %s listener port = %d, want 9443 (that region's own apex console-https port; the console ELB forwards to nothing else)", secondaryRegionKey, got)
	}
	if got := certRefName(t, https); got != f.names.CertName {
		t.Errorf("secondary region %s listener certificateRef = %q, want %q", secondaryRegionKey, got, f.names.CertName)
	}
	httpL := listenerIn(t, f.region, f.names.HTTPName)
	if httpL == nil {
		t.Fatalf("secondary region %s carries no %s listener", secondaryRegionKey, f.names.HTTPName)
	}
	if got, _ := listenerPort(httpL); got != 9080 {
		t.Errorf("secondary region %s HTTP listener port = %d, want 9080", secondaryRegionKey, got)
	}

	// CONTROL — the same probe against a name that was never written must
	// answer the other way. Without this, "listenerIn returned non-nil" could
	// be satisfied by any lookup bug that returns the first listener.
	if sibling := listenerIn(t, f.region, "console-https-neverprovisioned"); sibling != nil {
		t.Fatalf("control failed: the presence probe reports a listener that was never written: %v", sibling)
	}

	// The host region keeps its own apex-derived ports — the fan-out must not
	// have written the peer's ports into the host.
	hostHTTPS := listenerIn(t, f.r.Client, f.names.HTTPSName)
	if hostHTTPS == nil {
		t.Fatalf("host region lost its %s listener", f.names.HTTPSName)
	}
	if got, _ := listenerPort(hostHTTPS); got != 8443 {
		t.Errorf("host region listener port = %d, want 8443", got)
	}

	// The peer's listener needs material to terminate on: the issued Secret is
	// mirrored, never a second Certificate (one Let's-Encrypt issuance per Org).
	mirrored := &corev1.Secret{}
	if err := f.region.Get(context.Background(), client.ObjectKey{
		Namespace: consoleTLSDefaultCertNamespace, Name: f.names.CertName,
	}, mirrored); err != nil {
		t.Fatalf("issued TLS Secret was not mirrored into region %s: %v", secondaryRegionKey, err)
	}
	if string(mirrored.Data["tls.crt"]) != "CERT-BYTES" {
		t.Errorf("mirrored TLS Secret tls.crt = %q, want the host's issued bytes", string(mirrored.Data["tls.crt"]))
	}
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	if err := f.region.Get(context.Background(), client.ObjectKey{
		Namespace: consoleTLSDefaultCertNamespace, Name: f.names.CertName,
	}, cert); err == nil {
		t.Errorf("a second cert-manager Certificate was created in region %s — both regions would solve the same DNS-01 challenge and burn two LE issuances for one SAN", secondaryRegionKey)
	}

	// Idempotence: a second pass changes nothing and still errors on nothing.
	changed, err := f.r.reconcileTenantConsoleTLS(context.Background(), f.org)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if changed {
		t.Errorf("second pass reported changed=true on a converged 2-region Sovereign — the fan-out is not idempotent")
	}
}

// ── 2. an unconverged secondary region holds the Org back ────────────────────

func TestVerifyProvisioned_SecondaryRegionMissingListenerIsNotProvisioned_5246(t *testing.T) {
	// Peer region carries its apex pair only — the pair for this Org is absent.
	f := newMultiRegionFixture(t, true, true, regionGatewayListeners(9443, 9080))
	// Host region IS converged: write the pair there and publish status.
	if _, err := f.r.ensureConsoleOrgListener(context.Background(), f.r.Client, f.names); err != nil {
		t.Fatalf("seed host listener: %v", err)
	}
	syncGatewayStatus(t, f.r.Client)

	got := f.r.verifyProvisioned(context.Background(), f.org)
	if got.complete() {
		t.Fatalf("Organization reported fully provisioned while region %s carries no %s listener — that is the hw292 shape: every status surface green, half the customer connections reset. missing=%v unverifiable=%v",
			secondaryRegionKey, f.names.HTTPSName, got.Missing, got.Unverifiable)
	}
	msg := got.message()
	if !strings.Contains(msg, secondaryRegionKey) {
		t.Errorf("the NOT-provisioned message does not name the region that is missing the listener — an operator cannot act on it.\ngot: %s", msg)
	}
	if !strings.Contains(msg, f.names.HTTPSName) {
		t.Errorf("the NOT-provisioned message does not name the listener.\ngot: %s", msg)
	}

	// CONTROL — converge the peer region and the SAME check must go green.
	// Without this the assertion above could be satisfied by a verifier that
	// never returns complete.
	if _, err := f.r.ensureConsoleOrgListener(context.Background(), f.region, f.names); err != nil {
		t.Fatalf("converge peer region: %v", err)
	}
	syncGatewayStatus(t, f.region)
	got = f.r.verifyProvisioned(context.Background(), f.org)
	if !got.complete() {
		t.Fatalf("control failed: with the pair present in BOTH regions the Org still reads NOT provisioned. missing=%v unverifiable=%v",
			got.Missing, got.Unverifiable)
	}
}

// ── 3. a meshed region with no kubeconfig is a MISSING artifact ──────────────

func TestConsoleRegionTargets_MeshedRegionWithNoKubeconfigIsUnwired_5246(t *testing.T) {
	// Mesh declares a remote cluster; nothing is wired to reach it.
	f := newMultiRegionFixture(t, true, false, regionGatewayListeners(9443, 9080))

	res := f.r.consoleRegionTargets(context.Background())
	if len(res.Targets) != 1 || !res.Targets[0].Host {
		t.Fatalf("expected only the host target when no kubeconfig is wired, got %d targets", len(res.Targets))
	}
	if len(res.Unwired) == 0 {
		t.Fatalf("a Sovereign whose ClusterMesh declares %q while zero kubeconfigs are wired reported NO shortfall — the region set would silently shrink to one and every downstream 'written in every region' claim would be made over it (#5246)", meshRemoteCluster)
	}

	// It must reach the operator as a MISSING artifact on the Org, not a log line.
	if _, err := f.r.ensureConsoleOrgListener(context.Background(), f.r.Client, f.names); err != nil {
		t.Fatalf("seed host listener: %v", err)
	}
	syncGatewayStatus(t, f.r.Client)
	got := f.r.verifyProvisioned(context.Background(), f.org)
	if got.complete() {
		t.Fatalf("Org reads fully provisioned while a meshed region has no credential to receive its listener. missing=%v", got.Missing)
	}

	// CONTROL — a genuine SINGLE-region Sovereign has no ClusterMesh Secret,
	// declares zero remotes, and must stay completely clean. Without this the
	// shortfall check would red-flag every single-region install.
	single := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	sres := single.r.consoleRegionTargets(context.Background())
	if len(sres.Unwired) != 0 || len(sres.Unreachable) != 0 {
		t.Fatalf("control failed: a single-region Sovereign reported unwired=%v unreachable=%v", sres.Unwired, sres.Unreachable)
	}
	if _, err := single.r.ensureConsoleOrgListener(context.Background(), single.r.Client, single.names); err != nil {
		t.Fatalf("seed single-region listener: %v", err)
	}
	syncGatewayStatus(t, single.r.Client)
	if sgot := single.r.verifyProvisioned(context.Background(), single.org); !sgot.complete() {
		t.Fatalf("control failed: a converged single-region Sovereign reads NOT provisioned. missing=%v unverifiable=%v",
			sgot.Missing, sgot.Unverifiable)
	}
}

// ── 4. teardown strips the pair in every region ──────────────────────────────

func TestTeardownConsoleTLS_StripsListenerInEveryRegion_5246(t *testing.T) {
	f := newMultiRegionFixture(t, true, true, regionGatewayListeners(9443, 9080))
	if _, err := f.r.reconcileTenantConsoleTLS(context.Background(), f.org); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if listenerIn(t, f.region, f.names.HTTPSName) == nil {
		t.Fatalf("fixture precondition: peer region never received the pair")
	}
	// A sibling Org's pair on the SAME peer Gateway — teardown must not touch it.
	siblingNames := orgConsoleTLSNamesFor("sibling", "omani.homes")
	if _, err := f.r.ensureConsoleOrgListener(context.Background(), f.region, siblingNames); err != nil {
		t.Fatalf("seed sibling listener: %v", err)
	}

	if _, err := f.r.teardownTenantConsoleTLS(context.Background(), f.org); err != nil {
		t.Fatalf("teardownTenantConsoleTLS: %v", err)
	}

	for _, name := range []string{f.names.HTTPSName, f.names.HTTPName} {
		if l := listenerIn(t, f.region, name); l != nil {
			t.Errorf("peer region still carries %s after teardown — this is the live hw292 r17probe orphan: a DELETED Org's listeners surviving in region B while the live Org has none there", name)
		}
		if l := listenerIn(t, f.r.Client, name); l != nil {
			t.Errorf("host region still carries %s after teardown", name)
		}
	}
	// CONTROL — the sibling Org's pair and both apex listeners survive intact,
	// proving the strip is name-scoped and not a wipe.
	for _, name := range []string{siblingNames.HTTPSName, siblingNames.HTTPName, consoleApexListenerHTTPSName, consoleApexListenerHTTPName} {
		if l := listenerIn(t, f.region, name); l == nil {
			t.Errorf("teardown removed %s from the peer region — it must strip only this Org's pair. listeners=%v", name, listenerNamesIn(t, f.region))
		}
	}
	// The mirrored TLS Secret goes with it — otherwise a future Org taking the
	// same subdomain lands on a Secret of exactly the expected name holding the
	// PREVIOUS Org's certificate (the R17 cross-Org identity leak).
	mirrored := &corev1.Secret{}
	if err := f.region.Get(context.Background(), client.ObjectKey{
		Namespace: consoleTLSDefaultCertNamespace, Name: f.names.CertName,
	}, mirrored); err == nil {
		t.Errorf("mirrored TLS Secret survived teardown in region %s — a future Org on the same subdomain would inherit this Org's certificate", secondaryRegionKey)
	}
}
