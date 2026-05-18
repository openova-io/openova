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
	"time"
)

// defaultRetryBackoff is the wait between retries when the SMTP server
// responds with "503 5.5.1" — Stalwart's rate-limit trip. The window is
// chosen to outlast Stalwart's default 30s rate-limit bucket; 60s gives
// the bucket enough headroom that a retry lands on a fresh quota even
// when wall-clock drift is involved (Refs #1793).
const defaultRetryBackoff = 60 * time.Second

// defaultMaxRetries caps total attempts at 3 (initial + 2 retries) so a
// permanently mis-credentialed Stalwart cannot hold a worker hostage.
const defaultMaxRetries = 3

// sendMailFunc is the indirection that lets unit tests substitute the
// real net/smtp.SendMail without touching production wiring.
type sendMailFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// sleepFunc lets unit tests collapse the 60s backoff to zero wall time.
type sleepFunc func(time.Duration)

// Mailer sends HTML emails via SMTP with bounded retry on Stalwart
// rate-limit (503 5.5.1) responses.
type Mailer struct {
	Host string
	Port string
	From string

	// RetryBackoff is the wait between retries when Stalwart returns
	// "503 5.5.1" (rate-limit). Configurable via the SMTP_RETRY_BACKOFF
	// environment variable; see NewMailer for parse rules.
	RetryBackoff time.Duration
	// MaxRetries caps total attempts at MaxRetries (initial + retries).
	MaxRetries int

	// sendMail and sleep are unexported test seams. Production code
	// always uses smtp.SendMail and time.Sleep.
	sendMail sendMailFunc
	sleep    sleepFunc
}

// NewMailer creates a Mailer configured for the given SMTP server. The
// retry-backoff window can be overridden by the SMTP_RETRY_BACKOFF
// environment variable. Accepted forms:
//
//   - Go duration string: "60s", "2m", "500ms"
//   - Bare integer: treated as seconds (e.g., "30" -> 30s)
//
// Invalid values fall back to defaultRetryBackoff (60s). The minimum
// is clamped to 30s — the task spec calls for >=30s so the rate-limiter
// has time to drain.
func NewMailer(host, port, from string) *Mailer {
	return &Mailer{
		Host:         host,
		Port:         port,
		From:         from,
		RetryBackoff: parseRetryBackoff(os.Getenv("SMTP_RETRY_BACKOFF")),
		MaxRetries:   defaultMaxRetries,
		sendMail:     smtp.SendMail,
		sleep:        time.Sleep,
	}
}

// parseRetryBackoff turns an env-var string into a duration with sane
// defaults and a 30s floor.
func parseRetryBackoff(raw string) time.Duration {
	if raw == "" {
		return defaultRetryBackoff
	}
	// Go duration string first ("60s", "2m"). A bare "0" parses as a
	// valid duration of 0 — treat that (and any non-positive) as
	// "value missing" so the default kicks in.
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return defaultRetryBackoff
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
	return defaultRetryBackoff
}

// Send delivers an HTML email to the given recipient. Stalwart
// rate-limit responses (503 5.5.1) trigger a bounded retry loop with
// RetryBackoff between attempts; all other errors return immediately
// to the caller so the consumer can NACK / dead-letter as appropriate.
func (m *Mailer) Send(to, subject, htmlBody string) error {
	addr := m.Host + ":" + m.Port

	headers := []string{
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		fmt.Sprintf("From: OpenOva SME <%s>", m.From),
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
	backoff := m.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := sendMail(addr, nil, m.From, []string{to}, msg)
		if err == nil {
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
		if attempt == maxRetries {
			break
		}
		slog.Warn(
			"smtp rate-limit backoff",
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
