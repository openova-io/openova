package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestPutFileIfMatch_CompareAndSwap — the D8 lease primitive: a create of an
// existing file and an update with a stale SHA both lose with ErrConflict;
// the matching SHA wins.
func TestPutFileIfMatch_CompareAndSwap(t *testing.T) {
	var mu sync.Mutex
	sha := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body commitFilePayload
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.Method {
		case http.MethodPost:
			if sha != "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"file already exists"}`))
				return
			}
		case http.MethodPut:
			if body.SHA != sha {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"sha does not match"}`))
				return
			}
		}
		sha = sha + "x"
		_ = json.NewEncoder(w).Encode(map[string]any{"content": map[string]any{"path": "p", "sha": sha}})
	}))
	defer srv.Close()
	c := New(srv.URL, "t")
	ctx := context.Background()

	f, err := c.PutFileIfMatch(ctx, "o", "r", "main", "p", []byte("a"), "m", "")
	if err != nil || f.SHA != "x" {
		t.Fatalf("create: %v %+v", err, f)
	}
	if _, err := c.PutFileIfMatch(ctx, "o", "r", "main", "p", []byte("b"), "m", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("second create must lose, got %v", err)
	}
	if _, err := c.PutFileIfMatch(ctx, "o", "r", "main", "p", []byte("b"), "m", "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update must lose, got %v", err)
	}
	if f, err := c.PutFileIfMatch(ctx, "o", "r", "main", "p", []byte("b"), "m", "x"); err != nil || f.SHA != "xx" {
		t.Fatalf("matching update must win: %v %+v", err, f)
	}
}
