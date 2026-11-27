package handshake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"maps"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/config"
)

func TestEncodeCountsCodePoints(t *testing.T) {
	if got, want := Encode("name", "jane.roé@例.org"), "4.name,14.jane.roé@例.org;"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := Encode("audio"), "5.audio;"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReaderReadsSplitInstructions(t *testing.T) {
	wire := "4.args,13.VERSION_1_5_0,8.hostname;4.name,14.jane.roé@例.org;0.,4.ping,3.123;"
	r := NewReader(iotest.OneByteReader(strings.NewReader(wire)))
	want := [][]string{{"args", "VERSION_1_5_0", "hostname"}, {"name", "jane.roé@例.org"}, {"", "ping", "123"}}
	var raws []string
	for _, w := range want {
		raw, el, err := r.Next(true)
		if err != nil || !slices.Equal(el, w) {
			t.Fatalf("got %q, %v, want %q", el, err, w)
		}
		raws = append(raws, string(raw))
	}
	if strings.Join(raws, "") != wire {
		t.Fatalf("the raw instructions %q are not the wire %q", raws, wire)
	}
	if _, _, err := r.Next(true); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last instruction got %v, want io.EOF", err)
	}
}

func TestReaderRefuses(t *testing.T) {
	for wire, want := range map[string]string{
		"4.size,x.1;":    "bad element length",
		"4.size:":        "bad terminator",
		"4.siz":          io.ErrUnexpectedEOF.Error(),
		"1.\x80;":        "not valid UTF-8",
		"99999999.a;":    "bad element length",
		"123456789012.a": "no element length",
	} {
		if _, _, err := NewReader(strings.NewReader(wire)).Next(false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", wire, err, want)
		}
	}
}

// guacd plays guacd on the other end of a pipe: it answers select with args
// and connect with ready, sends sync in the same write as ready, and records
// every instruction it received.
func guacd(t *testing.T, args []string, answer string) (net.Conn, <-chan [][]string) {
	t.Helper()
	agent, srv := net.Pipe()
	got := make(chan [][]string, 1)
	go func() {
		defer func() { _ = srv.Close() }()
		var seen [][]string
		defer func() { got <- seen }()
		r := NewReader(srv)
		for {
			_, el, err := r.Next(true)
			if err != nil {
				return
			}
			seen = append(seen, el)
			switch el[0] {
			case "select":
				if _, err := io.WriteString(srv, Encode(append([]string{"args"}, args...)...)); err != nil {
					return
				}
			case "connect":
				if _, err := io.WriteString(srv, answer); err != nil {
					return
				}
			}
		}
	}()
	return agent, got
}

const readyAndSync = "5.ready,37.$260d01da-779b-4ee9-afc1-c16bae885cc7;4.sync,1.1;"

func TestRunPerProtocol(t *testing.T) {
	params := map[string]string{"hostname": "k8s-ws-proxy.catalyst-system.svc.cluster.local", "port": "8443", "namespace": "acme",
		"pod": "web-0", "container": "main", "client-cert": "CERT", "recording-name": "screen-1", "username": "ro"}
	cases := []struct {
		protocol string
		args     []string
		connect  []string
	}{
		{"kubernetes", []string{"VERSION_1_5_0", "hostname", "port", "namespace", "pod", "container", "exec-command", "client-cert", "recording-name", "read-only"},
			[]string{"connect", "VERSION_1_5_0", "k8s-ws-proxy.catalyst-system.svc.cluster.local", "8443", "acme", "web-0", "main", "", "CERT", "screen-1", ""}},
		{"ssh", []string{"VERSION_1_5_0", "hostname", "host-key", "port", "username", "private-key", "public-key", "recording-name", "enable-sftp"},
			[]string{"connect", "VERSION_1_5_0", "k8s-ws-proxy.catalyst-system.svc.cluster.local", "", "8443", "ro", "", "", "screen-1", ""}},
		{"vnc", []string{"hostname", "port", "password"},
			[]string{"connect", "k8s-ws-proxy.catalyst-system.svc.cluster.local", "8443", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			conn, got := guacd(t, tc.args, readyAndSync)
			r := NewReader(conn)
			res, err := Run(conn, r, tc.protocol, "jane.roe@example.org", Size{Width: 1024, Height: 768, DPI: 96, Timezone: "Asia/Muscat"}, params)
			if err != nil {
				t.Fatal(err)
			}
			raw, _, err := r.Next(false)
			if err != nil || string(raw) != "4.sync,1.1;" {
				t.Fatalf("the first instruction after ready is %q (%v), want guacd's sync", raw, err)
			}
			_ = conn.Close()
			want := [][]string{{"select", tc.protocol}, {"size", "1024", "768", "96"}, {"audio"}, {"video"},
				{"image", "image/png", "image/jpeg", "image/webp"}, {"timezone", "Asia/Muscat"}, {"name", "jane.roe@example.org"}, tc.connect}
			if seen := <-got; !slices.EqualFunc(seen, want, slices.Equal) {
				t.Fatalf("guacd received\n%q\nwant\n%q", seen, want)
			}
			if res.ID != "$260d01da-779b-4ee9-afc1-c16bae885cc7" {
				t.Fatalf("the connection identifier is %q", res.ID)
			}
		})
	}
}

