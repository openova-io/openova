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
	"regexp"
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

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
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

// seedSharedPGSourceSecrets seeds the 3 shared data instances'
// `<instance>-replication` + `<instance>-ca` Secrets on the PRIMARY (#3571)
// in the shared-data namespace, so the crossRegion flip's cross-cluster copy
// succeeds. Mirrors what CNPG mints once each shared engine's postgres
// initdb's.
func seedSharedPGSourceSecrets(t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sharedPGNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create shared-data namespace: %v", err)
	}
	for _, name := range sharedPGReplicaAuthSecrets {
		typ := corev1.SecretTypeTLS
		data := map[string][]byte{"tls.crt": []byte("SPG-" + name + "-CRT"), "tls.key": []byte("SPG-" + name + "-KEY")}
		if strings.HasSuffix(name, "-ca") {
			typ = corev1.SecretTypeOpaque
			data = map[string][]byte{"ca.crt": []byte("SPG-" + name + "-CA")}
		}
		if _, err := cs.CoreV1().Secrets(sharedPGNamespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sharedPGNamespace},
			Type:       typ,
			Data:       data,
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create shared-pg source Secret %s: %v", name, err)
		}
	}
}

// seedSharedPGReplicaNamespace pre-creates `shared-data` on a replica fake
// clientset (slot 16a ships the Namespace unconditionally on every region).
func seedSharedPGReplicaNamespace(t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sharedPGNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create shared-data namespace on replica: %v", err)
	}
}

// enableSharedPGInFixture flips SOVEREIGN_ENABLE_SHARED_PG=true on every
// region's bootstrap-kit Kustomization substitute AND seeds the shared-pg
// source Secrets on the primary + the shared-data namespace on each replica,
// so the #3571 shared-pg replica-auth sync runs on the crossRegion flip.
func enableSharedPGInFixture(t *testing.T, fx *testFixture) {
	t.Helper()
	// Re-seed each region's dynamic client with a substitute map that adds
	// the shared-pg flag.
	for kcPath := range fx.dynClients {
		sub := defaultBootstrapKitSubstitute()
		sub[clusterMeshSharedPGSubstituteKey] = "true"
		fx.dynClients[kcPath] = newFakeKustomizationDynClient(t, buildBootstrapKitKustomization(sub))
	}
	// Seed the shared-pg source Secrets on the primary, the namespace on
	// replicas.
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	seedSharedPGSourceSecrets(t, primaryCS)
	for kcPath, cs := range fx.clients {
		if kcPath != fx.primaryKubeconfigPath {
			seedSharedPGReplicaNamespace(t, cs)
		}
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
	prev := loadClusterMeshTestClientFactory()
	setClusterMeshTestClientFactory(func(kcPath string) (kubernetes.Interface, bool) {
		c, ok := clients[kcPath]
		return c, ok
	})
	return func() { setClusterMeshTestClientFactory(prev) }
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
	prev := loadClusterMeshTestDynamicClientFactory()
	setClusterMeshTestDynamicClientFactory(func(kcPath string) (dynamic.Interface, bool) {
		c, ok := clients[kcPath]
		return c, ok
	})
	return func() { setClusterMeshTestDynamicClientFactory(prev) }
}

// setClusterMeshLBOverrides sets the atomic LB-discovery poll overrides for
// the duration of the test and restores them on cleanup. Atomic-backed so a
// steady-state heal goroutine leaked by an earlier test (status never leaves
// "ready") reading these in waitForClusterMeshLB cannot data-race this write
// under `go test -race` (#4811 part-b).
func setClusterMeshLBOverrides(t *testing.T, timeout, interval time.Duration) {
	t.Helper()
	prevTimeout := clusterMeshTestOverrideLBTimeoutNanos.Load()
	prevInterval := clusterMeshTestOverrideLBIntervalNanos.Load()
	clusterMeshTestOverrideLBTimeoutNanos.Store(int64(timeout))
	clusterMeshTestOverrideLBIntervalNanos.Store(int64(interval))
	t.Cleanup(func() {
		clusterMeshTestOverrideLBTimeoutNanos.Store(prevTimeout)
		clusterMeshTestOverrideLBIntervalNanos.Store(prevInterval)
	})
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
	setClusterMeshLBOverrides(t, 200*time.Millisecond, 25*time.Millisecond)

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

// watchAndStopSteadyStateOnConverged spawns a background watcher that, the
// moment the reconcile loop emits its final "reconcile loop complete"
// success event, flips the deployment out of "ready". #3583 changed
// runAutoEstablishClusterMesh so first-convergence hands off to the
// (blocking) steady-state heal phase instead of returning; tests that
// assert the bounded CONVERGENCE path (not the steady-state heal) use this
// to release the heal goroutine so runAutoEstablishClusterMesh returns.
// Returns a stop func that ends the watcher. The status flip happens AFTER
// convergence (the success event fires only once the retry loop converged),
// so it never short-circuits the convergence the test is exercising.
func watchAndStopSteadyStateOnConverged(fx *testFixture) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if hasClusterMeshEvent(fx.dep, "info", "reconcile loop complete") {
					fx.dep.mu.Lock()
					if fx.dep.Status == "ready" {
						fx.dep.Status = "wiped"
					}
					fx.dep.mu.Unlock()
					return
				}
			}
		}
	}()
	return func() { close(stop) }
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
	setClusterMeshLBOverrides(t, 150*time.Millisecond, 25*time.Millisecond)
	fx.handler.clusterMeshRetryInitialBackoff = 20 * time.Millisecond
	fx.handler.clusterMeshRetryMaxBackoff = 60 * time.Millisecond
	fx.handler.clusterMeshRetryBudget = 20 * time.Second
	fx.handler.clusterMeshAttemptTimeout = 5 * time.Second
	// #3583: convergence now hands off to the steady-state heal phase rather
	// than returning. Fast interval so the heal goroutine notices the
	// post-convergence status flip (below) and releases runAutoEstablish.
	fx.handler.clusterMeshSteadyStateInterval = 20 * time.Millisecond

	// #3583: once the loop reaches convergence (final success event) flip the
	// deployment out of "ready" so the steady-state heal phase exits and
	// runAutoEstablishClusterMesh returns — this test asserts the convergence
	// path, not the (separately-tested) steady-state heal.
	stopSteady := watchAndStopSteadyStateOnConverged(fx)
	defer stopSteady()

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
	t.Setenv("SOVEREIGN_FQDN", "") // mothership mode for chrootEnsureOrgPoolSeed
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

