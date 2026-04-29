// Tests for the deployment persistence + restore-on-startup feature.
//
// User-reported regression: a deployment id created at 12:57 vanished
// after the catalyst-api Pod restarted ~6 times for image rolls. The
// new Pod's empty sync.Map returned 404 for the wizard's poll, the
// SSE stream closed before completion, and the user saw "Unreachable
// / Couldn't reach the deployment stream / SSE connection closed
// before completion / Deployment id 5cd1bceaaacb71f6".
//
// This file proves the four invariants that close the regression:
//
//  1. CreateDeployment writes the row to the store immediately, so a
//     Pod kill in the gap before the goroutine's first emit still
//     leaves a reachable record on disk.
//  2. recordEventAndPersist re-serializes after every event so the
//     in-flight history is durable.
//  3. NewWithStore on the same directory rehydrates every record into
//     a fresh handler's sync.Map — i.e. a Pod restart re-populates
//     state from the PVC.
//  4. An on-disk record still labelled "provisioning" at restart time
//     is rewritten to "failed" with an explanatory error, so the
//     wizard's FailureCard renders instead of a stuck-forever
//     progress bar.
//
// Plus the secret-redaction invariant: the on-disk JSON for a real
// deployment with a real Hetzner token must not contain that token
// anywhere in its bytes.
package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// silentLogger discards log output so test runs don't spam stdout
// with the expected "persist failed" lines from negative-path tests.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// makeStoreHandler wires a Handler with a fresh store on a t.TempDir().
// Returns the handler and the store dir so tests can assert on the
// on-disk side of the contract.
func makeStoreHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := NewWithStore(silentLogger(), &fakePDM{}, st)
	return h, dir
}

