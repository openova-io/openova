// Package handler holds shared state for all HTTP handlers.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
)

// Handler holds shared state for all HTTP handlers.
//
// dynadotAPIKey + dynadotAPISecret remain on the Handler so the OpenTofu
// module's `dynadot_*` variables can still receive credentials for the
// Phase-0 DNS bootstrap that runs at first `tofu apply` time. After #163
// Phase 4 lands the Crossplane Composition that wraps PDM as a declarative
// MR, even those fields go away (PDM holds the credentials; catalyst-api
// merely calls PDM via the in-cluster service FQDN).
//
// pdm is the central authority for OpenOva-pool subdomain allocation
// (introduced by #163). catalyst-api never calls api.dynadot.com directly
// for the availability check / reservation lifecycle after this lands —
// every interaction with the Dynadot zone flows through PDM.
type Handler struct {
	log              *slog.Logger
	deployments      sync.Map // map[string]*Deployment
	dynadotAPIKey    string
	dynadotAPISecret string

	// pdm — pool-domain-manager client. Required in production; tests can
	// inject a fake via NewWithPDM. The default URL points at the in-cluster
	// service FQDN so a stock Catalyst-Zero deployment "just works" without
	// per-pod configuration.
	pdm pdmClient
}

// New creates a Handler with the runtime configuration loaded from env.
//
// POOL_DOMAIN_MANAGER_URL — defaults to the in-cluster service FQDN. Per
// docs/INVIOLABLE-PRINCIPLES.md #4 the URL is configuration, not code; an
// air-gapped install can override it to point at the operator's own
// PDM endpoint.
func New(log *slog.Logger) *Handler {
	pdmURL := os.Getenv("POOL_DOMAIN_MANAGER_URL")
	if pdmURL == "" {
		pdmURL = "http://pool-domain-manager.openova-system.svc.cluster.local:8080"
	}
	return &Handler{
		log:              log,
		dynadotAPIKey:    os.Getenv("DYNADOT_API_KEY"),
		dynadotAPISecret: os.Getenv("DYNADOT_API_SECRET"),
		pdm:              pdm.New(pdmURL),
	}
}

// NewWithPDM is exposed for tests; production code uses New.
func NewWithPDM(log *slog.Logger, client pdmClient) *Handler {
	return &Handler{
		log:              log,
		dynadotAPIKey:    os.Getenv("DYNADOT_API_KEY"),
		dynadotAPISecret: os.Getenv("DYNADOT_API_SECRET"),
		pdm:              client,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
