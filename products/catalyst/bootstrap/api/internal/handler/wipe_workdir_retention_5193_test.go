// wipe_workdir_retention_5193_test.go — #5193 (item 3) regression coverage.
//
// A PARTIAL destroy (the hw268 shape: region-a tore down but region-b /
// EIPs / VPCs / OBS survived) leaves the wipe NOT verified complete. The
// per-deployment tofu workdir (tofu.auto.tfvars.json + terraform.tfstate)
// is the exact state a retried wipe re-`tofu destroy`s against — so the
// purge epilogue must RETAIN it whenever the wipe did not converge, and
// only reclaim it once BOTH convergence signals agree (TofuDestroyed AND
// VerifiedZeroOrphans). Pre-fix the epilogue removed the workdir
// UNCONDITIONALLY, clobbering the tfstate the retry needs and stranding
// the env un-wipeable via the clean tofu path.
//
// These tests drive the real async purge (runWipePurge via WipeDeployment)
// through a stub CloudProvider whose WipeResult TofuDestroyed /
// VerifiedZeroOrphans flags are controllable, seed a real workdir on disk,
// and assert its survival vs removal. Reverting the gate (back to an
// unconditional os.RemoveAll) fails the partial-destroy case: the seeded
// workdir would be gone.
package handler

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// ── controllable stub CloudProvider ──────────────────────────────────

// stub5193ProviderName is a test-only provider registered once (init
// below) so runWipePurge's providers.Get() resolves to a WipeResult we
// fully control — without hitting a real cloud API.
const stub5193ProviderName = "stub5193"

var (
	stub5193Mu     sync.Mutex
	stub5193Result *providers.WipeResult
)

func setStub5193Result(r *providers.WipeResult) {
	stub5193Mu.Lock()
	stub5193Result = r
	stub5193Mu.Unlock()
}

type stub5193Provider struct{}

func (stub5193Provider) Name() string { return stub5193ProviderName }

func (stub5193Provider) Provision(context.Context, providers.ProvisionSpec, chan<- providers.ProvisionEvent) (*providers.ProvisionResult, error) {
	return &providers.ProvisionResult{}, nil
}

func (stub5193Provider) Wipe(_ context.Context, _ providers.WipeSpec, progress func(msg string)) (*providers.WipeResult, error) {
	if progress != nil {
		progress("stub5193 wipe invoked")
	}
	stub5193Mu.Lock()
	r := stub5193Result
	stub5193Mu.Unlock()
	if r == nil {
		r = &providers.WipeResult{ProviderPurge: map[string][]string{}}
	}
	return r, nil
}

func (stub5193Provider) ListServers(context.Context, string, string, providers.ProviderCreds) ([]providers.ServerInfo, error) {
	return nil, nil
}

func (stub5193Provider) ValidateCreds(context.Context, providers.ProviderCreds) error { return nil }

func init() { providers.RegisterProvider(stub5193ProviderName, stub5193Provider{}) }

// runStub5193Wipe seeds a real per-deployment workdir on disk, drives the
// async wipe through the stub provider with the supplied WipeResult, and
// returns the workdir path so the caller can assert survival/removal.
func runStub5193Wipe(t *testing.T, id string, res *providers.WipeResult) string {
	t.Helper()
	workRoot := t.TempDir()
	t.Setenv("CATALYST_TOFU_WORKDIR", workRoot)

	// Seed the per-deployment workdir with the two files a retry needs.
	depWorkDir := filepath.Join(workRoot, id)
	if err := os.MkdirAll(depWorkDir, 0o755); err != nil {
		t.Fatalf("seed workdir: %v", err)
	}
	for _, f := range []string{"terraform.tfstate", "tofu.auto.tfvars.json"} {
		if err := os.WriteFile(filepath.Join(depWorkDir, f), []byte("{}"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}

	setStub5193Result(res)
	t.Cleanup(func() { setStub5193Result(nil) })

	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := &Deployment{
		ID:        id,
		Status:    "failed",
		StartedAt: time.Now().Add(-45 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN: id + ".omani.works",
			// NOT pool → the PDM release step no-ops, keeping this test
			// focused purely on the workdir-retention gate.
			SovereignDomainMode: "byo",
			Regions:             []provisioner.RegionSpec{{Provider: stub5193ProviderName, CloudRegion: "test"}},
		},
	}
	h.deployments.Store(dep.ID, dep)

	// Hetzner-shaped creds guard (provHint defaults to hetzner because
	// dep.Request.Provider is empty) — the stub provider ignores them.
	w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-token"}`)
	if w.Code != 202 {
		t.Fatalf("wipe status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}
	waitForWipeDone(t, dep, 60*time.Second)
	return depWorkDir
}

// TestWipePurge_PartialDestroy_RetainsWorkdir is the headline #5193 item-3
// assertion: when the wipe is NOT verified complete, the per-deployment
// tofu workdir (tfvars + tfstate) MUST survive so a retried wipe still has
// the terraform state to destroy against. Each sub-case is a distinct
// "did not converge" shape.
func TestWipePurge_PartialDestroy_RetainsWorkdir(t *testing.T) {
	cases := []struct {
		name   string
		result *providers.WipeResult
	}{
		{
			// tofu destroy failed (region-a died mid-teardown) — the
			// classic hw268 partial destroy.
			name:   "tofu destroy failed",
			result: &providers.WipeResult{TofuDestroyed: false, VerifiedZeroOrphans: false, ProviderPurge: map[string][]string{}},
		},
		{
			// tofu destroy returned clean but the post-wipe verify found a
			// survivor (e.g. a stuck SG / leaked EIP) — still not converged.
			name:   "destroyed but orphans survived",
			result: &providers.WipeResult{TofuDestroyed: true, VerifiedZeroOrphans: false, ProviderPurge: map[string][]string{}, ResidualOrphans: map[string][]string{"firewalls": {"catalyst-x-sg"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			depWorkDir := runStub5193Wipe(t, "dep-5193-retain", tc.result)
			if _, err := os.Stat(depWorkDir); err != nil {
				t.Fatalf("workdir %s was removed after a NON-verified wipe (%s) — the tfstate a retried wipe needs is gone; #5193 item-3 regression: %v", depWorkDir, tc.name, err)
			}
			// The state files specifically must still be there.
			for _, f := range []string{"terraform.tfstate", "tofu.auto.tfvars.json"} {
				if _, err := os.Stat(filepath.Join(depWorkDir, f)); err != nil {
					t.Errorf("%s did not survive a partial wipe: %v", f, err)
				}
			}
		})
	}
}

// TestWipePurge_VerifiedComplete_RemovesWorkdir is the paired positive
// case: a fully-converged wipe (tofu destroy clean AND zero orphans
// verified) reclaims the workdir exactly as before — the gate must not
// leak state on the happy path.
func TestWipePurge_VerifiedComplete_RemovesWorkdir(t *testing.T) {
	depWorkDir := runStub5193Wipe(t, "dep-5193-clean", &providers.WipeResult{
		TofuDestroyed:       true,
		VerifiedZeroOrphans: true,
		ProviderPurge:       map[string][]string{},
	})
	if _, err := os.Stat(depWorkDir); !os.IsNotExist(err) {
		t.Fatalf("workdir %s survived a VERIFIED-complete wipe (err=%v) — the epilogue must reclaim it on convergence, else every clean wipe leaks a workdir on the PVC", depWorkDir, err)
	}
}
