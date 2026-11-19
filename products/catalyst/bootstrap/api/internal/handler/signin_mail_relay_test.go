package handler

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/signinmail"
)

// testRelay is an SMTP submission relay on 127.0.0.1 that accepts mail only
// after AUTH PLAIN with the one credential it issued.
type testRelay struct {
	addr, user, pass string
	mu               sync.Mutex
	from, rcpt, data []string
}

func startTestRelay(t *testing.T, user, pass string) *testRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	r := &testRelay{addr: ln.Addr().String(), user: user, pass: pass}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go r.serve(conn)
		}
	}()
	return r
}

func (r *testRelay) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	in := bufio.NewReader(conn)
	say := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	say("220 relay.test ESMTP")
	authed := false
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			say("250-relay.test")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(upper, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN "):]))
			if string(raw) == "\x00"+r.user+"\x00"+r.pass {
				authed = true
				say("235 2.7.0 accepted")
			} else {
				say("535 5.7.8 credential not issued for this sender")
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if !authed {
				say("530 5.7.0 authentication required")
				continue
			}
			r.mu.Lock()
			r.from = append(r.from, cmd[len("MAIL FROM:"):])
			r.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			r.mu.Lock()
			r.rcpt = append(r.rcpt, cmd[len("RCPT TO:"):])
			r.mu.Unlock()
			say("250 ok")
		case upper == "DATA":
			say("354 go on")
			var body strings.Builder
			for {
				l, err := in.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				body.WriteString(l)
			}
			r.mu.Lock()
			r.data = append(r.data, body.String())
			r.mu.Unlock()
			say("250 queued")
		case upper == "QUIT":
			say("221 bye")
			return
		default:
			say("501 not understood")
		}
	}
}

func (r *testRelay) delivered() (from, rcpt, data []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.from...), append([]string(nil), r.rcpt...), append([]string(nil), r.data...)
}

// relaySovereign makes signinmail read a Sovereign in relay mode whose relay
// is addr and whose relay Secret holds user and pass. The Secret of a local
// mail server and the environment name another server and credential.
func relaySovereign(t *testing.T, addr, user, pass string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("relay address: %v", err)
	}
	vars := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: upstream.Namespace, Name: upstream.Name},
		Data: map[string]string{"MAIL_MODE": "relay", "MAIL_RELAY_HOST": host, "MAIL_RELAY_PORT": port,
			"MAIL_FROM": "noreply@t2.example.test", "MAIL_RELAY_SECRET": "sovereign-mail-relay"},
	}
	relay := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: signinmail.Namespace, Name: "sovereign-mail-relay"},
		Data:       map[string][]byte{"smtp-user": []byte(user), "smtp-pass": []byte(pass)},
	}
	local := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: signinmail.Namespace, Name: signinmail.ObjectName},
		Data: map[string][]byte{"smtp-host": []byte("mail.t2.example.test"), "smtp-user": []byte("noreply"),
			"smtp-pass": []byte("local-server")},
	}
	prev := signinmail.ClientFor
	signinmail.ClientFor = func() (kubernetes.Interface, error) { return fake.NewSimpleClientset(vars, relay, local), nil }
	t.Cleanup(func() { signinmail.ClientFor = prev })
	t.Setenv("CATALYST_SMTP_HOST", "env.example.test")
	t.Setenv("CATALYST_SMTP_USER", "env-user")
	t.Setenv("CATALYST_SMTP_PASS", "env-pass")
}

// TestSendPinEmail_ThroughRelay (SOV-323): on a Sovereign whose variables say
// MAIL_MODE relay, the sign-in code is submitted to the relay, signed in with
// the credential the relay issued for this Sovereign, from MAIL_FROM. A
// credential issued for another Sovereign is refused and delivers nothing.
func TestSendPinEmail_ThroughRelay(t *testing.T) {
	relay := startTestRelay(t, "t2", "issued-for-t2")

	relaySovereign(t, relay.addr, "t2", "issued-for-t2")
	if err := sendPinEmailDefault("owner@t2.example.test", "482913"); err != nil {
		t.Fatalf("send through the relay: %v", err)
	}
	from, rcpt, data := relay.delivered()
	if len(data) != 1 || !strings.Contains(data[0], "482913") {
		t.Fatalf("the relay holds %d messages, want one with the code: %q", len(data), data)
	}
	if from[0] != "<noreply@t2.example.test>" || rcpt[0] != "<owner@t2.example.test>" {
		t.Errorf("envelope from %q to %q, want MAIL_FROM to the owner", from[0], rcpt[0])
	}

	relaySovereign(t, relay.addr, "t3", "issued-for-t3")
	if err := sendPinEmailDefault("owner@t2.example.test", "735204"); err == nil {
		t.Fatal("a credential issued for another Sovereign was accepted")
	}
	if _, _, data := relay.delivered(); len(data) != 1 {
		t.Errorf("the relay holds %d messages after the refused credential, want 1", len(data))
	}
}
