// Package api is the suite's HTTP client for the Sovereign console API. It
// calls the SAME paths the console calls (products/catalyst/bootstrap/ui/src/lib)
// with the SAME bodies, authenticated by the catalyst_session cookie.
//
// The cookie value is never printed: errors carry method, path, status and a
// truncated body only.
//
// A session has a lifetime (sovereign-acceptance mint-session: 3 hours), and a
// run that waits for a reboot window lasts longer. With Renew set, a call the
// console answers HTTP 401 renews the session once and is sent once more; a
// second 401 is an error of its own. Do and Stream both do it, so every call
// path of the suite does.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/retry"
)

// Client talks to https://console.<fqdn>.
type Client struct {
	Base    string // e.g. https://console.hw307.omani.works (no trailing slash)
	Session string // the catalyst_session value the client starts with — secret
	HTTP    *http.Client
	// ReadOnly refuses every non-GET call. `discover` sets it so a dry run
	// can never mutate the Sovereign, whatever a code path tries.
	ReadOnly bool
	// Retry, when set, retries a GET that got no answer, or an answer HTTP 429
	// or 5xx (package retry). Only the discovery sets it. Any other method is
	// sent once, with or without it.
	Retry *retry.Policy
	// Renew, when set, renews the session when the console answers HTTP 401.
	// It is a pointer: a copy of the client shares it, and with it the
	// session minted last.
	Renew *Renewal
}

// Renewal holds the session that replaced Client.Session and mints the next.
type Renewal struct {
	// Mint returns a new session value. It never returns the value in an error.
	Mint func(ctx context.Context) (string, error)

	mu     sync.Mutex
	value  string // the session minted last; "" before the first renewal
	minted int
}

// Minted is how many sessions were minted.
func (r *Renewal) Minted() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.minted
}

func (r *Renewal) current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value
}

// renew replaces the session used, which the console refused. When another
// call renewed it since used was sent, the newer session stands and nothing is
// minted: calls refused together mint one session.
func (r *Renewal) renew(ctx context.Context, used string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.value != "" && r.value != used {
		return nil
	}
	if r.Mint == nil {
		return errors.New("no way to mint a session was given")
	}
	v, err := r.Mint(ctx)
	switch {
	case err != nil:
		return err
	case v == "":
		return errors.New("the minted session is empty")
	}
	r.value = v
	r.minted++
	return nil
}

// SessionValue is the session the next call sends: the one minted last, else
// Session.
func (c *Client) SessionValue() string {
	if c.Renew != nil {
		if v := c.Renew.current(); v != "" {
			return v
		}
	}
	return c.Session
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

// Do issues one request. body may be nil or any JSON-marshalable value. With a
// Retry policy a GET is retried as the policy says; when the server answered,
// its last answer is returned, whatever its status. With Renew, an answer HTTP
// 401 renews the session and the call is sent once more (the console refuses
// a call it does not authenticate before acting on it); when the renewal fails
// or the answer is 401 again, Do returns that answer with an error.
func (c *Client) Do(ctx context.Context, method, path string, body any) (*Response, error) {
	if c.ReadOnly && method != http.MethodGet {
		return nil, fmt.Errorf("read-only mode: refusing %s %s", method, path)
	}
	used := c.SessionValue()
	res, err := c.send(ctx, method, path, body, used)
	if err != nil || res == nil || res.Status != http.StatusUnauthorized || c.Renew == nil {
		return res, err
	}
	if err := c.Renew.renew(ctx, used); err != nil {
		return res, fmt.Errorf("%s %s: HTTP 401, and the session could not be renewed: %w", method, path, err)
	}
	res, err = c.send(ctx, method, path, body, c.SessionValue())
	if err == nil && res != nil && res.Status == http.StatusUnauthorized {
		return res, fmt.Errorf("%s %s: HTTP 401 again with a renewed session: %s", method, path, Snippet(res.Body))
	}
	return res, err
}

// send is one call with session, retried as the Retry policy says.
func (c *Client) send(ctx context.Context, method, path string, body any, session string) (*Response, error) {
	if c.Retry == nil || method != http.MethodGet {
		return c.once(ctx, method, path, body, session)
	}
	var last *Response
	err := c.Retry.Do(ctx, func() (retry.Kind, error) {
		res, err := c.once(ctx, method, path, body, session)
		last = res
		switch {
		case err != nil:
			return retry.Classify(err.Error()), err
		case res.Status == http.StatusTooManyRequests || res.Status >= 500:
			return retry.Answered, fmt.Errorf("%s %s: HTTP %d", method, path, res.Status)
		}
		return retry.Final, nil
	})
	if last != nil {
		return last, nil
	}
	return nil, err
}

// once sends the request one time with session.
func (c *Client) once(ctx context.Context, method, path string, body any, session string) (*Response, error) {
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
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "catalyst_session", Value: session})
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

// Stream opens the event stream GET path with client (nil: c.HTTP, else
// http.DefaultClient) and returns the response, whose body the caller closes.
// An answer HTTP 401 renews the session as Do does; a second 401, or a renewal
// that fails, is an error and the body is closed.
func (c *Client) Stream(ctx context.Context, client *http.Client, path string) (*http.Response, error) {
	if client == nil {
		client = c.HTTP
	}
	if client == nil {
		client = http.DefaultClient
	}
	used := c.SessionValue()
	res, err := c.openStream(ctx, client, path, used)
	if err != nil || res.StatusCode != http.StatusUnauthorized || c.Renew == nil {
		return res, err
	}
	_ = res.Body.Close()
	if err := c.Renew.renew(ctx, used); err != nil {
		return nil, fmt.Errorf("GET %s: HTTP 401, and the session could not be renewed: %w", path, err)
	}
	res, err = c.openStream(ctx, client, path, c.SessionValue())
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP 401 again with a renewed session: %s", path, Snippet(b))
	}
	return res, err
}

func (c *Client) openStream(ctx context.Context, client *http.Client, path, session string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "catalyst_session", Value: session})
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return res, nil
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
