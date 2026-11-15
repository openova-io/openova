package handler

// #7922 — the cross-region replica-auth copies must not be owned by anything
// in the destination cluster.
//
// hw307 (2026-11-15, deployment 9836870916c31984), region b, namespace
// shared-data:
//
//	16:26:46  bp-postgres-shared v1 (crossRegion=false, side=secondary)
//	          renders the #4460 placeholder singleton Cluster `shared-pg`
//	          (initdb, no spec.certificates) -> CNPG mints `shared-pg-ca` and
//	          `shared-pg-replication` with a controller ownerReference to it
//	16:49:53  catalyst-api copies region A's `shared-pg-ca` /
//	          `shared-pg-replication` (and -b, -c) onto those SAME names. The
//	          Update path kept the existing ownerReferences.
//	16:49:57  bp-postgres-shared v2 (crossRegion=true) renders
//	          `shared-pg-replica` and no longer renders `shared-pg`
//	16:50:02  CNPG: `Resource has been deleted {"name":"shared-pg"}`
//	16:50:03  CNPG: `missing specified client CA secret shared-pg-ca:
//	          Secret "shared-pg-ca" not found` -- the garbage collector took
//	          the copies with their owner
//	17:01:10  next steady-state pass re-creates them (Create path, no owner)
//
// The fake clientset has no garbage collector, so the test runs the GC step
// itself: delete every Secret whose ownerReferences name the deleted owner.

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// cnpgClusterOwner returns the controller ownerReference CNPG stamps on the
// PKI Secrets it mints for a Cluster.
func cnpgClusterOwner(name string, uid types.UID) []metav1.OwnerReference {
	controller := true
	return []metav1.OwnerReference{{
		APIVersion: "postgresql.cnpg.io/v1",
		Kind:       "Cluster",
		Name:       name,
		UID:        uid,
		Controller: &controller,
	}}
}

func seedOwnedSecret(t *testing.T, cs kubernetes.Interface, name string, owner []metav1.OwnerReference, payload string) {
	t.Helper()
	typ := corev1.SecretTypeOpaque
	data := map[string][]byte{"ca.crt": []byte(payload + "-crt"), "ca.key": []byte(payload + "-key")}
	if strings.HasSuffix(name, "-replication") {
		typ = corev1.SecretTypeTLS
		data = map[string][]byte{"tls.crt": []byte(payload + "-crt"), "tls.key": []byte(payload + "-key")}
	}
	if _, err := cs.CoreV1().Secrets(sharedPGNamespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       sharedPGNamespace,
			Labels:          map[string]string{"app.kubernetes.io/managed-by": "cloudnative-pg"},
			OwnerReferences: owner,
		},
		Type: typ,
		Data: data,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed Secret %s: %v", name, err)
	}
}

// garbageCollectDependents does what the kube-controller-manager garbage
// collector does after an owner is deleted with background propagation:
// every Secret whose ownerReferences name that owner UID goes too.
func garbageCollectDependents(t *testing.T, cs kubernetes.Interface, ownerUID types.UID) []string {
	t.Helper()
	ctx := context.Background()
	list, err := cs.CoreV1().Secrets(sharedPGNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Secrets: %v", err)
	}
	var deleted []string
	for _, s := range list.Items {
		for _, ref := range s.OwnerReferences {
			if ref.UID == ownerUID {
				if err := cs.CoreV1().Secrets(sharedPGNamespace).Delete(ctx, s.Name, metav1.DeleteOptions{}); err != nil {
					t.Fatalf("GC delete %s: %v", s.Name, err)
				}
				deleted = append(deleted, s.Name)
				break
			}
		}
	}
	return deleted
}

// TestSyncSharedPGReplicaAuthSecrets_CopiesSurvivePlaceholderClusterDeletion_7922
// replays the hw307 sequence: the replica region already holds the
// placeholder Clusters' own CNPG-minted `<instance>-ca` / `-replication`
// (owned by `<instance>`), the sync copies region A's bytes over them, then
// the flip deletes the placeholders and the GC runs. Every copy must survive
// with region A's bytes.
func TestSyncSharedPGReplicaAuthSecrets_CopiesSurvivePlaceholderClusterDeletion_7922(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-7922"}
	ctx := context.Background()

	primaryCS := kfake.NewSimpleClientset()
	replicaCS := kfake.NewSimpleClientset()
	for _, cs := range []kubernetes.Interface{primaryCS, replicaCS} {
		if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: sharedPGNamespace},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace: %v", err)
		}
	}

	placeholderUID := map[string]types.UID{}
	for _, inst := range sharedPGInstanceNames {
		// Region A: the authoritative primary Cluster's PKI.
		aOwner := cnpgClusterOwner(inst, types.UID("region-a-"+inst))
		seedOwnedSecret(t, primaryCS, inst+"-ca", aOwner, "A-"+inst)
		seedOwnedSecret(t, primaryCS, inst+"-replication", aOwner, "A-"+inst)

		// Region B before the flip: the placeholder singleton `<inst>` and the
		// PKI CNPG minted for it, under the very names the sync writes. The
		// `-server` Secret is the control: nothing copies it, so the GC step
		// must remove it (proves the GC simulation is live, not vacuous).
		uid := types.UID("region-b-placeholder-" + inst)
		placeholderUID[inst] = uid
		bOwner := cnpgClusterOwner(inst, uid)
		seedOwnedSecret(t, replicaCS, inst+"-ca", bOwner, "B-placeholder-"+inst)
		seedOwnedSecret(t, replicaCS, inst+"-replication", bOwner, "B-placeholder-"+inst)
		seedOwnedSecret(t, replicaCS, inst+"-server", bOwner, "B-placeholder-"+inst)
	}

	slots := []regionSlot{
		{key: "", clientset: primaryCS},
		{key: "me-east-215-b-1", clientset: replicaCS},
	}
	if !h.syncSharedPGReplicaAuthSecrets(ctx, dep, slots) {
		t.Fatalf("syncSharedPGReplicaAuthSecrets refused with every source present")
	}

	// The flip: Helm deletes each placeholder Cluster, the GC follows.
	for _, inst := range sharedPGInstanceNames {
		deleted := garbageCollectDependents(t, replicaCS, placeholderUID[inst])
		found := false
		for _, n := range deleted {
			if n == inst+"-server" {
				found = true
			}
		}
		if !found {
			t.Fatalf("control failed: GC simulation did not remove %s-server (deleted=%v) — the test would pass vacuously", inst, deleted)
		}
	}

	for _, name := range sharedPGReplicaAuthSecrets {
		got, err := replicaCS.CoreV1().Secrets(sharedPGNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Errorf("replica-auth copy %s/%s was garbage-collected with the placeholder Cluster (%v) — the replica Cluster would sit in `missing specified client CA secret` until the next steady-state pass (hw307: 11 minutes)", sharedPGNamespace, name, err)
			continue
		}
		if len(got.OwnerReferences) != 0 {
			t.Errorf("replica-auth copy %s still carries ownerReferences %v — it is one owner deletion away from the GC", name, got.OwnerReferences)
		}
		src, err := primaryCS.CoreV1().Secrets(sharedPGNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get source %s: %v", name, err)
		}
		for k, v := range src.Data {
			if string(got.Data[k]) != string(v) {
				t.Errorf("replica-auth copy %s key %q holds %q, want region A's %q", name, k, got.Data[k], v)
			}
		}
	}
}

