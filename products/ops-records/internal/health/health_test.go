package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type answer struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Checks  map[string]string `json:"checks"`
}

func get(t *testing.T, h *Handler, method, path string) (int, answer) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	var a answer
	if rec.Code != http.StatusMethodNotAllowed {
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s %s: Content-Type %q", method, path, ct)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code, a
}

func TestHealthzReportsVersion(t *testing.T) {
	h := New("1.2.3", 0)
	h.Add("store", func(context.Context) error { return errors.New("down") })
	code, a := get(t, h, http.MethodGet, "/healthz")
	if code != http.StatusOK || a.Status != "ok" || a.Version != "1.2.3" {
		t.Fatalf("got %d %+v", code, a)
	}
}

func TestReadyzWithoutChecksIsNotReady(t *testing.T) {
	code, a := get(t, New("v", 0), http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable || a.Status != "degraded" {
		t.Fatalf("got %d %+v", code, a)
	}
}

func TestReadyzAllChecksPass(t *testing.T) {
	h := New("v", 0)
	h.Add("store", func(context.Context) error { return nil })
	h.Add("bus", func(context.Context) error { return nil })
	code, a := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusOK || a.Status != "ready" || a.Checks["store"] != "ok" || a.Checks["bus"] != "ok" {
		t.Fatalf("got %d %+v", code, a)
	}
}

func TestReadyzOneCheckFails(t *testing.T) {
	h := New("v", 0)
	h.Add("store", func(context.Context) error { return errors.New("connection refused") })
	h.Add("bus", func(context.Context) error { return nil })
	code, a := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable || a.Status != "degraded" {
		t.Fatalf("got %d %+v", code, a)
	}
	if a.Checks["store"] != "connection refused" || a.Checks["bus"] != "ok" {
		t.Fatalf("checks %+v", a.Checks)
	}
}

func TestReadyzSlowCheckTimesOut(t *testing.T) {
	h := New("v", 20*time.Millisecond)
	release := make(chan struct{})
	defer close(release)
	h.Add("store", func(context.Context) error { <-release; return nil })
	start := time.Now()
	code, a := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable || a.Checks["store"] != context.DeadlineExceeded.Error() {
		t.Fatalf("got %d %+v", code, a)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the time limit was not applied")
	}
}

func TestAddReplacesACheck(t *testing.T) {
	h := New("v", 0)
	h.Add("store", func(context.Context) error { return errors.New("old") })
	h.Add("store", func(context.Context) error { return nil })
	if code, _ := get(t, h, http.MethodGet, "/readyz"); code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
}

func TestOnlyGetIsServed(t *testing.T) {
	h := New("v", 0)
	h.Add("store", func(context.Context) error { return nil })
	for _, p := range []string{"/healthz", "/readyz"} {
		if code, _ := get(t, h, http.MethodPost, p); code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s: %d", p, code)
		}
	}
}
