// Wave-11 tests for gitea.pr.create + gitea.pr.merge + gitea.issue.*.
//
// The fakeGiteaServer helper in gitea_test.go covers ONLY the read
// surface (GET /repos/orgs/pulls), so this file adds a focused
// httptest fake for the write endpoints — POST /pulls,
// POST /pulls/{n}/merge, POST /issues, POST /issues/{n}/comments,
// GET /issues/{n}. Per-feature isolation keeps the assertion radius
// small and avoids regressing the existing read coverage.
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type writeFakeState struct {
	mu sync.Mutex
	// prs is keyed by "owner/repo/number" → the PR JSON.
	prs map[string]map[string]any
	// merged is keyed by "owner/repo/number" → merge style.
	merged map[string]string
	// issues is keyed by "owner/repo/number" → the Issue JSON.
	issues   map[string]map[string]any
	comments map[string]int
}

func fakeWriteServer(t *testing.T) (*httptest.Server, *writeFakeState) {
	t.Helper()
	st := &writeFakeState{
		prs:      map[string]map[string]any{},
		merged:   map[string]string{},
		issues:   map[string]map[string]any{},
		comments: map[string]int{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		switch {
		// POST /api/v1/repos/{owner}/{repo}/pulls — create PR
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls"):
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/pulls")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			st.mu.Lock()
			n := len(st.prs) + 1
			pr := map[string]any{
				"id":       n + 1000,
				"number":   n,
				"state":    "open",
				"title":    body["title"],
				"html_url": "http://gitea/pr/" + strconv.Itoa(n),
			}
			st.prs[parts[0]+"/"+parts[1]+"/"+strconv.Itoa(n)] = pr
			st.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(pr)
			return
		// POST /api/v1/repos/{owner}/{repo}/pulls/{n}/merge
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/merge"):
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/merge")
			parts := strings.Split(rest, "/")
			if len(parts) != 4 || parts[2] != "pulls" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			st.mu.Lock()
			st.merged[parts[0]+"/"+parts[1]+"/"+parts[3]], _ = body["Do"].(string)
			st.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		// GET /api/v1/repos/{owner}/{repo}/issues/{n}
		case r.Method == http.MethodGet && strings.Contains(p, "/issues/") && !strings.HasSuffix(p, "/comments"):
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			parts := strings.Split(rest, "/")
			if len(parts) != 4 || parts[2] != "issues" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			st.mu.Lock()
			iss, ok := st.issues[parts[0]+"/"+parts[1]+"/"+parts[3]]
			st.mu.Unlock()
			if !ok {
				http.Error(w, "no issue", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(iss)
			return
		// GET /api/v1/repos/{owner}/{repo}/issues?state=...
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/issues"):
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/issues")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			prefix := parts[0] + "/" + parts[1] + "/"
			st.mu.Lock()
			out := []map[string]any{}
			for k, v := range st.issues {
				if strings.HasPrefix(k, prefix) {
					out = append(out, v)
				}
			}
			st.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return
		// POST /api/v1/repos/{owner}/{repo}/issues
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/issues"):
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/issues")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			st.mu.Lock()
			n := 0
			prefix := parts[0] + "/" + parts[1] + "/"
			for k := range st.issues {
				if strings.HasPrefix(k, prefix) {
					n++
				}
			}
			n++
			iss := map[string]any{
				"id":     n + 9000,
				"number": n,
				"state":  "open",
				"title":  body["title"],
				"body":   body["body"],
			}
			st.issues[prefix+strconv.Itoa(n)] = iss
			st.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(iss)
			return
		// POST /api/v1/repos/{owner}/{repo}/issues/{n}/comments
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/comments"):
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/comments")
			parts := strings.Split(rest, "/")
			if len(parts) != 4 || parts[2] != "issues" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			st.mu.Lock()
			st.comments[parts[0]+"/"+parts[1]+"/"+parts[3]]++
			st.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "body": body["body"]})
			return
		}
		http.Error(w, "unhandled "+r.Method+" "+p, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestGiteaPRCreate_HappyPath(t *testing.T) {
	srv, st := fakeWriteServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)
	args := json.RawMessage(`{"repo":"eventforge","head":"feat/x","base":"main","title":"add x","body":"why"}`)
	res, err := r.Call(context.Background(), "gitea.pr.create", args, CallOpts{})
	if err != nil {
		t.Fatalf("pr.create: %v", err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"add x"`) {
		t.Errorf("response missing title: %s", b)
	}
	if len(st.prs) != 1 {
		t.Errorf("server prs = %d, want 1", len(st.prs))
	}
}

func TestGiteaPRCreate_MissingArgs(t *testing.T) {
	env := &Env{OrgID: "acme", GiteaBaseURL: "http://x", GiteaToken: "tok"}
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "gitea.pr.create", json.RawMessage(`{"repo":"r","head":"h","base":"main"}`), CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Errorf("err = %v, want title-missing", err)
	}
}

func TestGiteaPRMerge_DefaultStyle(t *testing.T) {
	srv, st := fakeWriteServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)
	res, err := r.Call(context.Background(), "gitea.pr.merge", json.RawMessage(`{"repo":"eventforge","number":42}`), CallOpts{})
	if err != nil {
		t.Fatalf("pr.merge: %v", err)
	}
	if m := res.(map[string]any); m["merged"] != true {
		t.Errorf("merged=%v", m["merged"])
	}
	if got := st.merged["acme/eventforge/42"]; got != "merge" {
		t.Errorf("style=%q want merge", got)
	}
}

func TestGiteaPRMerge_ExplicitSquash(t *testing.T) {
	srv, st := fakeWriteServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "gitea.pr.merge",
		json.RawMessage(`{"repo":"eventforge","number":7,"style":"squash"}`), CallOpts{})
	if err != nil {
		t.Fatalf("pr.merge: %v", err)
	}
	if got := st.merged["acme/eventforge/7"]; got != "squash" {
		t.Errorf("style=%q want squash", got)
	}
}

func TestGiteaIssueCreate_HappyPath(t *testing.T) {
	srv, st := fakeWriteServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)
	res, err := r.Call(context.Background(), "gitea.issue.create",
		json.RawMessage(`{"repo":"eventforge","title":"hello","body":"world"}`), CallOpts{})
	if err != nil {
		t.Fatalf("issue.create: %v", err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"hello"`) {
		t.Errorf("response missing title: %s", b)
	}
	if len(st.issues) != 1 {
		t.Errorf("server issues = %d, want 1", len(st.issues))
	}
}

func TestGiteaIssueList_AfterCreate(t *testing.T) {
	srv, _ := fakeWriteServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)
	for _, ti := range []string{"a", "b", "c"} {
		_, err := r.Call(context.Background(), "gitea.issue.create",
			json.RawMessage(`{"repo":"eventforge","title":"`+ti+`"}`), CallOpts{})
		if err != nil {
			t.Fatalf("create %q: %v", ti, err)
		}
	}
	res, err := r.Call(context.Background(), "gitea.issue.list",
		json.RawMessage(`{"repo":"eventforge"}`), CallOpts{})
	if err != nil {
		t.Fatalf("issue.list: %v", err)
	}
	m := res.(map[string]any)
	if m["count"].(int) != 3 {
		t.Errorf("count=%v want 3", m["count"])
	}
}

func TestGiteaIssueList_InvalidState(t *testing.T) {
	env := &Env{OrgID: "acme", GiteaBaseURL: "http://x", GiteaToken: "tok"}
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "gitea.issue.list",
		json.RawMessage(`{"repo":"x","state":"wat"}`), CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "invalid state") {
		t.Errorf("err = %v, want invalid state", err)
	}
}

func TestGiteaIssueGet_HappyPath(t *testing.T) {
	srv, _ := fakeWriteServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)
	// Seed: create issue first.
	_, _ = r.Call(context.Background(), "gitea.issue.create",
		json.RawMessage(`{"repo":"eventforge","title":"first"}`), CallOpts{})

	res, err := r.Call(context.Background(), "gitea.issue.get",
		json.RawMessage(`{"repo":"eventforge","number":1}`), CallOpts{})
	if err != nil {
		t.Fatalf("issue.get: %v", err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"first"`) {
		t.Errorf("response missing title: %s", b)
	}
}

func TestGiteaIssueComment_HappyPath(t *testing.T) {
	srv, st := fakeWriteServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)
	res, err := r.Call(context.Background(), "gitea.issue.comment",
		json.RawMessage(`{"repo":"eventforge","number":42,"body":"LGTM"}`), CallOpts{})
	if err != nil {
		t.Fatalf("issue.comment: %v", err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"LGTM"`) {
		t.Errorf("response missing body: %s", b)
	}
	if st.comments["acme/eventforge/42"] != 1 {
		t.Errorf("comments=%v want 1", st.comments["acme/eventforge/42"])
	}
}

func TestGiteaIssueComment_MissingArgs(t *testing.T) {
	env := &Env{OrgID: "acme", GiteaBaseURL: "http://x", GiteaToken: "tok"}
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "gitea.issue.comment",
		json.RawMessage(`{"repo":"r","number":1}`), CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("err = %v, want body-missing", err)
	}
}

func TestK8sReadLogs_MissingPod(t *testing.T) {
	env := &Env{OrgID: "acme", SandboxNamespace: "sandbox-x"}
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "k8s.read.logs", json.RawMessage(`{}`), CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "pod") {
		t.Errorf("err = %v, want pod-required", err)
	}
}

func TestK8sReadLogs_MissingNamespace(t *testing.T) {
	// No SandboxNamespace + no namespace arg → required.
	env := &Env{OrgID: "acme"}
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "k8s.read.logs", json.RawMessage(`{"pod":"x"}`), CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Errorf("err = %v, want namespace-required", err)
	}
}
