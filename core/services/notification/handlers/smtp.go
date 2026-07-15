package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultBaseBackoff is the FIRST wait between retries when the SMTP
// server responds with "503 5.5.1" — Stalwart's rate-limit trip. The
// 30s floor is set by the issue spec (TBD-X1, Refs #1793) so the
// Stalwart bucket has time to drain (default 5 req / 1000 ms). Each
// subsequent retry doubles the wait (exponential backoff) capped at
// defaultMaxBackoff so a permanently misconfigured upstream cannot
// hold a worker hostage for hours.
const defaultBaseBackoff = 30 * time.Second

// defaultMaxBackoff caps the exponential backoff growth so a worker
// thread is never stuck waiting >5 minutes per attempt. 30s -> 60s ->
// 120s -> 240s -> 300s (cap). Combined with the circuit-breaker
// (defaultBreakerTrip-consecutive 503s) this bounds total wall time
// per failed send to ~2 minutes.
const defaultMaxBackoff = 5 * time.Minute

// defaultMaxRetries caps total attempts at 3 (initial + 2 retries) so a
// permanently mis-credentialed Stalwart cannot hold a worker hostage.
const defaultMaxRetries = 3

// Circuit-breaker tuning (Refs #1793 deliverable PR A):
//
//   defaultBreakerTrip      — consecutive 503 5.5.1 responses required
//                             to open the breaker. The issue spec
//                             calls for 3.
//   defaultBreakerWindow    — sliding window over which the trip count
//                             accumulates. The issue spec calls for
//                             90s. Older 503s age out so a slow drip
//                             of rate-limits across minutes does not
//                             trip the breaker.
//   defaultBreakerCooldown  — once open, the breaker rejects every
//                             Send immediately for this duration. The
//                             cooldown gives Stalwart's rate-limiter
//                             time to fully drain so the first
//                             post-cooldown send has a chance to
//                             succeed and reset the breaker.
const (
	defaultBreakerTrip     = 3
	defaultBreakerWindow   = 90 * time.Second
	defaultBreakerCooldown = 120 * time.Second
)

// ErrCircuitOpen is returned by Send when the SMTP circuit breaker is
// open — i.e. the Mailer has seen defaultBreakerTrip consecutive 503
// 5.5.1 responses within defaultBreakerWindow. Callers (the events
// consumer) can match on this error to NACK + dead-letter the message
// instead of tight-looping on the same upstream that's already known
// to be rate-limited.
var ErrCircuitOpen = errors.New("smtp circuit breaker open: too many consecutive 503 5.5.1 responses; not sending until cooldown elapses")

// sendMailFunc is the indirection that lets unit tests substitute the
// real net/smtp.SendMail without touching production wiring.
type sendMailFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// sleepFunc lets unit tests collapse the exponential-backoff sleeps to
// zero wall time.
type sleepFunc func(time.Duration)

// nowFunc lets unit tests advance the breaker's notion of "now"
// without sleeping for the breaker window.
type nowFunc func() time.Time

// Mailer sends HTML emails via SMTP with bounded exponential-backoff
// retry on Stalwart rate-limit (503 5.5.1) responses + a sliding-
// window circuit breaker that short-circuits Send once Stalwart is
// clearly overwhelmed (Refs #1793).
type Mailer struct {
	Host string
	Port string
	From string

	// Auth holds the SMTP-PLAIN credentials passed to net/smtp on
	// every send. When nil, net/smtp.SendMail dials without
	// authentication — which is exactly the pre-#1793 bug: Stalwart
	// then rejects the submission with 503 5.5.1 "You must
	// authenticate first" and the retry storm trips Stalwart's
	// 5-req/1000ms rate-limiter for every other tenant on the same
	// relay.
	Auth smtp.Auth

	// BaseBackoff is the FIRST wait between retries when Stalwart
	// returns "503 5.5.1" (rate-limit). Each subsequent retry
	// doubles the wait up to MaxBackoff (exponential). Configurable
	// via the SMTP_RETRY_BACKOFF environment variable; see NewMailer
	// for parse rules.
	BaseBackoff time.Duration
	// MaxBackoff caps the exponential growth so a worker thread is
	// never stuck >MaxBackoff between attempts.
	MaxBackoff time.Duration
	// MaxRetries caps total attempts at MaxRetries (initial + retries).
	MaxRetries int

	// BreakerTrip / BreakerWindow / BreakerCooldown tune the circuit
	// breaker. Default 3 consecutive 503 5.5.1 responses inside a
	// 90s sliding window open the breaker; while open Send returns
	// ErrCircuitOpen immediately for BreakerCooldown so we don't
	// keep slamming a known-rate-limited upstream.
	BreakerTrip     int
	BreakerWindow   time.Duration
	BreakerCooldown time.Duration

	// breaker holds the rolling-window state. Internal — callers
	// drive it only through Send.
	breaker breakerState

	// sendMail, sleep, now are unexported test seams. Production
	// code uses smtp.SendMail / time.Sleep / time.Now.
	sendMail sendMailFunc
	sleep    sleepFunc
	now      nowFunc
}

