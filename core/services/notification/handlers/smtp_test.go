package handlers

import (
	"bytes"
	"errors"
	"net/mail"
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
	mu      sync.Mutex
	calls   int
	errs    []error
	lastMsg []byte
}

func (f *fakeSendMail) send(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastMsg = append([]byte(nil), msg...)
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

// fixedNow returns a controllable now() seam for breaker tests. Each
// call returns the t value, which the caller advances explicitly via
// the returned advance() closure. Used to step past BreakerCooldown
// without sleeping.
func fixedNow() (now func() time.Time, advance func(time.Duration), set func(time.Time)) {
	var (
		mu sync.Mutex
		t  = time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	)
	now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return t
	}
	advance = func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(d)
	}
	set = func(v time.Time) {
		mu.Lock()
		defer mu.Unlock()
		t = v
	}
	return
}

// newTestMailer builds a Mailer with all seams wired for unit tests.
// Defaults: 3 retries, 30s base / 5min max backoff, breaker 3-in-90s.
func newTestMailer(fs *fakeSendMail, rs *recordingSleep, now func() time.Time) *Mailer {
	return &Mailer{
		Host:            "stalwart.test",
		Port:            "25",
		From:            "noreply@openova.io",
		BaseBackoff:     30 * time.Second,
		MaxBackoff:      5 * time.Minute,
		MaxRetries:      3,
		BreakerTrip:     3,
		BreakerWindow:   90 * time.Second,
		BreakerCooldown: 120 * time.Second,
		sendMail:        fs.send,
		sleep:           rs.sleep,
		now:             now,
	}
}

// TestMailerSend_RateLimitRetrySucceeds asserts that when Stalwart
// returns 503 5.5.1 on the first attempt and then 250 OK on the
// second, Send retries with the base backoff and returns nil.
func TestMailerSend_RateLimitRetrySucceeds(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Too many messages"}
	fs := &fakeSendMail{errs: []error{rateLimitErr, nil}}
	rs := &recordingSleep{}
	now, _, _ := fixedNow()

	m := newTestMailer(fs, rs, now)

	if err := m.Send("to@example.test", "Subject", "<p>body</p>"); err != nil {
		t.Fatalf("Send returned error after successful retry: %v", err)
	}
	if fs.calls != 2 {
		t.Errorf("sendMail called %d times, want 2 (one fail, one ok)", fs.calls)
	}
	if len(rs.waits) != 1 {
		t.Fatalf("sleep called %d times, want exactly 1 backoff", len(rs.waits))
	}
	if rs.waits[0] != 30*time.Second {
		t.Errorf("backoff = %v, want 30s (base, attempt 1)", rs.waits[0])
	}
}

// TestMailerSend_ExponentialBackoff asserts that when the upstream
// returns 503 5.5.1 then 503 5.5.1 then 250 OK, two backoffs are
// recorded and the second is double the first (exponential).
func TestMailerSend_ExponentialBackoff(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Too many messages"}
	fs := &fakeSendMail{errs: []error{rateLimitErr, rateLimitErr, nil}}
	rs := &recordingSleep{}
	now, _, _ := fixedNow()

	// Allow 4 attempts so we observe the doubling without tripping
	// the 3-in-window breaker on the second 503.
	m := newTestMailer(fs, rs, now)
	m.MaxRetries = 4
	m.BreakerTrip = 99 // disable breaker for this test

	if err := m.Send("to@example.test", "Subject", "<p>body</p>"); err != nil {
		t.Fatalf("Send returned error after successful retry: %v", err)
	}
	if fs.calls != 3 {
		t.Errorf("sendMail called %d times, want 3 (two fail, one ok)", fs.calls)
	}
	if len(rs.waits) != 2 {
		t.Fatalf("sleep called %d times, want 2 backoffs", len(rs.waits))
	}
	if rs.waits[0] != 30*time.Second {
		t.Errorf("backoff[0] = %v, want 30s (base)", rs.waits[0])
	}
	if rs.waits[1] != 60*time.Second {
		t.Errorf("backoff[1] = %v, want 60s (2x base — exponential)", rs.waits[1])
	}
}

// TestMailerSend_BackoffCapped asserts that exponential growth is
// capped at MaxBackoff so a long-running storm never sleeps for more
// than MaxBackoff per attempt.
func TestMailerSend_BackoffCapped(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Too many messages"}
	// Many 503s; eventually a success so the loop exits cleanly.
	fs := &fakeSendMail{errs: []error{
		rateLimitErr, rateLimitErr, rateLimitErr, rateLimitErr, rateLimitErr, nil,
	}}
	rs := &recordingSleep{}
	now, _, _ := fixedNow()

	m := newTestMailer(fs, rs, now)
	m.MaxRetries = 6
	m.MaxBackoff = 90 * time.Second
	m.BreakerTrip = 99 // disable breaker

	if err := m.Send("to@example.test", "Subject", "<p>body</p>"); err != nil {
		t.Fatalf("Send returned error after successful retry: %v", err)
	}
	// 5 backoffs expected (between 5 fails and 1 success).
	if len(rs.waits) != 5 {
		t.Fatalf("got %d backoffs, want 5: %v", len(rs.waits), rs.waits)
	}
	// 30, 60, 90 (cap), 90 (cap), 90 (cap)
	wantWaits := []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second, 90 * time.Second, 90 * time.Second}
	for i, w := range rs.waits {
		if w != wantWaits[i] {
			t.Errorf("backoff[%d] = %v, want %v", i, w, wantWaits[i])
		}
	}
}

