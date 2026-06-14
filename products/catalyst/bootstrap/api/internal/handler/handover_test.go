package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
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
	//
	// Workdir is keyed by DeploymentID per PR #1487
	// (provisioner.Request.workdirKey) — match what
	// FinaliseHandover does with `filepath.Join(prov.WorkDir, id)`.
	tmp := t.TempDir()
	t.Setenv("CATALYST_TOFU_WORKDIR", tmp)
	workdir := filepath.Join(tmp, "dep-full")
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

	// Slim-record contract (#453): the deployment must NOT be deleted.
	// Audit-minimum + redirect-essentials must remain on the in-memory
	// row so a follow-up GET /api/v1/deployments/{id} hands the UI the
	// `adoptedAt` + `sovereignFQDN` it needs to fire the redirect from
	// console.openova.io/sovereign/<id> → console.<sovereign-fqdn>.
	val, ok := h.deployments.Load("dep-full")
	if !ok {
		t.Fatalf("deployment record was deleted post-handover; #453 contract requires slim retention")
	}
	got := val.(*Deployment)
	if got.AdoptedAt == nil {
		t.Errorf("AdoptedAt nil after FinaliseHandover; redirect contract needs non-nil timestamp")
	}
	if got.Request.SovereignFQDN != "tenant-y.omani.works" {
		t.Errorf("SovereignFQDN dropped from slim record: %q", got.Request.SovereignFQDN)
	}
	if got.ID != "dep-full" {
		t.Errorf("ID drifted on slim record: %q", got.ID)
	}
	// Operational fields must be zeroed.
	if got.Result != nil {
		t.Errorf("Result not zeroed on slim record: %+v", got.Result)
	}
	if got.Error != "" {
		t.Errorf("Error not zeroed on slim record: %q", got.Error)
	}
	if got.Request.HetznerToken != "" || got.Request.DynadotAPIKey != "" {
		t.Errorf("credential fields leaked into slim record: %+v", got.Request)
	}
	if got.Status != "adopted" {
		t.Errorf("Status not transitioned to adopted: %q", got.Status)
	}
}

func TestFinaliseHandover_ReceiverFailureKeepsLocalState(t *testing.T) {
	h := newTestHandler(t)
	seedDeployment(t, h, "dep-fail", "tenant-z.omani.works")

	tmp := t.TempDir()
	t.Setenv("CATALYST_TOFU_WORKDIR", tmp)
	// Workdir keyed by DeploymentID per PR #1487.
	workdir := filepath.Join(tmp, "dep-fail")
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

// TestReceiveTofuArchive_IsOutsideRequireSession (#3379) — the handover
// archive receiver MUST be reachable WITHOUT a catalyst_session cookie.
//
// Catalyst-Zero's postTofuArchive POSTs the sealed OpenTofu state to
// https://api.<sov-fqdn>/api/v1/handover/tofu-archive with ONLY a
// Content-Type header — it is a server-to-server call, no browser, no
// cookie. On Catalyst-Zero this slipped through because RequireSession is
// a nil-cfg passthrough; but on a REAL Sovereign (CATALYST_KC_ADDR set)
// the active middleware rejected every archive POST with 401
// {"error":"unauthenticated"} BEFORE ReceiveTofuArchive ever ran — so the
// tofu-phase0-archive marker never sealed → the cutover engine never fired
// → cutoverComplete stayed false forever (caught live on the hw136 cutover
// walk). This test wires an ACTIVE RequireSession and asserts a cookieless
// archive POST reaches the handler (503 for no-OpenBao, the handler's own
// content-based gate) rather than being 401'd by the middleware.
func TestReceiveTofuArchive_IsOutsideRequireSession(t *testing.T) {
	h := newTestHandler(t) // no OpenBao client → handler's own gate returns 503
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// A non-nil Config (no cookie secret / no JWKS) makes RequireSession
	// ENFORCE: ReadSessionToken returns "" for a cookieless request, so the
	// middleware 401s before any wrapped handler. A nil Config would be a
	// transparent passthrough and would NOT catch the placement bug.
	cfg := &auth.Config{Realm: "sovereign", ClientID: "catalyst-zero-ui"}

	r := chi.NewMux()
	// PUBLIC group — mirrors main.go: the archive receiver lives here,
	// OUTSIDE RequireSession (next to the kubeconfig PUT + cutover trigger).
	r.Post("/api/v1/handover/tofu-archive", h.ReceiveTofuArchive)
	// SESSION-GATED group — mirrors main.go's r.Group(RequireSession). The
	// finalise route + whoami live here.
	r.Group(func(rg chi.Router) {
		rg.Use(auth.RequireSession(cfg, log))
		rg.Get("/api/v1/whoami", h.HandleWhoami)
	})

	body, _ := json.Marshal(tofuArchiveRequest{
		DeploymentID:  "dep-x",
		SovereignFQDN: "x.example",
		Files:         map[string]string{"terraform.tfstate": "e30="},
	})

	// 1) The archive POST must reach the handler WITHOUT a cookie → 503
	//    (handler's no-OpenBao gate), NEVER 401 (middleware block).
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(body)))
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("REGRESSION (#3379): cookieless tofu-archive POST got 401 %s — "+
			"the handover archive receiver is behind RequireSession again; it MUST "+
			"be registered in the PUBLIC route section of main.go (the mother→child "+
			"call carries no session cookie, so a 401 here means the cutover can "+
			"NEVER fire)", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected the receiver to reach the handler (503 for no-OpenBao) "+
			"on a cookieless POST, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	// 2) Harness sanity: the RequireSession middleware IS enforcing here, so
	//    the 503 above is a real "handler ran cookieless", not a passthrough.
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("harness check: a RequireSession route should 401 without a "+
			"cookie, got %d — the middleware is not enforcing, so this test "+
			"cannot prove the receiver is genuinely public", rec2.Code)
	}
}