// TestStartupClusterMeshRetry_SelfHealsTransientKubeconfigMiss is the #4811
// regression: on a fresh 2-region Sovereign restore, restoreFromStore runs
// inside New() sometimes MILLISECONDS before the k8scache dir-load writes the
// primary kubeconfig file to the PVC. The record's omitempty
// Result.KubeconfigPath is empty and the conventional file is not yet on disk,
// so shouldStartupClusterMeshReconcile's os.Stat misses it and the one-shot
// gate returns false. The OLD code dropped the mesh reconcile forever, so the
// establish loop (and its steady-state heal that regenerates a stale
// clustermesh endpoint) never started and region-b stayed ClusterMesh 0/1
// across every OOM restart. The FIX starts a bounded retry that re-checks the
// gate and launches the establish the moment the kubeconfig lands.
func TestStartupClusterMeshRetry_SelfHealsTransientKubeconfigMiss(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// Simulate the transient miss: the record lost its omitempty
	// Result.KubeconfigPath AND the conventional primary file is not yet on the
	// PVC when the reconcile is first evaluated.
	fx.dep.mu.Lock()
	fx.dep.Result.KubeconfigPath = ""
	fx.dep.mu.Unlock()
	if err := os.Remove(fx.primaryKubeconfigPath); err != nil {
		t.Fatalf("remove primary kubeconfig to simulate transient miss: %v", err)
	}

	// Precondition: the one-shot gate skips right now (the bug), but the dep IS
	// a ready multi-region mesh candidate — so the retry SHOULD arm.
	if fx.handler.shouldStartupClusterMeshReconcile(fx.dep) {
		t.Fatalf("precondition: gate must skip while the primary kubeconfig is absent")
	}
	if !fx.handler.clusterMeshReconcileStatusGate(fx.dep) {
		t.Fatalf("precondition: a ready 2-region dep must pass the status gate so the retry arms")
	}

	// Sub-second knobs so the retry re-checks and the establish converges in ms.
	fx.handler.clusterMeshStartupRetryInterval = 10 * time.Millisecond
	fx.handler.clusterMeshStartupRetryBudget = 10 * time.Second
	fx.handler.clusterMeshRetryInitialBackoff = 20 * time.Millisecond
	fx.handler.clusterMeshRetryMaxBackoff = 60 * time.Millisecond
	fx.handler.clusterMeshRetryBudget = 10 * time.Second
	fx.handler.clusterMeshAttemptTimeout = 5 * time.Second
	fx.handler.clusterMeshSteadyStateInterval = 20 * time.Millisecond

	// Release the (blocking) steady-state heal once convergence lands so the
	// establish goroutine returns cleanly.
	stopSteady := watchAndStopSteadyStateOnConverged(fx)
	defer stopSteady()

	// Arm the bounded retry — it must NOT establish yet (file still absent).
	go fx.handler.retryStartupClusterMeshReconcile(fx.dep)

	// The kubeconfig FILE lands on the PVC shortly after (k8scache dir-load
	// finishes). The retry's next poll now resolves it, passes the gate, and
	// launches the establish → the mesh converges.
	time.Sleep(40 * time.Millisecond)
	if hasClusterMeshEvent(fx.dep, "info", "reconcile loop complete") {
		t.Fatalf("establish fired before the kubeconfig landed — retry must not launch while the primary is unresolved")
	}
	writeFakeKubeconfig(t, fx.dir, fx.dep.ID, "") // re-creates <dir>/<id>.yaml (== fx.primaryKubeconfigPath)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if hasClusterMeshEvent(fx.dep, "info", "reconcile loop complete") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("startup mesh reconcile never fired after the transient kubeconfig miss self-healed; events:\n%s", dumpClusterMeshEvents(fx.dep))
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

// peerEndpointBlob returns the single cilium-clustermesh peer config-blob
// entry (the key whose value carries `endpoints:`) from a region's Secret,
// together with its key. Fails the test if zero or more than one such entry
// exists — the 2-region fixture has exactly one peer per region.
func peerEndpointBlob(t *testing.T, client kubernetes.Interface) (peerKey string, blob []byte) {
	t.Helper()
	secret, err := client.CoreV1().Secrets(clusterMeshNamespace).Get(context.Background(), clusterMeshSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get cilium-clustermesh: %v", err)
	}
	for k, v := range secret.Data {
		if bytes.Contains(v, []byte("endpoints:")) {
			if peerKey != "" {
				t.Fatalf("expected exactly one endpoint config-blob entry, found >1 (%q and %q)", peerKey, k)
			}
			peerKey, blob = k, append([]byte(nil), v...)
		}
	}
	if peerKey == "" {
		t.Fatalf("no endpoint config-blob entry in Secret (keys %v)", secretKeys(secret))
	}
	return peerKey, blob
}

// TestAutoEstablishClusterMesh_RewritesStaleDialPortOnReEstablish pins the
// #4811 part-b migration invariant: an env whose cilium-clustermesh peer
// endpoint was written in a PRE-12379 window (endpoint carries the stale
// KINE `:2379` port) has that endpoint AUTHORITATIVELY corrected to the
// current clusterMeshDialPort() (`:12379`) on the next establish pass —
// the merge in applyClusterMeshSecret overwrites the managed endpoint key
// rather than preserving the stale one. The per-peer cert/key/CA material
// is preserved byte-for-byte across the port-only rewrite.
//
// Live proof this matters: on hw228 the mesh stayed ClusterMesh 0/1 until
// the secret endpoint was patched `:2379`→`:12379` and cilium rolled. No
// prior test asserted the endpoint PORT at all (they only checked the
// 4-key entry COUNT), so a buildPeerConfigBlob regression that hardcoded
// `:2379` would have shipped green — this test closes that gap.
func TestAutoEstablishClusterMesh_RewritesStaleDialPortOnReEstablish(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pass 1 — no dial-port override, so clusterMeshDialPort() == the
	// default 2379. This seeds each region's Secret with a peer endpoint
	// carrying the stale KINE port, exactly the pre-12379-window shape.
	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("pass 1 (default port): %v", err)
	}
	primary := fx.clients[fx.primaryKubeconfigPath]
	stalePeerKey, staleBlob := peerEndpointBlob(t, primary)
	staleEndpoint := fmt.Sprintf("https://%s:%d", peerMeshHostname(stalePeerKey), clusterMeshAPIServerPort)
	if !bytes.Contains(staleBlob, []byte(staleEndpoint)) {
		t.Fatalf("pass 1 endpoint blob does not carry default port %d:\n%s",
			clusterMeshAPIServerPort, staleBlob)
	}
	// Snapshot the peer cert/key/CA bytes so we can prove they survive the
	// port-only rewrite untouched.
	staleSecret, err := primary.CoreV1().Secrets(clusterMeshNamespace).Get(ctx, clusterMeshSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get stale secret: %v", err)
	}
	certMaterial := map[string][]byte{}
	for _, suffix := range []string{"-ca.crt", ".crt", ".key"} {
		k := stalePeerKey + suffix
		v, ok := staleSecret.Data[k]
		if !ok {
			t.Fatalf("expected cert material key %q in seeded secret (keys %v)", k, secretKeys(staleSecret))
		}
		certMaterial[k] = append([]byte(nil), v...)
	}

	// Pass 2 — env now sets the clustermesh-proxy dial port (12379), the
	// no-CCM Huawei value. The establish must REWRITE the stale endpoint.
	t.Setenv(clusterMeshDialPortEnvVar, "12379")
	if got := clusterMeshDialPort(""); got != 12379 {
		t.Fatalf("clusterMeshDialPort() = %d after env set, want 12379", got)
	}
	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("pass 2 (12379 override): %v", err)
	}

	freshPeerKey, freshBlob := peerEndpointBlob(t, primary)
	if freshPeerKey != stalePeerKey {
		t.Fatalf("peer key changed across passes: %q -> %q", stalePeerKey, freshPeerKey)
	}
	wantEndpoint := fmt.Sprintf("https://%s:12379", peerMeshHostname(freshPeerKey))
	if !bytes.Contains(freshBlob, []byte(wantEndpoint)) {
		t.Errorf("endpoint NOT corrected to :12379 after re-establish; blob:\n%s", freshBlob)
	}
	if bytes.Contains(freshBlob, []byte(staleEndpoint)) {
		t.Errorf("stale :2379 endpoint PRESERVED after re-establish (merge kept it instead of overwriting):\n%s", freshBlob)
	}

	// Cert/key/CA material preserved byte-for-byte — only the port moved.
	freshSecret, err := primary.CoreV1().Secrets(clusterMeshNamespace).Get(ctx, clusterMeshSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get fresh secret: %v", err)
	}
	for k, want := range certMaterial {
		got, ok := freshSecret.Data[k]
		if !ok {
			t.Errorf("cert material key %q dropped by re-establish", k)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("cert material key %q mutated by the port-only rewrite", k)
		}
	}
}

