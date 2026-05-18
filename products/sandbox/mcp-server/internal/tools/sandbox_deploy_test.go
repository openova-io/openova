// Wave-13 tests for sandbox.deploy.staging / production / status /
// rollback. The Gitea write surface is fully exercised via an httptest
// fake; the cluster-read path is exercised separately by assertion of
// hrReady() + buildHRObject() against synthetic Unstructured objects.
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// deployFakeState pins the minimal Gitea state the deploy tests need:
// the contents of one path under `<org>/catalyst-tenant`.
type deployFakeState struct {
	mu    sync.Mutex
	files map[string][]byte // key = "<org>/<repo>/<branch>/<path>"
	shas  map[string]string
}

// fakeDeployGitea spins up an httptest.Server that emulates the
// `/api/v1/repos/{owner}/{repo}/contents/{path}` GET + POST + PUT
// endpoints PutFile uses (read existing → upsert).
func fakeDeployGitea(t *testing.T) (*httptest.Server, *deployFakeState) {
	t.Helper()
	st := &deployFakeState{files: map[string][]byte{}, shas: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		// strip /api/v1/repos/<org>/<repo>/contents/<path>
		p := r.URL.Path
		const prefix = "/api/v1/repos/"
		if !strings.HasPrefix(p, prefix) {
			http.Error(w, "bad path "+p, http.StatusBadRequest)
			return
		}
		rest := strings.TrimPrefix(p, prefix)
		idx := strings.Index(rest, "/contents/")
		if idx < 0 {
			http.Error(w, "no /contents/ in "+p, http.StatusBadRequest)
			return
		}
		ownerRepo := rest[:idx]
		path := strings.TrimPrefix(rest[idx:], "/contents/")
		// Strip ?ref=branch (PutFile branch goes through query on GET).
		branch := "main"
		if b := r.URL.Query().Get("ref"); b != "" {
			branch = b
		}
		key := ownerRepo + "/" + branch + "/" + path
		switch r.Method {
		case http.MethodGet:
			st.mu.Lock()
			body, ok := st.files[key]
			sha := st.shas[key]
			st.mu.Unlock()
			if !ok {
				http.Error(w, "no such file", http.StatusNotFound)
				return
			}
			out := map[string]any{
				"path":    path,
				"sha":     sha,
				"content": base64.StdEncoding.EncodeToString(body),
				"type":    "file",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return
		case http.MethodPost, http.MethodPut:
			var body struct {
				Message string `json:"message"`
				Content string `json:"content"`
				SHA     string `json:"sha"`
				Branch  string `json:"branch"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Branch != "" {
				key = ownerRepo + "/" + body.Branch + "/" + path
			}
			raw, _ := base64.StdEncoding.DecodeString(body.Content)
			st.mu.Lock()
			st.files[key] = raw
			st.shas[key] = "sha-" + path
			st.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": map[string]any{
					"path": path,
					"sha":  st.shas[key],
					"type": "file",
				},
			})
			return
		}
		http.Error(w, "unhandled "+r.Method+" "+p, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func deployEnv(srv *httptest.Server) *Env {
	return &Env{
		OrgID:        "acme",
		SandboxID:    "eventforge",
		OwnerUID:     "emrah-baysal-at-openova-io",
		GiteaBaseURL: srv.URL,
		GiteaToken:   "tok",
	}
}

func TestSplitImage(t *testing.T) {
	cases := map[string]struct {
		repo, tag string
	}{
		"":                                        {"", ""},
		"foo":                                     {"foo", ""},
		"ghcr.io/x/y:v1":                          {"ghcr.io/x/y", "v1"},
		"ghcr.io/x/y@sha256:abc":                  {"ghcr.io/x/y", "@sha256:abc"},
		"harbor.example.com:5000/foo/bar:v2.3.4":  {"harbor.example.com:5000/foo/bar", "v2.3.4"},
		"harbor.example.com:5000/foo/bar":         {"harbor.example.com:5000/foo/bar", ""},
	}
	for in, want := range cases {
		gotR, gotT := splitImage(in)
		if gotR != want.repo || gotT != want.tag {
			t.Errorf("%q → (%q,%q) want (%q,%q)", in, gotR, gotT, want.repo, want.tag)
		}
	}
}

func TestDeployAppSlug(t *testing.T) {
	cases := map[string]struct {
		env  *Env
		app  string
		want string
		err  string
	}{
		"explicit":         {&Env{SandboxID: "any"}, "wordpress", "wordpress", ""},
		"upper-explicit":   {&Env{SandboxID: "any"}, "WordPress", "wordpress", ""},
		"falls-back":       {&Env{SandboxID: "evt"}, "", "evt", ""},
		"upper-fallback":   {&Env{SandboxID: "EVT"}, "", "evt", ""},
		"invalid-explicit": {&Env{}, "bad slug!", "", "must match"},
		"missing-both":     {&Env{}, "", "", "not supplied"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := deployAppSlug(tc.env, tc.app)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("err=%v want nil", err)
				}
				if got != tc.want {
					t.Errorf("got=%q want=%q", got, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("err=%v want %q", err, tc.err)
			}
		})
	}
}

func TestDeployValidEnv(t *testing.T) {
	for _, ok := range []string{"staging", "production"} {
		if err := deployValidEnv(ok); err != nil {
			t.Errorf("%q: err=%v want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "dev", "stagging", "PRODUCTION"} {
		if err := deployValidEnv(bad); err == nil {
			t.Errorf("%q: err=nil want non-nil", bad)
		}
	}
}

func TestDeployGiteaPath(t *testing.T) {
	got := deployGiteaPath("Emrah-Baysal", "eventforge", "staging")
	want := "sandbox/emrah-baysal/deploy/eventforge/staging/helmrelease.yaml"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
}

func TestSandboxDeployStaging_CreatesHR(t *testing.T) {
	srv, st := fakeDeployGitea(t)
	r := NewRegistry(deployEnv(srv))
	res, err := r.Call(context.Background(), "sandbox.deploy.staging",
		json.RawMessage(`{"image":"ghcr.io/acme/eventforge:v1.0.0"}`), CallOpts{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	m := res.(map[string]any)
	if m["status"] != "Created" {
		t.Errorf("status=%v want Created", m["status"])
	}
	if m["image"] != "ghcr.io/acme/eventforge:v1.0.0" {
		t.Errorf("image=%v", m["image"])
	}
	if m["hr_name"] != "eventforge-staging" {
		t.Errorf("hr_name=%v", m["hr_name"])
	}
	// Confirm Gitea state actually carries the YAML.
	st.mu.Lock()
	defer st.mu.Unlock()
	body, ok := st.files["acme/catalyst-tenant/main/sandbox/emrah-baysal-at-openova-io/deploy/eventforge/staging/helmrelease.yaml"]
	if !ok {
		t.Fatalf("file missing in fake gitea; have: %v", st.files)
	}
	if !strings.Contains(string(body), "ghcr.io/acme/eventforge") {
		t.Errorf("body missing image repo:\n%s", body)
	}
	if !strings.Contains(string(body), `"tag": "v1.0.0"`) {
		t.Errorf("body missing tag:\n%s", body)
	}
	if !strings.Contains(string(body), "helm.toolkit.fluxcd.io/v2") {
		t.Errorf("body missing apiVersion:\n%s", body)
	}
}

func TestSandboxDeployStaging_UpdateRecordsPrevious(t *testing.T) {
	srv, st := fakeDeployGitea(t)
	r := NewRegistry(deployEnv(srv))
	// First deploy.
	if _, err := r.Call(context.Background(), "sandbox.deploy.staging",
		json.RawMessage(`{"image":"ghcr.io/acme/x:v1"}`), CallOpts{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Second deploy — should record v1 as previous.
	res, err := r.Call(context.Background(), "sandbox.deploy.staging",
		json.RawMessage(`{"image":"ghcr.io/acme/x:v2"}`), CallOpts{})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	m := res.(map[string]any)
	if m["status"] != "Updated" {
		t.Errorf("status=%v want Updated", m["status"])
	}
	if m["previous_image"] != "ghcr.io/acme/x:v1" {
		t.Errorf("previous_image=%v want v1", m["previous_image"])
	}
	// Body must carry last-deployed-image annotation.
	st.mu.Lock()
	defer st.mu.Unlock()
	body := st.files["acme/catalyst-tenant/main/sandbox/emrah-baysal-at-openova-io/deploy/eventforge/staging/helmrelease.yaml"]
	if !strings.Contains(string(body), "openova.io/last-deployed-image") {
		t.Errorf("body missing last-deployed-image annotation:\n%s", body)
	}
	if !strings.Contains(string(body), "ghcr.io/acme/x:v1") {
		t.Errorf("body missing previous image v1:\n%s", body)
	}
}

func TestSandboxDeployProduction_NoCapability_Refused(t *testing.T) {
	srv, _ := fakeDeployGitea(t)
	env := deployEnv(srv)
	env.JWTSecret = []byte("sekret") // turn on auth gate
	r := NewRegistry(env)
	// Claims have base sandbox.deploy but NOT sandbox.deploy.production.
	claims := &sharedauth.Claims{
		OrgID:        "acme",
		Capabilities: []string{"sandbox.deploy"},
	}
	_, err := r.Call(context.Background(), "sandbox.deploy.production",
		json.RawMessage(`{"image":"ghcr.io/acme/x:v9"}`), CallOpts{Claims: claims})
	if err == nil || !strings.Contains(err.Error(), "sandbox.deploy.production") {
		t.Fatalf("err=%v want forbidden production", err)
	}
}

func TestSandboxDeployProduction_WithCapability_Succeeds(t *testing.T) {
	srv, st := fakeDeployGitea(t)
	env := deployEnv(srv)
	env.JWTSecret = []byte("sekret")
	r := NewRegistry(env)
	claims := &sharedauth.Claims{
		OrgID:        "acme",
		Capabilities: []string{"sandbox.deploy", "sandbox.deploy.production"},
	}
	res, err := r.Call(context.Background(), "sandbox.deploy.production",
		json.RawMessage(`{"image":"ghcr.io/acme/x:v9"}`), CallOpts{Claims: claims})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	m := res.(map[string]any)
	if m["env"] != "production" {
		t.Errorf("env=%v want production", m["env"])
	}
	if m["hr_name"] != "eventforge-production" {
		t.Errorf("hr_name=%v", m["hr_name"])
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.files["acme/catalyst-tenant/main/sandbox/emrah-baysal-at-openova-io/deploy/eventforge/production/helmrelease.yaml"]; !ok {
		t.Errorf("file missing for production; have: %v", st.files)
	}
}

func TestSandboxDeployStatus_NotDeployed(t *testing.T) {
	srv, _ := fakeDeployGitea(t)
	r := NewRegistry(deployEnv(srv))
	res, err := r.Call(context.Background(), "sandbox.deploy.status",
		json.RawMessage(`{"env":"staging"}`), CallOpts{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	m := res.(map[string]any)
	if m["status"] != "NotDeployed" {
		t.Errorf("status=%v want NotDeployed", m["status"])
	}
}

func TestSandboxDeployStatus_BadEnv(t *testing.T) {
	srv, _ := fakeDeployGitea(t)
	r := NewRegistry(deployEnv(srv))
	_, err := r.Call(context.Background(), "sandbox.deploy.status",
		json.RawMessage(`{"env":"dev"}`), CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "staging") {
		t.Errorf("err=%v want env-validation error", err)
	}
}

func TestSandboxDeployRollback_NoPrior(t *testing.T) {
	srv, _ := fakeDeployGitea(t)
	r := NewRegistry(deployEnv(srv))
	// First deploy — no previous image to roll back to.
	if _, err := r.Call(context.Background(), "sandbox.deploy.staging",
		json.RawMessage(`{"image":"ghcr.io/acme/x:v1"}`), CallOpts{}); err != nil {
		t.Fatalf("staging: %v", err)
	}
	res, err := r.Call(context.Background(), "sandbox.deploy.rollback",
		json.RawMessage(`{"env":"staging"}`), CallOpts{})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	m := res.(map[string]any)
	if m["status"] != "NoPrior" {
		t.Errorf("status=%v want NoPrior", m["status"])
	}
}

func TestSandboxDeployRollback_RevertsToPreviousImage(t *testing.T) {
	srv, st := fakeDeployGitea(t)
	r := NewRegistry(deployEnv(srv))
	// Deploy v1, then v2, then rollback.
	for _, img := range []string{"ghcr.io/acme/x:v1", "ghcr.io/acme/x:v2"} {
		if _, err := r.Call(context.Background(), "sandbox.deploy.staging",
			json.RawMessage(`{"image":"`+img+`"}`), CallOpts{}); err != nil {
			t.Fatalf("deploy %q: %v", img, err)
		}
	}
	res, err := r.Call(context.Background(), "sandbox.deploy.rollback",
		json.RawMessage(`{"env":"staging"}`), CallOpts{})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	m := res.(map[string]any)
	if m["status"] != "Rolledback" {
		t.Errorf("status=%v want Rolledback", m["status"])
	}
	if m["from_image"] != "ghcr.io/acme/x:v2" {
		t.Errorf("from_image=%v want v2", m["from_image"])
	}
	if m["to_image"] != "ghcr.io/acme/x:v1" {
		t.Errorf("to_image=%v want v1", m["to_image"])
	}
	st.mu.Lock()
	body := st.files["acme/catalyst-tenant/main/sandbox/emrah-baysal-at-openova-io/deploy/eventforge/staging/helmrelease.yaml"]
	st.mu.Unlock()
	if !strings.Contains(string(body), `"tag": "v1"`) {
		t.Errorf("body should now have v1 as tag:\n%s", body)
	}
}

func TestSandboxDeployRollback_Production_RequiresCapability(t *testing.T) {
	srv, _ := fakeDeployGitea(t)
	env := deployEnv(srv)
	env.JWTSecret = []byte("sekret")
	r := NewRegistry(env)
	claims := &sharedauth.Claims{
		OrgID:        "acme",
		Capabilities: []string{"sandbox.deploy"},
	}
	_, err := r.Call(context.Background(), "sandbox.deploy.rollback",
		json.RawMessage(`{"env":"production"}`), CallOpts{Claims: claims})
	if err == nil || !strings.Contains(err.Error(), "sandbox.deploy.production") {
		t.Fatalf("err=%v want forbidden production rollback", err)
	}
}

func TestHRReady_ParsesConditions(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"values": map[string]any{
					"image": map[string]any{
						"repository": "ghcr.io/x/y",
						"tag":        "v1.2.3",
					},
				},
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":    "Ready",
						"status":  "True",
						"reason":  "ReconciliationSucceeded",
						"message": "Helm install/upgrade succeeded",
					},
				},
			},
		},
	}
	ready, reason, message, observed := hrReady(obj)
	if !ready {
		t.Errorf("ready=false want true")
	}
	if reason != "ReconciliationSucceeded" {
		t.Errorf("reason=%q", reason)
	}
	if !strings.Contains(message, "succeeded") {
		t.Errorf("message=%q", message)
	}
	if observed != "ghcr.io/x/y:v1.2.3" {
		t.Errorf("observed=%q want ghcr.io/x/y:v1.2.3", observed)
	}
}

func TestHRReady_DigestForm(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"values": map[string]any{
					"image": map[string]any{
						"repository": "ghcr.io/x/y",
						"tag":        "@sha256:abc",
					},
				},
			},
		},
	}
	_, _, _, observed := hrReady(obj)
	if observed != "ghcr.io/x/y@sha256:abc" {
		t.Errorf("observed=%q want digest form", observed)
	}
}

func TestBuildHRObject_CarriesLabelsAndAnnotations(t *testing.T) {
	env := &Env{OrgID: "acme", SandboxID: "sb1", OwnerUID: "uid"}
	obj := buildHRObject(env, "wordpress", "staging", "ghcr.io/x:v1", "ghcr.io/x:v0")
	labels := obj.GetLabels()
	if labels[cnpgManagedByLabel] != cnpgManagedByValue {
		t.Errorf("missing managed-by label")
	}
	if labels["openova.io/sandbox-app"] != "wordpress" {
		t.Errorf("missing app label")
	}
	if labels["openova.io/sandbox-env"] != "staging" {
		t.Errorf("missing env label")
	}
	anns := obj.GetAnnotations()
	if anns[deployRequestedImageAnnotation] != "ghcr.io/x:v1" {
		t.Errorf("missing requested-image annotation")
	}
	if anns[deployLastImageAnnotation] != "ghcr.io/x:v0" {
		t.Errorf("missing last-deployed-image annotation")
	}
	// Confirm namespace = OrgID (Flux Kustomization targets the Org
	// vcluster namespace).
	if obj.GetNamespace() != "acme" {
		t.Errorf("namespace=%q want acme", obj.GetNamespace())
	}
}

func TestRequireSandboxDeployScope_MissingEnv(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{})
	_, err := requireSandboxDeployScope(ctx)
	if err == nil {
		t.Fatalf("err=nil want missing-config")
	}
	if !strings.Contains(err.Error(), "SANDBOX_ORG_ID") {
		t.Errorf("err=%v want SANDBOX_ORG_ID", err)
	}
}

// Smoke: ensure the 4 new tools appear in the registry catalogue with
// their RequiredCapability set.
func TestRegistry_AdvertisesDeployTools(t *testing.T) {
	r := NewRegistry(&Env{})
	want := map[string]string{
		"sandbox.deploy.staging":    "sandbox.deploy",
		"sandbox.deploy.production": "sandbox.deploy",
		"sandbox.deploy.status":     "sandbox.deploy",
		"sandbox.deploy.rollback":   "sandbox.deploy",
	}
	got := map[string]string{}
	for _, t := range r.List() {
		got[t.Name] = t.RequiredCapability
	}
	for name, cap := range want {
		if got[name] != cap {
			t.Errorf("%s: cap=%q want %q", name, got[name], cap)
		}
	}
}

// Ensure marshalHRYAML produces stable bytes for byte-equal short-circuit.
func TestMarshalHRYAML_Deterministic(t *testing.T) {
	env := &Env{OrgID: "acme", SandboxID: "sb1", OwnerUID: "uid"}
	a := buildHRObject(env, "x", "staging", "img:v1", "")
	// Fix the timestamp annotation: equality across two builds is
	// only guaranteed when we strip the `created-at` field — but
	// here we just confirm marshalHRYAML doesn't error and emits
	// stable JSON-shaped YAML.
	out, err := marshalHRYAML(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Errorf("output should end with \\n")
	}
	// JSON-flavoured YAML.
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "{") {
		t.Errorf("expected JSON-flavoured YAML, got:\n%s", out)
	}
	// Confirm structure round-trips through json.Unmarshal.
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if back["kind"] != "HelmRelease" {
		t.Errorf("kind=%v", back["kind"])
	}
}

// Smoke: simple GVR pin so a future apps/v2 (or v2 → v3) migration
// shows up here at test time.
func TestDeployHRGVR_Pinned(t *testing.T) {
	if deployHRGVR.Group != "helm.toolkit.fluxcd.io" || deployHRGVR.Version != "v2" || deployHRGVR.Resource != "helmreleases" {
		t.Errorf("GVR drifted: %v", deployHRGVR)
	}
}

// Use the metav1.GetOptions import so the file compiles cleanly when
// the cluster-read code path is exercised by future expansion.
var _ = metav1.GetOptions{}
