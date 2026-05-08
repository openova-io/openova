package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/blueprint/internal/gitea"
)

// newScheme wires the Blueprint GVR into a runtime.Scheme so the fake
// dynamic client knows how to resolve list/watch calls.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	// Register the Blueprint GVR with both List and singular kinds.
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "catalyst.openova.io", Version: "v1", Kind: "Blueprint"},
		&unstructured.Unstructured{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "catalyst.openova.io", Version: "v1", Kind: "BlueprintList"},
		&unstructured.UnstructuredList{},
	)
	return s
}

// listKindMap tells the fake dynamic client which list-kind to use for
// our cluster-scoped CR.
func listKindMap() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		BlueprintGVR: "BlueprintList",
	}
}

// makeBlueprint builds a minimal Blueprint CR fixture.
func makeBlueprint(name, version, visibility string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	u.SetGeneration(1)
	u.Object["spec"] = map[string]interface{}{
		"version":    version,
		"visibility": visibility,
		"card": map[string]interface{}{
			"title": strings.Title(strings.TrimPrefix(name, "bp-")),
		},
		"placementSchema": map[string]interface{}{
			"modes":   []interface{}{"single-region"},
			"default": "single-region",
		},
	}
	return u
}

// fakeGiteaCounter is a slim fake-Gitea handler that records the set
// of (method, repo, path) tuples for assertion. Built on the same
// idea as gitea/client_test.go's fakeGitea, but inline so this test
// file owns its mutable test state.
type fakeGiteaCounter struct {
	mu      sync.Mutex
	files   map[string][]byte // key = "repo/path"
	repos   map[string]bool
	puts    int
	deletes int
}

func newFakeGiteaCounter() *fakeGiteaCounter {
	return &fakeGiteaCounter{
		files: map[string][]byte{},
		repos: map[string]bool{},
	}
}

