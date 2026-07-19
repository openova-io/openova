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
// #5244 — the member set spans ALL regions, not just the primary. Members
// were primary-region-only (both the tofu seed and this reconciler), so on a
// region-a kill the ELB health monitors drained every member and the public
// EIP black-holed for the whole outage even though region-b's cilium-envoy
// was Running and bound on node:443 (proven live on hw275 G12, dep 7886def2:
// every pool member 10.208.1.x/region-a, zero 10.218.1.x/region-b,
// ip_target_enable=false). Peer-region nodes live in a DIFFERENT VPC than
// the ELB, so they are added as cross-VPC IP-type members (no
// subnet_cidr_id; requires the LB's ip_target_enable, which this reconciler
// flips on when needed) reachable over the existing mesh VPC peering. The
// ports stay the identical hostNetwork host ports — never a nodePort (§854).
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

// Console ELB name/pool suffixes tofu gives the dedicated console ELB
// (huaweicloud_elb_loadbalancer.console → "${name_prefix}-elb-console", pools
// "-elb-pool-console-https" / "-elb-pool-console-http"). Under the huawei
// hostNetwork gateway the console gateway binds node:8443/:8080 (NOT :443/:80 —
// that would collide with the primary gateway, hw218 #4715), so the console ELB
// forwards public :443/:80 → node:8443/:8080. These are DISTINCT from the
// primary suffixes so the two reconciles never touch each other's ELB.
const (
	consoleELBNameSuffix   = "-elb-console"
	consolePoolHTTPSSuffix = "-elb-pool-console-https"
	consolePoolHTTPSuffix  = "-elb-pool-console-http"
)

// ReconcileGatewayELBMembers converges the gateway ELB's HTTPS/HTTP pool
// member sets to the union of primaryIPs + peerIPs at the fixed gateway host
// ports, and points each pool's health monitor at the same port.
//
// It is an exported method on *Provider (mirrors SweepOrphanEIPs) so the
// handler reaches it via h.huaweiProvider("gateway-elb").
//
//   - primaryIPs are the PRIMARY region cluster's node InternalIPs — nodes in
//     the ELB's own VPC, added as classic subnet members.
//   - peerIPs are every OTHER region cluster's node InternalIPs (#5244) —
//     nodes in peer VPCs, added as cross-VPC IP-type members (no
//     subnet_cidr_id; the reconciler enables the LB's ip_target_enable when
//     peer members are needed and it is off).
//   - sweepStale gates DELETION of members outside the union. Pass false when
//     region enumeration was INCOMPLETE (e.g. a peer cluster unreachable —
//     possibly mid region outage): the reconcile then only ADDS missing
//     members, so a temporarily unlistable region's members — the exact
//     members carrying the EIP through the outage — are never drained by a
//     blind sweep.
//   - httpsPort/httpPort are the durable gateway host ports (443/80), bound on
//     every node in every region by cilium-envoy's gateway-api hostNetwork
//     mode.
//
// Returns the number of members changed (adds + removals; 0 == already
// converged) and an error only on a hard API failure. Per-member failures are
// logged via progress and do NOT abort the whole reconcile (best-effort, like
// the day-2 post-handover hooks).
func (p *Provider) ReconcileGatewayELBMembers(ctx context.Context, ak, sk, projectID, region, sovereignFQDN string, primaryIPs, peerIPs []string, httpsPort, httpPort int, sweepStale bool, progress func(msg string)) (int, error) {
	return p.reconcileELBMembers(ctx, ak, sk, projectID, region, sovereignFQDN,
		gatewayELBNameSuffix, gatewayPoolHTTPSSuffix, gatewayPoolHTTPSuffix,
		primaryIPs, peerIPs, httpsPort, httpPort, sweepStale, false /*softSkipMissing*/, progress)
}

// ReconcileConsoleELBMembers converges the dedicated CONSOLE ELB's member set
// to the same mesh-wide node union at the console gateway host ports
// (8443/8080 on huawei — #5244: the console EIP black-holed on region-kill for
// the same primary-region-only-membership root cause as the gateway ELB).
// Identical convergence to the primary gateway, targeting the "-elb-console"
// ELB. It is softSkipMissing: a console_isolation_enabled=false prov has no
// console ELB, so "ELB not found" is a clean no-op (0, nil), not an error.
// Without this, primary self-heals node churn (post_handover) but the console
// ELB kept its tofu-seeded boot member set forever → after an autoscaler node
// roll the console front door lost its live backends → Console 000. Companion
// to ReconcileGatewayELBMembers.
func (p *Provider) ReconcileConsoleELBMembers(ctx context.Context, ak, sk, projectID, region, sovereignFQDN string, primaryIPs, peerIPs []string, httpsPort, httpPort int, sweepStale bool, progress func(msg string)) (int, error) {
	return p.reconcileELBMembers(ctx, ak, sk, projectID, region, sovereignFQDN,
		consoleELBNameSuffix, consolePoolHTTPSSuffix, consolePoolHTTPSuffix,
		primaryIPs, peerIPs, httpsPort, httpPort, sweepStale, true /*softSkipMissing*/, progress)
}

