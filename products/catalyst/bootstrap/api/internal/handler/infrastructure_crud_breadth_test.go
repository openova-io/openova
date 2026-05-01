// infrastructure_crud_breadth_test.go — coverage for the extended
// CRUD surface added by issue #349 (full Add/Edit/Delete on every
// Cloud resource type).
//
// These tests exercise the new PATCH endpoints and the new POST
// endpoints (WorkerNode / Network / PVC / Bucket / Volume). The
// existing infrastructure_crud_test.go file covers the original
// surface; this file is purely additive.
package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/infrastructure"
)

/* ── PATCH /infrastructure/regions/{id} ──────────────────────────── */

func TestPatchRegion_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-region")

	body := map[string]any{
		"skuCp":       "cpx41",
		"workerCount": 5,
	}
	rec := callCRUDInfra(t, h, http.MethodPatch, "regions/fsn1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/regions/{id}", h.PatchInfrastructureRegion)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindRegionClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindRegionClaim)
	}
	if !strings.Contains(out.XRCName, "region-fsn1") {
		t.Fatalf("xrcName must contain 'region-fsn1': got %q", out.XRCName)
	}
}

func TestPatchRegion_NoFields(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-region-empty")

	rec := callCRUDInfra(t, h, http.MethodPatch, "regions/fsn1", dep.ID, map[string]any{}, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/regions/{id}", h.PatchInfrastructureRegion)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── PATCH /infrastructure/clusters/{id} ─────────────────────────── */

func TestPatchCluster_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-cluster")

	body := map[string]any{
		"name":    "edge-renamed",
		"version": "v1.31.4+k3s1",
	}
	rec := callCRUDInfra(t, h, http.MethodPatch, "clusters/cluster-x", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/clusters/{id}", h.PatchInfrastructureCluster)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindClusterClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindClusterClaim)
	}
}

/* ── PATCH /infrastructure/vclusters/{id} ────────────────────────── */

func TestPatchVCluster_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-vcluster")

	body := map[string]any{"name": "tenant-a-renamed", "isolationMode": "rtz"}
	rec := callCRUDInfra(t, h, http.MethodPatch, "vclusters/vc-1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/vclusters/{id}", h.PatchInfrastructureVCluster)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── PATCH /infrastructure/loadbalancers/{id} ────────────────────── */

func TestPatchLB_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-lb")

	body := map[string]any{
		"name": "edge-https-renamed",
		"listeners": []map[string]any{
			{"port": 80, "protocol": "tcp"},
			{"port": 443, "protocol": "tcp"},
		},
	}
	rec := callCRUDInfra(t, h, http.MethodPatch, "loadbalancers/lb-1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/loadbalancers/{id}", h.PatchInfrastructureLoadBalancer)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindLoadBalancerClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindLoadBalancerClaim)
	}
}

/* ── POST + PATCH /infrastructure/networks ───────────────────────── */

func TestCreateNetwork_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-create-network")

	body := map[string]any{"regionId": "fsn1", "name": "eu-vpc", "cidr": "10.0.0.0/16"}
	rec := callCRUDInfra(t, h, http.MethodPost, "networks", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Post("/api/v1/deployments/{depId}/infrastructure/networks", h.CreateInfrastructureNetwork)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindNetworkClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindNetworkClaim)
	}
}

func TestPatchNetwork_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-network")

	body := map[string]any{"name": "eu-vpc-renamed"}
	rec := callCRUDInfra(t, h, http.MethodPatch, "networks/net-1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/networks/{id}", h.PatchInfrastructureNetwork)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST + PATCH /infrastructure/{cluster}/nodes — WorkerNode ───── */

func TestCreateWorkerNode_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-create-wn")

	body := map[string]any{"name": "worker-9", "sku": "cpx41", "role": "worker"}
	rec := callCRUDInfra(t, h, http.MethodPost, "clusters/cluster-x/nodes", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/nodes", h.CreateInfrastructureWorkerNode)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindWorkerNodeClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindWorkerNodeClaim)
	}
}

func TestPatchWorkerNode_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-wn")

	body := map[string]any{"sku": "cpx51", "labels": "tier=hot"}
	rec := callCRUDInfra(t, h, http.MethodPatch, "nodes/wn-1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/nodes/{id}", h.PatchInfrastructureWorkerNode)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST + PATCH /infrastructure/pvcs ───────────────────────────── */

