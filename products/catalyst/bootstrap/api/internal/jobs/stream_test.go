package jobs

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// streamServer serves the stream route with the given access check.
func streamServer(t *testing.T, access StreamAccess) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/jobs/stream", StreamHandler(access))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// readLine reads one line or fails the test after two seconds.
func readLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("stream closed")
		}
		return l
	case <-time.After(2 * time.Second):
		t.Fatal("no line within two seconds")
	}
	return ""
}

// openStream opens the stream of depID and returns its status and lines.
func openStream(t *testing.T, srv *httptest.Server, depID string) (int, <-chan string) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/v1/deployments/" + depID + "/jobs/stream")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return resp.StatusCode, lines
}

func TestStreamFrameAfterUpsertJob(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := streamServer(t, func(http.ResponseWriter, *http.Request, string) (*Store, bool) { return st, true })
	code, lines := openStream(t, srv, "dep-1")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	// The handler subscribes before it writes the header, so the write
	// below cannot fall between the two.
	if err := st.UpsertJob(Job{DeploymentID: "dep-1", JobName: "install-a"}); err != nil {
		t.Fatal(err)
	}
	if l := readLine(t, lines); l != "event: jobs" {
		t.Fatalf("got %q, want the event line", l)
	}
	data := readLine(t, lines)
	if !strings.HasPrefix(data, "data: ") {
		t.Fatalf("got %q, want a data line", data)
	}
	raw := strings.TrimPrefix(data, "data: ")
	if want := `{"deploymentId":"dep-1","jobId":"dep-1:install-a","executionId":"","kind":"job-upserted","seq":`; !strings.HasPrefix(raw, want) {
		t.Fatalf("frame %s, want it to start with %s", raw, want)
	}
	var f map[string]any
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("frame is not JSON: %v", err)
	}
	if f["seq"].(float64) < 1 {
		t.Fatalf("seq %v", f["seq"])
	}
}

func TestStreamPingComment(t *testing.T) {
	prev := streamPing
	streamPing = 50 * time.Millisecond
	t.Cleanup(func() { streamPing = prev })
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := streamServer(t, func(http.ResponseWriter, *http.Request, string) (*Store, bool) { return st, true })
	_, lines := openStream(t, srv, "dep-1")
	if l := readLine(t, lines); l != ": ping" {
		t.Fatalf("got %q, want the ping comment", l)
	}
}

func TestStreamAccessDeniedWritesItsResponse(t *testing.T) {
	srv := streamServer(t, func(w http.ResponseWriter, _ *http.Request, depID string) (*Store, bool) {
		if depID != "dep-x" {
			t.Errorf("access got deployment %q", depID)
		}
		w.WriteHeader(http.StatusNotFound)
		return nil, false
	})
	code, _ := openStream(t, srv, "dep-x")
	if code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", code)
	}
}
