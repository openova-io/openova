// Tests for the flat-file deployment store.
//
// The user-reported regression — a deployment id that vanished after a
// catalyst-api Pod restart — has three test surfaces:
//
//  1. Redact() must drop every credential (HetznerToken, DynadotAPIKey,
//     DynadotAPISecret, RegistrarToken). No leak via the on-disk
//     projection. Adding a future credential field that doesn't get
//     redacted is the kind of regression this test exists to catch.
//  2. Save+LoadAll round-trips a record byte-equivalent on every field
//     that is NOT a credential. The PDM reservation token is fine to
//     persist (per-deployment opaque identifier, not a credential).
//  3. LoadAll on a directory with garbage files (hidden, non-json,
//     half-written) returns the valid records and reports per-file
//     errors via the callback — a single corruption must not kill the
//     restore path for every other deployment.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func TestRedact_OmitsAllSecrets(t *testing.T) {
	req := provisioner.Request{
		OrgName:             "Omantel",
		OrgEmail:            "ops@omantel.om",
		SovereignFQDN:       "omantel.omani.works",
		SovereignDomainMode: "pool",
		SovereignPoolDomain: "omani.works",
		SovereignSubdomain:  "omantel",
		HetznerToken:        "real-hcloud-token-DO-NOT-LEAK",
		HetznerProjectID:    "real-project-id",
		Region:              "fsn1",
		ControlPlaneSize:    "cx32",
		WorkerSize:          "cx32",
		WorkerCount:         3,
		HAEnabled:           true,
		SSHPublicKey:        "ssh-ed25519 AAAA test",
		DynadotAPIKey:       "dynadot-key-DO-NOT-LEAK",
		DynadotAPISecret:    "dynadot-secret-DO-NOT-LEAK",
	}

	out := Redact(req)

	// Every credential field must be the redacted marker, never the
	// original plaintext. We assert on the marker AND on the absence
	// of the original substring — a regression that swapped the marker
	// for "***" or some other token would still be caught.
	if out.HetznerToken != redactedMarker {
		t.Errorf("HetznerToken = %q, want %q", out.HetznerToken, redactedMarker)
	}
	if out.DynadotAPIKey != redactedMarker {
		t.Errorf("DynadotAPIKey = %q, want %q", out.DynadotAPIKey, redactedMarker)
	}
	if out.DynadotAPISecret != redactedMarker {
		t.Errorf("DynadotAPISecret = %q, want %q", out.DynadotAPISecret, redactedMarker)
	}

	// The serialized form must not contain any of the secret values
	// anywhere. This catches a future field-reorder bug where Redact
	// forgets to overwrite a struct field copy.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	leaks := []string{
		"real-hcloud-token-DO-NOT-LEAK",
		"dynadot-key-DO-NOT-LEAK",
		"dynadot-secret-DO-NOT-LEAK",
	}
	for _, leak := range leaks {
		if strings.Contains(string(raw), leak) {
			t.Errorf("serialized RedactedRequest contains secret %q — leak via %s", leak, raw)
		}
	}

	// Non-credential fields ARE preserved — a wizard hitting GET
	// /deployments/<id> after a Pod restart needs the FQDN, region,
	// SKU, etc. for the FailureCard's diagnostic readout.
	if out.SovereignFQDN != "omantel.omani.works" {
		t.Errorf("SovereignFQDN should be preserved, got %q", out.SovereignFQDN)
	}
	if out.Region != "fsn1" {
		t.Errorf("Region should be preserved, got %q", out.Region)
	}
	if out.ControlPlaneSize != "cx32" {
		t.Errorf("ControlPlaneSize should be preserved, got %q", out.ControlPlaneSize)
	}
}

