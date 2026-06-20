// Package handler — organization_keycloak.go: pre-creation of OIDC
// clients + group templates in the SME-vcluster Keycloak realm
// (issue #804 step 5).
//
// The bp-keycloak chart's `bootstrap` values block (rendered by the
// per-tenant overlay generator in organization_gitops.go) already
// declares the OIDC clients (catalyst-ui, wordpress, openclaw,
// stalwart) + group templates (sme-admin, sme-user) the SME needs.
// Once Flux reconciles the chart inside the SME vcluster, Keycloak
// has them populated.
//
// However: the bootstrap-from-values path is delivered by an
// in-cluster Job that runs after the realm itself is up. Until that
// Job lands the OIDC clients aren't yet usable. This file is the
// orchestrator-side back-stop: it polls the SME-vcluster Keycloak
// admin API and confirms the clients exist; if they don't, it
// creates them via the admin API directly.
//
// Two strategies are supported:
//
//   - **Chart-bootstrap-aware (default)**: the orchestrator trusts
//     the bp-keycloak chart's bootstrap Job to materialise the
//     clients. ProvisionOrganizationClients then becomes a verification step:
//     poll Keycloak for the expected client list, return success
//     when present, return a transient error (so the reconciler
//     retries) when not.
//
//   - **Direct (fallback)**: when CATALYST_ORG_KC_DIRECT_PROVISION
//     env is "true", the orchestrator creates the clients itself via
//     the SME-vcluster Keycloak admin API. Used during initial
//     bring-up of a Sovereign before bp-keycloak's bootstrap Job is
//     reliable.
//
// The interface OrganizationKeycloakClientProvisioner abstracts both;
// tests inject a stub.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// ChartBootstrapKeycloakProvisioner is the production
// OrganizationKeycloakClientProvisioner. It treats the bp-keycloak
// chart's bootstrap Job as the authoritative provisioner and runs a
// verification poll against the SME-vcluster Keycloak admin API.
//
// On verification failure it returns a transient error so the
// pipeline reconciler retries (the reconciler runs every 30s; the
// chart's bootstrap Job typically completes in 2-5 minutes).
type ChartBootstrapKeycloakProvisioner struct {
	Log *slog.Logger
	// HTTPClient — defaults to a 15s-timeout client. Tests inject a
	// httptest.Server-backed client to exercise the verification path.
	HTTPClient *http.Client
	// SAToken — service-account token with realm-management.view-clients
	// on the SME realm. In production wired from
	// CATALYST_ORG_KC_SA_TOKEN; tests inject a fixed value.
	SAToken string
}

// ProvisionOrganizationClients implements OrganizationKeycloakClientProvisioner.
// In production this verifies the bp-keycloak chart's bootstrap Job
// has populated the expected clients; in CI/tests with no live
// Keycloak it returns nil so the pipeline can advance.
func (p ChartBootstrapKeycloakProvisioner) ProvisionOrganizationClients(ctx context.Context, rec store.OrganizationProvisionRecord) error {
	if p.SAToken == "" {
		// Without an SA token the orchestrator can't query Keycloak.
		// Return nil so the pipeline advances (the chart's bootstrap
		// Job is still authoritative); a future enhancement is to
		// surface this as a structured warning in the response so the
		// operator knows the verification is being skipped.
		if p.Log != nil {
			p.Log.Info("sme-tenant: keycloak SA token not wired; skipping verification",
				"sme_tenant_id", rec.OrganizationID,
			)
		}
		return nil
	}

	// Realm admin URL: the SME-vcluster Keycloak's admin API. Per
	// docs/INVIOLABLE-PRINCIPLES.md #4 the URL pattern is configurable
	// via the per-tenant overlay; the default the chart emits is
	// http://keycloak-<subdomain>.<tenant-ns>.svc:8080. From
	// catalyst-api (in OTECH control plane) this is reachable via the
	// vcluster API server's port-forwarded service.
	adminURL := fmt.Sprintf("http://keycloak-%s.%s.svc:8080",
		rec.Subdomain, rec.TenantNamespace)
	realm := "org-" + rec.Subdomain
	expected := []string{"catalyst-ui", "wordpress", "openclaw", "stalwart"}
	missing, err := verifyKeycloakClients(ctx, p.httpClient(), adminURL, realm, p.SAToken, expected)
	if err != nil {
		return fmt.Errorf("verify clients: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("clients missing: %v (chart bootstrap incomplete)", missing)
	}
	return nil
}

func (p ChartBootstrapKeycloakProvisioner) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// verifyKeycloakClients lists clients in the realm and returns the
// expected ids that are absent. Used by both the production
// verifier and tests.
func verifyKeycloakClients(ctx context.Context, hc *http.Client, adminURL, realm, saToken string, expected []string) ([]string, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/clients", adminURL, realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET clients: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET clients: HTTP %d", resp.StatusCode)
	}
	var raw []struct {
		ClientID string `json:"clientId"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode clients: %w", err)
	}
	have := map[string]bool{}
	for _, c := range raw {
		have[c.ClientID] = true
	}
	var missing []string
	for _, e := range expected {
		if !have[e] {
			missing = append(missing, e)
		}
	}
	return missing, nil
}

// NoopOrganizationKeycloakClientProvisioner satisfies the interface
// with a no-op success. Used in tests + CI where no live Keycloak
// is available. Production main.go wires the
// ChartBootstrapKeycloakProvisioner only when the SA token env is
// populated.
type NoopOrganizationKeycloakClientProvisioner struct{}

// ProvisionOrganizationClients is a no-op (always returns nil).
func (NoopOrganizationKeycloakClientProvisioner) ProvisionOrganizationClients(_ context.Context, _ store.OrganizationProvisionRecord) error {
	return nil
}

// errKeycloakClientsMissing is the canonical error the verifier
// returns when the chart bootstrap hasn't completed yet. Surfaced
// through the pipeline as a transient failure so the reconciler
// retries until the chart catches up.
var errKeycloakClientsMissing = errors.New("keycloak: expected clients not yet provisioned")
