// Wave 5.93 (#2445) — RotateBlocklistedNATEIPs scans the SNAT rules on
// every NAT gateway tagged for this deployment and, for any rule whose
// floating-IP address is on the known-blocklist, releases + reallocates
// a fresh EIP and swaps the SNAT rule to use it. Eliminates the manual
// "watcher script" intervention that hw01/hw02/hw03 needed.
//
// Why blocklist + pool-drain instead of reachability-probe-from-tofu:
// running an actual TCP probe to harbor.openova.io requires either an
// ECS in the same VPC with that EIP attached (the chicken-and-egg
// problem we're trying to avoid) OR a tofu local-exec from the runner
// (catalyst-api pod) — but catalyst-api lives on Contabo, so a probe
// from catalyst-api to "Contabo + Huawei EIP" doesn't tell us anything
// about Huawei egress reachability. The pragmatic answer: maintain a
// statically-declared blocklist of Huawei me-east-215 EIPs known to
// be on third-party IP-reputation lists for Contabo 45.151.123.0/24,
// and rotate them post-allocation. Pool drain: when Huawei recycles a
// blocklisted EIP back into a fresh allocate() (the small free-pool
// case), the rotation HOLDS the bad EIP (doesn't release immediately)
// so the next allocate() returns a different IP. This breaks the
// small-pool recycle loop.
//
// Caller: catalyst-api provisioner calls this right after tofu apply
// + before the Phase-1 "watch for HRs Ready" loop. The 2-second
// per-rotation cost is negligible compared to the 14-min provisioning
// budget, and it eliminates a major source of false-failure perception
// for operators.
//
// Operators can extend hwBlocklistedEIPs without a code change by
// setting CATALYST_HUAWEI_NAT_EIP_BLOCKLIST=ip1,ip2,... at catalyst-
// api startup. The list is read once at New(); a fresh blocklist
// requires a catalyst-api restart.

package huawei

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// hwBlocklistedEIPs is the seed list of Huawei me-east-215 EIPs observed
// 2026-05-25 to be blocklisted for outbound to Contabo 45.151.123.0/24
// (harbor.openova.io). Caller can extend via CATALYST_HUAWEI_NAT_EIP_BLOCKLIST.
var hwBlocklistedEIPs = map[string]bool{
	"212.72.24.48": true, // hw01 ~10-attempt observation, hw03 b-region NAT
	"212.72.24.14": true, // hw01 b-region NAT, hw03 b-region NAT
}

// blocklist returns the merged set of seed + env-supplied blocklist.
func blocklist() map[string]bool {
	out := map[string]bool{}
	for k := range hwBlocklistedEIPs {
		out[k] = true
	}
	if v := os.Getenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST"); v != "" {
		for _, ip := range strings.Split(v, ",") {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				out[ip] = true
			}
		}
	}
	return out
}

type hwSNATRule struct {
	ID                  string `json:"id"`
	NATGatewayID        string `json:"nat_gateway_id"`
	NetworkID           string `json:"network_id"`
	FloatingIPID        string `json:"floating_ip_id"`
	FloatingIPAddress   string `json:"floating_ip_address"`
	Status              string `json:"status"`
}

