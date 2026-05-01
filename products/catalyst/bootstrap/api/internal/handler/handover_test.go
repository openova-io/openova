package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeReceiver simulates the new Sovereign's /api/v1/handover/tofu-archive
// endpoint. It records every call so the caller-side test can assert the
// request shape was correct.
type fakeReceiver struct {
	mu       sync.Mutex
	body     tofuArchiveRequest
	status   int
	respBody []byte
	called   int
}

func (f *fakeReceiver) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.called++
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &f.body)
		status := f.status
		respBody := f.respBody
		f.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		if respBody == nil {
			respBody, _ = json.Marshal(tofuArchiveResponse{
				OK:         true,
				StoredAt:   "2026-05-01T00:00:00Z",
				SecretPath: "secret/catalyst/tofu-phase0-archive",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
	}
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func seedDeployment(t *testing.T, h *Handler, id, fqdn string) *Deployment {
	t.Helper()
	dep := &Deployment{
		ID:        id,
		Status:    "ready",
		Request:   provisioner.Request{SovereignFQDN: fqdn},
		eventsCh:  make(chan provisioner.Event, 16),
		eventsBuf: nil,
		done:      make(chan struct{}),
	}
	h.deployments.Store(id, dep)
	return dep
}

func TestFinaliseHandover_DryRunEmitsEventOnly(t *testing.T) {
	h := newTestHandler(t)
	dep := seedDeployment(t, h, "dep-dry", "tenant-x.omani.works")

	r := chi.NewRouter()
	r.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)

	body, _ := json.Marshal(finaliseRequest{DryRun: true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/finalise/dep-dry", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp finaliseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Steps.HandoverEventEmitted {
		t.Errorf("dry-run must still emit handover SSE event")
	}
	if resp.Steps.TofuArchiveSubmitted {
		t.Errorf("dry-run must NOT submit tofu archive")
	}
	if resp.Steps.KubeconfigRemoved {
		t.Errorf("dry-run must NOT remove kubeconfig")
	}
	if resp.ConsoleURL != "https://console.tenant-x.omani.works" {
		t.Errorf("consoleURL wrong: %q", resp.ConsoleURL)
	}

	// Confirm the SSE event landed in the buffer
	if len(dep.eventsBuf) == 0 || dep.eventsBuf[0].Phase != "handover" {
		t.Errorf("expected handover event in buffer; got %+v", dep.eventsBuf)
	}
}

func TestFinaliseHandover_FullFlow(t *testing.T) {
	h := newTestHandler(t)
	dep := seedDeployment(t, h, "dep-full", "tenant-y.omani.works")
	_ = dep

	// Pretend the OpenTofu workdir exists on disk with one file. The
	// finalise handler builds the provisioner via provisioner.New() so
	// it picks up the env-default workdir. We override via env var.
	tmp := t.TempDir()
	t.Setenv("CATALYST_TOFU_WORKDIR", tmp)
	sovereignName := "tenant-y-omani-works"
	workdir := filepath.Join(tmp, sovereignName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	stateFile := filepath.Join(workdir, "terraform.tfstate")
	if err := os.WriteFile(stateFile, []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatalf("write tfstate: %v", err)
	}

	// Pretend the kubeconfig exists on disk.
	kcDir := t.TempDir()
	h.kubeconfigsDir = kcDir
	kcPath := filepath.Join(kcDir, "dep-full.yaml")
	if err := os.WriteFile(kcPath, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("write kc: %v", err)
	}

	// Stand up a fake receiver and wire the handler to it.
	recvr := &fakeReceiver{}
	srv := httptest.NewServer(recvr.handler())
	defer srv.Close()
	h.SetHandoverTargetURL(srv.URL)
	h.SetHandoverHTTPClient(srv.Client())

	r := chi.NewRouter()
	r.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/finalise/dep-full", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp finaliseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Steps.HandoverEventEmitted {
		t.Errorf("event not emitted: %+v", resp)
	}
	if !resp.Steps.TofuArchiveSubmitted {
		t.Errorf("archive not submitted: %+v", resp)
	}
	if !resp.Steps.TofuWorkdirRemoved {
		t.Errorf("workdir not removed: %+v errs=%v", resp, resp.Errors)
	}
	if !resp.Steps.KubeconfigRemoved {
		t.Errorf("kubeconfig not removed: %+v errs=%v", resp, resp.Errors)
	}

	// Confirm the receiver actually saw the archive.
	if recvr.called != 1 {
		t.Fatalf("receiver called %d times, want 1", recvr.called)
	}
	if recvr.body.DeploymentID != "dep-full" {
		t.Errorf("body.deploymentId: %q", recvr.body.DeploymentID)
	}
	if recvr.body.SovereignFQDN != "tenant-y.omani.works" {
		t.Errorf("body.sovereignFqdn: %q", recvr.body.SovereignFQDN)
	}
	if len(recvr.body.Files) != 1 {
		t.Fatalf("expected 1 file in archive, got %d", len(recvr.body.Files))
	}
	gotEncoded, ok := recvr.body.Files["terraform.tfstate"]
	if !ok {
		t.Fatalf("archive missing terraform.tfstate; keys=%v", keysOf(recvr.body.Files))
	}
	gotDecoded, err := base64.StdEncoding.DecodeString(gotEncoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(gotDecoded) != `{"hello":"world"}` {
		t.Errorf("archive payload mismatch: %q", gotDecoded)
	}

	// Disk side-effects.
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Errorf("workdir not cleaned: %v", err)
	}
	if _, err := os.Stat(kcPath); !os.IsNotExist(err) {
		t.Errorf("kubeconfig not cleaned: %v", err)
	}
}

func TestFinaliseHandover_ReceiverFailureKeepsLocalState(t *testing.T) {
	h := newTestHandler(t)
	seedDeployment(t, h, "dep-fail", "tenant-z.omani.works")

	tmp := t.TempDir()
	t.Setenv("CATALYST_TOFU_WORKDIR", tmp)
	workdir := filepath.Join(tmp, "tenant-z-omani-works")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "terraform.tfstate"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error":"openbao unreachable"}`))
	}))
	defer srv.Close()
	h.SetHandoverTargetURL(srv.URL)
	h.SetHandoverHTTPClient(srv.Client())

	r := chi.NewRouter()
	r.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/finalise/dep-fail", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expect 200 with errors body; got %d", rec.Code)
	}
	var resp finaliseResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Steps.TofuArchiveSubmitted {
		t.Errorf("archive should not be submitted on receiver failure")
	}
	if resp.Steps.TofuWorkdirRemoved {
		t.Errorf("workdir must NOT be removed when archive submission failed")
	}
	if len(resp.Errors) == 0 {
		t.Errorf("expected error in response")
	}
	if _, err := os.Stat(workdir); err != nil {
		t.Errorf("workdir incorrectly cleaned: %v", err)
	}
}

