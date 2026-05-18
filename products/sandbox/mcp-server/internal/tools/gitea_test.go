package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGiteaServer is a minimal httptest stand-in for the Gitea endpoints
// these tools touch. We don't reuse the canonical pkg/gitea fake (it
// lives in a _test.go file and isn't importable). The handlers here
// reflect the URL patterns the pkg/gitea client uses verbatim.
func fakeGiteaServer(t *testing.T) (*httptest.Server, *fakeGiteaState) {
	t.Helper()
	state := &fakeGiteaState{
		orgs:  map[string]bool{},
		repos: map[string]bool{},
		prs:   map[string]json.RawMessage{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/orgs/") && strings.HasSuffix(p, "/repos"):
			org := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/orgs/"), "/repos")
			if !state.orgs[org] {
				http.Error(w, "no org", http.StatusNotFound)
				return
			}
			out := []map[string]any{}
			for k := range state.repos {
				if strings.HasPrefix(k, org+"/") {
					out = append(out, map[string]any{"name": strings.TrimPrefix(k, org+"/"), "full_name": k})
				}
			}
			writeJSONResp(w, out)
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/repos/") && !strings.Contains(p, "/pulls"):
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			if !state.repos[rest] {
				http.Error(w, "no repo", http.StatusNotFound)
				return
			}
			writeJSONResp(w, map[string]any{"name": strings.Split(rest, "/")[1], "full_name": rest})
		case r.Method == http.MethodGet && strings.Contains(p, "/pulls/"):
			// GET /api/v1/repos/{owner}/{repo}/pulls/{number}
			parts := strings.Split(strings.TrimPrefix(p, "/api/v1/repos/"), "/")
			if len(parts) != 4 || parts[2] != "pulls" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			pr, ok := state.prs[parts[0]+"/"+parts[1]+"/"+parts[3]]
			if !ok {
				http.Error(w, "no pr", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(pr)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls"):
			// GET /api/v1/repos/{owner}/{repo}/pulls?state=...
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/pulls")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			if !state.repos[parts[0]+"/"+parts[1]] {
				http.Error(w, "no repo", http.StatusNotFound)
				return
			}
			out := []map[string]any{}
			for k, raw := range state.prs {
				if strings.HasPrefix(k, parts[0]+"/"+parts[1]+"/") {
					var pr map[string]any
					_ = json.Unmarshal(raw, &pr)
					out = append(out, pr)
				}
			}
			writeJSONResp(w, out)
		default:
			http.Error(w, "unhandled "+r.Method+" "+p, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, state
}

type fakeGiteaState struct {
	orgs  map[string]bool
	repos map[string]bool
	prs   map[string]json.RawMessage
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestGiteaRepoList_HappyPath(t *testing.T) {
	srv, state := fakeGiteaServer(t)
	state.orgs["acme"] = true
	state.repos["acme/eventforge"] = true
	state.repos["acme/internal-tools"] = true
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)

	res, err := r.Call(context.Background(), "gitea.repo.list", nil, CallOpts{})
	if err != nil {
		t.Fatalf("repo.list: %v", err)
	}
	m := res.(map[string]any)
	if m["owner"] != "acme" {
		t.Errorf("owner=%v", m["owner"])
	}
	if m["count"].(int) != 2 {
		t.Errorf("count=%v want 2", m["count"])
	}
}

func TestGiteaRepoList_CrossTenantBlocked(t *testing.T) {
	srv, state := fakeGiteaServer(t)
	state.orgs["acme"] = true
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)

	args := json.RawMessage(`{"owner":"bankdhofar"}`)
	_, err := r.Call(context.Background(), "gitea.repo.list", args, CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "cross-tenant") {
		t.Errorf("err = %v, want cross-tenant", err)
	}
}

func TestGiteaRepoGet_HappyPath(t *testing.T) {
	srv, state := fakeGiteaServer(t)
	state.repos["acme/eventforge"] = true
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)

	args := json.RawMessage(`{"repo":"eventforge"}`)
	res, err := r.Call(context.Background(), "gitea.repo.get", args, CallOpts{})
	if err != nil {
		t.Fatalf("repo.get: %v", err)
	}
	// pkg/gitea.Repo struct serialised — Name field present.
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"eventforge"`) {
		t.Errorf("response missing repo name: %s", b)
	}
}

func TestGiteaRepoGet_RepoNotFound(t *testing.T) {
	srv, _ := fakeGiteaServer(t)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)

	args := json.RawMessage(`{"repo":"ghost"}`)
	res, err := r.Call(context.Background(), "gitea.repo.get", args, CallOpts{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	m := res.(map[string]any)
	if m["status"].(int) != http.StatusNotFound {
		t.Errorf("status=%v want 404", m["status"])
	}
}

func TestGiteaPRList_HappyPath(t *testing.T) {
	srv, state := fakeGiteaServer(t)
	state.repos["acme/eventforge"] = true
	for i, st := range []string{"open", "open", "closed"} {
		state.prs[`acme/eventforge/`+itoa(i+1)] = json.RawMessage(
			`{"id":` + itoa(i+1) + `,"number":` + itoa(i+1) + `,"state":"` + st + `","title":"pr-` + itoa(i+1) + `"}`)
	}
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)

	args := json.RawMessage(`{"repo":"eventforge","state":"open"}`)
	// NOTE: our fake currently returns all PRs (no state filter); pkg/gitea
	// passes the state param but the fake doesn't filter — the assertion
	// here checks the tool wrapper, not the upstream filter logic (which
	// is tested in pkg/gitea/pulls_test.go).
	res, err := r.Call(context.Background(), "gitea.pr.list", args, CallOpts{})
	if err != nil {
		t.Fatalf("pr.list: %v", err)
	}
	m := res.(map[string]any)
	if m["repo"] != "eventforge" {
		t.Errorf("repo=%v", m["repo"])
	}
	if m["state"] != "open" {
		t.Errorf("state=%v", m["state"])
	}
	if m["count"].(int) < 1 {
		t.Errorf("count too low: %v", m["count"])
	}
}

func TestGiteaPRList_InvalidState(t *testing.T) {
	env := &Env{OrgID: "acme", GiteaBaseURL: "http://x", GiteaToken: "tok"}
	r := NewRegistry(env)
	args := json.RawMessage(`{"repo":"x","state":"wat"}`)
	_, err := r.Call(context.Background(), "gitea.pr.list", args, CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "invalid state") {
		t.Errorf("err = %v, want invalid state", err)
	}
}

func TestGiteaPRGet_HappyPath(t *testing.T) {
	srv, state := fakeGiteaServer(t)
	state.repos["acme/eventforge"] = true
	state.prs["acme/eventforge/42"] = json.RawMessage(
		`{"id":42,"number":42,"state":"open","title":"hello","html_url":"http://gitea/x"}`)
	env := &Env{OrgID: "acme", GiteaBaseURL: srv.URL, GiteaToken: "tok"}
	r := NewRegistry(env)

	args := json.RawMessage(`{"repo":"eventforge","number":42}`)
	res, err := r.Call(context.Background(), "gitea.pr.get", args, CallOpts{})
	if err != nil {
		t.Fatalf("pr.get: %v", err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"hello"`) {
		t.Errorf("response missing title: %s", b)
	}
}

func TestGiteaPRGet_MissingArgs(t *testing.T) {
	env := &Env{OrgID: "acme", GiteaBaseURL: "http://x", GiteaToken: "tok"}
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "gitea.pr.get", json.RawMessage(`{"repo":"x"}`), CallOpts{})
	if err == nil {
		t.Error("want error for missing number")
	}
	_, err = r.Call(context.Background(), "gitea.pr.get", json.RawMessage(`{"number":1}`), CallOpts{})
	if err == nil {
		t.Error("want error for missing repo")
	}
}

func TestGitea_NotConfigured(t *testing.T) {
	env := &Env{OrgID: "acme"} // no GiteaBaseURL / GiteaToken
	r := NewRegistry(env)
	_, err := r.Call(context.Background(), "gitea.repo.list", nil, CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "GITEA_BASE_URL") {
		t.Errorf("err = %v, want GITEA_BASE_URL message", err)
	}
	env.GiteaBaseURL = "http://x"
	_, err = r.Call(context.Background(), "gitea.repo.list", nil, CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "GITEA_TOKEN") {
		t.Errorf("err = %v, want GITEA_TOKEN message", err)
	}
}

// itoa is a tiny strconv.Itoa stand-in kept inline so the test file
// keeps its single-import discipline.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
