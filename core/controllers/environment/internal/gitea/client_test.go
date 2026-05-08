package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Verify GetOrg success.
func TestGetOrg_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/orgs/acme", r.URL.Path)
		assert.Equal(t, "token test-tok", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Org{Username: "acme", FullName: "ACME Corp"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/api/v1", "test-tok")
	got, err := c.GetOrg(context.Background(), "acme")
	require.NoError(t, err)
	assert.Equal(t, "acme", got.Username)
	assert.Equal(t, "ACME Corp", got.FullName)
}

// Verify GetOrg 404 maps to ErrOrgNotFound.
func TestGetOrg_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/api/v1", "tok")
	_, err := c.GetOrg(context.Background(), "ghost")
	assert.True(t, errors.Is(err, ErrOrgNotFound))
}

// Verify GetOrg 500 surfaces a wrapped error (not ErrOrgNotFound).
func TestGetOrg_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/api/v1", "tok")
	_, err := c.GetOrg(context.Background(), "acme")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrOrgNotFound))
	assert.Contains(t, err.Error(), "500")
}

// Verify the create path: when GetFile returns 404 file-only, UpsertFile POSTs.
func TestUpsertFile_CreatePath(t *testing.T) {
	gotPost := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// File doesn't exist yet — return 404 with no "repository" hint.
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"file does not exist"}`)
		case http.MethodPost:
			gotPost = true
			var body contentsBody
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "main", body.Branch)
			assert.Equal(t, "test commit", body.Message)
			decoded, err := base64.StdEncoding.DecodeString(body.Content)
			require.NoError(t, err)
			assert.Equal(t, "hello\n", string(decoded))
			assert.Empty(t, body.SHA, "create path must not include SHA")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"content":{}}`)
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/api/v1", "tok")
	committed, err := c.UpsertFile(context.Background(),
		"acme", "acme-environment", "main", "clusters/x/y.yaml",
		[]byte("hello\n"), "test commit", "bot", "bot@test")
	require.NoError(t, err)
	assert.True(t, committed)
	assert.True(t, gotPost, "must call POST when file is missing")
}

// Verify the update path: GET returns existing content, identical bytes
// short-circuit (no PUT); different bytes trigger PUT with SHA.
func TestUpsertFile_IdempotentAndUpdate(t *testing.T) {
	state := []byte("hello\n")
	const sha = "abc123"
	getCount, putCount := 0, 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(FileContent{
				Path:    "clusters/x/y.yaml",
				SHA:     sha,
				Content: base64.StdEncoding.EncodeToString(state),
			})
		case http.MethodPut:
			putCount++
			var body contentsBody
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, sha, body.SHA, "update path must include the existing blob SHA")
			decoded, err := base64.StdEncoding.DecodeString(body.Content)
			require.NoError(t, err)
			state = decoded
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"content":{}}`)
		default:
			t.Fatalf("unexpected: %s", r.Method)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/api/v1", "tok")

	// 1st call: identical content → no commit.
	committed, err := c.UpsertFile(context.Background(),
		"acme", "acme-environment", "main", "clusters/x/y.yaml",
		[]byte("hello\n"), "noop", "bot", "bot@test")
	require.NoError(t, err)
	assert.False(t, committed, "identical bytes must short-circuit")
	assert.Equal(t, 1, getCount)
	assert.Equal(t, 0, putCount, "no PUT when bytes match")

	// 2nd call: different content → PUT with SHA.
	committed, err = c.UpsertFile(context.Background(),
		"acme", "acme-environment", "main", "clusters/x/y.yaml",
		[]byte("world\n"), "drift fix", "bot", "bot@test")
	require.NoError(t, err)
	assert.True(t, committed)
	assert.Equal(t, 2, getCount)
	assert.Equal(t, 1, putCount)
	assert.Equal(t, "world\n", string(state))
}

// Verify repo-level 404 surfaces ErrRepoNotFound (distinct from
// ErrFileNotFound).
func TestUpsertFile_RepoNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"The target couldn't be found, repository missing."}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/api/v1", "tok")
	_, err := c.UpsertFile(context.Background(),
		"acme", "missing-repo", "main", "x.yaml",
		[]byte("data"), "msg", "bot", "bot@test")
	assert.True(t, errors.Is(err, ErrRepoNotFound), "expected ErrRepoNotFound, got %v", err)
}

// Verify path escaping preserves slashes (Gitea expects directory tree).
func TestPathEscape(t *testing.T) {
	assert.Equal(t, "clusters/hetzner-fsn-rtz-prod/environments/acme-prod/gitrepository.yaml",
		pathEscape("clusters/hetzner-fsn-rtz-prod/environments/acme-prod/gitrepository.yaml"))
	// Special chars in segments get escaped, slashes preserved.
	escaped := pathEscape("a b/c d/e.yaml")
	assert.True(t, strings.Contains(escaped, "/"), "slashes preserved")
	assert.True(t, strings.Contains(escaped, "%20") || strings.Contains(escaped, "+"),
		"spaces escaped within segments")
}

// Verify required-arg validation.
func TestUpsertFile_RequiresAllArgs(t *testing.T) {
	c := NewClient("http://nope/api/v1", "tok")
	_, err := c.UpsertFile(context.Background(), "acme", "r", "main", "x", []byte("data"), "", "bot", "bot@x")
	require.Error(t, err)
	_, err = c.UpsertFile(context.Background(), "acme", "r", "main", "x", []byte("data"), "msg", "", "bot@x")
	require.Error(t, err)
}

// Verify GetFile required-arg validation.
func TestGetFile_RequiresAllArgs(t *testing.T) {
	c := NewClient("http://nope/api/v1", "tok")
	_, err := c.GetFile(context.Background(), "", "r", "main", "p")
	require.Error(t, err)
}

// Verify GetOrg empty slug rejected.
func TestGetOrg_EmptySlug(t *testing.T) {
	c := NewClient("http://nope/api/v1", "tok")
	_, err := c.GetOrg(context.Background(), "")
	require.Error(t, err)
}
