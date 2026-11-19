package storefront

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

// imapServer answers as an IMAP server with the mails in box: UID -> recipient
// and text. It keeps every command it got. A password other than "pw" is refused.
type imapServer struct {
	box  map[int][2]string
	got  []string
	fail string // the command whose answer is NO
}

func (s *imapServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	_, _ = fmt.Fprint(conn, "* OK IMAP4rev2 ready\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.got = append(s.got, line)
		tag, cmd, _ := strings.Cut(line, " ")
		switch {
		case s.fail != "" && strings.HasPrefix(cmd, s.fail):
			_, _ = fmt.Fprintf(conn, "%s NO refused\r\n", tag)
		case strings.HasPrefix(cmd, "LOGIN ") && !strings.HasSuffix(cmd, `"pw"`):
			_, _ = fmt.Fprintf(conn, "%s NO [AUTHENTICATIONFAILED] invalid credentials\r\n", tag)
		case strings.HasPrefix(cmd, "UID SEARCH TO "):
			uids := []string{}
			for uid := 1; uid <= len(s.box); uid++ {
				if m := s.box[uid]; strings.Contains(cmd, `TO "`+m[0]+`"`) {
					uids = append(uids, fmt.Sprint(uid))
				}
			}
			_, _ = fmt.Fprintf(conn, "* SEARCH %s\r\n%s OK done\r\n", strings.Join(uids, " "), tag)
		case strings.HasPrefix(cmd, "UID FETCH "):
			var uid int
			_, _ = fmt.Sscanf(cmd, "UID FETCH %d", &uid)
			text := s.box[uid][1]
			_, _ = fmt.Fprintf(conn, "* 1 FETCH (UID %d BODY[TEXT] {%d}\r\n%s)\r\n%s OK done\r\n", uid, len(text), text, tag)
		case cmd == "LOGOUT":
			_, _ = fmt.Fprintf(conn, "* BYE\r\n%s OK bye\r\n", tag)
			return
		default:
			_, _ = fmt.Fprintf(conn, "%s OK done\r\n", tag)
		}
	}
}

func (s *imapServer) mailbox(password string) Mailbox {
	return Mailbox{Addr: "imap.test:993", User: "gate", Password: password,
		Dial: func(context.Context, string) (net.Conn, error) {
			client, server := net.Pipe()
			go s.serve(server)
			return client, nil
		}}
}

func TestMailboxReadsTheNewestCode(t *testing.T) {
	s := &imapServer{box: map[int][2]string{
		1: {"gate+a@mail.test", "Your login code: 111111\r\n"},
		2: {"gate+b@mail.test", "Your login code: 222222\r\n"},
		3: {"gate+a@mail.test", "Your login code: 333333\r\n"},
	}}
	code, err := s.mailbox("pw").Code(context.Background(), "gate+a@mail.test")
	if err != nil || code != "333333" {
		t.Fatalf("code %q err %v, want 333333", code, err)
	}
	want := []string{`a1 LOGIN "gate" "pw"`, "a2 EXAMINE INBOX", `a3 UID SEARCH TO "gate+a@mail.test" SUBJECT "Your login code"`,
		"a4 UID FETCH 3 BODY.PEEK[TEXT]", "a5 LOGOUT"}
	if strings.Join(s.got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant (read-only, nothing marked seen):\n%s", strings.Join(s.got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMailboxFailures(t *testing.T) {
	ctx := context.Background()
	s := &imapServer{box: map[int][2]string{1: {"gate+a@mail.test", "Hello, no code here"}}}
	if _, err := s.mailbox("pw").Code(ctx, "gate+z@mail.test"); !errors.Is(err, ErrNoMail) {
		t.Errorf("no mail to the address: want ErrNoMail, got %v", err)
	}
	if _, err := s.mailbox("wrong").Code(ctx, "gate+a@mail.test"); err == nil || !strings.Contains(err.Error(), "LOGIN: NO [AUTHENTICATIONFAILED]") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("a refused login: want the server's answer without the password, got %v", err)
	}
	if _, err := s.mailbox("pw").Code(ctx, "gate+a@mail.test"); err == nil || !strings.Contains(err.Error(), "holds no six-digit code") {
		t.Errorf("a mail without a code: got %v", err)
	}
	s.fail = "EXAMINE"
	if _, err := s.mailbox("pw").Code(ctx, "gate+a@mail.test"); err == nil || !strings.Contains(err.Error(), "EXAMINE INBOX: NO refused") {
		t.Errorf("a refused EXAMINE: got %v", err)
	}
}

func TestCodeFrom(t *testing.T) {
	for body, want := range map[string]string{
		"Your login code: 123456\r\n": "123456",
		"<h2 style=\"font-size:22px;\">Your login code</h2><span style=\"color:#3b82f6;\">=\r\n654321</span>": "654321",
		"Your login code: 12345\r\n":  "",
		"color:#123456; nothing else": "",
	} {
		if got := CodeFrom(body); got != want {
			t.Errorf("CodeFrom(%q) = %q, want %q", body, got, want)
		}
	}
}