// TestClusterMeshDialPort_CloudAware — #4811. The dial port must be resolved
// from the DIALED region's cloud when the env is unset, because the mothership
// catalyst-api establishes meshes for BOTH clouds from ONE env-unset
// deployment. A no-CCM cloud (Huawei/kom4dc) dials the clustermesh-proxy
// hostPort (12379); a CCM cloud (Hetzner) dials the canonical etcd :2379. An
// explicit env override still wins over the cloud default (single-cloud escape
// hatch). Regression guard: env-unset + provider=huawei previously returned
// 2379 (KINE) and wedged every Huawei Sovereign at ClusterMesh 0/1.
func TestClusterMeshDialPort_CloudAware(t *testing.T) {
	// Ensure no ambient override leaks in from the environment.
	t.Setenv(clusterMeshDialPortEnvVar, "")

	cases := []struct {
		provider string
		want     int
	}{
		{"huawei", clusterMeshProxyDialPort}, // no-CCM → proxy hostPort 12379
		{"hcs", clusterMeshProxyDialPort},
		{"kom4dc", clusterMeshProxyDialPort},
		{"HUAWEI", clusterMeshProxyDialPort}, // case-insensitive
		{"hetzner", clusterMeshAPIServerPort}, // CCM real LB → 2379
		{"", clusterMeshAPIServerPort},        // unknown → safe 2379 default
		{"aws", clusterMeshAPIServerPort},
	}
	for _, c := range cases {
		if got := clusterMeshDialPort(c.provider); got != c.want {
			t.Errorf("clusterMeshDialPort(%q) = %d, want %d", c.provider, got, c.want)
		}
	}

	// Explicit env override wins over the cloud-aware default, for either cloud.
	t.Setenv(clusterMeshDialPortEnvVar, "12379")
	if got := clusterMeshDialPort("hetzner"); got != 12379 {
		t.Errorf("env override ignored for hetzner: got %d, want 12379", got)
	}
	t.Setenv(clusterMeshDialPortEnvVar, "2379")
	if got := clusterMeshDialPort("huawei"); got != 2379 {
		t.Errorf("env override ignored for huawei: got %d, want 2379", got)
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
	setClusterMeshLBOverrides(t, 200*time.Millisecond, 25*time.Millisecond)

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

// TestAutoEstablishClusterMesh_SharedPGEnabled_SyncsSharedReplicaAuthThenFlips
// (#3571) — when SOVEREIGN_ENABLE_SHARED_PG=true the bp-postgres shared
// instances (shared-pg/-b/-c) ride the SAME crossRegion flip, so their
// `<instance>-replication`/`-ca` Secrets (namespace shared-data) must also be
// copied primary → replica before the gate flips. Assert all 6 land on the
// replica byte-identical AND the gate flips on both regions.
func TestAutoEstablishClusterMesh_SharedPGEnabled_SyncsSharedReplicaAuthThenFlips(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	enableSharedPGInFixture(t, fx)
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// Sanity: the replica starts WITHOUT the shared-pg source Secrets.
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	for _, name := range sharedPGReplicaAuthSecrets {
		if _, err := replicaCS.CoreV1().Secrets(sharedPGNamespace).Get(context.Background(), name, metav1.GetOptions{}); err == nil {
			t.Fatalf("precondition: replica unexpectedly already has shared-pg Secret %s", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	requireFullyMeshed(t, statuses)

	// All 6 shared-pg source Secrets must now exist on the replica, byte-identical.
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	for _, name := range sharedPGReplicaAuthSecrets {
		got, err := replicaCS.CoreV1().Secrets(sharedPGNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("replica missing copied shared-pg Secret %s — replica WAL-stream auth would dangle: %v", name, err)
		}
		src, _ := primaryCS.CoreV1().Secrets(sharedPGNamespace).Get(ctx, name, metav1.GetOptions{})
		for k, v := range src.Data {
			if string(got.Data[k]) != string(v) {
				t.Errorf("shared-pg Secret %s key %q: replica=%q want %q (copy not byte-identical)", name, k, got.Data[k], v)
			}
		}
	}

	// AND the gate flipped on BOTH regions (so the shared-pg replica halves render).
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
}

// TestAutoEstablishClusterMesh_SharedPGEnabled_MissingSourceRefusesReplicaFlip
// (#3571) — shared-pg ON but one shared engine's source Secret not yet minted
// (its postgres mid-bootstrap) → the replica side is refused (its gate stays
// OFF) while the primary side flips (stage 1). Mirrors the cnpg-pair
// missing-source guard, extended to the shared-pg secret set.
func TestAutoEstablishClusterMesh_SharedPGEnabled_MissingSourceRefusesReplicaFlip(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	enableSharedPGInFixture(t, fx)
	// Remove ONE shared-pg source Secret from the primary.
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	if err := primaryCS.CoreV1().Secrets(sharedPGNamespace).Delete(context.Background(), "shared-pg-b-replication", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete shared-pg source Secret: %v", err)
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

	// Two-stage: primary flips, replica refused until the shared-pg auth lands.
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateNotFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
	if !strings.Contains(logBuf.String(), "shared-pg replica-auth sync: source Secret unavailable on primary") {
		t.Errorf("expected a loud warning about the missing shared-pg source Secret, got:\n%s", logBuf.String())
	}
}

// TestAutoEstablishClusterMesh_SharedPGDisabled_DoesNotBlockFlip (#3571) —
// when SOVEREIGN_ENABLE_SHARED_PG is absent/false the shared instances render
// empty releases, CNPG never mints their Secrets, and the shared-pg sync must
// be SKIPPED entirely — the cnpg-pair flip proceeds even with NO shared-pg
// source Secrets present. This is the default-shape regression-lock.
func TestAutoEstablishClusterMesh_SharedPGDisabled_DoesNotBlockFlip(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	// Deliberately do NOT enable shared-pg and do NOT seed any shared-pg
	// Secret (the default fixture). The cnpg-pair sync still has its own
	// secrets seeded by newTestFixture.
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

	// The gate flips on BOTH regions despite ZERO shared-pg Secrets present —
	// the shared-pg sync was skipped (flag off), not refused.
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
	// And no shared-pg Secret was conjured onto the replica.
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	if _, err := replicaCS.CoreV1().Secrets(sharedPGNamespace).Get(ctx, "shared-pg-replication", metav1.GetOptions{}); err == nil {
		t.Errorf("shared-pg Secret appeared on the replica despite shared-pg being disabled")
	}
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
	// #3583: release the post-convergence steady-state heal phase so the loop
	// returns (this test asserts the convergence path).
	fx.handler.clusterMeshSteadyStateInterval = 20 * time.Millisecond
	stopSteady := watchAndStopSteadyStateOnConverged(fx)
	defer stopSteady()

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

// TestBuildPeerConfigBlob_AlwaysDialsVIP2379 locks in #4765 (founder
// 2026-07-03, "FUCK THE NODEPORTS!!!"): peers ALWAYS dial the clustermesh
// VIP on :2379 — NodePort is ABSOLUTELY FORBIDDEN. The pre-#4765
// node-owned-EIP NodePort fallback (resolveClusterMeshDialPort, which
// returned the Service NodePort on the DNAT'd Huawei EIP — the #3241
// shape) is DELETED: the single sovereign-vip LB-IPAM pool gives the
// Service a real VIP that Cilium's kube-proxy-replacement LB frontend
// serves on 2379. The peer endpoint therefore always carries :2379 with
// the canonical *.mesh.cilium.io hostname (TLS SAN), and NEVER a
// 30000-32767 NodePort.
func TestBuildPeerConfigBlob_AlwaysDialsVIP2379(t *testing.T) {
	blob := string(buildPeerConfigBlob("hw224-me-east-b", clusterMeshAPIServerPort))
	want := "- https://hw224-me-east-b.mesh.cilium.io:2379"
	if !strings.Contains(blob, want) {
		t.Errorf("blob missing canonical VIP dial %q:\n%s", want, blob)
	}
	// No NodePort (30000-32767) may ever appear on the peer endpoint.
	if m := regexp.MustCompile(`mesh\.cilium\.io:3[0-2][0-9]{3}`).FindString(blob); m != "" {
		t.Errorf("blob dials a forbidden NodePort %q (#4765):\n%s", m, blob)
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

// TestShouldStartupClusterMeshReconcile_RescuesTimeoutRecords locks in
// the #3285/hw130 doctrine gap: a Phase-1 TIMEOUT record ("components
// observed, none hard-failed") keeps converging under Flux and must
// NOT be abandoned by the mesh orchestrator — previously the gate's
// status=="ready" check excluded it forever (hw130 sat fully converged
// with 0/0 mesh). failed-by-TIMEOUT is rescuable; hard failures stay
// excluded.
func TestShouldStartupClusterMeshReconcile_RescuesTimeoutRecords(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw130timeoutrescue"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	regions := []provisioner.RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	mk := func(status, outcome string) *Deployment {
		return &Deployment{ID: depID, Status: status,
			Request: provisioner.Request{Regions: regions},
			Result:  &provisioner.Result{Phase1Outcome: outcome}}
	}
	if !h.shouldStartupClusterMeshReconcile(mk("failed", helmwatch.OutcomeTimeout)) {
		t.Errorf("failed+timeout record must be rescued (the hw130 abandonment)")
	}
	if h.shouldStartupClusterMeshReconcile(mk("failed", helmwatch.OutcomeFailed)) {
		t.Errorf("failed+hard-failure record must stay excluded")
	}
	if h.shouldStartupClusterMeshReconcile(mk("failed", "flux-not-reconciling")) {
		t.Errorf("flux-not-reconciling record must stay excluded")
	}
	if !h.shouldStartupClusterMeshReconcile(mk("ready", helmwatch.OutcomeReady)) {
		t.Errorf("ready record must still pass")
	}
}

// ── #3583 steady-state self-heal + copy-skip-on-match ───────────────────

// TestCopySecretAcrossClusters_SkipsUpdateWhenUnchanged locks in the
// #3583 write-elision: copying a Secret that already matches the source on
// the destination must NOT issue an Update (so a steady-state heal pass
// stays a cheap Get+compare). A fake-clientset "update" reactor counts the
// Update calls: zero across an unchanged re-copy proves the write was
// elided; one more after the source drifts proves the heal write still
// fires.
func TestCopySecretAcrossClusters_SkipsUpdateWhenUnchanged(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dst := kfake.NewSimpleClientset()
	var updates int
	dst.PrependReactor("update", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil // fall through to the default tracker
	})
	ctx := context.Background()
	if _, err := dst.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sharedPGNamespace},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-pg-replication", Namespace: sharedPGNamespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("CRT-1"), "tls.key": []byte("KEY-1")},
	}

	// First copy creates the Secret (a Create, not an Update).
	// #4878: the create path must report changed=true so the caller rolls the
	// consumer that had booted against a divergent local password.
	changed, err := h.copySecretAcrossClusters(ctx, src, dst, sharedPGNamespace)
	if err != nil {
		t.Fatalf("first copy (create): %v", err)
	}
	if !changed {
		t.Errorf("create path reported changed=false, want true — #4878 rollout-restart would never fire on a first-copy")
	}
	if updates != 0 {
		t.Fatalf("create path issued %d Update(s), want 0", updates)
	}

	// Second copy with an IDENTICAL source must be a no-op — no Update, and
	// #4878: changed=false so a steady-state heal pass never restart-thrashes.
	changed, err = h.copySecretAcrossClusters(ctx, src, dst, sharedPGNamespace)
	if err != nil {
		t.Fatalf("second copy (unchanged): %v", err)
	}
	if changed {
		t.Errorf("unchanged re-copy reported changed=true, want false — #4878 rollout-restart would thrash the consumer every level-trigger pass")
	}
	if updates != 0 {
		t.Errorf("unchanged re-copy issued %d Update(s), want 0 — write-elision regressed (a heal pass would churn the apiserver every interval)", updates)
	}

	// Now drift the source — the next copy MUST write (exactly one Update)
	// so a genuine heal still lands, and #4878: changed=true so the consumer
	// gets rolled onto the corrected credential.
	drifted := src.DeepCopy()
	drifted.Data["tls.crt"] = []byte("CRT-2-HEALED")
	changed, err = h.copySecretAcrossClusters(ctx, drifted, dst, sharedPGNamespace)
	if err != nil {
		t.Fatalf("third copy (drifted): %v", err)
	}
	if !changed {
		t.Errorf("drifted re-copy reported changed=false, want true — #4878 rollout-restart would never fire on a genuine password rotation")
	}
	if updates != 1 {
		t.Errorf("drifted re-copy issued %d Update(s), want exactly 1 — the heal Update never fired", updates)
	}
	afterHeal, err := dst.CoreV1().Secrets(sharedPGNamespace).Get(ctx, src.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after heal copy: %v", err)
	}
	if string(afterHeal.Data["tls.crt"]) != "CRT-2-HEALED" {
		t.Errorf("drifted re-copy left stale bytes %q, want %q", afterHeal.Data["tls.crt"], "CRT-2-HEALED")
	}
}

// TestSecretContentMatches covers the StringData/Type-aware comparison the
// write-elision relies on, including the apiserver's StringData→Data fold.
func TestSecretContentMatches(t *testing.T) {
	base := func() *corev1.Secret {
		return &corev1.Secret{
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{"tls.crt": []byte("A"), "tls.key": []byte("B")},
		}
	}
	if !secretContentMatches(base(), base()) {
		t.Errorf("identical Secrets must match")
	}
	// Type drift → no match.
	a, b := base(), base()
	b.Type = corev1.SecretTypeOpaque
	if secretContentMatches(a, b) {
		t.Errorf("Type drift must NOT match")
	}
	// Data drift → no match.
	a, b = base(), base()
	b.Data["tls.crt"] = []byte("Z")
	if secretContentMatches(a, b) {
		t.Errorf("Data drift must NOT match")
	}
	// StringData on the desired side folds into the effective data and must
	// compare equal to a destination that already carries it under .Data.
	desired := &corev1.Secret{Type: corev1.SecretTypeOpaque, StringData: map[string]string{"k": "v"}}
	existing := &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("v")}}
	if !secretContentMatches(existing, desired) {
		t.Errorf("StringData(desired) vs folded Data(existing) must match (apiserver fold semantics)")
	}
	// nil guard.
	if secretContentMatches(nil, base()) || secretContentMatches(base(), nil) {
		t.Errorf("nil operand must never match")
	}
}

// TestRunClusterMeshSteadyStateHeal_ReCopiesDeletedReplicaSecret is the
// heart of #3583: after first convergence the steady-state heal phase
// keeps re-running the idempotent establish, so a replica-auth Secret that
// gets collaterally deleted out of the replica's namespace (the hw144
// shared-pg case, and the cnpg-pair case) is re-copied on the next pass —
// no catalyst-api restart required. Drives runClusterMeshSteadyStateHeal
// directly with a sub-second interval; deletes BOTH a shared-pg and a
// cnpg-pair replica Secret; asserts both reappear; then flips status away
// from "ready" to terminate the goroutine (the wipe path).
func TestRunClusterMeshSteadyStateHeal_ReCopiesDeletedReplicaSecret(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	enableSharedPGInFixture(t, fx)
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	// Land first convergence: one establish copies every replica-auth Secret
	// and flips the gate. (This is the state the retry loop reaches right
	// before it hands off to the steady-state heal phase.)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("initial establish: %v", err)
	}
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	// Sanity: both a shared-pg and a cnpg-pair replica Secret are present.
	sharedVictim := sharedPGReplicaAuthSecrets[0]
	cnpgVictim := cnpgPairReplicaAuthSecrets[0]
	if _, err := replicaCS.CoreV1().Secrets(sharedPGNamespace).Get(ctx, sharedVictim, metav1.GetOptions{}); err != nil {
		t.Fatalf("precondition: shared-pg replica Secret %s should exist after first establish: %v", sharedVictim, err)
	}
	if _, ok := getCNPGSecret(t, replicaCS, cnpgVictim); !ok {
		t.Fatalf("precondition: cnpg-pair replica Secret %s should exist after first establish", cnpgVictim)
	}

	// hw144 shape: convergence churn collaterally deletes the replica-auth
	// Secrets out of the replica's namespaces.
	if err := replicaCS.CoreV1().Secrets(sharedPGNamespace).Delete(ctx, sharedVictim, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete shared-pg replica Secret: %v", err)
	}
	if err := replicaCS.CoreV1().Secrets(cnpgPairNamespace).Delete(ctx, cnpgVictim, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete cnpg-pair replica Secret: %v", err)
	}

	// Sub-second heal cadence so the next pass fires in milliseconds.
	fx.handler.clusterMeshSteadyStateInterval = 20 * time.Millisecond
	fx.handler.clusterMeshAttemptTimeout = 5 * time.Second

	healDone := make(chan struct{})
	go func() {
		fx.handler.runClusterMeshSteadyStateHeal(fx.dep)
		close(healDone)
	}()

	// Poll for both Secrets to reappear — the steady-state pass self-heals.
	deadline := time.Now().Add(10 * time.Second)
	healed := false
	for time.Now().Before(deadline) {
		_, sErr := replicaCS.CoreV1().Secrets(sharedPGNamespace).Get(context.Background(), sharedVictim, metav1.GetOptions{})
		_, cOK := getCNPGSecret(t, replicaCS, cnpgVictim)
		if sErr == nil && cOK {
			healed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !healed {
		t.Fatalf("steady-state heal did not re-copy the deleted replica Secrets;\nevents:\n%s", dumpClusterMeshEvents(fx.dep))
	}

	// The re-copied Secrets must be byte-identical to the primary source —
	// a hollow placeholder would not authenticate the WAL stream.
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	srcShared, err := primaryCS.CoreV1().Secrets(sharedPGNamespace).Get(context.Background(), sharedVictim, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get primary shared-pg source %s: %v", sharedVictim, err)
	}
	gotShared, err := replicaCS.CoreV1().Secrets(sharedPGNamespace).Get(context.Background(), sharedVictim, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get re-healed shared-pg replica %s: %v", sharedVictim, err)
	}
	for k, v := range srcShared.Data {
		if string(gotShared.Data[k]) != string(v) {
			t.Errorf("re-healed shared-pg Secret %s key %q: replica=%q want %q (heal copy not byte-identical)", sharedVictim, k, gotShared.Data[k], v)
		}
	}

	// Flip status away from ready (the wipe path) — the heal goroutine must
	// observe it and terminate promptly.
	fx.dep.mu.Lock()
	fx.dep.Status = "wiped"
	fx.dep.mu.Unlock()
	select {
	case <-healDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("steady-state heal goroutine did not stop after status left ready")
	}
}

// TestRunClusterMeshSteadyStateHeal_StopsImmediatelyWhenNotReady proves the
// heal goroutine exits at once if the deployment already left status=ready
// before the first pass (a wipe that lands in the convergence→steady-state
// handoff window) — it must never hurl an establish at a tearing-down
// cluster.
func TestRunClusterMeshSteadyStateHeal_StopsImmediatelyWhenNotReady(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	fx.dep.mu.Lock()
	fx.dep.Status = "wiping"
	fx.dep.mu.Unlock()
	fx.handler.clusterMeshSteadyStateInterval = 1 * time.Hour // would block forever if entered

	done := make(chan struct{})
	go func() {
		fx.handler.runClusterMeshSteadyStateHeal(fx.dep)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("steady-state heal did not return immediately when status != ready at entry")
	}
}

// ── #3629: cross-region consumer HUB Secret sync ────────────────────────

// getSharedDataSecret reads a Secret from the shared-data namespace of a fake
// clientset, returning (nil, false) when absent.
func getSharedDataSecret(t *testing.T, cs kubernetes.Interface, name string) (*corev1.Secret, bool) {
	t.Helper()
	s, err := cs.CoreV1().Secrets(sharedPGNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get Secret %s/%s: %v", sharedPGNamespace, name, err)
	}
	return s, true
}

// seedHubSecret creates a consumer hub Secret in shared-data on the given
// clientset with the supplied host + password (mirrors what bp-postgres
// role-secrets.yaml renders). Carries the reflector-auto annotations so the
// copy is byte-identical to production.
func seedHubSecret(t *testing.T, cs kubernetes.Interface, name, host, password string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sharedPGNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create shared-data namespace: %v", err)
	}
	if _, err := cs.CoreV1().Secrets(sharedPGNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: sharedPGNamespace,
			Annotations: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed":      "true",
				"reflector.v1.k8s.emberstack.com/reflection-auto-enabled": "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"host":     []byte(host),
			"password": []byte(password),
			"uri":      []byte("postgresql://owner:" + password + "@" + host + ":5432/db"),
		},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create hub Secret %s: %v", name, err)
	}
}

// seedMangledHubSecret creates a vc-mgmt MANGLED hub Secret (role-secrets.yaml
// Pass 4, #3878) in shared-data — same shape as seedHubSecret but carrying the
// `reflection-auto-namespaces: mgmt` annotation the mangled copy uses to
// auto-push into the vCluster host namespace `mgmt` (#4158).
func seedMangledHubSecret(t *testing.T, cs kubernetes.Interface, name, host, password string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sharedPGNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create shared-data namespace: %v", err)
	}
	if _, err := cs.CoreV1().Secrets(sharedPGNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: sharedPGNamespace,
			Annotations: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed":            "true",
				"reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces": "mgmt",
				"reflector.v1.k8s.emberstack.com/reflection-auto-enabled":       "true",
				"reflector.v1.k8s.emberstack.com/reflection-auto-namespaces":    "mgmt",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"host":     []byte(host),
			"password": []byte(password),
			"uri":      []byte("postgresql://owner:" + password + "@" + host + ":5432/db"),
		},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create mangled hub Secret %s: %v", name, err)
	}
}

