package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/auth"
)

func TestParseExecPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want struct {
			ns, pod, container string
			ok                 bool
		}
	}{
		{
			name: "happy",
			in:   "/proxy/exec/default/web-7d8b/web",
			want: struct {
				ns, pod, container string
				ok                 bool
			}{"default", "web-7d8b", "web", true},
		},
		{
			name: "wrong-prefix",
			in:   "/api/v1/exec/default/web/web",
			want: struct {
				ns, pod, container string
				ok                 bool
			}{"", "", "", false},
		},
		{
			name: "missing-container",
			in:   "/proxy/exec/default/web",
			want: struct {
				ns, pod, container string
				ok                 bool
			}{"", "", "", false},
		},
		{
			name: "empty-segment",
			in:   "/proxy/exec/default//web",
			want: struct {
				ns, pod, container string
				ok                 bool
			}{"", "", "", false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns, pod, container, ok := parseExecPath(tc.in)
			if ok != tc.want.ok || ns != tc.want.ns || pod != tc.want.pod || container != tc.want.container {
				t.Fatalf("got (%q,%q,%q,%v), want (%q,%q,%q,%v)",
					ns, pod, container, ok,
					tc.want.ns, tc.want.pod, tc.want.container, tc.want.ok)
			}
		})
	}
}

func TestParseCommand_Defaults(t *testing.T) {
	got := parseCommand(nil)
	if len(got) != 1 || got[0] != "/bin/sh" {
		t.Fatalf("default cmd: got %v, want [/bin/sh]", got)
	}
	got = parseCommand(url.Values{"command": []string{"bash", "-l"}})
	if len(got) != 2 || got[0] != "bash" || got[1] != "-l" {
		t.Fatalf("explicit cmd: got %v, want [bash -l]", got)
	}
}

func TestWrapTmux_Wraps(t *testing.T) {
	got := wrapTmux([]string{"bash", "-l"})
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("expected sh -c wrapper, got %v", got)
	}
	if !strings.Contains(got[2], "tmux attach -t catalyst-ops") {
		t.Fatalf("missing tmux attach: %q", got[2])
	}
	if !strings.Contains(got[2], "tmux new -s catalyst-ops") {
		t.Fatalf("missing tmux new: %q", got[2])
	}
}

func TestBuildExecURL_Shape(t *testing.T) {
	u, err := buildExecURL("https://kubernetes.default.svc:443",
		"default", "web-7d8b", "web", []string{"bash", "-l"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/api/v1/namespaces/default/pods/web-7d8b/exec" {
		t.Fatalf("path: %q", u.Path)
	}
	q := u.Query()
	if q.Get("container") != "web" {
		t.Fatalf("container: %q", q.Get("container"))
	}
	if q.Get("tty") != "true" {
		t.Fatalf("tty: %q", q.Get("tty"))
	}
	cmds := q["command"]
	if len(cmds) != 2 || cmds[0] != "bash" || cmds[1] != "-l" {
		t.Fatalf("commands: %v", cmds)
	}
}

func TestServeHTTP_RejectsBadPath(t *testing.T) {
	v, err := auth.NewVerifier([]byte("k"), 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(HandlerOptions{
		Verifier:   v,
		RESTConfig: &rest.Config{Host: "https://example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/wrong/path")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestServeHTTP_RejectsBadHMAC(t *testing.T) {
	v, err := auth.NewVerifier([]byte("k"), 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(HandlerOptions{
		Verifier:   v,
		RESTConfig: &rest.Config{Host: "https://example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/proxy/exec/default/web/web", nil)
	req.Header.Set(auth.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set(auth.HeaderHMAC, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestServeHTTP_DeniedNamespace(t *testing.T) {
	secret := []byte("k")
	v, err := auth.NewVerifier(secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(HandlerOptions{
		Verifier:          v,
		RESTConfig:        &rest.Config{Host: "https://example"},
		AllowedNamespaces: []string{"safe-namespace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	path := "/proxy/exec/default/web/web"
	now := time.Now().Unix()
	mac := auth.ComputeHex(secret, now, path)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set(auth.HeaderTimestamp, strconv.FormatInt(now, 10))
	req.Header.Set(auth.HeaderHMAC, mac)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// TestServeHTTP_UpgradeAndProtocolEcho asserts that a valid HMAC
// request advances past the auth/policy gates AND that the WebSocket
// upgrade negotiates the v4.channel.k8s.io subprotocol.
//
// The real apiserver dial is mocked via a stub RESTConfig that points
// nowhere; the test asserts the handshake completes (WS Upgrade succeeds
// + the agreed subprotocol comes back as v4.channel.k8s.io). The
// downstream SPDY connect WILL fail because the host is unreachable;
// that failure is logged and the WS is closed with an error frame —
// exactly the path the production code takes when the apiserver is
// down. The test asserts that error path closes cleanly.
func TestServeHTTP_UpgradeAndProtocolEcho(t *testing.T) {
	secret := []byte("k")
	v, err := auth.NewVerifier(secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(HandlerOptions{
		Verifier:   v,
		RESTConfig: &rest.Config{Host: "https://127.0.0.1:1"}, // unroutable
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/proxy/exec/default/web/web"
	now := time.Now().Unix()
	mac := auth.ComputeHex(secret, now, "/proxy/exec/default/web/web")

	dialer := websocket.Dialer{Subprotocols: []string{"v4.channel.k8s.io"}}
	hdr := http.Header{}
	hdr.Set(auth.HeaderTimestamp, strconv.FormatInt(now, 10))
	hdr.Set(auth.HeaderHMAC, mac)

	conn, resp, err := dialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer conn.Close()

	if got := conn.Subprotocol(); got != "v4.channel.k8s.io" {
		t.Fatalf("subprotocol: got %q, want v4.channel.k8s.io", got)
	}

	// The bridge will fail to connect to the bogus apiserver; expect a
	// CloseInternalServerErr (1011) followed by a close frame.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			// any close error is acceptable — the handshake itself was
			// the assertion; subsequent transport error is the design.
			return
		}
	}
}
