package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChangeFiles_OneCallAllFiles — the whole batch goes out in one POST
// to /contents with author, committer and every operation; the answer's
// null entry (a deleted file) is skipped.
func TestChangeFiles_OneCallAllFiles(t *testing.T) {
	var calls int
	var got ChangeFilesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/acme/sovereign-regions/contents" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "token abc" {
			t.Errorf("Authorization header: %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"files":[{"path":"intent/a.yaml","sha":"s1"},{"path":"intent/b.yaml","sha":"s2"},null],"commit":{"sha":"c1"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "abc")

	req := ChangeFilesRequest{
		Branch:    "main",
		Message:   "m",
		Author:    CommitIdentity{Name: "Ann", Email: "ann@example.org"},
		Committer: CommitIdentity{Name: "bot", Email: "bot@robots.example.org"},
		Files: []ChangeFile{
			{Operation: "create", Path: "intent/a.yaml", ContentBase64: "YQ=="},
			{Operation: "update", Path: "intent/b.yaml", ContentBase64: "Yg==", SHA: "old"},
			{Operation: "delete", Path: "intent/c.yaml", SHA: "old2"},
		},
	}
	out, err := c.ChangeFiles(context.Background(), "acme", "sovereign-regions", req)
	if err != nil {
		t.Fatalf("ChangeFiles: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if got.Branch != "main" || got.Message != "m" || got.Author != req.Author || got.Committer != req.Committer || len(got.Files) != 3 || got.Files[2] != req.Files[2] {
		t.Errorf("request body = %+v", got)
	}
	if out.CommitSHA != "c1" || len(out.Files) != 2 || out.Files[1] != (ChangedFile{Path: "intent/b.yaml", SHA: "s2"}) {
		t.Errorf("response = %+v", out)
	}
}

// TestChangeFiles_ErrorIsHTTPError — a refused batch comes back as
// *HTTPError with the status, so a caller can tell 422 from 5xx.
func TestChangeFiles_ErrorIsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"sha does not match [given: x, expected: y]"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "abc")
	_, err := c.ChangeFiles(context.Background(), "o", "r", ChangeFilesRequest{Files: []ChangeFile{{Operation: "update", Path: "p", SHA: "x"}}})
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnprocessableEntity {
		t.Fatalf("want *HTTPError 422, got %v", err)
	}
	if _, err := c.ChangeFiles(context.Background(), "o", "r", ChangeFilesRequest{}); err == nil {
		t.Fatal("an empty file list must be refused before any call")
	}
}

// TestBranchHead — the head commit id is read from the branch; a missing
// branch is a 404 that IsNotFound recognises.
func TestBranchHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/o/r/branches/main" {
			_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"h1"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"branch not found"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "abc")
	head, err := c.BranchHead(context.Background(), "o", "r", "main")
	if err != nil || head != "h1" {
		t.Fatalf("BranchHead = %q, %v", head, err)
	}
	if _, err := c.BranchHead(context.Background(), "o", "r", "gone"); !IsNotFound(err) {
		t.Fatalf("missing branch: want a not-found error, got %v", err)
	}
	if _, err := c.BranchHead(context.Background(), "o", "r", ""); err == nil {
		t.Fatal("an empty branch must be refused")
	}
}
