package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/handshake"
)

const uid = "5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11"

// args are the parameter names the fake guacd asks for, in its order.
var args = []string{"VERSION_1_5_0", "hostname", "port", "namespace", "pod", "container", "exec-command", "use-ssl",
	"client-cert", "client-key", "ca-cert", "recording-path", "recording-name", "create-recording-path", "recording-include-keys",
	"typescript-path", "typescript-name", "create-typescript-path", "disable-copy", "disable-paste", "read-only"}

// recordedFiles is what the fake guacd writes into the recording folder.
var recordedFiles = map[string]string{
	"screen-1":            strings.Repeat("4.sync,13.1763647391000;", 9),
	"typescript-1":        strings.Repeat("$ ls -la\r\ntotal 0\r\n", 4),
	"typescript-1.timing": "0.104 9\n0.051 9\n0.230 9\n0.012 9\n",
}

type world struct {
	dir, rec, creds, certPEM string
	connects                 chan []string
	keys                     chan string
	mu                       sync.Mutex
	calls                    []call
	ingest                   *httptest.Server
	guacd                    net.Listener
}

type call struct{ method, path, sum, body string }

// newWorld starts a fake guacd that answers the handshake, writes the
// recording files, takes one instruction and ends the connection; and a fake
// ingest that takes every request.
func newWorld(t *testing.T, chunkBytes int) *world {
	t.Helper()
	w := &world{dir: t.TempDir(), connects: make(chan []string, 1), keys: make(chan string, 1)}
	w.rec, w.creds = filepath.Join(w.dir, "rec"), filepath.Join(w.dir, "creds")
	for _, d := range []string{w.rec, w.creds} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(0x0a0b), Subject: pkix.Name{CommonName: "session-" + uid},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(5 * time.Minute)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	w.certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	for name, text := range map[string]string{"exec-cert": w.certPEM, "exec-key": "KEY\n", "exec-ca": "CA\n"} {
		w.write(t, filepath.Join(w.creds, name), text)
	}
	w.ingest = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.calls = append(w.calls, call{r.Method, r.URL.Path, r.Header.Get("X-Chunk-SHA256"), string(b)})
		w.mu.Unlock()
		rw.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(w.ingest.Close)
	if w.guacd, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.guacd.Close() })
	go func() {
		for {
			c, err := w.guacd.Accept()
			if err != nil {
				return
			}
			go w.serve(t, c)
		}
	}()
	cfg := map[string]any{"schema": "openova.io/access-session-config/v1", "sessionUID": uid, "kind": "pod",
		"subject": "jane.roe@example.org", "protocol": "kubernetes",
		"params": map[string]string{"hostname": "k8s-ws-proxy.catalyst-system.svc.cluster.local", "port": "8443", "use-ssl": "true",
			"namespace": "acme", "pod": "web-0", "container": "main", "exec-command": "/bin/sh"},
		"paramFiles": map[string]string{"client-cert": "exec-cert", "client-key": "exec-key", "ca-cert": "exec-ca"},
		"controls":   map[string]bool{"keystrokes": false, "clipboardCopy": false, "clipboardPaste": true},
		"terminal":   true, "ingest": w.ingest.URL, "chunkBytes": chunkBytes, "chunkSeconds": map[bool]int{true: 1, false: 0}[chunkBytes > 0]}
	b, _ := json.Marshal(cfg)
	w.write(t, filepath.Join(w.dir, "config.json"), string(b))
	return w
}

func (w *world) write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// serve plays guacd on one connection. A connection that sends nothing (the
// agent's check that guacd listens) is closed.
func (w *world) serve(t *testing.T, c net.Conn) {
	defer func() { _ = c.Close() }()
	r := handshake.NewReader(c)
	for {
		_, el, err := r.Next(true)
		if err != nil {
			return
		}
		switch el[0] {
		case "select":
			_, _ = io.WriteString(c, handshake.Encode(append([]string{"args"}, args...)...))
		case "connect":
			w.connects <- el[1:]
			for name, text := range recordedFiles { // guacd creates the files at connect, as spike ACC-020 item (e) saw
				w.write(t, filepath.Join(w.rec, name), text[:len(text)/2])
			}
			_, _ = io.WriteString(c, "5.ready,4.$abc;4.sync,1.1;")
		case "key":
			w.keys <- handshake.Encode(el...)
			for name, text := range recordedFiles {
				f, err := os.OpenFile(filepath.Clean(filepath.Join(w.rec, name)), os.O_APPEND|os.O_WRONLY, 0o600)
				if err == nil {
					_, _ = f.WriteString(text[len(text)/2:])
					_ = f.Close()
				}
			}
			return // guacd ends the connection: the shell exited
		}
	}
}

