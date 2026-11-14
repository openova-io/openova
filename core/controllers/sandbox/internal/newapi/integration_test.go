// integration_test.go — end-to-end token-mint round-trip exercising the
// REAL newapi.Client through net/http against a httptest.Server playing
// the role of catalyst-api's /admin/tokens/sandbox bridge handler
// (platform/newapi/internal/handler/sandbox_token.go, PR #1638).
//
// The unit tests next door (client_test.go) cover request validation +
// happy-path field marshalling. The controller-side tests
// (../controller/sandbox_controller_test.go) cover the reconcile state
// machine via an in-process stub. This file fills the gap between the
// two: it wires the controller-runtime Reconciler with a REAL
// newapi.New(...) client + REAL net/http transport against a httptest
// server that:
//
//   - rejects the wrong Authorization bearer with 401 (mirroring the
//     handler's subtle.ConstantTimeCompare on AdminSecret), and
//   - returns 200 with {token, expires_at} for the matching bearer
//     (mirroring the handler's successful JWT mint).
//
// Both arms close the loop end-to-end: happy-path → controller writes
// the token bytes into the per-Sandbox gitops Secret; 401 → controller
// stamps TokenMintFailed on the CR and renders no manifests.
//
// READ-ONLY against real clusters; no kubectl, no helm, no Flux. Pure
// in-process: a controller-runtime fake.Client + a httptest server.
//
// Wave 15 — PR test(sandbox+newapi): integration test newapi token
// mint round-trip + verify reflector wiring.
package newapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/controller"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/newapi"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

// adminBearer is the shared-secret value the controller presents on
// every POST. The httptest handler below uses it as the gold-standard
// in its Authorization-header comparison — different value on the
// client side ⇒ 401 from the handler ⇒ controller stamps
// TokenMintFailed.
const adminBearer = "admin-bearer-int-test"

// newBridgeServer stands up a httptest server that mirrors the wire
// contract of platform/newapi/internal/handler/sandbox_token.go EXACTLY
// for the bits the controller depends on:
//
//   - POST /admin/tokens/sandbox
//   - Authorization: Bearer <adminBearer> → 200 with {token, expires_at}
//   - any other Authorization                        → 401 JSON error
//   - any other method/path                           → 404/405
//
// Returns the server URL + a pointer to a counter of requests received
// so the test can assert exact-call-count expectations.
func newBridgeServer(t *testing.T, expectedAdmin, replyToken string, replyExpiresAt time.Time) (string, *bridgeServerState, func()) {
	t.Helper()
	st := &bridgeServerState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.calls = append(st.calls, capturedCall{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
		})
		st.mu.Unlock()

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/admin/tokens/sandbox" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Mirror the handler's subtle.ConstantTimeCompare on the bearer
		// (handler/sandbox_token.go line ~200). On mismatch return the
		// same 401 JSON envelope the real handler ships.
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "missing or invalid authorization header",
			})
			return
		}
		if strings.TrimPrefix(auth, "Bearer ") != expectedAdmin {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid admin credentials",
			})
			return
		}

		// Echo the body fields into the recorded call so the test can
		// later assert org_id/user_id/sandbox_id propagation through
		// the controller -> client -> wire path.
		var body newapi.MintRequest
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		st.mu.Lock()
		st.lastBody = body
		st.mu.Unlock()

		writeJSON(w, http.StatusOK, newapi.MintResponse{
			Token:     replyToken,
			ExpiresAt: replyExpiresAt,
		})
	}))
	return srv.URL, st, srv.Close
}

type capturedCall struct {
	method string
	path   string
	auth   string
}

type bridgeServerState struct {
	mu       sync.Mutex
	calls    []capturedCall
	lastBody newapi.MintRequest
}

func (s *bridgeServerState) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *bridgeServerState) snapshot() ([]capturedCall, newapi.MintRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedCall, len(s.calls))
	copy(out, s.calls)
	return out, s.lastBody
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// giteaStub is a minimal in-process Gitea API surface sufficient for
// the reconciler's PutFile calls. Mirrors the giteaServer fixture used
// by sandbox_controller_test.go but kept here so this _test file is
// self-contained (the test lives in package newapi_test, not the
// controller package).
type giteaStub struct {
	mu          sync.Mutex
	files       map[string]fileEntry
	createFiles int
	updateFiles int
	server      *httptest.Server
}

type fileEntry struct {
	sha     string
	content []byte
}

func newGiteaStub(t *testing.T) *giteaStub {
	t.Helper()
	g := &giteaStub{files: map[string]fileEntry{}}
	g.server = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.server.Close)
	return g
}

func (g *giteaStub) URL() string { return g.server.URL }

