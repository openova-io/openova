package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/retry"
)

// fast is the bound of the discovery without its waits.
func fast() *retry.Policy {
	p := retry.Default()
	p.Sleep = func(context.Context, time.Duration) error { return nil }
	return p
}

// console answers status for the first failures requests (-1: every request)
// and 200 {"tier":"owner"} after; status 0 closes the connection without an
// answer. It counts the requests.
func console(t *testing.T, failures, status int) (*Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if i := int(n.Add(1)); failures < 0 || i <= failures {
			if status == 0 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"tier":"owner"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "session")
	c.Retry = fast()
	return c, &n
}

func TestGetRetriesAClosedConnectionThenReads(t *testing.T) {
	c, n := console(t, 2, 0)
	var who struct{ Tier string }
	if _, err := c.Get(context.Background(), "/api/v1/whoami", &who); err != nil || who.Tier != "owner" || n.Load() != 3 {
		t.Fatalf("two closed connections, then an answer: err=%v tier=%q after %d requests, want owner after 3", err, who.Tier, n.Load())
	}
}

func TestGetRetriesA503ThenReads(t *testing.T) {
	c, n := console(t, 2, http.StatusServiceUnavailable)
	if _, err := c.Get(context.Background(), "/api/v1/whoami", nil); err != nil || n.Load() != 3 {
		t.Fatalf("two 503, then 200: err=%v after %d requests", err, n.Load())
	}
}

func TestGetGivesUpUnreachable(t *testing.T) {
	c, n := console(t, -1, 0)
	_, err := c.Get(context.Background(), "/api/v1/whoami", nil)
	var u *retry.Unreachable
	if !errors.As(err, &u) || n.Load() != 5 || !retry.OnlyUnreachable(err) || !strings.Contains(err.Error(), "GET /api/v1/whoami") {
		t.Fatalf("a console that never answers: err=%v after %d requests, want unreachable after 5", err, n.Load())
	}
}

// A closed port is a connection refused: unreachable, not an answer.
func TestGetOfAClosedPortIsUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	c := New("http://"+addr, "session")
	c.Retry = fast()
	if _, err := c.Get(context.Background(), "/api/v1/whoami", nil); !retry.OnlyUnreachable(err) {
		t.Fatalf("a closed port: want unreachable, got %v", err)
	}
}

// A 503 that does not go away is the server's answer: it stands, and it is not unreachable.
func TestGetOfA503ThatStaysIsTheAnswer(t *testing.T) {
	c, n := console(t, -1, http.StatusServiceUnavailable)
	_, err := c.Get(context.Background(), "/api/v1/whoami", nil)
	if err == nil || retry.OnlyUnreachable(err) || !strings.Contains(err.Error(), "HTTP 503") || n.Load() != 5 {
		t.Fatalf("a console that keeps answering 503: err=%v after %d requests, want HTTP 503 after 5", err, n.Load())
	}
}

func TestGetNeverRetriesARealAnswer(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity} {
		c, n := console(t, -1, status)
		_, err := c.Get(context.Background(), "/api/v1/whoami", nil)
		if err == nil || n.Load() != 1 || retry.OnlyUnreachable(err) {
			t.Errorf("HTTP %d: err=%v after %d requests, want the answer after one", status, err, n.Load())
		}
	}
}

// A call that may write is sent once, with or without a policy.
func TestOnlyAGetIsRetried(t *testing.T) {
	c, n := console(t, -1, http.StatusServiceUnavailable)
	res, err := c.Do(context.Background(), http.MethodPost, "/catalyst/v1/apps/instances", map[string]string{"name": "x"})
	if err != nil || res.Status != http.StatusServiceUnavailable || n.Load() != 1 {
		t.Fatalf("a POST: status=%v err=%v after %d requests, want one request", res, err, n.Load())
	}
}

// Without a policy, as in every journey and check, a GET is sent once.
func TestGetWithoutAPolicyIsSentOnce(t *testing.T) {
	c, n := console(t, -1, 0)
	c.Retry = nil
	if _, err := c.Get(context.Background(), "/api/v1/whoami", nil); err == nil || retry.OnlyUnreachable(err) || n.Load() != 1 {
		t.Fatalf("no policy: err=%v after %d requests, want the plain error after one", err, n.Load())
	}
}
