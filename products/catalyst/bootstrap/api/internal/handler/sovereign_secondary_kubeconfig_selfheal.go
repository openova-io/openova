// sovereign_secondary_kubeconfig_selfheal.go — #4000 durable self-heal
// for the cross-region apiserver datapath on the chroot.
//
// Context (the #3991/#3994 gap this closes):
//
//	#3991 taught the chroot to rewrite a secondary region's kubeconfig
//	`server:` host from the region's PUBLIC EIP (which the IN-cluster
//	catalyst-api cannot route to — HCS DNAT is external-only) to the
//	VPC-peered PRIVATE node IP it CAN route to. But that rewrite ONLY
//	fires when the secondary CP's cloud-init shipped a `nodeInternalIp`
//	in the POST body / `.nodeip` sidecar. For every env whose cloud-init
//	PREDATES the #3991 IaC change — and for any env where the runtime
//	`ip addr` detection returned empty — no private IP was ever supplied,
//	so the chroot kept the unroutable EIP. The secondary region's
//	informers then never sync (connect timeout), so EVERY component the
//	per-app Topology tab renders collapses to a false `singleton`
//	(only region-a's pods are visible). Proven live on hw173 (depID
//	7bb723da8da06047): region-b kubeconfig server = EIP 212.72.24.35:6443
//	(timed out from inside the VPC), region-b informers 0/0 synced,
//	grafana/keycloak/alloy/… all rendered singleton — yet the region-b
//	cp1 private IP 10.179.1.131:6443 was OPEN over the cross-VPC peering.
//
// This file makes the chroot SELF-HEAL with NO pre-shipped data:
//
//	When a secondary kubeconfig's `server:` host is unreachable AND it is
//	a public (non-RFC1918) address, the chroot connects to the apiserver
//	with InsecureSkipVerify JUST to read its TLS certificate, extracts the
//	PRIVATE (RFC1918 / ULA / CGNAT) IP SANs the k3s apiserver advertises
//	(it lists `--tls-san=$NODE_IP`), picks the first that is actually
//	reachable on :6443, and rewrites the kubeconfig server host to it. The
//	pinned `certificate-authority-data` keeps validating because the
//	swapped host is a real cert SAN. Purely additive + idempotent: a
//	kubeconfig already pointing at a reachable host is left untouched; a
//	heal that finds no reachable private SAN is a no-op (the EIP stays,
//	pre-#4000 behaviour).
//
// Runs on the chroot only (SOVEREIGN_FQDN set), in TWO places:
//   - the POST handler, as the fallback when no nodeInternalIp was shipped;
//   - SelfHealSecondaryKubeconfigsOnDisk, called once at startup after
//     FactoryFromEnv's LoadClustersFromDir, so an EIP kubeconfig that was
//     already persisted (the typical pre-#3991 / restarted-chroot case)
//     heals on boot without any mothership re-POST.
//
// Ref #4000 (+ #3991, #3982, #3969).

package handler

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// secondaryDialTimeout bounds each TCP+TLS probe against a candidate
// apiserver host. Short — the heal runs in the request path (POST) and at
// startup; an unreachable host must fail fast, not stall the boot.
var secondaryDialTimeout = 3 * time.Second

// secondaryHealPort is the apiserver port a healed host must answer on.
// k3s/k8s control planes serve the apiserver on 6443; the kubeconfig
// server URL always carries it, and we re-probe the candidate SANs there.
const secondaryHealPort = "6443"

// isRoutablePrivateIP reports whether ip is a private/site-local address
// the chroot can plausibly reach over VPC peering (RFC1918, CGNAT
// 100.64/10, IPv6 ULA fc00::/7; loopback/link-local excluded). A public
// address is NOT routable from inside the per-region VPC (the EIP DNAT is
// external-only), so we only ever heal TOWARD a private SAN.
func isRoutablePrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip.IsPrivate() {
		return true // RFC1918 + IPv6 ULA
	}
	// RFC6598 carrier-grade NAT 100.64.0.0/10 — HCS/Hetzner sometimes hand
	// these out for the internal node network; reachable over peering.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1]&0xc0 == 0x40 {
			return true
		}
	}
	return false
}

