// Default-StorageClass durability probe (#3971).
//
// The P0 blocker #3971: k3s ships local-path (rancher.io/local-path) as the
// cluster-default StorageClass, and with no cloud block-storage CSI installed
// EVERY PVC on a Sovereign landed on ephemeral node-local disk — a node
// replacement destroys the data (prod Postgres, openbao secrets, customer DB).
// The bootstrap-kit now installs a per-provider cloud CSI (bp-hcloud-csi on
// Hetzner) and flips the default class to the durable cloud volume.
//
// DefaultStorageClassFromKubeconfig is the read-only probe the Phase-1 DoD
// gate (handler.markPhase1Done) uses to FAIL a prov whose resolved default
// StorageClass is still local-path — so a Sovereign can never silently come
// up on ephemeral storage again. Per docs/PRINCIPLES.md the probe is
// strictly read-only: it lists StorageClasses and reports the default's
// name + provisioner. It never patches anything.
package helmwatch

import (
	"context"
	"fmt"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// defaultStorageClassAnnotation is the canonical Kubernetes annotation that
// marks a StorageClass as the cluster default.
const defaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"

// LocalPathProvisioner is the k3s-shipped ephemeral provisioner. A default
// StorageClass backed by this provisioner is the #3971 failure condition.
const LocalPathProvisioner = "rancher.io/local-path"

// DefaultStorageClassInfo is the result of the default-class probe: the name
// + provisioner of the cluster's default StorageClass, and whether exactly
// one default was found. Ephemeral is true when the default's provisioner is
// the k3s local-path provisioner (the #3971 non-durable condition).
type DefaultStorageClassInfo struct {
	// Name of the default StorageClass ("" when none is marked default).
	Name string
	// Provisioner of the default StorageClass (its .provisioner field).
	Provisioner string
	// Found is true when exactly one StorageClass is marked default.
	Found bool
	// MultipleDefaults is true when >1 StorageClass carries the default
	// annotation — an error state Kubernetes resolves non-deterministically.
	MultipleDefaults bool
	// Ephemeral is true when the default's provisioner is local-path (the
	// non-durable, node-bound k3s provisioner) — the #3971 failure.
	Ephemeral bool
}

// storageClassLister is the minimal surface DefaultStorageClassInfoFromList
// needs — a typed kubernetes.Interface satisfies it via
// StorageV1().StorageClasses().List. Tests inject a fake.NewSimpleClientset.
type storageClassLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*storagev1.StorageClassList, error)
}

// DefaultStorageClassInfoFromList computes the default-class info from a live
// StorageClass list. Pulled out of the kubeconfig path so unit tests can
// drive it with a fake clientset without a real cluster.
func DefaultStorageClassInfoFromList(ctx context.Context, lister storageClassLister) (DefaultStorageClassInfo, error) {
	list, err := lister.List(ctx, metav1.ListOptions{})
	if err != nil {
		return DefaultStorageClassInfo{}, fmt.Errorf("list storageclasses: %w", err)
	}

	info := DefaultStorageClassInfo{}
	defaults := 0
	for i := range list.Items {
		sc := &list.Items[i]
		if sc.Annotations[defaultStorageClassAnnotation] != "true" {
			continue
		}
		defaults++
		// First default wins for the reported Name/Provisioner; the
		// MultipleDefaults flag captures the >1 error state separately.
		if !info.Found {
			info.Name = sc.Name
			info.Provisioner = sc.Provisioner
			info.Found = true
			info.Ephemeral = sc.Provisioner == LocalPathProvisioner
		}
	}
	info.MultipleDefaults = defaults > 1
	return info, nil
}

// DefaultStorageClassFromKubeconfig builds a typed client from the new
// Sovereign's kubeconfig and reports its default StorageClass. The same
// rest.Config.Timeout treatment as the rest of the helmwatch path (issue
// #923) so a hung apiserver fails fast rather than blocking the gate.
//
// Read-only (docs/PRINCIPLES.md #3) — lists StorageClasses, patches nothing.
func DefaultStorageClassFromKubeconfig(ctx context.Context, kubeconfigYAML string) (DefaultStorageClassInfo, error) {
	clientset, err := NewKubernetesClientFromKubeconfig(kubeconfigYAML)
	if err != nil {
		return DefaultStorageClassInfo{}, err
	}
	return DefaultStorageClassInfoFromList(ctx, clientset.StorageV1().StorageClasses())
}
