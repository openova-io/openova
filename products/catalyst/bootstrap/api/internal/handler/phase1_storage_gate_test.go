// phase1_storage_gate_test.go — #3971 wiring coverage. Proves the
// storage-durability gate in markPhase1Done downgrades an otherwise-ready
// deployment to `failed` (default-storageclass-ephemeral) when the new
// Sovereign's default StorageClass is still k3s local-path, leaves a
// durable-default prov untouched, and SKIPS (never downgrades) when the
// probe cannot run.

package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func newStorageGateDeployment(id string) *Deployment {
	return &Deployment{
		ID:        id,
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-storage.example.com",
			OrgEmail:      "operator@storage.example.com",
			BcpTopology:   provisioner.BcpTopologySingleRegion,
			Regions: []provisioner.RegionSpec{
				{Provider: "hetzner", CloudRegion: "fsn1"},
			},
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-storage.example.com",
			// Non-empty so the gate actually runs (it SKIPS on empty path).
			KubeconfigPath: "/var/lib/catalyst/kubeconfigs/" + id + ".yaml",
		},
		OwnerEmail: "operator@storage.example.com",
	}
}

// Default StorageClass is the ephemeral local-path → ready must flip to
// failed with the canonical default-storageclass-ephemeral reason, and NO
// handover token is minted.
func TestMarkPhase1Done_EphemeralDefaultStorageClass_NotReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))
	h.phase1StorageGate = func(ctx context.Context, path string) (helmwatch.DefaultStorageClassInfo, error) {
		return helmwatch.DefaultStorageClassInfo{
			Name:        "local-path",
			Provisioner: helmwatch.LocalPathProvisioner,
			Found:       true,
			Ephemeral:   true,
		}, nil
	}

	dep := newStorageGateDeployment("phase1-storage-ephemeral")
	h.deployments.Store(dep.ID, dep)

	finalStates := map[string]string{
		"cilium":            helmwatch.StateInstalled,
		"catalyst-platform": helmwatch.StateInstalled,
		"hcloud-csi":        helmwatch.StateInstalled,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "failed" {
		t.Fatalf("Status = %q, want failed (ephemeral local-path default)", dep.Status)
	}
	if !strings.Contains(strings.ToLower(dep.Error), "local-path") {
		t.Errorf("Error must name local-path; got %q", dep.Error)
	}
	if dep.Result.Phase1Outcome != provisioner.ReasonDefaultStorageClassEphemeral {
		t.Errorf("Phase1Outcome = %q, want %q", dep.Result.Phase1Outcome, provisioner.ReasonDefaultStorageClassEphemeral)
	}
	if dep.Result.HandoverFiredAt != nil {
		t.Errorf("HandoverFiredAt unexpectedly set on ephemeral-storage failure")
	}
}

// Durable cloud CSI default → the gate leaves the ready deployment alone.
func TestMarkPhase1Done_DurableDefaultStorageClass_Ready(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))
	h.phase1StorageGate = func(ctx context.Context, path string) (helmwatch.DefaultStorageClassInfo, error) {
		return helmwatch.DefaultStorageClassInfo{
			Name:        "hcloud-volumes",
			Provisioner: "csi.hetzner.cloud",
			Found:       true,
			Ephemeral:   false,
		}, nil
	}

	dep := newStorageGateDeployment("phase1-storage-durable")
	h.deployments.Store(dep.ID, dep)

	finalStates := map[string]string{
		"cilium":     helmwatch.StateInstalled,
		"hcloud-csi": helmwatch.StateInstalled,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want ready (durable cloud CSI default)", dep.Status)
	}
	if dep.Result.Phase1Outcome == provisioner.ReasonDefaultStorageClassEphemeral {
		t.Errorf("durable-default prov must not be stamped %q", provisioner.ReasonDefaultStorageClassEphemeral)
	}
}

// A probe error (e.g. unreadable kubeconfig, apiserver blip) must SKIP the
// gate — a transient failure can never downgrade a ready Sovereign.
func TestMarkPhase1Done_StorageGateProbeError_SkipsNotDowngrade(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))
	h.phase1StorageGate = func(ctx context.Context, path string) (helmwatch.DefaultStorageClassInfo, error) {
		return helmwatch.DefaultStorageClassInfo{}, context.DeadlineExceeded
	}

	dep := newStorageGateDeployment("phase1-storage-probe-error")
	h.deployments.Store(dep.ID, dep)

	finalStates := map[string]string{"cilium": helmwatch.StateInstalled}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want ready (probe error must SKIP, not downgrade)", dep.Status)
	}
	if dep.Result.Phase1Outcome == provisioner.ReasonDefaultStorageClassEphemeral {
		t.Errorf("probe error must not stamp the ephemeral-storage reason")
	}
}

// No default StorageClass at all → also a hard failure (every default-less
// PVC hangs Pending), distinct from the local-path case.
func TestMarkPhase1Done_NoDefaultStorageClass_NotReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))
	h.phase1StorageGate = func(ctx context.Context, path string) (helmwatch.DefaultStorageClassInfo, error) {
		return helmwatch.DefaultStorageClassInfo{Found: false}, nil
	}

	dep := newStorageGateDeployment("phase1-storage-no-default")
	h.deployments.Store(dep.ID, dep)

	finalStates := map[string]string{"cilium": helmwatch.StateInstalled}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "failed" {
		t.Fatalf("Status = %q, want failed (no default StorageClass)", dep.Status)
	}
	if dep.Result.Phase1Outcome != provisioner.ReasonDefaultStorageClassEphemeral {
		t.Errorf("Phase1Outcome = %q, want %q", dep.Result.Phase1Outcome, provisioner.ReasonDefaultStorageClassEphemeral)
	}
}