// TestSyncSharedPGConsumerHubSecrets covers the #3629 best-effort cross-region
// consumer-hub Secret sync: (1) a `-mesh-rw` source propagates to the replica
// (correct host + password, overwriting the replica's divergent copy); (2) a
// region-local `-rw` source is DEFERRED (not pushed — it would NXDOMAIN on the
// replica); (3) a missing source is skipped (consumer unconfigured); (4)
// single-region is a no-op.
func TestSyncSharedPGConsumerHubSecrets(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-3629"}

	t.Run("mesh-rw-source-propagates-to-replica", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		// Primary hub carries the topology-aware -mesh-rw host + the
		// AUTHORITATIVE region-A password.
		seedHubSecret(t, primaryCS, "grafana-database-env",
			"shared-pg-b-mesh-rw.shared-data.svc.cluster.local", "E5WJ-region-A-pw")
		// Replica starts with the DIVERGENT singleton-phase copy (region-local
		// host + its OWN random password) — the exact hw147 region-B defect.
		seedHubSecret(t, replicaCS, "grafana-database-env",
			"shared-pg-b-rw.shared-data.svc.cluster.local", "RXq1-region-B-pw")

		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

		got, ok := getSharedDataSecret(t, replicaCS, "grafana-database-env")
		if !ok {
			t.Fatalf("replica hub Secret missing after sync")
		}
		if h := string(got.Data["host"]); h != "shared-pg-b-mesh-rw.shared-data.svc.cluster.local" {
			t.Errorf("replica host = %q, want the -mesh-rw host (region-local -rw would NXDOMAIN)", h)
		}
		if p := string(got.Data["password"]); p != "E5WJ-region-A-pw" {
			t.Errorf("replica password = %q, want the authoritative region-A password (the replica's catalog has region-A's role pw)", p)
		}
		// The reflector-auto annotations carry over so the replica's reflector
		// re-pushes the corrected Secret into the consumer namespace.
		if got.Annotations["reflector.v1.k8s.emberstack.com/reflection-auto-enabled"] != "true" {
			t.Errorf("replica hub Secret lost the reflection-auto annotation — consumer ns would not get the corrected copy")
		}
	})

	t.Run("region-local-rw-source-is-deferred", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		// Primary hub still carries the SINGLETON -rw host (region-A has not
		// reconciled crossRegion=true yet) — must NOT be pushed.
		seedHubSecret(t, primaryCS, "grafana-database-env",
			"shared-pg-b-rw.shared-data.svc.cluster.local", "pw-A")
		// Replica has its own divergent copy; it must be left UNTOUCHED.
		seedHubSecret(t, replicaCS, "grafana-database-env",
			"shared-pg-b-rw.shared-data.svc.cluster.local", "pw-B-stale")

		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

		got, _ := getSharedDataSecret(t, replicaCS, "grafana-database-env")
		if p := string(got.Data["password"]); p != "pw-B-stale" {
			t.Errorf("replica password = %q, want it UNCHANGED (pw-B-stale) — a -rw source must be deferred, not pushed", p)
		}
	})

	t.Run("missing-source-is-skipped", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset() // no hub Secrets at all
		replicaCS := kfake.NewSimpleClientset()
		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		// Must not panic / error — best-effort skip of every unconfigured
		// consumer.
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)
		if _, ok := getSharedDataSecret(t, replicaCS, "grafana-database-env"); ok {
			t.Errorf("replica unexpectedly gained a hub Secret from an empty primary")
		}
	})

	t.Run("single-region-is-noop", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		seedHubSecret(t, primaryCS, "grafana-database-env",
			"shared-pg-b-rw.shared-data.svc.cluster.local", "pw")
		// len(slots) == 1 → no replica → must return immediately, no panic.
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, []regionSlot{{key: "", clientset: primaryCS}})
	})

	// #4158: the vc-mgmt MANGLED hub copies (rendered primary-side only by
	// role-secrets.yaml Pass 4 for the 4 in-vc-mgmt consumers) must cross the
	// mesh too — otherwise region-B's mgmt-vCluster keycloak (and gitea/harbor/
	// grafana) wedge at `FailedMount … secret "<…>-x-<ns>-x-mgmt-vcluster" not
	// found`. The copy must carry the `reflection-auto-namespaces: mgmt`
	// annotation through so the replica's own reflector re-pushes it into the
	// replica's `mgmt` namespace (the object the in-vc pod actually mounts).
	t.Run("mangled-vc-mgmt-copies-propagate-to-replica-with-mgmt-autoreflect", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		// Seed all four mangled vc-mgmt hub copies on the primary, carrying the
		// `-mesh-rw` host (readiness gate) + the `mgmt` auto-reflect annotation.
		mangled := []string{
			"keycloak-database-secret-x-keycloak-x-mgmt-vcluster",
			"gitea-database-secret-x-gitea-x-mgmt-vcluster",
			"harbor-database-secret-x-harbor-x-mgmt-vcluster",
			"grafana-database-env-x-grafana-x-mgmt-vcluster",
		}
		for _, name := range mangled {
			seedMangledHubSecret(t, primaryCS, name,
				"shared-pg-mesh-rw.shared-data.svc.cluster.local", "region-A-pw")
		}

		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

		for _, name := range mangled {
			got, ok := getSharedDataSecret(t, replicaCS, name)
			if !ok {
				t.Fatalf("replica missing mangled vc-mgmt hub Secret %q after sync — region-B mgmt-vCluster pod would FailedMount", name)
			}
			if p := string(got.Data["password"]); p != "region-A-pw" {
				t.Errorf("%s: replica password = %q, want the authoritative region-A password", name, p)
			}
			// The `mgmt` auto-namespace annotation MUST carry over so the
			// replica's reflector re-pushes the mangled object into `mgmt`.
			if ns := got.Annotations["reflector.v1.k8s.emberstack.com/reflection-auto-namespaces"]; ns != "mgmt" {
				t.Errorf("%s: replica lost reflection-auto-namespaces=mgmt (got %q) — the in-vc pod's host Secret would never materialise in region-B", name, ns)
			}
		}
	})
}