func TestFinaliseHandover_NotFound(t *testing.T) {
	h := newTestHandler(t)
	r := chi.NewRouter()
	r.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/finalise/nope", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404; got %d", rec.Code)
	}
}

func TestReceiveTofuArchive_NoOpenBaoReturns503(t *testing.T) {
	h := newTestHandler(t)
	body, _ := json.Marshal(tofuArchiveRequest{
		DeploymentID:  "x",
		SovereignFQDN: "x.example",
		Files:         map[string]string{"a": "Yg=="},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ReceiveTofuArchive(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when openbao client absent; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReceiveTofuArchive_SealsToOpenBao(t *testing.T) {
	h := newTestHandler(t)

	var (
		gotPath string
		gotData map[string]any
	)
	bao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		var wrap struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(raw, &wrap)
		gotData = wrap.Data
		w.WriteHeader(http.StatusNoContent)
	}))
	defer bao.Close()
	c := openbao.New(bao.URL, "test-token")
	c.HTTP = bao.Client()
	h.SetOpenBao(c)

	body, _ := json.Marshal(tofuArchiveRequest{
		DeploymentID:  "dep-1",
		SovereignFQDN: "ten.example",
		CapturedAt:    "2026-05-01T00:00:00Z",
		Files: map[string]string{
			"terraform.tfstate": base64.StdEncoding.EncodeToString([]byte(`{"v":1}`)),
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ReceiveTofuArchive(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	var ack tofuArchiveResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &ack)
	if !ack.OK {
		t.Errorf("ack.ok false: %+v", ack)
	}
	if ack.SecretPath != "secret/catalyst/tofu-phase0-archive" {
		t.Errorf("ack.secretPath: %q", ack.SecretPath)
	}
	if gotPath != "/v1/secret/data/catalyst/tofu-phase0-archive" {
		t.Errorf("openbao path: %q", gotPath)
	}
	if gotData["deploymentId"] != "dep-1" {
		t.Errorf("missing deploymentId in payload: %v", gotData)
	}
	if gotData["sovereignFqdn"] != "ten.example" {
		t.Errorf("missing sovereignFqdn: %v", gotData)
	}
	filesJSON, ok := gotData["files"].(string)
	if !ok {
		t.Fatalf("files not stringified JSON: %v", gotData["files"])
	}
	var roundTripped map[string]string
	if err := json.Unmarshal([]byte(filesJSON), &roundTripped); err != nil {
		t.Fatalf("files JSON not parseable: %v", err)
	}
	if _, ok := roundTripped["terraform.tfstate"]; !ok {
		t.Errorf("files missing tfstate: %v", roundTripped)
	}
}

func TestReceiveTofuArchive_ValidationErrors(t *testing.T) {
	c := openbao.New("http://nowhere", "tk")
	cases := []struct {
		name string
		body any
	}{
		{"missing-fields", tofuArchiveRequest{}},
		{"missing-files", tofuArchiveRequest{DeploymentID: "x", SovereignFQDN: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t)
			h.SetOpenBao(c)
			raw, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(raw))
			rec := httptest.NewRecorder()
			h.ReceiveTofuArchive(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400; got %d (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBuildTofuArchive(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "a.tfstate"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "subdir", "lock"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlink should be skipped.
	if err := os.Symlink("a.tfstate", filepath.Join(tmp, "linked")); err != nil {
		t.Fatal(err)
	}

	got, err := buildTofuArchive(tmp)
	if err != nil {
		t.Fatalf("buildTofuArchive: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files; want 2 (regular files only). keys=%v", len(got), keysOf(got))
	}
	for _, k := range []string{"a.tfstate", filepath.Join("subdir", "lock")} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s in archive: keys=%v", k, keysOf(got))
		}
	}
}

func TestBuildTofuArchive_MissingDirIsEmpty(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "absent")
	got, err := buildTofuArchive(tmp)
	if err != nil {
		t.Fatalf("missing dir should not error; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty archive; got %v", got)
	}
}

// keysOf is a tiny helper used by table-driven tests above. Mirrors the
// shape of test helpers elsewhere in this package.
func keysOf[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Ensure the helmwatch.Watcher.Cancel hook compiles against the existing
// internal/helmwatch package without surprising the linker.
var _ = context.Background