func (f *fakeGiteaCounter) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// /api/v1/repos/<org>/<repo>             GET probe
		// /api/v1/orgs/<org>/repos                POST create
		// /api/v1/repos/<org>/<repo>/contents/<path> GET/POST/PUT/DELETE
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/repos") && r.Method == http.MethodPost:
			parts := strings.Split(path, "/")
			org := parts[4]
			// extract name from JSON body (cheap; we don't need the
			// rest)
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			// crude name extraction: assume `"name":"<v>"` is in body
			name := extractJSONString(string(body), "name")
			f.repos[org+"/"+name] = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"` + name + `"}`))

		case strings.HasPrefix(path, "/api/v1/repos/"):
			rest := strings.TrimPrefix(path, "/api/v1/repos/")
			segs := strings.SplitN(rest, "/", 4)
			org, repo := segs[0], segs[1]
			if len(segs) == 2 {
				if !f.repos[org+"/"+repo] {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"name":"` + repo + `"}`))
				return
			}
			if len(segs) == 4 && segs[2] == "contents" {
				p := segs[3]
				key := repo + "/" + p
				switch r.Method {
				case http.MethodGet:
					content, ok := f.files[key]
					if !ok {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = w.Write([]byte(`{"path":"` + p + `","sha":"sha-` + key + `","content":"` + base64Encode(content) + `","type":"file"}`))
					return
				case http.MethodPost, http.MethodPut:
					body := make([]byte, r.ContentLength)
					_, _ = r.Body.Read(body)
					encoded := extractJSONString(string(body), "content")
					decoded, _ := base64Decode(encoded)
					f.files[key] = decoded
					f.puts++
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"path":"` + p + `","sha":"sha-` + key + `","content":"` + encoded + `","type":"file"}`))
					return
				case http.MethodDelete:
					delete(f.files, key)
					f.deletes++
					w.WriteHeader(http.StatusOK)
					return
				}
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// extractJSONString does cheap key:"value" extraction so the test stub
// doesn't need to import encoding/json (avoids JSON decoder allocation
// noise in -race).
func extractJSONString(body, key string) string {
	idx := strings.Index(body, `"`+key+`":"`)
	if idx < 0 {
		return ""
	}
	body = body[idx+len(key)+4:]
	end := strings.Index(body, `"`)
	if end < 0 {
		return ""
	}
	return body[:end]
}

func base64Encode(b []byte) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n int
		if i+2 < len(b) {
			n = int(b[i])<<16 | int(b[i+1])<<8 | int(b[i+2])
			sb.WriteByte(tbl[(n>>18)&0x3f])
			sb.WriteByte(tbl[(n>>12)&0x3f])
			sb.WriteByte(tbl[(n>>6)&0x3f])
			sb.WriteByte(tbl[n&0x3f])
		} else if i+1 < len(b) {
			n = int(b[i])<<16 | int(b[i+1])<<8
			sb.WriteByte(tbl[(n>>18)&0x3f])
			sb.WriteByte(tbl[(n>>12)&0x3f])
			sb.WriteByte(tbl[(n>>6)&0x3f])
			sb.WriteByte('=')
		} else {
			n = int(b[i]) << 16
			sb.WriteByte(tbl[(n>>18)&0x3f])
			sb.WriteByte(tbl[(n>>12)&0x3f])
			sb.WriteByte('=')
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

func base64Decode(s string) ([]byte, error) {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	rev := map[byte]int{}
	for i := 0; i < len(tbl); i++ {
		rev[tbl[i]] = i
	}
	var out []byte
	var buf, n int
	for _, c := range []byte(s) {
		if c == '=' || c == '\n' || c == '\r' {
			continue
		}
		v, ok := rev[c]
		if !ok {
			continue
		}
		buf = (buf << 6) | v
		n += 6
		if n >= 8 {
			n -= 8
			out = append(out, byte((buf>>n)&0xff))
		}
	}
	return out, nil
}

// makeReconciler wires a Reconciler against a fake dynamic client +
// httptest Gitea server.
func makeReconciler(t *testing.T, items ...*unstructured.Unstructured) (*Reconciler, *fakeGiteaCounter, *httptest.Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	objs := make([]runtime.Object, 0, len(items))
	for _, it := range items {
		objs = append(objs, it)
	}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(newScheme(), listKindMap(), objs...)
	fc := newFakeGiteaCounter()
	srv := httptest.NewServer(fc.handler())
	t.Cleanup(srv.Close)
	cli := gitea.NewClient(srv.URL, "test-token")
	cli.HTTP = srv.Client()
	r := New(Config{
		DynamicClient: dc,
		Gitea:         cli,
	})
	return r, fc, srv, dc
}

func TestReconcile_Listed_Mirrors(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-test", "1.0.0", "listed")
	r, fc, _, _ := makeReconciler(t, bp)
	if err := r.Reconcile(context.Background(), bp); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if !fc.repos["catalog/bp-test"] {
		t.Errorf("expected repo catalog/bp-test created")
	}
	if _, ok := fc.files["bp-test/blueprint.yaml"]; !ok {
		t.Errorf("expected file written; got files=%v", keys(fc.files))
	}
	if fc.puts != 1 {
		t.Errorf("expected 1 PUT, got %d", fc.puts)
	}
}

func TestReconcile_Private_DeletesFromMirror(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-private", "1.0.0", "private")
	r, fc, _, _ := makeReconciler(t, bp)
	// Pre-seed: pretend a previous listed publish put the file.
	fc.mu.Lock()
	fc.repos["catalog/bp-private"] = true
	fc.files["bp-private/blueprint.yaml"] = []byte("previous content")
	fc.mu.Unlock()

	if err := r.Reconcile(context.Background(), bp); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if _, ok := fc.files["bp-private/blueprint.yaml"]; ok {
		t.Errorf("expected file removed; still present")
	}
}

func TestReconcile_Unlisted_RemovesFromPublicMirror(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-unlisted", "1.0.0", "unlisted")
	r, fc, _, _ := makeReconciler(t, bp)
	// Pre-seed: pretend a previous listed publish put the file.
	fc.mu.Lock()
	fc.repos["catalog/bp-unlisted"] = true
	fc.files["bp-unlisted/blueprint.yaml"] = []byte("previous content")
	fc.mu.Unlock()

	if err := r.Reconcile(context.Background(), bp); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if _, ok := fc.files["bp-unlisted/blueprint.yaml"]; ok {
		t.Errorf("unlisted: expected mirror file removed")
	}
}

func TestReconcile_PendingDependency(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-with-dep", "1.0.0", "listed")
	bp.Object["spec"].(map[string]interface{})["depends"] = []interface{}{
		map[string]interface{}{"blueprint": "bp-not-yet-landed"},
	}
	r, _, _, dc := makeReconciler(t, bp)
	// Catalog snapshot is empty; reconciler should NOT error but
	// surface a Pending condition on status.conditions[].
	if err := r.Reconcile(context.Background(), bp); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	out, err := dc.Resource(BlueprintGVR).Namespace("").Get(context.Background(), "bp-with-dep", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	conds, _, _ := unstructured.NestedSlice(out.Object, "status", "conditions")
	hasPending := false
	for _, c := range conds {
		cm := c.(map[string]interface{})
		if cm["type"] == "Pending" {
			hasPending = true
		}
	}
	if !hasPending {
		t.Errorf("expected Pending condition; status.conditions=%v", conds)
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-idem", "1.0.0", "listed")
	r, fc, _, dc := makeReconciler(t, bp)
	ctx := context.Background()
	if err := r.Reconcile(ctx, bp); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Re-fetch and reconcile again — same content should NOT re-PUT.
	out, _ := dc.Resource(BlueprintGVR).Namespace("").Get(ctx, "bp-idem", metav1.GetOptions{})
	if err := r.Reconcile(ctx, out); err != nil {
		t.Fatalf("second: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.puts != 1 {
		t.Errorf("idempotent: expected 1 PUT total, got %d", fc.puts)
	}
}

func TestReconcile_ValidationFailure_NoMirror(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-bad-modes", "1.0.0", "listed")
	// Inject invalid placement mode.
	bp.Object["spec"].(map[string]interface{})["placementSchema"] = map[string]interface{}{
		"modes": []interface{}{"round-robin"},
	}
	r, fc, _, dc := makeReconciler(t, bp)
	if err := r.Reconcile(context.Background(), bp); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.puts != 0 {
		t.Errorf("validation failure: expected no mirror writes, got %d puts", fc.puts)
	}
	out, _ := dc.Resource(BlueprintGVR).Namespace("").Get(context.Background(), "bp-bad-modes", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(out.Object, "status", "phase")
	if phase != PhaseDraft {
		t.Errorf("expected phase=Draft, got %q", phase)
	}
}

func TestReconcile_NoGiteaClient_StillUpdatesStatus(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-no-gitea", "1.0.0", "listed")
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(newScheme(), listKindMap(), bp)
	r := New(Config{DynamicClient: dc})
	if err := r.Reconcile(context.Background(), bp); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	out, _ := dc.Resource(BlueprintGVR).Namespace("").Get(context.Background(), "bp-no-gitea", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(out.Object, "status", "phase")
	if phase != PhasePublished {
		t.Errorf("expected phase=Published when Gitea is nil (skip mirror), got %q", phase)
	}
}

func TestReconcile_v1alpha1_Transparent(t *testing.T) {
	t.Parallel()
	bp := makeBlueprint("bp-v1alpha1", "0.5.0", "listed")
	bp.SetAPIVersion("catalyst.openova.io/v1alpha1")
	r, fc, _, _ := makeReconciler(t, bp)
	if err := r.Reconcile(context.Background(), bp); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.puts != 1 {
		t.Errorf("v1alpha1 path: expected 1 PUT, got %d", fc.puts)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
