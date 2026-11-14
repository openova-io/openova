// sandbox_controller_test.go — Wave 1 + Wave 8 happy-path + drift +
// idempotency coverage for the sandbox reconciler.

package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/newapi"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stubNewAPI is an in-process newapi.Client used by the reconciler
// tests. Captures every MintRequest + replies with the configured
// MintResponse / error.
type stubNewAPI struct {
	mu        sync.Mutex
	calls     []newapi.MintRequest
	resp      newapi.MintResponse
	err       error
	mintError func(newapi.MintRequest) (*newapi.MintResponse, error)
}

func (s *stubNewAPI) MintSandboxToken(_ context.Context, req newapi.MintRequest) (*newapi.MintResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if s.mintError != nil {
		return s.mintError(req)
	}
	if s.err != nil {
		return nil, s.err
	}
	r := s.resp
	return &r, nil
}

func (s *stubNewAPI) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

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
		// TBD-P4 B4 — canonical SANDBOX_* env-var wiring (chart defaults).
		GiteaBaseURL:                "http://gitea-http.gitea.svc.cluster.local:3000",
		GiteaTokenSecretName:        "catalyst-gitea-token",
		GiteaTokenSecretKey:         "token",
		DomainAPIURL:                "http://domain.org-services.svc.cluster.local:8086",
		MarketplaceAPIURL:           "http://marketplace-api.marketplace.svc.cluster.local:8082",
		StorageS3Endpoint:           "http://seaweedfs.storage.svc.cluster.local:8333",
		StorageS3Region:             "us-east-1",
		StorageS3UseTLS:             "false",
		StorageS3CredsSecretName:    "sandbox-storage-s3",
		StorageS3AccessKeyKey:       "AWS_ACCESS_KEY_ID",
		StorageS3SecretKeyKey:       "AWS_SECRET_ACCESS_KEY",
		KeycloakAdminURL:            "http://keycloak.keycloak.svc.cluster.local:8080",
		KeycloakParentRealm:         "master",
		KeycloakAdminTokenSecret:    "keycloak-admin-token",
		KeycloakAdminTokenSecretKey: "token",
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

	// Wave 1 + Wave 8 + TBD-P4 B2/B3: 6 fixed + 1 kust + 2 repo PVCs
	// + 3 wave-8 runtime + 1 MCP-config ConfigMap = 13.
	// (TBD-P4 B2 #1986 removed deployment-mcp.yaml — the stdio
	// openova-sandbox-mcp binary EOF-crashed inside a Pod, so the
	// per-Sandbox MCP Deployment was deleted. The binary now lives in
	// the pty-server image at /usr/local/bin/openova-sandbox-mcp and
	// is launched as a subprocess by the agent via the mcp.json
	// ConfigMap PR #2049 added. The 3 wave-8 files left are
	// pty-server StatefulSet + Service + HTTPRoute; the +1 is
	// configmap-mcp-config.yaml.)
	expectedFiles := 6 + 1 + 2 + 3 + 1
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
// (pty-server StatefulSet, Service, HTTPRoute) carry the right
// identity + env wiring + BYOS branching + hostname derivation. Post
// TBD-P4 B2 (2026-05-20) the MCP Deployment was removed and the
// canonical SANDBOX_* env block was relocated onto the pty-server
// StatefulSet (the MCP binary now runs as a subprocess of the agent
// and inherits env via os.Environ()).
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
		// TBD-P4 B3 (#1986) — MCP config ConfigMap volume + mounts at
		// every canonical agent-config path so claude-code, qwen-code,
		// and cursor-agent all auto-discover openova-sandbox-mcp without
		// any user-typed config. ASSERTING ALL four mount paths so any
		// future renderer change that drops one is caught at test time.
		"name: mcp-config",
		"mountPath: /workspace/.mcp.json",
		"mountPath: /home/node/.claude.json",
		"mountPath: /home/node/.qwen/settings.json",
		"mountPath: /workspace/.cursor/mcp.json",
		"subPath: mcp.json",
		"name: sandbox-mcp-config",
		// TBD-P4 B2 (2026-05-20) — canonical SANDBOX_* env block was
		// relocated FROM the deleted per-Sandbox MCP Deployment ONTO
		// the pty-server StatefulSet. The openova-sandbox-mcp binary
		// (a stdio JSON-RPC server) now runs as a subprocess of the
		// agent (PR #2049 wired the mcp.json ConfigMap pointing at
		// /usr/local/bin/openova-sandbox-mcp; PR #1988 bundled the
		// agent CLIs; THIS PR bundles the MCP binary in the pty-server
		// image). The agent inherits env via os.Environ()
		// (session/session.go:92) and the MCP child inherits from the
		// agent — so every var on the pty-server reaches the MCP
		// subprocess unchanged.
		"name: SANDBOX_ORG_ID",
		"name: SANDBOX_SOVEREIGN_FQDN",
		"name: SANDBOX_ID",
		"name: SANDBOX_NAMESPACE",
		"name: SANDBOX_TENANT_ID",
		"name: SANDBOX_GITEA_BASE_URL",
		"name: SANDBOX_GITEA_TOKEN",
		"name: SANDBOX_DOMAIN_API_URL",
		"name: SANDBOX_MARKETPLACE_API_URL",
		"name: SANDBOX_STORAGE_S3_ENDPOINT",
		"name: SANDBOX_STORAGE_S3_REGION",
		"name: SANDBOX_STORAGE_S3_USE_TLS",
		"name: SANDBOX_STORAGE_S3_ACCESS_KEY",
		"name: SANDBOX_STORAGE_S3_SECRET_KEY",
		"name: KEYCLOAK_ADMIN_URL",
		"name: KEYCLOAK_PARENT_REALM",
		"name: KEYCLOAK_ADMIN_TOKEN",
		"name: SANDBOX_TOKEN",
		"name: SANDBOX_JWT_SECRET",
		"name: SANDBOX_REPOS",
		`name: "newapi-bp-newapi-token-signing-key"`,
		`key: "SIGNING_KEY"`,
		// SANDBOX_REPOS MUST be the comma-joined sb.Spec.Repos[].
		// giteaRepo list (sampleSandbox has acme/eventforge +
		// acme/internal-tools; renderer sorts stable).
		`value: "acme/eventforge,acme/internal-tools"`,
		// Values plumbed from the controller's chart-level env.
		"http://gitea-http.gitea.svc.cluster.local:3000",
		"http://domain.org-services.svc.cluster.local:8086",
		"http://seaweedfs.storage.svc.cluster.local:8333",
		"http://keycloak.keycloak.svc.cluster.local:8080",
		`name: "catalyst-gitea-token"`,
		`name: "sandbox-storage-s3"`,
		`name: "keycloak-admin-token"`,
	} {
		if !strings.Contains(ss, want) {
			t.Errorf("statefulset-pty-server.yaml missing %q", want)
		}
	}

	// TBD-P4 B3 (#1986) — the MCP config ConfigMap MUST be rendered as
	// a sibling file under the Gitea prefix. The pty-server StatefulSet
	// references it by name (`sandbox-mcp-config`) via a configMap
	// volume source; missing this ConfigMap = pty-server Pod stays in
	// ContainerCreating with FailedMount.
	cm := get("configmap-mcp-config.yaml")
	for _, want := range []string{
		"kind: ConfigMap",
		"name: sandbox-mcp-config",
		"namespace: sandbox-ceo-at-acme-com",
		"openova.io/sandbox: emrah",
		`openova.io/sandbox-mcp-config-version: "v1"`,
		"mcp.json: |",
		`"mcpServers"`,
		`"openova-sandbox-mcp"`,
		`"command": "/usr/local/bin/openova-sandbox-mcp"`,
		`"args": []`,
		`"env": {}`,
	} {
		if !strings.Contains(cm, want) {
			t.Errorf("configmap-mcp-config.yaml missing %q", want)
		}
	}

	// TBD-P4 B2 (2026-05-20) — assert the per-Sandbox MCP Deployment
	// MUST NOT render. Running the stdio binary as a Pod EOF-crashed
	// the openova-sandbox-mcp binary with zero operator-visible signal
	// for >2 weeks. The canonical pattern is subprocess-launched via
	// the agent + mcp.json (the binary lives in the pty-server image
	// at /usr/local/bin/openova-sandbox-mcp per the pty-server
	// Dockerfile's multi-stage copy).
	gs.mu.Lock()
	for path := range gs.files {
		if strings.HasSuffix(path, "/deployment-mcp.yaml") {
			t.Errorf("MCP Deployment MUST NOT render — path %q present "+
				"(TBD-P4 B2: stdio binary cannot run as a Pod, must be "+
				"launched as a subprocess by the agent)", path)
		}
	}
	gs.mu.Unlock()

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
		// Sandbox HTTPRoute now attaches to the canonical Cilium Gateway
		// (cilium-gateway/kube-system) so the wildcard *.<sov-fqdn>
		// listener serves traffic to sandbox.<sov-fqdn>. The previous
		// "catalyst-public/catalyst-system/https" parentRefs pointed at a
		// Gateway that doesn't exist on a Sovereign.
		"name: cilium-gateway",
		"namespace: kube-system",
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
		"httproute-pty-server.yaml",
		// TBD-P4 B3 (#1986) — the MCP config ConfigMap MUST be listed
		// in the kustomization so Flux applies it. Without this entry
		// the ConfigMap never lands in the cluster and the pty-server
		// Pod sits in ContainerCreating with FailedMount.
		"configmap-mcp-config.yaml",
	} {
		if !strings.Contains(kust, want) {
			t.Errorf("kustomization.yaml missing %q", want)
		}
	}
	// TBD-P4 B2 (2026-05-20) — kustomization MUST NOT reference the
	// deleted deployment-mcp.yaml manifest.
	if strings.Contains(kust, "deployment-mcp.yaml") {
		t.Errorf("kustomization.yaml MUST NOT reference deployment-mcp.yaml "+
			"(TBD-P4 B2 removed the per-Sandbox MCP Deployment)")
	}
}