func (w *world) seen() []call {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

func (w *world) run(t *testing.T) (net.Listener, <-chan int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	code := make(chan int, 1)
	go func() {
		code <- run(context.Background(), options{config: filepath.Join(w.dir, "config.json"), guacd: w.guacd.Addr().String(),
			rec: w.rec, creds: w.creds, listeners: []net.Listener{ln}, pause: 10 * time.Millisecond, giveUp: 2 * time.Second, settle: 100 * time.Millisecond,
			used: func() (float64, error) { return 0.1, nil }, log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	return ln, code
}

// session opens the tunnel, wants guacd's first instruction after ready,
// types one key and reads until guacd's end closes the socket.
func (w *world) session(t *testing.T, ln net.Listener) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(w.seen()) == 0 && time.Now().Before(deadline) {
		<-time.After(10 * time.Millisecond)
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/tunnel?width=1024&height=768&dpi=96&timezone=UTC",
		http.Header{"X-Access-Session-UID": {uid}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "4.sync,1.1;" {
		t.Fatalf("the first message is %q (%v), want guacd's first instruction after ready", msg, err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte("3.key,5.65293,1.1;")); err != nil {
		t.Fatal(err)
	}
	if got := <-w.keys; got != "3.key,5.65293,1.1;" {
		t.Fatalf("guacd got %q", got)
	}
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
	}
}

func exitCode(t *testing.T, code <-chan int) int {
	t.Helper()
	select {
	case c := <-code:
		return c
	case <-time.After(15 * time.Second):
		t.Fatal("the agent did not end in 15 seconds")
	}
	return -1
}

// TestChain runs handshake, relay and shipper against the fake guacd and the
// fake ingest: ready first, then the chunks whose bytes are the files guacd
// wrote, in order and with their digests (the chain's inputs), then closed
// with reason user.
func TestChain(t *testing.T) {
	w := newWorld(t, 64)
	ln, code := w.run(t)
	w.session(t, ln)
	if c := exitCode(t, code); c != 0 {
		t.Fatalf("exit code %d", c)
	}
	want := []string{"VERSION_1_5_0", "k8s-ws-proxy.catalyst-system.svc.cluster.local", "8443", "acme", "web-0", "main", "/bin/sh", "true",
		strings.TrimSuffix(w.certPEM, "\n"), "KEY", "CA", "/rec", "screen-1", "true", "false", "/rec", "typescript-1", "true", "true", "false", ""}
	if got := <-w.connects; !slices.Equal(got, want) {
		t.Fatalf("connect sent\n%q\nwant\n%q", got, want)
	}
	calls, base := w.seen(), "/internal/v1/sessions/"+uid
	if first := calls[0]; first != (call{"POST", base + "/ready", "", `{"credentialID":"0a:0b"}`}) {
		t.Fatalf("the first call is %+v, want ready with the certificate's serial", first)
	}
	if last := calls[len(calls)-1]; last != (call{"POST", base + "/closed", "", `{"reason":"user"}`}) {
		t.Fatalf("the last call is %+v, want closed with reason user", last)
	}
	streams := map[string]string{"screen-1": "screen-1", "typescript-1": "typescript-1", "typescript-timing-1": "typescript-1.timing"}
	for stream, file := range streams {
		var joined strings.Builder
		n := 0
		for _, c := range calls[1 : len(calls)-1] {
			seq, ok := strings.CutPrefix(c.path, base+"/streams/"+stream+"/chunks/")
			if !ok {
				continue
			}
			n++
			sum := sha256.Sum256([]byte(c.body))
			if c.method != "PUT" || seq != strconv.Itoa(n) || c.sum != hex.EncodeToString(sum[:]) || len(c.body) > 64 || len(c.body) == 0 {
				t.Fatalf("chunk %d of %s: %s %s, digest %s, %d bytes", n, stream, c.method, c.path, c.sum, len(c.body))
			}
			joined.WriteString(c.body)
		}
		if joined.String() != recordedFiles[file] {
			t.Fatalf("stream %s shipped %q, want the file %s, %q", stream, joined.String(), file, recordedFiles[file])
		}
	}
}

// TestRecordingOff: with chunkBytes 0 the handshake sends no recording or
// typescript parameter and nothing is shipped.
func TestRecordingOff(t *testing.T) {
	w := newWorld(t, 0)
	ln, code := w.run(t)
	w.session(t, ln)
	if c := exitCode(t, code); c != 0 {
		t.Fatalf("exit code %d", c)
	}
	got := <-w.connects
	if want := []string{"", "", "", "", "", "", "", "true", "false", ""}; !slices.Equal(got[11:], want) {
		t.Fatalf("the recording parameters are %q, want %q", got[11:], want)
	}
	calls := w.seen()
	if len(calls) != 2 || !strings.HasSuffix(calls[0].path, "/ready") || calls[1].body != `{"reason":"user"}` {
		t.Fatalf("the calls are %+v, want ready and closed only", calls)
	}
}

func TestBadConfigurationExits78(t *testing.T) {
	w := newWorld(t, 0)
	w.write(t, filepath.Join(w.dir, "config.json"), `{"schema": "openova.io/access-session-config/v1"}`)
	_, code := w.run(t)
	if c := exitCode(t, code); c != exitConfig {
		t.Fatalf("exit code %d, want %d", c, exitConfig)
	}
	if calls := w.seen(); len(calls) != 0 {
		t.Fatalf("an agent without a configuration called %+v", calls)
	}
}
