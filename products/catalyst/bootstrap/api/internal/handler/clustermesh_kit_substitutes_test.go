package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
)

// sovereignVariablesName is the ConfigMap the control-plane boot template
// writes every substitute into, and names in spec.postBuild.substituteFrom
// of the inline Kustomizations.
const sovereignVariablesName = "sovereign-variables"

// buildBootstrapKitKustomizationFromSource returns the bootstrap-kit
// Kustomization as a fresh installation has it: NO inline
// spec.postBuild.substitute, only a substituteFrom reference to the
// ConfigMap flux-system/sovereign-variables.
func buildBootstrapKitKustomizationFromSource() *unstructured.Unstructured {
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
				"substituteFrom": []any{
					map[string]any{"kind": "ConfigMap", "name": sovereignVariablesName},
				},
			},
		},
	}}
}

func seedSovereignVariables(t *testing.T, cs kubernetes.Interface, data map[string]string) {
	t.Helper()
	ctx := context.Background()
	_, _ = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: fluxSystemNamespace},
	}, metav1.CreateOptions{})
	if _, err := cs.CoreV1().ConfigMaps(fluxSystemNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: sovereignVariablesName, Namespace: fluxSystemNamespace},
		Data:       data,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create %s: %v", sovereignVariablesName, err)
	}
}

// useVariablesConfigMapInFixture turns the fixture into the shape of a fresh
// installation: both regions' bootstrap-kit Kustomizations carry only
// substituteFrom, and each region's ConfigMap holds the values.
func useVariablesConfigMapInFixture(t *testing.T, fx *testFixture, extra map[string]string) {
	t.Helper()
	for kcPath := range fx.dynClients {
		fx.dynClients[kcPath] = newFakeKustomizationDynClient(t, buildBootstrapKitKustomizationFromSource())
		role := "primary"
		if kcPath != fx.primaryKubeconfigPath {
			role = "secondary"
		}
		data := map[string]string{
			"SOVEREIGN_FQDN":                      "tcm.example.io",
			clusterMeshPrimaryRegionSubstituteKey: testPrimaryRegionLabel,
			clusterMeshReplicaRegionSubstituteKey: testReplicaRegionLabel,
			clusterMeshRegionRoleSubstituteKey:    role,
		}
		for k, v := range extra {
			data[k] = v
		}
		seedSovereignVariables(t, fx.clients[kcPath], data)
	}
}

// TestAutoEstablishClusterMesh_FlipsCNPGPairGateFromVariablesConfigMap is
// the #7902 defect-2 case: on a fresh installation the region keys live in
// the ConfigMap named by substituteFrom, not in an inline map. The switch
// must land in both regions. Before the repair the orchestrator refused it
// ("carries no postBuild.substitute map") on every attempt.
func TestAutoEstablishClusterMesh_FlipsCNPGPairGateFromVariablesConfigMap(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	useVariablesConfigMapInFixture(t, fx, nil)
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	statuses, converged, err := fx.handler.autoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("autoEstablishClusterMesh returned error: %v", err)
	}
	requireFullyMeshed(t, statuses)
	if !converged {
		t.Errorf("database-pair switch did not land: the orchestrator would retry for ever\nevents:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")

	// The switch is written inline (an inline value wins over a source
	// value in Flux); the reference to the ConfigMap must survive the patch.
	for _, kcPath := range []string{fx.primaryKubeconfigPath, fx.secondaryKubeconfigPath} {
		ks := getBootstrapKitKustomization(t, fx.dynClients[kcPath])
		from, found, _ := unstructured.NestedSlice(ks.Object, "spec", "postBuild", "substituteFrom")
		if !found || len(from) != 1 {
			t.Errorf("region %q: spec.postBuild.substituteFrom = %v, want the one ConfigMap reference kept", kcPath, from)
		}
	}
}

// TestAutoEstablishClusterMesh_SharedPGReadFromVariablesConfigMap is the
// second key of the same reader: SOVEREIGN_ENABLE_SHARED_PG decides whether
// the shared engines' replication Secrets are copied to the follower. Read
// from the inline map alone it was never true, so the follower's replicas
// stayed on "missing specified client CA secret".
func TestAutoEstablishClusterMesh_SharedPGReadFromVariablesConfigMap(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	useVariablesConfigMapInFixture(t, fx, map[string]string{clusterMeshSharedPGSubstituteKey: "true"})
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	replicaCS := fx.clients[fx.secondaryKubeconfigPath]
	seedSharedPGSourceSecrets(t, primaryCS)
	seedSharedPGReplicaNamespace(t, replicaCS)
	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	for _, instance := range sharedPGInstanceNames {
		for _, suffix := range []string{"-replication", "-ca"} {
			if _, ok := getSharedDataSecret(t, replicaCS, instance+suffix); !ok {
				t.Errorf("follower has no %s%s: the shared-database key was not read from the ConfigMap", instance, suffix)
			}
		}
	}
}

