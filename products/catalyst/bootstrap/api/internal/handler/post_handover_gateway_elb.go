// post_handover_gateway_elb.go — #4690 / #4686 foundation-fix: after Phase-1
// converges, discover the Sovereign gateway LoadBalancer Service's live,
// auto-allocated nodePort and reconcile the Huawei gateway ELB's pool members
// to it.
//
// THE BREAK THIS CLOSES. On the CCM-less Huawei provider the Sovereign Gateway
// is a `Service type=LoadBalancer` (cilium-gateway-cilium-gateway, kube-system,
// #4682) fronted publicly by a dedicated Huawei ELB (infra/providers/huawei/
// main.tf huaweicloud_elb_*.primary). That ELB forwards public :443/:80 →
// node:<gateway-Service-nodePort> — the only path that routes on a no-CCM
// Huawei node (node:443 does NOT, verified hw208). The nodePort is
// auto-allocated by Cilium and unknown at tofu-apply time, so the tofu ELB
// members start at a placeholder port (var.gateway_service_nodeport_*) and this
// hook repoints them at the live nodePort post-convergence.
//
// This is exactly what hcloud-ccm does on Hetzner (reads the Service, programs
// the LB); Huawei has no CCM so catalyst-api does it explicitly. Hetzner
// deployments skip this hook entirely (the tofu LB already targets node:443).
//
// Runs as a background goroutine from the OutcomeReady terminal block, next to
// runPostHandoverAdoptionApply / runPostHandoverSpineApplications. Best-effort:
// failures log + emit an SSE warn but never fail the handover. Idempotent — a
// re-run against already-reconciled members is a no-op.
package handler

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

const (
	// gatewayServiceNamespace / gatewayServiceName — the Service Cilium's
	// gateway-controller auto-creates for the `cilium-gateway` Gateway
	// (kube-system). Name = cilium-gateway-<gateway-name> (see
	// clusters/_template/sovereign-tls/cilium-gateway.yaml).
	gatewayServiceNamespace = "kube-system"
	gatewayServiceName      = "cilium-gateway-cilium-gateway"

	// gatewayELBReconcileMaxWait — budget waiting for the gateway Service to
	// exist + get its nodePort allocated. The Service lands only after
	// bp-cilium reconciles the Gateway, which may still be settling at first
	// OutcomeReady. Fully off the bootstrap critical path.
	gatewayELBReconcileMaxWait = 15 * time.Minute
	gatewayELBReconcilePoll    = 30 * time.Second
)

// runPostHandoverGatewayELB reconciles the Huawei gateway ELB members to the
// live gateway-Service nodePort. See file header. Huawei-only.
func (h *Handler) runPostHandoverGatewayELB(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("gateway-elb: panic recovered", "id", dep.ID, "panic", r)
		}
	}()

	// Hetzner (and any non-Huawei) path: hcloud-ccm programs the gateway LB
	// against node:443 automatically — nothing to reconcile here.
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

	// Poll for the gateway Service's per-port nodePorts, then reconcile the ELB.
	// Require BOTH the :443 and :80 nodePorts before reconciling — a
	// type=LoadBalancer Service is assigned nodePorts for all its ports
	// atomically, so a partial read means the Service is still being programmed;
	// reconciling on a partial read would strand the un-read pool's members on
	// the placeholder port.
	deadline := time.Now().Add(gatewayELBReconcileMaxWait)
	for {
		httpsNP, httpNP, perr := h.discoverGatewayNodePorts(cs)
		if perr == nil && httpsNP > 0 && httpNP > 0 {
			changed, rerr := hp.ReconcileGatewayELBMembers(
				context.Background(),
				dep.Request.HuaweiAccessKey, dep.Request.HuaweiSecretKey, dep.Request.HuaweiProjectID,
				region, fqdn, httpsNP, httpNP,
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
				"id", dep.ID, "httpsNodePort", httpsNP, "httpNodePort", httpNP, "membersChanged", changed)
			dep.recordEvent(provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   "post-handover",
				Level:   "info",
				Message: "Gateway ELB reconciled to the live gateway-Service nodePort (https→node:" + strconv.Itoa(httpsNP) + ", http→node:" + strconv.Itoa(httpNP) + "); public :443/:80 front door is wired.",
			})
			return
		}
		if time.Now().After(deadline) {
			h.log.Warn("gateway-elb: budget exhausted waiting for the gateway Service nodePort; ELB members left at the placeholder port",
				"id", dep.ID, "service", gatewayServiceNamespace+"/"+gatewayServiceName)
			dep.recordEvent(provisioner.Event{
				Time:    time.Now().UTC().Format(time.RFC3339),
				Phase:   "post-handover",
				Level:   "warn",
				Message: "Gateway Service " + gatewayServiceNamespace + "/" + gatewayServiceName + " never exposed a nodePort within budget; the gateway ELB members stay at the tofu placeholder port and the *." + fqdn + " front door will not serve until reconciled.",
			})
			return
		}
		time.Sleep(gatewayELBReconcilePoll)
	}
}

// discoverGatewayNodePorts reads the gateway LoadBalancer Service and returns
// its per-port (443/80) auto-allocated nodePorts. Returns (0,0,nil) when the
// Service exists but has not been assigned nodePorts yet (caller retries).
func (h *Handler) discoverGatewayNodePorts(cs kubernetes.Interface) (httpsNodePort, httpNodePort int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	svc, gerr := cs.CoreV1().Services(gatewayServiceNamespace).Get(ctx, gatewayServiceName, metav1.GetOptions{})
	if gerr != nil {
		return 0, 0, gerr
	}
	for _, p := range svc.Spec.Ports {
		if p.NodePort <= 0 {
			continue
		}
		switch p.Port {
		case 443:
			httpsNodePort = int(p.NodePort)
		case 80:
			httpNodePort = int(p.NodePort)
		}
	}
	return httpsNodePort, httpNodePort, nil
}
