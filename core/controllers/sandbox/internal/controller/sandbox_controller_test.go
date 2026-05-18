// sandbox_controller_test.go — Wave 1 + Wave 8 happy-path + drift +
// idempotency coverage for the sandbox reconciler.

package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type giteaServer struct {
	t *testing.T

	mu sync.Mutex

	files map[string]fileEntry

	createFiles int
	updateFiles int

	server *httptest.Server
}

type fileEntry struct {
	sha     string
	content []byte
}

func newGiteaServer(t *testing.T) *giteaServer {
	gs := &giteaServer{
		t:     t,
		files: map[string]fileEntry{},
	}
	gs.server = httptest.NewServer(http.HandlerFunc(gs.handle))
	t.Cleanup(gs.server.Close)
	return gs
}

func (g *giteaServer) URL() string { return g.server.URL }

func (g *giteaServer) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if r.Header.Get("Authorization") == "" {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path

	if strings.HasPrefix(p, "/api/v1/repos/") && strings.Contains(p, "/contents/") {
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
			return
		case http.MethodPost, http.MethodPut:
			var body struct {
				Message string `json:"message"`
				Content string `json:"content"`
				Branch  string `json:"branch"`
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
			return
		}
	}

	g.t.Logf("giteaServer: unhandled %s %s", r.Method, r.URL.Path)
	http.Error(w, "not found", http.StatusNotFound)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func makeReconciler(t *testing.T, objs ...client.Object) (*Reconciler, *giteaServer) {
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
		WithObjects(objs...).
		Build()

	gs := newGiteaServer(t)

	r := &Reconciler{
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
	}
	return r, gs
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
			UID:        "00000000-0000-0000-0000-000000000001",
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
			Repos: []sandboxapi.SandboxRepo{
				{GiteaRepo: "acme/eventforge"},
				{GiteaRepo: "acme/internal-tools"},
			},
			AgentCatalogue: []string{"claude-code", "cursor-agent"},
			PreviewDomain:  "sb-emrah.rzk7.openova.io",
		},
	}
}

func TestReconcile_HappyPath(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("happy path should not requeue: got %v", res)
	}

	// Wave 1 + Wave 8: 6 fixed + 1 kust + 2 repo PVCs + 4 wave-8 = 13.
	expectedFiles := 6 + 1 + 2 + 4
	if gs.createFiles != expectedFiles {
		t.Errorf("expected %d file creates, got %d", expectedFiles, gs.createFiles)
	}
	if gs.updateFiles != 0 {
		t.Errorf("expected 0 file updates on first reconcile, got %d", gs.updateFiles)
	}

	wantPrefix := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/"
	for key := range gs.files {
		if !strings.HasPrefix(key, wantPrefix) {
			t.Errorf("file %q not under expected prefix %q", key, wantPrefix)
		}
	}

	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get post-reconcile: %v", err)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Errorf("observedGeneration: got %d want 1", got.Status.ObservedGeneration)
	}
	if got.Status.Phase != "Provisioning" {
		t.Errorf("phase: got %q want %q", got.Status.Phase, "Provisioning")
	}
	if got.Status.GitopsPath != "sandbox/ceo-at-acme-com" {
		t.Errorf("gitopsPath: got %q", got.Status.GitopsPath)
	}
	if len(got.Status.Conditions) != 1 ||
		got.Status.Conditions[0].Type != "Ready" ||
		got.Status.Conditions[0].Status != "True" ||
		got.Status.Conditions[0].Reason != "GitopsReconciled" {
		t.Errorf("expected Ready=True/GitopsReconciled, got %+v", got.Status.Conditions)
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	firstCreates := gs.createFiles
	firstUpdates := gs.updateFiles

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if delta := gs.createFiles - firstCreates; delta != 0 {
		t.Errorf("idempotency: expected zero new creates, got %d", delta)
	}
	if delta := gs.updateFiles - firstUpdates; delta != 0 {
		t.Errorf("idempotency: expected zero file updates, got %d", delta)
	}
}

func TestReconcile_OwnerOrgRefMissing(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	sb.Spec.Owner.OrgRef.Slug = ""
	r, gs := makeReconciler(t, sb)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile (drift): %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("drift should not requeue: got %v", res)
	}
	if gs.createFiles != 0 || gs.updateFiles != 0 {
		t.Errorf("drift: no Gitea writes expected, got creates=%d updates=%d",
			gs.createFiles, gs.updateFiles)
	}

	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != "Failed" {
		t.Errorf("phase: got %q want Failed", got.Status.Phase)
	}
	if len(got.Status.Conditions) != 1 ||
		got.Status.Conditions[0].Status != "False" ||
		got.Status.Conditions[0].Reason != "OwnerOrgRefMissing" {
		t.Errorf("expected OwnerOrgRefMissing False condition, got %+v", got.Status.Conditions)
	}
}