// hostReachable reports whether host:6443 accepts a TCP connection within
// the dial budget. Used both to decide whether a kubeconfig even NEEDS
// healing (current host already reachable → leave it) and to validate a
// candidate private SAN before we rewrite to it (never rewrite to an
// address we can't reach).
func hostReachable(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, secondaryHealPort), secondaryDialTimeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// apiserverCertPrivateSANs connects to host:6443 with InsecureSkipVerify
// JUST to read the served apiserver certificate, and returns its private
// (routable) IP SANs in cert order. We never trust the connection for data
// — only the leaf cert's IPAddresses field, which the k3s apiserver
// populates from its `--tls-san` set (including the node's private IP).
func apiserverCertPrivateSANs(host string) []string {
	dialer := &net.Dialer{Timeout: secondaryDialTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, secondaryHealPort), &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // cert read for SANs only, never trusted for data
		ServerName:         "kubernetes",
	})
	if err != nil {
		return nil
	}
	defer func() { _ = conn.Close() }()
	state := conn.ConnectionState()
	var leaf *x509.Certificate
	if len(state.PeerCertificates) > 0 {
		leaf = state.PeerCertificates[0]
	}
	if leaf == nil {
		return nil
	}
	out := make([]string, 0, len(leaf.IPAddresses))
	seen := map[string]struct{}{}
	for _, ip := range leaf.IPAddresses {
		if !isRoutablePrivateIP(ip) {
			continue
		}
		s := ip.String()
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// kubeconfigServerHost extracts the host (no port) of the FIRST cluster's
// server URL in a kubeconfig. Empty when the kubeconfig is unparseable or
// has no clusters.
func kubeconfigServerHost(raw string) string {
	cfg, err := clientcmd.Load([]byte(raw))
	if err != nil || cfg == nil {
		return ""
	}
	for _, c := range cfg.Clusters {
		if c == nil || strings.TrimSpace(c.Server) == "" {
			continue
		}
		u, perr := url.Parse(strings.TrimSpace(c.Server))
		if perr != nil || u.Host == "" {
			continue
		}
		if hn := u.Hostname(); hn != "" {
			return hn
		}
	}
	return ""
}

// selfHealKubeconfigServer is the core #4000 routine. Given a kubeconfig's
// raw bytes, it returns the (possibly rewritten) bytes, the host it healed
// TO ("" when unchanged), and a human reason for the log.
//
// Decision ladder:
//  1. Parse the current server host. If it's empty/unparseable → no-op.
//  2. If the current host is already reachable on :6443 → no-op (healthy;
//     this also makes the routine cheap to call repeatedly / on every boot).
//  3. If the current host is a hostname (not an IP) we can't reach → no-op
//     (DNS/routing issue we can't fix by SAN-swapping).
//  4. If the current host is a PRIVATE IP that is simply down → no-op (not
//     an EIP-routing problem; we must not invent a different host).
//  5. The current host is a PUBLIC, unreachable address (the EIP case).
//     Read the apiserver cert's private SANs; pick the first that IS
//     reachable; rewrite the server host to it. No reachable private SAN →
//     no-op (honest: nothing to heal toward).
func selfHealKubeconfigServer(raw string) (string, string, string) {
	host := kubeconfigServerHost(raw)
	if host == "" {
		return raw, "", "no server host"
	}
	if hostReachable(host) {
		return raw, "", "current host reachable"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return raw, "", "host is a name, not an EIP"
	}
	if isRoutablePrivateIP(ip) {
		return raw, "", "host already private (down, not an EIP-routing issue)"
	}
	for _, san := range apiserverCertPrivateSANs(host) {
		if !hostReachable(san) {
			continue
		}
		rewritten, n := rewriteKubeconfigServerHost(raw, san)
		if n > 0 {
			return rewritten, san, "healed EIP -> reachable private SAN"
		}
	}
	return raw, "", "no reachable private SAN on apiserver cert"
}

// SelfHealSecondaryKubeconfigsOnDisk scans the kubeconfigs dir for
// secondary kubeconfigs (`<depID>-<region>.yaml`) whose server host is an
// unroutable EIP, heals each in place, persists the rewrite back to disk
// (so a subsequent restart / LoadClustersFromDir reads the healed bytes),
// and re-registers the cluster in the cache so its informers reconnect to
// the reachable host.
//
// Called once at startup AFTER FactoryFromEnv populated the cache from
// disk, and safe to re-run (idempotent — a reachable host is skipped).
// Chroot-only: the mothership keeps the EIP it needs from outside the VPC.
//
// Best-effort throughout: a malformed file, an unreadable dir, or a heal
// that finds no reachable SAN is logged and skipped — never fatal. The
// primary (`<depID>.yaml`, no region suffix) is left untouched — it is the
// chroot's own in-cluster apiserver, reachable by definition, so the
// "current host reachable" short-circuit makes it a no-op.
func (h *Handler) SelfHealSecondaryKubeconfigsOnDisk(log *slog.Logger) {
	if h == nil || h.k8sCache == nil || !isChroot() {
		return
	}
	dir := kubeconfigsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("secondary-kubeconfig self-heal: read dir failed", "dir", dir, "err", err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := filepath.Ext(name)
		if ext != ".yaml" && ext != ".yml" && ext != ".kubeconfig" {
			continue
		}
		stem := strings.TrimSuffix(name, ext)
		if stem == "" {
			continue
		}
		path := filepath.Join(dir, name)
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			log.Warn("secondary-kubeconfig self-heal: read failed", "path", path, "err", rerr)
			continue
		}
		healed, healedTo, reason := selfHealKubeconfigServer(string(raw))
		if healedTo == "" {
			log.Debug("secondary-kubeconfig self-heal: no change", "clusterID", stem, "reason", reason)
			continue
		}
		if werr := os.WriteFile(path, []byte(healed), 0o600); werr != nil {
			log.Warn("secondary-kubeconfig self-heal: persist failed", "path", path, "err", werr)
			continue
		}
		// Re-register so the cache rebuilds this cluster's informers against
		// the healed (now-reachable) host. AddCluster shadows the prior
		// entry per the k8sCache contract.
		if aerr := h.k8sCache.AddCluster(k8scache.ClusterRef{ID: stem, KubeconfigPath: path}); aerr != nil {
			log.Warn("secondary-kubeconfig self-heal: re-register failed", "clusterID", stem, "err", aerr)
			continue
		}
		log.Info("secondary-kubeconfig self-heal: healed EIP -> private SAN (#4000)",
			"clusterID", stem, "healedTo", healedTo)
	}
}
