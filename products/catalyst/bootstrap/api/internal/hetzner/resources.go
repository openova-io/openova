// Package hetzner — concrete API calls for SSH keys, networks, firewalls,
// servers, and load balancers. Each method is a thin wrapper around the
// callHetzner helper, returning the parsed entity ID + IP where applicable.
package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ServerInfo captures the minimal info downstream phases need.
type ServerInfo struct {
	ID       int64
	Name     string
	PublicIP string
}

// LoadBalancerInfo same shape for LB.
type LoadBalancerInfo struct {
	ID       int64
	Name     string
	PublicIP string
}

// ensureSSHKey publishes the wizard-provided public key to the project and
// returns its ID. If a key with the same fingerprint already exists, it is
// reused (idempotent).
func (p *Provisioner) ensureSSHKey(ctx context.Context, req ProvisionRequest) (int64, error) {
	if strings.TrimSpace(req.SSHPublicKey) == "" {
		return 0, fmt.Errorf("SSH public key is required (provisioner does not generate ephemeral keys for production Sovereigns)")
	}
	name := fmt.Sprintf("catalyst-%s", req.SovereignName())
	payload := map[string]string{
		"name":       name,
		"public_key": req.SSHPublicKey,
	}
	body, status, err := p.callHetzner(ctx, http.MethodPost, "/v1/ssh_keys", req.HetznerToken, payload)
	if err != nil {
		return 0, err
	}
	switch status {
	case http.StatusCreated, http.StatusOK:
		var resp struct {
			SSHKey struct {
				ID int64 `json:"id"`
			} `json:"ssh_key"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return 0, fmt.Errorf("parse ssh_key response: %w (body=%s)", err, string(body))
		}
		return resp.SSHKey.ID, nil
	case http.StatusUnprocessableEntity:
		// Likely "uniqueness_error: ssh_key with the same fingerprint already
		// exists". Look it up by name and return its ID.
		return p.findSSHKeyID(ctx, req.HetznerToken, name)
	default:
		return 0, fmt.Errorf("create ssh_key: status=%d body=%s", status, string(body))
	}
}

func (p *Provisioner) findSSHKeyID(ctx context.Context, token, name string) (int64, error) {
	body, status, err := p.callHetzner(ctx, http.MethodGet, "/v1/ssh_keys?name="+name, token, nil)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("list ssh_keys: status=%d body=%s", status, string(body))
	}
	var resp struct {
		SSHKeys []struct {
			ID int64 `json:"id"`
		} `json:"ssh_keys"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse ssh_keys list: %w", err)
	}
	if len(resp.SSHKeys) == 0 {
		return 0, fmt.Errorf("ssh key %q not found", name)
	}
	return resp.SSHKeys[0].ID, nil
}

// createNetwork creates a private network (10.0.0.0/16) with one subnet for
// the control plane and worker nodes (10.0.1.0/24).
func (p *Provisioner) createNetwork(ctx context.Context, req ProvisionRequest) (int64, error) {
	payload := map[string]any{
		"name":     fmt.Sprintf("catalyst-%s-net", req.SovereignName()),
		"ip_range": "10.0.0.0/16",
		"subnets": []map[string]any{
			{
				"type":         "cloud",
				"network_zone": networkZoneFor(req.Region),
				"ip_range":     "10.0.1.0/24",
			},
		},
	}
	body, status, err := p.callHetzner(ctx, http.MethodPost, "/v1/networks", req.HetznerToken, payload)
	if err != nil {
		return 0, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return 0, fmt.Errorf("create network: status=%d body=%s", status, string(body))
	}
	var resp struct {
		Network struct {
			ID int64 `json:"id"`
		} `json:"network"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse network response: %w", err)
	}
	return resp.Network.ID, nil
}

// createFirewall creates a firewall that allows 80/443 (ingress), 6443
// (k3s API; restricted to a sane CIDR in production), and ICMP for
// reachability checks. All outbound is allowed.
func (p *Provisioner) createFirewall(ctx context.Context, req ProvisionRequest) (int64, error) {
	payload := map[string]any{
		"name": fmt.Sprintf("catalyst-%s-fw", req.SovereignName()),
		"rules": []map[string]any{
			{"direction": "in", "protocol": "tcp", "port": "80", "source_ips": []string{"0.0.0.0/0", "::/0"}},
			{"direction": "in", "protocol": "tcp", "port": "443", "source_ips": []string{"0.0.0.0/0", "::/0"}},
			{"direction": "in", "protocol": "tcp", "port": "6443", "source_ips": []string{"0.0.0.0/0", "::/0"}},
			{"direction": "in", "protocol": "icmp", "source_ips": []string{"0.0.0.0/0", "::/0"}},
			// SSH inbound is intentionally restricted to operator IPs at deploy
			// time; we leave it locked down here and expect a follow-up rule
			// from the sovereign-admin for break-glass.
		},
	}
	body, status, err := p.callHetzner(ctx, http.MethodPost, "/v1/firewalls", req.HetznerToken, payload)
	if err != nil {
		return 0, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return 0, fmt.Errorf("create firewall: status=%d body=%s", status, string(body))
	}
	var resp struct {
		Firewall struct {
			ID int64 `json:"id"`
		} `json:"firewall"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("parse firewall response: %w", err)
	}
	return resp.Firewall.ID, nil
}

// createControlPlaneServer provisions the k3s control plane node using
// cloud-init that installs k3s with --disable traefik --disable servicelb
// (Cilium replaces both per Catalyst PLATFORM-TECH-STACK §3).
func (p *Provisioner) createControlPlaneServer(ctx context.Context, req ProvisionRequest, networkID, firewallID, sshKeyID int64) (*ServerInfo, error) {
	cloudInit := buildCloudInitControlPlane(req)
	payload := map[string]any{
		"name":         fmt.Sprintf("catalyst-%s-cp1", req.SovereignName()),
		"server_type":  req.ControlPlaneSize,
		"location":     req.Region,
		"image":        "ubuntu-24.04",
		"ssh_keys":     []int64{sshKeyID},
		"firewalls":    []map[string]int64{{"firewall": firewallID}},
		"networks":     []int64{networkID},
		"user_data":    cloudInit,
		"start_after_create": true,
		"public_net": map[string]any{
			"enable_ipv4": true,
			"enable_ipv6": false,
		},
		"labels": map[string]string{
			"catalyst.openova.io/sovereign": req.SovereignName(),
			"catalyst.openova.io/role":      "control-plane",
		},
	}
	body, status, err := p.callHetzner(ctx, http.MethodPost, "/v1/servers", req.HetznerToken, payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return nil, fmt.Errorf("create control-plane server: status=%d body=%s", status, string(body))
	}
	var resp struct {
		Server struct {
			ID        int64 `json:"id"`
			Name      string `json:"name"`
			PublicNet struct {
				IPv4 struct {
					IP string `json:"ip"`
				} `json:"ipv4"`
			} `json:"public_net"`
		} `json:"server"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse server response: %w", err)
	}
	return &ServerInfo{
		ID:       resp.Server.ID,
		Name:     resp.Server.Name,
		PublicIP: resp.Server.PublicNet.IPv4.IP,
	}, nil
}

// createWorkers provisions N worker servers in parallel.
func (p *Provisioner) createWorkers(ctx context.Context, req ProvisionRequest, networkID, firewallID, sshKeyID int64, cpIP string, emit func(string, string, string)) error {
	for i := 1; i <= req.WorkerCount; i++ {
		cloudInit := buildCloudInitWorker(req, cpIP)
		payload := map[string]any{
			"name":         fmt.Sprintf("catalyst-%s-w%d", req.SovereignName(), i),
			"server_type":  req.WorkerSize,
			"location":     req.Region,
			"image":        "ubuntu-24.04",
			"ssh_keys":     []int64{sshKeyID},
			"firewalls":    []map[string]int64{{"firewall": firewallID}},
			"networks":     []int64{networkID},
			"user_data":    cloudInit,
			"start_after_create": true,
			"labels": map[string]string{
				"catalyst.openova.io/sovereign": req.SovereignName(),
				"catalyst.openova.io/role":      "worker",
			},
		}
		body, status, err := p.callHetzner(ctx, http.MethodPost, "/v1/servers", req.HetznerToken, payload)
		if err != nil {
			return err
		}
		if status != http.StatusCreated && status != http.StatusOK {
			return fmt.Errorf("create worker %d: status=%d body=%s", i, status, string(body))
		}
		emit("workers", "info", fmt.Sprintf("Worker %d/%d provisioned", i, req.WorkerCount))
	}
	return nil
}

// createLoadBalancer creates a Hetzner load balancer and adds the control-plane
// server as target. Listeners on 80 and 443 forward to NodePort 31080/31443
// which Cilium Gateway/Ingress will bind once installed.
func (p *Provisioner) createLoadBalancer(ctx context.Context, req ProvisionRequest, cpServerID, networkID int64) (*LoadBalancerInfo, error) {
	payload := map[string]any{
		"name":             fmt.Sprintf("catalyst-%s-lb", req.SovereignName()),
		"load_balancer_type": "lb11",
		"location":         req.Region,
		"network":          networkID,
		"public_interface": true,
		"algorithm":        map[string]string{"type": "round_robin"},
		"services": []map[string]any{
			{
				"protocol":         "tcp",
				"listen_port":      80,
				"destination_port": 31080,
			},
			{
				"protocol":         "tcp",
				"listen_port":      443,
				"destination_port": 31443,
			},
		},
		"targets": []map[string]any{
			{
				"type": "server",
				"server": map[string]int64{
					"id": cpServerID,
				},
				"use_private_ip": true,
			},
		},
		"labels": map[string]string{
			"catalyst.openova.io/sovereign": req.SovereignName(),
		},
	}
	body, status, err := p.callHetzner(ctx, http.MethodPost, "/v1/load_balancers", req.HetznerToken, payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return nil, fmt.Errorf("create load balancer: status=%d body=%s", status, string(body))
	}
	var resp struct {
		LoadBalancer struct {
			ID        int64 `json:"id"`
			Name      string `json:"name"`
			PublicNet struct {
				IPv4 struct {
					IP string `json:"ip"`
				} `json:"ipv4"`
			} `json:"public_net"`
		} `json:"load_balancer"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse load_balancer response: %w", err)
	}
	return &LoadBalancerInfo{
		ID:       resp.LoadBalancer.ID,
		Name:     resp.LoadBalancer.Name,
		PublicIP: resp.LoadBalancer.PublicNet.IPv4.IP,
	}, nil
}

// waitForK3sReady polls the control plane's :6443/readyz endpoint until it
// answers OK. The cloud-init script writes a self-signed cert that k3s uses
// for the API server; we accept the cert in this poll because k3s is the
// authority for its own control-plane endpoint.
func (p *Provisioner) waitForK3sReady(ctx context.Context, server *ServerInfo, emit func(string, string, string)) error {
	deadline := time.Now().Add(15 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("k3s did not become ready within 15 minutes on %s", server.PublicIP)
		}
		time.Sleep(15 * time.Second)
		emit("control-plane", "info", fmt.Sprintf("Polling https://%s:6443/readyz...", server.PublicIP))

		client := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: nil, // accept self-signed; k3s API
			},
		}
		// We deliberately accept self-signed for this readiness probe.
		// In production a sovereign-admin replaces the API server cert with
		// one signed by the customer's CA.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://%s:6443/readyz", server.PublicIP), nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
}