// TestPersistence_CreateDeploymentWritesRowImmediately covers invariant 1.
// A POST /api/v1/deployments must produce a JSON file in the store
// directory before the response writer returns 201, so a Pod kill in
// the goroutine-spawn gap still leaves the deployment reachable.
func TestPersistence_CreateDeploymentWritesRowImmediately(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
	pdm.ResetManagedDomains()

	h, dir := makeStoreHandler(t)

	body, _ := json.Marshal(map[string]any{
		"orgName":             "Acme",
		"orgEmail":            "ops@acme.io",
		"sovereignFQDN":       "k8s.acme.io",
		"sovereignDomainMode": "byo",
		"sovereignSubdomain":  "k8s",
		"hetznerToken":        "hcloud-secret-token-DO-NOT-LEAK",
		"hetznerProjectID":    "proj",
		"region":              "fsn1",
		"sshPublicKey":        "ssh-ed25519 AAAA test",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// Decode the response to get the deployment id.
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// File on disk MUST exist before the response returned.
	want := filepath.Join(dir, resp.ID+".json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("CreateDeployment did not persist row to %s: %v", want, err)
	}

	// Bytes on disk must not contain the customer's Hetzner token.
	raw, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "hcloud-secret-token-DO-NOT-LEAK") {
		t.Fatalf("on-disk record leaked Hetzner token; bytes:\n%s", raw)
	}

	// Wait for the runProvisioning goroutine to finish before t.TempDir
	// cleanup runs. The goroutine fails fast at `tofu` exec (no creds /
	// no PATH) and writes the failure-state record before returning;
	// without this wait, the cleanup races the final persistDeployment
	// and the directory's RemoveAll fails.
	waitForGoroutine(t, h, resp.ID)
}

// waitForGoroutine polls the deployment until isDone() is true, then
// returns. Used by tests that POST a deployment to drive
// runProvisioning's full lifecycle (which fails fast in CI without
// tofu creds / PATH) so test cleanup doesn't race the goroutine's
// final persist.
func waitForGoroutine(t *testing.T, h *Handler, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		val, ok := h.deployments.Load(id)
		if !ok {
			t.Fatalf("deployment %s missing from sync.Map", id)
		}
		if val.(*Deployment).isDone() {
			// Brief grace for the post-close persistDeployment.
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("deployment %s did not finish within 10s — runProvisioning hung?", id)
}

// TestPersistence_RestoreOnStartupRehydratesDeployments covers
// invariant 3 — a Pod restart re-populates the in-memory map from the
// PVC. We simulate it by writing records with a first handler, then
// constructing a second handler against the SAME directory and
// asserting both deployments are reachable via GET /deployments/{id}.
func TestPersistence_RestoreOnStartupRehydratesDeployments(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// First "Pod" — register two deployments and persist them.
	h1 := NewWithStore(silentLogger(), &fakePDM{}, st)
	dep1 := makeDeployment(t, h1, "persist-id-1")
	dep1.Status = "ready"
	dep1.recordEvent(provisioner.Event{Phase: "tofu-apply", Level: "info", Message: "step 1"})
	dep1.recordEvent(provisioner.Event{Phase: "tofu-apply", Level: "info", Message: "step 2"})
	finishDeployment(dep1, "ready")
	h1.persistDeployment(dep1)

	dep2 := makeDeployment(t, h1, "persist-id-2")
	dep2.Status = "ready"
	dep2.recordEvent(provisioner.Event{Phase: "ready", Level: "info", Message: "all done"})
	finishDeployment(dep2, "ready")
	h1.persistDeployment(dep2)

	// Second "Pod" — same directory, fresh in-memory state.
	st2, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New (Pod 2): %v", err)
	}
	h2 := NewWithStore(silentLogger(), &fakePDM{}, st2)

	// Both ids must be reachable via the GET endpoint.
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{id}", h2.GetDeployment)
	r.Get("/api/v1/deployments/{id}/events", h2.GetDeploymentEvents)
	r.Get("/api/v1/deployments/{id}/logs", h2.StreamLogs)
	srv := httptest.NewServer(r)
	defer srv.Close()

	for _, id := range []string{"persist-id-1", "persist-id-2"} {
		resp, err := http.Get(srv.URL + "/api/v1/deployments/" + id)
		if err != nil {
			t.Fatalf("GET %s: %v", id, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 (rehydration failed)", id, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// dep1's events MUST be replayable via /events — that's the
	// SSE-replay invariant after restart.
	resp, err := http.Get(srv.URL + "/api/v1/deployments/persist-id-1/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	var ev struct {
		State  map[string]any      `json:"state"`
		Events []provisioner.Event `json:"events"`
		Done   bool                `json:"done"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(ev.Events) != 2 {
		t.Errorf("rehydrated dep1 events = %d, want 2", len(ev.Events))
	}
	if !ev.Done {
		t.Errorf("rehydrated dep1 done = false; loaded deployments must report done=true (no goroutine alive)")
	}
}

// TestPersistence_InFlightStatusBecomesFailedOnRestart covers invariant 4.
// The architectural promise: a Pod restart during `tofu apply` orphans
// real Hetzner resources, and the wizard's FailureCard MUST render
// instead of a stuck-forever progress bar. The error message must
// mention the orphaned-resources cleanup.
func TestPersistence_InFlightStatusBecomesFailedOnRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// First "Pod" — write a record that's still in-flight (Pod
	// crashed mid-apply, never reached the terminal-state Save).
	h1 := NewWithStore(silentLogger(), &fakePDM{}, st)
	dep := makeDeployment(t, h1, "in-flight-id")
	dep.Status = "tofu-applying"
	dep.recordEvent(provisioner.Event{Phase: "tofu-apply", Level: "info", Message: "applying..."})
	h1.persistDeployment(dep)
	// Note: NOT calling finishDeployment — this simulates a Pod kill
	// before the goroutine reached close(dep.done).

	// Inspect on-disk state — confirm it's still labelled in-flight.
	rec, err := st.Load("in-flight-id")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rec.Status != "tofu-applying" {
		t.Fatalf("on-disk status = %q, want tofu-applying (test setup wrong)", rec.Status)
	}

	// Second "Pod" — restart with the same directory. The restored
	// deployment's status should now be `failed` with the operator
	// instructions in Error.
	st2, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New (Pod 2): %v", err)
	}
	h2 := NewWithStore(silentLogger(), &fakePDM{}, st2)

	val, ok := h2.deployments.Load("in-flight-id")
	if !ok {
		t.Fatal("rehydration failed: in-flight-id not in sync.Map")
	}
	dep2 := val.(*Deployment)

	dep2.mu.Lock()
	defer dep2.mu.Unlock()

	if dep2.Status != "failed" {
		t.Errorf("status after restart = %q, want failed", dep2.Status)
	}
	if !strings.Contains(dep2.Error, "catalyst-api restarted during provisioning") {
		t.Errorf("Error missing operator-facing description; got %q", dep2.Error)
	}
	if !strings.Contains(dep2.Error, "in-flight-id") {
		t.Errorf("Error missing deployment id (operator needs it for cleanup); got %q", dep2.Error)
	}
	if !strings.Contains(dep2.Error, "orphan") {
		t.Errorf("Error must surface that orphaned cloud resources exist; got %q", dep2.Error)
	}

	// And the on-disk file must reflect the rewritten state — so the
	// next Pod restart (or a manual ReadDir for an audit) sees the
	// terminal state, not the stuck in-flight one.
	rec2, err := st2.Load("in-flight-id")
	if err != nil {
		t.Fatalf("Load after restart: %v", err)
	}
	if rec2.Status != "failed" {
		t.Errorf("on-disk status after restart = %q, want failed (re-persist missing)", rec2.Status)
	}
}

// TestPersistence_OnDiskJSONIsRedacted covers the secret-redaction
// invariant end-to-end: a real CreateDeployment with a non-empty
// HetznerToken + Dynadot creds must produce a JSON file with the
// redaction marker, no plaintext secret anywhere.
func TestPersistence_OnDiskJSONIsRedacted(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	// Pool-mode requires CATALYST_GHCR_PULL_TOKEN; the test placeholder
	// must NOT appear on disk (the redaction invariant covers it via
	// the json:"-" tag on Request.GHCRPullToken).
	t.Setenv("CATALYST_GHCR_PULL_TOKEN", "ghp_TEST_REDACT_PLACEHOLDER_DO_NOT_LEAK")
	pdm.ResetManagedDomains()

	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := NewWithStore(silentLogger(), &fakePDM{}, st)
	// Inject Dynadot creds that the handler would normally read from
	// env at request time.
	h.dynadotAPIKey = "DYNADOT-KEY-DO-NOT-LEAK"
	h.dynadotAPISecret = "DYNADOT-SECRET-DO-NOT-LEAK"

	body, _ := json.Marshal(map[string]any{
		"orgName":             "Omantel",
		"orgEmail":            "ops@omantel.om",
		"sovereignFQDN":       "omantel.omani.works",
		"sovereignDomainMode": "pool",
		"sovereignPoolDomain": "omani.works",
		"sovereignSubdomain":  "omantel",
		"hetznerToken":        "HCLOUD-TOKEN-DO-NOT-LEAK",
		"hetznerProjectID":    "proj",
		"region":              "fsn1",
		"sshPublicKey":        "ssh-ed25519 AAAA test",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
	h.CreateDeployment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	waitForGoroutine(t, h, resp.ID)

	// Read every json file in the directory and assert no secret
	// leaks anywhere — even if a future code path adds a redundant
	// persist that bypasses Redact().
	entries, _ := os.ReadDir(dir)
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", ent.Name(), err)
		}
		secrets := []string{
			"HCLOUD-TOKEN-DO-NOT-LEAK",
			"DYNADOT-KEY-DO-NOT-LEAK",
			"DYNADOT-SECRET-DO-NOT-LEAK",
			// GHCR pull token must NEVER reach disk; Request.GHCRPullToken
			// is `json:"-"`. A regression that drops the tag would leak
			// here.
			"ghp_TEST_REDACT_PLACEHOLDER_DO_NOT_LEAK",
		}
		for _, s := range secrets {
			if strings.Contains(string(raw), s) {
				t.Errorf("on-disk file %s leaked secret %q", ent.Name(), s)
			}
		}
		// And the redacted marker must appear at least once for the
		// HetznerToken — proving redaction wrote SOMETHING in its
		// place, not silently dropping the field. The store disables
		// JSON HTML-escaping, so the marker lands literally as
		// "<redacted>" rather than the JS-string-safe "<redacted>".
		if !strings.Contains(string(raw), "<redacted>") {
			t.Errorf("on-disk file %s missing redaction marker; bytes:\n%s", ent.Name(), raw)
		}
	}
}

// TestPersistence_EventsAppendedToOnDiskRecord covers invariant 2 — the
// hot-path persist after every event. We don't run a real `tofu apply`
// (no creds, no PATH); instead we drive recordEventAndPersist directly
// and assert the on-disk record grows with each event.
func TestPersistence_EventsAppendedToOnDiskRecord(t *testing.T) {
	h, dir := makeStoreHandler(t)
	dep := makeDeployment(t, h, "events-append-id")
	// Persist the row first so the file exists.
	h.persistDeployment(dep)

	want := filepath.Join(dir, "events-append-id.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("initial persist did not produce %s: %v", want, err)
	}

	// Emit 5 events, asserting the file's event count grows after each.
	for i := 0; i < 5; i++ {
		h.recordEventAndPersist(dep, provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "tofu",
			Level:   "info",
			Message: "step " + string(rune('a'+i)),
		})
		raw, err := os.ReadFile(want)
		if err != nil {
			t.Fatalf("read after emit %d: %v", i, err)
		}
		var rec store.Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("decode after emit %d: %v", i, err)
		}
		if len(rec.Events) != i+1 {
			t.Errorf("on-disk events after emit %d = %d, want %d", i, len(rec.Events), i+1)
		}
	}
}

// TestPersistence_StoreAbsentNoOpForExistingTests confirms a nil store
// is a clean no-op — every existing test that uses NewWithPDM (no
// store) keeps working without writing anywhere.
func TestPersistence_StoreAbsentNoOpForExistingTests(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	if h.store != nil {
		t.Fatal("NewWithPDM must not auto-wire a store")
	}
	// Direct call must be a clean no-op (no panic, no error).
	dep := makeDeployment(t, h, "noop-id")
	h.persistDeployment(dep)
	h.recordEventAndPersist(dep, provisioner.Event{Message: "no-op"})
}

// TestPersistence_DockerStyleRoundTrip simulates the manual-proof
// scenario from the ticket: POST a deployment, simulate a Pod restart
// by constructing a new Handler against the same directory, GET the
// deployment id — must return the persisted state, not a 404.
//
// This is the unit-test analogue of `docker run -v /tmp/catalyst-test:
// /var/lib/catalyst/deployments`, POST, kill, restart, GET. The Go
// path is exercised end-to-end through the same HTTP routes the
// browser hits.
func TestPersistence_DockerStyleRoundTrip(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
	pdm.ResetManagedDomains()

	dir := t.TempDir()

	// "Pod 1" — wire the handler, POST a deployment, capture the id.
	st1, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h1 := NewWithStore(silentLogger(), &fakePDM{}, st1)

	r1 := chi.NewRouter()
	r1.Post("/api/v1/deployments", h1.CreateDeployment)
	srv1 := httptest.NewServer(r1)

	body, _ := json.Marshal(map[string]any{
		"orgName":             "RestartTest",
		"orgEmail":            "ops@example.io",
		"sovereignFQDN":       "k8s.example.io",
		"sovereignDomainMode": "byo",
		"sovereignSubdomain":  "k8s",
		"hetznerToken":        "tok",
		"hetznerProjectID":    "proj",
		"region":              "fsn1",
		"sshPublicKey":        "ssh-ed25519 AAAA test",
	})
	resp, err := http.Post(srv1.URL+"/api/v1/deployments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()
	srv1.Close()

	// Give the runProvisioning goroutine a moment to kick. It will
	// fail at tofu exec in CI (no creds) — that's fine; the test
	// only needs the row on disk, which CreateDeployment guarantees
	// before returning.
	time.Sleep(50 * time.Millisecond)

	// "Pod 2" — fresh handler, same directory.
	st2, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New (Pod 2): %v", err)
	}
	h2 := NewWithStore(silentLogger(), &fakePDM{}, st2)

	r2 := chi.NewRouter()
	r2.Get("/api/v1/deployments/{id}", h2.GetDeployment)
	srv2 := httptest.NewServer(r2)
	defer srv2.Close()

	// GET the deployment id — must NOT be 404.
	getResp, err := http.Get(srv2.URL + "/api/v1/deployments/" + created.ID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		gotBody, _ := io.ReadAll(getResp.Body)
		t.Fatalf("GET status = %d, want 200 (cross-Pod restore failed); body=%s", getResp.StatusCode, gotBody)
	}
	var state map[string]any
	_ = json.NewDecoder(getResp.Body).Decode(&state)
	if state["id"] != created.ID {
		t.Errorf("GET id = %v, want %v (cross-Pod identity mismatch)", state["id"], created.ID)
	}
	// Wait for both Pod's lingering goroutines (the original Pod's
	// runProvisioning may still be wrapping up after the goroutine
	// fails fast on the missing tofu binary). h1 is closed but its
	// goroutine refs h1.store, which references the same dir.
	waitForGoroutine(t, h1, created.ID)
}

// TestPersistence_New_FallsBackOnUnwritableDir ensures a New() call
// against a default path the process can't write to (CI without
// /var/lib access) doesn't crash; instead the handler runs without
// persistence and logs the warning. This is the safety net that lets
// existing tests + load tests run unchanged without the deployments
// directory configured.
func TestPersistence_New_FallsBackOnUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("CI sandbox; root bypasses unwritable-dir test")
	}
	t.Setenv("CATALYST_DEPLOYMENTS_DIR", "/proc/no-such-dir/under-procfs/x/y/z")
	h := New(silentLogger())
	if h == nil {
		t.Fatal("New returned nil on unwritable dir; must fall back to in-memory")
	}
	if h.store != nil {
		t.Errorf("expected store=nil on unwritable dir, got %+v", h.store)
	}
	// And basic operations should still work (no panic).
	dep := makeDeployment(t, h, "fallback-id")
	h.persistDeployment(dep)
	h.recordEventAndPersist(dep, provisioner.Event{Message: "x"})
}

// TestPersistence_CorruptFileSkippedAtStartup confirms a single
// corrupt JSON file in the deployments directory does not abort the
// whole load — every other deployment must still rehydrate.
func TestPersistence_CorruptFileSkippedAtStartup(t *testing.T) {
	dir := t.TempDir()
	// A valid record we expect to recover.
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h1 := NewWithStore(silentLogger(), &fakePDM{}, st)
	dep := makeDeployment(t, h1, "good-after-corrupt")
	dep.Status = "ready"
	finishDeployment(dep, "ready")
	h1.persistDeployment(dep)

	// Drop a corrupt sibling file.
	if err := os.WriteFile(filepath.Join(dir, "garbage.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	// Restart — corrupt file must be skipped, good record loaded.
	st2, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New (Pod 2): %v", err)
	}
	h2 := NewWithStore(silentLogger(), &fakePDM{}, st2)
	if _, ok := h2.deployments.Load("good-after-corrupt"); !ok {
		t.Error("good-after-corrupt missing after restart; corrupt sibling broke load")
	}
}

