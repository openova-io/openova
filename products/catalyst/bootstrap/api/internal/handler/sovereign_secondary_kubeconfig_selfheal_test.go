// Tests for #4000 — durable self-heal of secondary-region kubeconfigs.
//
// The network-dependent paths (apiserverCertPrivateSANs, hostReachable)
// are exercised against an httptest TLS server with a private-IP SAN cert;
// the pure decision-ladder + parsing functions are tested directly.

package handler

import (
	"net"
	"strings"
	"testing"
	"time"
)

// Keep the dial-timeout no-op branches fast: the two "unreachable host"
// cases below each hit hostReachable on an address nothing listens on.
func init() { secondaryDialTimeout = 150 * time.Millisecond }

func TestIsRoutablePrivateIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.179.1.131", true},          // RFC1918 (hw173 region-b cp1)
		{"172.16.5.4", true},            // RFC1918
		{"192.168.1.10", true},          // RFC1918
		{"100.64.0.7", true},            // RFC6598 CGNAT
		{"100.127.255.1", true},         // RFC6598 upper edge
		{"212.72.24.35", false},         // public EIP (hw173 region-b)
		{"8.8.8.8", false},              // public
		{"127.0.0.1", false},            // loopback
		{"169.254.1.1", false},          // link-local
		{"0.0.0.0", false},              // unspecified
		{"100.128.0.1", false},          // just OUTSIDE 100.64/10
		{"fc00::1", true},               // IPv6 ULA
		{"fe80::1", false},              // IPv6 link-local
		{"2001:4860:4860::8888", false}, // IPv6 public
	}
	for _, c := range cases {
		got := isRoutablePrivateIP(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("isRoutablePrivateIP(%q) = %v, want %v", c.ip, got, c.want)
		}
	}
}

const kubeconfigTmpl = `apiVersion: v1
kind: Config
clusters:
- name: default
  cluster:
    server: https://%s:6443
    certificate-authority-data: ZHVtbXk=
contexts:
- name: default
  context:
    cluster: default
    user: default
current-context: default
users:
- name: default
  user:
    token: dummy
`

func TestKubeconfigServerHost(t *testing.T) {
	raw := strings.Replace(kubeconfigTmpl, "%s", "212.72.24.35", 1)
	if got := kubeconfigServerHost(raw); got != "212.72.24.35" {
		t.Errorf("kubeconfigServerHost = %q, want 212.72.24.35", got)
	}
	if got := kubeconfigServerHost("not a kubeconfig"); got != "" {
		t.Errorf("kubeconfigServerHost(garbage) = %q, want empty", got)
	}
}

// TestSelfHealNoOpForPrivateHost: a kubeconfig already pointing at a
// (down) private IP must NOT be rewritten — that is not an EIP-routing
// problem, and inventing a different host would be wrong.
func TestSelfHealNoOpForPrivateHost(t *testing.T) {
	// Use a private IP on a port nothing listens on so hostReachable=false,
	// exercising the "host already private (down)" branch deterministically.
	raw := strings.Replace(kubeconfigTmpl, "%s", "10.255.255.254", 1)
	out, healedTo, reason := selfHealKubeconfigServer(raw)
	if healedTo != "" {
		t.Errorf("private-host heal: healedTo=%q want empty (reason=%q)", healedTo, reason)
	}
	if out != raw {
		t.Errorf("private-host heal must leave bytes unchanged")
	}
	if !strings.Contains(reason, "private") {
		t.Errorf("reason = %q, want a 'private' explanation", reason)
	}
}

// TestSelfHealNoOpForUnreachableHostname: a hostname (not an IP) we can't
// reach is a DNS/routing issue we can't fix by SAN-swapping → no-op.
func TestSelfHealNoOpForUnreachableHostname(t *testing.T) {
	raw := strings.Replace(kubeconfigTmpl, "%s", "apiserver.invalid.example", 1)
	out, healedTo, reason := selfHealKubeconfigServer(raw)
	if healedTo != "" {
		t.Errorf("hostname heal: healedTo=%q want empty (reason=%q)", healedTo, reason)
	}
	if out != raw {
		t.Errorf("hostname heal must leave bytes unchanged")
	}
	if !strings.Contains(reason, "name") {
		t.Errorf("reason = %q, want a 'name' explanation", reason)
	}
}

// TestSelfHealNoOpForNoServer: a kubeconfig with no parseable server is a
// no-op.
func TestSelfHealNoOpForNoServer(t *testing.T) {
	_, healedTo, reason := selfHealKubeconfigServer("garbage")
	if healedTo != "" || !strings.Contains(reason, "no server host") {
		t.Errorf("garbage heal: healedTo=%q reason=%q", healedTo, reason)
	}
}

// TestRewriteKubeconfigServerHostPreservesPortAndScheme guards the
// invariant the heal depends on: only the HOST is swapped (scheme + port
// + CA-data intact) so the pinned cert keeps validating.
func TestRewriteKubeconfigServerHostPreservesPortAndScheme(t *testing.T) {
	raw := strings.Replace(kubeconfigTmpl, "%s", "212.72.24.35", 1)
	out, n := rewriteKubeconfigServerHost(raw, "10.179.1.131")
	if n != 1 {
		t.Fatalf("rewrite changed %d server lines, want 1", n)
	}
	if !strings.Contains(out, "server: https://10.179.1.131:6443") {
		t.Errorf("rewritten kubeconfig missing healed host:port; got:\n%s", out)
	}
	if strings.Contains(out, "212.72.24.35") {
		t.Errorf("rewritten kubeconfig still carries the EIP")
	}
	if !strings.Contains(out, "certificate-authority-data: ZHVtbXk=") {
		t.Errorf("rewrite must not touch certificate-authority-data")
	}
}