// TestCopySecretAcrossClusters_OrphansOwnedDestination_7922 covers the two
// shapes the sync above does not reach on its own: a destination whose bytes
// ALREADY match but which is still owned (e.g. written by an older
// catalyst-api before this fix), and the create-raced update fallback.
func TestCopySecretAcrossClusters_OrphansOwnedDestination_7922(t *testing.T) {
	ctx := context.Background()
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-pg-ca", Namespace: sharedPGNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"ca.crt": []byte("A-crt"), "ca.key": []byte("A-key")},
	}
	newDst := func(t *testing.T, data map[string][]byte) (*kfake.Clientset, *int) {
		t.Helper()
		dst := kfake.NewSimpleClientset()
		updates := new(int)
		dst.PrependReactor("update", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
			*updates++
			return false, nil, nil
		})
		if _, err := dst.CoreV1().Secrets(sharedPGNamespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            src.Name,
				Namespace:       sharedPGNamespace,
				OwnerReferences: cnpgClusterOwner("shared-pg", "region-b-placeholder"),
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed destination: %v", err)
		}
		return dst, updates
	}
	ownerRefs := func(t *testing.T, dst kubernetes.Interface) []metav1.OwnerReference {
		t.Helper()
		got, err := dst.CoreV1().Secrets(sharedPGNamespace).Get(ctx, src.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get destination: %v", err)
		}
		return got.OwnerReferences
	}

	t.Run("matching-bytes-but-owned-is-orphaned-once", func(t *testing.T) {
		h := &Handler{log: silentLogger()}
		dst, updates := newDst(t, map[string][]byte{"ca.crt": []byte("A-crt"), "ca.key": []byte("A-key")})
		changed, err := h.copySecretAcrossClusters(ctx, src, dst, sharedPGNamespace)
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		if refs := ownerRefs(t, dst); len(refs) != 0 {
			t.Errorf("owned destination with matching bytes kept ownerReferences %v — the steady-state skip left it for the GC", refs)
		}
		if changed {
			t.Errorf("orphan-only write reported changed=true — the bytes did not change, a #4878 restart must not fire")
		}
		if *updates != 1 {
			t.Errorf("orphan-only pass issued %d Update(s), want exactly 1", *updates)
		}
		// The next pass is steady state again: no write.
		if changed, err := h.copySecretAcrossClusters(ctx, src, dst, sharedPGNamespace); err != nil || changed {
			t.Fatalf("second pass: changed=%v err=%v, want false/nil", changed, err)
		}
		if *updates != 1 {
			t.Errorf("orphaned destination was written again (%d Updates) — the heal pass would churn", *updates)
		}
	})

	t.Run("drifted-and-owned-is-overwritten-and-orphaned", func(t *testing.T) {
		h := &Handler{log: silentLogger()}
		dst, _ := newDst(t, map[string][]byte{"ca.crt": []byte("B-crt"), "ca.key": []byte("B-key")})
		changed, err := h.copySecretAcrossClusters(ctx, src, dst, sharedPGNamespace)
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		if !changed {
			t.Errorf("drifted copy reported changed=false")
		}
		if refs := ownerRefs(t, dst); len(refs) != 0 {
			t.Errorf("drifted owned destination kept ownerReferences %v after the Update (the hw307 16:49:53 write)", refs)
		}
	})

	t.Run("create-race-fallback-orphans", func(t *testing.T) {
		h := &Handler{log: silentLogger()}
		dst, _ := newDst(t, map[string][]byte{"ca.crt": []byte("B-crt"), "ca.key": []byte("B-key")})
		desired := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: sharedPGNamespace},
			Type:       src.Type,
			Data:       src.Data,
		}
		changed, err := h.updateCopiedSecret(ctx, desired, dst, sharedPGNamespace)
		if err != nil {
			t.Fatalf("race fallback: %v", err)
		}
		if !changed {
			t.Errorf("race fallback over drifted bytes reported changed=false")
		}
		if refs := ownerRefs(t, dst); len(refs) != 0 {
			t.Errorf("race fallback kept ownerReferences %v", refs)
		}
	})
}