func TestRunGuacdError(t *testing.T) {
	conn, _ := guacd(t, []string{"VERSION_1_5_0", "hostname"}, "5.error,23.Unable to start session,3.519;")
	_, err := Run(conn, NewReader(conn), "kubernetes", "jane.roe@example.org", Size{Width: 1, Height: 1, DPI: 96, Timezone: "UTC"}, nil)
	var ge *GuacdError
	if !errors.As(err, &ge) || ge.Status != "519" || ge.Message != "Unable to start session" {
		t.Fatalf("got %v, want guacd's error with status 519", err)
	}
}

// cert writes a PEM certificate with serial and validity end notAfter.
func cert(t *testing.T, serial int64, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "session-test"},
		NotBefore: notAfter.Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func write(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func podConfig(chunkBytes int, terminal bool) *config.Config {
	return &config.Config{Kind: "pod", SessionUID: "5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11", Protocol: "kubernetes",
		Params:     map[string]string{"hostname": "k8s-ws-proxy.catalyst-system.svc.cluster.local", "pod": "web-0"},
		ParamFiles: map[string]string{"client-cert": "exec-cert", "client-key": "exec-key"},
		Controls:   config.Controls{Keystrokes: true, ClipboardPaste: true}, Terminal: terminal, ChunkBytes: chunkBytes, ChunkSeconds: 5}
}

func TestParams(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	pemCert := cert(t, 7, now.Add(time.Hour))
	write(t, dir, "exec-cert", pemCert)
	write(t, dir, "exec-key", []byte("KEY-1\n\n"))
	base := map[string]string{"hostname": "k8s-ws-proxy.catalyst-system.svc.cluster.local", "pod": "web-0",
		"client-cert": strings.TrimSuffix(string(pemCert), "\n"), "client-key": "KEY-1\n", "disable-copy": "true", "disable-paste": "false"}
	recorded := maps.Clone(base)
	maps.Copy(recorded, map[string]string{"recording-path": "/rec", "recording-name": "screen-2", "create-recording-path": "true",
		"recording-include-keys": "true", "typescript-path": "/rec", "typescript-name": "typescript-2", "create-typescript-path": "true"})
	noTypescript := maps.Clone(recorded)
	for _, k := range []string{"typescript-path", "typescript-name", "create-typescript-path"} {
		delete(noTypescript, k)
	}
	for _, tc := range []struct {
		name string
		c    *config.Config
		want map[string]string
	}{{"recording off", podConfig(0, true), base}, {"recorded, terminal", podConfig(1024, true), recorded},
		{"recorded, no terminal", podConfig(1024, false), noTypescript}} {
		got, err := Params(tc.c, 2, dir, now)
		if err != nil || !maps.Equal(got, tc.want) {
			t.Errorf("%s: got %q, %v\nwant %q", tc.name, got, err, tc.want)
		}
	}
	write(t, dir, "exec-key", []byte("KEY-2\n"))
	if got, err := Params(podConfig(0, true), 3, dir, now); err != nil || got["client-key"] != "KEY-2" {
		t.Errorf("the key file is not read again at the next handshake: %q, %v", got["client-key"], err)
	}
	write(t, dir, "exec-cert", cert(t, 7, now.Add(-time.Minute)))
	if _, err := Params(podConfig(0, true), 4, dir, now); !errors.Is(err, ErrCredentialExpired) {
		t.Errorf("an expired certificate gave %v, want ErrCredentialExpired", err)
	}
	if _, err := Params(podConfig(0, true), 5, t.TempDir(), now); err == nil {
		t.Error("a missing credential file gave no error")
	}
}

func TestCredentialID(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "exec-cert", cert(t, 0x0102ab, time.Now().Add(time.Hour)))
	write(t, dir, "db-username", []byte("v-kubernet-acme.acce-8sXq2LrPzK-1763647391\n"))
	pod := podConfig(0, true)
	ssh, db, other := *pod, *pod, *pod
	ssh.Kind, db.Kind, other.Kind = "ssh", "database", "web"
	for c, want := range map[*config.Config]string{pod: "01:02:ab", &ssh: "5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11",
		&db: "v-kubernet-acme.acce-8sXq2LrPzK-1763647391"} {
		if got, err := CredentialID(c, dir); err != nil || got != want {
			t.Errorf("kind %s: got %q, %v, want %q", c.Kind, got, err, want)
		}
	}
	if _, err := CredentialID(&other, dir); err == nil {
		t.Error("kind web gave a credential identifier")
	}
	if _, err := CredentialID(&db, t.TempDir()); err == nil {
		t.Error("kind database without the user file gave a credential identifier")
	}
}