// TestAutoEstablishClusterMesh_ConsumerHubSecretsSyncPreMesh locks in #5230:
// the #3629 consumer-hub Secret sync must fire from the EARLY per-slot phase of
// autoEstablishClusterMesh — clustermesh NOT required — not from behind the
// countFullyMeshedRegions full-mesh gate inside enableCNPGPairAfterFullMesh.
// Region B deliberately has NO LB ingress, so the mesh can NEVER fully
// establish and the cnpg-pair flip path is never reached; under the pre-#5230
// wiring the replica's hub Secret therefore stayed divergent for the whole
// mesh-establishment window (hw274: region-B harbor-core sat in
// CreateContainerConfigError `secret "harbor-database-secret" not found` for
// ≈22m37s on every fresh 2-region prov). The only prerequisites the sync
// genuinely has: region-A's authoritative `-mesh-rw` hub Secret + a reachable
// replica clientset — both present here.
func TestAutoEstablishClusterMesh_ConsumerHubSecretsSyncPreMesh(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", ""})
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()
	// Shrink the LB lookup timeout so region B's absent LB fails fast.
	setClusterMeshLBOverrides(t, 200*time.Millisecond, 25*time.Millisecond)

	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	// Region-A's hub is AUTHORITATIVE from first render on a fresh prov
	// (topology-aware -mesh-rw host — the hw274 21:15:53 state).
	seedHubSecret(t, primaryCS, "harbor-database-secret",
		"shared-pg-mesh-rw.shared-data.svc.cluster.local", "region-A-authoritative-pw")
	// Region-B carries its divergent singleton-phase copy (the defect state
	// the sync must overwrite without waiting for the mesh).
	seedHubSecret(t, replicaCS, "harbor-database-secret",
		"shared-pg-rw.shared-data.svc.cluster.local", "region-B-divergent-pw")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}

	// Sanity: the mesh must NOT be fully established (region B has no LB) —
	// this is exactly the window in which the pre-#5230 wiring never synced.
	if fullyMeshed := countFullyMeshedRegions(statuses); fullyMeshed == len(statuses) {
		t.Fatalf("fixture invalid: mesh fully established (%d/%d) — the pre-mesh window this test locks in did not occur", fullyMeshed, len(statuses))
	}
	// Sanity: the cnpg-pair flip must not have landed either (the old call
	// site's gate) — proves the sync observed below ran OUTSIDE the flip.
	ks := getBootstrapKitKustomization(t, fx.dynClients[fx.secondaryKubeconfigPath])
	substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if substitute[clusterMeshCNPGPairSubstituteKey] == "true" {
		t.Fatalf("fixture invalid: SOVEREIGN_ENABLE_CNPG_PAIR flipped ON despite the mesh never establishing")
	}

	// THE #5230 ASSERTION: region-B's hub Secret already carries region-A's
	// authoritative host + password — synced pre-mesh, on the very first pass.
	got, ok := getSharedDataSecret(t, replicaCS, "harbor-database-secret")
	if !ok {
		t.Fatalf("replica hub Secret missing — pre-mesh consumer-hub sync did not run (#5230 regression: sync still gated behind the full-mesh flip)")
	}
	if h := string(got.Data["host"]); h != "shared-pg-mesh-rw.shared-data.svc.cluster.local" {
		t.Errorf("replica host = %q, want the -mesh-rw host synced PRE-mesh (#5230)", h)
	}
	if p := string(got.Data["password"]); p != "region-A-authoritative-pw" {
		t.Errorf("replica password = %q, want region-A's authoritative password synced PRE-mesh (#5230) — harbor-core would sit in CreateContainerConfigError until the full-mesh flip otherwise", p)
	}
}

