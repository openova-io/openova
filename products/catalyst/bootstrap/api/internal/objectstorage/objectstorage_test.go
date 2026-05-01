package objectstorage

import (
	"context"
	"errors"
	"testing"
)

type stubProvider struct {
	endpointFn func(string) string
	validateFn func(context.Context, string, string, string) (bool, error)
}

func (s *stubProvider) Endpoint(r string) string { return s.endpointFn(r) }
func (s *stubProvider) Validate(ctx context.Context, region, accessKey, secretKey string) (bool, error) {
	return s.validateFn(ctx, region, accessKey, secretKey)
}

func TestResolve_RegisteredProvider(t *testing.T) {
	// Reset registry for test isolation; tests run sequentially in this
	// file but parallel packages must not see state-bleed.
	prev := providerRegistry
	providerRegistry = map[string]Provider{}
	defer func() { providerRegistry = prev }()

	p := &stubProvider{
		endpointFn: func(r string) string { return r + ".test" },
		validateFn: func(_ context.Context, _, _, _ string) (bool, error) { return true, nil },
	}
	Register("test", p)
	got, err := Resolve("test")
	if err != nil {
		t.Fatalf("Resolve(test) err=%v", err)
	}
	if got.Endpoint("eu") != "eu.test" {
		t.Errorf("Endpoint roundtrip failed: got %q", got.Endpoint("eu"))
	}
}

func TestResolve_UnknownProvider(t *testing.T) {
	prev := providerRegistry
	providerRegistry = map[string]Provider{}
	defer func() { providerRegistry = prev }()

	_, err := Resolve("does-not-exist")
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Errorf("expected ErrUnsupportedProvider, got %v", err)
	}
}

func TestRegister_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on nil Provider")
		}
	}()
	Register("nil-impl", nil)
}
