// gateway_elb.go — #4690 / #4686 foundation-fix: reconcile the Sovereign
// gateway ELB's pool members to the gateway LoadBalancer Service's live,
// auto-allocated nodePort.
//
// WHY THIS EXISTS. On the CCM-less Huawei provider the Sovereign Gateway is a
// `Service type=LoadBalancer` (cilium-gateway-cilium-gateway, kube-system,
// #4682) fronted publicly by a dedicated Huawei ELB (infra/providers/huawei/
// main.tf huaweicloud_elb_*.primary). The ELB forwards public :443/:80 →
// node:<gateway-Service-nodePort> — that is the only path that routes on a
// no-CCM Huawei node (node:443 does NOT, verified live on hw208). The nodePort
// is AUTO-ALLOCATED by Cilium in the 30000-32767 range and is unknown at
// tofu-apply time (tofu runs at Phase-0, before the cluster — and thus the
// Service — exists), and Cilium 1.16.5 exposes no durable pin for it. So the
// tofu ELB members start at a PLACEHOLDER port (var.gateway_service_nodeport_*)
// and catalyst-api rewrites them to the live nodePort here, post-convergence.
//
// This is exactly what hcloud-ccm does on Hetzner (it reads the Service and
// programs the LB); Huawei has no CCM, so catalyst-api does it explicitly. The
// gateway Service TYPE stays LoadBalancer (never type=NodePort — §854).
//
// Idempotent by construction: a member already on the desired port is left
// untouched; only mismatched members are DELETE+POST-recreated. Safe to re-run.
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

// ReconcileGatewayELBMembers repoints the gateway ELB's HTTPS/HTTP pool members
// (and their health monitors) at the live gateway-Service nodePorts.
//
// It is an exported method on *Provider (mirrors SweepOrphanEIPs) so the
// handler reaches it via h.huaweiProvider("gateway-elb"). httpsNodePort /
// httpNodePort are the LoadBalancer Service's real per-port nodePorts, which
// the caller discovers from the live cluster.
//
// Returns the number of members changed (0 == already converged / nothing to
// do) and an error only on a hard API failure. Per-member failures are logged
// via progress and do NOT abort the whole reconcile (best-effort, like the
// day-2 post-handover hooks).
func (p *Provider) ReconcileGatewayELBMembers(ctx context.Context, ak, sk, projectID, region, sovereignFQDN string, httpsNodePort, httpNodePort int, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if httpsNodePort <= 0 && httpNodePort <= 0 {
		return 0, fmt.Errorf("gateway-elb: no valid nodePort supplied (https=%d http=%d)", httpsNodePort, httpNodePort)
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

	// 2. Resolve the ELB's pools; classify each as https/http by name suffix.
	lbResp, status, err := doSignedRequest(client, hw, http.MethodGet, base+"/loadbalancers/"+gwELBID, nil)
	if err != nil {
		return 0, fmt.Errorf("gateway-elb: get LB %s: %w", gwELBID, err)
	}
	if status >= 400 {
		return 0, fmt.Errorf("gateway-elb: get LB %s: status %d: %s", gwELBID, status, snippet(lbResp, 240))
	}
	var lb struct {
		LoadBalancer struct {
			Pools []struct {
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
			desired = httpsNodePort
		case strings.HasSuffix(pool.Pool.Name, gatewayPoolHTTPSuffix):
			desired = httpNodePort
		default:
			// Not a gateway pool (shouldn't happen — the gateway ELB owns only
			// these two — but skip defensively rather than mis-port a member).
			continue
		}
		if desired <= 0 {
			continue
		}

		// 3. DELETE+POST each member whose port != desired. A member already on
		//    the desired port is left untouched (idempotent re-run).
		for _, m := range pool.Pool.Members {
			if m.ProtocolPort == desired {
				continue
			}
			// Delete the stale-port member.
			_, dStat, dErr := doSignedRequest(client, hw, http.MethodDelete, base+"/pools/"+pl.ID+"/members/"+m.ID, nil)
			if dErr != nil || (dStat >= 400 && dStat != http.StatusNotFound) {
				progress(fmt.Sprintf("gateway-elb: pool %s: delete member %s (%s:%d) failed (status %d): %v", pool.Pool.Name, m.ID, m.Address, m.ProtocolPort, dStat, dErr))
				continue
			}
			// Recreate at the live nodePort (same address + subnet).
			body := gatewayMemberCreateBody(m.Address, desired, m.SubnetID)
			cResp, cStat, cErr := doSignedRequest(client, hw, http.MethodPost, base+"/pools/"+pl.ID+"/members", body)
			if cErr != nil || cStat >= 400 {
				progress(fmt.Sprintf("gateway-elb: pool %s: recreate member %s:%d failed (status %d): %s", pool.Pool.Name, m.Address, desired, cStat, snippet(cResp, 200)))
				continue
			}
			progress(fmt.Sprintf("gateway-elb: pool %s: member %s repointed %d→%d", pool.Pool.Name, m.Address, m.ProtocolPort, desired))
			changed++
		}

		// 4. Repoint the pool's health monitor at the live nodePort too, so the
		//    ELB marks the members healthy against the port it actually forwards.
		if pool.Pool.HealthMonitorID != "" {
			hmBody := []byte(fmt.Sprintf(`{"healthmonitor":{"monitor_port":%d}}`, desired))
			hResp, hStat, hErr := doSignedRequest(client, hw, http.MethodPut, base+"/healthmonitors/"+pool.Pool.HealthMonitorID, hmBody)
			if hErr != nil || hStat >= 400 {
				progress(fmt.Sprintf("gateway-elb: pool %s: monitor repoint→%d failed (status %d): %s", pool.Pool.Name, desired, hStat, snippet(hResp, 200)))
			} else {
				progress(fmt.Sprintf("gateway-elb: pool %s: monitor repointed →%d", pool.Pool.Name, desired))
			}
		}
	}

	return changed, nil
}

// gatewayMemberCreateBody builds the ELB v3 member-create payload. subnet_cidr_id
// is the member's IPv4 subnet (echoed back from the existing member so the new
// member lands on the same subnet tofu placed it in). When empty it is omitted
// (cross-VPC member — not our case, but keep the encoder honest).
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
