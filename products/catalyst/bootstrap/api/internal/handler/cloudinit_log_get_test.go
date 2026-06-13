// Tests for GET /api/v1/deployments/{id}/cloudinit-log — the
// post-mortem-survives-the-wipe contract (#3380 row D false-negative).
//
// The bug: the GET gated on `h.deployments.Load(id)` and 404'd
// "deployment not found" the moment the in-memory record was gone — even
// when `<id>-cloudinit.log` was still on the PVC. On kom4dc the pushed
// log is the ONLY Phase-1 forensic, so a wiped/GC'd record must NOT hide
// a log file that physically exists. These tests pin the decoupling:
// file lookup is independent of record lookup; ownership applies only
// while the record is live; 404 fires only when the file is truly
// absent; path-traversal ids are rejected before any ReadFile.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

// routerForCloudInitGet wires GetCloudInitLog into a chi router so the
// {id} URL param parses exactly as it does in production.
func routerForCloudInitGet(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{id}/cloudinit-log", h.GetCloudInitLog)
	return r
}

// seedCloudInitLog writes a fake <id>-cloudinit.log into dir, matching
// the on-PVC filename the PUT side produces.
func seedCloudInitLog(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir kubeconfigsDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+"-cloudinit.log"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed cloudinit log: %v", err)
	}
}

// TestGetCloudInitLog_RecordWiped_FileSurvives — the core #3380 fix:
// the deployment record is NOT in the in-memory map (wiped/GC'd), but
// the log file is on disk. The GET MUST serve it (200 + body), not 404.
func TestGetCloudInitLog_RecordWiped_FileSurvives(t *testing.T) {
	dir := t.TempDir()
	id := "059b62793440dc02" // real-shape 16-hex deployment id, no record
	want := "Cloud-init v. 24.x running 'modules:final'\nPhase-1 aborted: did not PUT kubeconfig\n"
	seedCloudInitLog(t, dir, id, want)

	h := &Handler{log: slog.Default(), kubeconfigsDir: dir}
	// NOTE: deliberately do NOT Store any deployment for id — this is the
	// post-mortem-after-wipe state.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id+"/cloudinit-log", nil)
	rec := httptest.NewRecorder()
	routerForCloudInitGet(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("record wiped but file present: want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != want {
		t.Fatalf("served body mismatch:\n want %q\n got  %q", want, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type: want text/plain; charset=utf-8, got %q", ct)
	}
}

// TestGetCloudInitLog_RecordLive_OwnerMatch — record still in the map,
// session email matches OwnerEmail → 200 + body (unchanged behaviour).
func TestGetCloudInitLog_RecordLive_OwnerMatch(t *testing.T) {
	dir := t.TempDir()
	id := "40c4e17667b600eb"
	want := "live-deployment cloud-init log\n"
	seedCloudInitLog(t, dir, id, want)

	h := &Handler{log: slog.Default(), kubeconfigsDir: dir}
	dep := &Deployment{ID: id, OwnerEmail: "emrah.baysal@example.com"}
	h.deployments.Store(id, dep)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id+"/cloudinit-log", nil)
	req.Header.Set("X-User-Email", "emrah.baysal@example.com")
	rec := httptest.NewRecorder()
	routerForCloudInitGet(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("owner match: want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != want {
		t.Fatalf("body mismatch: want %q got %q", want, rec.Body.String())
	}
}

// TestGetCloudInitLog_RecordLive_OwnerMismatch — record in the map but a
// different operator asks: 404 (never 403, never leaks existence), and
// the log bytes are NOT served, even though the file exists. Ownership
// scoping for LIVE records is preserved by the fix.
func TestGetCloudInitLog_RecordLive_OwnerMismatch(t *testing.T) {
	dir := t.TempDir()
	id := "40c4e17667b600eb"
	seedCloudInitLog(t, dir, id, "secret live log\n")

	h := &Handler{log: slog.Default(), kubeconfigsDir: dir}
	dep := &Deployment{ID: id, OwnerEmail: "owner@example.com"}
	h.deployments.Store(id, dep)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id+"/cloudinit-log", nil)
	req.Header.Set("X-User-Email", "intruder@example.com")
	rec := httptest.NewRecorder()
	routerForCloudInitGet(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant on a LIVE record: want 404, got %d", rec.Code)
	}
	if rec.Body.String() == "secret live log\n" {
		t.Fatalf("cross-tenant read leaked the log bytes")
	}
}

// TestGetCloudInitLog_NoFile_404 — neither record nor file: genuine
// 404 with the no-cloudinit-log body (not "deployment not found").
func TestGetCloudInitLog_NoFile_404(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: slog.Default(), kubeconfigsDir: dir}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/deadbeefdeadbeef/cloudinit-log", nil)
	rec := httptest.NewRecorder()
	routerForCloudInitGet(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("no file: want 404, got %d", rec.Code)
	}
	if got := rec.Body.String(); !contains(got, "no-cloudinit-log") {
		t.Fatalf("want no-cloudinit-log body, got %q", got)
	}
}

// TestGetCloudInitLog_PathTraversalRejected — an id carrying traversal
// is rejected with 400 BEFORE any filesystem read, so the
// record-decoupled file read can never escape kubeconfigsDir.
func TestGetCloudInitLog_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	// Plant a sensitive file one level up that a traversal would target.
	outside := filepath.Join(filepath.Dir(dir), "secret-cloudinit.log")
	_ = os.WriteFile(outside, []byte("TOP SECRET"), 0o600)
	t.Cleanup(func() { _ = os.Remove(outside) })

	h := &Handler{log: slog.Default(), kubeconfigsDir: dir}

	// chi will not route a literal "../" cleanly, so exercise the
	// sanitiser via a RouteContext-injected param (the same surface chi
	// hands the handler).
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "../secret")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/x/cloudinit-log", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.GetCloudInitLog(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path-traversal id: want 400, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() == "TOP SECRET" {
		t.Fatalf("traversal escaped kubeconfigsDir and leaked an outside file")
	}
}
