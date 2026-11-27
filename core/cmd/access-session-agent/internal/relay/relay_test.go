package relay

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/config"
	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/handshake"
)

const uid = "5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11"

// fakeGuacd accepts connections on the loopback address. For each it answers
// the handshake (args: a version, recording-name, hostname; then ready and
// sync), reports the connect values, forwards every later instruction to got,
// and ends the connection when the test closes end.
type fakeGuacd struct {
	ln       net.Listener
	connects chan []string
	got      chan string
	end      chan struct{}
	accepted int
	mu       sync.Mutex
}

func newFakeGuacd(t *testing.T, answer string) *fakeGuacd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGuacd{ln: ln, connects: make(chan []string, 4), got: make(chan string, 16), end: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.accepted++
			f.mu.Unlock()
			go f.serve(c, answer)
		}
	}()
	return f
}

func (f *fakeGuacd) serve(c net.Conn, answer string) {
	defer func() { _ = c.Close() }()
	r := handshake.NewReader(c)
	go func() { <-f.end; _ = c.Close() }()
	for {
		raw, el, err := r.Next(true)
		if err != nil {
			return
		}
		switch el[0] {
		case "select":
			_, _ = io.WriteString(c, handshake.Encode("args", "VERSION_1_5_0", "recording-name", "hostname"))
		case "size", "audio", "video", "image", "timezone", "name":
		case "connect":
			f.connects <- el[1:]
			_, _ = io.WriteString(c, answer)
		default:
			f.got <- string(raw)
		}
	}
}

func (f *fakeGuacd) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accepted
}

type ended struct {
	n   int
	how End
}

func server(t *testing.T, g *fakeGuacd) (*Server, *httptest.Server, chan int, chan ended) {
	t.Helper()
	begun, finished := make(chan int, 4), make(chan ended, 4)
	s := &Server{
		Config: &config.Config{SessionUID: uid, Kind: "ssh", Protocol: "ssh", Subject: "jane.roe@example.org",
			Params: map[string]string{"hostname": "db-1.acme.internal"}, ChunkBytes: 1024, ChunkSeconds: 5},
		CredDir: t.TempDir(), Now: time.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", g.ln.Addr().String())
		},
		Begin:  func(n int) { begun <- n },
		Finish: func(n int, how End, _ error) { finished <- ended{n, how} },
	}
	h := httptest.NewServer(s)
	t.Cleanup(h.Close)
	return s, h, begun, finished
}

const query = "/tunnel?width=1024&height=768&dpi=96&timezone=Asia/Muscat"

func dial(t *testing.T, h *httptest.Server, path, id string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.URL, "http")+path, http.Header{UIDHeader: {id}})
}

func wait[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived in 5 seconds")
	}
	panic("unreachable")
}

func TestTunnelRefusesBeforeGuacd(t *testing.T) {
	g := newFakeGuacd(t, "5.ready,4.$abc;")
	_, h, _, _ := server(t, g)
	for _, tc := range []struct {
		path, id string
		status   int
	}{{query, "7c1d0e3a-0000-4000-8000-000000000000", http.StatusForbidden},
		{"/tunnel?width=1024&height=768&timezone=UTC", uid, http.StatusBadRequest},
		{"/tunnel?width=1024&height=768&dpi=96&timezone=..%2Fetc", uid, http.StatusBadRequest}} {
		_, resp, err := dial(t, h, tc.path, tc.id)
		if err == nil || resp == nil || resp.StatusCode != tc.status {
			t.Fatalf("%s as %s: got %v, %v, want %d", tc.path, tc.id, resp, err, tc.status)
		}
	}
	if n := g.count(); n != 0 {
		t.Fatalf("guacd got %d connections from refused tunnels", n)
	}
}

func TestTunnelRelaysAndCountsConnections(t *testing.T) {
	g := newFakeGuacd(t, "5.ready,4.$abc;4.sync,1.1;")
	_, h, begun, finished := server(t, g)
	ws, _, err := dial(t, h, query, uid)
	if err != nil {
		t.Fatal(err)
	}
	if n := wait(t, begun); n != 1 {
		t.Fatalf("the first connection is number %d", n)
	}
	if c := wait(t, g.connects); !slices.Equal(c, []string{"VERSION_1_5_0", "screen-1", "db-1.acme.internal"}) {
		t.Fatalf("connect of connection 1 sent %q", c)
	}
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "4.sync,1.1;" {
		t.Fatalf("the first message is %q (%v), want guacd's first instruction after ready", msg, err)
	}
	if _, resp, err := dial(t, h, query, uid); err == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second tunnel beside the first got %v, %v, want 409", resp, err)
	}
	key := "3.key,5.65307,1.1;"
	if err := ws.WriteMessage(websocket.TextMessage, []byte(key)); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, g.got); got != key {
		t.Fatalf("guacd got %q, want %q unchanged", got, key)
	}
	_ = ws.Close()
	if e := wait(t, finished); e != (ended{1, Dropped}) {
		t.Fatalf("the broker's close ended the connection as %+v, want 1 dropped", e)
	}
	ws, _, err = dial(t, h, query, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	if n := wait(t, begun); n != 2 {
		t.Fatalf("the reconnect is number %d, want 2", n)
	}
	if c := wait(t, g.connects); c[1] != "screen-2" {
		t.Fatalf("connect of connection 2 sent %q", c)
	}
	close(g.end)
	if e := wait(t, finished); e != (ended{2, GuacdEnded}) {
		t.Fatalf("guacd's end ended the connection as %+v, want 2 guacd-ended", e)
	}
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				t.Fatalf("the broker's socket ended with %v, want a normal close", err)
			}
			break
		}
	}
}

func TestTunnelHandshakeFailure(t *testing.T) {
	g := newFakeGuacd(t, "5.error,23.Unable to start session,3.519;")
	_, h, _, finished := server(t, g)
	if _, resp, err := dial(t, h, query, uid); err == nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("got %v, %v, want 502", resp, err)
	}
	if e := wait(t, finished); e != (ended{1, Refused}) {
		t.Fatalf("got %+v, want 1 refused", e)
	}
}

func TestCloseEndsTheTunnel(t *testing.T) {
	g := newFakeGuacd(t, "5.ready,4.$abc;")
	s, h, begun, finished := server(t, g)
	ws, _, err := dial(t, h, query, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	wait(t, begun)
	s.Close()
	if e := wait(t, finished); e != (ended{1, Shutdown}) {
		t.Fatalf("got %+v, want 1 shutdown", e)
	}
	if _, resp, err := dial(t, h, query, uid); err == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("a tunnel after Close got %v, %v, want 409", resp, err)
	}
}

func TestHealthz(t *testing.T) {
	g := newFakeGuacd(t, "")
	_, h, _, _ := server(t, g)
	get := func() int {
		resp, err := http.Get(h.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("with guacd listening /healthz is %d", code)
	}
	_ = g.ln.Close()
	if code := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("with guacd gone /healthz is %d", code)
	}
}