// breakerState holds the sliding window of 503 5.5.1 timestamps plus
// the openedAt instant. Guarded by mu so concurrent Send calls (the
// events consumer fans out per-message) share a single breaker.
type breakerState struct {
	mu        sync.Mutex
	hits      []time.Time
	openedAt  time.Time
}

// NewMailer creates a Mailer configured for the given SMTP server. The
// base retry-backoff window can be overridden by the SMTP_RETRY_BACKOFF
// environment variable. Accepted forms:
//
//   - Go duration string: "60s", "2m", "500ms"
//   - Bare integer: treated as seconds (e.g., "30" -> 30s)
//
// Invalid values fall back to defaultBaseBackoff (30s). The minimum is
// clamped to 30s — the task spec calls for >=30s so the rate-limiter
// has time to drain.
//
// SMTP auth is read from the SMTP_USER + SMTP_PASS env vars when both
// are non-empty. Either-or-neither: setting one without the other is
// surfaced as a slog warn and treated as no-auth (preserves the
// pre-#1793 behaviour rather than constructing half-broken creds).
func NewMailer(host, port, from string) *Mailer {
	return &Mailer{
		Host:            host,
		Port:            port,
		From:            from,
		Auth:            buildAuth(host),
		BaseBackoff:     parseRetryBackoff(os.Getenv("SMTP_RETRY_BACKOFF")),
		MaxBackoff:      defaultMaxBackoff,
		MaxRetries:      defaultMaxRetries,
		BreakerTrip:     defaultBreakerTrip,
		BreakerWindow:   defaultBreakerWindow,
		BreakerCooldown: defaultBreakerCooldown,
		sendMail:        smtp.SendMail,
		sleep:           time.Sleep,
		now:             time.Now,
	}
}

// buildAuth pulls SMTP_USER / SMTP_PASS from the environment and
// constructs an smtp.PlainAuth bound to the given SMTP host. Both
// values must be non-empty; otherwise Auth stays nil (no-auth) so
// Stalwart's misconfig detection is loud — the operator sees 503 5.5.1
// "You must authenticate first" exactly once before the circuit
// breaker opens, instead of a silent retry storm.
//
// host carries the SMTP server hostname (e.g. mail.openova.io); the
// stdlib net/smtp PlainAuth pins the credential exchange to that
// hostname so an MX redirection cannot harvest creds.
func buildAuth(host string) smtp.Auth {
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	if user == "" && pass == "" {
		return nil
	}
	if user == "" || pass == "" {
		slog.Warn(
			"smtp auth half-configured: one of SMTP_USER/SMTP_PASS is empty; falling back to no-auth (likely causes Stalwart 503 5.5.1)",
			"hasUser", user != "",
			"hasPass", pass != "",
			"host", host,
		)
		return nil
	}
	return smtp.PlainAuth("", user, pass, host)
}

