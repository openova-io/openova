package handler

import (
	"net/mail"
	"strings"
	"testing"
)

// TestPinEmailMessage_HasRFC5322DateHeader guards #5104 facet C: the
// live hw255 funnel walk proved sign-in-code mails arrive with NO Date
// header (msg.get('Date') == None on IMAP fetch) — Stalwart relays the
// message as-composed, so the composer must stamp the mandatory RFC
// 5322 Date originator field itself.
func TestPinEmailMessage_HasRFC5322DateHeader(t *testing.T) {
	msg := pinEmailMessage(
		"noreply@openova.io", "user@example.com",
		"Your OpenOva sign-in code: 123456",
		"plain body", "<p>html body</p>", "deadbeefboundary",
	)

	parsed, err := mail.ReadMessage(strings.NewReader(msg))
	if err != nil {
		t.Fatalf("composed message does not parse as RFC 5322: %v", err)
	}
	if _, err := parsed.Header.Date(); err != nil {
		t.Errorf("Date header missing or unparseable: %v (got %q)", err, parsed.Header.Get("Date"))
	}
	// The multipart contract must survive the refactor.
	if ct := parsed.Header.Get("Content-Type"); !strings.Contains(ct, "multipart/alternative") || !strings.Contains(ct, "deadbeefboundary") {
		t.Errorf("Content-Type lost multipart contract: %q", ct)
	}
}
