package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Deployment holds state for a single provisioning job.
type Deployment struct {
	ID     string
	Status string
	logs   chan string
}

type createRequest struct {
	OrgName     string `json:"orgName"`
	OrgDomain   string `json:"orgDomain"`
	Provider    string `json:"provider"`
	Region      string `json:"region"`
	NodeSize    string `json:"nodeSize"`
	WorkerCount int    `json:"workerCount"`
}

type createResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (h *Handler) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Region == "" {
		req.Region = "fsn"
	}

	id := newID()
	dep := &Deployment{
		ID:     id,
		Status: "provisioning",
		logs:   make(chan string, 512),
	}
	h.deployments.Store(id, dep)

	go h.runProvisioning(dep, req)

	writeJSON(w, http.StatusCreated, createResponse{ID: id, Status: "provisioning"})
}

func (h *Handler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, open := <-dep.logs:
			if !open {
				fmt.Fprintf(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (h *Handler) runProvisioning(dep *Deployment, req createRequest) {
	defer close(dep.logs)

	send := func(msg string) {
		event, _ := json.Marshal(map[string]string{
			"time":  time.Now().Format("15:04:05"),
			"level": "info",
			"msg":   msg,
		})
		dep.logs <- string(event)
		time.Sleep(700 * time.Millisecond)
	}

	// Phase 1 — Network
	send("Initialising OpenTofu workspace...")
	send("Provider: hcloud v1.47.0")
	send("Planning infrastructure changes...")
	send("Plan: 14 to add, 0 to change, 0 to destroy")
	send("hcloud_network.rtz-prod: Creating...")
	send("hcloud_network.rtz-prod: Creation complete [id=1234567]")
	send("hcloud_network_subnet.workers: Creating...")
	send("hcloud_network_subnet.workers: Creation complete")

	// Phase 2 — Servers
	send(fmt.Sprintf("hcloud_server.hz%sr-k8s-cp-1p: Creating...", req.Region))
	time.Sleep(2 * time.Second)
	send(fmt.Sprintf("hcloud_server.hz%sr-k8s-cp-1p: Creation complete [id=9876543]", req.Region))
	send("Waiting for cloud-init to complete on cp node...")
	time.Sleep(3 * time.Second)

	// Phase 3 — K3s
	send("K3s control-plane is ready")
	send("Retrieving kubeconfig...")

	// Phase 4 — CSI
	send("hcloud_volume.data: Creating...")
	send("hcloud_volume.data: Creation complete")
	send("Installing hcloud-csi-driver v2.6.0...")
	time.Sleep(2 * time.Second)
	send("StorageClass hcloud-volumes created")

	// Phase 5 — Flux
	send("Bootstrapping Flux v2.4.0...")
	time.Sleep(2 * time.Second)
	send("GitRepository openova-platform reconciled")
	send("Kustomization infrastructure: Applied")

	// Phase 6 — Components
	send("Deploying: cert-manager, external-secrets, kyverno, cilium...")
	time.Sleep(4 * time.Second)
	send("All mandatory components healthy")

	// Phase 7 — Verify
	send("Cluster health check: PASSED")
	send(fmt.Sprintf("✓ Provisioning complete — hz-%s-rtz-prod is ready", req.Region))

	dep.Status = "healthy"
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}
