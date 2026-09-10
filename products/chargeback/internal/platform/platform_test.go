package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// recorder is a stand-in sovereign-admin API that records every request.
type recorder struct {
	mu   sync.Mutex
	got  []recorded
	code int
}

type recorded struct {
	Path   string
	Bearer string
	Reason string
}

func (r *recorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.got = append(r.got, recorded{Path: req.URL.Path, Bearer: req.Header.Get("Authorization"), Reason: body.Reason})
		code := r.code
		r.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"slug":"acme"}`))
	})
}

func (r *recorder) calls() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded{}, r.got...)
}

// TestHTTPReadsTheTokenFileOnEveryCall — the chart projects a ServiceAccount
// token that the kubelet rotates; a bearer read once at start-up would begin
// failing an hour later. Write the file, call, rewrite it, call again: the
// two bearers differ, and both calls hit the INTERNAL route.
func TestHTTPReadsTheTokenFileOnEveryCall(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A literal is ALSO configured, to prove the file wins over it.
	c := NewHTTP(srv.URL+"/", "stale-literal", tokenFile)
	c.Client = srv.Client()

	if err := c.SuspendOrganization(context.Background(), "Acme", "45 days overdue"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := os.WriteFile(tokenFile, []byte("second-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.ResumeOrganization(context.Background(), "acme"); err != nil {
		t.Fatalf("resume: %v", err)
	}

	got := rec.calls()
	if len(got) != 2 {
		t.Fatalf("calls = %d, want 2", len(got))
	}
	if got[0].Path != "/api/v1/internal/organizations/acme/suspend" || got[1].Path != "/api/v1/internal/organizations/acme/resume" {
		t.Fatalf("paths = %q / %q — the ServiceAccount token is only accepted on the internal routes", got[0].Path, got[1].Path)
	}
	if got[0].Bearer != "Bearer first-token" || got[1].Bearer != "Bearer second-token" {
		t.Fatalf("bearers = %q / %q — the token file must be re-read on every call", got[0].Bearer, got[1].Bearer)
	}
	if got[0].Reason != "45 days overdue" {
		t.Fatalf("reason = %q", got[0].Reason)
	}
}

// TestHTTPFallsBackToTheLiteralWithoutAFile — a local run against a
// Sovereign passes PLATFORM_API_TOKEN and no file.
func TestHTTPFallsBackToTheLiteralWithoutAFile(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	c := NewHTTP(srv.URL, "literal-token", "")
	c.Client = srv.Client()
	if err := c.SuspendOrganization(context.Background(), "acme", "x"); err != nil {
		t.Fatal(err)
	}
	if got := rec.calls(); len(got) != 1 || got[0].Bearer != "Bearer literal-token" {
		t.Fatalf("calls = %+v", got)
	}
}

// TestHTTPRefusesAnUnreadableTokenFile — a configured file that is missing
// or empty is an error on the call, never a silent fall-back to the literal:
// the platform would refuse the call anyway and the audit should say why.
func TestHTTPRefusesAnUnreadableTokenFile(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	c := NewHTTP(srv.URL, "literal-token", filepath.Join(t.TempDir(), "absent"))
	c.Client = srv.Client()
	if err := c.SuspendOrganization(context.Background(), "acme", "x"); err == nil {
		t.Fatal("a missing token file was not an error")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.TokenFile = empty
	if err := c.SuspendOrganization(context.Background(), "acme", "x"); err == nil {
		t.Fatal("an empty token file was not an error")
	}
	if got := rec.calls(); len(got) != 0 {
		t.Fatalf("the platform was called without a bearer: %+v", got)
	}
}

// TestHTTPSurfacesThePlatformsRefusal — a 403 from the allow-list, a 404 for
// an unknown Organization: the status and body come back in the error so the
// enforcement audit carries them.
func TestHTTPSurfacesThePlatformsRefusal(t *testing.T) {
	rec := &recorder{code: http.StatusForbidden}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	c := NewHTTP(srv.URL, "t", "")
	c.Client = srv.Client()
	err := c.SuspendOrganization(context.Background(), "acme", "x")
	if err == nil {
		t.Fatal("a 403 was not an error")
	}
	if want := "answered 403"; !contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to carry %q", err, want)
	}
	if err := c.SuspendOrganization(context.Background(), "   ", "x"); err == nil {
		t.Fatal("an empty slug was not refused before the call")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
