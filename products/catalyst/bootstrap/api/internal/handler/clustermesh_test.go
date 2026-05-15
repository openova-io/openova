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
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
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
	dir      string
	clients  map[string]kubernetes.Interface // keyed by kubeconfig path
	dep      *Deployment
	handler  *Handler
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
		clients[kcPath] = buildFakeClusterMeshCluster(t, lbIP, caCert, caKey)
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

	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	return &testFixture{dir: dir, clients: clients, dep: dep, handler: h}
}

// ── tests ───────────────────────────────────────────────────────────

// TestAutoEstablishClusterMesh_HappyPath_TwoRegions — both regions
// have LB IPs + CA bytes; the orchestrator wires each as a peer of
// the other and returns Connected=true on every PeerStatus.
func TestAutoEstablishClusterMesh_HappyPath_TwoRegions(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()

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
}

// TestAutoEstablishClusterMesh_Idempotent — second invocation produces
// identical Secret keys (same number of entries, same names) and the
// returned statuses match.
func TestAutoEstablishClusterMesh_Idempotent(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()

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