// TestPatchSecondaryCrossRegionHosts_ReadsVariablesConfigMap: the follower's
// write-host values come from the ConfigMap on a fresh installation. The
// function must (a) run at all, and (b) write only the keys whose effective
// value is wrong: here the two Harbor store keys, not the three database
// hosts the boot template already set.
func TestPatchSecondaryCrossRegionHosts_ReadsVariablesConfigMap(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	useVariablesConfigMapInFixture(t, fx, map[string]string{
		clusterMeshSharedPGSubstituteKey:       "true",
		clusterMeshKeycloakPGHostSubstituteKey: clusterMeshSharedPGMeshRWHost,
		clusterMeshGiteaPGHostSubstituteKey:    clusterMeshSharedPGMeshRWHost,
		clusterMeshHarborPGHostSubstituteKey:   clusterMeshSharedPGMeshRWHost,
	})
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()

	slots := []regionSlot{
		{key: "", kubeconfigPath: fx.primaryKubeconfigPath, clientset: fx.clients[fx.primaryKubeconfigPath]},
		{key: "hel1-1", kubeconfigPath: fx.secondaryKubeconfigPath, clientset: fx.clients[fx.secondaryKubeconfigPath]},
	}
	fx.handler.patchSecondaryCrossRegionHosts(context.Background(), fx.dep, slots)

	ks := getBootstrapKitKustomization(t, fx.dynClients[fx.secondaryKubeconfigPath])
	inline, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if got := inline[clusterMeshHarborRedisTypeSubstituteKey]; got != clusterMeshHarborRedisExternalType {
		t.Errorf("follower inline %s = %q, want %q (the function skipped a Kustomization without an inline map)",
			clusterMeshHarborRedisTypeSubstituteKey, got, clusterMeshHarborRedisExternalType)
	}
	if got := inline[clusterMeshHarborRedisAddrSubstituteKey]; got != clusterMeshHarborRedisMeshAddr {
		t.Errorf("follower inline %s = %q, want %q", clusterMeshHarborRedisAddrSubstituteKey, got, clusterMeshHarborRedisMeshAddr)
	}
	for _, k := range []string{clusterMeshKeycloakPGHostSubstituteKey, clusterMeshGiteaPGHostSubstituteKey, clusterMeshHarborPGHostSubstituteKey} {
		if v, ok := inline[k]; ok {
			t.Errorf("follower inline %s = %q written although the ConfigMap already holds the mesh host", k, v)
		}
	}
	// The primary is never patched.
	pks := getBootstrapKitKustomization(t, fx.dynClients[fx.primaryKubeconfigPath])
	if _, found, _ := unstructured.NestedStringMap(pks.Object, "spec", "postBuild", "substitute"); found {
		t.Errorf("primary Kustomization got an inline map; the function must leave the primary alone")
	}
}

// TestBootTemplateAndOrchestratorAgreeOnSubstituteLocation is the contract
// between the two sides of #7902 defect 2. The reader was left on the old
// location when the boot template moved the values; this fails when the
// template stops naming the ConfigMap the reader follows, or stops setting a
// key the orchestrator decides from.
func TestBootTemplateAndOrchestratorAgreeOnSubstituteLocation(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers", "_shared", "cloudinit-control-plane.tftpl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("boot template not in this build context (%v)", err)
	}
	tpl := string(raw)
	if !strings.Contains(tpl, "substituteFrom:") || !strings.Contains(tpl, "name: "+sovereignVariablesName) {
		t.Fatalf("the boot template no longer references ConfigMap %s through substituteFrom: move crossregion.KitSubstitutes with it", sovereignVariablesName)
	}
	for _, key := range []string{
		clusterMeshPrimaryRegionSubstituteKey,
		clusterMeshReplicaRegionSubstituteKey,
		clusterMeshRegionRoleSubstituteKey,
		clusterMeshSharedPGSubstituteKey,
	} {
		if !strings.Contains(tpl, key+":") && !strings.Contains(tpl, key+"=") {
			t.Errorf("the boot template no longer sets %s, which the ClusterMesh orchestrator decides from", key)
		}
	}
}
