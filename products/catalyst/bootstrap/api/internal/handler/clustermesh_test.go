// Tests for Cilium ClusterMesh auto-establishment.
//
// Coverage:
//   - happy path: 2 regions, both LBs present, both CAs present, both
//     peers wired
//   - LB absent in one region: peer marked Connected=false, no orchestrator
//     error returned
//   - idempotent re-run: second invocation produces identical Secret
//     bytes, no change, final status identical
//   - single-region: orchestrator returns (nil, nil) without touching
//     any client
//   - invariant A3: NodePort-typed clustermesh-apiserver Service yields
//     a hard error per region
//
// Tests use kfake.NewSimpleClientset with a per-region client wired via
// SetClusterMeshClientFactory so no real cluster is needed. CA material
// is generated once per test from a small in-process RSA keypair.
package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// ── test helpers ────────────────────────────────────────────────────

// genCAForTest mints an RSA-2048 self-signed cert + key. Returned PEM
// blocks are dropped into the kube-system/cilium-ca Secret of each fake
// cluster.
func genCAForTest(t *testing.T, cn string) (caCertPEM, caKeyPEM []byte) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)})
	return caCertPEM, caKeyPEM
}

// buildFakeClusterMeshCluster returns a fake clientset pre-seeded with:
//   - kube-system/cilium-ca Secret (ca.crt / ca.key bytes)
//   - kube-system/clustermesh-apiserver Service of type LoadBalancer
//     with ingress IP set IF lbIP != "".
//   - kube-system/cilium DaemonSet + cilium-operator Deployment +
//     clustermesh-apiserver Deployment so the rollout-restart Patch
//     calls have targets and don't IsNotFound (kept silent).
func buildFakeClusterMeshCluster(t *testing.T, lbIP string, caCert, caKey []byte) kubernetes.Interface {
	t.Helper()
	cs := kfake.NewSimpleClientset()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: clusterMeshNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := cs.CoreV1().Secrets(clusterMeshNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: clusterMeshCASecretName, Namespace: clusterMeshNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt": caCert,
			"ca.key": caKey,
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create cilium-ca: %v", err)
	}
	// clustermesh-apiserver-remote-cert — the upstream chart generates
	// this with CN=remote. orchestrator's snapshotRemoteCert reads
	// these bytes verbatim and writes them as the peer client cert in
	// the REMOTE cluster's cilium-clustermesh Secret. Use the same
	// fake CA bytes as the cilium-ca Secret so the seeded "cert" is
	// at least syntactically a PEM blob.
	if _, err := cs.CoreV1().Secrets(clusterMeshNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "clustermesh-apiserver-remote-cert", Namespace: clusterMeshNamespace},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":  caCert,
			"tls.crt": caCert,
			"tls.key": caKey,
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create clustermesh-apiserver-remote-cert: %v", err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: clusterMeshApiserverService, Namespace: clusterMeshNamespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	if lbIP != "" {
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: lbIP}}
	}
	if _, err := cs.CoreV1().Services(clusterMeshNamespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create clustermesh-apiserver Service: %v", err)
	}
	return cs
}

// seedCNPGPairSourceSecrets pre-creates the namespace `cnpg` and the two
// primary-side CNPG replication-auth Secrets the slot-16b flip copies to
// every replica (#3254). Called on the PRIMARY fake clientset so the
// happy-path flip succeeds; tests that exercise the missing-source guard
// delete one before invoking the orchestrator.
func seedCNPGPairSourceSecrets(t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: cnpgPairNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create cnpg namespace: %v", err)
	}
	secrets := map[string]map[string][]byte{
		cnpgPairReplicationCert: {"tls.crt": []byte("REPL-CERT"), "tls.key": []byte("REPL-KEY")},
		cnpgPairReplicationCA:   {"ca.crt": []byte("REPL-CA")},
	}
	for name, data := range secrets {
		typ := corev1.SecretTypeTLS
		if name == cnpgPairReplicationCA {
			typ = corev1.SecretTypeOpaque
		}
		if _, err := cs.CoreV1().Secrets(cnpgPairNamespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: cnpgPairNamespace,
				Labels:    map[string]string{"cnpg.io/cluster": cnpgPairReleaseFullname + "-primary"},
			},
			Type: typ,
			Data: data,
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create source Secret %s: %v", name, err)
		}
	}
}

// seedCNPGPairReplicaNamespace pre-creates the `cnpg` namespace on a
// replica fake clientset, matching production where slot 16b ships the
// Namespace unconditionally on every region. Without it the copy's
// Create would fail "namespaces \"cnpg\" not found" — but in the
// fake clientset Create into a missing namespace succeeds, so this is
// here for parity/clarity rather than strict necessity.
func seedCNPGPairReplicaNamespace(t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: cnpgPairNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create cnpg namespace on replica: %v", err)
	}
}

// writeFakeKubeconfig drops a stub kubeconfig YAML into the directory
// at the path AutoEstablishClusterMesh expects. The content is parsed
// by helmwatch.NewKubernetesClientFromKubeconfig — but our test path
// short-circuits that by wiring a clusterMeshClientFactory below, so
// the YAML body is only required to satisfy the os.Stat existence
// check inside buildRegionSlots.
func writeFakeKubeconfig(t *testing.T, dir, depID, regionKey string) string {
	t.Helper()
	filename := depID + ".yaml"
	if regionKey != "" {
		filename = depID + "-" + regionKey + ".yaml"
	}
	path := filepath.Join(dir, filename)
	// A minimal valid YAML kubeconfig; the test factory intercepts the
	// build so the bytes don't need to point at a real cluster, but
	// helmwatch.NewKubernetesClientFromKubeconfig is invoked as a
	// fallback in production. Tests inject via the factory.
	body := []byte(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://fake.test
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user: {}
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// testFixture wraps a Handler with the per-region fake clientsets
// pre-built. The handler's k8s client factory is monkeypatched via
// the package-private helper installClusterMeshClientFactory so we
// don't have to plumb a real kubeconfig parser.
type testFixture struct {
	dir                     string
	clients                 map[string]kubernetes.Interface // keyed by kubeconfig path
	dynClients              map[string]dynamic.Interface    // keyed by kubeconfig path (EVERY region — split-side flip patches all)
	primaryKubeconfigPath   string
	secondaryKubeconfigPath string // first secondary region's path ("" when single-region)
	dep                     *Deployment
	handler                 *Handler
}

// installClusterMeshClientFactory replaces helmwatch's kubeconfig
// parser at test time. Production reads the file then calls
// helmwatch.NewKubernetesClientFromKubeconfig; tests intercept by
// replacing the package-level helper via a small indirection in
// buildRegionSlots. For the test path we instead inject the clientset
// via a per-deployment override map.
//
// The override is implemented as a package-level variable in
// clustermesh.go that defaults to nil; tests set it to a map keyed by
// kubeconfig path. Production never touches this variable.
func installClusterMeshClientFactory(clients map[string]kubernetes.Interface) func() {
	prev := clusterMeshTestClientFactory
	clusterMeshTestClientFactory = func(kcPath string) (kubernetes.Interface, bool) {
		c, ok := clients[kcPath]
		return c, ok
	}
	return func() { clusterMeshTestClientFactory = prev }
}

func newTestFixture(t *testing.T, lbIPs []string) *testFixture {
	t.Helper()
	dir := t.TempDir()
	caCert, caKey := genCAForTest(t, "test-mesh-ca")

	depID := "depclustermesh"
	clients := map[string]kubernetes.Interface{}
	regions := []provisioner.RegionSpec{}
	for i, lbIP := range lbIPs {
		cloudRegion := "fsn1"
		if i == 1 {
			cloudRegion = "hel1"
		}
		if i == 2 {
			cloudRegion = "nbg1"
		}
		regionKey := ""
		clusterName := "test-mesh"
		if i > 0 {
			regionKey = "" // computed below
		}
		_ = clusterName
		// Note: we let buildRegionSlots derive the key + name from
		// dep.Request.Regions, matching production.
		regions = append(regions, provisioner.RegionSpec{
			Provider:    "hetzner",
			CloudRegion: cloudRegion,
		})
		// Write a kubeconfig file at the expected path so the os.Stat
		// existence check passes; the factory intercepts the actual
		// parse.
		key := regionKeyFromSpec(regions[i], i)
		_ = regionKey
		kcPath := writeFakeKubeconfig(t, dir, depID, key)
		cs := buildFakeClusterMeshCluster(t, lbIP, caCert, caKey)
		// #3254: seed the cnpg-pair replica-auth Secrets on the PRIMARY
		// (idx 0) so the slot-16b flip's cross-cluster copy succeeds; seed
		// the `cnpg` namespace on each replica (matches slot 16b shipping
		// the Namespace unconditionally on every region). Tests exercising
		// the missing-source guard delete a source Secret afterwards.
		if i == 0 {
			seedCNPGPairSourceSecrets(t, cs)
		} else {
			seedCNPGPairReplicaNamespace(t, cs)
		}
		clients[kcPath] = cs
	}

	dep := &Deployment{
		ID:        depID,
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN:   "tcm.example.io",
			ClusterMeshName: "test-mesh",
			ClusterMeshID:   100,
			Regions:         regions,
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "tcm.example.io",
			KubeconfigPath: filepath.Join(dir, depID+".yaml"),
		},
		secondaryKubeconfigPaths: map[string]string{},
	}
	// Populate secondaries map (the production path also sets this on
	// PutKubeconfig). The buildRegionSlots fallback handles missing
	// entries via filesystem stat, but explicit map entries are the
	// canonical path.
	for i := 1; i < len(regions); i++ {
		k := regionKeyFromSpec(regions[i], i)
		dep.secondaryKubeconfigPaths[k] = filepath.Join(dir, depID+"-"+k+".yaml")
	}

	// Per-region fake DYNAMIC clients — each carries the bootstrap-kit
	// Flux Kustomization the cnpg-pair gate flip (#3236) patches. Since
	// the chart-0.2.0 split-side topology, the flip lands on EVERY
	// region's Kustomization (each cluster renders its own half of the
	// pair), so every region gets its own seeded fake. Seeded with the
	// canonical 2-region substitute map cloud-init stamps on every
	// control plane (distinct non-empty SOVEREIGN_PRIMARY_REGION /
	// SOVEREIGN_REPLICA_REGION — the tftpl stamps both keys in the
	// shared block). Tests that exercise the precondition guard replace
	// entries before installing the factories.
	primaryKubeconfigPath := filepath.Join(dir, depID+".yaml")
	dynClients := map[string]dynamic.Interface{}
	secondaryKubeconfigPath := ""
	for i := range regions {
		key := regionKeyFromSpec(regions[i], i)
		kcPath := primaryKubeconfigPath
		if i > 0 {
			kcPath = filepath.Join(dir, depID+"-"+key+".yaml")
			if secondaryKubeconfigPath == "" {
				secondaryKubeconfigPath = kcPath
			}
		}
		dynClients[kcPath] = newFakeKustomizationDynClient(t,
			buildBootstrapKitKustomization(defaultBootstrapKitSubstitute()))
	}

	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	return &testFixture{
		dir:                     dir,
		clients:                 clients,
		dynClients:              dynClients,
		primaryKubeconfigPath:   primaryKubeconfigPath,
		secondaryKubeconfigPath: secondaryKubeconfigPath,
		dep:                     dep,
		handler:                 h,
	}
}

// ── ClusterMesh-gated cnpg-pair enable (#3236) — test helpers ───────

// Canonical region labels matching the shape cloud-init stamps into the
// bootstrap-kit substitute map on a 2-region prov (tftpl
// primary_region_canonical_label / replica_region_canonical_label).
const (
	testPrimaryRegionLabel = "hw-mea-rtz-prod"
	testReplicaRegionLabel = "hw-meb-rtz-prod"
)

// buildBootstrapKitKustomization returns the flux-system/bootstrap-kit
// Kustomization (kustomize.toolkit.fluxcd.io/v1) shaped like the one
// cloud-init applies (infra/providers/_shared/cloudinit-control-plane
// .tftpl flux-bootstrap.yaml), carrying the given postBuild.substitute
// map.
func buildBootstrapKitKustomization(substitute map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata": map[string]any{
			"name":      bootstrapKitKustomizationName,
			"namespace": fluxSystemNamespace,
		},
		"spec": map[string]any{
			"interval": "5m",
			"path":     "./clusters/_template/bootstrap-kit",
			"postBuild": map[string]any{
				"substitute": substitute,
			},
		},
	}}
}