// TestMailerSend_CircuitBreakerTrips asserts that after BreakerTrip
// consecutive 503 5.5.1s within BreakerWindow the breaker opens and
// the in-flight Send aborts the rest of its retry budget.
func TestMailerSend_CircuitBreakerTrips(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Rate limit exceeded"}
	fs := &fakeSendMail{errs: []error{rateLimitErr, rateLimitErr, rateLimitErr, rateLimitErr}}
	rs := &recordingSleep{}
	now, _, _ := fixedNow()

	m := newTestMailer(fs, rs, now)
	m.MaxRetries = 5

	err := m.Send("to@example.test", "Subject", "<p>body</p>")
	if err == nil {
		t.Fatal("Send returned nil after breaker tripped; want wrapped error")
	}
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("error %q does not unwrap to ErrCircuitOpen", err.Error())
	}
	if fs.calls != 3 {
		t.Errorf("sendMail called %d times, want exactly 3 (breaker trips at trip-th hit)", fs.calls)
	}
}

// TestMailerSend_CircuitBreakerShortCircuits asserts that once the
// breaker is open, subsequent Send calls return ErrCircuitOpen
// immediately without calling sendMail.
func TestMailerSend_CircuitBreakerShortCircuits(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Rate limit exceeded"}
	fs := &fakeSendMail{errs: []error{rateLimitErr, rateLimitErr, rateLimitErr, rateLimitErr}}
	rs := &recordingSleep{}
	now, _, _ := fixedNow()

	m := newTestMailer(fs, rs, now)
	m.MaxRetries = 5

	// Trip the breaker.
	if err := m.Send("to1@example.test", "S", "<p>b</p>"); err == nil {
		t.Fatal("first Send should fail with breaker open")
	}
	openedCalls := fs.calls

	// Second Send: should short-circuit immediately, sendMail count
	// must not advance.
	err := m.Send("to2@example.test", "S", "<p>b</p>")
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("second Send returned %v, want ErrCircuitOpen", err)
	}
	if fs.calls != openedCalls {
		t.Errorf("sendMail called %d more times after breaker open; want 0 more", fs.calls-openedCalls)
	}
}

// TestMailerSend_CircuitBreakerCooldownElapses asserts that after the
// cooldown window the breaker re-closes and a subsequent send hits
// the upstream again.
func TestMailerSend_CircuitBreakerCooldownElapses(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Rate limit exceeded"}
	// First call: 3 503s → trip breaker. Next call after cooldown:
	// 250 OK on the first attempt.
	fs := &fakeSendMail{errs: []error{rateLimitErr, rateLimitErr, rateLimitErr, nil}}
	rs := &recordingSleep{}
	now, advance, _ := fixedNow()

	m := newTestMailer(fs, rs, now)
	m.MaxRetries = 3

	// Trip.
	if err := m.Send("to1@example.test", "S", "<p>b</p>"); err == nil {
		t.Fatal("first Send should fail with breaker open")
	}

	// Mid-cooldown: still short-circuited.
	advance(60 * time.Second)
	if err := m.Send("to2@example.test", "S", "<p>b</p>"); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("mid-cooldown Send = %v, want ErrCircuitOpen", err)
	}

	// Past cooldown (120s): breaker re-closes, sendMail runs again.
	advance(61 * time.Second) // total 121s past trip
	if err := m.Send("to3@example.test", "S", "<p>b</p>"); err != nil {
		t.Fatalf("post-cooldown Send returned %v; want nil after breaker reset", err)
	}
}

// TestMailerSend_BreakerWindowAgesOut asserts that 503s older than
// BreakerWindow do not count toward the trip — a slow drip of one
// rate-limit every minute should never open the breaker.
func TestMailerSend_BreakerWindowAgesOut(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1"}
	// 3 alternating fail/success — each fail ages out the prior one
	// because we advance >window between calls.
	fs := &fakeSendMail{errs: []error{rateLimitErr, nil, rateLimitErr, nil, rateLimitErr, nil}}
	rs := &recordingSleep{}
	now, advance, _ := fixedNow()

	m := newTestMailer(fs, rs, now)
	m.MaxRetries = 2

	for i := 0; i < 3; i++ {
		if err := m.Send("to@example.test", "S", "<p>b</p>"); err != nil {
			t.Fatalf("iter %d: Send returned %v; want nil (retry succeeded)", i, err)
		}
		advance(2 * time.Minute) // > 90s window
	}
	// Breaker must not be open at the end.
	if open, _ := m.breakerStateNow(); open {
		t.Error("breaker is open; want closed after slow-drip 503s aged out of window")
	}
}

