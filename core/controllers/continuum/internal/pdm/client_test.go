package pdm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
)

type fakeDoer struct {
	got    *http.Request
	status int
	body   string
	err    error
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	// Drain body to assert later.
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(strings.NewReader(string(b)))
	}
	f.got = req
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

func sampleRecords() []dns.Record {
	return []dns.Record{
		{Hostname: "a.example.com", LuaBody: "ifurlup(...)", TTL: 30, PrimaryRegion: "hz-fsn-rtz-prod"},
	}
}

func TestCommitLuaRecords_HappyPath(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{status: 200, body: `{"ok":true}`}
	c := &Client{BaseURL: "http://pdm:8080", AuthToken: "tok", HTTP: doer}
	err := c.CommitLuaRecords(context.Background(), "example.com", sampleRecords())
	if err != nil {
		t.Fatalf("CommitLuaRecords: %v", err)
	}
	if doer.got == nil {
		t.Fatal("expected http call")
	}
	if got := doer.got.URL.Path; got != "/v1/lua/commit" {
		t.Fatalf("path = %q want /v1/lua/commit", got)
	}
	if got := doer.got.Header.Get("X-Catalyst-Token"); got != "tok" {
		t.Fatalf("auth header = %q want tok", got)
	}
	if got := doer.got.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	// Body should be valid JSON containing the zone + records.
	b, _ := io.ReadAll(doer.got.Body)
	if !strings.Contains(string(b), `"zone":"example.com"`) {
		t.Fatalf("body missing zone: %s", b)
	}
	if !strings.Contains(string(b), `"records":[`) {
		t.Fatalf("body missing records: %s", b)
	}
}

func TestCommitLuaRecords_NoOpOnEmpty(t *testing.T) {
	t.Parallel()
	c := New("http://pdm:8080", "")
	if err := c.CommitLuaRecords(context.Background(), "z", nil); !errors.Is(err, ErrNoOp) {
		t.Fatalf("err = %v want ErrNoOp", err)
	}
}

func TestCommitLuaRecords_RequiresZone(t *testing.T) {
	t.Parallel()
	c := New("http://pdm:8080", "")
	if err := c.CommitLuaRecords(context.Background(), "", sampleRecords()); err == nil {
		t.Fatal("expected error on empty zone")
	}
}

func TestCommitLuaRecords_AuthRejected(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{status: 401, body: `{"error":"unauth"}`}
	c := &Client{BaseURL: "http://pdm:8080", HTTP: doer}
	err := c.CommitLuaRecords(context.Background(), "z", sampleRecords())
	if err == nil || !strings.Contains(err.Error(), "auth rejected") {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestCommitLuaRecords_5xxErrorSurface(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{status: 500, body: `boom`}
	c := &Client{BaseURL: "http://pdm:8080", HTTP: doer}
	err := c.CommitLuaRecords(context.Background(), "z", sampleRecords())
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}

func TestCommitLuaRecords_TransportError(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{err: errors.New("connection refused")}
	c := &Client{BaseURL: "http://pdm:8080", HTTP: doer}
	err := c.CommitLuaRecords(context.Background(), "z", sampleRecords())
	if err == nil || !strings.Contains(err.Error(), "http:") {
		t.Fatalf("expected transport error, got %v", err)
	}
}

func TestCommitLuaRecords_RequiresBaseURL(t *testing.T) {
	t.Parallel()
	c := &Client{HTTP: &fakeDoer{}}
	if err := c.CommitLuaRecords(context.Background(), "z", sampleRecords()); err == nil {
		t.Fatal("expected error without BaseURL")
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	t.Parallel()
	c := New("http://pdm:8080/", "")
	if c.BaseURL != "http://pdm:8080" {
		t.Fatalf("BaseURL = %q want http://pdm:8080", c.BaseURL)
	}
}