func TestRedact_EmptyCredentialsStayEmpty(t *testing.T) {
	// A wizard that didn't supply Dynadot creds (BYO mode) must not
	// have empty fields rewritten as `<redacted>` — that would falsely
	// suggest a credential was attached.
	req := provisioner.Request{
		OrgName:             "Acme",
		SovereignDomainMode: "byo",
		HetznerToken:        "t",
		// DynadotAPIKey + DynadotAPISecret intentionally empty.
	}
	out := Redact(req)
	if out.DynadotAPIKey != "" {
		t.Errorf("empty DynadotAPIKey was rewritten to %q", out.DynadotAPIKey)
	}
	if out.DynadotAPISecret != "" {
		t.Errorf("empty DynadotAPISecret was rewritten to %q", out.DynadotAPISecret)
	}
	if out.HetznerToken != redactedMarker {
		t.Errorf("non-empty HetznerToken should be redacted, got %q", out.HetznerToken)
	}
}

func TestSaveAndLoadAll_RoundTripsRecord(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Build a representative record. 3 events keeps the test fast but
	// exercises slice serialization.
	now := time.Now().UTC().Truncate(time.Second)
	rec := Record{
		ID:     "abcdef0123456789",
		Status: "ready",
		Request: Redact(provisioner.Request{
			OrgName:          "Omantel",
			SovereignFQDN:    "omantel.omani.works",
			Region:           "fsn1",
			HetznerToken:     "leaked-if-broken",
			DynadotAPIKey:    "leaked-if-broken",
			DynadotAPISecret: "leaked-if-broken",
		}),
		Result: &provisioner.Result{
			SovereignFQDN:  "omantel.omani.works",
			ControlPlaneIP: "1.2.3.4",
			LoadBalancerIP: "5.6.7.8",
		},
		StartedAt:           now,
		FinishedAt:          now.Add(15 * time.Minute),
		PDMReservationToken: "pdm-reservation-token-not-a-credential",
		PDMPoolDomain:       "omani.works",
		PDMSubdomain:        "omantel",
		Events: []provisioner.Event{
			{Time: now.Format(time.RFC3339), Phase: "tofu-init", Level: "info", Message: "Initialising"},
			{Time: now.Format(time.RFC3339), Phase: "tofu-apply", Level: "info", Message: "hcloud_server.cp[0] created"},
			{Time: now.Format(time.RFC3339), Phase: "ready", Level: "info", Message: "Sovereign ready"},
		},
	}

	if err := s.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File exists with the expected name.
	want := filepath.Join(dir, "abcdef0123456789.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("Save did not produce %s: %v", want, err)
	}

	// File contents must NOT contain any of the leaked-if-broken
	// values — this is the redacted-on-disk invariant.
	raw, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "leaked-if-broken") {
		t.Fatalf("on-disk file leaked credential — bytes:\n%s", raw)
	}

	// LoadAll round-trips.
	got, err := s.LoadAll(nil)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("LoadAll returned %d records, want 1", len(got))
	}
	g := got[0]
	if g.ID != rec.ID {
		t.Errorf("ID round-trip: %q != %q", g.ID, rec.ID)
	}
	if g.Status != rec.Status {
		t.Errorf("Status round-trip: %q != %q", g.Status, rec.Status)
	}
	if g.Result == nil || g.Result.LoadBalancerIP != "5.6.7.8" {
		t.Errorf("Result round-trip failed: %+v", g.Result)
	}
	if !g.StartedAt.Equal(rec.StartedAt) {
		t.Errorf("StartedAt round-trip: %v != %v", g.StartedAt, rec.StartedAt)
	}
	if g.PDMReservationToken != rec.PDMReservationToken {
		t.Errorf("PDMReservationToken round-trip failed (must persist — not a credential)")
	}
	if len(g.Events) != 3 {
		t.Errorf("Events round-trip: len=%d want 3", len(g.Events))
	}
	if g.Events[1].Message != "hcloud_server.cp[0] created" {
		t.Errorf("event message round-trip failed: %q", g.Events[1].Message)
	}
	if g.Request.HetznerToken != redactedMarker {
		t.Errorf("HetznerToken on disk should be %q, got %q", redactedMarker, g.Request.HetznerToken)
	}
}

