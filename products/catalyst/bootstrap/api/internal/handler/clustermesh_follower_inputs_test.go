package handler

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

func seedPlainSecret(t *testing.T, cs kubernetes.Interface, namespace, name string, data map[string]string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", namespace, err)
	}
	b := make(map[string][]byte, len(data))
	for k, v := range data {
		b[k] = []byte(v)
	}
	if _, err := cs.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       b,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Secret %s/%s: %v", namespace, name, err)
	}
}

func secretValue(t *testing.T, cs kubernetes.Interface, namespace, name, key string) (string, bool) {
	t.Helper()
	s, err := cs.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("get Secret %s/%s: %v", namespace, name, err)
	}
	return string(s.Data[key]), true
}

// TestAutoEstablishClusterMesh_FollowerInputsCopiedBeforeMeshAndPair is the
// #7902 case for defects 3, 4 and 5 together. The follower's mesh address
// never appears, so the mesh is not established and the database-pair switch
// never runs: the window in which the Keycloak admin copy used to be
// unreachable (it sat in the last stage of the switch), and in which nothing
// at all copied the Git auth Secret or the Harbor admin Secret.
func TestAutoEstablishClusterMesh_FollowerInputsCopiedBeforeMeshAndPair(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", ""})
	primaryCS := fx.clients[fx.primaryKubeconfigPath]
	followerCS := fx.clients[fx.secondaryKubeconfigPath]

	// Primary: the values the shared databases hold.
	seedKeycloakAdminSecret(t, primaryCS, "primary-keycloak-admin")
	seedPlainSecret(t, primaryCS, fluxSystemNamespace, "openova-org-tenants-git-auth",
		map[string]string{"username": "gitea_admin", "password": "primary-git-token"})
	seedPlainSecret(t, primaryCS, "harbor", "harbor-admin",
		map[string]string{"HARBOR_ADMIN_PASSWORD": "primary-harbor-admin"})
	// Follower: its own minted values, which the shared databases reject.
	seedKeycloakAdminSecret(t, followerCS, "follower-own-keycloak-admin")
	seedPlainSecret(t, followerCS, "harbor", "harbor-admin",
		map[string]string{"HARBOR_ADMIN_PASSWORD": "follower-own-harbor-admin"})
	_, _ = followerCS.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: fluxSystemNamespace},
	}, metav1.CreateOptions{})

	// The follower's Harbor release is Ready, and its two setup Jobs have
	// failed on the follower's own password. They are plain Jobs, not hooks,
	// so nothing would run them again.
	fx.dynClients[fx.secondaryKubeconfigPath] = newFakeKustomizationDynClient(t,
		buildBootstrapKitKustomization(defaultBootstrapKitSubstitute()),
		buildTestHelmRelease("bp-harbor", hrCondition("Ready", "True", "InstallSucceeded", time.Now().Add(-time.Hour))),
	)
	for _, name := range []string{"harbor-pull-robot-62d6ba9f", "harbor-registry-projects-2cb40aa0"} {
		if _, err := followerCS.BatchV1().Jobs("harbor").Create(context.Background(), &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "harbor", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
				Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "bp-harbor", "helm.toolkit.fluxcd.io/namespace": fluxSystemNamespace}},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{Name: "harbor-admin", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "harbor-admin"}}}},
			}}},
			Status: batchv1.JobStatus{Failed: 4, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed Job %s: %v", name, err)
		}
	}

	restore := installClusterMeshClientFactory(fx.clients)
	defer restore()
	restoreDyn := installClusterMeshDynamicClientFactory(fx.dynClients)
	defer restoreDyn()
	setClusterMeshLBOverrides(t, 200*time.Millisecond, 25*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	statuses, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh returned error: %v", err)
	}
	if fullyMeshed := countFullyMeshedRegions(statuses); fullyMeshed == len(statuses) {
		t.Fatalf("fixture invalid: the mesh established (%d/%d); this test needs the window before it", fullyMeshed, len(statuses))
	}
	assertGateNotFlippedInline(t, fx.dynClients[fx.secondaryKubeconfigPath])

	if got, _ := secretValue(t, followerCS, keycloakAdminSecretNamespace, keycloakAdminSecretName, "admin-password"); got != "primary-keycloak-admin" {
		t.Errorf("follower keycloak admin password = %q, want the primary's: the follower's config-cli answers HTTP 401 against the shared database until its retries are used up (defect 3)", got)
	}
	if got, ok := secretValue(t, followerCS, fluxSystemNamespace, "openova-org-tenants-git-auth", "password"); !ok || got != "primary-git-token" {
		t.Errorf("follower flux-system/openova-org-tenants-git-auth password = %q (present=%v), want the primary's: the follower's sovereign-region source cannot authenticate to its Gitea (defect 4)", got, ok)
	}
	if got, _ := secretValue(t, followerCS, "harbor", "harbor-admin", "HARBOR_ADMIN_PASSWORD"); got != "primary-harbor-admin" {
		t.Errorf("follower harbor admin password = %q, want the primary's: the follower's Harbor setup Jobs answer 401 (defect 5)", got)
	}
	// The primary is the source and is never written.
	if got, _ := secretValue(t, primaryCS, "harbor", "harbor-admin", "HARBOR_ADMIN_PASSWORD"); got != "primary-harbor-admin" {
		t.Errorf("primary harbor admin password changed to %q", got)
	}

	// Defect 5, second half: the Jobs that failed on the old password are
	// gone, and their release is asked for a fresh attempt, which creates
	// them again with the Secret as it is now.
	left, err := followerCS.BatchV1().Jobs("harbor").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(left.Items) != 0 {
		t.Errorf("%d failed Harbor setup Job(s) are still there: nothing runs them again, and the follower never gets its pull login", len(left.Items))
	}
	ann := getFluxSystemHelmRelease(t, fx.dynClients[fx.secondaryKubeconfigPath], "bp-harbor").GetAnnotations()
	token := ann[helmwatch.AnnotationReconcileRequestedAt]
	if token == "" || ann[helmwatch.AnnotationReconcileForceAt] != token {
		t.Errorf("follower bp-harbor annotations = %v, want a retry request after its failed Jobs were removed", ann)
	}

	// A second pass changes nothing and asks for nothing.
	if _, err := fx.handler.AutoEstablishClusterMesh(ctx, fx.dep); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	ann2 := getFluxSystemHelmRelease(t, fx.dynClients[fx.secondaryKubeconfigPath], "bp-harbor").GetAnnotations()
	if ann2[helmwatch.AnnotationReconcileRequestedAt] != token {
		t.Errorf("a pass that copied nothing asked bp-harbor for another attempt (%q → %q)", token, ann2[helmwatch.AnnotationReconcileRequestedAt])
	}
}

func assertGateNotFlippedInline(t *testing.T, dyn dynamic.Interface) {
	t.Helper()
	ks := getBootstrapKitKustomization(t, dyn)
	substitute, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if substitute[clusterMeshCNPGPairSubstituteKey] == "true" {
		t.Fatalf("fixture invalid: the database-pair switch landed although the mesh never established")
	}
}