// TestReconcileSharedPGConsumerRestart locks in the #4878 RESIDUAL fix
// (live-verified on hw232): the consumer rollout-restart must NOT fire the
// instant the shared-data hub Secret changes — it must wait until the corrected
// region-A credential has actually PROPAGATED into the CONSUMER's own namespace
// (the emberstack reflector re-push). Firing early recreates the pod against the
// STALE mounted Secret and it crashloops on `password authentication failed`
// (keycloak-0 crashlooped 5× 06:43:50→06:47:23Z). Once the credential is
// consistent in-namespace it restarts EXACTLY ONCE (idempotent per credential
// fingerprint), so a steady-state / re-checked pass never thrashes (#3241/#3583).
func TestReconcileSharedPGConsumerRestart(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-4878"}

	const (
		ns         = "keycloak"
		secretName = "keycloak-database-secret"
		stsName    = "keycloak"
	)
	target := sharedPGConsumerRestartTargets[secretName] // statefulset keycloak/keycloak
	const regionAHost = "shared-pg-mesh-rw.shared-data.svc.cluster.local"
	const regionAPassword = "E5WJ-region-A-pw"

	// authoritative is the region-A hub Secret the sync just copied onto the
	// replica's shared-data (the `src` argument).
	authoritative := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: sharedPGNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"host":     []byte(regionAHost),
				"password": []byte(regionAPassword),
				"uri":      []byte("postgresql://keycloak:" + regionAPassword + "@" + regionAHost + ":5432/keycloak"),
			},
		}
	}
	// consumerSecret is the CONSUMER-namespace reflected copy with a given
	// password — byte-identical to authoritative() when pw == regionAPassword.
	consumerSecret := func(pw string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"host":     []byte(regionAHost),
				"password": []byte(pw),
				"uri":      []byte("postgresql://keycloak:" + pw + "@" + regionAHost + ":5432/keycloak"),
			},
		}
	}
	newReplica := func(t *testing.T, consumerPW string, withConsumerSecret bool) kubernetes.Interface {
		t.Helper()
		cs := kfake.NewSimpleClientset()
		ctx := context.Background()
		if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create ns: %v", err)
		}
		if _, err := cs.AppsV1().StatefulSets(ns).Create(ctx, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName, Namespace: ns}}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create sts: %v", err)
		}
		if withConsumerSecret {
			if _, err := cs.CoreV1().Secrets(ns).Create(ctx, consumerSecret(consumerPW), metav1.CreateOptions{}); err != nil {
				t.Fatalf("create consumer secret: %v", err)
			}
		}
		return cs
	}
	restartStampOf := func(t *testing.T, cs kubernetes.Interface) (stamp, hash string) {
		t.Helper()
		sts, err := cs.AppsV1().StatefulSets(ns).Get(context.Background(), stsName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get sts: %v", err)
		}
		return sts.Spec.Template.Annotations["catalyst.openova.io/restartedAt"],
			sts.Spec.Template.Annotations[sharedPGConsumerCredHashAnnotation]
	}

	t.Run("defers-when-consumer-ns-secret-absent", func(t *testing.T) {
		cs := newReplica(t, "", false)
		slot := &regionSlot{key: "secondary", clientset: cs}
		h.reconcileSharedPGConsumerRestart(context.Background(), dep, slot, secretName, target, authoritative())
		if stamp, _ := restartStampOf(t, cs); stamp != "" {
			t.Errorf("restarted despite the consumer-ns Secret being absent (stamp=%q) — the reflector had not propagated yet", stamp)
		}
	})

	t.Run("defers-when-consumer-ns-secret-stale", func(t *testing.T) {
		cs := newReplica(t, "RXq1-region-B-STALE-pw", true)
		slot := &regionSlot{key: "secondary", clientset: cs}
		h.reconcileSharedPGConsumerRestart(context.Background(), dep, slot, secretName, target, authoritative())
		if stamp, _ := restartStampOf(t, cs); stamp != "" {
			t.Errorf("restarted while the consumer-ns Secret still carried the STALE password (stamp=%q) — this is exactly the premature-restart crashloop the residual fix prevents", stamp)
		}
	})

	t.Run("restarts-and-is-idempotent-once-consistent", func(t *testing.T) {
		cs := newReplica(t, regionAPassword, true) // consumer-ns == authoritative
		slot := &regionSlot{key: "secondary", clientset: cs}
		h.reconcileSharedPGConsumerRestart(context.Background(), dep, slot, secretName, target, authoritative())
		stamp, hash := restartStampOf(t, cs)
		if stamp == "" {
			t.Fatalf("did NOT restart even though the consumer-ns credential is consistent with region-A — the stale in-process password would never clear")
		}
		if want := sharedPGConsumerCredentialFingerprint(authoritative()); hash != want {
			t.Errorf("cred-hash annotation = %q, want the authoritative fingerprint %q", hash, want)
		}
		// Idempotent re-run: consumer still consistent AND the workload is already
		// rolled onto this fingerprint → must NOT restart again (no thrash).
		h.reconcileSharedPGConsumerRestart(context.Background(), dep, slot, secretName, target, authoritative())
		if stamp2, _ := restartStampOf(t, cs); stamp2 != stamp {
			t.Errorf("idempotent re-run bumped restartedAt %q -> %q — the #3241/#3583 no-thrash contract broke", stamp, stamp2)
		}
	})

	t.Run("re-fires-after-propagation", func(t *testing.T) {
		// Pass 1 — consumer-ns still STALE → deferred, no restart (the hw232
		// window where the old code fired prematurely and crashlooped).
		cs := newReplica(t, "region-B-STALE", true)
		slot := &regionSlot{key: "secondary", clientset: cs}
		h.reconcileSharedPGConsumerRestart(context.Background(), dep, slot, secretName, target, authoritative())
		if stamp, _ := restartStampOf(t, cs); stamp != "" {
			t.Fatalf("pass 1 restarted against the stale credential (stamp=%q)", stamp)
		}
		// The reflector catches up: the consumer-ns Secret now carries region-A's
		// authoritative password.
		if _, err := cs.CoreV1().Secrets(ns).Update(context.Background(), consumerSecret(regionAPassword), metav1.UpdateOptions{}); err != nil {
			t.Fatalf("update consumer secret to consistent: %v", err)
		}
		// Pass 2 — now consistent → the restart fires exactly once.
		h.reconcileSharedPGConsumerRestart(context.Background(), dep, slot, secretName, target, authoritative())
		if stamp, _ := restartStampOf(t, cs); stamp == "" {
			t.Fatalf("pass 2 did NOT restart after the corrected credential propagated — the consumer would stay pinned to the stale in-process password")
		}
	})
}

// seedKeycloakNamedSecret creates a keycloak admin Secret with the given name +
// data key in the host `keycloak` namespace (keycloakAdminSecretNamespace) on the
// given clientset — mirrors what the #4325 de-vcluster host keycloak install +
// #4344/#2914 chart templates materialise directly on the host.
func seedKeycloakNamedSecret(t *testing.T, cs kubernetes.Interface, name, key, value string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: keycloakAdminSecretNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create %s namespace: %v", keycloakAdminSecretNamespace, err)
	}
	if _, err := cs.CoreV1().Secrets(keycloakAdminSecretNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: keycloakAdminSecretNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{key: []byte(value)},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create keycloak Secret %s: %v", name, err)
	}
}

// seedKeycloakAdminSecret seeds the primary `keycloak-admin` admin-password Secret
// (the master-realm credential config-cli reads).
func seedKeycloakAdminSecret(t *testing.T, cs kubernetes.Interface, adminPassword string) {
	t.Helper()
	seedKeycloakNamedSecret(t, cs, keycloakAdminSecretName, "admin-password", adminPassword)
}

func getKeycloakNamedSecret(t *testing.T, cs kubernetes.Interface, name string) (*corev1.Secret, bool) {
	t.Helper()
	s, err := cs.CoreV1().Secrets(keycloakAdminSecretNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get keycloak Secret %s: %v", name, err)
	}
	return s, true
}

func getKeycloakAdminSecret(t *testing.T, cs kubernetes.Interface) (*corev1.Secret, bool) {
	t.Helper()
	return getKeycloakNamedSecret(t, cs, keycloakAdminSecretName)
}