// defaultBootstrapKitSubstitute mirrors the substitute keys a 2-region
// prov carries: distinct, non-empty primary/replica regions. The cnpg-
// pair gate key is ABSENT (the provisioner never registers it — Flux's
// inline `:-false` default keeps slot 16b off until the flip).
func defaultBootstrapKitSubstitute() map[string]any {
	return map[string]any{
		"SOVEREIGN_FQDN":                      "tcm.example.io",
		"SOVEREIGN_ENABLE_HOT_STANDBY":        "true",
		clusterMeshPrimaryRegionSubstituteKey: testPrimaryRegionLabel,
		clusterMeshReplicaRegionSubstituteKey: testReplicaRegionLabel,
	}
}

// newFakeKustomizationDynClient builds a fake dynamic client that knows
// the Flux Kustomization GVR, seeded with the given objects.
func newFakeKustomizationDynClient(t *testing.T, objs ...runtime.Object) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList",
	}, &unstructured.UnstructuredList{})
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{fluxKustomizationGVR: "KustomizationList"},
		objs...)
}

// installClusterMeshDynamicClientFactory mirrors
// installClusterMeshClientFactory for the dynamic-client path used by
// the cnpg-pair gate flip. Returns a restore func.
func installClusterMeshDynamicClientFactory(clients map[string]dynamic.Interface) func() {
	prev := clusterMeshTestDynamicClientFactory
	clusterMeshTestDynamicClientFactory = func(kcPath string) (dynamic.Interface, bool) {
		c, ok := clients[kcPath]
		return c, ok
	}
	return func() { clusterMeshTestDynamicClientFactory = prev }
}

// getBootstrapKitKustomization re-reads the Kustomization from the fake
// dynamic client so assertions observe post-patch state.
func getBootstrapKitKustomization(t *testing.T, dyn dynamic.Interface) *unstructured.Unstructured {
	t.Helper()
	ks, err := dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
		Get(context.Background(), bootstrapKitKustomizationName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get bootstrap-kit Kustomization: %v", err)
	}
	return ks
}

// ── tests ───────────────────────────────────────────────────────────

// TestAutoEstablishClusterMesh_HappyPath_TwoRegions — both regions
// have LB IPs + CA bytes; the orchestrator wires each as a peer of
// the other and returns Connected=true on every PeerStatus.
func TestAutoEstablishClusterMesh_HappyPath_TwoRegions(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses len = %d, want 2", len(statuses))
	}
	for _, st := range statuses {
		if st.LoadBalancerIP == "" {
			t.Errorf("region %q LB IP empty", st.RegionKey)
		}
		if len(st.Peers) != 1 {
			t.Errorf("region %q peers len = %d, want 1", st.RegionKey, len(st.Peers))
		}
		for _, p := range st.Peers {
			if !p.Connected {
				t.Errorf("region %q peer %q Connected=false, error=%q",
					st.RegionKey, p.Name, p.Error)
			}
		}
		if st.ReadyAt.IsZero() {
			t.Errorf("region %q ReadyAt not stamped despite all peers connected", st.RegionKey)
		}
	}

	// Verify each region's cilium-clustermesh Secret carries the peer
	// entries for the OTHER region.
	for kcPath, client := range fx.clients {
		secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(context.Background(), clusterMeshSecretName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Get cilium-clustermesh in %q: %v", kcPath, err)
		}
		// One peer per region (the other one): expect 4 keys (config
		// blob + -ca.crt + .crt + .key).
		if len(secret.Data) != 4 {
			t.Errorf("Secret in %q has %d entries, want 4 (got keys %v)",
				kcPath, len(secret.Data), secretKeys(secret))
		}
	}
}

// TestAutoEstablishClusterMesh_LBAbsentInOneRegion — region B has no
// LoadBalancer ingress; the orchestrator marks B's peer entries on A
// as Connected=false but does NOT return an error.
func TestAutoEstablishClusterMesh_LBAbsentInOneRegion(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", ""})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()
	// Shrink the LB lookup timeout so the test isn't slow.
	// (We reach into the package-level const indirectly via a test
	// hook below.)
	prev := clusterMeshTestOverrideLBTimeout
	clusterMeshTestOverrideLBTimeout = 200 * time.Millisecond
	defer func() { clusterMeshTestOverrideLBTimeout = prev }()
	prevInt := clusterMeshTestOverrideLBInterval
	clusterMeshTestOverrideLBInterval = 25 * time.Millisecond
	defer func() { clusterMeshTestOverrideLBInterval = prevInt }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses len = %d, want 2", len(statuses))
	}
	// At least one PeerStatus must reflect the LB-absent gap. Both
	// regions' Peers list contains the OTHER region, so we expect at
	// least one Connected=false.
	connectedFalse := 0
	for _, st := range statuses {
		for _, p := range st.Peers {
			if !p.Connected {
				connectedFalse++
			}
		}
	}
	if connectedFalse == 0 {
		t.Errorf("expected at least one Connected=false peer when one region has no LB IP; got none")
	}

	// #3241 — the LB-never-appeared failure must be visible in the
	// clustermesh-progress event stream WITH the failing region's key,
	// not only in server-side logs (G91 lesson).
	regionKey := regionKeyFromSpec(fx.dep.Request.Regions[1], 1)
	if !hasClusterMeshEvent(fx.dep, "warn", regionKey, "LB lookup failed") {
		t.Errorf("expected a warn clustermesh-progress event carrying region key %q + %q; events:\n%s",
			regionKey, "LB lookup failed", dumpClusterMeshEvents(fx.dep))
	}
}

