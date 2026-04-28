package handlers

import (
	"log"
	"time"

	"github.com/openova-io/openova-private/website/marketplace-api/store"
)

// runProvisioning simulates the async provisioning workflow.
// In production, this commits manifests to Git and polls Flux reconciliation status.
func (h *Handler) runProvisioning(p *store.Provision) {
	sizeLabels := map[string]string{"xs": "XS", "s": "S", "m": "M", "l": "L"}

	for i := range p.Steps {
		now := time.Now()
		p.Steps[i].Status = store.StatusProvisioning
		p.Steps[i].StartedAt = &now
		p.UpdatedAt = now
		h.Store.UpdateProvision(p)

		// Simulate work — in production this would be:
		// 1. Git commit (vcluster.yaml, resource-quota.yaml, helmrelease.yaml)
		// 2. Wait for Flux reconciliation webhook
		// 3. Verify pod readiness
		duration := 3*time.Second + time.Duration(i)*time.Second
		time.Sleep(duration)

		done := time.Now()
		p.Steps[i].Status = store.StatusCompleted
		p.Steps[i].DoneAt = &done
		p.Steps[i].Message = "Done"
		p.UpdatedAt = done
		h.Store.UpdateProvision(p)

		log.Printf("Provision %s: step %d/%d completed (%s)", p.ID, i+1, len(p.Steps), p.Steps[i].Name)
	}

	p.Status = store.StatusCompleted
	p.UpdatedAt = time.Now()
	h.Store.UpdateProvision(p)

	// Create tenant record
	tenant := &store.Tenant{
		ID:             p.TenantID,
		CompanyName:    p.CompanyName,
		Email:          p.Email,
		Subdomain:      p.Subdomain,
		VClusterName:   "vc-" + p.Subdomain,
		VClusterStatus: "running",
		Size:           p.Size,
		SizeLabel:      sizeLabels[p.Size],
		Apps:           make([]store.App, 0, len(p.Apps)),
		Domains: []store.Domain{
			{
				Domain:    p.Subdomain + ".openova.cloud",
				Type:      "subdomain",
				TLSReady:  true,
				CreatedAt: time.Now().Format(time.RFC3339),
			},
		},
		CreatedAt: time.Now(),
	}

	for _, appSlug := range p.Apps {
		tenant.Apps = append(tenant.Apps, store.App{
			Slug:       appSlug,
			Name:       appSlug, // In production, resolve from catalog
			Status:     "running",
			URL:        "https://" + appSlug + "." + p.Subdomain + ".openova.cloud",
			Version:    "latest",
			DeployedAt: time.Now().Format(time.RFC3339),
			Healthy:    true,
		})
	}

	h.Store.CreateTenant(tenant)

	log.Printf("Provision %s completed: tenant %s created with %d apps", p.ID, p.TenantID, len(p.Apps))
}
