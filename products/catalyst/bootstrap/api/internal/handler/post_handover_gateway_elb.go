// post_handover_gateway_elb.go — #4706: after Phase-1 converges, converge the
// Huawei gateway ELB's pool member SET to the live cluster's nodes at the
// durable gateway host ports (:443/:80).
//
// THE SHAPE. On the CCM-less Huawei provider the Sovereign Gateway is served
// by cilium-envoy in gateway-api hostNetwork mode (cilium 1.19.3, bp-cilium
// 1.4.8): envoy — a hostNetwork DaemonSet — binds node:443/:80 directly on
// every node, and the dedicated public gateway ELB (infra/providers/huawei/
// main.tf huaweicloud_elb_*.primary) does TCP passthrough public :443/:80 →
// node:443/:80. tofu seeds correct members at Phase-0 (the ports are durable
// and known at apply time — no nodePort anywhere, §854). What this hook heals
// is member-set DRIFT: autoscaler-added nodes missing from the pools and
// replaced/removed nodes leaving stale members — the job hcloud-ccm does
// continuously on Hetzner, done explicitly here because Huawei has no CCM.
//
// History: on cilium 1.16.5 (hostNetwork bind bug) this hook discovered the
// gateway Service's auto-allocated nodePort and repointed member PORTS
// (#4690/#4691). That shape is retired by the 1.19.3 bump.
//
// Runs as a background goroutine from the OutcomeReady terminal block, next to
// runPostHandoverAdoptionApply / runPostHandoverSpineApplications. Best-effort:
// failures log + emit an SSE warn but never fail the handover. Idempotent — a
// re-run against already-converged members is a no-op.
package handler

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

const (
	// gatewayHostPortHTTPS / gatewayHostPortHTTP — the durable host ports
	// cilium-envoy binds on every node in gateway-api hostNetwork mode.
	// Must match the Gateway listener ports (clusters/_template/sovereign-tls/
	// cilium-gateway.yaml) and the tofu member ports
	// (var.gateway_member_port_{https,http}).
	gatewayHostPortHTTPS = 443
	gatewayHostPortHTTP  = 80

	// consoleHostPortHTTPS / consoleHostPortHTTP — the console gateway's host
	// ports under the huawei hostNetwork gateway. Two Gateways cannot both bind
	// node:443, so the console gateway uses 8443/8080 (unprivileged host ports,
	// NOT nodePorts — #4715); the console ELB forwards public :443/:80 →
	// node:8443/:8080. Must match consoleGateway.{https,http}Port
	// (clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml #4718) and
	// the tofu console member ports (var.console_member_port_{https,http}).
	consoleHostPortHTTPS = 8443
	consoleHostPortHTTP  = 8080

	// gatewayELBReconcileMaxWait — budget waiting for the primary cluster to
	// report ≥1 node InternalIP. Nodes exist long before OutcomeReady, so this
	// is defensive only. Fully off the bootstrap critical path.
	gatewayELBReconcileMaxWait = 15 * time.Minute
	gatewayELBReconcilePoll    = 30 * time.Second
)