// ── Level-triggered reconcile + silent-path kill (#3241) ────────────

// hasClusterMeshEvent reports whether the deployment's durable event
// buffer carries a clustermesh-progress event at the given level whose
// message contains EVERY substring.
func hasClusterMeshEvent(dep *Deployment, level string, substrs ...string) bool {
	for _, ev := range dep.snapshotEvents() {
		if ev.Phase != clusterMeshPhase {
			continue
		}
		if level != "" && ev.Level != level {
			continue
		}
		all := true
		for _, s := range substrs {
			if !strings.Contains(ev.Message, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// dumpClusterMeshEvents renders the clustermesh-progress events for
// failure messages.
func dumpClusterMeshEvents(dep *Deployment) string {
	var sb strings.Builder
	for _, ev := range dep.snapshotEvents() {
		if ev.Phase != clusterMeshPhase {
			continue
		}
		sb.WriteString(ev.Level)
		sb.WriteString(": ")
		sb.WriteString(ev.Message)
		sb.WriteString("\n")
	}
	return sb.String()
}

// waitForClusterMeshEvent polls the durable event buffer until an event
// matching level+substrings appears or the timeout elapses.
func waitForClusterMeshEvent(t *testing.T, dep *Deployment, timeout time.Duration, level string, substrs ...string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if hasClusterMeshEvent(dep, level, substrs...) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s clustermesh-progress event containing %v within %s; events:\n%s",
		level, substrs, timeout, dumpClusterMeshEvents(dep))
}

// TestAutoEstablishClusterMesh_RegionSlotFailureEmitsEventWithRegionKey
// — the hw126 silence shape (#3241): a region whose slot fails during
// buildRegionSlots (kubeconfig path empty / unreadable) used to enter
// the fan-out with err pre-set and produce ZERO events — Steps 1-3 all
// `continue` past failed slots before their first emit, so the region
// simply vanished from the operator's stream while its peer reported
// "wired 0/1 peers". Every per-region failure MUST emit a
// clustermesh-progress warn event carrying the region key + the error
// string.
func TestAutoEstablishClusterMesh_RegionSlotFailureEmitsEventWithRegionKey(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	// Break region B's kubeconfig resolution entirely: drop the
	// explicit map entry AND the on-disk file so buildRegionSlots's
	// filesystem fallback misses too — the slot enters the fan-out
	// with err pre-set ("kubeconfig path empty ...").
	regionKey := regionKeyFromSpec(fx.dep.Request.Regions[1], 1)
	brokenPath := fx.dep.secondaryKubeconfigPaths[regionKey]
	delete(fx.dep.secondaryKubeconfigPaths, regionKey)
	if err := os.Remove(brokenPath); err != nil {
		t.Fatalf("remove secondary kubeconfig: %v", err)
	}
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	if got := countFullyMeshedRegions(statuses); got != 0 {
		t.Errorf("fully meshed regions = %d, want 0 when one slot is broken", got)
	}

	// The pre-fan-out slot failure must surface with the region key +
	// error string.
	if !hasClusterMeshEvent(fx.dep, "warn", regionKey, "kubeconfig") {
		t.Errorf("expected a warn clustermesh-progress event carrying region key %q + the kubeconfig error; events:\n%s",
			regionKey, dumpClusterMeshEvents(fx.dep))
	}
	// And the failed region must get a terminal per-region line too —
	// the healthy region gets "wired N/M peers"; the broken one must
	// not end the attempt silently.
	if !hasClusterMeshEvent(fx.dep, "warn", regionKey, "skipped") {
		t.Errorf("expected a terminal warn event for skipped region %q; events:\n%s",
			regionKey, dumpClusterMeshEvents(fx.dep))
	}
}

// TestRunAutoEstablishClusterMesh_RetryConvergesAfterLBAppears — the
// level-triggered reconcile (#3241). Attempt 1 sees region-A (primary)
// WITHOUT a LoadBalancer IP (the hw126 LB-IPAM race) and ends
// partially meshed; the test then stamps the LB IP onto the fake
// Service (LB-IPAM "catching up") and the retry loop must converge to
// fully meshed, land the #3236 cnpg-pair flip, emit per-attempt
// progress events, and stop cleanly with a final success event.
func TestRunAutoEstablishClusterMesh_RetryConvergesAfterLBAppears(t *testing.T) {
	fx := newTestFixture(t, []string{"", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// Fast knobs: sub-second LB poll + retry backoff so the loop
	// converges in milliseconds.
	prev := clusterMeshTestOverrideLBTimeout
	clusterMeshTestOverrideLBTimeout = 150 * time.Millisecond
	defer func() { clusterMeshTestOverrideLBTimeout = prev }()
	prevInt := clusterMeshTestOverrideLBInterval
	clusterMeshTestOverrideLBInterval = 25 * time.Millisecond
	defer func() { clusterMeshTestOverrideLBInterval = prevInt }()
	fx.handler.clusterMeshRetryInitialBackoff = 20 * time.Millisecond
	fx.handler.clusterMeshRetryMaxBackoff = 60 * time.Millisecond
	fx.handler.clusterMeshRetryBudget = 20 * time.Second
	fx.handler.clusterMeshAttemptTimeout = 5 * time.Second

	// Once attempt 1 reports partial mesh, stamp the missing LB IP —
	// the level-trigger's whole point is that a later-arriving IP
	// still converges without any external re-trigger.
	flipDone := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if hasClusterMeshEvent(fx.dep, "warn", "retrying in") {
				cs := fx.clients[fx.primaryKubeconfigPath]
				svc, err := cs.CoreV1().Services(clusterMeshNamespace).Get(context.Background(), clusterMeshApiserverService, metav1.GetOptions{})
				if err != nil {
					flipDone <- err
					return
				}
				svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}
				_, err = cs.CoreV1().Services(clusterMeshNamespace).Update(context.Background(), svc, metav1.UpdateOptions{})
				flipDone <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		flipDone <- fmt.Errorf("never observed a retry progress event")
	}()

	loopDone := make(chan struct{})
	go func() {
		fx.handler.runAutoEstablishClusterMesh(fx.dep)
		close(loopDone)
	}()
	select {
	case <-loopDone:
	case <-time.After(25 * time.Second):
		t.Fatalf("reconcile loop did not terminate; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	if err := <-flipDone; err != nil {
		t.Fatalf("LB flip goroutine: %v", err)
	}

	// Per-attempt progress event (attempt N, fullyMeshed X/Y).
	if !hasClusterMeshEvent(fx.dep, "warn", "attempt 1 ended with", "regions fully meshed", "retrying in") {
		t.Errorf("expected per-attempt retry progress event; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	// Final success event — the loop stops cleanly once fully meshed.
	if !hasClusterMeshEvent(fx.dep, "info", "fully meshed (2/2 regions)", "reconcile loop complete") {
		t.Errorf("expected final success event after convergence; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}

	// The mesh actually converged: both regions carry the 4 peer
	// Secret entries.
	for kcPath, client := range fx.clients {
		secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(context.Background(), clusterMeshSecretName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Get cilium-clustermesh in %q: %v", kcPath, err)
		}
		if len(secret.Data) != 4 {
			t.Errorf("Secret in %q has %d entries, want 4 (keys %v)", kcPath, len(secret.Data), secretKeys(secret))
		}
	}

	// And the #3236 cnpg-pair flip landed on the converged attempt —
	// on BOTH regions' Kustomizations (split-side topology).
	for _, kcPath := range []string{fx.primaryKubeconfigPath, fx.secondaryKubeconfigPath} {
		ks := getBootstrapKitKustomization(t, fx.dynClients[kcPath])
		substitute, found, err := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
		if err != nil || !found {
			t.Fatalf("region %q: read spec.postBuild.substitute: found=%v err=%v", kcPath, found, err)
		}
		if got := substitute[clusterMeshCNPGPairSubstituteKey]; got != "true" {
			t.Errorf("region %q: substitute[%s] = %q, want \"true\" after retry convergence",
				kcPath, clusterMeshCNPGPairSubstituteKey, got)
		}
	}
}

// TestRestoreFromStore_StartupKicksClusterMeshReconcile — #3241 part 2:
// a stored status=ready multi-region deployment whose Phase-1 already
// terminated gets the level-triggered reconcile loop kicked at
// catalyst-api startup (this is what heals an hw126-shaped Sovereign
// zero-touch on the next mothership roll — nothing else ever re-fires
// the establish before handover).
func TestRestoreFromStore_StartupKicksClusterMeshReconcile(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "") // mothership mode for chrootEnsureSMEPoolSeed
	dir := t.TempDir()
	caCert, caKey := genCAForTest(t, "test-mesh-ca")

	depID := "depstartupmesh"
	regions := []provisioner.RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1"},
		{Provider: "hetzner", CloudRegion: "hel1"},
	}
	lbIPs := []string{"203.0.113.30", "203.0.113.40"}
	clients := map[string]kubernetes.Interface{}
	for i := range regions {
		key := regionKeyFromSpec(regions[i], i)
		kcPath := writeFakeKubeconfig(t, dir, depID, key)
		cs := buildFakeClusterMeshCluster(t, lbIPs[i], caCert, caKey)
		// #3254: seed the cnpg-pair replica-auth source Secrets on the
		// primary (idx 0) + the cnpg namespace on the replica, so the
		// slot-16b flip's cross-cluster copy succeeds and the gate flips.
		if i == 0 {
			seedCNPGPairSourceSecrets(t, cs)
		} else {
			seedCNPGPairReplicaNamespace(t, cs)
		}
		clients[kcPath] = cs
	}
	primaryKubeconfigPath := filepath.Join(dir, depID+".yaml")
	// EVERY region's Kustomization gets the split-side flip, so each
	// region needs its own seeded fake dynamic client.
	dynClients := map[string]dynamic.Interface{}
	for i := range regions {
		key := regionKeyFromSpec(regions[i], i)
		kcPath := primaryKubeconfigPath
		if i > 0 {
			kcPath = filepath.Join(dir, depID+"-"+key+".yaml")
		}
		dynClients[kcPath] = newFakeKustomizationDynClient(t,
			buildBootstrapKitKustomization(defaultBootstrapKitSubstitute()))
	}
	restore := installClusterMeshClientFactory(clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(dynClients)
	defer restoreDyn()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	phase1Done := time.Now().Add(-1 * time.Hour).UTC()
	rec := store.Record{
		ID:     depID,
		Status: "ready",
		Request: store.Redact(provisioner.Request{
			SovereignFQDN: "tsm.example.io",
			Regions:       regions,
		}),
		Result: &provisioner.Result{
			SovereignFQDN:    "tsm.example.io",
			KubeconfigPath:   primaryKubeconfigPath,
			Phase1FinishedAt: &phase1Done, // Phase-1 terminated → no watch resume; startup kick owns the re-establish
		},
		StartedAt:  time.Now().Add(-2 * time.Hour),
		FinishedAt: time.Now().Add(-90 * time.Minute),
	}
	if err := st.Save(rec); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	h := &Handler{log: silentLogger(), kubeconfigsDir: dir, store: st}
	h.clusterMeshRetryInitialBackoff = 20 * time.Millisecond
	h.clusterMeshRetryMaxBackoff = 60 * time.Millisecond
	h.clusterMeshRetryBudget = 20 * time.Second
	h.clusterMeshAttemptTimeout = 5 * time.Second

	h.restoreFromStore()

	val, ok := h.deployments.Load(depID)
	if !ok {
		t.Fatalf("deployment %q not restored", depID)
	}
	dep := val.(*Deployment)

	// The startup-kicked loop must run the establish and converge —
	// success event lands on the rehydrated record's durable buffer.
	waitForClusterMeshEvent(t, dep, 10*time.Second, "info", "fully meshed (2/2 regions)", "reconcile loop complete")

	// Establish was genuinely invoked: peer Secrets written in both
	// fake regions + the #3236 flip landed.
	for kcPath, client := range clients {
		secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(context.Background(), clusterMeshSecretName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Get cilium-clustermesh in %q: %v", kcPath, err)
		}
		if len(secret.Data) != 4 {
			t.Errorf("Secret in %q has %d entries, want 4 (keys %v)", kcPath, len(secret.Data), secretKeys(secret))
		}
	}
	for kcPath, dyn := range dynClients {
		ks := getBootstrapKitKustomization(t, dyn)
		substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
		if got := substitute[clusterMeshCNPGPairSubstituteKey]; got != "true" {
			t.Errorf("region %q: substitute[%s] = %q, want \"true\" after startup reconcile (split-side: BOTH regions flip)",
				kcPath, clusterMeshCNPGPairSubstituteKey, got)
		}
	}
}

// TestShouldStartupClusterMeshReconcile_Guards — warn-and-skip
// per-deployment guards: single-region, non-ready, and
// missing-kubeconfig deployments never get the loop kicked.
func TestShouldStartupClusterMeshReconcile_Guards(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	kcPath := filepath.Join(dir, "depguard.yaml")
	if err := os.WriteFile(kcPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	twoRegions := []provisioner.RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1"},
		{Provider: "hetzner", CloudRegion: "hel1"},
	}
	cases := []struct {
		name string
		dep  *Deployment
		want bool
	}{
		{
			name: "ready-two-region-kubeconfig-present",
			dep: &Deployment{ID: "g1", Status: "ready",
				Request: provisioner.Request{Regions: twoRegions},
				Result:  &provisioner.Result{KubeconfigPath: kcPath}},
			want: true,
		},
		{
			name: "single-region-skipped",
			dep: &Deployment{ID: "g2", Status: "ready",
				Request: provisioner.Request{Regions: twoRegions[:1]},
				Result:  &provisioner.Result{KubeconfigPath: kcPath}},
			want: false,
		},
		{
			name: "failed-status-skipped",
			dep: &Deployment{ID: "g3", Status: "failed",
				Request: provisioner.Request{Regions: twoRegions},
				Result:  &provisioner.Result{KubeconfigPath: kcPath}},
			want: false,
		},
		{
			name: "kubeconfig-missing-warn-and-skip",
			dep: &Deployment{ID: "g4", Status: "ready",
				Request: provisioner.Request{Regions: twoRegions},
				Result:  &provisioner.Result{KubeconfigPath: filepath.Join(dir, "absent.yaml")}},
			want: false,
		},
		{
			name: "result-nil-warn-and-skip",
			dep: &Deployment{ID: "g5", Status: "ready",
				Request: provisioner.Request{Regions: twoRegions}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.shouldStartupClusterMeshReconcile(tc.dep); got != tc.want {
				t.Errorf("shouldStartupClusterMeshReconcile = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldStartupClusterMeshReconcile_ConventionalFallback — the hw126
// (#3241) regression: a deployment record restored from the PVC loses the
// `omitempty` Result.KubeconfigPath FIELD, but the kubeconfig FILE always
// survives at the conventional `<kubeconfigsDir>/<id>.yaml`. The reconcile
// must derive that path (mirroring #3153's resume fallback) instead of
// emitting "primary kubeconfig path empty" and skipping forever. It must
// also stamp the resolved path back onto dep.Result so the downstream
// AutoEstablishClusterMesh fan-out (which re-reads dep.Result.KubeconfigPath)
// can reach the primary region.
func TestShouldStartupClusterMeshReconcile_ConventionalFallback(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	twoRegions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	const depID = "c986326a77d391d4"
	// Conventional primary file present on the PVC; record field empty.
	convPath := filepath.Join(dir, depID+".yaml")
	if err := os.WriteFile(convPath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write conventional kubeconfig: %v", err)
	}

	t.Run("empty-field-result-present", func(t *testing.T) {
		dep := &Deployment{ID: depID, Status: "ready",
			Request: provisioner.Request{Regions: twoRegions},
			Result:  &provisioner.Result{}} // KubeconfigPath == ""
		if !h.shouldStartupClusterMeshReconcile(dep) {
			t.Fatalf("reconcile skipped despite conventional kubeconfig present at %s", convPath)
		}
		// READ-ONLY contract: resolvePrimaryKubeconfigPath must NOT stamp
		// the resolved path onto dep.Result — State() leaks the *Result
		// pointer to lock-free JSON marshals, and the mesh-reconcile
		// goroutine calling this would race them (#3241 -race regression).
		// The reconcile loop re-resolves the path on every attempt instead.
		if dep.Result.KubeconfigPath != "" {
			t.Errorf("read-only gate mutated dep.Result.KubeconfigPath: got %q", dep.Result.KubeconfigPath)
		}
	})

	t.Run("nil-result-conventional-present", func(t *testing.T) {
		dep := &Deployment{ID: depID, Status: "ready",
			Request: provisioner.Request{Regions: twoRegions}} // Result == nil
		if !h.shouldStartupClusterMeshReconcile(dep) {
			t.Fatalf("reconcile skipped on nil-Result despite conventional kubeconfig present")
		}
		if dep.Result != nil {
			t.Errorf("read-only gate allocated dep.Result: %+v", dep.Result)
		}
	})

	t.Run("empty-field-no-file-still-skips", func(t *testing.T) {
		// A different id whose conventional file does NOT exist must still
		// warn-and-skip (no spinning the retry budget against a phantom).
		dep := &Deployment{ID: "no-such-id", Status: "ready",
			Request: provisioner.Request{Regions: twoRegions},
			Result:  &provisioner.Result{}}
		if h.shouldStartupClusterMeshReconcile(dep) {
			t.Fatalf("reconcile should skip when neither field nor conventional file resolves")
		}
	})

	t.Run("empty-kubeconfigsDir-skips", func(t *testing.T) {
		// Without a configured kubeconfigsDir the conventional path can't
		// be derived — the original warn-and-skip must still hold.
		hNoDir := &Handler{log: silentLogger()}
		dep := &Deployment{ID: depID, Status: "ready",
			Request: provisioner.Request{Regions: twoRegions},
			Result:  &provisioner.Result{}}
		if hNoDir.shouldStartupClusterMeshReconcile(dep) {
			t.Fatalf("reconcile should skip when kubeconfigsDir is empty")
		}
	})
}

// TestBuildRegionSlots_SecondaryConventionalFallback documents + locks in
// the part-2 finding: secondary region kubeconfigs are resolved from the
// conventional `<kubeconfigsDir>/<id>-<region>-<idx>.yaml` filesystem path
// INDEPENDENTLY of the in-memory dep.secondaryKubeconfigPaths map (which is
// also emptied on a PVC restore). No code change is needed for secondaries —
// this test proves the existing pickPath fallback already derives them with
// the same naming the PUT path (?region=<k>) writes.
func TestBuildRegionSlots_SecondaryConventionalFallback(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "c986326a77d391d4"
	regions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	primaryPath := filepath.Join(dir, depID+".yaml")
	// Secondary key per regionKeyFromSpec: "<cloudRegion>-<idx>" = "me-east-215-b-1".
	secondaryPath := filepath.Join(dir, depID+"-me-east-215-b-1.yaml")
	for _, p := range []string{primaryPath, secondaryPath} {
		if err := os.WriteFile(p, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// Route both resolved paths through fake clientsets so the slot build
	// exercises path resolution without parsing a real kubeconfig.
	restore := installClusterMeshClientFactory(map[string]kubernetes.Interface{
		primaryPath:   kfake.NewSimpleClientset(),
		secondaryPath: kfake.NewSimpleClientset(),
	})
	defer restore()
	dep := &Deployment{ID: depID, Status: "ready",
		Request: provisioner.Request{Regions: regions}}

	// Empty secondaryPaths map mimics the post-restore state.
	slots := h.buildRegionSlots(dep, regions, primaryPath, map[string]string{})
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	if slots[1].kubeconfigPath != secondaryPath {
		t.Errorf("secondary slot did not resolve conventional path: got %q want %q",
			slots[1].kubeconfigPath, secondaryPath)
	}
	if slots[1].err != nil {
		t.Errorf("secondary slot unexpectedly failed: %v", slots[1].err)
	}
}

// TestBuildRegionSlots_PrimaryConventionalFallback locks in the #3241 fix:
// after a catalyst-api restart / deploy-bot roll, dep.Result is not
// re-hydrated, so the passed-in primaryKubeconfigPath is "". Before the fix
// the PRIMARY slot (cluster=primaryMeshName) entered with an empty path →
// "kubeconfig path empty" → the secondary never peered with the primary →
// fullyMeshed=0 → SOVEREIGN_ENABLE_CNPG_PAIR stayed OFF (the live hw128
// failure). The primary kubeconfig survives at the conventional
// `<kubeconfigsDir>/<id>.yaml`, identical to how secondaries are recovered.
func TestBuildRegionSlots_PrimaryConventionalFallback(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "5cc5f21df5f64ea7"
	regions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	primaryPath := filepath.Join(dir, depID+".yaml")
	// Secondary key per regionKeyFromSpec: "<cloudRegion>-<idx>" = "me-east-215-b-1".
	secondaryPath := filepath.Join(dir, depID+"-me-east-215-b-1.yaml")
	for _, p := range []string{primaryPath, secondaryPath} {
		if err := os.WriteFile(p, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	restore := installClusterMeshClientFactory(map[string]kubernetes.Interface{
		primaryPath:   kfake.NewSimpleClientset(),
		secondaryPath: kfake.NewSimpleClientset(),
	})
	defer restore()

	t.Run("empty-primary-path-resolves-conventional", func(t *testing.T) {
		dep := &Deployment{ID: depID, Status: "ready",
			Request: provisioner.Request{Regions: regions}}
		// Empty primaryKubeconfigPath mimics dep.Result==nil after a restart.
		slots := h.buildRegionSlots(dep, regions, "", map[string]string{})
		if len(slots) != 2 {
			t.Fatalf("expected 2 slots, got %d", len(slots))
		}
		if slots[0].kubeconfigPath != primaryPath {
			t.Errorf("primary slot did not resolve conventional path: got %q want %q",
				slots[0].kubeconfigPath, primaryPath)
		}
		if slots[0].err != nil {
			t.Errorf("primary slot unexpectedly failed: %v", slots[0].err)
		}
	})

	t.Run("primary-file-absent-stays-empty", func(t *testing.T) {
		// A deployment id with no kubeconfig file on disk: the fallback
		// must NOT invent a path — behaviour is unchanged (empty → the
		// existing "kubeconfig path empty" error slot).
		dep := &Deployment{ID: "ghostdepnofile", Status: "ready",
			Request: provisioner.Request{Regions: regions}}
		slots := h.buildRegionSlots(dep, regions, "", map[string]string{})
		if len(slots) != 2 {
			t.Fatalf("expected 2 slots, got %d", len(slots))
		}
		if slots[0].kubeconfigPath != "" {
			t.Errorf("primary slot should stay empty when no file present: got %q",
				slots[0].kubeconfigPath)
		}
		if slots[0].err == nil {
			t.Errorf("primary slot should carry the kubeconfig-path-empty error when no file present")
		}
	})
}

// TestAutoEstablishClusterMesh_Idempotent — second invocation produces
// identical Secret keys (same number of entries, same names) and the
// returned statuses match.
func TestAutoEstablishClusterMesh_Idempotent(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("status len changed across runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].RegionKey != second[i].RegionKey {
			t.Errorf("region key changed across runs at idx %d: %q vs %q",
				i, first[i].RegionKey, second[i].RegionKey)
		}
		if len(first[i].Peers) != len(second[i].Peers) {
			t.Errorf("peer count changed across runs for region %q: %d vs %d",
				first[i].RegionKey, len(first[i].Peers), len(second[i].Peers))
		}
	}
	// Verify the Secret entry NAMES are unchanged across runs (the
	// cert bytes themselves rotate each time mintPeerClientCert runs,
	// but the entry shape is stable — idempotent on shape, not on
	// bytes).
	for _, client := range fx.clients {
		secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(context.Background(), clusterMeshSecretName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Get cilium-clustermesh: %v", err)
		}
		if len(secret.Data) != 4 {
			t.Errorf("Secret entries len = %d, want 4 after idempotent re-run", len(secret.Data))
		}
	}
}

// TestAutoEstablishClusterMesh_SingleRegionSkips — len(Regions) < 2
// returns (nil, nil) immediately.
func TestAutoEstablishClusterMesh_SingleRegionSkips(t *testing.T) {
	dir := t.TempDir()
	dep := &Deployment{
		ID:        "dep-single",
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "tsingle.example.io",
			Regions: []provisioner.RegionSpec{
				{Provider: "hetzner", CloudRegion: "fsn1"},
			},
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "tsingle.example.io",
			KubeconfigPath: filepath.Join(dir, "dep-single.yaml"),
		},
	}
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	statuses, err := h.AutoEstablishClusterMesh(context.Background(), dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	if statuses != nil {
		t.Errorf("statuses = %+v, want nil for single-region deployment", statuses)
	}
}

// ── ClusterMesh-gated cnpg-pair enable (#3236) ──────────────────────

// requireFullyMeshed asserts every region's ReadyAt is stamped — the
// precondition the cnpg-pair flip tests rely on so a mesh regression
// doesn't masquerade as a gate-flip regression.
func requireFullyMeshed(t *testing.T, statuses []ClusterMeshStatus) {
	t.Helper()
	for _, st := range statuses {
		if st.ReadyAt.IsZero() {
			t.Fatalf("precondition: region %q not fully meshed (peers: %+v)", st.RegionKey, st.Peers)
		}
	}
}

// TestAutoEstablishClusterMesh_FullMeshFlipsCNPGPairGate — after a
// CONFIRMED full-mesh establishment across both regions, EVERY
// region's flux-system/bootstrap-kit Kustomization must carry
// postBuild.substitute.SOVEREIGN_ENABLE_CNPG_PAIR="true" plus the
// reconcile.fluxcd.io/requestedAt annotation so slot 16b (bp-cnpg-pair)
// deploys zero-touch on the next Flux reconcile. (#3236; the gate
// itself exists because of hw124/#3196 — see the slot header in
// clusters/_template/bootstrap-kit/16b-bp-cnpg-pair.yaml.) The
// all-regions scope is the chart-0.2.0 split-side topology: each
// cluster renders its own half of the pair, so a primary-only flip
// would leave the secondary rendering an empty release — no replica,
// no WAL stream.
func TestAutoEstablishClusterMesh_FullMeshFlipsCNPGPairGate(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	requireFullyMeshed(t, statuses)

	for _, kcPath := range []string{fx.primaryKubeconfigPath, fx.secondaryKubeconfigPath} {
		ks := getBootstrapKitKustomization(t, fx.dynClients[kcPath])
		substitute, found, err := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
		if err != nil || !found {
			t.Fatalf("region %q: read spec.postBuild.substitute: found=%v err=%v", kcPath, found, err)
		}
		if got := substitute[clusterMeshCNPGPairSubstituteKey]; got != "true" {
			t.Errorf("region %q: substitute[%s] = %q, want \"true\" after full mesh establishment (split-side: BOTH regions must flip)",
				kcPath, clusterMeshCNPGPairSubstituteKey, got)
		}
		// The JSON merge patch must MERGE into the substitute map, never
		// replace it — clobbering SOVEREIGN_PRIMARY_REGION/… would fail the
		// chart's required-fail-fast and wedge the whole atomic apply.
		if got := substitute[clusterMeshPrimaryRegionSubstituteKey]; got != testPrimaryRegionLabel {
			t.Errorf("region %q: substitute[%s] = %q, want %q (patch clobbered sibling substitute keys)",
				kcPath, clusterMeshPrimaryRegionSubstituteKey, got, testPrimaryRegionLabel)
		}
		if got := substitute[clusterMeshReplicaRegionSubstituteKey]; got != testReplicaRegionLabel {
			t.Errorf("region %q: substitute[%s] = %q, want %q (patch clobbered sibling substitute keys)",
				kcPath, clusterMeshReplicaRegionSubstituteKey, got, testReplicaRegionLabel)
		}
		stamp := ks.GetAnnotations()[fluxReconcileRequestedAtAnnotation]
		if stamp == "" {
			t.Fatalf("region %q: annotation %q absent — Flux reconcile never requested", kcPath, fluxReconcileRequestedAtAnnotation)
		}
		if _, perr := time.Parse(time.RFC3339Nano, stamp); perr != nil {
			t.Errorf("region %q: annotation %q = %q is not RFC3339Nano: %v", kcPath, fluxReconcileRequestedAtAnnotation, stamp, perr)
		}
	}
}

// TestAutoEstablishClusterMesh_SecondaryKustomizationMissingBlocksFlip
// — the split-side flip is ALL-OR-NOTHING: when the secondary region's
// bootstrap-kit Kustomization is unreadable, the flip must be refused
// on the PRIMARY too. A half-flipped pair (primary half active,
// secondary gated OFF) is exactly the broken hw126 topology — primary
// Cluster waiting forever for a replica the secondary never renders.
func TestAutoEstablishClusterMesh_SecondaryKustomizationMissingBlocksFlip(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	// Secondary dyn client exists but has NO bootstrap-kit Kustomization
	// → the gather phase's Get fails for the secondary region.
	fx.dynClients[fx.secondaryKubeconfigPath] = newFakeKustomizationDynClient(t)
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	requireFullyMeshed(t, statuses)

	ks := getBootstrapKitKustomization(t, fx.dynClients[fx.primaryKubeconfigPath])
	substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if got, ok := substitute[clusterMeshCNPGPairSubstituteKey]; ok {
		t.Errorf("substitute[%s] = %q on the PRIMARY — flip must be all-or-nothing when the secondary Kustomization is unreadable",
			clusterMeshCNPGPairSubstituteKey, got)
	}
	if stamp := ks.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp != "" {
		t.Errorf("annotation %q = %q set on the PRIMARY despite secondary gather failure",
			fluxReconcileRequestedAtAnnotation, stamp)
	}
}

// TestAutoEstablishClusterMesh_PartialMeshDoesNotFlipCNPGPairGate —
// region B never gets a LoadBalancer IP, so the mesh is NOT fully
// established. The gate flip must NOT happen (🛑 hw124/#3196 anti-
// pattern guard: flipping on raw 2-region-ness — or on partial mesh —
// renders a replica that can never stream its basebackup).
func TestAutoEstablishClusterMesh_PartialMeshDoesNotFlipCNPGPairGate(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", ""})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()
	prev := clusterMeshTestOverrideLBTimeout
	clusterMeshTestOverrideLBTimeout = 200 * time.Millisecond
	defer func() { clusterMeshTestOverrideLBTimeout = prev }()
	prevInt := clusterMeshTestOverrideLBInterval
	clusterMeshTestOverrideLBInterval = 25 * time.Millisecond
	defer func() { clusterMeshTestOverrideLBInterval = prevInt }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}

	ks := getBootstrapKitKustomization(t, fx.dynClients[fx.primaryKubeconfigPath])
	substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if got, ok := substitute[clusterMeshCNPGPairSubstituteKey]; ok {
		t.Errorf("substitute[%s] = %q — gate flipped on PARTIAL mesh (hw124/#3196 anti-pattern)",
			clusterMeshCNPGPairSubstituteKey, got)
	}
	if stamp := ks.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp != "" {
		t.Errorf("annotation %q = %q set despite partial mesh — reconcile must not be requested",
			fluxReconcileRequestedAtAnnotation, stamp)
	}
}

// TestAutoEstablishClusterMesh_SubstitutePreconditionBlocksCNPGPairFlip
// — even on a fully-established mesh, the flip is refused when the
// substitute map's SOVEREIGN_PRIMARY_REGION / SOVEREIGN_REPLICA_REGION
// are absent, empty, or equal: the bp-cnpg-pair chart `required`s
// distinct non-empty regions when enabled=true, and a render failure
// inside the bootstrap-kit Kustomization fails the WHOLE atomic apply
// (0 HRs). A loud warning must be logged so the gap is debuggable.
func TestAutoEstablishClusterMesh_SubstitutePreconditionBlocksCNPGPairFlip(t *testing.T) {
	cases := []struct {
		name       string
		substitute map[string]any
	}{
		{
			name: "replica-region-missing",
			substitute: map[string]any{
				"SOVEREIGN_FQDN":                      "tcm.example.io",
				clusterMeshPrimaryRegionSubstituteKey: testPrimaryRegionLabel,
			},
		},
		{
			name: "regions-equal",
			substitute: map[string]any{
				"SOVEREIGN_FQDN":                      "tcm.example.io",
				clusterMeshPrimaryRegionSubstituteKey: testPrimaryRegionLabel,
				clusterMeshReplicaRegionSubstituteKey: testPrimaryRegionLabel,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
			fx.dynClients[fx.primaryKubeconfigPath] = newFakeKustomizationDynClient(t,
				buildBootstrapKitKustomization(tc.substitute))
			var logBuf bytes.Buffer
			fx.handler.log = slog.New(slog.NewTextHandler(&logBuf, nil))
			restore := installClusterMeshClientFactory(fx.clients)
			defer restore()
			restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
			defer restoreDyn()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
			if err != nil {
				t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
			}
			requireFullyMeshed(t, statuses)

			// All-or-nothing: the PRIMARY's failed precondition must
			// block the flip on the SECONDARY too (whose own substitute
			// map is healthy) — a half-flipped split-side pair is the
			// hw126 broken topology.
			for _, kcPath := range []string{fx.primaryKubeconfigPath, fx.secondaryKubeconfigPath} {
				ks := getBootstrapKitKustomization(t, fx.dynClients[kcPath])
				substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
				if got, ok := substitute[clusterMeshCNPGPairSubstituteKey]; ok {
					t.Errorf("region %q: substitute[%s] = %q — gate flipped despite failed region precondition",
						kcPath, clusterMeshCNPGPairSubstituteKey, got)
				}
				if stamp := ks.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp != "" {
					t.Errorf("region %q: annotation %q = %q set despite failed region precondition",
						kcPath, fluxReconcileRequestedAtAnnotation, stamp)
				}
			}
			if !strings.Contains(logBuf.String(), "refusing to flip SOVEREIGN_ENABLE_CNPG_PAIR") {
				t.Errorf("expected loud warning containing %q in log output, got:\n%s",
					"refusing to flip SOVEREIGN_ENABLE_CNPG_PAIR", logBuf.String())
			}
		})
	}
}

// ── #3254: cross-cluster CNPG replica-auth Secret sync ──────────────

// getCNPGSecret reads a Secret from the cnpg namespace of a fake
// clientset, returning (nil, false) when absent.
func getCNPGSecret(t *testing.T, cs kubernetes.Interface, name string) (*corev1.Secret, bool) {
	t.Helper()
	s, err := cs.CoreV1().Secrets(cnpgPairNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get Secret %s/%s: %v", cnpgPairNamespace, name, err)
	}
	return s, true
}

// assertGateFlipped/assertGateNotFlipped assert the SOVEREIGN_ENABLE_
// CNPG_PAIR substitute + reconcile annotation state on a region's
// bootstrap-kit Kustomization.
func assertGateFlipped(t *testing.T, dyn dynamic.Interface, region string) {
	t.Helper()
	ks := getBootstrapKitKustomization(t, dyn)
	substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if got := substitute[clusterMeshCNPGPairSubstituteKey]; got != "true" {
		t.Errorf("region %q: substitute[%s] = %q, want \"true\"", region, clusterMeshCNPGPairSubstituteKey, got)
	}
	if stamp := ks.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp == "" {
		t.Errorf("region %q: reconcile annotation absent — Flux reconcile never requested", region)
	}
}

func assertGateNotFlipped(t *testing.T, dyn dynamic.Interface, region string) {
	t.Helper()
	ks := getBootstrapKitKustomization(t, dyn)
	substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if got, ok := substitute[clusterMeshCNPGPairSubstituteKey]; ok {
		t.Errorf("region %q: substitute[%s] = %q — gate flipped when it must stay OFF", region, clusterMeshCNPGPairSubstituteKey, got)
	}
	if stamp := ks.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp != "" {
		t.Errorf("region %q: reconcile annotation = %q set when the flip must be refused", region, stamp)
	}
}

// TestAutoEstablishClusterMesh_FullMeshSyncsReplicaAuthSecretsThenFlips
// — happy path: a fully-meshed 2-region prov copies BOTH primary CNPG
// replica-auth Secrets (the `-replication` client cert + the `-ca`) onto
// the replica cluster's `cnpg` namespace AND THEN flips both regions'
// SOVEREIGN_ENABLE_CNPG_PAIR gates. Without the copy the replica Cluster
// CR's externalClusters sslKey/sslCert/sslRootCert refs would dangle and
// the WAL stream could never authenticate (#3254).
func TestAutoEstablishClusterMesh_FullMeshSyncsReplicaAuthSecretsThenFlips(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// Sanity: the replica starts WITHOUT the source Secrets.
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	for _, name := range cnpgPairReplicaAuthSecrets {
		if _, ok := getCNPGSecret(t, replicaCS, name); ok {
			t.Fatalf("precondition: replica unexpectedly already has Secret %s", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	requireFullyMeshed(t, statuses)

	// Both source Secrets must now exist on the replica, byte-identical.
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	for _, name := range cnpgPairReplicaAuthSecrets {
		got, ok := getCNPGSecret(t, replicaCS, name)
		if !ok {
			t.Fatalf("replica missing copied Secret %s — replica WAL-stream auth would dangle", name)
		}
		src, _ := getCNPGSecret(t, primaryCS, name)
		for k, v := range src.Data {
			if string(got.Data[k]) != string(v) {
				t.Errorf("Secret %s key %q: replica=%q want %q (copy not byte-identical)", name, k, got.Data[k], v)
			}
		}
		if got.Type != src.Type {
			t.Errorf("Secret %s: replica Type=%q want %q", name, got.Type, src.Type)
		}
		// Server-side metadata must be stripped (no inherited UID).
		if got.UID == src.UID && src.UID != "" {
			t.Errorf("Secret %s: replica inherited the primary's UID %q — server-side metadata not stripped", name, got.UID)
		}
	}

	// AND the gate flipped on BOTH regions.
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
}

// TestAutoEstablishClusterMesh_MissingSourceSecretRefusesFlip — when the
// primary has NOT yet produced one of the CNPG replica-auth Secrets (its
// postgres is still bootstrapping — CNPG mints `-replication`/`-ca` only
// after initdb), the TWO-STAGE flip (#3241 first-flip deadlock) flips
// the PRIMARY side anyway (its Cluster has no dependency on those
// Secrets — it is what MINTS them) but REFUSES the replica side: its
// gate stays OFF and no Secret lands on the replica. The level-trigger
// re-run converges once the primary's Secret appears.
func TestAutoEstablishClusterMesh_MissingSourceSecretRefusesFlip(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	// Remove ONE source Secret from the primary (simulates postgres
	// mid-bootstrap).
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	if err := primaryCS.CoreV1().Secrets(cnpgPairNamespace).Delete(context.Background(), cnpgPairReplicationCert, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete source Secret: %v", err)
	}
	var logBuf bytes.Buffer
	fx.handler.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	requireFullyMeshed(t, statuses)

	// Two-stage: the primary side flips (stage 1, unconditional); the
	// replica side is refused until its auth Secrets can be synced.
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateNotFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
	// The OTHER source Secret (the -ca) must NOT have been partially
	// copied — the source read is all-or-nothing before any copy.
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	if _, ok := getCNPGSecret(t, replicaCS, cnpgPairReplicationCA); ok {
		t.Errorf("replica got %s despite missing sibling source Secret — copy must be all-or-nothing", cnpgPairReplicationCA)
	}
	if !strings.Contains(logBuf.String(), "source Secret unavailable on primary") {
		t.Errorf("expected a loud warning about the missing source Secret, got:\n%s", logBuf.String())
	}
}

// TestAutoEstablishClusterMesh_ReplicaCopyFailureRefusesFlip — when the
// copy to the replica cluster fails (here: a reactor rejects the Secret
// write), the REPLICA side is refused: its gate stays OFF (a replica
// gate ON without its auth Secrets is the broken topology this guard
// prevents). The PRIMARY side flips regardless (stage 1 of the #3241
// two-stage flip — it has no dependency on the replica copy).
func TestAutoEstablishClusterMesh_ReplicaCopyFailureRefusesFlip(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	// Make every Secret write into the replica's cnpg namespace fail.
	replicaCS := fx.clients[fx.secondaryKubeconfigPath].(*kfake.Clientset)
	failWrite := func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == cnpgPairNamespace {
			return true, nil, fmt.Errorf("injected replica Secret write failure")
		}
		return false, nil, nil
	}
	replicaCS.PrependReactor("create", "secrets", failWrite)
	replicaCS.PrependReactor("update", "secrets", failWrite)

	var logBuf bytes.Buffer
	fx.handler.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	requireFullyMeshed(t, statuses)

	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateNotFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
	if !strings.Contains(logBuf.String(), "copy to replica failed") {
		t.Errorf("expected a loud warning about the failed replica copy, got:\n%s", logBuf.String())
	}
}

// TestAutoEstablishClusterMesh_ReRunAfterSecretsAppearConverges — the
// hw126 22:38 state (#3241/#3236): the mesh is fully established on the
// FIRST attempt (both LBs present) but the slot-16b flip REFUSES because
// the primary postgres hasn't minted its replica-auth Secrets yet. The
// prior loop treated "mesh meshed" as converged and STOPPED, so the flip
// stayed unapplied until a manual catalyst-api restart.
//
// With the flip-convergence signal threaded out of
// enableCNPGPairAfterFullMesh, the reconcile loop must now treat
// "meshed but flip refused" as NOT-converged and keep retrying on its own
// backoff. This test drives the LOOP (runAutoEstablishClusterMesh) ONCE
// — no fresh kick — and proves it converges across two attempts: attempt
// 1 meshes + refuses the flip; a goroutine then mints the source Secret
// (the primary finishing initdb); attempt 2 copies both Secrets and flips
// both gates; the loop stops with the final success event.
func TestAutoEstablishClusterMesh_ReRunAfterSecretsAppearConverges(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	// Source Secret missing at the start → attempt 1's flip refuses even
	// though the mesh fully establishes (both LBs present from the start).
	if err := primaryCS.CoreV1().Secrets(cnpgPairNamespace).Delete(context.Background(), cnpgPairReplicationCert, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete source Secret: %v", err)
	}
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// Fast retry knobs so the loop's two attempts complete in milliseconds.
	fx.handler.clusterMeshRetryInitialBackoff = 20 * time.Millisecond
	fx.handler.clusterMeshRetryMaxBackoff = 60 * time.Millisecond
	fx.handler.clusterMeshRetryBudget = 20 * time.Second
	fx.handler.clusterMeshAttemptTimeout = 5 * time.Second

	// Once attempt 1 reports "meshed but the cnpg-pair flip has not landed"
	// (the new retry shape), the primary postgres finishes initdb → CNPG
	// mints the Secret. The level-trigger's whole point: a later-appearing
	// Secret still converges with NO external re-kick.
	secretDone := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if hasClusterMeshEvent(fx.dep, "warn", "cnpg-pair flip has not landed", "retrying in") {
				_, err := primaryCS.CoreV1().Secrets(cnpgPairNamespace).Create(context.Background(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: cnpgPairReplicationCert, Namespace: cnpgPairNamespace},
					Type:       corev1.SecretTypeTLS,
					Data:       map[string][]byte{"tls.crt": []byte("REPL-CERT-2"), "tls.key": []byte("REPL-KEY-2")},
				}, metav1.CreateOptions{})
				secretDone <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		secretDone <- fmt.Errorf("never observed the meshed-but-flip-pending retry event")
	}()

	loopDone := make(chan struct{})
	go func() {
		fx.handler.runAutoEstablishClusterMesh(fx.dep)
		close(loopDone)
	}()
	select {
	case <-loopDone:
	case <-time.After(25 * time.Second):
		t.Fatalf("reconcile loop did not terminate; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	if err := <-secretDone; err != nil {
		t.Fatalf("source-Secret mint goroutine: %v", err)
	}

	// Attempt 1 emitted the meshed-but-flip-pending retry event (proves the
	// loop did NOT stop on mesh-only readiness — the hw126 regression).
	if !hasClusterMeshEvent(fx.dep, "warn", "cnpg-pair flip has not landed", "retrying in") {
		t.Errorf("expected a meshed-but-flip-pending retry event; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	// Final success event — the loop stops cleanly only once the flip lands.
	if !hasClusterMeshEvent(fx.dep, "info", "fully meshed (2/2 regions)", "cnpg-pair flip landed", "reconcile loop complete") {
		t.Errorf("expected final success event after flip converged; events:\n%s", dumpClusterMeshEvents(fx.dep))
	}

	// The convergence happened: both replica-auth Secrets copied + both
	// gates flipped — within ONE loop invocation, no fresh kick.
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	for _, name := range cnpgPairReplicaAuthSecrets {
		if _, ok := getCNPGSecret(t, replicaCS, name); !ok {
			t.Errorf("replica still missing Secret %s after source appeared", name)
		}
	}
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
}

// TestResolveClusterMeshDialPort locks in the #3241 hw128 finding: when
// the clustermesh-apiserver "LB" is Cilium nodeIPAM, the ingress IP is a
// node-owned EIP that is DNAT'd to the node's private address — the BPF
// VIP frontend (keyed on the public EIP) never matches inbound packets,
// so EIP:2379 is connection-refused from outside the cluster and the
// ONLY externally-reachable path is the Service NodePort (verified live:
// 212.72.24.52:2379 refused, :31744 open). A real cloud LB (Hetzner)
// listens on 2379 itself and must keep dialing 2379.
func TestResolveClusterMeshDialPort(t *testing.T) {
	const eip = "212.72.24.52"
	mkSvc := func(nodePort int32) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: clusterMeshApiserverService, Namespace: clusterMeshNamespace},
			Spec: corev1.ServiceSpec{
				Type:  corev1.ServiceTypeLoadBalancer,
				Ports: []corev1.ServicePort{{Port: 2379, NodePort: nodePort}},
			},
		}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: eip}}
		return svc
	}
	mkNode := func(externalIP string) *corev1.Node {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cp1"}}
		if externalIP != "" {
			n.Status.Addresses = []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.59.1.52"},
				{Type: corev1.NodeExternalIP, Address: externalIP},
			}
		}
		return n
	}
	h := &Handler{log: silentLogger()}
	ctx := context.Background()

	t.Run("real-cloud-LB-keeps-2379", func(t *testing.T) {
		// Node ExternalIPs do NOT include the ingress IP — a real LB owns it.
		cs := kfake.NewSimpleClientset(mkNode("198.51.100.7"))
		port, err := h.resolveClusterMeshDialPort(ctx, cs, mkSvc(31744), eip)
		if err != nil {
			t.Fatalf("resolveClusterMeshDialPort: %v", err)
		}
		if port != 2379 {
			t.Errorf("port = %d, want 2379 (real cloud LB)", port)
		}
	})

	t.Run("node-owned-EIP-dials-NodePort", func(t *testing.T) {
		cs := kfake.NewSimpleClientset(mkNode(eip))
		port, err := h.resolveClusterMeshDialPort(ctx, cs, mkSvc(31744), eip)
		if err != nil {
			t.Fatalf("resolveClusterMeshDialPort: %v", err)
		}
		if port != 31744 {
			t.Errorf("port = %d, want 31744 (nodeIPAM EIP must dial NodePort)", port)
		}
	})

	t.Run("node-owned-EIP-without-nodeport-falls-back", func(t *testing.T) {
		cs := kfake.NewSimpleClientset(mkNode(eip))
		port, err := h.resolveClusterMeshDialPort(ctx, cs, mkSvc(0), eip)
		if err != nil {
			t.Fatalf("resolveClusterMeshDialPort: %v", err)
		}
		if port != 2379 {
			t.Errorf("port = %d, want 2379 fallback (warn-and-continue, etcd step surfaces it)", port)
		}
	})
}

// TestBuildPeerConfigBlob_DialPort — the endpoint line must carry the
// RESOLVED dial port (NodePort on nodeIPAM peers), not hardcoded 2379,
// while keeping the canonical *.mesh.cilium.io hostname (TLS SAN).
func TestBuildPeerConfigBlob_DialPort(t *testing.T) {
	blob := string(buildPeerConfigBlob("hw128-me-east-215-b", 31744))
	want := "- https://hw128-me-east-215-b.mesh.cilium.io:31744"
	if !strings.Contains(blob, want) {
		t.Errorf("blob missing %q:\n%s", want, blob)
	}
	if strings.Contains(blob, ":2379") {
		t.Errorf("blob still hardcodes 2379:\n%s", blob)
	}
}

// TestEnableCNPGPair_TwoStageFirstFlip locks in the #3241 first-flip
// fix: on a FRESH env the primary's replica-auth Secrets cannot exist
// yet (CNPG mints them only after the primary postgres initdb's, and
// that postgres only renders once the gate flips ON). Run 1 with the
// Secrets ABSENT must flip the PRIMARY region's Kustomization, leave
// the replica's OFF, and report NOT-converged. Run 2 with the Secrets
// present (initdb finished) must flip the replica too and converge.
// The pre-fix behaviour — sync gating ALL patches — left both regions
// OFF forever (the hw128 live deadlock).
func TestEnableCNPGPair_TwoStageFirstFlip(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// Delete the fixture-seeded primary replica-auth Secrets — the
	// fresh-env state where the primary postgres has not initdb'd.
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	for _, name := range cnpgPairReplicaAuthSecrets {
		if err := primaryCS.CoreV1().Secrets(cnpgPairNamespace).Delete(
			context.Background(), name, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("delete seeded secret %s: %v", name, err)
		}
	}

	gateOf := func(kcPath string) string {
		t.Helper()
		ks, err := fx.dynClients[kcPath].Resource(fluxKustomizationGVR).
			Namespace(fluxSystemNamespace).Get(context.Background(), bootstrapKitKustomizationName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get Kustomization (%s): %v", kcPath, err)
		}
		sub, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
		return sub[clusterMeshCNPGPairSubstituteKey]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run 1 — Secrets absent: primary flips ON, replica stays OFF,
	// flip reports not-converged so the level-trigger re-runs.
	_, converged, err := fx.handler.autoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if converged {
		t.Fatalf("run 1 reported converged despite missing replica-auth Secrets")
	}
	if g := gateOf(fx.primaryKubeconfigPath); g != "true" {
		t.Errorf("run 1: PRIMARY region gate = %q, want \"true\" (stage-1 unconditional)", g)
	}
	if g := gateOf(fx.secondaryKubeconfigPath); g == "true" {
		t.Errorf("run 1: REPLICA region gate flipped ON before its auth Secrets exist")
	}

	// Primary postgres "finishes initdb": re-seed the Secrets.
	seedCNPGPairSourceSecrets(t, primaryCS)

	// Run 2 — idempotent re-run converges: replica flips too.
	_, converged, err = fx.handler.autoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if !converged {
		t.Fatalf("run 2 not converged despite Secrets present")
	}
	if g := gateOf(fx.secondaryKubeconfigPath); g != "true" {
		t.Errorf("run 2: REPLICA region gate = %q, want \"true\"", g)
	}
}

// TestAutoEstablishClusterMesh_IdempotentRerunSkipsRolloutRestart locks
// in the #3241 layer-4 fix: the level-triggered reconcile re-runs the
// orchestrator every ~2 min, and an UNCONDITIONAL rollout-restart per
// pass crash-cycled the mesh components (clustermesh-apiserver
// Deployment reached generation 35 live on hw128) — the agents never
// got a stable window to finish the remote-config sync. Run 1 (Secret
// created) must stamp restartedAt; run 2 (byte-identical peer config)
// must NOT bump it.
func TestAutoEstablishClusterMesh_IdempotentRerunSkipsRolloutRestart(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	// Seed a minimal cilium DaemonSet on the primary so the
	// rollout-restart patch has a real object to stamp (the fake
	// otherwise swallows it as IsNotFound).
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	if _, err := primaryCS.AppsV1().DaemonSets(clusterMeshNamespace).Create(context.Background(),
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: clusterMeshNamespace}},
		metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed cilium DaemonSet: %v", err)
	}
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	stampOf := func() string {
		t.Helper()
		ds, err := primaryCS.AppsV1().DaemonSets(clusterMeshNamespace).Get(context.Background(), "cilium", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get cilium DaemonSet: %v", err)
		}
		return ds.Spec.Template.Annotations["catalyst.openova.io/restartedAt"]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	first := stampOf()
	if first == "" {
		t.Fatalf("run 1 did not stamp restartedAt despite creating the peer Secret")
	}

	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if second := stampOf(); second != first {
		t.Errorf("idempotent re-run bumped restartedAt %q -> %q — the #3241 layer-4 thrash", first, second)
	}
}
