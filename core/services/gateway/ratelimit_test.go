package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantLen int
	}{
		{"empty", "", 0},
		{"whitespace", "   ", 0},
		{"single cidr", "10.42.0.0/16", 1},
		{"multiple cidrs", "10.42.0.0/16, 192.168.0.0/24", 2},
		{"bare ipv4 is normalised to /32", "10.1.2.3", 1},
		{"bare ipv6 is normalised to /128", "fd00::1", 1},
		{"invalid entries are skipped", "not-a-cidr, 10.42.0.0/16, 999.999.999.999/16", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTrustedProxies(tc.input)
			if len(got) != tc.wantLen {
				t.Fatalf("parseTrustedProxies(%q) returned %d nets, want %d", tc.input, len(got), tc.wantLen)
			}
		})
	}
}

func TestClientIP_UntrustedPeerIgnoresForwardedHeaders(t *testing.T) {
	trusted := parseTrustedProxies("10.42.0.0/16")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:54321" // public internet, untrusted
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "5.6.7.8")

	got := clientIP(req, trusted)
	if got != "203.0.113.5" {
		t.Fatalf("untrusted peer with forged XFF/X-Real-IP: got %q, want %q", got, "203.0.113.5")
	}
}

func TestClientIP_TrustedPeerHonoursXForwardedFor(t *testing.T) {
	trusted := parseTrustedProxies("10.42.0.0/16")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.42.0.174:54321" // traefik pod, trusted
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	got := clientIP(req, trusted)
	if got != "203.0.113.99" {
		t.Fatalf("trusted proxy XFF: got %q, want %q", got, "203.0.113.99")
	}
}

func TestClientIP_TrustedPeerPrefersXRealIP(t *testing.T) {
	trusted := parseTrustedProxies("10.42.0.0/16")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.42.0.174:54321"
	req.Header.Set("X-Real-IP", "203.0.113.10")
	req.Header.Set("X-Forwarded-For", "192.0.2.1")

	got := clientIP(req, trusted)
	if got != "203.0.113.10" {
		t.Fatalf("X-Real-IP should take precedence: got %q, want %q", got, "203.0.113.10")
	}
}

func TestClientIP_ChainWalksRightToLeftStoppingAtUntrustedHop(t *testing.T) {
	trusted := parseTrustedProxies("10.42.0.0/16,192.168.1.0/24")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.42.0.174:54321"
	// Chain: client -> edge(untrusted) -> internal-proxy -> gateway
	req.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.5, 192.168.1.10")

	got := clientIP(req, trusted)
	if got != "198.51.100.5" {
		t.Fatalf("should return first untrusted hop walking right-to-left: got %q, want %q", got, "198.51.100.5")
	}
}

func TestClientIP_NoTrustedProxiesFallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.42.0.174:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	got := clientIP(req, nil)
	if got != "10.42.0.174" {
		t.Fatalf("with no trusted proxies configured, XFF must be ignored: got %q, want %q", got, "10.42.0.174")
	}
}

func TestClientIP_MalformedXFFFallsBackToPeer(t *testing.T) {
	trusted := parseTrustedProxies("10.42.0.0/16")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.42.0.174:54321"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	got := clientIP(req, trusted)
	if got != "10.42.0.174" {
		t.Fatalf("malformed XFF: got %q, want fallback to peer %q", got, "10.42.0.174")
	}
}

func TestIsTrustedProxy(t *testing.T) {
	trusted := parseTrustedProxies("10.42.0.0/16,192.168.1.0/24")

	cases := []struct {
		ip   string
		want bool
	}{
		{"10.42.0.174", true},
		{"10.42.255.1", true},
		{"192.168.1.50", true},
		{"192.168.2.1", false},
		{"203.0.113.5", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			got := isTrustedProxy(parseIP(tc.ip), trusted)
			if got != tc.want {
				t.Fatalf("isTrustedProxy(%q): got %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// parseIP is a local helper that returns nil for the empty string so the
// table-driven test above stays compact.
func parseIP(s string) (result net.IP) {
	if s == "" {
		return nil
	}
	return net.ParseIP(s)
}