// TestReconcile_DefaultAgentFromCatalogue asserts the TBD-P4 A4 wire:
// the controller projects sb.Spec.AgentCatalogue[0] into the pty-server
// StatefulSet's SANDBOX_DEFAULT_AGENT env var so lazy-spawn-on-attach
// (products/sandbox/pty-server/internal/server/routes.go: lazySpawn)
// dispatches the correct agent binary on the first WS attach.
//
// We pin qwen-code here because the CLAUDE.md §0 canonical journey
// requires qwen-code (zero Anthropic cost-leak path); a regression
// that drops the env var would silently take the canonical journey
// back to "blank xterm + 404".
func TestReconcile_DefaultAgentFromCatalogue(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	sb.Spec.AgentCatalogue = []string{"qwen-code"}
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
	if !strings.Contains(body, "name: SANDBOX_DEFAULT_AGENT") {
		t.Errorf("statefulset missing SANDBOX_DEFAULT_AGENT env var\n--- rendered ---\n%s", body)
	}
	if !strings.Contains(body, `value: "qwen-code"`) {
		t.Errorf("statefulset SANDBOX_DEFAULT_AGENT value is not %q\n--- rendered ---\n%s", "qwen-code", body)
	}
}

// TestReconcile_DefaultAgentEmptyWhenCatalogueEmpty guards the no-regression
// path: a Sandbox CR with an empty agentCatalogue must NOT emit the env
// var (preserves the historic 404-on-attach behaviour for hand-rolled
// CRs without a chosen agent).
func TestReconcile_DefaultAgentEmptyWhenCatalogueEmpty(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	sb.Spec.AgentCatalogue = nil
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
	if strings.Contains(body, "SANDBOX_DEFAULT_AGENT") {
		t.Errorf("statefulset must NOT emit SANDBOX_DEFAULT_AGENT when catalogue is empty\n--- rendered ---\n%s", body)
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

// TestReconcile_NewAPI_MintsAndRendersSecret exercises the Wave 9 mint
// path: NewAPIClient wired + no prior token annotation → the
// controller calls the bridge once, stamps both lifecycle annotations
// on the CR, and renders secret-newapi-token.yaml under the Gitea
// prefix with the expected token bytes.
func TestReconcile_NewAPI_MintsAndRendersSecret(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)

	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	exp := fixedNow.Add(7 * 24 * time.Hour)
	stub := &stubNewAPI{resp: newapi.MintResponse{Token: "jwt-fresh", ExpiresAt: exp}}
	r.NewAPIClient = stub
	r.DefaultChannels = []string{"qwen"}
	r.Now = func() time.Time { return fixedNow }

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if stub.callCount() != 1 {
		t.Errorf("mint calls: got %d want 1", stub.callCount())
	}
	gotReq := stub.calls[0]
	if gotReq.OrgID != "acme" {
		t.Errorf("mint req OrgID: got %q", gotReq.OrgID)
	}
	if gotReq.UserID != "ceo@acme.com" {
		t.Errorf("mint req UserID: got %q", gotReq.UserID)
	}
	if gotReq.SandboxID != string(sb.UID) {
		t.Errorf("mint req SandboxID: got %q want %q", gotReq.SandboxID, sb.UID)
	}
	if len(gotReq.AllowedChannels) != 1 || gotReq.AllowedChannels[0] != "qwen" {
		t.Errorf("mint req channels: got %v", gotReq.AllowedChannels)
	}

	// The rendered Secret manifest must exist + carry the token bytes
	// + expiry annotation + rotation marker (first issuance is also a
	// rotation event, so kubectl.kubernetes.io/restartedAt is present).
	secretKey := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/secret-newapi-token.yaml"
	entry, ok := gs.files[secretKey]
	if !ok {
		t.Fatalf("expected secret-newapi-token.yaml under %q; files=%v",
			secretKey, gsKeys(gs))
	}
	if !strings.Contains(string(entry.content), "LLM_GATEWAY_TOKEN: \"jwt-fresh\"") {
		t.Errorf("rendered Secret missing token bytes: %s", string(entry.content))
	}
	if !strings.Contains(string(entry.content), "openova.io/sandbox-token-expires-at: \""+exp.UTC().Format(time.RFC3339)+"\"") {
		t.Errorf("rendered Secret missing expires-at annotation: %s", string(entry.content))
	}
	if !strings.Contains(string(entry.content), "kubectl.kubernetes.io/restartedAt:") {
		t.Errorf("rendered Secret missing restartedAt annotation: %s", string(entry.content))
	}

	// The Sandbox CR must carry both lifecycle annotations.
	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get post-reconcile: %v", err)
	}
	if got.Annotations[annotationTokenExpiresAt] != exp.UTC().Format(time.RFC3339) {
		t.Errorf("CR expires-at annotation: got %q", got.Annotations[annotationTokenExpiresAt])
	}
	if got.Annotations[annotationTokenRotatedAt] != fixedNow.UTC().Format(time.RFC3339) {
		t.Errorf("CR rotated-at annotation: got %q", got.Annotations[annotationTokenRotatedAt])
	}

	// kustomization.yaml must reference the new secret.
	kustKey := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/kustomization.yaml"
	kustEntry, ok := gs.files[kustKey]
	if !ok {
		t.Fatalf("expected kustomization.yaml at %q", kustKey)
	}
	if !strings.Contains(string(kustEntry.content), "secret-newapi-token.yaml") {
		t.Errorf("kustomization.yaml missing secret-newapi-token entry: %s", string(kustEntry.content))
	}
}

