package keycloak

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestEnsureUser_409ConflictReFinds asserts the TC-R-089 race-tolerant
// path: when createUser returns 409 (concurrent caller already
// created the user), EnsureUser re-queries by email and returns the
// existing user ID instead of surfacing 409 to the caller as a 5xx.
func TestEnsureUser_409ConflictReFinds(t *testing.T) {
	const existingID = "abc-123-already-here"
	var saTokenIssued atomic.Int32
	var createCalls atomic.Int32
	var refindCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenIssued.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"access_token":"sa-tok","token_type":"Bearer","expires_in":300}`))
		case r.URL.Path == "/admin/realms/test-realm/users" && r.Method == http.MethodGet:
			// First lookup → not found. Second lookup (after 409) → returns the row.
			n := refindCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if n == 1 {
				w.Write([]byte(`[]`))
			} else {
				w.Write([]byte(`[{"id":"` + existingID + `"}]`))
			}
		case r.URL.Path == "/admin/realms/test-realm/users" && r.Method == http.MethodPost:
			createCalls.Add(1)
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"errorMessage":"User exists with same email"}`))
		case strings.Contains(r.URL.Path, "/groups"):
			// addUserToGroup short-circuit: pretend group doesn't exist
			// so the test stays focused on the 409 retry path.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.URL, "test-realm", "catalyst-zero-server", "shh", &http.Client{Timeout: 5 * time.Second})

	id, err := c.EnsureUser(context.Background(), "race@example.test", "")
	if err != nil {
		t.Fatalf("EnsureUser err = %v; want nil (409 must be re-found)", err)
	}
	if id != existingID {
		t.Errorf("EnsureUser id = %q; want %q (must come from re-find lookup)", id, existingID)
	}
	if got := createCalls.Load(); got != 1 {
		t.Errorf("createUser calls = %d; want 1", got)
	}
	if got := refindCalls.Load(); got != 2 {
		t.Errorf("findUserByEmail calls = %d; want 2 (initial miss + post-409 re-find)", got)
	}
}

// TestEnsureUser_5xxStillBubbles asserts non-409 server errors from
// createUser still surface as errors so a real KC outage isn't
// silently swallowed by the 409-tolerant path.
func TestEnsureUser_5xxStillBubbles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"access_token":"sa-tok","token_type":"Bearer","expires_in":300}`))
		case r.URL.Path == "/admin/realms/test-realm/users" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		case r.URL.Path == "/admin/realms/test-realm/users" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"db"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.URL, "test-realm", "x", "y", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.EnsureUser(context.Background(), "x@y.z", ""); err == nil {
		t.Fatal("EnsureUser err = nil; want error on 500")
	}
}
