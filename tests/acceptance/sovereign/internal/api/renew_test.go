package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// signIn is a console whose sessions expire: it accepts a call whose cookie
// is in valid and answers every other one HTTP 401 unauthenticated, as
// catalyst-api does for a session past its exp. It records the session and the
// body of every call.
type signIn struct {
	mu    sync.Mutex
	valid map[string]bool
	calls []string // "<method> <path> <session> <body>"
}

func (s *signIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	session := ""
	if c, err := r.Cookie("catalyst_session"); err == nil {
		session = c.Value
	}
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls = append(s.calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+session+" "+string(b)))
	ok := s.valid[session]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthenticated"}`)
		return
	}
	if r.Header.Get("Accept") == "text/event-stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\n")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, `{"state":"pending"}`)
}

func (s *signIn) expire(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.valid, session)
}

func (s *signIn) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

const (
	oldSession = "old-session-7Hq2"
	newSession = "new-session-9Kz4"
)

// newSignIn is a console that accepts oldSession, and a client that starts with
// it and renews it to newSession, which the console accepts from the start.
func newSignIn(t *testing.T) (*signIn, *Client) {
	t.Helper()
	s := &signIn{valid: map[string]bool{oldSession: true, newSession: true}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c := New(srv.URL, oldSession)
	c.Renew = &Renewal{Mint: func(context.Context) (string, error) { return newSession, nil }}
	return s, c
}

// The session expires in a long run: the next call is refused, the session is
// renewed once and the call is sent once more with the same body; later
// calls, and a copy of the client, send the new session.
func TestAnExpiredSessionIsRenewedOnceAndTheCallSentAgain(t *testing.T) {
	s, c := newSignIn(t)
	ctx := context.Background()
	if res, err := c.Do(ctx, http.MethodPost, "/api/v1/organizations", map[string]string{"subdomain": "iso-a"}); err != nil || res.Status != http.StatusAccepted {
		t.Fatalf("before the expiry: %v %v", res, err)
	}
	s.expire(oldSession)
	res, err := c.Do(ctx, http.MethodPost, "/api/v1/organizations", map[string]string{"subdomain": "iso-b"})
	if err != nil || res.Status != http.StatusAccepted {
		t.Fatalf("after the expiry: want 202 after one renewal, got %v %v", res, err)
	}
	copied := *c // as BSS-G01 copies the run's client
	if _, err := copied.Do(ctx, http.MethodDelete, "/api/v1/org/billing/vouchers/revoke/VCH-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "/api/v1/whoami", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /api/v1/organizations ` + oldSession + ` {"subdomain":"iso-a"}`,
		`POST /api/v1/organizations ` + oldSession + ` {"subdomain":"iso-b"}`,
		`POST /api/v1/organizations ` + newSession + ` {"subdomain":"iso-b"}`,
		`DELETE /api/v1/org/billing/vouchers/revoke/VCH-1 ` + newSession,
		`GET /api/v1/whoami ` + newSession,
	}
	if got := s.seen(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n got %q\nwant %q", got, want)
	}
	if c.Renew.Minted() != 1 || c.SessionValue() != newSession || c.Session != oldSession {
		t.Errorf("minted %d, sends %q, started with %q: want one renewal", c.Renew.Minted(), c.SessionValue(), c.Session)
	}
}

// A second 401, with the renewed session, is a failure with its own message,
// and the call is not sent a third time. A renewal that fails says so. Neither
// message holds a session.
func TestASecond401IsARealFailure(t *testing.T) {
	s, c := newSignIn(t)
	s.expire(oldSession)
	s.expire(newSession)
	res, err := c.Do(context.Background(), http.MethodPost, "/api/v1/organizations", nil)
	if err == nil || !strings.Contains(err.Error(), "POST /api/v1/organizations: HTTP 401 again with a renewed session: {\"error\":\"unauthenticated\"}") ||
		res == nil || res.Status != http.StatusUnauthorized || len(s.seen()) != 2 || c.Renew.Minted() != 1 {
		t.Fatalf("want the second 401 as an error after 2 calls and 1 renewal, got %v %v after %d calls and %d renewals", res, err, len(s.seen()), c.Renew.Minted())
	}

	s, c = newSignIn(t)
	s.expire(oldSession)
	c.Renew = &Renewal{Mint: func(context.Context) (string, error) { return "", errors.New("[region-a] catalyst-api has no signer") }}
	_, err = c.Do(context.Background(), http.MethodDelete, "/api/v1/organizations/t-1", nil)
	if err == nil || err.Error() != "DELETE /api/v1/organizations/t-1: HTTP 401, and the session could not be renewed: [region-a] catalyst-api has no signer" || len(s.seen()) != 1 {
		t.Fatalf("a renewal that fails: got %v after %d calls", err, len(s.seen()))
	}
	for _, e := range []error{err} {
		if strings.Contains(e.Error(), oldSession) || strings.Contains(e.Error(), newSession) {
			t.Errorf("an error holds a session: %v", e)
		}
	}

	// Without Renew the answer is returned as before, with no error.
	s, c = newSignIn(t)
	s.expire(oldSession)
	c.Renew = nil
	if res, err := c.Do(context.Background(), http.MethodPost, "/api/v1/organizations", nil); err != nil || res.Status != http.StatusUnauthorized || len(s.seen()) != 1 {
		t.Fatalf("without Renew: %v %v after %d calls", res, err, len(s.seen()))
	}
}

// Calls refused together mint one session.
func TestCallsRefusedTogetherMintOneSession(t *testing.T) {
	s, c := newSignIn(t)
	s.expire(oldSession)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.Do(context.Background(), http.MethodGet, "/api/v1/whoami", nil)
			if err == nil && res.Status != http.StatusAccepted {
				err = fmt.Errorf("HTTP %d after the renewal", res.Status)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := c.Renew.Minted(); n != 1 {
		t.Errorf("8 calls refused together minted %d sessions, want 1", n)
	}
}

// A GET of the discovery, retried by its policy, is renewed too.
func TestARetriedGetIsRenewed(t *testing.T) {
	s, c := newSignIn(t)
	c.Retry = fast()
	s.expire(oldSession)
	if _, err := c.Get(context.Background(), "/api/v1/whoami", nil); err != nil || len(s.seen()) != 2 {
		t.Fatalf("got %v after %d calls, want success after 2", err, len(s.seen()))
	}
}

// An event stream is renewed as a call is.
func TestAStreamIsRenewed(t *testing.T) {
	s, c := newSignIn(t)
	s.expire(oldSession)
	res, err := c.Stream(context.Background(), nil, "/api/v1/flows/dep-1/stream")
	if err != nil || res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("got %v %v", res, err)
	}
	_ = res.Body.Close()
	if got := s.seen(); len(got) != 2 || got[1] != "GET /api/v1/flows/dep-1/stream "+newSession {
		t.Errorf("calls %q", got)
	}
	s.expire(newSession)
	c.Renew = &Renewal{Mint: func(context.Context) (string, error) { return "third-session", nil }}
	if _, err := c.Stream(context.Background(), nil, "/api/v1/flows/dep-1/stream"); err == nil ||
		err.Error() != `GET /api/v1/flows/dep-1/stream: HTTP 401 again with a renewed session: {"error":"unauthenticated"}` {
		t.Fatalf("a stream refused twice: got %v", err)
	}
}