// RotateBlocklistedNATEIPs runs the pre-flight check for one deployment.
// Returns (rotated_count, error). Idempotent — re-running on a clean
// state is a no-op.
//
// Wave 5.93 entry point — signature matches provisioner.natEIPPreflightFunc
// so the provisioner package can register it as the NATEIPPreflightHook
// at init() time without an import cycle.
func RotateBlocklistedNATEIPs(ctx context.Context, _provider, deploymentID, sovereignFQDN, accessKey, secretKey, projectID, region string, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	creds := providers.ProviderCreds{Raw: map[string]string{
		"huawei_access_key": accessKey,
		"huawei_secret_key": secretKey,
		"huawei_project_id": projectID,
		"huawei_region":     region,
	}}
	hw, err := credsFromProviderCreds(creds)
	if err != nil {
		return 0, fmt.Errorf("creds: %w", err)
	}
	if region == "" {
		region = regionFromCreds(creds)
	}
	client := httpClientFor(creds)
	bl := blocklist()

	// List SNAT rules
	url := fmt.Sprintf("%s/v2/%s/snat_rules", endpointFor("nat", region), hw.ProjectID)
	body, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("snat list: %w", err)
	}
	if status >= 400 {
		return 0, fmt.Errorf("snat list status %d: %s", status, snippet(body, 240))
	}
	var decoded struct {
		SNATRules []hwSNATRule `json:"snat_rules"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return 0, fmt.Errorf("decode snat: %w", err)
	}

	rotated := 0
	for _, r := range decoded.SNATRules {
		if !bl[r.FloatingIPAddress] {
			continue
		}
		// Only rotate SNAT rules whose NAT gateway is tagged for this
		// deployment. Match by NAT gateway name prefix (deployment-id
		// substring) is the safest filter — checking tags would require
		// an extra round trip per NAT.
		if !natGatewayBelongsToDeployment(ctx, client, hw, region, r.NATGatewayID, deploymentID) {
			continue
		}
		progress(fmt.Sprintf("Wave 5.93: SNAT %s EIP %s is blocklisted, rotating",
			r.ID[:8], r.FloatingIPAddress))
		newEIPID, newAddr, held, err := allocateCleanEIP(ctx, client, hw, region, deploymentID, bl, 8)
		if err != nil {
			return rotated, fmt.Errorf("allocate clean EIP: %w", err)
		}
		// Delete old SNAT (must come before SNAT-create since one SNAT
		// per network).
		oldSNATURL := fmt.Sprintf("%s/v2/%s/nat_gateways/%s/snat_rules/%s",
			endpointFor("nat", region), hw.ProjectID, r.NATGatewayID, r.ID)
		if _, st, err := doSignedRequest(client, hw, http.MethodDelete, oldSNATURL, nil); err != nil || st >= 400 {
			return rotated, fmt.Errorf("delete old snat %s: status %d err %v", r.ID, st, err)
		}
		time.Sleep(6 * time.Second) // propagation
		// Create new SNAT with the clean EIP
		newSNATBody := fmt.Sprintf(`{"snat_rule":{"nat_gateway_id":%q,"network_id":%q,"source_type":0,"floating_ip_id":%q}}`,
			r.NATGatewayID, r.NetworkID, newEIPID)
		newSNATURL := fmt.Sprintf("%s/v2/%s/snat_rules", endpointFor("nat", region), hw.ProjectID)
		if _, st, err := doSignedRequest(client, hw, http.MethodPost, newSNATURL, []byte(newSNATBody)); err != nil || st >= 400 {
			return rotated, fmt.Errorf("create new snat: status %d err %v", st, err)
		}
		// Release the OLD (blocklisted) EIP — held EIPs are released too
		_ = deleteEIP(ctx, client, hw, region, r.FloatingIPID)
		for _, h := range held {
			_ = deleteEIP(ctx, client, hw, region, h)
		}
		progress(fmt.Sprintf("Wave 5.93: SNAT %s rotated to clean EIP %s", r.ID[:8], newAddr))
		rotated++
	}
	return rotated, nil
}

// allocateCleanEIP allocates EIPs until one is NOT on the blocklist.
// Returns (cleanEIPID, cleanEIPAddress, heldEIPIDs, error).
// Held EIPs are blocklisted EIPs that we keep allocated to drain the
// Huawei free-pool past their recycle.
func allocateCleanEIP(ctx context.Context, client *http.Client, hw hwCreds, region, deploymentID string, bl map[string]bool, maxAttempts int) (string, string, []string, error) {
	held := []string{}
	for i := 0; i < maxAttempts; i++ {
		body := fmt.Sprintf(`{"publicip":{"type":"5_bgp"},"bandwidth":{"name":"catalyst-%s-nat-preflight-%d","size":300,"share_type":"PER","charge_mode":"traffic"}}`,
			deploymentID[:8], i+1)
		url := fmt.Sprintf("%s/v1/%s/publicips", endpointFor("vpc", region), hw.ProjectID)
		resp, status, err := doSignedRequest(client, hw, http.MethodPost, url, []byte(body))
		if err != nil || status >= 400 {
			return "", "", held, fmt.Errorf("alloc EIP attempt %d: status %d err %v", i+1, status, err)
		}
		var decoded struct {
			PublicIP struct {
				ID      string `json:"id"`
				Address string `json:"public_ip_address"`
			} `json:"publicip"`
		}
		if err := json.Unmarshal(resp, &decoded); err != nil {
			return "", "", held, fmt.Errorf("decode alloc EIP: %w", err)
		}
		if !bl[decoded.PublicIP.Address] {
			return decoded.PublicIP.ID, decoded.PublicIP.Address, held, nil
		}
		// Blocklisted — hold to drain pool
		held = append(held, decoded.PublicIP.ID)
	}
	return "", "", held, fmt.Errorf("exhausted %d attempts; all allocated EIPs were blocklisted", maxAttempts)
}

// natGatewayBelongsToDeployment matches NAT gateway name against the
// deployment ID prefix. The cloudinit names NATs as
// "catalyst-<fqdn-slug>-<dep-id-prefix>-<region>-nat".
func natGatewayBelongsToDeployment(ctx context.Context, client *http.Client, hw hwCreds, region, natID, deploymentID string) bool {
	if len(deploymentID) < 8 {
		return false
	}
	url := fmt.Sprintf("%s/v2/%s/nat_gateways/%s", endpointFor("nat", region), hw.ProjectID, natID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return false
	}
	var decoded struct {
		NATGateway struct {
			Name string `json:"name"`
		} `json:"nat_gateway"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return false
	}
	return strings.Contains(decoded.NATGateway.Name, deploymentID[:8])
}
