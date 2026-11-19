package storefront

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mailbox reads the sign-in code that the sign-in service mailed to one
// address of the gate lane's mailbox. It speaks the few IMAP commands it
// needs (RFC 9051) over implicit TLS: LOGIN, EXAMINE INBOX (read-only),
// UID SEARCH by recipient and subject, UID FETCH BODY.PEEK[TEXT] and LOGOUT.
// It changes nothing in the mailbox: EXAMINE opens it read-only and PEEK sets
// no flag. The password is never printed.
type Mailbox struct {
	Addr     string // host:port of the IMAP server, implicit TLS
	User     string
	Password string
	// ServerName is the name the server's certificate is checked against.
	// Empty: the host of Addr. A reader that reaches the server through a
	// port forward dials a local address and names the server here.
	ServerName string
	// Dial opens the connection. Nil: TLS to Addr, the certificate checked
	// against ServerName.
	Dial func(ctx context.Context, addr string) (net.Conn, error)
}

// ErrNoMail means the mailbox holds no sign-in mail for the address yet.
var ErrNoMail = errors.New("no sign-in mail for the address yet")

// codeSubject is the subject of the mail of the sign-in service
// (core/services/auth/handlers/handlers.go, sendCodeEmail).
const codeSubject = "Your login code"

// The code in the text mail ("Your login code: 123456") or, in the mail of
// the notification service, alone in an element.
var (
	codeInText = regexp.MustCompile(`(?i)login code:\s*(\d{6})\b`)
	codeInHTML = regexp.MustCompile(`>\s*(\d{6})\s*<`)
)

// CodeFrom returns the six-digit code of a sign-in mail body, or "".
func CodeFrom(body string) string {
	body = strings.ReplaceAll(body, "=\r\n", "") // quoted-printable soft breaks
	for _, re := range []*regexp.Regexp{codeInText, codeInHTML} {
		if m := re.FindStringSubmatch(body); m != nil {
			return m[1]
		}
	}
	return ""
}

// Code returns the code of the newest sign-in mail sent to address, or
// ErrNoMail when there is none.
func (m Mailbox) Code(ctx context.Context, address string) (string, error) {
	dial := m.Dial
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			d := tls.Dialer{Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: m.ServerName}}
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	conn, err := dial(ctx, m.Addr)
	if err != nil {
		return "", fmt.Errorf("imap %s: %w", m.Addr, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	s := &imapSession{r: bufio.NewReader(conn), w: conn}
	if line, err := s.line(); err != nil || !strings.HasPrefix(line, "* OK") {
		return "", fmt.Errorf("imap %s: no greeting: %q %v", m.Addr, line, err)
	}
	if _, _, err := s.cmd("LOGIN " + quote(m.User) + " " + quote(m.Password)); err != nil {
		return "", fmt.Errorf("imap %s: LOGIN: %w", m.Addr, err)
	}
	defer func() { _, _, _ = s.cmd("LOGOUT") }()
	if _, _, err := s.cmd("EXAMINE INBOX"); err != nil {
		return "", fmt.Errorf("imap %s: EXAMINE INBOX: %w", m.Addr, err)
	}
	lines, _, err := s.cmd("UID SEARCH TO " + quote(address) + " SUBJECT " + quote(codeSubject))
	if err != nil {
		return "", fmt.Errorf("imap %s: UID SEARCH: %w", m.Addr, err)
	}
	newest := uint64(0)
	for _, l := range lines {
		if !strings.HasPrefix(l, "* SEARCH") {
			continue
		}
		for _, f := range strings.Fields(l)[2:] {
			if n, err := strconv.ParseUint(f, 10, 32); err == nil && n > newest {
				newest = n
			}
		}
	}
	if newest == 0 {
		return "", ErrNoMail
	}
	_, literal, err := s.cmd(fmt.Sprintf("UID FETCH %d BODY.PEEK[TEXT]", newest))
	if err != nil {
		return "", fmt.Errorf("imap %s: UID FETCH: %w", m.Addr, err)
	}
	code := CodeFrom(string(literal))
	if code == "" {
		return "", fmt.Errorf("imap %s: the newest sign-in mail to %s (uid %d) holds no six-digit code", m.Addr, address, newest)
	}
	return code, nil
}

// quote writes s as an IMAP quoted string.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

type imapSession struct {
	r   *bufio.Reader
	w   io.Writer
	tag int
}

// line reads one line without its CRLF.
func (s *imapSession) line() (string, error) {
	l, err := s.r.ReadString('\n')
	return strings.TrimRight(l, "\r\n"), err
}

// literalSize is the size of the literal that ends a line ("{123}").
var literalSize = regexp.MustCompile(`\{(\d+)\}$`)

// cmd sends one tagged command and reads up to its tagged answer. It returns
// the untagged lines and the bytes of the literals they carried. An answer
// other than OK is an error.
func (s *imapSession) cmd(c string) ([]string, []byte, error) {
	s.tag++
	tag := fmt.Sprintf("a%d", s.tag)
	if _, err := io.WriteString(s.w, tag+" "+c+"\r\n"); err != nil {
		return nil, nil, err
	}
	var lines []string
	var literal []byte
	for {
		l, err := s.line()
		if err != nil {
			return lines, literal, err
		}
		if m := literalSize.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n > 4<<20 {
				return lines, literal, fmt.Errorf("a literal of %d bytes", n)
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(s.r, b); err != nil {
				return lines, literal, err
			}
			literal = append(literal, b...)
		}
		if rest, ok := strings.CutPrefix(l, tag+" "); ok {
			if !strings.HasPrefix(rest, "OK") {
				return lines, literal, errors.New(rest)
			}
			return lines, literal, nil
		}
		lines = append(lines, l)
	}
}