func (g *giteaStub) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if r.Header.Get("Authorization") == "" {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/repos/") || !strings.Contains(p, "/contents/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	const prefix = "/api/v1/repos/"
	rest := p[len(prefix):]
	idx := strings.Index(rest, "/contents/")
	if idx < 0 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	ownerRepo := rest[:idx]
	filePath := rest[idx+len("/contents/"):]
	key := ownerRepo + "/" + filePath

	switch r.Method {
	case http.MethodGet:
		f, ok := g.files[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, gitea.File{
			Path:          filePath,
			SHA:           f.sha,
			Type:          "file",
			ContentBase64: base64.StdEncoding.EncodeToString(f.content),
		})
	case http.MethodPost, http.MethodPut:
		var body struct {
			Content string `json:"content"`
			SHA     string `json:"sha"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			http.Error(w, "bad b64", http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPost {
			if _, exists := g.files[key]; exists {
				http.Error(w, "exists", http.StatusUnprocessableEntity)
				return
			}
			g.createFiles++
		} else {
			g.updateFiles++
		}
		g.files[key] = fileEntry{
			sha:     fmt.Sprintf("sha-%d", g.createFiles+g.updateFiles),
			content: data,
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"content": gitea.File{
				Path: filePath,
				SHA:  g.files[key].sha,
				Type: "file",
			},
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func sampleSandbox() *sandboxapi.Sandbox {
	return &sandboxapi.Sandbox{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sandboxapi.GroupVersion.String(),
			Kind:       "Sandbox",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "emrah",
			Namespace:  "acme",
			Generation: 1,
			UID:        "00000000-0000-0000-0000-000000000099",
		},
		Spec: sandboxapi.SandboxSpec{
			Owner: sandboxapi.SandboxOwner{
				Email:  "ceo@acme.com",
				OrgRef: sandboxapi.SandboxOrgRef{Slug: "acme"},
			},
			Quota: sandboxapi.SandboxQuota{
				CPU:                "4",
				Memory:             "8Gi",
				Storage:            "50Gi",
				ConcurrentSessions: 3,
			},
			AgentCatalogue: []string{"claude-code"},
		},
	}
}

func makeReconciler(t *testing.T, bridgeURL, bridgeBearer string, sb *sandboxapi.Sandbox, fixedNow time.Time) (*controller.Reconciler, *giteaStub) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	if err := sandboxapi.AddToScheme(scheme); err != nil {
		t.Fatalf("add sandboxapi scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sandboxapi.Sandbox{}).
		WithObjects(sb).
		Build()

	gs := newGiteaStub(t)

	// THE integration boundary — the real newapi.Client speaking real
	// net/http against a real httptest server. No in-process stub.
	napi, err := newapi.New(bridgeURL, bridgeBearer, nil)
	if err != nil {
		t.Fatalf("newapi.New: %v", err)
	}

	r := &controller.Reconciler{
		Client:                cl,
		Log:                   logr.Discard(),
		GiteaClient:           gitea.New(gs.URL(), "test-token"),
		HostCluster:           "ct-eu-mgt-prod",
		SovereignFQDN:         "omantel.omani.works",
		Branch:                "main",
		TenantRepoName:        "catalyst-tenant",
		PtyServerImage:        "ghcr.io/openova-io/openova/sandbox-pty-server:test-sha",
		MCPImage:              "ghcr.io/openova-io/openova/sandbox-mcp:test-sha",
		NewapiURL:             "https://newapi.omantel.omani.works/v1",
		LLMGatewayTokenSecret: "sandbox-tokens",
		BYOSSecretPrefix:      "sandbox-byos-claude-code",
		IdleTimeoutMinutes:    30,
		NewAPIClient:          napi,
		DefaultChannels:       []string{"qwen"},
		Now:                   func() time.Time { return fixedNow },
	}
	return r, gs
}

// TestIntegration_TokenMint_HappyPath wires the real newapi.Client
// against a httptest server pretending to be the catalyst-api bridge.
// On a successful mint the controller MUST:
//
//   - POST exactly one request to /admin/tokens/sandbox
//   - carry Authorization: Bearer <adminBearer>
//   - carry the Sandbox CR's owner + org + uid in the body
//   - render the per-Sandbox Secret manifest under the Gitea prefix
//     with the bridge-issued token bytes
//   - stamp expiresAt + rotatedAt annotations on the CR
func TestIntegration_TokenMint_HappyPath(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	exp := fixedNow.Add(7 * 24 * time.Hour)
	const token = "jwt-int-happy"

	bridgeURL, st, closeBridge := newBridgeServer(t, adminBearer, token, exp)
	defer closeBridge()

	sb := sampleSandbox()
	r, gs := makeReconciler(t, bridgeURL, adminBearer, sb, fixedNow)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if got := st.callCount(); got != 1 {
		t.Errorf("bridge call count: got %d want 1", got)
	}
	calls, body := st.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one captured call, got %d", len(calls))
	}
	c := calls[0]
	if c.method != http.MethodPost {
		t.Errorf("method: got %q want POST", c.method)
	}
	if c.path != "/admin/tokens/sandbox" {
		t.Errorf("path: got %q want /admin/tokens/sandbox", c.path)
	}
	if c.auth != "Bearer "+adminBearer {
		t.Errorf("auth header: got %q", c.auth)
	}
	if body.OrgID != "acme" {
		t.Errorf("body.OrgID: got %q want acme", body.OrgID)
	}
	if body.UserID != "ceo@acme.com" {
		t.Errorf("body.UserID: got %q want ceo@acme.com", body.UserID)
	}
	if body.SandboxID != string(sb.UID) {
		t.Errorf("body.SandboxID: got %q want %q", body.SandboxID, sb.UID)
	}
	if len(body.AllowedChannels) != 1 || body.AllowedChannels[0] != "qwen" {
		t.Errorf("body.AllowedChannels: got %v want [qwen]", body.AllowedChannels)
	}

	// Per-Sandbox Secret manifest carries the bridge-issued token bytes.
	secretKey := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/secret-newapi-token.yaml"
	entry, ok := gs.files[secretKey]
	if !ok {
		t.Fatalf("expected rendered Secret at %q; files=%v", secretKey, mapKeys(gs.files))
	}
	if !strings.Contains(string(entry.content), `LLM_GATEWAY_TOKEN: "`+token+`"`) {
		t.Errorf("rendered Secret missing token bytes %q: %s", token, string(entry.content))
	}

	// Sandbox CR carries both lifecycle annotations.
	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get post-reconcile: %v", err)
	}
	wantExpStamp := exp.UTC().Format(time.RFC3339)
	if got.Annotations["openova.io/sandbox-token-expires-at"] != wantExpStamp {
		t.Errorf("CR expires-at annotation: got %q want %q",
			got.Annotations["openova.io/sandbox-token-expires-at"], wantExpStamp)
	}
	wantRotStamp := fixedNow.UTC().Format(time.RFC3339)
	if got.Annotations["openova.io/sandbox-token-rotated-at"] != wantRotStamp {
		t.Errorf("CR rotated-at annotation: got %q want %q",
			got.Annotations["openova.io/sandbox-token-rotated-at"], wantRotStamp)
	}
}

// TestIntegration_TokenMint_Unauthorized exercises the failure arm. The
// controller is wired with the WRONG admin bearer; the bridge handler
// stub returns 401; the controller MUST stamp TokenMintFailed on the CR
// + render zero gitops files + requeue.
func TestIntegration_TokenMint_Unauthorized(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	exp := fixedNow.Add(7 * 24 * time.Hour)

	bridgeURL, st, closeBridge := newBridgeServer(t, adminBearer, "never-issued", exp)
	defer closeBridge()

	sb := sampleSandbox()
	// Note: makeReconciler passes a DIFFERENT bearer than the server
	// expects → server returns 401.
	r, gs := makeReconciler(t, bridgeURL, "wrong-bearer", sb, fixedNow)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile returned error (it should not — failure is stamped on the CR): %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected non-zero RequeueAfter on bridge auth failure")
	}

	// Bridge handler MUST have received exactly one POST (the controller
	// learns the failure mode from the wire, not a cached short-circuit).
	if got := st.callCount(); got != 1 {
		t.Errorf("bridge call count: got %d want 1", got)
	}
	if gs.createFiles != 0 {
		t.Errorf("no gitea writes expected on 401, got %d creates", gs.createFiles)
	}

	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != "Failed" {
		t.Errorf("phase: got %q want Failed", got.Status.Phase)
	}
	if len(got.Status.Conditions) != 1 {
		t.Fatalf("conditions: got %d want 1: %+v", len(got.Status.Conditions), got.Status.Conditions)
	}
	cond := got.Status.Conditions[0]
	if cond.Reason != "TokenMintFailed" {
		t.Errorf("condition reason: got %q want TokenMintFailed", cond.Reason)
	}
	if cond.Status != "False" {
		t.Errorf("condition status: got %q want False", cond.Status)
	}
	if !strings.Contains(cond.Message, "401") {
		t.Errorf("condition message should surface 401 from the bridge: %q", cond.Message)
	}
	// CR must NOT have lifecycle annotations stamped on failure.
	if v := got.Annotations["openova.io/sandbox-token-expires-at"]; v != "" {
		t.Errorf("expires-at annotation should be empty on failed mint, got %q", v)
	}
}

// TestIntegration_TokenMint_BridgeUnreachable exercises a transport-
// level failure (server closed before request). The controller MUST
// surface this as TokenMintFailed (same Reason as a 401 — the
// architecture treats every newapi failure as a single retryable
// class) without panicking.
func TestIntegration_TokenMint_BridgeUnreachable(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	exp := fixedNow.Add(7 * 24 * time.Hour)

	// Stand up the server then immediately close it so the URL is
	// unreachable. The reconciler's MintSandboxToken must surface this
	// as a non-nil error → TokenMintFailed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, newapi.MintResponse{Token: "x", ExpiresAt: exp})
	}))
	bridgeURL := srv.URL
	srv.Close()

	sb := sampleSandbox()
	r, _ := makeReconciler(t, bridgeURL, adminBearer, sb, fixedNow)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected non-zero RequeueAfter on transport failure")
	}

	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != "Failed" {
		t.Errorf("phase: got %q want Failed", got.Status.Phase)
	}
	if len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Reason != "TokenMintFailed" {
		t.Errorf("expected TokenMintFailed condition on transport failure, got %+v", got.Status.Conditions)
	}
}

// mapKeys is a tiny helper for diagnostic logging.
func mapKeys(m map[string]fileEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
