package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetRef_UsesPluralRefsPath asserts the client requests the Gitea-compatible
// plural /git/refs/heads/<branch> path, NOT the GitHub-only singular
// /git/ref/heads/<branch> path. The singular path 404s on Gitea 1.22, blocking
// customer journey step 14 ("Committing manifests to Git") and downstream Org CR
// creation. See TBD-C18c (2026-05-18).
func TestGetRef_UsesPluralRefsPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		// Gitea always returns an array for /git/refs/heads/<branch>.
		_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "owner", "repo", srv.URL)
	sha, err := c.getRef(context.Background(), "main")
	if err != nil {
		t.Fatalf("getRef returned error: %v", err)
	}
	if sha != "c3f4799deadbeef0000000000000000deadbeef" {
		t.Fatalf("getRef sha = %q, want c3f4799deadbeef0000000000000000deadbeef", sha)
	}

	wantSuffix := "/git/refs/heads/main"
	if !strings.HasSuffix(gotPath, wantSuffix) {
		t.Fatalf("request path = %q, want suffix %q (plural /git/refs/, NOT singular /git/ref/)", gotPath, wantSuffix)
	}
	// Defence-in-depth: assert we did NOT regress to the GitHub-only singular form.
	if strings.Contains(gotPath, "/git/ref/heads/") {
		t.Fatalf("request path = %q contains GitHub-only singular /git/ref/heads/ — regression of TBD-C18c", gotPath)
	}
}

// TestGetRef_AcceptsSingleObjectResponse covers GitHub's exact-ref response
// shape (object, not array). Kept for forward compatibility — even though the
// Sovereign path always hits Gitea today, the client is also used against
// upstream github.com from the contabo path.
func TestGetRef_AcceptsSingleObjectResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":{"sha":"abc1230000000000000000000000000000abc123"}}`))
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "owner", "repo", srv.URL)
	sha, err := c.getRef(context.Background(), "main")
	if err != nil {
		t.Fatalf("getRef returned error: %v", err)
	}
	if sha != "abc1230000000000000000000000000000abc123" {
		t.Fatalf("getRef sha = %q, want abc1230000000000000000000000000000abc123", sha)
	}
}

// TestGetRef_EmptyArrayReturnsError guards against an upstream returning an
// empty refs array (would otherwise silently produce sha="" and propagate into
// a malformed updateRef call).
func TestGetRef_EmptyArrayReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "owner", "repo", srv.URL)
	_, err := c.getRef(context.Background(), "main")
	if err == nil {
		t.Fatalf("getRef returned nil error on empty refs array; want error")
	}
}
