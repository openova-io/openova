// Package mail sends PIN and invite messages over SMTP, or logs them at info
// level when no SMTP host is configured (development mode).
package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Sender delivers a plain-text message.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Options configures the SMTP sender.
type Options struct {
	Host, User, Pass, From string
	Port                   int
}

// New returns an SMTP sender when Host is set and a log sender otherwise.
func New(o Options) Sender {
	if strings.TrimSpace(o.Host) == "" {
		slog.Info("SMTP_HOST unset: PIN codes and invite links are logged at info level (development mode)")
		return LogSender{}
	}
	if o.Port == 0 {
		o.Port = 587
	}
	return &SMTPSender{o: o}
}

// LogSender writes the message to the log instead of sending it.
type LogSender struct{}

// Send logs the message.
func (LogSender) Send(_ context.Context, to, subject, body string) error {
	slog.Info("mail (dev mode, not sent)", "to", to, "subject", subject, "body", body)
	return nil
}

// SMTPSender delivers through an SMTP relay (STARTTLS when offered, PLAIN
// auth when a user is configured).
type SMTPSender struct {
	o Options
}

// Send delivers one message.
func (s *SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", s.o.Host, s.o.Port)
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	c, err := smtp.NewClient(conn, s.o.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.o.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.o.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.o.User, s.o.Pass, s.o.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.o.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		s.o.From, to, subject, time.Now().UTC().Format(time.RFC1123Z), body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}