func TestReconcile_OwnerEmailMissing(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	sb.Spec.Owner.Email = ""
	r, gs := makeReconciler(t, sb)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile (drift): %v", err)
	}
	if gs.createFiles != 0 {
		t.Errorf("drift: no Gitea writes expected, got %d creates", gs.createFiles)
	}

	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.Conditions) != 1 ||
		got.Status.Conditions[0].Reason != "OwnerEmailMissing" {
		t.Errorf("expected OwnerEmailMissing False condition, got %+v", got.Status.Conditions)
	}
}

func TestReconcile_Missing_NoError(t *testing.T) {
	t.Parallel()
	r, _ := makeReconciler(t)
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ghost", Namespace: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile of missing CR should be a no-op, got: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("missing CR should not requeue, got %v", res)
	}
}

// TestReconcile_Wave8RuntimeShape asserts the Wave 8 runtime manifests
// (pty-server StatefulSet, MCP Deployment, Service, HTTPRoute) carry
// the right identity + env wiring + BYOS branching + hostname derivation.
func TestReconcile_Wave8RuntimeShape(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	prefix := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/"
	get := func(name string) string {
		gs.mu.Lock()
		defer gs.mu.Unlock()
		entry, ok := gs.files[prefix+name]
		if !ok {
			t.Fatalf("expected rendered file %q in gitea stub", prefix+name)
		}
		return string(entry.content)
	}

	ss := get("statefulset-pty-server.yaml")
	for _, want := range []string{
		"kind: StatefulSet",
		"name: pty-server",
		"namespace: sandbox-ceo-at-acme-com",
		"replicas: 3",
		`image: "ghcr.io/openova-io/openova/sandbox-pty-server:test-sha"`,
		"PTY_SERVER_ADDR",
		"SANDBOX_OWNER_UID",
		`value: "ceo-at-acme-com"`,
		"ORG_ID",
		`value: "acme"`,
		"NEWAPI_URL",
		`value: "https://newapi.omantel.omani.works/v1"`,
		"OPENAI_BASE_URL",
		"LLM_GATEWAY_TOKEN",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		`name: "sandbox-byos-claude-code-ceo-at-acme-com"`,
		"key: access_token",
		"openova.io/sandbox-idle-timeout-minutes",
		"name: repo-acme-eventforge",
		"mountPath: /workspace/acme-eventforge",
		"name: repo-acme-internal-tools",
	} {
		if !strings.Contains(ss, want) {
			t.Errorf("statefulset-pty-server.yaml missing %q", want)
		}
	}

	dep := get("deployment-mcp.yaml")
	for _, want := range []string{
		"kind: Deployment",
		"name: openova-sandbox-mcp",
		`image: "ghcr.io/openova-io/openova/sandbox-mcp:test-sha"`,
		"PTY_SERVER_URL",
		"pty-server.sandbox-ceo-at-acme-com.svc.cluster.local:7681",
	} {
		if !strings.Contains(dep, want) {
			t.Errorf("deployment-mcp.yaml missing %q", want)
		}
	}

	svc := get("service-pty-server.yaml")
	for _, want := range []string{
		"kind: Service",
		"name: pty-server",
		"port: 7681",
		"targetPort: 7681",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service-pty-server.yaml missing %q", want)
		}
	}

	rt := get("httproute-pty-server.yaml")
	for _, want := range []string{
		"kind: HTTPRoute",
		`- "sandbox.omantel.omani.works"`,
		"value: /sessions/ceo-at-acme-com/",
		"name: catalyst-public",
		"namespace: catalyst-system",
		"name: pty-server",
		"port: 7681",
	} {
		if !strings.Contains(rt, want) {
			t.Errorf("httproute-pty-server.yaml missing %q", want)
		}
	}

	kust := get("kustomization.yaml")
	for _, want := range []string{
		"statefulset-pty-server.yaml",
		"service-pty-server.yaml",
		"deployment-mcp.yaml",
		"httproute-pty-server.yaml",
	} {
		if !strings.Contains(kust, want) {
			t.Errorf("kustomization.yaml missing %q", want)
		}
	}
}

// TestReconcile_Wave8NoBYOSWhenAgentMissing asserts that a Sandbox
// without claude-code in spec.agentCatalogue does NOT wire the
// ANTHROPIC_API_KEY env into the rendered StatefulSet.
func TestReconcile_Wave8NoBYOSWhenAgentMissing(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	sb.Spec.AgentCatalogue = []string{"cursor-agent"}
	r, gs := makeReconciler(t, sb)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	gs.mu.Lock()
	entry, ok := gs.files["acme/catalyst-tenant/sandbox/ceo-at-acme-com/statefulset-pty-server.yaml"]
	gs.mu.Unlock()
	if !ok {
		t.Fatalf("expected statefulset-pty-server.yaml")
	}
	body := string(entry.content)
	if strings.Contains(body, "ANTHROPIC_API_KEY") {
		t.Errorf("expected NO ANTHROPIC_API_KEY env when claude-code not in agentCatalogue")
	}
	if strings.Contains(body, "sandbox-byos-claude-code-ceo-at-acme-com") {
		t.Errorf("expected NO BYOS Secret reference when claude-code not in agentCatalogue")
	}
}