// parseRetryBackoff turns an env-var string into a duration with sane
// defaults and a 30s floor (issue TBD-X1 explicit spec).
func parseRetryBackoff(raw string) time.Duration {
	if raw == "" {
		return defaultBaseBackoff
	}
	// Go duration string first ("60s", "2m"). A bare "0" parses as a
	// valid duration of 0 — treat that (and any non-positive) as
	// "value missing" so the default kicks in.
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return defaultBaseBackoff
		}
		if d < 30*time.Second {
			return 30 * time.Second
		}
		return d
	}
	// Fall back to bare-integer seconds ("30").
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		d := time.Duration(n) * time.Second
		if d < 30*time.Second {
			return 30 * time.Second
		}
		return d
	}
	return defaultBaseBackoff
}

// Send delivers an HTML email to the given recipient. Stalwart
// rate-limit responses (503 5.5.1) trigger a bounded exponential-
// backoff retry loop; all other errors return immediately to the
// caller so the consumer can NACK / dead-letter as appropriate.
//
// If the circuit breaker is open (defaultBreakerTrip consecutive 503
// 5.5.1 responses within BreakerWindow), Send returns ErrCircuitOpen
// immediately for the duration of BreakerCooldown.
func (m *Mailer) Send(to, subject, htmlBody string) error {
	if open, untilWall := m.breakerStateNow(); open {
		slog.Warn(
			"smtp circuit breaker open; skipping send",
			"to", to,
			"subject", subject,
			"reopensIn", untilWall.String(),
		)
		return ErrCircuitOpen
	}

	addr := m.Host + ":" + m.Port

	headers := []string{
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		// RFC 5322 §3.6.1: Date is a REQUIRED originator field. Stalwart
		// relays the message as-is, so without it clients sort/thread the
		// mail badly and some spam filters score on the absence (#5104 C).
		fmt.Sprintf("Date: %s", time.Now().Format(time.RFC1123Z)),
		fmt.Sprintf("From: OpenOva <%s>", m.From),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
	}

	msg := []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + htmlBody)

	sendMail := m.sendMail
	if sendMail == nil {
		sendMail = smtp.SendMail
	}
	sleep := m.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	maxRetries := m.MaxRetries
	if maxRetries < 1 {
		maxRetries = defaultMaxRetries
	}
	base := m.BaseBackoff
	if base <= 0 {
		base = defaultBaseBackoff
	}
	maxBackoff := m.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxBackoff
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := sendMail(addr, m.Auth, m.From, []string{to}, msg)
		if err == nil {
			m.breakerResetOnSuccess()
			return nil
		}
		lastErr = err
		if !isRateLimit(err) {
			// Non-rate-limit errors are not retried — return to
			// caller immediately so the consumer can decide
			// whether to NACK, dead-letter, or surface to the
			// operator.
			return err
		}

		// Rate-limit: record into the breaker window. If the
		// breaker now trips, abort the remaining retry budget
		// rather than wait through more exponential sleeps for an
		// upstream we know is overwhelmed.
		if m.breakerRecordRateLimit() {
			slog.Warn(
				"smtp circuit breaker tripped during retry; abandoning send",
				"to", to,
				"subject", subject,
				"trip", m.breakerTripCount(),
				"window", m.breakerWindowDuration().String(),
			)
			return fmt.Errorf("smtp send to %s: circuit opened after %d attempts: %w", to, attempt, errors.Join(lastErr, ErrCircuitOpen))
		}

		if attempt == maxRetries {
			break
		}

		// Exponential backoff: base * 2^(attempt-1), capped.
		backoff := base << (attempt - 1)
		if backoff > maxBackoff || backoff < base /* overflow guard */ {
			backoff = maxBackoff
		}
		slog.Warn(
			"smtp rate-limit backoff (exponential)",
			"to", to,
			"subject", subject,
			"backoff", backoff.String(),
			"retry", fmt.Sprintf("%d/%d", attempt, maxRetries),
			"error", err.Error(),
		)
		sleep(backoff)
	}
	return fmt.Errorf("smtp send to %s: rate-limited after %d attempts: %w", to, maxRetries, lastErr)
}

