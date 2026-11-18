package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// TestLegacyRecordLoads proves that a deployment record in the form the store
// has written so far still loads (work package CLD-101). Its provider stands
// at the top and in every region, and it carries "hetznerToken": "<redacted>".
// Work packages that move the credentials of a request into other fields keep
// this test green: a record already on a volume is read before it is ever
// written again.
func TestLegacyRecordLoads(t *testing.T) {
	const id = "4c3a9e1f0b2d7a65" // the "id" inside the record
	raw, err := os.ReadFile(filepath.Join("..", "provisioner", "testdata", "legacy-record.json"))
	if err != nil {
		t.Fatalf("read the legacy record: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o600); err != nil {
		t.Fatalf("place the legacy record: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var loadErrors []string
	recs, err := s.LoadAll(func(path string, err error) { loadErrors = append(loadErrors, path+": "+err.Error()) })
	if err != nil || len(loadErrors) > 0 || len(recs) != 1 {
		t.Fatalf("LoadAll: %d records, error %v, refused %v; want the one legacy record", len(recs), err, loadErrors)
	}
	single, err := s.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, rec := range map[string]Record{"LoadAll": recs[0], "Load": single} {
		if rec.ID != id || rec.Status != "ready" {
			t.Errorf("%s: id %q status %q, want %q %q", name, rec.ID, rec.Status, id, "ready")
		}
		req := rec.Request.ToProvisionerRequest()
		for where, got := range map[string]string{"stored": rec.Request.Provider, "request": req.Provider} {
			if got != "hetzner" {
				t.Errorf("%s: provider of the %s form = %q, want %q", name, where, got, "hetzner")
			}
		}
		checkLegacyRegions(t, name+", stored form", rec.Request.Regions)
		checkLegacyRegions(t, name+", request", req.Regions)
	}
}

// checkLegacyRegions asserts the two regions of the legacy record, in order.
func checkLegacyRegions(t *testing.T, where string, got []provisioner.RegionSpec) {
	t.Helper()
	want := []provisioner.RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1", ControlPlaneSize: "cpx31", WorkerSize: "cpx41", WorkerCount: 3},
		{Provider: "hetzner", CloudRegion: "hel1", ControlPlaneSize: "cpx31", WorkerSize: "cpx41", WorkerCount: 2},
	}
	if len(got) != len(want) {
		t.Errorf("%s: %d regions, want %d", where, len(got), len(want))
		return
	}
	for i, w := range want {
		g := got[i]
		if g.Provider != w.Provider || g.CloudRegion != w.CloudRegion || g.ControlPlaneSize != w.ControlPlaneSize ||
			g.WorkerSize != w.WorkerSize || g.WorkerCount != w.WorkerCount {
			t.Errorf("%s: region %d = %+v, want %+v", where, i, g, w)
		}
	}
}