// TestSyncKeycloakAdminSecret covers the #4158/#4915 best-effort cross-region
// keycloak master-realm admin Secret sync: (1) region-A's authoritative
// admin-password overwrites the replica's DIVERGENT local value (the live
// region-B 401 root cause); (2) a missing source is skipped (keycloak still
// installing); (3) single-region is a no-op; (4) region-A (slots[0]) is NEVER
// written — the primary's working keycloak must not regress; (5) #4915 — BOTH
// host-ns admin Secrets (keycloak-admin + catalyst-kc-master-admin-credentials)
// are synced so config-cli AND sso-bridge authenticate against the shared DB.
func TestSyncKeycloakAdminSecret(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-4158"}

	t.Run("region-A-admin-password-overwrites-replica-divergent-copy", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		// Region-A: the AUTHORITATIVE password whose hash lives in the
		// cross-region-replicated keycloak catalog.
		seedKeycloakAdminSecret(t, primaryCS, "1b58-region-A-pw")
		// Region-B: its OWN divergent random password — the bitnami subchart
		// generated a fresh one at install (the live 2e7e… defect) → 401.
		seedKeycloakAdminSecret(t, replicaCS, "2e7e-region-B-pw")

		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		h.syncKeycloakAdminSecret(context.Background(), dep, slots)

		got, ok := getKeycloakAdminSecret(t, replicaCS)
		if !ok {
			t.Fatalf("replica keycloak admin Secret missing after sync")
		}
		if p := string(got.Data["admin-password"]); p != "1b58-region-A-pw" {
			t.Errorf("replica admin-password = %q, want the authoritative region-A password (the replica's keycloak catalog has region-A's admin hash → only region-A's pw authenticates)", p)
		}
		// Region-A (the source) must be UNTOUCHED — the primary keycloak is
		// working and must not regress.
		src, _ := getKeycloakAdminSecret(t, primaryCS)
		if p := string(src.Data["admin-password"]); p != "1b58-region-A-pw" {
			t.Errorf("primary admin-password changed to %q — the source region must never be written", p)
		}
	})

	t.Run("missing-source-is-skipped", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset() // no keycloak admin Secret yet
		replicaCS := kfake.NewSimpleClientset()
		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		// Must not panic / error — best-effort skip while keycloak still installs.
		h.syncKeycloakAdminSecret(context.Background(), dep, slots)
		if _, ok := getKeycloakAdminSecret(t, replicaCS); ok {
			t.Errorf("replica unexpectedly gained an admin Secret from an empty primary")
		}
	})

	t.Run("single-region-is-noop", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		seedKeycloakAdminSecret(t, primaryCS, "pw")
		// len(slots) == 1 → no replica → must return immediately, no panic.
		h.syncKeycloakAdminSecret(context.Background(), dep, []regionSlot{{key: "", clientset: primaryCS}})
	})

	// #4915: post-#4325 de-vcluster, keycloak runs on the HOST `keycloak` ns and
	// bp-sso-bridge reads catalyst-kc-master-admin-credentials for per-Org realm
	// creation. Both host-ns admin Secrets must adopt region-A's value (the shared
	// keycloak DB, seeded by region-A, only accepts region-A's password), not just
	// keycloak-admin. This is the concrete gap #4915 fixes.
	t.Run("both-host-admin-secrets-synced-to-replica", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		// region-A authoritative values (whose hash the shared DB holds).
		seedKeycloakNamedSecret(t, primaryCS, keycloakAdminSecretName, "admin-password", "mMPW-region-A-pw")
		seedKeycloakNamedSecret(t, primaryCS, "catalyst-kc-master-admin-credentials", "master-realm-admin-password", "mMPW-region-A-pw")
		// region-B divergent locally-generated values → 401 until synced.
		seedKeycloakNamedSecret(t, replicaCS, keycloakAdminSecretName, "admin-password", "aevF-region-B-pw")
		seedKeycloakNamedSecret(t, replicaCS, "catalyst-kc-master-admin-credentials", "master-realm-admin-password", "aevF-region-B-pw")

		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		h.syncKeycloakAdminSecret(context.Background(), dep, slots)

		gotAdmin, ok := getKeycloakNamedSecret(t, replicaCS, keycloakAdminSecretName)
		if !ok {
			t.Fatalf("replica keycloak-admin Secret missing after sync")
		}
		if p := string(gotAdmin.Data["admin-password"]); p != "mMPW-region-A-pw" {
			t.Errorf("replica keycloak-admin admin-password = %q, want region-A's authoritative value", p)
		}
		gotMaster, ok := getKeycloakNamedSecret(t, replicaCS, "catalyst-kc-master-admin-credentials")
		if !ok {
			t.Fatalf("replica catalyst-kc-master-admin-credentials Secret missing after sync (#4915 — sso-bridge would 401 on per-Org realm creation)")
		}
		if p := string(gotMaster.Data["master-realm-admin-password"]); p != "mMPW-region-A-pw" {
			t.Errorf("replica catalyst-kc-master-admin-credentials master-realm-admin-password = %q, want region-A's authoritative value (#4915)", p)
		}
		// Region-A (the source) must be UNTOUCHED for both Secrets.
		if src, _ := getKeycloakNamedSecret(t, primaryCS, keycloakAdminSecretName); string(src.Data["admin-password"]) != "mMPW-region-A-pw" {
			t.Errorf("primary keycloak-admin was written — the source region must never be modified")
		}
	})
}

func seedSSOOIDCMangledSecret(t *testing.T, cs kubernetes.Interface, name, clientSecret string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ssoOIDCMangledHostSecretsNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create %s namespace: %v", ssoOIDCMangledHostSecretsNamespace, err)
	}
	if _, err := cs.CoreV1().Secrets(ssoOIDCMangledHostSecretsNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ssoOIDCMangledHostSecretsNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET": []byte(clientSecret)},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create SSO-OIDC mangled Secret %s: %v", name, err)
	}
}

func getSSOOIDCMangledSecret(t *testing.T, cs kubernetes.Interface, name string) (*corev1.Secret, bool) {
	t.Helper()
	s, err := cs.CoreV1().Secrets(ssoOIDCMangledHostSecretsNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get SSO-OIDC mangled Secret %s: %v", name, err)
	}
	return s, true
}

// TestSyncSSOOIDCMangledSecrets covers the #4158 best-effort cross-region per-app
// SSO/OIDC credential sync (the layer above the #4159 DB-secret + #4162
// admin-secret fixes): (1) region-A's RESOLVED mangled SSO Secret is delivered
// into the replica's host `mgmt` namespace where region-B's ESO 403s and never
// materialises it (the live grafana CreateContainerConfigError root cause); (2) a
// source not yet resolved on region-A is skipped per-Secret (the others still
// sync); (3) single-region is a no-op; (4) region-A (slots[0]) is NEVER written.
func TestSyncSSOOIDCMangledSecrets(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-4158-sso"}
	grafanaName := "grafana-sso-oidc-credentials-x-grafana-x-mgmt-vcluster"
	harborName := "harbor-sso-oidc-credentials-x-harbor-x-mgmt-vcluster"

	t.Run("region-A-resolved-sso-secret-delivered-to-replica", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		// Region-A: ESO resolved the OIDC client creds from its OWN host-apiserver
		// OpenBao auth. Region-B: NOTHING — its ESO 403s, the grafana Pod wedges
		// CreateContainerConfigError on the absent mangled Secret.
		seedSSOOIDCMangledSecret(t, primaryCS, grafanaName, "grafana-client-secret-A")
		// Replica namespace must exist for the copy target.
		if _, err := replicaCS.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ssoOIDCMangledHostSecretsNamespace},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create replica mgmt ns: %v", err)
		}

		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		h.syncSSOOIDCMangledSecrets(context.Background(), dep, slots)

		got, ok := getSSOOIDCMangledSecret(t, replicaCS, grafanaName)
		if !ok {
			t.Fatalf("replica %s missing after sync — grafana would stay CreateContainerConfigError", grafanaName)
		}
		if s := string(got.Data["GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET"]); s != "grafana-client-secret-A" {
			t.Errorf("replica grafana SSO client_secret = %q, want region-A's resolved value", s)
		}
		// Region-A (the source) must be UNTOUCHED.
		src, _ := getSSOOIDCMangledSecret(t, primaryCS, grafanaName)
		if s := string(src.Data["GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET"]); s != "grafana-client-secret-A" {
			t.Errorf("primary grafana SSO client_secret changed to %q — the source region must never be written", s)
		}
	})

	t.Run("per-secret-source-miss-is-independent", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		// Only grafana has resolved on region-A; harbor's ESO hasn't yet. The
		// harbor miss must NOT block grafana's delivery.
		seedSSOOIDCMangledSecret(t, primaryCS, grafanaName, "grafana-client-secret-A")
		if _, err := replicaCS.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ssoOIDCMangledHostSecretsNamespace},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create replica mgmt ns: %v", err)
		}
		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "secondary", clientset: replicaCS},
		}
		h.syncSSOOIDCMangledSecrets(context.Background(), dep, slots)

		if _, ok := getSSOOIDCMangledSecret(t, replicaCS, grafanaName); !ok {
			t.Errorf("grafana SSO Secret not delivered despite being resolved on region-A (a harbor miss must not block it)")
		}
		if _, ok := getSSOOIDCMangledSecret(t, replicaCS, harborName); ok {
			t.Errorf("harbor SSO Secret unexpectedly delivered from an unresolved source")
		}
	})

	t.Run("single-region-is-noop", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		seedSSOOIDCMangledSecret(t, primaryCS, grafanaName, "x")
		// len(slots) == 1 → no replica → must return immediately, no panic.
		h.syncSSOOIDCMangledSecrets(context.Background(), dep, []regionSlot{{key: "", clientset: primaryCS}})
	})
}

