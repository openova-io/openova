package handler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// CLD-111 — the janitor reaches the cloud through capabilities: every
// provider's providers.OrphanSweeper, and providers.LivenessProber for the
// failed-record probe. No adapter type is asserted and no provider named.

const stubJanitorName = "stub-cld111-janitor"

var (
	stubJanitorMu    sync.Mutex
	stubJanitorSweep []providers.SweepInput
	stubJanitorAlive bool
)

type stubJanitorProvider struct{}

func (stubJanitorProvider) Name() string { return stubJanitorName }
func (stubJanitorProvider) Normalize(*providers.ProvisionSpec, *cloudprofile.Profile) error {
	return nil
}
func (stubJanitorProvider) TfVars(providers.ProvisionSpec, *cloudprofile.Profile) (map[string]any, error) {
	return map[string]any{}, nil
}
func (stubJanitorProvider) Wipe(context.Context, providers.WipeSpec, func(string)) (*providers.WipeResult, error) {
	return &providers.WipeResult{}, nil
}
func (stubJanitorProvider) ListServers(context.Context, string, string, providers.ProviderCreds) ([]providers.ServerInfo, error) {
	return nil, nil
}
func (stubJanitorProvider) ValidateCreds(context.Context, providers.ProviderCreds) error { return nil }
func (stubJanitorProvider) SweepKinds() []string                                          { return []string{"Widgets"} }
func (stubJanitorProvider) SweepOrphans(_ context.Context, in providers.SweepInput) (providers.SweepResult, error) {
	stubJanitorMu.Lock()
	defer stubJanitorMu.Unlock()
	stubJanitorSweep = append(stubJanitorSweep, in)
	return providers.SweepResult{Deleted: map[string]int{"Widgets": 3}}, nil
}
func (stubJanitorProvider) HasLiveInfrastructure(context.Context, string, string, providers.ProviderCreds) (bool, error) {
	stubJanitorMu.Lock()
	defer stubJanitorMu.Unlock()
	return stubJanitorAlive, nil
}

func init() { providers.RegisterProvider(stubJanitorName, stubJanitorProvider{}) }

func lastStubSweep(t *testing.T) providers.SweepInput {
	t.Helper()
	stubJanitorMu.Lock()
	defer stubJanitorMu.Unlock()
	if len(stubJanitorSweep) == 0 {
		t.Fatal("the provider's OrphanSweeper was not called")
	}
	return stubJanitorSweep[len(stubJanitorSweep)-1]
}

// TestJanitorPass_SweepsThroughTheCapability — a pass calls the sweeper of
// a provider that has one, with every non-wiped record protected and the
// destructive gate of the environment, and names each count for its mode
// (#5545): WouldReap when log-only, Deleted when destructive.
func TestJanitorPass_SweepsThroughTheCapability(t *testing.T) {
	for _, destructive := range []bool{false, true} {
		if destructive {
			t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "true")
		}
		f := makeFailedReapFixture(t, "live0002cafe0000", "ready")
		f.h.deployments.Store("gone0002cafe0000", &Deployment{ID: "gone0002cafe0000", Status: "wiped"})
		f.h.deployments.Store("stub0002cafe0000", &Deployment{ID: "stub0002cafe0000", Status: "ready",
			Request: provisioner.Request{Provider: stubJanitorName, Credentials: map[string]string{"token": "t"}}})
		var buf bytes.Buffer
		f.h.log = slog.New(slog.NewTextHandler(&buf, nil))
		f.runPass()

		in := lastStubSweep(t)
		if in.Destructive != destructive {
			t.Errorf("Destructive = %v, want %v", in.Destructive, destructive)
		}
		if _, ok := in.ProtectedIDs["live0002"]; !ok {
			t.Errorf("ProtectedIDs = %v, want the ready record live0002", in.ProtectedIDs)
		}
		if _, ok := in.ProtectedIDs["gone0002"]; ok {
			t.Error("a wiped record was protected")
		}
		if in.Creds.Get("token") != "t" {
			t.Errorf("creds = %v, want the record's", in.Creds.Raw)
		}
		key := "orphanWidgetsWouldReap=3"
		if destructive {
			key = "orphanWidgetsDeleted=3"
		}
		if !strings.Contains(buf.String(), key) {
			t.Errorf("pass log lacks %s:\n%s", key, buf.String())
		}
	}
}

// TestFailedInfraProbe_AsksTheLivenessProber — when ListServers is empty
// the adapter's LivenessProber decides; a record that names no provider is
// alive with an error (fail-closed), never defaulted to a provider.
func TestFailedInfraProbe_AsksTheLivenessProber(t *testing.T) {
	f := makeFailedReapFixture(t, "stub0003cafe0000", "failed")
	v, _ := f.h.deployments.Load(f.id)
	dep := v.(*Deployment)
	dep.Request.Regions = []provisioner.RegionSpec{{Provider: stubJanitorName}}
	for _, want := range []bool{true, false} {
		stubJanitorMu.Lock()
		stubJanitorAlive = want
		stubJanitorMu.Unlock()
		alive, err := f.h.failedDeploymentInfraAlive(context.Background(), dep, f.work)
		if err != nil || alive != want {
			t.Fatalf("prober says %v: probe = (%v, %v)", want, alive, err)
		}
	}
	dep.Request.Regions = nil
	if alive, err := f.h.failedDeploymentInfraAlive(context.Background(), dep, f.work); !alive || err == nil || !strings.Contains(err.Error(), "provider unknown") {
		t.Fatalf("no provider: probe = (%v, %v), want alive and provider unknown", alive, err)
	}
	f.runPass()
	if !f.recordPresent() {
		t.Fatal("a record that names no provider was reaped")
	}
}
