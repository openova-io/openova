package handlers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// #5205 — the funnel completion interstitial used to poll the per-Org
// console host DIRECTLY from the browser with `fetch(..., {mode:'no-cors'})`.
// A live hw270 walk showed that probe NEVER observed success even though a
// direct curl/browser-nav to the same host returned 200 within seconds — the
// opaque cross-origin Response gave the browser no readable status. These
// tests lock the readable, same-origin replacement: isConsoleHost (the SSRF/
// open-redirect guard), ipsArePublic (the DNS-rebinding guard), and
// probeConsoleHealthz (the part that actually reads the upstream status,
// unlike the old opaque-response guess).

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func TestIsConsoleHost(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"bare console https", "https://console.acme.omani.works", true},
		{"exact console host", "https://console", true},
		{"console with port", "https://console.acme.omani.works:8443", true},
		{"case-insensitive host", "https://CONSOLE.acme.omani.works", true},
		{"http rejected — must be https", "http://console.acme.omani.works", false},
		{"non-console host rejected — open-redirect guard", "https://evil.example.com", false},
		{"consoleish-but-not-dot-prefixed host rejected", "https://consolefoo.example.com", false},
		{"marketplace host rejected", "https://marketplace.acme.omani.works", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := mustParseURL(t, tc.raw)
			if got := isConsoleHost(u); got != tc.want {
				t.Errorf("isConsoleHost(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestIpsArePublic(t *testing.T) {
	cases := []struct {
		name string
		ips  []net.IP
		want bool
	}{
		{"empty resolution rejected", nil, false},
		{"public IPv4", []net.IP{net.ParseIP("203.0.113.10")}, true},
		{"private RFC1918 rejected", []net.IP{net.ParseIP("10.0.0.1")}, false},
		{"private RFC1918 192.168 rejected", []net.IP{net.ParseIP("192.168.1.1")}, false},
		{"loopback rejected", []net.IP{net.ParseIP("127.0.0.1")}, false},
		{"link-local rejected", []net.IP{net.ParseIP("169.254.1.1")}, false},
		{"unspecified rejected", []net.IP{net.ParseIP("0.0.0.0")}, false},
		{"mixed public+private rejected (fail closed on ANY private answer)",
			[]net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("10.0.0.1")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ipsArePublic(tc.ips); got != tc.want {
				t.Errorf("ipsArePublic(%v) = %v, want %v", tc.ips, got, tc.want)
			}
		})
	}
}

// TestProbeConsoleHealthz_ReadsRealStatus is the heart of the #5205 fix: unlike
// the browser's opaque no-cors Response (which resolves the same way whether
// the upstream answered 200 or 500), this probe reads the ACTUAL status code.
func TestProbeConsoleHealthz_ReadsRealStatus(t *testing.T) {
	t.Run("2xx is ready", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				t.Errorf("expected probe path /healthz, got %q", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()

		if !probeConsoleHealthz(context.Background(), srv.URL) {
			t.Error("probeConsoleHealthz() = false, want true for a 200 upstream")
		}
	})

	t.Run("non-2xx is not ready — the opaque probe could never see this", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		if probeConsoleHealthz(context.Background(), srv.URL) {
			t.Error("probeConsoleHealthz() = true, want false for a 503 upstream")
		}
	})

	t.Run("connection refused (host torn down) is not ready", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // closed before use — guarantees connection-refused

		if probeConsoleHealthz(context.Background(), srv.URL) {
			t.Error("probeConsoleHealthz() = true, want false against a closed listener")
		}
	})

	t.Run("context deadline exceeded is not ready", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if probeConsoleHealthz(ctx, srv.URL) {
			t.Error("probeConsoleHealthz() = true, want false when the context deadline is exceeded")
		}
	})
}

// TestConsoleReady_HandlerRejectsInvalidHost exercises the HTTP handler's
// input-validation path (no network call is reachable for these cases — a bad
// host must never even attempt an outbound probe).
func TestConsoleReady_HandlerRejectsInvalidHost(t *testing.T) {
	h := &Handler{}

	cases := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"missing host param", "", http.StatusBadRequest},
		{"non-console host (open-redirect/SSRF attempt)", "?host=" + url.QueryEscape("https://evil.example.com"), http.StatusBadRequest},
		{"http scheme rejected", "?host=" + url.QueryEscape("http://console.acme.omani.works"), http.StatusBadRequest},
		{"unparseable host", "?host=" + url.QueryEscape("://not-a-url"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/provisioning/console-ready"+tc.query, nil)
			rec := httptest.NewRecorder()
			h.ConsoleReady(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestConsoleReady_HandlerFailsClosedOnPrivateResolution locks the
// DNS-rebinding guard end-to-end through the handler: a host that resolves
// only to private/loopback addresses must report ready:false (200), not
// attempt the outbound probe against internal infrastructure.
func TestConsoleReady_HandlerFailsClosedOnPrivateResolution(t *testing.T) {
	orig := resolvesToPublicIP
	resolvesToPublicIP = func(host string) bool { return false }
	t.Cleanup(func() { resolvesToPublicIP = orig })

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/provisioning/console-ready?host="+url.QueryEscape("https://console.acme.omani.works"), nil)
	rec := httptest.NewRecorder()
	h.ConsoleReady(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-closed to ready:false, not an error)", rec.Code)
	}
	if body := rec.Body.String(); body != `{"ready":false}`+"\n" {
		t.Errorf("body = %q, want ready:false", body)
	}
}
