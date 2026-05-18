package handlers

import (
	"errors"
	"net/smtp"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSendMail is a stub of net/smtp.SendMail. It returns errs[i] for
// the i-th call (saturating to the last entry if calls > len(errs)),
// and records every invocation under the mutex.
type fakeSendMail struct {
	mu    sync.Mutex
	calls int
	errs  []error
}

func (f *fakeSendMail) send(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if i >= len(f.errs) {
		i = len(f.errs) - 1
	}
	return f.errs[i]
}

// recordingSleep captures every backoff request so tests can assert on
// total wall-time without actually sleeping.
type recordingSleep struct {
	mu      sync.Mutex
	waits   []time.Duration
	wallSum time.Duration
}

func (r *recordingSleep) sleep(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waits = append(r.waits, d)
	r.wallSum += d
}

// TestMailerSend_RateLimitRetrySucceeds asserts that when Stalwart
// returns 503 5.5.1 on the first attempt and then 250 OK on the second,
// Send retries with the configured backoff and returns nil. Verifies
// the slog "rate-limit backoff" log line by way of the call count +
// recorded backoff waits.
func TestMailerSend_RateLimitRetrySucceeds(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Too many messages"}
	fs := &fakeSendMail{errs: []error{rateLimitErr, nil}}
	rs := &recordingSleep{}

	m := &Mailer{
		Host:         "stalwart.test",
		Port:         "25",
		From:         "noreply@openova.io",
		RetryBackoff: 60 * time.Second,
		MaxRetries:   3,
		sendMail:     fs.send,
		sleep:        rs.sleep,
	}

	if err := m.Send("to@example.test", "Subject", "<p>body</p>"); err != nil {
		t.Fatalf("Send returned error after successful retry: %v", err)
	}
	if fs.calls != 2 {
		t.Errorf("sendMail called %d times, want 2 (one fail, one ok)", fs.calls)
	}
	if len(rs.waits) != 1 {
		t.Fatalf("sleep called %d times, want exactly 1 backoff", len(rs.waits))
	}
	if rs.waits[0] != 60*time.Second {
		t.Errorf("backoff = %v, want 60s", rs.waits[0])
	}
}

// TestMailerSend_RateLimitExhaustsRetries asserts that after MaxRetries
// 503 5.5.1 responses, Send returns a wrapped error and the total
// number of backoff sleeps is MaxRetries-1 (no sleep after the final
// failed attempt).
func TestMailerSend_RateLimitExhaustsRetries(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Rate limit exceeded"}
	fs := &fakeSendMail{errs: []error{rateLimitErr, rateLimitErr, rateLimitErr}}
	rs := &recordingSleep{}

	m := &Mailer{
		Host:         "stalwart.test",
		Port:         "25",
		From:         "noreply@openova.io",
		RetryBackoff: 30 * time.Second,
		MaxRetries:   3,
		sendMail:     fs.send,
		sleep:        rs.sleep,
	}

	err := m.Send("to@example.test", "Subject", "<p>body</p>")
	if err == nil {
		t.Fatal("Send returned nil after exhausted retries; want wrapped error")
	}
	if !strings.Contains(err.Error(), "rate-limited after 3 attempts") {
		t.Errorf("error %q missing rate-limit summary", err.Error())
	}
	// Underlying *textproto.Error must remain reachable via errors.Is/As.
	var te *textproto.Error
	if !errors.As(err, &te) {
		t.Error("returned error does not unwrap to *textproto.Error")
	}
	if fs.calls != 3 {
		t.Errorf("sendMail called %d times, want 3 (= MaxRetries)", fs.calls)
	}
	if len(rs.waits) != 2 {
		t.Errorf("sleep called %d times, want 2 (between attempts 1->2 and 2->3)", len(rs.waits))
	}
	if rs.wallSum != 60*time.Second {
		t.Errorf("total backoff = %v, want 60s (2 * 30s)", rs.wallSum)
	}
}

// TestMailerSend_NonRateLimitErrorReturnsImmediately asserts that
// non-503-5.5.1 errors are NOT retried — they bubble up to the caller
// so the events consumer can NACK or dead-letter as appropriate.
func TestMailerSend_NonRateLimitErrorReturnsImmediately(t *testing.T) {
	authErr := &textproto.Error{Code: 535, Msg: "5.7.8 Authentication credentials invalid"}
	fs := &fakeSendMail{errs: []error{authErr, nil}}
	rs := &recordingSleep{}

	m := &Mailer{
		Host:         "stalwart.test",
		Port:         "25",
		From:         "noreply@openova.io",
		RetryBackoff: 60 * time.Second,
		MaxRetries:   3,
		sendMail:     fs.send,
		sleep:        rs.sleep,
	}

	err := m.Send("to@example.test", "Subject", "<p>body</p>")
	if err == nil {
		t.Fatal("Send returned nil for non-rate-limit auth error; want pass-through")
	}
	if fs.calls != 1 {
		t.Errorf("sendMail called %d times, want exactly 1 (no retry for non-rate-limit)", fs.calls)
	}
	if len(rs.waits) != 0 {
		t.Errorf("sleep called %d times, want 0 (no backoff for non-rate-limit)", len(rs.waits))
	}
}

// TestIsRateLimit_TextprotoError asserts detection of the canonical
// SMTP rate-limit reply when returned as *textproto.Error.
func TestIsRateLimit_TextprotoError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"rate-limit", &textproto.Error{Code: 503, Msg: "5.5.1 Too many messages"}, true},
		{"503-without-5.5.1", &textproto.Error{Code: 503, Msg: "5.5.0 Generic"}, false},
		{"535-auth-failure", &textproto.Error{Code: 535, Msg: "5.7.8 Auth invalid"}, false},
		{"plain-error-with-503-5.5.1-substring", errors.New("smtp: 503 5.5.1 rate limit"), true},
		{"plain-error-with-multiline-form", errors.New("smtp: 503-5.5.1 continuation form"), true},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRateLimit(tc.err)
			if got != tc.want {
				t.Errorf("isRateLimit(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestParseRetryBackoff covers env-var parsing including the 30s floor
// and bare-integer-seconds form.
func TestParseRetryBackoff(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", 60 * time.Second},             // unset -> default
		{"60s", 60 * time.Second},          // explicit Go duration
		{"2m", 2 * time.Minute},            // larger duration
		{"45", 45 * time.Second},           // bare integer = seconds
		{"10s", 30 * time.Second},          // below floor -> floor
		{"5", 30 * time.Second},            // bare-int below floor -> floor
		{"garbage", 60 * time.Second},      // unparseable -> default
		{"-1", 60 * time.Second},           // negative bare-int -> default
		{"0", 60 * time.Second},            // zero bare-int -> default (n>0 guard)
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := parseRetryBackoff(tc.raw)
			if got != tc.want {
				t.Errorf("parseRetryBackoff(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