// TestMailerSend_NonRateLimitErrorReturnsImmediately asserts that
// non-503-5.5.1 errors are NOT retried — they bubble up to the caller
// so the events consumer can NACK or dead-letter as appropriate.
func TestMailerSend_NonRateLimitErrorReturnsImmediately(t *testing.T) {
	authErr := &textproto.Error{Code: 535, Msg: "5.7.8 Authentication credentials invalid"}
	fs := &fakeSendMail{errs: []error{authErr, nil}}
	rs := &recordingSleep{}
	now, _, _ := fixedNow()

	m := newTestMailer(fs, rs, now)

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

// TestMailerSend_RateLimitExhaustsRetries — with breaker disabled,
// MaxRetries 503s in a row return the rate-limited wrap error.
func TestMailerSend_RateLimitExhaustsRetries(t *testing.T) {
	rateLimitErr := &textproto.Error{Code: 503, Msg: "5.5.1 Rate limit exceeded"}
	fs := &fakeSendMail{errs: []error{rateLimitErr, rateLimitErr, rateLimitErr}}
	rs := &recordingSleep{}
	now, _, _ := fixedNow()

	m := newTestMailer(fs, rs, now)
	m.BreakerTrip = 99 // disable breaker to isolate retry-exhaustion path

	err := m.Send("to@example.test", "Subject", "<p>body</p>")
	if err == nil {
		t.Fatal("Send returned nil after exhausted retries; want wrapped error")
	}
	if !strings.Contains(err.Error(), "rate-limited after 3 attempts") {
		t.Errorf("error %q missing rate-limit summary", err.Error())
	}
	if fs.calls != 3 {
		t.Errorf("sendMail called %d times, want 3 (= MaxRetries)", fs.calls)
	}
	if len(rs.waits) != 2 {
		t.Errorf("sleep called %d times, want 2 (between attempts 1->2 and 2->3)", len(rs.waits))
	}
	// Exponential: 30s + 60s = 90s total.
	if rs.wallSum != 90*time.Second {
		t.Errorf("total backoff = %v, want 90s (30 + 60 exponential)", rs.wallSum)
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
		{"", 30 * time.Second},        // unset -> default base
		{"60s", 60 * time.Second},     // explicit Go duration
		{"2m", 2 * time.Minute},       // larger duration
		{"45", 45 * time.Second},      // bare integer = seconds
		{"10s", 30 * time.Second},     // below floor -> floor
		{"5", 30 * time.Second},       // bare-int below floor -> floor
		{"garbage", 30 * time.Second}, // unparseable -> default
		{"-1", 30 * time.Second},      // negative bare-int -> default
		{"0", 30 * time.Second},       // zero bare-int -> default (n>0 guard)
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

// TestBuildAuth covers SMTP auth construction across the
// neither / user-only / pass-only / both cases.
func TestBuildAuth(t *testing.T) {
	withEnv := func(t *testing.T, user, pass string, want bool) {
		t.Helper()
		t.Setenv("SMTP_USER", user)
		t.Setenv("SMTP_PASS", pass)
		got := buildAuth("stalwart.test")
		if (got != nil) != want {
			t.Errorf("buildAuth(user=%q, pass=%q) returned non-nil=%v, want %v", user, pass, got != nil, want)
		}
	}
	t.Run("neither", func(t *testing.T) { withEnv(t, "", "", false) })
	t.Run("user-only", func(t *testing.T) { withEnv(t, "u", "", false) })
	t.Run("pass-only", func(t *testing.T) { withEnv(t, "", "p", false) })
	t.Run("both", func(t *testing.T) { withEnv(t, "u", "p", true) })
}

// TestMailerSend_IncludesRFC5322DateHeader guards #5104 facet C: Stalwart
// relays messages as-composed, so Send must stamp the mandatory RFC 5322
// Date originator field itself — its absence breaks client sorting and
// feeds spam scoring.
func TestMailerSend_IncludesRFC5322DateHeader(t *testing.T) {
	fs := &fakeSendMail{errs: []error{nil}}
	now, _, _ := fixedNow()
	m := newTestMailer(fs, &recordingSleep{}, now)

	if err := m.Send("to@example.com", "subject", "<p>hi</p>"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(fs.lastMsg))
	if err != nil {
		t.Fatalf("composed message does not parse as RFC 5322: %v", err)
	}
	if _, err := parsed.Header.Date(); err != nil {
		t.Errorf("Date header missing or unparseable: %v (got %q)", err, parsed.Header.Get("Date"))
	}
}