func TestCreatePVC_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-create-pvc")

	body := map[string]any{
		"name":         "postgres-data",
		"namespace":    "default",
		"capacity":     "10Gi",
		"storageClass": "standard",
	}
	rec := callCRUDInfra(t, h, http.MethodPost, "pvcs", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Post("/api/v1/deployments/{depId}/infrastructure/pvcs", h.CreateInfrastructurePVC)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != "PVCClaim" {
		t.Fatalf("xrcKind: got %q want PVCClaim", out.XRCKind)
	}
}

func TestPatchPVC_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-pvc")

	body := map[string]any{"capacity": "20Gi"}
	rec := callCRUDInfra(t, h, http.MethodPatch, "pvcs/pvc-1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/pvcs/{id}", h.PatchInfrastructurePVC)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPatchPVC_NoCapacity(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-pvc-empty")

	rec := callCRUDInfra(t, h, http.MethodPatch, "pvcs/pvc-1", dep.ID, map[string]any{}, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/pvcs/{id}", h.PatchInfrastructurePVC)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST + PATCH /infrastructure/buckets ────────────────────────── */

func TestCreateBucket_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-create-bucket")

	body := map[string]any{
		"name":          "backups-prod",
		"capacity":      "1Ti",
		"retentionDays": "30",
	}
	rec := callCRUDInfra(t, h, http.MethodPost, "buckets", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Post("/api/v1/deployments/{depId}/infrastructure/buckets", h.CreateInfrastructureBucket)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindBucketClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindBucketClaim)
	}
}

func TestPatchBucket_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-bucket")

	body := map[string]any{"capacity": "2Ti"}
	rec := callCRUDInfra(t, h, http.MethodPatch, "buckets/bucket-1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/buckets/{id}", h.PatchInfrastructureBucket)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── POST + PATCH /infrastructure/volumes ────────────────────────── */

func TestCreateVolume_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-create-volume")

	body := map[string]any{
		"regionId": "fsn1",
		"name":     "postgres-eu",
		"capacity": "50Gi",
	}
	rec := callCRUDInfra(t, h, http.MethodPost, "volumes", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Post("/api/v1/deployments/{depId}/infrastructure/volumes", h.CreateInfrastructureVolume)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	out := mustDecodeMutation(t, rec)
	if out.XRCKind != infrastructure.KindVolumeClaim {
		t.Fatalf("xrcKind: got %q want %q", out.XRCKind, infrastructure.KindVolumeClaim)
	}
}

func TestPatchVolume_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeXRCDynamicFactory()
	dep := installCRUDDeployment(t, h, "dep-patch-volume")

	body := map[string]any{"capacity": "100Gi", "attachedTo": "node-3"}
	rec := callCRUDInfra(t, h, http.MethodPatch, "volumes/vol-1", dep.ID, body, func(r chi.Router, h *Handler) {
		r.Patch("/api/v1/deployments/{depId}/infrastructure/volumes/{id}", h.PatchInfrastructureVolume)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── DELETE /infrastructure/{newKinds}/{id} ──────────────────────── */

func TestDeleteResource_NewKinds(t *testing.T) {
	cases := []struct {
		kindURL string
		xrcKind string
	}{
		{"networks", infrastructure.KindNetworkClaim},
		{"buckets", infrastructure.KindBucketClaim},
		{"volumes", infrastructure.KindVolumeClaim},
		{"pvcs", "PVCClaim"},
	}

	for _, tc := range cases {
		t.Run(tc.kindURL, func(t *testing.T) {
			h := NewWithPDM(silentLogger(), &fakePDM{})
			h.dynamicFactory = fakeXRCDynamicFactory()
			dep := installCRUDDeployment(t, h, "dep-delete-"+tc.kindURL)

			rec := callCRUDInfra(t, h, http.MethodDelete, tc.kindURL+"/foo-1", dep.ID, nil, func(r chi.Router, h *Handler) {
				r.Delete("/api/v1/deployments/{depId}/infrastructure/{kind}/{id}", h.DeleteInfrastructureResource)
			})
			// Both 202 (accepted) and 404 (already-absent — claim
			// didn't exist on the fake client) are valid happy paths.
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
			}
			out := mustDecodeMutation(t, rec)
			if out.XRCKind != tc.xrcKind {
				t.Fatalf("xrcKind: got %q want %q", out.XRCKind, tc.xrcKind)
			}
		})
	}
}