// TestReconcile_NewAPI_RotationOnExpiry verifies that a token whose
// expiry sits within the rotation lead-time triggers a fresh mint +
// fresh restart marker.
func TestReconcile_NewAPI_RotationOnExpiry(t *testing.T) {
	t.Parallel()
	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	expSoon := fixedNow.Add(30 * time.Minute) // inside default 24h lead time
	sb := sampleSandbox()
	sb.Annotations = map[string]string{
		annotationTokenExpiresAt: expSoon.UTC().Format(time.RFC3339),
		annotationTokenRotatedAt: fixedNow.Add(-6 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	r, gs := makeReconciler(t, sb)

	newExp := fixedNow.Add(7 * 24 * time.Hour)
	stub := &stubNewAPI{resp: newapi.MintResponse{Token: "jwt-rotated", ExpiresAt: newExp}}
	r.NewAPIClient = stub
	r.DefaultChannels = []string{"qwen"}
	r.Now = func() time.Time { return fixedNow }

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if stub.callCount() != 1 {
		t.Errorf("expected exactly one mint call, got %d", stub.callCount())
	}
	secretKey := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/secret-newapi-token.yaml"
	entry := gs.files[secretKey]
	if !strings.Contains(string(entry.content), "LLM_GATEWAY_TOKEN: \"jwt-rotated\"") {
		t.Errorf("rotation did not write new token: %s", string(entry.content))
	}
	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Annotations[annotationTokenExpiresAt] != newExp.UTC().Format(time.RFC3339) {
		t.Errorf("rotation did not bump expires-at: got %q",
			got.Annotations[annotationTokenExpiresAt])
	}
}

// TestReconcile_NewAPI_NoMintWhenHealthy verifies the steady-state
// path: a CR with a token whose expiry is well outside the rotation
// lead-time triggers zero mint calls AND the rendered Secret carries
// the previous bytes.
func TestReconcile_NewAPI_NoMintWhenHealthy(t *testing.T) {
	t.Parallel()
	fixedNow := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	farExp := fixedNow.Add(5 * 24 * time.Hour) // outside default 24h lead
	sb := sampleSandbox()
	sb.Annotations = map[string]string{
		annotationTokenExpiresAt: farExp.UTC().Format(time.RFC3339),
		annotationTokenRotatedAt: fixedNow.Add(-2 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	r, gs := makeReconciler(t, sb)

	stub := &stubNewAPI{} // any call would explode (empty MintResponse)
	r.NewAPIClient = stub
	r.DefaultChannels = []string{"qwen"}
	r.Now = func() time.Time { return fixedNow }

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if stub.callCount() != 0 {
		t.Errorf("steady-state should not call mint, got %d", stub.callCount())
	}
	// The Secret manifest is NOT rendered because tokenValue is empty
	// when the controller decides not to mint. The previous Secret
	// content remains in Gitea untouched (we trust PutFile's byte-
	// equal guard) — for this in-memory test there was no prior file,
	// so the in-memory store simply doesn't have a secret-newapi-token
	// entry. The kustomization.yaml must therefore NOT reference it.
	kustKey := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/kustomization.yaml"
	kust := gs.files[kustKey]
	if strings.Contains(string(kust.content), "secret-newapi-token.yaml") {
		t.Errorf("kustomization should not reference secret-newapi-token when not minted")
	}
}

// TestReconcile_NewAPI_MintFailureSurfacesCondition exercises the
// failure path: the bridge returns a non-2xx → controller records a
// Failed/TokenMintFailed condition + requeues + NO manifests written.
func TestReconcile_NewAPI_MintFailureSurfacesCondition(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)
	stub := &stubNewAPI{err: errors.New("newapi: POST .../admin/tokens/sandbox: status 503: outage")}
	r.NewAPIClient = stub
	r.DefaultChannels = []string{"qwen"}
	r.Now = func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) }

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected non-zero requeue on bridge failure")
	}
	if gs.createFiles != 0 {
		t.Errorf("no Gitea writes expected on token-mint failure, got %d creates", gs.createFiles)
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
		got.Status.Conditions[0].Reason != "TokenMintFailed" ||
		got.Status.Conditions[0].Status != "False" {
		t.Errorf("expected TokenMintFailed False condition, got %+v", got.Status.Conditions)
	}
}

// TestReconcile_NewAPI_NoChannelsConfigured surfaces the misconfig
// path: operator didn't wire DefaultChannels → fail-loud rather than
// minting a token with an empty allowed_channels list (the bridge
// would 400 anyway, but the controller fails earlier with a more
// helpful Reason).
func TestReconcile_NewAPI_NoChannelsConfigured(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)
	stub := &stubNewAPI{}
	r.NewAPIClient = stub
	r.DefaultChannels = nil // misconfig
	r.Now = func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) }

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if stub.callCount() != 0 {
		t.Errorf("misconfig should not call bridge, got %d calls", stub.callCount())
	}
	if gs.createFiles != 0 {
		t.Errorf("misconfig: no gitea writes expected, got %d", gs.createFiles)
	}
	var got sandboxapi.Sandbox
	if err := r.Get(context.Background(),
		client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Reason != "NoAllowedChannels" {
		t.Errorf("expected NoAllowedChannels condition, got %+v", got.Status.Conditions)
	}
}