// TestPatchSecondaryCrossRegionPGHosts — #4436. On a 2-region shared-pg
// Sovereign the CNPG primary's region-local `shared-pg-rw` Service exists ONLY
// in region-A; the SECONDARY region has no such Service, so keycloak/gitea/
// harbor (which dial the write host as a scalar) NXDOMAIN there. The post-mesh
// gate must re-stamp the SECONDARY region's bootstrap-kit substitute map to the
// ClusterMesh-global `shared-pg-mesh-rw` WRITE alias — self-healing stale (pre-
// #4159) envs whose substitute map carries the region-local `-rw` host (or omits
// the keycloak key entirely → the slot 09 default `shared-pg-rw` wins). The
// PRIMARY region's map is left untouched (its `-rw` Service resolves locally),
// and an own-cluster (non-shared-pg) Sovereign is skipped entirely.
func TestPatchSecondaryCrossRegionPGHosts(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep4436"}

	primaryPath := "/kc/primary.yaml"
	secondaryPath := "/kc/secondary.yaml"
	slots := []regionSlot{
		{key: "", kubeconfigPath: primaryPath},
		{key: testReplicaRegionLabel, kubeconfigPath: secondaryPath},
	}
	meshHost := clusterMeshSharedPGMeshRWHost

	t.Run("stale-secondary-flips-to-mesh-rw-primary-untouched", func(t *testing.T) {
		// PRIMARY carries the region-local -rw host (correct for it) + shared-pg ON.
		primarySub := defaultBootstrapKitSubstitute()
		primarySub[clusterMeshSharedPGSubstituteKey] = "true"
		primarySub[clusterMeshGiteaPGHostSubstituteKey] = "shared-pg-rw.shared-data.svc.cluster.local"
		primarySub[clusterMeshHarborPGHostSubstituteKey] = "shared-pg-rw.shared-data.svc.cluster.local"
		// keycloak key DELIBERATELY ABSENT on the primary too (slot default wins).
		primaryDyn := newFakeKustomizationDynClient(t, buildBootstrapKitKustomization(primarySub))

		// SECONDARY (stale, pre-#4159): gitea/harbor carry the region-local -rw
		// host, keycloak key is ABSENT — exactly the live dep 4635277cae4ffed9 shape.
		secSub := defaultBootstrapKitSubstitute()
		secSub[clusterMeshSharedPGSubstituteKey] = "true"
		secSub[clusterMeshRegionRoleSubstituteKey] = "secondary"
		secSub[clusterMeshGiteaPGHostSubstituteKey] = "shared-pg-rw.shared-data.svc.cluster.local"
		secSub[clusterMeshHarborPGHostSubstituteKey] = "shared-pg-rw.shared-data.svc.cluster.local"
		secondaryDyn := newFakeKustomizationDynClient(t, buildBootstrapKitKustomization(secSub))

		restore := installClusterMeshDynamicClientFactory(map[string]dynamic.Interface{
			primaryPath:   primaryDyn,
			secondaryPath: secondaryDyn,
		})
		defer restore()

		h.patchSecondaryCrossRegionPGHosts(context.Background(), dep, slots)

		// SECONDARY: every write-host key now resolves to the mesh alias.
		secKS := getBootstrapKitKustomization(t, secondaryDyn)
		gotSub, _, _ := unstructured.NestedStringMap(secKS.Object, "spec", "postBuild", "substitute")
		for _, k := range []string{
			clusterMeshKeycloakPGHostSubstituteKey,
			clusterMeshGiteaPGHostSubstituteKey,
			clusterMeshHarborPGHostSubstituteKey,
		} {
			if got := gotSub[k]; got != meshHost {
				t.Errorf("secondary substitute[%s] = %q, want %q (region-local -rw NXDOMAINs the replica region)", k, got, meshHost)
			}
		}
		// Sibling keys must survive the MERGE patch (never replaced).
		if got := gotSub[clusterMeshPrimaryRegionSubstituteKey]; got != testPrimaryRegionLabel {
			t.Errorf("secondary substitute[%s] = %q, want %q (merge patch clobbered a sibling key)", clusterMeshPrimaryRegionSubstituteKey, got, testPrimaryRegionLabel)
		}
		// Flux reconcile must be requested so the flip lands immediately.
		if stamp := secKS.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp == "" {
			t.Errorf("secondary: %q annotation absent — Flux reconcile never requested", fluxReconcileRequestedAtAnnotation)
		}

		// PRIMARY: untouched — keeps the region-local -rw host (resolves locally)
		// and the keycloak key stays ABSENT (no needless indirection).
		priKS := getBootstrapKitKustomization(t, primaryDyn)
		priSub, _, _ := unstructured.NestedStringMap(priKS.Object, "spec", "postBuild", "substitute")
		if got := priSub[clusterMeshGiteaPGHostSubstituteKey]; got != "shared-pg-rw.shared-data.svc.cluster.local" {
			t.Errorf("primary substitute[%s] = %q, want the region-local -rw host (primary must NOT be flipped to mesh)", clusterMeshGiteaPGHostSubstituteKey, got)
		}
		if _, present := priSub[clusterMeshKeycloakPGHostSubstituteKey]; present {
			t.Errorf("primary substitute[%s] should stay ABSENT (slot default resolves locally); patch wrongly touched the primary", clusterMeshKeycloakPGHostSubstituteKey)
		}
		if stamp := priKS.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp != "" {
			t.Errorf("primary: %q annotation set — primary must not be patched/reconciled", fluxReconcileRequestedAtAnnotation)
		}
	})

	t.Run("already-mesh-rw-is-noop", func(t *testing.T) {
		secSub := defaultBootstrapKitSubstitute()
		secSub[clusterMeshSharedPGSubstituteKey] = "true"
		secSub[clusterMeshRegionRoleSubstituteKey] = "secondary"
		secSub[clusterMeshKeycloakPGHostSubstituteKey] = meshHost
		secSub[clusterMeshGiteaPGHostSubstituteKey] = meshHost
		secSub[clusterMeshHarborPGHostSubstituteKey] = meshHost
		secondaryDyn := newFakeKustomizationDynClient(t, buildBootstrapKitKustomization(secSub))
		primaryDyn := newFakeKustomizationDynClient(t, buildBootstrapKitKustomization(defaultBootstrapKitSubstitute()))

		restore := installClusterMeshDynamicClientFactory(map[string]dynamic.Interface{
			primaryPath:   primaryDyn,
			secondaryPath: secondaryDyn,
		})
		defer restore()

		h.patchSecondaryCrossRegionPGHosts(context.Background(), dep, slots)

		// No-op: nothing to change → no reconcile stamp (the merge patch never fired).
		secKS := getBootstrapKitKustomization(t, secondaryDyn)
		if stamp := secKS.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp != "" {
			t.Errorf("secondary already carries -mesh-rw on every key — expected a no-op, but a reconcile stamp %q was written", stamp)
		}
	})

	t.Run("own-cluster-shared-pg-off-is-skipped", func(t *testing.T) {
		// shared-pg DISABLED → keycloak/gitea/harbor dial their OWN <app>-pg-rw
		// hosts (resolve locally) → must NOT be touched.
		secSub := defaultBootstrapKitSubstitute()
		secSub[clusterMeshRegionRoleSubstituteKey] = "secondary"
		// SOVEREIGN_ENABLE_SHARED_PG intentionally absent (own-cluster).
		secondaryDyn := newFakeKustomizationDynClient(t, buildBootstrapKitKustomization(secSub))
		primaryDyn := newFakeKustomizationDynClient(t, buildBootstrapKitKustomization(defaultBootstrapKitSubstitute()))

		restore := installClusterMeshDynamicClientFactory(map[string]dynamic.Interface{
			primaryPath:   primaryDyn,
			secondaryPath: secondaryDyn,
		})
		defer restore()

		h.patchSecondaryCrossRegionPGHosts(context.Background(), dep, slots)

		secKS := getBootstrapKitKustomization(t, secondaryDyn)
		gotSub, _, _ := unstructured.NestedStringMap(secKS.Object, "spec", "postBuild", "substitute")
		if _, present := gotSub[clusterMeshKeycloakPGHostSubstituteKey]; present {
			t.Errorf("own-cluster Sovereign: keycloak host key was wrongly stamped (shared-pg is OFF — apps dial their own local host)")
		}
		if stamp := secKS.GetAnnotations()[fluxReconcileRequestedAtAnnotation]; stamp != "" {
			t.Errorf("own-cluster Sovereign: reconcile stamp %q written — must be skipped entirely", stamp)
		}
	})

	t.Run("single-region-is-noop", func(t *testing.T) {
		// len(slots) == 1 → no secondary → return immediately, no panic.
		h.patchSecondaryCrossRegionPGHosts(context.Background(), dep, []regionSlot{{key: "", kubeconfigPath: primaryPath}})
	})
}

// TestClusterMeshReconcileStatusGate_ConsoleDowngradedRecord pins the #5253
// heal arm: a failed record whose Phase1Outcome is "ready" — the signature a
// pre-#5253 build persisted when the #4706 console gate downgraded a fully
// converged primary (hw276) — must be a mesh candidate, so a catalyst-api
// restart re-establishes ClusterMesh instead of leaving the cross-region
// topology inert forever. Genuine failures (any non-OutcomeReady,
// non-OutcomeTimeout Phase1Outcome) and single-region records stay excluded,
// and the existing ready + failed-by-TIMEOUT (#3317) arms are unchanged.
func TestClusterMeshReconcileStatusGate_ConsoleDowngradedRecord(t *testing.T) {
	h := &Handler{log: silentLogger()}
	regions := func(n int) []provisioner.RegionSpec {
		out := make([]provisioner.RegionSpec, n)
		for i := range out {
			out[i] = provisioner.RegionSpec{Provider: "huawei"}
		}
		return out
	}
	mk := func(status, outcome string, regionCount int) *Deployment {
		return &Deployment{
			ID:      "gate-5253",
			Status:  status,
			Request: provisioner.Request{Regions: regions(regionCount)},
			Result:  &provisioner.Result{Phase1Outcome: outcome},
		}
	}

	cases := []struct {
		name    string
		status  string
		outcome string
		regions int
		want    bool
	}{
		{"ready multi-region (baseline)", "ready", helmwatch.OutcomeReady, 2, true},
		{"failed+timeout multi-region (#3317 arm intact)", "failed", helmwatch.OutcomeTimeout, 2, true},
		{"failed+OutcomeReady multi-region (#5253 hw276 pre-fix shape)", "failed", helmwatch.OutcomeReady, 2, true},
		{"failed+OutcomeReady single-region", "failed", helmwatch.OutcomeReady, 1, false},
		{"failed+OutcomeFailed (genuine hard failure)", "failed", helmwatch.OutcomeFailed, 2, false},
		{"failed+flux-not-reconciling (genuine hard failure)", "failed", helmwatch.OutcomeFluxNotReconciling, 2, false},
		{"failed+storage-downgrade (#3971 replaces the outcome)", "failed", provisioner.ReasonDefaultStorageClassEphemeral, 2, false},
	}
	for _, c := range cases {
		if got := h.clusterMeshReconcileStatusGate(mk(c.status, c.outcome, c.regions)); got != c.want {
			t.Errorf("%s: gate=%v, want %v", c.name, got, c.want)
		}
	}
}