func TestSave_RewriteOverwritesPreviousRecord(t *testing.T) {
	// Persistence-on-every-event semantics: rewrite the same id with a
	// growing event slice. The on-disk record must reflect the latest
	// state, not append duplicates.
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	base := Record{
		ID:        "rewrite-test-id",
		Status:    "provisioning",
		StartedAt: time.Now().UTC(),
	}
	for i := 0; i < 5; i++ {
		base.Events = append(base.Events, provisioner.Event{
			Phase:   "tofu",
			Level:   "info",
			Message: fmt.Sprintf("step %d", i),
		})
		if err := s.Save(base); err != nil {
			t.Fatalf("Save iter %d: %v", i, err)
		}
	}

	got, err := s.Load("rewrite-test-id")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != 5 {
		t.Errorf("after 5 saves with growing slice, on-disk events = %d, want 5", len(got.Events))
	}
	// Only one file in the directory — no per-write residue.
	entries, _ := os.ReadDir(dir)
	jsonCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Errorf("expected 1 .json file after rewrites, got %d", jsonCount)
	}
}

func TestLoadAll_SkipsGarbageFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// One valid record.
	good := Record{ID: "good-id", Status: "ready", StartedAt: time.Now().UTC()}
	if err := s.Save(good); err != nil {
		t.Fatalf("Save good: %v", err)
	}
	// One half-written file (invalid JSON).
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{\"id\": \"broken"), 0o600); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	// One unrelated file (a stray *.txt).
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	// One hidden file (looks like a leftover temp).
	if err := os.WriteFile(filepath.Join(dir, ".hidden.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write hidden: %v", err)
	}
	// One JSON with no ID — garbage from manual editing.
	if err := os.WriteFile(filepath.Join(dir, "noid.json"), []byte(`{"status":"hi"}`), 0o600); err != nil {
		t.Fatalf("write noid: %v", err)
	}

	var errs []string
	got, err := s.LoadAll(func(path string, e error) {
		errs = append(errs, fmt.Sprintf("%s: %v", filepath.Base(path), e))
	})
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// Exactly the one valid record loaded.
	if len(got) != 1 {
		t.Fatalf("LoadAll: got %d records, want 1 (errs: %v)", len(got), errs)
	}
	if got[0].ID != "good-id" {
		t.Errorf("loaded id = %q, want good-id", got[0].ID)
	}
	// The two recoverable-error files (broken.json, noid.json) should
	// each have invoked the callback. The hidden + non-json files
	// should have been silently skipped.
	if len(errs) != 2 {
		t.Errorf("expected 2 onErr calls (broken.json, noid.json), got %d: %v", len(errs), errs)
	}
}

func TestNew_UnwritableDirIsErrorAtStartup(t *testing.T) {
	// A PVC mounted with the wrong UID would surface as an unwritable
	// directory. Catching it at New() time means the catalyst-api Pod
	// fails its readiness probe instead of silently dropping every
	// deployment write. A tmpfs in-memory dir we can chmod 0o500 is
	// a portable substitute for the real PVC failure mode.
	if os.Getuid() == 0 {
		t.Skip("test relies on file-mode permission enforcement; running as root makes that a no-op")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := New(dir); err == nil {
		t.Fatal("New on read-only dir should return error, got nil")
	}
}

func TestPath_RejectsTraversal(t *testing.T) {
	// Defence-in-depth: a future caller passing a slash-bearing id
	// must not be able to write outside the store directory.
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bad := []string{"../escape", "a/b", "..", ".", `c:\\windows`, ""}
	for _, id := range bad {
		if _, err := s.path(id); err == nil {
			t.Errorf("path(%q) returned no error — traversal possible", id)
		}
	}
}
