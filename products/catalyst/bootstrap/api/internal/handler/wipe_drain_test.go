package handler

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Test_drain_ReleasesLoadBalancersAndCSIVolumes proves the #4677 drain deletes
// the LoadBalancer Services (so CCM releases the LB+EIP) and the PVCs (so CSI
// deletes the cloud volume) — the runtime-provisioned resources tofu destroy
// cannot see. A ClusterIP Service is left untouched.
func Test_drain_ReleasesLoadBalancersAndCSIVolumes(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "kube-system"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "internal", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-abc"},
			Spec:       corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data-postgres-0", Namespace: "cnpg-system"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-abc"},
		},
	)

	drainPVSettleTimeout = 200 * time.Millisecond // keep CI fast
	drainLoadBalancerServices(context.Background(), cs, nil)
	drainPVCs(context.Background(), cs, nil)

	// LoadBalancer Service deleted, ClusterIP Service kept.
	if _, err := cs.CoreV1().Services("kube-system").Get(context.Background(), "gateway", metav1.GetOptions{}); err == nil {
		t.Error("LoadBalancer Service 'gateway' should have been deleted (CCM must release the LB+EIP)")
	}
	if _, err := cs.CoreV1().Services("default").Get(context.Background(), "internal", metav1.GetOptions{}); err != nil {
		t.Error("ClusterIP Service 'internal' must NOT be touched by the drain")
	}

	// PVC deleted → CSI releases the cloud volume.
	if _, err := cs.CoreV1().PersistentVolumeClaims("cnpg-system").Get(context.Background(), "data-postgres-0", metav1.GetOptions{}); err == nil {
		t.Error("PVC 'data-postgres-0' should have been deleted (CSI must release the EVS volume)")
	}

	// The bound PV's reclaim policy was forced to Delete so the volume is
	// actually removed even though the StorageClass default was Retain.
	pv, err := cs.CoreV1().PersistentVolumes().Get(context.Background(), "pvc-abc", metav1.GetOptions{})
	if err == nil && pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
		t.Errorf("PV reclaim policy = %q, want Delete (else Retain orphans the volume)", pv.Spec.PersistentVolumeReclaimPolicy)
	}
}

// Test_drain_EmptyKubeconfigNoOp proves a wipe of an already-dead env (no
// kubeconfig) no-ops cleanly rather than erroring — the wipe proceeds to tofu
// destroy + the cloud-GC backstop.
func Test_drain_EmptyKubeconfigNoOp(t *testing.T) {
	if err := drainClusterCloudResources(context.Background(), nil, nil); err != nil {
		t.Fatalf("empty kubeconfig should no-op, got %v", err)
	}
	if err := drainClusterCloudResources(context.Background(), []byte("not-a-kubeconfig"), nil); err != nil {
		t.Fatalf("bad kubeconfig should no-op (best-effort), got %v", err)
	}
}