// TestReceiveTofuArchive_AddrOnlyClientReturns503 (#3374) — an addr-only
// openbao client (the on-Sovereign shape that powers the bare-URL SSO
// shim) must NOT turn this Pod into a handover target: the seal path
// still requires the token and keeps the exact 503 semantics.
func TestReceiveTofuArchive_AddrOnlyClientReturns503(t *testing.T) {
	h := newTestHandler(t)
	h.SetOpenBao(openbao.New("http://openbao.openbao.svc.cluster.local:8200", ""))
	body, _ := json.Marshal(tofuArchiveRequest{
		DeploymentID:  "x",
		SovereignFQDN: "x.example",
		Files:         map[string]string{"a": "Yg=="},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ReceiveTofuArchive(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for addr-only (token-less) openbao client; got %d body=%s", rec.Code, rec.Body.String())
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

// TestReceiveTofuArchive_FiresCutoverEngineOnSeal is the issue #933 / #3052
// regression guard: a successful Phase-0 archive seal at handover must
// auto-fire the cutover engine exactly once with source="handover", so the
// post-handover cutover (and TC-16's deny-egress proof, #2940) is no longer
// dormant. The engine itself is replaced by a seam (spawnCutoverEngineFn) so
// the test asserts the fire WITHOUT standing up the real 8-step engine.
//
// The fire is async (a goroutine), so the seam closes a buffered channel /
// records the call under a mutex; the test waits on a short deadline.
func TestReceiveTofuArchive_FiresCutoverEngineOnSeal(t *testing.T) {
	h := newTestHandler(t)

	// Real OpenBao seam (httptest) so the seal succeeds and returns 200.
	bao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer bao.Close()
	c := openbao.New(bao.URL, "test-token")
	c.HTTP = bao.Client()
	h.SetOpenBao(c)

	// A deps factory so cutoverDepsFor() succeeds (a Sovereign WITH the
	// cutover chart installed). The fake clientset is unused by the seam.
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) {
		return &cutoverDeps{ns: "catalyst"}, nil
	})

	// Engine seam: record every call (source + count) under a mutex and
	// signal completion via a channel so the async goroutine is observable.
	var (
		mu     sync.Mutex
		calls  int
		gotSrc string
		fired  = make(chan struct{}, 1)
	)
	h.spawnCutoverEngineFn = func(_ context.Context, _ *cutoverDeps, source string) (cutoverSpawnResult, error) {
		mu.Lock()
		calls++
		gotSrc = source
		mu.Unlock()
		select {
		case fired <- struct{}{}:
		default:
		}
		return cutoverSpawnResult{outcome: cutoverSpawnStarted}, nil
	}

	body, _ := json.Marshal(tofuArchiveRequest{
		DeploymentID:  "dep-933",
		SovereignFQDN: "t99.omani.works",
		CapturedAt:    "2026-06-08T00:00:00Z",
		Files: map[string]string{
			"terraform.tfstate": base64.StdEncoding.EncodeToString([]byte(`{"v":1}`)),
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ReceiveTofuArchive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("seal status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Wait for the async fire.
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("cutover engine was not fired within 5s of a successful archive seal")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("spawnCutoverEngine called %d times, want exactly 1", calls)
	}
	if gotSrc != "handover" {
		t.Errorf("spawnCutoverEngine source = %q, want %q (gate must be skipped on the handover-seal path)", gotSrc, "handover")
	}
}

// TestReceiveTofuArchive_SealSucceedsWhenCutoverNotConfigured proves the
// best-effort contract: a Sovereign without the cutover chart (cutoverDepsFor
// errors) STILL completes the handover with 200, and the engine is NOT fired.
// A handover must never fail just because the cutover surface is absent.
func TestReceiveTofuArchive_SealSucceedsWhenCutoverNotConfigured(t *testing.T) {
	h := newTestHandler(t)

	bao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer bao.Close()
	c := openbao.New(bao.URL, "test-token")
	c.HTTP = bao.Client()
	h.SetOpenBao(c)

	// Deps factory returns an error → cutover not configured on this
	// Sovereign. The async fire branch must be skipped entirely.
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) {
		return nil, errors.New("cutover: in-cluster config unavailable")
	})

	var fired int32
	h.spawnCutoverEngineFn = func(_ context.Context, _ *cutoverDeps, _ string) (cutoverSpawnResult, error) {
		atomic.AddInt32(&fired, 1)
		return cutoverSpawnResult{}, nil
	}

	body, _ := json.Marshal(tofuArchiveRequest{
		DeploymentID:  "dep-933b",
		SovereignFQDN: "t99.omani.works",
		Files: map[string]string{
			"terraform.tfstate": base64.StdEncoding.EncodeToString([]byte(`{"v":1}`)),
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ReceiveTofuArchive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("seal status = %d, want 200 (handover must succeed without cutover chart); body=%s", rec.Code, rec.Body.String())
	}
	// Give any (erroneously-spawned) goroutine a moment to run, then assert
	// the engine was never fired.
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&fired); n != 0 {
		t.Errorf("cutover engine fired %d times when cutover not configured, want 0", n)
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

// TestFinaliseHandover_PreservesRedirectContract is the explicit guard
// against the regression #453 fixed: handover-finalisation MUST leave a
// reachable deployment row whose JSON shape carries `adoptedAt` +
// `sovereignFQDN`, because that is what the UI router's beforeLoad
// fetches to fire the post-handover redirect (issue #319 PR #451).
//
// We drive the same FinaliseHandover handler used in production, then
// hit GET /api/v1/deployments/{id} (the canonical handler the UI
// fetches) and assert the JSON response contains the redirect
// contract. Anything else (404, missing adoptedAt, missing
// sovereignFQDN) breaks the redirect.
func TestFinaliseHandover_PreservesRedirectContract(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := newTestHandler(t)
	h.store = st

	dep := seedDeployment(t, h, "dep-redir", "tenant-r.omani.works")
	dep.Request.OrgEmail = "ops@tenant-r.example"
	dep.Request.OrgName = "Tenant R"
	// Seed an initial Save so on-disk state exists pre-handover.
	h.persistDeployment(dep)

	// Wire a fake receiver so the Tofu archive POST succeeds.
	recvr := &fakeReceiver{}
	srv := httptest.NewServer(recvr.handler())
	defer srv.Close()
	h.SetHandoverTargetURL(srv.URL)
	h.SetHandoverHTTPClient(srv.Client())

	r := chi.NewRouter()
	r.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)
	r.Get("/api/v1/deployments/{id}", h.GetDeployment)

	// Drive the finalise.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/handover/finalise/dep-redir", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("finalise: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// GET /api/v1/deployments/{id} — the path the UI router beforeLoad
	// fetches. Per #319 PR #451 it expects {adoptedAt, sovereignFQDN}.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/dep-redir", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET deployment after handover: status=%d body=%s — record was deleted! redirect cannot fire", getRec.Code, getRec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	adoptedAt, ok := body["adoptedAt"].(string)
	if !ok || adoptedAt == "" {
		t.Fatalf("adoptedAt missing/empty in GET response: %v", body)
	}
	if _, err := time.Parse(time.RFC3339, adoptedAt); err != nil {
		t.Errorf("adoptedAt not RFC3339: %q (%v)", adoptedAt, err)
	}
	if got, _ := body["sovereignFQDN"].(string); got != "tenant-r.omani.works" {
		t.Errorf("sovereignFQDN missing or wrong: %v", body)
	}

	// On-disk record must round-trip through the store with AdoptedAt set.
	rec2, err := st.Load("dep-redir")
	if err != nil {
		t.Fatalf("store.Load post-handover: %v — on-disk record missing breaks redirect after Pod restart", err)
	}
	if rec2.AdoptedAt == nil {
		t.Errorf("on-disk record AdoptedAt nil")
	}
	if rec2.Request.SovereignFQDN != "tenant-r.omani.works" {
		t.Errorf("on-disk record SovereignFQDN: %q", rec2.Request.SovereignFQDN)
	}
	// Operational fields must be absent on disk.
	if rec2.Result != nil {
		t.Errorf("on-disk Result not zeroed: %+v", rec2.Result)
	}
	if rec2.Request.HetznerToken != "" {
		t.Errorf("on-disk credentials leaked: %q", rec2.Request.HetznerToken)
	}
}

// TestSlimForHandover is a unit-level cover for the in-memory record
// transform invoked by FinaliseHandover. We assert all four classes of
// behaviour: audit fields kept, redirect field set, operational fields
// zeroed, channels closed.
func TestSlimForHandover(t *testing.T) {
	finished := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		in   *Deployment
	}{
		{
			name: "full-record",
			in: &Deployment{
				ID:        "dep-1",
				Status:    "ready",
				StartedAt: earlier,
				Request: provisioner.Request{
					OrgName:       "Acme",
					OrgEmail:      "ops@acme.io",
					SovereignFQDN: "k8s.acme.io",
					HetznerToken:  "secret-do-not-leak",
					DynadotAPIKey: "secret-too",
				},
				Result: &provisioner.Result{
					LoadBalancerIP: "1.2.3.4",
					KubeconfigPath: "/tmp/kc",
				},
				Error:                "",
				kubeconfigBearerHash: "deadbeef",
				pdmReservationToken:  "rt",
				pdmPoolDomain:        "omani.works",
				pdmSubdomain:         "acme",
				eventsCh:             make(chan provisioner.Event, 4),
				done:                 make(chan struct{}),
			},
		},
		{
			name: "minimal-record",
			in: &Deployment{
				ID:        "dep-2",
				Status:    "phase1-watching",
				StartedAt: earlier,
				Request: provisioner.Request{
					SovereignFQDN: "byo.example.com",
				},
				eventsCh: make(chan provisioner.Event, 1),
				done:     make(chan struct{}),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.in.SlimForHandover(finished)
			if out == nil {
				t.Fatal("SlimForHandover returned nil")
			}
			// Audit-minimum kept.
			if out.ID != tc.in.ID {
				t.Errorf("ID dropped: in=%q out=%q", tc.in.ID, out.ID)
			}
			if out.Request.SovereignFQDN != tc.in.Request.SovereignFQDN {
				t.Errorf("SovereignFQDN dropped: in=%q out=%q", tc.in.Request.SovereignFQDN, out.Request.SovereignFQDN)
			}
			if !out.StartedAt.Equal(tc.in.StartedAt) {
				t.Errorf("StartedAt dropped: in=%v out=%v", tc.in.StartedAt, out.StartedAt)
			}
			if out.Request.OrgName != tc.in.Request.OrgName {
				t.Errorf("OrgName dropped: in=%q out=%q", tc.in.Request.OrgName, out.Request.OrgName)
			}
			if out.Request.OrgEmail != tc.in.Request.OrgEmail {
				t.Errorf("OrgEmail (createdBy) dropped: in=%q out=%q", tc.in.Request.OrgEmail, out.Request.OrgEmail)
			}
			// Redirect contract.
			if out.AdoptedAt == nil {
				t.Errorf("AdoptedAt nil")
			} else if !out.AdoptedAt.Equal(finished) {
				t.Errorf("AdoptedAt: got=%v want=%v", *out.AdoptedAt, finished)
			}
			// Status transitioned.
			if out.Status != "adopted" {
				t.Errorf("Status: got=%q want=adopted", out.Status)
			}
			// Operational fields zeroed.
			if out.Result != nil {
				t.Errorf("Result not zeroed: %+v", out.Result)
			}
			if out.Error != "" {
				t.Errorf("Error not zeroed: %q", out.Error)
			}
			if out.Request.HetznerToken != "" {
				t.Errorf("HetznerToken leaked: %q", out.Request.HetznerToken)
			}
			if out.Request.DynadotAPIKey != "" {
				t.Errorf("DynadotAPIKey leaked: %q", out.Request.DynadotAPIKey)
			}
			if out.kubeconfigBearerHash != "" {
				t.Errorf("kubeconfigBearerHash leaked: %q", out.kubeconfigBearerHash)
			}
			if out.pdmReservationToken != "" {
				t.Errorf("pdmReservationToken leaked: %q", out.pdmReservationToken)
			}
			// Channels closed (any consumer terminates immediately).
			select {
			case _, open := <-out.eventsCh:
				if open {
					t.Errorf("eventsCh not closed")
				}
			default:
				t.Errorf("eventsCh blocked (should be closed)")
			}
			select {
			case <-out.done:
				// closed — good
			default:
				t.Errorf("done not closed")
			}
		})
	}
}

// TestSlimForHandover_StoreRecordRoundTrip verifies the slim record
// serialised through store.Record JSON and decoded back retains the
// AdoptedAt + SovereignFQDN contract. This is the cross-restart guard:
// after a Pod roll, the rehydrated row must still serve the redirect.
func TestSlimForHandover_StoreRecordRoundTrip(t *testing.T) {
	finished := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	original := &Deployment{
		ID:        "dep-rt",
		Status:    "ready",
		StartedAt: time.Date(2026, 4, 28, 9, 0, 0, 0, time.UTC),
		Request: provisioner.Request{
			OrgName:       "Tenant",
			OrgEmail:      "ops@tenant.io",
			SovereignFQDN: "tenant.omani.works",
			HetznerToken:  "secret-must-not-survive",
		},
	}
	slim := original.SlimForHandover(finished)
	rec := slim.toRecord()

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded store.Record
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.AdoptedAt == nil || !decoded.AdoptedAt.Equal(finished) {
		t.Errorf("AdoptedAt did not round-trip: %v", decoded.AdoptedAt)
	}
	if decoded.Request.SovereignFQDN != "tenant.omani.works" {
		t.Errorf("SovereignFQDN did not round-trip: %q", decoded.Request.SovereignFQDN)
	}
	// Credentials must be redacted on disk (existing Redact contract).
	if decoded.Request.HetznerToken != "" {
		// Slim record sets HetznerToken to "" before redaction; redaction
		// only marks present fields. So an empty token must round-trip
		// as empty (not <redacted>).
		t.Errorf("HetznerToken not zero on slim record after round-trip: %q", decoded.Request.HetznerToken)
	}
}

// Ensure the helmwatch.Watcher.Cancel hook compiles against the existing
// internal/helmwatch package without surprising the linker.
var _ = context.Background
