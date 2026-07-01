package handler

// wipe_drain.go — #4677 drain-before-destroy.
//
// THE ROOT CAUSE this fixes
// ─────────────────────────
// The canonical wipe runs `tofu destroy`, which deletes ONLY the
// Terraform-managed infra (VPC / ECS / EIPs / NAT / the tofu-declared ELB).
// But a running Sovereign creates cloud resources at RUNTIME, OUTSIDE tofu
// state, via the in-cluster controllers:
//
//   - the CSI driver (huawei-evs-csi / hcloud-csi) provisions one cloud VOLUME
//     per PVC (postgres, gitea, harbor, openbao, loki, mimir, tempo, …), named
//     `pvc-<uuid>` — no deployment identity;
//   - the cloud-controller-manager provisions one LOAD BALANCER (+ its EIP) per
//     `Service type=LoadBalancer`.
//
// `tofu destroy` tears down the nodes/VPC before the in-cluster CSI/CCM
// controllers can delete those resources, so EVERY wipe orphans all of them
// (detached volumes, dangling LBs) — billed forever, and invisible to the
// name-prefix janitor because `pvc-<uuid>` carries no dep name to match.
//
// THE FIX
// ───────
// Before `tofu destroy`, while the cluster is still reachable, DRAIN the K8s
// objects that own cloud resources so the in-cluster controllers release them:
//   1. delete every `Service type=LoadBalancer` → CCM releases the LB + EIP;
//   2. for every PVC, force its PV reclaim policy to Delete, then delete the
//      PVC → CSI deletes the backing cloud volume;
//   3. bounded wait for the PVs to disappear (best-effort).
//
// Best-effort by design: on a wipe of an ALREADY-DEAD env (apiserver
// unreachable) the drain no-ops and the wipe proceeds to tofu destroy — the
// post-destroy cloud-GC (swept by detached-status, not name) is the backstop
// for that case.

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// drainPVSettleTimeout bounds the post-delete wait for CSI to remove the PVs
// (cloud volumes). Package var so tests can shorten it; the wipe never stalls
// past it (the cloud-GC backstops any still-settling deletes).
var drainPVSettleTimeout = 90 * time.Second

// drainClusterCloudResources deletes the LoadBalancer Services + PVCs of the
// cluster addressed by kcRaw so the in-cluster CCM/CSI controllers release
// their cloud resources (LBs, EIPs, EVS volumes) BEFORE tofu destroys the
// nodes. Returns nil (best-effort) when the cluster is unreachable — the
// caller proceeds to tofu destroy regardless. progress may be nil.
func drainClusterCloudResources(ctx context.Context, kcRaw []byte, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}
	if len(kcRaw) == 0 {
		return nil // no kubeconfig on the PVC → nothing to drain (already gone)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kcRaw)
	if err != nil {
		progress("drain: skipped — bad kubeconfig: " + err.Error())
		return nil
	}
	// A tight timeout so a dead apiserver never stalls the wipe.
	restCfg.Timeout = 20 * time.Second
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		progress("drain: skipped — client build failed: " + err.Error())
		return nil
	}

	// Reachability probe: one cheap list. If the apiserver is gone (already-dead
	// env), skip the drain entirely and let tofu destroy + the cloud-GC handle it.
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := cs.CoreV1().Namespaces().List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		progress("drain: cluster unreachable — skipping drain, relying on tofu destroy + cloud-GC (" + err.Error() + ")")
		return nil
	}

	drainLoadBalancerServices(ctx, cs, progress)
	drainPVCs(ctx, cs, progress)
	return nil
}

// drainLoadBalancerServices deletes every Service of type LoadBalancer so the
// cloud-controller-manager releases the backing LB + its EIP.
func drainLoadBalancerServices(ctx context.Context, cs kubernetes.Interface, progress func(string)) {
	if progress == nil {
		progress = func(string) {}
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	svcs, err := cs.CoreV1().Services("").List(lctx, metav1.ListOptions{})
	if err != nil {
		progress("drain: list services failed: " + err.Error())
		return
	}
	deleted := 0
	for i := range svcs.Items {
		s := &svcs.Items[i]
		if s.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
		err := cs.CoreV1().Services(s.Namespace).Delete(dctx, s.Name, metav1.DeleteOptions{})
		dcancel()
		if err != nil {
			progress(fmt.Sprintf("drain: delete svc %s/%s failed: %s", s.Namespace, s.Name, err.Error()))
			continue
		}
		deleted++
	}
	progress(fmt.Sprintf("drain: deleted %d LoadBalancer Service(s) → CCM releasing LB(s)+EIP(s)", deleted))
}

// drainPVCs forces each PV's reclaim policy to Delete then deletes the PVC so
// the CSI driver deletes the backing cloud volume (works even when the
// StorageClass default was Retain — the whole env is being destroyed).
func drainPVCs(ctx context.Context, cs kubernetes.Interface, progress func(string)) {
	if progress == nil {
		progress = func(string) {}
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pvcs, err := cs.CoreV1().PersistentVolumeClaims("").List(lctx, metav1.ListOptions{})
	if err != nil {
		progress("drain: list pvcs failed: " + err.Error())
		return
	}
	deleted := 0
	pvNames := make([]string, 0, len(pvcs.Items))
	for i := range pvcs.Items {
		p := &pvcs.Items[i]
		// Force the bound PV to Delete so releasing the PVC deletes the cloud
		// volume regardless of the StorageClass's default reclaim policy.
		if p.Spec.VolumeName != "" {
			patch := []byte(`{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`)
			pctx, pcancel := context.WithTimeout(ctx, 15*time.Second)
			_, perr := cs.CoreV1().PersistentVolumes().Patch(pctx, p.Spec.VolumeName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
			pcancel()
			if perr != nil {
				progress(fmt.Sprintf("drain: patch pv %s reclaim=Delete failed: %s", p.Spec.VolumeName, perr.Error()))
			} else {
				pvNames = append(pvNames, p.Spec.VolumeName)
			}
		}
		dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
		err := cs.CoreV1().PersistentVolumeClaims(p.Namespace).Delete(dctx, p.Name, metav1.DeleteOptions{})
		dcancel()
		if err != nil {
			progress(fmt.Sprintf("drain: delete pvc %s/%s failed: %s", p.Namespace, p.Name, err.Error()))
			continue
		}
		deleted++
	}
	progress(fmt.Sprintf("drain: deleted %d PVC(s) (reclaim→Delete on %d PV) → CSI deleting cloud volume(s)", deleted, len(pvNames)))

	// Bounded wait for the PVs to disappear (CSI delete completed). Best-effort:
	// a slow CSI never stalls the wipe past the deadline; the cloud-GC backstops.
	deadline := time.Now().Add(drainPVSettleTimeout)
	for time.Now().Before(deadline) {
		remaining := 0
		wctx, wcancel := context.WithTimeout(ctx, 15*time.Second)
		for _, n := range pvNames {
			if _, err := cs.CoreV1().PersistentVolumes().Get(wctx, n, metav1.GetOptions{}); err == nil {
				remaining++
			}
		}
		wcancel()
		if remaining == 0 {
			progress("drain: all CSI volumes released (PVs gone)")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Second):
		}
	}
	progress("drain: proceeding to tofu destroy (some CSI deletes still settling — cloud-GC will backstop)")
}
