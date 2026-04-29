// Tests for the cloud-init kubeconfig postback contract (issue #183,
// Option D). What this file proves:
//
//  1. PUT /api/v1/deployments/{id}/kubeconfig
//     - 401 when Authorization header is missing or malformed
//     - 403 when bearer hash mismatches the persisted hash
//     - 403 when the deployment has no bearer hash on record
//     - 403 when KubeconfigPath is already set (single-use replay
//       defence)
//     - 422 when the body is empty or oversize
//     - 204 on first successful PUT, file written 0600, Result.
//       KubeconfigPath set, JSON record persisted
//  2. GET /api/v1/deployments/{id}/kubeconfig reads from the path
//     pointer (file content streamed verbatim, 200 application/yaml).
//  3. The bearer-token hash mint is constant-time-compare correct
//     (same plaintext → equal hashes; different plaintexts diverge).
//  4. The deployment JSON record on disk NEVER contains the
//     kubeconfig plaintext after Save — only the path pointer.
//  5. PUT triggers the Phase-1 helmwatch goroutine.
//  6. After a Pod restart, restoreFromStore re-launches helmwatch
//     for deployments whose KubeconfigPath points at an existing
//     file (issue #183 spec gate #6).
//  7. shouldResumePhase1 gates the resume launch correctly across
//     every branch of the predicate.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// validKubeconfigYAML — k3s-style kubeconfig used across PUT tests.
// Realistic enough that a sentinel grep on the JSON record proves
// the redaction invariant (the cluster-CA / server-token markers
// would only be in the record if the plaintext leaked).
const validKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://203.0.113.10:6443
    certificate-authority-data: TEST-CA-MARKER
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
users:
- name: default
  user:
    token: TEST-USER-TOKEN-MARKER