// TestReconcile_NewAPI_CapabilitiesFromPlan exercises the tier-bound
// capability path (PR #1671): when the CR carries spec.planId without
// an explicit spec.capabilities overlay, the controller resolves the
// plan's capability allowlist and threads it into the MintRequest.
func TestReconcile_NewAPI_CapabilitiesFromPlan(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	sb.Spec.PlanID = sandboxapi.PlanSandboxPro

	r, _ := makeReconciler(t, sb)
	stub := &stubNewAPI{resp: newapi.MintResponse{
		Token: "jwt-pro", ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}}
	r.NewAPIClient = stub
	r.DefaultChannels = []string{"qwen"}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if stub.callCount() != 1 {
		t.Fatalf("mint calls: got %d want 1", stub.callCount())
	}
	gotCaps := stub.calls[0].Capabilities
	wantSubset := []string{
		"gitea.repo.list",     // Free baseline.
		"sandbox.db.*",        // Pro extra.
		"sandbox.storage.*",   // Pro extra.
		"flux.status",         // Pro extra.
	}
	got := make(map[string]bool, len(gotCaps))
	for _, c := range gotCaps {
		got[c] = true
	}
	for _, w := range wantSubset {
		if !got[w] {
			t.Errorf("Pro plan capability %q missing from MintRequest: %v", w, gotCaps)
		}
	}
	// Pro plan MUST NOT grant Ent-only capabilities.
	for _, forbidden := range []string{
		"sandbox.deploy.production", "sandbox.stripe.*", "flux.reconcile",
	} {
		if got[forbidden] {
			t.Errorf("Pro plan unexpectedly granted Ent capability %q: %v", forbidden, gotCaps)
		}
	}
}