// elbMember is one pool member as returned by the ELB v3 members-list
// endpoint.
type elbMember struct {
	ID           string `json:"id"`
	Address      string `json:"address"`
	ProtocolPort int    `json:"protocol_port"`
	SubnetID     string `json:"subnet_cidr_id"`
}

func (p *Provider) reconcileELBMembers(ctx context.Context, ak, sk, projectID, region, sovereignFQDN, elbSuffix, poolHTTPSSuffix, poolHTTPSuffix string, primaryIPs, peerIPs []string, httpsPort, httpPort int, sweepStale, softSkipMissing bool, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if httpsPort <= 0 && httpPort <= 0 {
		return 0, fmt.Errorf("gateway-elb: no valid gateway host port supplied (https=%d http=%d)", httpsPort, httpPort)
	}
	// desiredIPs = the full member union; crossVPC marks the peer-region
	// subset that must be created WITHOUT a subnet_cidr_id (IP-type member).
	// A duplicate IP in both lists classifies as primary.
	desiredIPs := make(map[string]bool, len(primaryIPs)+len(peerIPs))
	crossVPC := make(map[string]bool, len(peerIPs))
	for _, ip := range peerIPs {
		if ip = strings.TrimSpace(ip); ip != "" {
			desiredIPs[ip] = true
			crossVPC[ip] = true
		}
	}
	for _, ip := range primaryIPs {
		if ip = strings.TrimSpace(ip); ip != "" {
			desiredIPs[ip] = true
			delete(crossVPC, ip)
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
		if strings.HasSuffix(e.Name, elbSuffix) {
			gwELBID = e.ID
			break
		}
	}
	if gwELBID == "" {
		if softSkipMissing {
			// e.g. the console ELB on a console_isolation_enabled=false prov —
			// there is nothing to reconcile, not a failure.
			progress(fmt.Sprintf("gateway-elb: no ELB named *%s for %s — nothing to reconcile (soft-skip)", elbSuffix, sovereignFQDN))
			return 0, nil
		}
		return 0, fmt.Errorf("gateway-elb: no ELB named *%s found for %s (elbs=%d)", elbSuffix, sovereignFQDN, len(elbs))
	}

	// 2. Resolve the ELB's pools + its VIP subnet (the fallback subnet for
	//    same-VPC member creates when a pool has no existing member to inherit
	//    from — primary-region nodes and the ELB share the region subnet by
	//    construction) + ip_target_enable (#5244 — cross-VPC members need it).
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
			IPTargetEnable  bool   `json:"ip_target_enable"`
			Pools           []struct {
				ID string `json:"id"`
			} `json:"pools"`
		} `json:"loadbalancer"`
	}
	if err := json.Unmarshal(lbResp, &lb); err != nil {
		return 0, fmt.Errorf("gateway-elb: decode LB %s: %w", gwELBID, err)
	}

	// 2b. #5244 — peer-region members are cross-VPC IP-type members; the LB
	// must have ip_target_enable on before their create is accepted. tofu sets
	// cross_vpc_backend at Phase-0 on multi-region provs; this heals drift
	// (pre-#5244 provs, or an HCS build that dropped the create-time flag).
	// On PUT failure: log + skip ADDING cross-VPC members this cycle (their
	// creates would be rejected anyway) — but keep them in desiredIPs so an
	// existing peer member is never swept as stale.
	if len(crossVPC) > 0 && !lb.LoadBalancer.IPTargetEnable {
		putBody := []byte(`{"loadbalancer":{"ip_target_enable":true}}`)
		pResp, pStat, pErr := doSignedRequest(client, hw, http.MethodPut, base+"/loadbalancers/"+gwELBID, putBody)
		if pErr != nil || pStat >= 400 {
			progress(fmt.Sprintf("gateway-elb: enable ip_target_enable on LB %s failed (status %d): %s — peer-region member adds skipped this cycle", gwELBID, pStat, snippet(pResp, 200)))
			for ip := range crossVPC {
				crossVPC[ip] = false // marker: known peer IP, but not addable now
			}
		} else {
			progress(fmt.Sprintf("gateway-elb: LB %s ip_target_enable → true (cross-VPC members permitted)", gwELBID))
		}
	}

	changed := 0
	for _, pl := range lb.LoadBalancer.Pools {
		// Fetch pool detail (name + healthmonitor_id). NOTE: the pool GET's
		// inline `members` are ID-only STUBS on HCS (proven live on hw275 —
		// address/protocol_port come back empty), so the member set is read
		// from the dedicated members-list endpoint below.
		pResp, pStat, perr := doSignedRequest(client, hw, http.MethodGet, base+"/pools/"+pl.ID, nil)
		if perr != nil || pStat >= 400 {
			progress(fmt.Sprintf("gateway-elb: get pool %s failed (status %d): %v", pl.ID, pStat, perr))
			continue
		}
		var pool struct {
			Pool struct {
				Name            string `json:"name"`
				HealthMonitorID string `json:"healthmonitor_id"`
			} `json:"pool"`
		}
		if err := json.Unmarshal(pResp, &pool); err != nil {
			progress(fmt.Sprintf("gateway-elb: decode pool %s failed: %v", pl.ID, err))
			continue
		}

		desired := 0
		switch {
		case strings.HasSuffix(pool.Pool.Name, poolHTTPSSuffix):
			desired = httpsPort
		case strings.HasSuffix(pool.Pool.Name, poolHTTPSuffix):
			desired = httpPort
		default:
			// Not a gateway pool (shouldn't happen — the gateway ELB owns only
			// these two — but skip defensively rather than mis-port a member).
			continue
		}
		if desired <= 0 {
			continue
		}

		// Fetch the pool's full member objects (address + port + subnet).
		mResp, mStat, merr := doSignedRequest(client, hw, http.MethodGet, base+"/pools/"+pl.ID+"/members", nil)
		if merr != nil || mStat >= 400 {
			progress(fmt.Sprintf("gateway-elb: list members of pool %s failed (status %d): %v", pl.ID, mStat, merr))
			continue
		}
		var members struct {
			Members []elbMember `json:"members"`
		}
		if err := json.Unmarshal(mResp, &members); err != nil {
			progress(fmt.Sprintf("gateway-elb: decode members of pool %s failed: %v", pl.ID, err))
			continue
		}

		// 3. Converge the member set: keep members already at (nodeIP, desired);
		//    delete everything else (stale nodes, wrong ports, empty addresses)
		//    — but ONLY when sweepStale (a partial region enumeration must
		//    never drain the unreachable region's members, #5244) — then add
		//    the missing nodes. Track a subnet to inherit for same-VPC creates.
		subnetForCreate := lb.LoadBalancer.VipSubnetCidrID
		present := make(map[string]bool, len(members.Members))
		for _, m := range members.Members {
			if m.SubnetID != "" {
				subnetForCreate = m.SubnetID
			}
			if desiredIPs[m.Address] && m.ProtocolPort == desired {
				present[m.Address] = true
				continue
			}
			if !sweepStale {
				progress(fmt.Sprintf("gateway-elb: pool %s: member %q:%d not in the enumerated node set, but region enumeration was incomplete — keeping (adds-only cycle)", pool.Pool.Name, m.Address, m.ProtocolPort))
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
			isCross, isKnownPeer := crossVPC[ip]
			if isKnownPeer && !isCross {
				// Peer IP whose cross-VPC enable failed above — skip the add.
				continue
			}
			subnet := subnetForCreate
			if isCross {
				// #5244 cross-VPC IP-type member: subnet_cidr_id MUST be
				// omitted — the peer node's IP is outside every subnet of the
				// ELB's VPC; the ELB reaches it over the mesh VPC peering.
				subnet = ""
			}
			body := gatewayMemberCreateBody(ip, desired, subnet)
			cResp, cStat, cErr := doSignedRequest(client, hw, http.MethodPost, base+"/pools/"+pl.ID+"/members", body)
			if cErr != nil || cStat >= 400 {
				progress(fmt.Sprintf("gateway-elb: pool %s: add member %s:%d failed (status %d): %s", pool.Pool.Name, ip, desired, cStat, snippet(cResp, 200)))
				continue
			}
			kind := "member"
			if isCross {
				kind = "cross-VPC member"
			}
			progress(fmt.Sprintf("gateway-elb: pool %s: %s %s:%d added", pool.Pool.Name, kind, ip, desired))
			changed++
		}

		// 4. Point the pool's health monitor at the gateway host port, so the
		//    ELB marks the members healthy against the port it actually forwards
		//    — and drains a dead region's members on region-kill (#5244).
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
// own VIP subnet — primary-region nodes and the ELB share the region subnet by
// construction). When empty it is omitted — the cross-VPC IP-type member shape
// (#5244): peer-region nodes live outside the ELB's VPC, and the members API
// rejects a subnet_cidr_id that does not contain the address.
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
