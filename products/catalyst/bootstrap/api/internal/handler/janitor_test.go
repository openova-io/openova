package handler

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestCleanOrphanTofuWorkdirs_PreservesSharedAndActive — #3151: the janitor
// must NOT reap the `_shared` cloud-agnostic cloud-init staging dir (#3147,
// a sibling of every per-deployment workdir). Reaping it races the next
// prov's tofu plan into a "no file exists at ./../_shared/..." failure
// (observed live: hw123 wipe → `[JANITOR] orphan tofu workdir deleted: _shared`).
// It must still preserve active deployment workdirs and reap genuine orphans.
func TestCleanOrphanTofuWorkdirs_PreservesSharedAndActive(t *testing.T) {
	work := t.TempDir()
	for _, d := range []string{"_shared", "deadbeefcafe1234", "abc123activedep"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	active := map[string]struct{}{"abc123activedep": {}}
	deleted := h.cleanOrphanTofuWorkdirs(work, active)

	mustExist := func(name string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(work, name)); err != nil {
			t.Errorf("%s should be preserved, but: %v", name, err)
		}
	}
	mustGone := func(name string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(work, name)); err == nil {
			t.Errorf("%s should have been reaped", name)
		}
	}
	mustExist("_shared")         // reserved staging dir — NEVER reaped
	mustExist("abc123activedep") // active deployment — preserved
	mustGone("deadbeefcafe1234") // genuine orphan — reaped
	if deleted != 1 {
		t.Errorf("expected 1 reap (the orphan only), got %d", deleted)
	}
}
