package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// notSupportedProviderName is a test-only provider whose ListServers
// cannot list: it returns providers.ErrNotSupported, which a caller must
// treat as "unknown", never as "empty".
const notSupportedProviderName = "stub-not-supported"

var notSupportedWipes int

type notSupportedProvider struct{}

func (notSupportedProvider) Name() string { return notSupportedProviderName }

func (notSupportedProvider) Normalize(*providers.ProvisionSpec, *cloudprofile.Profile) error {
	return nil
}

func (notSupportedProvider) TfVars(providers.ProvisionSpec, *cloudprofile.Profile) (map[string]any, error) {
	return map[string]any{}, nil
}

func (notSupportedProvider) Wipe(context.Context, providers.WipeSpec, func(string)) (*providers.WipeResult, error) {
	notSupportedWipes++
	return &providers.WipeResult{}, nil
}

func (notSupportedProvider) ListServers(context.Context, string, string, providers.ProviderCreds) ([]providers.ServerInfo, error) {
	return nil, providers.ErrNotSupported
}

func (notSupportedProvider) ValidateCreds(context.Context, providers.ProviderCreds) error { return nil }

func init() { providers.RegisterProvider(notSupportedProviderName, notSupportedProvider{}) }

// TestJanitorDoesNotReapOnNotSupported — a failed record whose provider
// cannot list its servers must keep its record, tofu state and kubeconfigs,
// and must not be destroyed, even with the janitor armed destructive. The
// production probe and the production destroy run; only the provider is a stub.
func TestJanitorDoesNotReapOnNotSupported(t *testing.T) {
	t.Setenv("CATALYST_JANITOR_DESTRUCTIVE", "true")
	f := makeFailedReapFixture(t, "ee0102notsupp001", "failed")
	v, _ := f.h.deployments.Load(f.id)
	dep := v.(*Deployment)
	dep.Request.Regions = []provisioner.RegionSpec{{Provider: notSupportedProviderName}}

	alive, err := f.h.failedDeploymentInfraAlive(context.Background(), dep, f.work)
	if !alive || !errors.Is(err, providers.ErrNotSupported) {
		t.Fatalf("probe = (%v, %v); want alive=true and an error that wraps providers.ErrNotSupported", alive, err)
	}

	notSupportedWipes = 0
	f.runPass()
	f.runPass()

	if notSupportedWipes != 0 {
		t.Fatalf("a probe that cannot list must never authorise a destroy; Wipe ran %d time(s)", notSupportedWipes)
	}
	if !f.recordPresent() || !f.tofuStatePresent(t) || !f.kubeconfigsPresent(t) {
		t.Fatal("ErrNotSupported was read as empty: the failed record was reaped")
	}
}

// TestJanitorProbeKeepsRecordWhenHetznerCannotList — the registered Hetzner
// adapter cannot list its servers yet. The production probe must report the
// infrastructure as alive with an error that wraps providers.ErrNotSupported,
// and a janitor pass must keep the failed record. Before CLD-109 the adapter
// answered nil, nil and the record was reaped as if the cloud were empty.
func TestJanitorProbeKeepsRecordWhenHetznerCannotList(t *testing.T) {
	f := makeFailedReapFixture(t, "ee0109hcannot001", "failed")
	v, _ := f.h.deployments.Load(f.id)
	dep := v.(*Deployment)
	dep.Request.Regions = []provisioner.RegionSpec{{Provider: "hetzner"}}

	alive, err := f.h.failedDeploymentInfraAlive(context.Background(), dep, f.work)
	if !alive || !errors.Is(err, providers.ErrNotSupported) {
		t.Fatalf("probe = (%v, %v); want alive=true and an error that wraps providers.ErrNotSupported", alive, err)
	}
	f.runPass()
	if !f.recordPresent() {
		t.Fatal("the Hetzner record was reaped although its adapter cannot list servers")
	}
}
