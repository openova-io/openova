package providers

import (
	"context"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
)

type fakeAdapter struct{ name string }

func (f fakeAdapter) Name() string                                        { return f.name }
func (fakeAdapter) Normalize(*ProvisionSpec, *cloudprofile.Profile) error { return nil }
func (fakeAdapter) TfVars(ProvisionSpec, *cloudprofile.Profile) (map[string]any, error) {
	return nil, nil
}
func (fakeAdapter) ValidateCreds(context.Context, ProviderCreds) error { return nil }
func (fakeAdapter) ListServers(context.Context, string, string, ProviderCreds) ([]ServerInfo, error) {
	return nil, ErrNotSupported
}
func (fakeAdapter) Wipe(context.Context, WipeSpec, func(string)) (*WipeResult, error) {
	return &WipeResult{}, nil
}

// probingAdapter implements the capability LivenessProber.
type probingAdapter struct{ fakeAdapter }

func (probingAdapter) HasLiveInfrastructure(context.Context, string, string, ProviderCreds) (bool, error) {
	return true, nil
}

// TestAs — As[T] finds a capability an adapter implements, reports ok=false
// for one it does not, and returns an error only for an unknown name.
func TestAs(t *testing.T) {
	reset()
	t.Cleanup(reset)
	RegisterProvider("plain", fakeAdapter{name: "plain"})
	RegisterProvider("probing", probingAdapter{fakeAdapter{name: "probing"}})

	if c, ok, err := As[LivenessProber]("probing"); err != nil || !ok || c == nil {
		t.Fatalf(`As[LivenessProber]("probing") = (%v, %v, %v); want the adapter, true, nil`, c, ok, err)
	}
	if _, ok, err := As[LivenessProber]("plain"); err != nil || ok {
		t.Fatalf(`As[LivenessProber]("plain") = (_, %v, %v); want false, nil`, ok, err)
	}
	if _, ok, err := As[LivenessProber]("missing"); err == nil || ok {
		t.Fatalf(`As[LivenessProber]("missing") = (_, %v, %v); want false and an error`, ok, err)
	}
	if got := (&FieldError{Field: "regions[0].zone", Message: "unknown zone"}).Error(); got != "regions[0].zone: unknown zone" {
		t.Fatalf("FieldError.Error() = %q", got)
	}
}