// TestReconcile_NewAPI_CapabilitiesSpecOverride asserts that an explicit
// spec.capabilities overlay wins over the plan default — the operator
// can tighten or widen the per-Sandbox grant by patching the CR.
func TestReconcile_NewAPI_CapabilitiesSpecOverride(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	sb.Spec.PlanID = sandboxapi.PlanSandboxEnt
	// Override: drop every Ent grant down to read-only intersect.
	sb.Spec.Capabilities = []string{"gitea.repo.list", "k8s.read.get"}

	r, _ := makeReconciler(t, sb)
	stub := &stubNewAPI{resp: newapi.MintResponse{
		Token: "jwt-override", ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}}
	r.NewAPIClient = stub
	r.DefaultChannels = []string{"qwen"}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	gotCaps := stub.calls[0].Capabilities
	if len(gotCaps) != 2 {
		t.Fatalf("override caps len: got %d (%v) want 2", len(gotCaps), gotCaps)
	}
	if gotCaps[0] != "gitea.repo.list" || gotCaps[1] != "k8s.read.get" {
		t.Errorf("override caps: got %v want [gitea.repo.list k8s.read.get]", gotCaps)
	}
}

// TBD-V22 #1986 F1 (2026-05-20) — verify the SANDBOX_RING_BUFFER_BYTES
// env var is emitted on the per-Sandbox pty-server StatefulSet ONLY when
// the controller has a non-zero RingBufferBytes (sourced from
// SANDBOX_RING_BUFFER_BYTES on the controller's own env, see
// cmd/sandbox-controller/main.go). Zero ⇒ omit (pty-server falls back
// to its own session.DefaultRingBytes). Non-zero ⇒ stamp the value as
// the env var so the pty-server's LoadDefaultRingBytesFromEnv consumes
// it at startup.
func TestReconcile_RingBufferBytes_OmittedWhenZero(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)
	// r.RingBufferBytes defaults to 0 in makeReconciler.

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	prefix := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/"
	gs.mu.Lock()
	entry, ok := gs.files[prefix+"statefulset-pty-server.yaml"]
	gs.mu.Unlock()
	if !ok {
		t.Fatalf("expected rendered statefulset-pty-server.yaml")
	}
	ss := string(entry.content)
	if strings.Contains(ss, "SANDBOX_RING_BUFFER_BYTES") {
		t.Errorf("expected NO SANDBOX_RING_BUFFER_BYTES env var when RingBufferBytes=0; got rendered output:\n%s", ss)
	}
}

func TestReconcile_RingBufferBytes_EmittedWhenNonZero(t *testing.T) {
	t.Parallel()
	sb := sampleSandbox()
	r, gs := makeReconciler(t, sb)
	// 2 MiB — distinct from the pty-server's default (1 MiB) so the
	// emitted value is unambiguously the controller's, not a noop default.
	r.RingBufferBytes = 2 << 20 // 2097152

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: sb.Name, Namespace: sb.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	prefix := "acme/catalyst-tenant/sandbox/ceo-at-acme-com/"
	gs.mu.Lock()
	entry, ok := gs.files[prefix+"statefulset-pty-server.yaml"]
	gs.mu.Unlock()
	if !ok {
		t.Fatalf("expected rendered statefulset-pty-server.yaml")
	}
	ss := string(entry.content)
	for _, want := range []string{
		"- name: SANDBOX_RING_BUFFER_BYTES",
		`value: "2097152"`,
	} {
		if !strings.Contains(ss, want) {
			t.Errorf("statefulset-pty-server.yaml missing %q", want)
		}
	}
}

func gsKeys(gs *giteaServer) []string {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	out := make([]string, 0, len(gs.files))
	for k := range gs.files {
		out = append(out, k)
	}
	return out
}
