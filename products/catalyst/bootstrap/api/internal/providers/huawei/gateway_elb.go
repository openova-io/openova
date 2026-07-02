// gateway_elb.go — #4706: reconcile the Sovereign gateway ELB's pool members
// to the live node set at the DURABLE gateway host ports (:443/:80).
//
// WHY THIS EXISTS. On the CCM-less Huawei provider the Sovereign Gateway is
// served by cilium-envoy in gateway-api hostNetwork mode (cilium 1.19.3,
// bp-cilium 1.4.8): envoy — a hostNetwork DaemonSet — binds node:443/:80
// directly on every node, and a dedicated public Huawei ELB
// (infra/providers/huawei/main.tf huaweicloud_elb_*.primary) does TCP
// passthrough public :443/:80 → node:443/:80. The member ports are durable
// and known at tofu-apply time, so tofu seeds correct members at Phase-0 —
// no nodePort anywhere (§854), no placeholder, no port discovery.
//
// What is left for catalyst-api is MEMBER-SET drift: nodes added by the
// autoscaler are missing from the pools, and replaced/removed nodes leave
// stale members behind. This reconciler converges each gateway pool's member
// set to the live cluster's node IPs at the fixed port — the same job
// hcloud-ccm does continuously on Hetzner, done explicitly here because
// Huawei has no CCM.
//
// History: on cilium 1.16.5 (hostNetwork bind bug) the gateway was an
// ELB→node:<auto-allocated nodePort> forward and this file repointed member
// PORTS post-convergence (#4690/#4691). That shape — and its failure mode
// (empty pools = console 000, hw217) — is retired by the 1.19.3 bump.
//
// Idempotent by construction: a member already at (nodeIP, port) is left
// untouched; only missing members are added and stale ones removed.
package huawei

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// gatewayELBNameSuffix is the name suffix tofu gives the gateway ELB
// (huaweicloud_elb_loadbalancer.primary → "${name_prefix}-elb-primary").
// listELBsByName returns every catalyst-<fqdn>-prefixed ELB; we select the
// gateway one by this suffix so the dedicated CONSOLE ELB ("-elb-console") is
// never touched.
const gatewayELBNameSuffix = "-elb-primary"

// gatewayPoolHTTPSSuffix / gatewayPoolHTTPSuffix are the pool name suffixes
// tofu gives the gateway ELB pools ("${name_prefix}-elb-pool-https" / "-http").
const (
	gatewayPoolHTTPSSuffix = "-elb-pool-https"
	gatewayPoolHTTPSuffix  = "-elb-pool-http"
)

