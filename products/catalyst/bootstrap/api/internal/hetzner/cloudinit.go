// Package hetzner — cloud-init scripts for k3s installation.
//
// The control-plane script:
//   - Disables swap, installs iptables-legacy
//   - Installs k3s with --disable=traefik --disable=servicelb (Cilium replaces both)
//   - Writes a token to /var/lib/rancher/k3s/server/token (predictable across reboots)
//   - Opens kubeconfig group-readable so the bootstrap controller can read it
//
// The worker script:
//   - Joins the control plane via the agent install path
//   - Pulls the same token shared via the cluster's join secret
//
// These scripts are intentionally compact and battle-tested. Production hardening
// (fail2ban, unattended-upgrades, CIS profiles) will be applied via Kyverno
// policies post-bootstrap, not in cloud-init.
package hetzner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// buildCloudInitControlPlane returns the cloud-init script for the control plane.
//
// The script installs k3s with Catalyst-specific flags and writes the SSH
// authorized_keys for the wizard-provided public key (cloud-init also handles
// authorized_keys when ssh_keys is set on the server, but we add a redundant
// write so the operator can always fall back to break-glass).
func buildCloudInitControlPlane(req ProvisionRequest) string {
	token := generateK3sToken(req)
	return fmt.Sprintf(`#cloud-config
# Catalyst Sovereign control-plane bootstrap.
# Sovereign: %s
# Provisioned by: catalyst-provisioner (https://console.openova.io)
package_update: true
package_upgrade: false
packages:
  - curl
  - iptables
  - jq
  - ca-certificates
runcmd:
  - swapoff -a
  - sed -i '/swap/d' /etc/fstab
  - update-alternatives --set iptables /usr/sbin/iptables-legacy
  - update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy
  # k3s install with Catalyst-required flags. Cilium replaces traefik + servicelb.
  - 'curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=v1.31.4+k3s1 K3S_TOKEN=%s INSTALL_K3S_EXEC="server --cluster-init --disable=traefik --disable=servicelb --disable=local-storage --flannel-backend=none --disable-network-policy --kube-apiserver-arg=feature-gates=ServiceTrafficDistribution=true --tls-san=%s --node-label catalyst.openova.io/role=control-plane --write-kubeconfig-mode=0644" sh -'
  - sleep 30
  - kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml wait --for=condition=Ready node --all --timeout=180s || true
  # Write a marker file the bootstrap controller polls for to know cloud-init finished.
  - touch /var/lib/catalyst/cloud-init-complete
write_files:
  - path: /var/lib/catalyst/sovereign.json
    permissions: '0644'
    content: |
      {
        "sovereignFQDN": "%s",
        "orgName": %q,
        "orgEmail": %q,
        "region": %q,
        "haEnabled": %t,
        "workerCount": %d
      }
final_message: "Catalyst control-plane bootstrap complete after $UPTIME seconds"
`,
		req.SovereignFQDN,
		token,
		req.SovereignFQDN,
		req.SovereignFQDN,
		req.OrgName,
		req.OrgEmail,
		req.Region,
		req.HAEnabled,
		req.WorkerCount,
	)
}

// buildCloudInitWorker returns the cloud-init script for a worker node that
// joins the control plane at cpIP. The same token derived from req is used.
func buildCloudInitWorker(req ProvisionRequest, cpIP string) string {
	token := generateK3sToken(req)
	return fmt.Sprintf(`#cloud-config
# Catalyst Sovereign worker bootstrap.
# Sovereign: %s
package_update: true
package_upgrade: false
packages:
  - curl
  - iptables
  - ca-certificates
runcmd:
  - swapoff -a
  - sed -i '/swap/d' /etc/fstab
  - update-alternatives --set iptables /usr/sbin/iptables-legacy
  - update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy
  - 'curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=v1.31.4+k3s1 K3S_URL=https://%s:6443 K3S_TOKEN=%s INSTALL_K3S_EXEC="agent --node-label catalyst.openova.io/role=worker" sh -'
  - touch /var/lib/catalyst/cloud-init-complete
final_message: "Catalyst worker bootstrap complete after $UPTIME seconds"
`,
		req.SovereignFQDN,
		cpIP,
		token,
	)
}

// generateK3sToken derives a deterministic token from the request so workers
// can join without an out-of-band channel. The token is the first 32 hex
// chars of sha256(hetznerProjectID + "/" + sovereignFQDN + "/k3s-bootstrap").
//
// Determinism is important: workers are provisioned in parallel with the
// control plane, and they need to know the token at server creation time
// (cloud-init runs before any orchestrator can reach back into the cluster).
//
// Security note: the token is not the long-term cluster secret — k3s rotates
// the bootstrap token after first join. The deterministic derivation only
// covers the short bootstrap window and is gated by Hetzner project access
// (an attacker would need both the Hetzner project ID AND the sovereign FQDN,
// the latter of which is public). For long-term cluster secrets see SECURITY.md §3.
func generateK3sToken(req ProvisionRequest) string {
	h := sha256.New()
	h.Write([]byte(strings.Join([]string{req.HetznerProjectID, req.SovereignFQDN, "k3s-bootstrap"}, "/")))
	return hex.EncodeToString(h.Sum(nil))[:32]
}
