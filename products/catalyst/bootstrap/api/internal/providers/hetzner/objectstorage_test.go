package hetzner

import (
	"context"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// The Object Storage check of the adapter is reached through the provider
// registry (CLD-112); the live ListBuckets call is not made here.

func TestObjectStorageEndpoint_KnownRegions(t *testing.T) {
	for region, want := range map[string]string{
		"fsn1": "fsn1.your-objectstorage.com",
		"nbg1": "nbg1.your-objectstorage.com",
		"hel1": "hel1.your-objectstorage.com",
	} {
		if got := New().ObjectStorageEndpoint(region); got != want {
			t.Errorf("ObjectStorageEndpoint(%q) = %q, want %q", region, got, want)
		}
	}
}

func TestObjectStorageEndpoint_UnknownRegion(t *testing.T) {
	for _, region := range []string{"", "us-east-1", "ash", "hil", "FSN1", "fsn"} {
		if got := New().ObjectStorageEndpoint(region); got != "" {
			t.Errorf("ObjectStorageEndpoint(%q) = %q, want empty", region, got)
		}
	}
}

func TestValidateObjectStorage_RefusesBeforeTheNetwork(t *testing.T) {
	for name, tc := range map[string]struct{ region, access, secret, want string }{
		"empty access key": {"fsn1", "", "secret", "access key"},
		"empty secret key": {"fsn1", "access", "", "secret key"},
		"unknown region":   {"us-east-1", "access", "secret-long-enough-to-pass-handler-check", "region"},
	} {
		ok, err := New().ValidateObjectStorage(context.Background(), tc.region, tc.access, tc.secret)
		if ok || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got (%v, %v), want false and an error naming %q", name, ok, err, tc.want)
		}
	}
}

// The registered adapter has the capability, so the credentials handler
// finds it through providers.As.
func TestObjectStorageValidator_Registered(t *testing.T) {
	v, ok, err := providers.As[providers.ObjectStorageValidator](Name)
	if err != nil || !ok {
		t.Fatalf("As[ObjectStorageValidator](%q) = %v, %v", Name, ok, err)
	}
	if got := v.ObjectStorageEndpoint("fsn1"); got != "fsn1.your-objectstorage.com" {
		t.Errorf("registered adapter returned endpoint %q", got)
	}
}