// ReconcileGatewayELBMembers converges the gateway ELB's HTTPS/HTTP pool
// member sets to nodeIPs at the fixed gateway host ports, and points each
// pool's health monitor at the same port.
//
// It is an exported method on *Provider (mirrors SweepOrphanEIPs) so the
// handler reaches it via h.huaweiProvider("gateway-elb"). nodeIPs are the
// live cluster's node InternalIPs (primary region — each region is its own
// cluster); httpsPort/httpPort are the durable gateway host ports (443/80),
// bound on every node by cilium-envoy's gateway-api hostNetwork mode.
//
// Returns the number of members changed (adds + removals; 0 == already
// converged) and an error only on a hard API failure. Per-member failures are
// logged via progress and do NOT abort the whole reconcile (best-effort, like
// the day-2 post-handover hooks).
func (p *Provider) ReconcileGatewayELBMembers(ctx context.Context, ak, sk, projectID, region, sovereignFQDN string, nodeIPs []string, httpsPort, httpPort int, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if httpsPort <= 0 && httpPort <= 0 {
		return 0, fmt.Errorf("gateway-elb: no valid gateway host port supplied (https=%d http=%d)", httpsPort, httpPort)
	}
	desiredIPs := make(map[string]bool, len(nodeIPs))
	for _, ip := range nodeIPs {
		if ip = strings.TrimSpace(ip); ip != "" {
			desiredIPs[ip] = true
		}
	}
	if len(desiredIPs) == 0 {
		return 0, fmt.Errorf("gateway-elb: no node IPs supplied — refusing to empty the pools")
	}
	hw := hwCreds{AccessKey: ak, SecretKey: sk, ProjectID: projectID}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": region}})
	base := fmt.Sprintf("%s/v3/%s/elb", endpointFor("elb", region), projectID)

	// 1. Find the gateway ELB (by the -elb-primary name suffix).
	elbs, err := listELBsByName(ctx, client, hw, region, sovereignFQDN)
	if err != nil {
		return 0, fmt.Errorf("gateway-elb: list ELBs: %w", err)
	}
	var gwELBID string
	for _, e := range elbs {
		if strings.HasSuffix(e.Name, gatewayELBNameSuffix) {
			gwELBID = e.ID
			break
		}
	}
	if gwELBID == "" {
		return 0, fmt.Errorf("gateway-elb: no ELB named *%s found for %s (elbs=%d)", gatewayELBNameSuffix, sovereignFQDN, len(elbs))
	}

	// 2. Resolve the ELB's pools + its VIP subnet (the fallback subnet for
	//    member creates when a pool has no existing member to inherit from —
	//    nodes and the ELB share the region subnet by construction).
	lbResp, status, err := doSignedRequest(client, hw, http.MethodGet, base+"/loadbalancers/"+gwELBID, nil)
	if err != nil {
		return 0, fmt.Errorf("gateway-elb: get LB %s: %w", gwELBID, err)
	}
	if status >= 400 {
		return 0, fmt.Errorf("gateway-elb: get LB %s: status %d: %s", gwELBID, status, snippet(lbResp, 240))
	}
	var lb struct {
		LoadBalancer struct {
			VipSubnetCidrID string `json:"vip_subnet_cidr_id"`
			Pools           []struct {
				ID string `json:"id"`
			} `json:"pools"`
		} `json:"loadbalancer"`
	}
	if err := json.Unmarshal(lbResp, &lb); err != nil {
		return 0, fmt.Errorf("gateway-elb: decode LB %s: %w", gwELBID, err)
	}

	changed := 0
	for _, pl := range lb.LoadBalancer.Pools {
		// Fetch pool detail (name + members + healthmonitor_id).
		pResp, pStat, perr := doSignedRequest(client, hw, http.MethodGet, base+"/pools/"+pl.ID, nil)
		if perr != nil || pStat >= 400 {
			progress(fmt.Sprintf("gateway-elb: get pool %s failed (status %d): %v", pl.ID, pStat, perr))
			continue
		}
		var pool struct {
			Pool struct {
				Name    string `json:"name"`
				Members []struct {
					ID           string `json:"id"`
					Address      string `json:"address"`
					ProtocolPort int    `json:"protocol_port"`
					SubnetID     string `json:"subnet_cidr_id"`
				} `json:"members"`
				HealthMonitorID string `json:"healthmonitor_id"`
			} `json:"pool"`
		}
		if err := json.Unmarshal(pResp, &pool); err != nil {
			progress(fmt.Sprintf("gateway-elb: decode pool %s failed: %v", pl.ID, err))
			continue
		}

		desired := 0
		switch {
		case strings.HasSuffix(pool.Pool.Name, gatewayPoolHTTPSSuffix):
			desired = httpsPort
		case strings.HasSuffix(pool.Pool.Name, gatewayPoolHTTPSuffix):
			desired = httpPort
		default:
			// Not a gateway pool (shouldn't happen — the gateway ELB owns only
			// these two — but skip defensively rather than mis-port a member).
			continue
		}
		if desired <= 0 {
			continue
		}

		// 3. Converge the member set: keep members already at (nodeIP, desired),
		//    delete everything else (stale nodes, wrong ports, empty addresses),
		//    then add the missing nodes. Track a subnet to inherit for creates.
		subnetForCreate := lb.LoadBalancer.VipSubnetCidrID
		present := make(map[string]bool, len(pool.Pool.Members))
		for _, m := range pool.Pool.Members {
			if m.SubnetID != "" {
				subnetForCreate = m.SubnetID
			}
			if desiredIPs[m.Address] && m.ProtocolPort == desired {
				present[m.Address] = true
				continue
			}
			// Stale member (node gone, wrong port, or empty address).
			_, dStat, dErr := doSignedRequest(client, hw, http.MethodDelete, base+"/pools/"+pl.ID+"/members/"+m.ID, nil)
			if dErr != nil || (dStat >= 400 && dStat != http.StatusNotFound) {
				progress(fmt.Sprintf("gateway-elb: pool %s: delete stale member %s (%q:%d) failed (status %d): %v", pool.Pool.Name, m.ID, m.Address, m.ProtocolPort, dStat, dErr))
				continue
			}
			progress(fmt.Sprintf("gateway-elb: pool %s: stale member %q:%d removed", pool.Pool.Name, m.Address, m.ProtocolPort))
			changed++
		}
		for ip := range desiredIPs {
			if present[ip] {
				continue
			}
			body := gatewayMemberCreateBody(ip, desired, subnetForCreate)
			cResp, cStat, cErr := doSignedRequest(client, hw, http.MethodPost, base+"/pools/"+pl.ID+"/members", body)
			if cErr != nil || cStat >= 400 {
				progress(fmt.Sprintf("gateway-elb: pool %s: add member %s:%d failed (status %d): %s", pool.Pool.Name, ip, desired, cStat, snippet(cResp, 200)))
				continue
			}
			progress(fmt.Sprintf("gateway-elb: pool %s: member %s:%d added", pool.Pool.Name, ip, desired))
			changed++
		}

		// 4. Point the pool's health monitor at the gateway host port, so the
		//    ELB marks the members healthy against the port it actually forwards.
		if pool.Pool.HealthMonitorID != "" {
			hmBody := []byte(fmt.Sprintf(`{"healthmonitor":{"monitor_port":%d}}`, desired))
			hResp, hStat, hErr := doSignedRequest(client, hw, http.MethodPut, base+"/healthmonitors/"+pool.Pool.HealthMonitorID, hmBody)
			if hErr != nil || hStat >= 400 {
				progress(fmt.Sprintf("gateway-elb: pool %s: monitor repoint→%d failed (status %d): %s", pool.Pool.Name, desired, hStat, snippet(hResp, 200)))
			} else {
				progress(fmt.Sprintf("gateway-elb: pool %s: monitor →%d", pool.Pool.Name, desired))
			}
		}
	}

	return changed, nil
}

// gatewayMemberCreateBody builds the ELB v3 member-create payload. subnet_cidr_id
// is the member's IPv4 subnet (inherited from an existing member, or the ELB's
// own VIP subnet — nodes and the ELB share the region subnet by construction).
// When empty it is omitted (cross-VPC member — not our case, but keep the
// encoder honest).
func gatewayMemberCreateBody(address string, port int, subnetCIDRID string) []byte {
	member := map[string]any{
		"address":       address,
		"protocol_port": port,
	}
	if subnetCIDRID != "" {
		member["subnet_cidr_id"] = subnetCIDRID
	}
	b, _ := json.Marshal(map[string]any{"member": member})
	return b
}