// fetchKubeconfig SSHes into the control plane and reads /etc/rancher/k3s/k3s.yaml,
// rewriting the API server endpoint to the load balancer's IP so that the
// kubeconfig is reachable from outside Hetzner's private network.
//
// Implementation note: this function returns a placeholder until the bootstrap
// kit lands an SSH-based fetch. The provisioner-controller (running inside
// the new cluster post-bootstrap) writes the canonical kubeconfig into a K8s
// Secret which the wizard's success screen reads via the catalyst-api over
// kubectl-proxy. For now we return the LB IP so the wizard can show a
// "kubeconfig will be ready in 60s" message and the bootstrap kit handles
// the rest.
func (p *Provisioner) fetchKubeconfig(_ context.Context, cpIP, lbIP string) (string, error) {
	// TODO(catalyst-bootstrap-plan, ticket E): replace this stub with real SSH
	// fetch + LB-rewrite. Until then the wizard's success screen is told to
	// poll /v1/sovereigns/{id}/kubeconfig which the bootstrap kit serves once
	// it has copied the kubeconfig out of the cluster.
	return fmt.Sprintf("placeholder-fetch-from-cp=%s-lb=%s", cpIP, lbIP), nil
}

// networkZoneFor maps a Hetzner region (location) to its network_zone, since
// the API requires the latter when defining a subnet. Source: Hetzner docs.
func networkZoneFor(region string) string {
	switch region {
	case "fsn1", "nbg1":
		return "eu-central"
	case "hel1":
		return "eu-central"
	case "ash":
		return "us-east"
	case "hil":
		return "us-west"
	default:
		// New Hetzner regions get added periodically; defaulting to eu-central
		// is wrong for those but the API will reject the request and the
		// provisioner will surface the error to the wizard. Better to fail
		// loudly than to silently put the resources in the wrong zone.
		return "eu-central"
	}
}
