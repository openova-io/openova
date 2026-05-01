// purge_test.go — regression sentinel for issue #392.
//
// The bug: purge.go filtered Hetzner resources by `catalyst-deployment-id=<id>`
// while OpenTofu (`infra/hetzner/main.tf`) emits `catalyst.openova.io/sovereign=<fqdn>`.
// The mismatch made `wipe.go`'s force-purge silently no-op for every failed
// deployment since the bug landed.
//
// These tests pin both halves of the contract: the label key constant and
// the wire-format produced by FilterByLabel. If either side drifts (Tofu
// stops emitting the canonical label, or purge.go stops looking for it),
// the test fails. The OpenTofu side is asserted by reading the canonical
// module file directly so a single PR that desyncs the two sides is
// caught here, not in production.
package hetzner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPurgeLabelKey_MatchesTofuEmit(t *testing.T) {
	// The label key the package filters by.
	if PurgeLabelKey != "catalyst.openova.io/sovereign" {
		t.Fatalf("PurgeLabelKey drifted from contract: got %q, want %q",
			PurgeLabelKey, "catalyst.openova.io/sovereign")
	}

	// And: the OpenTofu module at infra/hetzner/main.tf MUST emit the same
	// key on every taggable resource. Walk up to the repo root, then read
	// the module file and assert the constant appears.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("repo root not found (test running outside repo? %v)", err)
	}
	tfPath := filepath.Join(repoRoot, "infra", "hetzner", "main.tf")
	bytes, err := os.ReadFile(tfPath)
	if err != nil {
		t.Skipf("Tofu module not readable at %s: %v", tfPath, err)
	}
	if !strings.Contains(string(bytes), `"`+PurgeLabelKey+`"`) {
		t.Fatalf("Tofu module %s does not emit label %q — purge.go and Tofu have drifted",
			tfPath, PurgeLabelKey)
	}
}

func TestFilterByLabel_WireFormat(t *testing.T) {
	got := FilterByLabel("catalyst.openova.io/sovereign", "omantel.omani.works")
	want := "catalyst.openova.io/sovereign=omantel.omani.works"
	if got != want {
		t.Fatalf("FilterByLabel wire format drift: got %q, want %q", got, want)
	}
}

func TestPurge_RejectsEmptyToken(t *testing.T) {
	_, err := Purge(context.Background(), "", "omantel.omani.works", nil)
	if err == nil {
		t.Fatal("expected error on empty token, got nil")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("expected token error, got %v", err)
	}
}

func TestPurge_RejectsEmptySovereignFQDN(t *testing.T) {
	_, err := Purge(context.Background(), "fake-token", "", nil)
	if err == nil {
		t.Fatal("expected error on empty sovereign fqdn, got nil")
	}
	// The error message names the new parameter so callers don't get
	// misled by stale "deployment id" wording.
	if !strings.Contains(err.Error(), "sovereign") {
		t.Fatalf("expected sovereign-fqdn error, got %v", err)
	}
}

// findRepoRoot walks upward from the current package directory looking for
// a directory containing both `infra/` and `products/`. Used so the
// regression test runs from anywhere `go test ./...` is invoked.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "infra", "hetzner", "main.tf")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "products", "catalyst")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
