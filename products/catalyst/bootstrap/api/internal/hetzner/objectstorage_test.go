// objectstorage_test.go — unit tests for the Hetzner Object Storage
// credential validator (issue #371).
//
// We don't reach the upstream Hetzner endpoints from a unit test; the
// only behaviour we need to lock in here is:
//   1. ObjectStorageEndpoint composes the canonical hostname for valid
//      regions and returns "" for unrecognised ones.
//   2. ValidateObjectStorageCredentials early-rejects empty/blank inputs
//      and unknown regions BEFORE attempting any network I/O — so the
//      wizard's error card surfaces the actionable message rather than
//      a generic upstream timeout.
//
// Live S3 ListBuckets coverage is exercised end-to-end during a real
// `tofu apply` against a freshly-issued Hetzner Object Storage
// credential pair — that's the integration boundary, not the unit one.
package hetzner

import (
	"context"
	"strings"
	"testing"
)

func TestObjectStorageEndpoint_KnownRegions(t *testing.T) {
	cases := map[string]string{
		"fsn1": "fsn1.your-objectstorage.com",
		"nbg1": "nbg1.your-objectstorage.com",
		"hel1": "hel1.your-objectstorage.com",
	}
	for region, want := range cases {
		got := ObjectStorageEndpoint(region)
		if got != want {
			t.Errorf("ObjectStorageEndpoint(%q) = %q, want %q", region, got, want)
		}
	}
}

func TestObjectStorageEndpoint_UnknownRegion(t *testing.T) {
	for _, region := range []string{"", "us-east-1", "ash", "hil", "FSN1", "fsn"} {
		if got := ObjectStorageEndpoint(region); got != "" {
			t.Errorf("ObjectStorageEndpoint(%q) = %q, want empty", region, got)
		}
	}
}

func TestValidateObjectStorageCredentials_RejectsEmptyAccess(t *testing.T) {
	ok, err := ValidateObjectStorageCredentials(context.Background(), "fsn1", "", "secret")
	if ok {
		t.Errorf("expected ok=false for empty access key")
	}
	if err == nil || !strings.Contains(err.Error(), "access key") {
		t.Errorf("expected access-key error, got %v", err)
	}
}

func TestValidateObjectStorageCredentials_RejectsEmptySecret(t *testing.T) {
	ok, err := ValidateObjectStorageCredentials(context.Background(), "fsn1", "access", "")
	if ok {
		t.Errorf("expected ok=false for empty secret key")
	}
	if err == nil || !strings.Contains(err.Error(), "secret key") {
		t.Errorf("expected secret-key error, got %v", err)
	}
}

func TestValidateObjectStorageCredentials_RejectsUnknownRegion(t *testing.T) {
	ok, err := ValidateObjectStorageCredentials(context.Background(), "us-east-1", "access", "secret-long-enough-to-pass-handler-check")
	if ok {
		t.Errorf("expected ok=false for unknown region")
	}
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Errorf("expected region error, got %v", err)
	}
}