`

// hashTokenForTest mirrors the production hashBearerToken so test
// cases can compute the expected hash inline.
func hashTokenForTest(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// makePutFixture wires a Handler with both a deployments store AND
// a kubeconfigs directory backed by t.TempDir(). Returns the
// handler, kubeconfigs dir, the deployment id, and the bearer
// plaintext to use in PUT requests.
func makePutFixture(t *testing.T, status string) (*Handler, string, string, string) {
	t.Helper()
	deploymentsDir := t.TempDir()
	kubeconfigsDir := t.TempDir()

	st, err := store.New(deploymentsDir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	h := NewWithStoreAndKubeconfigsDir(silentLogger(), &fakePDM{}, st, kubeconfigsDir)

	id := "putkc-" + status
	bearer, hash, err := newBearerToken()
	if err != nil {
		t.Fatalf("newBearerToken: %v", err)
	}

	dep := &Deployment{
		ID:        id,
		Status:    status,
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "test." + id + ".example",
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "test." + id + ".example",
		},
		kubeconfigBearerHash: hash,
	}
	h.deployments.Store(id, dep)
	// Persist the freshly-minted record so the on-disk JSON-leak
	// grep below has something to read against.
	h.persistDeployment(dep)

	return h, kubeconfigsDir, id, bearer
}

// putReq composes a PUT *http.Request with the chi route param
// attached. Empty bearer means no Authorization header.
func putReq(t *testing.T, id, bearer string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut,
		"/api/v1/deployments/"+id+"/kubeconfig",
		bytes.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// getReq composes a GET *http.Request with the chi route param.
func getReq(t *testing.T, id string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/deployments/"+id+"/kubeconfig", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// ─────────────────────────────────────────────────────────────────
// PUT /kubeconfig — failure modes
// ─────────────────────────────────────────────────────────────────

func TestPutKubeconfig_MissingAuthorizationReturns401(t *testing.T) {
	h, _, id, _ := makePutFixture(t, "phase1-watching")

	w := httptest.NewRecorder()
	r := putReq(t, id, "", []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "missing-bearer") {
		t.Errorf("body should mention missing-bearer; got %s", w.Body.String())
	}
}

func TestPutKubeconfig_MalformedAuthorizationReturns401(t *testing.T) {
	h, _, id, _ := makePutFixture(t, "phase1-watching")

	cases := []struct {
		name   string
		header string
	}{
		{"basic-auth", "Basic dXNlcjpwYXNz"},
		{"empty-bearer", "Bearer "},
		{"no-scheme", "abcdef"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPut,
				"/api/v1/deployments/"+id+"/kubeconfig",
				bytes.NewReader([]byte(validKubeconfigYAML)))
			r.Header.Set("Authorization", c.header)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", id)
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

			h.PutKubeconfig(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPutKubeconfig_BearerMismatchReturns403(t *testing.T) {
	h, _, id, _ := makePutFixture(t, "phase1-watching")

	wrongBearer := strings.Repeat("a", 64)
	w := httptest.NewRecorder()
	r := putReq(t, id, wrongBearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid-bearer") {
		t.Errorf("body should mention invalid-bearer; got %s", w.Body.String())
	}
}

func TestPutKubeconfig_NoBearerHashOnRecordReturns403(t *testing.T) {
	deploymentsDir := t.TempDir()
	kubeconfigsDir := t.TempDir()
	st, err := store.New(deploymentsDir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := NewWithStoreAndKubeconfigsDir(silentLogger(), &fakePDM{}, st, kubeconfigsDir)

	id := "putkc-no-hash"
	dep := &Deployment{
		ID:        id,
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request:   provisioner.Request{SovereignFQDN: "test.example", Region: "fsn1"},
		Result:    &provisioner.Result{SovereignFQDN: "test.example"},
		// kubeconfigBearerHash deliberately empty
	}
	h.deployments.Store(id, dep)

	w := httptest.NewRecorder()
	r := putReq(t, id, "any-bearer-here", []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no-bearer-hash") {
		t.Errorf("body should mention no-bearer-hash; got %s", w.Body.String())
	}
}

func TestPutKubeconfig_AlreadySetReturns403(t *testing.T) {
	h, kcDir, id, bearer := makePutFixture(t, "phase1-watching")

	// First PUT — succeeds.
	w1 := httptest.NewRecorder()
	r1 := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(w1, r1)
	if w1.Code != http.StatusNoContent {
		t.Fatalf("first PUT: status = %d, want 204; body=%s", w1.Code, w1.Body.String())
	}
	if _, err := os.Stat(filepath.Join(kcDir, id+".yaml")); err != nil {
		t.Fatalf("first PUT did not write file: %v", err)
	}

	// Second PUT with the same bearer — 403 already-set.
	w2 := httptest.NewRecorder()
	r2 := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(w2, r2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("second PUT: status = %d, want 403; body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "already-set") {
		t.Errorf("body should mention already-set; got %s", w2.Body.String())
	}
}

func TestPutKubeconfig_EmptyBodyReturns422(t *testing.T) {
	h, kcDir, id, bearer := makePutFixture(t, "phase1-watching")

	w := httptest.NewRecorder()
	r := putReq(t, id, bearer, []byte{})
	h.PutKubeconfig(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(kcDir, id+".yaml")); !os.IsNotExist(err) {
		t.Errorf("empty-body PUT must NOT create kubeconfig file (got err=%v)", err)
	}
}

func TestPutKubeconfig_OversizeBodyReturns422(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "phase1-watching")

	body := make([]byte, 2<<20) // 2 MiB > 1 MiB cap
	w := httptest.NewRecorder()
	r := putReq(t, id, bearer, body)
	h.PutKubeconfig(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "body-too-large") {
		t.Errorf("body should mention body-too-large; got %s", w.Body.String())
	}
}

func TestPutKubeconfig_DeploymentNotFoundReturns404(t *testing.T) {
	deploymentsDir := t.TempDir()
	kubeconfigsDir := t.TempDir()
	st, _ := store.New(deploymentsDir)
	h := NewWithStoreAndKubeconfigsDir(silentLogger(), &fakePDM{}, st, kubeconfigsDir)

	w := httptest.NewRecorder()
	r := putReq(t, "nonexistent", "any-bearer", []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────
// PUT /kubeconfig — happy path
// ─────────────────────────────────────────────────────────────────

func TestPutKubeconfig_FirstSuccessWritesFile0600(t *testing.T) {
	h, kcDir, id, bearer := makePutFixture(t, "phase1-watching")

	w := httptest.NewRecorder()
	r := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	want := filepath.Join(kcDir, id+".yaml")
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("kubeconfig file missing: %v", err)
	}

	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("kubeconfig file mode = %o, want 0600", mode)
	}

	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read kubeconfig file: %v", err)
	}
	if string(got) != validKubeconfigYAML {
		t.Errorf("kubeconfig content drift: got %q want %q", got, validKubeconfigYAML)
	}

	val, _ := h.deployments.Load(id)
	dep := val.(*Deployment)
	dep.mu.Lock()
	gotPath := dep.Result.KubeconfigPath
	dep.mu.Unlock()
	if gotPath != want {
		t.Errorf("Result.KubeconfigPath = %q, want %q", gotPath, want)
	}

	rec, err := h.store.Load(id)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if rec.Result == nil || rec.Result.KubeconfigPath != want {
		t.Errorf("on-disk record KubeconfigPath = %v, want %q", rec.Result, want)
	}
}

// TestPutKubeconfig_OnDiskJSONNeverContainsPlaintext is the
// load-bearing redaction invariant. The on-disk JSON for a
// deployment with a captured kubeconfig MUST NOT contain the
// kubeconfig plaintext anywhere in its bytes — only the file path.
func TestPutKubeconfig_OnDiskJSONNeverContainsPlaintext(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "phase1-watching")

	w := httptest.NewRecorder()
	r := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}

	rawBytes, err := os.ReadFile(filepath.Join(h.store.Dir(), id+".json"))
	if err != nil {
		t.Fatalf("read on-disk record: %v", err)
	}
	raw := string(rawBytes)

	for _, leak := range []string{
		"TEST-CA-MARKER",
		"TEST-USER-TOKEN-MARKER",
		"certificate-authority-data",
		"BEGIN CERTIFICATE",
	} {
		if strings.Contains(raw, leak) {
			t.Errorf("on-disk record leaked kubeconfig plaintext (sentinel %q):\n%s", leak, raw)
		}
	}
	if !strings.Contains(raw, `"kubeconfigPath"`) {
		t.Errorf("on-disk record missing kubeconfigPath pointer:\n%s", raw)
	}
}

func TestPutKubeconfig_LaunchesPhase1Watch(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "phase1-watching")
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
	)
	h.phase1WatchTimeout = 2 * time.Second

	w := httptest.NewRecorder()
	r := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	val, _ := h.deployments.Load(id)
	dep := val.(*Deployment)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dep.mu.Lock()
		got := dep.Result != nil && len(dep.Result.ComponentStates) > 0
		dep.mu.Unlock()
		if got {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Result == nil || len(dep.Result.ComponentStates) == 0 {
		t.Errorf("Phase 1 watch did not populate ComponentStates after PUT: %+v", dep.Result)
	}
	if dep.Result.ComponentStates["cilium"] != helmwatch.StateInstalled {
		t.Errorf("ComponentStates[cilium] = %q, want %q",
			dep.Result.ComponentStates["cilium"], helmwatch.StateInstalled)
	}
}

// ─────────────────────────────────────────────────────────────────
// GET /kubeconfig — path-pointer flow
// ─────────────────────────────────────────────────────────────────

func TestGetKubeconfig_ReadsFromPathPointer(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "phase1-watching")

	wPut := httptest.NewRecorder()
	rPut := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(wPut, rPut)
	if wPut.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204; body=%s", wPut.Code, wPut.Body.String())
	}

	wGet := httptest.NewRecorder()
	rGet := getReq(t, id)
	h.GetKubeconfig(wGet, rGet)

	if wGet.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", wGet.Code, wGet.Body.String())
	}
	if got := wGet.Body.String(); got != validKubeconfigYAML {
		t.Errorf("GET body drift: got %q want %q", got, validKubeconfigYAML)
	}
	if ct := wGet.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
}

func TestGetKubeconfig_PathPointerSetButFileMissingReturns409(t *testing.T) {
	deploymentsDir := t.TempDir()
	kubeconfigsDir := t.TempDir()
	st, _ := store.New(deploymentsDir)
	h := NewWithStoreAndKubeconfigsDir(silentLogger(), &fakePDM{}, st, kubeconfigsDir)

	id := "kc-file-missing"
	dep := &Deployment{
		ID:        id,
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request:   provisioner.Request{SovereignFQDN: "test.example"},
		Result: &provisioner.Result{
			SovereignFQDN:  "test.example",
			KubeconfigPath: filepath.Join(kubeconfigsDir, "ghost.yaml"),
		},
	}
	h.deployments.Store(id, dep)

	w := httptest.NewRecorder()
	r := getReq(t, id)
	h.GetKubeconfig(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "kubeconfig-file-missing") {
		t.Errorf("body should mention kubeconfig-file-missing; got %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────
// Bearer-token mint / hash semantics
// ─────────────────────────────────────────────────────────────────

func TestNewBearerToken_HashRoundTrips(t *testing.T) {
	plaintext, hashHex, err := newBearerToken()
	if err != nil {
		t.Fatalf("newBearerToken: %v", err)
	}
	if len(plaintext) != 64 {
		t.Errorf("plaintext length = %d, want 64 hex chars", len(plaintext))
	}
	if len(hashHex) != 64 {
		t.Errorf("hashHex length = %d, want 64 hex chars", len(hashHex))
	}
	got := hashBearerToken(plaintext)
	if got != hashHex {
		t.Errorf("hashBearerToken(%q) = %q, want %q", plaintext, got, hashHex)
	}
	if hashHex == plaintext {
		t.Error("hashHex equals plaintext — hash function is identity")
	}
}

func TestNewBearerToken_DistinctTokensProduceDistinctHashes(t *testing.T) {
	p1, h1, err := newBearerToken()
	if err != nil {
		t.Fatalf("newBearerToken 1: %v", err)
	}
	p2, h2, err := newBearerToken()
	if err != nil {
		t.Fatalf("newBearerToken 2: %v", err)
	}
	if p1 == p2 {
		t.Errorf("two calls produced identical plaintexts — RNG broken")
	}
	if h1 == h2 {
		t.Errorf("two calls produced identical hashes — RNG broken")
	}
}

func TestConstantTimeCompare_SafeForBearerVerification(t *testing.T) {
	plaintext := strings.Repeat("a", 64)
	wrong := strings.Repeat("b", 64)

	hashRight := hashTokenForTest(plaintext)
	hashWrong := hashTokenForTest(wrong)

	if subtle.ConstantTimeCompare([]byte(hashRight), []byte(hashRight)) != 1 {
		t.Error("constant-time compare returned !=1 for identical hashes")
	}
	if subtle.ConstantTimeCompare([]byte(hashRight), []byte(hashWrong)) != 0 {
		t.Error("constant-time compare returned !=0 for distinct hashes")
	}
}

// ─────────────────────────────────────────────────────────────────
// Pod-restart resume (issue #183 spec gate #6)
// ─────────────────────────────────────────────────────────────────

func TestRestoreFromStore_ResumesHelmwatchWhenKubeconfigPathExists(t *testing.T) {
	deploymentsDir := t.TempDir()
	kubeconfigsDir := t.TempDir()

	id := "resume-on-restart"
	kcPath := filepath.Join(kubeconfigsDir, id+".yaml")
	if err := os.WriteFile(kcPath, []byte(validKubeconfigYAML), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	st1, _ := store.New(deploymentsDir)
	rec := store.Record{
		ID:        id,
		Status:    "ready",
		StartedAt: time.Now().Add(-1 * time.Minute),
		Request: store.RedactedRequest{
			SovereignFQDN: "test.example",
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "test.example",
			KubeconfigPath: kcPath,
			// Phase1FinishedAt deliberately nil — watch hadn't
			// terminated when the previous Pod died.
		},
		KubeconfigBearerHash: hashTokenForTest("any-old-bearer"),
	}
	if err := st1.Save(rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// "Pod restart" — build the handler WITHOUT auto-restore so we
	// can wire the fake dynamic factory before any goroutine
	// observes h.dynamicFactory. Then call restoreFromStore
	// explicitly. This is the production race-free path:
	// NewWithStoreAndKubeconfigsDir auto-restores at the end of
	// construction; in production the dynamic factory is the
	// (race-free) global default helmwatch.NewDynamicClientFromKubeconfig.
	// In tests we need to inject a fake first.
	st2, _ := store.New(deploymentsDir)
	h := newTestHandlerNoRestore(t, st2, kubeconfigsDir)
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
	)
	h.phase1WatchTimeout = 2 * time.Second

	// Now drive the restore. The shouldResumePhase1 predicate
	// passes for our seeded record (KubeconfigPath set, file
	// exists, Phase1FinishedAt nil, status="ready"), so the
	// resume goroutine fires through h.resumePhase1Watch with
	// our injected factory.
	h.restoreFromStore()

	val, ok := h.deployments.Load(id)
	if !ok {
		t.Fatalf("rehydrated deployment %q missing", id)
	}
	dep := val.(*Deployment)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dep.mu.Lock()
		populated := dep.Result != nil && len(dep.Result.ComponentStates) > 0
		dep.mu.Unlock()
		if populated {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Result.ComponentStates["cilium"] != helmwatch.StateInstalled {
		t.Errorf("post-resume ComponentStates[cilium] = %q, want %q",
			dep.Result.ComponentStates["cilium"], helmwatch.StateInstalled)
	}
}

// newTestHandlerNoRestore builds a Handler with a store + kubeconfigs
// directory but does NOT call restoreFromStore. Tests that need to
// inject a dynamic factory BEFORE the resume goroutine fires use
// this and then call h.restoreFromStore() themselves.
//
// In production NewWithStoreAndKubeconfigsDir auto-restores; the
// production dynamicFactory is the package default
// (helmwatch.NewDynamicClientFromKubeconfig) and is wired at
// runPhase1Watch resolution time, not at handler construction —
// so production has no race.
func newTestHandlerNoRestore(t *testing.T, st *store.Store, kubeconfigsDir string) *Handler {
	t.Helper()
	if kubeconfigsDir != "" {
		_ = os.MkdirAll(kubeconfigsDir, 0o700)
	}
	return &Handler{
		log:              silentLogger(),
		pdm:              &fakePDM{},
		store:            st,
		kubeconfigsDir:   kubeconfigsDir,
	}
}

func TestShouldResumePhase1_GatesProperly(t *testing.T) {
	deploymentsDir := t.TempDir()
	kubeconfigsDir := t.TempDir()
	st, _ := store.New(deploymentsDir)
	h := NewWithStoreAndKubeconfigsDir(silentLogger(), &fakePDM{}, st, kubeconfigsDir)

	finishedAt := time.Now().UTC()
	existingFile := filepath.Join(kubeconfigsDir, "exists.yaml")
	if err := os.WriteFile(existingFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	cases := []struct {
		name string
		dep  *Deployment
		rec  store.Record
		want bool
	}{
		{
			name: "no-result",
			dep:  &Deployment{Result: nil},
			rec:  store.Record{Status: "ready"},
			want: false,
		},
		{
			name: "empty-path",
			dep:  &Deployment{Result: &provisioner.Result{KubeconfigPath: ""}},
			rec:  store.Record{Status: "ready"},
			want: false,
		},
		{
			name: "already-finished",
			dep: &Deployment{Result: &provisioner.Result{
				KubeconfigPath:   existingFile,
				Phase1FinishedAt: &finishedAt,
			}},
			rec:  store.Record{Status: "ready"},
			want: false,
		},
		{
			name: "in-flight-rewritten-to-failed",
			dep:  &Deployment{Result: &provisioner.Result{KubeconfigPath: existingFile}},
			rec:  store.Record{Status: "phase1-watching"},
			want: false,
		},
		{
			name: "file-missing",
			dep:  &Deployment{Result: &provisioner.Result{KubeconfigPath: filepath.Join(kubeconfigsDir, "missing.yaml")}},
			rec:  store.Record{Status: "ready"},
			want: false,
		},
		{
			name: "resume-candidate",
			dep:  &Deployment{Result: &provisioner.Result{KubeconfigPath: existingFile}},
			rec:  store.Record{Status: "ready"},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.shouldResumePhase1(c.dep, c.rec)
			if got != c.want {
				t.Errorf("shouldResumePhase1 = %v, want %v", got, c.want)
			}
		})
	}
}

// TestPhase1Started_GuardPreventsDoubleWatch proves the at-most-
// once guard. After PUT triggers the watch, a manual call to
// runPhase1Watch on the SAME deployment must short-circuit.
func TestPhase1Started_GuardPreventsDoubleWatch(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "phase1-watching")
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
	)
	h.phase1WatchTimeout = 2 * time.Second

	w := httptest.NewRecorder()
	r := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	h.PutKubeconfig(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", w.Code)
	}

	val, _ := h.deployments.Load(id)
	dep := val.(*Deployment)

	// Wait for the PUT-launched watch to terminate.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dep.mu.Lock()
		done := dep.Result != nil && dep.Result.Phase1FinishedAt != nil
		dep.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Snapshot ComponentStates BEFORE the second invocation.
	dep.mu.Lock()
	before := len(dep.Result.ComponentStates)
	dep.mu.Unlock()

	// Second runPhase1Watch — must short-circuit via phase1Started.
	h.runPhase1Watch(dep)

	dep.mu.Lock()
	after := len(dep.Result.ComponentStates)
	dep.mu.Unlock()

	if after != before {
		t.Errorf("phase1Started guard failed: ComponentStates changed from %d to %d on second runPhase1Watch", before, after)
	}
}

// TestExtractBearer_RFC6750 covers the bearer-extraction helper.
// Case-insensitive scheme, single-space separator, trim trailing.
func TestExtractBearer_RFC6750(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"only-scheme", "Bearer", ""},
		{"empty-token", "Bearer ", ""},
		{"lower-case-scheme", "bearer abc123", "abc123"},
		{"upper-case-scheme", "BEARER abc123", "abc123"},
		{"mixed-case-scheme", "BeArEr abc123", "abc123"},
		{"with-trailing-space", "Bearer abc123 ", "abc123"},
		{"basic-auth-not-bearer", "Basic dXNlcjpwYXNz", ""},
		{"normal", "Bearer my-token", "my-token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractBearer(c.header)
			if got != c.want {
				t.Errorf("extractBearer(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}
