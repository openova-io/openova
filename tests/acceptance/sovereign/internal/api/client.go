// Package api is the suite's HTTP client for the Sovereign console API. It
// calls the SAME paths the console calls (products/catalyst/bootstrap/ui/src/lib)
// with the SAME bodies, authenticated by the catalyst_session cookie.
//
// The cookie value is never printed: errors carry method, path, status and a
// truncated body only.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to https://console.<fqdn>.
type Client struct {
	Base    string // e.g. https://console.hw307.omani.works (no trailing slash)
	Session string // catalyst_session value — secret
	HTTP    *http.Client
	// ReadOnly refuses every non-GET call. `discover` sets it so a dry run
	// can never mutate the Sovereign, whatever a code path tries.
	ReadOnly bool
}

// New builds a client with a sane timeout.
func New(base, session string) *Client {
	return &Client{
		Base:    strings.TrimRight(base, "/"),
		Session: session,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Response is a fully read HTTP answer.
type Response struct {
	Status int
	Body   []byte
	Header http.Header
}

// JSON decodes the body into v.
func (r *Response) JSON(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode %d body: %w (body=%s)", r.Status, err, Snippet(r.Body))
	}
	return nil
}

// Snippet is a log-safe excerpt of a body.
func Snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// Do issues one request. body may be nil or any JSON-marshalable value.
func (c *Client) Do(ctx context.Context, method, path string, body any) (*Response, error) {
	if c.ReadOnly && method != http.MethodGet {
		return nil, fmt.Errorf("read-only mode: refusing %s %s", method, path)
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Session != "" {
		req.AddCookie(&http.Cookie{Name: "catalyst_session", Value: c.Session})
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	return &Response{Status: res.StatusCode, Body: b, Header: res.Header}, nil
}

// Get is Do(GET) that also fails on non-2xx.
func (c *Client) Get(ctx context.Context, path string, v any) (*Response, error) {
	r, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if r.Status < 200 || r.Status > 299 {
		return r, fmt.Errorf("GET %s: HTTP %d %s", path, r.Status, Snippet(r.Body))
	}
	if v != nil {
		return r, r.JSON(v)
	}
	return r, nil
}