// breakerStateNow reports whether the breaker is currently open. When
// open, it returns the remaining cooldown so callers can log a useful
// message; once cooldown has elapsed it auto-clears.
func (m *Mailer) breakerStateNow() (open bool, remaining time.Duration) {
	m.breaker.mu.Lock()
	defer m.breaker.mu.Unlock()
	if m.breaker.openedAt.IsZero() {
		return false, 0
	}
	cooldown := m.BreakerCooldown
	if cooldown <= 0 {
		cooldown = defaultBreakerCooldown
	}
	now := m.nowFn()
	elapsed := now.Sub(m.breaker.openedAt)
	if elapsed >= cooldown {
		// Cooldown elapsed — clear breaker. Hits stay so the next
		// transient 503 cluster can still trip again quickly.
		m.breaker.openedAt = time.Time{}
		// Drop hits older than the window so the next trip count
		// starts fresh from a clean slate after cooldown.
		m.breaker.hits = nil
		return false, 0
	}
	return true, cooldown - elapsed
}

// breakerRecordRateLimit appends a 503-5.5.1 timestamp to the sliding
// window and returns true if the recorded event tripped the breaker
// (i.e. trip-count consecutive 503s within BreakerWindow). When it
// returns true the openedAt clock is set so subsequent Send calls
// short-circuit to ErrCircuitOpen until cooldown elapses.
func (m *Mailer) breakerRecordRateLimit() bool {
	m.breaker.mu.Lock()
	defer m.breaker.mu.Unlock()

	window := m.BreakerWindow
	if window <= 0 {
		window = defaultBreakerWindow
	}
	trip := m.BreakerTrip
	if trip <= 0 {
		trip = defaultBreakerTrip
	}

	now := m.nowFn()
	cutoff := now.Add(-window)

	// Drop hits older than the window. O(n) per insert is fine —
	// trip count is small (3) and 90s window holds at most a few
	// dozen entries even under storm conditions.
	kept := m.breaker.hits[:0]
	for _, t := range m.breaker.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	m.breaker.hits = append(kept, now)

	if len(m.breaker.hits) >= trip {
		m.breaker.openedAt = now
		return true
	}
	return false
}

// breakerResetOnSuccess clears the in-window hit count after a
// successful send. This implements "consecutive 503s" semantics: a
// single 250 OK between rate-limit storms restarts the trip counter.
//
// The openedAt instant is NOT cleared here — once the breaker has
// opened, only the cooldown elapsing in breakerStateNow re-enables
// sends. This avoids a thundering-herd retry the moment one tenant's
// send happens to succeed in a parallel goroutine.
func (m *Mailer) breakerResetOnSuccess() {
	m.breaker.mu.Lock()
	defer m.breaker.mu.Unlock()
	m.breaker.hits = nil
}

// breakerTripCount + breakerWindowDuration are read-side helpers used
// by structured logs; both take the lock briefly.
func (m *Mailer) breakerTripCount() int {
	m.breaker.mu.Lock()
	defer m.breaker.mu.Unlock()
	return len(m.breaker.hits)
}

func (m *Mailer) breakerWindowDuration() time.Duration {
	if m.BreakerWindow <= 0 {
		return defaultBreakerWindow
	}
	return m.BreakerWindow
}

// nowFn returns the (test-overridable) current time. Production wires
// time.Now via NewMailer.
func (m *Mailer) nowFn() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// isRateLimit reports whether err is Stalwart's "503 5.5.1" rate-limit
// response. net/smtp surfaces SMTP-level errors as *textproto.Error,
// but legacy code paths (and some auth-failure branches in net/smtp
// itself) wrap the wire response in a plain error.New, so we also fall
// back to a substring match against the canonical 5.5.1 enhanced code.
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		if te.Code == 503 && strings.Contains(te.Msg, "5.5.1") {
			return true
		}
	}
	msg := err.Error()
	// Match both "503 5.5.1 ..." and "503-5.5.1 ..." (multiline reply
	// continuation form per RFC 5321).
	return strings.Contains(msg, "503 5.5.1") || strings.Contains(msg, "503-5.5.1")
}
