// hetzner_test.go — unit tests for the Hetzner Provider impl of the
// objectstorage seam (issue #425, migrated from internal/hetzner/
// objectstorage_test.go).
//
// We don't reach the upstream Hetzner endpoints from a unit test; the
// only behaviour we need to lock in here is:
//   1. Endpoint composes the canonical hostname for valid regions and
//      returns "" for unrecognised ones.
//   2. Validate early-rejects empty/blank inputs and unknown regions
//      BEFORE attempting any network I/O — so the wizard's error card
//      surfaces the actionable message rather than a generic upstream
//      timeout.
//   3. The Provider self-registers under "hetzner" via init() so
//      objectstorage.Resolve("hetzner") returns this impl.
//
// Live S3 ListBuckets coverage is exercised end-to-end during a real
// `tofu apply` against a freshly-issued Hetzner Object Storage
// credential pair — that's the integration boundary, not the unit one.
package hetzner

import (
	"context"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/objectstorage"
)

func TestEndpoint_KnownRegions(t *testing.T) {
	p := Provider{}
	cases := map[string]string{
		"fsn1": "fsn1.your-objectstorage.com",
		"nbg1": "nbg1.your-objectstorage.com",
		"hel1": "hel1.your-objectstorage.com",
	}
	for region, want := range cases {
		got := p.Endpoint(region)
		if got != want {
			t.Errorf("Endpoint(%q) = %q, want %q", region, got, want)
		}
	}
}

func TestEndpoint_UnknownRegion(t *testing.T) {
	p := Provider{}
	for _, region := range []string{"", "us-east-1", "ash", "hil", "FSN1", "fsn"} {
		if got := p.Endpoint(region); got != "" {
			t.Errorf("Endpoint(%q) = %q, want empty", region, got)
		}
	}
}

func TestValidate_RejectsEmptyAccess(t *testing.T) {
	p := Provider{}
	ok, err := p.Validate(context.Background(), "fsn1", "", "secret")
	if ok {
		t.Errorf("expected ok=false for empty access key")
	}
	if err == nil || !strings.Contains(err.Error(), "access key") {
		t.Errorf("expected access-key error, got %v", err)
	}
}

func TestValidate_RejectsEmptySecret(t *testing.T) {
	p := Provider{}
	ok, err := p.Validate(context.Background(), "fsn1", "access", "")
	if ok {
		t.Errorf("expected ok=false for empty secret key")
	}
	if err == nil || !strings.Contains(err.Error(), "secret key") {
		t.Errorf("expected secret-key error, got %v", err)
	}
}

func TestValidate_RejectsUnknownRegion(t *testing.T) {
	p := Provider{}
	ok, err := p.Validate(context.Background(), "us-east-1", "access", "secret-long-enough-to-pass-handler-check")
	if ok {
		t.Errorf("expected ok=false for unknown region")
	}
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Errorf("expected region error, got %v", err)
	}
}

// TestProvider_Registered confirms the init() side-effect — the wizard
// handler resolves the impl by `provider: "hetzner"` from the payload.
func TestProvider_Registered(t *testing.T) {
	got, err := objectstorage.Resolve("hetzner")
	if err != nil {
		t.Fatalf("Resolve(hetzner) err=%v — init() did not register", err)
	}
	if got.Endpoint("fsn1") != "fsn1.your-objectstorage.com" {
		t.Errorf("registered Provider returned wrong endpoint: %q", got.Endpoint("fsn1"))
	}
}