// runPostHandoverGatewayELB converges the Huawei gateway ELB member set to the
// live node IPs at the gateway host ports. See file header. Huawei-only.
func (h *Handler) runPostHandoverGatewayELB(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("gateway-elb: panic recovered", "id", dep.ID, "panic", r)
		}
	}()

	// Hetzner (and any non-Huawei) path: hcloud-ccm programs the gateway LB
	// against the live nodes automatically — nothing to reconcile here.
	if strings.ToLower(strings.TrimSpace(dep.Request.Provider)) != "huawei" {
		return
	}

	hp := h.huaweiProvider("gateway-elb")
	if hp == nil {
		return // huaweiProvider already logged the reason
	}

	kcPath, ok := h.resolvePrimaryKubeconfigPath(dep)
	if !ok {
		h.log.Warn("gateway-elb: no kubeconfig for deployment; skipping ELB reconcile", "id", dep.ID)
		return
	}
	kcRaw, err := os.ReadFile(kcPath)
	if err != nil {
		h.log.Warn("gateway-elb: read kubeconfig failed; skipping", "id", dep.ID, "path", kcPath, "err", err)
		return
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kcRaw)
	if err != nil {
		h.log.Warn("gateway-elb: parse kubeconfig failed", "id", dep.ID, "err", err)
		return
	}
	restCfg.Timeout = 20 * time.Second
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		h.log.Warn("gateway-elb: build clientset failed", "id", dep.ID, "err", err)
		return
	}

	region := strings.TrimSpace(dep.Request.HuaweiRegion)
	if region == "" && len(dep.Request.Regions) > 0 {
		region = strings.TrimSpace(dep.Request.Regions[0].CloudRegion)
	}
	if region == "" {
		region = "me-east-215"
	}
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)

	// Poll for the primary cluster's node InternalIPs (the primary kubeconfig
	// scopes this to primary-region nodes — each region is its own cluster),
	// then converge the ELB member set to them at the gateway host ports.
	deadline := time.Now().Add(gatewayELBReconcileMaxWait)
	for {
		nodeIPs, perr := h.discoverNodeInternalIPs(cs)
		if perr == nil && len(nodeIPs) > 0 {
			changed, rerr := hp.ReconcileGatewayELBMembers(
				context.Background(),
				dep.Request.HuaweiAccessKey, dep.Request.HuaweiSecretKey, dep.Request.HuaweiProjectID,
				region, fqdn, nodeIPs, gatewayHostPortHTTPS, gatewayHostPortHTTP,
				func(msg string) { h.log.Info("gateway-elb: " + msg) },
			)
			if rerr != nil {
				h.log.Warn("gateway-elb: ELB member reconcile failed", "id", dep.ID, "err", rerr)
				dep.recordEvent(provisioner.Event{
					Time:    time.Now().UTC().Format(time.RFC3339),
					Phase:   "post-handover",
					Level:   "warn",
					Message: "Gateway ELB member reconcile failed: " + rerr.Error() + ". The wildcard *." + fqdn + " front door may not serve until this succeeds (retry on next convergence).",
				})
				return
			}
			h.log.Info("gateway-elb: reconcile complete",
				"id", dep.ID, "nodes", len(nodeIPs), "membersChanged", changed)
			dep.recordEvent(provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   "post-handover",
				Level:   "info",
				Message: "Gateway ELB member set converged to " + strconv.Itoa(len(nodeIPs)) + " live nodes at node:" + strconv.Itoa(gatewayHostPortHTTPS) + "/:" + strconv.Itoa(gatewayHostPortHTTP) + " (" + strconv.Itoa(changed) + " changed); public :443/:80 front door is wired.",
			})

			// Also converge the dedicated CONSOLE ELB (console_isolation provs)
			// to the same live nodes at the console gateway host ports (8443/8080).
			// Best-effort + soft-skip: a console_isolation=false prov has no
			// console ELB → clean no-op. Without this the console ELB kept its
			// tofu-seeded boot members forever and lost its backends after an
			// autoscaler node roll → Console 000 (#4706). Its failure must NOT
			// mask the primary success above, so it is logged, not returned.
			if cChanged, cErr := hp.ReconcileConsoleELBMembers(
				context.Background(),
				dep.Request.HuaweiAccessKey, dep.Request.HuaweiSecretKey, dep.Request.HuaweiProjectID,
				region, fqdn, nodeIPs, consoleHostPortHTTPS, consoleHostPortHTTP,
				func(msg string) { h.log.Info("console-elb: " + msg) },
			); cErr != nil {
				h.log.Warn("console-elb: member reconcile failed (non-fatal; primary front door is up)", "id", dep.ID, "err", cErr)
			} else {
				h.log.Info("console-elb: reconcile complete", "id", dep.ID, "membersChanged", cChanged)
			}
			return
		}
		if time.Now().After(deadline) {
			h.log.Warn("gateway-elb: budget exhausted waiting for node InternalIPs; ELB members left as tofu seeded them",
				"id", dep.ID)
			dep.recordEvent(provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   "post-handover",
				Level:   "warn",
				Message: "Could not list the primary cluster's node IPs within budget; the gateway ELB members stay as tofu seeded them (correct for the boot-time node set; autoscaler drift unhealed this cycle).",
			})
			return
		}
		time.Sleep(gatewayELBReconcilePoll)
	}
}

// discoverNodeInternalIPs lists the cluster's nodes and returns their
// InternalIP addresses — the gateway ELB pool member addresses. Every node
// runs cilium-envoy (hostNetwork DaemonSet) with the gateway listeners bound
// on node:443/:80, so every node is a valid member.
func (h *Handler) discoverNodeInternalIPs(cs kubernetes.Interface) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, n := range nodes.Items {
		for _, addr := range n.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP && strings.TrimSpace(addr.Address) != "" {
				ips = append(ips, addr.Address)
			}
		}
	}
	return ips, nil
}
