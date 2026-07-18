// Tests for #5224 — managed-role password re-assert through CNPG's
// passwordSecret-resourceVersion contract (the hw273 harbor 28P01
// lockout heal, structured).

package cnpg

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func gvrListMapWithSecrets() map[schema.GroupVersionResource]string {
	m := gvrListMap()
	m[SecretGVR] = "SecretList"
	return m
}

// newClusterWithManagedRoles builds a CNPG Cluster CR carrying
// spec.managed.roles entries — the bp-postgres cluster.yaml shape
// (one role per unique owner, passwordSecret per role).
func newClusterWithManagedRoles(ns, name string, roleToSecret map[string]string) *unstructured.Unstructured {
	c := newCluster(ns, name, nil, nil, false)
	roles := []interface{}{}
	for role, secret := range roleToSecret {
		entry := map[string]interface{}{
			"name":   role,
			"ensure": "present",
			"login":  true,
		}
		if secret != "" {
			entry["passwordSecret"] = map[string]interface{}{"name": secret}
		}
		roles = append(roles, entry)
	}
	_ = unstructured.SetNestedSlice(c.Object, roles, "spec", "managed", "roles")
	return c
}

func newRoleSecret(ns, name string) *unstructured.Unstructured {
	s := &unstructured.Unstructured{}
	s.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
	s.SetNamespace(ns)
	s.SetName(name)
	_ = unstructured.SetNestedStringMap(s.Object, map[string]string{
		"username": "harbor",
		"password": "canonical-password",
	}, "stringData")
	return s
}

func TestManagedRolePasswordSecrets_ParsesAndDedupes(t *testing.T) {
	t.Parallel()
	c := newCluster("shared-data", "shared-pg", nil, nil, false)
	_ = unstructured.SetNestedSlice(c.Object, []interface{}{
		map[string]interface{}{"name": "harbor", "passwordSecret": map[string]interface{}{"name": "shared-pg-harbor"}},
		map[string]interface{}{"name": "gitea", "passwordSecret": map[string]interface{}{"name": "shared-pg-gitea"}},
		// Duplicate secret ref (two roles sharing one Secret) — deduped.
		map[string]interface{}{"name": "gitea_ro", "passwordSecret": map[string]interface{}{"name": "shared-pg-gitea"}},
		// No passwordSecret at all — skipped.
		map[string]interface{}{"name": "nopass"},
	}, "spec", "managed", "roles")

	got := ManagedRolePasswordSecrets(c)
	want := []string{"shared-pg-harbor", "shared-pg-gitea"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestManagedRolePasswordSecrets_NoManagedRoles(t *testing.T) {
	t.Parallel()
	// The bp-postgres replica-half CR carries NO managed.roles — must be
	// a clean nil (no-op re-assert), never a guess.
	c := newCluster("shared-data", "shared-pg-replica", nil, nil, true)
	if got := ManagedRolePasswordSecrets(c); got != nil {
		t.Fatalf("replica-half CR: got %v want nil", got)
	}
	if got := ManagedRolePasswordSecrets(nil); got != nil {
		t.Fatalf("nil CR: got %v want nil", got)
	}
}

func TestReassertManagedRoles_TouchesSecretRV(t *testing.T) {
	t.Parallel()
	const ns = "shared-data"
	cluster := newClusterWithManagedRoles(ns, "shared-pg", map[string]string{"harbor": "shared-pg-harbor"})
	secret := newRoleSecret(ns, "shared-pg-harbor")
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMapWithSecrets(), cluster, secret)
	r := NewReader(dyn)

	at := time.Date(2026, 7, 18, 18, 35, 9, 0, time.UTC)
	touched, err := r.ReassertManagedRoles(context.Background(), ns, "shared-pg", at)
	if err != nil {
		t.Fatalf("ReassertManagedRoles: %v", err)
	}
	if len(touched) != 1 || touched[0] != "shared-pg-harbor" {
		t.Fatalf("touched = %v want [shared-pg-harbor]", touched)
	}

	got, gErr := dyn.Resource(SecretGVR).Namespace(ns).Get(context.Background(), "shared-pg-harbor", metav1.GetOptions{})
	if gErr != nil {
		t.Fatalf("readback: %v", gErr)
	}
	ann := got.GetAnnotations()
	if ann[RoleReassertAnnotation] != "2026-07-18T18:35:09Z" {
		t.Fatalf("annotation = %q want the RFC3339 touch stamp", ann[RoleReassertAnnotation])
	}
	// The credential bytes must be UNTOUCHED — the re-assert is a
	// metadata-only rv bump, never a rotation.
	pw, _, _ := unstructured.NestedString(got.Object, "stringData", "password")
	if pw != "canonical-password" {
		t.Fatalf("password bytes changed: %q — the re-assert must never rotate the credential", pw)
	}
}

func TestReassertManagedRoles_MissingSecretSkipped(t *testing.T) {
	t.Parallel()
	const ns = "shared-data"
	// The pre-flip placeholder shape: the CR references role Secrets a
	// replica-role region never mints (bp-postgres >=0.2.13 side-gate).
	// Missing Secrets are SKIPPED — no error, no touch.
	cluster := newClusterWithManagedRoles(ns, "shared-pg", map[string]string{"harbor": "shared-pg-harbor"})
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMapWithSecrets(), cluster)
	r := NewReader(dyn)

	touched, err := r.ReassertManagedRoles(context.Background(), ns, "shared-pg", time.Now())
	if err != nil {
		t.Fatalf("missing Secret must be skipped, got error: %v", err)
	}
	if len(touched) != 0 {
		t.Fatalf("touched = %v want none", touched)
	}
}

func TestReassertManagedRoles_NoManagedRolesIsNoOp(t *testing.T) {
	t.Parallel()
	const ns = "shared-data"
	cluster := newCluster(ns, "shared-pg-replica", nil, nil, true)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMapWithSecrets(), cluster)
	r := NewReader(dyn)

	touched, err := r.ReassertManagedRoles(context.Background(), ns, "shared-pg-replica", time.Now())
	if err != nil {
		t.Fatalf("no managed roles must be a clean no-op, got: %v", err)
	}
	if len(touched) != 0 {
		t.Fatalf("touched = %v want none", touched)
	}
}

func TestReassertManagedRoles_ClusterAbsentErrors(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMapWithSecrets())
	r := NewReader(dyn)
	if _, err := r.ReassertManagedRoles(context.Background(), "ns", "absent", time.Now()); err == nil {
		t.Fatal("absent Cluster CR must surface an error (caller retries next tick)")
	}
}
