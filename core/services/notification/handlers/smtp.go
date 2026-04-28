package handlers

import (
	"fmt"
	"net/smtp"
	"strings"
)

// Mailer sends HTML emails via SMTP.
type Mailer struct {
	Host string
	Port string
	From string
}

// NewMailer creates a Mailer configured for the given SMTP server.
func NewMailer(host, port, from string) *Mailer {
	return &Mailer{
		Host: host,
		Port: port,
		From: from,
	}
}

// Send delivers an HTML email to the given recipient.
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

	return smtp.SendMail(addr, nil, m.From, []string{to}, msg)
}
